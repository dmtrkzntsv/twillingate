SELECT CASE WHEN COUNT(DISTINCT d.id) > 0
            THEN ROUND(SUM(d.views + d.events) * 1.0 / COUNT(DISTINCT d.id), 1) ELSE 0 END AS value
FROM v_identity_daily d
WHERE d.project_id = :project AND d.kind = 'user' AND d.id != ''
  AND d.day BETWEEN :from AND :to
