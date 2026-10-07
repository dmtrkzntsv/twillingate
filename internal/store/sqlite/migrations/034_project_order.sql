-- 034: projects in an order of your choosing. sort_key is a fractional
-- key (internal/shared/sortkey), as dashboards and project tabs use: a
-- move writes one row. Every listing of projects (the console,
-- list_projects, the CLI) shows the keyed projects by key, then the
-- unkeyed ones ('') by id. So today's order, by id, carries over, and a
-- new project, created unkeyed, comes last. The first move keys every
-- project in the order shown. Archived projects keep their key, so a
-- restore puts one back where it was.
ALTER TABLE projects ADD COLUMN sort_key TEXT NOT NULL DEFAULT '';
