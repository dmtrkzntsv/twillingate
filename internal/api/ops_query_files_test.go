package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// fakeFiles stands in for the external SQL engine: what query sends it
// and what it answers.
type fakeFiles struct {
	req      sqlengine.Request
	deadline bool
	res      sqlengine.Result
	err      error
}

func (f *fakeFiles) Query(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error) {
	_, f.deadline = ctx.Deadline()
	f.req = req
	return f.res, f.err
}

func TestQueryFilesSourceRunsOnTheFiles(t *testing.T) {
	h, cs := newTestHost(t)
	files := &fakeFiles{res: sqlengine.Result{
		Columns: []string{"n", "plan", "ok", "ts", "list"},
		Rows:    [][]any{{json.Number("3"), nil, true, "2026-10-12T10:00:00Z", []any{"a", json.Number("1")}}},
	}}
	h.sql = files
	res := callTool(t, cs, "query", map[string]any{"sql": "SELECT count(*) FROM events"})
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
	if r := files.req; r.SQL != "SELECT count(*) FROM events" || r.Mode != sqlengine.ModeRows || r.MaxRows != h.queryRows || !files.deadline ||
		r.Scope != nil || !r.Confine || len(r.Args) != 0 {
		t.Fatalf("the engine got %+v, deadline %v; want the SQL as rows, CONSOLE_QUERY_ROWS_MAX, a deadline, every project and the seven tables only",
			r, files.deadline)
	}
	// Every project's files (blog's: docs has none), by paths relative to
	// EVENTS_DIR.
	if ps := files.req.Tables.Projects; len(ps) != 1 || ps[0].ID != 1 || len(ps[0].Parquet) == 0 || filepath.IsAbs(ps[0].Parquet[0]) {
		t.Errorf("tables = %+v, want blog's files, relative", files.req.Tables)
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
	h.sql = files
	ctx := context.Background()
	if _, err := h.ops.BlockEvent(ctx, "t", 1, "product", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ops.BlockAttribute(ctx, "t", 1, "k"); err != nil {
		t.Fatal(err)
	}
	if res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1"}); res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	rules := files.req.Tables.Rules
	if len(rules.Events) != 1 || rules.Events[0].Name != "x" ||
		len(rules.Attributes) != 1 || rules.Attributes[0].Key != "k" {
		t.Fatalf("rules = %+v, want the block on x and on k", rules)
	}
}

func TestQueryFilesSourceErrors(t *testing.T) {
	h, cs := newTestHost(t)
	kind := func(k sqlengine.Kind, msg string) error {
		return &sqlengine.Error{Status: k.Status(), Kind: k, Message: msg}
	}
	for _, c := range []struct {
		name string
		err  error
		want []string
	}{
		{"timeout", context.DeadlineExceeded, []string{"exceeded", "CONSOLE_QUERY_TIMEOUT"}},
		{"engine timeout", kind(sqlengine.KindTimeout, "the query exceeded X-Timeout-Ms"), []string{"exceeded", "CONSOLE_QUERY_TIMEOUT"}},
		{"memory", kind(sqlengine.KindOutOfMemory, "Out of Memory Error: failed to allocate data of size 16.0 MiB (242.3 MiB/244.1 MiB used)"),
			[]string{"ENGINE_MEMORY_LIMIT", "GROUP BY"}},
		{"sqlite table", kind(sqlengine.KindSQL, "Catalog Error: Table with name v_views_daily does not exist!\nDid you mean \"pg_views\"?"),
			[]string{"v_views_daily", "docs://twillingate"}},
		{"sqlite table upper case", kind(sqlengine.KindSQL, "Catalog Error: Table with name V_VIEWS_DAILY does not exist!"),
			[]string{"V_VIEWS_DAILY", "docs://twillingate"}},
		{"memory loading the logs", kind(sqlengine.KindOutOfMemoryLoadingLog, "Out of Memory Error: failed to allocate data of size 16.0 MiB"),
			[]string{"open logs", "ENGINE_MEMORY_LIMIT", "raise"}},
		{"child died", kind(sqlengine.KindExited, "engine: child exited"), []string{"stopped", "ENGINE_MEMORY_LIMIT"}},
		{"busy", kind(sqlengine.KindBusy, "no free slot; retry later"), []string{"busy", "retry"}},
		{"stale", kind(sqlengine.KindStale, "a file in the table spec is gone"), []string{"changed under the query", "retry"}},
		{"internal", kind(sqlengine.KindInternal, "spawn failed"), []string{"SQL engine failed", "SQL_ENGINE_SOCKET"}},
		{"unavailable", fmt.Errorf("%w: dial unix: connection refused", sqlengine.ErrUnavailable), []string{"SQL engine failed"}},
		{"sql", kind(sqlengine.KindSQL, `Binder Error: Referenced column "nope" not found`), []string{"DuckDB", `"nope"`}},
		{"confined", kind(sqlengine.KindRefused, "main.events names a schema; SQL here reads only …"), []string{"main.events names a schema"}},
		{"confined sqlite table", kind(sqlengine.KindRefused, `"v_views_daily" is not a table here; SQL here reads only …`), []string{"v_views_daily", "docs://twillingate"}},
	} {
		h.sql = &fakeFiles{err: c.err}
		res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1"})
		msg := textOf(res)
		if !res.IsError {
			t.Errorf("%s: accepted", c.name)
		}
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%s: message %q lacks %q", c.name, msg, w)
			}
		}
		if strings.Contains(msg, `source "sqlite"`) {
			t.Errorf("%s: message %q points at the removed sqlite source", c.name, msg)
		}
		if c.name == "memory loading the logs" && strings.Contains(msg, "COUNT(DISTINCT") {
			t.Errorf("%s: message %q advises on a query the logs' load was not", c.name, msg)
		}
	}
}

