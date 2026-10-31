// Package app wires the serve subcommand (spec §1, §5, §9).
package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/api"
	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/geo"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/jobs"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/pipeline"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/server"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
)

func NewLogger(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	out := os.Stdout
	if cfg.File != "" {
		if f, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640); err == nil {
			out = f
		}
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(out, opts))
	}
	return slog.New(slog.NewJSONHandler(out, opts))
}

// httpSurface pairs a listen address with the handler serving it, so
// Serve can start/shut down an arbitrary number of listeners (one for the
// shared -ingest/-console case, up to two when the surfaces use different
// addresses) with the same loop. label names which surface(s) the listener
// carries ("ingest", "console" or "ingest,console"), logged at boot so a
// role swap between two units is visible in journalctl rather than only
// inferred from the port.
type httpSurface struct {
	addr    string
	handler http.Handler
	label   string
}

// NewOps loads the project registry from st and returns the management
// frontend over it, with the configured form draft period: what Serve and
// the CLI both manage through.
func NewOps(ctx context.Context, cfg *config.Config, st store.Store, logger *slog.Logger) (*manage.Ops, error) {
	reg := manage.New(st, logger)
	if err := reg.Reload(ctx); err != nil {
		return nil, err
	}
	ops := manage.NewOps(reg, st)
	ops.FormDraftDays = cfg.Forms.DraftDays
	return ops, nil
}

