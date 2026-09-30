package api

import (
	"context"
	"strings"
)

type measuresIn struct {
	rangeIn
	Name    string `json:"name,omitempty" jsonschema:"filter to one metric name"`
	AttrKey string `json:"attr_key,omitempty" jsonschema:"break each metric down by this attribute key: a system key such as $browser, or a key the project declares"`
}

// measures answers percentiles from the log-bucket histograms in
// v_measures_daily (or v_measures_attrs when attr_key is set): each
// bucket's weight is the estimated true count, so sampled rows count at
// full size (design decisions 18-20).
func (h *host) measures(ctx context.Context, in measuresIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	p := h.reg.Snapshot(ctx).Project(in.ProjectID)
	if p == nil {
		return tableOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	src, dim, by := "v_measures_daily", "", ""
	args := []any{in.ProjectID, in.From, in.To}
	if in.AttrKey != "" {
		src, dim, by = "v_measures_attrs", " AND attr_key = ?", "attr_value, "
		args = append(args, in.AttrKey)
	}
	if in.Name != "" {
		dim += " AND event_name = ?"
		args = append(args, in.Name)
	}
	// partition is the PARTITION BY / final GROUP BY key beyond event_name,
	// measure: attr_value too when attr_key narrows to one breakdown.
	partition := strings.TrimSuffix(", "+by, ", ")
	q := `WITH h AS (
	  SELECT event_name, measure, ` + by + `bucket, approx_value,
	         SUM(samples) AS s, SUM(weight) AS w, SUM(sum) AS total
	  FROM ` + src + ` WHERE project_id = ? AND day BETWEEN ? AND ?` + dim + `
	  GROUP BY event_name, measure, ` + by + `bucket, approx_value),
	c AS (
	  SELECT *, SUM(w) OVER (PARTITION BY event_name, measure` + partition + ` ORDER BY bucket) AS run,
	            SUM(w) OVER (PARTITION BY event_name, measure` + partition + `) AS all_w
	  FROM h)
	SELECT event_name, measure, ` + by + `SUM(s) AS samples, ROUND(SUM(w), 1) AS est_count,
	       ROUND(SUM(total) / SUM(w), 3) AS mean,
	       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.50 * all_w), 3) AS p50,
	       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * all_w), 3) AS p75,
	       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.95 * all_w), 3) AS p95
	FROM c GROUP BY event_name, measure` + partition + `
	ORDER BY event_name, measure` + partition
	return h.table(ctx, q, args...)
}
