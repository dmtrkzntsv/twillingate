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
	var contains, redirectTo *string
	if sub == "set" {
		enable = sf.Bool("enable", false, "turn the monitor on")
		disable = sf.Bool("disable", false, "turn the monitor off")
		contains = sf.String("contains", "", "text the page must contain; empty clears it")
		redirectTo = sf.String("redirect-to", "", "another of the project's origins the check must be redirected to; empty clears it")
	}
	if err := sf.Parse(subArgs); err != nil {
		return 2
	}
	if *project == 0 || (origin != nil && *origin == "") {
		fmt.Fprintln(stdout, "usage: twillingate monitor "+sub+" -project-id <id>"+
			map[string]string{"list": "", "set": " -origin URL [-enable|-disable] [-contains TEXT] [-redirect-to URL]", "check": " -origin URL"}[sub])
		return 2
	}
	var spec manage.MonitorSpec
	if sub == "set" {
		// A flag left out keeps the value; an empty -contains or -redirect-to clears it.
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
			spec.Expect.Contains = contains
		}
		if given["redirect-to"] {
			spec.Expect.RedirectTo = redirectTo
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
	m, ok := l.Monitor(origin)
	deadline := time.Now().Add(wait)
	for ok && m.CheckPending() {
		if time.Now().After(deadline) {
			fmt.Fprintln(stdout, monitorCheckNotFinished(m.CheckRequestedAt, time.Now()))
			return 1
		}
		time.Sleep(monitorCheckPoll)
		if l, err = ops.ListMonitors(ctx, project); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		m, ok = l.Monitor(origin)
	}
	// A row gone reads as the blank one (this process's list of origins may
	// not have caught up yet), which no request was ever made on.
	if !ok || m.CheckRequestedAt == "" {
		fmt.Fprintf(stdout, "the monitor of %s was removed: it is no longer an origin of project %d\n", origin, project)
		return 1
	}
	if m.CheckNotRun() {
		fmt.Fprintln(stdout, "the check did not run")
		return 1
	}
	fmt.Fprintln(stdout, monitorResultText(m.Last))
	return 0
}

// monitorResultText is an availability check's result, as the console words it.
func monitorResultText(r store.MonitorResult) string {
	if r.Up {
		return fmt.Sprintf("up, %d, %d ms (%s)", r.Status, r.MS, r.CheckedAt)
	}
	return fmt.Sprintf("down, %s: %s (%s)", r.Reason, r.Error, r.CheckedAt)
}

// printMonitors prints a project's monitors and the domains they share.
func printMonitors(w io.Writer, project int64, l manage.MonitorList, interval, timeout time.Duration) {
	fmt.Fprintf(w, "project %d monitors\n", project)
	if interval > 0 && !l.Idle {
		fmt.Fprintf(w, "one check every\t%d seconds, the enabled sites taking turns\n", int(interval/time.Second))
	}
	if timeout > 0 {
		fmt.Fprintf(w, "timeout\t%d seconds\n", int(timeout/time.Second))
	}
	if len(l.Monitors) == 0 {
		fmt.Fprintln(w, "no exact http(s) origin to monitor")
	}
	for _, m := range l.Monitors {
		printMonitor(w, m, l.CheckEvery(interval))
	}
	for _, d := range l.Domains {
		fmt.Fprintf(w, "domain %s\texpires %s, checked %s, origins %s\n", d.Domain, orUnknown(d.ExpiresAt), orUnknown(d.CheckedAt), strings.Join(d.Origins, ", "))
	}
}

// printMonitor prints one monitor: a line for it, and what it holds under it.
func printMonitor(w io.Writer, m store.Monitor, every time.Duration) {
	state := "disabled"
	if m.Enabled {
		state = "enabled"
	}
	fmt.Fprintf(w, "%s %s\n", m.Origin, state)
	if m.Enabled && every > 0 {
		fmt.Fprintf(w, "  checked every\t%d seconds\n", int(every/time.Second))
	}
	if m.Expect.Contains != "" {
		fmt.Fprintf(w, "  contains\t%s\n", m.Expect.Contains)
	}
	if m.Expect.RedirectTo != "" {
		fmt.Fprintf(w, "  redirect to\t%s\n", m.Expect.RedirectTo)
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
	if m.Last.CheckedAt != "" {
		fmt.Fprintf(w, "  last check\t%s\n", monitorResultText(m.Last))
	}
}

func orUnknown(s string) string {
	return cmp.Or(s, "unknown")
}
