package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
)

func init() { commands["project"] = cmdProject }

// envFileLookup overlays KEY=VALUE lines from path under the real
// environment: real env wins, matching how EnvironmentFile= behaves.
func envFileLookup(path string) (func(string) (string, bool), error) {
	fromFile := map[string]string{}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				fromFile[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok {
			return v, true
		}
		v, ok := fromFile[key]
		return v, ok
	}, nil
}

// openOps opens the store named by DATABASE_DSN (optionally via
// -env-file), migrates, and returns the management frontend. The CLI
// talks to the database directly — break-glass by design (spec §7.1).
func openOps(stdout io.Writer, envFile string) (*manage.Ops, *config.Config, func(), int) {
	lookup, err := envFileLookup(envFile)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	cfg, err := config.FromEnv(lookup)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	reg := manage.New(st, app.NewLogger(cfg.Log))
	if err := reg.Reload(ctx); err != nil {
		st.Close()
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	return manage.NewOps(reg, st), cfg, func() { st.Close() }, 0
}

const projectUsage = "usage: twillingate project <create|update|list|archive|restore|delete> [flags]"

func cmdProject(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	fs.SetOutput(stdout)
	envFile := fs.String("env-file", "", "load environment from this file (real env wins)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stdout, projectUsage)
		return 2
	}
	sub, subArgs := rest[0], rest[1:]
	switch sub {
	case "create", "update", "list", "archive", "restore", "delete":
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\n%s\n", sub, projectUsage)
		return 2
	}
	ops, _, closeStore, code := openOps(stdout, *envFile)
	if code != 0 {
		return code
	}
	defer closeStore()
	ctx := context.Background()
	switch sub {
	case "create":
		sf := flag.NewFlagSet("project create", flag.ContinueOnError)
		sf.SetOutput(stdout)
		name := sf.String("name", "", "display name (required)")
		var origins, attrs multiFlag
		sf.Var(&origins, "origin", "allowed origin, `*` wildcards accepted (repeatable)")
		sf.Var(&attrs, "attr", "attribute key to break down (repeatable)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if *name == "" {
			fmt.Fprintln(stdout, "usage: twillingate project create -name <name> [-origin ...] [-attr ...]")
			return 2
		}
		p, err := ops.CreateProject(ctx, "cli", manage.ProjectSpec{
			Name: *name, AllowedOrigins: origins, Attributes: attrs})
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %d (%q) created\n", p.ID, p.Name)
		fmt.Fprintf(stdout, "next: twillingate key issue -project-id %d -label web\n", p.ID)
		return 0
	case "update":
		sf := flag.NewFlagSet("project update", flag.ContinueOnError)
		sf.SetOutput(stdout)
		id := sf.Int64("id", 0, "project id (required)")
		name := sf.String("name", "", "new display name")
		var origins, attrs multiFlag
		sf.Var(&origins, "origin", "allowed origin, replaces the whole list (repeatable)")
		clearOrigins := sf.Bool("clear-origins", false, "remove every allowed origin")
		sf.Var(&attrs, "attr", "attribute key to break down, replaces the whole list (repeatable)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if *id == 0 {
			fmt.Fprintln(stdout, "usage: twillingate project update -id <id> [-name ...] [-origin ... | -clear-origins] [-attr ...]")
			return 2
		}
		if *clearOrigins && len(origins) > 0 {
			fmt.Fprintln(stdout, "use -origin or -clear-origins, not both")
			return 2
		}
		// A flag left out is nil and keeps the current list; -clear-origins
		// sends an empty non-nil list, which clears it.
		spec := manage.ProjectSpec{ID: *id, Name: *name,
			AllowedOrigins: origins, Attributes: attrs}
		if *clearOrigins {
			spec.AllowedOrigins = []string{}
		}
		p, err := ops.UpdateProject(ctx, "cli", spec)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %d (%q) updated\n", p.ID, p.Name)
		return 0
	case "list":
		for _, p := range ops.Reg.Snapshot(ctx).Projects() {
			state := ""
			if p.Archived {
				state = "\t(archived)"
			}
			fmt.Fprintf(stdout, "%d\t%s%s\n", p.ID, p.Name, state)
		}
		return 0
	case "archive", "restore":
		sf := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
		sf.SetOutput(stdout)
		id := sf.Int64("id", 0, "project id (required)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if *id == 0 {
			fmt.Fprintf(stdout, "usage: twillingate project %s -id <id>\n", sub)
			return 2
		}
		var err error
		if sub == "archive" {
			err = ops.ArchiveProject(ctx, "cli", *id)
		} else {
			err = ops.RestoreProject(ctx, "cli", *id)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %d %sd\n", *id, sub)
		return 0
	default: // delete
		sf := flag.NewFlagSet("project delete", flag.ContinueOnError)
		sf.SetOutput(stdout)
		id := sf.Int64("id", 0, "project id (required)")
		force := sf.Bool("force", false, "skip confirmation")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if *id == 0 {
			fmt.Fprintln(stdout, "usage: twillingate project delete -id <id> [-force]")
			return 2
		}
		if !*force {
			fmt.Fprintf(stdout, "This permanently deletes project %d and ALL its data.\n", *id)
			fmt.Fprintln(stdout, "Re-run with -force to confirm.")
			return 1
		}
		if err := ops.DeleteProject(ctx, "cli", *id); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %d deleted\n", *id)
		return 0
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
