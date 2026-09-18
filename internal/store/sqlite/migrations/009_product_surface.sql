-- Actors first seen through custom events alone now get surface 'product'
-- (retention.go's actorSources). Before this, product_events mapped to
-- 'web', so a product-only project showed a single "web" retention curve.
--
-- Relabel history the way UpsertActors would have labelled it: an actor is
-- product when its first day has product events but no web hits (app
-- actors were never 'web', so they are untouched). A missing web hit only
-- proves something while that day's web rows are still raw: web raw ages
-- out sooner than product raw by default (7 days against 30), and a day in
-- agg_web_daily has had its web rows deleted. An actor whose first day
-- cannot be proven keeps its label.
DROP TABLE IF EXISTS temp.moved_actors;
CREATE TEMP TABLE moved_actors AS
SELECT project, actor_id, first_seen_day
FROM actors
WHERE surface = 'web'
  AND NOT EXISTS (
    SELECT 1 FROM agg_web_daily g
    WHERE g.project = actors.project AND g.day = actors.first_seen_day)
  AND NOT EXISTS (
    SELECT 1 FROM web_hits w
    WHERE w.project = actors.project AND w.actor_id = actors.actor_id
      AND substr(w.ts,1,10) = actors.first_seen_day)
  AND EXISTS (
    SELECT 1 FROM product_events e
    WHERE e.project = actors.project AND e.actor_id = actors.actor_id
      AND substr(e.ts,1,10) = actors.first_seen_day);

UPDATE actors SET surface = 'product'
WHERE EXISTS (
  SELECT 1 FROM moved_actors m
  WHERE m.project = actors.project AND m.actor_id = actors.actor_id);

-- Move the relabelled actors' share of each cohort row from web to product,
-- counted from raw the way AggregateRetentionDay counted it. Rebuilding
-- whole web cohorts instead would drop other web actors whose raw rows are
-- already gone -- history that cannot be reconstructed. A day whose raw
-- rows are gone moves nothing, leaving those actors counted under web.
DROP TABLE IF EXISTS temp.moved_delta;
CREATE TEMP TABLE moved_delta AS
WITH active AS (
  SELECT project, substr(ts,1,10) AS day, actor_id FROM app_views
  WHERE actor_id IN (SELECT actor_id FROM moved_actors)
  UNION
  SELECT project, substr(ts,1,10), actor_id FROM web_hits
  WHERE actor_id IN (SELECT actor_id FROM moved_actors)
  UNION
  SELECT project, substr(ts,1,10), actor_id FROM product_events
  WHERE actor_id IN (SELECT actor_id FROM moved_actors)
)
SELECT m.project, m.first_seen_day AS cohort_day,
       CAST(julianday(a.day) - julianday(m.first_seen_day) AS INTEGER) AS day_offset,
       COUNT(DISTINCT m.actor_id) AS n
FROM moved_actors m
JOIN active a ON a.project = m.project AND a.actor_id = m.actor_id
WHERE a.day >= m.first_seen_day
GROUP BY m.project, m.first_seen_day, a.day;

-- MAX guards a row the pass wrote before all of that day had arrived: the
-- delta, counted from the complete day, can exceed it.
UPDATE agg_retention SET actors = MAX(agg_retention.actors - d.n, 0)
FROM moved_delta d
WHERE agg_retention.surface = 'web'
  AND agg_retention.project = d.project
  AND agg_retention.cohort_day = d.cohort_day
  AND agg_retention.day_offset = d.day_offset;

DELETE FROM agg_retention
WHERE surface = 'web' AND actors = 0
  AND EXISTS (
    SELECT 1 FROM moved_delta d
    WHERE d.project = agg_retention.project AND d.cohort_day = agg_retention.cohort_day
      AND d.day_offset = agg_retention.day_offset);

INSERT OR REPLACE INTO agg_retention (project, surface, cohort_day, day_offset, actors)
SELECT project, 'product', cohort_day, day_offset, n FROM moved_delta;

DROP TABLE temp.moved_delta;
DROP TABLE temp.moved_actors;
