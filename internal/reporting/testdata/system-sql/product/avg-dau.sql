-- Active users are distinct per day; the average is over the days with any
-- product event.
WITH d AS (
  SELECT count(DISTINCT actor_id) AS active_users
  FROM product
  WHERE project_id = :project AND day BETWEEN :from AND :to
  GROUP BY day
)
SELECT ROUND(AVG(active_users), 1) AS value
FROM d
