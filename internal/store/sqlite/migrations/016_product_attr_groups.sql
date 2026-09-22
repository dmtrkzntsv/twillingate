-- 016: groups in the product attribute breakdown.
-- Spec: docs/superpowers/specs/2026-09-21-product-attr-groups-design.md
--
-- agg_product_attrs gains unique_groups -- distinct non-empty group_id
-- among the rows carrying an attribute value -- and v_product_attrs is
-- recreated with group_id threaded through every arm. Structure only:
-- there is no value to fold and no data step.
--
-- Nullable on purpose, against the NOT NULL convention of every other
-- column here. Aggregation deletes the day's raw rows in the same
-- transaction that writes this table, so days rolled up before this
-- migration cannot be backfilled -- their group count is unknown, not
-- zero. The live half always writes an integer (COUNT DISTINCT returns 0,
-- not NULL, when no row carries a group), so NULL means exactly one thing:
-- aggregated before 016.
ALTER TABLE agg_product_attrs ADD COLUMN unique_groups INTEGER;

-- 015's definition with group_id carried from vals through counted and
-- ranked into all three arms of the union. Ranking stays ORDER BY c DESC,
-- attr_value: a value's place in the top N is decided by how often it
-- appeared, so the rows this view returns are 015's rows plus one column.
-- (other) counts its groups from raw, not as a sum of the tail's own
-- counts, for the same reason its unique_users is computed there.
DROP VIEW IF EXISTS v_product_attrs;
CREATE VIEW v_product_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta
                   WHERE key='product_attributes_top_n'
                     AND CAST(value AS INTEGER) > 0), 50) AS n
),
declared AS (
  SELECT DISTINCT p.id AS project_id, j.value AS attr_key
  FROM projects p,
       json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
  WHERE j.type = 'text'
),
vals AS (
  SELECT e.project_id AS project_id, substr(e.ts,1,10) AS day,
         e.event_name AS event_name, d.attr_key AS attr_key,
         json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') AS attr_value,
         e.actor_id AS actor_id, e.group_id AS group_id
  FROM events e
  JOIN declared d ON d.project_id = e.project_id
  WHERE json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') IS NOT NULL
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$os', os, actor_id, group_id
  FROM events WHERE os <> ''
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$platform', platform, actor_id, group_id
  FROM events WHERE platform <> ''
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$app_version', app_version, actor_id, group_id
  FROM events WHERE app_version <> ''
),
counted AS (
  SELECT project_id, day, event_name, attr_key, attr_value,
         COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
         COUNT(DISTINCT NULLIF(group_id,'')) AS g
  FROM vals
  GROUP BY project_id, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT project_id, day, event_name, attr_key, attr_value, c, u, g,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, attr_key
                            ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups
FROM agg_product_attrs
UNION ALL
SELECT project_id, day, event_name, attr_key, attr_value, c, u, g
FROM ranked
WHERE rn <= (SELECT n FROM cap)
UNION ALL
SELECT v.project_id, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id), COUNT(DISTINCT NULLIF(v.group_id,''))
FROM vals v
WHERE NOT EXISTS (
  SELECT 1 FROM ranked r
  WHERE r.project_id = v.project_id AND r.day = v.day
    AND r.event_name = v.event_name AND r.attr_key = v.attr_key
    AND r.attr_value = v.attr_value
    AND r.rn <= (SELECT n FROM cap))
GROUP BY v.project_id, v.day, v.event_name, v.attr_key;
