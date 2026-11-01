package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func init() { commands["monitor"] = cmdMonitor }

const monitorUsage = "usage: twillingate monitor <list|set|check> -project-id <id> [flags]"

// What monitor check waits for: the longest check the server's runner can run
// is the availability check and the certificate read at MONITOR_TIMEOUT each and
// the RDAP lookup's limit, and it drops a request it has not served after
// monitorCheckDrop. These mirror internal/monitor's rdapTimeout and checkMaxAge.
const (
	monitorCheckRDAP   = 15 * time.Second
	monitorCheckMargin = 5 * time.Second // the tick that starts it and the one that writes
	monitorCheckDrop   = 2 * time.Minute
)

// monitorCheckPoll is how often monitor check looks; a variable for the tests.
var monitorCheckPoll = time.Second

func monitorCheckWait(timeout time.Duration) time.Duration {
	return 2*timeout + monitorCheckRDAP + monitorCheckMargin
}

// monitorCheckNotFinished says what a request still pending at now means. Within
// the drop a runner may well be on it; past it, one would have dropped it.
func monitorCheckNotFinished(requestedAt string, now time.Time) string {
	if t, err := time.Parse(time.RFC3339, requestedAt); err == nil && now.Sub(t) <= monitorCheckDrop {
		return "the check is still running; see monitor list for its result"
	}
	return "the check did not finish and nothing picked it up: is the server up?"
}

func cmdMonitor(args []string, stdout io.Writer) int {
	envFile, sub, subArgs, ok := subcommand("monitor", monitorUsage, []string{"list", "set", "check"}, args, stdout)
	if !ok {
		return 2
	}
	sf := flag.NewFlagSet("monitor "+sub, flag.ContinueOnError)
	sf.SetOutput(stdout)
	project := sf.Int64("project-id", 0, "project id (required)")
	var origin *string
	if sub != "list" {
		origin = sf.String("origin", "", "the monitor's origin, one of the project's exact http(s) origins (required)")
	}
	var enable, disable *bool
	var contains *string
	if sub == "set" {
		enable = sf.Bool("enable", false, "turn the monitor on")
		disable = sf.Bool("disable", false, "turn the monitor off")
		contains = sf.String("contains", "", "text the page must contain; empty clears it")
	}
	if err := sf.Parse(subArgs); err != nil {
		return 2
	}
	if *project == 0 || (origin != nil && *origin == "") {
		fmt.Fprintln(stdout, "usage: twillingate monitor "+sub+" -project-id <id>"+
			map[string]string{"list": "", "set": " -origin URL [-enable|-disable] [-contains TEXT]", "check": " -origin URL"}[sub])
		return 2
	}
	var spec manage.MonitorSpec
	if sub == "set" {
		// A flag left out keeps the value; an empty -contains clears it.
		given := map[string]bool{}
		sf.Visit(func(f *flag.Flag) { given[f.Name] = true })
		if given["enable"] && given["disable"] {
			fmt.Fprintln(stdout, "use -enable or -disable, not both")
			return 2
		}
		if given["enable"] {
			spec.Enabled = enable
		}
		if given["disable"] {
			on := !*disable
			spec.Enabled = &on
		}
		if given["contains"] {
			spec.Contains = contains
		}
	}
	ops, cfg, closeStore, err := openOps(envFile)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer closeStore()
	ctx := context.Background()
	if sub == "check" {
		return monitorCheck(ctx, ops, *project, *origin, monitorCheckWait(cfg.Monitor.Timeout), stdout)
	}
	var l manage.MonitorList
	if sub == "set" {
		l, err = ops.SetMonitor(ctx, "cli", *project, *origin, spec)
	} else {
		l, err = ops.ListMonitors(ctx, *project)
	}
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	printMonitors(stdout, *project, l, cfg.Monitor.Interval, cfg.Monitor.Timeout)
	return 0
}

