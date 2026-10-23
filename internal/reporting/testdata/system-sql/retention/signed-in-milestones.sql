-- The retention rows, as the daily pass used to count them: an actor
-- identified by a $user_id or a stable $install_id (actor_kind user or
-- install) joins the cohort of its first day with either, and is
-- "returned" at offset k when it sent a view or product event k days
-- later (measures do not count). It is a user from the first day it was
-- identified as one, an install before. Days before today only: today is
-- partial. cohort_size is the cohort's offset-0 count.
--
-- A cohort with nobody back on day k has no row at offset k, so pooling
-- only the rows that exist would drop it from the denominator and read
-- high. So the denominator at offset k is every cohort in the range that
-- has reached k, whether or not anyone came back that day, and the
-- numerator sums whoever did. "Reached" is measured against the clock,
-- not the data: the latest day with any activity is not the latest day
-- that has passed, and cutting there would drop complete days nobody came
-- back on.
WITH acts AS (
  SELECT DISTINCT actor_id, actor_kind, day
  FROM events
  -- Only actors a cohort can hold: an anonymous actor's id is a daily hash,
  -- never a user's or an install's, and reading them all was most of the
  -- dashboard's cost.
  WHERE project_id = :project AND family IN ('views', 'product') AND actor_id <> ''
    AND actor_kind IN ('user', 'install')
    AND day < current_date AND day <= :to + 45
),
firsts AS (
  SELECT actor_id, MIN(day) AS cohort_day, MIN(day) FILTER (WHERE actor_kind = 'user') AS user_day
  FROM acts
  WHERE actor_kind IN ('user', 'install')
  GROUP BY actor_id
),
returned AS (
  SELECT CASE WHEN f.user_day <= a.day THEN 'user' ELSE 'install' END AS actor_kind,
         f.cohort_day, a.day - f.cohort_day AS day_offset, count(DISTINCT a.actor_id) AS actors
  FROM (SELECT DISTINCT actor_id, day FROM acts) a
  JOIN firsts f ON f.actor_id = a.actor_id AND a.day >= f.cohort_day
  WHERE f.cohort_day BETWEEN :from AND :to
  GROUP BY 1, f.cohort_day, 3
),
retention AS (
  SELECT r.actor_kind, r.cohort_day, r.day_offset, r.actors, c.actors AS cohort_size
  FROM returned r
  JOIN returned c ON c.actor_kind = r.actor_kind AND c.cohort_day = r.cohort_day AND c.day_offset = 0
),
sizes AS (
  SELECT o.day_offset, SUM(r.cohort_size) AS cohort_size
  FROM retention r, range(0, 46) AS o(day_offset)
  WHERE r.actor_kind = 'user' AND r.day_offset = 0
    AND r.cohort_day + CAST(o.day_offset AS INTEGER) < current_date
  GROUP BY o.day_offset
),
back AS (
  SELECT day_offset, SUM(actors) AS actors
  FROM retention
  WHERE actor_kind = 'user' AND day_offset BETWEEN 0 AND 45
    AND cohort_day + CAST(day_offset AS INTEGER) < current_date
  GROUP BY day_offset
),
curve AS (
  SELECT s.day_offset,
         CASE WHEN s.cohort_size > 0 THEN COALESCE(b.actors, 0)::DOUBLE / s.cohort_size ELSE 0 END AS retention
  FROM sizes s
  LEFT JOIN back b ON b.day_offset = s.day_offset
)
SELECT MAX(CASE WHEN day_offset = 7 THEN retention END) AS "D7",
       MAX(CASE WHEN day_offset = 30 THEN retention END) AS "D30",
       MAX(CASE WHEN day_offset = 45 THEN retention END) AS "D45"
FROM curve
