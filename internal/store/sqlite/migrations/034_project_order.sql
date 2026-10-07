-- 034: projects in an order of your choosing. position orders every
-- listing of projects (the console, list_projects, the CLI), ascending,
-- id breaking ties. A move rewrites every project's position in one
-- transaction. Archived projects keep theirs, so a restore puts one back
-- where it was. Today's order, by id, carries over.
ALTER TABLE projects ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
UPDATE projects SET position = id;

-- A new project comes last, whoever inserts it. A rebuild of projects
-- must recreate this, as it must 032's project_tabs_new_project.
CREATE TRIGGER projects_position_last AFTER INSERT ON projects
WHEN NEW.position = 0
BEGIN
  UPDATE projects SET position = (SELECT MAX(position) FROM projects) + 1 WHERE id = NEW.id;
END;
