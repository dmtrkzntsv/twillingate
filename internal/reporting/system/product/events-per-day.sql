SELECT day AS x, SUM(total_events) AS y
FROM v_product_totals
WHERE project_id = :project
  AND day BETWEEN :from AND :to
GROUP BY day
ORDER BY day
