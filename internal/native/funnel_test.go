package native

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// TestFunnel: each step counts the actors that took it and every step
// before it, any time in the range: the first step as many as the stat
// of its actors, the next no more, and a step every actor takes keeps
// them all.
func TestFunnel(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	p := Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}
	de := []Filter{{Column: "country", Op: "eq", Values: []string{"DE"}}}
	q := []Filter{{Column: "path", Op: "prefix", Values: []string{"/q"}}}
	views := []Filter{{Column: "family", Op: "eq", Values: []string{"views"}}}
	stat := func(where ...Filter) string {
		res, err := e.Answer(context.Background(), Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "actors"}}, Where: where}, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		return res.Rows[0][0]
	}
	for _, seq := range []bool{false, true} {
		res, err := e.Answer(context.Background(), Query{Kind: "funnel", Sequence: seq, Steps: []Step{
			{Label: "German", Where: de}, {Label: "Any view", Where: views}, {Label: "Q page", Where: q}}}, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 3 || res.Columns[0] != "step" || res.Rows[1][0] != "Any view" {
			t.Fatalf("sequence %v: %v %v", seq, res.Columns, res.Rows)
		}
		n := func(i int) int64 { v, _ := strconv.ParseInt(res.Rows[i][1], 10, 64); return v }
		if res.Rows[0][1] != stat(de...) || n(1) > n(0) || n(2) > n(1) {
			t.Errorf("sequence %v: %v; the German actors are %s", seq, res.Rows, stat(de...))
		}
		if !seq && res.Rows[2][1] != stat(Filter{Op: "ever", Over: "range", Where: de}, Filter{Op: "ever", Over: "range", Where: views}, q[0]) {
			t.Errorf("any order: Q page %s, want the actors with all three, %s", res.Rows[2][1],
				stat(Filter{Op: "ever", Over: "range", Where: de}, Filter{Op: "ever", Over: "range", Where: views}, q[0]))
		}
	}
	// AnswerMany answers it alone, the same.
	var many Answered
	fq := Query{Kind: "funnel", Steps: []Step{{Label: "a", Where: de}, {Label: "b", Where: q}}}
	e.AnswerMany(context.Background(), []Request{{Query: fq, Params: p, Rules: engineRules}}, func(_ int, a Answered) { many = a })
	if many.Err != nil || len(many.Result.Rows) != 2 {
		t.Errorf("AnswerMany = %+v", many)
	}
}

func TestValidateFunnel(t *testing.T) {
	st := []Step{{Label: "a", Where: []Filter{{Column: "path", Op: "is_set"}}}, {Label: "b", Where: []Filter{{Column: "host", Op: "is_set"}}}}
	if err := (Query{Kind: "funnel", Table: "product", By: "user", Sequence: true, Steps: st}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		q    Query
		path string
	}{
		{"one step", Query{Kind: "funnel", Steps: st[:1]}, "steps"},
		{"measures", Query{Kind: "funnel", Table: "measures", Steps: st}, "table"},
		{"metrics", Query{Kind: "funnel", Steps: st, Metrics: []Metric{{Op: "count"}}}, "metrics"},
		{"bad by", Query{Kind: "funnel", Steps: st, By: "session"}, "by"},
		{"same label", Query{Kind: "funnel", Steps: []Step{st[0], st[0]}}, "steps[1].label"},
		{"no filters", Query{Kind: "funnel", Steps: []Step{st[0], {Label: "c"}}}, "steps[1].where"},
		{"bad filter", Query{Kind: "funnel", Steps: []Step{st[0], {Label: "c", Where: []Filter{{Column: "nope", Op: "is_set"}}}}}, "steps[1].where[0].column"},
		{"steps on a stat", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}}, Steps: st}, "steps"},
		{"sequence on a stat", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}}, Sequence: true}, "sequence"},
	} {
		err := tc.q.Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.path) {
			t.Errorf("%s: %v, want a refusal at %s", tc.name, err, tc.path)
		}
	}
}
