-- 045: a widget share may have no project, for a widget shared from a
-- dashboard with none to pin it to. Such a share is never deleted with a
-- project; it lives until archived by hand or by its archive_at (NULL =
-- never). project_id and project_name are NULL together, and a share with
-- no project never names one. The table is rebuilt since SQLite cannot
-- drop NOT NULL in place; rows and indexes are kept as they were.
CREATE TABLE widget_shares_new (
    id           TEXT PRIMARY KEY,
    widget_id    INTEGER REFERENCES widgets(id) ON DELETE SET NULL,
    project_id   INTEGER REFERENCES projects(id),
    range_from   TEXT NOT NULL,
    range_to     TEXT NOT NULL,
    title        TEXT NOT NULL,
    project_name TEXT,
    caption_project INTEGER NOT NULL DEFAULT 1,
    caption_range   INTEGER NOT NULL DEFAULT 1,
    image        BLOB NOT NULL,
    image_2x     BLOB NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    archive_at   TEXT,
    archived_at  TEXT,
    CHECK ((project_id IS NULL) = (project_name IS NULL)),
    CHECK (project_id IS NOT NULL OR caption_project = 0)
);
INSERT INTO widget_shares_new
  (id, widget_id, project_id, range_from, range_to, title, project_name, caption_project, caption_range,
   image, image_2x, created_at, archive_at, archived_at)
  SELECT id, widget_id, project_id, range_from, range_to, title, project_name, caption_project, caption_range,
         image, image_2x, created_at, archive_at, archived_at
  FROM widget_shares;
DROP TABLE widget_shares;
ALTER TABLE widget_shares_new RENAME TO widget_shares;
CREATE INDEX idx_widget_shares_widget ON widget_shares(widget_id);
CREATE INDEX idx_widget_shares_archive ON widget_shares(archive_at) WHERE archive_at IS NOT NULL;
