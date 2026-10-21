package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/store"
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
		t.Errorf("missing daily totals: %s", out)
	}
}

// docs's events carry no plan; blog's do (see seed_files_test.go), so docs
// returns an empty result rather than an error.
func TestProductAttributesReturnsEmptyForUndeclaredProject(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_attributes", map[string]any{
		"project_id": 2, "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); strings.Contains(out, "\"plan\"") {
		t.Errorf("docs has no plan values and must not see blog's: %s", out)
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
	// unique_groups flows through as the last column: 0 for the "pro"
	// events, which no group sent (the files count groups on every day;
	// SQLite's NULL for days rolled up before migration 016 has no
	// counterpart), 2 for "team". Decode the table rather than
	// substring-matching.
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
			if got := row[groupsIdx]; got != "0" {
				t.Errorf("pro row unique_groups = %q, want 0", got)
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
	// The system keys come from the typed columns, declared or not.
	var sawPlatform bool
	for _, row := range table.Rows {
		if row[2] == "$platform" && row[attrValueIdx] == "web" {
			sawPlatform = true
		}
	}
	if !sawPlatform {
		t.Errorf("missing the $platform row: %s", out)
	}
	// The reserved keys a declaration once had to unlock break down too,
	// undeclared: u2's signup came on /pricing from the newsletter.
	for _, want := range []string{
		`["2026-08-21","signup","$path","/pricing","1","1","1"]`,
		`["2026-08-21","signup","$utm_source","newsletter","1","1","1"]`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s: %s", want, out)
		}
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
	// The files' cohort: 10 users on 2026-08-01, 4 back a week later;
	// the files reach through 2026-08-26, the newest day with activity.
	var got retentionOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.AggregatedThrough != "2026-08-26" {
		t.Errorf("aggregated_through = %q, want 2026-08-26", got.AggregatedThrough)
	}
	for _, want := range [][]string{{"2026-08-01", "0", "10", "10"}, {"2026-08-01", "7", "4", "10"}} {
		if !slices.ContainsFunc(got.Rows, func(r []string) bool { return slices.Equal(r, want) }) {
			t.Errorf("missing row %v: %v", want, got.Rows)
		}
	}
}

// docs (project 2) has no cohorts because its clients send no ids: the
// answer is an empty table, not a refusal.
func TestRetentionWithoutIdsIsEmpty(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project_id": 2, "actor": "user", "from": "2026-07-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("retention on a project without ids must not error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, `"rows":[]`) && !strings.Contains(out, `"rows":null`) {
		t.Errorf("want an empty rows array, got %s", out)
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
	// Busiest first: u1's view and two signups; then the ids with two
	// actions by id, r01 first. Actors and users are distinct per day and
	// added up, as v_identity_daily summed: u1 is active on three days
	// (as actor u1 on 08-20 and 08-21, a1 on 08-26), so 3 and 3, not the
	// 2 actors and 1 user of the whole range.
	var tbl tableOut
	if err := json.Unmarshal([]byte(out), &tbl); err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) < 2 || !slices.Equal(tbl.Rows[0], []string{"u1", "Jane Doe", "3", "3", "1", "2"}) || tbl.Rows[1][0] != "r01" {
		t.Errorf("rows = %v, want u1 (Jane Doe, 3 actors, 3 users, 1 view, 2 events) then r01", tbl.Rows)
	}
	// u9 viewed on two days: 2 actors and 2 users summed, 1 of each over
	// the range.
	res = callTool(t, cs, "identities", map[string]any{
		"project_id": 1, "kind": "user", "from": "2026-08-23", "to": "2026-08-24"})
	if out := textOf(res); !strings.Contains(out, `"rows":[["u9","","2","2","2","0"]]`) {
		t.Errorf("u9 over 08-23..24 = %s, want 2 actors, 2 users, 2 views", out)
	}
	res = callTool(t, cs, "identities", map[string]any{
		"project_id": 1, "kind": "user", "from": "2026-08-01", "to": "2026-08-31", "limit": 1})
	if err := json.Unmarshal([]byte(textOf(res)), &tbl); err != nil || len(tbl.Rows) != 1 {
		t.Errorf("limit 1 = %v (%v)", tbl.Rows, err)
	}
	if !strings.Contains(out, "views") || !strings.Contains(out, "events") {
		t.Errorf("identities missing views/events columns: %s", out)
	}
	if strings.Contains(out, "hits") {
		t.Errorf("identities must not carry the retired hits column: %s", out)
	}
}

// TestProductAttributesWithoutAnyAttribute: on files where no event ever
// carried an attribute (no attr_* column at all), the system keys still
// break down and measures' attr_key finds no column, without an error.
func TestProductAttributesWithoutAnyAttribute(t *testing.T) {
	h, cs := newTestHost(t)
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw")
	log, err := archive.OpenLog(raw)
	if err != nil {
		t.Fatal(err)
	}
	value := 5.0
	if err := log.WriteEvents(context.Background(), []store.Event{
		{ID: "0190ffff-0000-7000-8000-000000000001", ProjectID: 1, Family: store.FamilyProduct, EventName: "open",
			TS: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC), ActorID: "x", ActorKind: store.ActorInstall, Platform: "ios", SampleRate: 1},
		{ID: "0190ffff-0000-7000-8000-000000000002", ProjectID: 1, Family: store.FamilyMeasures, EventName: "load",
			TS: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC), ActorID: "x", ActorKind: store.ActorInstall, Platform: "ios",
			Value: &value, Measure: store.MeasureTime, SampleRate: 1},
	}); err != nil {
		t.Fatal(err)
	}
	log.Close()
	eng, err := hostEngine(dir, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	if h.files, err = archive.NewReader(raw, eng); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-20"}
	res := callTool(t, cs, "product_attributes", args)
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, `"rows":[["2026-08-20","open","$platform","ios","1","1","0"]]`) {
		t.Errorf("want the $platform row alone: %s", out)
	}
	args["attr_key"] = "plan"
	if res := callTool(t, cs, "measures", args); res.IsError || !strings.Contains(textOf(res), `"rows":[]`) {
		t.Errorf("measures by a key no file has = %s", textOf(res))
	}
}

// TestProductAttributesRefusesMoreThanAQuarter: its per-day rows over a
// year run out of the engine's memory, so a longer range is refused with
// a way out, and a quarter still answers.
func TestProductAttributesRefusesMoreThanAQuarter(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_attributes", map[string]any{
		"project_id": 1, "from": "2026-05-27", "to": "2026-08-27"})
	if !res.IsError {
		t.Fatalf("93 days answered: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "at most 92 days") || !strings.Contains(out, "query") {
		t.Errorf("refusal = %s, want the limit and the way out", out)
	}
	res = callTool(t, cs, "product_attributes", map[string]any{
		"project_id": 1, "from": "2026-05-28", "to": "2026-08-27"})
	if res.IsError {
		t.Fatalf("92 days refused: %s", textOf(res))
	}
}
