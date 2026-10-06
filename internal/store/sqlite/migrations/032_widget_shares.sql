-- 032: widget shares (docs/superpowers/specs/2026-10-05-widget-shares-design.md).
-- A share is a frozen picture of one widget: two PNGs and the text they
-- show, copied at capture so the public page matches the picture after a
-- rename. It lives as long as its project (deleteProject removes it, via
-- projectTables) and outlives its widget (widget_id goes NULL). archive_at
-- NULL = project lifetime; archived_at NULL = not archived yet.
-- caption_project and caption_range say whether the page names the project
-- and the range: only what the widget follows, so never a false caption.
CREATE TABLE widget_shares (
    id           TEXT PRIMARY KEY,
    widget_id    INTEGER REFERENCES widgets(id) ON DELETE SET NULL,
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    range_from   TEXT NOT NULL,
    range_to     TEXT NOT NULL,
    title        TEXT NOT NULL,
    project_name TEXT NOT NULL,
    caption_project INTEGER NOT NULL DEFAULT 1,
    caption_range   INTEGER NOT NULL DEFAULT 1,
    image        BLOB NOT NULL,
    image_2x     BLOB NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    archive_at   TEXT,
    archived_at  TEXT
);
CREATE INDEX idx_widget_shares_widget ON widget_shares(widget_id);
CREATE INDEX idx_widget_shares_archive ON widget_shares(archive_at) WHERE archive_at IS NOT NULL;
