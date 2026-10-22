package native

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func count() []Metric { return []Metric{{Op: "count"}} }

func TestValidate(t *testing.T) {
	valid := map[string]Query{
		"stat":            {Kind: "stat", Table: "events", Metrics: count()},
		"stat compare":    {Kind: "stat", Table: "views", Metrics: count(), Compare: "previous"},
		"trend series":    {Kind: "trend", Table: "product", Metrics: count(), Series: "attr_plan", SeriesLimit: 3},
		"series_limit 50": {Kind: "trend", Table: "product", Metrics: count(), Series: "path", SeriesLimit: 50},
		"previous_ alone": {Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count", As: "previous_users"}, {Op: "actors"}}, Compare: "previous"},
		"no compare":      {Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count", As: "previous_users"}, {Op: "users"}}},
		"breakdown":       {Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "sum", Column: "value"}}, Dimensions: []string{"path", "day"}, Order: "sum_value", Limit: 1000, Other: true},
		"daily":           {Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "daily_max", Of: "users"}, {Op: "daily_sum", Of: "actors", As: "x"}}},
		"order dimension": {Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"path", "day"}, Order: "day", Limit: 1000},
		"measures bucket": {Kind: "breakdown", Table: "measures", Metrics: count(), Dimensions: []string{"bucket"}, Where: []Filter{{Column: "bucket", Op: "gte", Values: []string{"3"}}}},
		"computed": {Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"resolution", "device_label", "platform_version"},
			Where: []Filter{{Column: "consent_state", Op: "eq", Values: []string{"given"}}}},
		"computed product": {Kind: "trend", Table: "product", Metrics: count(), Series: "consent_state",
			Where: []Filter{{Column: "platform_version", Op: "is_set"}, {Column: "device_label", Op: "ne", Values: []string{"x"}}}},
		"sessions": {Kind: "breakdown", Table: "sessions", Metrics: []Metric{{Op: "count"}, {Op: "actors"}},
			Dimensions: []string{"entry_path", "day", "project_id"}, Where: []Filter{{Column: "views", Op: "gt", Values: []string{"1"}},
				{Column: "duration_sec", Op: "lte", Values: []string{"60"}}, {Column: "utm_source", Op: "is_set"}}},
		"sessions trend": {Kind: "trend", Table: "sessions", Metrics: count(), Series: "referrer_source"},
		"session metrics": {Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "bounces"}, {Op: "bounce_rate"},
			{Op: "sum", Column: "duration_sec"}, {Op: "avg", Column: "views"}, {Op: "min", Column: "duration_sec"}, {Op: "max", Column: "views"}}},
		"bounce rate breakdown": {Kind: "breakdown", Table: "sessions", Metrics: []Metric{{Op: "bounce_rate"}},
			Dimensions: []string{"referrer_source"}, Order: "bounce_rate", Other: true},
		"status": {Kind: "trend", Table: "events", Metrics: []Metric{{Op: "users"}}, Series: "user_status",
			Where: []Filter{{Column: "group_status", Op: "eq", Values: []string{"new"}}}},
		"status product": {Kind: "breakdown", Table: "product", Metrics: count(), Dimensions: []string{"group_status", "user_status"}},
		"identities": {Kind: "breakdown", Table: "identities", Metrics: []Metric{{Op: "count"}, {Op: "sum", Column: "views"}, {Op: "avg", Column: "events"}},
			Dimensions: []string{"kind", "id", "name"}, Where: []Filter{{Column: "views", Op: "gt", Values: []string{"1"}},
				{Column: "events", Op: "lte", Values: []string{"5"}}, {Column: "day", Op: "gte", Values: []string{"2026-01-02"}}}},
		"identities trend": {Kind: "trend", Table: "identities", Metrics: count(), Series: "kind"},
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
		{"table", Query{Kind: "stat", Table: "visits", Metrics: count()}, "table"},
		{"path on sessions", Query{Kind: "breakdown", Table: "sessions", Metrics: count(), Dimensions: []string{"path"}}, "dimensions[0]"},
		{"views as a dimension", Query{Kind: "breakdown", Table: "sessions", Metrics: count(), Dimensions: []string{"views"}}, "dimensions[0]"},
		{"duration as a series", Query{Kind: "trend", Table: "sessions", Metrics: count(), Series: "duration_sec"}, "series"},
		{"started_at filter", Query{Kind: "stat", Table: "sessions", Metrics: count(), Where: []Filter{{Column: "started_at", Op: "is_set"}}}, "where[0].column"},
		{"computed on sessions", Query{Kind: "stat", Table: "sessions", Metrics: count(), Where: []Filter{{Column: "consent_state", Op: "is_set"}}}, "where[0].column"},
		{"attr on sessions", Query{Kind: "breakdown", Table: "sessions", Metrics: count(), Dimensions: []string{"attr_plan"}}, "dimensions[0]"},
		{"users on sessions", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "users"}}}, "metrics[0].op"},
		{"sum value on sessions", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "sum", Column: "value"}}}, "metrics[0].column"},
		{"avg duration on views", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "avg", Column: "duration_sec"}}}, "metrics[0].column"},
		{"bounces on views", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "bounces"}}}, "metrics[0].op"},
		{"bounce_rate on events", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "bounce_rate"}}}, "metrics[0].op"},
		{"column on bounces", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "bounces", Column: "views"}}}, "metrics[0].column"},
		{"of on bounce_rate", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "bounce_rate", Of: "actors"}}}, "metrics[0].of"},
		{"daily on sessions", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "daily_sum", Of: "actors"}}}, "metrics[0].op"},
		{"entry_path on views", Query{Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"entry_path"}}, "dimensions[0]"},
		{"views filter on events", st(Filter{Column: "views", Op: "gt", Values: []string{"1"}}), "where[0].column"},
		{"user_status on measures", Query{Kind: "breakdown", Table: "measures", Metrics: count(), Dimensions: []string{"user_status"}}, "dimensions[0]"},
		{"group_status on sessions", Query{Kind: "stat", Table: "sessions", Metrics: count(), Where: []Filter{{Column: "group_status", Op: "is_set"}}}, "where[0].column"},
		{"gt on status", st(Filter{Column: "user_status", Op: "gt", Values: []string{"1"}}), "where[0].op"},
		{"path on identities", Query{Kind: "breakdown", Table: "identities", Metrics: count(), Dimensions: []string{"path"}}, "dimensions[0]"},
		{"views as an identities dimension", Query{Kind: "breakdown", Table: "identities", Metrics: count(), Dimensions: []string{"views"}}, "dimensions[0]"},
		{"status on identities", Query{Kind: "stat", Table: "identities", Metrics: count(), Where: []Filter{{Column: "user_status", Op: "is_set"}}}, "where[0].column"},
		{"users on identities", Query{Kind: "stat", Table: "identities", Metrics: []Metric{{Op: "users"}}}, "metrics[0].op"},
		{"actors on identities", Query{Kind: "stat", Table: "identities", Metrics: []Metric{{Op: "actors"}}}, "metrics[0].op"},
		{"sum duration on identities", Query{Kind: "stat", Table: "identities", Metrics: []Metric{{Op: "sum", Column: "duration_sec"}}}, "metrics[0].column"},
		{"sum events on views", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "sum", Column: "events"}}}, "metrics[0].column"},
		{"sum events on sessions", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "sum", Column: "events"}}}, "metrics[0].column"},
		{"no dims", bd(), "dimensions"},
		{"four dims", bd("path", "host", "os", "browser"), "dimensions"},
		{"dim id", bd("id"), "dimensions[0]"},
		{"dim bucket on events", bd("bucket"), "dimensions[0]"},
		{"dim empty attr", bd("attr_"), "dimensions[0]"},
		{"resolution on product", Query{Kind: "breakdown", Table: "product", Metrics: count(), Dimensions: []string{"resolution"}}, "dimensions[0]"},
		{"consent_state on measures", Query{Kind: "trend", Table: "measures", Metrics: count(), Series: "consent_state"}, "series"},
		{"gt on computed", st(Filter{Column: "consent_state", Op: "gt", Values: []string{"1"}}), "where[0].op"},
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
		{"order on a column not a dimension", Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"path"}, Order: "day"}, "order"},
		{"order on stat", Query{Kind: "stat", Table: "events", Metrics: count(), Order: "count"}, "order"},
		{"compare on trend", Query{Kind: "trend", Table: "events", Metrics: count(), Compare: "previous"}, "compare"},
		{"compare bad", Query{Kind: "stat", Table: "events", Metrics: count(), Compare: "next"}, "compare"},
		{"series on stat", Query{Kind: "stat", Table: "events", Metrics: count(), Series: "path"}, "series"},
		{"series bad", Query{Kind: "trend", Table: "events", Metrics: count(), Series: "id"}, "series"},
		{"series day", Query{Kind: "trend", Table: "events", Metrics: count(), Series: "day"}, "series"},
		{"series_limit neg", Query{Kind: "trend", Table: "events", Metrics: count(), Series: "path", SeriesLimit: -1}, "series_limit"},
		{"series_limit 51", Query{Kind: "trend", Table: "events", Metrics: count(), Series: "path", SeriesLimit: 51}, "series_limit"},
		{"series_limit on stat", Query{Kind: "stat", Table: "events", Metrics: count(), SeriesLimit: 3}, "series_limit"},
		{"compare collides", Query{Kind: "stat", Table: "events", Compare: "previous",
			Metrics: []Metric{{Op: "count", As: "previous_users"}, {Op: "users"}}}, "metrics[0].as"},
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
		{Op: "bounce_rate"}:                  "bounce_rate",
		{Op: "avg", Column: "duration_sec"}:  "avg_duration_sec",
	} {
		if got := m.Name(); got != want {
			t.Errorf("%+v: %q, want %q", m, got, want)
		}
	}
}

