-- 041: store the raw events table encoded (spec
-- docs/superpowers/specs/2026-10-11-events-encoding-design.md, D1-D10,
-- D17). Most of a raw row was repeated text: attribute keys and values
-- written in full on every row, 36-character UUIDs, ISO timestamps, and a
-- dozen short strings (host, path, browser...). Each becomes an id:
--
--   - family is 1 (views), 2 (product), 3 (measures);
--   - id is the UUID's 16 bytes; actor_id, user_id, group_id and
--     session_id are x'01' + 16 bytes for a lowercase canonical UUID,
--     x'02' + the bytes for non-empty even-length lowercase hex, and stay
--     text otherwise, so each decodes to exactly the text sent (D3);
--   - ts and received_at are Unix seconds, '' received_at being 0 (D4);
--   - every text column is an id into strings, '' being 0, which has no
--     row (D2); day stays text so a range filter keeps using the key (D5);
--   - attributes is {"<project_attributes.id>": <attribute_values.n>} (D6).
--
-- raw_views, raw_product and raw_measures decode every column back to
-- today's names and text (D10), so the v_* views read them unchanged and
-- are recreated below byte-for-byte from the schema 040 left, except
-- v_identity_daily, the one view that read events itself. The expressions
-- below are ingest's encoding (encode.go) written in SQL;
-- TestMigrationEncodesLikeIngest requires equal bytes from both.
--
-- The Go pre-step (prepareEncoding, migration041.go) has already refused
-- attributes that are not an object of strings, dropped raw rows of
-- projects that are gone, and added the catalog row of every key a raw row
-- carries. The process-owned flat view was dropped by the migrator, and
-- the process rebuilds it on the decoded columns. Irreversible, like 012
-- and 020.

-- Every view reading events or a raw_* view goes first: the rebuild drops
-- the table they name. v_retention reads agg_retention only and stays.
DROP VIEW v_identity_daily;
DROP VIEW v_measures_attrs;
DROP VIEW v_measures_daily;
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
DROP VIEW raw_views;
DROP VIEW raw_product;
DROP VIEW raw_measures;

-- The catalog becomes the key dictionary (D8): an id per key, the newest
-- day a raw row carried it (D7) and the counter that numbers its values.
-- Most-used keys get the lowest ids, so the common ones take one byte.
CREATE TABLE project_attributes_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id),
    key           TEXT    NOT NULL,
    breakdown     INTEGER NOT NULL DEFAULT 0 CHECK (breakdown IN (0, 1)),
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    last_seen_day TEXT    NOT NULL DEFAULT '',
    values_next   INTEGER NOT NULL DEFAULT 1,
    UNIQUE (project_id, key)
);
INSERT INTO project_attributes_new (project_id, key, breakdown, created_at, last_seen_day)
  SELECT a.project_id, a.key, a.breakdown, a.created_at,
         COALESCE(u.last_day, '')
  FROM project_attributes a
  LEFT JOIN (SELECT e.project_id, j.key, MAX(e.day) AS last_day, COUNT(*) AS n
             FROM events e, json_each(e.attributes) j GROUP BY e.project_id, j.key) u
    ON u.project_id = a.project_id AND u.key = a.key
  ORDER BY COALESCE(u.n, 0) DESC, a.project_id, a.key;
DROP TABLE project_attributes;
ALTER TABLE project_attributes_new RENAME TO project_attributes;

-- Each key's values, numbered within the key, most frequent first (D6).
-- n is never reused within a key: the counter on the catalog row only
-- grows, and ingest numbers a new value from it.
CREATE TABLE attribute_values (
    key_id        INTEGER NOT NULL REFERENCES project_attributes(id) ON DELETE CASCADE,
    n             INTEGER NOT NULL,
    value         TEXT    NOT NULL,
    last_seen_day TEXT    NOT NULL,
    PRIMARY KEY (key_id, n),
    UNIQUE (key_id, value)
) WITHOUT ROWID;
CREATE TRIGGER attribute_values_next AFTER INSERT ON attribute_values BEGIN
  UPDATE project_attributes SET values_next = NEW.n + 1 WHERE id = NEW.key_id AND values_next <= NEW.n;
