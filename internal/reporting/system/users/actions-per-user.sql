-- Actions are the views and product events sent with a user_id.
SELECT CASE WHEN count(DISTINCT user_id) > 0
            THEN ROUND(count(*)::DOUBLE / count(DISTINCT user_id), 1) ELSE 0 END AS value
FROM events
WHERE project_id = :project AND family IN ('views', 'product') AND user_id <> ''
  AND day BETWEEN :from AND :to
