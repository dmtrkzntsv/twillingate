-- A cohort with nobody back on day k has no row at offset k, so pooling
-- only the rows that exist would drop it from the denominator and read
-- high. So the denominator at offset k is every cohort in the range that
-- has reached k, whether or not anyone came back that day, and the
-- numerator sums whoever did.
--
-- "Reached" is measured against the clock, not the data: the latest day
-- with any activity is not the latest day processed, and cutting there
-- would drop complete days nobody came back on. Today is partial, and so
-- is yesterday until the 03:00 UTC pass recounts it, hence the three-hour
-- lag.
WITH RECURSIVE offsets(day_offset) AS (
  SELECT 0
  UNION ALL
  SELECT day_offset + 1 FROM offsets WHERE day_offset < 45
),
sizes AS (
  SELECT o.day_offset, SUM(r.cohort_size) AS cohort_size
  FROM v_retention r
  JOIN offsets o ON date(r.cohort_day, '+' || o.day_offset || ' days') < date('now', '-3 hours')
  WHERE r.project_id = :project AND r.actor_kind = 'user' AND r.day_offset = 0
    AND r.cohort_day BETWEEN :from AND :to
  GROUP BY o.day_offset
),
returned AS (
  SELECT day_offset, SUM(actors) AS actors
  FROM v_retention
  WHERE project_id = :project AND actor_kind = 'user' AND day_offset BETWEEN 0 AND 45
    AND cohort_day BETWEEN :from AND :to
    AND date(cohort_day, '+' || day_offset || ' days') < date('now', '-3 hours')
  GROUP BY day_offset
),
curve AS (
  SELECT s.day_offset,
         CASE WHEN s.cohort_size > 0 THEN COALESCE(r.actors, 0) * 1.0 / s.cohort_size ELSE 0 END AS retention
  FROM sizes s
  LEFT JOIN returned r ON r.day_offset = s.day_offset
)
SELECT day_offset AS x, retention AS y
FROM curve
ORDER BY day_offset
