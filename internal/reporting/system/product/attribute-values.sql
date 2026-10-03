-- Summed across events: a value's count is how often it appeared on any
-- event that day. unique_users and unique_groups are per event and cannot
-- be summed (one person or one group firing two events would count twice),
-- so the largest single-event figure is shown, a floor on the true number.
-- unique_groups is NULL for days rolled up before the collector measured
-- it; MAX() skips those, so such a day shows an empty cell, not 0.
--
-- Every (attribute, day, value) in the range: the table is remote, so the
-- viewer's filters, sort and page run over all of them in SQL.
SELECT attr_key AS "Attribute", day AS "Day", attr_value AS "Value", SUM(count) AS "Count",
       MAX(unique_users) AS "Users (at least)", MAX(unique_groups) AS "Groups (at least)"
FROM v_product_attrs
WHERE project_id = :project
  AND day BETWEEN :from AND :to
GROUP BY attr_key, day, attr_value
ORDER BY attr_key, day DESC, "Count" DESC, attr_value
