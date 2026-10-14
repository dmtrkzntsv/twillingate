-- Design C's data: SQLite's events (base.db) copied into a DuckDB file with
-- typed columns and one column per attribute key (D4), in ts order as
-- appends would leave it.
--
--	duckdb events.duckdb < load_duckdb.sql
--
-- Then the monthly Parquet files (Q9's alternative), from the DuckDB file:
--
--	duckdb events.duckdb -c "COPY (SELECT *, strftime(day, '%Y-%m') AS month FROM events ORDER BY ts)
--	  TO 'parquet' (FORMAT parquet, PARTITION_BY (project_id, family, month), COMPRESSION zstd)"

ATTACH 'base.db' AS s (TYPE sqlite, READ_ONLY);

CREATE TABLE events AS
SELECT family, project_id, CAST(day AS DATE) AS day, CAST(id AS UUID) AS id, event_name,
       strptime(ts, '%Y-%m-%dT%H:%M:%SZ') AS ts,
       strptime(received_at, '%Y-%m-%dT%H:%M:%SZ') AS received_at,
       kind, actor_id, actor_kind, user_id, group_id, session_id,
       host, path, referrer_source, utm_source, utm_medium, utm_campaign,
       platform, os, os_version, os_name, browser, browser_version, browser_locale,
       app_version, app_locale, device, device_model,
       CAST(display_width AS INTEGER) AS display_width, CAST(display_height AS INTEGER) AS display_height,
       country, CAST(consent AS TINYINT) AS consent, value, measure, sample_rate,
       attributes ->> '$.access_state'        AS attr_access_state,
       attributes ->> '$.deployment'          AS attr_deployment,
       attributes ->> '$.auth_apple'          AS attr_auth_apple,
       attributes ->> '$.auth_google'         AS attr_auth_google,
       attributes ->> '$.auth_password'       AS attr_auth_password,
       attributes ->> '$.auth_sso'            AS attr_auth_sso,
       attributes ->> '$.locale'              AS attr_locale,
       attributes ->> '$.mode'                AS attr_mode,
       attributes ->> '$.signup_year'         AS attr_signup_year,
       attributes ->> '$.signup_month'        AS attr_signup_month,
       attributes ->> '$.payees'              AS attr_payees,
       attributes ->> '$.tags'                AS attr_tags,
       attributes ->> '$.payees_archived'     AS attr_payees_archived,
       attributes ->> '$.tags_archived'       AS attr_tags_archived,
       attributes ->> '$.categories_archived' AS attr_categories_archived,
       attributes ->> '$.connections'         AS attr_connections,
       attributes ->> '$.accounts'            AS attr_accounts,
       attributes ->> '$.categories'          AS attr_categories,
       attributes ->> '$.months_since_signup' AS attr_months_since_signup,
       attributes ->> '$.current_url'         AS attr_current_url,
       attributes ->> '$.host'                AS attr_host
FROM s.events
ORDER BY ts;

CHECKPOINT;
