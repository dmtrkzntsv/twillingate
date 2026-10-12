package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// prepareEncoding runs before 041's SQL (spec D17): it refuses attributes
// the encoding cannot carry, drops raw rows whose project is gone (their
// keys cannot get catalog rows), and adds the catalog row of every key a
// raw row carries, views included, which the encoding joins on.
func prepareEncoding(ctx context.Context, tx *sql.Tx) error {
	var invalid, nonString int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE NOT json_valid(attributes)
		OR json_type(attributes) <> 'object'`).Scan(&invalid); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events e, json_each(e.attributes) j
		WHERE json_valid(e.attributes) AND j.type <> 'text'`).Scan(&nonString); err != nil {
		return err
	}
	if invalid > 0 || nonString > 0 {
		return fmt.Errorf("events: %d rows with attributes that are not a JSON object, %d attribute values that are not a string; fix them before upgrading (deploy/UPGRADES.md, 041)", invalid, nonString)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE project_id NOT IN (SELECT id FROM projects)`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_attributes (project_id, key)
		SELECT DISTINCT e.project_id, j.key FROM events e, json_each(e.attributes) j`)
	return err
}