// Serve runs the requested surfaces (ingest: events and the SDK, console:
// MCP, REST, login and dashboards) until ctx is cancelled or a listener
// fails. At least one of ingest/console must be true; the caller (cmd/twillingate) enforces that as a
// usage error before reaching here.
//
// Store/registry/geo/pipeline/jobs setup runs regardless of which surfaces
// are requested: jobs and pipeline are harmless when only the console runs,
// and the console itself needs store+registry. Only the HTTP listeners and
// the ingest-summary goroutine are conditional.
//
// Shutdown order matters: HTTP drains first so no new events/requests
// arrive, then the ingest summary logger, then the jobs runner stops, and
// only then is the pipeline cancelled — its cancellation is what triggers
// the final flush, so it must come last or buffered events would be lost.
func Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger, runIngest, runConsole bool) error {
	// Retired settings still set start nothing: say so once.
	if w := cfg.IgnoredWarning(); w != "" {
		logger.Warn(w)
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	// The external SQL engine (SQL_ENGINE_SOCKET) answers SQL over the
	// event files: the reporting migrator checks the SQL widgets through it
	// at start-up, and the console's SQL widgets and the query tool's sql
	// mode run on it. Without one, that SQL is refused. Closed after the
	// HTTP surfaces and the widget check return (Serve waits for both),
	// its last users.
	// Only the console calls it at start (openSQL: another protocol
	// version fails the start, an engine that does not answer is logged);
	// an ingest-only process needs no engine, and its migrator reaches it
	// lazily, only for a sql system widget to check.
	var sql api.SQL
	if runConsole {
		client, err := openSQL(ctx, cfg.SQLEngineSocket, logger)
		if err != nil {
			return err
		}
		if client != nil {
			defer client.Close()
			sql = client
		}
	} else if lazy := newLazySQL(cfg.SQLEngineSocket, logger); lazy != nil {
		defer lazy.Close()
		sql = lazy
	}
	if err := migrate(ctx, cfg, st, sql); err != nil {
		return err
	}
	ops, err := NewOps(ctx, cfg, st, logger)
	if err != nil {
		return err
	}
	reg := ops.Reg
	// The native engine answers the console's structured queries, the
	// rule-save checks and form attribution over the files in Go; the
	// external SQL engine serves SQL alone (the query tool's sql mode, SQL
	// widgets and their start-up check).
	nat := native.New(native.Options{Dir: cfg.EventsDir})
	ops.Presence = &filePresence{nat: nat, reg: reg, timeout: cfg.Console.QueryTimeout}
	if len(reg.Snapshot(ctx).Projects()) == 0 {
		logger.Warn("no projects configured; create one with `twillingate project create` or an API management operation")
	}
	salter := identity.NewSalter(st, time.Now)
	dataDir := filepath.Dir(DatabasePath(cfg.Database))
	geoProvider, err := geo.New(cfg.Geo, dataDir, logger)
	if err != nil {
		return err
	}
	defer geoProvider.Close()

	// The archive log (raw-first PR 3): the ingest process appends every
	// event to DATA_DIR beside SQLite; a console-only process writes none.
	// The log is the only sink; only the ingest process enqueues, so a
	// process without ingest builds no buffer.
	var archiveLog *archive.Log
	var buf *pipeline.Buffer
	pipeCtx, stopPipe := context.WithCancel(context.Background())
	pipeDone := make(chan struct{})
	if runIngest {
		archiveLog, err = archive.OpenLog(cfg.EventsDir)
		if err != nil {
			stopPipe()
			return err
		}
		defer archiveLog.Close()
		// The pipeline and jobs runner get their own contexts rather than
		// ctx, so cancelling ctx does not tear them down before HTTP has
		// drained.
		buf = pipeline.New(cfg.Buffer, archiveLog, logger)
		go func() { buf.Run(pipeCtx); close(pipeDone) }()
	} else {
		close(pipeDone)
	}

	// A project with no active ingest key can receive nothing. That is a
	// legitimate retired state, so warn rather than refuse to start.
	for _, p := range reg.Snapshot(ctx).KeylessProjects() {
		logger.Warn("project has no active ingest keys and can receive nothing", "project_id", p.ID, "name", p.Name)
	}

	runner := jobs.New(st, cfg, reg, salter, logger, time.Now)
	if archiveLog != nil {
		runner.WithArchive(archive.New(archiveLog, logger))
		// Form attribution (raw-first PR 5b): the process that ingests
		// looks each submission's source up in the files a minute after
		// it arrives (natively) and writes its event to
		// the log.
		runner.WithAttribution(nat, archiveLog)
	}
	jobsCtx, stopJobs := context.WithCancel(context.Background())
	jobsDone := make(chan struct{})
	go func() { runner.Run(jobsCtx); close(jobsDone) }()

	// stopBackground shuts down jobs then pipeline, in that order, for use
	// on early-return error paths before the HTTP surfaces exist.
	stopBackground := func() {
		stopJobs()
		<-jobsDone
		stopPipe()
		<-pipeDone
	}

	var ingestHandler *server.Server
	if runIngest {
		ingestHandler = server.New(cfg, reg, buf, geoProvider, salter, st, logger)
		if cfg.IngestDisabled {
			logger.Warn("ingest is disabled (INGEST_DISABLED): every ingest route refuses events and form submissions with 429")
		}
	}

	// Assemble the HTTP surface(s). When both -ingest and -console target the
	// same address, they share one listener/mux, each registering its own
	// patterns (Build, not NewHandler, so /mcp and /api/ mount alongside
	// the ingest routes without a double /healthz registration); otherwise
	// each surface gets a listener of its own.
	var surfaces []httpSurface
	var apiClose func() error
	switch {
	case runConsole && runIngest && cfg.Console.Addr == cfg.IngestAddr:
		protected, closeDB, err := api.Build(ctx, cfg, reg, ops, st, logger, sql, nat)
		if err != nil {
			stopBackground()
			return err
		}
		apiClose = closeDB
		mux := http.NewServeMux()
		ingestHandler.Mount(mux)
		api.RegisterOn(mux, protected, cfg, false, logger)
		surfaces = append(surfaces, httpSurface{cfg.IngestAddr, api.NotFoundPages(mux), "ingest,console"})
	case runConsole:
		h, closeDB, err := api.NewHandler(ctx, cfg, reg, ops, st, logger, sql, nat)
		if err != nil {
			stopBackground()
			return err
		}
		apiClose = closeDB
		if runIngest {
			surfaces = append(surfaces, httpSurface{cfg.IngestAddr, ingestHandler, "ingest"})
		}
		surfaces = append(surfaces, httpSurface{cfg.Console.Addr, h, "console"})
	default:
		surfaces = append(surfaces, httpSurface{cfg.IngestAddr, ingestHandler, "ingest"})
	}
	if apiClose != nil {
		defer apiClose()
	}

	// Bind every listener before serving or logging "serving": a caller (or
	// a test) that sees one surface answer can rely on the others accepting
	// too, and a port in use fails here, before anything is left running.
	listeners := make([]net.Listener, 0, len(surfaces))
	for _, s := range surfaces {
		ln, err := net.Listen("tcp", s.addr)
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			stopBackground()
			return err
		}
		listeners = append(listeners, ln)
	}

	summaryDone := make(chan struct{})
	stopSummary := func() {}
	if runIngest {
		var sumCtx context.Context
		sumCtx, stopSummary = context.WithCancel(context.Background())
		// ingestSummaryInterval is read here, synchronously, rather than
		// inside the goroutine: it is a package var a test shrinks to avoid
		// waiting a real minute, and reading it lazily from the background
		// goroutine would race against a later test's write to it.
		interval := ingestSummaryInterval
		go func() { logIngestSummary(sumCtx, ingestHandler, logger, interval); close(summaryDone) }()
	} else {
		close(summaryDone)
	}

	srvs := make([]*http.Server, len(surfaces))
	errCh := make(chan error, len(surfaces))
	for i, s := range surfaces {
		sv := &http.Server{
			Addr:              s.addr,
			Handler:           s.handler,
			ReadHeaderTimeout: 5 * time.Second,
		}
		srvs[i] = sv
		go func(sv *http.Server, ln net.Listener) { errCh <- sv.Serve(ln) }(sv, listeners[i])
		logger.Info("serving", "addr", s.addr, "surfaces", s.label, "projects", len(reg.Snapshot(ctx).Projects()))
	}

	// The users' widgets are checked once the console answers, in the
	// background: Serve does not wait for the queries, only for the check
	// to stop before the client closes.
	checkDone := make(chan struct{})
	checkCtx, stopCheck := context.WithCancel(context.Background())
	if runConsole {
		opt := api.WidgetOptions(cfg, reg, reporting.Options{})
		go func() { checkUserWidgets(checkCtx, st, sql, opt, logger); close(checkDone) }()
	} else {
		close(checkDone)
	}

	remaining := len(srvs)
	var listenErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		listenErr = err
		remaining--
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, sv := range srvs {
		if err := sv.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http shutdown", "error", err, "addr", sv.Addr)
		}
	}
	stopSummary()
	<-summaryDone
	stopCheck()
	<-checkDone
	stopJobs()
	<-jobsDone
	stopPipe() // triggers final flush
	<-pipeDone

	for i := 0; i < remaining; i++ {
		if err := <-errCh; listenErr == nil && err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr = err
		}
	}
	return listenErr
}

// DatabasePath extracts the filesystem path from a sqlite DSN: the data dir
// (the GeoLite2 DB lives next to the database) and the CLI's read handle
// both start from it.
func DatabasePath(dsn string) string {
	return strings.TrimPrefix(dsn, "sqlite://")
}

// ingestSummaryInterval is how often logIngestSummary drains the counters.
// Package var (mirroring internal/geo's refreshInterval) so a test can
// shrink it instead of waiting a real minute for the ticker to fire.
var ingestSummaryInterval = time.Minute

// logIngestSummary emits one line per active key label each minute instead
// of logging per request. Labels, never keys — and this is what tells an
// operator an old key has gone idle and is safe to retire.
func logIngestSummary(ctx context.Context, srv *server.Server, logger *slog.Logger, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for label, c := range srv.Counters().Drain() {
				logger.Info("ingest summary", "key_label", label,
					"accepted", c[0], "rejected", c[1])
			}
		}
	}
}
