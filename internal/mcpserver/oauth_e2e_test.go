package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
// refreshes, on both of app's mounting paths.
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
			h := e2eHandler(t, "token://ar_testtoken?password=hunter2&redirect=http://localhost/callback&resource="+base+"/mcp", shared)
			var refreshes atomic.Int32
			srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// ParseForm is idempotent on a request, so the handler still sees the form.
				if r.URL.Path == "/oauth/token" && r.ParseForm() == nil && r.PostForm.Get("grant_type") == "refresh_token" {
					refreshes.Add(1)
				}
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
		})
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
	env := map[string]string{"DATABASE_DSN": "sqlite://" + seedDB(t), "MCP_AUTH_DSN": dsn}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateMCP(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, cfg.Retention, logger)
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	if !shared {
		h, closeDB, err := NewHandler(context.Background(), cfg, reg, ops, logger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closeDB() })
		return h
	}
	protected, closeDB, err := Build(context.Background(), cfg, reg, ops, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB() })
	mux := http.NewServeMux()
	mux.Handle("/", http.NotFoundHandler()) // the ingest surface's catch-all
	RegisterOn(mux, protected, cfg, false, logger)
	return mux
}