// monitorCheck asks for a check of one monitor and waits for the server's runner
// to serve it.
func monitorCheck(ctx context.Context, ops *manage.Ops, project int64, origin string, wait time.Duration, stdout io.Writer) int {
	l, err := ops.CheckMonitor(ctx, "cli", project, origin)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	// Taken as the server stored it: it was accepted, so this spelling is one.
	origin = manage.MonitorOrigins([]string{strings.TrimSpace(origin)})[0]
	m, _ := l.Monitor(origin)
	deadline := time.Now().Add(wait)
	for m.CheckRequestedAt > m.CheckFinishedAt {
		if time.Now().After(deadline) {
			fmt.Fprintln(stdout, monitorCheckNotFinished(m.CheckRequestedAt, time.Now()))
			return 1
		}
		time.Sleep(monitorCheckPoll)
		if l, err = ops.ListMonitors(ctx, project); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		m, _ = l.Monitor(origin)
	}
	fmt.Fprintln(stdout, monitorCheckText(m))
	if !m.CheckUp && m.CheckReason == "" {
		return 1 // finished, but it never ran
	}
	return 0
}

// monitorCheckText is the last manual check's result, as the console words it.
func monitorCheckText(m store.Monitor) string {
	switch {
	case m.CheckUp:
		return fmt.Sprintf("up, %d, %d ms (%s)", m.CheckStatus, m.CheckMS, m.CheckFinishedAt)
	case m.CheckReason == "":
		return fmt.Sprintf("%s (%s)", m.CheckError, m.CheckFinishedAt)
	}
	return fmt.Sprintf("down, %s: %s (%s)", m.CheckReason, m.CheckError, m.CheckFinishedAt)
}

// printMonitors prints a project's monitors and the domains they share.
func printMonitors(w io.Writer, project int64, l manage.MonitorList, interval, timeout time.Duration) {
	fmt.Fprintf(w, "project %d monitors\n", project)
	if interval > 0 {
		fmt.Fprintf(w, "each site checked every\t%d seconds\n", int(interval/time.Second)*max(l.Enabled(), 1))
	}
	if timeout > 0 {
		fmt.Fprintf(w, "timeout\t%d seconds\n", int(timeout/time.Second))
	}
	if len(l.Monitors) == 0 {
		fmt.Fprintln(w, "no exact http(s) origin to monitor")
	}
	for _, m := range l.Monitors {
		printMonitor(w, m)
	}
	for _, d := range l.Domains {
		fmt.Fprintf(w, "domain %s\texpires %s, checked %s, origins %s\n", d.Domain, orUnknown(d.ExpiresAt), orUnknown(d.CheckedAt), strings.Join(d.Origins, ", "))
	}
}

// printMonitor prints one monitor: a line for it, and what it holds under it.
func printMonitor(w io.Writer, m store.Monitor) {
	state := "disabled"
	if m.Enabled {
		state = "enabled"
	}
	fmt.Fprintf(w, "%s %s\n", m.Origin, state)
	if m.Contains != "" {
		fmt.Fprintf(w, "  contains\t%s\n", m.Contains)
	}
	if !strings.HasPrefix(m.Origin, "http://") && (m.CertCheckedAt != "" || m.CertError != "") {
		fmt.Fprintf(w, "  certificate\texpires %s, issuer %s, valid %t, checked %s", orUnknown(m.CertExpiresAt), orUnknown(m.CertIssuer), m.CertValid, orUnknown(m.CertCheckedAt))
		if m.CertError != "" {
			fmt.Fprintf(w, ", error %s", m.CertError)
		}
		fmt.Fprintln(w)
	}
	if m.Redirects > 0 {
		fmt.Fprintf(w, "  redirects\t%d\n", m.Redirects)
	}
	if m.CheckFinishedAt != "" {
		fmt.Fprintf(w, "  last manual check\t%s\n", monitorCheckText(m))
	}
}

func orUnknown(s string) string {
	return cmp.Or(s, "unknown")
}
