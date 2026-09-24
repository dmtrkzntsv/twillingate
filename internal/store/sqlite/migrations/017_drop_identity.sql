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
-- which is what a daily-rotating hash is. Nothing else about the rows
-- changes. This must run while the column still exists.
UPDATE views SET actor_kind = 'connection'
WHERE actor_kind IN ('user', 'install')
  AND project_id IN (SELECT id FROM projects WHERE identity = 'anonymous');
UPDATE events SET actor_kind = 'connection'
WHERE actor_kind IN ('user', 'install')
  AND project_id IN (SELECT id FROM projects WHERE identity = 'anonymous');
ALTER TABLE projects DROP COLUMN identity;
