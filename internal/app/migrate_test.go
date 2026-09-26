package app

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

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
