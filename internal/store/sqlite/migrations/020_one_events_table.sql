-- 020: one raw table for views and product events.
-- Spec: docs/superpowers/specs/2026-09-24-one-events-table-design.md
--
-- events becomes the union of both raw tables plus family ('views' or
-- 'product', written by ingest; the database holds no list of families).
-- Raw tables only hold days not yet rolled up, so this copies about a
-- month of rows. Copied views are named from their kind, the pairing
-- viewName() uses: the original name was never stored.
--
-- Every view that read a raw table is dropped first: ALTER TABLE ... RENAME
-- re-parses the schema and would fail on a view naming a dropped table.
-- They are recreated at the end against raw_views and raw_product, the
-- only read path (v_events_flat excepted: it holds both families).
DROP VIEW IF EXISTS v_events_flat;
DROP VIEW v_identity_daily;
DROP VIEW v_product_attrs;
DROP VIEW v_product_daily;
DROP VIEW v_product_totals;
DROP VIEW v_views_app_versions;
DROP VIEW v_views_browsers;
DROP VIEW v_views_consent;
DROP VIEW v_views_countries;
DROP VIEW v_views_daily;
DROP VIEW v_views_devices;
DROP VIEW v_views_displays;
DROP VIEW v_views_hosts;
DROP VIEW v_views_locales;
DROP VIEW v_views_os;
DROP VIEW v_views_paths;
DROP VIEW v_views_platforms;
DROP VIEW v_views_referrers;
DROP VIEW v_views_utm;

CREATE TABLE events_new (
    id              TEXT PRIMARY KEY,
    project_id      INTEGER NOT NULL,
    family          TEXT NOT NULL,
    event_name      TEXT NOT NULL,
    ts              TEXT NOT NULL,
    day             TEXT GENERATED ALWAYS AS (substr(ts,1,10)) STORED,
    received_at     TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT '',
    actor_id        TEXT NOT NULL,
    actor_kind      TEXT NOT NULL DEFAULT '',
    user_id         TEXT NOT NULL DEFAULT '',
    group_id        TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    host            TEXT NOT NULL DEFAULT '',
    path            TEXT NOT NULL DEFAULT '',
    referrer_source TEXT NOT NULL DEFAULT '',
    utm_source      TEXT NOT NULL DEFAULT '',
    utm_medium      TEXT NOT NULL DEFAULT '',
    utm_campaign    TEXT NOT NULL DEFAULT '',
    platform        TEXT NOT NULL DEFAULT 'unknown',
    os              TEXT NOT NULL DEFAULT '',
    os_version      TEXT NOT NULL DEFAULT '',
    os_name         TEXT NOT NULL DEFAULT '',
    browser         TEXT NOT NULL DEFAULT '',
    browser_version TEXT NOT NULL DEFAULT '',
    browser_locale  TEXT NOT NULL DEFAULT '',
    app_version     TEXT NOT NULL DEFAULT '',
    app_locale      TEXT NOT NULL DEFAULT '',
    device          TEXT NOT NULL DEFAULT '',
    device_model    TEXT NOT NULL DEFAULT '',
    display_width   INTEGER NOT NULL DEFAULT 0,
    display_height  INTEGER NOT NULL DEFAULT 0,
    country         TEXT NOT NULL DEFAULT '',
    consent         INTEGER,
    attributes      TEXT NOT NULL DEFAULT '{}'
);

INSERT INTO events_new (id, project_id, family, event_name, ts, received_at, kind,
    actor_id, actor_kind, user_id, group_id, session_id, host, path, referrer_source,
    utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser,
    browser_version, browser_locale, app_version, app_locale, device, device_model,
    display_width, display_height, country, consent, attributes)
SELECT id, project_id, 'views', CASE kind WHEN 'web' THEN '$page_view' ELSE '$screen_view' END,
    ts, received_at, kind,
    actor_id, actor_kind, user_id, group_id, session_id, host, path, referrer_source,
    utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser,
    browser_version, browser_locale, app_version, app_locale, device, device_model,
    display_width, display_height, country, consent, '{}'
FROM views;

-- OR IGNORE: the two old tables had separate primary keys, so a client that
-- reused a UUID can hold it once in each. The view wins and that product
-- event is dropped, silently: losing the rare row a client double-used is
-- accepted over failing the upgrade or minting an id nobody sent.
INSERT OR IGNORE INTO events_new (id, project_id, family, event_name, ts, received_at,
    actor_id, actor_kind, user_id, group_id, platform, os, app_version, app_locale,
    consent, attributes)
SELECT id, project_id, 'product', event_name, ts, received_at,
    actor_id, actor_kind, user_id, group_id, platform, os, app_version, app_locale,
    consent, attributes
FROM events;

DROP TABLE views;
DROP TABLE events;
ALTER TABLE events_new RENAME TO events;

