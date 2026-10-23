-- Visitors are distinct per day, platform and version.
SELECT day AS x, COALESCE(platform, '') || ' ' || app_version AS series, count(DISTINCT actor_id) AS y
FROM views
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND app_version <> ''
GROUP BY day, platform, app_version
ORDER BY day, series
