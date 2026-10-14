-- 041: a submission's source is found after it arrives (raw-first PR 5b):
-- a job reads the event files for the submitter's last non-direct visit.
-- Until then the submission holds the $form_submit event as the request
-- knew it (pending_event, JSON) and whether to write it (pending_write:
-- 1 when its form was approved on arrival). Both are NULL once attributed,
-- as they are for every submission from before this migration.
ALTER TABLE submissions ADD COLUMN pending_event TEXT;
ALTER TABLE submissions ADD COLUMN pending_write INTEGER;
CREATE INDEX submissions_pending ON submissions (received_at) WHERE pending_event IS NOT NULL;
