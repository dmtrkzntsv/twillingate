-- Visitors are distinct per day and display, added up.
WITH d AS (
  SELECT concat(display_width, 'x', display_height) AS display, count(DISTINCT actor_id) AS visitors
  FROM views
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND display_width > 0 AND display_height > 0
  GROUP BY day, 1
)
SELECT display AS label, SUM(visitors) AS value
FROM d
GROUP BY display
ORDER BY value DESC, label
LIMIT 15
