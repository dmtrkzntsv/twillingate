package native

import (
	"errors"
	"strconv"
	"strings"
)

// ErrInvalid is wrapped by every refusal of a malformed Query; the message
// names the offending field.
var ErrInvalid = errors.New("native: invalid query")

// ErrTooLarge is wrapped by the refusal of a query whose aggregation needs
// more memory than Options.MaxBytes allows.
var ErrTooLarge = errors.New("native: the query needs more memory than allowed; narrow the range or add filters")

// Query is a structured query over one events table. Kind picks the shape:
// a stat is one row of metrics, a trend one metric per day (optionally split
// by Series, or up to five metrics as its series; Fill gives it every
// day), a breakdown the metrics grouped by 1-3 Dimensions, a
// retention its cohorts' days back (Output, IdentifiedBy, Offsets,
// Milestones), an attributes the attribute keys and values its rows carry
// (Output, Reserved, Keep).
type Query struct {
	Kind        string   `json:"kind" jsonschema:"paths: the flows from step to step of the sessions, each session's views (or product events) in time order, a row per flow, source, target and value the sessions, for a sankey (dimensions: the column a step reads, path or event_name by default; depth; limit, default 50); funnel: the actors (users, groups) at each of its steps, taken in the range in any order or in sequence (steps, by, sequence; no metrics); stat: one row of metrics (with compare previous, a previous_<name> column each); trend: one metric per day, optionally split by series, or 2 to 5 metrics per day as its series (day, series, value); breakdown: the metrics grouped by 1 to 3 dimensions; retention: the cohorts of users and installs first active in the range, and their days back (output, identified_by, offsets, milestones; no metrics); attributes: every attribute key and value the product or measures rows carry, with their metrics (output values; dimensions day and event_name, reserved), or every key with its events and distinct values (output keys; limit, keep; no metrics)"`
	Table       string   `json:"table,omitempty" jsonschema:"the rows read: events (every family), views, product or measures; or sessions, one row per session of views; or identities, one row per user or group first seen in the range (rules apply to neither); a retention reads events (absent or events); an attributes product or measures, its keys also events (product and measures both: the default)"`
	Metrics     []Metric `json:"metrics,omitempty" jsonschema:"the aggregates, each an output column; a trend takes 1 to 5, exactly 1 when split by series; with more than 1 its answer is long, day, series (each metric's name) and value, a number, so none is a min or max of a day or time; a retention and an attributes' keys none; an attributes' values count, actors, users, groups and actors_or_null"`
	Where       []Filter `json:"where,omitempty" jsonschema:"filters, all of which a row must satisfy"`
	Dimensions  []string `json:"dimensions,omitempty" jsonschema:"breakdown only, 1 to 3: columns to group by, each one a filter's column may name on the table (path, country, event_name, attr_<key>, …; resolution on events and views only, vital and vital_rating on measures only, the other computed ones on events, views and product), but duration_sec and views on sessions and views and events on identities; attributes only: day, event_name or both, in that order, before its attribute and value"`
	Series      string   `json:"series,omitempty" jsonschema:"trend only, optional: a column to split each day by, one a filter's column may name on the table except day (resolution on events and views only, vital and vital_rating on measures only, the other computed ones on events, views and product), and except duration_sec and views on sessions and views and events on identities"`
	Fill        string   `json:"fill,omitempty" jsonschema:"trend only: zero gives every day of the range a row, for every kept series or metric: a counted metric (count, actors, users, groups, bounces, weight, daily_sum, daily_max, daily_avg, days) reads 0 where it is empty, the others stay empty"`
	Define      []Define `json:"define,omitempty" jsonschema:"stat, trend and breakdown only: up to 5 text columns the query defines, each def_<name>, reading per row the label of its first case whose filters the row passes (else its else, or NULL): an event group (Apple Pay setup = event_name prefix import_shortcut_ or import_setup_), a segment by host or attribute; dimensions, series and filters name it as any text column"`
	Shape       string   `json:"shape,omitempty" jsonschema:"stat and breakdown only: wide (the default), a column per metric, or long, a row per metric: the dimensions, metric (its name) and value (and previous_value with compare previous), so metrics side by side draw as a bar's x or series; every metric a number"`
	Interval    string   `json:"interval,omitempty" jsonschema:"trend only: day (the default), week or month: one row per week (from Monday) or calendar month instead of per day, day reading the bucket's first day (the first and last buckets may hold only part of theirs, the range's); a distinct count counts over the bucket, and daily_sum, daily_max, daily_avg and days, which need the days, are refused"`
	SeriesLimit int      `json:"series_limit,omitempty" jsonschema:"a trend with a series only: how many series, 1 to 50, the top ones by the metric over the range (default 5)"`
	Compare     string   `json:"compare,omitempty" jsonschema:"stat only: previous adds each metric over as many days ending the day before from"`
	Limit       int      `json:"limit,omitempty" jsonschema:"breakdown and attributes only: how many groups, 1 to 1000 (default 20; an attributes' values all of them, its keys 50)"`
	Other       bool     `json:"other,omitempty" jsonschema:"breakdown only: fold the groups past limit into one row whose dimensions read Other, when there are two or more: a single one is listed under its own name (limit 4 gives at most 5 rows)"`
	Order       string   `json:"order,omitempty" jsonschema:"breakdown and attributes values only: a metric name to rank groups by, descending, or one of its dimensions (an attributes' attribute and value among them) to sort them by, ascending, ties by the other dimensions ascending (default: the first metric; an attributes with day by day, the first metric descending, event_name, attribute and value, else by event_name, attribute, the first metric descending and value); limit keeps the first groups in that order"`
	// The funnel kind's fields.
	Steps    []Step `json:"steps,omitempty" jsonschema:"funnel only, 2 to 10: the steps in order, each {label, where}; the answer is a row per step, step (its label) and value, the actors (users, groups: by) that took it and every step before it"`
	By       string `json:"by,omitempty" jsonschema:"funnel only: actor (the default), user or group: whose steps are counted"`
	Sequence bool   `json:"sequence,omitempty" jsonschema:"funnel only: true counts the steps taken one after another in time (an event may take several at once); false, the default, taken any time in the range, in any order"`
	// The paths kind's.
	Depth int `json:"depth,omitempty" jsonschema:"paths only, 2 to 6 (default 3): the steps of each session read, a run of one value one step"`
	// The retention kind's fields.
	Output       string `json:"output,omitempty" jsonschema:"retention and attributes only, required. An attributes: values (a row per dimensions, attribute and value) or keys (attribute, events, values, keys_total: a row per attribute, its events and distinct values, keys_total the number of attributes; the limit busiest, by events, then those keep names). A retention: grid (cohort, identified_by, offset, size, returned, retention: a row per cohort, identity kind and day back, offset the days since the cohort day), curve (offset, retention: one identity kind's cohorts pooled per day back) or milestones (one row, a d<n> column per milestone: the curve's retention that day back)"`
	IdentifiedBy string `json:"identified_by,omitempty" jsonschema:"retention only: install or user, the cohorts a curve or milestones pools (required there); on a grid optional, its rows of that kind alone (without it, both)"`
	Offsets      int    `json:"offsets,omitempty" jsonschema:"retention only: the days back counted, 1 to 90 (default 45)"`
	Milestones   []int  `json:"milestones,omitempty" jsonschema:"retention milestones only: 1 to 6 distinct days back, each 0 to offsets (default 7, 30 and 45)"`
	// The attributes kind's.
	Reserved bool     `json:"reserved,omitempty" jsonschema:"attributes values only: also the reserved keys, each read from its column (empty read as NULL) under its own name, as $platform or $country"`
	Keep     []string `json:"keep,omitempty" jsonschema:"attributes keys only: up to 1000 attributes (the key without attr_, spelled exactly as the answer spells it) listed past the limit too, when the range has events carrying them"`
}

