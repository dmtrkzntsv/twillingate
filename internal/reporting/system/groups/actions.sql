-- Actions are the views and product events sent with a group_id.
SELECT count(*) AS value
FROM events
WHERE project_id = :project AND family IN ('views', 'product') AND group_id <> ''
  AND day BETWEEN :from AND :to
