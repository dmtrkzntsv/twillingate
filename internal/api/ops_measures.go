package api

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
)

type measuresIn struct {
	rangeIn
	Name    string `json:"name,omitempty" jsonschema:"filter to one metric name"`
	AttrKey string `json:"attr_key,omitempty" jsonschema:"break each metric down by this attribute key: a reserved key such as $browser or $path, or any key the measures carried; no declaration needed"`
}

// measures answers percentiles from the log-scale buckets of the measures
// table, as the Measures dashboard does (its percentile metric; the old
// SQL is reporting's testdata/system-sql/measures/metrics.sql): each
// sample weighs 1 / sample_rate, so sampled rows count at full size
// (design decisions 18-20), and a percentile is the first bucket whose
// running weight reaches its share of the total.
func (h *host) measures(ctx context.Context, in measuresIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	p := h.reg.Snapshot(ctx).Project(in.ProjectID)
	if p == nil {
		return tableOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	// by and partition add attr_value to the grouping when attr_key narrows
	// to one breakdown.
	// One deadline for the column lookup and the query together.
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	dim, by, partition := "", "", ""
	if in.AttrKey != "" {
		value, err := h.measureAttr(ctx, in.AttrKey)
		if err != nil {
			return tableOut{}, err
		}
		dim, by, partition = " AND "+value+" IS NOT NULL", value+" AS attr_value, ", ", attr_value"
	}
	cond, vals := eventFilter(in.Name)
	q := `WITH h AS (
  SELECT event_name, measure, ` + by + `bucket,
         CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
         count(*) AS s, SUM(1 / sample_rate) AS w, SUM(value / sample_rate) AS total
  FROM measures
  WHERE project_id = :project AND day BETWEEN :from AND :to` + cond + dim + `
  GROUP BY ALL
),
c AS (
  SELECT *, SUM(w) OVER (PARTITION BY event_name, measure` + partition + ` ORDER BY bucket) AS run,
            SUM(w) OVER (PARTITION BY event_name, measure` + partition + `) AS all_w
  FROM h
)
SELECT event_name, measure` + partition + `, SUM(s) AS samples, ROUND(SUM(w), 1) AS est_count,
       ROUND(SUM(total) / SUM(w), 3) AS mean,
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.50 * all_w), 3) AS p50,
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * all_w), 3) AS p75,
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.95 * all_w), 3) AS p95
FROM c GROUP BY event_name, measure` + partition + `
ORDER BY event_name, measure` + partition
	return h.filesTable(ctx, in.rangeIn, q, vals)
}

// measureAttr is the SQL value attr_key reads on the measures table: a
// system key's typed column (empty read as NULL), else the key's attr_<key>
// column, quoted. A key no file carries reads NULL, so its breakdown is
// empty rather than an error.
func (h *host) measureAttr(ctx context.Context, key string) (string, error) {
	if col, ok := native.ReservedKeyColumns[key]; ok {
		return "nullif(" + col + ", '')", nil
	}
	res, err := h.runFiles(ctx, archive.Scoped{SQL: "SELECT column_name FROM (DESCRIBE SELECT * FROM measures)"})
	if err != nil {
		return "", err
	}
	cols := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		if c, ok := row[0].(string); ok {
			cols = append(cols, c)
		}
	}
	if col, ok := enginesql.AttrColumn(cols, key); ok {
		return col, nil
	}
	return "CAST(NULL AS VARCHAR)", nil
}
