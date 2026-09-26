-- 021: reporting (spec 2026-09-25). Components are the React components
-- the UI ships, registered by the release; dashboards and widgets are what
-- agents build, plus the system dashboards the release migrates on every
-- run (reporting_migrations records each). Two foreign keys and no checks:
-- a dashboard's widgets go with it, and a component deleted by a release
-- leaves its widgets with component NULL ("component removed", D14).
-- Every other rule is enforced in Go.
CREATE TABLE components (
    name           TEXT PRIMARY KEY,
    description    TEXT NOT NULL,
    accepts        TEXT NOT NULL,  -- JSON array of source type names
    inputs         TEXT NOT NULL,  -- JSON {open, columns}
    props          TEXT NOT NULL,  -- JSON schema
    default_width  INTEGER NOT NULL,
    default_height INTEGER NOT NULL
);

CREATE TABLE dashboards (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    owner           TEXT NOT NULL,  -- 'system' | 'user'
    title           TEXT NOT NULL,
    sort_key        TEXT NOT NULL,
    last_project_id INTEGER,
    last_range      TEXT,
    last_from       TEXT,
    last_to         TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    archived_at     TEXT
);
CREATE UNIQUE INDEX dashboards_order ON dashboards (owner, sort_key);

CREATE TABLE widgets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    dashboard_id INTEGER NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
    component    TEXT REFERENCES components(name) ON DELETE SET NULL,
    sort_key     TEXT NOT NULL,
    width        INTEGER NOT NULL,
    height       INTEGER NOT NULL,
    name         TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    props        TEXT NOT NULL DEFAULT '{}',
    source_type  TEXT NOT NULL,
    source       TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    archived_at  TEXT
);
CREATE UNIQUE INDEX widgets_order ON widgets (dashboard_id, sort_key);
CREATE UNIQUE INDEX widgets_name ON widgets (dashboard_id, name);

CREATE TABLE reporting_migrations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    hash       TEXT NOT NULL,
    version    TEXT NOT NULL,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- Ids 1–999 are the system dashboards' (spec D8): agent-made ones start
-- at 1001, so a system id can never collide with one.
INSERT INTO sqlite_sequence (name, seq) VALUES ('dashboards', 1000);
