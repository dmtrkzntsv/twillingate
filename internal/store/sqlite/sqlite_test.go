package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestMain runs the package's tests, then removes the template database's
// temp dir (see templateDB) once every test in the binary has finished
// with it.
func TestMain(m *testing.M) {
	code := m.Run()
	if templateDir != "" {
		os.RemoveAll(templateDir)
	}
	os.Exit(code)
}

var (
	templateOnce sync.Once
	templateDir  string
	templatePath string
)

// templateDB migrates a database to the current schema version once per
// test binary and returns its path. Under -race, running every migration
// per test (~3s each here, unraced ~0.1s, ~185 tests in the package) blows
// past go test's 10-minute default timeout; newTestDB copies this file
// instead of migrating one from scratch for every test.
func templateDB(t *testing.T) string {
	t.Helper()
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "twillingate-sqlite-template-")
		if err != nil {
			t.Fatal(err)
		}
		templateDir = dir
		path := filepath.Join(dir, "template.db")
		db, err := openAt(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Close checkpoints the WAL into the main file (verified by hand:
		// no -wal/-shm sidecar survives it), so copyFile below only ever
		// needs the one file.
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		templatePath = path
	})
	if templatePath == "" {
		t.Fatal("template database was not built")
	}
	return templatePath
}

func newTestDB(t *testing.T) *DB {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "test.db")
	copyFile(t, templateDB(t), dst)
	db, err := openAt(dst)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// A no-op at the version the template already carries; kept so this
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
		"meta", "projects", "events",
		"agg_views_daily", "agg_views_paths", "agg_views_referrers", "agg_views_countries",
		"agg_views_devices", "agg_views_browsers", "agg_views_os", "agg_views_utm",
		"agg_product_daily", "agg_product_totals", "agg_product_attrs",
	} {
		var name string
		err := db.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

// --- app analytics schema (migrations 003/004) ---

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

func TestMigration003Schema(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, table := range []string{
		"events", "actors", "agg_retention", "identities", "agg_identity_daily",
	} {
		var n int
		if err := db.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s missing", table)
		}
	}

	for _, c := range []struct{ table, column string }{
		{"events", "kind"}, {"events", "family"}, {"events", "display_width"},
		{"events", "actor_id"}, {"events", "user_id"},
		{"events", "group_id"}, {"events", "actor_kind"}, {"events", "os"},
		{"events", "app_version"}, {"events", "received_at"},
	} {
		if !hasColumn(t, db, c.table, c.column) {
			t.Errorf("%s.%s missing", c.table, c.column)
		}
	}
	// 012 renamed the old events.platform (an OS by another name) to os;
	// 015 reintroduced platform as a distinct column meaning the surface.
	// Both must be present, and os must not have been lost to the rename.
	if !hasColumn(t, db, "events", "platform") {
		t.Error("events.platform missing; 015 adds it beside os")
	}
}

func TestMigrationViews(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, view := range []string{
		"v_views_daily", "v_views_paths", "v_views_hosts", "v_views_referrers", "v_views_utm",
		"v_views_countries", "v_views_platforms", "v_views_os", "v_views_browsers",
		"v_views_app_versions",
		"v_views_devices", "v_views_displays", "v_views_consent",
		"v_product_daily", "v_product_totals", "v_product_attrs",
		"v_identity_daily", "v_retention",
	} {
		var n int
		if err := db.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='view' AND name=?`, view).Scan(&n); err != nil {
			t.Fatalf("%s: %v", view, err)
		}
		if n != 1 {
			t.Errorf("view %s missing", view)
		}
		// Every view must be queryable, not merely present.
		if _, err := db.db.ExecContext(ctx, `SELECT * FROM `+view+` LIMIT 1`); err != nil {
			t.Errorf("view %s not queryable: %v", view, err)
		}
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}
