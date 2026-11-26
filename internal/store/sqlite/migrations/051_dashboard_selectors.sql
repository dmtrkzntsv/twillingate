-- 051: a dashboard's selectors (JSON, [] = none) and the selection a share was made under ('' = All).
ALTER TABLE dashboards ADD COLUMN selectors TEXT NOT NULL DEFAULT '[]';
ALTER TABLE widget_shares ADD COLUMN selection TEXT NOT NULL DEFAULT '';
