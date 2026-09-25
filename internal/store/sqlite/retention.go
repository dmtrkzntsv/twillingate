package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
)

// actorSources are the two family views over the one raw table
// (020_one_events_table.sql) an actor can appear in. Both carry actor_kind
// since 012, so the family no longer implies anything about the
// population; only the kind does.
var actorSources = []string{rawViews, rawProduct}

// cohortKinds are the actor kinds stable enough to cohort. A connection
// hash rotates with the salt (and a pre-012 product row carries an empty
// kind), so recording those only ever produced an offset-0 row.
const cohortKinds = `('user', 'install')`

// UpsertActors records first/last seen for every user- or install-identified
// actor active on the given day. On conflict, a user identification always
// wins over an install one — once a human is known as a user that is the
// truer, stable description of the same literal actor_id (e.g. an install id
// later reused as the login $user_id) — otherwise the incoming kind applies.
// Must run before AggregateRetentionDay for the same day, and before that
// day's raw rows are deleted.
func (d *DB) UpsertActors(ctx context.Context, projectID int64, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, source := range actorSources {
			q := fmt.Sprintf(`
INSERT INTO actors (project_id, actor_id, actor_kind, first_seen_day, last_seen_day)
SELECT ?, actor_id, actor_kind, ?, ?
FROM %[1]s WHERE project_id=? AND day=? AND actor_id <> '' AND actor_kind IN %[2]s
GROUP BY actor_id, actor_kind
ON CONFLICT(project_id, actor_id) DO UPDATE SET
  actor_kind     = CASE WHEN actors.actor_kind = 'user' OR excluded.actor_kind = 'user'
                        THEN 'user' ELSE excluded.actor_kind END,
  first_seen_day = MIN(actors.first_seen_day, excluded.first_seen_day),
  last_seen_day  = MAX(actors.last_seen_day,  excluded.last_seen_day)`, source, cohortKinds)
			if _, err := tx.ExecContext(ctx, q,
				projectID, day.String(), day.String(), projectID, day.String()); err != nil {
				return fmt.Errorf("upsert actors from %s: %w", source, err)
			}
		}
		return nil
	})
}

// AggregateRetentionDay computes every (cohort, offset) pair that day D owns.
//
// Each pair is produced by exactly one day — D = cohort + offset — so
// INSERT OR REPLACE is a full recompute of precisely the rows that day owns,
// and re-running a day is safe, like every other aggregate here. Because the
// computation reads day D's raw rows while they still exist, no per-day
// activity history has to be stored.
//
// Cohorts are built for every project now. What keeps a daily-rotating hash
// out of them is the kind filter, not the caller: UpsertActors only records
// actor_kind IN ('user', 'install') (cohortKinds), so a connection-hash
// actor — whose first_seen_day would always equal D, making every cohort
// hold nothing but offset 0 — is never cohorted. Migration 017 re-kinded the
// hashed user/install rows a formerly anonymous project had already
// received to connection for the same reason.
func (d *DB) AggregateRetentionDay(ctx context.Context, projectID int64, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO agg_retention (project_id, actor_kind, cohort_day, day_offset, actors)
WITH active AS (
  SELECT DISTINCT actor_id FROM raw_views   WHERE project_id=? AND day=? AND actor_id <> ''
  UNION
  SELECT DISTINCT actor_id FROM raw_product WHERE project_id=? AND day=? AND actor_id <> ''
)
SELECT a.project_id, a.actor_kind, a.first_seen_day,
       CAST(julianday(?) - julianday(a.first_seen_day) AS INTEGER),
       COUNT(DISTINCT a.actor_id)
FROM actors a JOIN active ON active.actor_id = a.actor_id
WHERE a.project_id=?
GROUP BY a.project_id, a.actor_kind, a.first_seen_day`,
			projectID, day.String(), projectID, day.String(),
			day.String(), projectID); err != nil {
			return fmt.Errorf("agg_retention: %w", err)
		}
		return nil
	})
}

// PruneActors evicts actors last seen outside the aggregate window, along
// with cohort rows whose cohort day has aged out. This eviction is what keeps
// actors bounded by yearly-active count rather than all-time — it is the only
// table in the system not bounded by a day window, which matters on the
// SD-card hardware target.
//
// The trade is that someone returning after the window counts as a new actor,
// so cohort figures are approximate at the long tail.
func (d *DB) PruneActors(ctx context.Context, projectID int64, before civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM actors WHERE project_id=? AND last_seen_day < ?`,
			projectID, before.String()); err != nil {
			return fmt.Errorf("prune actors: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agg_retention WHERE project_id=? AND cohort_day < ?`,
			projectID, before.String()); err != nil {
			return fmt.Errorf("prune agg_retention: %w", err)
		}
		return nil
	})
}
