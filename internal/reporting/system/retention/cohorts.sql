SELECT cohort_day AS "Cohort", actor_kind AS "Identified by", day_offset AS "Day",
       cohort_size AS "Size", actors AS "Returned",
       CASE WHEN cohort_size > 0 THEN actors * 1.0 / cohort_size ELSE 0 END AS "Retention"
FROM v_retention
WHERE project_id = :project AND day_offset BETWEEN 0 AND 45
  AND cohort_day BETWEEN :from AND :to
ORDER BY cohort_day DESC, actor_kind, day_offset
