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

// resolveSurfaces maps the -ingest/-console flags to the surfaces to run.
// Bare `serve` runs both, leniently: a console without auth config is
// skipped with a warning rather than failing the process. Naming a flag is
// an explicit request, so misconfiguration of a requested surface stays a
// hard error.
func resolveSurfaces(ingest, console bool) (runIngest, runConsole, lenient bool) {
	if !ingest && !console {
		return true, true, true
	}
	return ingest, console, false
}

func cmdServe(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	ingest := fs.Bool("ingest", false, "run only the ingest surface (events and the SDK)")
	console := fs.Bool("console", false, "run only the console surface (MCP, REST, login and dashboards)")
	if renamedFlag(args, "api", "console", stdout) {
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	runIngest, runConsole, lenient := resolveSurfaces(*ingest, *console)
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	logger := app.NewLogger(cfg.Log)
	if runConsole {
		if err := cfg.ValidateConsole(); err != nil {
			// Bare `serve` is only lenient about a *missing* CONSOLE_AUTH_DSN
			// (no config offered, so silently skipping the console is the
			// friendly default). A DSN that is set but fails to parse is a
			// mistake, not an absence, and must fail the process even in
			// bare mode — otherwise a typo'd resource= or an old-shaped
			// DSN would silently boot without the console at all.
			if !lenient || cfg.Console.AuthDSN != "" {
				fmt.Fprintln(stdout, err)
				return 1
			}
			logger.Warn("console disabled", "reason", err.Error())
			runConsole = false
		}
	}
	// Per operator request: a surface without its own port is worth a
	// warning, never a failure. CONSOLE_ADDR unset means the console rides
	// the ingestion listener — a supported topology, flagged so a shared
	// port is never a surprise.
	if runIngest && runConsole && cfg.Console.Addr == cfg.IngestAddr {
		logger.Warn("CONSOLE_ADDR not set; the console shares the ingestion listener", "addr", cfg.IngestAddr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Serve(ctx, cfg, logger, runIngest, runConsole); err != nil {
		logger.Error("serve failed", "error", err)
		return 1
	}
	return 0
}
