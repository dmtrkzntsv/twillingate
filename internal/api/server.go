package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// The RFC 9728 metadata path, and the MCP endpoint's path, which the
// metadata for /mcp carries as a suffix (RFC 9728 §3.1).
const (
	metadataPath = "/.well-known/oauth-protected-resource"
	mcpPath      = "/mcp"
)

// Build assembles the tool host, both transports and the auth middleware,
// returning one handler that serves the API surface: the MCP streamable
// endpoint at /mcp and the REST routes under /api/ (unmatched /api/ paths
// answer a JSON 404), plus, unauthenticated, their OpenAPI document at
// /api/openapi.json and /api/docs, a redirect to its Swagger UI. Each prefix is auth-wrapped on its own so its 401
// challenge names metadata whose resource is that prefix's URL. It mounts
// nothing itself: NewHandler wraps it with its own mux for the standalone
// listener, and app calls it directly to mount on the ingest surface's mux
// via RegisterOn. rst is the store reporting reads and writes; widget
// queries run on the same read-only handle, and so under the same
// API_QUERY_TIMEOUT and API_QUERY_MAX_ROWS, as the query tool.
func Build(ctx context.Context, cfg *config.Config, reg *manage.Registry, ops *manage.Ops, rst reporting.Store, logger *slog.Logger) (http.Handler, func() error, error) {
	db, err := readsql.Open(cfg.API.DBPath, cfg.API.QueryTimeout, cfg.API.QueryMaxRows)
	if err != nil {
		return nil, nil, err
	}
	rep := reporting.New(rst, db, reporting.Options{CacheAge: cfg.Reporting.CacheAge, RefreshAge: cfg.Reporting.RefreshAge})
	h := &host{db: db, reg: reg, ops: ops, rep: rep,
		publicURL: cfg.PublicURL, logger: logger}
	srv := mcp.NewServer(&mcp.Implementation{Name: "twillingate", Version: "1.0.0"},
		&mcp.ServerOptions{Instructions: serverInstructions})
	rest := http.NewServeMux()
	r := &registrar{mcp: srv, rest: rest, logger: logger}
	h.register(r)
	h.registerResources(srv)
	doc, err := openAPI(r.specs)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	rest.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]apiError{"error": {Code: "not_found", Message: "no such API route"}})
	})

	requireAuth, err := wrapAuth(ctx, cfg.API)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	resource := cfg.API.ResourceURL
	protected := http.NewServeMux()
	protected.Handle(mcpPath, requireAuth(resource+metadataPath+mcpPath,
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)))
	protected.Handle("/api/", requireAuth(resource+metadataPath, rest))
	// The route reference is public, like the dashboards' page: it names
	// routes and fields, never data, and Swagger UI reads it before login.
	protected.HandleFunc("GET "+openAPIPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(doc)
	})
	protected.Handle("GET "+docsPath, http.RedirectHandler(docsUIPath, http.StatusFound))
	return protected, db.Close, nil
}

// NewHandler assembles the API surface: tool host, both transports, auth
// middleware, and (mode-dependent) the RFC 9728 metadata route, mounted
// on its own mux. Routes registered on the returned mux: /mcp, /api/,
// /app/, /healthz, and
// /.well-known/oauth-protected-resource[/mcp] in oauth mode, and the login
// server's routes in token mode with a password configured.
// The func() error closes the read DB.
func NewHandler(ctx context.Context, cfg *config.Config, reg *manage.Registry, ops *manage.Ops, rst reporting.Store, logger *slog.Logger) (http.Handler, func() error, error) {
	protected, closeDB, err := Build(ctx, cfg, reg, ops, rst, logger)
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	RegisterOn(mux, protected, cfg, true, logger)
	return mux, closeDB, nil
}

