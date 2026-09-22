package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
	"github.com/golang-jwt/jwt/v5"
)

func newHandlerFixture(t *testing.T, over map[string]string) http.Handler {
	t.Helper()
	path := seedDB(t) // from readdb_test.go: migrated DB with project 1 (My blog)
	base := map[string]string{
		"DATABASE_DSN": "sqlite://" + path,
		"API_AUTH_DSN": "token://ar_testtoken",
	}
	for k, v := range over {
		if v == "" {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, logger)
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, closeDB, err := NewHandler(context.Background(), cfg, reg, manage.NewOps(reg, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB() })
	return h
}

func TestMCPRequires401WithChallenge(t *testing.T) {
	f := newJWKSFixture(t)
	h := newHandlerFixture(t, map[string]string{
		"API_AUTH_DSN": "oauth+insecure://" + strings.TrimPrefix(f.issuer, "http://") +
			"?resource=https://twillingate.example.com"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
	www := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(www, "resource_metadata") {
		t.Errorf("WWW-Authenticate = %q; must point at the metadata URL", www)
	}
	// /mcp names the metadata whose resource is origin/mcp (RFC 9728 §3.1
	// suffix form), which is what a strict client matches against.
	const want = `resource_metadata="https://twillingate.example.com/.well-known/oauth-protected-resource/mcp"`
	if !strings.Contains(www, want) {
		t.Errorf("WWW-Authenticate = %q; want %s", www, want)
	}
}

func TestAPIRequiresAuth(t *testing.T) {
	h := newHandlerFixture(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/projects", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/api/projects", nil)
	req.Header.Set("Authorization", "Bearer ar_testtoken")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"projects"`) {
		t.Fatalf("static token on /api/projects: %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/nope", nil)
	req.Header.Set("Authorization", "Bearer ar_testtoken")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Fatalf("unknown API route: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMCPTokenAuthPasses(t *testing.T) {
	h := newHandlerFixture(t, nil)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	req.Header.Set("Authorization", "Bearer ar_testtoken")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "twillingate") {
		t.Errorf("no serverInfo in %s", rec.Body.String())
	}
}

func TestMCPWrongTokenRejected(t *testing.T) {
	h := newHandlerFixture(t, nil)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer ar_wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestPRMServedWhenIssuerConfigured(t *testing.T) {
	f := newJWKSFixture(t)
	h := newHandlerFixture(t, map[string]string{
		"API_AUTH_DSN": "oauth+insecure://" + strings.TrimPrefix(f.issuer, "http://") +
			"?resource=https://twillingate.example.com"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, f.issuer) || !strings.Contains(body, `"resource":"https://twillingate.example.com"`) {
		t.Errorf("metadata = %s", body)
	}
}

func TestPRM404WithoutIssuer(t *testing.T) {
	h := newHandlerFixture(t, nil) // token mode, no issuer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestTokenNeverLoggedAtInfo(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	path := seedDB(t)
	base := map[string]string{"DATABASE_DSN": "sqlite://" + path,
		"API_AUTH_DSN": "token://ar_secrettoken"}
	cfg, _ := config.FromEnv(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	st, _ := store.Open(cfg.Database)
	defer st.Close()
	reg := manage.New(st, logger)
	reg.Reload(context.Background())
	h, closeDB, err := NewHandler(context.Background(), cfg, reg, manage.NewOps(reg, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer ar_secrettoken")
	h.ServeHTTP(httptest.NewRecorder(), req)
	req2 := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req2.Header.Set("Authorization", "Bearer ar_wrongtoken")
	h.ServeHTTP(httptest.NewRecorder(), req2)
	for _, secret := range []string{"ar_secrettoken", "ar_wrongtoken"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("token %q appeared in info-level logs", secret)
		}
	}
}

// initReq builds a raw initialize JSON-RPC request body with the
// headers the StreamableHTTPHandler requires, per TestMCPTokenAuthPasses.
func initReq() *http.Request {
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return req
}

// TestMCPOAuthModePasses exercises oauth mode through the assembled
// handler end to end: valid fixture-signed JWTs pass, invalid ones are
// rejected with a challenge pointing at the absolute metadata URL, and
// the PRM route serves the fixture issuer.
func TestMCPOAuthModePasses(t *testing.T) {
	f := newJWKSFixture(t)
	h := newHandlerFixture(t, map[string]string{
		"API_AUTH_DSN": "oauth+insecure://" + strings.TrimPrefix(f.issuer, "http://") +
			"?resource=https://twillingate.example.com",
	})

	req := initReq()
	req.Header.Set("Authorization", "Bearer "+f.sign(t, f.claims(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "twillingate") {
		t.Errorf("no serverInfo in %s", rec.Body.String())
	}

	bad := []struct {
		name string
		tok  string
	}{
		{"wrong signature", "not.a.jwt"},
		{"expired", f.sign(t, f.claims(jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()}))},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			req := initReq()
			req.Header.Set("Authorization", "Bearer "+tc.tok)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("code = %d", rec.Code)
			}
			www := rec.Header().Get("WWW-Authenticate")
			want := `resource_metadata="https://twillingate.example.com/.well-known/oauth-protected-resource/mcp"`
			if !strings.Contains(www, want) {
				t.Errorf("WWW-Authenticate = %q; want absolute metadata URL %q", www, want)
			}
		})
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), f.issuer) {
		t.Errorf("metadata = %s; want issuer %s", rec2.Body.String(), f.issuer)
	}
}

func TestHealthzUnauthenticated(t *testing.T) {
	h := newHandlerFixture(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}

// TestRegisterOnWithoutHealthzOmitsRoute proves the shared-mux case
// (Task 21: API_ADDR == INGEST_ADDR) doesn't collide with the ingest
// surface's own /healthz — RegisterOn(..., withHealthz=false) must not
// mount the route at all.
func TestRegisterOnWithoutHealthzOmitsRoute(t *testing.T) {
	path := seedDB(t)
	base := map[string]string{"DATABASE_DSN": "sqlite://" + path,
		"API_AUTH_DSN": "token://ar_testtoken"}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, logger)
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	protected, closeDB, err := Build(context.Background(), cfg, reg, manage.NewOps(reg, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB() })

	mux := http.NewServeMux()
	RegisterOn(mux, protected, cfg, false, logger)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d; /healthz must not be mounted when withHealthz=false", rec.Code)
	}
}

// TestWrapAuthUnknownMode exercises wrapAuth's default branch directly:
// ValidateAPI normally rejects an unknown AuthMode before Build ever
// runs, so this branch only matters as a defense-in-depth invariant if
// that validation is ever bypassed or a mode is added to one but not the
// other.
func TestWrapAuthUnknownMode(t *testing.T) {
	_, err := wrapAuth(context.Background(), config.APIConfig{AuthMode: "bogus"})
	if err == nil {
		t.Fatal("unknown auth mode accepted")
	}
}

// TestBuildFailsWhenOAuthIssuerUnreachable exercises Build's own error
// branch (wrapAuth failing after OpenReadDB already succeeded, so Build
// must close the DB it just opened rather than leak it) — ValidateAPI
// only parses API_AUTH_DSN, it does not probe the issuer, so
// an unreachable issuer surfaces here, at Build time.
func TestBuildFailsWhenOAuthIssuerUnreachable(t *testing.T) {
	path := seedDB(t)
	base := map[string]string{
		"DATABASE_DSN": "sqlite://" + path,
		"API_AUTH_DSN": "oauth+insecure://127.0.0.1:0?resource=https://twillingate.example.com",
	}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, logger)
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Build(context.Background(), cfg, reg, manage.NewOps(reg, st), logger); err == nil {
		t.Fatal("Build succeeded against an unreachable oauth issuer")
	}
}

const loginDSN = "token://ar_testtoken?password=hunter2" +
	"&redirect=https://claude.ai/api/mcp/auth_callback&resource=https://mcp.example.com"

func TestTokenLoginRoutesFollowRedirects(t *testing.T) {
	serve := func(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	plain := newHandlerFixture(t, nil)
	if rec := serve(plain, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("plain token://: metadata %d, want 404", rec.Code)
	}
	if rec := serve(plain, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))); rec.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("plain token:// grew a challenge: %q", rec.Header().Get("WWW-Authenticate"))
	}

	login := newHandlerFixture(t, map[string]string{"API_AUTH_DSN": loginDSN})
	if rec := serve(login, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)); rec.Code != http.StatusOK {
		t.Errorf("login: metadata %d, want 200", rec.Code)
	}
	rec := serve(login, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}")))
	const want = `resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp"`
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), want) {
		t.Errorf("login: /mcp %d WWW-Authenticate=%q, want 401 with %s", rec.Code, rec.Header().Get("WWW-Authenticate"), want)
	}
	passwordOnly := newHandlerFixture(t, map[string]string{
		"API_AUTH_DSN": "token://ar_testtoken?password=hunter2&resource=https://mcp.example.com"})
	if rec := serve(passwordOnly, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)); rec.Code != http.StatusOK {
		t.Errorf("password without redirect: metadata %d, want 200 — the password turns the login on", rec.Code)
	}
	req := initReq()
	req.Header.Set("Authorization", "Bearer ar_testtoken")
	if rec := serve(login, req); rec.Code != http.StatusOK {
		t.Errorf("login: header token %d, want 200 — it must keep working", rec.Code)
	}
}

