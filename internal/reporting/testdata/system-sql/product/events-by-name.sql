-- The range's five busiest events, one line each in its own chart color:
-- a line per event name turns into a wall of legend once an app sends
-- dozens. The Events table below lists every one.
WITH top AS (
  SELECT event_name
  FROM product
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
  GROUP BY event_name
  ORDER BY count(*) DESC, event_name
  LIMIT 5
)
SELECT day AS x, event_name AS series, count(*) AS y
FROM product
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND event_name IN (SELECT event_name FROM top)
GROUP BY day, event_name
ORDER BY day, series
