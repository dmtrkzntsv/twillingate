-- Reads sessions, the views split into sessions by the daily pass (and
-- for the days it has not reached yet, as the query runs): a session lasts
-- from its first view to its last.
SELECT day AS x,
       CASE WHEN count(*) > 0 THEN SUM(duration_sec)::DOUBLE / count(*) ELSE 0 END AS y
FROM sessions
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY day
ORDER BY day
