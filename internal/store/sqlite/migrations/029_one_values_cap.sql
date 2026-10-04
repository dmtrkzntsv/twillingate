-- 029: one values cap. ATTRIBUTE_VALUES_TOP_N (meta row attributes_top_n)
-- caps the views breakdowns and v_views_daily's kinds as well as the
-- attributes, so the views_dimensions_top_n row is no longer written or
-- read and goes. The thirteen views reading it are recreated as 025 left
-- them but for the key and the fallback for a missing or malformed row,
-- now 100 like the attribute views'.

DELETE FROM meta WHERE key = 'views_dimensions_top_n';

-- The v_views_* breakdowns: one aggregate per full key over raw_views,
-- ranked in the same SELECT, with each key's actors kept as a JSON array;
-- then one row per kept key (the day's most viewed up to the cap, the
-- last column '(other)' past them) and the columns before it. Views add up; distinct
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
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, platform, app_version, visitors, views FROM agg_views_app_versions
UNION ALL
SELECT project_id, day, platform, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, platform, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, platform,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, platform, app_version) <= (SELECT n FROM cap)
                THEN app_version ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    WHERE app_version <> ''
    GROUP BY project_id, day, platform, app_version
  )
  WINDOW f AS (PARTITION BY project_id, day, platform, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_browsers AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, browser, browser_version, visitors, views FROM agg_views_browsers
UNION ALL
SELECT project_id, day, browser, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, browser, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, browser,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, browser, browser_version) <= (SELECT n FROM cap)
                THEN browser_version ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    GROUP BY project_id, day, browser, browser_version
  )
  WINDOW f AS (PARTITION BY project_id, day, browser, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_countries AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, country, visitors, views FROM agg_views_countries
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, country) <= (SELECT n FROM cap)
                THEN country ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    GROUP BY project_id, day, country
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_devices AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, device, device_model, visitors, views FROM agg_views_devices
UNION ALL
SELECT project_id, day, device, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, device, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, device,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, device, device_model) <= (SELECT n FROM cap)
                THEN device_model ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    GROUP BY project_id, day, device, device_model
  )
  WINDOW f AS (PARTITION BY project_id, day, device, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_displays AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, display, visitors, views FROM agg_views_displays
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, display_width || 'x' || display_height) <= (SELECT n FROM cap)
                THEN display_width || 'x' || display_height ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    WHERE display_width > 0 AND display_height > 0
    GROUP BY project_id, day, display_width || 'x' || display_height
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_hosts AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, host, visitors, views FROM agg_views_hosts
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, host) <= (SELECT n FROM cap)
                THEN host ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    GROUP BY project_id, day, host
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_locales AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, browser_locale, app_locale, visitors, views FROM agg_views_locales
UNION ALL
SELECT project_id, day, browser_locale, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, browser_locale, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, browser_locale,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, browser_locale, app_locale) <= (SELECT n FROM cap)
                THEN app_locale ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    WHERE NOT (browser_locale = '' AND app_locale = '')
    GROUP BY project_id, day, browser_locale, app_locale
  )
  WINDOW f AS (PARTITION BY project_id, day, browser_locale, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_os AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, os, os_version, visitors, views FROM agg_views_os
UNION ALL
SELECT project_id, day, os, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, os, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, os,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, os, os_version) <= (SELECT n FROM cap)
                THEN os_version ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    GROUP BY project_id, day, os, os_version
  )
  WINDOW f AS (PARTITION BY project_id, day, os, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_paths AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, path, visitors, views FROM agg_views_paths
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, path) <= (SELECT n FROM cap)
                THEN path ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    GROUP BY project_id, day, path
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_platforms AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, platform, visitors, views FROM agg_views_platforms
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, platform) <= (SELECT n FROM cap)
                THEN platform ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    GROUP BY project_id, day, platform
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_referrers AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, source, visitors, views FROM agg_views_referrers
UNION ALL
SELECT project_id, day, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, referrer_source) <= (SELECT n FROM cap)
                THEN referrer_source ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
    FROM raw_views
    GROUP BY project_id, day, referrer_source
  )
  WINDOW f AS (PARTITION BY project_id, day, kept)
)
WHERE rf = 1;

CREATE VIEW v_views_utm AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, utm_source, utm_medium, utm_campaign, visitors, views FROM agg_views_utm
UNION ALL
SELECT project_id, day, utm_source, utm_medium, kept,
       CASE WHEN keys = 1 THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(merged)) END,
       views
FROM (
  SELECT project_id, day, utm_source, utm_medium, kept, u,
         COUNT(*) OVER f AS keys, SUM(c) OVER f AS views,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',') OVER f || ']' AS merged,
         ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, utm_source, utm_medium,
           CASE WHEN ROW_NUMBER() OVER (PARTITION BY project_id, day
                                        ORDER BY COUNT(*) DESC, utm_source, utm_medium, utm_campaign) <= (SELECT n FROM cap)
                THEN utm_campaign ELSE '(other)' END AS kept,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors
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
-- too; kinds past the cap (the day's most viewed first) are '(other)', as
-- before.
DROP VIEW v_views_daily;
CREATE VIEW v_views_daily AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
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
            -- the day's views under their bucketed kind: the cap's kinds
            -- with the most views that day, the rest as (other)
            SELECT CASE WHEN DENSE_RANK() OVER (ORDER BY kind_views DESC, kind) <= (SELECT n FROM cap)
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
