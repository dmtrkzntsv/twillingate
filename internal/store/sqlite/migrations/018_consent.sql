-- 018: whether the client had consent to keep anything on the device.
-- Spec: docs/superpowers/specs/2026-09-23-consent-attribute-design.md
--
-- 1, 0, or NULL for unknown. NULL is every row stored before this
-- migration and every row a client sends without $consent; there is
-- nothing to backfill from, and 0 would claim a refusal that was never
-- recorded. The accepted spellings are validated in Go at ingest; the
-- database carries no copy of them.
ALTER TABLE views  ADD COLUMN consent INTEGER;
ALTER TABLE events ADD COLUMN consent INTEGER;

-- Daily rollup, modelled on agg_views_platforms. consent here is the
-- breakdown value ('given', 'none', 'unknown'), not the raw flag.
CREATE TABLE agg_views_consent (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, consent TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, consent)
) WITHOUT ROWID;

-- Seeded from the daily totals as 'unknown', the way 015 seeded
-- platforms, so a range covering rolled-up days shows a full 'unknown'
-- bar rather than nothing. Summing visitors across kinds over-counts an
-- actor seen on two kinds in one day; 015 accepted the same.
INSERT INTO agg_views_consent (project_id, day, consent, visitors, views)
SELECT project_id, day, 'unknown', SUM(visitors), SUM(views)
FROM agg_views_daily GROUP BY project_id, day;

-- Three values need no top-500 cap, so this is the plain
-- aggregate-plus-live-half shape. The CASE is consentSQL in
-- aggregate_views.go; TestStitchViewConsentAcrossBoundary keeps them equal.
-- Aliased "v" like every other dimension view's live half, not for a join
-- here but so EXPLAIN QUERY PLAN names it "v" and
-- TestViewsLiveHalvesUseTheDayIndex can recognise the day-bounded search.
CREATE VIEW v_views_consent AS
SELECT project_id, day, consent, visitors, views FROM agg_views_consent
UNION ALL
SELECT project_id, day,
       CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM views v
GROUP BY project_id, day, CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END;
