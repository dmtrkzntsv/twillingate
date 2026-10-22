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
// by Series), a breakdown the metrics grouped by 1-3 Dimensions.
type Query struct {
	Kind        string   `json:"kind" jsonschema:"stat: one row of metrics (with compare previous, a previous_<name> column each); trend: one metric per day, optionally split by series; breakdown: the metrics grouped by 1 to 3 dimensions"`
	Table       string   `json:"table" jsonschema:"the rows read: events (every family), views, product or measures"`
	Metrics     []Metric `json:"metrics" jsonschema:"the aggregates, each an output column; a trend takes exactly 1"`
	Where       []Filter `json:"where,omitempty" jsonschema:"filters, all of which a row must satisfy"`
	Dimensions  []string `json:"dimensions,omitempty" jsonschema:"breakdown only, 1 to 3: columns to group by, each one a filter's column may name (path, country, event_name, attr_<key>, …)"`
	Series      string   `json:"series,omitempty" jsonschema:"trend only, optional: a column to split each day by, one a filter's column may name except day"`
	SeriesLimit int      `json:"series_limit,omitempty" jsonschema:"trend only: how many series, 1 to 50, the top ones by the metric over the range (default 5)"`
	Compare     string   `json:"compare,omitempty" jsonschema:"stat only: previous adds each metric over as many days ending the day before from"`
	Limit       int      `json:"limit,omitempty" jsonschema:"breakdown only: how many groups, 1 to 1000 (default 20)"`
	Other       bool     `json:"other,omitempty" jsonschema:"breakdown only: fold the groups past limit into one row whose dimensions read Other"`
	Order       string   `json:"order,omitempty" jsonschema:"breakdown only: a metric name to rank groups by, descending, or one of its dimensions to sort them by, ascending, ties by the other dimensions ascending (default: the first metric); limit keeps the first groups in that order"`
}

// Metric is one aggregate.
type Metric struct {
	Op     string `json:"op" jsonschema:"count: rows; actors, users, groups: distinct actor_id, user_id, group_id; sum, avg, min, max: of column; daily_sum, daily_max, daily_avg: the per-day distinct count of of, summed, maxed or averaged over the days with rows"`
	Column string `json:"column,omitempty" jsonschema:"sum, avg, min and max only: the numeric column"`
	Of     string `json:"of,omitempty" jsonschema:"daily_* only: what is counted distinct per day"`
	As     string `json:"as,omitempty" jsonschema:"the output column's name (default: op, or op_column, or op_of)"`
}

// Filter keeps the rows whose Column satisfies Op against Values.
type Filter struct {
	Column string   `json:"column" jsonschema:"one of project_id, family, event_name, day, kind, actor_id, actor_kind, user_id, group_id, session_id, install_id, user_name, group_name, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, measure, bucket (measures only), or attr_<key> for an attribute"`
	Op     string   `json:"op" jsonschema:"eq, ne, contains, prefix, gt, gte, lt, lte take 1 value; in, not_in 1 or more; is_set none (not NULL, and text not empty). gt/gte/lt/lte take an integer, or YYYY-MM-DD on day, and only on numeric columns. NULL fails every op, ne and not_in included"`
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
	}
	return m.Op
}

