package native

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
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
	Kind        string   `json:"kind" jsonschema:"funnel: the actors (users, groups) at each of its steps, taken in the range in any order or in sequence (steps, by, sequence; no metrics); stat: one row of metrics (with compare previous, a previous_<name> column each); trend: one metric per day, optionally split by series, or 2 to 5 metrics per day as its series (day, series, value); breakdown: the metrics grouped by 1 to 3 dimensions; retention: the cohorts of users and installs first active in the range, and their days back (output, identified_by, offsets, milestones; no metrics); attributes: every attribute key and value the product or measures rows carry, with their metrics (output values; dimensions day and event_name, reserved), or every key with its events and distinct values (output keys; limit, keep; no metrics)"`
	Table       string   `json:"table,omitempty" jsonschema:"the rows read: events (every family), views, product or measures; or sessions, one row per session of views; or identities, one row per user or group first seen in the range (rules apply to neither); a retention reads events (absent or events); an attributes product or measures, its keys also events (product and measures both: the default)"`
	Metrics     []Metric `json:"metrics,omitempty" jsonschema:"the aggregates, each an output column; a trend takes 1 to 5, exactly 1 when split by series; with more than 1 its answer is long, day, series (each metric's name) and value, a number, so none is a min or max of a day or time; a retention and an attributes' keys none; an attributes' values count, actors, users, groups and actors_or_null"`
	Where       []Filter `json:"where,omitempty" jsonschema:"filters, all of which a row must satisfy"`
	Dimensions  []string `json:"dimensions,omitempty" jsonschema:"breakdown only, 1 to 3: columns to group by, each one a filter's column may name on the table (path, country, event_name, attr_<key>, …; resolution on events and views only, vital and vital_rating on measures only, the other computed ones on events, views and product), but duration_sec and views on sessions and views and events on identities; attributes only: day, event_name or both, in that order, before its attribute and value"`
	Series      string   `json:"series,omitempty" jsonschema:"trend only, optional: a column to split each day by, one a filter's column may name on the table except day (resolution on events and views only, vital and vital_rating on measures only, the other computed ones on events, views and product), and except duration_sec and views on sessions and views and events on identities"`
	Fill        string   `json:"fill,omitempty" jsonschema:"trend only: zero gives every day of the range a row, for every kept series or metric: a counted metric (count, actors, users, groups, bounces, weight, daily_sum, daily_max, daily_avg, days) reads 0 where it is empty, the others stay empty"`
	Define      []Define `json:"define,omitempty" jsonschema:"stat, trend and breakdown only: up to 5 text columns the query defines, each def_<name>, reading per row the label of its first case whose filters the row passes (else its else, or NULL): an event group (Apple Pay setup = event_name prefix import_shortcut_ or import_setup_), a segment by host or attribute; dimensions, series and filters name it as any text column"`
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
	Op     string   `json:"op" jsonschema:"latest: the value of column (any column a filter names, attr_<key> and def_ ones included, identity ones aside) at the latest row by ts, of the group (on the day, in a trend), empty over none, its type the column's: an instance's version as last seen; not on sessions or identities, nor in a ratio's side. count: rows; actors, users, groups: distinct actor_id, user_id, group_id; sum, avg, min, max: of column; daily_sum, daily_max, daily_avg: the per-day distinct count of of, summed, maxed or averaged over the days with rows; days: the distinct days with rows (0 over none; with a where, of the rows passing it), as the active days of the built-in top users and groups. measures alone takes percentile (the q quantile of value, a sample weighing 1 / sample_rate, read from its log-scale bucket: within about 2%), mean (value weighted so) and weight (the samples' 1 / sample_rate summed). ratio: num over den, two metrics of the table each with its own where, as a DOUBLE; empty when den is 0 or empty or num is empty, but a counted num (count, actors, users, groups, bounces, weight, daily_*, days) over no rows reads 0 when den is above 0; an Other row's both sides over its folded groups; approximate when either side is. share (trend and breakdown only): the group's of (count, or on measures weight) over its partition's: in a breakdown the groups of the same first dimension (all with one dimension; an Other row's is all of them), in a trend the day's; empty over none. sessions takes count (sessions), actors, share, ratio, sum, avg, min and max, and its own bounces (sessions of 1 view) and bounce_rate (bounces / sessions, empty over none). identities takes count, share, ratio, sum, avg, min and max. attributes takes count, actors, users and groups (group_id's empty value left out) and its own actors_or_null (distinct actor_id, NULL counting as one)"`
	Q      float64  `json:"q,omitempty" jsonschema:"percentile only, above 0 and below 1: the quantile (0.75 for p75)"`
	Column string   `json:"column,omitempty" jsonschema:"latest: any column a filter names, whose value it reads. sum, avg, min and max: the numeric column, value, sample_rate, display_width or display_height; on sessions duration_sec or views; on identities views or events. min and max also take day (a YYYY-MM-DD day) and ts or received_at (a UTC time, as 2026-09-01T08:00:05Z), on sessions day, started_at or ended_at; not on identities, nor in a ratio's side"`
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
	Column string   `json:"column,omitempty" jsonschema:"required but for any_set, any_of, ever and never: one of project_id, family, event_name, day, kind, actor_id, actor_kind, user_id, group_id, session_id, install_id, user_name, group_name, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, measure, bucket (measures only), or attr_<key> for an attribute; or computed, text: resolution (display_width x display_height, as 1920x1080, where both are above 0; events and views only), device_label (device_model, else device, where either is not empty), platform_version (platform, a space, app_version, where app_version is not empty), consent_state (given, none or unknown, from consent), user_status and group_status (new on the day the user_id or group_id was first seen, as identities has it, returning on a later day, NULL without one), user_label and group_label (the identity's name, as identities has it, else the id; NULL without one), these seven on events, views and product only; computed, a day: user_first_seen and group_first_seen (the identity's first-seen day, as identities has it; NULL without one or when identities lacks it), on events, views and product only; on measures only, vital (LCP, INP, CLS, FCP or TTFB for the event_name $lcp, $inp, $cls, $fcp or $ttfb, else NULL) and vital_rating (good, needs improvement or poor: the sample's bucket value against the vital's thresholds, LCP 2500 and 4000, INP 200 and 500, CLS 0.1 and 0.25, FCP 1800 and 3000, TTFB 800 and 1800; NULL for the other measures). On sessions instead: project_id, day, kind, actor_id, actor_kind, session_id, entry_host, entry_path, exit_path, referrer_source, utm_source, utm_medium, utm_campaign, and the numbers duration_sec and views. On identities instead: kind (user or group), id, name, day (first_seen's day), and the numbers views and events"`
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
	var cols []string
	switch q.Kind {
	case "funnel":
		return []string{"step", "value"}
	case "retention":
		switch q.Output {
		case "grid":
			return []string{"cohort", "identified_by", "offset", "size", "returned", "retention"}
		case "curve":
			return []string{"offset", "retention"}
		}
		for _, m := range q.milestones() {
			cols = append(cols, "d"+strconv.Itoa(m))
		}
		return cols
	case "trend":
		cols = append(cols, "day")
		if q.Series != "" || q.long() {
			cols = append(cols, "series")
		}
		if q.long() {
			return append(cols, "value")
		}
	case "breakdown":
		cols = append(cols, q.Dimensions...)
	case "attributes":
		if q.Output == "keys" {
			return []string{"attribute", "events", "values", "keys_total"}
		}
		cols = append(cols, q.Dimensions...)
		cols = append(cols, "attribute", "value")
	}
	for _, m := range q.Metrics {
		cols = append(cols, m.Name())
	}
	if q.Kind == "stat" && q.Compare == "previous" {
		for _, m := range q.Metrics {
			cols = append(cols, "previous_"+m.Name())
		}
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
	var types []string
	switch q.Kind {
	case "funnel":
		return []string{"text", "number"}
	case "retention":
		for _, c := range q.Columns() {
			switch c {
			case "cohort":
				types = append(types, "day")
			case "identified_by":
				types = append(types, "text")
			default:
				types = append(types, "number")
			}
		}
		return types
	case "trend":
		types = append(types, "day")
		if q.Series != "" || q.long() {
			types = append(types, "text")
		}
		if q.long() {
			return append(types, "number")
		}
	case "attributes":
		if q.Output == "keys" {
			return []string{"text", "number", "number", "number"}
		}
		for _, d := range q.Dimensions {
			types = append(types, columnType(d))
		}
		types = append(types, "text", "text")
	case "breakdown":
		for _, d := range q.Dimensions {
			if q.Other {
				types = append(types, "text")
			} else {
				types = append(types, columnType(d))
			}
		}
	}
	for _, m := range q.Metrics {
		types = append(types, m.valueType())
	}
	if q.Kind == "stat" && q.Compare == "previous" {
		for _, m := range q.Metrics {
			types = append(types, m.valueType())
		}
	}
	return types
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

// columnType is a dimension column's type, as ColumnTypes names it.
func columnType(column string) string {
	switch {
	case column == "day", isFirstSeen(column):
		return "day"
	case column == "bucket", plainColumns[column]:
		return "number"
	}
	return "text"
}

// maxLimit bounds a breakdown's limit, maxSeries a trend's series_limit,
// maxTrendMetrics an unsplit trend's metrics.
const (
	maxLimit        = 1000
	maxSeries       = 50
	maxTrendMetrics = 5
)

// Columns are the non-attribute columns a dimension, series or filter on
// the event tables may name (bucket aside, which only measures has; the
// computed ones on the tables computedTables lists).
var Columns = []string{
	"project_id", "family", "event_name", "day", "kind", "actor_id", "actor_kind",
	"user_id", "group_id", "session_id", "install_id", "user_name", "group_name",
	"host", "path", "referrer_source", "utm_source", "utm_medium", "utm_campaign",
	"platform", "os", "os_version", "os_name", "browser", "browser_version",
	"browser_locale", "app_version", "app_locale", "device", "device_model",
	"display_width", "display_height", "country", "consent", "measure",
	"resolution", "device_label", "platform_version", "consent_state",
	"user_status", "group_status", "user_label", "group_label",
	"user_first_seen", "group_first_seen", "vital", "vital_rating",
}

// computedTables are the tables each computed column (computedColumns)
// is a column of.
var computedTables = map[string][]string{
	"resolution":       {"events", "views"},
	"device_label":     {"events", "views", "product"},
	"platform_version": {"events", "views", "product"},
	"consent_state":    {"events", "views", "product"},
	"user_status":      {"events", "views", "product"},
	"group_status":     {"events", "views", "product"},
	"user_label":       {"events", "views", "product"},
	"group_label":      {"events", "views", "product"},
	"user_first_seen":  {"events", "views", "product"},
	"group_first_seen": {"events", "views", "product"},
	"vital":            {"measures"},
	"vital_rating":     {"measures"},
}

// SessionColumns are the columns a dimension, series or filter on
// sessions may name: its text columns, day and project_id; and, filters
// only, the numbers duration_sec and views. started_at and ended_at are
// not.
var SessionColumns = []string{
	"project_id", "day", "kind", "actor_id", "actor_kind", "session_id",
	"entry_host", "entry_path", "exit_path", "referrer_source",
	"utm_source", "utm_medium", "utm_campaign", "duration_sec", "views",
}

// sessionColumnSet is SessionColumns as a set; sessionFilterOnly those of
// them a dimension or series may not name.
var (
	sessionColumnSet  = setOf(SessionColumns)
	sessionFilterOnly = setOf([]string{"duration_sec", "views"})
)

// IdentityColumns are the columns a dimension, series or filter on
// identities may name: kind, id and name, and day, its first_seen's day;
// and, filters only, the numbers views and events.
var IdentityColumns = []string{"kind", "id", "name", "day", "views", "events"}

// identityColumnSet is IdentityColumns as a set; identityFilterOnly those
// of them a dimension or series may not name.
var (
	identityColumnSet  = setOf(IdentityColumns)
	identityFilterOnly = setOf([]string{"views", "events"})
)

// plainColumns are Columns, SessionColumns and IdentityColumns, each with
// whether it is numeric (BIGINT, or the DATE day, which takes gt/lt
// against YYYY-MM-DD); the computed ones are text.
var plainColumns = func() map[string]bool {
	numeric := setOf([]string{"project_id", "day", "display_width", "display_height", "consent", "duration_sec", "views", "events"})
	m := make(map[string]bool, len(Columns)+len(SessionColumns)+len(IdentityColumns))
	for _, c := range slices.Concat(Columns, SessionColumns, IdentityColumns) {
		m[c] = numeric[c]
	}
	return m
}()

// columnInfo reports whether column takes gt/gte/lt/lte, and ok whether the
// table has the column at all as a filter (as a dimension or series too,
// but for sessionFilterOnly).
func columnInfo(table, column string) (orderable, ok bool) {
	if rest, found := strings.CutPrefix(column, defPrefix); found {
		return false, rest != "" // a defined column; validateDefines checks it is
	}
	switch table {
	case "sessions":
		return plainColumns[column], sessionColumnSet[column]
	case "identities":
		return plainColumns[column], identityColumnSet[column]
	}
	if column == "bucket" {
		return true, table == "measures"
	}
	if tables, found := computedTables[column]; found {
		return isFirstSeen(column), slices.Contains(tables, table)
	}
	if rest, found := strings.CutPrefix(column, "attr_"); found {
		return false, rest != ""
	}
	if !slices.Contains(Columns, column) {
		return false, false
	}
	return plainColumns[column], true
}

// dimensionOf says whether table's breakdowns and trends may group by
// column.
func dimensionOf(table, column string) bool {
	_, ok := columnInfo(table, column)
	return ok && !(table == "sessions" && sessionFilterOnly[column]) && !(table == "identities" && identityFilterOnly[column])
}

// The closed vocabularies of a Query, which a schema describing it lists.
var (
	Kinds = []string{"stat", "trend", "breakdown", "retention", "attributes", "funnel"}
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

func invalid(path, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalid, path, fmt.Sprintf(format, args...))
}

// Validate refuses a malformed query; the error wraps ErrInvalid and names
// the field.
func (q Query) Validate() error {
	if !slices.Contains(Kinds, q.Kind) {
		return invalid("kind", "%q is not one of %s", q.Kind, strings.Join(Kinds, ", "))
	}
	if q.Reserved && q.Kind != "attributes" {
		return invalid("reserved", "only an attributes takes reserved")
	}
	if len(q.Keep) > 0 && (q.Kind != "attributes" || q.Output != "keys") {
		return invalid("keep", "only an attributes' keys takes keep")
	}
	switch {
	case q.Fill != "" && q.Kind != "trend":
		return invalid("fill", "only a trend fills its days")
	case q.Fill != "" && q.Fill != "zero":
		return invalid("fill", "%q is not zero", q.Fill)
	case q.Interval != "" && q.Kind != "trend":
		return invalid("interval", "only a trend takes an interval")
	case q.Interval != "" && !slices.Contains(Intervals, q.Interval):
		return invalid("interval", "%q is not day, week or month", q.Interval)
	}
	if q.bucketed() {
		for i, m := range q.Metrics {
			if err := m.notDaily(fmt.Sprintf("metrics[%d]", i), q.Interval); err != nil {
				return err
			}
		}
	}
	if err := q.validateDefines(); err != nil {
		return err
	}
	switch q.Kind {
	case "retention":
		return q.validateRetention()
	case "attributes":
		return q.validateAttributes()
	case "funnel":
		return q.validateFunnel()
	}
	switch {
	case len(q.Steps) > 0:
		return invalid("steps", "only a funnel takes steps")
	case q.By != "":
		return invalid("by", "only a funnel takes by")
	case q.Sequence:
		return invalid("sequence", "only a funnel takes sequence")
	}
	switch {
	case q.Output != "":
		return invalid("output", "only a retention or an attributes takes an output")
	case q.IdentifiedBy != "":
		return invalid("identified_by", "only a retention takes identified_by")
	case q.Offsets != 0:
		return invalid("offsets", "only a retention takes offsets")
	case len(q.Milestones) > 0:
		return invalid("milestones", "only a retention takes milestones")
	}
	if !slices.Contains(Tables, q.Table) {
		return invalid("table", "%q is not one of %s", q.Table, strings.Join(Tables, ", "))
	}

	names := map[string]bool{}
	for i, d := range q.Dimensions {
		p := fmt.Sprintf("dimensions[%d]", i)
		if !dimensionOf(q.Table, d) {
			return invalid(p, "%q is not a dimension of %s", d, q.Table)
		}
		if names[d] {
			return invalid(p, "%q is repeated", d)
		}
		names[d] = true
	}
	switch q.Kind {
	case "breakdown":
		if n := len(q.Dimensions); n < 1 || n > 3 {
			return invalid("dimensions", "a breakdown takes 1 to 3, got %d", n)
		}
	default:
		if len(q.Dimensions) > 0 {
			return invalid("dimensions", "only a breakdown takes dimensions")
		}
	}

	switch {
	case q.Kind == "trend" && q.Series != "" && len(q.Metrics) != 1:
		return invalid("metrics", "a trend split by series takes exactly 1, got %d", len(q.Metrics))
	case q.Kind == "trend" && len(q.Metrics) > maxTrendMetrics:
		return invalid("metrics", "a trend takes 1 to %d, got %d", maxTrendMetrics, len(q.Metrics))
	case len(q.Metrics) < 1:
		return invalid("metrics", "at least 1 is required")
	}
	reserved := map[string]bool{"series": true, "day": true}
	metricNames := map[string]bool{}
	for i, m := range q.Metrics {
		if err := m.validate(q.Kind, q.Table, i); err != nil {
			return err
		}
		// A trend of several metrics has one value column, a number.
		if q.long() && m.valueType() != "number" {
			return invalid(fmt.Sprintf("metrics[%d].column", i), "%s of %s is not a number, which a trend of several metrics' value is", m.Op, m.Column)
		}
		// An unsplit trend's partition is the day's only group: its share
		// would always be 1.
		if m.Op == "share" && q.Kind == "trend" && q.Series == "" {
			return invalid(fmt.Sprintf("metrics[%d].op", i), "share is a metric of a trend split by series or of a breakdown; an unsplit trend's is always 1")
		}
		n := m.Name()
		if metricNames[n] || names[n] || reserved[n] {
			return invalid(fmt.Sprintf("metrics[%d].as", i), "name %q is already used", n)
		}
		metricNames[n] = true
	}

	for i, f := range q.Where {
		if err := f.validate(q.Table, fmt.Sprintf("where[%d]", i)); err != nil {
			return err
		}
	}

	if q.Series != "" {
		if q.Kind != "trend" {
			return invalid("series", "only a trend takes a series")
		}
		if !dimensionOf(q.Table, q.Series) {
			return invalid("series", "%q is not a dimension of %s", q.Series, q.Table)
		}
		if q.Series == "day" {
			return invalid("series", "a trend is by day already")
		}
	}
	if q.SeriesLimit < 0 || q.SeriesLimit > maxSeries || (q.SeriesLimit > 0 && q.Kind != "trend") {
		return invalid("series_limit", "%d is not between 1 and %d for a trend", q.SeriesLimit, maxSeries)
	}
	if q.SeriesLimit > 0 && q.Series == "" {
		return invalid("series_limit", "a trend without a series takes none")
	}
	switch q.Compare {
	case "":
	case "previous":
		if q.Kind != "stat" {
			return invalid("compare", "only a stat compares")
		}
		// The previous range's columns are previous_<name>: none may be
		// another metric's name.
		for i, m := range q.Metrics {
			if of, ok := strings.CutPrefix(m.Name(), "previous_"); ok && metricNames[of] {
				return invalid(fmt.Sprintf("metrics[%d].as", i), "name %q is metric %q's column over the previous range", m.Name(), of)
			}
		}
	default:
		return invalid("compare", "%q is not previous", q.Compare)
	}
	if q.Limit < 0 || q.Limit > maxLimit || (q.Limit > 0 && q.Kind != "breakdown") {
		return invalid("limit", "%d is not between 1 and %d for a breakdown", q.Limit, maxLimit)
	}
	if q.Other && q.Kind != "breakdown" {
		return invalid("other", "only a breakdown folds the rest")
	}
	if q.Order != "" {
		if q.Kind != "breakdown" {
			return invalid("order", "only a breakdown orders")
		}
		if !metricNames[q.Order] && !names[q.Order] {
			return invalid("order", "%q is not a metric name or a dimension", q.Order)
		}
	}
	return nil
}

// validateRetention refuses a malformed retention: it reads events, and
// takes its own fields and where alone.
func (q Query) validateRetention() error {
	if q.Table != "" && q.Table != "events" {
		return invalid("table", "a retention reads events, not %q", q.Table)
	}
	switch {
	case len(q.Metrics) > 0:
		return invalid("metrics", "a retention takes none; its output names its columns")
	case len(q.Dimensions) > 0:
		return invalid("dimensions", "a retention takes none")
	case q.Series != "":
		return invalid("series", "a retention takes none")
	case q.SeriesLimit != 0:
		return invalid("series_limit", "a retention takes none")
	case q.Compare != "":
		return invalid("compare", "a retention takes none")
	case q.Limit != 0:
		return invalid("limit", "a retention takes none")
	case q.Other:
		return invalid("other", "a retention takes none")
	case q.Order != "":
		return invalid("order", "a retention takes none")
	}
	if !slices.Contains(RetentionOutputs, q.Output) {
		return invalid("output", "%q is not one of %s", q.Output, strings.Join(RetentionOutputs, ", "))
	}
	switch {
	case q.Output == "grid" && q.IdentifiedBy != "" && !slices.Contains(IdentifiedBy, q.IdentifiedBy):
		return invalid("identified_by", "%q is not install or user; a grid without it has both", q.IdentifiedBy)
	case q.Output != "grid" && !slices.Contains(IdentifiedBy, q.IdentifiedBy):
		return invalid("identified_by", "%q is not install or user, which a %s needs", q.IdentifiedBy, q.Output)
	}
	if q.Offsets < 0 || q.Offsets > maxOffsets {
		return invalid("offsets", "%d is not between 1 and %d", q.Offsets, maxOffsets)
	}
	offsets := q.offsets()
	switch {
	case q.Output != "milestones" && len(q.Milestones) > 0:
		return invalid("milestones", "only the milestones output takes milestones")
	case len(q.Milestones) > maxMilestones:
		return invalid("milestones", "at most %d, got %d", maxMilestones, len(q.Milestones))
	case q.Output == "milestones" && len(q.Milestones) == 0 && offsets < slices.Max(defaultMilestones):
		return invalid("milestones", "the default 7, 30 and 45 need offsets of 45 or more, got %d; give milestones", offsets)
	}
	for i, m := range q.Milestones {
		p := fmt.Sprintf("milestones[%d]", i)
		if m < 0 || m > offsets {
			return invalid(p, "%d is not between 0 and offsets, %d", m, offsets)
		}
		if slices.Contains(q.Milestones[:i], m) {
			return invalid(p, "%d is repeated", m)
		}
	}
	for i, f := range q.Where {
		if err := f.validate("events", fmt.Sprintf("where[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

// validateAttributes refuses a malformed attributes query: it reads
// product or measures, groups by its attribute and value after day and
// event_name when named, and takes its own metrics.
func (q Query) validateAttributes() error {
	if q.Output == "keys" {
		return q.validateAttributeKeys()
	}
	switch {
	case !slices.Contains(AttributeTables, q.Table):
		return invalid("table", "an attributes reads %s, not %q", strings.Join(AttributeTables, " or "), q.Table)
	case !slices.Contains(AttributeOutputs, q.Output):
		return invalid("output", "%q is not one of %s", q.Output, strings.Join(AttributeOutputs, ", "))
	case q.Series != "":
		return invalid("series", "an attributes takes none")
	case q.SeriesLimit != 0:
		return invalid("series_limit", "an attributes takes none")
	case q.Compare != "":
		return invalid("compare", "an attributes takes none")
	case q.Other:
		return invalid("other", "an attributes takes none")
	case q.IdentifiedBy != "":
		return invalid("identified_by", "an attributes takes none")
	case q.Offsets != 0:
		return invalid("offsets", "an attributes takes none")
	case len(q.Milestones) > 0:
		return invalid("milestones", "an attributes takes none")
	case q.Limit < 0 || q.Limit > maxLimit:
		return invalid("limit", "%d is not between 1 and %d", q.Limit, maxLimit)
	}
	names := map[string]bool{"attribute": true, "value": true}
	next := 0 // the dimensions come in AttributeDimensions' order
	for i, d := range q.Dimensions {
		j := slices.Index(AttributeDimensions, d)
		if j < 0 {
			return invalid(fmt.Sprintf("dimensions[%d]", i), "%q is not day or event_name", d)
		}
		if j < next {
			return invalid("dimensions", "day and event_name come once each, in that order")
		}
		next = j + 1
		names[d] = true
	}
	if len(q.Metrics) < 1 {
		return invalid("metrics", "at least 1 is required")
	}
	metricNames := map[string]bool{}
	for i, m := range q.Metrics {
		p := fmt.Sprintf("metrics[%d]", i)
		if !slices.Contains(AttributeOps, m.Op) {
			return invalid(p+".op", "%q is not a metric of attributes, which takes %s", m.Op, strings.Join(AttributeOps, ", "))
		}
		if len(m.Where) > 0 {
			return invalid(p+".where", "an attributes' metric takes none")
		}
		if err := m.validate("attributes", q.Table, i); err != nil {
			return err
		}
		n := m.Name()
		if metricNames[n] || names[n] || n == "day" {
			return invalid(p+".as", "name %q is already used", n)
		}
		metricNames[n] = true
	}
	for i, f := range q.Where {
		if err := f.validate(q.Table, fmt.Sprintf("where[%d]", i)); err != nil {
			return err
		}
	}
	if q.Order != "" && !metricNames[q.Order] && !names[q.Order] {
		return invalid("order", "%q is not a metric name, a dimension, attribute or value", q.Order)
	}
	return nil
}

// The attributes keys' bounds: the keys it keeps by default, and MaxKeep
// the names keep may list.
const (
	defaultKeysLimit = 50
	MaxKeep          = 1000
)

// keysTable is an attributes keys' table, events when unset.
func (q Query) keysTable() string {
	if q.Table == "" {
		return "events"
	}
	return q.Table
}

// keysLimit is an attributes keys' limit, its default when unset.
func (q Query) keysLimit() int {
	if q.Limit == 0 {
		return defaultKeysLimit
	}
	return q.Limit
}

// validateAttributeKeys refuses a malformed attributes keys: it reads
// events (product and measures), product or measures, and takes only
// where, limit and keep beside them.
func (q Query) validateAttributeKeys() error {
	switch t := q.keysTable(); {
	case t != "events" && !slices.Contains(AttributeTables, t):
		return invalid("table", "an attributes' keys reads events, product or measures, not %q", q.Table)
	case len(q.Metrics) > 0:
		return invalid("metrics", "an attributes' keys takes none; its columns are attribute, events, values and keys_total")
	case len(q.Dimensions) > 0:
		return invalid("dimensions", "an attributes' keys takes none")
	case q.Reserved:
		return invalid("reserved", "an attributes' keys takes none")
	case q.Order != "":
		return invalid("order", "an attributes' keys are by events, descending, then attribute")
	case q.Series != "":
		return invalid("series", "an attributes takes none")
	case q.SeriesLimit != 0:
		return invalid("series_limit", "an attributes takes none")
	case q.Compare != "":
		return invalid("compare", "an attributes takes none")
	case q.Other:
		return invalid("other", "an attributes takes none")
	case q.IdentifiedBy != "":
		return invalid("identified_by", "an attributes takes none")
	case q.Offsets != 0:
		return invalid("offsets", "an attributes takes none")
	case len(q.Milestones) > 0:
		return invalid("milestones", "an attributes takes none")
	case q.Limit < 0 || q.Limit > maxLimit:
		return invalid("limit", "%d is not between 1 and %d", q.Limit, maxLimit)
	case len(q.Keep) > MaxKeep:
		return invalid("keep", "%d names, at most %d", len(q.Keep), MaxKeep)
	}
	for i, f := range q.Where {
		if err := f.validate(q.keysTable(), fmt.Sprintf("where[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

func (m Metric) validate(kind, table string, i int) error {
	return m.validateAt(kind, table, fmt.Sprintf("metrics[%d]", i), false)
}

// validateAt is validate of the metric at path p (metrics[0], or a
// ratio's side, metrics[0].num, when side).
func (m Metric) validateAt(kind, table, p string, side bool) error {
	if !metricOps[m.Op] {
		return invalid(p+".op", "%q is not a metric", m.Op)
	}
	if m.Op == "ratio" {
		if err := m.validateRatio(kind, table, p); err != nil {
			return err
		}
	} else if m.Num != nil || m.Den != nil {
		side := ".num"
		if m.Num == nil {
			side = ".den"
		}
		return invalid(p+side, "only a ratio takes num and den")
	}
	if m.Op == "actors_or_null" && kind != "attributes" {
		return invalid(p+".op", "actors_or_null is a metric of attributes only")
	}
	if table == "sessions" && !sessionOps[m.Op] {
		return invalid(p+".op", "%s is not a metric of sessions, which takes count, actors, share, ratio, bounces, bounce_rate, sum, avg, min and max", m.Op)
	}
	if table == "identities" && !idOps[m.Op] {
		return invalid(p+".op", "%s is not a metric of identities, which takes count, share, ratio, sum, avg, min and max", m.Op)
	}
	if table != "sessions" && bounceOps[m.Op] {
		return invalid(p+".op", "%s is a metric of sessions only", m.Op)
	}
	if table != "measures" && measureOps[m.Op] {
		return invalid(p+".op", "%s is a metric of measures only", m.Op)
	}
	switch {
	case m.Op == "percentile" && !(m.Q > 0 && m.Q < 1):
		return invalid(p+".q", "%v is not above 0 and below 1", m.Q)
	case m.Op != "percentile" && m.Q != 0:
		return invalid(p+".q", "%s takes no q", m.Op)
	}
	if m.Round != nil && (*m.Round < 0 || *m.Round > maxRound) {
		return invalid(p+".round", "%d is not between 0 and %d", *m.Round, maxRound)
	}
	for j, f := range m.Where {
		if err := f.validate(table, fmt.Sprintf("%s.where[%d]", p, j)); err != nil {
			return err
		}
	}
	switch m.Op {
	case "latest":
		switch {
		case side:
			return invalid(p+".op", "latest is no number, which a ratio's side is")
		case kind == "attributes":
			return invalid(p+".op", "latest is not a metric of attributes")
		case m.Column == "":
			return invalid(p+".column", "latest needs a column: the one whose value it reads")
		case m.Of != "":
			return invalid(p+".of", "latest takes no of")
		case idColumns[m.Column].id != "":
			return invalid(p+".column", "%s is read from identities; latest reads a column of the rows", m.Column)
		}
		if _, ok := columnInfo(table, m.Column); !ok || m.Column == "bucket" {
			return invalid(p+".column", "%q is not a column of %s", m.Column, table)
		}
	case "sum", "avg", "min", "max":
		if m.Column == "" {
			return invalid(p+".column", "%s needs a column", m.Op)
		}
		bound := (m.Op == "min" || m.Op == "max") && ((table == "sessions" && sessBound[m.Column]) ||
			(table != "sessions" && table != "identities" && boundCols[m.Column]))
		switch {
		case bound && side:
			return invalid(p+".column", "%s of %s is not a number, which a ratio's side is", m.Op, m.Column)
		case bound:
		case table == "sessions" && !sessValue[m.Column]:
			return invalid(p+".column", "%q is not duration_sec or views, the numbers of sessions (min and max also take day, started_at and ended_at)", m.Column)
		case table == "identities" && !idValue[m.Column]:
			return invalid(p+".column", "%q is not views or events, the numbers of identities", m.Column)
		case table != "sessions" && table != "identities" && !valueCols[m.Column]:
			return invalid(p+".column", "%q is not value, sample_rate, display_width or display_height (min and max also take day, ts and received_at)", m.Column)
		}
		if m.Of != "" {
			return invalid(p+".of", "%s takes no of", m.Op)
		}
	case "share":
		// A stat is one group: its share would always be 1.
		if kind == "stat" {
			return invalid(p+".op", "share is a metric of a trend or a breakdown, not a stat")
		}
		if !slices.Contains(ShareOfColumns, m.Of) {
			return invalid(p+".of", "share needs count or weight, got %q", m.Of)
		}
		if m.Of == "weight" && table != "measures" {
			return invalid(p+".of", "share of weight is a metric of measures only")
		}
		if m.Column != "" {
			return invalid(p+".column", "share takes no column")
		}
	case "daily_sum", "daily_max", "daily_avg":
		if m.Of == "" {
			return invalid(p+".of", "%s needs actors, users or groups", m.Op)
		}
		if !ofCols[m.Of] {
			return invalid(p+".of", "%q is not actors, users or groups", m.Of)
		}
		if m.Column != "" {
			return invalid(p+".column", "%s takes no column", m.Op)
		}
	default:
		if m.Column != "" {
			return invalid(p+".column", "%s takes no column", m.Op)
		}
		if m.Of != "" {
			return invalid(p+".of", "%s takes no of", m.Op)
		}
	}
	return nil
}

// validateRatio refuses a malformed ratio at path p: num and den are
// metrics of the table, each with its own where, but no as, round,
// share or ratio; the ratio itself takes no where.
func (m Metric) validateRatio(kind, table, p string) error {
	if len(m.Where) > 0 {
		return invalid(p+".where", "a ratio takes none; give num and den each its own")
	}
	for _, side := range []struct {
		name string
		m    *Metric
	}{{"num", m.Num}, {"den", m.Den}} {
		sp := p + "." + side.name
		switch {
		case side.m == nil:
			return invalid(sp, "a ratio needs %s, a metric", side.name)
		case side.m.Op == "ratio" || side.m.Op == "share":
			return invalid(sp+".op", "%s is not a side of a ratio", side.m.Op)
		case side.m.As != "":
			return invalid(sp+".as", "a ratio's side has no name; give the ratio as")
		case side.m.Round != nil:
			return invalid(sp+".round", "a ratio's side is not rounded; give the ratio round")
		}
		if err := side.m.validateAt(kind, table, sp, true); err != nil {
			return err
		}
	}
	return nil
}

// validate refuses a malformed filter; p is its path (where[0],
// metrics[1].where[0]).
func (f Filter) validate(table, p string) error {
	if f.Op == "ever" || f.Op == "never" {
		return f.validateEver(table, p)
	}
	if len(f.Where) > 0 || f.By != "" || f.Over != "" {
		return invalid(p+".where", "only ever and never take where, by and over")
	}
	if f.Op == "any_of" {
		switch n := len(f.Any); {
		case f.Column != "":
			return invalid(p+".column", "any_of takes any, not column")
		case len(f.Columns) != 0:
			return invalid(p+".columns", "any_of takes any, not columns")
		case len(f.Values) != 0:
			return invalid(p+".values", "any_of takes none")
		case n < 2 || n > maxAnyOf:
			return invalid(p+".any", "any_of takes 2 to %d filters, got %d", maxAnyOf, n)
		}
		for j, g := range f.Any {
			q := fmt.Sprintf("%s.any[%d]", p, j)
			if g.Op == "any_of" || g.Op == "ever" || g.Op == "never" {
				return invalid(q+".op", "an any_of holds no %s", g.Op)
			}
			if err := g.validate(table, q); err != nil {
				return err
			}
		}
		return nil
	}
	if len(f.Any) > 0 {
		return invalid(p+".any", "only any_of takes any")
	}
	if f.Op == "any_set" {
		switch n := len(f.Columns); {
		case f.Column != "":
			return invalid(p+".column", "any_set takes columns, not column")
		case n < 2 || n > maxAnySet:
			return invalid(p+".columns", "any_set takes 2 to %d columns, got %d", maxAnySet, n)
		case len(f.Values) != 0:
			return invalid(p+".values", "any_set takes none")
		}
		for j, c := range f.Columns {
			if _, ok := columnInfo(table, c); !ok {
				return invalid(fmt.Sprintf("%s.columns[%d]", p, j), "%q is not a column of %s", c, table)
			}
		}
		return nil
	}
	if len(f.Columns) > 0 {
		return invalid(p+".columns", "only any_set takes columns")
	}
	orderable, ok := columnInfo(table, f.Column)
	if !ok {
		return invalid(p+".column", "%q is not a column of %s", f.Column, table)
	}
	if !filterOps[f.Op] {
		return invalid(p+".op", "%q is not a filter operator", f.Op)
	}
	n := len(f.Values)
	switch f.Op {
	case "is_set":
		if n != 0 {
			return invalid(p+".values", "is_set takes none")
		}
	case "in", "not_in":
		if n < 1 {
			return invalid(p+".values", "%s needs at least 1", f.Op)
		}
	default:
		if n != 1 {
			return invalid(p+".values", "%s takes exactly 1, got %d", f.Op, n)
		}
	}
	if orderedOps[f.Op] {
		if !orderable {
			return invalid(p+".op", "%s is not allowed on %s", f.Op, f.Column)
		}
		v := f.Values[0]
		if typeOf(f.Column) == typeDay {
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return invalid(p+".values", "%q is not a YYYY-MM-DD date", v)
			}
		} else if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return invalid(p+".values", "%q is not an integer", v)
		}
	}
	return nil
}
