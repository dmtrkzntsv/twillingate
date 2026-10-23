-- Each metric's p75 per day (the reserved $ ones are on Web Vitals): the
-- first bucket whose running weight (1 / sample_rate a sample) reaches 75%
-- of that day's total. A name sent as two kinds is two series.
WITH h AS (
  SELECT day, event_name, measure, bucket,
         CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
         SUM(1 / sample_rate) AS w
  FROM measures
  WHERE project_id = :project AND day BETWEEN :from AND :to
    AND event_name NOT LIKE '$%'
  GROUP BY day, event_name, measure, bucket
),
c AS (
  SELECT day, event_name, measure, approx_value,
         SUM(w) OVER (PARTITION BY day, event_name, measure ORDER BY bucket) AS run,
         SUM(w) OVER (PARTITION BY day, event_name, measure) AS total
  FROM h
),
kinds AS (
  SELECT event_name, count(DISTINCT measure) AS n FROM h GROUP BY event_name
)
SELECT c.day AS x,
       CASE WHEN k.n > 1 THEN c.event_name || ' (' || c.measure || ')' ELSE c.event_name END AS series,
       ROUND(MIN(c.approx_value) FILTER (WHERE c.run >= 0.75 * c.total), 3) AS y
FROM c JOIN kinds k ON k.event_name = c.event_name
GROUP BY c.day, c.event_name, c.measure, k.n
ORDER BY x, series
