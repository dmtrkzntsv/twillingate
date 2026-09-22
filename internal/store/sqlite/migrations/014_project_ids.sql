-- Projects are identified by an integer id, not an alias
-- (docs/superpowers/specs/2026-09-19-project-ids-design.md).
--
-- Every table keyed by the alias is rebuilt with `project_id INTEGER` in
-- the same position: SQLite cannot drop or retype a column that leads a
-- composite key, so each is CREATE _new / INSERT SELECT / DROP / RENAME,
-- the shape 012 used for actors. The raw product table takes its new name,
-- `events`, in the same rebuild. Views are dropped first (a rename with a
-- view still pointing at the old table fails) and recreated last.
--
-- Every copy LEFT JOINs the id map. A row whose project has no registry
-- row yields a NULL project_id, which fails NOT NULL and aborts the whole
-- migration -- an inner join would drop that row silently. An ingest key
-- label repeated within one project trips UNIQUE (project_id, label) the
-- same way. docs/deployment.md lists the pre-upgrade checks that find
-- both before the service is stopped.
--
-- Irreversible. Take a backup first.

-- ===== 1. views off =====
DROP VIEW IF EXISTS v_views_daily;
DROP VIEW IF EXISTS v_views_paths;
DROP VIEW IF EXISTS v_views_hosts;
DROP VIEW IF EXISTS v_views_referrers;
DROP VIEW IF EXISTS v_views_countries;
DROP VIEW IF EXISTS v_views_displays;
DROP VIEW IF EXISTS v_views_os;
DROP VIEW IF EXISTS v_views_browsers;
DROP VIEW IF EXISTS v_views_app_versions;
DROP VIEW IF EXISTS v_views_devices;
DROP VIEW IF EXISTS v_views_utm;
DROP VIEW IF EXISTS v_product_daily;
DROP VIEW IF EXISTS v_product_totals;
DROP VIEW IF EXISTS v_product_attrs;
DROP VIEW IF EXISTS v_retention;
DROP VIEW IF EXISTS v_identity_daily;
DROP VIEW IF EXISTS v_events_flat;

-- ===== 2. the id map =====
-- Ids follow creation order, alias breaking ties, starting at 1.
CREATE TEMP TABLE project_map (alias TEXT PRIMARY KEY, id INTEGER NOT NULL);
INSERT INTO project_map (alias, id)
SELECT alias, ROW_NUMBER() OVER (ORDER BY created_at, alias) FROM projects;

-- ===== 3. projects =====
-- AUTOINCREMENT so a deleted project's id is never reissued: stale
-- references (audit rows, bookmarks, agent memory) must not silently land
-- on a new project. Explicit ids advance sqlite_sequence, so the next
-- project gets max + 1. alias and retention are not carried over.
CREATE TABLE projects_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    identity        TEXT NOT NULL DEFAULT 'anonymous',
    allowed_origins TEXT NOT NULL DEFAULT '[]',
    attributes      TEXT NOT NULL DEFAULT '[]',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    archived_at     TEXT                                -- NULL = active
);
INSERT INTO projects_new (id, name, identity, allowed_origins, attributes, created_at, archived_at)
SELECT m.id, p.name, p.identity, p.allowed_origins, p.attributes, p.created_at, p.archived_at
FROM projects p JOIN project_map m ON m.alias = p.alias;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;

-- ===== 4. ingest_keys =====
-- UNIQUE (project_id, label) replaces the count-then-insert check in Go and
-- the idx_ingest_keys_project index, which the DROP takes with it.
CREATE TABLE ingest_keys_new (
    key         TEXT PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id),
    label       TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    disabled_at TEXT,                                   -- NULL = active
    UNIQUE (project_id, label)
);
INSERT INTO ingest_keys_new (key, project_id, label, created_at, disabled_at)
SELECT k.key, m.id, k.label, k.created_at, k.disabled_at
FROM ingest_keys k LEFT JOIN project_map m ON m.alias = k.project;
DROP TABLE ingest_keys;
ALTER TABLE ingest_keys_new RENAME TO ingest_keys;

