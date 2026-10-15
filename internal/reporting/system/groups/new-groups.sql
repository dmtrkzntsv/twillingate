-- A group's first day is the first event the files hold for it (identities):
-- "new" means new to that history, so a group returning after it has aged out
-- counts as new.
SELECT count(DISTINCT e.group_id) FILTER (WHERE e.day = i.first_seen::DATE) AS value
FROM events e
JOIN identities i ON i.kind = 'group' AND i.id = e.group_id
WHERE e.project_id = :project AND e.family IN ('views', 'product') AND e.group_id <> ''
  AND e.day BETWEEN :from AND :to
