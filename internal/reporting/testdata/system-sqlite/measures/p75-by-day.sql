-- Each metric's p75 per day (the reserved $ ones are on Web Vitals): the
-- first bucket whose running weight reaches 75% of that day's total. A
-- name sent as two kinds is two series.
WITH h AS (
  SELECT day, event_name, measure, bucket, approx_value, SUM(weight) AS w
  FROM v_measures_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to
    AND event_name NOT LIKE '$%'
  GROUP BY day, event_name, measure, bucket, approx_value
),
c AS (
  SELECT day, event_name, measure, approx_value,
         SUM(w) OVER (PARTITION BY day, event_name, measure ORDER BY bucket) AS run,
         SUM(w) OVER (PARTITION BY day, event_name, measure) AS total
  FROM h
),
kinds AS (
  SELECT event_name, COUNT(DISTINCT measure) AS n FROM h GROUP BY event_name
)
SELECT c.day AS x,
       CASE WHEN k.n > 1 THEN c.event_name || ' (' || c.measure || ')' ELSE c.event_name END AS series,
       ROUND(MIN(c.approx_value) FILTER (WHERE c.run >= 0.75 * c.total), 3) AS y
FROM c JOIN kinds k ON k.event_name = c.event_name
GROUP BY c.day, c.event_name, c.measure
ORDER BY x, series
