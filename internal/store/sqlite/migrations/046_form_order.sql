-- 046: forms in an order of your choosing. sort_key is a fractional key
-- (internal/shared/sortkey), as projects, dashboards and project tabs
-- use: list_forms, the console and the CLI show a project's forms by key,
-- and move_form writes one row, the moved form's key between its new
-- neighbours. A new form, created up front or by a first submission,
-- takes the key after the project's largest, archived forms included, so
-- it comes last. An archived form keeps its key, so a restore puts it
-- back where it was.
--
-- Today's order carries over: live forms before archived ones, each list
-- drafts first, then by name, as list_forms showed them. Each project's
-- forms get the keys sortkey hands out when appending, as 034 gave the
-- projects: a0 … az (62 keys), then b00 … bzz (3,844), then c000 … czzz.
ALTER TABLE forms ADD COLUMN sort_key TEXT NOT NULL DEFAULT '';

WITH ranked AS (
  SELECT project_id, name,
         ROW_NUMBER() OVER (PARTITION BY project_id
                            ORDER BY archived_at IS NOT NULL, status = 'draft' DESC, name) - 1 AS r,
         '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz' AS d
  FROM forms
)
UPDATE forms SET sort_key = (
  SELECT CASE
    WHEN r < 62 THEN 'a' || substr(d, r + 1, 1)
    WHEN r < 3906 THEN 'b' || substr(d, (r - 62) / 62 + 1, 1) || substr(d, (r - 62) % 62 + 1, 1)
    ELSE 'c' || substr(d, (r - 3906) / 3844 + 1, 1) || substr(d, (r - 3906) / 62 % 62 + 1, 1)
              || substr(d, (r - 3906) % 62 + 1, 1)
  END
  FROM ranked WHERE ranked.project_id = forms.project_id AND ranked.name = forms.name);
