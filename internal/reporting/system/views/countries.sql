-- Visitors are distinct per day and country, added up.
WITH d AS (
  SELECT country, count(DISTINCT actor_id) AS visitors
  FROM views
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND country <> ''
  GROUP BY day, country
)
SELECT country AS label, SUM(visitors) AS value
FROM d
GROUP BY country
ORDER BY value DESC, label
LIMIT 20
