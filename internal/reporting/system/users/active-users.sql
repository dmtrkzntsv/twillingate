SELECT count(DISTINCT user_id) AS value
FROM events
WHERE project_id = :project AND family IN ('views', 'product') AND user_id <> ''
  AND day BETWEEN :from AND :to