END;
INSERT INTO attribute_values (key_id, n, value, last_seen_day)
  SELECT k.id, ROW_NUMBER() OVER (PARTITION BY k.id ORDER BY COUNT(*) DESC, j.value), j.value, MAX(e.day)
  FROM events e, json_each(e.attributes) j
  JOIN project_attributes k ON k.project_id = e.project_id AND k.key = j.key
  GROUP BY k.id, j.value;

-- One dictionary for every text column of every project (D2), most
-- frequent first. '' is id 0 and has no row. AUTOINCREMENT: an id is never
-- reused, so a cleaned-up text seen again gets a new one (D7).
CREATE TABLE strings (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    value         TEXT NOT NULL UNIQUE,
    last_seen_day TEXT NOT NULL
);
INSERT INTO strings (value, last_seen_day)
  SELECT v, MAX(day) FROM (
    SELECT event_name AS v, day FROM events
    UNION ALL SELECT measure, day FROM events
    UNION ALL SELECT kind, day FROM events
    UNION ALL SELECT actor_kind, day FROM events
    UNION ALL SELECT host, day FROM events
    UNION ALL SELECT path, day FROM events
    UNION ALL SELECT referrer_source, day FROM events
    UNION ALL SELECT utm_source, day FROM events
    UNION ALL SELECT utm_medium, day FROM events
    UNION ALL SELECT utm_campaign, day FROM events
    UNION ALL SELECT platform, day FROM events
    UNION ALL SELECT os, day FROM events
    UNION ALL SELECT os_version, day FROM events
    UNION ALL SELECT os_name, day FROM events
    UNION ALL SELECT browser, day FROM events
    UNION ALL SELECT browser_version, day FROM events
    UNION ALL SELECT browser_locale, day FROM events
    UNION ALL SELECT app_version, day FROM events
    UNION ALL SELECT app_locale, day FROM events
    UNION ALL SELECT device, day FROM events
    UNION ALL SELECT device_model, day FROM events
    UNION ALL SELECT country, day FROM events
  ) WHERE v <> '' GROUP BY v ORDER BY COUNT(*) DESC, v;

