-- 042: events live only in the event files (raw-first PR 6). SQLite keeps
-- metadata, rules, forms and dashboards; the raw rows, the rollups, the
-- views over both and the attribute catalog go. It cannot be undone.
--
-- The migrator refuses this migration before it starts while events holds
-- rows that `twillingate events import` has not copied into the files
-- (meta events_imported unset; preChecks in migrate.go), so nothing is
-- dropped that only SQLite holds. The process-owned flat view is dropped
-- by the migrator before every migration, so it is not named here.
DROP VIEW v_views_daily;
DROP VIEW v_views_paths;
DROP VIEW v_views_hosts;
DROP VIEW v_views_referrers;
DROP VIEW v_views_utm;
DROP VIEW v_views_countries;
DROP VIEW v_views_platforms;
DROP VIEW v_views_os;
DROP VIEW v_views_browsers;
DROP VIEW v_views_app_versions;
DROP VIEW v_views_devices;
DROP VIEW v_views_displays;
DROP VIEW v_views_consent;
DROP VIEW v_views_locales;
DROP VIEW v_product_daily;
DROP VIEW v_product_totals;
DROP VIEW v_product_attrs;
DROP VIEW v_measures_daily;
DROP VIEW v_measures_attrs;
DROP VIEW v_identity_daily;
DROP VIEW v_retention;

DROP VIEW raw_views;
DROP VIEW raw_product;
DROP VIEW raw_measures;

DROP TABLE agg_views_daily;
DROP TABLE agg_views_paths;
DROP TABLE agg_views_hosts;
DROP TABLE agg_views_referrers;
DROP TABLE agg_views_utm;
DROP TABLE agg_views_countries;
DROP TABLE agg_views_platforms;
DROP TABLE agg_views_os;
DROP TABLE agg_views_browsers;
DROP TABLE agg_views_app_versions;
DROP TABLE agg_views_devices;
DROP TABLE agg_views_displays;
DROP TABLE agg_views_consent;
DROP TABLE agg_views_locales;
DROP TABLE agg_product_daily;
DROP TABLE agg_product_totals;
DROP TABLE agg_product_attrs;
DROP TABLE agg_measures_daily;
DROP TABLE agg_measures_attrs;
DROP TABLE agg_retention;
DROP TABLE agg_identity_daily;

DROP TABLE actors;
DROP TABLE identities;
DROP TABLE events;
DROP TABLE usage_attributes;
DROP TABLE project_attributes;

-- The caps the views' live halves read, written at every start until now.
DELETE FROM meta WHERE key IN ('attributes_top_n', 'attribute_breakdowns_max', 'identities_top_n');
-- The stats of the rows and rollups just dropped; the database's size and
-- each project's file bytes stay.
DELETE FROM usage_history WHERE key NOT IN ('database_bytes', 'file_bytes');
