package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func init() { commands["events"] = cmdEvents }

const eventsUsage = "usage: twillingate events import [-force]"

// cmdEvents is `twillingate events import`: once, with the ingest service
// stopped, it copies the raw rows the database holds into the event files
// (app.ImportEvents). It migrates the schema first, as `migrate` does, so
// it can run before the new binary's first start.
func cmdEvents(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "import" {
		fmt.Fprintln(stdout, eventsUsage)
		return 2
	}
	fs := flag.NewFlagSet("events import", flag.ContinueOnError)
	fs.SetOutput(stdout)
	force := fs.Bool("force", false, "import again although it ran (what the files hold is skipped)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()
	if err := app.Migrate(ctx, cfg, st); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if err := app.ImportEvents(ctx, cfg, st, *force, stdout); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	return 0
}
