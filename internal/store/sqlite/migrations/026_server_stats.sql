-- 026: server_stats holds measurements the daily pass takes about the
-- server itself — today each project's estimated disk use. One row per
-- stat and scope, latest value only: key is a constant in the code
-- (internal/store/store.go), project_id 0 is the whole
-- server, measured_at is when the value was measured (RFC 3339, UTC).
-- A new stat needs a new constant, never a migration.
CREATE TABLE server_stats (
  key         TEXT    NOT NULL,
  project_id  INTEGER NOT NULL DEFAULT 0,
  value       INTEGER NOT NULL,
  measured_at TEXT    NOT NULL,
  PRIMARY KEY (key, project_id)
) WITHOUT ROWID;
