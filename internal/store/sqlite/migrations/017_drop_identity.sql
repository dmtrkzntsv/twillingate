-- The project's identity mode is gone: the collector stores what a client
-- sends, and the served SDK's identity mode decides what is sent
-- (docs/twillingate.md, Identity). Ids hashed under the old anonymous mode
-- stay hashed and cannot be linked to anything the client sends from now
-- on.
--
-- Raw rows an anonymous project received with a $user_id or $install_id
-- carry actor_kind user/install over a daily-rotating hash. Now that
-- cohorts are built for every project, that kind would make each day's
-- hashes a cohort that never returns, so they are re-kinded to connection,
-- which is what a daily-rotating hash is. The hashed user_id itself is
-- cleared and the per-day identity rows built from it (agg_identity_daily,
-- kind user) are deleted, because they are rotating hashes that the old
-- anonymous mode hid and the users page and identities tool would now show
-- unfiltered; group rows are untouched, since groups were always stored
-- raw. Nothing else about the rows changes. This must run while the column
-- still exists.
UPDATE views SET user_id = ''
WHERE user_id <> ''
  AND project_id IN (SELECT id FROM projects WHERE identity = 'anonymous');
UPDATE events SET user_id = ''
WHERE user_id <> ''
  AND project_id IN (SELECT id FROM projects WHERE identity = 'anonymous');
UPDATE views SET actor_kind = 'connection'
WHERE actor_kind IN ('user', 'install')
  AND project_id IN (SELECT id FROM projects WHERE identity = 'anonymous');
UPDATE events SET actor_kind = 'connection'
WHERE actor_kind IN ('user', 'install')
  AND project_id IN (SELECT id FROM projects WHERE identity = 'anonymous');
DELETE FROM agg_identity_daily
WHERE kind = 'user'
  AND project_id IN (SELECT id FROM projects WHERE identity = 'anonymous');
ALTER TABLE projects DROP COLUMN identity;
