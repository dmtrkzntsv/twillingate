-- 052: alerts (spec 2026-11-24-alerts-design). One row per alert a project has
-- been given: its definition as one JSON object, whether it is paused or
-- snoozed, the revision that counts its resets, the state the last evaluation
-- left and what it read, and the manual check a request asks the runner for.
-- The second table keeps each alert's evaluations, newest last by id, and goes
-- with its alert. Plain new tables. AUTOINCREMENT so a purged alert's id is
-- never reissued: an old notification's alert_id and link must not name a
-- new alert.
CREATE TABLE alerts (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id         INTEGER NOT NULL REFERENCES projects(id),
    name               TEXT    NOT NULL,
    definition         TEXT    NOT NULL,
    paused             INTEGER NOT NULL DEFAULT 0,
    snoozed_until      TEXT    NOT NULL DEFAULT '',
    revision           INTEGER NOT NULL DEFAULT 1,
    state              TEXT    NOT NULL DEFAULT 'pending',
    reason             TEXT    NOT NULL DEFAULT '',
    last_value         REAL,
    last_evaluated_at  TEXT    NOT NULL DEFAULT '',
    last_day           TEXT    NOT NULL DEFAULT '',
    fails              INTEGER NOT NULL DEFAULT 0,
    retry_at           TEXT    NOT NULL DEFAULT '',
    check_requested_at TEXT    NOT NULL DEFAULT '',
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL,
    archived_at        TEXT
);
CREATE UNIQUE INDEX alerts_live_name ON alerts(project_id, name) WHERE archived_at IS NULL;
CREATE TABLE alert_evaluations (
    id        INTEGER PRIMARY KEY,
    alert_id  INTEGER NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    at        TEXT    NOT NULL,
    day_from  TEXT    NOT NULL DEFAULT '',
    day_to    TEXT    NOT NULL DEFAULT '',
    value     REAL,
    compare   REAL,
    volume    REAL,
    outcome   TEXT    NOT NULL,
    error     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX alert_evaluations_alert ON alert_evaluations(alert_id, id);
CREATE INDEX alert_evaluations_at ON alert_evaluations(at);