-- events in the encoded shape (D1), columns in today's order and the key
-- unchanged. The id columns are declared BLOB (no affinity), so a value
-- that stays text is stored as text. bucket is regenerated as in 024.
CREATE TABLE events_new (
    family          INTEGER NOT NULL,
    project_id      INTEGER NOT NULL,
    day             TEXT    NOT NULL,
    id              BLOB    NOT NULL,
    event_name      INTEGER NOT NULL DEFAULT 0,
    ts              INTEGER NOT NULL,
    received_at     INTEGER NOT NULL DEFAULT 0,
    kind            INTEGER NOT NULL DEFAULT 0,
    actor_id        BLOB    NOT NULL,
    actor_kind      INTEGER NOT NULL DEFAULT 0,
    user_id         BLOB    NOT NULL DEFAULT '',
    group_id        BLOB    NOT NULL DEFAULT '',
    session_id      BLOB    NOT NULL DEFAULT '',
    host            INTEGER NOT NULL DEFAULT 0,
    path            INTEGER NOT NULL DEFAULT 0,
    referrer_source INTEGER NOT NULL DEFAULT 0,
    utm_source      INTEGER NOT NULL DEFAULT 0,
    utm_medium      INTEGER NOT NULL DEFAULT 0,
    utm_campaign    INTEGER NOT NULL DEFAULT 0,
    platform        INTEGER NOT NULL DEFAULT 0,
    os              INTEGER NOT NULL DEFAULT 0,
    os_version      INTEGER NOT NULL DEFAULT 0,
    os_name         INTEGER NOT NULL DEFAULT 0,
    browser         INTEGER NOT NULL DEFAULT 0,
    browser_version INTEGER NOT NULL DEFAULT 0,
    browser_locale  INTEGER NOT NULL DEFAULT 0,
    app_version     INTEGER NOT NULL DEFAULT 0,
    app_locale      INTEGER NOT NULL DEFAULT 0,
    device          INTEGER NOT NULL DEFAULT 0,
    device_model    INTEGER NOT NULL DEFAULT 0,
    display_width   INTEGER NOT NULL DEFAULT 0,
    display_height  INTEGER NOT NULL DEFAULT 0,
    country         INTEGER NOT NULL DEFAULT 0,
    consent         INTEGER,
    attributes      TEXT    NOT NULL DEFAULT '{}',
    value           REAL,
    measure         INTEGER NOT NULL DEFAULT 0,
    sample_rate     REAL    NOT NULL DEFAULT 1,
    bucket          INTEGER GENERATED ALWAYS AS
  (CASE WHEN value IS NULL THEN NULL
        WHEN value > 1e-17 THEN CAST(ceil(log(value) / log(1.04)) AS INTEGER)
        ELSE -1000 END) VIRTUAL,
    PRIMARY KEY (family, project_id, day, id)
) WITHOUT ROWID;
INSERT INTO events_new (family, project_id, day, id, event_name, ts, received_at, kind, actor_id, actor_kind, user_id, group_id, session_id, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, attributes, value, measure, sample_rate)
SELECT CASE e.family WHEN 'views' THEN 1 WHEN 'product' THEN 2 WHEN 'measures' THEN 3 END,
       e.project_id,
       e.day,
       CASE WHEN length(e.id) = 36 AND substr(e.id, 9, 1) = '-' AND substr(e.id, 14, 1) = '-'
               AND substr(e.id, 19, 1) = '-' AND substr(e.id, 24, 1) = '-'
               AND length(replace(e.id, '-', '')) = 32 AND replace(e.id, '-', '') NOT GLOB '*[^0-9a-f]*'
          THEN unhex(replace(e.id, '-', ''))
          ELSE e.id END,
       COALESCE((SELECT id FROM strings WHERE value = e.event_name), 0),
       unixepoch(e.ts),
       CASE WHEN e.received_at = '' THEN 0 ELSE unixepoch(e.received_at) END,
       COALESCE((SELECT id FROM strings WHERE value = e.kind), 0),
       CASE WHEN length(e.actor_id) = 36 AND substr(e.actor_id, 9, 1) = '-' AND substr(e.actor_id, 14, 1) = '-'
               AND substr(e.actor_id, 19, 1) = '-' AND substr(e.actor_id, 24, 1) = '-'
               AND length(replace(e.actor_id, '-', '')) = 32 AND replace(e.actor_id, '-', '') NOT GLOB '*[^0-9a-f]*'
          THEN unhex('01' || replace(e.actor_id, '-', ''))
          WHEN e.actor_id <> '' AND length(e.actor_id) % 2 = 0 AND e.actor_id NOT GLOB '*[^0-9a-f]*'
          THEN unhex('02' || e.actor_id)
          ELSE e.actor_id END,
       COALESCE((SELECT id FROM strings WHERE value = e.actor_kind), 0),
       CASE WHEN length(e.user_id) = 36 AND substr(e.user_id, 9, 1) = '-' AND substr(e.user_id, 14, 1) = '-'
               AND substr(e.user_id, 19, 1) = '-' AND substr(e.user_id, 24, 1) = '-'
               AND length(replace(e.user_id, '-', '')) = 32 AND replace(e.user_id, '-', '') NOT GLOB '*[^0-9a-f]*'
          THEN unhex('01' || replace(e.user_id, '-', ''))
          WHEN e.user_id <> '' AND length(e.user_id) % 2 = 0 AND e.user_id NOT GLOB '*[^0-9a-f]*'
          THEN unhex('02' || e.user_id)
          ELSE e.user_id END,
       CASE WHEN length(e.group_id) = 36 AND substr(e.group_id, 9, 1) = '-' AND substr(e.group_id, 14, 1) = '-'
               AND substr(e.group_id, 19, 1) = '-' AND substr(e.group_id, 24, 1) = '-'
               AND length(replace(e.group_id, '-', '')) = 32 AND replace(e.group_id, '-', '') NOT GLOB '*[^0-9a-f]*'
          THEN unhex('01' || replace(e.group_id, '-', ''))
          WHEN e.group_id <> '' AND length(e.group_id) % 2 = 0 AND e.group_id NOT GLOB '*[^0-9a-f]*'
          THEN unhex('02' || e.group_id)
          ELSE e.group_id END,
       CASE WHEN length(e.session_id) = 36 AND substr(e.session_id, 9, 1) = '-' AND substr(e.session_id, 14, 1) = '-'
               AND substr(e.session_id, 19, 1) = '-' AND substr(e.session_id, 24, 1) = '-'
               AND length(replace(e.session_id, '-', '')) = 32 AND replace(e.session_id, '-', '') NOT GLOB '*[^0-9a-f]*'
          THEN unhex('01' || replace(e.session_id, '-', ''))
          WHEN e.session_id <> '' AND length(e.session_id) % 2 = 0 AND e.session_id NOT GLOB '*[^0-9a-f]*'
          THEN unhex('02' || e.session_id)
          ELSE e.session_id END,
       COALESCE((SELECT id FROM strings WHERE value = e.host), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.path), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.referrer_source), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.utm_source), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.utm_medium), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.utm_campaign), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.platform), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.os), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.os_version), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.os_name), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.browser), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.browser_version), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.browser_locale), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.app_version), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.app_locale), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.device), 0),
       COALESCE((SELECT id FROM strings WHERE value = e.device_model), 0),
       e.display_width,
       e.display_height,
       COALESCE((SELECT id FROM strings WHERE value = e.country), 0),
       e.consent,
       (SELECT COALESCE(json_group_object(CAST(k.id AS TEXT), v.n), '{}')
            FROM json_each(e.attributes) j
            JOIN project_attributes k ON k.project_id = e.project_id AND k.key = j.key
            JOIN attribute_values v ON v.key_id = k.id AND v.value = j.value),
       e.value,
       COALESCE((SELECT id FROM strings WHERE value = e.measure), 0),
       e.sample_rate
