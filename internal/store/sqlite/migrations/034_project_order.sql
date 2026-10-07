-- 034: projects in an order of your choosing. sort_key is a fractional
-- key (internal/shared/sortkey), as dashboards and project tabs use: a
-- move writes one row, and every listing of projects (the console,
-- list_projects, the CLI) shows them by key. A new project takes the key
-- after the largest, so it comes last. Archived projects keep theirs, so
-- a restore puts one back where it was.
--
-- Today's order, by id, carries over: the projects get the keys sortkey
-- hands out when appending, a0 … az (62 keys), then b00 … bzz (3,844),
-- then c000 … czzz (238,328).
ALTER TABLE projects ADD COLUMN sort_key TEXT NOT NULL DEFAULT '';

WITH ranked AS (
  SELECT id, ROW_NUMBER() OVER (ORDER BY id) - 1 AS r,
         '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz' AS d
  FROM projects
)
UPDATE projects SET sort_key = (
  SELECT CASE
    WHEN r < 62 THEN 'a' || substr(d, r + 1, 1)
    WHEN r < 3906 THEN 'b' || substr(d, (r - 62) / 62 + 1, 1) || substr(d, (r - 62) % 62 + 1, 1)
    ELSE 'c' || substr(d, (r - 3906) / 3844 + 1, 1) || substr(d, (r - 3906) / 62 % 62 + 1, 1)
              || substr(d, (r - 3906) % 62 + 1, 1)
  END
  FROM ranked WHERE ranked.id = projects.id);
