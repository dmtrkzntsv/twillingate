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
