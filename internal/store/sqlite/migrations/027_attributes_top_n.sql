-- 027: PRODUCT_ATTRIBUTES_TOP_N is now ATTRIBUTES_TOP_N (it caps measures'
-- attributes as well as product events'), and its meta row follows, from
-- product_attributes_top_n to attributes_top_n. The row moves with its
-- value, which the start writes again from the setting anyway, and the two
-- views reading it are recreated as 025 left them but for the key.

INSERT OR REPLACE INTO meta (key, value)
SELECT 'attributes_top_n', value FROM meta WHERE key = 'product_attributes_top_n';
DELETE FROM meta WHERE key = 'product_attributes_top_n';

DROP VIEW v_product_attrs;
CREATE VIEW v_product_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups
FROM agg_product_attrs
UNION ALL
SELECT project_id, day, event_name, attr_key,
       CASE WHEN rn <= (SELECT n FROM cap) THEN attr_value ELSE '(other)' END,
       CASE WHEN rn <= (SELECT n FROM cap) THEN c ELSE tail_c END,
       CASE WHEN rn <= (SELECT n FROM cap) THEN u
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(tail_actors)) END,
       CASE WHEN rn <= (SELECT n FROM cap) THEN g
            ELSE (SELECT COUNT(DISTINCT value) FROM json_each(tail_groups)) END
FROM (
  -- Whole-partition aggregates over the values past the cap; NULL where
  -- there are none. Each array loses its brackets, is joined and wrapped
  -- again: one array of every tail row's actor (or group, null for none).
  SELECT project_id, day, event_name, attr_key, attr_value, c, u, g, rn,
         SUM(c) FILTER (WHERE rn > (SELECT n FROM cap)) OVER w AS tail_c,
         '[' || group_concat(substr(actors, 2, length(actors) - 2), ',')
                FILTER (WHERE rn > (SELECT n FROM cap)) OVER w || ']' AS tail_actors,
         '[' || group_concat(substr(groups, 2, length(groups) - 2), ',')
                FILTER (WHERE rn > (SELECT n FROM cap)) OVER w || ']' AS tail_groups
  FROM (
    SELECT project_id, day, event_name, attr_key, attr_value,
           COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
           COUNT(DISTINCT NULLIF(group_id, '')) AS g,
           json_group_array(actor_id) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS actors,
           json_group_array(NULLIF(group_id, '')) FILTER (WHERE (SELECT n FROM cap) < 4611686018427387904) AS groups,
           ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, attr_key
                              ORDER BY COUNT(*) DESC, attr_value) AS rn
    FROM (
      SELECT e.project_id AS project_id, e.day AS day, e.event_name AS event_name,
             e.actor_id AS actor_id, e.group_id AS group_id, k.attr_key AS attr_key,
             CASE k.attr_key
               WHEN '$platform'        THEN NULLIF(e.platform, '')
               WHEN '$os'              THEN NULLIF(e.os, '')
               WHEN '$app_version'     THEN NULLIF(e.app_version, '')
               WHEN '$app_locale'      THEN NULLIF(e.app_locale, '')
               WHEN '$kind'            THEN NULLIF(e.kind, '')
               WHEN '$browser'         THEN NULLIF(e.browser, '')
               WHEN '$device'          THEN NULLIF(e.device, '')
               WHEN '$browser_locale'  THEN NULLIF(e.browser_locale, '')
               WHEN '$host'            THEN NULLIF(e.host, '')
               WHEN '$path'            THEN NULLIF(e.path, '')
               WHEN '$referrer'        THEN NULLIF(e.referrer_source, '')
               WHEN '$utm_source'      THEN NULLIF(e.utm_source, '')
               WHEN '$utm_medium'      THEN NULLIF(e.utm_medium, '')
               WHEN '$utm_campaign'    THEN NULLIF(e.utm_campaign, '')
               WHEN '$os_version'      THEN NULLIF(e.os_version, '')
               WHEN '$browser_version' THEN NULLIF(e.browser_version, '')
               WHEN '$device_model'    THEN NULLIF(e.device_model, '')
               ELSE CASE WHEN k.attr_key LIKE '$%' THEN NULL
                         ELSE json_extract(e.attributes, '$."' || replace(k.attr_key,'"','\"') || '"') END
             END AS attr_value
      FROM raw_product e
      -- Each project's keys: what it declares, and every system key. UNION
      -- drops a system key a project also declares (manage refuses that
      -- today; older registries may hold one).
      JOIN (
        SELECT p.id AS project_id, j.value AS attr_key
        FROM projects p,
             json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
        WHERE j.type = 'text'
        UNION
        SELECT p.id, s.column1
        FROM projects p,
             (VALUES ('$platform'), ('$os'), ('$app_version'), ('$app_locale'),
                     ('$kind'), ('$browser'), ('$device'), ('$browser_locale')) s
      ) k ON k.project_id = e.project_id
    )
    WHERE attr_value IS NOT NULL
    GROUP BY project_id, day, event_name, attr_key, attr_value
  )
  WINDOW w AS (PARTITION BY project_id, day, event_name, attr_key)
)
WHERE rn <= (SELECT n FROM cap) + 1;

