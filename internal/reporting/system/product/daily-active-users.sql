SELECT day AS x, SUM(active_users) AS y
FROM v_product_totals
WHERE project_id = :project
  AND day BETWEEN :from AND :to
GROUP BY day
ORDER BY day
