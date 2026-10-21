package sqlite

import (
	"context"
	"database/sql"
	"errors"
)

// eventsImportedKey is the meta row v0.21's `twillingate events import`
// wrote once every raw row SQLite held was in the event files.
const eventsImportedKey = "events_imported"

// eventsNotImported is migration 042's refusal while events holds rows
// and eventsImportedKey is unset. This release has no import: the
// release before it does.
const eventsNotImported = "migration 042 drops the events table, which still holds raw events the event files may lack: " +
	"upgrade to v0.21.x first and run `twillingate events import` there, then upgrade to this release"

// checkEventsImported is migration 042's pre-check (preChecks): it refuses
// while events holds a row and the import has not run, so an install
// still below 042 never loses events by skipping v0.21. A database with no
// events table, or an empty one, passes. It runs on the schema 041 left,
// so internal/sqlcheck lists this file in migrationSteps.
func checkEventsImported(ctx context.Context, conn *sql.Conn) error {
	var table bool
	if err := conn.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'events')`).Scan(&table); err != nil {
		return err
	}
	if !table {
		return nil
	}
	var held, imported bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events)`).Scan(&held); err != nil {
		return err
	}
	if !held {
		return nil
	}
	if err := conn.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM meta WHERE key = ? AND value <> '')`, eventsImportedKey).Scan(&imported); err != nil {
		return err
	}
	if !imported {
		return errors.New(eventsNotImported)
	}
	return nil
}
