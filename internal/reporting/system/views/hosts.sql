SELECT host AS "Host", SUM(visitors) AS "Visitors", SUM(views) AS "Views"
FROM v_views_hosts
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND host != ''
GROUP BY host
ORDER BY SUM(views) DESC
LIMIT 20
