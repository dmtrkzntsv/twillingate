-- v_identity_daily counted every recent day twice. Unlike the other stitch
-- views, its aggregate is written while the day's raw rows still exist: the
-- daily pass rolls identity up over every raw day (jobs.go), and only
-- AggregateAppDay deletes raw -- web hits and product events stay for their
-- own raw window. The live half therefore has to skip days the aggregate
-- already holds, rather than rely on the two being disjoint.
--
-- The aggregate is preferred over raw for a day that has both: raw windows
-- differ per table, so once web hits for a day are pruned but its product
-- events are not, the live half would silently lose the web share. The
-- daily pass does not roll up today, so today is always served live.
DROP VIEW v_identity_daily;

CREATE VIEW v_identity_daily AS
SELECT project, day, kind, id, actors, users, hits, views, events
FROM agg_identity_daily
UNION ALL
SELECT project, day, kind, id, actors, users, hits, views, events
FROM (
  SELECT project, day, kind, id,
         COUNT(DISTINCT actor_id) AS actors,
         CASE WHEN kind = 'user' THEN 1 ELSE COUNT(DISTINCT NULLIF(user_id, '')) END AS users,
         SUM(is_hit) AS hits, SUM(is_view) AS views, SUM(is_event) AS events
  FROM (
    SELECT project, substr(ts,1,10) AS day, 'user' AS kind, user_id AS id,
           actor_id, user_id, 1 AS is_hit, 0 AS is_view, 0 AS is_event
    FROM web_hits WHERE user_id <> ''
    UNION ALL
    SELECT project, substr(ts,1,10), 'user', user_id, actor_id, user_id, 0, 1, 0
    FROM app_views WHERE user_id <> ''
    UNION ALL
    SELECT project, substr(ts,1,10), 'user', user_id, actor_id, user_id, 0, 0, 1
    FROM product_events WHERE user_id <> ''
    UNION ALL
    SELECT project, substr(ts,1,10), 'group', group_id, actor_id, user_id, 1, 0, 0
    FROM web_hits WHERE group_id <> ''
    UNION ALL
    SELECT project, substr(ts,1,10), 'group', group_id, actor_id, user_id, 0, 1, 0
    FROM app_views WHERE group_id <> ''
    UNION ALL
    SELECT project, substr(ts,1,10), 'group', group_id, actor_id, user_id, 0, 0, 1
    FROM product_events WHERE group_id <> ''
  )
  GROUP BY project, day, kind, id
) live
WHERE NOT EXISTS (
  SELECT 1 FROM agg_identity_daily g
  WHERE g.project = live.project AND g.day = live.day);

-- Earlier passes rolled up today as well, from whatever had arrived by 03:00.
-- Left in place, that partial row would hide today's live half until
-- tomorrow's pass. Yesterday is left alone even though it is partial too
-- when this runs before today's pass: with raw_days=0 its raw rows may
-- already be gone, and the next pass completes it within hours.
DELETE FROM agg_identity_daily WHERE day >= date('now');
