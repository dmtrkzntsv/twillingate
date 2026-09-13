# Browser login for `token://` MCP auth — design

Date: 2026-09-12
Status: proposed

## 1. Purpose

Let claude.ai, Claude Desktop and Claude Code connect to the MCP endpoint
through a normal OAuth login, with no identity provider and no third-party
edge in the path. Today `token://` only works for clients that can send an
`Authorization` header (Claude Code); Desktop needs the `mcp-remote` bridge
and claude.ai cannot connect at all. `cloudflare://` covered those clients
but is blocked by an outstanding bug.

The binary gains a minimal OAuth 2.1 **authorization server** inside
`token://` mode. The operator sets a password and a redirect allowlist in
`MCP_AUTH_DSN`; a client registers itself, the browser shows one page asking
for the password, and the client receives tokens. Nothing is stored: every
value the server hands out is signed with keys derived from the configured
secrets.

This supersedes the "no built-in authorization server" non-goal of
`2026-08-24-mcp-endpoint-design.md` §2 for `token://` mode only. `oauth://`
and `cloudflare://` are unchanged.

## 2. Non-goals

- **Per-client revocation.** Revocation is changing the token or the
  password, which logs out every client (§9). No grant table, no CLI or MCP
  tool to list logins.
- **Client ID Metadata Documents.** Clients fall back to Dynamic Client
  Registration when the server does not advertise CIMD.
- **Refresh-token reuse detection.** It requires storage (§12).
- **Confidential clients.** Every client is public (`token_endpoint_auth_method: none`), protected by PKCE.
- **CORS.** Browser-hosted MCP clients (e.g. MCP Inspector in a tab) cannot
  run discovery against this server. Claude clients do OAuth natively or
  server-side.
- **More than one user.** There is one password and one identity (`mcp`),
  exactly as there is one token today.
- **A CLI to mint a `client_id`** for clients without registration support.
  Add it when such a client appears.

## 3. Configuration

```bash
MCP_AUTH_DSN=token://<token>?password=<password>&redirect=<uri>[&redirect=<uri>…][&resource=<url>]
```

No new environment variable. `parseMCPAuthDSN`'s `token` case cuts the DSN
at the first `?`: the part before is the token, verbatim as today; the part
after is parsed with `url.ParseQuery`.

| Parameter | Meaning |
| --- | --- |
| `redirect` | Repeatable. One allowed redirect URI per occurrence. Any `redirect` enables the login server. |
| `password` | The secret typed on the login page. Required when any `redirect` is set. |
| `resource` | Public URL of `/mcp`. Defaults to `PUBLIC_URL` + `/mcp`. |

`MCPConfig` gains `RedirectURIs []string` and `Password string`;
`ResourceURL` is reused. With no query string, `token://` behaves exactly as
before: header token only, no new routes.

**Startup rejects** (returned through `authErr`, surfaced by `ValidateMCP`):

- a query that `url.ParseQuery` rejects, or an unknown parameter (catches
  typos such as `redirects=`);
- `password` or `resource` without any `redirect`;
- `redirect` without `password` (an empty `password=` counts as missing);
- a redirect URI that does not parse, is not absolute, carries a fragment,
  or uses `http` on a host other than `localhost`, `127.0.0.1` or `[::1]`;
- no `resource` and no `PUBLIC_URL` to derive it from;
- a resource URL that is not absolute, or is `http` on a non-loopback host.

The **issuer** is the resource URL's scheme and host (`https://mcp.example.com`).
It comes from configuration, never from the request's `Host` header.

Query values are percent-decoded, so a password containing `&`, `#`, `%`,
`+` or `;` must be percent-encoded; `+` decodes to a space. The token is
minted as `ar_` plus hex and cannot contain `?`. A hand-set token that does
would now be cut; `docs/deployment.md` says so.

## 4. Routes

Mounted by `RegisterOn` when `len(cfg.MCP.RedirectURIs) > 0`, on both the
standalone mux (`NewHandler`) and the shared ingest mux (`app`'s
`MCP_ADDR == LISTEN` path). All are unauthenticated.

