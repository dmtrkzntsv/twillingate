package native

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckCompilesValues: Check refuses what Validate lets through and
// answering refuses before it reads a file: a value its column's type
// cannot parse, wherever the filter sits.
func TestCheckCompilesValues(t *testing.T) {
	bad := []Filter{{Column: "display_width", Op: "eq", Values: []string{"wide"}}}
	stat := func() Query { return Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count"}}} }
	cases := map[string]struct {
		q    Query
		path string
	}{
		"where":        {func() Query { q := stat(); q.Where = bad; return q }(), "where[0].values"},
		"metric where": {func() Query { q := stat(); q.Metrics[0].Where = bad; return q }(), "metrics[0].where[0].values"},
		"ratio side": {func() Query {
			q := stat()
			q.Metrics = []Metric{{Op: "ratio", Num: &Metric{Op: "count", Where: bad}, Den: &Metric{Op: "count"}}}
			return q
		}(), "metrics[0].num.where[0].values"},
		"define": {func() Query {
			q := stat()
			q.Define = []Define{{Name: "def_size", Cases: []Case{{Label: "wide", Where: bad}}, Else: "other"}}
			return q
		}(), "define[0].cases[0].where[0].values"},
		"funnel step": {Query{Kind: "funnel", Table: "events", Steps: []Step{
			{Label: "a", Where: []Filter{{Column: "event_name", Op: "eq", Values: []string{"x"}}}},
			{Label: "b", Where: bad},
		}}, "steps[1].where[0].values"},
	}
	for name, c := range cases {
		if err := c.q.Validate(); err != nil {
			t.Fatalf("%s: Validate = %v; the case must pass it", name, err)
		}
		err := c.q.Check()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.path+": ") || !strings.Contains(err.Error(), `"wide" is not an integer`) {
			t.Errorf("%s: Check = %v, want ErrInvalid at %s", name, err, c.path)
		}
	}

	ok := stat()
	ok.Where = []Filter{{Column: "display_width", Op: "eq", Values: []string{"1280"}}}
	if err := ok.Check(); err != nil {
		t.Errorf("Check(a good query) = %v", err)
	}
	if err := (Query{Kind: "nope"}).Check(); !errors.Is(err, ErrInvalid) {
		t.Errorf("Check(a bad kind) = %v, want Validate's refusal", err)
	}
}
