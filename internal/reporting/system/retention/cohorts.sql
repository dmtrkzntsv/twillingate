-- The retention rows, as the daily pass used to count them: an actor
-- identified by a $user_id or a stable $install_id (actor_kind user or
-- install) joins the cohort of its first day with either, and is
-- "returned" at offset k when it sent a view or product event k days
-- later (measures do not count). It is a user from the first day it was
-- identified as one, an install before. Days before today only: today is
-- partial. cohort_size is the cohort's offset-0 count.
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
)
SELECT cohort_day AS "Cohort", actor_kind AS "Identified by", day_offset AS "Day",
       cohort_size AS "Size", actors AS "Returned",
       CASE WHEN cohort_size > 0 THEN actors::DOUBLE / cohort_size ELSE 0 END AS "Retention"
FROM retention
WHERE day_offset BETWEEN 0 AND 45
ORDER BY cohort_day DESC, actor_kind, day_offset
