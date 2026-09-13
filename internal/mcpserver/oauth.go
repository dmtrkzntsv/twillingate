package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// The token:// login server: a minimal OAuth 2.1 authorization server whose
// only credential is the password in MCP_AUTH_DSN, for clients that cannot
// send the token as a header (docs/superpowers/specs/2026-09-12-mcp-token-login-design.md).

const (
	formTTL              = 10 * time.Minute
	codeTTL              = time.Minute
	refreshTokenTTL      = 30 * 24 * time.Hour
	maxFailuresPerMinute = 5
	maxOAuthBody         = 16 << 10
)

// accessTokenTTL is a var so the end-to-end test can shrink it below the
// oauth2 client's ten-second early-expiry margin and force refreshes.
var accessTokenTTL = time.Hour

type loginServer struct {
	keys      loginKeys
	static    auth.TokenVerifier // the env token, still accepted on /mcp
	password  []byte
	redirects []string // the MCP_AUTH_DSN allowlist
	resource  string
	issuer    string
	accessTTL time.Duration
	logger    *slog.Logger
	now       func() time.Time
}

func newLoginServer(m config.MCPConfig, logger *slog.Logger) *loginServer {
	return &loginServer{
		keys:      deriveLoginKeys(m.Token, m.Password),
		static:    StaticVerifier(m.Token),
		password:  []byte(m.Password),
		redirects: m.RedirectURIs,
		resource:  m.ResourceURL,
		issuer:    originOf(m.ResourceURL),
		accessTTL: accessTokenTTL,
		logger:    logger,
		now:       time.Now,
	}
}

// mount adds the login server's routes (login spec §4).
func (s *loginServer) mount(mux *http.ServeMux) {
	mux.Handle("GET /.well-known/oauth-protected-resource", auth.ProtectedResourceMetadataHandler(
		&oauthex.ProtectedResourceMetadata{Resource: s.resource, AuthorizationServers: []string{s.issuer}}))
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("POST /oauth/register", s.registerClient)
}

// verify accepts the env token as before, or an access token this server
// issued for this resource (login spec §8).
func (s *loginServer) verify(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error) {
	if info, err := s.static(ctx, token, req); err == nil {
		return info, nil
	}
	var c grantClaims
	if err := s.keys.parse(kindAccess, token, &c, s.now, jwt.WithIssuer(s.issuer),
		jwt.WithAudience(s.resource), jwt.WithExpirationRequired()); err != nil {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{UserID: "mcp", Expiration: c.ExpiresAt.Time}, nil
}

// authServerMetadata is RFC 8414's document. A local type rather than
// oauthex.AuthServerMeta, which would emit an empty jwks_uri.
type authServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
	IssParameterSupported             bool     `json:"authorization_response_iss_parameter_supported"`
}

func (s *loginServer) metadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, authServerMetadata{
		Issuer:                            s.issuer,
		AuthorizationEndpoint:             s.issuer + "/oauth/authorize",
		TokenEndpoint:                     s.issuer + "/oauth/token",
		RegistrationEndpoint:              s.issuer + "/oauth/register",
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		ScopesSupported:                   []string{"offline_access"},
		IssParameterSupported:             true,
	})
}

type registrationRequest struct {
	RedirectURIs  []string `json:"redirect_uris"`
	GrantTypes    []string `json:"grant_types"`
	ResponseTypes []string `json:"response_types"`
	ClientName    string   `json:"client_name"`
}

type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	ClientName              string   `json:"client_name,omitempty"`
}

// registerClient is RFC 7591 registration (login spec §5). The client_id is
// the registration itself, signed; nothing is stored.
func (s *loginServer) registerClient(w http.ResponseWriter, r *http.Request) {
	var req registrationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOAuthBody)).Decode(&req); err != nil {
		oauthError(w, "invalid_client_metadata", "body must be a JSON registration request of at most 16 KB")
		return
	}
	if len(req.RedirectURIs) == 0 {
		oauthError(w, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !matchesAny(s.redirects, u) {
			s.logger.Warn("mcp login: registration redirect rejected", "redirect_uri", u)
			oauthError(w, "invalid_redirect_uri", "redirect URI not in the MCP_AUTH_DSN allowlist: "+u)
			return
		}
	}
	grants := req.GrantTypes
	if len(grants) == 0 {
		grants = []string{"authorization_code"}
	}
	for _, g := range grants {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, "invalid_client_metadata", "unsupported grant type: "+g)
			return
		}
	}
	for _, rt := range req.ResponseTypes {
		if rt != "code" {
			oauthError(w, "invalid_client_metadata", "unsupported response type: "+rt)
			return
		}
	}
	name := req.ClientName
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64])
	}
	now := s.now()
	id := s.keys.sign(kindClient, clientClaims{RedirectURIs: req.RedirectURIs, ClientName: name,
		RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(now)}})
	writeJSON(w, http.StatusCreated, registrationResponse{
		ClientID:                id,
		ClientIDIssuedAt:        now.Unix(),
		RedirectURIs:            req.RedirectURIs,
		GrantTypes:              grants,
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		ClientName:              name,
	})
}

// redirectMatches applies login spec §5.1: scheme, lowercase host, path and
// query equal byte for byte, no fragment or userinfo, and the port ignored
// on loopback hosts only (RFC 8252 §7.3).
func redirectMatches(allowed, candidate string) bool {
	a, errA := url.Parse(allowed)
	c, errC := url.Parse(candidate)
	if errA != nil || errC != nil || strings.Contains(candidate, "#") {
		return false
	}
	host := strings.ToLower(c.Hostname())
	return c.Scheme != "" && a.Scheme == c.Scheme &&
		host != "" && strings.ToLower(a.Hostname()) == host &&
		(isLoopbackHost(host) || a.Port() == c.Port()) &&
		a.EscapedPath() == c.EscapedPath() &&
		a.RawQuery == c.RawQuery &&
		a.User.String() == c.User.String()
}

func matchesAny(allowed []string, candidate string) bool {
	return slices.ContainsFunc(allowed, func(a string) bool { return redirectMatches(a, candidate) })
}

func isLoopbackHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// oauthError writes an RFC 6749 §5.2 / RFC 7591 §3.2.2 error body.
func oauthError(w http.ResponseWriter, code, description string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": description})
}
