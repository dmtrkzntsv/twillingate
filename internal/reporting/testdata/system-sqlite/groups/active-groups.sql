SELECT COUNT(DISTINCT d.id) AS value
FROM v_identity_daily d
WHERE d.project_id = :project AND d.kind = 'group' AND d.id != ''
  AND d.day BETWEEN :from AND :to