-- ===== 5. raw tables =====
CREATE TABLE views_new (
    id              TEXT PRIMARY KEY,
    project_id      INTEGER NOT NULL,
    ts              TEXT NOT NULL,
    -- See 012 for why day is a stored generated column.
    day             TEXT GENERATED ALWAYS AS (substr(ts,1,10)) STORED,
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
INSERT INTO views_new (id, project_id, ts, received_at, kind, actor_id, actor_kind, user_id, group_id,
    session_id, host, path, referrer_source, utm_source, utm_medium, utm_campaign, os, os_version,
    browser, browser_version, app_version, device, device_model, locale, display_width, display_height, country)
SELECT v.id, m.id, v.ts, v.received_at, v.kind, v.actor_id, v.actor_kind, v.user_id, v.group_id,
    v.session_id, v.host, v.path, v.referrer_source, v.utm_source, v.utm_medium, v.utm_campaign, v.os, v.os_version,
    v.browser, v.browser_version, v.app_version, v.device, v.device_model, v.locale, v.display_width, v.display_height, v.country
FROM views v LEFT JOIN project_map m ON m.alias = v.project;
DROP TABLE views;
ALTER TABLE views_new RENAME TO views;
CREATE INDEX idx_views_project_ts  ON views(project_id, ts);
CREATE INDEX idx_views_actor       ON views(project_id, actor_id, ts);
CREATE INDEX idx_views_session     ON views(project_id, session_id, ts);
CREATE INDEX idx_views_project_day ON views(project_id, day);

-- product_events is rebuilt under its new name. The product *family*
-- (agg_product_*, v_product_*, the product_events tool) keeps its prefix;
-- only the raw table is renamed, and its indexes already carried the name.
CREATE TABLE events (
    id          TEXT PRIMARY KEY,                       -- UUIDv7
    project_id  INTEGER NOT NULL,
    event_name  TEXT NOT NULL,
    actor_id    TEXT NOT NULL,
    ts          TEXT NOT NULL,
    attributes  TEXT NOT NULL DEFAULT '{}',             -- JSON
    user_id     TEXT NOT NULL DEFAULT '',
    group_id    TEXT NOT NULL DEFAULT '',
    os          TEXT NOT NULL DEFAULT '',
    app_version TEXT NOT NULL DEFAULT '',
    received_at TEXT NOT NULL DEFAULT '',
    actor_kind  TEXT NOT NULL DEFAULT ''
);
INSERT INTO events (id, project_id, event_name, actor_id, ts, attributes, user_id, group_id, os, app_version, received_at, actor_kind)
SELECT e.id, m.id, e.event_name, e.actor_id, e.ts, e.attributes, e.user_id, e.group_id, e.os, e.app_version, e.received_at, e.actor_kind
FROM product_events e LEFT JOIN project_map m ON m.alias = e.project;
DROP TABLE product_events;
CREATE INDEX idx_events_project_name_ts ON events(project_id, event_name, ts);
CREATE INDEX idx_events_project_user_ts ON events(project_id, actor_id, ts);

-- ===== 6. views aggregates =====
CREATE TABLE agg_views_daily_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, kind TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    sessions INTEGER NOT NULL, bounces INTEGER NOT NULL, duration_sec INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, kind)
) WITHOUT ROWID;
INSERT INTO agg_views_daily_new SELECT m.id, a.day, a.kind, a.visitors, a.views, a.sessions, a.bounces, a.duration_sec
FROM agg_views_daily a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_daily;
ALTER TABLE agg_views_daily_new RENAME TO agg_views_daily;

CREATE TABLE agg_views_paths_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, path TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, path)
) WITHOUT ROWID;
INSERT INTO agg_views_paths_new SELECT m.id, a.day, a.path, a.visitors, a.views
FROM agg_views_paths a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_paths;
ALTER TABLE agg_views_paths_new RENAME TO agg_views_paths;

CREATE TABLE agg_views_hosts_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, host TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, host)
) WITHOUT ROWID;
INSERT INTO agg_views_hosts_new SELECT m.id, a.day, a.host, a.visitors, a.views
FROM agg_views_hosts a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_hosts;
ALTER TABLE agg_views_hosts_new RENAME TO agg_views_hosts;

CREATE TABLE agg_views_referrers_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, source TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, source)
) WITHOUT ROWID;
INSERT INTO agg_views_referrers_new SELECT m.id, a.day, a.source, a.visitors, a.views
FROM agg_views_referrers a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_referrers;
ALTER TABLE agg_views_referrers_new RENAME TO agg_views_referrers;

