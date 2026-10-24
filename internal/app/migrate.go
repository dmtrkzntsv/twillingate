package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/api"
	"github.com/dmtrkzntsv/twillingate/internal/config"
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
// files, through the external SQL engine at SQL_ENGINE_SOCKET when one is
// set. The engine is reached lazily (lazySQL): only a sql system widget's
// check calls it, health first, so a command that finds none (while no
// system widget reads SQL, every one) never does, and an engine that is
// down or of another version does not stop `twillingate project …`.
// Should one be checked, another protocol version fails the command and
// an engine that does not answer is logged.
//
// That check is part of the boot, not a viewer's query: each of its
// queries runs under migrateQueryTimeout rather than
// CONSOLE_QUERY_TIMEOUT. A check that cannot run (the engine is down,
// busy, or short of time or memory: reporting.ErrUnchecked) is logged and
// the start goes on, so the daily pass runs and the next start checks
// again. A refusal of a system widget's SQL still fails the start.
func Migrate(ctx context.Context, cfg *config.Config, st store.Store) error {
	sql := newLazySQL(cfg.SQLEngineSocket, NewLogger(cfg.Log))
	if sql == nil {
		return migrate(ctx, cfg, st, nil)
	}
	defer sql.Close()
	return migrate(ctx, cfg, st, sql)
}

// migrate is Migrate on the caller's engine client (nil: none): serve's,
// which the console goes on using.
func migrate(ctx context.Context, cfg *config.Config, st store.Store, sql reporting.SQL) error {
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	reg := manage.New(st, NewLogger(cfg.Log))
	if err := reg.Reload(ctx); err != nil {
		return err
	}
	// No event file at all (a fresh install): nothing to check the system
	// widgets on, so the migrator sends no query (reporting.Migrate with
	// no SQL); the first start with files checks them. While no system
	// widget reads SQL, even that check sends none.
	some, err := hasEventFiles(cfg.EventsDir)
	if err != nil {
		return err
	}
	if !some {
		sql = nil
	}
	opt := api.WidgetOptions(cfg, reg, reporting.Options{})
	opt.QueryTimeout = migrateQueryTimeout
	return migrateReporting(ctx, st, sql, opt, NewLogger(cfg.Log))
}

// migrateQueryTimeout bounds each query of the reporting migrator's check
// of the system widgets' SQL (none while no system widget reads SQL):
// long enough for an engine to load a month of logs at the cap that the
// daily pass has not dumped yet, short enough that a start which cannot
// goes on (ErrUnchecked) while ingest waits.
const migrateQueryTimeout = 5 * time.Minute

// migrateReporting runs the reporting migrator on sql. A check of the
// system widgets that could not run for want of time, memory or the
// engine is logged as an error and the start goes on; the next start
// checks again.
func migrateReporting(ctx context.Context, st store.Store, sql reporting.SQL, opt reporting.Options, logger *slog.Logger) error {
	err := reportingMigrate(ctx, st, sql, opt)
	if errors.Is(err, reporting.ErrUnchecked) {
		logger.Error("could not check the system dashboards on the event files; starting without, so the daily pass can run",
			"error", err.Error())
		return nil
	}
	return err
}

// reportingMigrate is reporting.Migrate; tests replace it, since there is
// no check to fail on while no system widget reads SQL.
var reportingMigrate = reporting.Migrate
