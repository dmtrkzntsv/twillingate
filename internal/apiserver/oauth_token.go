package apiserver

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var codeVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// token is the token endpoint (login spec §7). client_id is required on both
// grants: every client is public (RFC 6749 §3.2.1).
func (s *loginServer) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBody)
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request", "body must be a form of at most 16 KB")
		return
	}
	form := r.PostForm
	if s.keys.parse(kindClient, form.Get("client_id"), &clientClaims{}, s.now) != nil {
		oauthError(w, "invalid_client", "unknown client_id")
		return
	}
	if form.Has("resource") && !s.resourceAccepted(form.Get("resource")) {
		oauthError(w, "invalid_target", "resource does not name this server")
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, form)
	case "refresh_token":
		s.refresh(w, form)
	default:
		oauthError(w, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

func (s *loginServer) exchangeCode(w http.ResponseWriter, form url.Values) {
	var c codeClaims
	// The code is burnt by its first presentation, before the other checks.
	if s.keys.parse(kindCode, form.Get("code"), &c, s.now, jwt.WithExpirationRequired()) != nil ||
		!s.used.claim(c.ID, c.ExpiresAt.Time, s.now()) {
		oauthError(w, "invalid_grant", "authorization code is invalid, expired or already used")
		return
	}
	if c.ClientID != form.Get("client_id") || c.RedirectURI != form.Get("redirect_uri") {
		oauthError(w, "invalid_grant", "client_id or redirect_uri differs from the authorization request")
		return
	}
	if !verifierMatches(form.Get("code_verifier"), c.CodeChallenge) {
		oauthError(w, "invalid_grant", "code_verifier does not match the code_challenge")
		return
	}
	s.issue(w, c.ClientID)
}

func (s *loginServer) refresh(w http.ResponseWriter, form url.Values) {
	var c grantClaims
	if s.keys.parse(kindRefresh, form.Get("refresh_token"), &c, s.now,
		jwt.WithAudience(s.resource), jwt.WithExpirationRequired()) != nil || c.ClientID != form.Get("client_id") {
		oauthError(w, "invalid_grant", "refresh token is invalid, expired or belongs to another client")
		return
	}
	s.issue(w, c.ClientID)
}

// issue answers with a fresh access and refresh token pair; issuing a new
// refresh token on every refresh is what makes the 30 days count from last use.
// aud names the origin and origin/mcp, the two resources the metadata
// documents advertise; verification requires the origin.
func (s *loginServer) issue(w http.ResponseWriter, clientID string) {
	now := s.now()
	grant := func(ttl time.Duration) grantClaims {
		return grantClaims{ClientID: clientID, RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{s.resource, s.resource + mcpPath},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        rand.Text(),
		}}
	}
	access := grant(s.accessTTL)
	access.Issuer, access.Subject = s.issuer, "mcp"
	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  s.keys.sign(kindAccess, access),
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.accessTTL / time.Second),
		RefreshToken: s.keys.sign(kindRefresh, grant(refreshTokenTTL)),
	})
}

// verifierMatches is the RFC 7636 S256 check, in constant time.
func verifierMatches(verifier, challenge string) bool {
	if !codeVerifierPattern.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// usedCodes remembers redeemed authorization codes until they expire, so
// each works once. Per process and lost on restart (login spec §10).
type usedCodes struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// claim reports whether jti was unused and marks it used until exp.
func (u *usedCodes) claim(jti string, exp, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.seen == nil {
		u.seen = map[string]time.Time{}
	}
	for id, e := range u.seen {
		if !e.After(now) {
			delete(u.seen, id)
		}
	}
	if _, dup := u.seen[jti]; dup {
		return false
	}
	u.seen[jti] = exp
	return true
}