// The retention defaults: the days back counted, the milestones read.
var (
	defaultOffsets    = 45
	defaultMilestones = []int{7, 30, 45}
)

// The retention bounds: the days back, the milestones.
const (
	maxOffsets    = 90
	maxMilestones = 6
)

// offsets is a retention's days back, its default when unset.
func (q Query) offsets() int {
	if q.Offsets == 0 {
		return defaultOffsets
	}
	return q.Offsets
}

// milestones is a retention's milestones, its default when unset.
func (q Query) milestones() []int {
	if len(q.Milestones) == 0 {
		return defaultMilestones
	}
	return q.Milestones
}

// Metric is one aggregate.
type Metric struct {
	Op     string   `json:"op" jsonschema:"latest: the value of column (any column a filter names but bucket, attr_<key> and def_ ones included, identity ones aside) at the latest row by ts, of the group (on the day, in a trend), empty over none, its type the column's: an instance's version as last seen; not on sessions or identities, nor in a ratio's side. count: rows; actors, users, groups: distinct actor_id, user_id, group_id; sum, avg, min, max: of column; daily_sum, daily_max, daily_avg: the per-day distinct count of of, summed, maxed or averaged over the days with rows; days: the distinct days with rows (0 over none; with a where, of the rows passing it), as the active days of the built-in top users and groups. measures alone takes percentile (the q quantile of value, a sample weighing 1 / sample_rate, read from its log-scale bucket: within about 2%), mean (value weighted so) and weight (the samples' 1 / sample_rate summed). ratio: num over den, two metrics of the table each with its own where, as a DOUBLE; empty when den is 0 or empty or num is empty, but a counted num (count, actors, users, groups, bounces, weight, daily_*, days) over no rows reads 0 when den is above 0; an Other row's both sides over its folded groups; approximate when either side is. share (trend and breakdown only): the group's of (count, or on measures weight) over its partition's: in a breakdown the groups of the same first dimension (all with one dimension; an Other row's is all of them), in a trend the day's; empty over none. sessions takes count (sessions), actors, share, ratio, sum, avg, min and max, and its own bounces (sessions of 1 view) and bounce_rate (bounces / sessions, empty over none). identities takes count, share, ratio, sum, avg, min and max. attributes takes count, actors, users and groups (group_id's empty value left out) and its own actors_or_null (distinct actor_id, NULL counting as one)"`
	Q      float64  `json:"q,omitempty" jsonschema:"percentile only, above 0 and below 1: the quantile (0.75 for p75)"`
	Column string   `json:"column,omitempty" jsonschema:"latest: any column a filter names but bucket, whose value it reads. sum, avg, min and max: the numeric column, value, sample_rate, display_width or display_height; on sessions duration_sec or views; on identities views or events. min and max also take day (a YYYY-MM-DD day) and ts or received_at (a UTC time, as 2026-09-01T08:00:05Z), on sessions day, started_at or ended_at; not on identities, nor in a ratio's side"`
	Of     string   `json:"of,omitempty" jsonschema:"daily_sum, daily_max and daily_avg take actors, users or groups (what is counted distinct per day); share takes count or, on measures, weight"`
	As     string   `json:"as,omitempty" jsonschema:"the output column's name (default: op, or op_column, or op_of, as share_weight; a percentile's p and 100 q, as p75 or p99.9)"`
	Round  *int     `json:"round,omitempty" jsonschema:"optional, 0 to 6: decimal places to round the value to, half away from zero (an integer stays as it is)"`
	Where  []Filter `json:"where,omitempty" jsonschema:"optional: filters, as the query's where, that a row must also satisfy to count toward this metric only; a group still exists from the query's where, so its metric may be 0 (count, distinct counts, bounces) or empty (the others)"`
	Num    *Metric  `json:"num,omitempty" jsonschema:"ratio only, required: the numerator, a metric of the table with its own optional where, without as, round, share or ratio"`
	Den    *Metric  `json:"den,omitempty" jsonschema:"ratio only, required: the denominator, as num"`
}

