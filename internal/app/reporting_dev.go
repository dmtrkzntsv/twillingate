package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// devQueryTimeout and devQueryMaxRows mirror API_QUERY_TIMEOUT and
// API_QUERY_MAX_ROWS' own defaults (config.go): `reporting dev` has no
// config of its own to read them from, and is a local preview tool
// rather than something an operator tunes.
const (
	devQueryTimeout = 10 * time.Second
	devQueryMaxRows = 1000
)

// ReportingDev runs `twillingate reporting dev`: a read-only handle on
// dbPath (the same sql widgets in the real API run against) and
// reporting.DevHandler, serving addr until ctx is cancelled. There is no
// store, no registry and no auth here — cmd/twillingate/reporting.go has
// already refused a non-loopback addr before this is called.
func ReportingDev(ctx context.Context, dirs []string, dbPath, addr string, logger *slog.Logger) error {
	db, err := readsql.Open(dbPath, devQueryTimeout, devQueryMaxRows)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := &http.Server{Addr: addr, Handler: reporting.DevHandler(dirs, db), ReadHeaderTimeout: 5 * time.Second}
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
