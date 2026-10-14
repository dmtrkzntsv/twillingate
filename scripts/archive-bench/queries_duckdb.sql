-- Design C: the same query set on DuckDB's events table (a DuckDB file, or a
-- view over the monthly Parquet files), with one column per attribute key
-- (D4). Placeholders as in queries_sqlite.sql; run by `archive-bench duckq`.

-- name: views_visitors_daily
SELECT day, COUNT(*) AS views, COUNT(DISTINCT actor_id) AS visitors
FROM events WHERE family = 'views' AND project_id = 2 AND day BETWEEN DATE '{from}' AND DATE '{to}'
GROUP BY day;

-- name: sessions_bounce
SELECT day, COUNT(*) AS sessions, SUM(CASE WHEN n = 1 THEN 1 ELSE 0 END) AS bounces, SUM(dur) AS duration_sec
FROM (
  SELECT day, actor_id, sk, COUNT(*) AS n, MAX(t) - MIN(t) AS dur
  FROM (
    SELECT day, actor_id, t, SUM(ns) OVER (PARTITION BY day, actor_id ORDER BY t ROWS UNBOUNDED PRECEDING) AS sk
    FROM (
      SELECT day, actor_id, t,
             CASE WHEN t - LAG(t) OVER (PARTITION BY day, actor_id ORDER BY t) <= 1800 THEN 0 ELSE 1 END AS ns
      FROM (SELECT day, actor_id, epoch(ts) AS t
            FROM events WHERE family = 'views' AND project_id = 2 AND day BETWEEN DATE '{from}' AND DATE '{to}')
    )
  )
  GROUP BY day, actor_id, sk
)
GROUP BY day;

-- name: top_pages
SELECT path, COUNT(*) AS views, COUNT(DISTINCT actor_id) AS visitors
FROM events WHERE family = 'views' AND project_id = 2 AND day BETWEEN DATE '{from}' AND DATE '{to}'
GROUP BY path ORDER BY views DESC LIMIT 20;

-- name: product_events_daily
SELECT day, event_name, COUNT(*) AS n
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}'
GROUP BY day, event_name;

-- name: dau
SELECT day, COUNT(DISTINCT user_id) AS users
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}'
GROUP BY day;

-- name: distinct_users_range
SELECT COUNT(DISTINCT user_id)
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}';

-- name: two_attrs
SELECT attr_access_state, attr_mode, COUNT(*) AS n, COUNT(DISTINCT user_id) AS users
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}'
GROUP BY 1, 2;

-- name: two_attrs_filter
SELECT event_name, COUNT(*) AS n, COUNT(DISTINCT user_id) AS users
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}'
  AND attr_access_state = 'trial' AND attr_deployment = 'self-hosted'
GROUP BY event_name ORDER BY n DESC LIMIT 20;

-- name: attr_x_country
SELECT attr_access_state, country, COUNT(*) AS n, COUNT(DISTINCT user_id) AS users
FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}'
GROUP BY 1, 2;

-- name: retention_weekly
WITH a AS (
  SELECT DISTINCT user_id, (day - DATE '{from}') // 7 AS w
  FROM events WHERE family = 'product' AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}'
)
SELECT c AS cohort, w - c AS week, COUNT(*) AS users
FROM (SELECT w, MIN(w) OVER (PARTITION BY user_id) AS c FROM a)
GROUP BY 1, 2;

-- name: actor_timeline
SELECT ts, family, event_name, path
FROM events WHERE family IN ('product', 'views') AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}'
  AND actor_id = '{actor}'
ORDER BY ts;

-- name: first_common
SELECT MIN(ts) FROM events
WHERE family = 'product' AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}' AND event_name = '{common}';

-- name: first_rare
SELECT MIN(ts) FROM events
WHERE family = 'product' AND project_id = 1 AND day BETWEEN DATE '{from}' AND DATE '{to}' AND event_name = '{rare}';