// RegisterOn mounts the API surface on a mux: protected (from Build) at
// /mcp and /api/, plus the unauthenticated metadata, login and health
// routes, and the dashboards at /app/ (GET / and GET /app redirect there,
// so the API's address opens the dashboards; an ingest-only listener keeps
// answering 404 at /). The dashboards' page is public like the login page:
// it holds no data, and reads everything through /api/ with the login's
// token. withHealthz=false when the mux is shared with the ingest surface,
// whose /healthz already exists (ServeMux panics on duplicate patterns).
func RegisterOn(mux *http.ServeMux, protected http.Handler, cfg *config.Config, withHealthz bool, logger *slog.Logger) {
	mux.Handle("/mcp", protected)
	mux.Handle("/api/", protected)
	mux.Handle("GET /app/", reporting.UI())
	mux.Handle("GET /app", http.RedirectHandler("/app/", http.StatusMovedPermanently))
	// 302, not 301: browsers cache a permanent redirect of the root forever,
	// and the root may serve something of its own one day.
	mux.Handle("GET /{$}", http.RedirectHandler("/app/", http.StatusFound))
	if cfg.API.AuthMode == "oauth" {
		mountResourceMetadata(mux, cfg.API.ResourceURL, cfg.API.Issuer)
	}
	if cfg.API.LoginEnabled() {
		newLoginServer(cfg.API, logger).mount(mux)
	}
	if withHealthz {
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		})
	}
}

// wrapAuth builds the mode's verifier once (endpoint spec §5.2) and
// returns the middleware applying it, given the metadata URL each prefix's
// 401 challenge names. Plain token:// has no metadata and no challenge.
func wrapAuth(ctx context.Context, m config.APIConfig) (func(metadataURL string, next http.Handler) http.Handler, error) {
	var verify auth.TokenVerifier
	opts := auth.RequireBearerTokenOptions{}
	challenge := true
	switch m.AuthMode {
	case "token":
		opts.AllowMissingExpiration = true
		if !m.LoginEnabled() {
			verify, challenge = StaticVerifier(m.Token), false
			break
		}
		// Keys derive from the config alone, so this verifier accepts what
		// the instance RegisterOn mounts issues; verifying logs nothing.
		verify = newLoginServer(m, slog.New(slog.DiscardHandler)).verify
	case "oauth":
		jwksURL, err := DiscoverJWKSURL(ctx, m.Issuer, nil)
		if err != nil {
			return nil, fmt.Errorf("api oauth mode: %w (is the API_AUTH_DSN issuer correct and reachable?)", err)
		}
		verify = OAuthVerifier(m.Issuer, oauthAudiences(m), NewJWKSCache(jwksURL, nil))
	default:
		return nil, fmt.Errorf("api: unknown auth mode %q", m.AuthMode)
	}
	return func(metadataURL string, next http.Handler) http.Handler {
		o := opts
		if challenge {
			o.ResourceMetadataURL = metadataURL
		}
		return auth.RequireBearerToken(verify, &o)(next)
	}, nil
}

// oauthAudiences is what an oauth:// JWT's aud must contain one of. An
// IdP mints aud from the resource the client asked for, the origin or
// origin/mcp, so both pass by default; an explicit audience= passes alone.
func oauthAudiences(m config.APIConfig) []string {
	if m.AudienceGiven() {
		return []string{m.Audience}
	}
	return []string{m.Audience, m.ResourceURL + mcpPath}
}

// mountResourceMetadata serves the RFC 9728 documents: the host-rooted one
// names the origin, which /api/ challenges point at; the /mcp-suffixed one
// names origin/mcp, the URL MCP clients connect to and require the
// document they are sent to to name (go-sdk checks this before any fallback).
func mountResourceMetadata(mux *http.ServeMux, resource, authServer string) {
	for _, suffix := range []string{"", mcpPath} {
		mux.Handle("GET "+metadataPath+suffix, auth.ProtectedResourceMetadataHandler(
			&oauthex.ProtectedResourceMetadata{Resource: resource + suffix, AuthorizationServers: []string{authServer}}))
	}
}

// originOf is the scheme and host of an absolute URL: the token:// login
// server's issuer.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Scheme + "://" + u.Host
}
