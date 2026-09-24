package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
)

// viewsAggTables and productAggTables must list every agg_* table in the
// schema; TestPruneAggregatesCoversAllAggTables fails if a migration adds
// one that is missing here, since such a table would never honour
// retention.
var viewsAggTables = []string{
	"agg_views_daily", "agg_views_paths", "agg_views_hosts", "agg_views_referrers",
	"agg_views_utm", "agg_views_countries", "agg_views_platforms", "agg_views_os",
	"agg_views_browsers", "agg_views_app_versions", "agg_views_devices", "agg_views_displays",
	"agg_views_consent", "agg_views_locales",
}

var productAggTables = []string{"agg_product_daily", "agg_product_totals", "agg_product_attrs"}

// agg_retention and agg_identity_daily are keyed by cohort_day and day and
// follow the views cutoff. agg_retention is pruned by PruneActors, which
// owns the cohort/actor pair; agg_identity_daily by PruneIdentities.
var identityAggTables = []string{"agg_identity_daily"}

// PruneAggregates drops aggregate rows older than the per-family retention
// cutoffs for one project, atomically across tables.
func (d *DB) PruneAggregates(ctx context.Context, projectID int64, viewsBefore, productBefore civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		del := func(tables []string, before civil.Date) error {
			for _, tbl := range tables {
				if _, err := tx.ExecContext(ctx,
					fmt.Sprintf(`DELETE FROM %s WHERE project_id=? AND day < ?`, tbl),
					projectID, before.String()); err != nil {
					return fmt.Errorf("prune %s: %w", tbl, err)
				}
			}
			return nil
		}
		if err := del(viewsAggTables, viewsBefore); err != nil {
			return err
		}
		return del(productAggTables, productBefore)
	})
}

// IncrementalVacuum reclaims a bounded number of free pages. It is bounded on
// purpose: a full VACUUM would rewrite the whole database, which is not
// affordable on the Raspberry Pi deployment. Relies on the database having
// been created with auto_vacuum=INCREMENTAL (see Open).
func (d *DB) IncrementalVacuum(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `PRAGMA incremental_vacuum(1000)`); err != nil {
		return fmt.Errorf("incremental vacuum: %w", err)
	}
	return nil
}
