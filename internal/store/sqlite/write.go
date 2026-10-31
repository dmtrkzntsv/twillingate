package sqlite

import (
	"context"
	"database/sql"
	"errors"
)

const tsFormat = "2006-01-02T15:04:05Z"

func (d *DB) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// tx runs fn in a transaction with commit/rollback handling; shared by all
// sqlite write paths.
func (d *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
