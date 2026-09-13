package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/config"
)

func init() {
	commands["serve"] = cmdServe
}

// resolveSurfaces maps the -ingest/-api flags to the surfaces to run. Bare
// `serve` runs both, leniently: an API without auth config is skipped with
// a warning rather than failing the process. Naming a flag is an explicit
// request, so misconfiguration of a requested surface stays a hard error.
func resolveSurfaces(ingest, api bool) (runIngest, runAPI, lenient bool) {
	if !ingest && !api {
		return true, true, true
	}
	return ingest, api, false
}

func cmdServe(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	ingest := fs.Bool("ingest", false, "run only the ingest surface (events and the SDK)")
	api := fs.Bool("api", false, "run only the API surface (MCP and REST)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	runIngest, runAPI, lenient := resolveSurfaces(*ingest, *api)
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	logger := app.NewLogger(cfg.Log)
	if runAPI {
		if err := cfg.ValidateAPI(); err != nil {
			if !lenient {
				fmt.Fprintln(stdout, err)
				return 1
			}
			logger.Warn("API disabled", "reason", err.Error())
			runAPI = false
		}
	}
	// Per operator request: a surface without its own port is worth a
	// warning, never a failure. API_ADDR unset means the API rides the
	// ingestion listener — a supported topology, flagged so a shared
	// port is never a surprise.
	if runIngest && runAPI && cfg.API.Addr == cfg.IngestAddr {
		logger.Warn("API_ADDR not set; the API shares the ingestion listener", "addr", cfg.IngestAddr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Serve(ctx, cfg, logger, runIngest, runAPI); err != nil {
		logger.Error("serve failed", "error", err)
		return 1
	}
	return 0
}