| Route | Handler |
| --- | --- |
| `GET /.well-known/oauth-protected-resource` | RFC 9728 via `auth.ProtectedResourceMetadataHandler`: `resource`, `authorization_servers: [issuer]` |
| `GET /.well-known/oauth-authorization-server` | RFC 8414 document (below) |
| `POST /oauth/register` | §5 |
| `GET /oauth/authorize` | §6 |
| `POST /oauth/authorize` | §6 |
| `POST /oauth/token` | §7 |

Authorization server metadata:

```json
{
  "issuer": "https://mcp.example.com",
  "authorization_endpoint": "https://mcp.example.com/oauth/authorize",
  "token_endpoint": "https://mcp.example.com/oauth/token",
  "registration_endpoint": "https://mcp.example.com/oauth/register",
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code", "refresh_token"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"],
  "scopes_supported": ["offline_access"],
  "authorization_response_iss_parameter_supported": true
}
```

Served from a local struct: `oauthex.AuthServerMeta` has no `omitempty` on
`jwks_uri` and would emit an empty one.

None of these paths collide with the ingest routes (`/`, `/api/events`,
`/js/*`, `/healthz`); `ServeMux` prefers the more specific patterns.

On `/mcp`, the `token` case of `wrapAuth` uses the verifier of §8 and, when
the login server is on, passes `ResourceMetadataURL` so a `401` carries
`WWW-Authenticate: Bearer resource_metadata="<origin>/.well-known/oauth-protected-resource"`.
`AllowMissingExpiration` stays `true` for the env token.

## 5. Registration — `POST /oauth/register`

RFC 7591. JSON body, at most 16 KB.

- `redirect_uris` is required and non-empty; **every** entry must match the
  allowlist (§5.1). Otherwise `400 {"error":"invalid_redirect_uri"}`.
- `grant_types`, if present, must be a subset of `authorization_code` and
  `refresh_token`; default `["authorization_code"]`.
- `response_types`, if present, must be a subset of `code`.
- `token_endpoint_auth_method` is ignored and answered as `none` (RFC 7591
  §3.2.1 lets the server substitute): no secret is ever issued, so a client
  that asked for one proceeds as a public client.
- `client_name` is optional, truncated to 64 runes.
- Any other validation failure: `400 {"error":"invalid_client_metadata"}`.

Success is `201` with `client_id`, `client_id_issued_at`, `redirect_uris`,
`grant_types`, `response_types`, `token_endpoint_auth_method: "none"` and
`client_name` if given. `client_id` is a signed value (§8); nothing is
stored.

### 5.1 Redirect matching

A candidate URI matches an allowlist entry when, after parsing both:

- scheme, lowercase hostname, path and raw query are equal byte for byte
  (no normalisation: `""` and `/` differ);
- for `localhost`, `127.0.0.1` and `[::1]` the port is ignored on both sides
  (RFC 8252 §7.3); for other hosts the port must be equal;
- `localhost`, `127.0.0.1` and `[::1]` are distinct hosts;
- the candidate carries no fragment.

The same function runs at registration and at `GET /oauth/authorize`. At
`POST /oauth/token` the `redirect_uri` must equal the code's byte for byte
(RFC 6749 §4.1.3).

## 6. The login page — `/oauth/authorize`

### 6.1 `GET`

Parameters: `response_type`, `client_id`, `redirect_uri`, `code_challenge`,
`code_challenge_method`, `state`, optional `resource`, optional `scope`
(ignored).

1. **Untrusted redirect** — `client_id` missing or badly signed,
   `redirect_uri` missing, not among the client's registered URIs, or no
   longer matching the current allowlist: respond `400` with the error page,
   **never redirect** (RFC 6749 §4.1.2.1). The page and a `warn` log show the
   rejected `redirect_uri` and the reason, so an operator can copy the real
   callback into the DSN.
2. **Trusted redirect, bad request** — redirect to `redirect_uri` with
   `error`, `state` and `iss`:
   - `response_type` ≠ `code` → `unsupported_response_type`;
   - `code_challenge` missing, or `code_challenge_method` ≠ `S256` →
     `invalid_request`;
   - `resource` present and ≠ the resource URL → `invalid_target` (RFC 8707).
3. **Otherwise** render the login page (`html/template`, embedded
   `oauth_page.html`): "Connect **‹client_name›**, which redirects to
   **‹redirect host›**", a password field and a submit button. The redirect
   host is shown because it is trusted (allowlisted); `client_name` is not.
   A hidden `request` field carries a signed form value (§8) holding the
   validated `client_id`, `redirect_uri`, `code_challenge` and `state`,
   expiring in 10 minutes.

