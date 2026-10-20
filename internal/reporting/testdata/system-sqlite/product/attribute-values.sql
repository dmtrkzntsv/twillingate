-- The widget's figures from SQLite's raw rows rather than v_product_attrs,
-- whose users and groups are distinct per (day, event) and cannot be made
-- distinct over the range: one row per (attribute, value), Count the
-- events carrying it, Users and Groups distinct over the range. Only the
-- project's own attributes: the `$` keys are typed columns on the files.
SELECT j.key AS "Attribute", j.value AS "Value", COUNT(*) AS "Count",
       COUNT(DISTINCT e.actor_id) AS "Users", COUNT(DISTINCT NULLIF(e.group_id, '')) AS "Groups"
FROM raw_product e, json_each(e.attributes) j
WHERE e.project_id = :project
  AND e.day BETWEEN :from AND :to
  AND j.key NOT LIKE '$%'
GROUP BY j.key, j.value
ORDER BY "Attribute", "Count" DESC, "Value"
