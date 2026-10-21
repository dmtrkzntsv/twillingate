package sqlite

import (
	"context"
	"fmt"
)

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