FROM events e;
DROP TABLE events;
ALTER TABLE events_new RENAME TO events;

-- The decoding views (D10): today's columns, names and text values, each
-- read through the fragments encode.go builds (sqlEventID, sqlID,
-- sqlString, sqlTS, sqlReceivedAt, sqlAttributes), pasted as text since a
-- migration is fixed at its version. A filter on project_id and day still
-- reaches the key (D11): both pass through as stored. The table keeps its
-- name, unaliased, so a query plan through these views reads "events".
CREATE VIEW raw_views AS
SELECT 'views' AS family,
       events.project_id AS project_id,
       events.day AS day,
       CASE WHEN typeof(events.id) = 'blob' THEN substr(lower(hex(events.id)), 1, 8) || '-' || substr(lower(hex(events.id)), 9, 4) || '-' || substr(lower(hex(events.id)), 13, 4) || '-' || substr(lower(hex(events.id)), 17, 4) || '-' || substr(lower(hex(events.id)), 21, 12) ELSE events.id END AS id,
       COALESCE((SELECT value FROM strings WHERE id = events.event_name), '') AS event_name,
       strftime('%Y-%m-%dT%H:%M:%SZ', events.ts, 'unixepoch') AS ts,
       CASE WHEN events.received_at = 0 THEN '' ELSE strftime('%Y-%m-%dT%H:%M:%SZ', events.received_at, 'unixepoch') END AS received_at,
       COALESCE((SELECT value FROM strings WHERE id = events.kind), '') AS kind,
       CASE WHEN typeof(events.actor_id) <> 'blob' THEN events.actor_id WHEN substr(events.actor_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.actor_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 21, 12) ELSE lower(hex(substr(events.actor_id, 2))) END AS actor_id,
       COALESCE((SELECT value FROM strings WHERE id = events.actor_kind), '') AS actor_kind,
       CASE WHEN typeof(events.user_id) <> 'blob' THEN events.user_id WHEN substr(events.user_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.user_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.user_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.user_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.user_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.user_id, 2))), 21, 12) ELSE lower(hex(substr(events.user_id, 2))) END AS user_id,
       CASE WHEN typeof(events.group_id) <> 'blob' THEN events.group_id WHEN substr(events.group_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.group_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.group_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.group_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.group_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.group_id, 2))), 21, 12) ELSE lower(hex(substr(events.group_id, 2))) END AS group_id,
       CASE WHEN typeof(events.session_id) <> 'blob' THEN events.session_id WHEN substr(events.session_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.session_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.session_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.session_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.session_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.session_id, 2))), 21, 12) ELSE lower(hex(substr(events.session_id, 2))) END AS session_id,
       COALESCE((SELECT value FROM strings WHERE id = events.host), '') AS host,
       COALESCE((SELECT value FROM strings WHERE id = events.path), '') AS path,
       COALESCE((SELECT value FROM strings WHERE id = events.referrer_source), '') AS referrer_source,
       COALESCE((SELECT value FROM strings WHERE id = events.utm_source), '') AS utm_source,
       COALESCE((SELECT value FROM strings WHERE id = events.utm_medium), '') AS utm_medium,
       COALESCE((SELECT value FROM strings WHERE id = events.utm_campaign), '') AS utm_campaign,
       COALESCE((SELECT value FROM strings WHERE id = events.platform), '') AS platform,
       COALESCE((SELECT value FROM strings WHERE id = events.os), '') AS os,
       COALESCE((SELECT value FROM strings WHERE id = events.os_version), '') AS os_version,
       COALESCE((SELECT value FROM strings WHERE id = events.os_name), '') AS os_name,
       COALESCE((SELECT value FROM strings WHERE id = events.browser), '') AS browser,
       COALESCE((SELECT value FROM strings WHERE id = events.browser_version), '') AS browser_version,
       COALESCE((SELECT value FROM strings WHERE id = events.browser_locale), '') AS browser_locale,
       COALESCE((SELECT value FROM strings WHERE id = events.app_version), '') AS app_version,
       COALESCE((SELECT value FROM strings WHERE id = events.app_locale), '') AS app_locale,
       COALESCE((SELECT value FROM strings WHERE id = events.device), '') AS device,
       COALESCE((SELECT value FROM strings WHERE id = events.device_model), '') AS device_model,
       events.display_width AS display_width,
       events.display_height AS display_height,
       COALESCE((SELECT value FROM strings WHERE id = events.country), '') AS country,
       events.consent AS consent,
       (SELECT json_group_object(k.key, COALESCE(v.value, j.value))
  FROM json_each(events.attributes) j
  JOIN project_attributes k ON k.id = CAST(j.key AS INTEGER)
  LEFT JOIN attribute_values v ON v.key_id = k.id AND v.n = j.value AND j.type = 'integer') AS attributes,
       events.value AS value,
       COALESCE((SELECT value FROM strings WHERE id = events.measure), '') AS measure,
       events.sample_rate AS sample_rate,
       events.bucket AS bucket
