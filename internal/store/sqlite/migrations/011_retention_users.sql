-- Signed-in users, cohorted apart from everyone else. On an identified
-- project an actor is its user_id when the client sends one, so an actor
-- that ever carried a user_id is a user. Visitors who never sign in are
-- still actors, but unless the client keeps a stable $install_id they can
-- never be recognised on return -- each page load is a new actor that never
-- comes back, and a curve blending them in buries the users' retention.
--
-- agg_retention.users is nullable on purpose: NULL is "not known". Rows
-- written before this migration cannot be recounted once their day has left
-- raw, and a 0 there would read as "nobody signed in" -- then a later offset
-- the pass does recompute would put users over a zero denominator and read
-- above 100%. Every row the pass writes from now on carries a number.
ALTER TABLE actors ADD COLUMN is_user INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agg_retention ADD COLUMN users INTEGER;

-- Mark existing actors from whatever still proves it: raw rows carrying a
-- user_id, or the per-user identity rollup, which outlives them. The
-- users counts in agg_retention are filled by the daily pass, which
-- recomputes every raw day and runs at boot, right after this migration.
UPDATE actors SET is_user = 1
WHERE EXISTS (
    SELECT 1 FROM agg_identity_daily g
    WHERE g.project = actors.project AND g.kind = 'user' AND g.id = actors.actor_id)
   OR EXISTS (
    SELECT 1 FROM product_events e
    WHERE e.project = actors.project AND e.actor_id = actors.actor_id AND e.user_id <> '')
   OR EXISTS (
    SELECT 1 FROM web_hits w
    WHERE w.project = actors.project AND w.actor_id = actors.actor_id AND w.user_id <> '')
   OR EXISTS (
    SELECT 1 FROM app_views a
    WHERE a.project = actors.project AND a.actor_id = actors.actor_id AND a.user_id <> '');

DROP VIEW v_retention;

-- user_cohort_size mirrors cohort_size: the offset-0 row's users, exposed
-- so a signed-in rate computes in one query. NULL when that row predates
-- the users count; skip such cohorts for signed-in rates.
CREATE VIEW v_retention AS
SELECT r.project, r.surface, r.cohort_day, r.day_offset, r.actors,
       c.actors AS cohort_size, r.users, c.users AS user_cohort_size
FROM agg_retention r
JOIN agg_retention c
  ON c.project = r.project AND c.surface = r.surface
 AND c.cohort_day = r.cohort_day AND c.day_offset = 0;
