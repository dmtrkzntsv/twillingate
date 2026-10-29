-- 043: a submission is archived rather than deleted (archived_at, RFC 3339
-- UTC, NULL while active). An archived submission leaves its form's table,
-- counts and export, can be restored, and the daily pass purges it
-- RETENTION_ARCHIVED_DAYS after archiving, as it purges an archived form.
-- The partial index serves that purge.
ALTER TABLE submissions ADD COLUMN archived_at TEXT;
CREATE INDEX submissions_archived ON submissions (archived_at) WHERE archived_at IS NOT NULL;
