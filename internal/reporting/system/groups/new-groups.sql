-- First day each group appears in the retained history. "New" means new to
-- that history: a group returning after it has aged out counts as new.
WITH firsts AS (
  SELECT id, MIN(day) AS first_day
  FROM v_identity_daily
  WHERE project_id = :project AND kind = 'group' AND id != ''
  GROUP BY id
)
SELECT COUNT(DISTINCT CASE WHEN f.first_day = d.day THEN d.id END) AS value
FROM v_identity_daily d
JOIN firsts f ON f.id = d.id
WHERE d.project_id = :project AND d.kind = 'group' AND d.id != ''
  AND d.day BETWEEN :from AND :to