-- One index serves every read. Leading on family lets a union arm that
-- gets no project filter pushed into it (the live halves of v_product_attrs
-- and v_identity_daily) search one family instead of scanning both;
-- project_id and day serve every ranged read and the daily pass;
-- event_name serves the per-event product rollup. The old actor and
-- session indexes had no reader and are not carried over.
CREATE INDEX idx_events_family ON events(family, project_id, day, event_name);

-- The only read path for Go code and every v_* definition: a query cannot
-- forget the family filter it never writes. SQLite flattens these into the
-- outer query, so the index above still applies.
CREATE VIEW raw_views   AS SELECT * FROM events WHERE family = 'views';
CREATE VIEW raw_product AS SELECT * FROM events WHERE family = 'product';

-- Base shape only; RebuildFlatView replaces it with the declared attr_
-- columns on the next boot or registry write.
CREATE VIEW v_events_flat AS SELECT id, project_id, family, event_name, actor_id, kind, session_id, user_id, group_id, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, ts, attributes FROM events;

-- ===== Every view that read a raw table, unchanged but for its source =====
-- Generated by rewriting the schema-19 definitions: FROM/JOIN views ->
-- raw_views, FROM/JOIN events -> raw_product, substr(ts,1,10) -> day.

CREATE VIEW v_identity_daily AS
SELECT project_id, day, kind, id, actors, users, views, events
FROM agg_identity_daily
UNION ALL
SELECT project_id, day, kind, id, actors, users, views, events
FROM (
  SELECT project_id, day, kind, id,
         COUNT(DISTINCT actor_id) AS actors,
         CASE WHEN kind = 'user' THEN 1 ELSE COUNT(DISTINCT NULLIF(user_id, '')) END AS users,
         SUM(is_view) AS views, SUM(is_event) AS events,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, kind ORDER BY COUNT(*) DESC, id) AS rn
  FROM (
    SELECT project_id, day, 'user' AS kind, user_id AS id,
           actor_id, user_id, 1 AS is_view, 0 AS is_event
    FROM raw_views WHERE user_id <> ''
    UNION ALL
    SELECT project_id, day, 'user', user_id, actor_id, user_id, 0, 1
    FROM raw_product WHERE user_id <> ''
    UNION ALL
    SELECT project_id, day, 'group', group_id, actor_id, user_id, 1, 0
    FROM raw_views WHERE group_id <> ''
    UNION ALL
    SELECT project_id, day, 'group', group_id, actor_id, user_id, 0, 1
    FROM raw_product WHERE group_id <> ''
  )
  GROUP BY project_id, day, kind, id
) live
WHERE rn <= 500
  AND NOT EXISTS (
    SELECT 1 FROM agg_identity_daily g
    WHERE g.project_id = live.project_id AND g.day = live.day);

-- v_product_attrs: 016's shape over raw_product, with an arm per
-- store.SystemAttributes entry (eight) and declared $ keys read from their
-- column (store.DeclarableAttributes). The CASE and the Go map must list
-- the same keys; TestProductAttrsDeclaredSystemKeysAcrossBoundary fails on
-- any key one of them misses. NULLIF turns an empty column into "absent",
-- the same meaning json_extract's NULL has for a custom key.
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
declared_vals AS (
  SELECT e.project_id AS project_id, e.day AS day,
         e.event_name AS event_name, d.attr_key AS attr_key,
         CASE d.attr_key
           WHEN '$host'            THEN NULLIF(e.host, '')
           WHEN '$path'            THEN NULLIF(e.path, '')
           WHEN '$referrer'        THEN NULLIF(e.referrer_source, '')
           WHEN '$utm_source'      THEN NULLIF(e.utm_source, '')
           WHEN '$utm_medium'      THEN NULLIF(e.utm_medium, '')
           WHEN '$utm_campaign'    THEN NULLIF(e.utm_campaign, '')
           WHEN '$os_version'      THEN NULLIF(e.os_version, '')
           WHEN '$browser_version' THEN NULLIF(e.browser_version, '')
           WHEN '$device_model'    THEN NULLIF(e.device_model, '')
           ELSE CASE WHEN d.attr_key LIKE '$%' THEN NULL
                     ELSE json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') END
         END AS attr_value,
         e.actor_id AS actor_id, e.group_id AS group_id
  FROM raw_product e
  JOIN declared d ON d.project_id = e.project_id
),
vals AS (
  SELECT project_id, day, event_name, attr_key, attr_value, actor_id, group_id
  FROM declared_vals WHERE attr_value IS NOT NULL
  UNION ALL
  SELECT project_id, day, event_name, '$platform', platform, actor_id, group_id FROM raw_product WHERE platform <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$os', os, actor_id, group_id FROM raw_product WHERE os <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$app_version', app_version, actor_id, group_id FROM raw_product WHERE app_version <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$app_locale', app_locale, actor_id, group_id FROM raw_product WHERE app_locale <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$kind', kind, actor_id, group_id FROM raw_product WHERE kind <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$browser', browser, actor_id, group_id FROM raw_product WHERE browser <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$device', device, actor_id, group_id FROM raw_product WHERE device <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$browser_locale', browser_locale, actor_id, group_id FROM raw_product WHERE browser_locale <> ''
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

CREATE VIEW v_product_daily AS
SELECT project_id, day, event_name, count, unique_users FROM agg_product_daily
UNION ALL
SELECT project_id, day, event_name, COUNT(*), COUNT(DISTINCT actor_id)
FROM raw_product GROUP BY project_id, day, event_name;

CREATE VIEW v_product_totals AS
SELECT project_id, day, total_events, active_users FROM agg_product_totals
UNION ALL
SELECT project_id, day, COUNT(*), COUNT(DISTINCT actor_id)
FROM raw_product GROUP BY project_id, day;

CREATE VIEW v_views_app_versions AS
SELECT project_id, day, platform, app_version, visitors, views FROM agg_views_app_versions
UNION ALL
SELECT project_id, day, platform, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.platform, v.app_version, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, platform, app_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, platform, app_version) AS rn
    FROM raw_views WHERE app_version <> '' GROUP BY project_id, day, platform, app_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.platform = v.platform AND r.app_version = v.app_version
  WHERE v.app_version <> ''
)
GROUP BY project_id, day, platform, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END;

