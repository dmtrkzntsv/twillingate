package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
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

// TestMigrateWithoutEventFilesSpawnsNoChild: on a fresh install, with no
// event file under EVENTS_DIR, or with no SQL engine, the reporting
// migrator has nothing to check the system widgets' SQL on. No system
// widget reads SQL any more, so there is nothing left unchecked either:
// the sync is recorded as checked, and no start spawns a child (a spawn
// creates engine/).
func TestMigrateWithoutEventFilesSpawnsNoChild(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := testConfig(t, freePort(t), filepath.Join(dir, "migrate.db"))
	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := Migrate(ctx, cfg, st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "engine")); !os.IsNotExist(err) {
		t.Errorf("stat engine/: %v, want it not to exist: no files, yet a child was spawned", err)
	}
	if hash, err := st.ReportingHash(ctx); err != nil || hash == "" || strings.HasPrefix(hash, "unchecked:") {
		t.Fatalf("ReportingHash = %q, %v; want the sync recorded, checked: no system widget reads SQL", hash, err)
	}

	writeEvent(t, cfg.EventsDir)
	if err := Migrate(ctx, cfg, st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "engine")); !os.IsNotExist(err) {
		t.Errorf("stat engine/: %v, want it not to exist: no system widget reads SQL, yet a child was spawned", err)
	}
	if hash, _ := st.ReportingHash(ctx); strings.HasPrefix(hash, "unchecked:") {
		t.Errorf("ReportingHash = %q after a start with files, still unchecked", hash)
	}
}

// writeEvent appends one event of project 1 to the log under dir.
func writeEvent(t *testing.T, dir string) {
	t.Helper()
	log, err := archive.OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	now := time.Now().UTC()
	if err := log.WriteEvents(context.Background(), []store.Event{{ID: "e1", ProjectID: 1,
		Family: store.FamilyProduct, EventName: "x", TS: now, ReceivedAt: now}}); err != nil {
		t.Fatal(err)
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
	// Neither the migrator nor the boot pass has anything to do on an
	// empty install, so neither spawns an engine child (a spawn creates
	// engine/).
	if _, err := os.Stat(filepath.Join(filepath.Dir(dbPath), "engine")); !os.IsNotExist(err) {
		t.Errorf("stat engine/: %v, want it not to exist: a fresh install spawned a child", err)
	}
}
