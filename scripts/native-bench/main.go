// Command native-bench measures a native Go query engine against the DuckDB
// baseline for the dashboard queries (the native engine's design and
// measurement plan are in docs/). Its own module, so the main module's
// go.mod and CI never see its dependencies. Scratch: delete it once the
// spec's numbers are filled in.
//
//	go build -o native-bench .
//	native-bench <subcommand> ...
//
//	native-bench rewrite -lib arrow|pqgo -rows N <in> <out>
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: native-bench rewrite -lib arrow|pqgo -rows N <in> <out>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "rewrite":
		if err := rewriteCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "native-bench:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "native-bench: unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}