CREATE TABLE agg_views_utm_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    utm_source TEXT NOT NULL, utm_medium TEXT NOT NULL, utm_campaign TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, utm_source, utm_medium, utm_campaign)
) WITHOUT ROWID;
INSERT INTO agg_views_utm_new SELECT m.id, a.day, a.utm_source, a.utm_medium, a.utm_campaign, a.visitors, a.views
FROM agg_views_utm a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_utm;
ALTER TABLE agg_views_utm_new RENAME TO agg_views_utm;

CREATE TABLE agg_views_countries_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, country TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, country)
) WITHOUT ROWID;
INSERT INTO agg_views_countries_new SELECT m.id, a.day, a.country, a.visitors, a.views
FROM agg_views_countries a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_countries;
ALTER TABLE agg_views_countries_new RENAME TO agg_views_countries;

CREATE TABLE agg_views_os_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, os TEXT NOT NULL, os_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, os, os_version)
) WITHOUT ROWID;
INSERT INTO agg_views_os_new SELECT m.id, a.day, a.os, a.os_version, a.visitors, a.views
FROM agg_views_os a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_os;
ALTER TABLE agg_views_os_new RENAME TO agg_views_os;

CREATE TABLE agg_views_browsers_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, browser TEXT NOT NULL, browser_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, browser, browser_version)
) WITHOUT ROWID;
INSERT INTO agg_views_browsers_new SELECT m.id, a.day, a.browser, a.browser_version, a.visitors, a.views
FROM agg_views_browsers a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_browsers;
ALTER TABLE agg_views_browsers_new RENAME TO agg_views_browsers;

CREATE TABLE agg_views_app_versions_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, os TEXT NOT NULL, app_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, os, app_version)
) WITHOUT ROWID;
INSERT INTO agg_views_app_versions_new SELECT m.id, a.day, a.os, a.app_version, a.visitors, a.views
FROM agg_views_app_versions a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_app_versions;
ALTER TABLE agg_views_app_versions_new RENAME TO agg_views_app_versions;

CREATE TABLE agg_views_devices_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, device TEXT NOT NULL, device_model TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, device, device_model)
) WITHOUT ROWID;
INSERT INTO agg_views_devices_new SELECT m.id, a.day, a.device, a.device_model, a.visitors, a.views
FROM agg_views_devices a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_devices;
ALTER TABLE agg_views_devices_new RENAME TO agg_views_devices;

CREATE TABLE agg_views_displays_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, display TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, display)
) WITHOUT ROWID;
INSERT INTO agg_views_displays_new SELECT m.id, a.day, a.display, a.visitors, a.views
FROM agg_views_displays a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_displays;
ALTER TABLE agg_views_displays_new RENAME TO agg_views_displays;

-- ===== 7. product aggregates =====
CREATE TABLE agg_product_daily_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, event_name TEXT NOT NULL,
    count INTEGER NOT NULL, unique_users INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, event_name)
) WITHOUT ROWID;
INSERT INTO agg_product_daily_new SELECT m.id, a.day, a.event_name, a.count, a.unique_users
FROM agg_product_daily a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_product_daily;
ALTER TABLE agg_product_daily_new RENAME TO agg_product_daily;

CREATE TABLE agg_product_totals_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    total_events INTEGER NOT NULL, active_users INTEGER NOT NULL,
    PRIMARY KEY (project_id, day)
) WITHOUT ROWID;
INSERT INTO agg_product_totals_new SELECT m.id, a.day, a.total_events, a.active_users
FROM agg_product_totals a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_product_totals;
ALTER TABLE agg_product_totals_new RENAME TO agg_product_totals;

CREATE TABLE agg_product_attrs_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, event_name TEXT NOT NULL,
    attr_key TEXT NOT NULL, attr_value TEXT NOT NULL,
    count INTEGER NOT NULL, unique_users INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, event_name, attr_key, attr_value)
) WITHOUT ROWID;
INSERT INTO agg_product_attrs_new SELECT m.id, a.day, a.event_name, a.attr_key, a.attr_value, a.count, a.unique_users
FROM agg_product_attrs a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_product_attrs;
ALTER TABLE agg_product_attrs_new RENAME TO agg_product_attrs;

