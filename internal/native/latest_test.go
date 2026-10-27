package native

import (
	"context"
	"errors"
	"strings"
	"testing"
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
	c := &Column{Dict: []string{"v1", ""}, Idx: []int32{0, 1}}
	if v, ok := latestText(c, 0, typeString); !ok || v != "v1" {
		t.Errorf("row 0 = %q, %v", v, ok)
	}
	if _, ok := latestText(c, 1, typeString); ok {
		t.Error("empty text read as a value")
	}
}
