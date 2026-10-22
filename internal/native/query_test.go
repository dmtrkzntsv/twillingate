package native

import (
	"errors"
	"strings"
	"testing"
)

func count() []Metric { return []Metric{{Op: "count"}} }

func TestValidate(t *testing.T) {
	valid := map[string]Query{
		"stat":            {Kind: "stat", Table: "events", Metrics: count()},
		"stat compare":    {Kind: "stat", Table: "views", Metrics: count(), Compare: "previous"},
		"trend series":    {Kind: "trend", Table: "product", Metrics: count(), Series: "attr_plan", SeriesLimit: 3},
		"breakdown":       {Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "sum", Column: "value"}}, Dimensions: []string{"path", "day"}, Order: "sum_value", Limit: 1000, Other: true},
		"daily":           {Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "daily_max", Of: "users"}, {Op: "daily_sum", Of: "actors", As: "x"}}},
		"measures bucket": {Kind: "breakdown", Table: "measures", Metrics: count(), Dimensions: []string{"bucket"}, Where: []Filter{{Column: "bucket", Op: "gte", Values: []string{"3"}}}},
		"filters": {Kind: "stat", Table: "events", Metrics: count(), Where: []Filter{
			{Column: "attr_a", Op: "is_set"}, {Column: "country", Op: "in", Values: []string{"DE", "FR"}},
			{Column: "day", Op: "gte", Values: []string{"2026-01-02"}}, {Column: "path", Op: "prefix", Values: []string{"/x"}}}},
	}
	for name, q := range valid {
		if err := q.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	bd := func(dims ...string) Query {
		return Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: dims}
	}
	st := func(w ...Filter) Query { return Query{Kind: "stat", Table: "events", Metrics: count(), Where: w} }
	invalid := []struct {
		name string
		q    Query
		path string
	}{
		{"kind", Query{Kind: "pie", Table: "events", Metrics: count()}, "kind"},
		{"table", Query{Kind: "stat", Table: "sessions", Metrics: count()}, "table"},
		{"no dims", bd(), "dimensions"},
		{"four dims", bd("path", "host", "os", "browser"), "dimensions"},
		{"dim id", bd("id"), "dimensions[0]"},
		{"dim bucket on events", bd("bucket"), "dimensions[0]"},
		{"dim empty attr", bd("attr_"), "dimensions[0]"},
		{"dim dup", bd("path", "path"), "dimensions[1]"},
		{"stat dims", Query{Kind: "stat", Table: "events", Metrics: count(), Dimensions: []string{"path"}}, "dimensions"},
		{"trend two metrics", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "actors"}}}, "metrics"},
		{"no metrics", Query{Kind: "stat", Table: "events"}, "metrics"},
		{"sum no column", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "sum"}}}, "metrics[1].column"},
		{"sum bad column", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "sum", Column: "path"}}}, "metrics[0].column"},
		{"daily_max no of", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "daily_max"}}}, "metrics[0].of"},
		{"unknown metric op", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "median"}}}, "metrics[0].op"},
		{"column on count", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count", Column: "value"}}}, "metrics[0].column"},
		{"dup names", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "count"}}}, "metrics[1].as"},
		{"as collides dim", Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "count", As: "path"}}, Dimensions: []string{"path"}}, "metrics[0].as"},
		{"as collides series", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "count", As: "series"}}}, "metrics[0].as"},
		{"as collides day", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "count", As: "day"}}}, "metrics[0].as"},
		{"unknown filter op", st(Filter{Column: "path", Op: "like", Values: []string{"x"}}), "where[0].op"},
		{"in no values", st(Filter{Column: "path", Op: "in"}), "where[0].values"},
		{"eq two values", st(Filter{Column: "path", Op: "eq", Values: []string{"a", "b"}}), "where[0].values"},
		{"is_set values", st(Filter{Column: "path", Op: "is_set", Values: []string{"a"}}), "where[0].values"},
		{"gt on string", st(Filter{Column: "path", Op: "gt", Values: []string{"a"}}), "where[0].op"},
		{"gt bad day", st(Filter{Column: "day", Op: "gt", Values: []string{"yesterday"}}), "where[0].values"},
		{"gt bad number", st(Filter{Column: "consent", Op: "gt", Values: []string{"x"}}), "where[0].values"},
		{"filter ts", st(Filter{Column: "ts", Op: "eq", Values: []string{"a"}}), "where[0].column"},
		{"limit", Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"path"}, Limit: 1001}, "limit"},
		{"limit negative", Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"path"}, Limit: -1}, "limit"},
		{"order unknown", Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"path"}, Order: "nope"}, "order"},
		{"order on stat", Query{Kind: "stat", Table: "events", Metrics: count(), Order: "count"}, "order"},
		{"compare on trend", Query{Kind: "trend", Table: "events", Metrics: count(), Compare: "previous"}, "compare"},
		{"compare bad", Query{Kind: "stat", Table: "events", Metrics: count(), Compare: "next"}, "compare"},
		{"series on stat", Query{Kind: "stat", Table: "events", Metrics: count(), Series: "path"}, "series"},
		{"series bad", Query{Kind: "trend", Table: "events", Metrics: count(), Series: "id"}, "series"},
		{"series day", Query{Kind: "trend", Table: "events", Metrics: count(), Series: "day"}, "series"},
		{"series_limit neg", Query{Kind: "trend", Table: "events", Metrics: count(), Series: "path", SeriesLimit: -1}, "series_limit"},
	}
	for _, c := range invalid {
		err := c.q.Validate()
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
			continue
		}
		if !strings.Contains(err.Error(), c.path) {
			t.Errorf("%s: %q does not name %q", c.name, err, c.path)
		}
	}
}

func TestMetricName(t *testing.T) {
	for m, want := range map[Metric]string{
		{Op: "count"}:                        "count",
		{Op: "users"}:                        "users",
		{Op: "sum", Column: "value"}:         "sum_value",
		{Op: "max", Column: "display_width"}: "max_display_width",
		{Op: "daily_avg", Of: "groups"}:      "daily_avg_groups",
		{Op: "count", As: "n"}:               "n",
	} {
		if got := m.Name(); got != want {
			t.Errorf("%+v: %q, want %q", m, got, want)
		}
	}
}