FROM events WHERE events.family = 1;

CREATE VIEW raw_product AS
SELECT 'product' AS family,
       events.project_id AS project_id,
       events.day AS day,
       CASE WHEN typeof(events.id) = 'blob' THEN substr(lower(hex(events.id)), 1, 8) || '-' || substr(lower(hex(events.id)), 9, 4) || '-' || substr(lower(hex(events.id)), 13, 4) || '-' || substr(lower(hex(events.id)), 17, 4) || '-' || substr(lower(hex(events.id)), 21, 12) ELSE events.id END AS id,
       COALESCE((SELECT value FROM strings WHERE id = events.event_name), '') AS event_name,
       strftime('%Y-%m-%dT%H:%M:%SZ', events.ts, 'unixepoch') AS ts,
       CASE WHEN events.received_at = 0 THEN '' ELSE strftime('%Y-%m-%dT%H:%M:%SZ', events.received_at, 'unixepoch') END AS received_at,
       COALESCE((SELECT value FROM strings WHERE id = events.kind), '') AS kind,
       CASE WHEN typeof(events.actor_id) <> 'blob' THEN events.actor_id WHEN substr(events.actor_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.actor_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 21, 12) ELSE lower(hex(substr(events.actor_id, 2))) END AS actor_id,
       COALESCE((SELECT value FROM strings WHERE id = events.actor_kind), '') AS actor_kind,
       CASE WHEN typeof(events.user_id) <> 'blob' THEN events.user_id WHEN substr(events.user_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.user_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.user_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.user_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.user_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.user_id, 2))), 21, 12) ELSE lower(hex(substr(events.user_id, 2))) END AS user_id,
       CASE WHEN typeof(events.group_id) <> 'blob' THEN events.group_id WHEN substr(events.group_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.group_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.group_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.group_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.group_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.group_id, 2))), 21, 12) ELSE lower(hex(substr(events.group_id, 2))) END AS group_id,
       CASE WHEN typeof(events.session_id) <> 'blob' THEN events.session_id WHEN substr(events.session_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.session_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.session_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.session_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.session_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.session_id, 2))), 21, 12) ELSE lower(hex(substr(events.session_id, 2))) END AS session_id,
       COALESCE((SELECT value FROM strings WHERE id = events.host), '') AS host,
       COALESCE((SELECT value FROM strings WHERE id = events.path), '') AS path,
       COALESCE((SELECT value FROM strings WHERE id = events.referrer_source), '') AS referrer_source,
       COALESCE((SELECT value FROM strings WHERE id = events.utm_source), '') AS utm_source,
       COALESCE((SELECT value FROM strings WHERE id = events.utm_medium), '') AS utm_medium,
       COALESCE((SELECT value FROM strings WHERE id = events.utm_campaign), '') AS utm_campaign,
       COALESCE((SELECT value FROM strings WHERE id = events.platform), '') AS platform,
       COALESCE((SELECT value FROM strings WHERE id = events.os), '') AS os,
       COALESCE((SELECT value FROM strings WHERE id = events.os_version), '') AS os_version,
       COALESCE((SELECT value FROM strings WHERE id = events.os_name), '') AS os_name,
       COALESCE((SELECT value FROM strings WHERE id = events.browser), '') AS browser,
       COALESCE((SELECT value FROM strings WHERE id = events.browser_version), '') AS browser_version,
       COALESCE((SELECT value FROM strings WHERE id = events.browser_locale), '') AS browser_locale,
       COALESCE((SELECT value FROM strings WHERE id = events.app_version), '') AS app_version,
       COALESCE((SELECT value FROM strings WHERE id = events.app_locale), '') AS app_locale,
       COALESCE((SELECT value FROM strings WHERE id = events.device), '') AS device,
       COALESCE((SELECT value FROM strings WHERE id = events.device_model), '') AS device_model,
       events.display_width AS display_width,
       events.display_height AS display_height,
       COALESCE((SELECT value FROM strings WHERE id = events.country), '') AS country,
       events.consent AS consent,
       (SELECT json_group_object(k.key, COALESCE(v.value, j.value))
  FROM json_each(events.attributes) j
  JOIN project_attributes k ON k.id = CAST(j.key AS INTEGER)
  LEFT JOIN attribute_values v ON v.key_id = k.id AND v.n = j.value AND j.type = 'integer') AS attributes,
       events.value AS value,
       COALESCE((SELECT value FROM strings WHERE id = events.measure), '') AS measure,
       events.sample_rate AS sample_rate,
       events.bucket AS bucket
