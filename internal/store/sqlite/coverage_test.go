package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func day(s string) civil.Date {
	d, err := civil.Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

// tx must leave no partial writes behind when fn fails; every write path
// depends on this for atomicity.
func TestTxRollsBackOnError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES ('tx', 'x')`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = 'tx'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows survived a failed transaction, want 0", n)
	}
}

// After Close, every operation must return an error rather than panic — the
// scheduler keeps calling into the store while shutdown is in flight.
func TestOperationsOnClosedDB(t *testing.T) {
	db, err := openAt(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for name, op := range map[string]func() error{
		"IncrementalVacuum":  func() error { return db.IncrementalVacuum(ctx) },
		"RecordUsageHistory": func() error { return db.RecordUsageHistory(ctx, time.Now(), map[int64]int64{1: 1}) },
		"Migrate":            func() error { return db.Migrate(ctx) },
		"CreateProject": func() error {
			_, err := db.CreateProject(ctx, store.RegistryProject{Name: "App", AllowedOrigins: "[]"},
				store.AuditEntry{Actor: "test", Action: "project.create"})
			return err
		},
		"SetMeta": func() error { return db.SetMeta(ctx, "k", "v") },
		"ProjectIDs": func() error {
			_, err := db.ProjectIDs(ctx)
			return err
		},
		"GetMeta": func() error { _, err := db.GetMeta(ctx, "k"); return err },
	} {
		if err := op(); err == nil {
			t.Errorf("%s on a closed DB returned nil, want error", name)
		}
	}
}

func TestOpenRejectsBadDSNAndPath(t *testing.T) {
	if _, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "no-such-dir", "x.db")); err == nil {
		t.Error("want error opening a database under a missing directory")
	}
	if _, err := store.Open("sqlite://\x7f\x00"); err == nil {
		t.Error("want error for an unparseable DSN")
	}
}

// A migration that fails must roll back and must NOT be recorded as applied;
// a phantom version row would permanently skip the migration on restart,
// leaving the schema half-built.
func TestMigrateFailureIsNotRecorded(t *testing.T) {
	db, err := openAt(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// Collide with a table the migration creates, so 001 fails partway.
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE agg_web_daily (nonsense TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err == nil {
		t.Fatal("want error from a colliding migration, got nil")
	}
	var applied int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Errorf("%d migrations recorded after failure, want 0", applied)
	}
	// The failed migration must have rolled back its earlier statements too.
	var projects int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Error("failed migration left the projects table behind; rollback did not happen")
	}
}

// A DSN with no leading slash puts the first path segment in url.Host; that
// relative form must resolve to a relative file, not be dropped.
func TestOpenRelativeDSN(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := store.Open("sqlite://sub/relative.db")
	if err == nil {
		s.Close()
		t.Fatal("want error: sub/ does not exist yet")
	}
	if err := os.Mkdir("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open("sqlite://sub/relative.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("sub", "relative.db")); err != nil {
		t.Errorf("relative DSN did not create sub/relative.db: %v", err)
	}
}

// ExecForTest is a thin pass-through exported for other packages' test
// seeding (internal/api's seed helper is the real caller — see
// seed_test.go there); a cross-package call does not register in this
// package's own coverage profile, so it needs a direct exercise here too,
// success and error path both.
func TestExecForTest(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.ExecForTest(`INSERT INTO meta (key, value) VALUES (?, ?)`,
		"execfortest", "ok"); err != nil {
		t.Fatalf("ExecForTest(valid insert) = %v, want nil", err)
	}
	var value string
	if err := db.db.QueryRow(`SELECT value FROM meta WHERE key = 'execfortest'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "ok" {
		t.Errorf("value = %q, want %q", value, "ok")
	}
	if _, err := db.ExecForTest(`INSERT INTO no_such_table (x) VALUES (1)`); err == nil {
		t.Error("ExecForTest(bad SQL) = nil, want error")
	}
}
