-- 015: the client declares its environment.
-- Spec: docs/superpowers/specs/2026-09-20-os-and-platform-design.md
--
-- Adds platform (views, events) and os_name (views), gives platform its
-- own aggregate and view, and rekeys agg_views_app_versions from
-- (os, app_version) to (platform, app_version).
--
-- This file changes structure only. Every value fold -- closing os,
-- browser and device to the lower-case vocabularies, backfilling an app
-- row's platform, and copying the app_versions history onto its new key
-- -- runs in Go (internal/store/sqlite/migration015.go) through the
-- validators in internal/enrich, in this same transaction and right
-- after the last statement below, so a vocabulary is defined once and the
-- database carries no copy of it. Statements are numbered as in the spec
-- and the order is load-bearing: the platform backfill (1) reads views.os
-- and must run before the OS fold (3) rewrites it. That backfill is the
-- only time platform is ever derived from os -- 012 folded
-- app_views.platform into os, and the data step inverts that fold. From
-- here on os is a genuine OS and says nothing about platform (macos could
-- be web or electron).
--
-- Irreversible. The OS fold copies the original name into os_name for
-- raw rows, but aggregate history has no such column, so its
-- out-of-vocabulary tail is left unfolded rather than merged.

-- Only these views are touched. They are dropped first so the
-- app_versions rebuild below can rename its table: with a view still
-- naming the dropped table, ALTER TABLE ... RENAME fails.
DROP VIEW IF EXISTS v_views_app_versions;
DROP VIEW IF EXISTS v_product_attrs;

ALTER TABLE views  ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE events ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE views  ADD COLUMN os_name  TEXT NOT NULL DEFAULT '';

-- 1. views platform backfill, structural half: a web view is web by
--    definition, so no value is derived here. App rows are backfilled by
--    the data step (it inverts 012's fold of app_views.platform into os);
--    every other kind keeps the column default, unknown.
UPDATE views SET platform = 'web' WHERE kind = 'web';

-- 2. events platform backfill: none. events has no kind column, so os
--    would label a web SDK's custom events macos rather than web. Every
--    row keeps the column default, unknown.

-- 6. agg_views_platforms, modelled on agg_views_countries and seeded from
--    agg_views_daily: web is exact; every other kind becomes unknown, so
--    the platform totals stay consistent with the daily totals. views
--    are additive; visitors overcount only where two non-web kinds shared
--    one pre-015 day, and only in that row.
CREATE TABLE agg_views_platforms (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, platform TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, platform)
) WITHOUT ROWID;
INSERT INTO agg_views_platforms (project_id, day, platform, visitors, views)
SELECT project_id, day, CASE WHEN kind = 'web' THEN 'web' ELSE 'unknown' END,
       SUM(visitors), SUM(views)
FROM agg_views_daily
GROUP BY project_id, day, CASE WHEN kind = 'web' THEN 'web' ELSE 'unknown' END;

-- 7. agg_views_app_versions rekey, structural half: (os, app_version) ->
--    (platform, app_version). The old table is set aside under _old and
--    the new one is created empty; the data step copies the rows through
--    the platform validator and drops _old. Renaming before the views are
--    created is what keeps them pointing at the new table, so no view
--    ever dangles.
ALTER TABLE agg_views_app_versions RENAME TO agg_views_app_versions_old;
CREATE TABLE agg_views_app_versions (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, platform TEXT NOT NULL, app_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, platform, app_version)
) WITHOUT ROWID;

-- 8. Views. v_views_platforms copies the v_views_countries shape (the
--    single-key dimension, including the 500-value cap); v_views_countries
--    itself is the template, not a target. v_views_app_versions is rekeyed.
--    v_product_attrs gains a $platform arm beside $os and $app_version.
CREATE VIEW v_views_platforms AS
SELECT project_id, day, platform, visitors, views FROM agg_views_platforms
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN platform ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.platform, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, platform,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, platform) AS rn
    FROM views GROUP BY project_id, day, platform
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.platform = v.platform
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN platform ELSE '(other)' END;

CREATE VIEW v_views_app_versions AS
SELECT project_id, day, platform, app_version, visitors, views FROM agg_views_app_versions
UNION ALL
SELECT project_id, day, platform, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.platform, v.app_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, platform, app_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, platform, app_version) AS rn
    FROM views WHERE app_version <> '' GROUP BY project_id, day, platform, app_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.platform = v.platform AND r.app_version = v.app_version
  WHERE v.app_version <> ''
)
GROUP BY project_id, day, platform, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END;

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
         e.actor_id AS actor_id
  FROM events e
  JOIN declared d ON d.project_id = e.project_id
  WHERE json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') IS NOT NULL
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$os', os, actor_id
  FROM events WHERE os <> ''
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$platform', platform, actor_id
  FROM events WHERE platform <> ''
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$app_version', app_version, actor_id
  FROM events WHERE app_version <> ''
),
counted AS (
  SELECT project_id, day, event_name, attr_key, attr_value,
         COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u
  FROM vals
  GROUP BY project_id, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT project_id, day, event_name, attr_key, attr_value, c, u,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, attr_key
                            ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT project_id, day, event_name, attr_key, attr_value, count, unique_users
FROM agg_product_attrs
UNION ALL
SELECT project_id, day, event_name, attr_key, attr_value, c, u
FROM ranked
WHERE rn <= (SELECT n FROM cap)
UNION ALL
SELECT v.project_id, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id)
FROM vals v
WHERE NOT EXISTS (
  SELECT 1 FROM ranked r
  WHERE r.project_id = v.project_id AND r.day = v.day
    AND r.event_name = v.event_name AND r.attr_key = v.attr_key
    AND r.attr_value = v.attr_value
    AND r.rn <= (SELECT n FROM cap))
GROUP BY v.project_id, v.day, v.event_name, v.attr_key;
