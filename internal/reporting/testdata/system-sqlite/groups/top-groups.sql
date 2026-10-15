-- First day each group appears in the retained history. users is distinct
-- per day, so the range figure is the busiest day's: the same person on two
-- days cannot be told apart from two people.
WITH firsts AS (
  SELECT id, MIN(day) AS first_day
  FROM v_identity_daily
  WHERE project_id = :project AND kind = 'group' AND id != ''
  GROUP BY id
)
SELECT COALESCE(i.name, d.id) AS "Group",
       SUM(d.views + d.events) AS "Actions",
       MAX(d.users) AS "Peak daily users",
       COUNT(DISTINCT d.day) AS "Active days",
       MIN(f.first_day) AS "First seen",
       MAX(d.day) AS "Last seen"
FROM v_identity_daily d
JOIN firsts f ON f.id = d.id
LEFT JOIN identities i
  ON i.project_id = d.project_id AND i.kind = 'group' AND i.id = d.id
WHERE d.project_id = :project AND d.kind = 'group' AND d.id != ''
  AND d.day BETWEEN :from AND :to
GROUP BY d.id, i.name
ORDER BY SUM(d.views + d.events) DESC
LIMIT 100
