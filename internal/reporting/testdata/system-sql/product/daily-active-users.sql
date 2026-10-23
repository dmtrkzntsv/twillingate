SELECT day AS x, count(DISTINCT actor_id) AS y
FROM product
WHERE project_id = :project
  AND day BETWEEN :from AND :to
GROUP BY day
ORDER BY day
