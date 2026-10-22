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
// by Series), a breakdown the metrics grouped by 1-3 Dimensions, a
// retention its cohorts' days back (Output, IdentifiedBy, Offsets,
// Milestones).
type Query struct {
	Kind        string   `json:"kind" jsonschema:"stat: one row of metrics (with compare previous, a previous_<name> column each); trend: one metric per day, optionally split by series; breakdown: the metrics grouped by 1 to 3 dimensions; retention: the cohorts of users and installs first active in the range, and their days back (output, identified_by, offsets, milestones; no metrics)"`
	Table       string   `json:"table,omitempty" jsonschema:"the rows read: events (every family), views, product or measures; or sessions, one row per session of views; or identities, one row per user or group first seen in the range (rules apply to neither); a retention reads events (absent or events)"`
	Metrics     []Metric `json:"metrics,omitempty" jsonschema:"the aggregates, each an output column; a trend takes exactly 1; a retention none"`
	Where       []Filter `json:"where,omitempty" jsonschema:"filters, all of which a row must satisfy"`
	Dimensions  []string `json:"dimensions,omitempty" jsonschema:"breakdown only, 1 to 3: columns to group by, each one a filter's column may name on the table (path, country, event_name, attr_<key>, …; resolution on events and views only, vital and vital_rating on measures only, the other computed ones on events, views and product), but duration_sec and views on sessions and views and events on identities"`
	Series      string   `json:"series,omitempty" jsonschema:"trend only, optional: a column to split each day by, one a filter's column may name on the table except day (resolution on events and views only, vital and vital_rating on measures only, the other computed ones on events, views and product), and except duration_sec and views on sessions and views and events on identities"`
	SeriesLimit int      `json:"series_limit,omitempty" jsonschema:"trend only: how many series, 1 to 50, the top ones by the metric over the range (default 5)"`
	Compare     string   `json:"compare,omitempty" jsonschema:"stat only: previous adds each metric over as many days ending the day before from"`
	Limit       int      `json:"limit,omitempty" jsonschema:"breakdown only: how many groups, 1 to 1000 (default 20)"`
	Other       bool     `json:"other,omitempty" jsonschema:"breakdown only: fold the groups past limit into one row whose dimensions read Other"`
	Order       string   `json:"order,omitempty" jsonschema:"breakdown only: a metric name to rank groups by, descending, or one of its dimensions to sort them by, ascending, ties by the other dimensions ascending (default: the first metric); limit keeps the first groups in that order"`
	// The retention kind's fields.
	Output       string `json:"output,omitempty" jsonschema:"retention only, required: grid (cohort, identified_by, day, size, returned, retention: a row per cohort, identity kind and day back), curve (day, retention: one identity kind's cohorts pooled per day back) or milestones (one row, a d<n> column per milestone: the curve's retention that day back)"`
	IdentifiedBy string `json:"identified_by,omitempty" jsonschema:"retention curve and milestones only, required: install or user, the cohorts pooled (a grid has both)"`
	Offsets      int    `json:"offsets,omitempty" jsonschema:"retention only: the days back counted, 1 to 90 (default 45)"`
	Milestones   []int  `json:"milestones,omitempty" jsonschema:"retention milestones only: 1 to 6 distinct days back, each 0 to offsets (default 7, 30 and 45)"`
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
	Op     string   `json:"op" jsonschema:"count: rows; actors, users, groups: distinct actor_id, user_id, group_id; sum, avg, min, max: of column; daily_sum, daily_max, daily_avg: the per-day distinct count of of, summed, maxed or averaged over the days with rows. measures alone takes percentile (the q quantile of value, a sample weighing 1 / sample_rate, read from its log-scale bucket: within about 2%), mean (value weighted so) and weight (the samples' 1 / sample_rate summed). share (trend and breakdown only): the group's of (count, or on measures weight) over its partition's: in a breakdown the groups of the same first dimension (all with one dimension; an Other row's is all of them), in a trend the day's; empty over none. sessions takes count (sessions), actors, share, sum, avg, min and max, and its own bounces (sessions of 1 view) and bounce_rate (bounces / sessions, empty over none). identities takes count, share, sum, avg, min and max"`
	Q      float64  `json:"q,omitempty" jsonschema:"percentile only, above 0 and below 1: the quantile (0.75 for p75)"`
	Column string   `json:"column,omitempty" jsonschema:"sum, avg, min and max only: the numeric column, value, sample_rate, display_width or display_height; on sessions duration_sec or views; on identities views or events"`
	Of     string   `json:"of,omitempty" jsonschema:"daily_sum, daily_max and daily_avg take actors, users or groups (what is counted distinct per day); share takes count or, on measures, weight"`
	As     string   `json:"as,omitempty" jsonschema:"the output column's name (default: op, or op_column, or op_of, as share_weight; a percentile's p and 100 q, as p75 or p99.9)"`
	Round  *int     `json:"round,omitempty" jsonschema:"optional, 0 to 6: decimal places to round the value to, half away from zero (an integer stays as it is)"`
	Where  []Filter `json:"where,omitempty" jsonschema:"optional: filters, as the query's where, that a row must also satisfy to count toward this metric only; a group still exists from the query's where, so its metric may be 0 (count, distinct counts, bounces) or empty (the others)"`
}

