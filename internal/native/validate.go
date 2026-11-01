package native

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

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
	if err := q.validateShape(); err != nil {
		return err
	}
	if q.Depth != 0 && q.Kind != "paths" {
		return invalid("depth", "only paths takes depth")
	}
	if q.Kind != "funnel" {
		switch {
		case len(q.Steps) > 0:
			return invalid("steps", "only a funnel takes steps")
		case q.By != "":
			return invalid("by", "only a funnel takes by")
		case q.Sequence:
			return invalid("sequence", "only a funnel takes sequence")
		}
	}
	switch q.Kind {
	case "retention":
		return q.validateRetention()
	case "attributes":
		return q.validateAttributes()
	case "funnel":
		return q.validateFunnel()
	case "paths":
		return q.validatePaths()
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
