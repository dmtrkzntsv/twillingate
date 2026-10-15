SELECT CASE WHEN SUM(CASE WHEN consent IN ('given', 'none') THEN visitors ELSE 0 END) > 0
            THEN SUM(CASE WHEN consent = 'given' THEN visitors ELSE 0 END) * 1.0
                 / SUM(CASE WHEN consent IN ('given', 'none') THEN visitors ELSE 0 END)
       END AS value
FROM v_views_consent
WHERE project_id = :project
  AND day BETWEEN :from AND :to
