-- 025: every v_* view computes its live half only for the raw days a
-- query's range covers (#111). Each view answers the same rows as before;
-- only its shape changes, so nothing is copied and nothing reads
-- differently. (The attribute views take their keys from each project's
-- row, so raw rows of a project id with no row in projects, which ingest
-- never writes and DeleteProjectData removes first, break down by nothing.)
--
-- A query filters a view by project_id and day. SQLite pushes that filter
-- down into a subquery through plain SELECTs, through each arm of a UNION
-- ALL, through window functions whose every PARTITION BY holds the
-- filtered columns, and into an aggregate over a table. It stops at a CTE
-- read twice (materialized once, before any filter), at an aggregate over
-- a subquery (the filter lands in its HAVING after the subquery has run),
-- and at two windows partitioned differently in one SELECT. Before this
-- migration:
--
--   - v_product_attrs (023) read two CTEs twice and grouped a UNION ALL of
--     nine arms; v_measures_attrs (024) read one twice and grouped a join;
--   - each v_views_* breakdown grouped raw rows joined to a ranking of the
--     day's values, and v_views_daily did that twice, once under its
--     sessionizing windows;
--   - v_identity_daily grouped a UNION ALL of four arms.
--
-- So every query computed those live halves over the whole raw window,
-- even one whose range ended before the first raw day. v_views_consent,
-- v_product_daily, v_product_totals and v_measures_daily already let the
-- filter through and are left alone; TestLiveHalvesReadOnlyTheRange holds
-- every view to it.
--
-- Each live half below is one chain the filter passes through, down to the
-- primary key of events. In v_product_attrs:
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
-- The other views follow, each with its own note. The caps, rankings and
-- "(other)" rules are the daily passes' (aggregate_product.go,
-- aggregate_measures.go, aggregate_views.go, identities.go), as before.
-- Each CASE lists store.SystemAttributes and store.DeclarableAttributes,
-- and each VALUES list store.SystemAttributes;
-- TestProductAttrsDeclaredSystemKeysAcrossBoundary fails on any key the
-- product view misses, and the TestMigration025 tests on any row a view
-- answers differently from 024. NULLIF turns an empty column into
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

-- v_measures_attrs: histogram rows add up and carry no distinct counts, so
-- the "(other)" fold is a sum and the whole live half is windows over one
-- aggregate: each (value, bucket) with its value's samples over every
-- bucket, the value's rank by those samples, then one row per kept value
-- (or "(other)") and bucket. A kept value that is literally "(other)"
-- merges with the tail, as in 024 and the daily pass.
DROP VIEW v_measures_attrs;
CREATE VIEW v_measures_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta
                   WHERE key='product_attributes_top_n'
                     AND CAST(value AS INTEGER) > 0), 50) AS n
)
SELECT project_id, day, event_name, measure, attr_key, attr_value, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
       samples, weight, sum
FROM agg_measures_attrs
UNION ALL
SELECT project_id, day, event_name, measure, attr_key, kept, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END,
       samples, weight, sum
FROM (
  SELECT project_id, day, event_name, measure, attr_key, kept, bucket,
         SUM(samples) OVER f AS samples, SUM(weight) OVER f AS weight,
         SUM(sum) OVER f AS sum, ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, event_name, measure, attr_key, bucket, samples, weight, sum,
           CASE WHEN DENSE_RANK() OVER (PARTITION BY project_id, day, event_name, measure, attr_key
                                        ORDER BY value_samples DESC, attr_value) <= (SELECT n FROM cap)
                THEN attr_value ELSE '(other)' END AS kept
    FROM (
      SELECT project_id, day, event_name, measure, attr_key, attr_value, bucket,
             COUNT(*) AS samples, SUM(1.0 / sample_rate) AS weight, SUM(value / sample_rate) AS sum,
             SUM(COUNT(*)) OVER (PARTITION BY project_id, day, event_name, measure, attr_key, attr_value)
               AS value_samples
      FROM (
        SELECT e.project_id AS project_id, e.day AS day, e.event_name AS event_name,
               e.measure AS measure, e.bucket AS bucket, e.sample_rate AS sample_rate,
               e.value AS value, k.attr_key AS attr_key,
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
        FROM raw_measures e
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
      GROUP BY project_id, day, event_name, measure, attr_key, attr_value, bucket
    )
  )
  WINDOW f AS (PARTITION BY project_id, day, event_name, measure, attr_key, kept, bucket)
)
WHERE rf = 1;

