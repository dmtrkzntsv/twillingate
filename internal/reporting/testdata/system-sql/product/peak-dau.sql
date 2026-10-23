-- Active users are distinct per day; the busiest day's.
WITH d AS (
  SELECT count(DISTINCT actor_id) AS active_users
  FROM product
  WHERE project_id = :project AND day BETWEEN :from AND :to
  GROUP BY day
)
SELECT MAX(active_users) AS value
FROM d
