-- p75 of $cls in the range: the first bucket whose running weight
-- reaches 75% of the total (docs/reporting.md, "Percentiles from
-- measures"). No row when the range has no samples, so the card is blank
-- rather than a perfect 0. No previous period: stat shows a rise as good,
-- and a vital is better lower.
WITH cur AS (
  SELECT bucket, approx_value, SUM(weight) AS w FROM v_measures_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to AND event_name = '$cls'
  GROUP BY bucket, approx_value),
c AS (SELECT approx_value, SUM(w) OVER (ORDER BY bucket) AS run, SUM(w) OVER () AS total FROM cur)
SELECT ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * total), 3) AS value
FROM c
HAVING COUNT(*) > 0
