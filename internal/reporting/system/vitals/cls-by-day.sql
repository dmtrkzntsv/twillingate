-- CLS p75 per day: the first bucket whose running weight (1 / sample_rate
-- a sample) reaches 75% of that day's total.
WITH h AS (
  SELECT day, bucket, CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
         SUM(1 / sample_rate) AS w
  FROM measures
  WHERE project_id = :project AND day BETWEEN :from AND :to AND event_name = '$cls'
  GROUP BY day, bucket
),
c AS (
  SELECT day, approx_value,
         SUM(w) OVER (PARTITION BY day ORDER BY bucket) AS run,
         SUM(w) OVER (PARTITION BY day) AS total
  FROM h
)
SELECT day AS x, ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * total), 3) AS y
FROM c
GROUP BY day
ORDER BY x
