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
		"trend fill":      {Kind: "trend", Table: "product", Metrics: count(), Series: "path", Fill: "zero"},
		"trend metrics": {Kind: "trend", Table: "events", Fill: "zero", Metrics: []Metric{{Op: "count"}, {Op: "actors"}, {Op: "users", As: "u"},
			{Op: "ratio", Num: &Metric{Op: "count"}, Den: &Metric{Op: "actors"}}, {Op: "daily_max", Of: "users"}}},
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
		"labels": {Kind: "breakdown", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "days"}, {Op: "days", As: "d", Where: []Filter{{Column: "group_first_seen", Op: "is_set"}}}},
			Dimensions: []string{"user_id", "user_label", "user_first_seen"}, Order: "user_first_seen",
			Where: []Filter{{Column: "user_first_seen", Op: "gte", Values: []string{"2026-01-02"}}, {Column: "group_label", Op: "contains", Values: []string{"a"}}}},
		"labels trend": {Kind: "trend", Table: "product", Metrics: []Metric{{Op: "days"}}, Series: "group_label",
			Where: []Filter{{Column: "group_first_seen", Op: "lt", Values: []string{"2026-01-02"}}}},
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
		"any_set": {Kind: "breakdown", Table: "views", Dimensions: []string{"utm_source"}, Metrics: []Metric{{Op: "count",
			Where: []Filter{{Op: "any_set", Columns: []string{"display_width", "attr_a", "user_first_seen"}}}}},
			Where: []Filter{{Op: "any_set", Columns: []string{"utm_source", "utm_medium", "utm_campaign"}}}},
		"attributes": {Kind: "attributes", Table: "product", Output: "values",
			Metrics: []Metric{{Op: "count"}, {Op: "actors_or_null", As: "users"}, {Op: "groups"}}},
		"attributes per day": {Kind: "attributes", Table: "measures", Output: "values", Dimensions: []string{"day", "event_name"},
			Reserved: true, Metrics: []Metric{{Op: "count"}, {Op: "actors"}, {Op: "users"}, {Op: "groups"}},
			Where: []Filter{{Column: "event_name", Op: "eq", Values: []string{"x"}}}, Order: "value", Limit: 1000},
		"attributes by event": {Kind: "attributes", Table: "product", Output: "values", Dimensions: []string{"event_name"},
			Metrics: count(), Order: "attribute"},
		"attributes by day": {Kind: "attributes", Table: "product", Output: "values", Dimensions: []string{"day"},
			Metrics: []Metric{{Op: "users", As: "n"}}, Order: "n"},
		"attribute keys":        {Kind: "attributes", Output: "keys"},
		"attribute keys events": {Kind: "attributes", Table: "events", Output: "keys", Limit: 1000, Keep: []string{"plan", "Plan"}},
		"attribute keys product": {Kind: "attributes", Table: "product", Output: "keys", Limit: 1,
			Where: []Filter{{Column: "event_name", Op: "eq", Values: []string{"x"}}}},
		"attribute keys measures": {Kind: "attributes", Table: "measures", Output: "keys", Keep: make([]string, MaxKeep)},
		"ratio stat": {Kind: "stat", Table: "events", Compare: "previous", Metrics: []Metric{{Op: "ratio", Round: ptr(1), As: "value",
			Num: &Metric{Op: "count"}, Den: &Metric{Op: "users"}}}},
		"ratio filtered sides": {Kind: "breakdown", Table: "views", Dimensions: []string{"country"}, Other: true, Order: "ratio",
			Metrics: []Metric{{Op: "ratio",
				Num: &Metric{Op: "daily_sum", Of: "actors", Where: []Filter{{Column: "consent", Op: "eq", Values: []string{"1"}}}},
				Den: &Metric{Op: "daily_sum", Of: "actors", Where: []Filter{{Column: "consent", Op: "is_set"}}}}}},
		"ratio trend":    {Kind: "trend", Table: "measures", Series: "event_name", Metrics: []Metric{{Op: "ratio", Num: &Metric{Op: "weight"}, Den: &Metric{Op: "count"}}}},
		"ratio sessions": {Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "ratio", Num: &Metric{Op: "sum", Column: "duration_sec"}, Den: &Metric{Op: "bounces"}}}},
		"bounds": {Kind: "breakdown", Table: "events", Dimensions: []string{"user_id"}, Other: true, Metrics: []Metric{
			{Op: "max", Column: "day"}, {Op: "min", Column: "day", Where: []Filter{{Column: "family", Op: "eq", Values: []string{"product"}}}},
			{Op: "max", Column: "received_at"}, {Op: "min", Column: "ts"}}, Order: "max_day"},
		"bounds compare":        {Kind: "stat", Table: "views", Compare: "previous", Metrics: []Metric{{Op: "max", Column: "received_at"}}},
		"bounds trend":          {Kind: "trend", Table: "product", Metrics: []Metric{{Op: "min", Column: "ts"}}, Series: "event_name"},
		"session bounds":        {Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "min", Column: "started_at"}, {Op: "max", Column: "ended_at"}, {Op: "max", Column: "day"}}},
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
	attrs := func(edit func(q *Query)) Query {
		q := Query{Kind: "attributes", Table: "product", Output: "values", Metrics: count()}
		edit(&q)
		return q
	}
	keys := func(edit func(q *Query)) Query {
		q := Query{Kind: "attributes", Table: "events", Output: "keys"}
		edit(&q)
		return q
	}
	st := func(w ...Filter) Query { return Query{Kind: "stat", Table: "events", Metrics: count(), Where: w} }
	ratio := func(num, den *Metric) Query {
		return Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "ratio", Num: num, Den: den}}}
	}
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
		{"any_set one column", Query{Kind: "stat", Table: "views", Metrics: count(), Where: []Filter{{Op: "any_set", Columns: []string{"path"}}}}, "where[0].columns"},
		{"any_set six columns", Query{Kind: "stat", Table: "views", Metrics: count(), Where: []Filter{{Op: "any_set",
			Columns: []string{"path", "host", "os", "browser", "device", "country"}}}}, "where[0].columns"},
		{"any_set with column", Query{Kind: "stat", Table: "views", Metrics: count(), Where: []Filter{{Op: "any_set", Column: "path",
			Columns: []string{"path", "host"}}}}, "where[0].column"},
		{"any_set with values", Query{Kind: "stat", Table: "views", Metrics: count(), Where: []Filter{{Op: "any_set",
			Columns: []string{"path", "host"}, Values: []string{"x"}}}}, "where[0].values"},
		{"any_set unknown column", Query{Kind: "stat", Table: "sessions", Metrics: count(), Where: []Filter{{Op: "any_set",
			Columns: []string{"utm_source", "path"}}}}, "where[0].columns[1]"},
		{"columns without any_set", Query{Kind: "stat", Table: "views", Metrics: count(), Where: []Filter{{Op: "is_set", Column: "path",
			Columns: []string{"path", "host"}}}}, "where[0].columns"},
		{"any_set in a metric", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count", Where: []Filter{{Op: "any_set",
			Columns: []string{"path", "nope"}}}}}}, "metrics[0].where[0].columns[1]"},
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
		{"gt on a label", st(Filter{Column: "user_label", Op: "gt", Values: []string{"1"}}), "where[0].op"},
		{"first seen not a date", st(Filter{Column: "user_first_seen", Op: "gte", Values: []string{"5"}}), "where[0].values"},
		{"label on measures", Query{Kind: "breakdown", Table: "measures", Metrics: count(), Dimensions: []string{"user_label"}}, "dimensions[0]"},
		{"first seen on sessions", Query{Kind: "stat", Table: "sessions", Metrics: count(), Where: []Filter{{Column: "group_first_seen", Op: "is_set"}}}, "where[0].column"},
		{"label on identities", Query{Kind: "breakdown", Table: "identities", Metrics: count(), Dimensions: []string{"group_label"}}, "dimensions[0]"},
		{"days on sessions", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "days"}}}, "metrics[0].op"},
		{"days on identities", Query{Kind: "stat", Table: "identities", Metrics: []Metric{{Op: "days"}}}, "metrics[0].op"},
		{"days with a column", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "days", Column: "day"}}}, "metrics[0].column"},
		{"days with of", Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "days", Of: "users"}}}, "metrics[0].of"},
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
		{"trend six metrics", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "actors"}, {Op: "users"},
			{Op: "groups"}, {Op: "days"}, {Op: "sum", Column: "value"}}}, "metrics"},
		{"trend metrics with series", Query{Kind: "trend", Table: "events", Series: "path", Metrics: []Metric{{Op: "count"}, {Op: "actors"}}}, "metrics"},
		{"trend metrics with a day", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "max", Column: "day"}}}, "metrics[1].column"},
		{"trend metrics with a time", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "min", Column: "ts"}, {Op: "count"}}}, "metrics[0].column"},
		{"trend metrics dup names", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "actors", As: "count"}}}, "metrics[1].as"},
		{"fill on a breakdown", Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"path"}, Fill: "zero"}, "fill"},
		{"fill on a stat", Query{Kind: "stat", Table: "events", Metrics: count(), Fill: "zero"}, "fill"},
		{"fill bad", Query{Kind: "trend", Table: "events", Metrics: count(), Fill: "null"}, "fill"},
		{"fill on a retention", Query{Kind: "retention", Output: "grid", Fill: "zero"}, "fill"},
		{"fill on an attributes", attrs(func(q *Query) { q.Fill = "zero" }), "fill"},
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
		{"series_limit unsplit", Query{Kind: "trend", Table: "events", Metrics: count(), SeriesLimit: 3}, "series_limit"},
		{"series_limit on several metrics", Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "actors"}}, SeriesLimit: 3}, "series_limit"},
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
		{"attributes on events", attrs(func(q *Query) { q.Table = "events" }), "table"},
		{"attributes on views", attrs(func(q *Query) { q.Table = "views" }), "table"},
		{"attributes no output", attrs(func(q *Query) { q.Output = "" }), "output"},
		{"attributes keep", attrs(func(q *Query) { q.Keep = []string{"plan"} }), "keep"},
		{"keep on a breakdown", Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"path"}, Keep: []string{"a"}}, "keep"},
		{"keys on views", keys(func(q *Query) { q.Table = "views" }), "table"},
		{"keys metrics", keys(func(q *Query) { q.Metrics = count() }), "metrics"},
		{"keys dimensions", keys(func(q *Query) { q.Dimensions = []string{"day"} }), "dimensions"},
		{"keys reserved", keys(func(q *Query) { q.Reserved = true }), "reserved"},
		{"keys order", keys(func(q *Query) { q.Order = "events" }), "order"},
		{"keys limit", keys(func(q *Query) { q.Limit = 1001 }), "limit"},
		{"keys keep", keys(func(q *Query) { q.Keep = make([]string, MaxKeep+1) }), "keep"},
		{"keys series", keys(func(q *Query) { q.Series = "path" }), "series"},
		{"keys other", keys(func(q *Query) { q.Other = true }), "other"},
		{"keys where", keys(func(q *Query) { q.Where = []Filter{{Column: "duration_sec", Op: "is_set"}} }), "where[0].column"},
		{"attributes grid", attrs(func(q *Query) { q.Output = "grid" }), "output"},
		{"attributes dims out of order", attrs(func(q *Query) { q.Dimensions = []string{"event_name", "day"} }), "dimensions"},
		{"attributes dim path", attrs(func(q *Query) { q.Dimensions = []string{"path"} }), "dimensions[0]"},
		{"attributes dim repeated", attrs(func(q *Query) { q.Dimensions = []string{"day", "day"} }), "dimensions"},
		{"attributes no metrics", attrs(func(q *Query) { q.Metrics = nil }), "metrics"},
		{"attributes sum", attrs(func(q *Query) { q.Metrics = []Metric{{Op: "sum", Column: "value"}} }), "metrics[0].op"},
		{"attributes daily", attrs(func(q *Query) { q.Metrics = []Metric{{Op: "count"}, {Op: "daily_sum", Of: "actors"}} }), "metrics[1].op"},
		{"attributes metric where", attrs(func(q *Query) {
			q.Metrics = []Metric{{Op: "count", Where: []Filter{{Column: "path", Op: "is_set"}}}}
		}), "metrics[0].where"},
		{"attributes as attribute", attrs(func(q *Query) { q.Metrics = []Metric{{Op: "count", As: "attribute"}} }), "metrics[0].as"},
		{"attributes as value", attrs(func(q *Query) { q.Metrics = []Metric{{Op: "count", As: "value"}} }), "metrics[0].as"},
		{"attributes as day", attrs(func(q *Query) { q.Metrics = []Metric{{Op: "count", As: "day"}} }), "metrics[0].as"},
		{"attributes dup names", attrs(func(q *Query) { q.Metrics = []Metric{{Op: "actors"}, {Op: "actors_or_null", As: "actors"}} }), "metrics[1].as"},
		{"attributes where", attrs(func(q *Query) { q.Where = []Filter{{Column: "duration_sec", Op: "is_set"}} }), "where[0].column"},
		{"attributes order", attrs(func(q *Query) { q.Order = "day" }), "order"},
		{"attributes limit", attrs(func(q *Query) { q.Limit = 1001 }), "limit"},
		{"attributes series", attrs(func(q *Query) { q.Series = "path" }), "series"},
		{"attributes series_limit", attrs(func(q *Query) { q.SeriesLimit = 2 }), "series_limit"},
		{"attributes compare", attrs(func(q *Query) { q.Compare = "previous" }), "compare"},
		{"attributes other", attrs(func(q *Query) { q.Other = true }), "other"},
		{"sum of day", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "sum", Column: "day"}}}, "metrics[0].column"},
		{"avg of ts", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "avg", Column: "ts"}}}, "metrics[0].column"},
		{"max received_at on sessions", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "max", Column: "received_at"}}}, "metrics[0].column"},
		{"min started_at on events", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "min", Column: "started_at"}}}, "metrics[0].column"},
		{"sum of started_at", Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "sum", Column: "started_at"}}}, "metrics[0].column"},
		{"max day on identities", Query{Kind: "stat", Table: "identities", Metrics: []Metric{{Op: "max", Column: "day"}}}, "metrics[0].column"},
		{"max day in a side", ratio(&Metric{Op: "max", Column: "day"}, &Metric{Op: "count"}), "metrics[0].num.column"},
		{"ratio without num", ratio(nil, &Metric{Op: "count"}), "metrics[0].num"},
		{"ratio without den", ratio(&Metric{Op: "count"}, nil), "metrics[0].den"},
		{"nested ratio", ratio(&Metric{Op: "ratio", Num: &Metric{Op: "count"}, Den: &Metric{Op: "users"}}, &Metric{Op: "count"}), "metrics[0].num.op"},
		{"as in a side", ratio(&Metric{Op: "count"}, &Metric{Op: "users", As: "u"}), "metrics[0].den.as"},
		{"round in a side", ratio(&Metric{Op: "count", Round: ptr(1)}, &Metric{Op: "users"}), "metrics[0].num.round"},
		{"share in a side", ratio(&Metric{Op: "share", Of: "count"}, &Metric{Op: "count"}), "metrics[0].num.op"},
		{"bounces in a side on events", ratio(&Metric{Op: "count"}, &Metric{Op: "bounces"}), "metrics[0].den.op"},
		{"bad side column", ratio(&Metric{Op: "sum", Column: "path"}, &Metric{Op: "count"}), "metrics[0].num.column"},
		{"bad side where", ratio(&Metric{Op: "count", Where: []Filter{{Column: "nope", Op: "is_set"}}}, &Metric{Op: "count"}), "metrics[0].num.where[0].column"},
		{"num on count", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "count", Num: &Metric{Op: "count"}}}}, "metrics[0].num"},
		{"den on users", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "users", Den: &Metric{Op: "count"}}}}, "metrics[0].den"},
		{"ratio where", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "ratio", Num: &Metric{Op: "count"}, Den: &Metric{Op: "users"},
			Where: []Filter{{Column: "path", Op: "is_set"}}}}}, "metrics[0].where"},
		{"ratio column", Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "ratio", Column: "value", Num: &Metric{Op: "count"}, Den: &Metric{Op: "users"}}}}, "metrics[0].column"},
		{"ratio on identities side", Query{Kind: "stat", Table: "identities", Metrics: []Metric{{Op: "ratio", Num: &Metric{Op: "count"}, Den: &Metric{Op: "users"}}}}, "metrics[0].den.op"},
		{"ratio in attributes", attrs(func(q *Query) {
			q.Metrics = []Metric{{Op: "ratio", Num: &Metric{Op: "count"}, Den: &Metric{Op: "users"}}}
		}), "metrics[0].op"},
		{"attributes identified_by", attrs(func(q *Query) { q.IdentifiedBy = "user" }), "identified_by"},
		{"attributes offsets", attrs(func(q *Query) { q.Offsets = 3 }), "offsets"},
		{"attributes milestones", attrs(func(q *Query) { q.Milestones = []int{1} }), "milestones"},
		{"reserved on a breakdown", Query{Kind: "breakdown", Table: "product", Metrics: count(), Dimensions: []string{"path"}, Reserved: true}, "reserved"},
		{"reserved on a retention", Query{Kind: "retention", Output: "grid", Reserved: true}, "reserved"},
		{"actors_or_null on a breakdown", Query{Kind: "breakdown", Table: "product", Metrics: []Metric{{Op: "actors_or_null"}}, Dimensions: []string{"path"}}, "metrics[0].op"},
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
		{Metric{Op: "days"}, "days"},
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
		{Metric{Op: "ratio", Num: &Metric{Op: "count"}, Den: &Metric{Op: "users"}}, "ratio"},
		{Metric{Op: "ratio", As: "per_user", Num: &Metric{Op: "count"}, Den: &Metric{Op: "users"}}, "per_user"},
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
		{"trend metrics", Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "count", As: "Views"}, {Op: "actors", As: "Visitors"}}},
			[]string{"day", "series", "value"}},
		{"trend filled", Query{Kind: "trend", Table: "views", Fill: "zero", Metrics: []Metric{{Op: "count", As: "views"}}},
			[]string{"day", "views"}},
		{"breakdown", Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "actors"}, {Op: "count", As: "n"}}, Dimensions: []string{"country", "path"}},
			[]string{"country", "path", "actors", "n"}},
		{"attributes", Query{Kind: "attributes", Output: "values", Metrics: []Metric{{Op: "count"}, {Op: "actors_or_null", As: "users"}}},
			[]string{"attribute", "value", "count", "users"}},
		{"attributes per day", Query{Kind: "attributes", Output: "values", Dimensions: []string{"day", "event_name"}, Metrics: []Metric{{Op: "groups"}}},
			[]string{"day", "event_name", "attribute", "value", "groups"}},
		{"attribute keys", Query{Kind: "attributes", Output: "keys"}, []string{"attribute", "events", "values", "keys_total"}},
		{"retention grid", Query{Kind: "retention", Output: "grid"},
			[]string{"cohort", "identified_by", "offset", "size", "returned", "retention"}},
		{"retention curve", Query{Kind: "retention", Output: "curve", IdentifiedBy: "user"}, []string{"offset", "retention"}},
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
		{"labels", Query{Kind: "breakdown", Metrics: []Metric{{Op: "count"}, {Op: "days"}},
			Dimensions: []string{"user_id", "user_label", "user_first_seen"}}, []string{"text", "text", "day", "number", "number"}},
		{"labels other", Query{Kind: "breakdown", Metrics: count(), Other: true,
			Dimensions: []string{"group_label", "group_first_seen"}}, []string{"text", "text", "number"}},
		{"first seen series", Query{Kind: "trend", Metrics: count(), Series: "user_first_seen"}, []string{"day", "text", "number"}},
		{"ratings", Query{Kind: "breakdown", Table: "measures", Metrics: []Metric{{Op: "share", Of: "weight"}},
			Dimensions: []string{"vital", "vital_rating"}}, []string{"text", "text", "number"}},
		{"breakdown other", Query{Kind: "breakdown", Metrics: count(), Dimensions: []string{"day", "consent"}, Other: true},
			[]string{"text", "text", "number"}},
		{"attributes", Query{Kind: "attributes", Output: "values", Dimensions: []string{"day", "event_name"}, Metrics: []Metric{{Op: "count"}, {Op: "actors"}}},
			[]string{"day", "text", "text", "text", "number", "number"}},
		{"attribute keys", Query{Kind: "attributes", Output: "keys"}, []string{"text", "number", "number", "number"}},
		{"bounds", Query{Kind: "stat", Compare: "previous", Metrics: []Metric{{Op: "max", Column: "day"}, {Op: "max", Column: "received_at"},
			{Op: "min", Column: "ts"}, {Op: "max", Column: "value"}}},
			[]string{"day", "text", "text", "number", "day", "text", "text", "number"}},
		{"session bounds", Query{Kind: "breakdown", Table: "sessions", Dimensions: []string{"entry_path"}, Metrics: []Metric{
			{Op: "min", Column: "started_at"}, {Op: "max", Column: "ended_at"}, {Op: "min", Column: "day"}}},
			[]string{"text", "text", "text", "day"}},
		{"bounds trend", Query{Kind: "trend", Metrics: []Metric{{Op: "max", Column: "day"}}}, []string{"day", "day"}},
		{"trend metrics", Query{Kind: "trend", Fill: "zero", Metrics: []Metric{{Op: "count"}, {Op: "avg", Column: "value"}}},
			[]string{"day", "text", "number"}},
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
