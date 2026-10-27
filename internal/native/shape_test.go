package native

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestLongShape: a long stat or breakdown is its wide answer a row per
// metric, the dimensions repeated, previous_value beside value with
// compare; through AnswerMany too.
func TestLongShape(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	p := Params{Project: 1, From: date(t, "2026-09-01"), To: date(t, today)}
	ms := []Metric{{Op: "count", As: "views"}, {Op: "actors", As: "visitors"}}
	for _, wide := range []Query{
		{Kind: "stat", Table: "views", Metrics: ms, Compare: "previous"},
		{Kind: "breakdown", Table: "views", Dimensions: []string{"country"}, Metrics: ms, Limit: 3},
	} {
		long := wide
		long.Shape = "long"
		w, err := e.Answer(context.Background(), wide, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		l, err := e.Answer(context.Background(), long, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(l.Columns, long.Columns()) {
			t.Errorf("%s columns %v, Columns() %v", wide.Kind, l.Columns, long.Columns())
		}
		if len(l.Rows) != 2*len(w.Rows) {
			t.Fatalf("%s: %d long rows for %d wide", wide.Kind, len(l.Rows), len(w.Rows))
		}
		nd := len(wide.Dimensions)
		for i, r := range w.Rows {
			for j, m := range ms {
				lr := l.Rows[2*i+j]
				want := append(append([]string{}, r[:nd]...), m.As, r[nd+j])
				if wide.Compare == "previous" {
					want = append(want, r[nd+2+j])
				}
				if !reflect.DeepEqual(lr, want) {
					t.Errorf("%s row %d metric %s = %v, want %v", wide.Kind, i, m.As, lr, want)
				}
			}
		}
		var many Answered
		e.AnswerMany(context.Background(), []Request{{Query: long, Params: p, Rules: engineRules}}, func(_ int, a Answered) { many = a })
		if many.Err != nil || !reflect.DeepEqual(many.Result.Rows, l.Rows) {
			t.Errorf("%s AnswerMany = %+v", wide.Kind, many)
		}
	}
	if got := (Query{Kind: "breakdown", Shape: "long", Dimensions: []string{"country"}, Metrics: ms}).ColumnTypes(); !reflect.DeepEqual(got, []string{"text", "text", "number"}) {
		t.Errorf("ColumnTypes = %v", got)
	}
	for _, tc := range []struct {
		q    Query
		path string
	}{
		{Query{Kind: "trend", Table: "views", Shape: "long", Metrics: ms}, "shape"},
		{Query{Kind: "trend", Table: "views", Shape: "wide", Metrics: ms}, "shape"},
		{Query{Kind: "stat", Table: "views", Shape: "tall", Metrics: ms}, "shape"},
		{Query{Kind: "stat", Table: "views", Shape: "long", Metrics: []Metric{{Op: "max", Column: "day"}}}, "metrics[0]"},
	} {
		if err := tc.q.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.path) {
			t.Errorf("%+v: %v, want a refusal at %s", tc.q, err, tc.path)
		}
	}
}

// TestLongApproximate: a metric named previous_<another> without compare
// marks value, not a previous_value the answer lacks.
func TestLongApproximate(t *testing.T) {
	q := Query{Kind: "stat", Shape: "long", Metrics: []Metric{{Op: "count", As: "total"}, {Op: "actors", As: "previous_total"}}}
	got := toLong(q, Result{Columns: []string{"total", "previous_total"}, Rows: [][]string{{"5", "3"}}, Approximate: []string{"previous_total"}})
	if len(got.Approximate) != 1 || got.Approximate[0] != "value" {
		t.Errorf("Approximate = %v, want [value]", got.Approximate)
	}
	q.Compare, q.Metrics = "previous", q.Metrics[:1]
	got = toLong(q, Result{Columns: []string{"total", "previous_total"}, Rows: [][]string{{"5", "3"}}, Approximate: []string{"previous_total"}})
	if len(got.Approximate) != 1 || got.Approximate[0] != "previous_value" {
		t.Errorf("with compare, Approximate = %v, want [previous_value]", got.Approximate)
	}
}
