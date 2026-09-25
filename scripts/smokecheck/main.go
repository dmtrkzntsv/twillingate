// Command smokecheck prints row counts for the two raw families of an
// analytics database, read through raw_views and raw_product (the one raw
// events table holds both). Used by scripts/smoke.sh to verify ingestion
// end-to-end without requiring a sqlite3 CLI on the host.
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: smokecheck <db-path>")
		os.Exit(2)
	}
	db, err := sql.Open("sqlite", "file:"+os.Args[1]+"?mode=ro")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	count := func(view string) int {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + view).Scan(&n); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return n
	}
	fmt.Printf("views=%d events=%d\n", count("raw_views"), count("raw_product"))
}
