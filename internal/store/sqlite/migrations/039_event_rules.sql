-- 039: event rules (spec 2026-10-11-event-rules). A rename maps an event
-- name to another, and its history was rewritten when the rule was made;
-- a block drops the name at ingest and keeps it out of the aggregates.
-- One rule per name (the primary key): a block replaces a rename and the
-- other way round. Rename targets are never themselves a rule's name of
-- action rename (event_rules.go keeps chains collapsed), so one lookup
-- resolves a name.
CREATE TABLE event_rules (
    project_id INTEGER NOT NULL REFERENCES projects(id),
    family     TEXT    NOT NULL CHECK (family IN ('product', 'measures')),
    name       TEXT    NOT NULL,
    action     TEXT    NOT NULL CHECK (action IN ('rename', 'block')),
    to_name    TEXT,
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    PRIMARY KEY (project_id, family, name),
    CHECK ((action = 'rename') = (to_name IS NOT NULL))
) WITHOUT ROWID;
