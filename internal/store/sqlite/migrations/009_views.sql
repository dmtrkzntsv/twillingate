-- One views family (views-family spec §6, §11). web_hits and app_views fold
-- into `views`; every agg_web_*/agg_app_* folds into agg_views_*; retention
-- and identity aggregates lose their surface/hits split. Irreversible.
--
-- Views are dropped first and recreated at the end, as 003 did: RENAME
-- COLUMN must not leave a stale view definition behind.

DROP VIEW IF EXISTS v_web_daily;
DROP VIEW IF EXISTS v_web_pages;
DROP VIEW IF EXISTS v_web_hosts;
DROP VIEW IF EXISTS v_web_referrers;
DROP VIEW IF EXISTS v_web_countries;
DROP VIEW IF EXISTS v_web_devices;
DROP VIEW IF EXISTS v_web_browsers;
DROP VIEW IF EXISTS v_web_os;
DROP VIEW IF EXISTS v_web_utm;
DROP VIEW IF EXISTS v_app_daily;
DROP VIEW IF EXISTS v_app_screens;
DROP VIEW IF EXISTS v_app_versions;
DROP VIEW IF EXISTS v_app_os;
DROP VIEW IF EXISTS v_app_devices;
DROP VIEW IF EXISTS v_app_countries;
DROP VIEW IF EXISTS v_product_daily;
DROP VIEW IF EXISTS v_product_totals;
DROP VIEW IF EXISTS v_product_attrs;
DROP VIEW IF EXISTS v_identity_daily;
DROP VIEW IF EXISTS v_retention;
DROP VIEW IF EXISTS v_events_flat;

-- ===== raw: views =====
CREATE TABLE views (
    id              TEXT PRIMARY KEY,
    project         TEXT NOT NULL,
    ts              TEXT NOT NULL,
    received_at     TEXT NOT NULL,
    kind            TEXT NOT NULL,
    actor_id        TEXT NOT NULL,
    actor_kind      TEXT NOT NULL,
    user_id         TEXT NOT NULL DEFAULT '',
    group_id        TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    host            TEXT NOT NULL DEFAULT '',
    path            TEXT NOT NULL,
    referrer_source TEXT NOT NULL DEFAULT '',
    utm_source      TEXT NOT NULL DEFAULT '',
    utm_medium      TEXT NOT NULL DEFAULT '',
    utm_campaign    TEXT NOT NULL DEFAULT '',
    os              TEXT NOT NULL DEFAULT '',
    os_version      TEXT NOT NULL DEFAULT '',
    browser         TEXT NOT NULL DEFAULT '',
    browser_version TEXT NOT NULL DEFAULT '',
    app_version     TEXT NOT NULL DEFAULT '',
    device          TEXT NOT NULL DEFAULT '',
    device_model    TEXT NOT NULL DEFAULT '',
    locale          TEXT NOT NULL DEFAULT '',
    display_width   INTEGER NOT NULL DEFAULT 0,
    display_height  INTEGER NOT NULL DEFAULT 0,
    country         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_views_project_ts ON views(project, ts);
CREATE INDEX idx_views_actor      ON views(project, actor_id, ts);
CREATE INDEX idx_views_session    ON views(project, session_id, ts);

-- Old raw rows: web rows were identified by user id or the connection
-- hash; app rows by user id or install id. Declared platform tokens fold
-- into the parser's OS vocabulary (enrich.NormalizeOS).
INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind,
    user_id, group_id, session_id, host, path, referrer_source,
    utm_source, utm_medium, utm_campaign, os, os_version, browser,
    browser_version, app_version, device, device_model, locale,
    display_width, display_height, country)
SELECT id, project, ts, received_at, 'web', actor_id,
       CASE WHEN user_id <> '' THEN 'user' ELSE 'connection' END,
       user_id, group_id, '', host, path, referrer_source,
       utm_source, utm_medium, utm_campaign, os, '', browser,
       '', '', device, '', '', 0, 0, country
FROM web_hits;

INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind,
    user_id, group_id, session_id, host, path, referrer_source,
    utm_source, utm_medium, utm_campaign, os, os_version, browser,
    browser_version, app_version, device, device_model, locale,
    display_width, display_height, country)
