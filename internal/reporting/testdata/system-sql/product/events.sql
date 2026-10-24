-- Peak daily uniques: the most distinct actors that sent the event on any
-- one day of the range.
WITH d AS (
  SELECT event_name, count(*) AS events, count(DISTINCT actor_id) AS users
  FROM product
  WHERE project_id = :project AND day BETWEEN :from AND :to
  GROUP BY day, event_name
)
SELECT event_name AS "Event", SUM(events) AS "Total", MAX(users) AS "Peak daily uniques"
FROM d
GROUP BY event_name
ORDER BY SUM(events) DESC, event_name