FROM events WHERE events.family = 2;

CREATE VIEW raw_measures AS
SELECT 'measures' AS family,
       events.project_id AS project_id,
       events.day AS day,
       CASE WHEN typeof(events.id) = 'blob' THEN substr(lower(hex(events.id)), 1, 8) || '-' || substr(lower(hex(events.id)), 9, 4) || '-' || substr(lower(hex(events.id)), 13, 4) || '-' || substr(lower(hex(events.id)), 17, 4) || '-' || substr(lower(hex(events.id)), 21, 12) ELSE events.id END AS id,
       COALESCE((SELECT value FROM strings WHERE id = events.event_name), '') AS event_name,
       strftime('%Y-%m-%dT%H:%M:%SZ', events.ts, 'unixepoch') AS ts,
       CASE WHEN events.received_at = 0 THEN '' ELSE strftime('%Y-%m-%dT%H:%M:%SZ', events.received_at, 'unixepoch') END AS received_at,
       COALESCE((SELECT value FROM strings WHERE id = events.kind), '') AS kind,
       CASE WHEN typeof(events.actor_id) <> 'blob' THEN events.actor_id WHEN substr(events.actor_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.actor_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.actor_id, 2))), 21, 12) ELSE lower(hex(substr(events.actor_id, 2))) END AS actor_id,
       COALESCE((SELECT value FROM strings WHERE id = events.actor_kind), '') AS actor_kind,
       CASE WHEN typeof(events.user_id) <> 'blob' THEN events.user_id WHEN substr(events.user_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.user_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.user_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.user_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.user_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.user_id, 2))), 21, 12) ELSE lower(hex(substr(events.user_id, 2))) END AS user_id,
       CASE WHEN typeof(events.group_id) <> 'blob' THEN events.group_id WHEN substr(events.group_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.group_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.group_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.group_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.group_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.group_id, 2))), 21, 12) ELSE lower(hex(substr(events.group_id, 2))) END AS group_id,
       CASE WHEN typeof(events.session_id) <> 'blob' THEN events.session_id WHEN substr(events.session_id, 1, 1) = x'01' THEN substr(lower(hex(substr(events.session_id, 2))), 1, 8) || '-' || substr(lower(hex(substr(events.session_id, 2))), 9, 4) || '-' || substr(lower(hex(substr(events.session_id, 2))), 13, 4) || '-' || substr(lower(hex(substr(events.session_id, 2))), 17, 4) || '-' || substr(lower(hex(substr(events.session_id, 2))), 21, 12) ELSE lower(hex(substr(events.session_id, 2))) END AS session_id,
       COALESCE((SELECT value FROM strings WHERE id = events.host), '') AS host,
       COALESCE((SELECT value FROM strings WHERE id = events.path), '') AS path,
       COALESCE((SELECT value FROM strings WHERE id = events.referrer_source), '') AS referrer_source,
       COALESCE((SELECT value FROM strings WHERE id = events.utm_source), '') AS utm_source,
       COALESCE((SELECT value FROM strings WHERE id = events.utm_medium), '') AS utm_medium,
       COALESCE((SELECT value FROM strings WHERE id = events.utm_campaign), '') AS utm_campaign,
       COALESCE((SELECT value FROM strings WHERE id = events.platform), '') AS platform,
       COALESCE((SELECT value FROM strings WHERE id = events.os), '') AS os,
       COALESCE((SELECT value FROM strings WHERE id = events.os_version), '') AS os_version,
       COALESCE((SELECT value FROM strings WHERE id = events.os_name), '') AS os_name,
       COALESCE((SELECT value FROM strings WHERE id = events.browser), '') AS browser,
       COALESCE((SELECT value FROM strings WHERE id = events.browser_version), '') AS browser_version,
       COALESCE((SELECT value FROM strings WHERE id = events.browser_locale), '') AS browser_locale,
       COALESCE((SELECT value FROM strings WHERE id = events.app_version), '') AS app_version,
       COALESCE((SELECT value FROM strings WHERE id = events.app_locale), '') AS app_locale,
       COALESCE((SELECT value FROM strings WHERE id = events.device), '') AS device,
       COALESCE((SELECT value FROM strings WHERE id = events.device_model), '') AS device_model,
       events.display_width AS display_width,
       events.display_height AS display_height,
       COALESCE((SELECT value FROM strings WHERE id = events.country), '') AS country,
       events.consent AS consent,
       (SELECT json_group_object(k.key, COALESCE(v.value, j.value))
  FROM json_each(events.attributes) j
  JOIN project_attributes k ON k.id = CAST(j.key AS INTEGER)
  LEFT JOIN attribute_values v ON v.key_id = k.id AND v.n = j.value AND j.type = 'integer') AS attributes,
       events.value AS value,
       COALESCE((SELECT value FROM strings WHERE id = events.measure), '') AS measure,
       events.sample_rate AS sample_rate,
       events.bucket AS bucket
