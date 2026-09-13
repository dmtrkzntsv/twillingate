package mcpserver

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

//go:embed oauth_page.html
var loginPageHTML string

var loginPage = template.Must(template.New("login").Parse(loginPageHTML))

// pageData fills oauth_page.html; a non-empty Request renders the form.
type pageData struct {
	ClientName       string
	RedirectHost     string
	Request          string
	Error            string
	RejectedRedirect string
}

// authorizePage validates an authorization request and shows the password
// form (login spec §6.1).
func (s *loginServer) authorizePage(w http.ResponseWriter, r *http.Request) {
	setPageHeaders(w)
	q := r.URL.Query()
	redirectURI := q.Get("redirect_uri")
	client, ok := s.trustedRedirect(w, q.Get("client_id"), redirectURI)
	if !ok {
		return
	}
	fail := func(code string) {
		http.Redirect(w, r, withParams(redirectURI,
			url.Values{"error": {code}, "state": {q.Get("state")}, "iss": {s.issuer}}), http.StatusFound)
	}
	switch {
	case q.Get("response_type") != "code":
		fail("unsupported_response_type")
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		fail("invalid_request")
	case q.Has("resource") && q.Get("resource") != s.resource:
		fail("invalid_target")
	default:
		form := s.keys.sign(kindForm, formClaims{ClientID: q.Get("client_id"), ClientName: client.ClientName,
			RedirectURI: redirectURI, CodeChallenge: q.Get("code_challenge"), State: q.Get("state"),
			RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(s.now().Add(formTTL))}})
		s.render(w, http.StatusOK, pageData{ClientName: client.ClientName, RedirectHost: hostOf(redirectURI), Request: form})
	}
}

// trustedRedirect resolves client_id and redirect_uri. When either cannot be
// trusted it answers with the error page itself and reports false: an
// untrusted redirect is never followed (RFC 6749 §4.1.2.1).
func (s *loginServer) trustedRedirect(w http.ResponseWriter, clientID, redirectURI string) (*clientClaims, bool) {
	var c clientClaims
	var reason string
	switch {
	case s.keys.parse(kindClient, clientID, &c, s.now) != nil:
		reason = "Unknown client. Remove the connector and add it again."
	case !matchesAny(c.RedirectURIs, redirectURI):
		reason = "The redirect URI is not registered for this client."
	case !matchesAny(s.redirects, redirectURI):
		reason = "The redirect URI is not in the MCP_AUTH_DSN allowlist."
	default:
		return &c, true
	}
	s.logger.Warn("mcp login: redirect rejected", "redirect_uri", redirectURI, "reason", reason)
	s.render(w, http.StatusBadRequest, pageData{Error: reason, RejectedRedirect: redirectURI})
	return nil, false
}

// authorizeSubmit checks the password and redirects back with a code
// (login spec §6.2).
func (s *loginServer) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	setPageHeaders(w)
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBody)
	if !s.limiter.allow(s.now()) {
		s.logger.Warn("mcp login: too many failed password attempts")
		s.render(w, http.StatusTooManyRequests, pageData{Error: "Too many attempts. Wait a minute, then start again from your client."})
		return
	}
	request := r.PostFormValue("request")
	var f formClaims
	if err := s.keys.parse(kindForm, request, &f, s.now, jwt.WithExpirationRequired()); err != nil {
		s.render(w, http.StatusBadRequest, pageData{Error: "This login request has expired. Start again from your client."})
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("password")), s.password) != 1 {
		s.limiter.fail(s.now())
		s.logger.Warn("mcp login: wrong password", "remote_addr", r.RemoteAddr)
		s.render(w, http.StatusUnauthorized, pageData{ClientName: f.ClientName, RedirectHost: hostOf(f.RedirectURI),
			Request: request, Error: "Password not recognised."})
		return
	}
	code := s.keys.sign(kindCode, codeClaims{ClientID: f.ClientID, RedirectURI: f.RedirectURI, CodeChallenge: f.CodeChallenge,
		RegisteredClaims: jwt.RegisteredClaims{ID: rand.Text(), ExpiresAt: jwt.NewNumericDate(s.now().Add(codeTTL))}})
	s.logger.Info("mcp login: client authorized", "client_name", f.ClientName, "redirect_host", hostOf(f.RedirectURI))
	http.Redirect(w, r, withParams(f.RedirectURI,
		url.Values{"code": {code}, "state": {f.State}, "iss": {s.issuer}}), http.StatusFound)
}

func (s *loginServer) render(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = loginPage.Execute(w, d) // fails only when the client has gone
}

func setPageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
}

// withParams adds the non-empty params to a redirect URI that already
// matched the allowlist, keeping any query it carries.
func withParams(base string, params url.Values) string {
	u, _ := url.Parse(base)
	q := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			if v != "" {
				q.Add(k, v)
			}
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// hostOf is the host of a redirect URI that already matched the allowlist.
func hostOf(raw string) string {
	u, _ := url.Parse(raw)
	return u.Host
}

// failureLimiter counts wrong passwords in fixed one-minute windows shared
// by every caller; per-IP limits fall to rotating addresses (login spec §10).
type failureLimiter struct {
	mu     sync.Mutex
	window time.Time
	count  int
}

func (l *failureLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roll(now)
	return l.count < maxFailuresPerMinute
}

func (l *failureLimiter) fail(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roll(now)
	l.count++
}

func (l *failureLimiter) roll(now time.Time) {
	if w := now.Truncate(time.Minute); !w.Equal(l.window) {
		l.window, l.count = w, 0
	}
}
