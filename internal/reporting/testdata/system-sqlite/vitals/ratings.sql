-- Each vital's share of samples (by weight) that rate good, needs
-- improvement or poor against Google's thresholds, which live only here
-- (they change: INP replaced FID in 2024).
WITH t(name, good, poor) AS (VALUES ('$lcp', 2500, 4000), ('$inp', 200, 500), ('$cls', 0.1, 0.25), ('$fcp', 1800, 3000), ('$ttfb', 800, 1800)),
r AS (
  SELECT upper(substr(d.event_name, 2)) AS x,
         CASE WHEN d.approx_value <= t.good THEN 'good' WHEN d.approx_value <= t.poor THEN 'needs improvement' ELSE 'poor' END AS series,
         SUM(d.weight) AS w
  FROM v_measures_daily d JOIN t ON t.name = d.event_name
  WHERE d.project_id = :project AND d.day BETWEEN :from AND :to
  GROUP BY 1, 2)
SELECT x, series, w / SUM(w) OVER (PARTITION BY x) AS y FROM r ORDER BY x, series
