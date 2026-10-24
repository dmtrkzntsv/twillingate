package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// devQueryTimeout and devQueryMaxRows mirror CONSOLE_QUERY_TIMEOUT and
// CONSOLE_QUERY_ROWS_MAX' own defaults (config.go): `reporting dev` has no
// config of its own to read them from, and is a local preview tool
// rather than something an operator tunes.
const (
	devQueryTimeout = 30 * time.Second
	devQueryMaxRows = 1000
)

// ReportingDev runs `twillingate reporting dev`: a read-only handle on
// dbPath, for the project switcher, an engine on this binary over the
// event files in eventsDir, where its sql widgets run, the native engine
// over them for its query widgets, and
// reporting.DevHandler, serving addr until ctx is cancelled. There is no
// store, no registry and no auth here — cmd/twillingate/reporting.go has
// already refused a non-loopback addr before this is called. Without a
// registry the widgets read the files without the projects' event and
// attribute rules.
func ReportingDev(ctx context.Context, dirs []string, dbPath, eventsDir, addr string, logger *slog.Logger) error {
	// The handle serves devListProjects' own query only: widget SQL runs
	// on the event files.
	db, err := readsql.Open(dbPath, devQueryTimeout, devQueryMaxRows)
	if err != nil {
		return err
	}
	defer db.Close()
	cfg := &config.Config{Database: "sqlite://" + dbPath, EventsDir: eventsDir,
		Engine:  config.EngineConfig{MemoryLimit: config.DefaultEngineMemoryLimit, MaxChildren: config.DefaultEngineMaxChildren},
		Console: config.ConsoleConfig{QueryTimeout: devQueryTimeout, QueryMaxRows: devQueryMaxRows}}
	eng, reader, err := openFiles(cfg)
	if err != nil {
		return err
	}
	defer eng.Close()
	files := newWidgetFiles(cfg, reader, nil)
	nat := native.New(native.Options{Dir: eventsDir})

	srv := &http.Server{Addr: addr, Handler: reporting.DevHandler(dirs, files, nat, db), ReadHeaderTimeout: 5 * time.Second}
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
