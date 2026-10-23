-- Each time vital's p75 per day: the first bucket whose running weight
-- (1 / sample_rate a sample) reaches 75% of that day's total.
WITH h AS (
  SELECT day, event_name, bucket,
         CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
         SUM(1 / sample_rate) AS w
  FROM measures
  WHERE project_id = :project AND day BETWEEN :from AND :to
    AND event_name IN ('$lcp', '$inp', '$fcp', '$ttfb')
  GROUP BY day, event_name, bucket
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