FROM events WHERE events.family = 3;

-- v_identity_daily: 025's text on the encoded rows. Families are their
-- codes; the ids group as stored (the encoding is one-to-one, so the
-- counts are unchanged), the rank's tie-break orders by the decoded id as
-- it ordered by the text, and the id decodes once per grouped row.
CREATE VIEW v_identity_daily AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='identities_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 1000) AS n
)
SELECT project_id, day, kind, id, actors, users, views, events
FROM agg_identity_daily
UNION ALL
SELECT project_id, day, kind, CASE WHEN typeof(sid) <> 'blob' THEN sid WHEN substr(sid, 1, 1) = x'01' THEN substr(lower(hex(substr(sid, 2))), 1, 8) || '-' || substr(lower(hex(substr(sid, 2))), 9, 4) || '-' || substr(lower(hex(substr(sid, 2))), 13, 4) || '-' || substr(lower(hex(substr(sid, 2))), 17, 4) || '-' || substr(lower(hex(substr(sid, 2))), 21, 12) ELSE lower(hex(substr(sid, 2))) END AS id, actors, users, views, events
FROM (
  SELECT project_id, day, k.kind AS kind,
         CASE k.kind WHEN 'user' THEN user_id ELSE group_id END AS sid,
         COUNT(DISTINCT actor_id) AS actors,
         CASE WHEN k.kind = 'user' THEN 1 ELSE COUNT(DISTINCT NULLIF(user_id, '')) END AS users,
         SUM(family = 1) AS views, SUM(family = 2) AS events,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, k.kind
                            ORDER BY COUNT(*) DESC, CASE WHEN typeof(CASE k.kind WHEN 'user' THEN user_id ELSE group_id END) <> 'blob' THEN CASE k.kind WHEN 'user' THEN user_id ELSE group_id END WHEN substr(CASE k.kind WHEN 'user' THEN user_id ELSE group_id END, 1, 1) = x'01' THEN substr(lower(hex(substr(CASE k.kind WHEN 'user' THEN user_id ELSE group_id END, 2))), 1, 8) || '-' || substr(lower(hex(substr(CASE k.kind WHEN 'user' THEN user_id ELSE group_id END, 2))), 9, 4) || '-' || substr(lower(hex(substr(CASE k.kind WHEN 'user' THEN user_id ELSE group_id END, 2))), 13, 4) || '-' || substr(lower(hex(substr(CASE k.kind WHEN 'user' THEN user_id ELSE group_id END, 2))), 17, 4) || '-' || substr(lower(hex(substr(CASE k.kind WHEN 'user' THEN user_id ELSE group_id END, 2))), 21, 12) ELSE lower(hex(substr(CASE k.kind WHEN 'user' THEN user_id ELSE group_id END, 2))) END) AS rn
  FROM events
  JOIN (SELECT 'user' AS kind UNION ALL SELECT 'group') k
  WHERE family IN (1, 2)
    AND CASE k.kind WHEN 'user' THEN user_id ELSE group_id END <> ''
  GROUP BY project_id, day, k.kind, CASE k.kind WHEN 'user' THEN user_id ELSE group_id END
) live
WHERE rn <= (SELECT n FROM cap)
  AND NOT EXISTS (
    SELECT 1 FROM agg_identity_daily g
    WHERE g.project_id = live.project_id AND g.day = live.day);

