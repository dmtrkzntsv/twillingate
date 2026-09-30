-- 022: dashboard groups (spec 2026-09-30). The dashboards sharing a
-- group_id are one sidebar entry, their live rows its tabs in sort_key
-- order. A new group's id is its first dashboard's id (ids are never
-- reused), so every existing dashboard starts as a group of one. No
-- foreign key: a group is only the rows that share the number, and
-- every rule (one owner per group, a group's rows contiguous in the
-- owner's order) is enforced in Go.
ALTER TABLE dashboards ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0;
UPDATE dashboards SET group_id = id;
CREATE INDEX dashboards_group ON dashboards (group_id);
-- A row inserted without a group_id (the column DEFAULT 0) is a group of
-- its own. The store relies on this for every new group, and it keeps a
-- rolled-back binary, which never names group_id, from bundling every
-- dashboard it creates into one "group 0". A rebuild of the dashboards
-- table must recreate this trigger.
CREATE TRIGGER dashboards_own_group AFTER INSERT ON dashboards
WHEN NEW.group_id = 0
BEGIN
  UPDATE dashboards SET group_id = NEW.id WHERE id = NEW.id;
END;