// TestQueryReadsOnlyTheTables: on the host's own files, query refuses SQL
// that reads a file or the catalog past the seven tables, as invalid with
// the check's message, and runs SQL over them across every project.
func TestQueryReadsOnlyTheTables(t *testing.T) {
	_, cs := newTestHost(t)
	for sql, want := range map[string]string{
		"SELECT * FROM read_parquet('/etc/passwd')": "read_parquet() is not allowed",
		"SELECT * FROM glob('/*')":                  "glob() is not allowed",
		"SELECT count(*) FROM main.events":          "main.events names a schema",
		`SELECT count(*) FROM "main"."events"`:      "main.events names a schema",
		"SELECT count(*) FROM _tw_all":              `"_tw_all" is not a table here`,
		"SELECT * FROM information_schema.tables":   "information_schema.tables names a schema",
		"SELECT * FROM 'raw/1/2026-08.parquet'":     `"raw/1/2026-08.parquet" is not a table here`,
	} {
		res := callTool(t, cs, "query", map[string]any{"sql": sql})
		if msg := textOf(res); !res.IsError || !strings.Contains(msg, want) || strings.Contains(msg, "DuckDB") {
			t.Errorf("%s: %v %q; want the refusal %q", sql, res.IsError, msg, want)
		}
	}
	res := callTool(t, cs, "query", map[string]any{"sql": "SELECT count(*) > 0 AS some FROM events WHERE kind IS DISTINCT FROM '/etc/passwd'"})
	if msg := textOf(res); res.IsError || !strings.Contains(msg, `"true"`) {
		t.Errorf("query over events: %s; want its rows", msg)
	}
}

func TestQueryReadsTheFiles(t *testing.T) {
	h, cs := newTestHost(t)
	// The host's own files, where blog's 2026-08-20 views are 45.
	res := callTool(t, cs, "query", map[string]any{"sql": "SELECT count(*) AS n FROM views WHERE project_id = 1 AND day = DATE '2026-08-20'"})
	if res.IsError || !strings.Contains(textOf(res), `[["45"]]`) {
		t.Errorf("query: %s", textOf(res))
	}
	h.sql = nil
	if res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1"}); !res.IsError ||
		textOf(res) != "SQL needs an external SQL engine (SQL_ENGINE_SOCKET)" {
		t.Errorf("without an engine: %s", textOf(res))
	}
	rec := serveREST(t, newTestRegistrar(t, h), "POST", "/api/query", `{"sql":"SELECT 1"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"invalid"`) ||
		!strings.Contains(rec.Body.String(), "SQL needs an external SQL engine (SQL_ENGINE_SOCKET)") {
		t.Errorf("REST without an engine = %d %s, want 400 invalid", rec.Code, rec.Body)
	}
}

// vanishing deletes the first Parquet file of the spec it is sent, once,
// before it passes the query on: the daily pass removing a file between
// the spec and the engine's read.
type vanishing struct {
	SQL
	dir     string
	specs   []sqlengine.Tables
	deleted string
}

func (v *vanishing) Query(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error) {
	v.specs = append(v.specs, req.Tables)
	if v.deleted == "" {
		for _, p := range req.Tables.Projects {
			if len(p.Parquet) > 0 {
				v.deleted = p.Parquet[0]
				if err := os.Remove(filepath.Join(v.dir, filepath.FromSlash(v.deleted))); err != nil {
					return sqlengine.Result{}, err
				}
				break
			}
		}
	}
	return v.SQL.Query(ctx, req)
}

// TestQueryRetriesAStaleSpec: a file the daily pass removed between the
// table spec and the engine's read answers stale, and query sends a
// fresh spec, without that file, which the engine answers.
func TestQueryRetriesAStaleSpec(t *testing.T) {
	h, cs := newTestHost(t)
	v := &vanishing{SQL: h.sql, dir: h.eventsDir}
	h.sql = v
	res := callTool(t, cs, "query", map[string]any{"sql": "SELECT count(*) AS n FROM events"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if v.deleted == "" || len(v.specs) != 2 || slices.ContainsFunc(v.specs[1].Projects, func(p sqlengine.Project) bool {
		return slices.Contains(p.Parquet, v.deleted)
	}) {
		t.Errorf("deleted %q, sent %d specs; want a second without it", v.deleted, len(v.specs))
	}
}
