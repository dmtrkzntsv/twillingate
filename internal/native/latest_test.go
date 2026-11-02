package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestLatest: latest reads a column's value at the group's latest row by
// ts, the greater value on a tie: the path with the greatest max(ts) per
// actor, from a breakdown by actor and path.
func TestLatest(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	p := Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}
	ask := func(q Query) Result {
		t.Helper()
		res, err := e.Answer(context.Background(), q, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	pairs := ask(Query{Kind: "breakdown", Table: "views", Dimensions: []string{"actor_id", "path"}, Limit: 1000,
		Where: []Filter{{Column: "path", Op: "is_set"}}, Metrics: []Metric{{Op: "max", Column: "ts"}}})
	want := map[string][2]string{} // actor → (ts, path)
	for _, r := range pairs.Rows {
		if w, ok := want[r[0]]; !ok || r[2] > w[0] || (r[2] == w[0] && r[1] > w[1]) {
			want[r[0]] = [2]string{r[2], r[1]}
		}
	}
	got := ask(Query{Kind: "breakdown", Table: "views", Dimensions: []string{"actor_id"}, Limit: 1000,
		Where: []Filter{{Column: "path", Op: "is_set"}}, Metrics: []Metric{{Op: "latest", Column: "path", As: "last_path"}}})
	if len(got.Rows) != len(want) || len(want) == 0 || got.Columns[1] != "last_path" {
		t.Fatalf("%d actors, want %d (%v)", len(got.Rows), len(want), got.Columns)
	}
	for _, r := range got.Rows {
		if r[1] != want[r[0]][1] {
			t.Errorf("actor %s: latest path %s, want %s", r[0], r[1], want[r[0]][1])
		}
	}
	// A number column prints as its number, and a stat has one latest.
	st := ask(Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "latest", Column: "display_width"}, {Op: "count"}}})
	if st.Rows[0][0] == "" || strings.ContainsAny(st.Rows[0][0], ".e") {
		t.Errorf("latest display_width = %q", st.Rows[0][0])
	}
	if types := (Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "latest", Column: "display_width"}, {Op: "latest", Column: "path"}}}).ColumnTypes(); types[0] != "number" || types[1] != "text" {
		t.Errorf("ColumnTypes = %v", types)
	}
}

func TestValidateLatest(t *testing.T) {
	st := func(table string, m Metric) Query {
		return Query{Kind: "stat", Table: table, Metrics: []Metric{m}}
	}
	if err := st("product", Metric{Op: "latest", Column: "attr_plan"}).Validate(); err != nil {
		t.Errorf("latest attr_plan: %v", err)
	}
	for _, tc := range []struct {
		name string
		q    Query
		path string
	}{
		{"no column", st("views", Metric{Op: "latest"}), "metrics[0].column"},
		{"unknown column", st("views", Metric{Op: "latest", Column: "nope"}), "metrics[0].column"},
		{"identity column", st("views", Metric{Op: "latest", Column: "user_label"}), "metrics[0].column"},
		{"of", st("views", Metric{Op: "latest", Column: "path", Of: "actors"}), "metrics[0].of"},
		{"on sessions", st("sessions", Metric{Op: "latest", Column: "entry_path"}), "metrics[0].op"},
		{"in a ratio", st("views", Metric{Op: "ratio", Num: &Metric{Op: "latest", Column: "path"}, Den: &Metric{Op: "count"}}), "metrics[0].num"},
		{"in a long trend", Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "latest", Column: "path"}}}, "metrics[1]"},
	} {
		err := tc.q.Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.path) {
			t.Errorf("%s: %v, want a refusal at %s", tc.name, err, tc.path)
		}
	}
}

// TestLatestRanksAndTies: a breakdown ordered by a latest number ranks
// by its value, a text one by its text; a tie at one ts takes the
// greater value as the column's type compares it.
func TestLatestRanksAndTies(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	p := Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}
	for _, col := range []string{"display_width", "path"} {
		res, err := e.Answer(context.Background(), Query{Kind: "breakdown", Table: "views", Dimensions: []string{"actor_id"},
			Where: []Filter{{Column: col, Op: "is_set"}}, Order: "w", Limit: 1000,
			Metrics: []Metric{{Op: "latest", Column: col, As: "w"}}}, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(res.Rows); i++ {
			if cmpLatest(res.Rows[i-1][1], res.Rows[i][1], col == "display_width") < 0 {
				t.Errorf("%s: row %d %v ranks above %v", col, i, res.Rows[i-1], res.Rows[i])
			}
		}
		if len(res.Rows) < 2 {
			t.Fatalf("%s: %d rows", col, len(res.Rows))
		}
	}
	if !later(5, "1200", 5, "900", true) || later(5, "1200", 5, "900", false) {
		t.Error("a tie compares numbers as numbers, text as text")
	}
	q := Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "latest", Column: "def_missing"}}}
	if err := q.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "metrics[0].column") {
		t.Errorf("latest of an undefined def_ column: %v", err)
	}
	for _, c := range LatestColumns {
		if _, ok := idColumns[c]; ok {
			t.Errorf("LatestColumns lists %s, an identity column", c)
		}
	}
}

