package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const e2eCallback = "http://localhost:43210/callback"

// TestTokenLoginEndToEnd drives the MCP SDK's own OAuth client through
// discovery, registration, the password page, the code exchange and
// refreshes, on both of app's mounting paths. The DSN carries no redirect=:
// a loopback client needs none. The resource is the origin while the client
// connects to /mcp: the SDK must find its metadata on the first fetch, the
// one the challenge names, and the token it ends up with also opens /api/.
func TestTokenLoginEndToEnd(t *testing.T) {
	// Below the oauth2 client's ten-second early-expiry margin, so every
	// request after the login refreshes.
	saved := accessTokenTTL
	accessTokenTTL = 5 * time.Second
	t.Cleanup(func() { accessTokenTTL = saved })

	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared=%v", shared), func(t *testing.T) {
			srv := httptest.NewUnstartedServer(nil)
			base := "http://" + srv.Listener.Addr().String()
			h := e2eHandler(t, "token://ar_testtoken?password=hunter2&resource="+base, shared)
			var refreshes atomic.Int32
			var mu sync.Mutex
			var prmFetches, resources []string
			srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// ParseForm is idempotent on a request, so the handler still sees the form.
				if r.URL.Path == "/oauth/token" && r.ParseForm() == nil && r.PostForm.Get("grant_type") == "refresh_token" {
					refreshes.Add(1)
				}
				mu.Lock()
				if strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource") {
					prmFetches = append(prmFetches, r.URL.Path)
				}
				if r.URL.Path == "/oauth/authorize" && r.Method == "GET" {
					resources = append(resources, r.URL.Query().Get("resource"))
				}
				mu.Unlock()
				h.ServeHTTP(w, r)
			})
			srv.Start()
			t.Cleanup(srv.Close)

			oauth, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
				DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
					Metadata: &oauthex.ClientRegistrationMetadata{
						RedirectURIs:            []string{e2eCallback},
						ClientName:              "e2e",
						GrantTypes:              []string{"authorization_code", "refresh_token"},
						TokenEndpointAuthMethod: "none",
					}},
				AuthorizationCodeFetcher: browserLogin(srv.URL, "hunter2"),
				RequestRefreshToken:      true,
			})
			if err != nil {
				t.Fatal(err)
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
			cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", OAuthHandler: oauth}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cs.Close() })

			for i := range 2 {
				res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_projects"})
				if err != nil || res.IsError {
					t.Fatalf("call %d: %v %+v", i+1, err, res)
				}
				if !strings.Contains(textOf(res), "blog") {
					t.Errorf("call %d: %s", i+1, textOf(res))
				}
			}
			if refreshes.Load() == 0 {
				t.Error("the client never refreshed its access token")
			}
			mu.Lock()
			if !slices.Equal(prmFetches, []string{"/.well-known/oauth-protected-resource/mcp"}) {
				t.Errorf("metadata fetched = %v, want only the /mcp document the challenge names (no fallback)", prmFetches)
			}
			if !slices.Equal(resources, []string{base + "/mcp"}) {
				t.Errorf("authorize resource = %v, want [%s/mcp]", resources, base)
			}
			mu.Unlock()

			ts, err := oauth.TokenSource(t.Context())
			if err != nil || ts == nil {
				t.Fatalf("token source: %v", err)
			}
			tok, err := ts.Token()
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/projects", nil)
			req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "blog") {
				t.Errorf("GET /api/projects with the MCP login's token = %d %s, want 200 listing blog", resp.StatusCode, body)
			}
		})
	}
}

// TestAppLoginEndToEnd plays the web app at /app/: it registers with
// its own origin's /app/callback, which no redirect= entry lists, runs
// PKCE through the password page with the resource named at authorize
// and token, and reads /api/dashboards with the token. Every request
// carries the API's public Host, as a browser behind the proxy would.
func TestAppLoginEndToEnd(t *testing.T) {
	const origin = "https://dash.example.com"
	const callback = origin + "/app/callback"
	srv := httptest.NewServer(e2eHandler(t, "token://ar_testtoken?password=hunter2&resource="+origin, true))
	t.Cleanup(srv.Close)
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, body io.Reader, header map[string]string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "dash.example.com"
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	read := func(resp *http.Response) string {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}

	resp := do("POST", "/oauth/register", strings.NewReader(`{"redirect_uris":["`+callback+`"],`+
		`"grant_types":["authorization_code","refresh_token"],"client_name":"twillingate"}`),
		map[string]string{"Content-Type": "application/json"})
	body := read(resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d %s", resp.StatusCode, body)
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal([]byte(body), &reg); err != nil {
		t.Fatal(err)
	}

	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {callback},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"state": {"s1"}, "resource": {origin}}
	page := read(do("GET", "/oauth/authorize?"+q.Encode(), nil, nil))
	m := requestField.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("login page: %s", page)
	}
	resp = do("POST", "/oauth/authorize", strings.NewReader(url.Values{"request": {m[1]}, "password": {"hunter2"}}.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	read(resp)
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc.String(), callback+"?") {
		t.Fatalf("login: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	form := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {reg.ClientID},
		"redirect_uri": {callback}, "code_verifier": {verifier}, "resource": {origin}}
	resp = do("POST", "/oauth/token", strings.NewReader(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	body = read(resp)
	var tok tokenResponse
	if err := json.Unmarshal([]byte(body), &tok); err != nil || resp.StatusCode != http.StatusOK || tok.AccessToken == "" {
		t.Fatalf("token = %d %s", resp.StatusCode, body)
	}

	resp = do("GET", "/api/dashboards", nil, map[string]string{"Authorization": "Bearer " + tok.AccessToken})
	if body := read(resp); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"timezone":"UTC"`) {
		t.Errorf("GET /api/dashboards with the app's token = %d %s", resp.StatusCode, body)
	}
}

// browserLogin plays the browser: load the login page, submit the password,
// and read the code from the redirect instead of following it.
func browserLogin(base, password string) auth.AuthorizationCodeFetcher {
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		page, err := browser.Get(args.URL)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(page.Body)
		page.Body.Close()
		m := requestField.FindStringSubmatch(string(body))
		if m == nil {
			return nil, fmt.Errorf("login page %d: %s", page.StatusCode, body)
		}
		resp, err := browser.PostForm(base+"/oauth/authorize", url.Values{"request": {m[1]}, "password": {password}})
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc.String(), e2eCallback+"?") {
			return nil, fmt.Errorf("login: %d %q", resp.StatusCode, resp.Header.Get("Location"))
		}
		q := loc.Query()
		return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
	}
}

// e2eHandler assembles the MCP surface the way app does: standalone via
// NewHandler, or shared via Build and RegisterOn beside an ingest stand-in.
func e2eHandler(t *testing.T, dsn string, shared bool) http.Handler {
	t.Helper()
	env := map[string]string{"DATABASE_DSN": "sqlite://" + seedDB(t), "API_AUTH_DSN": dsn}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
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
	ops := manage.NewOps(reg, st)
	if !shared {
		h, closeDB, err := NewHandler(context.Background(), cfg, reg, ops, st, logger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closeDB() })
		return h
	}
	protected, closeDB, err := Build(context.Background(), cfg, reg, ops, st, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB() })
	mux := http.NewServeMux()
	// Stand-in for the ingest surface's own /healthz, which RegisterOn
	// omits (withHealthz=false) when the mux is shared.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	RegisterOn(mux, protected, cfg, false, logger)
	return mux
}
