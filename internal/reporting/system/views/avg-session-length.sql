-- Reads agg_views_daily and raw_views, the two halves v_views_daily
-- stitches, rather than the view itself: the view's live half sessionizes
-- every raw view of every project before any filter applies.
-- A view with a session id belongs to that session; without one, a gap of
-- more than 30 minutes since the actor's previous view that day starts a
-- new one.
WITH src AS (
  SELECT day, kind, actor_id, session_id, CAST(strftime('%s', ts) AS INTEGER) AS t
  FROM raw_views
  WHERE project_id = :project AND day BETWEEN :from AND :to
),
marked AS (
  SELECT day, kind, actor_id, session_id, t,
         CASE WHEN session_id <> '' THEN 0
              WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
              ELSE 0 END AS new_session
  FROM src WINDOW w AS (PARTITION BY day, kind, actor_id ORDER BY t)
),
keyed AS (
  SELECT day, kind, actor_id, t,
         CASE WHEN session_id <> '' THEN session_id
              ELSE CAST(SUM(new_session) OVER (PARTITION BY day, kind, actor_id ORDER BY t) AS TEXT)
         END AS skey
  FROM marked
),
spans AS (
  SELECT day, kind, COUNT(*) AS view_count, MAX(t) - MIN(t) AS dur
  FROM keyed
  GROUP BY day, kind, actor_id, skey
),
daily AS (
  SELECT day, kind, sessions, bounces, duration_sec
  FROM agg_views_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to
  UNION ALL
  SELECT day, kind, COUNT(*), SUM(CASE WHEN view_count = 1 THEN 1 ELSE 0 END), COALESCE(SUM(dur), 0)
  FROM spans
  GROUP BY day, kind
)
SELECT day AS x,
       CASE WHEN SUM(sessions) > 0 THEN SUM(duration_sec) * 1.0 / SUM(sessions) ELSE 0 END AS y
FROM daily
GROUP BY day
ORDER BY day
