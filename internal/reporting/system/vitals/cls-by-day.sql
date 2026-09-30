-- CLS p75 per day: the first bucket whose running weight reaches 75% of
-- that day's total.
WITH h AS (
  SELECT day, bucket, approx_value, SUM(weight) AS w
  FROM v_measures_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to AND event_name = '$cls'
  GROUP BY day, bucket, approx_value
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