DROP VIEW v_measures_attrs;
CREATE VIEW v_measures_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
                                ELSE CAST(value AS INTEGER) END
                   FROM meta WHERE key='attributes_top_n'
                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
)
SELECT project_id, day, event_name, measure, attr_key, attr_value, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
       samples, weight, sum
FROM agg_measures_attrs
UNION ALL
SELECT project_id, day, event_name, measure, attr_key, kept, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END,
       samples, weight, sum
FROM (
  SELECT project_id, day, event_name, measure, attr_key, kept, bucket,
         SUM(samples) OVER f AS samples, SUM(weight) OVER f AS weight,
         SUM(sum) OVER f AS sum, ROW_NUMBER() OVER f AS rf
  FROM (
    SELECT project_id, day, event_name, measure, attr_key, bucket, samples, weight, sum,
           CASE WHEN DENSE_RANK() OVER (PARTITION BY project_id, day, event_name, measure, attr_key
                                        ORDER BY value_samples DESC, attr_value) <= (SELECT n FROM cap)
                THEN attr_value ELSE '(other)' END AS kept
    FROM (
      SELECT project_id, day, event_name, measure, attr_key, attr_value, bucket,
             COUNT(*) AS samples, SUM(1.0 / sample_rate) AS weight, SUM(value / sample_rate) AS sum,
             SUM(COUNT(*)) OVER (PARTITION BY project_id, day, event_name, measure, attr_key, attr_value)
               AS value_samples
      FROM (
        SELECT e.project_id AS project_id, e.day AS day, e.event_name AS event_name,
               e.measure AS measure, e.bucket AS bucket, e.sample_rate AS sample_rate,
               e.value AS value, k.attr_key AS attr_key,
               CASE k.attr_key
                 WHEN '$platform'        THEN NULLIF(e.platform, '')
                 WHEN '$os'              THEN NULLIF(e.os, '')
                 WHEN '$app_version'     THEN NULLIF(e.app_version, '')
                 WHEN '$app_locale'      THEN NULLIF(e.app_locale, '')
                 WHEN '$kind'            THEN NULLIF(e.kind, '')
                 WHEN '$browser'         THEN NULLIF(e.browser, '')
                 WHEN '$device'          THEN NULLIF(e.device, '')
                 WHEN '$browser_locale'  THEN NULLIF(e.browser_locale, '')
                 WHEN '$host'            THEN NULLIF(e.host, '')
                 WHEN '$path'            THEN NULLIF(e.path, '')
                 WHEN '$referrer'        THEN NULLIF(e.referrer_source, '')
                 WHEN '$utm_source'      THEN NULLIF(e.utm_source, '')
                 WHEN '$utm_medium'      THEN NULLIF(e.utm_medium, '')
                 WHEN '$utm_campaign'    THEN NULLIF(e.utm_campaign, '')
                 WHEN '$os_version'      THEN NULLIF(e.os_version, '')
                 WHEN '$browser_version' THEN NULLIF(e.browser_version, '')
                 WHEN '$device_model'    THEN NULLIF(e.device_model, '')
                 ELSE CASE WHEN k.attr_key LIKE '$%' THEN NULL
                           ELSE json_extract(e.attributes, '$."' || replace(k.attr_key,'"','\"') || '"') END
               END AS attr_value
        FROM raw_measures e
        JOIN (
          SELECT p.id AS project_id, j.value AS attr_key
          FROM projects p,
               json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
          WHERE j.type = 'text'
          UNION
          SELECT p.id, s.column1
          FROM projects p,
               (VALUES ('$platform'), ('$os'), ('$app_version'), ('$app_locale'),
                       ('$kind'), ('$browser'), ('$device'), ('$browser_locale')) s
        ) k ON k.project_id = e.project_id
      )
      WHERE attr_value IS NOT NULL
      GROUP BY project_id, day, event_name, measure, attr_key, attr_value, bucket
    )
  )
  WINDOW f AS (PARTITION BY project_id, day, event_name, measure, attr_key, kept, bucket)
)
WHERE rf = 1;
