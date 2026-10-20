-- Each time vital's p75 per day: the first bucket whose running weight
-- reaches 75% of that day's total.
WITH h AS (
  SELECT day, event_name, bucket, approx_value, SUM(weight) AS w
  FROM v_measures_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to
    AND event_name IN ('$lcp', '$inp', '$fcp', '$ttfb')
  GROUP BY day, event_name, bucket, approx_value
),
c AS (
  SELECT day, event_name, approx_value,
         SUM(w) OVER (PARTITION BY day, event_name ORDER BY bucket) AS run,
         SUM(w) OVER (PARTITION BY day, event_name) AS total
  FROM h
)
SELECT day AS x, upper(substr(event_name, 2)) AS series,
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * total)) AS y
FROM c
GROUP BY day, event_name
ORDER BY x, series
