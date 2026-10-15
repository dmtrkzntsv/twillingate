-- A group's first day is the first event the files hold for it (identities):
-- "new" means new to that history, so a group returning after it has aged out
-- counts as new.
WITH daily AS (
  SELECT e.day,
         count(DISTINCT e.group_id) FILTER (WHERE e.day = i.first_seen::DATE) AS new_count,
         count(DISTINCT e.group_id) FILTER (WHERE e.day > i.first_seen::DATE) AS returning_count
  FROM events e
  JOIN identities i ON i.project_id = :project AND i.kind = 'group' AND i.id = e.group_id
  WHERE e.project_id = :project AND e.family IN ('views', 'product') AND e.group_id <> ''
    AND e.day BETWEEN :from AND :to
  GROUP BY e.day
)
SELECT day AS x, 'New groups' AS series, new_count AS y FROM daily
UNION ALL
SELECT day, 'Returning groups', returning_count FROM daily
ORDER BY x, series
