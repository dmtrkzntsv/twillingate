-- Actions are the views and product events sent with the user_id. First
-- seen is the first event the files hold for the user (identities), which
-- also names it.
WITH d AS (
  SELECT user_id AS id, day,
         count(*) FILTER (WHERE family = 'views') AS views,
         count(*) FILTER (WHERE family = 'product') AS events
  FROM events
  WHERE project_id = :project AND family IN ('views', 'product') AND user_id <> ''
    AND day BETWEEN :from AND :to
  GROUP BY user_id, day
)
SELECT COALESCE(i.name, d.id) AS "User",
       SUM(d.views + d.events) AS "Actions",
       SUM(d.events) AS "Events",
       SUM(d.views) AS "Views",
       count(DISTINCT d.day) AS "Active days",
       MIN(i.first_seen::DATE) AS "First seen",
       MAX(d.day) AS "Last seen"
FROM d
LEFT JOIN identities i ON i.project_id = :project AND i.kind = 'user' AND i.id = d.id
GROUP BY d.id, i.name
ORDER BY SUM(d.views + d.events) DESC, d.id
LIMIT 100
