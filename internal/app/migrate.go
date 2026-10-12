package app

import (
	"context"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
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
// The reporting migrator gets its own read-only handle on the writer's
// own database file (DatabasePath(cfg.Database)), not CONSOLE_DATABASE_DSN, which
// may point at a copy that lags the write this migration itself just made.
// It is the scoped handle widget SQL runs on (readsql.OpenScoped), so a
// system widget reading a table widget SQL may not read stops the boot
// rather than failing on every dashboard load.
func Migrate(ctx context.Context, cfg *config.Config, st store.Store) error {
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
	db, err := readsql.OpenScoped(DatabasePath(cfg.Database), cfg.Console.QueryTimeout, cfg.Console.QueryMaxRows)
	if err != nil {
		return err
	}
	defer db.Close()
	return reporting.Migrate(ctx, st, db)
}
