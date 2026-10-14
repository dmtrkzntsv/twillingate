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
//
//	duckdb events.duckdb < load_duckdb.sql   # design C's data; Parquet: see the file
//	/tmp/archive-bench sqlq base.db queries_sqlite.sql 2025-10-13 2026-10-12 900 actor=<id> common=page_view rare=<name>
//	/tmp/archive-bench duckq events.duckdb small.sql queries_duckdb.sql 2025-10-13 2026-10-12 3 actor=<id> ...
//	python ops_duckdb.py events.duckdb queries_duckdb.sql <actor>
//
// sqlq times design A's query set, duckq design C's (small.sql holds the
// SETs: threads=2, memory_limit=256MB; for Parquet, also a view named
// events over read_parquet), ops_duckdb.py C's appends and deletes.
package main

import (
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: bench gen|sqlq|duckq ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		cmdGen(os.Args[2:])
	case "sqlq":
		cmdSQLQ(os.Args[2:])
	case "duckq":
		cmdDuckQ(os.Args[2:])
	default:
		fmt.Println("unknown command")
		os.Exit(2)
	}
}
