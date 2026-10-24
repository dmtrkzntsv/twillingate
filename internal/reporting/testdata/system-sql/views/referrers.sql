-- Visitors are distinct per day and referrer, added up.
WITH d AS (
  SELECT referrer_source, count(DISTINCT actor_id) AS visitors
  FROM views
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND referrer_source <> ''
  GROUP BY day, referrer_source
)
SELECT referrer_source AS label, SUM(visitors) AS value
FROM d
GROUP BY referrer_source
ORDER BY value DESC, label
LIMIT 20
