-- $device and $browser are system dimensions, kept in the aggregates for
-- every day. Samples counts the page loads that reported LCP.
WITH h AS (
  SELECT attr_key, attr_value, event_name, bucket, approx_value,
         SUM(samples) AS samples, SUM(weight) AS w
  FROM v_measures_attrs
  WHERE project_id = :project AND day BETWEEN :from AND :to
    AND attr_key IN ('$device', '$browser')
    AND event_name IN ('$lcp', '$inp', '$cls')
  GROUP BY attr_key, attr_value, event_name, bucket, approx_value
),
c AS (
  SELECT attr_key, attr_value, event_name, samples, approx_value,
         SUM(w) OVER (PARTITION BY attr_key, attr_value, event_name ORDER BY bucket) AS run,
         SUM(w) OVER (PARTITION BY attr_key, attr_value, event_name) AS total
  FROM h
),
p AS (
  SELECT attr_key, attr_value, event_name, SUM(samples) AS samples,
         MIN(approx_value) FILTER (WHERE run >= 0.75 * total) AS p75
  FROM c
  GROUP BY attr_key, attr_value, event_name
)
SELECT substr(attr_key, 2) AS "Dimension", attr_value AS "Value",
       COALESCE(MAX(samples) FILTER (WHERE event_name = '$lcp'), 0) AS "Samples",
       ROUND(MAX(p75) FILTER (WHERE event_name = '$lcp')) AS "LCP p75 (ms)",
       ROUND(MAX(p75) FILTER (WHERE event_name = '$inp')) AS "INP p75 (ms)",
       ROUND(MAX(p75) FILTER (WHERE event_name = '$cls'), 3) AS "CLS p75"
FROM p
GROUP BY attr_key, attr_value
ORDER BY 1, 3 DESC, 2