CREATE VIEW v_views_browsers AS
SELECT project_id, day, browser, browser_version, visitors, views FROM agg_views_browsers
UNION ALL
SELECT project_id, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.browser, v.browser_version, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, browser, browser_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, browser, browser_version) AS rn
    FROM raw_views GROUP BY project_id, day, browser, browser_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.browser = v.browser AND r.browser_version = v.browser_version
)
GROUP BY project_id, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END;

CREATE VIEW v_views_consent AS
SELECT project_id, day, consent, visitors, views FROM agg_views_consent
UNION ALL
SELECT project_id, day,
       CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM raw_views v
GROUP BY project_id, day, CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END;

CREATE VIEW v_views_countries AS
SELECT project_id, day, country, visitors, views FROM agg_views_countries
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.country, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, country,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, country) AS rn
    FROM raw_views GROUP BY project_id, day, country
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.country = v.country
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END;

CREATE VIEW v_views_daily AS
SELECT project_id, day, kind, visitors, views, sessions, bounces, duration_sec FROM agg_views_daily
UNION ALL
SELECT c.project_id, c.day, c.kind, c.visitors, c.views, p.sessions, p.bounces, p.duration_sec
FROM (
  -- visitors and views per bucketed kind
  SELECT b.project_id, b.day, b.kind, COUNT(DISTINCT b.actor_id) AS visitors, COUNT(*) AS views
  FROM (
    SELECT v.project_id, v.day,
           CASE WHEN k.rn <= 500 THEN v.kind ELSE '(other)' END AS kind, v.actor_id
    FROM raw_views v
    JOIN (
      SELECT project_id, day, kind,
             ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, kind) AS rn
      FROM raw_views GROUP BY project_id, day, kind
    ) k ON k.project_id = v.project_id AND k.day = v.day AND k.kind = v.kind
  ) b
  GROUP BY b.project_id, b.day, b.kind
) c
JOIN (
  -- sessions, bounces and duration per bucketed kind
  WITH src AS (
    SELECT v.project_id, v.day,
           CASE WHEN k.rn <= 500 THEN v.kind ELSE '(other)' END AS kind,
           v.actor_id, v.session_id, CAST(strftime('%s', v.ts) AS INTEGER) AS t
    FROM raw_views v
    JOIN (
      SELECT project_id, day, kind,
             ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, kind) AS rn
      FROM raw_views GROUP BY project_id, day, kind
    ) k ON k.project_id = v.project_id AND k.day = v.day AND k.kind = v.kind
  ),
  marked AS (
    SELECT project_id, day, kind, actor_id, session_id, t,
           CASE WHEN session_id <> '' THEN 0
                WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
                ELSE 0 END AS new_session
    FROM src WINDOW w AS (PARTITION BY project_id, day, kind, actor_id ORDER BY t)
  ),
  keyed AS (
    SELECT project_id, day, kind, actor_id, t,
           CASE WHEN session_id <> '' THEN session_id
                ELSE CAST(SUM(new_session) OVER (PARTITION BY project_id, day, kind, actor_id ORDER BY t) AS TEXT)
           END AS skey
    FROM marked
  ),
  spans AS (
    SELECT project_id, day, kind, actor_id, skey, COUNT(*) AS view_count, MAX(t) - MIN(t) AS dur
    FROM keyed GROUP BY project_id, day, kind, actor_id, skey
  )
  SELECT project_id, day, kind, COUNT(*) AS sessions,
         SUM(CASE WHEN view_count = 1 THEN 1 ELSE 0 END) AS bounces,
         COALESCE(SUM(dur), 0) AS duration_sec
  FROM spans GROUP BY project_id, day, kind
) p ON p.project_id = c.project_id AND p.day = c.day AND p.kind = c.kind;

