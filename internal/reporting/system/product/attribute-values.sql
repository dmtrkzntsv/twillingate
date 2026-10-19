-- Every attribute the project's events carry, one attr_<key> column each,
-- turned into (key, value) rows; the `$` keys (environment, location) are
-- typed columns rather than attributes, and the Views dashboard has them.
-- "attr_$" is a column no key can name, there so the unpivot has one when
-- no event ever carried an attribute; it is NULL, and the unpivot drops
-- NULLs. The COLUMNS pattern takes actor_id too: one that matched nothing
-- would be an error.
--
-- One row per (attribute, value) over the whole range: Count is the events
-- that carried the value, Users the distinct actors who sent it and Groups
-- the distinct groups (0 when none), both exact for the range. There is no
-- Day column: a year of per-day values is millions of rows nobody reads,
-- and one attribute's trend over time is a chart's job.
--
-- No DISTINCT aggregate: DuckDB cannot spill one, and a year at the cap
-- holds millions of (attribute, value, actor) triples. The events are first
-- folded into one row per actor, group and set of values (an actor sends
-- the same values again and again), then grouped by (attribute, value) and
-- actor and, apart, by (attribute, value) and group: plain GROUP BYs, which
-- spill; each actor's or group's row then counts once.
--
-- The table is remote, so the viewer's filters, sort and page run over
-- every row in SQL.
WITH folded AS (
  SELECT COLUMNS('^(actor_id|attr_.*)$'), nullif(group_id, '') AS group_id, NULL::VARCHAR AS "attr_$",
         count(*) AS events
  FROM product
  WHERE project_id = :project AND day BETWEEN :from AND :to
  GROUP BY ALL
),
a AS (
  UNPIVOT folded ON COLUMNS('^attr_') INTO NAME attr_key VALUE attr_value
),
per AS (
  SELECT attr_key, attr_value, actor_id, group_id, GROUPING(actor_id) AS by_group, sum(events) AS events
  FROM a
  GROUP BY GROUPING SETS ((attr_key, attr_value, actor_id), (attr_key, attr_value, group_id))
)
SELECT substr(attr_key, 6) AS "Attribute", attr_value AS "Value",
       CAST(sum(events) FILTER (WHERE by_group = 0) AS BIGINT) AS "Count",
       count(*) FILTER (WHERE by_group = 0) AS "Users",
       count(group_id) FILTER (WHERE by_group = 1) AS "Groups"
FROM per
GROUP BY attr_key, attr_value
ORDER BY "Attribute", "Count" DESC, "Value"
