package apiserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

var loginEpoch = time.Date(2026, 9, 12, 12, 0, 30, 0, time.UTC)

func fixedNow() time.Time { return loginEpoch }

func TestDeriveLoginKeys(t *testing.T) {
	a := deriveLoginKeys("ar_token", "pw")
	b := deriveLoginKeys("ar_token", "pw")
	for _, kd := range []kind{kindClient, kindForm, kindCode, kindAccess, kindRefresh} {
		if len(a[kd]) != 32 {
			t.Fatalf("%s key length = %d, want 32", kd, len(a[kd]))
		}
		if string(a[kd]) != string(b[kd]) {
			t.Errorf("%s key differs between derivations; a restart would log everyone out", kd)
		}
	}
	if string(a[kindCode]) == string(a[kindAccess]) {
		t.Error("code and access keys are equal; one kind could pass as another")
	}
	for name, other := range map[string]loginKeys{
		"token changed":    deriveLoginKeys("ar_other", "pw"),
		"password changed": deriveLoginKeys("ar_token", "pw2"),
		"bytes shifted":    deriveLoginKeys("ar_tokenp", "w"),
	} {
		if string(other[kindAccess]) == string(a[kindAccess]) {
			t.Errorf("%s: access key unchanged", name)
		}
	}
}

func TestSignedValuesRoundTrip(t *testing.T) {
	k := deriveLoginKeys("ar_token", "pw")
	raw := k.sign(kindCode, codeClaims{ClientID: "c1", RedirectURI: "https://claude.ai/cb", CodeChallenge: "ch",
		RegisteredClaims: jwt.RegisteredClaims{ID: "j1", ExpiresAt: jwt.NewNumericDate(loginEpoch.Add(time.Minute))}})
	var got codeClaims
	if err := k.parse(kindCode, raw, &got, fixedNow, jwt.WithExpirationRequired()); err != nil {
		t.Fatal(err)
	}
	if got.ClientID != "c1" || got.RedirectURI != "https://claude.ai/cb" || got.CodeChallenge != "ch" || got.ID != "j1" {
		t.Errorf("claims = %+v", got)
	}
}

func TestSignedValuesRejected(t *testing.T) {
	k := deriveLoginKeys("ar_token", "pw")
	valid := jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(loginEpoch.Add(time.Minute))}
	code := k.sign(kindCode, codeClaims{RegisteredClaims: valid})

	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, valid).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, valid).SignedString(k[kindAccess])
	if err != nil {
		t.Fatal(err)
	}
	expired := k.sign(kindAccess, grantClaims{RegisteredClaims: jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(loginEpoch.Add(-time.Second))}})
	noExp := k.sign(kindAccess, grantClaims{})

	cases := map[string]string{
		"code presented as access": code,
		"alg none":                 none,
		"HS512":                    hs512,
		"expired":                  expired,
		"missing exp":              noExp,
		"garbage":                  "ar_token",
	}
	for name, raw := range cases {
		var c grantClaims
		if err := k.parse(kindAccess, raw, &c, fixedNow, jwt.WithExpirationRequired()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := deriveLoginKeys("ar_token", "new").parse(kindCode, code, &codeClaims{}, fixedNow); err == nil {
		t.Error("value signed before a password change still verifies")
	}
}

const (
	testToken      = "ar_testtoken"
	testPassword   = "hunter2"
	testResource   = "https://mcp.example.com/mcp"
	testIssuer     = "https://mcp.example.com"
	claudeCallback = "https://claude.ai/api/mcp/auth_callback"
	loopbackEntry  = "http://localhost/callback"
)

// loginFixture is a login server on its own mux with a settable clock and
// captured logs.
type loginFixture struct {
	t    *testing.T
	s    *loginServer
	mux  *http.ServeMux
	now  time.Time
	logs *strings.Builder
}

func newLoginFixture(t *testing.T, over func(*config.APIConfig)) *loginFixture {
	t.Helper()
	m := config.APIConfig{AuthMode: "token", Token: testToken, Password: testPassword, ResourceURL: testResource}
	if over != nil {
		over(&m)
	}
	f := &loginFixture{t: t, now: loginEpoch, logs: &strings.Builder{}}
	f.s = newLoginServer(m, slog.New(slog.NewTextHandler(f.logs, nil)))
	f.s.now = func() time.Time { return f.now }
	f.mux = http.NewServeMux()
	f.s.mount(f.mux)
	return f
}

func (f *loginFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *loginFixture) registerRaw(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return f.serve(req)
}

// register registers a client named "Claude" and returns its client_id.
func (f *loginFixture) register(redirects ...string) string {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"redirect_uris": redirects, "client_name": "Claude"})
	rec := f.registerRaw(string(body))
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		f.t.Fatal(err)
	}
	return resp.ClientID
}

