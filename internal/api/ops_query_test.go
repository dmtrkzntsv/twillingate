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
		`{"project_id":1,"query":{"kind":"trend","table":"views","series":"path","series_limit":51,"metrics":[{"op":"count"}]}}`:    "series_limit",
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
	fake.err = fmt.Errorf("%w (about 300 MiB counted, 128 MiB allowed)", native.ErrTooLarge)
	rec = serveREST(t, newTestRegistrar(t, h), "POST", "/api/query",
		`{"project_id":1,"query":{"kind":"stat","table":"views","metrics":[{"op":"daily_sum","of":"actors"}]}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tooLarge) {
		t.Errorf("ErrTooLarge = %d %s, want 400 with %q", rec.Code, rec.Body, tooLarge)
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
	// compare "" is no comparison, over MCP as over REST.
	if e := r.specs[i].in.Properties["query"].Properties["compare"].Enum; !slices.Equal(e, []any{"", "previous"}) {
		t.Errorf("compare's enum = %v, want \"\" and previous", e)
	}
	// A retention's and an attributes' own fields.
	rq := r.specs[i].in.Properties["query"]
	if e := rq.Properties["output"].Enum; !slices.Equal(e, []any{"grid", "curve", "milestones", "values"}) {
		t.Errorf("output's enum = %v", e)
	}
	if e := rq.Properties["identified_by"].Enum; !slices.Equal(e, []any{"install", "user"}) {
		t.Errorf("identified_by's enum = %v", e)
	}
	if o := rq.Properties["offsets"]; o.Minimum == nil || *o.Minimum != 1 || o.Maximum == nil || *o.Maximum != 90 {
		t.Errorf("offsets' bounds = %v..%v, want 1..90", o.Minimum, o.Maximum)
	}
	if m := rq.Properties["milestones"]; m.MaxItems == nil || *m.MaxItems != 6 || m.Items.Minimum == nil || *m.Items.Minimum != 0 {
		t.Errorf("milestones = %+v, want at most 6, each from 0", m)
	}
	if slices.Contains(rq.Required, "metrics") || slices.Contains(rq.Required, "table") {
		t.Errorf("query requires %v: a retention has no metrics, nor needs a table", rq.Required)
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
	// A metric's own filters and its round.
	m := r.specs[i].in.Properties["query"].Properties["metrics"].Items
	if e := m.Properties["where"].Items.Properties["op"].Enum; len(e) != len(native.FilterOps) {
		t.Errorf("a metric's where op enum = %v, want the filter operators", e)
	}
	if rd := m.Properties["round"]; rd.Minimum == nil || *rd.Minimum != 0 || rd.Maximum == nil || *rd.Maximum != 6 {
		t.Errorf("round's bounds = %v..%v, want 0..6", rd.Minimum, rd.Maximum)
	}
	if q := m.Properties["q"]; q.ExclusiveMinimum == nil || *q.ExclusiveMinimum != 0 || q.ExclusiveMaximum == nil || *q.ExclusiveMaximum != 1 {
		t.Errorf("q's bounds = %v..%v, want above 0, below 1", q.ExclusiveMinimum, q.ExclusiveMaximum)
	}
}

// A share's of, count or weight, passes MCP's schema, and vital and
// vital_rating are dimensions of measures.
func TestQueryStructuredShare(t *testing.T) {
	_, cs := newTestHost(t)
	q := map[string]any{"kind": "breakdown", "table": "measures", "dimensions": []any{"vital", "vital_rating"}, "order": "vital",
		"metrics": []any{map[string]any{"op": "share", "of": "weight"}, map[string]any{"op": "share", "of": "count", "round": 3}}}
	if res := callTool(t, cs, "query", map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-21", "query": q}); res.IsError {
		t.Errorf("a share over MCP: %s", textOf(res))
	}
	q["metrics"] = []any{map[string]any{"op": "share", "of": "rows"}}
	if res := callTool(t, cs, "query", map[string]any{"project_id": 1, "query": q}); !res.IsError {
		t.Error("share of rows over MCP: accepted")
	}
}

// A percentile's q passes MCP's schema when within (0, 1), and is refused
// past it.
func TestQueryStructuredPercentile(t *testing.T) {
	_, cs := newTestHost(t)
	q := map[string]any{"kind": "stat", "table": "measures", "metrics": []any{
		map[string]any{"op": "percentile", "q": 0.75}, map[string]any{"op": "mean", "round": 3}, map[string]any{"op": "weight"}}}
	if res := callTool(t, cs, "query", map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-21", "query": q}); res.IsError {
		t.Errorf("a percentile over MCP: %s", textOf(res))
	}
	q["metrics"] = []any{map[string]any{"op": "percentile", "q": 1}}
	if res := callTool(t, cs, "query", map[string]any{"project_id": 1, "query": q}); !res.IsError {
		t.Error("q 1 over MCP: accepted")
	}
}

// A metric's where and round pass MCP's schema.
func TestQueryStructuredMetricWhere(t *testing.T) {
	_, cs := newTestHost(t)
	q := map[string]any{"kind": "stat", "table": "views", "metrics": []any{
		map[string]any{"op": "count", "as": "web", "round": 2, "where": []any{map[string]any{"column": "kind", "op": "eq", "values": []any{"web"}}}}}}
	if res := callTool(t, cs, "query", map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-21", "query": q}); res.IsError {
		t.Errorf("a metric's where over MCP: %s", textOf(res))
	}
	q["metrics"] = []any{map[string]any{"op": "count", "round": 7}}
	if res := callTool(t, cs, "query", map[string]any{"project_id": 1, "query": q}); !res.IsError {
		t.Error("round 7 over MCP: accepted")
	}
}

// compare "" (no comparison) passes MCP's schema as it passes REST.
func TestQueryStructuredCompareEmpty(t *testing.T) {
	_, cs := newTestHost(t)
	q := map[string]any{"kind": "stat", "table": "views", "compare": "", "metrics": []any{map[string]any{"op": "count"}}}
	if res := callTool(t, cs, "query", map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-21", "query": q}); res.IsError {
		t.Errorf("compare \"\" over MCP: %s", textOf(res))
	}
}
