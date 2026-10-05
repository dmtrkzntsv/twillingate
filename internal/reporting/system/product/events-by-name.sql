-- The range's eight busiest events, one line each: a line per event name
-- turns into a wall of legend once an app sends dozens. The Events table
-- below lists every one.
WITH top AS (
  SELECT event_name
  FROM v_product_daily
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
  GROUP BY event_name
  ORDER BY SUM(count) DESC, event_name
  LIMIT 8
)
SELECT day AS x, event_name AS series, SUM(count) AS y
FROM v_product_daily
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND event_name IN (SELECT event_name FROM top)
GROUP BY day, event_name
ORDER BY day, series
