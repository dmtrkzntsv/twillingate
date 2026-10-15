SELECT day AS x, platform || ' ' || app_version AS series, SUM(visitors) AS y
FROM v_views_app_versions
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND app_version != ''
GROUP BY day, platform, app_version
ORDER BY day, series
