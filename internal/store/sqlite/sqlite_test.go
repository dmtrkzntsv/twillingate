package sqlite

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestMain runs the package's tests, then removes the snapshot databases'
// temp dir (see snapshotAt) once every test in the binary has finished
// with them.
func TestMain(m *testing.M) {
	code := m.Run()
	if snapshotDir != "" {
		os.RemoveAll(snapshotDir)
	}
	os.Exit(code)
}

var (
	snapshotMu  sync.Mutex
	snapshotDir string
	snapshots   = map[int]string{}
)

// snapshotAt returns the path of a database migrated through version v
// (math.MaxInt for the current schema), built once per test binary. Under
// -race every migration costs seconds and the package hit go test's
// 10-minute default timeout when each test migrated its own database
// from scratch, so tests copy a snapshot instead. A new snapshot starts
// from a copy of the nearest lower one, so the whole binary runs each
// migration on an empty database about once. Migrations are
// deterministic on an empty database and schema_migrations alone decides
// what is pending, so a copy behaves exactly like a fresh migration.
func snapshotAt(t *testing.T, v int) string {
	t.Helper()
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	if path, ok := snapshots[v]; ok {
		return path
	}
	if snapshotDir == "" {
		dir, err := os.MkdirTemp("", "twillingate-sqlite-snapshots-")
		if err != nil {
			t.Fatal(err)
		}
		snapshotDir = dir
	}
	path := filepath.Join(snapshotDir, fmt.Sprintf("at-%d.db", v))
	base := -1
	for k := range snapshots {
		if k < v && k > base {
			base = k
		}
	}
	if base >= 0 {
		copyFile(t, snapshots[base], path)
	}
	db, err := openAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.migrateThrough(context.Background(), v); err != nil {
		db.Close()
		t.Fatal(err)
	}
	// Close checkpoints the WAL into the main file (verified by hand:
	// no -wal/-shm sidecar survives it), so copyFile only ever needs the
	// one file.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshots[v] = path
	return path
}

// openSnapshot opens a private copy of snapshotAt(v).
func openSnapshot(t *testing.T, v int) *DB {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "test.db")
	copyFile(t, snapshotAt(t, v), dst)
	db, err := openAt(dst)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestDBAt opens a temp database migrated only through version v.
func newTestDBAt(t *testing.T, v int) *DB {
	t.Helper()
	return openSnapshot(t, v)
}

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db := openSnapshot(t, math.MaxInt)
	// A no-op at the version the snapshot already carries; kept so this
	// test path still matches how production opens an existing database.
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenViaRegistry(t *testing.T) {
	s, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPragmas(t *testing.T) {
	db := newTestDB(t)
	for pragma, want := range map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"auto_vacuum":  "2", // INCREMENTAL
	} {
		var got string
		if err := db.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := db.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("schema_migrations rows = %d", n)
	}
}

func TestSchemaTablesExist(t *testing.T) {
	db := newTestDB(t)
	for _, table := range []string{
		"meta", "projects", "ingest_keys", "rules", "audit_log", "usage_history",
		"components", "dashboards", "widgets", "project_tabs", "widget_shares", "forms", "submissions",
	} {
		var name string
		err := db.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func hasColumn(t *testing.T, db *DB, table, column string) bool {
	t.Helper()
	rows, err := db.db.QueryContext(context.Background(),
		`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func hasTable(t *testing.T, db *DB, name string) bool {
	t.Helper()
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestMigrationsAreIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}
