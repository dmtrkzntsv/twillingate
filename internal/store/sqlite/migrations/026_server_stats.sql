-- 026: server_stats holds measurements the daily pass takes about the
-- server itself — today each project's estimated disk use. One row per
-- stat, scope and day, kept as a daily history until the project is
-- purged: key is a constant in the code (internal/store/store.go),
-- project_id 0 is the whole server, measured_at is the UTC day
-- (YYYY-MM-DD) the value was measured on. A second run on the same day
-- (the pass also runs at start) replaces that day's rows.
-- A new stat needs a new constant, never a migration.
CREATE TABLE server_stats (
  key         TEXT    NOT NULL,
  project_id  INTEGER NOT NULL DEFAULT 0,
  measured_at TEXT    NOT NULL,
  value       INTEGER NOT NULL,
  PRIMARY KEY (key, project_id, measured_at)
) WITHOUT ROWID;