SELECT id, project, ts, received_at, 'app', actor_id,
       CASE WHEN user_id <> '' THEN 'user' ELSE 'install' END,
       user_id, group_id, session_id, '', screen, '',
       '', '', '',
       CASE lower(platform)
         WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
         WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
         WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
         ELSE platform END,
       os_version, '', '', app_version, '', device_model, locale,
       0, 0, country
FROM app_views;

-- ===== aggregates =====
-- Counts, not rates. visitors = COUNT(DISTINCT actor_id), views = COUNT(*).
CREATE TABLE agg_views_daily (
    project TEXT NOT NULL, day TEXT NOT NULL, kind TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    sessions INTEGER NOT NULL, bounces INTEGER NOT NULL, duration_sec INTEGER NOT NULL,
    PRIMARY KEY (project, day, kind)
) WITHOUT ROWID;
CREATE TABLE agg_views_paths (
    project TEXT NOT NULL, day TEXT NOT NULL, path TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, path)
) WITHOUT ROWID;
CREATE TABLE agg_views_hosts (
    project TEXT NOT NULL, day TEXT NOT NULL, host TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, host)
) WITHOUT ROWID;
CREATE TABLE agg_views_referrers (
    project TEXT NOT NULL, day TEXT NOT NULL, source TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, source)
) WITHOUT ROWID;
CREATE TABLE agg_views_utm (
    project TEXT NOT NULL, day TEXT NOT NULL,
    utm_source TEXT NOT NULL, utm_medium TEXT NOT NULL, utm_campaign TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, utm_source, utm_medium, utm_campaign)
) WITHOUT ROWID;
CREATE TABLE agg_views_countries (
    project TEXT NOT NULL, day TEXT NOT NULL, country TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, country)
) WITHOUT ROWID;
CREATE TABLE agg_views_os (
    project TEXT NOT NULL, day TEXT NOT NULL, os TEXT NOT NULL, os_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, os, os_version)
) WITHOUT ROWID;
CREATE TABLE agg_views_browsers (
    project TEXT NOT NULL, day TEXT NOT NULL, browser TEXT NOT NULL, browser_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, browser, browser_version)
) WITHOUT ROWID;
CREATE TABLE agg_views_app_versions (
    project TEXT NOT NULL, day TEXT NOT NULL, os TEXT NOT NULL, app_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, os, app_version)
) WITHOUT ROWID;
CREATE TABLE agg_views_devices (
    project TEXT NOT NULL, day TEXT NOT NULL, device TEXT NOT NULL, device_model TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, device, device_model)
) WITHOUT ROWID;
CREATE TABLE agg_views_displays (
    project TEXT NOT NULL, day TEXT NOT NULL, display TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, display)
) WITHOUT ROWID;

-- Fold the two old families. Sums are exact: the two populations had
-- disjoint actor ids, so distinct-visitor counts add. App days carry zero
-- bounces (the old family never counted them).
INSERT INTO agg_views_daily (project, day, kind, visitors, views, sessions, bounces, duration_sec)
SELECT project, day, 'web', visitors, pageviews, sessions, bounces, duration_sec FROM agg_web_daily;
INSERT INTO agg_views_daily (project, day, kind, visitors, views, sessions, bounces, duration_sec)
SELECT project, day, 'app', actives, views, sessions, 0, duration_sec FROM agg_app_daily;

INSERT INTO agg_views_paths (project, day, path, visitors, views)
SELECT project, day, path, SUM(visitors), SUM(views) FROM (
  SELECT project, day, path, visitors, pageviews AS views FROM agg_web_pages
  UNION ALL
  SELECT project, day, screen, actives, views FROM agg_app_screens
) GROUP BY project, day, path;

INSERT INTO agg_views_hosts (project, day, host, visitors, views)
SELECT project, day, host, visitors, pageviews FROM agg_web_hosts;
INSERT INTO agg_views_referrers (project, day, source, visitors, views)
SELECT project, day, source, visitors, pageviews FROM agg_web_referrers;
INSERT INTO agg_views_utm (project, day, utm_source, utm_medium, utm_campaign, visitors, views)
SELECT project, day, utm_source, utm_medium, utm_campaign, visitors, pageviews FROM agg_web_utm;
INSERT INTO agg_views_browsers (project, day, browser, browser_version, visitors, views)
SELECT project, day, browser, '', visitors, pageviews FROM agg_web_browsers;

