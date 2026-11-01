package api

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/native"
)

type measuresIn struct {
	rangeIn
	Name    string `json:"name,omitempty" jsonschema:"filter to one metric name"`
	AttrKey string `json:"attr_key,omitempty" jsonschema:"break each metric down by this attribute key: a reserved key such as $browser or $path, or any key the measures carried; no declaration needed"`
}

// measures answers a native breakdown of the measures by name and kind
// (and by attr_key's value when given), as the Measures dashboard reads
// them: the samples, their estimated count (each sample weighing
// 1 / sample_rate, so sampled rows count at full size, design decisions
// 18-20), the weighted mean, and p50, p75 and p95 read from the
// log-scale buckets, by name, kind and value: at most
// CONSOLE_QUERY_ROWS_MAX rows (and the engine's 1,000).
func (h *host) measures(ctx context.Context, in measuresIn) (tableOut, error) {
	p, ok, err := h.rangeOf(ctx, in.rangeIn)
	if err != nil {
		return tableOut{}, err
	}
	dims, cols := []string{"event_name", "measure"}, []string{"event_name", "measure"}
	var where []native.Filter
	if in.Name != "" {
		where = append(where, native.Filter{Column: "event_name", Op: "eq", Values: []string{in.Name}})
	}
	if in.AttrKey != "" {
		col, set := measureAttr(in.AttrKey)
		dims, cols, where = append(dims, col), append(cols, "attr_value"), append(where, set)
	}
	cols = append(cols, "samples", "est_count", "mean", "p50", "p75", "p95")
	if !ok {
		return tableOut{Columns: cols, Rows: [][]string{}}, nil
	}
	one, three := 1, 3
	percentile := func(q float64, as string) native.Metric {
		return native.Metric{Op: "percentile", Q: q, Round: &three, As: as}
	}
	out, err := h.capped(ctx, native.Query{Kind: "breakdown", Table: "measures", Dimensions: dims, Order: "event_name",
		Where: where, Metrics: []native.Metric{{Op: "count", As: "samples"}, {Op: "weight", Round: &one, As: "est_count"},
			{Op: "mean", Round: &three, As: "mean"}, percentile(0.5, "p50"), percentile(0.75, "p75"), percentile(0.95, "p95")}}, p, 0)
	if err != nil {
		return tableOut{}, err
	}
	out.Columns = cols
	return out, nil
}

// measureAttr is the column attr_key reads on the measures and the filter
// that keeps the measures carrying it: a reserved key's typed column, set
// (empty read as no value), else the key's attr_<key> column, any value,
// an empty one included (prefix "" passes every value but NULL). A key no
// file carries reads NULL, so its breakdown is empty rather than an error.
func measureAttr(key string) (string, native.Filter) {
	if col, ok := native.ReservedKeyColumns[key]; ok {
		return col, native.Filter{Column: col, Op: "is_set"}
	}
	col := "attr_" + key
	return col, native.Filter{Column: col, Op: "prefix", Values: []string{""}}
}
