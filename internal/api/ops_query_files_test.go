package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// fakeFiles stands in for archive.Reader: what query hands the files and
// what they answer.
type fakeFiles struct {
	sql      string
	maxRows  int
	project  int64
	rules    archive.Rules
	deadline bool
	res      engine.Result
	err      error
}

func (f *fakeFiles) QueryScoped(ctx context.Context, q archive.Scoped, rules archive.Rules) (engine.Result, error) {
	_, f.deadline = ctx.Deadline()
	f.sql, f.maxRows, f.project, f.rules = q.SQL, q.MaxRows, q.Project, rules
	return f.res, f.err
}

func TestQueryFilesSourceRunsOnTheFiles(t *testing.T) {
	h, cs := newTestHost(t)
	files := &fakeFiles{res: engine.Result{
		Columns: []string{"n", "plan", "ok", "ts", "list"},
		Rows:    [][]any{{json.Number("3"), nil, true, "2026-10-12T10:00:00Z", []any{"a", json.Number("1")}}},
	}}
	h.files = files
	res := callTool(t, cs, "query", map[string]any{"sql": "SELECT count(*) FROM events", "source": "files"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	var out tableOut
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatal(err)
	}
	if want := `[["3","","true","2026-10-12T10:00:00Z","[\"a\",1]"]]`; mustJSON(t, out.Rows) != want {
		t.Fatalf("rows = %s, want %s", mustJSON(t, out.Rows), want)
	}
	if files.sql != "SELECT count(*) FROM events" || files.maxRows != h.db.MaxRows() || !files.deadline || files.project != 0 {
		t.Fatalf("files got sql %q, maxRows %d, deadline %v, project %d; want the SQL, CONSOLE_QUERY_ROWS_MAX, a deadline and every project",
			files.sql, files.maxRows, files.deadline, files.project)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestQueryFilesSourcePassesTheRules is Review Focus 5 at the API: the
// registry's rules reach the tables.
func TestQueryFilesSourcePassesTheRules(t *testing.T) {
	h, cs := newTestHost(t)
	files := &fakeFiles{}
	h.files = files
	ctx := context.Background()
	if _, err := h.ops.BlockEvent(ctx, "t", 1, "product", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ops.BlockAttribute(ctx, "t", 1, "k"); err != nil {
		t.Fatal(err)
	}
	if res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1", "source": "files"}); res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if len(files.rules.Events) != 1 || files.rules.Events[0].Name != "x" ||
		len(files.rules.Attributes) != 1 || files.rules.Attributes[0].Key != "k" {
		t.Fatalf("rules = %+v, want the block on x and on k", files.rules)
	}
}

func TestQueryFilesSourceErrors(t *testing.T) {
	h, cs := newTestHost(t)
	for _, c := range []struct {
		name string
		err  error
		want []string
	}{
		{"timeout", context.DeadlineExceeded, []string{"exceeded", "CONSOLE_QUERY_TIMEOUT"}},
		{"memory", &engine.QueryError{Msg: "Out of Memory Error: failed to allocate data of size 16.0 MiB (242.3 MiB/244.1 MiB used)"},
			[]string{"ENGINE_MEMORY_LIMIT", "GROUP BY"}},
		{"sqlite table", &engine.QueryError{Msg: "Catalog Error: Table with name v_views_daily does not exist!\nDid you mean \"pg_views\"?"},
			[]string{"v_views_daily", `source "sqlite"`}},
		{"sqlite table upper case", &engine.QueryError{Msg: "Catalog Error: Table with name V_VIEWS_DAILY does not exist!"},
			[]string{"V_VIEWS_DAILY", `source "sqlite"`}},
		{"memory loading the logs", fmt.Errorf("%w: %w", archive.ErrLoadingLogs, &engine.QueryError{Msg: "Out of Memory Error: failed to allocate data of size 16.0 MiB"}),
			[]string{"open logs", "ENGINE_MEMORY_LIMIT", "raise"}},
		{"child died", engine.ErrChildExited, []string{"stopped", "ENGINE_MEMORY_LIMIT"}},
		{"sql", &engine.QueryError{Msg: `Binder Error: Referenced column "nope" not found`}, []string{"DuckDB", `"nope"`}},
	} {
		h.files = &fakeFiles{err: c.err}
		res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1", "source": "files"})
		msg := textOf(res)
		if !res.IsError {
			t.Errorf("%s: accepted", c.name)
		}
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%s: message %q lacks %q", c.name, msg, w)
			}
		}
		if c.name == "memory loading the logs" && strings.Contains(msg, "COUNT(DISTINCT") {
			t.Errorf("%s: message %q advises on a query the logs' load was not", c.name, msg)
		}
	}
}

func TestQuerySourceChoices(t *testing.T) {
	h, cs := newTestHost(t)
	if res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1", "source": "duck"}); !res.IsError || !strings.Contains(textOf(res), `source must be "files" or "sqlite"`) {
		t.Errorf("source duck: %s", textOf(res))
	}
	// No source reads the files: the host's own, where blog's 2026-08-20
	// views are 45.
	res := callTool(t, cs, "query", map[string]any{"sql": "SELECT count(*) AS n FROM views WHERE project_id = 1 AND day = DATE '2026-08-20'"})
	if res.IsError || !strings.Contains(textOf(res), `[["45"]]`) {
		t.Errorf("no source: %s", textOf(res))
	}
	// sqlite, deprecated, still reads the views and never the files.
	h.files = &fakeFiles{err: errors.New("must not be called")}
	res = callTool(t, cs, "query", map[string]any{"sql": "SELECT project_id FROM v_views_daily LIMIT 1", "source": "sqlite"})
	if res.IsError {
		t.Errorf("source sqlite: %s", textOf(res))
	}
	h.files = nil
	for _, source := range []string{"", "files"} {
		if res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1", "source": source}); !res.IsError || !strings.Contains(textOf(res), "cannot be queried") {
			t.Errorf("source %q without a reader: %s", source, textOf(res))
		}
	}
}
