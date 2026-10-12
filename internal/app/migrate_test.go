package app

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestMigrateSyncsComponents(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate.db")
	addr := freePort(t)
	cfg := testConfig(t, addr, dbPath)

	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := Migrate(context.Background(), cfg, st); err != nil {
		t.Fatal(err)
	}

	comps, err := st.ListComponents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manifestComps, err := reporting.ParseManifest(reporting.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	if len(comps) != len(manifestComps) {
		t.Errorf("components = %d, want %d (the manifest's own count)", len(comps), len(manifestComps))
	}
}

// TestServeBootsWithReportingMigration guards Serve's switch to
// Migrate(ctx, cfg, st): a fresh database must still boot and answer
// /healthz once the reporting sync runs as part of that call.
func TestServeBootsWithReportingMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "boot.db")
	addr := freePort(t)
	cfg := testConfig(t, addr, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, slog.Default(), true, false) }()

	waitHealthy(t, "http://"+addr)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}
}

// migratedWith opens a migrated store at a fresh path and runs stmts
// against the file directly, as an operator or a migration would.
func migratedWith(t *testing.T, stmts ...string) (store.Store, *config.Config) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "flat.db")
	cfg := testConfig(t, freePort(t), dbPath)
	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := Migrate(context.Background(), cfg, st); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, q := range stmts {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return st, cfg
}

// Migrate rebuilds v_events_flat from the declared attributes, so a
// database whose view a migration dropped comes back with its attr_
// columns before anything serves, from serve and the CLI alike.
func TestMigrateRebuildsTheFlatView(t *testing.T) {
	st, cfg := migratedWith(t,
		`INSERT INTO projects (name, allowed_origins) VALUES ('blog', '[]')`,
		`INSERT INTO project_attributes (project_id, key, breakdown) SELECT id, 'plan', 1 FROM projects WHERE name = 'blog'`,
		`DROP VIEW v_events_flat`)
	if err := Migrate(context.Background(), cfg, st); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", DatabasePath(cfg.Database))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('v_events_flat') WHERE name = 'attr_plan'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("v_events_flat has no attr_plan after Migrate")
	}
}

// A flat view that cannot be rebuilt stops the boot rather than leaving
// widgets reading a view without their columns.
func TestMigrateFailsWhenTheFlatViewCannotBeRebuilt(t *testing.T) {
	st, cfg := migratedWith(t, `DROP VIEW v_events_flat`, `CREATE TABLE v_events_flat (x)`)
	err := Migrate(context.Background(), cfg, st)
	if err == nil || !strings.Contains(err.Error(), "v_events_flat") {
		t.Fatalf("Migrate = %v, want the rebuild's error", err)
	}
}
