SELECT day AS x, kind AS series, count(DISTINCT actor_id) AS y
FROM views
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY day, kind
ORDER BY day, kind
