-- Summed across events: a value's count is how often it appeared on any
-- event that day. unique_users and unique_groups are per event and cannot
-- be summed (one person or one group firing two events would count twice),
-- so the largest single-event figure is shown, a floor on the true number.
-- unique_groups is NULL for days rolled up before the collector measured
-- it; MAX() skips those, so such a day shows an empty cell, not 0.
--
-- Ranked within each day, so a quiet day keeps its own top values rather
-- than losing all of them to a busier day's. Each day keeps 100 / days
-- values, at least 5, at most 900 / days: about 100 rows for a short
-- range, five a day for a long one, and always under the row cap.
-- Shown grouped by attribute, newest day first.
WITH summed AS (
  SELECT attr_key, day, attr_value, SUM(count) AS count,
         MAX(unique_users) AS max_users,
         MAX(unique_groups) AS max_groups
  FROM v_product_attrs
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
  GROUP BY attr_key, day, attr_value
),
ranked AS (
  SELECT *, ROW_NUMBER() OVER (PARTITION BY day ORDER BY count DESC, attr_key, attr_value) AS pos
  FROM summed
),
span AS (
  SELECT CAST(julianday(:to) - julianday(:from) AS INTEGER) + 1 AS days
)
SELECT attr_key AS "Attribute", day AS "Day", attr_value AS "Value", count AS "Count",
       max_users AS "Users (at least)", max_groups AS "Groups (at least)"
FROM ranked, span
WHERE pos <= MIN(MAX(5, 100 / days), MAX(1, 900 / days))
ORDER BY attr_key, day DESC, count DESC
