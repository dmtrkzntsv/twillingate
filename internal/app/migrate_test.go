package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
// event file under EVENTS_DIR, the reporting migrator has nothing to
// check the system widgets on, so it syncs them without the engine and
// marks the sync unchecked; the next start with files checks them. No
// system widget reads SQL any more, so that check runs no query either:
// neither start spawns a child (a spawn creates engine/).
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
	if hash, err := st.ReportingHash(ctx); err != nil || !strings.HasPrefix(hash, "unchecked:") {
		t.Fatalf("ReportingHash = %q, %v; want the sync marked unchecked", hash, err)
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

// TestMigrateRefusesAnotherUserBeforeTheEngine: a command run as anyone but
// the database file's owner (root, say) must not spawn an engine child,
// which would leave engine/ and raw/ owned by that user. The reporting
// migrator's files refuse their first query, the one that would spawn it,
// before it reaches the engine, and every query after. No system widget
// reads SQL any more, so Migrate itself runs no query and goes on.
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

	inner := &failingFiles{}
	files := &ownerFiles{Files: inner, path: DatabasePath(cfg.Database)}
	for range 2 {
		_, err = files.Query(ctx, archive.Scoped{})
		if err == nil || !strings.Contains(err.Error(), "service user") || !strings.Contains(err.Error(), "twillingate migrate") {
			t.Fatalf("err = %v, want a refusal naming the service user and the command", err)
		}
	}
	if inner.queries != 0 {
		t.Errorf("%d queries reached the engine past the refusal", inner.queries)
	}

	writeEvent(t, cfg.EventsDir)
	if err := Migrate(ctx, cfg, st); err != nil {
		t.Fatalf("another user, no system SQL to check: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "engine")); !os.IsNotExist(err) {
		t.Errorf("stat engine/: %v, want it not to exist: another user's run spawned a child", err)
	}

	geteuid = owner
	if _, err := (&ownerFiles{Files: inner, path: DatabasePath(cfg.Database)}).Query(ctx, archive.Scoped{}); err != nil || inner.queries != 1 {
		t.Errorf("as the owner: err %v, %d queries; want it passed on", err, inner.queries)
	}
}

// TestCheckOwner: the database file's owner may run a command that spawns
// the engine; anyone else is refused with the owner's uid and the service
// user named.
func TestCheckOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twillingate.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := int(info.Sys().(*syscall.Stat_t).Uid)
	if err := checkOwner(path, owner, "migrate"); err != nil {
		t.Fatalf("the owner: %v, want nil", err)
	}
	err = checkOwner(path, owner+1, "migrate")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("uid %d", owner)) || !strings.Contains(err.Error(), "service user") {
		t.Fatalf("another uid: %v, want a refusal naming uid %d and the service user", err, owner)
	}
	if err := checkOwner(filepath.Join(t.TempDir(), "missing.db"), owner, "migrate"); err == nil {
		t.Fatal("a missing database file passed")
	}
}
