package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func init() { commands["monitor"] = cmdMonitor }

const monitorUsage = "usage: twillingate monitor <get|set> -project-id <id> [flags]"

func cmdMonitor(args []string, stdout io.Writer) int {
	envFile, sub, subArgs, ok := subcommand("monitor", monitorUsage, []string{"get", "set"}, args, stdout)
	if !ok {
		return 2
	}
	sf := flag.NewFlagSet("monitor "+sub, flag.ContinueOnError)
	sf.SetOutput(stdout)
	project := sf.Int64("project-id", 0, "project id (required)")
	var enable, disable *bool
	var origin, contains *string
	if sub == "set" {
		enable = sf.Bool("enable", false, "turn the monitor on")
		disable = sf.Bool("disable", false, "turn the monitor off")
		origin = sf.String("origin", "", "the origin to check, one of the project's exact http(s) origins")
		contains = sf.String("contains", "", "text the page must contain; empty clears it")
	}
	if err := sf.Parse(subArgs); err != nil {
		return 2
	}
	if *project == 0 {
		fmt.Fprintln(stdout, "usage: twillingate monitor "+sub+" -project-id <id>"+
			map[string]string{"get": "", "set": " [-enable|-disable] [-origin URL] [-contains TEXT]"}[sub])
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
		if given["origin"] {
			spec.Origin = origin
		}
		if given["contains"] {
			spec.Contains = contains
		}
	}
	ops, _, closeStore, err := openOps(envFile)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer closeStore()
	ctx := context.Background()
	var m store.Monitor
	if sub == "set" {
		m, err = ops.SetMonitor(ctx, "cli", *project, spec)
	} else {
		m, err = ops.Monitor(ctx, *project)
	}
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	var eligible []string
	if p := ops.Reg.Snapshot(ctx).Project(*project); p != nil {
		eligible = manage.MonitorOrigins(p.AllowedOrigins)
	}
	printMonitor(stdout, m, eligible)
	return 0
}

func printMonitor(w io.Writer, m store.Monitor, eligible []string) {
	state := "disabled"
	if m.Enabled {
		state = "enabled"
	}
	fmt.Fprintf(w, "project %d monitor %s\n", m.ProjectID, state)
	fmt.Fprintf(w, "origin\t%s\n", m.Origin)
	fmt.Fprintf(w, "contains\t%s\n", m.Contains)
	fmt.Fprintf(w, "eligible origins\t%s\n", strings.Join(eligible, ", "))
	if !strings.HasPrefix(m.Origin, "http://") && (m.CertCheckedAt != "" || m.CertError != "") {
		fmt.Fprintf(w, "certificate\texpires %s, issuer %s, valid %t, checked %s", orUnknown(m.CertExpiresAt), orUnknown(m.CertIssuer), m.CertValid, orUnknown(m.CertCheckedAt))
		if m.CertError != "" {
			fmt.Fprintf(w, ", error %s", m.CertError)
		}
		fmt.Fprintln(w)
	}
	if m.Redirects > 0 {
		fmt.Fprintf(w, "redirects\t%d\n", m.Redirects)
	}
	if m.DomainCheckedAt != "" {
		fmt.Fprintf(w, "domain\texpires %s, checked %s\n", orUnknown(m.DomainExpiresAt), m.DomainCheckedAt)
	}
}

func orUnknown(s string) string {
	return cmp.Or(s, "unknown")
}
