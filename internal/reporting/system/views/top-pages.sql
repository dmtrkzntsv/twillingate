SELECT path AS "Path", SUM(visitors) AS "Visitors", SUM(views) AS "Views"
FROM v_views_paths
WHERE project_id = :project
  AND day BETWEEN :from AND :to
GROUP BY path
ORDER BY SUM(views) DESC
LIMIT 20
