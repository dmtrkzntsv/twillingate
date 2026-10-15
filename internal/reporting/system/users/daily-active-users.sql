-- A user's first day is the first event the files hold for it (identities):
-- "new" means new to that history, so a user returning after it has aged out
-- counts as new.
WITH daily AS (
  SELECT e.day,
         count(DISTINCT e.user_id) FILTER (WHERE e.day = i.first_seen::DATE) AS new_count,
         count(DISTINCT e.user_id) FILTER (WHERE e.day > i.first_seen::DATE) AS returning_count
  FROM events e
  JOIN identities i ON i.kind = 'user' AND i.id = e.user_id
  WHERE e.project_id = :project AND e.family IN ('views', 'product') AND e.user_id <> ''
    AND e.day BETWEEN :from AND :to
  GROUP BY e.day
)
SELECT day AS x, 'New users' AS series, new_count AS y FROM daily
UNION ALL
SELECT day, 'Returning users', returning_count FROM daily
ORDER BY x, series
