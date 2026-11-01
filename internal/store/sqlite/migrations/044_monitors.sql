-- 044: availability monitors (spec 2026-10-31-availability-monitoring). One
-- row per monitor a project has been given, keyed by its origin: an optional
-- text the page must contain, the last certificate reading, how many redirect
-- hops the last check that got an answer followed, and the last manual check: a
-- request the runner serves, and its result. A row's origin is one of the
-- project's: an origins edit that drops it deletes the row. A plain new table.
CREATE TABLE monitors (
    project_id        INTEGER NOT NULL REFERENCES projects(id),
    origin            TEXT    NOT NULL,
    enabled           INTEGER NOT NULL DEFAULT 0,
    contains          TEXT    NOT NULL DEFAULT '',
    cert_expires_at   TEXT    NOT NULL DEFAULT '',
    cert_issuer       TEXT    NOT NULL DEFAULT '',
    cert_valid        INTEGER NOT NULL DEFAULT 0,
    cert_error        TEXT    NOT NULL DEFAULT '',
    cert_checked_at   TEXT    NOT NULL DEFAULT '',
    redirects         INTEGER NOT NULL DEFAULT 0,
    check_requested_at TEXT   NOT NULL DEFAULT '',
    check_finished_at TEXT    NOT NULL DEFAULT '',
    check_up          INTEGER NOT NULL DEFAULT 0,
    check_status      INTEGER NOT NULL DEFAULT 0,
    check_reason      TEXT    NOT NULL DEFAULT '',
    check_error       TEXT    NOT NULL DEFAULT '',
    check_ms          INTEGER NOT NULL DEFAULT 0,
    updated_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    PRIMARY KEY (project_id, origin)
) WITHOUT ROWID;

-- The registration-expiry reading of a main domain, per project: a project's
-- origins on one main domain share it. A row goes when no monitor of the
-- project has an origin on that domain any more.
CREATE TABLE monitor_domains (
    project_id INTEGER NOT NULL REFERENCES projects(id),
    domain     TEXT    NOT NULL,
    expires_at TEXT    NOT NULL DEFAULT '',
    checked_at TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (project_id, domain)
) WITHOUT ROWID;