-- v_identity_daily: one aggregate over events itself, its views and
-- product rows each paired with the two kinds, rather than a UNION ALL of
-- four arms over raw_views and raw_product; ranked in the same SELECT.
DROP VIEW v_identity_daily;
CREATE VIEW v_identity_daily AS
SELECT project_id, day, kind, id, actors, users, views, events
FROM agg_identity_daily
UNION ALL
SELECT project_id, day, kind, id, actors, users, views, events
FROM (
  SELECT project_id, day, k.kind AS kind,
         CASE k.kind WHEN 'user' THEN user_id ELSE group_id END AS id,
         COUNT(DISTINCT actor_id) AS actors,
         CASE WHEN k.kind = 'user' THEN 1 ELSE COUNT(DISTINCT NULLIF(user_id, '')) END AS users,
         SUM(family = 'views') AS views, SUM(family = 'product') AS events,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, k.kind
                            ORDER BY COUNT(*) DESC, CASE k.kind WHEN 'user' THEN user_id ELSE group_id END) AS rn
  FROM events
  JOIN (SELECT 'user' AS kind UNION ALL SELECT 'group') k
  WHERE family IN ('views', 'product')
    AND CASE k.kind WHEN 'user' THEN user_id ELSE group_id END <> ''
  GROUP BY project_id, day, k.kind, CASE k.kind WHEN 'user' THEN user_id ELSE group_id END
) live
WHERE rn <= 500
  AND NOT EXISTS (
    SELECT 1 FROM agg_identity_daily g
    WHERE g.project_id = live.project_id AND g.day = live.day);

-- The v_views_* breakdowns: one aggregate per full key over raw_views,
-- ranked in the same SELECT, with each key's actors kept as a JSON array;
-- then one row per kept key (the day's 500 most viewed, the last column
-- '(other)' past them) and the columns before it. Views add up; distinct
-- visitors of a row that merges several keys (the tail, and a kept value
-- spelled '(other)', which merges with it as before) are counted from
-- their joined arrays.
DROP VIEW v_views_app_versions;
DROP VIEW v_views_browsers;
DROP VIEW v_views_countries;
DROP VIEW v_views_devices;
DROP VIEW v_views_displays;
DROP VIEW v_views_hosts;
DROP VIEW v_views_locales;
DROP VIEW v_views_os;
DROP VIEW v_views_paths;
DROP VIEW v_views_platforms;
DROP VIEW v_views_referrers;
DROP VIEW v_views_utm;

CREATE VIEW v_views_app_versions AS
SELECT project_id, day, platform, app_version, visitors, views FROM agg_views_app_versions
UNION ALL
SELECT project_id, day, platform, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, platform, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, platform,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, platform, app_version) <= 500
                THEN app_version ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    WHERE app_version <> ''
    GROUP BY project_id, day, platform, app_version
  )
  WINDOW f AS (PARTITION BY project_id, day, platform, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_browsers AS
SELECT project_id, day, browser, browser_version, visitors, views FROM agg_views_browsers
UNION ALL
SELECT project_id, day, browser, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, browser, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, browser,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, browser, browser_version) <= 500
                THEN browser_version ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    GROUP BY project_id, day, browser, browser_version
  )
  WINDOW f AS (PARTITION BY project_id, day, browser, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_countries AS
SELECT project_id, day, country, visitors, views FROM agg_views_countries
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, country) <= 500
                THEN country ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    GROUP BY project_id, day, country
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_devices AS
SELECT project_id, day, device, device_model, visitors, views FROM agg_views_devices
UNION ALL
SELECT project_id, day, device, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, device, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, device,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, device, device_model) <= 500
                THEN device_model ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    GROUP BY project_id, day, device, device_model
  )
  WINDOW f AS (PARTITION BY project_id, day, device, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_displays AS
SELECT project_id, day, display, visitors, views FROM agg_views_displays
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, display_width || 'x' || display_height) <= 500
                THEN display_width || 'x' || display_height ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    WHERE display_width > 0 AND display_height > 0
    GROUP BY project_id, day, display_width || 'x' || display_height
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_hosts AS
SELECT project_id, day, host, visitors, views FROM agg_views_hosts
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, host) <= 500
                THEN host ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    GROUP BY project_id, day, host
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_locales AS
SELECT project_id, day, browser_locale, app_locale, visitors, views FROM agg_views_locales
UNION ALL
SELECT project_id, day, browser_locale, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, browser_locale, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, browser_locale,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, browser_locale, app_locale) <= 500
                THEN app_locale ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    WHERE NOT (browser_locale = '' AND app_locale = '')
    GROUP BY project_id, day, browser_locale, app_locale
  )
  WINDOW f AS (PARTITION BY project_id, day, browser_locale, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_os AS
SELECT project_id, day, os, os_version, visitors, views FROM agg_views_os
UNION ALL
SELECT project_id, day, os, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, os, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, os,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, os, os_version) <= 500
                THEN os_version ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    GROUP BY project_id, day, os, os_version
  )
  WINDOW f AS (PARTITION BY project_id, day, os, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_paths AS
SELECT project_id, day, path, visitors, views FROM agg_views_paths
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, path) <= 500
                THEN path ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    GROUP BY project_id, day, path
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_platforms AS
SELECT project_id, day, platform, visitors, views FROM agg_views_platforms
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, platform) <= 500
                THEN platform ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    GROUP BY project_id, day, platform
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_referrers AS
SELECT project_id, day, source, visitors, views FROM agg_views_referrers
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, referrer_source) <= 500
                THEN referrer_source ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    GROUP BY project_id, day, referrer_source
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_utm AS
SELECT project_id, day, utm_source, utm_medium, utm_campaign, visitors, views FROM agg_views_utm
UNION ALL
SELECT project_id, day, utm_source, utm_medium, kept,
       CASE WHEN n = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, utm_source, utm_medium, kept, u,
         COUNT(*) OVER f AS n, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, utm_source, utm_medium,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, utm_source, utm_medium, utm_campaign) <= 500
                THEN utm_campaign ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u, json_group_array(actor_id) AS actors
    FROM raw_views
    WHERE NOT (utm_source = '' AND utm_medium = '' AND utm_campaign = '')
    GROUP BY project_id, day, utm_source, utm_medium, utm_campaign
  )
  WINDOW f AS (PARTITION BY project_id, day, utm_source, utm_medium, kept)
)
WHERE rf = 1;

