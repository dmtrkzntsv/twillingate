# Browser login for `token://` MCP auth — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let claude.ai, Claude Desktop and Claude Code log in to the `token://` MCP endpoint through a built-in OAuth 2.1 authorization server whose only credential is a password from `MCP_AUTH_DSN`.

**Architecture:** `internal/config` parses `redirect=`, `password=` and `resource=` from the `token://` query. `internal/mcpserver` gains a stateless `loginServer`: every value it issues is an HS256 JWT under HKDF keys derived from the token and password. `RegisterOn` mounts its routes; `wrapAuth` accepts either the env token or an issued access token.

**Tech Stack:** Go 1.25 standard library (`crypto/hkdf`, `html/template`, `embed`), `github.com/golang-jwt/jwt/v5`, `github.com/modelcontextprotocol/go-sdk` v1.7 (`auth`, `oauthex`, `mcp`).

**Spec:** `docs/superpowers/specs/2026-09-12-mcp-token-login-design.md`

## Global Constraints

- No new package, no new module dependency, no new environment variable.
- Go is at `/usr/local/go/bin/go` and is not on `PATH`: prefix commands with `PATH=/usr/local/go/bin:$PATH`.
- `make check` fails locally on its restore step (no `sqlite3`); run `./scripts/coverage.sh` instead. It requires ≥ 90% total and ≥ 90% average per-function coverage in `internal/config` and `internal/mcpserver`.
- Secrets never logged: password, token, codes, access/refresh tokens, the `request` form value.
- Lifetimes: form 10 min, code 60 s, access 1 h, refresh 30 days. Limiter: 5 failures per fixed one-minute window, global.
- Loopback hosts are exactly `localhost`, `127.0.0.1`, `::1` (hostname form).
- Commit subjects follow Conventional Commits (`CLAUDE.md`); end each message with `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.
- `docs/deployment.md` changes land in the same commit as the code that needs them (Task 6).

## Deviations from the spec, resolved while planning

Task 7 folds these into the spec:

1. `RegisterOn` gains a `logger *slog.Logger` parameter (the login server logs); `internal/app/app.go` changes one call.
2. `jti` values are `crypto/rand.Text()` (26 base32 characters, 130 bits) instead of 16 bytes base64url.
3. The form value also carries `client_name`, so the page can be re-rendered after a wrong password.
4. At `POST /oauth/token`, `redirect_uri` is compared for exact equality with the code's value (RFC 6749 §4.1.3); §5.1 matching runs at registration and `GET /oauth/authorize` only.
5. The metadata document is a local struct, not `oauthex.AuthServerMeta`, whose `jwks_uri` has no `omitempty` and would emit `"jwks_uri": ""`.

## File Structure

| File | Responsibility |
| --- | --- |
| `internal/config/config.go` | Parse and validate the `token://` login query |
| `internal/mcpserver/oauth_keys.go` | HKDF key derivation, claim types, sign/parse |
| `internal/mcpserver/oauth.go` | `loginServer`: construction, routes, metadata, registration, redirect matching, verifier |
| `internal/mcpserver/oauth_authorize.go` | Login page GET/POST, failure limiter, page rendering |
| `internal/mcpserver/oauth_token.go` | Token endpoint, used-code set, PKCE check |
| `internal/mcpserver/oauth_page.html` | Embedded login/error page |
| `internal/mcpserver/server.go` | `RegisterOn` mounts the login server; `wrapAuth` token case; `originOf` |
| `internal/app/app.go` | Pass `logger` to `RegisterOn` |
| `internal/mcpserver/oauth_test.go` | Shared fixture + unit tests for keys, metadata, registration, matching, verifier |
| `internal/mcpserver/oauth_authorize_test.go` | Login page tests |
| `internal/mcpserver/oauth_token_test.go` | Token endpoint tests |
| `internal/mcpserver/oauth_e2e_test.go` | SDK OAuth client end to end |
| `docs/deployment.md`, `.env.example` | Operator documentation |

---

### Task 1: Parse the `token://` login query

