-- Visitors are distinct per day and host, added up.
WITH d AS (
  SELECT host, count(DISTINCT actor_id) AS visitors, count(*) AS views
  FROM views
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND host <> ''
  GROUP BY day, host
)
SELECT host AS "Host", SUM(visitors) AS "Visitors", SUM(views) AS "Views"
FROM d
GROUP BY host
ORDER BY SUM(views) DESC, host
LIMIT 20
