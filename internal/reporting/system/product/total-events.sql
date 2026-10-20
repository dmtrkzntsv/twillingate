-- Product events in the range; previous is the same count over as many
-- days just before :from.
SELECT count(*) FILTER (WHERE day >= :from) AS value,
       count(*) FILTER (WHERE day < :from) AS previous
FROM product
WHERE project_id = :project
  AND day BETWEEN :from - CAST(:to - :from + 1 AS INTEGER) AND :to
