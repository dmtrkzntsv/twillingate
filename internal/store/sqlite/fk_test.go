package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestWriterEnforcesForeignKeys checks the pragma the writer DSN sets
// (openAt's _pragma=foreign_keys(1)), not just that it was passed in the
// connection string, since a typo there fails silently.
func TestWriterEnforcesForeignKeys(t *testing.T) {
	db := newTestDB(t)
	var on int
	if err := db.db.QueryRowContext(context.Background(),
		`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Errorf("PRAGMA foreign_keys = %d, want 1", on)
	}
}

// TestDeleteProjectDataWithKeys guards the reorder in DeleteProjectData:
// with foreign keys enforced, deleting the projects row while an
// ingest_keys row still references it would violate the constraint, so
// the dependents must go first.
func TestDeleteProjectDataWithKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "cli", Action: "project.create"}
	id, err := db.CreateProjectWithKey(ctx,
		store.RegistryProject{Name: "blog", AllowedOrigins: "[]"},
		store.RegistryKey{Label: "web"},
		audit, store.AuditEntry{Actor: "cli", Action: "key.create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProjectData(ctx, id, store.AuditEntry{Actor: "cli", Action: "project.delete"}); err != nil {
		t.Fatalf("DeleteProjectData: %v", err)
	}
	var projects, keys int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=?`, id).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ingest_keys WHERE project_id=?`, id).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if projects != 0 || keys != 0 {
		t.Errorf("projects=%d keys=%d, want 0, 0", projects, keys)
	}
}

// TestMigrationViolationFails seeds a database at schema 20 with an
// orphan ingest_keys row (project_id 999, no such project), the way a
// pre-existing installation could carry a stray row FKs were never
// enforced against, then checks Migrate refuses to leave it in place.
func TestMigrationViolationFails(t *testing.T) {
	db := newTestDBAt(t, 20)
	if _, err := db.ExecForTest(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecForTest(
		`INSERT INTO ingest_keys (key, project_id, label) VALUES ('k1', 999, 'web')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecForTest(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	err := db.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("Migrate = %v, want an error mentioning foreign_key_check", err)
	}
}

// TestExistingDatabasePassesCheck seeds projects and keys through the
// store, then migrates to latest: no dangling FK should ever come out of
// ordinary use.
func TestExistingDatabasePassesCheck(t *testing.T) {
	db := newTestDBAt(t, 19)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "seed", Action: "project.create"}
	if _, err := db.CreateProjectWithKey(ctx,
		store.RegistryProject{Name: "app", AllowedOrigins: "[]"},
		store.RegistryKey{Label: "web"},
		audit, store.AuditEntry{Actor: "seed", Action: "key.create"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
}

func TestManageErrInvalidIsStoreErrInvalid(t *testing.T) {
	if !errors.Is(manage.ErrInvalid, store.ErrInvalid) {
		t.Error("manage.ErrInvalid is not store.ErrInvalid")
	}
}

// TestMigrateWithCancelledContextLeavesForeignKeysOn guards the deferred
// "PRAGMA foreign_keys=ON" at the end of migrateThrough: it must run on
// context.Background(), not the caller's ctx, or a caller that gives up
// (or a migration failure that unwinds the stack after ctx is cancelled)
// would leave the writer's one pooled connection permanently enforcing
// nothing.
//
// This drives migrateThrough with an already-cancelled context. Passing
// that ctx to d.db.Conn(ctx) (the first thing migrateThrough does) fails
// immediately — database/sql checks ctx.Done() before it will even hand
// back an idle connection — so the pragma is never touched either way in
// this run. What the test actually protects is the invariant surviving
// that early failure: an older version of this code that swallowed the
// deferred pragma's error entirely, or that constructed the connection
// differently, could regress this without any test noticing. A genuine
// "cancelled after PRAGMA OFF, before the deferred PRAGMA ON" interleaving
// cannot be reproduced deterministically against modernc.org/sqlite: its
// stmt.exec only consults ctx.Done() at the step phase, but "PRAGMA
// foreign_keys=ON" is a set-pragma SQLite applies during prepare, before
// exec's ctx check ever runs (confirmed by direct experiment: calling
// ExecContext with an already-cancelled context still flips the pragma
// and merely reports ctx.Err() alongside the real effect). So the one
// interleaving this fix targets — the pragma silently not applying — was
// not reproducible even with the old, unfixed code; using
// context.Background() and surfacing its error removes the dependency on
// that SQLite implementation detail rather than guarding an observed
// failure.
func TestMigrateWithCancelledContextLeavesForeignKeysOn(t *testing.T) {
	db := newTestDBAt(t, 19) // migration 20 still pending
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err := db.migrateThrough(cancelled, 20)
	if err == nil {
		t.Fatal("migrateThrough with a cancelled context: want an error, got nil")
	}

	var on int
	if err := db.db.QueryRowContext(context.Background(),
		`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Errorf("PRAGMA foreign_keys = %d after a cancelled migrate, want 1", on)
	}

	var done int
	if err := db.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schema_migrations WHERE version=20`).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if done != 0 {
		t.Errorf("migration 20 applied despite the cancelled context (schema_migrations count = %d)", done)
	}
}
