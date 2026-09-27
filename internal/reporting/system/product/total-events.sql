SELECT
  (SELECT COALESCE(SUM(total_events), 0) FROM v_product_totals
    WHERE project_id = :project
      AND day BETWEEN :from AND :to) AS value,
  (SELECT COALESCE(SUM(total_events), 0) FROM v_product_totals
    WHERE project_id = :project
      AND day BETWEEN date(:from, '-' || (julianday(:to) - julianday(:from) + 1) || ' days')
                      AND date(:from, '-1 day')) AS previous
