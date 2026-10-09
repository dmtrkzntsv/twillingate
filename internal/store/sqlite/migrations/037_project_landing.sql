-- 037: project landing (spec 2026-10-08). One order for all of a
-- project's tabs: project_tabs.sort_key, built-ins included, so a
-- built-in can be dragged like your own. The keys are rewritten here into
-- the order the page showed (built-ins by dashboards.sort_key, then your
-- own by row key, archived rows kept at their place) as 'b' + two base-62
-- digits of the rank: valid fractional keys (internal/shared/sortkey) for
-- up to 3,844 tabs on one project. A built-in a release adds goes last on
-- every project. forms.seen_at is how far the console has read a form's
-- submissions; existing forms are seen as of the upgrade.
WITH ranked AS (
  SELECT pt.project_id, pt.dashboard_id,
         ROW_NUMBER() OVER (
           PARTITION BY pt.project_id
           ORDER BY d.owner = 'system' DESC,
                    CASE WHEN d.owner = 'system' THEN d.sort_key ELSE pt.sort_key END,
                    pt.dashboard_id) - 1 AS r
    FROM project_tabs pt JOIN dashboards d ON d.id = pt.dashboard_id
)
UPDATE project_tabs SET sort_key =
    'b' || substr('0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz', ranked.r / 62 + 1, 1)
        || substr('0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz', ranked.r % 62 + 1, 1)
  FROM ranked
 WHERE project_tabs.project_id = ranked.project_id
   AND project_tabs.dashboard_id = ranked.dashboard_id;

-- A new built-in goes after the project's last tab: its largest key with
-- 'V' appended sorts after it and is still a valid key.
DROP TRIGGER project_tabs_new_builtin;
CREATE TRIGGER project_tabs_new_builtin AFTER INSERT ON dashboards
WHEN NEW.owner = 'system' AND NEW.project_tab = 1
BEGIN
  INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
    SELECT p.id, NEW.id,
           COALESCE((SELECT MAX(pt.sort_key) FROM project_tabs pt WHERE pt.project_id = p.id) || 'V', 'a0')
      FROM projects p;
END;

ALTER TABLE forms ADD COLUMN seen_at TEXT;
UPDATE forms SET seen_at = strftime('%Y-%m-%dT%H:%M:%SZ','now');
