-- $app_version rolls up unconditionally, so this needs no declared
-- attribute. unique_users is per event and cannot be summed across event
-- names (one person firing two events would count twice), so a day's
-- figure is the largest single-event count for that version, a floor on
-- the true number, as in the App versions table.
SELECT day AS x, attr_value AS series, MAX(unique_users) AS y
FROM v_product_attrs
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND attr_key = '$app_version'
GROUP BY day, attr_value
ORDER BY day, y DESC
