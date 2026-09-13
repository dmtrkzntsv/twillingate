package apiserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

// RFC 7636 appendix B.
const (
	testVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	testChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

var requestField = regexp.MustCompile(`name="request" value="([^"]+)"`)

func authorizeQuery(clientID, redirect string) url.Values {
	return url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"code_challenge": {testChallenge}, "code_challenge_method": {"S256"}, "state": {"st4te"},
		"resource": {testResource}}
}

func (f *loginFixture) authorize(q url.Values) *httptest.ResponseRecorder {
	return f.serve(httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
}

// loginForm loads the login page and returns its hidden request value.
func (f *loginFixture) loginForm(q url.Values) string {
	f.t.Helper()
	rec := f.authorize(q)
	m := requestField.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		f.t.Fatalf("login page: %d %s", rec.Code, rec.Body)
	}
	return m[1]
}

func (f *loginFixture) submit(request, password string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/oauth/authorize",
		strings.NewReader(url.Values{"request": {request}, "password": {password}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return f.serve(req)
}

// authCode completes a login and returns the authorization code.
func (f *loginFixture) authCode(clientID, redirect string) string {
	f.t.Helper()
	rec := f.submit(f.loginForm(authorizeQuery(clientID, redirect)), testPassword)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || err != nil || loc.Query().Get("code") == "" {
		f.t.Fatalf("login: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	return loc.Query().Get("code")
}

func TestAuthorizePageRendersForm(t *testing.T) {
	f := newLoginFixture(t, nil)
	rec := f.authorize(authorizeQuery(f.register(claudeCallback), claudeCallback))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{"Claude", "claude.ai", `type="password"`, `name="request"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for header, want := range map[string]string{
		"Cache-Control":           "no-store",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	// A loopback client registered on one port logs in from another.
	id := f.register("http://localhost:4000/callback")
	rec = f.authorize(authorizeQuery(id, "http://localhost:5555/callback"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "localhost:5555") {
		t.Errorf("loopback port change: %d %s", rec.Code, rec.Body)
	}

	// With no allowlist at all, a loopback client still logs in.
	bare := newLoginFixture(t, nil)
	codex := "http://127.0.0.1:1455/callback/abc123"
	if rec := bare.authorize(authorizeQuery(bare.register(codex), codex)); rec.Code != http.StatusOK {
		t.Errorf("loopback without allowlist: %d %s", rec.Code, rec.Body)
	}

	// resource is optional.
	q := authorizeQuery(f.register(claudeCallback), claudeCallback)
	q.Del("resource")
	if rec := f.authorize(q); rec.Code != http.StatusOK {
		t.Errorf("without resource: %d", rec.Code)
	}
}

func TestAuthorizeUntrustedRedirectShowsErrorPage(t *testing.T) {
	const appCallback = "https://app.example.com/cb"
	f := newLoginFixture(t, func(m *config.MCPConfig) { m.RedirectHosts = []string{"app.example.com"} })
	app := f.register(appCallback)
	// Same token and password, so the same keys, but the entry is gone.
	shrunk := newLoginFixture(t, nil)

	cases := map[string]struct {
		f        *loginFixture
		client   string
		redirect string
	}{
		"unknown client":              {f, "garbage", appCallback},
		"redirect not registered":     {f, app, "http://localhost:4000/callback"},
		"redirect missing":            {f, app, ""},
		"redirect left the allowlist": {shrunk, app, appCallback},
	}
	for name, tc := range cases {
		rec := tc.f.authorize(authorizeQuery(tc.client, tc.redirect))
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s: %d Location=%q; an untrusted redirect must never be followed",
				name, rec.Code, rec.Header().Get("Location"))
		}
		if tc.redirect != "" && !strings.Contains(rec.Body.String(), tc.redirect) {
			t.Errorf("%s: page does not show the rejected redirect", name)
		}
		if strings.Contains(rec.Body.String(), `name="request"`) {
			t.Errorf("%s: error page offers the password form", name)
		}
	}
	if !strings.Contains(shrunk.logs.String(), "redirect rejected") || !strings.Contains(shrunk.logs.String(), appCallback) {
		t.Errorf("rejection not logged with the redirect: %s", shrunk.logs)
	}
}

func TestAuthorizeRequestErrorsRedirectBack(t *testing.T) {
	f := newLoginFixture(t, nil)
	client := f.register(claudeCallback)
	cases := map[string]struct {
		edit func(url.Values)
		want string
	}{
		"implicit flow":    {func(q url.Values) { q.Set("response_type", "token") }, "unsupported_response_type"},
		"no challenge":     {func(q url.Values) { q.Del("code_challenge") }, "invalid_request"},
		"plain challenge":  {func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request"},
		"foreign resource": {func(q url.Values) { q.Set("resource", "https://other.example.com/mcp") }, "invalid_target"},
	}
	for name, tc := range cases {
		q := authorizeQuery(client, claudeCallback)
		tc.edit(q)
		rec := f.authorize(q)
		loc, err := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusFound || err != nil {
			t.Fatalf("%s: %d %q", name, rec.Code, rec.Header().Get("Location"))
		}
		got := loc.Query()
		if !strings.HasPrefix(loc.String(), claudeCallback+"?") || got.Get("error") != tc.want ||
			got.Get("state") != "st4te" || got.Get("iss") != testIssuer {
			t.Errorf("%s: Location = %s, want error=%s with state and iss", name, loc, tc.want)
		}
	}
}

func TestAuthorizeSubmit(t *testing.T) {
	f := newLoginFixture(t, func(m *config.MCPConfig) {
		m.RedirectHosts = []string{"app.example.com"}
	})
	client := f.register(claudeCallback)
	request := f.loginForm(authorizeQuery(client, claudeCallback))

	rec := f.submit(request, "wrong-pw")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Password not recognised") ||
		!strings.Contains(rec.Body.String(), request) {
		t.Errorf("wrong password: %d %s", rec.Code, rec.Body)
	}

	rec = f.submit(request, testPassword)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || err != nil {
		t.Fatalf("right password: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), claudeCallback+"?") || q.Get("code") == "" ||
		q.Get("state") != "st4te" || q.Get("iss") != testIssuer {
		t.Errorf("Location = %s", loc)
	}

	logs := f.logs.String()
	if !strings.Contains(logs, "wrong password") || !strings.Contains(logs, "client authorized") {
		t.Errorf("logs = %s", logs)
	}
	for _, secret := range []string{"wrong-pw", testPassword, q.Get("code"), request} {
		if strings.Contains(logs, secret) {
			t.Errorf("secret %q logged", secret)
		}
	}

	// A registered redirect that carries a query keeps it.
	app := f.register("https://app.example.com/cb?tenant=1")
	rec = f.submit(f.loginForm(authorizeQuery(app, "https://app.example.com/cb?tenant=1")), testPassword)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "tenant=1") || !strings.Contains(loc, "code=") {
		t.Errorf("query not preserved: %q", loc)
	}

	if rec := f.submit("garbage", testPassword); rec.Code != http.StatusBadRequest {
		t.Errorf("tampered request: %d", rec.Code)
	}
	f.now = f.now.Add(formTTL + time.Second)
	if rec := f.submit(request, testPassword); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "expired") {
		t.Errorf("expired request: %d %s", rec.Code, rec.Body)
	}
}

func TestAuthorizeLimiter(t *testing.T) {
	f := newLoginFixture(t, nil)
	request := f.loginForm(authorizeQuery(f.register(claudeCallback), claudeCallback))
	for i := range maxFailuresPerMinute {
		if rec := f.submit(request, "guess"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, rec.Code)
		}
	}
	if rec := f.submit(request, testPassword); rec.Code != http.StatusTooManyRequests {
		t.Errorf("right password during lockout: %d, want 429 without checking it", rec.Code)
	}
	f.now = f.now.Add(time.Minute)
	if rec := f.submit(request, testPassword); rec.Code != http.StatusFound {
		t.Errorf("next window: %d, want 302", rec.Code)
	}
}
