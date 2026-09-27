SELECT COALESCE(SUM(d.views + d.events), 0) AS value
FROM v_identity_daily d
WHERE d.project_id = :project AND d.kind = 'user' AND d.id != ''
  AND d.day BETWEEN :from AND :to