// mint signs a grant of kind kd for this fixture's issuer and resource.
func (f *loginFixture) mint(kd kind, ttl time.Duration) string {
	return f.s.keys.sign(kd, grantClaims{ClientID: "c", RegisteredClaims: jwt.RegisteredClaims{
		Issuer: testIssuer, Subject: "mcp", Audience: jwt.ClaimStrings{testResource},
		ExpiresAt: jwt.NewNumericDate(f.now.Add(ttl))}})
}

func TestRedirectMatches(t *testing.T) {
	cases := []struct {
		allowed, candidate string
		want               bool
	}{
		{claudeCallback, claudeCallback, true},
		{claudeCallback, "https://CLAUDE.ai/api/mcp/auth_callback", true},
		{claudeCallback, "https://claude.ai/api/mcp/auth_callback/", false},
		{claudeCallback, "https://claude.ai:443/api/mcp/auth_callback", false},
		{claudeCallback, "http://claude.ai/api/mcp/auth_callback", false},
		{claudeCallback, "https://claude.ai/api/mcp/auth_callback?x=1", false},
		{claudeCallback, "https://claude.ai/api/mcp/auth_callback#f", false},
		{claudeCallback, "https://evil@claude.ai/api/mcp/auth_callback", false},
		{claudeCallback, "https://claude.ai.evil.com/api/mcp/auth_callback", false},
		{claudeCallback, "/api/mcp/auth_callback", false},
		{claudeCallback, "%zz", false},
		{"%zz", claudeCallback, false},
		{loopbackEntry, "http://localhost:53124/callback", true},
		{"http://localhost:8080/callback", "http://localhost:53124/callback", true},
		{loopbackEntry, "http://127.0.0.1:53124/callback", false},
		{loopbackEntry, "http://localhost:53124/other", false},
		{"http://[::1]/callback", "http://[::1]:9/callback", true},
		{"https://app.example.com:8443/cb", "https://app.example.com:9443/cb", false},
	}
	for _, tc := range cases {
		if got := redirectMatches(tc.allowed, tc.candidate); got != tc.want {
			t.Errorf("redirectMatches(%q, %q) = %v, want %v", tc.allowed, tc.candidate, got, tc.want)
		}
	}
}

func TestRedirectAllowed(t *testing.T) {
	hosts := []string{"app.example.com"}
	cases := []struct {
		candidate string
		want      bool
	}{
		// Loopback needs no entry: the code can only reach this machine.
		{"http://127.0.0.1:53124/callback/abc123", true},
		{"http://localhost:8080/any/path?x=1", true},
		{"http://[::1]:9/cb", true},
		{"https://localhost/cb", true},
		{"myapp://127.0.0.1/cb", false},
		{"http://127.0.0.1.evil.com/cb", false},
		// The web connectors are built in, any path, https only, no subdomains.
		{claudeCallback, true},
		{"https://claude.ai/some/other/callback", true},
		{"https://chatgpt.com/connector_platform_oauth_redirect", true},
		{"https://CHATGPT.com/elsewhere", true},
		{"http://claude.ai/api/mcp/auth_callback", false},
		{"https://foo.claude.ai/cb", false},
		{"https://claude.ai.evil.com/cb", false},
		// Configured hosts, likewise.
		{"https://app.example.com/cb?tenant=1", true},
		{"https://app.example.com:8443/cb", true},
		{"http://app.example.com/cb", false},
		{"https://evil.example/cb", false},
		// Never: fragments, userinfo, garbage.
		{"https://claude.ai/cb#f", false},
		{"https://evil@claude.ai/cb", false},
		{"/relative/cb", false},
		{"%zz", false},
	}
	for _, tc := range cases {
		if got := redirectAllowed(hosts, tc.candidate); got != tc.want {
			t.Errorf("redirectAllowed(%q) = %v, want %v", tc.candidate, got, tc.want)
		}
	}
}

