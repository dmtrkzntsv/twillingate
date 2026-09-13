package apiserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Build assembles the tool host, both transports and the auth middleware,
// returning one auth-wrapped handler that serves the API surface: the MCP
// streamable endpoint at /mcp and the REST routes under /api/ (unmatched
// /api/ paths answer a JSON 404). It mounts nothing itself: NewHandler
// wraps it with its own mux for the standalone listener, and app calls it
// directly to mount on the ingest surface's mux via RegisterOn.
func Build(ctx context.Context, cfg *config.Config, reg *manage.Registry, ops *manage.Ops, logger *slog.Logger) (http.Handler, func() error, error) {
	db, err := OpenReadDB(cfg.API.DBPath)
	if err != nil {
		return nil, nil, err
	}
	h := &host{db: db, reg: reg, ops: ops,
		timeout: cfg.API.QueryTimeout, maxRows: cfg.API.QueryMaxRows,
		publicURL: cfg.PublicURL, logger: logger}
	srv := mcp.NewServer(&mcp.Implementation{Name: "twillingate", Version: "1.0.0"}, nil)
	inner := http.NewServeMux()
	h.register(&registrar{mcp: srv, rest: inner, logger: logger})
	h.registerResources(srv)
	inner.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv }, nil))
	inner.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]apiError{"error": {Code: "not_found", Message: "no such API route"}})
	})

	protected, err := wrapAuth(ctx, cfg.API, inner)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return protected, db.Close, nil
}

// NewHandler assembles the API surface: tool host, both transports, auth
// middleware, and (mode-dependent) the RFC 9728 metadata route, mounted
// on its own mux. Routes registered on the returned mux: /mcp, /api/,
// /healthz, and
// /.well-known/oauth-protected-resource in oauth mode, and the login
// server's routes in token mode with a password configured.
// The func() error closes the read DB.
func NewHandler(ctx context.Context, cfg *config.Config, reg *manage.Registry, ops *manage.Ops, logger *slog.Logger) (http.Handler, func() error, error) {
	protected, closeDB, err := Build(ctx, cfg, reg, ops, logger)
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	RegisterOn(mux, protected, cfg, true, logger)
	return mux, closeDB, nil
}

// RegisterOn mounts the API surface on a mux: protected (from Build) at
// /mcp and /api/, plus the unauthenticated metadata, login and health
// routes. withHealthz=false when the mux is shared with the ingest surface,
// whose /healthz already exists (ServeMux panics on duplicate patterns).
func RegisterOn(mux *http.ServeMux, protected http.Handler, cfg *config.Config, withHealthz bool, logger *slog.Logger) {
	mux.Handle("/mcp", protected)
	mux.Handle("/api/", protected)
	if cfg.API.AuthMode == "oauth" {
		meta := &oauthex.ProtectedResourceMetadata{
			Resource:             cfg.API.ResourceURL,
			AuthorizationServers: []string{cfg.API.Issuer},
		}
		mux.Handle("GET /.well-known/oauth-protected-resource",
			auth.ProtectedResourceMetadataHandler(meta))
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

// wrapAuth builds the mode's verifier and middleware (endpoint spec §5.2).
func wrapAuth(ctx context.Context, m config.APIConfig, next http.Handler) (http.Handler, error) {
	switch m.AuthMode {
	case "token":
		opts := &auth.RequireBearerTokenOptions{AllowMissingExpiration: true}
		if !m.LoginEnabled() {
			return auth.RequireBearerToken(StaticVerifier(m.Token), opts)(next), nil
		}
		// Keys derive from the config alone, so this verifier accepts what
		// the instance RegisterOn mounts issues; verifying logs nothing.
		opts.ResourceMetadataURL = metadataURLFor(m.ResourceURL)
		verify := newLoginServer(m, slog.New(slog.DiscardHandler)).verify
		return auth.RequireBearerToken(verify, opts)(next), nil
	case "oauth":
		jwksURL, err := DiscoverJWKSURL(ctx, m.Issuer, nil)
		if err != nil {
			return nil, fmt.Errorf("mcp oauth mode: %w (is the API_AUTH_DSN issuer correct and reachable?)", err)
		}
		v := OAuthVerifier(m.Issuer, m.Audience, NewJWKSCache(jwksURL, nil))
		return auth.RequireBearerToken(v, &auth.RequireBearerTokenOptions{
			ResourceMetadataURL: metadataURLFor(m.ResourceURL),
		})(next), nil
	default:
		return nil, fmt.Errorf("apiserver: unknown auth mode %q", m.AuthMode)
	}
}

func metadataURLFor(resourceURL string) string {
	// RFC 9728: the well-known path is host-rooted; the resource URL's
	// origin carries it. Good enough for the single-origin deployments
	// this server targets; revisit if a path-scoped resource needs the
	// path-suffix form.
	return originOf(resourceURL) + "/.well-known/oauth-protected-resource"
}

// originOf is the scheme and host of an absolute URL: the base of the RFC
// 9728 well-known URL and the token:// login server's issuer.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Scheme + "://" + u.Host
}
