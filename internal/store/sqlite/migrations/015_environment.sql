-- 015: the client declares its environment.
-- Spec: docs/superpowers/specs/2026-09-20-os-and-platform-design.md
--
-- Adds platform (views, events) and os_name (views), closes os, browser
-- and device to lower-case vocabularies, gives platform its own aggregate
-- and view, and rekeys agg_views_app_versions from (os, app_version) to
-- (platform, app_version). Statements are numbered as in the spec and the
-- order is load-bearing: the platform backfill (1) reads views.os and
-- must run before the OS fold (3) rewrites it. This is the only time
-- platform is ever derived from os -- 012 folded app_views.platform into
-- os, and this inverts that fold. From here on os is a genuine OS and
-- says nothing about platform (macos could be web or electron).
--
-- Irreversible. The OS fold copies the original name into os_name for
-- raw rows, but aggregate history has no such column, so its
-- out-of-vocabulary tail is left unfolded rather than merged (4).

-- Only these views are touched. They are dropped first so the
-- app_versions rebuild below can rename its table: with a view still
-- naming the dropped table, ALTER TABLE ... RENAME fails.
DROP VIEW IF EXISTS v_views_app_versions;
DROP VIEW IF EXISTS v_product_attrs;

ALTER TABLE views  ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE events ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE views  ADD COLUMN os_name  TEXT NOT NULL DEFAULT '';

-- 1. views platform backfill, faithful: web is web; app inverts 012's
--    fold; every other kind keeps the default. A derived token outside
--    ^[a-z][a-z0-9_]{0,15}$ is unknown.
UPDATE views SET platform = 'web' WHERE kind = 'web';
UPDATE views SET platform = lower(os)
WHERE kind = 'app'
  AND lower(os) GLOB '[a-z]*'
  AND lower(os) NOT GLOB '*[^a-z0-9_]*'
  AND length(os) <= 16;

-- 2. events platform backfill: none. events has no kind column, so
--    lower(os) would label a web SDK's custom events macos rather than
--    web. Every row keeps the column default, unknown.

-- 3. OS fold, raw rows. The original is preserved first, so other stays
--    investigable; then lower-case, '' -> unknown, unlisted -> other.
UPDATE views SET os_name = os
WHERE os <> '' AND lower(os) NOT IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown');
UPDATE views SET os = CASE
  WHEN os = '' THEN 'unknown'
  WHEN lower(os) IN (
    'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
    'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown')
    THEN lower(os)
  ELSE 'other' END;
UPDATE events SET os = CASE
  WHEN os = '' THEN 'unknown'
  WHEN lower(os) IN (
    'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
    'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown')
    THEN lower(os)
  ELSE 'other' END;

-- 4. OS fold, aggregate history: only what merges nothing. Canonical
--    values are lower-cased (injective over the vocabulary) and '' is
--    relabelled to unknown (a value that did not exist before). An
--    unlisted value is left exactly as it was: folding it into other
--    would SUM visitors that were counted as distinct actors. Two
--    spellings of one canonical value on one key would collide here and
--    abort the migration; docs/deployment.md lists the query that finds
--    them beforehand.
UPDATE agg_views_os SET os = CASE WHEN os = '' THEN 'unknown' ELSE lower(os) END
WHERE os = '' OR lower(os) IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown');
UPDATE agg_product_attrs SET attr_value = CASE WHEN attr_value = '' THEN 'unknown' ELSE lower(attr_value) END
WHERE attr_key = '$os' AND (attr_value = '' OR lower(attr_value) IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown'));

-- 5. Browser and device fold: loss-free. No client could write these
--    columns before 015, so every value came from ParseUserAgent -- six
--    browser names, three device classes, or '' -- and all nine are in
--    the new vocabularies. The fold is injective and merges nothing. The
--    '' rows are the app and cli rows kind gating never enriched.
UPDATE views SET browser = CASE
  WHEN browser = '' THEN 'unknown'
  WHEN browser = 'Samsung Internet' THEN 'samsung_internet'
  ELSE lower(browser) END;
UPDATE views SET device = CASE WHEN device = '' THEN 'unknown' ELSE lower(device) END;
UPDATE agg_views_browsers SET browser = CASE
  WHEN browser = '' THEN 'unknown'
  WHEN browser = 'Samsung Internet' THEN 'samsung_internet'
  ELSE lower(browser) END;
UPDATE agg_views_devices SET device = CASE WHEN device = '' THEN 'unknown' ELSE lower(device) END;

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

-- 7. agg_views_app_versions rekey: (os, app_version) -> (platform,
--    app_version), platform = lower(os), or unknown outside the platform
--    pattern. Value-preserving for canonical os; only values outside the
--    pattern merge, and only into unknown.
CREATE TABLE agg_views_app_versions_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, platform TEXT NOT NULL, app_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, platform, app_version)
) WITHOUT ROWID;
INSERT INTO agg_views_app_versions_new (project_id, day, platform, app_version, visitors, views)
SELECT project_id, day,
       CASE WHEN lower(os) GLOB '[a-z]*' AND lower(os) NOT GLOB '*[^a-z0-9_]*' AND length(os) <= 16
            THEN lower(os) ELSE 'unknown' END,
       app_version, SUM(visitors), SUM(views)
FROM agg_views_app_versions
GROUP BY 1, 2, 3, 4;
DROP TABLE agg_views_app_versions;
ALTER TABLE agg_views_app_versions_new RENAME TO agg_views_app_versions;

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
