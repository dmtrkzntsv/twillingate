-- 024: a third family, measures: a name, a number and a time.
-- Spec: docs/superpowers/specs/2026-09-30-performance-measures-design.md,
-- decisions 14-16 and 18.
--
-- A measure is a row of events whose family is 'measures', carrying a
-- numeric value, the kind it was declared as (time in milliseconds, size
-- in bytes, number unitless) and the share of occurrences the client sent.
-- The columns are added in place (no rebuild): every other family stores
-- NULL, '' and 1. As everywhere else there are no CHECK constraints:
-- ingest validates.
ALTER TABLE events ADD COLUMN value REAL;
ALTER TABLE events ADD COLUMN measure TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN sample_rate REAL NOT NULL DEFAULT 1;
-- Log-scale bucket, base 1.04 (about ±2% relative precision). -1000 is the
-- zero bucket: below every real bucket, and not NULL because the aggregate
-- tables key on it.
ALTER TABLE events ADD COLUMN bucket INTEGER GENERATED ALWAYS AS
  (CASE WHEN value IS NULL THEN NULL
        WHEN value > 1e-17 THEN CAST(ceil(log(value) / log(1.04)) AS INTEGER)
        ELSE -1000 END) VIRTUAL;

CREATE VIEW raw_measures AS SELECT * FROM events WHERE family = 'measures';

-- Histograms: one row per bucket with data. samples counts rows received,
-- weight sums 1/sample_rate (the estimated true count) and sum sums
-- value/sample_rate (for an exact mean). Rows add up across days and
-- dimensions, so percentiles over any range stay exact to the bucket.
CREATE TABLE agg_measures_daily (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    event_name TEXT NOT NULL, measure TEXT NOT NULL,
    bucket INTEGER NOT NULL,
    samples INTEGER NOT NULL, weight REAL NOT NULL, sum REAL NOT NULL,
    PRIMARY KEY (project_id, day, event_name, measure, bucket)
) WITHOUT ROWID;

CREATE TABLE agg_measures_attrs (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    event_name TEXT NOT NULL, measure TEXT NOT NULL,
    attr_key TEXT NOT NULL, attr_value TEXT NOT NULL,
    bucket INTEGER NOT NULL,
    samples INTEGER NOT NULL, weight REAL NOT NULL, sum REAL NOT NULL,
    PRIMARY KEY (project_id, day, event_name, measure, attr_key, attr_value, bucket)
) WITHOUT ROWID;

-- approx_value is the value with the smallest relative error for the
-- bucket, 2 * 1.04^bucket / 2.04, and 0 for the zero bucket, so SQL over
-- these views never needs the bucket formula.
CREATE VIEW v_measures_daily AS
SELECT project_id, day, event_name, measure, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
       samples, weight, sum
FROM agg_measures_daily
UNION ALL
SELECT project_id, day, event_name, measure, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END,
       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
FROM raw_measures
GROUP BY project_id, day, event_name, measure, bucket;

