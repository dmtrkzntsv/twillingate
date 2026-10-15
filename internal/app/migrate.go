package app

import (
	"context"
	"fmt"
	"sync"

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

// Migrate runs the store's own schema migrations, rebuilds the objects the
// process owns rather than the migrations (v_events_flat, from the declared
// attributes), then runs the release's reporting migrator (components and
// system dashboards, D20/D23) — every entry point that opens the store
// (serve, migrate, project) calls this rather than st.Migrate directly, so
// a CLI-only install stays in sync with the release too.
//
// The reporting migrator validates every system widget on the event
// files, so Migrate builds an engine on this binary for it, closed before
// it returns. The engine spawns its child only when a query runs, which
// is only when the release's system definition changed: most runs spawn
// none. The run that does must be the database owner's (checkOwner): a
// child spawned as root leaves engine/slots, engine/spill and raw/ owned
// by root, which the service user then cannot open. Migrate checks just
// before that first query, so a command run as root on a release that
// changed nothing goes on as before.
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
	// A migration drops the flat view, and widgets and BI queries read its
	// attr_ columns: a failed rebuild must stop the boot, not leave the
	// view missing.
	reg := manage.New(st, NewLogger(cfg.Log))
	if err := reg.Reload(ctx); err != nil {
		return err
	}
	if err := st.RebuildFlatView(ctx, reg.Snapshot(ctx).DeclaredAttributeKeys()); err != nil {
		return fmt.Errorf("rebuild v_events_flat: %w", err)
	}
	var files reporting.Files = newWidgetFiles(cfg, reader, reg)
	if owner != "" {
		files = &ownerFiles{Files: files, path: owner}
	}
	return reporting.Migrate(ctx, st, files)
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

func (f *ownerFiles) Query(ctx context.Context, sql string, args []engine.Arg, maxRows int, project int64) (engine.Result, error) {
	f.once.Do(func() {
		if err := checkOwner(f.path, geteuid(), "migrate"); err != nil {
			f.err = fmt.Errorf("this release changed the system dashboards, which are checked with an engine child "+
				"that must run as the database's owner: %w", err)
		}
	})
	if f.err != nil {
		return engine.Result{}, f.err
	}
	return f.Files.Query(ctx, sql, args, maxRows, project)
}
