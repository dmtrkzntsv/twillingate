SELECT event_name AS "Event", SUM(count) AS "Total", MAX(unique_users) AS "Peak daily uniques"
FROM v_product_daily
WHERE project_id = :project
  AND day BETWEEN :from AND :to
GROUP BY event_name
ORDER BY SUM(count) DESC
