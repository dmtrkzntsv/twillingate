-- 031: dashboard group names (spec 2026-10-04). A row names one group of
-- dashboards (dashboards.group_id); a group with no row shows its first
-- live tab's title. No foreign key: dashboards.group_id is not unique.
-- The store moves a row when a group's id is handed over (D3), and these
-- triggers delete it once no dashboard, archived ones included, has its
-- group_id (D4): a purge, a system dashboard a release dropped, a group
-- of one joining another, or a write by an older binary after a
-- rollback. A rebuild of the dashboards table must recreate them, as it
-- must recreate 022's dashboards_own_group.
CREATE TABLE dashboard_groups (
    group_id INTEGER PRIMARY KEY,
    title    TEXT NOT NULL
);

CREATE TRIGGER dashboard_groups_gone_delete AFTER DELETE ON dashboards
BEGIN
  DELETE FROM dashboard_groups WHERE group_id = OLD.group_id
    AND NOT EXISTS (SELECT 1 FROM dashboards WHERE group_id = OLD.group_id);
END;

CREATE TRIGGER dashboard_groups_gone_update AFTER UPDATE OF group_id ON dashboards
WHEN NEW.group_id <> OLD.group_id
BEGIN
  DELETE FROM dashboard_groups WHERE group_id = OLD.group_id
    AND NOT EXISTS (SELECT 1 FROM dashboards WHERE group_id = OLD.group_id);
END;
