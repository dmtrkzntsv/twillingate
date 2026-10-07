// Command analytics is the single binary for the ultra-lite analytics
// system: `serve` (ingestion server), `reporting dev` (a local preview
// server for dashboard files), `migrate`, `keygen`, and `version`.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	internalversion "github.com/dmtrkzntsv/twillingate/internal/shared/version"
)

var version = internalversion.Version

const usage = "usage: twillingate <serve|reporting|migrate|keygen|project|key|form|version> [flags]"

var commands = map[string]func(args []string, stdout io.Writer) int{}

func init() {
	commands["version"] = func(_ []string, stdout io.Writer) int {
		fmt.Fprintf(stdout, "twillingate %s\n", version)
		return 0
	}
}

func run(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, usage)
		return 2
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stdout, "unknown command %q\n%s\n", args[0], usage)
		return 2
	}
	return cmd(args[1:], stdout)
}

// renamedFlag reports a retired flag by its replacement, so a leftover
// `serve -api` in a unit or compose file names the fix instead of printing
// the flag package's bare "flag provided but not defined".
func renamedFlag(args []string, old, repl string, stdout io.Writer) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && name == old {
			fmt.Fprintf(stdout, "-%s was renamed to -%s\n", old, repl)
			return true
		}
	}
	return false
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
