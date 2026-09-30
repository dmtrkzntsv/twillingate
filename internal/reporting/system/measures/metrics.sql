-- Every metric but the reserved $ ones (the Web Vitals dashboard has
-- those), one row per name and kind. time is in milliseconds, size in
-- bytes, number unitless. Mean is exact (sum over weight); each
-- percentile is the first bucket whose running weight reaches that share
-- of the total, within about 2%.
WITH h AS (
  SELECT event_name, measure, bucket, approx_value,
         SUM(samples) AS samples, SUM(weight) AS w, SUM(sum) AS s
  FROM v_measures_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to
    AND event_name NOT LIKE '$%'
  GROUP BY event_name, measure, bucket, approx_value
),
c AS (
  SELECT event_name, measure, samples, w, s, approx_value,
         SUM(w) OVER (PARTITION BY event_name, measure ORDER BY bucket) AS run,
         SUM(w) OVER (PARTITION BY event_name, measure) AS total
  FROM h
)
SELECT event_name AS "Metric", measure AS "Kind", SUM(samples) AS "Samples",
       ROUND(SUM(s) / SUM(w), 3) AS "Mean",
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.5 * total), 3) AS "p50",
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * total), 3) AS "p75",
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.95 * total), 3) AS "p95"
FROM c
GROUP BY event_name, measure
ORDER BY 3 DESC, 1, 2
LIMIT 100
