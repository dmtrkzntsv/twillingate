-- Visitors are distinct per day and consent state, added up.
WITH d AS (
  SELECT CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END AS consent,
         count(DISTINCT actor_id) AS visitors, count(*) AS views
  FROM views
  WHERE project_id = :project AND day BETWEEN :from AND :to
  GROUP BY day, 1
)
SELECT consent AS "Consent", SUM(visitors) AS "Visitors", SUM(views) AS "Views"
FROM d
GROUP BY consent
ORDER BY SUM(visitors) DESC, consent
