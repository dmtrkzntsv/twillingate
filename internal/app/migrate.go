package app

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// store.Store must keep satisfying reporting.Store: the slice reporting's
// migrator (and the rest of that package) reads and writes.
var _ reporting.Store = store.Store(nil)

// Migrate runs the store's own schema migrations, then the release's
// reporting migrator (components and system dashboards, D20/D23) — every
// entry point that opens the store (serve, migrate, project) calls this
// rather than st.Migrate directly, so a CLI-only install stays in sync
// with the release too.
//
// The reporting migrator gets its own read-only handle on the writer's
// own database file (databasePath(cfg.Database)), not API_DB_PATH, which
// may point at a copy that lags the write this migration itself just made.
func Migrate(ctx context.Context, cfg *config.Config, st store.Store) error {
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	db, err := readsql.Open(databasePath(cfg.Database), cfg.API.QueryTimeout, cfg.API.QueryMaxRows)
	if err != nil {
		return err
	}
	defer db.Close()
	return reporting.Migrate(ctx, st, db)
}
