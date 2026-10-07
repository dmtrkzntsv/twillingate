-- 035: forms and their submissions (spec 2026-10-05-forms-design.md).
-- A form is created by its first submission, as a draft that expires at
-- draft_until unless approved; expected_fields narrows what an approved
-- form keeps, and forms.fields lists every name ever seen. A submission is
-- a flat string-to-string row kept as long as its project (both tables are
-- in projectTables); visit snapshots the actor's session at write time.
-- Timestamps are text like widget_shares.
CREATE TABLE forms (
    project_id        INTEGER NOT NULL,
    name              TEXT    NOT NULL,
    purpose           TEXT    NOT NULL DEFAULT '',
    return_url        TEXT    NOT NULL DEFAULT '',
    fields            TEXT    NOT NULL DEFAULT '[]',
    status            TEXT    NOT NULL DEFAULT 'draft',
    expected_fields   TEXT,
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    draft_until       TEXT,
    approved_at       TEXT,
    closes_at         TEXT,
    last_submitted_at TEXT,
    archived_at       TEXT,
    PRIMARY KEY (project_id, name)
) WITHOUT ROWID;

CREATE TABLE submissions (
    project_id  INTEGER NOT NULL,
    id          TEXT    NOT NULL,
    form        TEXT    NOT NULL,
    received_at TEXT    NOT NULL,
    fields      TEXT    NOT NULL,
    actor_kind  TEXT    NOT NULL,
    actor_id    TEXT    NOT NULL,
    host        TEXT    NOT NULL DEFAULT '',
    path        TEXT    NOT NULL DEFAULT '',
    via         TEXT    NOT NULL,
    visit       TEXT,
    PRIMARY KEY (project_id, id)
);
CREATE INDEX submissions_form ON submissions (project_id, form, received_at);
