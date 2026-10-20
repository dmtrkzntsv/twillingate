SELECT utm_source AS "Source", utm_medium AS "Medium", utm_campaign AS "Campaign",
       SUM(visitors) AS "Visitors", SUM(views) AS "Views"
FROM v_views_utm
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND utm_source != ''
GROUP BY utm_source, utm_medium, utm_campaign
ORDER BY SUM(visitors) DESC
LIMIT 20