-- v_measures_attrs: v_product_attrs (023) over raw_measures, with the same
-- keys (store.SystemAttributes always, plus the project's declared keys)
-- and the same cap (product_attributes_top_n values per key per metric
-- per day, ranked by samples then value, the rest in '(other)'). measure
-- and bucket are carried through, and there are no unique-user or
-- unique-group columns: they do not add up across buckets.
CREATE VIEW v_measures_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta
                   WHERE key='product_attributes_top_n'
                     AND CAST(value AS INTEGER) > 0), 50) AS n
),
declared AS (
  SELECT DISTINCT p.id AS project_id, j.value AS attr_key
  FROM projects p,
       json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
  WHERE j.type = 'text'
),
declared_vals AS (
  SELECT e.project_id AS project_id, e.day AS day,
         e.event_name AS event_name, e.measure AS measure, d.attr_key AS attr_key,
         CASE d.attr_key
           WHEN '$host'            THEN NULLIF(e.host, '')
           WHEN '$path'            THEN NULLIF(e.path, '')
           WHEN '$referrer'        THEN NULLIF(e.referrer_source, '')
           WHEN '$utm_source'      THEN NULLIF(e.utm_source, '')
           WHEN '$utm_medium'      THEN NULLIF(e.utm_medium, '')
           WHEN '$utm_campaign'    THEN NULLIF(e.utm_campaign, '')
           WHEN '$os_version'      THEN NULLIF(e.os_version, '')
           WHEN '$browser_version' THEN NULLIF(e.browser_version, '')
           WHEN '$device_model'    THEN NULLIF(e.device_model, '')
           ELSE CASE WHEN d.attr_key LIKE '$%' THEN NULL
                     ELSE json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') END
         END AS attr_value,
         e.bucket AS bucket, e.sample_rate AS sample_rate, e.value AS value
  FROM raw_measures e
  JOIN declared d ON d.project_id = e.project_id
),
vals AS (
  SELECT project_id, day, event_name, measure, attr_key, attr_value, bucket, sample_rate, value
  FROM declared_vals WHERE attr_value IS NOT NULL
  UNION ALL
  SELECT project_id, day, event_name, measure, '$platform', platform, bucket, sample_rate, value FROM raw_measures WHERE platform <> ''
  UNION ALL
  SELECT project_id, day, event_name, measure, '$os', os, bucket, sample_rate, value FROM raw_measures WHERE os <> ''
  UNION ALL
  SELECT project_id, day, event_name, measure, '$app_version', app_version, bucket, sample_rate, value FROM raw_measures WHERE app_version <> ''
  UNION ALL
  SELECT project_id, day, event_name, measure, '$app_locale', app_locale, bucket, sample_rate, value FROM raw_measures WHERE app_locale <> ''
  UNION ALL
  SELECT project_id, day, event_name, measure, '$kind', kind, bucket, sample_rate, value FROM raw_measures WHERE kind <> ''
  UNION ALL
  SELECT project_id, day, event_name, measure, '$browser', browser, bucket, sample_rate, value FROM raw_measures WHERE browser <> ''
  UNION ALL
  SELECT project_id, day, event_name, measure, '$device', device, bucket, sample_rate, value FROM raw_measures WHERE device <> ''
  UNION ALL
  SELECT project_id, day, event_name, measure, '$browser_locale', browser_locale, bucket, sample_rate, value FROM raw_measures WHERE browser_locale <> ''
),
ranked AS (
  SELECT project_id, day, event_name, measure, attr_key, attr_value,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, measure, attr_key
                            ORDER BY COUNT(*) DESC, attr_value) AS rn
  FROM vals
  GROUP BY project_id, day, event_name, measure, attr_key, attr_value
),
capped AS (
  SELECT v.project_id, v.day, v.event_name, v.measure, v.attr_key,
         CASE WHEN r.rn <= (SELECT n FROM cap) THEN v.attr_value ELSE '(other)' END AS attr_value,
         v.bucket, v.sample_rate, v.value
  FROM vals v
  JOIN ranked r ON r.project_id = v.project_id AND r.day = v.day
    AND r.event_name = v.event_name AND r.measure = v.measure
    AND r.attr_key = v.attr_key AND r.attr_value = v.attr_value
)
SELECT project_id, day, event_name, measure, attr_key, attr_value, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
       samples, weight, sum
FROM agg_measures_attrs
UNION ALL
SELECT project_id, day, event_name, measure, attr_key, attr_value, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END,
       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
FROM capped
GROUP BY project_id, day, event_name, measure, attr_key, attr_value, bucket;

-- Base shape only, the 023 columns plus the three measure columns;
-- RebuildFlatView (flatViewBaseColumns) builds the same text for no
-- declared keys, and replaces it with the declared attr_ columns on the
-- next boot or registry write.
DROP VIEW IF EXISTS v_events_flat;
CREATE VIEW v_events_flat AS SELECT id, project_id, family, event_name, actor_id, kind, session_id, user_id, group_id, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, ts, attributes, value, measure, sample_rate FROM events;
