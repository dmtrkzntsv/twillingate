-- 019: two locales instead of one.
--
-- $locale was the browser's language (navigator.language) and was stored
-- on views only, read by nothing. It becomes $browser_locale, and
-- $app_locale is the language the product itself is shown in, declared
-- by the client the way $app_version is. Values are stored as sent; the
-- database carries no vocabulary.
ALTER TABLE views  RENAME COLUMN locale TO browser_locale;
ALTER TABLE views  ADD COLUMN app_locale TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN app_locale TEXT NOT NULL DEFAULT '';

-- Daily rollup, keyed on the pair so a visitor whose browser and product
-- disagree stays visible. Rows that declare neither are left out, as
-- agg_views_utm leaves out rows with no campaign. Nothing is seeded:
-- days rolled up before this migration never kept a locale.
CREATE TABLE agg_views_locales (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    browser_locale TEXT NOT NULL, app_locale TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, browser_locale, app_locale)
) WITHOUT ROWID;

-- The live half mirrors the agg_views_locales entry in viewDimensions
-- (aggregate_views.go): top 500 pairs per day, the tail folded into
-- app_locale '(other)' under its browser_locale.
CREATE VIEW v_views_locales AS
SELECT project_id, day, browser_locale, app_locale, visitors, views FROM agg_views_locales
UNION ALL
SELECT project_id, day, browser_locale, CASE WHEN rn <= 500 THEN app_locale ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.browser_locale, v.app_locale, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, browser_locale, app_locale,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, browser_locale, app_locale) AS rn
    FROM views WHERE NOT (browser_locale = '' AND app_locale = '')
    GROUP BY project_id, day, browser_locale, app_locale
  ) r ON r.project_id = v.project_id AND r.day = v.day
     AND r.browser_locale = v.browser_locale AND r.app_locale = v.app_locale
  WHERE NOT (v.browser_locale = '' AND v.app_locale = '')
)
GROUP BY project_id, day, browser_locale, CASE WHEN rn <= 500 THEN app_locale ELSE '(other)' END;

-- 016's definition plus an $app_locale arm beside $app_version. The
-- aggregation's systemDims (aggregate_product.go) gains the same key.
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
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$app_locale', app_locale, actor_id, group_id
  FROM events WHERE app_locale <> ''
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
