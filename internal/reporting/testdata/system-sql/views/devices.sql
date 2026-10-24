-- A device's model where the client sent one, else its type. Visitors are
-- distinct per day, type and model, added up.
WITH d AS (
  SELECT device, device_model, count(DISTINCT actor_id) AS visitors
  FROM views
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND (device <> '' OR device_model <> '')
  GROUP BY day, device, device_model
)
SELECT CASE WHEN device_model <> '' THEN device_model ELSE device END AS label,
       SUM(visitors) AS value
FROM d
GROUP BY 1
ORDER BY value DESC, label
LIMIT 15