Headers on every page from this route: `Cache-Control: no-store`,
`X-Frame-Options: DENY`,
`Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'`,
`Referrer-Policy: no-referrer`.

### 6.2 `POST`

Form fields `request` and `password`; anything else is ignored.

1. If the failure limiter (§10) is exhausted: `429` with the page and a
   "too many attempts, wait a minute" message. The password is **not**
   checked, or the limiter would not limit.
2. `request` badly signed or expired: `400` error page, "this login request
   expired, start again from your client".
3. Password compared with `crypto/subtle.ConstantTimeCompare`. On mismatch:
   record a failure, `warn` log with `r.RemoteAddr`, re-render the page
   (same `request` value) with "Password not recognised", status `401`.
4. On match: issue an authorization code (§8) and `302` to
   `redirect_uri?code=…&state=…&iss=<issuer>` (RFC 9207), appending to any
   query the registered URI already carries. `info` log with `client_name`
   and redirect host.

The env token is never accepted on this page.

## 7. Token endpoint — `POST /oauth/token`

`application/x-www-form-urlencoded`. Responses are JSON with
`Cache-Control: no-store`; errors follow RFC 6749 §5.2 with status `400`.

`client_id` is required on both grants (public clients, RFC 6749 §3.2.1)
and must carry a valid signature, else `invalid_client`.

### 7.1 `grant_type=authorization_code`

Required: `code`, `redirect_uri`, `client_id`, `code_verifier`. Optional:
`resource` (must equal the resource URL, else `invalid_target`).

- `code` must be validly signed and unexpired, and its `jti` absent from the
  used-code set; otherwise `invalid_grant`. The `jti` is added **before**
  the remaining checks, so a code is burnt by its first presentation.
- `client_id` and `redirect_uri` must equal the values in the code:
  `invalid_grant`.
- `code_verifier` must be 43–128 characters from the RFC 7636 alphabet, and
  `BASE64URL(SHA256(code_verifier))` must equal the code's `code_challenge`
  under constant-time comparison: `invalid_grant`.

### 7.2 `grant_type=refresh_token`

Required: `refresh_token`, `client_id`. `scope` is ignored.

- `refresh_token` validly signed, unexpired, and its `aud` equal to the
  resource URL, else `invalid_grant`.
- `client_id` equals the token's, else `invalid_grant`.
- The client's registered `grant_types` are not consulted: every client
  that completed a login may refresh.

### 7.3 Success

Both grants return a fresh pair:

```json
{"access_token": "…", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "…"}
```

Refresh tokens are always issued, whether or not `offline_access` was
requested. Any other `grant_type` → `unsupported_grant_type`.

## 8. Keys and signed values

**Derivation.** At construction, five 32-byte keys come from HKDF-SHA256
(`crypto/hkdf`, standard library since Go 1.24):

```
secret = u32be(len(token)) ‖ token ‖ u32be(len(password)) ‖ password
key    = hkdf.Key(sha256.New, secret, nil, "twillingate mcp <kind>", 32)
kind  ∈ {client, form, code, access, refresh}
```

Length prefixes keep distinct `(token, password)` pairs from producing the
same bytes. The keys depend on nothing but the configuration, so restarts,
redeploys and a second process computing them independently all agree.
`wrapAuth` and `RegisterOn` each derive them from `cfg`. `RegisterOn` takes
the logger the login server needs, one new argument at `app`'s shared-mux
call.

**Format.** Compact JWS via `golang-jwt`, parsed with
`jwt.WithValidMethods([]string{"HS256"})`; `none` and every other algorithm
are rejected. One key per kind, so a value of one kind never verifies as
another even when the claims would fit. HMAC is correct here because the
server verifies its own values; this path is separate from `verifyJWT`,
whose asymmetric-only allowlist stays as it is.

| Kind | Claims | Lifetime |
| --- | --- | --- |
| `client` | `redirect_uris`, `client_name`, `iat`, `jti` | none |
| `form` | `client_id`, `client_name`, `redirect_uri`, `code_challenge`, `state`, `exp` | 10 min |
| `code` | `client_id`, `redirect_uri`, `code_challenge`, `jti`, `exp` | 60 s, single use |
| `access` | `iss`, `aud` (resource URL), `sub: "mcp"`, `client_id`, `iat`, `exp`, `jti` | 1 h |
| `refresh` | `aud` (resource URL), `client_id`, `iat`, `exp`, `jti` | 30 days |