// maxRound bounds a metric's round.
const maxRound = 6

// Filter keeps the rows whose Column satisfies Op against Values.
type Filter struct {
	Column string   `json:"column" jsonschema:"one of project_id, family, event_name, day, kind, actor_id, actor_kind, user_id, group_id, session_id, install_id, user_name, group_name, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, measure, bucket (measures only), or attr_<key> for an attribute; or computed, text: resolution (display_width x display_height, as 1920x1080, where both are above 0; events and views only), device_label (device_model, else device, where either is not empty), platform_version (platform, a space, app_version, where app_version is not empty), consent_state (given, none or unknown, from consent), user_status and group_status (new on the day the user_id or group_id was first seen, as identities has it, returning on a later day, NULL without one), these five on events, views and product only; on measures only, vital (LCP, INP, CLS, FCP or TTFB for the event_name $lcp, $inp, $cls, $fcp or $ttfb, else NULL) and vital_rating (good, needs improvement or poor: the sample's bucket value against the vital's thresholds, LCP 2500 and 4000, INP 200 and 500, CLS 0.1 and 0.25, FCP 1800 and 3000, TTFB 800 and 1800; NULL for the other measures). On sessions instead: project_id, day, kind, actor_id, actor_kind, session_id, entry_host, entry_path, exit_path, referrer_source, utm_source, utm_medium, utm_campaign, and the numbers duration_sec and views. On identities instead: kind (user or group), id, name, day (first_seen's day), and the numbers views and events"`
	Op     string   `json:"op" jsonschema:"eq, ne, contains, prefix, not_prefix (text that does not start with the value), gt, gte, lt, lte take 1 value; in, not_in 1 or more; is_set none (not NULL, and text not empty). gt/gte/lt/lte take an integer, or YYYY-MM-DD on day, and only on numeric columns. NULL fails every op, ne and not_in included"`
	Values []string `json:"values,omitempty" jsonschema:"the values compared against, as text"`
}

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
// its metric; a breakdown's dimensions, then its metrics; a retention
// grid's cohort, identified_by, day, size, returned and retention, a
// curve's day and retention, milestones' d<n> each. It does not
// validate q.
func (q Query) Columns() []string {
	var cols []string
	switch q.Kind {
	case "retention":
		switch q.Output {
		case "grid":
			return []string{"cohort", "identified_by", "day", "size", "returned", "retention"}
		case "curve":
			return []string{"day", "retention"}
		}
		for _, m := range q.milestones() {
			cols = append(cols, "d"+strconv.Itoa(m))
		}
		return cols
	case "trend":
		cols = append(cols, "day")
		if q.Series != "" {
			cols = append(cols, "series")
		}
	case "breakdown":
		cols = append(cols, q.Dimensions...)
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
// and the numeric columns (project_id, display_width, display_height,
// consent, bucket) are number; day is day; a trend's series and every
// other column, attr_<key> included, text. A breakdown with other has
// its dimensions text, its Other row reading "Other" in each. A
// retention's cohort is day, identified_by text, the others number. It
// does not validate q.
func (q Query) ColumnTypes() []string {
	var types []string
	switch q.Kind {
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
		if q.Series != "" {
			types = append(types, "text")
		}
	case "breakdown":
		for _, d := range q.Dimensions {
			if q.Other {
				types = append(types, "text")
			} else {
				types = append(types, columnType(d))
			}
		}
	}
	for range q.Metrics {
		types = append(types, "number")
	}
	if q.Kind == "stat" && q.Compare == "previous" {
		for range q.Metrics {
			types = append(types, "number")
		}
	}
	return types
}

// columnType is a dimension column's type, as ColumnTypes names it.
func columnType(column string) string {
	switch {
	case column == "day":
		return "day"
	case column == "bucket", plainColumns[column]:
		return "number"
	}
	return "text"
}

// maxLimit bounds a breakdown's limit, maxSeries a trend's series_limit.
const (
	maxLimit  = 1000
	maxSeries = 50
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
	"user_status", "group_status", "vital", "vital_rating",
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

// plainColumns are Columns, SessionColumns and IdentityColumns, each with whether it is
// numeric (BIGINT, or the DATE day, which takes gt/lt against
// YYYY-MM-DD); the computed ones are text.
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
		return false, slices.Contains(tables, table)
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
	Kinds = []string{"stat", "trend", "breakdown", "retention"}
	// RetentionOutputs are a retention's outputs, IdentifiedBy what its
	// curve and milestones pool.
	RetentionOutputs = []string{"grid", "curve", "milestones"}
	IdentifiedBy     = []string{"install", "user"}
	Tables           = []string{"events", "views", "product", "measures", "sessions", "identities"}
	MetricOps        = []string{"count", "actors", "users", "groups", "sum", "avg", "min", "max", "daily_sum", "daily_max", "daily_avg", "bounces", "bounce_rate", "percentile", "mean", "weight", "share"}
	ValueColumns     = []string{"value", "sample_rate", "display_width", "display_height"}
	// SessionValueColumns are the columns sum, avg, min and max take on
	// sessions (ValueColumns on the other tables).
	SessionValueColumns = []string{"duration_sec", "views"}
	// IdentityValueColumns are the columns sum, avg, min and max take on
	// identities.
	IdentityValueColumns = []string{"views", "events"}
	OfColumns            = []string{"actors", "users", "groups"}
	// ShareOfColumns are what share takes as of (weight on measures only).
	ShareOfColumns = []string{"count", "weight"}
	FilterOps      = []string{"eq", "ne", "in", "not_in", "contains", "prefix", "not_prefix", "gt", "gte", "lt", "lte", "is_set"}
)

var (
	metricOps  = setOf(MetricOps)
	valueCols  = setOf(ValueColumns)
	sessValue  = setOf(SessionValueColumns)
	idValue    = setOf(IdentityValueColumns)
	idOps      = setOf([]string{"count", "share", "sum", "avg", "min", "max"})
	ofCols     = setOf(OfColumns)
	filterOps  = setOf(FilterOps)
	sessionOps = setOf([]string{"count", "actors", "share", "sum", "avg", "min", "max", "bounces", "bounce_rate"})
	bounceOps  = setOf([]string{"bounces", "bounce_rate"})
	measureOps = setOf([]string{"percentile", "mean", "weight"})
	orderedOps = map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true}
)

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
	if q.Kind == "retention" {
		return q.validateRetention()
	}
	switch {
	case q.Output != "":
		return invalid("output", "only a retention takes an output")
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
	case q.Kind == "trend" && len(q.Metrics) != 1:
		return invalid("metrics", "a trend takes exactly 1, got %d", len(q.Metrics))
	case len(q.Metrics) < 1:
		return invalid("metrics", "at least 1 is required")
	}
	reserved := map[string]bool{"series": true, "day": true}
	metricNames := map[string]bool{}
	for i, m := range q.Metrics {
		if err := m.validate(q.Kind, q.Table, i); err != nil {
			return err
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
	case q.Output == "grid" && q.IdentifiedBy != "":
		return invalid("identified_by", "a grid has both install and user; only a curve or milestones takes identified_by")
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

func (m Metric) validate(kind, table string, i int) error {
	p := fmt.Sprintf("metrics[%d]", i)
	if !metricOps[m.Op] {
		return invalid(p+".op", "%q is not a metric", m.Op)
	}
	if table == "sessions" && !sessionOps[m.Op] {
		return invalid(p+".op", "%s is not a metric of sessions, which takes count, actors, share, bounces, bounce_rate, sum, avg, min and max", m.Op)
	}
	if table == "identities" && !idOps[m.Op] {
		return invalid(p+".op", "%s is not a metric of identities, which takes count, share, sum, avg, min and max", m.Op)
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
	case "sum", "avg", "min", "max":
		if m.Column == "" {
			return invalid(p+".column", "%s needs a column", m.Op)
		}
		switch {
		case table == "sessions" && !sessValue[m.Column]:
			return invalid(p+".column", "%q is not duration_sec or views, the numbers of sessions", m.Column)
		case table == "identities" && !idValue[m.Column]:
			return invalid(p+".column", "%q is not views or events, the numbers of identities", m.Column)
		case table != "sessions" && table != "identities" && !valueCols[m.Column]:
			return invalid(p+".column", "%q is not value, sample_rate, display_width or display_height", m.Column)
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

// validate refuses a malformed filter; p is its path (where[0],
// metrics[1].where[0]).
func (f Filter) validate(table, p string) error {
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
		if f.Column == "day" {
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return invalid(p+".values", "%q is not a YYYY-MM-DD date", v)
			}
		} else if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return invalid(p+".values", "%q is not an integer", v)
		}
	}
	return nil
}
