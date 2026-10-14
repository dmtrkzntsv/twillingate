-- Design A: the query set on SQLite's events table as it is today (no
-- secondary index, attributes as JSON). {from} {to} {actor} {common} {rare}
-- are substituted by `archive-bench sqlq`.

-- name: views_visitors_daily
SELECT day, COUNT(*) AS views, COUNT(DISTINCT actor_id) AS visitors
FROM events WHERE family = 'views' AND project_id = 2 AND day BETWEEN '{from}' AND '{to}'
GROUP BY day;

-- name: sessions_bounce
SELECT day, COUNT(*) AS sessions, SUM(n = 1) AS bounces, SUM(dur) AS duration_sec
FROM (
  SELECT day, actor_id, sk, COUNT(*) AS n, MAX(t) - MIN(t) AS dur
  FROM (
    SELECT day, actor_id, t, SUM(ns) OVER (PARTITION BY day, actor_id ORDER BY t ROWS UNBOUNDED PRECEDING) AS sk
    FROM (
      SELECT day, actor_id, t,
             CASE WHEN t - LAG(t) OVER (PARTITION BY day, actor_id ORDER BY t) <= 1800 THEN 0 ELSE 1 END AS ns
      FROM (SELECT day, actor_id, CAST(strftime('%s', ts) AS INTEGER) AS t
            FROM events WHERE family = 'views' AND project_id = 2 AND day BETWEEN '{from}' AND '{to}')
    )
  )
  GROUP BY day, actor_id, sk
)
GROUP BY day;

-- name: top_pages
SELECT path, COUNT(*) AS views, COUNT(DISTINCT actor_id) AS visitors
FROM events WHERE family = 'views' AND project_id = 2 AND day BETWEEN '{from}' AND '{to}'
GROUP BY path ORDER BY views DESC LIMIT 20;

-- name: product_events_daily
SELECT day, event_name, COUNT(*) AS n
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN '{from}' AND '{to}'
GROUP BY day, event_name;

-- name: dau
SELECT day, COUNT(DISTINCT user_id) AS users
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN '{from}' AND '{to}'
GROUP BY day;

-- name: distinct_users_range
SELECT COUNT(DISTINCT user_id)
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN '{from}' AND '{to}';

-- name: two_attrs
SELECT attributes ->> '$.access_state' AS access_state, attributes ->> '$.mode' AS mode,
       COUNT(*) AS n, COUNT(DISTINCT user_id) AS users
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN '{from}' AND '{to}'
GROUP BY 1, 2;

-- name: two_attrs_filter
SELECT event_name, COUNT(*) AS n, COUNT(DISTINCT user_id) AS users
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN '{from}' AND '{to}'
  AND attributes ->> '$.access_state' = 'trial' AND attributes ->> '$.deployment' = 'self-hosted'
GROUP BY event_name ORDER BY n DESC LIMIT 20;

-- name: attr_x_country
SELECT attributes ->> '$.access_state' AS access_state, country,
       COUNT(*) AS n, COUNT(DISTINCT user_id) AS users
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN '{from}' AND '{to}'
GROUP BY 1, 2;

-- name: retention_weekly
WITH a AS (
  SELECT DISTINCT user_id, CAST((julianday(day) - julianday('{from}')) / 7 AS INTEGER) AS w
  FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN '{from}' AND '{to}'
)
SELECT c AS cohort, w - c AS week, COUNT(*) AS users
FROM (SELECT w, MIN(w) OVER (PARTITION BY user_id) AS c FROM a)
GROUP BY 1, 2;

-- name: actor_timeline
SELECT ts, family, event_name, path
FROM events WHERE family IN ('product', 'views') AND project_id = 1 AND day BETWEEN '{from}' AND '{to}'
  AND actor_id = '{actor}'
ORDER BY ts;

-- name: first_common
SELECT MIN(ts) FROM events
WHERE family = 'product' AND project_id = 1 AND day BETWEEN '{from}' AND '{to}' AND event_name = '{common}';

-- name: first_rare
SELECT MIN(ts) FROM events
WHERE family = 'product' AND project_id = 1 AND day BETWEEN '{from}' AND '{to}' AND event_name = '{rare}';
