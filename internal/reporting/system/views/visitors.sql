-- Visitors as the Visitors and views chart counts them: distinct per day
-- and kind, added up over the days. previous is the same count over as
-- many days just before :from.
WITH daily AS (
  SELECT day, kind, count(DISTINCT actor_id) AS visitors
  FROM views
  WHERE project_id = :project
    AND day BETWEEN :from - CAST(:to - :from + 1 AS INTEGER) AND :to
  GROUP BY day, kind
)
SELECT COALESCE(SUM(visitors) FILTER (WHERE day >= :from), 0) AS value,
       COALESCE(SUM(visitors) FILTER (WHERE day < :from), 0) AS previous
FROM daily
