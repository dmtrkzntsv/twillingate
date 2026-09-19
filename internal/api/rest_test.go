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
	req := httptest.NewRequest("GET", "/api/projects/blog/web/breakdown?from=2026-08-20&to=2026-08-21&dimension=pages&limit=5", nil)
	req.SetPathValue("project", "blog")
	var in breakdownIn
	if err := decodeRequest(req, &in); err != nil {
		t.Fatal(err)
	}
	if in.Project != "blog" || in.From != "2026-08-20" || in.To != "2026-08-21" || in.Dimension != "pages" || in.Limit != 5 {
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
		{"unknown body field", httptest.NewRequest("POST", "/x", strings.NewReader(`{"alias":"a","colour":"red"}`)), &projectIn{}},
		{"not json", httptest.NewRequest("POST", "/x", strings.NewReader(`alias=a`)), &projectIn{}},
		{"trailing data", httptest.NewRequest("POST", "/x", strings.NewReader(`{"alias":"a"}{"alias":"b"}`)), &projectIn{}},
		{"query on a POST", httptest.NewRequest("POST", "/x?alias=a", strings.NewReader(`{}`)), &projectIn{}},
		{"body too large", httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"`+strings.Repeat("a", maxAPIBody)+`"}`)), &projectIn{}},
		{"bad bool", httptest.NewRequest("GET", "/x?skip_key=maybe", nil), &projectIn{}},
		{"slice from the URL", httptest.NewRequest("GET", "/x?allowed_origins=a", nil), &projectIn{}},
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
	rec := serveREST(t, r, "GET", "/api/projects/blog/product/events?from=2026-08-20&to=2026-08-21&event=signup%ZZ", "")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid" {
		t.Errorf("malformed filter = %d %s, want 400 invalid", rec.Code, rec.Body.String())
	}
}

func TestDecodeRequestPathOverridesBody(t *testing.T) {
	req := httptest.NewRequest("PATCH", "/api/projects/blog", strings.NewReader(`{"alias":"other","name":"Blog"}`))
	req.SetPathValue("alias", "blog")
	var in projectIn
	if err := decodeRequest(req, &in); err != nil {
		t.Fatal(err)
	}
	if in.Alias != "blog" || in.Name != "Blog" {
		t.Errorf("decoded %+v", in)
	}
}

func TestDecodeRequestHeadReadsQuery(t *testing.T) {
	req := httptest.NewRequest("HEAD", "/x?skip_key=true", nil)
	var in projectIn
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
		req := httptest.NewRequest("GET", "/api/projects/blog/web/overview", nil)
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
			if !strings.Contains(logs.String(), "method=GET") || !strings.Contains(logs.String(), "path=/api/projects/blog/web/overview") {
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
	args := map[string]any{"project": "blog", "from": "2026-08-20", "to": "2026-08-21"}
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
		{"views_overview", "/api/projects/blog/views/overview?" + rng, args},
		{"views_breakdown", "/api/projects/blog/views/breakdown?dimension=paths&" + rng, with(map[string]any{"dimension": "paths"})},
		{"retention", "/api/projects/blog/retention?actor=user&" + rng, with(map[string]any{"actor": "user"})},
		{"product_events", "/api/projects/blog/product/events?" + rng, args},
		{"product_attributes", "/api/projects/blog/product/attributes?" + rng, args},
		{"identities", "/api/projects/blog/identities?kind=user&" + rng, with(map[string]any{"kind": "user"})},
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

	rec := serveREST(t, r, "POST", "/api/projects", `{"alias":"shop","allowed_origins":["https://shop.example.com"]}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"key":"ak_`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects", `{"alias":"shop"}`); rec.Code != 409 || errorCode(t, rec) != "conflict" {
		t.Errorf("duplicate create = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "PATCH", "/api/projects/shop", `{"name":"Shop"}`); rec.Code != 200 {
		t.Errorf("patch = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects/shop/keys", `{"label":"ios"}`); rec.Code != http.StatusCreated {
		t.Errorf("issue key = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects/shop/keys/ios/disable", ""); rec.Code != 200 {
		t.Errorf("disable key = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects/shop/archive", ""); rec.Code != 200 {
		t.Errorf("archive = %d %s", rec.Code, rec.Body.String())
	}
	rec = serveREST(t, r, "GET", "/api/projects/nope/views/overview?from=2026-08-20&to=2026-08-21", "")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "valid aliases") {
		t.Errorf("unknown project = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "GET", "/api/projects/blog/views/overview?from=2026-08-20&to=2026-08-21&colour=red", ""); rec.Code != 400 {
		t.Errorf("unknown parameter = %d", rec.Code)
	}
	if rec := serveREST(t, r, "POST", "/api/query", `{"sql":"SELECT day, visitors FROM v_views_daily WHERE project='blog'"}`); rec.Code != 200 {
		t.Errorf("query = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/query", `{"sql":"ATTACH 'x' AS y"}`); rec.Code != 400 {
		t.Errorf("attach = %d", rec.Code)
	}
	if rec := serveREST(t, r, "GET", "/api/schema/views", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "v_views_daily") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("schema/views = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := serveREST(t, r, "GET", "/api/projects/blog/integration-guide?platform=web", ""); rec.Code != 404 {
		t.Errorf("integration guide must be MCP-only, got %d", rec.Code)
	}

	var actor string
	if err := h.db.QueryRow(`SELECT actor FROM audit_log WHERE action='project.create' AND subject='shop'`).Scan(&actor); err != nil || actor != "api" {
		t.Errorf("audit actor = %q, %v; want api", actor, err)
	}
	// The first key is issued by the create itself, not a follow-up call.
	if err := h.db.QueryRow(`SELECT actor FROM audit_log WHERE action='key.issue' AND subject='shop/default'`).Scan(&actor); err != nil || actor != "api" {
		t.Errorf("first-key audit actor = %q, %v; want api", actor, err)
	}
	// A refused create leaves no project behind: the retry is not a 409.
	if rec := serveREST(t, r, "POST", "/api/projects", `{"alias":"Bad-Alias"}`); rec.Code != 400 {
		t.Errorf("bad alias = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects", `{"alias":"badalias"}`); rec.Code != http.StatusCreated {
		t.Errorf("retry after a refusal = %d %s", rec.Code, rec.Body.String())
	}
}
