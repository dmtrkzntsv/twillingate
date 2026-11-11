-- 048: what a monitor expects of the site beyond a 2xx answer, as one JSON
-- object (store.MonitorExpect) the process reads and writes whole: contains,
-- the text the page must hold, and redirect_to, the origin the check must
-- end at. Further expectations are keys of it, with no migration. An empty
-- object expects nothing more. Today's contains moves in; nothing else
-- changes on upgrade.
ALTER TABLE monitors ADD COLUMN expect TEXT NOT NULL DEFAULT '{}';
UPDATE monitors SET expect = json_object('contains', contains) WHERE contains != '';
ALTER TABLE monitors DROP COLUMN contains;
