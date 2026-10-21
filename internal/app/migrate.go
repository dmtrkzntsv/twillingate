package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// store.Store must keep satisfying reporting.Store: the slice reporting's
// migrator (and the rest of that package) reads and writes.
var _ reporting.Store = store.Store(nil)

// Migrate runs the store's own schema migrations, then the release's
// reporting migrator (components and system dashboards, D20/D23) — every
// entry point that opens the store (serve, migrate, project) calls this
// rather than st.Migrate directly, so a CLI-only install stays in sync with
// the release too. Migration 042 refuses while SQLite holds raw events
// that were never imported into the event files (v0.21's
// `twillingate events import` did that; this release has no import).
//
// The reporting migrator validates every system widget on the event
// files, so Migrate builds an engine on this binary for it, closed before
// it returns. The engine spawns its child only when a query runs, which
// is only when the release's system definition changed and EVENTS_DIR
// holds an event file to check it on: most runs spawn none, and neither
// does a fresh install's first. The run that does must be the database
// owner's (checkOwner): a child spawned as root leaves engine/slots,
// engine/spill and raw/ owned by root, which the service user then cannot
// open. Migrate checks just before that first query, so a command run as
// root on a release that changed nothing, or on an install with no event
// file, goes on as before.
//
// That check is part of the boot, not a viewer's query: each of its
// queries runs under migrateQueryTimeout rather than
// CONSOLE_QUERY_TIMEOUT. On an upgrade's first start its first query
// loads every log the daily pass has not dumped yet, which can take
// minutes or more memory than the engine has; a check that cannot run
// for that (reporting.ErrUnchecked) is logged and the start goes on, so
// the pass runs and the next start checks again. A refusal of a system
// widget's SQL still fails the start.
func Migrate(ctx context.Context, cfg *config.Config, st store.Store) error {
	eng, reader, err := openFiles(cfg)
	if err != nil {
		return err
	}
	defer eng.Close()
	return migrate(ctx, cfg, st, reader, DatabasePath(cfg.Database))
}

// migrate is Migrate on the caller's reader: serve's, whose engine the
// console goes on using. With owner set (the database file's path), the
// reporting migrator's first query checks that this process runs as its
// owner; serve passes "".
func migrate(ctx context.Context, cfg *config.Config, st store.Store, reader *archive.Reader, owner string) error {
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	reg := manage.New(st, NewLogger(cfg.Log))
	if err := reg.Reload(ctx); err != nil {
		return err
	}
	// No event file at all (a fresh install): nothing to check the system
	// widgets on, so the migrator runs no query and spawns no child
	// (reporting.Migrate with nil Files); the first start with files
	// checks them.
	some, err := hasEventFiles(cfg.EventsDir)
	if err != nil {
		return err
	}
	var files reporting.Files
	if some {
		boot := newWidgetFiles(cfg, reader, reg)
		boot.timeout = migrateQueryTimeout
		files = boot
		if owner != "" {
			files = &ownerFiles{Files: files, path: owner}
		}
	}
	return migrateReporting(ctx, st, files, NewLogger(cfg.Log))
}

// migrateQueryTimeout bounds each query of the reporting migrator's check
// of the system widgets: long enough for the first to load a month of
// logs at the cap that the daily pass has not dumped yet, short enough
// that a start which cannot load them goes on (ErrUnchecked) while ingest
// waits.
const migrateQueryTimeout = 5 * time.Minute

// migrateReporting runs the reporting migrator on files. A check of the
// system widgets that could not run for want of time or memory is logged
// as an error and the start goes on; the next start checks again.
func migrateReporting(ctx context.Context, st store.Store, files reporting.Files, logger *slog.Logger) error {
	err := reporting.Migrate(ctx, st, files)
	if errors.Is(err, reporting.ErrUnchecked) {
		logger.Error("could not check the system dashboards on the event files; starting without, so the daily pass can run",
			"error", err.Error())
		return nil
	}
	return err
}

// geteuid is the effective uid Migrate checks against the database file's
// owner (checkOwner); tests replace it.
var geteuid = os.Geteuid

// checkOwner refuses a run whose effective uid is not the owner of the
// database file at path: the directories the run creates would belong to
// that user, and the service user could not write them afterwards.
// command is the one to show run as the service user.
func checkOwner(path string, euid int, command string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == euid {
		return nil
	}
	return fmt.Errorf("%s belongs to uid %d, not %d: run it as the service user, as in sudo -u twillingate twillingate %s", path, st.Uid, euid, command)
}

// ownerFiles is Files that refuses its first query, the one that spawns
// the engine's child, unless this process runs as the owner of the
// database file at path (checkOwner), and every query after a refusal.
type ownerFiles struct {
	reporting.Files
	path string
	once sync.Once
	err  error
}

func (f *ownerFiles) Query(ctx context.Context, q archive.Scoped) (engine.Result, error) {
	f.once.Do(func() {
		if err := checkOwner(f.path, geteuid(), "migrate"); err != nil {
			f.err = fmt.Errorf("this release changed the system dashboards, which are checked with an engine child "+
				"that must run as the database's owner: %w", err)
		}
	})
	if f.err != nil {
		return engine.Result{}, f.err
	}
	return f.Files.Query(ctx, q)
}
