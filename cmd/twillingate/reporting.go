package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/config"
)

func init() { commands["reporting"] = cmdReporting }

const reportingUsage = "usage: twillingate reporting dev <dir>... [-db <path>] [-addr 127.0.0.1:3100]"

func cmdReporting(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, reportingUsage)
		return 2
	}
	sub, subArgs := args[0], args[1:]
	if sub != "dev" {
		fmt.Fprintf(stdout, "unknown subcommand %q\n%s\n", sub, reportingUsage)
		return 2
	}
	return cmdReportingDev(subArgs, stdout)
}

// cmdReportingDev runs a local, no-login preview server over one or more
// dashboard-file directories (D44): unlike every other subcommand, it
// talks to whatever database -db (or DATABASE_DSN) names read-only, and
// serves the built UI without auth, so -addr is refused unless it can
// only ever be reached from this machine.
func cmdReportingDev(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("reporting dev", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbFlag := fs.String("db", "", "sqlite database path (default: DATABASE_DSN's path)")
	addr := fs.String("addr", "127.0.0.1:3100", "address to listen on; must be loopback")
	// The usage is "<dir>... [-db ...] [-addr ...]": dirs before flags,
	// which the flag package's Parse cannot handle on its own (it stops
	// scanning for flags at the first plain argument, so a flag written
	// after a directory would otherwise be swallowed as another
	// "directory" instead of being recognized). splitFlags separates the
	// two before Parse ever sees them.
	flagArgs, dirs := splitFlags(args, "-db", "-addr")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(dirs) == 0 {
		fmt.Fprintln(stdout, reportingUsage)
		return 2
	}
	if !isLoopbackAddr(*addr) {
		fmt.Fprintf(stdout, "-addr must be loopback (127.0.0.1, ::1 or localhost); got %q\n", *addr)
		return 2
	}
	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = strings.TrimPrefix(os.Getenv("DATABASE_DSN"), "sqlite://")
	}
	if dbPath == "" {
		fmt.Fprintln(stdout, "-db or DATABASE_DSN is required")
		return 2
	}

	logger := app.NewLogger(config.LogConfig{})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.ReportingDev(ctx, dirs, dbPath, *addr, logger); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	return 0
}

// splitFlags partitions args into the flag tokens (anything starting
// with "-") and the positional directories (everything else), so a flag
// may appear anywhere among the directories rather than only before or
// after them — flag.FlagSet's own Parse stops looking for flags at the
// first plain argument, which is no good for a usage of the shape
// "<dir>... [-flag ...]". A value-flag named in flags (matched exactly,
// e.g. "-db") given as "-name value" also consumes the following token
// as its value, whatever it looks like, so a directory that happens to
// come right after one is never mistaken for that value; "-name=value"
// is already one token. An unrecognized "-flag" is still routed to
// flagArgs rather than positional, so fs.Parse sees it and refuses it as
// unknown, the same as a typo before any directory would be — it is
// never silently read as a directory named "-flag".
func splitFlags(args []string, flags ...string) (flagArgs, positional []string) {
	known := make(map[string]bool, len(flags))
	for _, f := range flags {
		known[f] = true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		name, _, hasEquals := strings.Cut(a, "=")
		if known[name] && !hasEquals && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return flagArgs, positional
}

// isLoopbackAddr reports whether addr (host:port) can only ever be
// reached from this machine: "localhost", 127.0.0.1/8, or ::1. A host
// that fails to parse as an IP and isn't "localhost" — including the
// empty host of ":3100", which means every interface — is refused.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
