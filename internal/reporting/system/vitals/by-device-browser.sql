-- The device type and the browser each page load reported. Samples counts
-- the page loads that reported LCP; each p75 weighs a sample by
-- 1 / sample_rate.
WITH m AS (
  SELECT '$device' AS attr_key, device AS attr_value, event_name, bucket, sample_rate
  FROM measures
  WHERE project_id = :project AND day BETWEEN :from AND :to
    AND event_name IN ('$lcp', '$inp', '$cls') AND device <> ''
  UNION ALL
  SELECT '$browser', browser, event_name, bucket, sample_rate
  FROM measures
  WHERE project_id = :project AND day BETWEEN :from AND :to
    AND event_name IN ('$lcp', '$inp', '$cls') AND browser <> ''
),
h AS (
  SELECT attr_key, attr_value, event_name, bucket,
         CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
         count(*) AS samples, SUM(1 / sample_rate) AS w
  FROM m
  GROUP BY attr_key, attr_value, event_name, bucket
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
