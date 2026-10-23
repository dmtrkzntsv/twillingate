-- Users and groups are distinct per event and day, and cannot be summed
-- (one person or one group firing two events would count twice), so the
-- largest single figure is shown, a floor on the true number.
WITH d AS (
  SELECT app_version, count(*) AS events,
         count(DISTINCT actor_id) AS users, count(DISTINCT group_id) AS groups
  FROM product
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND app_version <> ''
  GROUP BY day, event_name, app_version
)
SELECT app_version AS "Version", SUM(events) AS "Events",
       MAX(users) AS "Users (at least)",
       MAX(groups) AS "Groups (at least)"
FROM d
GROUP BY app_version
ORDER BY SUM(events) DESC, app_version
