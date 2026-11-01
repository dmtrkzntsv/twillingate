-- 044: availability monitors (spec 2026-10-31-availability-monitoring). One
-- row per project that has been given monitor settings: the origin to check,
-- an optional text the page must contain, and the last certificate and
-- domain-expiry readings (cleared when the origin changes), and how many
-- redirect hops the last check that got an answer followed. A plain new table.
CREATE TABLE monitors (
    project_id        INTEGER PRIMARY KEY REFERENCES projects(id),
    enabled           INTEGER NOT NULL DEFAULT 0,
    origin            TEXT    NOT NULL DEFAULT '',
    contains          TEXT    NOT NULL DEFAULT '',
    cert_expires_at   TEXT    NOT NULL DEFAULT '',
    cert_issuer       TEXT    NOT NULL DEFAULT '',
    cert_valid        INTEGER NOT NULL DEFAULT 0,
    cert_error        TEXT    NOT NULL DEFAULT '',
    cert_checked_at   TEXT    NOT NULL DEFAULT '',
    domain_expires_at TEXT    NOT NULL DEFAULT '',
    domain_checked_at TEXT    NOT NULL DEFAULT '',
    redirects         INTEGER NOT NULL DEFAULT 0,
    updated_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
) WITHOUT ROWID;
