package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

type issuedTokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

func codeGrant(client, code, redirect string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {code},
		"redirect_uri": {redirect}, "code_verifier": {testVerifier}, "resource": {testResource}}
}

func refreshGrant(client, refresh string) url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {refresh}}
}

func (f *loginFixture) tokenRequest(v url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return f.serve(req)
}

// grant runs a token request that must succeed.
func (f *loginFixture) grant(v url.Values) issuedTokens {
	f.t.Helper()
	rec := f.tokenRequest(v)
	var out issuedTokens
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		f.t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}
	return out
}

func wantOAuthError(t *testing.T, name string, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"`+code+`"`) {
		t.Errorf("%s: %d %s, want 400 %s", name, rec.Code, rec.Body, code)
	}
}

func TestTokenAuthorizationCode(t *testing.T) {
	f := newLoginFixture(t, nil)
	client := f.register(claudeCallback)
	code := f.authCode(client, claudeCallback)

	rec := f.tokenRequest(codeGrant(client, code, claudeCallback))
	var got issuedTokens
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("exchange: %d %s", rec.Code, rec.Body)
	}
	if got.TokenType != "Bearer" || got.ExpiresIn != 3600 || got.AccessToken == "" || got.RefreshToken == "" {
		t.Errorf("tokens = %+v", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("token response is cacheable")
	}
	if _, err := f.s.verify(t.Context(), got.AccessToken, nil); err != nil {
		t.Errorf("issued access token rejected on /mcp: %v", err)
	}
	if _, err := f.s.verify(t.Context(), got.RefreshToken, nil); err == nil {
		t.Error("refresh token accepted on /mcp")
	}

	wantOAuthError(t, "code reused", f.tokenRequest(codeGrant(client, code, claudeCallback)), "invalid_grant")
}

func TestTokenAuthorizationCodeRejections(t *testing.T) {
	f := newLoginFixture(t, nil)
	client := f.register(claudeCallback)
	loopback := f.register("http://localhost:4000/callback")
	other := f.register(claudeCallback)

	cases := map[string]struct {
		request func() url.Values
		want    string
	}{
		"wrong verifier": {func() url.Values {
			v := codeGrant(client, f.authCode(client, claudeCallback), claudeCallback)
			v.Set("code_verifier", strings.Repeat("a", 43))
			return v
		}, "invalid_grant"},
		"malformed verifier": {func() url.Values {
			v := codeGrant(client, f.authCode(client, claudeCallback), claudeCallback)
			v.Set("code_verifier", "short")
			return v
		}, "invalid_grant"},
		"redirect differs": {func() url.Values {
			return codeGrant(client, f.authCode(client, claudeCallback), "https://claude.ai/other")
		}, "invalid_grant"},
		"loopback port differs from the authorization": {func() url.Values {
			return codeGrant(loopback, f.authCode(loopback, "http://localhost:4000/callback"), "http://localhost:5000/callback")
		}, "invalid_grant"},
		"code of another client": {func() url.Values {
			return codeGrant(other, f.authCode(client, claudeCallback), claudeCallback)
		}, "invalid_grant"},
		"garbage code": {func() url.Values {
			return codeGrant(client, "garbage", claudeCallback)
		}, "invalid_grant"},
		"foreign resource": {func() url.Values {
			v := codeGrant(client, f.authCode(client, claudeCallback), claudeCallback)
			v.Set("resource", "https://other.example.com/mcp")
			return v
		}, "invalid_target"},
		"unknown client": {func() url.Values {
			return codeGrant("garbage", f.authCode(client, claudeCallback), claudeCallback)
		}, "invalid_client"},
		"password grant": {func() url.Values {
			return url.Values{"grant_type": {"password"}, "client_id": {client}}
		}, "unsupported_grant_type"},
	}
	for name, tc := range cases {
		wantOAuthError(t, name, f.tokenRequest(tc.request()), tc.want)
	}

	// A failed presentation burns the code.
	code := f.authCode(client, claudeCallback)
	f.tokenRequest(codeGrant(client, code, "https://claude.ai/other"))
	wantOAuthError(t, "code after a failed presentation", f.tokenRequest(codeGrant(client, code, claudeCallback)), "invalid_grant")

	code = f.authCode(client, claudeCallback)
	f.now = f.now.Add(codeTTL + time.Second)
	wantOAuthError(t, "expired code", f.tokenRequest(codeGrant(client, code, claudeCallback)), "invalid_grant")

	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader("%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wantOAuthError(t, "malformed body", f.serve(req), "invalid_request")
}

func TestTokenRefresh(t *testing.T) {
	f := newLoginFixture(t, nil)
	client := f.register(claudeCallback)
	first := f.grant(codeGrant(client, f.authCode(client, claudeCallback), claudeCallback))

	// A client in use keeps sliding its 30-day window forward.
	f.now = f.now.Add(29 * 24 * time.Hour)
	second := f.grant(refreshGrant(client, first.RefreshToken))
	if _, err := f.s.verify(t.Context(), second.AccessToken, nil); err != nil {
		t.Errorf("refreshed access token rejected: %v", err)
	}
	f.now = f.now.Add(29 * 24 * time.Hour)
	third := f.grant(refreshGrant(client, second.RefreshToken))

	wantOAuthError(t, "access token as refresh", f.tokenRequest(refreshGrant(client, third.AccessToken)), "invalid_grant")
	wantOAuthError(t, "another client's refresh", f.tokenRequest(refreshGrant(f.register(claudeCallback), third.RefreshToken)), "invalid_grant")

	moved := newLoginFixture(t, func(m *config.APIConfig) { m.ResourceURL = "https://other.example.com" })
	moved.now = f.now
	wantOAuthError(t, "after resource change", moved.tokenRequest(refreshGrant(client, third.RefreshToken)), "invalid_grant")

	f.now = f.now.Add(refreshTokenTTL + time.Second)
	wantOAuthError(t, "idle for 30 days", f.tokenRequest(refreshGrant(client, third.RefreshToken)), "invalid_grant")
}

func TestUsedCodesPrunesExpired(t *testing.T) {
	var u usedCodes
	if !u.claim("a", loginEpoch.Add(time.Minute), loginEpoch) || u.claim("a", loginEpoch.Add(time.Minute), loginEpoch) {
		t.Fatal("claim is not single-use")
	}
	later := loginEpoch.Add(2 * time.Minute)
	if !u.claim("b", later.Add(time.Minute), later) || len(u.seen) != 1 {
		t.Errorf("expired entries not pruned: %v", u.seen)
	}
}

func TestResourceParameterMayNameAPathUnderTheOrigin(t *testing.T) {
	f := newLoginFixture(t, nil)
	for r, want := range map[string]bool{
		"https://mcp.example.com":          true,
		"https://mcp.example.com/":         true,
		"https://mcp.example.com/mcp":      true,
		"https://mcp.example.com/api":      true,
		"https://mcp.example.com.evil.com": false,
		"https://mcp.example.com@evil.com": false,
		"https://mcp.example.com:8443/mcp": false,
		"https://other.example.com/mcp":    false,
		"http://mcp.example.com/mcp":       false,
		"":                                 false,
	} {
		if got := f.s.resourceAccepted(r); got != want {
			t.Errorf("resourceAccepted(%q) = %v, want %v", r, got, want)
		}
	}
}

// TestResourceUnderTheOriginGetsAnOriginToken runs the login with the
// resource an MCP client sends, the URL it connected to, and checks the
// token still names the origin, so the same token opens /api/.
func TestResourceUnderTheOriginGetsAnOriginToken(t *testing.T) {
	f := newLoginFixture(t, nil)
	client := f.register(claudeCallback)
	q := authorizeQuery(client, claudeCallback)
	q.Set("resource", testResource+"/mcp")
	loc, err := url.Parse(f.submit(f.loginForm(q), testPassword).Header().Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("login with resource=%s/mcp: %v %q", testResource, err, loc)
	}
	grant := codeGrant(client, loc.Query().Get("code"), claudeCallback)
	grant.Set("resource", testResource+"/mcp")
	got := f.grant(grant)
	var c grantClaims
	if err := f.s.keys.parse(kindAccess, got.AccessToken, &c, f.s.now); err != nil {
		t.Fatal(err)
	}
	if len(c.Audience) != 1 || c.Audience[0] != testResource {
		t.Errorf("aud = %v, want only the origin %s", c.Audience, testResource)
	}
	if _, err := f.s.verify(t.Context(), got.AccessToken, nil); err != nil {
		t.Errorf("token rejected: %v", err)
	}

	sibling := authorizeQuery(client, claudeCallback)
	sibling.Set("resource", testResource+".evil.com/mcp")
	if loc, _ := url.Parse(f.authorize(sibling).Header().Get("Location")); loc == nil || loc.Query().Get("error") != "invalid_target" {
		t.Errorf("authorize with a sibling-host resource: %v, want invalid_target", loc)
	}
	grant = refreshGrant(client, got.RefreshToken)
	grant.Set("resource", testResource+".evil.com")
	wantOAuthError(t, "refresh with a sibling-host resource", f.tokenRequest(grant), "invalid_target")
}
