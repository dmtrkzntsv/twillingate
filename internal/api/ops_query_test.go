package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
)

// source was removed one release after the database's events: it is an
// unknown field, refused over MCP and REST before anything is read, "files"
// included.
func TestQuerySourceIsRefused(t *testing.T) {
	h, cs := newTestHost(t)
	h.files = &fakeFiles{err: errors.New("must not be called")}
	for _, source := range []string{"files", "sqlite"} {
		if res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1", "source": source}); !res.IsError ||
			strings.Contains(textOf(res), "must not be called") {
			t.Errorf("MCP source %s = %s, want refused before the query", source, textOf(res))
		}
		rec := serveREST(t, newTestRegistrar(t, h), "POST", "/api/query", `{"sql":"SELECT 1","source":"`+source+`"}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `unknown field \"source\"`) {
			t.Errorf("REST source %s = %d %s, want 400 for an unknown field", source, rec.Code, rec.Body)
		}
	}
}

func TestQueryToolCapsRows(t *testing.T) {
	h, cs := newTestHost(t)
	setGuards(t, h, h.queryTimeout, 1)
	res := callTool(t, cs, "query", map[string]any{"sql": "SELECT * FROM range(3)"})
	if res.IsError {
		t.Fatalf("files: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "truncated") || !strings.Contains(out, "PARTIAL") {
		t.Errorf("files: truncation not flagged: %s", out)
	}
}

// fakeNative records what the query tool asked the native engine.
type fakeNative struct {
	q        native.Query
	p        native.Params
	rules    archive.Rules
	deadline bool
	res      native.Result
	err      error
}

func (f *fakeNative) Answer(ctx context.Context, q native.Query, p native.Params, rules archive.Rules) (native.Result, error) {
	_, f.deadline = ctx.Deadline()
	f.q, f.p, f.rules = q, p, rules
	return f.res, f.err
}

// A structured breakdown through both transports answers the native
// engine's rows over the template's event files.
func TestQueryStructuredRuns(t *testing.T) {
	h, cs := newTestHost(t)
	q := map[string]any{"kind": "breakdown", "table": "views",
		"metrics": []any{map[string]any{"op": "count"}}, "dimensions": []any{"kind"}}
	res := callTool(t, cs, "query", map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-21", "query": q})
	if res.IsError {
		t.Fatalf("query: %s", textOf(res))
	}
	var out struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("%v: %s", err, textOf(res))
	}
	want := [][]string{{"web", "55"}, {"app", "20"}}
	if !slices.Equal(out.Columns, []string{"kind", "count"}) || len(out.Rows) != 2 ||
		!slices.Equal(out.Rows[0], want[0]) || !slices.Equal(out.Rows[1], want[1]) {
		t.Errorf("MCP = %v %v, want [kind count] %v", out.Columns, out.Rows, want)
	}
	body, _ := json.Marshal(map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-21", "query": q})
	rec := serveREST(t, newTestRegistrar(t, h), "POST", "/api/query", string(body))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `["web","55"]`) {
		t.Errorf("REST = %d %s", rec.Code, rec.Body)
	}
}

func TestQueryRefusesBoth(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	for _, body := range []string{
		`{"sql":"SELECT 1","project_id":1,"query":{"kind":"stat","table":"views","metrics":[{"op":"count"}]}}`,
		`{}`,
	} {
		rec := serveREST(t, r, "POST", "/api/query", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sql") || !strings.Contains(rec.Body.String(), "query") {
			t.Errorf("%s = %d %s, want 400 naming sql and query", body, rec.Code, rec.Body)
		}
	}
}

func TestQueryStructuredInvalid(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	for body, field := range map[string]string{
		`{"project_id":1,"query":{"kind":"breakdown","table":"views","metrics":[{"op":"count"}]}}`:                                  "dimensions",
		`{"project_id":1,"query":{"kind":"stat","table":"views","metrics":[{"op":"sum"}]}}`:                                         "metrics[0].column",
		`{"project_id":1,"from":"2026-08-21","to":"2026-08-20","query":{"kind":"stat","table":"views","metrics":[{"op":"count"}]}}`: "from",
		`{"project_id":1,"from":"2026-8-1","query":{"kind":"stat","table":"views","metrics":[{"op":"count"}]}}`:                     "YYYY-MM-DD",
		`{"project_id":1,"from":"2020-01-01","to":"2026-01-01","query":{"kind":"stat","table":"views","metrics":[{"op":"count"}]}}`: "1100",
	} {
		rec := serveREST(t, r, "POST", "/api/query", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
			t.Errorf("%s = %d %s, want 400 naming %s", body, rec.Code, rec.Body, field)
		}
	}
}

func TestQueryStructuredNeedsProject(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	q := `"query":{"kind":"stat","table":"views","metrics":[{"op":"count"}]}`
	if rec := serveREST(t, r, "POST", "/api/query", `{`+q+`}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "project_id") {
		t.Errorf("no project = %d %s, want 400 naming project_id", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "POST", "/api/query", `{"project_id":99,`+q+`}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d %s, want 404", rec.Code, rec.Body)
	}
	// sql takes none of the structured fields.
	if rec := serveREST(t, r, "POST", "/api/query", `{"sql":"SELECT 1","project_id":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("sql with project_id = %d %s, want 400", rec.Code, rec.Body)
	}
}

// The structured path defaults to the 30 days ending today, applies the
// registry's rules for every project, runs under the timeout, and caps the
// rows at queryRows, passing approximate through.
func TestQueryStructuredDefaultsAndCaps(t *testing.T) {
	h, _ := newTestHost(t)
	fake := &fakeNative{res: native.Result{Columns: []string{"kind", "actors"},
		Rows: [][]string{{"a", "1"}, {"b", "2"}, {"c", "3"}}, Approximate: []string{"actors"}}}
	h.native = fake
	setGuards(t, h, h.queryTimeout, 2)
	rec := serveREST(t, newTestRegistrar(t, h), "POST", "/api/query",
		`{"project_id":1,"query":{"kind":"breakdown","table":"views","metrics":[{"op":"actors"}],"dimensions":["kind"]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
	today := civil.Today(time.Now())
	if fake.p.Project != 1 || fake.p.To != today || fake.p.From != today.AddDays(-29) {
		t.Errorf("params = %+v, want project 1, %s..%s", fake.p, today.AddDays(-29), today)
	}
	if !fake.deadline {
		t.Error("ran without the query timeout")
	}
	var out struct {
		Rows        [][]string `json:"rows"`
		Truncated   bool       `json:"truncated"`
		Approximate []string   `json:"approximate"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 2 || !out.Truncated || !slices.Equal(out.Approximate, []string{"actors"}) {
		t.Errorf("out = %+v, want 2 rows, truncated, approximate [actors]", out)
	}
	fake.err = context.DeadlineExceeded
	rec = serveREST(t, newTestRegistrar(t, h), "POST", "/api/query",
		`{"project_id":1,"query":{"kind":"stat","table":"views","metrics":[{"op":"count"}]}}`)
	if want := enginesql.Timeout(h.queryTimeout); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
		t.Errorf("deadline = %d %s, want 400 with %q", rec.Code, rec.Body, want)
	}
	fake.err = fmt.Errorf("%w: where[0].values: bad", native.ErrInvalid)
	rec = serveREST(t, newTestRegistrar(t, h), "POST", "/api/query",
		`{"project_id":1,"query":{"kind":"stat","table":"views","metrics":[{"op":"count"}]}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "where[0].values") {
		t.Errorf("ErrInvalid = %d %s, want 400 naming the field", rec.Code, rec.Body)
	}
}

// The tool's input schema describes the structured query inline for an
// agent: the kinds, tables, metric and filter operators as enums.
func TestQuerySchemaDescribesStructured(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	i := slices.IndexFunc(r.specs, func(s spec) bool { return s.Name == "query" })
	if i < 0 {
		t.Fatal("no query tool")
	}
	in, _ := json.Marshal(r.specs[i].in)
	text := string(in)
	for _, want := range []string{`"breakdown"`, `"measures"`, `"daily_avg"`, `"not_in"`, `"is_set"`, `"project_id"`} {
		if !strings.Contains(text, want) {
			t.Errorf("query's input schema lacks %s: %s", want, text)
		}
	}
	col := r.specs[i].in.Properties["query"].Properties["where"].Items.Properties["column"]
	for _, c := range native.Columns {
		if !strings.Contains(col.Description, c) {
			t.Errorf("where's column description lacks %s: %s", c, col.Description)
		}
	}
	if strings.Contains(text, "$defs") || strings.Contains(text, "$ref") {
		t.Errorf("query's input schema is not inline: %s", text)
	}
}
