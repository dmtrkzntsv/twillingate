-- First day each user appears in the retained history. "New" means new to
-- that history: a user returning after it has aged out counts as new.
WITH firsts AS (
  SELECT id, MIN(day) AS first_day
  FROM v_identity_daily
  WHERE project_id = :project AND kind = 'user' AND id != ''
  GROUP BY id
),
daily AS (
  SELECT d.day,
         COUNT(DISTINCT CASE WHEN d.day = f.first_day THEN d.id END) AS new_count,
         COUNT(DISTINCT CASE WHEN d.day > f.first_day THEN d.id END) AS returning_count
  FROM v_identity_daily d
  JOIN firsts f ON f.id = d.id
  WHERE d.project_id = :project AND d.kind = 'user' AND d.id != ''
    AND d.day BETWEEN :from AND :to
  GROUP BY d.day
)
SELECT day AS x, 'New users' AS series, new_count AS y FROM daily
UNION ALL
SELECT day, 'Returning users', returning_count FROM daily
ORDER BY x, series