INSERT INTO agg_views_countries (project, day, country, visitors, views)
SELECT project, day, country, SUM(visitors), SUM(views) FROM (
  SELECT project, day, country, visitors, pageviews AS views FROM agg_web_countries
  UNION ALL
  SELECT project, day, country, actives, views FROM agg_app_countries
) GROUP BY project, day, country;

INSERT INTO agg_views_os (project, day, os, os_version, visitors, views)
SELECT project, day, os, os_version, SUM(visitors), SUM(views) FROM (
  SELECT project, day, os, '' AS os_version, visitors, pageviews AS views FROM agg_web_os
  UNION ALL
  SELECT project, day,
         CASE lower(platform)
           WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
           WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
           WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
           ELSE platform END,
         os_version, actives, views FROM agg_app_os
) GROUP BY project, day, os, os_version;

INSERT INTO agg_views_app_versions (project, day, os, app_version, visitors, views)
SELECT project, day,
       CASE lower(platform)
         WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
         WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
         WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
         ELSE platform END,
       app_version, SUM(actives), SUM(views)
FROM agg_app_versions GROUP BY project, day, 3, app_version;

INSERT INTO agg_views_devices (project, day, device, device_model, visitors, views)
SELECT project, day, device, '', visitors, pageviews FROM agg_web_devices;
INSERT INTO agg_views_devices (project, day, device, device_model, visitors, views)
SELECT project, day, '', device_model, actives, views FROM agg_app_devices;

-- ===== retention: surface -> actor_kind =====
CREATE TABLE actors_new (
    project TEXT NOT NULL, actor_id TEXT NOT NULL,
    actor_kind TEXT NOT NULL,
    first_seen_day TEXT NOT NULL,
    last_seen_day  TEXT NOT NULL,
    PRIMARY KEY (project, actor_id)
) WITHOUT ROWID;
INSERT INTO actors_new SELECT project, actor_id,
       CASE surface WHEN 'app' THEN 'install' ELSE 'user' END,
       first_seen_day, last_seen_day FROM actors;
DROP TABLE actors;
ALTER TABLE actors_new RENAME TO actors;
CREATE INDEX idx_actors_last_seen ON actors(project, last_seen_day);

CREATE TABLE agg_retention_new (
    project TEXT NOT NULL, actor_kind TEXT NOT NULL,
    cohort_day TEXT NOT NULL, day_offset INTEGER NOT NULL,
    actors INTEGER NOT NULL,
    PRIMARY KEY (project, actor_kind, cohort_day, day_offset)
) WITHOUT ROWID;
INSERT INTO agg_retention_new SELECT project,
       CASE surface WHEN 'app' THEN 'install' ELSE 'user' END,
       cohort_day, day_offset, actors FROM agg_retention;
DROP TABLE agg_retention;
ALTER TABLE agg_retention_new RENAME TO agg_retention;

-- ===== identity daily: hits + views -> views =====
CREATE TABLE agg_identity_daily_new (
    project TEXT NOT NULL, day TEXT NOT NULL,
    kind TEXT NOT NULL, id TEXT NOT NULL,
    actors INTEGER NOT NULL, users INTEGER NOT NULL,
    views INTEGER NOT NULL, events INTEGER NOT NULL,
    PRIMARY KEY (project, day, kind, id)
) WITHOUT ROWID;
INSERT INTO agg_identity_daily_new
SELECT project, day, kind, id, actors, users, hits + views, events FROM agg_identity_daily;
DROP TABLE agg_identity_daily;
ALTER TABLE agg_identity_daily_new RENAME TO agg_identity_daily;

-- ===== product events: actor_kind, platform -> os =====
ALTER TABLE product_events ADD COLUMN actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events RENAME COLUMN platform TO os;
UPDATE product_events SET os = CASE lower(os)
    WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
    WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
    WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
    ELSE os END
  WHERE os <> '';

-- '$platform' rows become '$os' with normalised values; 'ios' and 'iOS'
-- rows for one (event, day) collapse into one, summed.
CREATE TEMP TABLE os_attrs AS
SELECT project, day, event_name, '$os' AS attr_key,
       CASE lower(attr_value)
         WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
         WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
         WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
         ELSE attr_value END AS attr_value,
       SUM(count) AS count, SUM(unique_users) AS unique_users
