-- Reads agg_views_daily and raw_views, the two halves v_views_daily
-- stitches, rather than the view itself: the view's live half sessionizes
-- every raw view of every project before any filter applies.
WITH daily AS (
  SELECT day, kind, visitors, views
  FROM agg_views_daily
  WHERE project_id = :project AND day BETWEEN date(:from, '-' || (julianday(:to) - julianday(:from) + 1) || ' days') AND :to
  UNION ALL
  SELECT day, kind, COUNT(DISTINCT actor_id), COUNT(*)
  FROM raw_views
  WHERE project_id = :project AND day BETWEEN date(:from, '-' || (julianday(:to) - julianday(:from) + 1) || ' days') AND :to
  GROUP BY day, kind
)
SELECT COALESCE(SUM(CASE WHEN day >= :from THEN visitors END), 0) AS value,
       COALESCE(SUM(CASE WHEN day < :from THEN visitors END), 0) AS previous
FROM daily