// maxRound bounds a metric's round.
const maxRound = 6

// Filter keeps the rows whose Column satisfies Op against Values.
type Filter struct {
	Column string   `json:"column,omitempty" jsonschema:"required but for any_set, any_of, ever and never: one of project_id, family, event_name, day, kind, actor_id, actor_kind, user_id, group_id, session_id, install_id, user_name, group_name, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, measure, bucket (measures only), or attr_<key> for an attribute; or computed, text: resolution (display_width x display_height, as 1920x1080, where both are above 0; events and views only), device_label (device_model, else device, where either is not empty), platform_version (platform, a space, app_version, where app_version is not empty), consent_state (given, none or unknown, from consent), hour (the UTC hour of ts, 00 to 23) and weekday (its UTC weekday, 1 Mon to 7 Sun; labels shows them as Mon to Sun) on events, views, product and measures, user_status and group_status (new on the day the user_id or group_id was first seen, as identities has it, returning on a later day, NULL without one), user_label and group_label (the identity's name, as identities has it, else the id; NULL without one), these seven on events, views and product only; computed, a day: user_first_seen and group_first_seen (the identity's first-seen day, as identities has it; NULL without one or when identities lacks it), on events, views and product only; on measures only, vital (LCP, INP, CLS, FCP or TTFB for the event_name $lcp, $inp, $cls, $fcp or $ttfb, else NULL) and vital_rating (good, needs improvement or poor: the sample's bucket value against the vital's thresholds, LCP 2500 and 4000, INP 200 and 500, CLS 0.1 and 0.25, FCP 1800 and 3000, TTFB 800 and 1800; NULL for the other measures). On sessions instead: project_id, day, kind, actor_id, actor_kind, session_id, entry_host, entry_path, exit_path, referrer_source, utm_source, utm_medium, utm_campaign, and the numbers duration_sec and views. On identities instead: kind (user or group), id, name, day (first_seen's day), and the numbers views and events"`
	Op     string   `json:"op" jsonschema:"eq, ne, contains, prefix, not_prefix (text that does not start with the value), gt, gte, lt, lte take 1 value; in, not_in 1 or more; is_set none (not NULL, and text not empty); any_set none, and columns instead of column (at least one of them is set, as is_set says); any_of none, and any instead of column (at least one of its filters passes); ever and never none, and where (with by and over) instead of column: the rows of the actors (users, groups) with at least one event passing where, or of those with none (a row without the id passes neither). gt/gte/lt/lte take an integer, or YYYY-MM-DD on day, user_first_seen and group_first_seen, and only on numeric columns and those days. NULL fails every op, ne and not_in included"`
	Values []string `json:"values,omitempty" jsonschema:"the values compared against, as text"`
	// Columns are any_set's columns, in place of Column.
	Columns []string `json:"columns,omitempty" jsonschema:"any_set only, in place of column: 2 to 5 columns, of which a row passes when at least one is set (not NULL, and text not empty)"`
	// Any are any_of's filters, in place of Column.
	Any []Filter `json:"any,omitempty" jsonschema:"any_of only, in place of column: 2 to 10 filters, none of them an any_of, of which a row passes when at least one does (an OR: event_name prefix import_ or event_name in a list)"`
	// Where, By and Over are ever's and never's.
	Where []Filter `json:"where,omitempty" jsonschema:"ever and never only, in place of column: 1 to 10 filters over the project's events (every family), all of which one of the actor's events passes"`
	By    string   `json:"by,omitempty" jsonschema:"ever and never only: actor (the default), user or group: whose events count, and whose rows pass"`
	Over  string   `json:"over,omitempty" jsonschema:"ever and never only: all (the default), every day up to the query's to, or range, its days only"`
}

