package native

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid is wrapped by every refusal of a malformed Query; the message
// names the offending field.
var ErrInvalid = errors.New("native: invalid query")

// Query is a structured query over one events table. Kind picks the shape:
// a stat is one row of metrics, a trend one metric per day (optionally split
// by Series), a breakdown the metrics grouped by 1-3 Dimensions.
type Query struct {
	Kind        string   `json:"kind"`  // "stat" | "trend" | "breakdown"
	Table       string   `json:"table"` // "events" | "views" | "product" | "measures"
	Metrics     []Metric `json:"metrics"`
	Where       []Filter `json:"where,omitempty"`
	Dimensions  []string `json:"dimensions,omitempty"`   // breakdown: 1-3
	Series      string   `json:"series,omitempty"`       // trend: optional split
	SeriesLimit int      `json:"series_limit,omitempty"` // trend: top N series over the range (default 5)
	Compare     string   `json:"compare,omitempty"`      // stat: "" | "previous"
	Limit       int      `json:"limit,omitempty"`        // breakdown: default 20, max 1000
	Other       bool     `json:"other,omitempty"`        // breakdown: fold the rest into "Other"
	Order       string   `json:"order,omitempty"`        // breakdown: metric name to sort by, desc (default: first metric)
}

// Metric is one aggregate.
type Metric struct {
	Op     string `json:"op"`               // count | actors | users | groups | sum | avg | min | max | daily_sum | daily_max | daily_avg
	Column string `json:"column,omitempty"` // sum/avg/min/max: value | sample_rate | display_width | display_height
	Of     string `json:"of,omitempty"`     // daily_*: actors | users | groups
	As     string `json:"as,omitempty"`     // output column name; default derived from op
}

// Filter keeps the rows whose Column satisfies Op against Values.
type Filter struct {
	Column string   `json:"column"` // a table column or attr_<key>
	Op     string   `json:"op"`     // eq | ne | in | not_in | contains | prefix | gt | gte | lt | lte | is_set
	Values []string `json:"values,omitempty"`
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

const maxLimit = 1000

// plainColumns are the non-attribute columns a dimension, series or filter
// may name, each with whether it is numeric (BIGINT, or the DATE day, which
// takes gt/lt against YYYY-MM-DD).
var plainColumns = map[string]bool{
	"project_id": true, "family": false, "event_name": false, "day": true,
	"kind": false, "actor_id": false, "actor_kind": false, "user_id": false,
	"group_id": false, "session_id": false, "install_id": false, "user_name": false,
	"group_name": false, "host": false, "path": false, "referrer_source": false,
	"utm_source": false, "utm_medium": false, "utm_campaign": false, "platform": false,
	"os": false, "os_version": false, "os_name": false, "browser": false,
	"browser_version": false, "browser_locale": false, "app_version": false,
	"app_locale": false, "device": false, "device_model": false,
	"display_width": true, "display_height": true, "country": false,
	"consent": true, "measure": false,
}

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

var (
	metricOps  = map[string]bool{"count": true, "actors": true, "users": true, "groups": true, "sum": true, "avg": true, "min": true, "max": true, "daily_sum": true, "daily_max": true, "daily_avg": true}
	valueCols  = map[string]bool{"value": true, "sample_rate": true, "display_width": true, "display_height": true}
	ofCols     = map[string]bool{"actors": true, "users": true, "groups": true}
	filterOps  = map[string]bool{"eq": true, "ne": true, "in": true, "not_in": true, "contains": true, "prefix": true, "gt": true, "gte": true, "lt": true, "lte": true, "is_set": true}
	orderedOps = map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true}
)

func invalid(path, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalid, path, fmt.Sprintf(format, args...))
}

// Validate refuses a malformed query; the error wraps ErrInvalid and names
// the field.
func (q Query) Validate() error {
	switch q.Kind {
	case "stat", "trend", "breakdown":
	default:
		return invalid("kind", "%q is not stat, trend or breakdown", q.Kind)
	}
	switch q.Table {
	case "events", "views", "product", "measures":
	default:
		return invalid("table", "%q is not events, views, product or measures", q.Table)
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
	if q.SeriesLimit < 0 || (q.SeriesLimit > 0 && q.Kind != "trend") {
		return invalid("series_limit", "%d is not valid here", q.SeriesLimit)
	}
	switch q.Compare {
	case "":
	case "previous":
		if q.Kind != "stat" {
			return invalid("compare", "only a stat compares")
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
		if !metricNames[q.Order] {
			return invalid("order", "%q is not a metric name", q.Order)
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