-- ===== 8. identity and retention =====
CREATE TABLE agg_identity_daily_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    kind TEXT NOT NULL, id TEXT NOT NULL,
    actors INTEGER NOT NULL, users INTEGER NOT NULL,
    views INTEGER NOT NULL, events INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, kind, id)
) WITHOUT ROWID;
INSERT INTO agg_identity_daily_new SELECT m.id, a.day, a.kind, a.id, a.actors, a.users, a.views, a.events
FROM agg_identity_daily a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_identity_daily;
ALTER TABLE agg_identity_daily_new RENAME TO agg_identity_daily;

CREATE TABLE agg_retention_new (
    project_id INTEGER NOT NULL, actor_kind TEXT NOT NULL,
    cohort_day TEXT NOT NULL, day_offset INTEGER NOT NULL,
    actors INTEGER NOT NULL,
    PRIMARY KEY (project_id, actor_kind, cohort_day, day_offset)
) WITHOUT ROWID;
INSERT INTO agg_retention_new SELECT m.id, a.actor_kind, a.cohort_day, a.day_offset, a.actors
FROM agg_retention a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_retention;
ALTER TABLE agg_retention_new RENAME TO agg_retention;

CREATE TABLE actors_new (
    project_id INTEGER NOT NULL, actor_id TEXT NOT NULL,
    actor_kind TEXT NOT NULL,
    first_seen_day TEXT NOT NULL,
    last_seen_day  TEXT NOT NULL,
    PRIMARY KEY (project_id, actor_id)
) WITHOUT ROWID;
INSERT INTO actors_new SELECT m.id, a.actor_id, a.actor_kind, a.first_seen_day, a.last_seen_day
FROM actors a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE actors;
ALTER TABLE actors_new RENAME TO actors;
CREATE INDEX idx_actors_last_seen ON actors(project_id, last_seen_day);

CREATE TABLE identities_new (
    project_id    INTEGER NOT NULL,
    kind          TEXT NOT NULL,
    id            TEXT NOT NULL,
    name          TEXT NOT NULL,
    last_seen_day TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (project_id, kind, id)
) WITHOUT ROWID;
INSERT INTO identities_new SELECT m.id, a.kind, a.id, a.name, a.last_seen_day, a.updated_at
FROM identities a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE identities;
ALTER TABLE identities_new RENAME TO identities;

DROP TABLE project_map;

-- ===== 9. views back, on project_id =====
-- Identical to 012 and 013 apart from the key column, the raw product
-- table's name, and v_product_attrs joining projects on id. v_events_flat
-- is not recreated here: the boot-time RebuildFlatView builds it.
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
    FROM views v
    JOIN (
      SELECT project_id, day, kind,
             ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, kind) AS rn
      FROM views GROUP BY project_id, day, kind
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
    FROM views v
    JOIN (
      SELECT project_id, day, kind,
             ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, kind) AS rn
      FROM views GROUP BY project_id, day, kind
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

CREATE VIEW v_views_paths AS
SELECT project_id, day, path, visitors, views FROM agg_views_paths
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.path, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, path,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, path) AS rn
    FROM views GROUP BY project_id, day, path
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.path = v.path
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END;

CREATE VIEW v_views_hosts AS
SELECT project_id, day, host, visitors, views FROM agg_views_hosts
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.host, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, host,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, host) AS rn
    FROM views GROUP BY project_id, day, host
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.host = v.host
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END;

CREATE VIEW v_views_referrers AS
SELECT project_id, day, source, visitors, views FROM agg_views_referrers
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.referrer_source AS source, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, referrer_source AS source,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, referrer_source) AS rn
    FROM views GROUP BY project_id, day, referrer_source
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.source = v.referrer_source
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END;

CREATE VIEW v_views_countries AS
SELECT project_id, day, country, visitors, views FROM agg_views_countries
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.country, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, country,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, country) AS rn
    FROM views GROUP BY project_id, day, country
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.country = v.country
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END;

CREATE VIEW v_views_displays AS
SELECT project_id, day, display, visitors, views FROM agg_views_displays
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.display_width || 'x' || v.display_height AS display, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, display_width || 'x' || display_height AS display,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, display_width || 'x' || display_height) AS rn
    FROM views WHERE display_width > 0 AND display_height > 0
    GROUP BY project_id, day, display_width || 'x' || display_height
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.display = v.display_width || 'x' || v.display_height
  WHERE v.display_width > 0 AND v.display_height > 0
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END;

