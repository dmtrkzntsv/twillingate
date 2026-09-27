SELECT day AS x, event_name AS series, SUM(count) AS y
FROM v_product_daily
WHERE project_id = :project
  AND day BETWEEN :from AND :to
GROUP BY day, event_name
ORDER BY day, series
