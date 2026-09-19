-- AggregateIdentityDay keeps only the busiest 500 ids per kind and day
-- (ranked by views + events, then id) and drops the rest, with no (other)
-- row. The live half had no cap, so a project with more than 500 active
-- users or groups in a day lost rows the moment the day rolled up. The live
-- half now ranks and cuts the same way.
--
-- Everything else is unchanged from 012: the aggregate is preferred over
-- raw for a day that has both, and today is always served live.
DROP VIEW v_identity_daily;

CREATE VIEW v_identity_daily AS
SELECT project, day, kind, id, actors, users, views, events
FROM agg_identity_daily
UNION ALL
SELECT project, day, kind, id, actors, users, views, events
FROM (
  SELECT project, day, kind, id,
         COUNT(DISTINCT actor_id) AS actors,
         CASE WHEN kind = 'user' THEN 1 ELSE COUNT(DISTINCT NULLIF(user_id, '')) END AS users,
         SUM(is_view) AS views, SUM(is_event) AS events,
         ROW_NUMBER() OVER (PARTITION BY project, day, kind ORDER BY COUNT(*) DESC, id) AS rn
  FROM (
    SELECT project, day, 'user' AS kind, user_id AS id,
           actor_id, user_id, 1 AS is_view, 0 AS is_event
    FROM views WHERE user_id <> ''
    UNION ALL
    SELECT project, substr(ts,1,10), 'user', user_id, actor_id, user_id, 0, 1
    FROM product_events WHERE user_id <> ''
    UNION ALL
    SELECT project, day, 'group', group_id, actor_id, user_id, 1, 0
    FROM views WHERE group_id <> ''
    UNION ALL
    SELECT project, substr(ts,1,10), 'group', group_id, actor_id, user_id, 0, 1
    FROM product_events WHERE group_id <> ''
  )
  GROUP BY project, day, kind, id
) live
WHERE rn <= 500
  AND NOT EXISTS (
    SELECT 1 FROM agg_identity_daily g
    WHERE g.project = live.project AND g.day = live.day);
