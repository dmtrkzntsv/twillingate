SELECT country AS label, SUM(visitors) AS value
FROM v_views_countries
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND country != ''
GROUP BY country
ORDER BY value DESC
LIMIT 20
