-- Actions are the views and product events sent with a user_id.
SELECT count(*) AS value
FROM events
WHERE project_id = :project AND family IN ('views', 'product') AND user_id <> ''
  AND day BETWEEN :from AND :to