// maxAnySet bounds any_set's columns, maxAnyOf any_of's filters.
const (
	maxAnySet = 5
	maxAnyOf  = 10
)

// Name is the metric's output column: As, else derived from the op.
func (m Metric) Name() string {
	switch {
	case m.As != "":
		return m.As
	case m.Column != "":
		return m.Op + "_" + m.Column
	case m.Of != "":
		return m.Op + "_" + m.Of
	case m.Op == "percentile":
		return "p" + percent(m.Q)
	}
	return m.Op
}

// percent is q (0 < q < 1) times 100 in decimal, shifted as text so no
// float error shows: 0.75 is 75, 0.999 is 99.9, 0.05 is 5.
func percent(q float64) string {
	s := strconv.FormatFloat(q, 'f', -1, 64)
	frac, ok := strings.CutPrefix(s, "0.")
	if !ok {
		return strconv.FormatFloat(q*100, 'f', -1, 64)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	whole := strings.TrimLeft(frac[:2], "0")
	if whole == "" {
		whole = "0"
	}
	if rest := frac[2:]; rest != "" {
		return whole + "." + rest
	}
	return whole
}

// Columns are the result's column names, in order, as Answer returns
// them for q, read from q alone: a stat's metrics, then with compare
// previous a previous_<name> each; a trend's day, series when split, and
// its metric, or with several metrics day, series and value; a
// breakdown's dimensions, then its metrics; a retention grid's cohort,
// identified_by, offset, size, returned and retention, a curve's offset
// and retention, milestones' d<n> each; an attributes' dimensions,
// attribute and value, then its metrics (its keys' attribute, events,
// values and keys_total). It does not validate q.
func (q Query) Columns() []string {
	outs := q.outputs()
	cols := make([]string, len(outs))
	for i, o := range outs {
		cols[i] = o.name
	}
	return cols
}

// ColumnTypes are the types of Columns' columns, in the same order, read
// from q alone: number, day or text, the type every value of the column
// has (an empty value, NULL, aside). Metrics, a stat's previous_<name>
// (a min or max of day day, of a time text), a trend's value and the
// numeric columns (project_id, display_width, display_height, consent,
// bucket) are number; day, user_first_seen and group_first_seen are day;
// a trend's series and every other column, attr_<key> included, text. A
// breakdown with other has its dimensions text, its Other row reading
// "Other" in each. A retention's cohort is day, identified_by text, the
// others number. An attributes' attribute and value are text (its keys'
// attribute text, the others number). It does not validate q.
func (q Query) ColumnTypes() []string {
	outs := q.outputs()
	types := make([]string, len(outs))
	for i, o := range outs {
		types[i] = o.typ
	}
	return types
}

// output is one of a query's result columns: its name (Columns') and its
// type (ColumnTypes').
type output struct{ name, typ string }

// outputs are q's result columns, which Columns and ColumnTypes read.
func (q Query) outputs() []output {
	var out []output
	add := func(typ string, names ...string) {
		for _, n := range names {
			out = append(out, output{n, typ})
		}
	}
	switch q.Kind {
	case "paths":
		add("text", "source", "target")
		add("number", "value")
		return out
	case "funnel":
		add("text", "step")
		add("number", "value")
		return out
	case "retention":
		switch q.Output {
		case "grid":
			add("day", "cohort")
			add("text", "identified_by")
			add("number", "offset", "size", "returned", "retention")
		case "curve":
			add("number", "offset", "retention")
		default:
			for _, m := range q.milestones() {
				add("number", "d"+strconv.Itoa(m))
			}
		}
		return out
	case "trend":
		add("day", "day")
		if q.Series != "" || q.long() {
			add("text", "series")
		}
		if q.long() {
			add("number", "value")
			return out
		}
	case "attributes":
		if q.Output == "keys" {
			add("text", "attribute")
			add("number", "events", "values", "keys_total")
			return out
		}
		for _, d := range q.Dimensions {
			add(columnType(d), d)
		}
		add("text", "attribute", "value")
	case "breakdown":
		for _, d := range q.Dimensions {
			if q.Other {
				add("text", d) // its Other row reads "Other"
			} else {
				add(columnType(d), d)
			}
		}
	}
	if q.longShape() { // longColumns, after the dimensions
		add("text", "metric")
		add("number", "value")
		if q.Compare == "previous" {
			add("number", "previous_value")
		}
		return out
	}
	for _, m := range q.Metrics {
		add(m.valueType(), m.Name())
	}
	if q.Kind == "stat" && q.Compare == "previous" {
		for _, m := range q.Metrics {
			add(m.valueType(), "previous_"+m.Name())
		}
	}
	return out
}

// long says whether q is a trend of several metrics, whose answer is
// long: day, series (the metric's name) and value.
func (q Query) long() bool { return q.Kind == "trend" && len(q.Metrics) > 1 }

// Intervals are a trend's buckets.
var Intervals = []string{"day", "week", "month"}

// bucketed says whether q is a trend by week or month.
func (q Query) bucketed() bool {
	return q.Kind == "trend" && (q.Interval == "week" || q.Interval == "month")
}

// dailyOps are the metrics read per day, which a trend by week or month
// has no days for.
var dailyOps = map[string]bool{"daily_sum": true, "daily_max": true, "daily_avg": true, "days": true}

// notDaily refuses a metric (or a ratio's side) read per day, under p.
func (m Metric) notDaily(p, interval string) error {
	if dailyOps[m.Op] {
		return invalid(p+".op", "%s needs days; a trend by %s has none (count actors, users or groups over the %s instead)", m.Op, interval, interval)
	}
	if m.Num != nil {
		if err := m.Num.notDaily(p+".num", interval); err != nil {
			return err
		}
	}
	if m.Den != nil {
		return m.Den.notDaily(p+".den", interval)
	}
	return nil
}

// valueType is the type of m's values, as ColumnTypes names it: a min
// or max of day is day, of a time text, every other metric number.
func (m Metric) valueType() string {
	if m.Op == "latest" {
		return columnType(m.Column)
	}
	switch boundForms[m.Column] {
	case formDay:
		return "day"
	case formTime:
		return "text"
	}
	return "number"
}

// maxLimit bounds a breakdown's limit, maxSeries a trend's series_limit,
// maxTrendMetrics an unsplit trend's metrics.
const (
	maxLimit        = 1000
	maxSeries       = 50
	maxTrendMetrics = 5
)

// The closed vocabularies of a Query, which a schema describing it lists.
var (
	Kinds = []string{"stat", "trend", "breakdown", "retention", "attributes", "funnel", "paths"}
	// RetentionOutputs are a retention's outputs, IdentifiedBy what its
	// curve and milestones pool.
	RetentionOutputs = []string{"grid", "curve", "milestones"}
	IdentifiedBy     = []string{"install", "user"}
	// AttributeOutputs are an attributes' outputs; AttributeTables the
	// tables it reads; AttributeDimensions its dimensions, in their order;
	// AttributeOps its metrics.
	AttributeOutputs    = []string{"values", "keys"}
	AttributeTables     = []string{"product", "measures"}
	AttributeDimensions = []string{"day", "event_name"}
	AttributeOps        = []string{"count", "actors", "users", "groups", "actors_or_null"}
	Tables              = []string{"events", "views", "product", "measures", "sessions", "identities"}
	MetricOps           = []string{"count", "actors", "users", "groups", "sum", "avg", "min", "max", "daily_sum", "daily_max", "daily_avg", "days", "bounces", "bounce_rate", "percentile", "mean", "weight", "share", "ratio", "actors_or_null", "latest"}
	ValueColumns        = []string{"value", "sample_rate", "display_width", "display_height"}
	// SessionValueColumns are the columns sum, avg, min and max take on
	// sessions (ValueColumns on the other tables).
	SessionValueColumns = []string{"duration_sec", "views"}
	// IdentityValueColumns are the columns sum, avg, min and max take on
	// identities.
	IdentityValueColumns = []string{"views", "events"}
	// BoundColumns are the columns min and max alone take beside
	// ValueColumns on the event tables, SessionBoundColumns beside
	// SessionValueColumns on sessions: a day, or a time.
	BoundColumns        = []string{"day", "ts", "received_at"}
	SessionBoundColumns = []string{"day", "started_at", "ended_at"}
	OfColumns           = []string{"actors", "users", "groups"}
	// ShareOfColumns are what share takes as of (weight on measures only).
	ShareOfColumns = []string{"count", "weight"}
	FilterOps      = []string{"eq", "ne", "in", "not_in", "contains", "prefix", "not_prefix", "gt", "gte", "lt", "lte", "is_set", "any_set", "any_of", "ever", "never"}
)

var (
	metricOps  = setOf(MetricOps)
	valueCols  = setOf(ValueColumns)
	sessValue  = setOf(SessionValueColumns)
	idValue    = setOf(IdentityValueColumns)
	boundCols  = setOf(BoundColumns)
	sessBound  = setOf(SessionBoundColumns)
	idOps      = setOf([]string{"count", "share", "ratio", "sum", "avg", "min", "max"})
	ofCols     = setOf(OfColumns)
	filterOps  = setOf(FilterOps)
	sessionOps = setOf([]string{"count", "actors", "share", "ratio", "sum", "avg", "min", "max", "bounces", "bounce_rate"})
	bounceOps  = setOf([]string{"bounces", "bounce_rate"})
	measureOps = setOf([]string{"percentile", "mean", "weight"})
	orderedOps = map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true}
)

// A value's form: a number, a day (days since 1970-01-01) or a time
// (unix microseconds, UTC).
const (
	formNumber = iota
	formDay
	formTime
)

// boundForms are the forms of the bound columns' values (BoundColumns,
// SessionBoundColumns), which min and max keep; every other column's is
// a number.
var boundForms = map[string]int{"day": formDay, "ts": formTime, "received_at": formTime, "started_at": formTime, "ended_at": formTime}

func setOf(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
