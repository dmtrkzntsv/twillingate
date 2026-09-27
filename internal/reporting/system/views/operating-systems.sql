SELECT CASE WHEN os_version != '' THEN os || ' ' || os_version ELSE os END AS label,
       SUM(visitors) AS value
FROM v_views_os
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND os != ''
GROUP BY 1
ORDER BY value DESC
LIMIT 15
