-- 025: v_product_attrs computes its live half only for the raw days a
-- query's range covers (#111). Same rows as 023's view; only its shape
-- changes, so nothing is copied and nothing reads differently.
--
-- A query filters the view by project_id and day. SQLite pushes that
-- filter down into a subquery through plain SELECTs, through each arm of a
-- UNION ALL, through window functions whose every PARTITION BY holds the
-- filtered columns, and into an aggregate over a table. It stops at a CTE
-- read twice (materialized once, before any filter) and at an aggregate
-- over a subquery (the filter lands in its HAVING after the subquery has
-- run).
-- 023's live half had both: vals and ranked were each read twice, and
-- counted grouped a UNION ALL of nine arms. So every query computed the
-- live half over the whole raw window, even one whose range ended before
-- the first raw day.
--
-- Here the live half is one chain the filter passes through, down to the
-- primary key of events:
--
--   - every raw row is paired with each of its project's keys (the
--     declared ones and store.SystemAttributes) and grouped per value in
--     one aggregate over raw_product, ranked in the same SELECT, with the
--     value's actors and groups also kept as JSON arrays;
--   - a window over the same partition sums the values ranked past the
--     cap and joins their arrays into one;
--   - the row ranked cap+1 becomes the "(other)" row, whose distinct users
--     and groups are counted from the joined arrays: a tail costs its own
--     size and no second pass over raw rows.
--
-- The cap, ranking and "(other)" rule are the daily pass's
-- (aggregate_product.go), as before. The CASE lists store.SystemAttributes
-- and store.DeclarableAttributes, and the VALUES list
-- store.SystemAttributes; TestProductAttrsDeclaredSystemKeysAcrossBoundary
-- fails on any key one of them misses. NULLIF turns an empty column into
-- "absent", the same meaning json_extract's NULL has for a custom key.
DROP VIEW v_product_attrs;
CREATE VIEW v_product_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta
                   WHERE key='product_attributes_top_n'
                     AND CAST(value AS INTEGER) > 0), 50) AS n
)
SELECT project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups
FROM agg_product_attrs
UNION ALL
SELECT project_id, day, event_name, attr_key,
       CASE WHEN rn <= (SELECT n FROM cap) THEN attr_value ELSE '(other)' END,
       CASE WHEN rn <= (SELECT n FROM cap) THEN c ELSE tail_c END,
       CASE WHEN rn <= (SELECT n FROM cap) THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(tail_actors)) END,
       CASE WHEN rn <= (SELECT n FROM cap) THEN g
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(tail_groups)) END
FROM (
  -- Whole-partition aggregates over the values past the cap; NULL where
  -- there are none. Each array loses its brackets, is joined and wrapped
  -- again: one array of every tail row's actor (or group, null for none).
  SELECT project_id, day, event_name, attr_key, attr_value, c, u, g, rn,
         SUM(c) FILTER (WHERE rn > (SELECT n FROM cap)) OVER w AS tail_c,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',')
                FILTER (WHERE rn > (SELECT n FROM cap)) OVER w || ']' AS tail_actors,
         '[' || group_concat(substr(groups, 2, length(groups) - 2), ',')
                FILTER (WHERE rn > (SELECT n FROM cap)) OVER w || ']' AS tail_groups
  FROM (
    SELECT project_id, day, event_name, attr_key, attr_value,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           COUNT(DISTINCT NULLIF(group_id, '')) AS g,
           json_group_array(actor_id) AS actors,
           json_group_array(NULLIF(group_id, '')) AS groups,
           ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, attr_key
                              ORDER BY COUNT(*) DESC, attr_value) AS rn
    FROM (
      SELECT e.project_id AS project_id, e.day AS day, e.event_name AS event_name,
             e.actor_id AS actor_id, e.group_id AS group_id, k.attr_key AS attr_key,
             CASE k.attr_key
               WHEN '$platform'        THEN NULLIF(e.platform, '')
               WHEN '$os'              THEN NULLIF(e.os, '')
               WHEN '$app_version'     THEN NULLIF(e.app_version, '')
               WHEN '$app_locale'      THEN NULLIF(e.app_locale, '')
               WHEN '$kind'            THEN NULLIF(e.kind, '')
               WHEN '$browser'         THEN NULLIF(e.browser, '')
               WHEN '$device'          THEN NULLIF(e.device, '')
               WHEN '$browser_locale'  THEN NULLIF(e.browser_locale, '')
               WHEN '$host'            THEN NULLIF(e.host, '')
               WHEN '$path'            THEN NULLIF(e.path, '')
               WHEN '$referrer'        THEN NULLIF(e.referrer_source, '')
               WHEN '$utm_source'      THEN NULLIF(e.utm_source, '')
               WHEN '$utm_medium'      THEN NULLIF(e.utm_medium, '')
               WHEN '$utm_campaign'    THEN NULLIF(e.utm_campaign, '')
               WHEN '$os_version'      THEN NULLIF(e.os_version, '')
               WHEN '$browser_version' THEN NULLIF(e.browser_version, '')
               WHEN '$device_model'    THEN NULLIF(e.device_model, '')
               ELSE CASE WHEN k.attr_key LIKE '$%' THEN NULL
                         ELSE json_extract(e.attributes, '$."' || replace(k.attr_key,'"','\"') || '"') END
             END AS attr_value
      FROM raw_product e
      -- Each project's keys: what it declares, and every system key. UNION
      -- drops a system key a project also declares (manage refuses that
      -- today; older registries may hold one).
      JOIN (
        SELECT p.id AS project_id, j.value AS attr_key
        FROM projects p,
             json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
        WHERE j.type = 'text'
        UNION
        SELECT p.id, s.column1
        FROM projects p,
             (VALUES ('$platform'), ('$os'), ('$app_version'), ('$app_locale'),
                     ('$kind'), ('$browser'), ('$device'), ('$browser_locale')) s
      ) k ON k.project_id = e.project_id
    )
    WHERE attr_value IS NOT NULL
    GROUP BY project_id, day, event_name, attr_key, attr_value
  )
  WINDOW w AS (PARTITION BY project_id, day, event_name, attr_key)
)
WHERE rn <= (SELECT n FROM cap) + 1;
