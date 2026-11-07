-- 046: forms in an order of your choosing. position orders a project's
-- forms in list_forms, the console and the CLI; move_form renumbers the
-- project's forms, the live ones in their new order, then the archived
-- ones in theirs. A new form, created up front or by a first submission,
-- takes the position after the project's largest, so it comes last. An
-- archived form keeps its position, so a restore puts it back there.
--
-- Today's order carries over: live forms before archived ones, each list
-- drafts first, then by name, as list_forms showed them.
ALTER TABLE forms ADD COLUMN position INTEGER NOT NULL DEFAULT 0;

WITH ranked AS (
  SELECT project_id, name,
         ROW_NUMBER() OVER (PARTITION BY project_id
                            ORDER BY archived_at IS NOT NULL, status = 'draft' DESC, name) AS r
  FROM forms
)
UPDATE forms SET position = (
  SELECT r FROM ranked WHERE ranked.project_id = forms.project_id AND ranked.name = forms.name);