CREATE VIEW v_views_os AS
SELECT project_id, day, os, os_version, visitors, views FROM agg_views_os
UNION ALL
SELECT project_id, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.os, v.os_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, os, os_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, os, os_version) AS rn
    FROM views GROUP BY project_id, day, os, os_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.os = v.os AND r.os_version = v.os_version
)
GROUP BY project_id, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END;

CREATE VIEW v_views_browsers AS
SELECT project_id, day, browser, browser_version, visitors, views FROM agg_views_browsers
UNION ALL
SELECT project_id, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.browser, v.browser_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, browser, browser_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, browser, browser_version) AS rn
    FROM views GROUP BY project_id, day, browser, browser_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.browser = v.browser AND r.browser_version = v.browser_version
)
GROUP BY project_id, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END;

CREATE VIEW v_views_app_versions AS
SELECT project_id, day, os, app_version, visitors, views FROM agg_views_app_versions
UNION ALL
SELECT project_id, day, os, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.os, v.app_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, os, app_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, os, app_version) AS rn
    FROM views WHERE app_version <> '' GROUP BY project_id, day, os, app_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.os = v.os AND r.app_version = v.app_version
  WHERE v.app_version <> ''
)
GROUP BY project_id, day, os, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END;

CREATE VIEW v_views_devices AS
SELECT project_id, day, device, device_model, visitors, views FROM agg_views_devices
UNION ALL
SELECT project_id, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.device, v.device_model, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, device, device_model,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, device, device_model) AS rn
    FROM views GROUP BY project_id, day, device, device_model
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.device = v.device AND r.device_model = v.device_model
)
GROUP BY project_id, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END;

CREATE VIEW v_views_utm AS
SELECT project_id, day, utm_source, utm_medium, utm_campaign, visitors, views FROM agg_views_utm
UNION ALL
SELECT project_id, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.utm_source, v.utm_medium, v.utm_campaign, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, utm_source, utm_medium, utm_campaign,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, utm_source, utm_medium, utm_campaign) AS rn
    FROM views WHERE NOT (utm_source='' AND utm_medium='' AND utm_campaign='')
    GROUP BY project_id, day, utm_source, utm_medium, utm_campaign
  ) r ON r.project_id = v.project_id AND r.day = v.day
     AND r.utm_source = v.utm_source AND r.utm_medium = v.utm_medium AND r.utm_campaign = v.utm_campaign
  WHERE NOT (v.utm_source='' AND v.utm_medium='' AND v.utm_campaign='')
)
GROUP BY project_id, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END;

CREATE VIEW v_product_daily AS
SELECT project_id, day, event_name, count, unique_users FROM agg_product_daily
UNION ALL
SELECT project_id, substr(ts,1,10), event_name, COUNT(*), COUNT(DISTINCT actor_id)
FROM events GROUP BY project_id, substr(ts,1,10), event_name;

CREATE VIEW v_product_totals AS
SELECT project_id, day, total_events, active_users FROM agg_product_totals
UNION ALL
SELECT project_id, substr(ts,1,10), COUNT(*), COUNT(DISTINCT actor_id)
FROM events GROUP BY project_id, substr(ts,1,10);

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

CREATE VIEW v_retention AS
SELECT r.project_id, r.actor_kind, r.cohort_day, r.day_offset, r.actors,
       c.actors AS cohort_size
FROM agg_retention r
JOIN agg_retention c
  ON c.project_id = r.project_id AND c.actor_kind = r.actor_kind
 AND c.cohort_day = r.cohort_day AND c.day_offset = 0;

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
    FROM views WHERE user_id <> ''
    UNION ALL
    SELECT project_id, substr(ts,1,10), 'user', user_id, actor_id, user_id, 0, 1
    FROM events WHERE user_id <> ''
    UNION ALL
    SELECT project_id, day, 'group', group_id, actor_id, user_id, 1, 0
    FROM views WHERE group_id <> ''
    UNION ALL
    SELECT project_id, substr(ts,1,10), 'group', group_id, actor_id, user_id, 0, 1
    FROM events WHERE group_id <> ''
  )
  GROUP BY project_id, day, kind, id
) live
WHERE rn <= 500
  AND NOT EXISTS (
    SELECT 1 FROM agg_identity_daily g
    WHERE g.project_id = live.project_id AND g.day = live.day);
