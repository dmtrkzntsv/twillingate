package native

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// TestDefinedColumns: a defined column reads the label of the first case
// a row passes, else its else: a breakdown by it, a filter on it and a
// trend split by it count as the cases' filters do.
func TestDefinedColumns(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	p := Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}
	de := Filter{Column: "country", Op: "eq", Values: []string{"DE"}}
	q := Filter{Column: "path", Op: "prefix", Values: []string{"/q"}}
	def := []Define{{Name: "def_seg", Cases: []Case{{Label: "German", Where: []Filter{de}}, {Label: "Q pages", Where: []Filter{q}}}, Else: "Rest"}}
	count := func(where ...Filter) int64 {
		t.Helper()
		res, err := e.Answer(context.Background(), Query{Kind: "stat", Table: "views", Define: def, Metrics: []Metric{{Op: "count"}}, Where: where}, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := strconv.ParseInt(res.Rows[0][0], 10, 64)
		return n
	}
	all, german := count(), count(de)
	qPages := count(q) - count(de, q) // first match wins: a German /q view is German
	want := map[string]int64{"German": german, "Q pages": qPages, "Rest": all - german - qPages}

	res, err := e.Answer(context.Background(), Query{Kind: "breakdown", Table: "views", Define: def,
		Dimensions: []string{"def_seg"}, Metrics: []Metric{{Op: "count"}}}, p, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 || res.Columns[0] != "def_seg" {
		t.Fatalf("breakdown = %v %v", res.Columns, res.Rows)
	}
	for _, r := range res.Rows {
		if n, _ := strconv.ParseInt(r[1], 10, 64); n != want[r[0]] || n == 0 {
			t.Errorf("breakdown %s = %s, want %d", r[0], r[1], want[r[0]])
		}
	}
	for label, n := range want {
		if got := count(Filter{Column: "def_seg", Op: "eq", Values: []string{label}}); got != n {
			t.Errorf("where def_seg = %s: %d, want %d", label, got, n)
		}
	}

	trend, err := e.Answer(context.Background(), Query{Kind: "trend", Table: "views", Define: def, Series: "def_seg",
		Metrics: []Metric{{Op: "count", Where: []Filter{{Column: "def_seg", Op: "ne", Values: []string{"Rest"}}}}}}, p, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	sums := map[string]int64{}
	for _, r := range trend.Rows {
		n, _ := strconv.ParseInt(r[2], 10, 64)
		sums[r[1]] += n
	}
	if sums["German"] != want["German"] || sums["Q pages"] != want["Q pages"] || sums["Rest"] != 0 {
		t.Errorf("trend by def_seg sums %v, want German %d, Q pages %d, Rest 0", sums, want["German"], want["Q pages"])
	}

	// Without else, a row no case takes reads NULL: its own, empty group.
	def[0].Else = ""
	res, err = e.Answer(context.Background(), Query{Kind: "breakdown", Table: "views", Define: def,
		Dimensions: []string{"def_seg"}, Metrics: []Metric{{Op: "count"}}, Order: "def_seg"}, p, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if last := res.Rows[len(res.Rows)-1]; last[0] != "" || last[1] != strconv.FormatInt(want["Rest"], 10) {
		t.Errorf("without else, the last group = %v, want empty with %d", last, want["Rest"])
	}
}

func TestValidateDefines(t *testing.T) {
	eq := Filter{Column: "path", Op: "eq", Values: []string{"/"}}
	ok := []Define{{Name: "def_x", Cases: []Case{{Label: "a", Where: []Filter{eq}}}}}
	q := func(edit func(q *Query)) Query {
		q := Query{Kind: "breakdown", Table: "views", Define: ok, Dimensions: []string{"def_x"}, Metrics: []Metric{{Op: "count"}}}
		edit(&q)
		return q
	}
	if err := q(func(*Query) {}).Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for _, tc := range []struct {
		name string
		q    Query
		path string
	}{
		{"undefined dimension", q(func(q *Query) { q.Dimensions = []string{"def_y"} }), "dimensions[0]"},
		{"undefined filter", q(func(q *Query) { q.Where = []Filter{{Column: "def_y", Op: "is_set"}} }), "where[0].column"},
		{"undefined in any_of", q(func(q *Query) {
			q.Where = []Filter{{Op: "any_of", Any: []Filter{eq, {Column: "def_y", Op: "is_set"}}}}
		}), "where[0].any[1].column"},
		{"undefined in a metric", q(func(q *Query) { q.Metrics[0].Where = []Filter{{Column: "def_y", Op: "is_set"}} }), "metrics[0].where[0].column"},
		{"bad name", q(func(q *Query) { q.Define = []Define{{Name: "seg", Cases: ok[0].Cases}} }), "define[0].name"},
		{"twice", q(func(q *Query) { q.Define = append(q.Define, ok[0]) }), "define[1].name"},
		{"no cases", q(func(q *Query) { q.Define = []Define{{Name: "def_x"}} }), "define[0].cases"},
		{"empty label", q(func(q *Query) { q.Define = []Define{{Name: "def_x", Cases: []Case{{Where: []Filter{eq}}}}} }), "define[0].cases[0].label"},
		{"no filters", q(func(q *Query) { q.Define = []Define{{Name: "def_x", Cases: []Case{{Label: "a"}}}} }), "define[0].cases[0].where"},
		{"case names a def", q(func(q *Query) {
			q.Define = []Define{{Name: "def_x", Cases: []Case{{Label: "a", Where: []Filter{{Column: "def_x", Op: "is_set"}}}}}}
		}), "define[0].cases[0].where[0]"},
		{"bad case filter", q(func(q *Query) {
			q.Define = []Define{{Name: "def_x", Cases: []Case{{Label: "a", Where: []Filter{{Column: "nope", Op: "is_set"}}}}}}
		}), "define[0].cases[0].where[0].column"},
		{"on a retention", Query{Kind: "retention", Output: "grid", Define: ok}, "define"},
	} {
		err := tc.q.Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.path+":") {
			t.Errorf("%s: %v, want a refusal at %s", tc.name, err, tc.path)
		}
	}
}