**Files:**
- Modify: `internal/config/config.go` (`MCPConfig` struct ~line 133-163; `parseMCPAuthDSN` `case "token"` ~line 367-373)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.MCPConfig.RedirectURIs []string`, `config.MCPConfig.Password string`; `MCPConfig.ResourceURL` is set in token mode when any `redirect=` is present. Login server is on iff `len(RedirectURIs) > 0`.

- [ ] **Step 1: Write the failing tests**

Append to the `cases` slice in `TestValidateMCP` (`internal/config/config_test.go`), after the `"cloudflare missing team"` case:

```go
		{"token login ok", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/api/mcp/auth_callback&resource=https://mcp.example.com/mcp"}, true},
		{"token login resource from PUBLIC_URL", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/api/mcp/auth_callback",
			"PUBLIC_URL":   "https://mcp.example.com"}, true},
		{"token login one-character password", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=a&redirect=http://localhost/callback&resource=https://mcp.example.com/mcp"}, true},
		{"token login http loopback redirect", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=http://127.0.0.1/callback&resource=https://mcp.example.com/mcp"}, true},
		{"token login no resource and no PUBLIC_URL", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/cb"}, false},
		{"token login no password", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?redirect=https://claude.ai/cb&resource=https://mcp.example.com/mcp"}, false},
		{"token login empty password", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=&redirect=https://claude.ai/cb&resource=https://mcp.example.com/mcp"}, false},
		{"token password without redirect", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw"}, false},
		{"token resource without redirect", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?resource=https://mcp.example.com/mcp"}, false},
		{"token unknown parameter", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirects=https://claude.ai/cb&resource=https://mcp.example.com/mcp"}, false},
		{"token malformed query", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=%zz"}, false},
		{"token empty token with query", map[string]string{
			"MCP_AUTH_DSN": "token://?password=pw&redirect=https://claude.ai/cb"}, false},
		{"token redirect http on public host", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=http://claude.ai/cb&resource=https://mcp.example.com/mcp"}, false},
		{"token redirect relative", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=/callback&resource=https://mcp.example.com/mcp"}, false},
		{"token redirect custom scheme", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=myapp://callback&resource=https://mcp.example.com/mcp"}, false},
		{"token redirect with fragment", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/cb%23frag&resource=https://mcp.example.com/mcp"}, false},
		{"token redirect unparseable", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=https://%25zz/cb&resource=https://mcp.example.com/mcp"}, false},
		{"token resource http on public host", map[string]string{
			"MCP_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/cb&resource=http://mcp.example.com/mcp"}, false},
```

Append a new test at the end of the file:

```go
func TestTokenLoginDSNParsing(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(map[string]string{
		"MCP_AUTH_DSN": "token://ar_x?redirect=https://claude.ai/api/mcp/auth_callback&password=a%2Bb%26c&redirect=http://localhost/callback",
		"PUBLIC_URL":   "https://mcp.example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateMCP(); err != nil {
		t.Fatal(err)
	}
	m := cfg.MCP
	if m.AuthMode != "token" || m.Token != "ar_x" {
		t.Errorf("mode = %q token = %q; the query must not leak into the token", m.AuthMode, m.Token)
	}
	if m.Password != "a+b&c" {
		t.Errorf("password = %q, want percent-decoded %q", m.Password, "a+b&c")
	}
	want := []string{"https://claude.ai/api/mcp/auth_callback", "http://localhost/callback"}
	if !slices.Equal(m.RedirectURIs, want) {
		t.Errorf("redirects = %v, want %v in DSN order", m.RedirectURIs, want)
	}
	if m.ResourceURL != "https://mcp.example.com/mcp" {
		t.Errorf("resource = %q, want PUBLIC_URL + /mcp", m.ResourceURL)
	}

	plain, err := FromEnv(mcpEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.MCP.RedirectURIs) != 0 || plain.MCP.Password != "" || plain.MCP.ResourceURL != "" {
		t.Errorf("plain token:// grew login settings: %+v", plain.MCP)
	}
}
```

Add `"slices"` to the test file's imports if absent.

- [ ] **Step 2: Run tests to verify they fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/config/ -run 'TestValidateMCP|TestTokenLoginDSNParsing'`
Expected: FAIL — compile error `m.RedirectURIs undefined` (and, once fields exist, the new rejection cases pass wrongly as ok).

- [ ] **Step 3: Implement**

In `MCPConfig`, replace the doc comment's DSN list and add two fields after `Token`:

```go
// MCPConfig carries the -mcp surface settings. Authentication comes from
// the single MCP_AUTH_DSN; parsing fans it out into the mode-specific
// fields the verifiers consume:
//
//	token://<token>[?password=<pw>&redirect=<uri>[&redirect=<uri>…][&resource=<url>]]
//	cloudflare://<team>.cloudflareaccess.com?aud=<application AUD tag>
//	oauth://<issuer-host>[/path][?resource=<url>][&audience=<aud>]
//
// Any redirect= on token:// turns on the browser login server, which
// requires password= and a resource URL. In token and oauth modes resource
// defaults to PUBLIC_URL + "/mcp"; the oauth issuer is https://<host>[/path]
// and audience defaults to the resource URL. The +insecure scheme variants
// (oauth+insecure, cloudflare+insecure) produce an http issuer for local
// IdPs and tests.
```

```go
	Token        string
	RedirectURIs []string // token:// redirect=; any turns on the login server
	Password     string   // token:// password=, the login page's secret
```

Replace the `case "token":` body in `parseMCPAuthDSN`:

```go
	case "token":
		// Not URL-parsed: the token is opaque and must survive verbatim.
		// Everything after the first '?' configures the login server.
		token, query, hasQuery := strings.Cut(rest, "?")
		m.AuthMode = "token"
		m.Token = token
		if m.Token == "" {
			return fmt.Errorf("config: MCP_AUTH_DSN token:// requires a token (mint with `twillingate keygen -mcp`)")
		}
		if hasQuery {
			return c.parseTokenLogin(query)
		}
```

Add after `parseMCPAuthDSN`:

```go
// parseTokenLogin reads the token:// query that turns on the browser login
// server: repeated redirect=, password= and resource=.
func (c *Config) parseTokenLogin(query string) error {
	m := &c.MCP
	q, err := url.ParseQuery(query)
	if err != nil {
		return fmt.Errorf("config: invalid MCP_AUTH_DSN token:// query: %v", err)
	}
	for k := range q {
		if k != "redirect" && k != "password" && k != "resource" {
			return fmt.Errorf("config: MCP_AUTH_DSN token:// has unknown parameter %q (redirect, password or resource)", k)
		}
	}
	m.RedirectURIs = q["redirect"]
	m.Password = q.Get("password")
	m.ResourceURL = q.Get("resource")
	if len(m.RedirectURIs) == 0 {
		return fmt.Errorf("config: MCP_AUTH_DSN token:// password= and resource= only apply with at least one redirect=")
	}
	if m.Password == "" {
		return fmt.Errorf("config: MCP_AUTH_DSN token:// redirect= requires a password=")
	}
	for _, r := range m.RedirectURIs {
		if err := checkLoginURL(r); err != nil {
			return fmt.Errorf("config: MCP_AUTH_DSN token:// redirect=%q %v", r, err)
		}
	}
	if m.ResourceURL == "" && c.PublicURL != "" {
		m.ResourceURL = c.PublicURL + "/mcp"
	}
	if m.ResourceURL == "" {
		return fmt.Errorf("config: MCP_AUTH_DSN token:// redirect= requires resource=<url> or PUBLIC_URL to derive it from")
	}
	if err := checkLoginURL(m.ResourceURL); err != nil {
		return fmt.Errorf("config: MCP_AUTH_DSN token:// resource=%q %v", m.ResourceURL, err)
	}
	return nil
}

// checkLoginURL admits an absolute http(s) URL with no fragment, and plain
// http only on a loopback host.
func checkLoginURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("does not parse: %v", err)
	}
	if u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("must be an absolute http(s) URL")
	}
	if strings.Contains(raw, "#") {
		return fmt.Errorf("must not carry a fragment")
	}
	if h := u.Hostname(); u.Scheme == "http" && h != "localhost" && h != "127.0.0.1" && h != "::1" {
		return fmt.Errorf("may only use http on localhost, 127.0.0.1 or [::1]")
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/config/`
Expected: PASS (all existing tests too).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): parse the token:// login query for mcp auth

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

### Task 2: Keys and signed values

**Files:**
- Create: `internal/mcpserver/oauth_keys.go`
- Test: `internal/mcpserver/oauth_test.go` (created here; later tasks append)

**Interfaces:**
- Produces:
  - `type kind string` with constants `kindClient`, `kindForm`, `kindCode`, `kindAccess`, `kindRefresh`
  - `type loginKeys map[kind][]byte`; `func deriveLoginKeys(token, password string) loginKeys`
  - `func (k loginKeys) sign(kd kind, claims jwt.Claims) string`
  - `func (k loginKeys) parse(kd kind, raw string, claims jwt.Claims, now func() time.Time, opts ...jwt.ParserOption) error`
  - Claim types: `clientClaims{RedirectURIs []string; ClientName string; jwt.RegisteredClaims}`, `formClaims{ClientID, ClientName, RedirectURI, CodeChallenge, State string; jwt.RegisteredClaims}`, `codeClaims{ClientID, RedirectURI, CodeChallenge string; jwt.RegisteredClaims}`, `grantClaims{ClientID string; jwt.RegisteredClaims}` (access and refresh)

- [ ] **Step 1: Write the failing tests**

Create `internal/mcpserver/oauth_test.go`:

```go
package mcpserver

import (
	"testing"
	"time"

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/mcpserver/ -run 'TestDeriveLoginKeys|TestSignedValues'`
Expected: FAIL — `undefined: deriveLoginKeys`.

- [ ] **Step 3: Implement**

Create `internal/mcpserver/oauth_keys.go`:

```go
package mcpserver

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Every value the token:// login server hands out is an HS256 JWT under a
// key derived from the token and the password (login spec §8). Nothing is
// stored: a restart derives the same keys, and changing either secret
// invalidates everything at once.
//
// HMAC is right here because the server only verifies what it signed
// itself; verifyJWT's asymmetric-only allowlist guards a different path.

type kind string

const (
	kindClient  kind = "client"
	kindForm    kind = "form"
	kindCode    kind = "code"
	kindAccess  kind = "access"
	kindRefresh kind = "refresh"
)

// loginKeys holds one key per kind, so a value of one kind never verifies
// as another even when its claims would fit.
type loginKeys map[kind][]byte

func deriveLoginKeys(token, password string) loginKeys {
	// Length prefixes keep ("ab", "c") and ("a", "bc") apart.
	secret := binary.BigEndian.AppendUint32(nil, uint32(len(token)))
	secret = append(secret, token...)
	secret = binary.BigEndian.AppendUint32(secret, uint32(len(password)))
	secret = append(secret, password...)
	keys := loginKeys{}
	for _, kd := range []kind{kindClient, kindForm, kindCode, kindAccess, kindRefresh} {
		key, err := hkdf.Key(sha256.New, secret, nil, "twillingate mcp "+string(kd), 32)
		if err != nil {
			panic(err) // only for key lengths beyond 255 hash blocks
		}
		keys[kd] = key
	}
	return keys
}

func (k loginKeys) sign(kd kind, claims jwt.Claims) string {
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(k[kd])
	if err != nil {
		panic(err) // HS256 over marshalable claims with a []byte key cannot fail
	}
	return s
}

// parse verifies raw as a kd value into claims. HS256 is the only accepted
// algorithm; now is the clock for exp.
func (k loginKeys) parse(kd kind, raw string, claims jwt.Claims, now func() time.Time, opts ...jwt.ParserOption) error {
	opts = append(opts, jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(now))
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return k[kd], nil }, opts...)
	return err
}

// clientClaims is a client_id: the registration, signed.
type clientClaims struct {
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name,omitempty"`
	jwt.RegisteredClaims
}

// formClaims is the login page's hidden request field.
type formClaims struct {
	ClientID      string `json:"client_id"`
	ClientName    string `json:"client_name,omitempty"`
	RedirectURI   string `json:"redirect_uri"`
	CodeChallenge string `json:"code_challenge"`
	State         string `json:"state,omitempty"`
	jwt.RegisteredClaims
}

// codeClaims is an authorization code.
type codeClaims struct {
	ClientID      string `json:"client_id"`
	RedirectURI   string `json:"redirect_uri"`
	CodeChallenge string `json:"code_challenge"`
	jwt.RegisteredClaims
}

// grantClaims is an access or a refresh token.
type grantClaims struct {
	ClientID string `json:"client_id"`
	jwt.RegisteredClaims
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/mcpserver/ -run 'TestDeriveLoginKeys|TestSignedValues'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/oauth_keys.go internal/mcpserver/oauth_test.go
git commit -m "feat(mcpserver): derive signing keys for the token:// login server

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

### Task 3: Login server core — discovery, registration, redirect matching, verifier

**Files:**
- Create: `internal/mcpserver/oauth.go`
- Modify: `internal/mcpserver/server.go` (`metadataURLFor` ~line 128-141)
- Test: `internal/mcpserver/oauth_test.go` (append)

**Interfaces:**
- Consumes (Task 1): `config.MCPConfig{Token, Password, RedirectURIs, ResourceURL}`. (Task 2): `deriveLoginKeys`, `loginKeys.sign/parse`, `clientClaims`, `grantClaims`, `kind*`.
- Produces:
  - `type loginServer struct` with fields `keys loginKeys`, `static auth.TokenVerifier`, `password []byte`, `redirects []string`, `resource string`, `issuer string`, `accessTTL time.Duration`, `logger *slog.Logger`, `now func() time.Time`
  - `func newLoginServer(m config.MCPConfig, logger *slog.Logger) *loginServer`
  - `func (s *loginServer) mount(mux *http.ServeMux)` — Tasks 4 and 5 add routes to it
  - `func (s *loginServer) verify(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error)`
  - `func redirectMatches(allowed, candidate string) bool`, `func matchesAny(allowed []string, candidate string) bool`, `func isLoopbackHost(h string) bool`
  - `func writeJSON(w http.ResponseWriter, status int, v any)`, `func oauthError(w http.ResponseWriter, code, description string)`
  - `func originOf(raw string) string` (in `server.go`)
  - package vars/consts: `var accessTokenTTL = time.Hour`; `formTTL`, `codeTTL`, `refreshTokenTTL`, `maxFailuresPerMinute`, `maxOAuthBody`
  - test fixture (in `oauth_test.go`): `loginFixture` with `t`, `s`, `mux`, `now`, `logs`; `newLoginFixture(t, over func(*config.MCPConfig))`; methods `serve(*http.Request)`, `registerRaw(body string)`, `register(redirects ...string) string`, `mint(kd kind, ttl time.Duration) string`; constants `testToken`, `testPassword`, `testResource`, `testIssuer`, `claudeCallback`, `loopbackEntry`

- [ ] **Step 1: Write the failing tests**

Replace the import block of `internal/mcpserver/oauth_test.go` and append below the existing tests:

```go
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
```

```go
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

func newLoginFixture(t *testing.T, over func(*config.MCPConfig)) *loginFixture {
	t.Helper()
	m := config.MCPConfig{AuthMode: "token", Token: testToken, Password: testPassword,
		RedirectURIs: []string{claudeCallback, loopbackEntry}, ResourceURL: testResource}
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
		"after password change": verify(newLoginFixture(t, func(m *config.MCPConfig) {
			m.Password = "changed"
		}).s, access),
		"after token change": verify(newLoginFixture(t, func(m *config.MCPConfig) {
			m.Token = "ar_changed"
		}).s, access),
		"after resource change": verify(newLoginFixture(t, func(m *config.MCPConfig) {
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/mcpserver/ -run 'TestRedirectMatches|TestLoginMetadata|TestRegisterClient|TestLoginVerifier'`
Expected: FAIL — `undefined: newLoginServer`.

- [ ] **Step 3: Implement**

In `internal/mcpserver/server.go`, replace `metadataURLFor` and add `originOf` (add `"net/url"` to imports):

```go
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
```

Create `internal/mcpserver/oauth.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/mcpserver/`
Expected: PASS, including the existing `TestMCPRequires401WithChallenge` (exercises the `metadataURLFor` refactor).

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/oauth.go internal/mcpserver/oauth_test.go internal/mcpserver/server.go
git commit -m "feat(mcpserver): serve discovery and client registration for token:// login

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

### Task 4: The login page

**Files:**
- Create: `internal/mcpserver/oauth_authorize.go`, `internal/mcpserver/oauth_page.html`
- Modify: `internal/mcpserver/oauth.go` (`loginServer` struct, `mount`)
- Test: `internal/mcpserver/oauth_authorize_test.go`

**Interfaces:**
- Consumes (Task 3): `loginServer`, `loginFixture` (`serve`, `register`, `logs`, `now`, `s`), `matchesAny`, constants. (Task 2): `formClaims`, `codeClaims`, `clientClaims`.
- Produces:
  - `loginServer.limiter failureLimiter`; `func (s *loginServer) authorizePage(w, r)`, `func (s *loginServer) authorizeSubmit(w, r)`
  - `func withParams(base string, params url.Values) string`, `func hostOf(raw string) string`
  - test helpers (in `oauth_authorize_test.go`): consts `testVerifier`, `testChallenge`; `func authorizeQuery(clientID, redirect string) url.Values`; `loginFixture` methods `authorize(url.Values)`, `loginForm(url.Values) string`, `submit(request, password string)`, `authCode(clientID, redirect string) string`

- [ ] **Step 1: Write the failing tests**

Create `internal/mcpserver/oauth_authorize_test.go`:

```go
package mcpserver

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

	// resource is optional.
	q := authorizeQuery(f.register(claudeCallback), claudeCallback)
	q.Del("resource")
	if rec := f.authorize(q); rec.Code != http.StatusOK {
		t.Errorf("without resource: %d", rec.Code)
	}
}

func TestAuthorizeUntrustedRedirectShowsErrorPage(t *testing.T) {
	f := newLoginFixture(t, nil)
	claude := f.register(claudeCallback)
	// Same token and password, so the same keys, but a shrunken allowlist.
	shrunk := newLoginFixture(t, func(m *config.MCPConfig) { m.RedirectURIs = []string{loopbackEntry} })

	cases := map[string]struct {
		f        *loginFixture
		client   string
		redirect string
	}{
		"unknown client":             {f, "garbage", claudeCallback},
		"redirect not registered":    {f, claude, "http://localhost:4000/callback"},
		"redirect missing":           {f, claude, ""},
		"redirect left the allowlist": {shrunk, claude, claudeCallback},
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
	if !strings.Contains(shrunk.logs.String(), "redirect rejected") || !strings.Contains(shrunk.logs.String(), claudeCallback) {
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
		m.RedirectURIs = append(m.RedirectURIs, "https://app.example.com/cb?tenant=1")
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/mcpserver/ -run 'TestAuthorize'`
Expected: FAIL — the authorize routes return 405/404 (`login page: 404`).

- [ ] **Step 3: Implement**

In `internal/mcpserver/oauth.go`, add to `loginServer` after `now`:

```go
	limiter   failureLimiter
```

and to `mount`, after the register route:

```go
	mux.HandleFunc("GET /oauth/authorize", s.authorizePage)
	mux.HandleFunc("POST /oauth/authorize", s.authorizeSubmit)
```

Create `internal/mcpserver/oauth_page.html`:

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Connect to twillingate</title>
<style>
body { font-family: system-ui, sans-serif; max-width: 26rem; margin: 4rem auto; padding: 0 1rem; color: #1a1a1a; }
h1 { font-size: 1.25rem; }
.error { color: #b00020; }
label, input, button { display: block; width: 100%; box-sizing: border-box; font: inherit; }
input, button { margin-top: .5rem; padding: .5rem; }
code { word-break: break-all; }
</style>
</head>
<body>
<h1>Connect to twillingate</h1>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
{{if .RejectedRedirect}}<p>Rejected redirect URI: <code>{{.RejectedRedirect}}</code></p>{{end}}
{{if .Request}}
<p><strong>{{or .ClientName "An MCP client"}}</strong> is asking for access and will return you to <strong>{{.RedirectHost}}</strong>.</p>
<form method="post" action="/oauth/authorize">
<input type="hidden" name="request" value="{{.Request}}">
<label for="password">Password</label>
<input id="password" name="password" type="password" autocomplete="current-password" autofocus required>
<button type="submit">Connect</button>
</form>
{{end}}
</body>
</html>
```

Create `internal/mcpserver/oauth_authorize.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/mcpserver/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/oauth_authorize.go internal/mcpserver/oauth_page.html internal/mcpserver/oauth.go internal/mcpserver/oauth_authorize_test.go
git commit -m "feat(mcpserver): add the password page for token:// login

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

### Task 5: The token endpoint

**Files:**
- Create: `internal/mcpserver/oauth_token.go`
- Modify: `internal/mcpserver/oauth.go` (`loginServer` struct, `mount`)
- Test: `internal/mcpserver/oauth_token_test.go`

**Interfaces:**
- Consumes (Tasks 3-4): `loginServer`, `oauthError`, `writeJSON`, `loginFixture` (`register`, `authCode`, `serve`, `s`, `now`), `newLoginFixture`, `testVerifier`, constants. (Task 2): `codeClaims`, `grantClaims`, `clientClaims`.
- Produces:
  - `loginServer.used usedCodes`; `func (s *loginServer) token(w, r)`
  - `type usedCodes struct`; `func (u *usedCodes) claim(jti string, exp, now time.Time) bool`
  - test helpers (in `oauth_token_test.go`): `type issuedTokens struct{AccessToken, TokenType, RefreshToken string; ExpiresIn int64}`; `func codeGrant(client, code, redirect string) url.Values`; `loginFixture` methods `tokenRequest(url.Values)`, `grant(url.Values) issuedTokens`

- [ ] **Step 1: Write the failing tests**

Create `internal/mcpserver/oauth_token_test.go`:

```go
package mcpserver

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

	moved := newLoginFixture(t, func(m *config.MCPConfig) { m.ResourceURL = "https://other.example.com/mcp" })
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/mcpserver/ -run 'TestToken|TestUsedCodes'`
Expected: FAIL — `undefined: usedCodes`.

- [ ] **Step 3: Implement**

In `internal/mcpserver/oauth.go`, add to `loginServer` after `limiter`:

```go
	used      usedCodes
```

and to `mount`, after the authorize routes:

```go
	mux.HandleFunc("POST /oauth/token", s.token)
```

Create `internal/mcpserver/oauth_token.go`:

```go
package mcpserver

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
	if form.Has("resource") && form.Get("resource") != s.resource {
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
func (s *loginServer) issue(w http.ResponseWriter, clientID string) {
	now := s.now()
	grant := func(ttl time.Duration) grantClaims {
		return grantClaims{ClientID: clientID, RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{s.resource},
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/mcpserver/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/oauth_token.go internal/mcpserver/oauth.go internal/mcpserver/oauth_token_test.go
git commit -m "feat(mcpserver): exchange codes and refresh tokens for token:// login

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

### Task 6: Wire the login server into `/mcp`, prove it end to end, document it

**Files:**
- Modify: `internal/mcpserver/server.go` (`NewHandler`, `RegisterOn`, `wrapAuth` `case "token"`)
- Modify: `internal/app/app.go` (the `mcpserver.RegisterOn(...)` call ~line 164)
- Modify: `internal/mcpserver/server_test.go` (`TestRegisterOnWithoutHealthzOmitsRoute`'s `RegisterOn` call; append tests)
- Create: `internal/mcpserver/oauth_e2e_test.go`
- Modify: `docs/deployment.md`, `.env.example`

**Interfaces:**
- Consumes (Tasks 1-5): `config.MCPConfig.RedirectURIs`, `newLoginServer`, `loginServer.mount`, `loginServer.verify`, `accessTokenTTL`, `requestField`, `grantClaims`, `kindAccess`. Existing test helpers: `newHandlerFixture`, `initReq`, `seedDB`, `textOf`.
- Produces: `func RegisterOn(mux *http.ServeMux, protected http.Handler, cfg *config.Config, withHealthz bool, logger *slog.Logger)`

- [ ] **Step 1: Write the failing tests**

Append to `internal/mcpserver/server_test.go`:

```go
const loginDSN = "token://ar_testtoken?password=hunter2" +
	"&redirect=https://claude.ai/api/mcp/auth_callback&resource=https://mcp.example.com/mcp"

func TestTokenLoginRoutesFollowRedirects(t *testing.T) {
	serve := func(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	plain := newHandlerFixture(t, nil)
	if rec := serve(plain, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("plain token://: metadata %d, want 404", rec.Code)
	}
	if rec := serve(plain, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))); rec.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("plain token:// grew a challenge: %q", rec.Header().Get("WWW-Authenticate"))
	}

	login := newHandlerFixture(t, map[string]string{"MCP_AUTH_DSN": loginDSN})
	if rec := serve(login, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)); rec.Code != http.StatusOK {
		t.Errorf("login: metadata %d, want 200", rec.Code)
	}
	rec := serve(login, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}")))
	const want = `resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), want) {
		t.Errorf("login: /mcp %d WWW-Authenticate=%q, want 401 with %s", rec.Code, rec.Header().Get("WWW-Authenticate"), want)
	}
	req := initReq()
	req.Header.Set("Authorization", "Bearer ar_testtoken")
	if rec := serve(login, req); rec.Code != http.StatusOK {
		t.Errorf("login: header token %d, want 200 — it must keep working", rec.Code)
	}
}

func TestIssuedAccessTokenNeedsTheLoginServer(t *testing.T) {
	m := config.MCPConfig{Token: "ar_testtoken", Password: "hunter2",
		RedirectURIs: []string{"https://claude.ai/api/mcp/auth_callback"}, ResourceURL: "https://mcp.example.com/mcp"}
	access := newLoginServer(m, slog.New(slog.DiscardHandler)).keys.sign(kindAccess, grantClaims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://mcp.example.com", Subject: "mcp",
			Audience: jwt.ClaimStrings{m.ResourceURL}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}})

	for name, tc := range map[string]struct {
		h    http.Handler
		want int
	}{
		"login on":                    {newHandlerFixture(t, map[string]string{"MCP_AUTH_DSN": loginDSN}), http.StatusOK},
		"every redirect removed":      {newHandlerFixture(t, nil), http.StatusUnauthorized},
	} {
		req := initReq()
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		tc.h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", name, rec.Code, tc.want)
		}
	}
}
```

Create `internal/mcpserver/oauth_e2e_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/mcpserver/ -run 'TestTokenLogin|TestIssuedAccessToken'`
Expected: FAIL — compile error `too many arguments in call to RegisterOn`.

- [ ] **Step 3: Implement the wiring**

In `internal/mcpserver/server.go`:

1. `NewHandler`'s doc comment: replace `/.well-known/oauth-protected-resource in oauth mode.` with `/.well-known/oauth-protected-resource in oauth mode, and the login server's routes in token mode with redirects configured.`; its call becomes `RegisterOn(mux, protected, cfg, true, logger)`.
2. Replace `RegisterOn`'s signature and add the login-server mount after the `oauth` block:

```go
// RegisterOn mounts the MCP routes on a mux. withHealthz=false when the
// mux is shared with the ingest surface, whose /healthz already exists
// (ServeMux panics on duplicate patterns).
func RegisterOn(mux *http.ServeMux, protected http.Handler, cfg *config.Config, withHealthz bool, logger *slog.Logger) {
```

```go
	if cfg.MCP.AuthMode == "token" && len(cfg.MCP.RedirectURIs) > 0 {
		newLoginServer(cfg.MCP, logger).mount(mux)
	}
```

3. Replace `wrapAuth`'s `case "token":` body:

```go
	case "token":
		opts := &auth.RequireBearerTokenOptions{AllowMissingExpiration: true}
		if len(m.RedirectURIs) == 0 {
			return auth.RequireBearerToken(StaticVerifier(m.Token), opts)(next), nil
		}
		// Keys derive from the config alone, so this verifier accepts what
		// the instance RegisterOn mounts issues; verifying logs nothing.
		opts.ResourceMetadataURL = metadataURLFor(m.ResourceURL)
		verify := newLoginServer(m, slog.New(slog.DiscardHandler)).verify
		return auth.RequireBearerToken(verify, opts)(next), nil
```

In `internal/mcpserver/server_test.go`, `TestRegisterOnWithoutHealthzOmitsRoute`: `RegisterOn(mux, protected, cfg, false, logger)`.

In `internal/app/app.go`: `mcpserver.RegisterOn(mux, protected, cfg, false, logger)`.

Then confirm no other caller: `grep -rn "RegisterOn(" --include=*.go .` lists only these three.

- [ ] **Step 4: Run tests to verify they pass**

Run: `PATH=/usr/local/go/bin:$PATH go test -race ./internal/mcpserver/ ./internal/app/`
Expected: PASS, including `TestTokenLoginEndToEnd/shared=false` and `/shared=true`.

- [ ] **Step 5: Document it in `docs/deployment.md`**

1. Environment table, `MCP_AUTH_DSN` row — replace with:

```markdown
| `MCP_AUTH_DSN` | MCP authentication: `token://<token>` (add `?password=…&redirect=…` for a browser login), `cloudflare://<team>?aud=<tag>` or `oauth://<issuer-host>`. Unset, bare `serve` skips MCP with a warning. |
```

2. In "The MCP endpoint", the DSN block's first line becomes
`MCP_AUTH_DSN=token://<token>[?password=<password>&redirect=<uri>[&redirect=<uri>…][&resource=<url>]]`,
and the mode table's `token://` row becomes:

```markdown
| `token://` | One operator, no identity provider — the simplest thing that is secure | ✅ `--header` or browser login | ✅ with the browser login | ✅ with the browser login |
```

3. Replace everything from the heading `### \`token://\` — a single static token` up to, not including, `### \`cloudflare://\` — Access managed OAuth` with:

````markdown
### `token://` — a static token, with an optional browser login

```bash
twillingate keygen -mcp        # prints: MCP_AUTH_DSN=token://ar_…
```

The token is a true secret — unlike ingest keys it reads every project and
authorizes the management tools. Rotate by minting a new one and restarting.

```bash
claude mcp add --transport http twillingate https://twillingate.example.com/mcp \
  --header "Authorization: Bearer ar_…"
```

The default scope is `local` — this machine, this project only. Add
`-s user` for every project you open, or `-s project` to write a checked-in
`.mcp.json`. **Do not put the token in a `-s project` config**: `.mcp.json`
is committed. Reference an environment variable instead, expanded at connect
time:

```json
{
  "mcpServers": {
    "twillingate": {
      "type": "http",
      "url": "https://twillingate.example.com/mcp",
      "headers": { "Authorization": "Bearer ${TWILLINGATE_MCP_TOKEN}" }
    }
  }
}
```

#### Browser login for Claude Desktop and claude.ai

Neither can attach an `Authorization` header. Add a password and the
redirect URIs you allow, and the binary serves its own OAuth login: the
client registers itself, your browser shows one page asking for the
password, and the client is connected. The token never goes near a
browser, and the header above keeps working.

```bash
MCP_AUTH_DSN=token://ar_…?password=<password>&redirect=https://claude.ai/api/mcp/auth_callback&redirect=http://localhost/callback
```

| Parameter | Meaning |
| --- | --- |
| `redirect` | A redirect URI clients may use; repeat it once per URI. Any `redirect` turns the login on. |
| `password` | What the login page asks for. Required with `redirect`. |
| `resource` | The public URL of `/mcp`. Defaults to `PUBLIC_URL` + `/mcp`; set it when MCP has its own hostname. |

- **Matching.** Scheme, host, path and query must equal an entry exactly.
  For `localhost`, `127.0.0.1` and `[::1]` the port is ignored, because
  Claude Code and Desktop pick a new port for each login; `localhost` and
  `127.0.0.1` still count as different hosts. Any other host needs `https`.
- **Finding a client's callback.** If a login stops at "The redirect URI
  is not in the MCP_AUTH_DSN allowlist", the page and the
  `mcp login: redirect rejected` log line show the URI the client used. Add
  it as another `redirect=` and restart.
- **Encoding.** The parameters are a query string: in the password write
  `&` as `%26`, `#` as `%23`, `%` as `%25`, `;` as `%3B` and `+` as `%2B`.
  An unencoded `+` becomes a space. In a Docker Compose `.env`, also write
  `$` as `$$`.
- **Guessing.** Five wrong passwords in a minute lock the page for everyone
  until the minute ends; connected clients are unaffected. No minimum length
  is enforced, so a short password is only as strong as that rate allows.
- **Hand-picked tokens.** `keygen -mcp` mints `ar_` plus hex. A token you
  choose yourself must not contain `?`, which now starts the parameters.

Claude Desktop and claude.ai: Settings → Connectors → **Add custom
connector**, endpoint `https://twillingate.example.com/mcp`, OAuth client ID
and secret left empty. Claude Code: add the server without a header, run
`/mcp`, choose *Authenticate*.

A client you use stays logged in: access tokens last an hour and refresh
silently, and every refresh extends the login by 30 days. Nothing about
logins is stored, so there is no per-client revocation — change the password
to cut a device off.

| Change | Effect |
| --- | --- |
| New token or new password | Every client logs in again |
| New `resource` URL | Every client logs in again |
| Every `redirect` removed | The login is off and issued tokens stop working |
| Redirect list edited | Connected clients keep working; new logins follow the list |
| A client unused for 30 days | That client logs in again |
| Restart or upgrade | Nothing |

#### Claude Desktop without the browser login

A local stdio-to-HTTP bridge can attach the header instead, with Node
installed. Edit `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows), then quit
Claude Desktop completely — closing the window leaves it in the tray, and
the config is only read at startup:

```json
{
  "mcpServers": {
    "twillingate": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://twillingate.example.com/mcp",
               "--header", "Authorization: Bearer ar_…"]
    }
  }
}
```

The token then sits in plain text, and `mcp-remote` is third-party code in
the path of an admin credential; the browser login avoids both.

````

4. "Verifying any mode": the first curl's comment becomes
`# → HTTP/1.1 401; in oauth mode and in token mode with redirect= the` / `#   WWW-Authenticate header names the discovery document`,
and the second's becomes
`# → oauth mode and token mode with redirect=: JSON naming the issuer;` / `#   cloudflare mode and plain token mode: 404`.

5. "Troubleshooting a connection": add after the "Login loops" entry:

```markdown
**Login page: redirect URI not allowed.** The page shows the URI the client
used; add it to `MCP_AUTH_DSN` as another `redirect=` and restart.

**Login page: password not recognised, though it is right.** A `+`, `&`,
`#`, `%` or `;` in the password must be percent-encoded in the DSN (see the
`token://` section). **Too many attempts** means five wrong passwords this
minute; wait for the next one.
```

- [ ] **Step 6: Update `.env.example`**

Replace the line `#   MCP_AUTH_DSN=token://<token>                  (mint with \`twillingate keygen -mcp\`)` with:

```
#   MCP_AUTH_DSN=token://<token>                  (mint with `twillingate keygen -mcp`)
#   MCP_AUTH_DSN=token://<token>?password=<pw>&redirect=https://claude.ai/api/mcp/auth_callback
#                                                 (browser login for Desktop/claude.ai; see docs/deployment.md)
```

- [ ] **Step 7: Run the full gate**

Run: `PATH=/usr/local/go/bin:$PATH go vet ./... && PATH=/usr/local/go/bin:$PATH ./scripts/coverage.sh`
Expected: vet clean; `total coverage` ≥ 90%; `internal/config` and `internal/mcpserver` lines ≥ 90%; exit 0. If `internal/mcpserver` dips below 90%, run `PATH=/usr/local/go/bin:$PATH go tool cover -func=coverage.out | grep oauth` and add a test for the lowest function rather than lowering the gate.

- [ ] **Step 8: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/server_test.go internal/mcpserver/oauth_e2e_test.go internal/app/app.go docs/deployment.md .env.example
git commit -m "feat(mcpserver): log in to token:// mcp from claude.ai and desktop with a password

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

### Task 7: Fold the planning decisions into the spec

**Files:**
- Modify: `docs/superpowers/specs/2026-09-12-mcp-token-login-design.md`

**Interfaces:** none (prose).

- [ ] **Step 1: Apply the five deviations listed at the top of this plan**

- §5.1, last line: "The same function runs at registration and at `GET /oauth/authorize`. At `POST /oauth/token` the `redirect_uri` must equal the code's byte for byte (RFC 6749 §4.1.3)."
- §8 table: `form` claims gain `client_name`; replace "`jti` is 16 random bytes, base64url." with "`jti` is `crypto/rand.Text()`: 26 base32 characters, 130 bits."
- §4, after the metadata JSON: "Served from a local struct: `oauthex.AuthServerMeta` has no `omitempty` on `jwks_uri` and would emit an empty one."
- §8 "Derivation" paragraph: replace "no new parameters cross the `app` boundary" with "`RegisterOn` takes the logger the login server needs, one new argument at `app`'s shared-mux call".
- §13: replace "`internal/app` is unchanged." with "`internal/app` passes `logger` to `RegisterOn`." and add rows for `oauth_authorize.go` (login page, limiter) and `oauth_token.go` (token endpoint, used codes), narrowing `oauth.go` to metadata, registration, matching and the verifier.

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-09-12-mcp-token-login-design.md
git commit -m "docs(mcpserver): record implementation decisions in the token:// login spec

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```
