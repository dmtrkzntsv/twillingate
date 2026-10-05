-- Operating system names alone, versions summed in: the six with the most
-- visitors and the rest as Other, as many slices as a pie can show.
WITH o AS (
  SELECT os AS label, SUM(visitors) AS value,
         ROW_NUMBER() OVER (ORDER BY SUM(visitors) DESC, os) AS n
  FROM v_views_os
  WHERE project_id = :project
    AND day BETWEEN :from AND :to
    AND os != ''
  GROUP BY os
)
SELECT CASE WHEN n <= 6 THEN label ELSE 'Other' END AS label, SUM(value) AS value
FROM o
GROUP BY 1
ORDER BY MIN(n)
