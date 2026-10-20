package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// measuresRow decodes one row of a measures tableOut by column name, so
// assertions survive column reordering.
func measuresRow(t *testing.T, out string, col, want string) map[string]string {
	t.Helper()
	var tbl struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &tbl); err != nil {
		t.Fatalf("decode table: %v\n%s", err, out)
	}
	idx := map[string]int{}
	for i, c := range tbl.Columns {
		idx[c] = i
	}
	colIdx, ok := idx[col]
	if !ok {
		t.Fatalf("missing column %q: %s", col, out)
	}
	for _, row := range tbl.Rows {
		if row[colIdx] == want {
			m := map[string]string{}
			for name, i := range idx {
				m[name] = row[i]
			}
			return m
		}
	}
	t.Fatalf("no row with %s=%q: %s", col, want, out)
	return nil
}

// TestMeasuresReturnsOneRowPerMetric checks the seeded histograms (see
// seed_test.go: agg_measures_daily/agg_measures_attrs rows on 2026-08-20
// plus raw measures written through WriteEvents on 2026-08-21) combine
// into one row per (event_name, measure) with samples and est_count
// matching the seeded totals, and that an all-zero metric's p75 is 0.
func TestMeasuresReturnsOneRowPerMetric(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "measures", map[string]any{
		"project_id": 1, "from": "2026-08-20", "to": "2026-08-21"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)

	checkout := measuresRow(t, out, "event_name", "checkout_api")
	if checkout["measure"] != "time" {
		t.Errorf("checkout_api measure = %q, want time", checkout["measure"])
	}
	if checkout["samples"] != "3" {
		t.Errorf("checkout_api samples = %q, want 3 (2 aggregated + 1 raw)", checkout["samples"])
	}
	if checkout["est_count"] != "3" {
		t.Errorf("checkout_api est_count = %q, want 3", checkout["est_count"])
	}

	cls := measuresRow(t, out, "event_name", "$cls")
	if cls["measure"] != "number" {
		t.Errorf("$cls measure = %q, want number", cls["measure"])
	}
	if cls["samples"] != "4" {
		t.Errorf("$cls samples = %q, want 4 (3 aggregated + 1 raw)", cls["samples"])
	}
	if cls["p75"] != "0" {
		t.Errorf("$cls p75 = %q, want 0 (every sample is the zero bucket)", cls["p75"])
	}
}

// TestMeasuresFiltersByName checks the name filter narrows to one metric.
func TestMeasuresFiltersByName(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "measures", map[string]any{
		"project_id": 1, "from": "2026-08-20", "to": "2026-08-21", "name": "checkout_api"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	var tbl struct {
		Rows [][]string `json:"rows"`
	}
	out := textOf(res)
	if err := json.Unmarshal([]byte(out), &tbl); err != nil {
		t.Fatalf("decode table: %v\n%s", err, out)
	}
	if len(tbl.Rows) != 1 {
		t.Fatalf("name filter returned %d rows, want 1: %s", len(tbl.Rows), out)
	}
	if !strings.Contains(out, "checkout_api") {
		t.Errorf("filtered row is not checkout_api: %s", out)
	}
}

// TestMeasuresBreaksDownByAttrKey checks attr_key adds an attr_value
// column and returns one row per browser, combining the aggregated
// $browser rows (seed_test.go) with the live half read off the raw
// events' browser column.
func TestMeasuresBreaksDownByAttrKey(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "measures", map[string]any{
		"project_id": 1, "from": "2026-08-20", "to": "2026-08-21",
		"name": "checkout_api", "attr_key": "$browser"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	chrome := measuresRow(t, out, "attr_value", "chrome")
	if chrome["samples"] != "2" {
		t.Errorf("chrome samples = %q, want 2 (1 aggregated + 1 raw)", chrome["samples"])
	}
	firefox := measuresRow(t, out, "attr_value", "firefox")
	if firefox["samples"] != "1" {
		t.Errorf("firefox samples = %q, want 1", firefox["samples"])
	}
}

func TestMeasuresRejectsUnknownProject(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "measures", map[string]any{
		"project_id": 99, "from": "2026-08-20", "to": "2026-08-21"})
	if !res.IsError {
		t.Fatal("unknown project accepted")
	}
	if msg := textOf(res); !strings.Contains(msg, "unknown project 99; valid projects: 1 (blog), 2 (docs)") {
		t.Errorf("error = %q, want the unknown-project message", msg)
	}
}

func TestMeasuresRejectsBadDate(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "measures", map[string]any{
		"project_id": 1, "from": "yesterday", "to": "2026-08-21"})
	if !res.IsError {
		t.Fatal("bad date accepted")
	}
	if msg := textOf(res); !strings.Contains(msg, "YYYY-MM-DD") {
		t.Errorf("error = %q, want a YYYY-MM-DD complaint", msg)
	}
}

// TestMeasuresBreaksDownByAnyKey: attr_key reads any key's attr_<key>
// column, declared or not, and a key no measure carried (or no event at
// all) gives no rows, not an error.
func TestMeasuresBreaksDownByAnyKey(t *testing.T) {
	_, cs := newTestHost(t)
	args := func(key string) map[string]any {
		return map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-21", "attr_key": key}
	}
	res := callTool(t, cs, "measures", args("tier"))
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	gold := measuresRow(t, textOf(res), "attr_value", "gold")
	if gold["event_name"] != "checkout_api" || gold["samples"] != "1" || gold["mean"] != "200" {
		t.Errorf("tier gold = %v, want checkout_api's one 200 ms sample", gold)
	}
	// A reserved key reads its column, declared or not.
	res = callTool(t, cs, "measures", args("$path"))
	if res.IsError {
		t.Fatalf("$path: %s", textOf(res))
	}
	if page := measuresRow(t, textOf(res), "attr_value", "/checkout"); page["samples"] != "1" || page["mean"] != "300" {
		t.Errorf("$path /checkout = %v, want checkout_api's one 300 ms sample", page)
	}
	for _, key := range []string{"TIER", "plan", "never_sent", `we"ird`, "$nope"} {
		res := callTool(t, cs, "measures", args(key))
		if res.IsError {
			t.Errorf("%s: %s", key, textOf(res))
			continue
		}
		var tbl struct {
			Rows [][]string `json:"rows"`
		}
		if err := json.Unmarshal([]byte(textOf(res)), &tbl); err != nil {
			t.Fatal(err)
		}
		if want := key == "TIER"; (len(tbl.Rows) == 1) != want {
			t.Errorf("%s: rows %v; a key reads its column whatever its case, and a key no measure carried reads none", key, tbl.Rows)
		}
	}
}
