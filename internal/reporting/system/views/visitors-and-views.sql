-- Visitors are distinct per day and kind, added up over a day's kinds.
WITH daily AS (
  SELECT day, kind, count(DISTINCT actor_id) AS visitors, count(*) AS views
  FROM views
  WHERE project_id = :project AND day BETWEEN :from AND :to
  GROUP BY day, kind
),
totals AS (
  SELECT day, SUM(visitors) AS visitors, SUM(views) AS views
  FROM daily
  GROUP BY day
)
SELECT day AS x, 'Visitors' AS series, visitors AS y FROM totals
UNION ALL
SELECT day, 'Views', views FROM totals
ORDER BY x, series