FROM agg_product_attrs WHERE attr_key = '$platform'
GROUP BY project, day, event_name, 5;
DELETE FROM agg_product_attrs WHERE attr_key = '$platform';
INSERT OR REPLACE INTO agg_product_attrs (project, day, event_name, attr_key, attr_value, count, unique_users)
SELECT project, day, event_name, attr_key, attr_value, count, unique_users FROM os_attrs;
DROP TABLE os_attrs;

-- ===== drop the old families =====
DROP TABLE web_hits;
DROP TABLE app_views;
DROP TABLE agg_web_daily;    DROP TABLE agg_web_pages;   DROP TABLE agg_web_hosts;
DROP TABLE agg_web_referrers; DROP TABLE agg_web_countries; DROP TABLE agg_web_devices;
DROP TABLE agg_web_browsers; DROP TABLE agg_web_os;      DROP TABLE agg_web_utm;
DROP TABLE agg_app_daily;    DROP TABLE agg_app_screens; DROP TABLE agg_app_versions;
DROP TABLE agg_app_os;       DROP TABLE agg_app_devices; DROP TABLE agg_app_countries;

-- ===== stitch views: aggregates UNION ALL live computation over raw rows =====
-- Raw and aggregated days are disjoint (aggregation deletes raw in-tx), so
-- no day is double counted. Live halves mirror aggregate_views.go exactly,
-- including the per-day top-N cap and the "(other)" tail.

CREATE VIEW v_views_daily AS
SELECT project, day, kind, visitors, views, sessions, bounces, duration_sec FROM agg_views_daily
UNION ALL
SELECT c.project, c.day, c.kind, c.visitors, c.views, p.sessions, p.bounces, p.duration_sec
FROM (
  -- visitors and views per bucketed kind
  SELECT b.project, b.day, b.kind, COUNT(DISTINCT b.actor_id) AS visitors, COUNT(*) AS views
  FROM (
    SELECT v.project, substr(v.ts,1,10) AS day,
           CASE WHEN k.rn <= 500 THEN v.kind ELSE '(other)' END AS kind, v.actor_id
    FROM views v
    JOIN (
      SELECT project, substr(ts,1,10) AS day, kind,
             ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, kind) AS rn
      FROM views GROUP BY project, substr(ts,1,10), kind
    ) k ON k.project = v.project AND k.day = substr(v.ts,1,10) AND k.kind = v.kind
  ) b
  GROUP BY b.project, b.day, b.kind
) c
JOIN (
  -- sessions, bounces and duration per bucketed kind
  WITH src AS (
    SELECT v.project, substr(v.ts,1,10) AS day,
           CASE WHEN k.rn <= 500 THEN v.kind ELSE '(other)' END AS kind,
           v.actor_id, v.session_id, CAST(strftime('%s', v.ts) AS INTEGER) AS t
    FROM views v
    JOIN (
      SELECT project, substr(ts,1,10) AS day, kind,
             ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, kind) AS rn
      FROM views GROUP BY project, substr(ts,1,10), kind
    ) k ON k.project = v.project AND k.day = substr(v.ts,1,10) AND k.kind = v.kind
  ),
  marked AS (
    SELECT project, day, kind, actor_id, session_id, t,
           CASE WHEN session_id <> '' THEN 0
                WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
                ELSE 0 END AS new_session
    FROM src WINDOW w AS (PARTITION BY project, day, kind, actor_id ORDER BY t)
  ),
  keyed AS (
    SELECT project, day, kind, actor_id, t,
           CASE WHEN session_id <> '' THEN session_id
                ELSE CAST(SUM(new_session) OVER (PARTITION BY project, day, kind, actor_id ORDER BY t) AS TEXT)
           END AS skey
    FROM marked
  ),
  spans AS (
    SELECT project, day, kind, actor_id, skey, COUNT(*) AS view_count, MAX(t) - MIN(t) AS dur
    FROM keyed GROUP BY project, day, kind, actor_id, skey
  )
  SELECT project, day, kind, COUNT(*) AS sessions,
         SUM(CASE WHEN view_count = 1 THEN 1 ELSE 0 END) AS bounces,
         COALESCE(SUM(dur), 0) AS duration_sec
  FROM spans GROUP BY project, day, kind
) p ON p.project = c.project AND p.day = c.day AND p.kind = c.kind;

