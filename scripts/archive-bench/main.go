// Command archive-bench measures the raw-first design
// (docs/superpowers/specs/2026-10-12-raw-first-design.md). Its own module,
// so the main module's go.mod and CI never see its dependencies. Scratch:
// delete it once the spec's numbers are filled in.
//
//	go build -o /tmp/archive-bench .
//	DATABASE_DSN=sqlite:///path/base.db twillingate migrate
//	twillingate project create -name app -attr access_state -attr deployment -attr mode
//	twillingate project create -name site
//	/tmp/archive-bench gen base.db 2023-10-13 1096 32850 500
//
// gen writes prod-shaped rows (1M a month at 32850 a day) with the ingest
// statement of internal/store/sqlite/write.go and prints rows/s and file
// size every 30 days.
package main

import (
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: bench gen|parquet|lookup ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		cmdGen(os.Args[2:])
	default:
		fmt.Println("unknown command")
		os.Exit(2)
	}
}
