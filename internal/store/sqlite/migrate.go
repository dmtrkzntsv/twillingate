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
// maxVersion, then checks the result carries no dangling foreign key.
// Migrate uses no ceiling; tests use one to build a database at an older
// schema and exercise the next migration against it.
//
// A migration rebuilds tables (CREATE _new / INSERT SELECT / DROP /
// RENAME), which foreign key enforcement would refuse mid-flight, and
// PRAGMA foreign_keys cannot change inside a transaction, so it is
// switched off for the whole pass and back on once every migration has
// committed. The store opens with SetMaxOpenConns(1) (openAt), so in
// steady state there is exactly one physical connection to switch the
// pragma on — but that guarantee lives in the pool's configuration, one
// file away, not in this function. d.db.Conn(ctx) pins the *sql.Conn the
// pragma is set on for the rest of this call, so every migration
// transaction and the closing foreign_key_check run against that same
// connection regardless of pool behaviour, rather than trusting a second
// ExecContext call to be handed the connection the first one used.
//
// The re-enable runs on context.Background(), not ctx: ctx may already be
// cancelled by the time it runs (the caller gave up, or a migration
// failed and unwound the stack), and a cancelled context would make the
// pragma silently fail to re-arm, leaving the one pooled connection
// enforcing nothing for the rest of the process's life. If re-enabling
// still fails, that error becomes migrateThrough's own return value
// (named so the deferred func can set it) unless a real migration error
// already claimed it — a connection quietly sitting in the pool with
// foreign_keys off is a correctness bug, not something to swallow.
func (d *DB) migrateThrough(ctx context.Context, maxVersion int) (err error) {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("sqlite: foreign_keys off: %w", err)
	}
	defer func() {
		if _, pErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); pErr != nil && err == nil {
			err = fmt.Errorf("sqlite: foreign_keys on: %w", pErr)
		}
	}()

	if _, err := conn.ExecContext(ctx,
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
		if err := conn.QueryRowContext(ctx,
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
		tx, err := conn.BeginTx(ctx, nil)
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

	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("sqlite: foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("sqlite: foreign_key_check: %w", err)
		}
		return fmt.Errorf("sqlite: foreign_key_check after migrating: %s row %d violates %s",
			table, rowid.Int64, parent)
	}
	return rows.Err()
}