CREATE VIEW v_views_paths AS
SELECT project, day, path, visitors, views FROM agg_views_paths
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.path, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, path,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, path) AS rn
    FROM views GROUP BY project, substr(ts,1,10), path
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.path = v.path
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END;

CREATE VIEW v_views_hosts AS
SELECT project, day, host, visitors, views FROM agg_views_hosts
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.host, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, host,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, host) AS rn
    FROM views GROUP BY project, substr(ts,1,10), host
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.host = v.host
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END;

CREATE VIEW v_views_referrers AS
SELECT project, day, source, visitors, views FROM agg_views_referrers
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.referrer_source AS source, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, referrer_source AS source,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, referrer_source) AS rn
    FROM views GROUP BY project, substr(ts,1,10), referrer_source
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.source = v.referrer_source
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END;

CREATE VIEW v_views_countries AS
SELECT project, day, country, visitors, views FROM agg_views_countries
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.country, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, country,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, country) AS rn
    FROM views GROUP BY project, substr(ts,1,10), country
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.country = v.country
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END;

CREATE VIEW v_views_displays AS
SELECT project, day, display, visitors, views FROM agg_views_displays
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.display_width || 'x' || v.display_height AS display, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, display_width || 'x' || display_height AS display,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, display_width || 'x' || display_height) AS rn
    FROM views WHERE display_width > 0 AND display_height > 0
    GROUP BY project, substr(ts,1,10), display_width || 'x' || display_height
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.display = v.display_width || 'x' || v.display_height
  WHERE v.display_width > 0 AND v.display_height > 0
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END;

CREATE VIEW v_views_os AS
SELECT project, day, os, os_version, visitors, views FROM agg_views_os
UNION ALL
SELECT project, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.os, v.os_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, os, os_version,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, os, os_version) AS rn
    FROM views GROUP BY project, substr(ts,1,10), os, os_version
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.os = v.os AND r.os_version = v.os_version
)
GROUP BY project, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END;

CREATE VIEW v_views_browsers AS
SELECT project, day, browser, browser_version, visitors, views FROM agg_views_browsers
UNION ALL
SELECT project, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.browser, v.browser_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, browser, browser_version,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, browser, browser_version) AS rn
    FROM views GROUP BY project, substr(ts,1,10), browser, browser_version
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.browser = v.browser AND r.browser_version = v.browser_version
)
GROUP BY project, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END;

CREATE VIEW v_views_app_versions AS
SELECT project, day, os, app_version, visitors, views FROM agg_views_app_versions
UNION ALL
SELECT project, day, os, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.os, v.app_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, os, app_version,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, os, app_version) AS rn
    FROM views WHERE app_version <> '' GROUP BY project, substr(ts,1,10), os, app_version
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.os = v.os AND r.app_version = v.app_version
  WHERE v.app_version <> ''
)
GROUP BY project, day, os, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END;

CREATE VIEW v_views_devices AS
SELECT project, day, device, device_model, visitors, views FROM agg_views_devices
UNION ALL
SELECT project, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.device, v.device_model, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, device, device_model,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, device, device_model) AS rn
    FROM views GROUP BY project, substr(ts,1,10), device, device_model
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.device = v.device AND r.device_model = v.device_model
)
GROUP BY project, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END;

CREATE VIEW v_views_utm AS
SELECT project, day, utm_source, utm_medium, utm_campaign, visitors, views FROM agg_views_utm
UNION ALL
SELECT project, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.utm_source, v.utm_medium, v.utm_campaign, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, utm_source, utm_medium, utm_campaign,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, utm_source, utm_medium, utm_campaign) AS rn
    FROM views WHERE NOT (utm_source='' AND utm_medium='' AND utm_campaign='')
    GROUP BY project, substr(ts,1,10), utm_source, utm_medium, utm_campaign
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10)
     AND r.utm_source = v.utm_source AND r.utm_medium = v.utm_medium AND r.utm_campaign = v.utm_campaign
  WHERE NOT (v.utm_source='' AND v.utm_medium='' AND v.utm_campaign='')
)
GROUP BY project, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END;

