package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProductEvents(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_events", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "signup") {
		t.Errorf("missing event: %s", out)
	}
	if !strings.Contains(out, "total_events") {
		t.Errorf("missing daily totals from v_product_totals: %s", out)
	}
}

// docs is seeded with no attributes declared; blog declares "plan" (see
// seed_test.go) for TestProductAttributesReturnsRows. Rollups are
// unconditional now (no enabled flag to opt into), so an undeclared-key
// project returns an empty result rather than an error.
func TestProductAttributesReturnsEmptyForUndeclaredProject(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_attributes", map[string]any{
		"project_id": 2, "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); strings.Contains(out, "\"plan\"") {
		t.Errorf("docs has no agg_product_attrs rows and must not see blog's: %s", out)
	}
}

func TestProductAttributesReturnsRows(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_attributes", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "plan") || !strings.Contains(out, "pro") {
		t.Errorf("missing attribute row: %s", out)
	}
	// unique_groups flows through as the last column. A NULL cell (the
	// "pro" row, rolled up before 016) decodes to the empty string; a
	// measured cell (the "team" row, seeded with 2) decodes to its
	// integer. Decode the table rather than substring-matching so a NULL
	// rendered as "0", "<nil>" or "null" would fail this test.
	var table struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &table); err != nil {
		t.Fatalf("decode table: %v\n%s", err, out)
	}
	groupsIdx := -1
	for i, c := range table.Columns {
		if c == "unique_groups" {
			groupsIdx = i
			break
		}
	}
	if groupsIdx == -1 {
		t.Fatalf("missing unique_groups column: %s", out)
	}
	attrValueIdx := -1
	for i, c := range table.Columns {
		if c == "attr_value" {
			attrValueIdx = i
			break
		}
	}
	if attrValueIdx == -1 {
		t.Fatalf("missing attr_value column: %s", out)
	}
	var sawPro, sawTeam bool
	for _, row := range table.Rows {
		switch row[attrValueIdx] {
		case "pro":
			sawPro = true
			if got := row[groupsIdx]; got != "" {
				t.Errorf("pro row (pre-016, unmeasured) unique_groups = %q, want empty", got)
			}
		case "team":
			sawTeam = true
			if got := row[groupsIdx]; got != "2" {
				t.Errorf("team row unique_groups = %q, want %q", got, "2")
			}
		}
	}
	if !sawPro || !sawTeam {
		t.Fatalf("missing pro or team row: %s", out)
	}
	// event filter branch
	res = callTool(t, cs, "product_attributes", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31", "event": "signup"})
	if res.IsError {
		t.Fatalf("error with event filter: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "pro") {
		t.Errorf("event-filtered attributes missing row: %s", out)
	}
}

func TestRetentionReturnsCurveAndAggregatedThrough(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project_id": 1, "actor": "user", "from": "2026-07-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"2026-08-01", "cohort_size", "aggregated_through"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
}

func TestRetentionOnAnonymousProjectExplains(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project_id": 2, "actor": "user", "from": "2026-07-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("anonymous project retention did not error")
	}
	if out := textOf(res); !strings.Contains(out, "identified") {
		t.Errorf("error must explain the identity requirement: %s", out)
	}
}

func TestRetentionRejectsUnknownActor(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project_id": 1, "actor": "app", "from": "2026-07-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("unknown actor did not error")
	}
	if out := textOf(res); !strings.Contains(out, "user or install") {
		t.Errorf("error must explain the valid actors: %s", out)
	}
}

func TestIdentities(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "identities", map[string]any{
		"project_id": 1, "kind": "user", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "u1") || !strings.Contains(out, "Jane Doe") {
		t.Errorf("identities missing id or name: %s", out)
	}
	if !strings.Contains(out, "views") || !strings.Contains(out, "events") {
		t.Errorf("identities missing views/events columns: %s", out)
	}
	if strings.Contains(out, "hits") {
		t.Errorf("identities must not carry the retired hits column: %s", out)
	}
}
