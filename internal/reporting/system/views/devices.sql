SELECT CASE WHEN device_model != '' THEN device_model ELSE device END AS label,
       SUM(visitors) AS value
FROM v_views_devices
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND (device != '' OR device_model != '')
GROUP BY 1
ORDER BY value DESC
LIMIT 15
