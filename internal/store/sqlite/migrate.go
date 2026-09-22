package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// dataSteps are the parts of a migration that must not be SQL: value
// folds that run through the same validators the server applies at
// ingest, so a vocabulary is defined once (internal/enrich) and the
// database never carries a copy. A step runs after its version's SQL, in
// the same transaction, and an error rolls the whole migration back.
var dataSteps = map[int]func(context.Context, *sql.Tx) error{
	15: foldEnvironment,
}

func (d *DB) Migrate(ctx context.Context) error {
	return d.migrateThrough(ctx, math.MaxInt)
}

// migrateThrough applies every pending migration whose version is <=
// maxVersion. Migrate uses no ceiling; tests use one to build a database
// at an older schema and exercise the next migration against it.
func (d *DB) migrateThrough(ctx context.Context, maxVersion int) error {
	if _, err := d.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
		   version INTEGER PRIMARY KEY,
		   applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return fmt.Errorf("sqlite: migrations table: %w", err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("sqlite: bad migration name %q", name)
		}
		var done int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&done); err != nil {
			return err
		}
		if done > 0 || version > maxVersion {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("sqlite: migration %s: %w", name, err)
		}
		if step, ok := dataSteps[version]; ok {
			if err := step(ctx, tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("sqlite: migration %s data step: %w", name, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
