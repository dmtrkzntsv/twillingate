package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
)

// identityKinds are the two dimensions agg_identity_daily rolls up, paired
// with the raw column each reads.
var identityKinds = []struct{ kind, column string }{
	{"user", "user_id"},
	{"group", "group_id"},
}

// AggregateIdentityDay rolls one day's activity per user and per group, then
// refreshes identities.last_seen_day so display-name rows age out alongside
// their subjects.
//
// users counts distinct users active in a group that day; for kind='user' it
// is always 1, since the row already is one user.
//
// Must run before AggregateViewDay and AggregateProductDay for the same day,
// which delete the raw rows this reads.
func (d *DB) AggregateIdentityDay(ctx context.Context, projectID int64, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, k := range identityKinds {
			q := fmt.Sprintf(`
INSERT OR REPLACE INTO agg_identity_daily
	(project_id, day, kind, id, actors, users, views, events)
WITH src AS (
  SELECT %[1]s AS id, actor_id, user_id, 1 AS is_view, 0 AS is_event
  FROM raw_views   WHERE project_id=? AND day=? AND %[1]s <> ''
  UNION ALL
  SELECT %[1]s, actor_id, user_id, 0, 1
  FROM raw_product WHERE project_id=? AND day=? AND %[1]s <> ''
),
ranked AS (
  SELECT id,
         COUNT(DISTINCT actor_id) AS actors,
         COUNT(DISTINCT NULLIF(user_id, '')) AS users,
         SUM(is_view) AS views, SUM(is_event) AS events,
         ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, id) AS rn
  FROM src GROUP BY id
)
SELECT ?, ?, ?, id, actors,
       CASE WHEN ? = 'user' THEN 1 ELSE users END,
       views, events
FROM ranked WHERE rn <= %[2]d`, k.column, topNDimension)

			if _, err := tx.ExecContext(ctx, q,
				projectID, day.String(), projectID, day.String(),
				projectID, day.String(), k.kind, k.kind); err != nil {
				return fmt.Errorf("agg_identity_daily %s: %w", k.kind, err)
			}

			if _, err := tx.ExecContext(ctx, `
UPDATE identities SET last_seen_day=?
WHERE project_id=? AND kind=? AND id IN (
  SELECT id FROM agg_identity_daily WHERE project_id=? AND day=? AND kind=?
)`, day.String(), projectID, k.kind, projectID, day.String(), k.kind); err != nil {
				return fmt.Errorf("identities last_seen %s: %w", k.kind, err)
			}
		}
		return nil
	})
}

// PruneIdentities drops display names and identity aggregates that have aged
// out, on the same window as actors. Names are PII, so letting them outlive
// the aggregates they label would be the wrong default.
func (d *DB) PruneIdentities(ctx context.Context, projectID int64, before civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM identities WHERE project_id=? AND last_seen_day <> '' AND last_seen_day < ?`,
			projectID, before.String()); err != nil {
			return fmt.Errorf("prune identities: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agg_identity_daily WHERE project_id=? AND day < ?`,
			projectID, before.String()); err != nil {
			return fmt.Errorf("prune agg_identity_daily: %w", err)
		}
		return nil
	})
}