// Columns are the result's column names, in order, as Answer returns
// them for q, read from q alone: a stat's metrics, then with compare
// previous a previous_<name> each; a trend's day, series when split, and
// its metric; a breakdown's dimensions, then its metrics. It does not
// validate q.
func (q Query) Columns() []string {
	var cols []string
	switch q.Kind {
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

// maxLimit bounds a breakdown's limit, maxSeries a trend's series_limit.
const (
	maxLimit  = 1000
	maxSeries = 50
)

// Columns are the non-attribute columns a dimension, series or filter may
// name (bucket aside, which only measures has).
var Columns = []string{
	"project_id", "family", "event_name", "day", "kind", "actor_id", "actor_kind",
	"user_id", "group_id", "session_id", "install_id", "user_name", "group_name",
	"host", "path", "referrer_source", "utm_source", "utm_medium", "utm_campaign",
	"platform", "os", "os_version", "os_name", "browser", "browser_version",
	"browser_locale", "app_version", "app_locale", "device", "device_model",
	"display_width", "display_height", "country", "consent", "measure",
}

// plainColumns are Columns, each with whether it is numeric (BIGINT, or
// the DATE day, which takes gt/lt against YYYY-MM-DD).
var plainColumns = func() map[string]bool {
	numeric := setOf([]string{"project_id", "day", "display_width", "display_height", "consent"})
	m := make(map[string]bool, len(Columns))
	for _, c := range Columns {
		m[c] = numeric[c]
	}
	return m
}()

// columnInfo reports whether column takes gt/gte/lt/lte, and ok whether the
// table has the column at all as a dimension or filter.
func columnInfo(table, column string) (orderable, ok bool) {
	if column == "bucket" {
		return true, table == "measures"
	}
	if rest, found := strings.CutPrefix(column, "attr_"); found {
		return false, rest != ""
	}
	orderable, ok = plainColumns[column]
	return orderable, ok
}

// The closed vocabularies of a Query, which a schema describing it lists.
var (
	Kinds        = []string{"stat", "trend", "breakdown"}
	Tables       = []string{"events", "views", "product", "measures"}
	MetricOps    = []string{"count", "actors", "users", "groups", "sum", "avg", "min", "max", "daily_sum", "daily_max", "daily_avg"}
	ValueColumns = []string{"value", "sample_rate", "display_width", "display_height"}
	OfColumns    = []string{"actors", "users", "groups"}
	FilterOps    = []string{"eq", "ne", "in", "not_in", "contains", "prefix", "gt", "gte", "lt", "lte", "is_set"}
)

var (
	metricOps  = setOf(MetricOps)
	valueCols  = setOf(ValueColumns)
	ofCols     = setOf(OfColumns)
	filterOps  = setOf(FilterOps)
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
	if !slices.Contains(Tables, q.Table) {
		return invalid("table", "%q is not one of %s", q.Table, strings.Join(Tables, ", "))
	}

	names := map[string]bool{}
	for i, d := range q.Dimensions {
		p := fmt.Sprintf("dimensions[%d]", i)
		if _, ok := columnInfo(q.Table, d); !ok {
			return invalid(p, "%q is not a column of %s", d, q.Table)
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
		if err := m.validate(i); err != nil {
			return err
		}
		n := m.Name()
		if metricNames[n] || names[n] || reserved[n] {
			return invalid(fmt.Sprintf("metrics[%d].as", i), "name %q is already used", n)
		}
		metricNames[n] = true
	}

	for i, f := range q.Where {
		if err := f.validate(q.Table, i); err != nil {
			return err
		}
	}

	if q.Series != "" {
		if q.Kind != "trend" {
			return invalid("series", "only a trend takes a series")
		}
		if _, ok := columnInfo(q.Table, q.Series); !ok {
			return invalid("series", "%q is not a column of %s", q.Series, q.Table)
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

func (m Metric) validate(i int) error {
	p := fmt.Sprintf("metrics[%d]", i)
	if !metricOps[m.Op] {
		return invalid(p+".op", "%q is not a metric", m.Op)
	}
	switch m.Op {
	case "sum", "avg", "min", "max":
		if m.Column == "" {
			return invalid(p+".column", "%s needs a column", m.Op)
		}
		if !valueCols[m.Column] {
			return invalid(p+".column", "%q is not value, sample_rate, display_width or display_height", m.Column)
		}
		if m.Of != "" {
			return invalid(p+".of", "%s takes no of", m.Op)
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

func (f Filter) validate(table string, i int) error {
	p := fmt.Sprintf("where[%d]", i)
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