-- v_views_daily: sessions need each actor's views in order, so its live
-- half runs once per raw day: a day list the filter reaches drives a
-- subquery over that day's rows, whose few rows (one per kind) come back
-- as JSON. The subquery is one chain with no join: SQLite re-runs a
-- correlated subquery's inner join side for every outer row. Every view
-- lands in exactly one span, so visitors and views come from the spans
-- too; kinds past the day's 500 most viewed are '(other)', as before.
DROP VIEW v_views_daily;
CREATE VIEW v_views_daily AS
SELECT project_id, day, kind, visitors, views, sessions, bounces, duration_sec FROM agg_views_daily
UNION ALL
SELECT pd.project_id, pd.day, r.value ->> 0, r.value ->> 1, r.value ->> 2,
       r.value ->> 3, r.value ->> 4, r.value ->> 5
FROM (SELECT project_id, day FROM raw_views GROUP BY project_id, day) pd,
     json_each((
  SELECT json_group_array(json_array(kind, visitors, views, sessions, bounces, duration_sec))
  FROM (
    -- visitors, views, sessions, bounces and duration per bucketed kind:
    -- every view is in exactly one span
    SELECT kind, COUNT(DISTINCT actor_id) AS visitors, SUM(view_count) AS views,
           COUNT(*) AS sessions, SUM(CASE WHEN view_count = 1 THEN 1 ELSE 0 END) AS bounces,
           COALESCE(SUM(dur), 0) AS duration_sec
    FROM (
      -- spans: one per actor and session key
      SELECT kind, actor_id, COUNT(*) AS view_count, MAX(t) - MIN(t) AS dur
      FROM (
        SELECT kind, actor_id, t,
               CASE WHEN session_id <> '' THEN session_id
                    ELSE CAST(SUM(new_session) OVER (PARTITION BY kind, actor_id ORDER BY t) AS TEXT)
               END AS skey
        FROM (
          SELECT kind, actor_id, session_id, t,
                 CASE WHEN session_id <> '' THEN 0
                      WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
                      ELSE 0 END AS new_session
          FROM (
            -- the day's views under their bucketed kind: the 500 kinds
            -- with the most views that day, the rest as (other)
            SELECT CASE WHEN DENSE_RANK() OVER (ORDER BY kind_views DESC, kind) <= 500
                        THEN kind ELSE '(other)' END AS kind,
                   actor_id, session_id, t
            FROM (
              SELECT kind, actor_id, session_id, CAST(strftime('%s', ts) AS INTEGER) AS t,
                     COUNT(*) OVER (PARTITION BY kind) AS kind_views
              FROM raw_views
              WHERE project_id = pd.project_id AND day = pd.day
            )
          )
          WINDOW w AS (PARTITION BY kind, actor_id ORDER BY t)
        )
      )
      GROUP BY kind, actor_id, skey
    )
    GROUP BY kind
  )
)) r;