CREATE VIEW v_views_devices AS
SELECT project_id, day, device, device_model, visitors, views FROM agg_views_devices
UNION ALL
SELECT project_id, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.device, v.device_model, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, device, device_model,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, device, device_model) AS rn
    FROM raw_views GROUP BY project_id, day, device, device_model
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.device = v.device AND r.device_model = v.device_model
)
GROUP BY project_id, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END;

CREATE VIEW v_views_displays AS
SELECT project_id, day, display, visitors, views FROM agg_views_displays
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.display_width || 'x' || v.display_height AS display, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, display_width || 'x' || display_height AS display,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, display_width || 'x' || display_height) AS rn
    FROM raw_views WHERE display_width > 0 AND display_height > 0
    GROUP BY project_id, day, display_width || 'x' || display_height
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.display = v.display_width || 'x' || v.display_height
  WHERE v.display_width > 0 AND v.display_height > 0
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END;

CREATE VIEW v_views_hosts AS
SELECT project_id, day, host, visitors, views FROM agg_views_hosts
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.host, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, host,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, host) AS rn
    FROM raw_views GROUP BY project_id, day, host
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.host = v.host
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END;

CREATE VIEW v_views_locales AS
SELECT project_id, day, browser_locale, app_locale, visitors, views FROM agg_views_locales
UNION ALL
SELECT project_id, day, browser_locale, CASE WHEN rn <= 500 THEN app_locale ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.browser_locale, v.app_locale, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, browser_locale, app_locale,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, browser_locale, app_locale) AS rn
    FROM raw_views WHERE NOT (browser_locale = '' AND app_locale = '')
    GROUP BY project_id, day, browser_locale, app_locale
  ) r ON r.project_id = v.project_id AND r.day = v.day
     AND r.browser_locale = v.browser_locale AND r.app_locale = v.app_locale
  WHERE NOT (v.browser_locale = '' AND v.app_locale = '')
)
GROUP BY project_id, day, browser_locale, CASE WHEN rn <= 500 THEN app_locale ELSE '(other)' END;

CREATE VIEW v_views_os AS
SELECT project_id, day, os, os_version, visitors, views FROM agg_views_os
UNION ALL
SELECT project_id, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.os, v.os_version, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, os, os_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, os, os_version) AS rn
    FROM raw_views GROUP BY project_id, day, os, os_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.os = v.os AND r.os_version = v.os_version
)
GROUP BY project_id, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END;

CREATE VIEW v_views_paths AS
SELECT project_id, day, path, visitors, views FROM agg_views_paths
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.path, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, path,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, path) AS rn
    FROM raw_views GROUP BY project_id, day, path
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.path = v.path
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END;

CREATE VIEW v_views_platforms AS
SELECT project_id, day, platform, visitors, views FROM agg_views_platforms
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN platform ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.platform, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, platform,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, platform) AS rn
    FROM raw_views GROUP BY project_id, day, platform
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.platform = v.platform
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN platform ELSE '(other)' END;

CREATE VIEW v_views_referrers AS
SELECT project_id, day, source, visitors, views FROM agg_views_referrers
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.referrer_source AS source, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, referrer_source AS source,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, referrer_source) AS rn
    FROM raw_views GROUP BY project_id, day, referrer_source
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.source = v.referrer_source
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END;

CREATE VIEW v_views_utm AS
SELECT project_id, day, utm_source, utm_medium, utm_campaign, visitors, views FROM agg_views_utm
UNION ALL
SELECT project_id, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.utm_source, v.utm_medium, v.utm_campaign, v.actor_id, r.rn
  FROM raw_views v
  JOIN (
    SELECT project_id, day, utm_source, utm_medium, utm_campaign,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, utm_source, utm_medium, utm_campaign) AS rn
    FROM raw_views WHERE NOT (utm_source='' AND utm_medium='' AND utm_campaign='')
    GROUP BY project_id, day, utm_source, utm_medium, utm_campaign
  ) r ON r.project_id = v.project_id AND r.day = v.day
     AND r.utm_source = v.utm_source AND r.utm_medium = v.utm_medium AND r.utm_campaign = v.utm_campaign
  WHERE NOT (v.utm_source='' AND v.utm_medium='' AND v.utm_campaign='')
)
GROUP BY project_id, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END;
