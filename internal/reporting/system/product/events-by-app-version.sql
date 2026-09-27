-- $app_version rolls up unconditionally, so this needs no declared
-- attribute. Summed across event names: a day's count is how many product
-- events that version fired, whatever they were.
SELECT day AS x, attr_value AS series, SUM(count) AS y
FROM v_product_attrs
WHERE project_id = :project
  AND day BETWEEN :from AND :to
  AND attr_key = '$app_version'
GROUP BY day, attr_value
ORDER BY day, y DESC
