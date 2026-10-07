package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func init() { commands["form"] = cmdForm }

const formUsage = "usage: twillingate form <list|approve|update|archive|restore|export|erase> [flags]"

// splitFields turns "a, b,,c" into [a b c].
func splitFields(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseClosesAt reads -closes-at: an RFC 3339 time, "now", or "never"
// (reopen: a non-nil pointer to nil, as manage.FormSpec reads it).
func parseClosesAt(v string) (**time.Time, error) {
	var t *time.Time
	switch v {
	case "never":
	case "now":
		n := time.Now().UTC()
		t = &n
	default:
		p, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("-closes-at: want an RFC 3339 time, now or never, got %q", v)
		}
		p = p.UTC()
		t = &p
	}
	return &t, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func cmdForm(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("form", flag.ContinueOnError)
	fs.SetOutput(stdout)
	envFile := fs.String("env-file", "", "load environment from this file (real env wins)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stdout, formUsage)
		return 2
	}
	sub, subArgs := rest[0], rest[1:]
	switch sub {
	case "list", "approve", "update", "archive", "restore", "export", "erase":
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\n%s\n", sub, formUsage)
		return 2
	}

	sf := flag.NewFlagSet("form "+sub, flag.ContinueOnError)
	sf.SetOutput(stdout)
	project := sf.Int64("project-id", 0, "project id (required)")
	var name, fields, purpose, returnURL, closesAt, search *string
	var archived *bool
	var ids multiFlag
	switch sub {
	case "list":
		archived = sf.Bool("archived", false, "list the archived forms instead")
	case "erase":
		sf.Var(&ids, "id", "submission id to erase (repeatable)")
		search = sf.String("search", "", "erase every submission with a field value containing this text (at least 2 characters)")
	default:
		name = sf.String("name", "", "form name (required)")
	}
	switch sub {
	case "approve":
		fields = sf.String("fields", "", "comma-separated fields to keep (required)")
	case "update":
		purpose = sf.String("purpose", "", "what the form is for")
		returnURL = sf.String("return-url", "", "where a plain HTML form sends the visitor back; empty clears it")
		closesAt = sf.String("closes-at", "", "RFC 3339 time after which submissions are refused, now, or never (reopen)")
		fields = sf.String("fields", "", "comma-separated fields an approved form keeps")
	}
	if err := sf.Parse(subArgs); err != nil {
		return 2
	}
	usageLine := "usage: twillingate form " + sub + " -project-id <id>"
	switch sub {
	case "approve":
		usageLine += " -name <name> -fields a,b"
	case "update":
		usageLine += " -name <name> [-purpose ...] [-return-url ...] [-closes-at RFC3339|now|never] [-fields a,b]"
	case "list":
		usageLine += " [-archived]"
	case "erase":
		usageLine += " (-id <id> ... | -search <text>)"
	default:
		usageLine += " -name <name>"
	}
	if *project == 0 || (name != nil && *name == "") {
		fmt.Fprintln(stdout, usageLine)
		return 2
	}

	// Which update flags were given: an omitted one keeps the value, an
	// empty one clears it.
	given := map[string]bool{}
	sf.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var spec manage.FormSpec
	switch sub {
	case "approve":
		if len(splitFields(*fields)) == 0 {
			fmt.Fprintln(stdout, usageLine)
			return 2
		}
	case "update":
		if given["purpose"] {
			spec.Purpose = purpose
		}
		if given["return-url"] {
			spec.ReturnURL = returnURL
		}
		if given["closes-at"] {
			c, err := parseClosesAt(*closesAt)
			if err != nil {
				fmt.Fprintln(stdout, err)
				return 2
			}
			spec.ClosesAt = c
		}
		if given["fields"] {
			f := splitFields(*fields)
			spec.ExpectedFields = &f
		}
		if spec == (manage.FormSpec{}) {
			fmt.Fprintf(stdout, "nothing to update\n%s\n", usageLine)
			return 2
		}
	case "erase":
		if (len(ids) == 0) == (*search == "") {
			fmt.Fprintf(stdout, "give exactly one of -id and -search\n%s\n", usageLine)
			return 2
		}
	}

	ops, cfg, closeStore, code := openOps(stdout, *envFile)
	if code != 0 {
		return code
	}
	defer closeStore()
	ctx := context.Background()
	fail := func(err error) int {
		fmt.Fprintln(stdout, err)
		return 1
	}

	switch sub {
	case "list":
		forms, err := ops.ListForms(ctx, *project, *archived)
		if err != nil {
			return fail(err)
		}
		for _, f := range forms {
			shown, closes := f.Fields, "-"
			if f.Status == store.FormApproved {
				shown = f.ExpectedFields
			}
			if f.ClosesAt != nil {
				closes = f.ClosesAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(stdout, "%s\t%s\t%d\t%s\t%s\n", f.Name, f.Status, f.Submissions, closes, strings.Join(shown, ","))
		}
	case "approve":
		if err := ops.ApproveForm(ctx, "cli", *project, *name, splitFields(*fields)); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "form %q approved\n", *name)
	case "update":
		if err := ops.UpdateForm(ctx, "cli", *project, *name, spec); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "form %q updated\n", *name)
	case "archive":
		if err := ops.ArchiveForm(ctx, "cli", *project, *name); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "form %q archived\n", *name)
	case "restore":
		if err := ops.RestoreForm(ctx, "cli", *project, *name); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "form %q restored\n", *name)
	case "export":
		f, err := ops.ActiveForm(ctx, *project, *name)
		if err != nil {
			return fail(err)
		}
		db, err := readsql.Open(app.DatabasePath(cfg.Database), cfg.Console.QueryTimeout, cfg.Console.QueryMaxRows)
		if err != nil {
			return fail(err)
		}
		defer db.Close()
		// Read everything before writing a byte, so a failure leaves
		// stdout empty rather than a truncated file.
		_, header, rows, err := manage.AllSubmissions(ctx, db, f, readsql.Page{})
		if err != nil {
			return fail(err)
		}
		header, rows = manage.CSVSafeTable(header, rows)
		w := csv.NewWriter(stdout)
		w.Write(header)
		for _, r := range rows {
			w.Write(r)
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return fail(err)
		}
	case "erase":
		// Never echo the search text or the ids: they are the erased
		// person's data and the output ends up in shell history and logs.
		var n int
		var err error
		if len(ids) > 0 {
			n, err = ops.DeleteSubmissions(ctx, "cli", *project, ids, "ids")
		} else {
			var matched []string
			if matched, err = ops.SubmissionIDsMatching(ctx, *project, *search); err == nil {
				n, err = ops.DeleteSubmissions(ctx, "cli", *project, matched, "search")
			}
		}
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%s erased\n", plural(n, "submission", "submissions"))
	}
	return 0
}
