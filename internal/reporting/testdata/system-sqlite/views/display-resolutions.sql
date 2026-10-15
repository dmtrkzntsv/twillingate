SELECT display AS label, SUM(visitors) AS value
FROM v_views_displays
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND display != ''
GROUP BY display
ORDER BY value DESC
LIMIT 15
