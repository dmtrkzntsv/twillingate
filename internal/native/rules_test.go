package native

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// nativeRows reads project 1's Parquet files and logs over the whole
// fixture range, as the engine will: the columns rs.columns(cols) asks for,
// of family (any, when ""), each batch through rs.apply, then cols
// resolved by name (Batch.col). It returns the rows formatted as cell does.
func nativeRows(t *testing.T, f *archivetest.Files, rs ruleSet, cols []string, family string) [][]string {
	t.Helper()
	full := rng(t, "2026-08-01", today)
	pq, logs, err := projectFiles(f.Dir, 1, full)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := parquetGroups(pq, family, full)
	if err != nil {
		t.Fatal(err)
	}
	read := rs.columns(cols)
	batches := readAll(t, groups, 2, read, family, full)
	for _, path := range logs {
		b, err := readLog(path, read, family, full)
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}
	var got [][]string
	for _, b := range batches {
		got = formatBatch(t, fold(rs.apply(b, family), cols), cols, got)
	}
	return got
}

// matchRules compares nativeRows with DuckDB's rows for q (SELECT … FROM
// events WHERE project_id = 1 …) under the same rules, its columns as
// DuckDB names them.
func matchRules(t *testing.T, f *archivetest.Files, rules archive.Rules, q, family string) []string {
	t.Helper()
	cols, want := duckRulesRows(t, f, q, rules)
	got := nativeRows(t, f, compileRules(1, rules), cols, family)
	sameRows(t, got, want, cols)
	return cols
}

func duckRulesRows(t *testing.T, f *archivetest.Files, q string, rules archive.Rules) ([]string, [][]string) {
	t.Helper()
	res, err := f.Reader.Query(context.Background(), q, 0, rules)
	if err != nil {
		t.Fatal(err)
	}
	cols := slices.Clone(res.Columns)
	if i := slices.Index(cols, "id"); i > 0 {
		cols[0], cols[i] = cols[i], cols[0]
	}
	var rows [][]string
	for _, row := range res.Rows {
		r := make([]string, len(cols))
		for j, name := range cols {
			r[j] = cell(t, row[slices.Index(res.Columns, name)])
		}
		rows = append(rows, r)
	}
	sortByFirst(rows)
	return cols, rows
}

func evRule(project int64, fam store.Family, name, action, to string) store.EventRule {
	return store.EventRule{ProjectID: project, Family: fam, Name: name, Action: action, To: to}
}

func attrRule(project int64, key, action, to string) store.AttributeRule {
	return store.AttributeRule{ProjectID: project, Key: key, Action: action, To: to}
}

// rulesFixture is seed's files plus today's log, which holds events with
// attributes of their own (size, shade, flag, junk) beside plan and color.
func rulesFixture(t *testing.T) *archivetest.Files {
	t.Helper()
	f := seed(t, 12)
	f.Write(t, events(t, 1, today, 12)...)
	ts := date(t, today).Time().Add(9 * time.Hour)
	extra := []map[string]string{
		{"size": "L"}, {"plan": "own", "size": "M"}, {"shade": "blue"}, {"color": "green", "shade": "grey"},
		{"flag": "on", "junk": "j"}, {"tone": "kept", "shade": "lost"}, {},
	}
	var evs []store.Event
	for i, attrs := range extra {
		for _, fam := range []store.Family{store.FamilyProduct, store.FamilyMeasures} {
			evs = append(evs, store.Event{ID: fmt.Sprintf("x-%s-%d", fam, i), ProjectID: 1, Family: fam,
				EventName: fmt.Sprintf("ev%d", i%4), TS: ts.Add(time.Duration(i) * time.Second), Attributes: attrs})
		}
	}
	f.Write(t, evs...)
	return f
}

