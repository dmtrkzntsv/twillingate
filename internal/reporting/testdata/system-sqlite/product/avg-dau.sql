SELECT ROUND(AVG(active_users), 1) AS value
FROM v_product_totals
WHERE project_id = :project
  AND day BETWEEN :from AND :to
