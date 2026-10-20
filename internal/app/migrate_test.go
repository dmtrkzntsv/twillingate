package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// TestMigrateRefusesAnotherUserBeforeTheEngine: a command run as anyone but
// the database file's owner (root, say) is refused before the reporting
// migrator's first query spawns an engine child, which would leave
// engine/ and raw/ owned by that user. On a release that changed nothing
// it spawns nothing, so it goes on.
func TestMigrateRefusesAnotherUserBeforeTheEngine(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := testConfig(t, freePort(t), filepath.Join(dir, "migrate.db"))
	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner := geteuid
	t.Cleanup(func() { geteuid = owner })
	geteuid = func() int { return os.Geteuid() + 1 }

	err = Migrate(ctx, cfg, st)
	if err == nil || !strings.Contains(err.Error(), "service user") || !strings.Contains(err.Error(), "twillingate migrate") {
		t.Fatalf("err = %v, want a refusal naming the service user and the command", err)
	}
	for _, d := range []string{filepath.Join(dir, "engine"), cfg.EventsDir} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("stat %s: %v, want it not to exist: a refused run spawned a child", d, err)
		}
	}

	geteuid = owner
	if err := Migrate(ctx, cfg, st); err != nil {
		t.Fatalf("as the owner: %v", err)
	}
	geteuid = func() int { return os.Geteuid() + 1 }
	if err := Migrate(ctx, cfg, st); err != nil {
		t.Fatalf("another user, nothing to check: %v", err)
	}
}
