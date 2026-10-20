SELECT source AS label, SUM(visitors) AS value
FROM v_views_referrers
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND source != ''
GROUP BY source
ORDER BY value DESC
LIMIT 20
