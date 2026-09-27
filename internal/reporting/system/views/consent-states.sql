SELECT consent AS "Consent", SUM(visitors) AS "Visitors", SUM(views) AS "Views"
FROM v_views_consent
WHERE project_id = :project
  AND day BETWEEN :from AND :to
GROUP BY consent
ORDER BY SUM(visitors) DESC
