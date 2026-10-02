-- Reads raw_measures: path is a typed column there, while the aggregates
-- carry $path only when the project declares it. Samples counts the page
-- loads that reported LCP; each p75 weighs a sample by 1 / sample_rate.
WITH h AS (
  SELECT path, event_name, bucket,
         CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
         COUNT(*) AS samples, SUM(1.0 / sample_rate) AS w
  FROM raw_measures
  WHERE project_id = :project AND day BETWEEN :from AND :to
    AND event_name IN ('$lcp', '$inp', '$cls') AND path != ''
  GROUP BY path, event_name, bucket
),
c AS (
  SELECT path, event_name, samples, approx_value,
         SUM(w) OVER (PARTITION BY path, event_name ORDER BY bucket) AS run,
         SUM(w) OVER (PARTITION BY path, event_name) AS total
  FROM h
),
p AS (
  SELECT path, event_name, SUM(samples) AS samples,
         MIN(approx_value) FILTER (WHERE run >= 0.75 * total) AS p75
  FROM c
  GROUP BY path, event_name
)
SELECT path AS "Page",
       COALESCE(MAX(samples) FILTER (WHERE event_name = '$lcp'), 0) AS "Samples",
       ROUND(MAX(p75) FILTER (WHERE event_name = '$lcp')) AS "LCP p75 (ms)",
       ROUND(MAX(p75) FILTER (WHERE event_name = '$inp')) AS "INP p75 (ms)",
       ROUND(MAX(p75) FILTER (WHERE event_name = '$cls'), 3) AS "CLS p75"
FROM p
GROUP BY path
ORDER BY 2 DESC, 1
LIMIT 50
