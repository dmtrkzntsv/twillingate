-- 049: what a monitor expects of the site beyond a 2xx answer, as one JSON
-- object (store.MonitorExpect) the process reads and writes whole: contains,
-- the text the page must hold, and redirect_to, the origin the check must
-- end at (048's column). Further expectations are keys of it, with no
-- migration. An empty object expects nothing more. Both columns move in and
-- go; nothing changes on upgrade.
ALTER TABLE monitors ADD COLUMN expect TEXT NOT NULL DEFAULT '{}';
UPDATE monitors SET expect = json_object('contains', contains) WHERE contains != '' AND redirect_to = '';
UPDATE monitors SET expect = json_object('redirect_to', redirect_to) WHERE contains = '' AND redirect_to != '';
UPDATE monitors SET expect = json_object('contains', contains, 'redirect_to', redirect_to) WHERE contains != '' AND redirect_to != '';
ALTER TABLE monitors DROP COLUMN contains;
ALTER TABLE monitors DROP COLUMN redirect_to;