`jti` is `crypto/rand.Text()`: 26 base32 characters, 130 bits. On a
`client_id` it keeps two identical registrations in the same second from
yielding the same identifier. `client_name` rides in the form value so the
page can be shown again after a wrong password. No clock leeway: one
server, one clock.

**Verifying `/mcp`.** The `token` verifier:

1. constant-time compares the bearer value with the env token; on match,
   `TokenInfo{UserID: "mcp"}` with no expiration (unchanged behaviour);
2. otherwise, **only when the login server is on**, parses it as an `access`
   value, requiring `iss` = issuer, `aud` = resource URL and a future `exp`;
   on success, `TokenInfo{UserID: "mcp", Expiration: exp}`;
3. otherwise `auth.ErrInvalidToken`, which the SDK middleware turns into a
   `401` with the challenge of §4.

Both paths share `UserID: "mcp"`, so the SDK's session binding treats a
session started with either credential as the same user.

## 9. Lifetimes and revocation

A client in regular use refreshes hourly and is never asked for the password
again; each refresh extends its refresh token by 30 days.

| Event | Effect on issued tokens |
| --- | --- |
| Token or password changed | All invalid immediately (new keys) |
| `resource` URL changed | All invalid (`aud` on access and refresh tokens) |
| Every `redirect` removed | All invalid (login server and `/oauth/token` gone; step 2 of §8 off) |
| Redirect list edited | Existing tokens and refreshes keep working; registration and login follow the new list |
| Refresh token unused for 30 days | That client logs in again |
| Restart or redeploy | Nothing; a login in progress at that moment fails once and is retried |

## 10. In-memory state

Per process, lost on restart, never shared:

- **Used codes** — `map[jti]expiry` behind a mutex; expired entries are
  pruned on each insert. Bounded by roughly one minute of logins.
- **Failure limiter** — a mutex-guarded count for the current fixed
  one-minute window, global rather than per IP (per-IP limits are bypassed
  by rotating addresses). Five failures exhaust the window. Successful logins
  neither consume nor reset it.

A code is always redeemed by the process that serves the MCP hostname, so a
two-process `-api`/`-mcp` topology needs no sharing. Several replicas behind
one hostname are not a supported topology.

## 11. Logging

Never logged: the password, the token, codes, access or refresh tokens, the
`request` form value. Logged:

- `warn` — rejected redirect URI with reason; wrong password with
  `r.RemoteAddr` (behind a proxy this is the proxy; `X-Forwarded-For` is not
  trusted); limiter exhausted.
- `info` — successful login with `client_name` and redirect host.

## 12. Security considerations

| Threat | Control |
| --- | --- |
| Rogue client registered with an attacker's redirect, operator phished into logging in | Redirect allowlist at registration, login and token exchange; the page shows the trusted redirect host |
| Authorization code intercepted | PKCE S256 only, 60 s lifetime, single use, bound to `client_id` and `redirect_uri` |
| Open redirect via `/oauth/authorize` | Never redirects to a URI that fails §5.1 |
| Forged consent POST | Requires the password; the signed `request` value prevents parameter tampering between page and submit |
| Clickjacking | `X-Frame-Options: DENY`, `frame-ancestors 'none'` |
| Password guessing | Five failures per minute globally (~7,200/day). Length is the operator's choice: no minimum is enforced, and a short password is only as strong as that rate allows |
| Host header injection into discovery documents | Issuer and endpoints come from configuration |
| One signed value substituted for another | Separate HKDF key per kind; HS256 only |
| Leaked access token | Useful for at most one hour |
| Env token exposure in a browser | The page accepts only the password |

Accepted residual risks:

- **A leaked refresh token** stays usable indefinitely by refreshing; only
  changing the token or the password stops it. Detecting reuse needs
  storage (§2).
- **Login lockout.** Anyone can exhaust the limiter and block the operator
  from logging in for a minute at a time. Connected clients are unaffected.
- **Plaintext password in the env file**, beside the admin token that
  already lives there. Hashing would protect nothing the token does not
  already expose, and `$` in hashes breaks Compose `.env` interpolation.

## 13. Code layout

