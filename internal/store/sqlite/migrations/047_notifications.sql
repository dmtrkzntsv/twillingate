-- 047: notifications (spec 2026-11-07-notifications-design). notifications
-- holds one row per inbox item, which is also what goes to the channel: its
-- type from a closed vocabulary, its project (NULL: none), its subject (an
-- origin, a domain or a form), the facts it renders from as a JSON object
-- (with the project's name at creation), and its delivery on the channel:
-- pending until the dispatcher tries it, the attempts so far and when the
-- next is due. Archived rows are purged RETENTION_ARCHIVED_DAYS after
-- archiving; active ones never are. notify_states is what the tracker
-- remembers per (project, kind, subject), as JSON it owns, so a restart does
-- not repeat a send. form_bursts folds a form's submissions within
-- FormBurstWindow of its last immediate notification into one follow-up.
-- forms.notify is the form's notify-on-submission setting: off for every
-- form already there, set by an approval from now on. New tables and a
-- column with a default.
CREATE TABLE notifications (
    id              INTEGER PRIMARY KEY,
    type            TEXT    NOT NULL,
    project_id      INTEGER REFERENCES projects(id),
    subject         TEXT    NOT NULL DEFAULT '',
    fields          TEXT    NOT NULL DEFAULT '{}',
    created_at      TEXT    NOT NULL,
    archived_at     TEXT,
    delivery        TEXT    NOT NULL DEFAULT 'pending',
    delivery_error  TEXT    NOT NULL DEFAULT '',
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT
);
CREATE INDEX notifications_active ON notifications(archived_at, id);
CREATE INDEX notifications_pending ON notifications(delivery, id) WHERE delivery = 'pending';

CREATE TABLE notify_states (
    project_id INTEGER NOT NULL REFERENCES projects(id),
    kind       TEXT    NOT NULL,
    subject    TEXT    NOT NULL,
    data       TEXT    NOT NULL DEFAULT '{}',
    PRIMARY KEY (project_id, kind, subject)
) WITHOUT ROWID;

CREATE TABLE form_bursts (
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    form         TEXT    NOT NULL,
    window_start TEXT    NOT NULL,
    pending      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (project_id, form)
) WITHOUT ROWID;

ALTER TABLE forms ADD COLUMN notify INTEGER NOT NULL DEFAULT 0;
