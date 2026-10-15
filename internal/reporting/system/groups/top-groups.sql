-- Actions are the views and product events sent with the group_id. Users
-- are distinct per day, so the range figure is the busiest day's: the same
-- person on two days cannot be told apart from two people. First seen is
-- the first event the files hold for the group (identities), which also
-- names it.
WITH d AS (
  SELECT group_id AS id, day, count(*) AS actions, count(DISTINCT nullif(user_id, '')) AS users
  FROM events
  WHERE project_id = :project AND family IN ('views', 'product') AND group_id <> ''
    AND day BETWEEN :from AND :to
  GROUP BY group_id, day
)
SELECT COALESCE(i.name, d.id) AS "Group",
       SUM(d.actions) AS "Actions",
       MAX(d.users) AS "Peak daily users",
       count(DISTINCT d.day) AS "Active days",
       MIN(i.first_seen::DATE) AS "First seen",
       MAX(d.day) AS "Last seen"
FROM d
JOIN identities i ON i.kind = 'group' AND i.id = d.id
GROUP BY d.id, i.name
ORDER BY SUM(d.actions) DESC, d.id
LIMIT 100
