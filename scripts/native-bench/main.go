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
//	native-bench query -src arrow|pqgo -q countries|dau|attrvalues|attrvalues-approx -from YYYY-MM-DD -to YYYY-MM-DD <files...>
//	native-bench matrix [-out RESULTS.md] <root>
//	native-bench matrix1b [-out RESULTS.md] [-volumes 100k,1m] <root>
//	native-bench dashboards -bin app.test -pkg <checkout>/internal/app [-out RESULTS.md] [-volumes 100k,1m] [-engines native,duckdb] [-from <root>] [-before dashboards.json] <root>
//	native-bench pass -bin archive.test -pkg <checkout>/internal/archive -src <3f root> [-out RESULTS.md] [-volumes 100k,1m] <root>
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: native-bench rewrite|query|matrix|matrix1b|dashboards|pass ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "rewrite":
		err = rewriteCmd(os.Args[2:])
	case "query":
		err = queryCmd(os.Args[2:], os.Stdout)
	case "matrix":
		err = matrixCmd(os.Args[2:])
	case "matrix1b":
		err = matrix1bCmd(os.Args[2:])
	case "dashboards":
		err = dashboardsCmd(os.Args[2:])
	case "pass":
		err = passCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "native-bench: unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "native-bench:", err)
		os.Exit(1)
	}
}
