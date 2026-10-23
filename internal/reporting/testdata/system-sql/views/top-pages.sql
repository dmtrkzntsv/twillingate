-- Visitors are distinct per day and path, added up. A view without a path
-- (an app's screen sent none) is the empty path.
WITH d AS (
  SELECT COALESCE(path, '') AS path, count(DISTINCT actor_id) AS visitors, count(*) AS views
  FROM views
  WHERE project_id = :project AND day BETWEEN :from AND :to
  GROUP BY day, 1
)
SELECT path AS "Path", SUM(visitors) AS "Visitors", SUM(views) AS "Views"
FROM d
GROUP BY path
ORDER BY SUM(views) DESC, path
LIMIT 20
