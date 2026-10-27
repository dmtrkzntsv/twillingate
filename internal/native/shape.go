package native

import (
	"fmt"
	"slices"
	"strings"
)

// Shapes are a stat's and a breakdown's answers: wide, a column per
// metric (the default), or long, a row per metric.
var Shapes = []string{"wide", "long"}

// longShape says whether q answers a row per metric.
func (q Query) longShape() bool { return q.Shape == "long" }

func (q Query) validateShape() error {
	switch {
	case q.Shape == "":
		return nil
	case !slices.Contains(Shapes, q.Shape):
		return invalid("shape", "%q is not wide or long", q.Shape)
	case q.Kind != "stat" && q.Kind != "breakdown":
		return invalid("shape", "only a stat or a breakdown takes a shape (a trend of several metrics is long already)")
	}
	if q.longShape() {
		for i, m := range q.Metrics {
			if m.valueType() != "number" {
				return invalid(fmt.Sprintf("metrics[%d]", i), "%s of %s is not a number, which a long answer's value is", m.Op, m.Column)
			}
		}
	}
	return nil
}

// longColumns are a long answer's columns, after a breakdown's
// dimensions: metric (each metric's name), value, and with compare
// previous_value.
func (q Query) longColumns() []string {
	cols := slices.Clone(q.Dimensions)
	cols = append(cols, "metric", "value")
	if q.Compare == "previous" {
		cols = append(cols, "previous_value")
	}
	return cols
}

// toLong is res, q's wide answer, as its long one: each row a row per
// metric, the dimensions repeated; value approximate when a metric's is.
func toLong(q Query, res Result) Result {
	nd := len(q.Dimensions)
	names := make([]string, len(q.Metrics))
	for i, m := range q.Metrics {
		names[i] = m.Name()
	}
	idx := func(name string) int { return slices.Index(res.Columns, name) }
	out := Result{Columns: q.longColumns(), Truncated: res.Truncated, Rows: make([][]string, 0, len(res.Rows)*len(names))}
	for _, r := range res.Rows {
		for _, n := range names {
			row := slices.Clone(r[:nd])
			row = append(row, n, r[idx(n)])
			if q.Compare == "previous" {
				row = append(row, r[idx("previous_"+n)])
			}
			out.Rows = append(out.Rows, row)
		}
	}
	for _, c := range res.Approximate {
		v := "value"
		if rest, ok := strings.CutPrefix(c, "previous_"); ok && q.Compare == "previous" && slices.Contains(names, rest) && !slices.Contains(names, c) {
			v = "previous_value"
		}
		if !slices.Contains(out.Approximate, v) && (v == "previous_value" || slices.Contains(names, c)) {
			out.Approximate = append(out.Approximate, v)
		}
	}
	return out
}