func TestEventRenameAndBlockMatchDuckDB(t *testing.T) {
	f := rulesFixture(t)
	// Lines without an event name or a family: a block whose condition
	// they leave unknown drops them, as SQL's NOT (NULL) does.
	path := logOf(t, f, 1)
	w, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{
		`{"v":1,"id":"nn-product","project_id":1,"family":"product","day":"2026-09-10"}`,
		`{"v":1,"id":"nn-views","project_id":1,"family":"views","day":"2026-09-10"}`,
		`{"v":1,"id":"nf-ev3","project_id":1,"event_name":"ev3","day":"2026-09-10"}`,
		`{"v":1,"id":"nf-ev0","project_id":1,"event_name":"ev0","day":"2026-09-10"}`,
	} {
		if _, err := w.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rules := archive.Rules{Events: []store.EventRule{
		evRule(1, store.FamilyProduct, "ev0", store.EventRuleRename, "zero"),
		evRule(1, store.FamilyProduct, "ev1", store.EventRuleRename, "ev2"), // a merge
		evRule(1, store.FamilyProduct, "ev3", store.EventRuleBlock, ""),
		evRule(1, store.FamilyMeasures, "ev1", store.EventRuleBlock, ""),
		evRule(1, store.FamilyMeasures, "ev2", store.EventRuleRename, "ev1"), // onto a blocked name
		evRule(1, store.FamilyMeasures, "ev0", store.EventRuleRename, "ev2"), // onto a renamed one: no chain
		evRule(2, store.FamilyProduct, "ev2", store.EventRuleBlock, ""),      // another project's
	}}
	cols := matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1", "")
	if !slices.Contains(cols, "event_name") {
		t.Fatalf("cols %v", cols)
	}
	matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1 AND family = 'measures'", "measures")
	matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1 AND family = 'views'", "views")

	// Asked for id alone, the scan also reads what the rules need.
	rs := compileRules(1, rules)
	read := rs.columns([]string{"id"})
	for _, c := range []string{"id", "event_name", "family"} {
		if !slices.Contains(read, c) {
			t.Errorf("columns %v lacks %s", read, c)
		}
	}
	_, want := duckRulesRows(t, f, "SELECT id FROM events WHERE project_id = 1", rules)
	sameRows(t, nativeRows(t, f, rs, []string{"id"}, ""), want, []string{"id"})

	// A merge's target keeps its index: the dictionary holds each name
	// once, and the input's columns are left as they were.
	in := &Batch{N: 3, Cols: map[string]*Column{"event_name": {Dict: []string{"ev1", "ev2", "ev0"}, Idx: []int32{0, 1, 2}}}}
	got := rs.apply(in, "product")
	if c := got.Cols["event_name"]; !slices.Equal(c.Dict, []string{"ev1", "ev2", "ev0", "zero"}) || !slices.Equal(c.Idx, []int32{1, 1, 3}) {
		t.Errorf("merged %q %v", c.Dict, c.Idx)
	}
	if c := in.Cols["event_name"]; !slices.Equal(c.Dict, []string{"ev1", "ev2", "ev0"}) || !slices.Equal(c.Idx, []int32{0, 1, 2}) {
		t.Errorf("input changed: %q %v", c.Dict, c.Idx)
	}

	// No rules: the batch as it is.
	b := &Batch{N: 1, Cols: map[string]*Column{"event_name": {Dict: []string{"a"}, Idx: []int32{0}}}}
	if got := compileRules(1, archive.Rules{}).apply(b, ""); got != b {
		t.Error("no rules: a new batch")
	}
	if got := compileRules(3, rules); !got.empty() {
		t.Error("project 3: rules")
	}
}

func TestAttributeRenameAndBlockMatchDuckDB(t *testing.T) {
	f := rulesFixture(t)
	rules := archive.Rules{Attributes: []store.AttributeRule{
		attrRule(1, "size", store.EventRuleRename, "plan"),   // a merge: plan's own value first
		attrRule(1, "shade", store.EventRuleRename, "tone"),  // tone exists in the log only
		attrRule(1, "color", store.EventRuleRename, "tone"),  // a second source, before shade
		attrRule(1, "junk", store.EventRuleBlock, ""),        // blocked
		attrRule(1, "flag", store.EventRuleRename, "junk"),   // onto a blocked key: lost
		attrRule(1, "nothing", store.EventRuleRename, "new"), // no file has it: no column
		attrRule(1, "Tone", store.EventRuleRename, "tone"),   // the same column: as it is
		attrRule(2, "plan", store.EventRuleBlock, ""),        // another project's
	}}
	cols := matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1", "")
	for _, c := range []string{"attr_tone", "attr_plan", "attr_size", "attr_junk", "attr_flag"} {
		if !slices.Contains(cols, c) {
			t.Fatalf("events lacks %s: %v", c, cols)
		}
	}
	matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1 AND family = 'product'", "product")

	// Asked for the targets alone, the scan reads their sources too.
	rs := compileRules(1, rules)
	want := []string{"id", "attr_tone", "attr_plan"}
	read := rs.columns(want)
	for _, c := range []string{"family", "attr_color", "attr_shade", "attr_size"} {
		if !slices.Contains(read, c) {
			t.Errorf("columns %v lacks %s", read, c)
		}
	}
	if slices.Contains(read, "attr_flag") {
		t.Errorf("columns %v: a source of a blocked target", read)
	}
	matchRules(t, f, rules, "SELECT id, attr_tone, attr_plan FROM events WHERE project_id = 1", "")
	_, rows := duckRulesRows(t, f, "SELECT id, attr_tone, attr_plan FROM events WHERE project_id = 1 AND family = 'measures'", rules)
	sameRows(t, nativeRows(t, f, rs, want, "measures"), rows, want)
}

// A rename chain: b is both a target and renamed itself, so it reads
// NULL, and c takes b's own value.
func TestAttributeRenameChainMatchesDuckDB(t *testing.T) {
	f := rulesFixture(t)
	rules := archive.Rules{Attributes: []store.AttributeRule{
		attrRule(1, "size", store.EventRuleRename, "shade"),
		attrRule(1, "shade", store.EventRuleRename, "tone"),
	}}
	matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1", "")
}

func TestAttributeRenameAcrossFiles(t *testing.T) {
	f := rulesFixture(t)
	// attr_color is in September's files only: the month file's batches
	// gain the target column.
	rules := archive.Rules{Attributes: []store.AttributeRule{
		attrRule(1, "plan", store.EventRuleRename, "color"),
	}}
	matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1", "")
	matchRules(t, f, rules, "SELECT id, attr_color FROM events WHERE project_id = 1", "")
	rs := compileRules(1, rules)
	pq, _, err := projectFiles(f.Dir, 1, rng(t, "2026-08-01", "2026-08-31"))
	if err != nil {
		t.Fatal(err)
	}
	groups, err := parquetGroups(pq, "product", rng(t, "2026-08-01", "2026-08-31"))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range readAll(t, groups, 1, []string{"id", "family", AttrCols}, "product", rng(t, "2026-08-01", "2026-08-31")) {
		if _, c := b.col("attr_color"); c != nil {
			t.Fatalf("the month file holds attr_color: %v", keys(b.Cols))
		}
		if _, c := rs.apply(b, "product").col("attr_color"); c == nil || b.N > 0 && c.Idx[0] < 0 {
			t.Fatalf("attr_color not added: %+v", c)
		}
	}
}

// DuckDB's names ignore case: the month file's attr_plan and the log's
// attr_Plan are one column, which a rule on plan covers in both.
func TestAttributeRuleMatchesKeysAcrossCase(t *testing.T) {
	f := seed(t, 12)
	ts := date(t, today).Time().Add(9 * time.Hour)
	var evs []store.Event
	for i := range 6 {
		attrs := map[string]string{"Plan": fmt.Sprintf("P%d", i%3)}
		if i%2 == 0 {
			attrs["Tier"] = "gold"
		}
		evs = append(evs, store.Event{ID: fmt.Sprintf("c-%d", i), ProjectID: 1, Family: store.FamilyProduct,
			EventName: "ev0", TS: ts.Add(time.Duration(i) * time.Second), Attributes: attrs})
	}
	f.Write(t, evs...)
	for _, rules := range []archive.Rules{
		{Attributes: []store.AttributeRule{attrRule(1, "plan", store.EventRuleRename, "tier")}},
		{Attributes: []store.AttributeRule{attrRule(1, "plan", store.EventRuleBlock, "")}},
		{Attributes: []store.AttributeRule{attrRule(1, "tier", store.EventRuleRename, "PLAN")}},
	} {
		cols := matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1", "")
		if !slices.ContainsFunc(cols, func(c string) bool { return c == "attr_plan" || c == "attr_Plan" }) {
			t.Fatalf("cols %v", cols)
		}
		matchRules(t, f, rules, "SELECT id, attr_plan, attr_tier FROM events WHERE project_id = 1", "")
	}
	// The log alone holds attr_Plan and, with odd, attr_plan_1: plan is
	// attr_Plan, as in DuckDB.
	f.Write(t, odd("odd-1"))
	rules := archive.Rules{Attributes: []store.AttributeRule{attrRule(1, "plan", store.EventRuleRename, "tier")}}
	matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1", "")
}

func TestRulesIgnoreViewsForAttributes(t *testing.T) {
	f := rulesFixture(t)
	ts := date(t, today).Time().Add(10 * time.Hour)
	var evs []store.Event
	for i := range 4 {
		evs = append(evs, store.Event{ID: fmt.Sprintf("v-%d", i), ProjectID: 1, Family: store.FamilyViews,
			EventName: "ev0", TS: ts.Add(time.Duration(i) * time.Second),
			Attributes: map[string]string{"plan": "viewplan", "junk": "vj"}})
	}
	f.Write(t, evs...)
	rules := archive.Rules{
		Events: []store.EventRule{evRule(1, store.FamilyProduct, "ev0", store.EventRuleBlock, "")},
		Attributes: []store.AttributeRule{
			attrRule(1, "plan", store.EventRuleRename, "tier"),
			attrRule(1, "junk", store.EventRuleBlock, ""),
		},
	}
	_, rows := duckRulesRows(t, f, "SELECT id, attr_plan FROM events WHERE project_id = 1 AND id LIKE 'v-%'", rules)
	if len(rows) != 4 || rows[0][1] != "viewplan" {
		t.Fatalf("DuckDB views rows %q", rows)
	}
	matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1", "")
	matchRules(t, f, rules, "SELECT * FROM events WHERE project_id = 1 AND family = 'views'", "views")
}

func TestBatchColResolvesCase(t *testing.T) {
	a, b := &Column{}, &Column{}
	bt := &Batch{Cols: map[string]*Column{"attr_Plan": a, "attr_plan_1": b}}
	for name, want := range map[string]*Column{"attr_Plan": a, "attr_plan": a, "ATTR_PLAN": a, "attr_plan_1": b, "attr_x": nil} {
		if _, got := bt.col(name); got != want {
			t.Errorf("%s: %p, want %p", name, got, want)
		}
	}
	bt.Cols["attr_plan"] = b
	if key, got := bt.col("attr_plan"); got != b || key != "attr_plan" {
		t.Error("exact match not first")
	}
	if key, _ := bt.col("ATTR_PLAN"); key != "attr_Plan" {
		t.Errorf("fold picks %s, want the first in order", key)
	}
	if got := expandCols([]string{"attr_Plan", "id"}, []string{"attr_plan", "ID", "nope"}); !slices.Equal(got, []string{"attr_Plan", "id", "nope"}) {
		t.Errorf("expandCols %v", got)
	}
}
