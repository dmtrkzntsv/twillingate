-- Reads sessions, the views split into sessions by the daily pass (and
-- for the days it has not reached yet, as the query runs): a session of
-- one view is a bounce.
SELECT CASE WHEN count(*) > 0 THEN count(*) FILTER (WHERE views = 1)::DOUBLE / count(*) ELSE 0 END AS value
FROM sessions
WHERE project_id = :project AND day BETWEEN :from AND :to
