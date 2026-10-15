-- Reads agg_views_daily and raw_views, the two halves v_views_daily
-- stitches, rather than the view itself: the view's live half sessionizes
-- every raw view of every project before any filter applies.
WITH daily AS (
  SELECT day, kind, visitors, views
  FROM agg_views_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to
  UNION ALL
  SELECT day, kind, COUNT(DISTINCT actor_id), COUNT(*)
  FROM raw_views
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
