-- A user's first day is the first event the files hold for it (identities):
-- "new" means new to that history, so a user returning after it has aged out
-- counts as new.
SELECT count(DISTINCT e.user_id) FILTER (WHERE e.day = i.first_seen::DATE) AS value
FROM events e
JOIN identities i ON i.kind = 'user' AND i.id = e.user_id
WHERE e.project_id = :project AND e.family IN ('views', 'product') AND e.user_id <> ''
  AND e.day BETWEEN :from AND :to
