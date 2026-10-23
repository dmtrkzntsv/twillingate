-- p75 of $ttfb in the range: the first bucket whose running weight
-- (1 / sample_rate a sample) reaches 75% of the total (docs/reporting.md,
-- "Percentiles from measures"). No row when the range has no samples, so
-- the card is blank rather than a perfect 0. No previous period: stat
-- shows a rise as good, and a vital is better lower.
WITH cur AS (
  SELECT bucket, CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
         SUM(1 / sample_rate) AS w
  FROM measures
  WHERE project_id = :project AND day BETWEEN :from AND :to AND event_name = '$ttfb'
  GROUP BY bucket),
c AS (SELECT approx_value, SUM(w) OVER (ORDER BY bucket) AS run, SUM(w) OVER () AS total FROM cur)
SELECT ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * total)) AS value
FROM c
HAVING count(*) > 0
