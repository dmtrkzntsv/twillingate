-- Visitors are distinct per day and campaign, added up.
WITH d AS (
  SELECT utm_source, COALESCE(utm_medium, '') AS utm_medium, COALESCE(utm_campaign, '') AS utm_campaign,
         count(DISTINCT actor_id) AS visitors, count(*) AS views
  FROM views
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND utm_source <> ''
  GROUP BY day, utm_source, utm_medium, utm_campaign
)
SELECT utm_source AS "Source", utm_medium AS "Medium", utm_campaign AS "Campaign",
       SUM(visitors) AS "Visitors", SUM(views) AS "Views"
FROM d
GROUP BY utm_source, utm_medium, utm_campaign
ORDER BY SUM(visitors) DESC, 1, 2, 3
LIMIT 20
