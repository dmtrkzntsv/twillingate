-- Actors first seen through custom events alone now get surface 'product'
-- (retention.go's actorSources). Before this, product_events mapped to
-- 'web', so a product-only project showed a single "web" retention curve.
--
-- Relabel history the way UpsertActors would have labelled it: an actor is
-- product when its first day has product events but no web hits (app
-- actors were never 'web', so they are untouched). An actor whose
-- first-day raw rows have already been pruned keeps its label -- nothing is
-- left to prove otherwise.
UPDATE actors SET surface = 'product'
WHERE surface = 'web'
  AND NOT EXISTS (
    SELECT 1 FROM web_hits w
    WHERE w.project = actors.project AND w.actor_id = actors.actor_id
      AND substr(w.ts,1,10) = actors.first_seen_day)
  AND EXISTS (
    SELECT 1 FROM product_events e
    WHERE e.project = actors.project AND e.actor_id = actors.actor_id
      AND substr(e.ts,1,10) = actors.first_seen_day);

-- The cohorts those actors sit in were counted under 'web'. Drop those rows
-- and recompute both surfaces for the touched cohorts from raw, exactly as
-- AggregateRetentionDay does one day at a time. No 'product' actor existed
-- before this migration, so the set below is precisely the relabelled one.
DELETE FROM agg_retention
WHERE surface = 'web'
  AND (project, cohort_day) IN (
    SELECT DISTINCT project, first_seen_day FROM actors WHERE surface = 'product');

INSERT OR REPLACE INTO agg_retention (project, surface, cohort_day, day_offset, actors)
WITH active AS (
  SELECT project, substr(ts,1,10) AS day, actor_id FROM app_views      WHERE actor_id <> ''
  UNION
  SELECT project, substr(ts,1,10),        actor_id FROM web_hits       WHERE actor_id <> ''
  UNION
  SELECT project, substr(ts,1,10),        actor_id FROM product_events WHERE actor_id <> ''
)
SELECT a.project, a.surface, a.first_seen_day,
       CAST(julianday(active.day) - julianday(a.first_seen_day) AS INTEGER),
       COUNT(DISTINCT a.actor_id)
FROM actors a
JOIN active ON active.project = a.project AND active.actor_id = a.actor_id
WHERE a.surface IN ('web', 'product')
  AND active.day >= a.first_seen_day
  AND (a.project, a.first_seen_day) IN (
    SELECT DISTINCT project, first_seen_day FROM actors WHERE surface = 'product')
GROUP BY a.project, a.surface, a.first_seen_day, active.day;
