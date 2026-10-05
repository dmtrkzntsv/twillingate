-- Browser names alone, versions summed in: five slices, one per chart
-- color, so past five the four with the most visitors and the rest as Other.
WITH b AS (
  SELECT browser AS label, SUM(visitors) AS value,
         ROW_NUMBER() OVER (ORDER BY SUM(visitors) DESC, browser) AS n
  FROM v_views_browsers
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND browser != ''
  GROUP BY browser
)
SELECT CASE WHEN n <= 4 OR (SELECT COUNT(*) FROM b) <= 5 THEN label ELSE 'Other' END AS label, SUM(value) AS value
FROM b
GROUP BY 1
ORDER BY MIN(n)
