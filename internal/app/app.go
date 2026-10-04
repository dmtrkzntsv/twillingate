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
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/api"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/geo"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/jobs"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/pipeline"
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
	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := Migrate(ctx, cfg, st); err != nil {
		return err
	}
	// The views' live halves read their caps from meta with a scalar
	// subquery -- SQL cannot see the environment. Written before anything
	// queries a view so the first read uses the configured caps rather than
	// the views' built-in fallbacks. 0 is written as is: the views read it
	// as no cap, as the daily pass does.
	for _, m := range []struct {
		key string
		n   int
	}{
		{"attributes_top_n", cfg.AttributesTopN},
		{"views_dimensions_top_n", cfg.ViewsDimensionsTopN},
		{"identities_top_n", cfg.IdentitiesTopN},
	} {
		if err := st.SetMeta(ctx, m.key, strconv.Itoa(m.n)); err != nil {
			return err
		}
	}

	reg := manage.New(st, logger)
	if err := reg.Reload(ctx); err != nil {
		return err
	}
	if len(reg.Snapshot(ctx).Projects()) == 0 {
		logger.Warn("no projects configured; create one with `twillingate project create` or an API management operation")
	}

	// Seed the flat view so it exists before the first daily pass.
	if err := st.RebuildFlatView(ctx, reg.Snapshot(ctx).DeclaredAttributeKeys()); err != nil {
		logger.Warn("flat view seed", "error", err)
	}

	salter := identity.NewSalter(st, time.Now)
	dataDir := filepath.Dir(databasePath(cfg.Database))
	geoProvider, err := geo.New(cfg.Geo, dataDir, logger)
	if err != nil {
		return err
	}
	defer geoProvider.Close()

	// The pipeline and jobs runner get their own contexts rather than ctx,
	// so cancelling ctx does not tear them down before HTTP has drained.
	buf := pipeline.New(cfg.Buffer, st, logger)
	pipeCtx, stopPipe := context.WithCancel(context.Background())
	pipeDone := make(chan struct{})
	go func() { buf.Run(pipeCtx); close(pipeDone) }()

	// A project with no active ingest key can receive nothing. That is a
	// legitimate retired state, so warn rather than refuse to start.
	for _, p := range reg.Snapshot(ctx).KeylessProjects() {
		logger.Warn("project has no active ingest keys and can receive nothing", "project_id", p.ID, "name", p.Name)
	}

	runner := jobs.New(st, cfg, reg, salter, logger, time.Now)
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
		protected, closeDB, err := api.Build(ctx, cfg, reg, manage.NewOps(reg, st), st, logger)
		if err != nil {
			stopBackground()
			return err
		}
		apiClose = closeDB
		mux := http.NewServeMux()
		ingestHandler.Mount(mux)
		api.RegisterOn(mux, protected, cfg, false, logger)
		surfaces = append(surfaces, httpSurface{cfg.IngestAddr, mux, "ingest,console"})
	case runConsole:
		h, closeDB, err := api.NewHandler(ctx, cfg, reg, manage.NewOps(reg, st), st, logger)
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

// databasePath extracts the filesystem path from a sqlite DSN for use as
// the data dir (GeoLite2 DB lives next to the database).
func databasePath(dsn string) string {
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
