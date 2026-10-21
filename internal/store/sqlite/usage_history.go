package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// RecordUsageHistory writes now's UTC day of usage_history in one
// transaction, so a reader sees the old values or the new, never a mix:
// the database file's size (project 0), and, when fileBytes is non-nil,
// each project's event file bytes, a project left out of it having no
// files that day. A second run the same day (the pass also runs at start)
// replaces what it writes of the day; earlier days stay. A nil fileBytes
// (a process that keeps no event files: the console of a split install)
// leaves the day's file_bytes as the other process wrote them.
func (d *DB) RecordUsageHistory(ctx context.Context, now time.Time, fileBytes map[int64]int64) error {
	day := civil.DateOf(now).String()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var size int64
		if err := tx.QueryRowContext(ctx, `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&size); err != nil {
			return fmt.Errorf("database size: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO usage_history (key, project_id, measured_at, value) VALUES (?, 0, ?, ?)`,
			store.StatDatabaseBytes, day, size); err != nil {
			return fmt.Errorf("database size: %w", err)
		}
		if fileBytes == nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM usage_history WHERE key = ? AND measured_at = ?`, store.StatFileBytes, day); err != nil {
			return fmt.Errorf("file bytes: %w", err)
		}
		for id, n := range fileBytes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO usage_history (key, project_id, measured_at, value) VALUES (?, ?, ?, ?)`,
				store.StatFileBytes, id, day, n); err != nil {
				return fmt.Errorf("file bytes: %w", err)
			}
		}
		return nil
	})
}
