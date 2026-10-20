-- Users are distinct per event and day, and cannot be summed across event
-- names (one person firing two events would count twice), so a day's
-- figure is the largest single-event count for that version, a floor on
-- the true number, as in the App versions table.
WITH d AS (
  SELECT day, app_version, count(DISTINCT actor_id) AS users
  FROM product
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND app_version <> ''
  GROUP BY day, event_name, app_version
)
SELECT day AS x, app_version AS series, MAX(users) AS y
FROM d
GROUP BY day, app_version
ORDER BY day, y DESC, series