func TestIssuedAccessTokenNeedsTheLoginServer(t *testing.T) {
	m := config.APIConfig{Token: "ar_testtoken", Password: "hunter2", ResourceURL: "https://mcp.example.com"}
	access := newLoginServer(m, slog.New(slog.DiscardHandler)).keys.sign(kindAccess, grantClaims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://mcp.example.com", Subject: "mcp",
			Audience: jwt.ClaimStrings{m.ResourceURL}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}})

	for name, tc := range map[string]struct {
		h    http.Handler
		want int
	}{
		"login on":  {newHandlerFixture(t, map[string]string{"API_AUTH_DSN": loginDSN}), http.StatusOK},
		"login off": {newHandlerFixture(t, nil), http.StatusUnauthorized},
	} {
		req := initReq()
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		tc.h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", name, rec.Code, tc.want)
		}
	}
}

// TestEachPrefixChallengesWithItsOwnMetadata: a strict MCP client checks
// that the challenge's metadata names the URL it connected to, so /mcp
// points at the /mcp-suffixed document and /api/ at the origin's.
func TestEachPrefixChallengesWithItsOwnMetadata(t *testing.T) {
	const origin = "https://twillingate.example.com"
	f := newJWKSFixture(t)
	modes := map[string]struct {
		dsn, issuer string
	}{
		"oauth": {"oauth+insecure://" + strings.TrimPrefix(f.issuer, "http://") + "?resource=" + origin, f.issuer},
		"login": {"token://ar_testtoken?password=hunter2&resource=" + origin, origin},
	}
	for mode, m := range modes {
		h := newHandlerFixture(t, map[string]string{"API_AUTH_DSN": m.dsn})
		for _, tc := range []struct{ method, path, meta, resource string }{
			{"POST", "/mcp", "/.well-known/oauth-protected-resource/mcp", origin + "/mcp"},
			{"GET", "/api/projects", "/.well-known/oauth-protected-resource", origin},
		} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
			want := `resource_metadata="` + origin + tc.meta + `"`
			if www := rec.Header().Get("WWW-Authenticate"); rec.Code != http.StatusUnauthorized || !strings.Contains(www, want) {
				t.Errorf("%s %s: %d WWW-Authenticate=%q, want 401 with %s", mode, tc.path, rec.Code, www, want)
			}
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", tc.meta, nil))
			var prm struct {
				Resource             string   `json:"resource"`
				AuthorizationServers []string `json:"authorization_servers"`
			}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &prm) != nil ||
				prm.Resource != tc.resource || len(prm.AuthorizationServers) != 1 || prm.AuthorizationServers[0] != m.issuer {
				t.Errorf("%s GET %s: %d %s, want resource %s and issuer %s", mode, tc.meta, rec.Code, rec.Body, tc.resource, m.issuer)
			}
		}
	}

	plain := newHandlerFixture(t, nil)
	for _, meta := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		rec := httptest.NewRecorder()
		plain.ServeHTTP(rec, httptest.NewRequest("GET", meta, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("plain token:// GET %s = %d, want 404", meta, rec.Code)
		}
	}
}

