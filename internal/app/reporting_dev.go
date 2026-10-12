package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/api"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// devQueryTimeout and devQueryMaxRows mirror CONSOLE_QUERY_TIMEOUT and
// CONSOLE_QUERY_ROWS_MAX' own defaults (config.go): `reporting dev` has no
// config of its own to read them from, and is a local preview tool
// rather than something an operator tunes.
const (
	devQueryTimeout = 10 * time.Second
	devQueryMaxRows = 1000
)

// ReportingDev runs `twillingate reporting dev`: two read-only handles on
// dbPath, the scoped one widget SQL runs on (as in the real console) and
// the full one reporting's own SQL reads projects through, and
// reporting.DevHandler, serving addr until ctx is cancelled. There is no
// store, no registry and no auth here — cmd/twillingate/reporting.go has
// already refused a non-loopback addr before this is called.
func ReportingDev(ctx context.Context, dirs []string, dbPath, addr string, logger *slog.Logger) error {
	// Widget SQL here is as custom as in the console: it may not read
	// what the console's widget SQL may not.
	db, err := readsql.OpenScoped(dbPath, devQueryTimeout, devQueryMaxRows, api.CustomSQLRefused()...)
	if err != nil {
		return err
	}
	defer db.Close()
	trusted, err := readsql.Open(dbPath, devQueryTimeout, devQueryMaxRows)
	if err != nil {
		return err
	}
	defer trusted.Close()

	srv := &http.Server{Addr: addr, Handler: reporting.DevHandler(dirs, db, trusted), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("reporting dev serving", "addr", addr, "dirs", dirs)

	var listenErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr = err
		}
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) && listenErr == nil {
		listenErr = err
	}
	return listenErr
}
