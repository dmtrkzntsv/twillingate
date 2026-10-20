-- Visitors are distinct per day and consent state, added up. A view sent
-- without $consent is unknown and left out of the rate.
WITH d AS (
  SELECT consent, count(DISTINCT actor_id) AS visitors
  FROM views
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND consent IS NOT NULL
  GROUP BY day, consent
)
SELECT CASE WHEN SUM(visitors) > 0
            THEN COALESCE(SUM(visitors) FILTER (WHERE consent = 1), 0)::DOUBLE / SUM(visitors)
       END AS value
FROM d