func TestColumns(t *testing.T) {
	cases := []struct {
		name string
		q    Query
		want []string
	}{
		{"stat", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "actors", As: "value"}}},
			[]string{"count", "value"}},
		{"stat compare", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "sum", Column: "value"}}, Compare: "previous"},
			[]string{"count", "sum_value", "previous_count", "previous_sum_value"}},
		{"trend", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "daily_max", Of: "users"}}},
			[]string{"day", "daily_max_users"}},
		{"trend series", Query{Kind: "trend", Table: "product", Metrics: count(), Series: "path"},
			[]string{"day", "series", "count"}},
		{"breakdown", Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "actors"}, {Op: "count", As: "n"}}, Dimensions: []string{"country", "path"}},
			[]string{"country", "path", "actors", "n"}},
	}
	for _, c := range cases {
		if got := c.q.Columns(); !slices.Equal(got, c.want) {
			t.Errorf("%s: Columns() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestColumnTypes(t *testing.T) {
	cases := []struct {
		name string
		q    Query
		want []string
	}{
		{"stat compare", Query{Kind: "stat", Metrics: []Metric{{Op: "count"}, {Op: "actors"}}, Compare: "previous"},
			[]string{"number", "number", "number", "number"}},
		{"trend", Query{Kind: "trend", Metrics: count()}, []string{"day", "number"}},
		{"trend series", Query{Kind: "trend", Metrics: count(), Series: "display_width"}, []string{"day", "text", "number"}},
		{"breakdown", Query{Kind: "breakdown", Metrics: count(),
			Dimensions: []string{"day", "path", "attr_plan", "project_id", "display_width", "display_height", "consent", "bucket"}},
			[]string{"day", "text", "text", "number", "number", "number", "number", "number", "number"}},
		{"sessions breakdown", Query{Kind: "breakdown", Table: "sessions", Metrics: count(),
			Dimensions: []string{"entry_path", "day", "project_id"}}, []string{"text", "day", "number", "number"}},
		{"breakdown computed", Query{Kind: "breakdown", Metrics: count(),
			Dimensions: []string{"resolution", "device_label", "consent_state"}},
			[]string{"text", "text", "text", "number"}},
		{"identities breakdown", Query{Kind: "breakdown", Table: "identities", Metrics: count(),
			Dimensions: []string{"kind", "id", "name", "day"}}, []string{"text", "text", "text", "day", "number"}},
		{"status series", Query{Kind: "trend", Metrics: count(), Series: "user_status"}, []string{"day", "text", "number"}},
		{"breakdown other", Query{Kind: "breakdown", Metrics: count(), Dimensions: []string{"day", "consent"}, Other: true},
			[]string{"text", "text", "number"}},
	}
	for _, c := range cases {
		if got := c.q.ColumnTypes(); !slices.Equal(got, c.want) {
			t.Errorf("%s: ColumnTypes() = %v, want %v", c.name, got, c.want)
		}
		if n := len(c.q.Columns()); n != len(c.want) {
			t.Errorf("%s: %d columns, %d types", c.name, n, len(c.want))
		}
	}
}
