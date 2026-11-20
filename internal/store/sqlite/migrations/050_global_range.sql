-- 050: the date range is no longer stored per dashboard. The web app keeps
-- one range for every dashboard and project page, in the browser; a
-- dashboard keeps only the project its viewer last chose. Nothing changes
-- on upgrade but that each dashboard opens on that one range (7 days until
-- one is chosen) rather than its own.
ALTER TABLE dashboards DROP COLUMN last_range;
ALTER TABLE dashboards DROP COLUMN last_from;
ALTER TABLE dashboards DROP COLUMN last_to;