// TestOAuthAudienceDefaultsToOriginOrMCP: an IdP mints aud from the
// resource the client asked for, which is the origin or origin/mcp; an
// explicit audience= admits only itself.
func TestOAuthAudienceDefaultsToOriginOrMCP(t *testing.T) {
	const origin = "https://twillingate.example.com"
	f := newJWKSFixture(t)
	dsn := "oauth+insecure://" + strings.TrimPrefix(f.issuer, "http://") + "?resource=" + origin
	for name, tc := range map[string]struct {
		dsn  string
		aud  string
		want int
	}{
		"default, aud origin":             {dsn, origin, http.StatusOK},
		"default, aud origin/mcp":         {dsn, origin + "/mcp", http.StatusOK},
		"default, aud origin/api":         {dsn, origin + "/api", http.StatusUnauthorized},
		"default, aud elsewhere":          {dsn, "https://other.example.com/mcp", http.StatusUnauthorized},
		"explicit origin, aud origin":     {dsn + "&audience=" + origin, origin, http.StatusOK},
		"explicit origin, aud origin/mcp": {dsn + "&audience=" + origin, origin + "/mcp", http.StatusUnauthorized},
		"explicit aud9, aud aud9":         {dsn + "&audience=aud9", "aud9", http.StatusOK},
		"explicit aud9, aud origin":       {dsn + "&audience=aud9", origin, http.StatusUnauthorized},
	} {
		h := newHandlerFixture(t, map[string]string{"API_AUTH_DSN": tc.dsn})
		req := httptest.NewRequest("GET", "/api/projects", nil)
		req.Header.Set("Authorization", "Bearer "+f.sign(t, f.claims(jwt.MapClaims{"aud": tc.aud})))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: GET /api/projects = %d %s, want %d", name, rec.Code, rec.Body, tc.want)
		}
	}
}
