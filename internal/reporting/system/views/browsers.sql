SELECT CASE WHEN browser_version != '' THEN browser || ' ' || browser_version ELSE browser END AS label,
       SUM(visitors) AS value
FROM v_views_browsers
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND browser != ''
GROUP BY 1
ORDER BY value DESC
LIMIT 15
