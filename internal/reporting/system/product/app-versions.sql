-- unique_users and unique_groups are per event and cannot be summed (one
-- person or one group firing two events would count twice), so the largest
-- single-event figure is shown, a floor on the true number. unique_groups
-- is NULL for days rolled up before the collector measured it; MAX() skips
-- those, so a range with no measured day shows an empty cell, not 0.
SELECT attr_value AS "Version", SUM(count) AS "Events",
       MAX(unique_users) AS "Users (at least)",
       MAX(unique_groups) AS "Groups (at least)"
FROM v_product_attrs
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND attr_key = '$app_version'
GROUP BY attr_value
ORDER BY SUM(count) DESC
