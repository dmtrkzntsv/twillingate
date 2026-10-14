package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// engine is left off the usage line: the server spawns it. `engine -check`
// is for operators and the build (docs/deployment.md).
func init() { commands["engine"] = cmdEngine }

func cmdEngine(args []string, stdout io.Writer) int {
	if len(args) == 1 && args[0] == "-check" {
		return engineCheck(stdout)
	}
	if err := engine.ServeChild(args, os.Stdin, stdout); err != nil {
		return 1
	}
	return 0
}

// engineCheck spawns a child the way the server will, with this
// environment's settings and data directory, and runs one query.
func engineCheck(stdout io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	opts, err := app.EngineOptions(cfg)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	e, err := engine.New(opts)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	res, err := e.Query(ctx, "SELECT 42 AS answer, version() AS duckdb", 1)
	if err != nil {
		fmt.Fprintln(stdout, "engine check failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "engine ok: %v in %d ms (spawn and first query), DuckDB %v\n",
		res.Rows[0][0], time.Since(start).Milliseconds(), res.Rows[0][1])
	return 0
}
