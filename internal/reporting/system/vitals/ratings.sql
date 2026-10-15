-- Each vital's share of samples (by weight, 1 / sample_rate a sample) that
-- rate good, needs improvement or poor against Google's thresholds, which
-- live only here (they change: INP replaced FID in 2024), typed DOUBLE as
-- the values they are compared with. A sample is rated by its bucket's
-- value, as the percentiles read it.
WITH t(name, good, poor) AS (VALUES ('$lcp', 2500::DOUBLE, 4000::DOUBLE), ('$inp', 200, 500), ('$cls', 0.1, 0.25), ('$fcp', 1800, 3000), ('$ttfb', 800, 1800)),
m AS (
  SELECT event_name, CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
         1 / sample_rate AS w
  FROM measures
  WHERE project_id = :project AND day BETWEEN :from AND :to
),
r AS (
  SELECT upper(substr(m.event_name, 2)) AS x,
         CASE WHEN m.approx_value <= t.good THEN 'good' WHEN m.approx_value <= t.poor THEN 'needs improvement' ELSE 'poor' END AS series,
         SUM(m.w) AS w
  FROM m JOIN t ON t.name = m.event_name
  GROUP BY 1, 2)
SELECT x, series, w / SUM(w) OVER (PARTITION BY x) AS y FROM r ORDER BY x, series
