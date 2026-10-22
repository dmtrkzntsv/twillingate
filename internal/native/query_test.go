package native

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func count() []Metric { return []Metric{{Op: "count"}} }

func ptr[T any](v T) *T { return &v }

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
		"metric where and round": {Kind: "breakdown", Table: "events", Dimensions: []string{"path"}, Metrics: []Metric{
			{Op: "count", As: "views", Where: []Filter{{Column: "family", Op: "eq", Values: []string{"views"}}}},
			{Op: "avg", Column: "value", Round: ptr(0), Where: []Filter{{Column: "consent_state", Op: "eq", Values: []string{"given"}},
				{Column: "day", Op: "gte", Values: []string{"2026-01-02"}}}},
			{Op: "daily_avg", Of: "users", Round: ptr(6), Where: []Filter{{Column: "user_status", Op: "eq", Values: []string{"new"}}}}}},
		"session metric where": {Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "bounce_rate", Round: ptr(3),
			Where: []Filter{{Column: "views", Op: "gt", Values: []string{"1"}}}}}},
		"percentiles": {Kind: "breakdown", Table: "measures", Dimensions: []string{"path"}, Metrics: []Metric{
			{Op: "percentile", Q: 0.75, Round: ptr(0), Where: []Filter{{Column: "event_name", Op: "eq", Values: []string{"$lcp"}}}},
			{Op: "percentile", Q: 0.999}, {Op: "mean", Round: ptr(3)}, {Op: "weight"}}, Order: "p75", Other: true},
		"percentile trend": {Kind: "trend", Table: "measures", Metrics: []Metric{{Op: "percentile", Q: 0.5}}, Series: "event_name"},
		"ratings": {Kind: "breakdown", Table: "measures", Dimensions: []string{"vital", "vital_rating"}, Order: "vital",
			Metrics: []Metric{{Op: "share", Of: "weight", As: "y"}, {Op: "share", Of: "count", Round: ptr(3)}},
			Where:   []Filter{{Column: "vital", Op: "is_set"}, {Column: "vital_rating", Op: "ne", Values: []string{"poor"}}}},
		"share trend": {Kind: "trend", Table: "events", Metrics: []Metric{{Op: "share", Of: "count",
			Where: []Filter{{Column: "family", Op: "eq", Values: []string{"views"}}}}}, Series: "country"},
		"share sessions":   {Kind: "breakdown", Table: "sessions", Dimensions: []string{"referrer_source"}, Metrics: []Metric{{Op: "share", Of: "count"}}},
		"share identities": {Kind: "breakdown", Table: "identities", Dimensions: []string{"kind"}, Metrics: []Metric{{Op: "share", Of: "count"}}},
		"filters": {Kind: "stat", Table: "events", Metrics: count(), Where: []Filter{
			{Column: "attr_a", Op: "is_set"}, {Column: "country", Op: "in", Values: []string{"DE", "FR"}},
			{Column: "day", Op: "gte", Values: []string{"2026-01-02"}}, {Column: "path", Op: "prefix", Values: []string{"/x"}}, {Column: "path", Op: "not_prefix", Values: []string{"/xy"}}}},
		"retention grid":        {Kind: "retention", Output: "grid"},
		"retention grid events": {Kind: "retention", Table: "events", Output: "grid", Offsets: 90, Where: []Filter{{Column: "country", Op: "eq", Values: []string{"DE"}}}},
		"retention curve":       {Kind: "retention", Output: "curve", IdentifiedBy: "install", Offsets: 1},
		"retention milestones":  {Kind: "retention", Output: "milestones", IdentifiedBy: "user"},
		"retention own milestones": {Kind: "retention", Output: "milestones", IdentifiedBy: "install", Offsets: 14,
			Milestones: []int{0, 1, 3, 7, 10, 14}},
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
		{"round 7", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "avg", Column: "value", Round: ptr(7)}}}, "metrics[0].round"},
		{"round negative", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "avg", Column: "value", Round: ptr(-1)}}}, "metrics[1].round"},
		{"metric where column", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count"},
			{Op: "count", As: "n", Where: []Filter{{Column: "is_set", Op: "eq", Values: []string{"x"}}}}}}, "metrics[1].where[0].column"},
		{"metric where op", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count",
			Where: []Filter{{Column: "path", Op: "is_set"}, {Column: "path", Op: "like", Values: []string{"x"}}}}}}, "metrics[0].where[1].op"},
		{"metric where values", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count",
			Where: []Filter{{Column: "path", Op: "eq"}}}}}, "metrics[0].where[0].values"},
		{"metric where gt on text", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count",
			Where: []Filter{{Column: "path", Op: "gt", Values: []string{"1"}}}}}}, "metrics[0].where[0].op"},
		{"metric where path on sessions", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "count",
			Where: []Filter{{Column: "path", Op: "is_set"}}}}}, "metrics[0].where[0].column"},
		{"metric where bucket on events", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count",
			Where: []Filter{{Column: "bucket", Op: "gt", Values: []string{"1"}}}}}}, "metrics[0].where[0].column"},
		{"percentile on events", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "percentile", Q: 0.5}}}, "metrics[0].op"},
		{"mean on views", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "mean"}}}, "metrics[1].op"},
		{"weight on sessions", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "weight"}}}, "metrics[0].op"},
		{"percentile without q", Query{Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "percentile"}}}, "metrics[0].q"},
		{"percentile q 1", Query{Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "percentile", Q: 1}}}, "metrics[0].q"},
		{"percentile q negative", Query{Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "percentile", Q: -0.5}}}, "metrics[0].q"},
		{"q on mean", Query{Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "mean", Q: 0.5}}}, "metrics[0].q"},
		{"q on count", Query{Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "count", Q: 0.5}}}, "metrics[0].q"},
		{"column on percentile", Query{Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "percentile", Q: 0.5, Column: "value"}}}, "metrics[0].column"},
		{"of on weight", Query{Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "weight", Of: "actors"}}}, "metrics[0].of"},
		{"two p50", Query{Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "percentile", Q: 0.5}, {Op: "percentile", Q: 0.5}}}, "metrics[1].as"},
		{"share on a stat", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "share", Of: "count"}}}, "metrics[0].op"},
		{"share without of", Query{Kind: "breakdown", Table: "events", Dimensions: []string{"path"}, Metrics: []Metric{{Op: "share"}}}, "metrics[0].of"},
		{"share of actors", Query{Kind: "breakdown", Table: "events", Dimensions: []string{"path"}, Metrics: []Metric{{Op: "share", Of: "actors"}}}, "metrics[0].of"},
		{"share of weight on events", Query{Kind: "breakdown", Table: "events", Dimensions: []string{"path"}, Metrics: []Metric{{Op: "share", Of: "weight"}}}, "metrics[0].of"},
		{"share on an unsplit trend", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "share", Of: "count"}}}, "metrics[0].op"},
		{"share with a column", Query{Kind: "breakdown", Table: "measures", Dimensions: []string{"path"}, Metrics: []Metric{{Op: "share", Of: "count", Column: "value"}}}, "metrics[0].column"},
		{"daily_sum of count", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "daily_sum", Of: "count"}}}, "metrics[0].of"},
		{"vital on events", Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"vital"}}, "dimensions[0]"},
		{"vital_rating on views", Query{Kind: "stat", Table: "views", Metrics: count(), Where: []Filter{{Column: "vital_rating", Op: "is_set"}}}, "where[0].column"},
		{"compare collides", Query{Kind: "stat", Table: "events", Compare: "previous",
			Metrics: []Metric{{Op: "count", As: "previous_users"}, {Op: "users"}}}, "metrics[0].as"},
		{"retention no output", Query{Kind: "retention"}, "output"},
		{"retention bad output", Query{Kind: "retention", Output: "table"}, "output"},
		{"retention table", Query{Kind: "retention", Table: "views", Output: "grid"}, "table"},
		{"retention grid identified_by", Query{Kind: "retention", Output: "grid", IdentifiedBy: "user"}, "identified_by"},
		{"retention curve no identified_by", Query{Kind: "retention", Output: "curve"}, "identified_by"},
		{"retention bad identified_by", Query{Kind: "retention", Output: "curve", IdentifiedBy: "group"}, "identified_by"},
		{"retention offsets 91", Query{Kind: "retention", Output: "grid", Offsets: 91}, "offsets"},
		{"retention offsets negative", Query{Kind: "retention", Output: "grid", Offsets: -1}, "offsets"},
		{"retention milestones on a grid", Query{Kind: "retention", Output: "grid", Milestones: []int{7}}, "milestones"},
		{"retention milestones on a curve", Query{Kind: "retention", Output: "curve", IdentifiedBy: "user", Milestones: []int{7}}, "milestones"},
		{"retention milestone past offsets", Query{Kind: "retention", Output: "milestones", IdentifiedBy: "user", Offsets: 10, Milestones: []int{7, 11}}, "milestones[1]"},
		{"retention milestone negative", Query{Kind: "retention", Output: "milestones", IdentifiedBy: "user", Milestones: []int{-1}}, "milestones[0]"},
		{"retention milestone repeated", Query{Kind: "retention", Output: "milestones", IdentifiedBy: "user", Milestones: []int{7, 7}}, "milestones[1]"},
		{"retention seven milestones", Query{Kind: "retention", Output: "milestones", IdentifiedBy: "user", Milestones: []int{1, 2, 3, 4, 5, 6, 7}}, "milestones"},
		{"retention default milestones past offsets", Query{Kind: "retention", Output: "milestones", IdentifiedBy: "user", Offsets: 30}, "milestones"},
		{"retention metrics", Query{Kind: "retention", Output: "grid", Metrics: count()}, "metrics"},
		{"retention dimensions", Query{Kind: "retention", Output: "grid", Dimensions: []string{"path"}}, "dimensions"},
		{"retention series", Query{Kind: "retention", Output: "grid", Series: "path"}, "series"},
		{"retention series_limit", Query{Kind: "retention", Output: "grid", SeriesLimit: 3}, "series_limit"},
		{"retention compare", Query{Kind: "retention", Output: "grid", Compare: "previous"}, "compare"},
		{"retention limit", Query{Kind: "retention", Output: "grid", Limit: 3}, "limit"},
		{"retention other", Query{Kind: "retention", Output: "grid", Other: true}, "other"},
		{"retention order", Query{Kind: "retention", Output: "grid", Order: "day"}, "order"},
		{"retention where", Query{Kind: "retention", Output: "grid", Where: []Filter{{Column: "bucket", Op: "is_set"}}}, "where[0].column"},
		{"output on a stat", Query{Kind: "stat", Table: "events", Metrics: count(), Output: "grid"}, "output"},
		{"identified_by on a trend", Query{Kind: "trend", Table: "events", Metrics: count(), IdentifiedBy: "user"}, "identified_by"},
		{"offsets on a breakdown", Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"path"}, Offsets: 3}, "offsets"},
		{"milestones on a stat", Query{Kind: "stat", Table: "events", Metrics: count(), Milestones: []int{1}}, "milestones"},
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
	for _, c := range []struct {
		m    Metric
		want string
	}{
		{Metric{Op: "count"}, "count"},
		{Metric{Op: "users"}, "users"},
		{Metric{Op: "sum", Column: "value"}, "sum_value"},
		{Metric{Op: "max", Column: "display_width"}, "max_display_width"},
		{Metric{Op: "daily_avg", Of: "groups"}, "daily_avg_groups"},
		{Metric{Op: "count", As: "n"}, "n"},
		{Metric{Op: "bounce_rate"}, "bounce_rate"},
		{Metric{Op: "avg", Column: "duration_sec"}, "avg_duration_sec"},
		{Metric{Op: "percentile", Q: 0.75}, "p75"},
		{Metric{Op: "percentile", Q: 0.5}, "p50"},
		{Metric{Op: "percentile", Q: 0.05}, "p5"},
		{Metric{Op: "percentile", Q: 0.999}, "p99.9"},
		{Metric{Op: "percentile", Q: 0.001}, "p0.1"},
		{Metric{Op: "percentile", Q: 0.1}, "p10"},
		{Metric{Op: "percentile", Q: 0.9, As: "slow"}, "slow"},
		{Metric{Op: "mean"}, "mean"},
		{Metric{Op: "weight"}, "weight"},
		{Metric{Op: "share", Of: "weight"}, "share_weight"},
		{Metric{Op: "share", Of: "count"}, "share_count"},
		{Metric{Op: "avg", Column: "value", Round: ptr(2), Where: []Filter{{Column: "path", Op: "is_set"}}}, "avg_value"},
	} {
		if got := c.m.Name(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.m, got, c.want)
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
		{"retention grid", Query{Kind: "retention", Output: "grid"},
			[]string{"cohort", "identified_by", "day", "size", "returned", "retention"}},
		{"retention curve", Query{Kind: "retention", Output: "curve", IdentifiedBy: "user"}, []string{"day", "retention"}},
		{"retention milestones", Query{Kind: "retention", Output: "milestones", IdentifiedBy: "user"}, []string{"d7", "d30", "d45"}},
		{"retention own milestones", Query{Kind: "retention", Output: "milestones", IdentifiedBy: "user", Milestones: []int{14, 0}},
			[]string{"d14", "d0"}},
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
		{"ratings", Query{Kind: "breakdown", Table: "measures", Metrics: []Metric{{Op: "share", Of: "weight"}},
			Dimensions: []string{"vital", "vital_rating"}}, []string{"text", "text", "number"}},
		{"breakdown other", Query{Kind: "breakdown", Metrics: count(), Dimensions: []string{"day", "consent"}, Other: true},
			[]string{"text", "text", "number"}},
		{"retention grid", Query{Kind: "retention", Output: "grid"},
			[]string{"day", "text", "number", "number", "number", "number"}},
		{"retention curve", Query{Kind: "retention", Output: "curve", IdentifiedBy: "install"}, []string{"number", "number"}},
		{"retention milestones", Query{Kind: "retention", Output: "milestones", IdentifiedBy: "install", Milestones: []int{1, 2}},
			[]string{"number", "number"}},
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

// not_prefix, like contains and prefix, is for text columns.
func TestNotPrefixIsText(t *testing.T) {
	_, err := compileFilters([]Filter{{Column: "day", Op: "not_prefix", Values: []string{"2026"}}}, "where")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "where[0].op") {
		t.Errorf("err = %v, want ErrInvalid naming where[0].op", err)
	}
}
