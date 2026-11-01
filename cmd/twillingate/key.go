package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

func init() { commands["key"] = cmdKey }

const keyUsage = "usage: twillingate key <issue|list|disable|enable> [flags]"

func cmdKey(args []string, stdout io.Writer) int {
	// No list of subcommands: an unknown one is refused after the store
	// is opened and migrated.
	envFile, sub, subArgs, ok := subcommand("key", keyUsage, nil, args, stdout)
	if !ok {
		return 2
	}
	ops, cfg, closeStore, err := openOps(envFile)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer closeStore()
	ctx := context.Background()
	sf := flag.NewFlagSet("key "+sub, flag.ContinueOnError)
	sf.SetOutput(stdout)
	projectID := sf.Int64("project-id", 0, "project id (required except for list)")
	label := sf.String("label", "", "key label")
	if err := sf.Parse(subArgs); err != nil {
		return 2
	}
	if sub != "list" && *projectID == 0 {
		fmt.Fprintf(stdout, "usage: twillingate key %s -project-id <id> -label <label>\n", sub)
		return 2
	}
	switch sub {
	case "issue":
		key, err := ops.IssueIngestKey(ctx, "cli", *projectID, *label)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		p := ops.Reg.Snapshot(ctx).Project(*projectID)
		if p == nil {
			fmt.Fprintf(stdout, "issued %s (label %q) but project %d vanished before the snippet could be built; run `twillingate key list` to confirm\n", key, *label, *projectID)
			return 1
		}
		fmt.Fprintf(stdout, "issued %s (label %q)\n\nWeb snippet:\n\n%s\n",
			key, *label, manage.Snippet(cfg.IngestURL, key))
		if cfg.IngestURL == "" {
			fmt.Fprintln(stdout, "\nnote: INGEST_URL is not set; replace "+manage.SnippetPlaceholderBase+" with your collector URL")
		}
		return 0
	case "list":
		_, ks, err := ops.St.LoadRegistry(ctx)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		for _, k := range ks {
			if *projectID != 0 && k.ProjectID != *projectID {
				continue
			}
			state := "active"
			if k.Disabled {
				state = "disabled"
			}
			fmt.Fprintf(stdout, "%d\t%s\t%s\t%s\n", k.ProjectID, k.Label, k.Key, state)
		}
		return 0
	case "disable", "enable":
		var err error
		if sub == "disable" {
			err = ops.DisableIngestKey(ctx, "cli", *projectID, *label)
		} else {
			err = ops.EnableIngestKey(ctx, "cli", *projectID, *label)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "key %d/%s %sd\n", *projectID, *label, sub)
		return 0
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\n%s\n", sub, keyUsage)
		return 2
	}
}
