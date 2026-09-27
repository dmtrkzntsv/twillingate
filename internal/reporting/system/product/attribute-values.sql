-- Summed across events: a value's count is how often it appeared on any
-- event that day. unique_users and unique_groups are per event and cannot
-- be summed (one person or one group firing two events would count twice),
-- so the largest single-event figure is shown, a floor on the true number.
-- unique_groups is NULL for days rolled up before the collector measured
-- it; MAX() skips those, so such a day shows an empty cell, not 0.
--
-- The busiest 100 attribute/day/value rows, so a wide range stays
-- under the row cap; shown grouped by attribute, newest day first.
WITH top AS (
  SELECT attr_key, day, attr_value, SUM(count) AS count,
         MAX(unique_users) AS max_users,
         MAX(unique_groups) AS max_groups
  FROM v_product_attrs
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
  GROUP BY attr_key, day, attr_value
  ORDER BY SUM(count) DESC, attr_key, day DESC, attr_value
  LIMIT 100
)
SELECT attr_key AS "Attribute", day AS "Day", attr_value AS "Value", count AS "Count",
       max_users AS "Users (at least)", max_groups AS "Groups (at least)"
FROM top
ORDER BY attr_key, day DESC, count DESC