CREATE VIEW v_product_daily AS
SELECT project, day, event_name, count, unique_users FROM agg_product_daily
UNION ALL
SELECT project, substr(ts,1,10), event_name, COUNT(*), COUNT(DISTINCT actor_id)
FROM product_events GROUP BY project, substr(ts,1,10), event_name;

CREATE VIEW v_product_totals AS
SELECT project, day, total_events, active_users FROM agg_product_totals
UNION ALL
SELECT project, substr(ts,1,10), COUNT(*), COUNT(DISTINCT actor_id)
FROM product_events GROUP BY project, substr(ts,1,10);

CREATE VIEW v_product_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta
                   WHERE key='product_attributes_top_n'
                     AND CAST(value AS INTEGER) > 0), 50) AS n
),
declared AS (
  SELECT DISTINCT p.alias AS project, j.value AS attr_key
  FROM projects p,
       json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
  WHERE j.type = 'text'
),
vals AS (
  SELECT pe.project AS project, substr(pe.ts,1,10) AS day,
         pe.event_name AS event_name, d.attr_key AS attr_key,
         json_extract(pe.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') AS attr_value,
         pe.actor_id AS actor_id
  FROM product_events pe
  JOIN declared d ON d.project = pe.project
  WHERE json_extract(pe.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') IS NOT NULL
  UNION ALL
  SELECT project, substr(ts,1,10), event_name, '$os', os, actor_id
  FROM product_events WHERE os <> ''
  UNION ALL
  SELECT project, substr(ts,1,10), event_name, '$app_version', app_version, actor_id
  FROM product_events WHERE app_version <> ''
),
counted AS (
  SELECT project, day, event_name, attr_key, attr_value,
         COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u
  FROM vals
  GROUP BY project, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT project, day, event_name, attr_key, attr_value, c, u,
         ROW_NUMBER() OVER (PARTITION BY project, day, event_name, attr_key
                            ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT project, day, event_name, attr_key, attr_value, count, unique_users
FROM agg_product_attrs
UNION ALL
SELECT project, day, event_name, attr_key, attr_value, c, u
FROM ranked
WHERE rn <= (SELECT n FROM cap)
UNION ALL
SELECT v.project, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id)
FROM vals v
WHERE NOT EXISTS (
  SELECT 1 FROM ranked r
  WHERE r.project = v.project AND r.day = v.day
    AND r.event_name = v.event_name AND r.attr_key = v.attr_key
    AND r.attr_value = v.attr_value
    AND r.rn <= (SELECT n FROM cap))
GROUP BY v.project, v.day, v.event_name, v.attr_key;

CREATE VIEW v_identity_daily AS
SELECT project, day, kind, id, actors, users, views, events
FROM agg_identity_daily
UNION ALL
SELECT project, day, kind, id,
       COUNT(DISTINCT actor_id),
       CASE WHEN kind = 'user' THEN 1 ELSE COUNT(DISTINCT NULLIF(user_id, '')) END,
       SUM(is_view), SUM(is_event)
FROM (
  SELECT project, substr(ts,1,10) AS day, 'user' AS kind, user_id AS id,
         actor_id, user_id, 1 AS is_view, 0 AS is_event
  FROM views WHERE user_id <> ''
  UNION ALL
  SELECT project, substr(ts,1,10), 'user', user_id, actor_id, user_id, 0, 1
  FROM product_events WHERE user_id <> ''
  UNION ALL
  SELECT project, substr(ts,1,10), 'group', group_id, actor_id, user_id, 1, 0
  FROM views WHERE group_id <> ''
  UNION ALL
  SELECT project, substr(ts,1,10), 'group', group_id, actor_id, user_id, 0, 1
  FROM product_events WHERE group_id <> ''
)
GROUP BY project, day, kind, id;

-- Retention is defined only over aggregated days, so there is no live half.
CREATE VIEW v_retention AS
SELECT r.project, r.actor_kind, r.cohort_day, r.day_offset, r.actors,
       c.actors AS cohort_size
FROM agg_retention r
JOIN agg_retention c
  ON c.project = r.project AND c.actor_kind = r.actor_kind
 AND c.cohort_day = r.cohort_day AND c.day_offset = 0;