// TestLatestSkipsEmptyText: empty text is no value, as is_set has it, so
// a later empty row does not hide the value before it.
func TestLatestSkipsEmptyText(t *testing.T) {
	c := &column{Dict: []string{"v1", ""}, Idx: []int32{0, 1}}
	if v, ok := latestText(c, 0, typeString); !ok || v != "v1" {
		t.Errorf("row 0 = %q, %v", v, ok)
	}
	if _, ok := latestText(c, 1, typeString); ok {
		t.Error("empty text read as a value")
	}
}

// latestValueFixture holds checks and measures rows, on two days: the
// newest of each name is what TestLatestValue reads.
func latestValueFixture(t *testing.T, maintain bool) *Engine {
	t.Helper()
	f := archivetest.New(t)
	base := date(t, "2026-09-08").Time()
	num := func(v float64) *float64 { return &v }
	rows := []struct {
		family store.Family
		name   string
		at     time.Duration
		value  *float64
	}{
		{store.FamilyChecks, "renewed", 3 * time.Hour, num(10)}, // two readings a day, the later greater
		{store.FamilyChecks, "renewed", 15 * time.Hour, num(90)},
		{store.FamilyChecks, "lower", 3 * time.Hour, num(90)}, // and the later lower: not a max
		{store.FamilyChecks, "lower", 15 * time.Hour, num(10)},
		{store.FamilyChecks, "cleared", 24 * time.Hour, num(40)}, // then a day later, no value
		{store.FamilyChecks, "cleared", 48 * time.Hour, nil},
		{store.FamilyChecks, "back", 24 * time.Hour, nil}, // no value, then a value
		{store.FamilyChecks, "back", 48 * time.Hour, num(0)},
		{store.FamilyChecks, "tied", 24 * time.Hour, nil}, // one instant, with and without
		{store.FamilyChecks, "tied", 24 * time.Hour, num(5)},
		{store.FamilyChecks, "never", 24 * time.Hour, nil},
		{store.FamilyMeasures, "m", 3 * time.Hour, num(1.5)},
		{store.FamilyMeasures, "m", 15 * time.Hour, num(2.5)},
	}
	for i, r := range rows {
		e := store.Event{ID: fmt.Sprintf("r%d", i), ProjectID: 1, Family: r.family, EventName: r.name, TS: base.Add(r.at), Value: r.value}
		if r.family == store.FamilyMeasures {
			e.Measure, e.SampleRate = "size", 1
		}
		f.Write(t, e)
	}
	if maintain {
		f.Maintain(t, today)
	}
	return New(Options{Dir: f.Dir, Workers: 2})
}

// TestLatestValue: latest of value is the newest row's, on the log and on
// Parquet; a newest row with no value reads as empty rather than falling
// back to an older one, which is how a reading says "none any more".
func TestLatestValue(t *testing.T) {
	for _, maintain := range []bool{false, true} {
		t.Run(fmt.Sprintf("parquet=%v", maintain), func(t *testing.T) {
			e := latestValueFixture(t, maintain)
			stat := func(table, name string) string {
				t.Helper()
				return checksRows(t, e, Query{Kind: "stat", Table: table,
					Where:   []Filter{{Column: "event_name", Op: "eq", Values: []string{name}}},
					Metrics: []Metric{{Op: "latest", Column: "value", As: "value"}}})[0][0]
			}
			for name, want := range map[string]string{
				"renewed": "90", "lower": "10", "cleared": "", "back": "0", "tied": "5", "never": "", "absent": "",
			} {
				if got := stat("checks", name); got != want {
					t.Errorf("checks %s = %q, want %q", name, got, want)
				}
			}
			if got := stat("measures", "m"); got != "2.5" {
				t.Errorf("measures m = %q, want 2.5", got)
			}
			// In a breakdown, the same; a group's empty ranks last.
			rows := checksRows(t, e, Query{Kind: "breakdown", Table: "checks", Dimensions: []string{"event_name"}, Limit: 10,
				Order: "v", Metrics: []Metric{{Op: "latest", Column: "value", As: "v"}}})
			got := map[string]string{}
			for _, r := range rows {
				got[r[0]] = r[1]
			}
			if got["renewed"] != "90" || got["cleared"] != "" || rows[0][0] != "renewed" {
				t.Errorf("breakdown = %v", rows)
			}
		})
	}
}

func TestLatestValueValidatesAsANumber(t *testing.T) {
	q := Query{Kind: "stat", Table: "checks", Metrics: []Metric{{Op: "latest", Column: "value"}}}
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	if types := q.ColumnTypes(); types[0] != "number" {
		t.Errorf("ColumnTypes = %v, want number", types)
	}
	for _, table := range []string{"sessions", "identities"} {
		q := Query{Kind: "stat", Table: table, Metrics: []Metric{{Op: "latest", Column: "value"}}}
		if err := q.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want a refusal", table, err)
		}
	}
}