No new package, dependency or environment variable; `internal/app` passes
`logger` to `RegisterOn`.

| File | Change |
| --- | --- |
| `internal/config/config.go` | §3 parsing and validation; `MCPConfig.RedirectURIs`, `MCPConfig.Password`; doc comment and error strings list the new DSN form |
| `internal/mcpserver/oauth_keys.go` | HKDF derivation; sign and verify per kind |
| `internal/mcpserver/oauth.go` | Metadata, registration, redirect matching, the `/mcp` verifier |
| `internal/mcpserver/oauth_authorize.go` | Login page GET and POST; failure limiter |
| `internal/mcpserver/oauth_token.go` | Token endpoint; used-code set; PKCE check |
| `internal/mcpserver/oauth_page.html` | Login and error page, embedded |
| `internal/mcpserver/server.go` | `RegisterOn` mounts §4 routes; `wrapAuth`'s `token` case uses §8 |

## 14. Testing

**Config** — table over the DSN: plain token unchanged; cut at `?`; repeated
`redirect`; each rejection in §3; `resource` default from `PUBLIC_URL`.

**Unit (`internal/mcpserver`)**

- Redirect matching (§5.1): loopback port ignored, non-loopback port
  enforced, path and query exact, `localhost` ≠ `127.0.0.1`, fragment
  rejected.
- Keys: identical across two derivations; different when either the token
  or the password changes.
- Each kind: rejected under another kind's key, with `alg: none`, with
  `HS512`, when expired.
- Registration: every rule in §5, including `refresh_token` accepted in
  `grant_types` and `client_secret_basic` answered as `none`.
- Authorize GET: error page (no `Location`) for each untrusted-redirect case;
  redirect-with-error for each trusted-redirect case; security headers.
- Authorize POST: wrong password → `401`; sixth failure in a window → `429`
  even with the right password; expired `request`; success `Location`
  carries `code`, `state`, `iss`.
- Token: PKCE mismatch, reused code, `redirect_uri` mismatch, `client_id`
  mismatch, expired code, `resource` mismatch, unknown grant; refresh
  success, expired refresh, refresh token presented as an access token,
  access token presented as a refresh token, refresh after `resource`
  change.
- `/mcp` verifier: env token; valid access token; expired access token;
  access token after token change, after password change, after `resource`
  change; access token rejected when no `redirect` is configured.

**End-to-end** — `NewHandler` on `httptest.Server`, connected with the SDK's
`auth.AuthorizationCodeHandler` using `DynamicClientRegistrationConfig`
(grant types `authorization_code`, `refresh_token`) and `RequestRefreshToken`.
Its `AuthorizationCodeFetcher` acts as the browser: GET the authorization
URL, read the hidden `request` field, POST the password with redirects
disabled, and return `code`, `state` and `iss` from `Location`. An MCP
client over that handler calls `list_projects`. Then advance past access
expiry (injected clock) and call again to exercise refresh. A second run
mounts through `RegisterOn` on a shared mux with a stub ingest handler at
`/`.

Coverage stays within `make check`'s threshold.

## 15. Documentation

Same commit as the code:

- `docs/deployment.md`
  - mode table: `token://` row ✅ for Claude Code, Claude Desktop and
    claude.ai when `redirect` and `password` are set;
  - `token://` section: login setup, the three DSN parameters, redirect
    matching rules, percent-encoding (`+` becomes a space), `$$` in Compose
    `.env`, finding callback URIs from the rejection log, the hand-set-token
    `?` caveat, lifetimes and what logs clients out;
  - the `mcp-remote` block shrinks to a note for installs without the login
    page;
  - `MCP_AUTH_DSN` row of the environment table;
  - verification: `/.well-known/oauth-authorization-server` returns `200` in
    this mode;
  - troubleshooting: rejected redirect URI, `429`, password with `+`.
- `docs/twillingate.md` defers authentication to `docs://deployment` and is
  unchanged.

## 16. To confirm during implementation

Recorded in the docs once observed against real clients, not assumed:

- claude.ai's callback URI (believed `https://claude.ai/api/mcp/auth_callback`);
- Claude Code's loopback callback path;
- the `resource` value each client sends, and that it equals `PUBLIC_URL` +
  `/mcp` exactly;
- that claude.ai and Claude Desktop refresh on a `401` rather than prompting
  for login.
