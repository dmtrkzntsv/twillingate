-- 041: a submission's source is found after it arrives (raw-first PR 5b):
-- a job reads the event files for the submitter's last non-direct visit.
-- Until then the submission holds the $form_submit event as the request
-- knew it (pending_event, JSON), whether to write it (pending_write: 1
-- when its form was approved on arrival) and the request's connection
-- hash (pending_connection, NULL if none), which the lookup follows to
-- the views sent before consent gave the visitor an id. All three are
-- NULL once attributed, as they are for every submission from before
-- this migration.
--
-- The event the job writes to events also waits here (unlogged_event,
-- JSON, set in the same transaction) until the job has appended it to
-- the event files: an outbox, so a crash or a failed log write between
-- the two is retried, never lost. NULL once logged, for a submission
-- whose event is not written, and for every submission from before this
-- migration.
ALTER TABLE submissions ADD COLUMN pending_event TEXT;
ALTER TABLE submissions ADD COLUMN pending_write INTEGER;
ALTER TABLE submissions ADD COLUMN pending_connection TEXT;
ALTER TABLE submissions ADD COLUMN unlogged_event TEXT;
CREATE INDEX submissions_pending ON submissions (received_at) WHERE pending_event IS NOT NULL;
CREATE INDEX submissions_unlogged ON submissions (received_at) WHERE unlogged_event IS NOT NULL;
