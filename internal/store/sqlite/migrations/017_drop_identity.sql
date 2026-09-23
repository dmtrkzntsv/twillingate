-- The project's identity mode is gone: the collector stores what a client
-- sends, and the served SDK's identity mode decides what is sent
-- (docs/twillingate.md, Identity). Data already stored is untouched: ids
-- hashed under the old anonymous mode stay hashed, and cannot be linked
-- to anything the client sends from now on.
ALTER TABLE projects DROP COLUMN identity;
