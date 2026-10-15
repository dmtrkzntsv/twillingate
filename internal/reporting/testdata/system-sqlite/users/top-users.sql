-- First day each user appears in the retained history.
WITH firsts AS (
  SELECT id, MIN(day) AS first_day
  FROM v_identity_daily
  WHERE project_id = :project AND kind = 'user' AND id != ''
  GROUP BY id
)
SELECT COALESCE(i.name, d.id) AS "User",
       SUM(d.views + d.events) AS "Actions",
       SUM(d.events) AS "Events",
       SUM(d.views) AS "Views",
       COUNT(DISTINCT d.day) AS "Active days",
       MIN(f.first_day) AS "First seen",
       MAX(d.day) AS "Last seen"
FROM v_identity_daily d
JOIN firsts f ON f.id = d.id
LEFT JOIN identities i
  ON i.project_id = d.project_id AND i.kind = 'user' AND i.id = d.id
WHERE d.project_id = :project AND d.kind = 'user' AND d.id != ''
  AND d.day BETWEEN :from AND :to
GROUP BY d.id, i.name
ORDER BY SUM(d.views + d.events) DESC
LIMIT 100
