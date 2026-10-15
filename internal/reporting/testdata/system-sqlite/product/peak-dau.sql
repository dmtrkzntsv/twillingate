SELECT MAX(active_users) AS value
FROM v_product_totals
WHERE project_id = :project
  AND day BETWEEN :from AND :to
