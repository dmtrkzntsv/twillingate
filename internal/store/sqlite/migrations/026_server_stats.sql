-- 026: server_stats holds what the daily pass measures and counts: each
-- project's estimated disk use and its views, events and measure samples
-- per day, and the database's size. One row per stat, scope and day, kept
-- as a daily history until the project is purged (past the aggregates'
-- retention): key is a constant in the code (internal/store/store.go),
-- project_id 0 is the whole server, measured_at is the UTC day
-- (YYYY-MM-DD) the value is for: the day a size was measured on, the day
-- a count counts. A later run replaces a day's rows.
-- A new stat needs a new constant, never a migration.
CREATE TABLE server_stats (
  key         TEXT    NOT NULL,
  project_id  INTEGER NOT NULL DEFAULT 0,
  measured_at TEXT    NOT NULL,
  value       INTEGER NOT NULL,
  PRIMARY KEY (key, project_id, measured_at)
) WITHOUT ROWID;