-- Every other view as 040 left it, byte-for-byte: it reads raw_*, which
-- keep their columns.
CREATE VIEW v_measures_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
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
                 WHEN '$country'         THEN NULLIF(e.country, '')
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
          SELECT project_id, key AS attr_key FROM project_attributes WHERE breakdown = 1
          UNION
          SELECT p.id, s.column1
          FROM projects p,
               (VALUES ('$platform'), ('$os'), ('$app_version'), ('$app_locale'),
                       ('$kind'), ('$browser'), ('$device'), ('$browser_locale'),
                       ('$country')) s
        ) k ON k.project_id = e.project_id
      )
      WHERE attr_value IS NOT NULL
      GROUP BY project_id, day, event_name, measure, attr_key, attr_value, bucket
    )
  )
  WINDOW f AS (PARTITION BY project_id, day, event_name, measure, attr_key, kept, bucket)
)
WHERE rf = 1;

CREATE VIEW v_measures_daily AS
SELECT project_id, day, event_name, measure, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
       samples, weight, sum
FROM agg_measures_daily
UNION ALL
SELECT project_id, day, event_name, measure, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END,
       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
FROM raw_measures
GROUP BY project_id, day, event_name, measure, bucket;

CREATE VIEW v_product_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
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
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors,
           json_group_array(NULLIF(group_id, '')) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS groups,
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
               WHEN '$country'         THEN NULLIF(e.country, '')
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
        SELECT project_id, key AS attr_key FROM project_attributes WHERE breakdown = 1
        UNION
        SELECT p.id, s.column1
        FROM projects p,
             (VALUES ('$platform'), ('$os'), ('$app_version'), ('$app_locale'),
                     ('$kind'), ('$browser'), ('$device'), ('$browser_locale'),
                     ('$country')) s
      ) k ON k.project_id = e.project_id
    )
    WHERE attr_value IS NOT NULL
    GROUP BY project_id, day, event_name, attr_key, attr_value
  )
  WINDOW w AS (PARTITION BY project_id, day, event_name, attr_key)
)
WHERE rn <= (SELECT n FROM cap) + 1;

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

CREATE VIEW v_views_consent AS
SELECT project_id, day, consent, visitors, views FROM agg_views_consent
UNION ALL
SELECT project_id, day,
       CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM raw_views v
GROUP BY project_id, day, CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END;

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

-- v_identities: identities as stored, for widget SQL (spec D15).
CREATE VIEW v_identities AS
SELECT project_id, kind, id, name, last_seen_day, updated_at FROM identities;
