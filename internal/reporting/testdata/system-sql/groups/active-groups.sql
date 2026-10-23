SELECT count(DISTINCT group_id) AS value
FROM events
WHERE project_id = :project AND family IN ('views', 'product') AND group_id <> ''
  AND day BETWEEN :from AND :to