func TestLoginMetadata(t *testing.T) {
	f := newLoginFixture(t, nil)
	rec := f.serve(httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var meta map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"issuer":                 testIssuer,
		"authorization_endpoint": testIssuer + "/oauth/authorize",
		"token_endpoint":         testIssuer + "/oauth/token",
		"registration_endpoint":  testIssuer + "/oauth/register",
		"authorization_response_iss_parameter_supported": true,
	} {
		if meta[key] != want {
			t.Errorf("%s = %v, want %v", key, meta[key], want)
		}
	}
	if got, _ := json.Marshal(meta["code_challenge_methods_supported"]); string(got) != `["S256"]` {
		t.Errorf("code_challenge_methods_supported = %s", got)
	}
	if got, _ := json.Marshal(meta["grant_types_supported"]); string(got) != `["authorization_code","refresh_token"]` {
		t.Errorf("grant_types_supported = %s", got)
	}
	if _, ok := meta["jwks_uri"]; ok {
		t.Error("metadata carries jwks_uri; there are no public keys to publish")
	}

	rec = f.serve(httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"resource":"`+testResource+`"`) ||
		!strings.Contains(rec.Body.String(), testIssuer) {
		t.Errorf("PRM = %d %s", rec.Code, rec.Body)
	}
}

func TestRegisterClient(t *testing.T) {
	f := newLoginFixture(t, nil)
	long := strings.Repeat("é", 80)
	rec := f.registerRaw(`{"redirect_uris":["` + claudeCallback + `","http://localhost:4000/callback"],` +
		`"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"client_secret_basic",` +
		`"application_type":"web","client_name":"` + long + `"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		ClientID     string   `json:"client_id"`
		IssuedAt     int64    `json:"client_id_issued_at"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
		GrantTypes   []string `json:"grant_types"`
		ClientName   string   `json:"client_name"`
		ClientSecret string   `json:"client_secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AuthMethod != "none" || resp.ClientSecret != "" {
		t.Errorf("auth method = %q secret = %q; every client is public", resp.AuthMethod, resp.ClientSecret)
	}
	if len(resp.GrantTypes) != 2 || resp.IssuedAt != loginEpoch.Unix() {
		t.Errorf("grant types = %v issued at = %d", resp.GrantTypes, resp.IssuedAt)
	}
	if len([]rune(resp.ClientName)) != 64 {
		t.Errorf("client_name has %d runes, want 64", len([]rune(resp.ClientName)))
	}
	var c clientClaims
	if err := f.s.keys.parse(kindClient, resp.ClientID, &c, fixedNow); err != nil || len(c.RedirectURIs) != 2 {
		t.Errorf("client_id does not carry the registration: %v %+v", err, c)
	}

	for name, tc := range map[string]struct{ body, errCode string }{
		"not json":             {`{`, "invalid_client_metadata"},
		"no redirects":         {`{}`, "invalid_redirect_uri"},
		"redirect not allowed": {`{"redirect_uris":["https://evil.example/cb"]}`, "invalid_redirect_uri"},
		"one of two disallowed": {`{"redirect_uris":["` + claudeCallback + `","https://evil.example/cb"]}`,
			"invalid_redirect_uri"},
		"client_credentials": {`{"redirect_uris":["` + claudeCallback + `"],"grant_types":["client_credentials"]}`,
			"invalid_client_metadata"},
		"implicit response type": {`{"redirect_uris":["` + claudeCallback + `"],"response_types":["token"]}`,
			"invalid_client_metadata"},
		"oversized body": {`{"redirect_uris":["` + claudeCallback + `"],"client_name":"` +
			strings.Repeat("x", 17<<10) + `"}`, "invalid_client_metadata"},
	} {
		rec := f.registerRaw(tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"`+tc.errCode+`"`) {
			t.Errorf("%s: %d %s, want 400 %s", name, rec.Code, rec.Body, tc.errCode)
		}
	}
	if !strings.Contains(f.logs.String(), "https://evil.example/cb") {
		t.Error("rejected registration redirect not logged")
	}

	// Loopback clients and the built-in web connectors register without
	// any allowlist entry.
	bare := newLoginFixture(t, nil)
	if id := bare.register("http://127.0.0.1:1455/callback/abc123", claudeCallback,
		"https://chatgpt.com/connector_platform_oauth_redirect"); id == "" {
		t.Error("default registration returned no client_id")
	}
}

func TestLoginVerifier(t *testing.T) {
	f := newLoginFixture(t, nil)
	verify := func(s *loginServer, token string) error {
		_, err := s.verify(t.Context(), token, httptest.NewRequest("POST", "/mcp", nil))
		return err
	}

	info, err := f.s.verify(t.Context(), testToken, nil)
	if err != nil || info.UserID != "mcp" || !info.Expiration.IsZero() {
		t.Errorf("env token: %v %+v", err, info)
	}
	access := f.mint(kindAccess, time.Hour)
	info, err = f.s.verify(t.Context(), access, nil)
	if err != nil || info.UserID != "mcp" || !info.Expiration.Equal(loginEpoch.Add(time.Hour).Truncate(time.Second)) {
		t.Errorf("access token: %v %+v", err, info)
	}

	rejected := map[string]error{
		"refresh token as access": verify(f.s, f.mint(kindRefresh, time.Hour)),
		"wrong token":             verify(f.s, "ar_wrong"),
		"after password change": verify(newLoginFixture(t, func(m *config.APIConfig) {
			m.Password = "changed"
		}).s, access),
		"after token change": verify(newLoginFixture(t, func(m *config.APIConfig) {
			m.Token = "ar_changed"
		}).s, access),
		"after resource change": verify(newLoginFixture(t, func(m *config.APIConfig) {
			m.ResourceURL = "https://other.example.com/mcp"
		}).s, access),
	}
	f.now = loginEpoch.Add(2 * time.Hour)
	rejected["expired"] = verify(f.s, access)
	for name, err := range rejected {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
