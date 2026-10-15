-- Every attribute the project's events carry, one attr_<key> column each,
-- turned into (key, value) rows; the `$` keys (environment, location) are
-- typed columns rather than attributes, and the Views dashboard has them.
-- "attr_$" is a column no key can name, there so the unpivot has one when
-- no event ever carried an attribute; it is NULL, and the unpivot drops
-- NULLs.
--
-- Summed across events: a value's count is how often it appeared on any
-- event that day. Users and groups are distinct per event and cannot be
-- summed (one person or one group firing two events would count twice),
-- so the largest single-event figure is shown, a floor on the true number.
--
-- Every (attribute, day, value) in the range: the table is remote, so the
-- viewer's filters, sort and page run over all of them in SQL.
WITH a AS (
  UNPIVOT (
    SELECT COLUMNS('^(day|event_name|actor_id|group_id|attr_.*)$'), NULL::VARCHAR AS "attr_$"
    FROM product
    WHERE project_id = :project AND day BETWEEN :from AND :to
  )
  ON COLUMNS('^attr_') INTO NAME attr_key VALUE attr_value
),
d AS (
  SELECT substr(attr_key, 6) AS attr_key, day, attr_value, count(*) AS events,
         count(DISTINCT actor_id) AS users, count(DISTINCT group_id) AS groups
  FROM a
  GROUP BY 1, day, event_name, attr_value
)
SELECT attr_key AS "Attribute", day AS "Day", attr_value AS "Value", SUM(events) AS "Count",
       MAX(users) AS "Users (at least)", MAX(groups) AS "Groups (at least)"
FROM d
GROUP BY attr_key, day, attr_value
ORDER BY attr_key, day DESC, "Count" DESC, attr_value
