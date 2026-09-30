-- p75 of $cls in the range and in the period of the same length
-- before it: the first bucket whose running weight reaches 75% of the
-- total (docs/reporting.md, "Percentiles from measures").
WITH cur AS (
  SELECT bucket, approx_value, SUM(weight) AS w FROM v_measures_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to AND event_name = '$cls'
  GROUP BY bucket, approx_value),
prev AS (
  SELECT bucket, approx_value, SUM(weight) AS w FROM v_measures_daily
  WHERE project_id = :project AND event_name = '$cls'
    AND day BETWEEN date(:from, '-' || (julianday(:to) - julianday(:from) + 1) || ' days') AND date(:from, '-1 day')
  GROUP BY bucket, approx_value),
c AS (SELECT approx_value, SUM(w) OVER (ORDER BY bucket) AS run, SUM(w) OVER () AS total FROM cur),
p AS (SELECT approx_value, SUM(w) OVER (ORDER BY bucket) AS run, SUM(w) OVER () AS total FROM prev)
SELECT (SELECT ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * total), 3) FROM c) AS value,
       (SELECT ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * total), 3) FROM p) AS previous
