-- 032: project tabs (spec 2026-10-05). A project page shows the
-- dashboards listed for it here. Each dashboard says whether it is in
-- the sidebar (sidebar) and whether a new project gets it as a tab
-- (project_tab); a built-in dashboard is never archived any more: hiding
-- one from the sidebar is sidebar = 0 (D5). Your own dashboards are
-- always in the sidebar: the service refuses sidebar on them.
--
-- The triggers below seed rows (D4): a new project gets every live
-- dashboard with project_tab = 1, and a new built-in (an INSERT, which
-- the release sync's upsert does only the first time) goes onto every
-- existing project. A safety net keeps D5 when a write goes outside the
-- service's checks (direct SQL, a future bug): a live user dashboard
-- with sidebar = 0 and project_tab = 0 is archived rather than left
-- orphaned, with sidebar back at 1 so a restore brings it into the
-- sidebar. One with project_tab = 1 stays as it is. A rebuild of
-- projects, dashboards or project_tabs must recreate them, as it must
-- 022's dashboards_own_group and 031's triggers.
ALTER TABLE dashboards ADD COLUMN sidebar INTEGER NOT NULL DEFAULT 1;
ALTER TABLE dashboards ADD COLUMN project_tab INTEGER NOT NULL DEFAULT 0;

CREATE TABLE project_tabs (
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    dashboard_id INTEGER NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
    sort_key     TEXT NOT NULL,
    PRIMARY KEY (project_id, dashboard_id)
);
CREATE INDEX project_tabs_dashboard ON project_tabs (dashboard_id);

-- Built-ins hidden today (archived, the old Hide) become live and out of
-- the sidebar: the sidebar looks the same after the upgrade.
UPDATE dashboards SET sidebar = 0, archived_at = NULL
  WHERE owner = 'system' AND archived_at IS NOT NULL;
UPDATE dashboards SET project_tab = 1 WHERE owner = 'system';
INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
  SELECT p.id, d.id, d.sort_key FROM projects p, dashboards d WHERE d.owner = 'system';

CREATE TRIGGER project_tabs_new_project AFTER INSERT ON projects
BEGIN
  INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
    SELECT NEW.id, d.id, d.sort_key FROM dashboards d
    WHERE d.project_tab = 1 AND d.archived_at IS NULL;
END;

CREATE TRIGGER project_tabs_new_builtin AFTER INSERT ON dashboards
WHEN NEW.owner = 'system' AND NEW.project_tab = 1
BEGIN
  INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
    SELECT p.id, NEW.id, NEW.sort_key FROM projects p;
END;

CREATE TRIGGER dashboards_user_orphaned AFTER UPDATE OF sidebar, project_tab ON dashboards
WHEN NEW.owner = 'user' AND NEW.sidebar = 0 AND NEW.project_tab = 0 AND NEW.archived_at IS NULL
BEGIN
  UPDATE dashboards SET archived_at = strftime('%Y-%m-%dT%H:%M:%SZ','now'), sidebar = 1,
      updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
    WHERE id = NEW.id;
END;

CREATE TRIGGER dashboards_user_orphaned_insert AFTER INSERT ON dashboards
WHEN NEW.owner = 'user' AND NEW.sidebar = 0 AND NEW.project_tab = 0 AND NEW.archived_at IS NULL
BEGIN
  UPDATE dashboards SET archived_at = strftime('%Y-%m-%dT%H:%M:%SZ','now'), sidebar = 1,
      updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
    WHERE id = NEW.id;
END;
