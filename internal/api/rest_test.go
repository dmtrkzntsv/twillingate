package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

func serveREST(t *testing.T, r *registrar, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	r.rest.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("error body %q: %v", rec.Body.String(), err)
	}
	return b.Error.Code
}

func TestDecodeRequestFillsPathAndQuery(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/projects/1/views/breakdown?from=2026-08-20&to=2026-08-21&dimension=pages&limit=5", nil)
	req.SetPathValue("project_id", "1")
	var in breakdownIn
	if err := decodeRequest(req, &in); err != nil {
		t.Fatal(err)
	}
	if in.ProjectID != 1 || in.From != "2026-08-20" || in.To != "2026-08-21" || in.Dimension != "pages" || in.Limit != 5 {
		t.Errorf("decoded %+v", in)
	}
}

func TestDecodeRequestRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		req  *http.Request
		dst  any
	}{
		{"unknown query parameter", httptest.NewRequest("GET", "/x?form=2026-08-20", nil), &breakdownIn{}},
		{"bad int", httptest.NewRequest("GET", "/x?limit=many", nil), &breakdownIn{}},
		{"bad int64", httptest.NewRequest("GET", "/x?project_id=blog", nil), &listKeysIn{}},
		{"unknown body field", httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"a","colour":"red"}`)), &createProjectIn{}},
		{"not json", httptest.NewRequest("POST", "/x", strings.NewReader(`name=a`)), &createProjectIn{}},
		{"trailing data", httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"a"}{"name":"b"}`)), &createProjectIn{}},
		{"query on a POST", httptest.NewRequest("POST", "/x?name=a", strings.NewReader(`{}`)), &createProjectIn{}},
		{"body too large", httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"`+strings.Repeat("a", maxAPIBody)+`"}`)), &createProjectIn{}},
		{"bad bool", httptest.NewRequest("GET", "/x?skip_key=maybe", nil), &createProjectIn{}},
		{"slice from the URL", httptest.NewRequest("GET", "/x?allowed_origins=a", nil), &createProjectIn{}},
		// url.Values drops pairs it cannot parse, which would silently
		// remove a filter instead of refusing the request.
		{"malformed escape", httptest.NewRequest("GET", "/x?from=2026-08-20&event=signup%ZZ", nil), &productEventsIn{}},
		{"semicolon in a value", httptest.NewRequest("GET", "/x?from=2026-08-20&event=sign;up", nil), &productEventsIn{}},
		{"repeated parameter", httptest.NewRequest("GET", "/x?event=signup&event=login", nil), &productEventsIn{}},
	} {
		if err := decodeRequest(c.req, c.dst); !errors.Is(err, manage.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
		}
	}
}

// TestRESTRefusesMalformedFilter: a filter that does not parse is a 400,
// never an unfiltered 200 that looks like a real answer.
func TestRESTRefusesMalformedFilter(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	rec := serveREST(t, r, "GET", "/api/projects/1/product/events?from=2026-08-20&to=2026-08-21&event=signup%ZZ", "")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid" {
		t.Errorf("malformed filter = %d %s, want 400 invalid", rec.Code, rec.Body.String())
	}
}

// TestDecodeRequestPathOverridesBody: the path is authoritative. A PATCH
// on project 1 whose body names project 2 renames 1 and leaves 2 alone.
func TestDecodeRequestPathOverridesBody(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	if rec := serveREST(t, r, "PATCH", "/api/projects/1", `{"project_id":2,"name":"Blog"}`); rec.Code != 200 {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body.String())
	}
	s := h.reg.Snapshot(t.Context())
	if got := s.Project(1).Name; got != "Blog" {
		t.Errorf("project 1 name = %q, want Blog (the path)", got)
	}
	if got := s.Project(2).Name; got != "docs" {
		t.Errorf("project 2 name = %q, want docs untouched (the body's id must not win)", got)
	}
}

func TestDecodeRequestHeadReadsQuery(t *testing.T) {
	req := httptest.NewRequest("HEAD", "/x?skip_key=true", nil)
	var in createProjectIn
	if err := decodeRequest(req, &in); err != nil || !in.SkipKey {
		t.Errorf("decoded %+v, err %v", in, err)
	}
}

func TestWriteErrorMapping(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{invalidf("bad"), 400, "invalid"},
		{notFoundf("gone"), 404, "not_found"},
		{fmt.Errorf("wrapped: %w", manage.ErrConflict), 409, "conflict"},
		{errors.New("disk on fire"), 500, "internal"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/projects/1/views/overview", nil)
		var logs strings.Builder
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		writeError(rec, logger, req, c.err)
		if rec.Code != c.status || errorCode(t, rec) != c.code {
			t.Errorf("%v → %d %s, want %d %s", c.err, rec.Code, rec.Body.String(), c.status, c.code)
		}
		if c.code == "internal" && strings.Contains(rec.Body.String(), "disk on fire") {
			t.Errorf("internal error text leaked: %s", rec.Body.String())
		}
		if c.code == "internal" {
			if !strings.Contains(logs.String(), "method=GET") || !strings.Contains(logs.String(), "path=/api/projects/1/views/overview") {
				t.Errorf("internal error log = %q, want method and path", logs.String())
			}
		} else if logs.Len() != 0 {
			t.Errorf("typed refusal %v must not log: %q", c.err, logs.String())
		}
	}
}

// TestRESTMatchesMCP calls each routed read through both transports with
// the same input and requires identical JSON.
func TestRESTMatchesMCP(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	rng := "from=2026-08-20&to=2026-08-21"
	args := map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-21"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range args {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		tool, target string
		args         map[string]any
	}{
		{"list_projects", "/api/projects", map[string]any{}},
		{"views_overview", "/api/projects/1/views/overview?" + rng, args},
		{"views_breakdown", "/api/projects/1/views/breakdown?dimension=paths&" + rng, with(map[string]any{"dimension": "paths"})},
		{"retention", "/api/projects/1/retention?actor=user&" + rng, with(map[string]any{"actor": "user"})},
		{"product_events", "/api/projects/1/product/events?" + rng, args},
		{"product_attributes", "/api/projects/1/product/attributes?" + rng, args},
		{"identities", "/api/projects/1/identities?kind=user&" + rng, with(map[string]any{"kind": "user"})},
		{"list_ingest_keys", "/api/keys", map[string]any{}},
	} {
		rec := serveREST(t, r, "GET", c.target, "")
		if rec.Code != 200 {
			t.Errorf("%s: GET %s = %d %s", c.tool, c.target, rec.Code, rec.Body.String())
			continue
		}
		res := callTool(t, cs, c.tool, c.args)
		if res.IsError {
			t.Errorf("%s: MCP error %s", c.tool, textOf(res))
			continue
		}
		want, _ := json.Marshal(res.StructuredContent)
		var gotV, wantV any
		json.Unmarshal(rec.Body.Bytes(), &gotV)
		json.Unmarshal(want, &wantV)
		if fmt.Sprint(gotV) != fmt.Sprint(wantV) {
			t.Errorf("%s: REST %s\n MCP %s", c.tool, rec.Body.String(), want)
		}
	}
}

func TestRESTWritesAndRefusals(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)

	rec := serveREST(t, r, "POST", "/api/projects", `{"name":"shop","allowed_origins":["https://shop.example.com"]}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"key":"ak_`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ProjectID int64 `json:"project_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ProjectID != 3 {
		t.Fatalf("create returned project_id %d (%v), want 3 after the fixture's 1 and 2: %s", created.ProjectID, err, rec.Body.String())
	}
	shop := "/api/projects/3"
	if rec := serveREST(t, r, "PATCH", shop, `{"name":"Shop"}`); rec.Code != 200 {
		t.Errorf("patch = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", shop+"/keys", `{"label":"ios"}`); rec.Code != http.StatusCreated {
		t.Errorf("issue key = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", shop+"/keys", `{"label":"ios"}`); rec.Code != 409 || errorCode(t, rec) != "conflict" {
		t.Errorf("duplicate key label = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", shop+"/keys/ios/disable", ""); rec.Code != 200 {
		t.Errorf("disable key = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", shop+"/archive", ""); rec.Code != 200 {
		t.Errorf("archive = %d %s", rec.Code, rec.Body.String())
	}
	rec = serveREST(t, r, "GET", "/api/projects/99/views/overview?from=2026-08-20&to=2026-08-21", "")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "valid projects: 1 (blog), 2 (docs)") {
		t.Errorf("unknown project = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "GET", "/api/projects/1/views/overview?from=2026-08-20&to=2026-08-21&colour=red", ""); rec.Code != 400 {
		t.Errorf("unknown parameter = %d", rec.Code)
	}
	if rec := serveREST(t, r, "POST", "/api/query", `{"sql":"SELECT day, visitors FROM v_views_daily WHERE project_id=1"}`); rec.Code != 200 {
		t.Errorf("query = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/query", `{"sql":"ATTACH 'x' AS y"}`); rec.Code != 400 {
		t.Errorf("attach = %d", rec.Code)
	}
	if rec := serveREST(t, r, "GET", "/api/schema/views", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "v_views_daily") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("schema/views = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := serveREST(t, r, "GET", "/api/projects/1/integration-guide?platform=web", ""); rec.Code != 404 {
		t.Errorf("integration guide must be MCP-only, got %d", rec.Code)
	}

	var actor string
	if err := h.db.QueryRow(`SELECT actor FROM audit_log WHERE action='project.create' AND subject='3'`).Scan(&actor); err != nil || actor != "api" {
		t.Errorf("audit actor = %q, %v; want api", actor, err)
	}
	// The first key is issued by the create itself, not a follow-up call.
	if err := h.db.QueryRow(`SELECT actor FROM audit_log WHERE action='key.issue' AND subject='3/default'`).Scan(&actor); err != nil || actor != "api" {
		t.Errorf("first-key audit actor = %q, %v; want api", actor, err)
	}
	// A refused create leaves no project behind, and no id is consumed:
	// the retry lands on the next id.
	if rec := serveREST(t, r, "POST", "/api/projects", `{"name":"bad","identity":"sometimes"}`); rec.Code != 400 {
		t.Errorf("bad identity = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects", `{"name":"retry"}`); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"project_id":4`) {
		t.Errorf("retry after a refusal = %d %s, want 201 as project 4", rec.Code, rec.Body.String())
	}
}

// TestPathProjectIdBindsAsInt64: the {project_id} wildcard must land in
// an int64 field, and a non-integer must be a 400, not a 404.
func TestPathProjectIdBindsAsInt64(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	if rec := serveREST(t, r, "GET", "/api/projects/1/views/overview?from=2026-08-20&to=2026-08-21", ""); rec.Code != 200 {
		t.Fatalf("id 1: %d %s", rec.Code, rec.Body.String())
	}
	rec := serveREST(t, r, "GET", "/api/projects/blog/views/overview?from=2026-08-20&to=2026-08-21", "")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "project_id must be an integer") {
		t.Fatalf("alias in the path: %d %s, want 400 project_id must be an integer", rec.Code, rec.Body.String())
	}
}
