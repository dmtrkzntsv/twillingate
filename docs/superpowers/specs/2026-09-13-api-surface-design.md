# One private API surface: MCP and REST together — design

Date: 2026-09-13
Status: implemented

## 1. Purpose

Expose Twillingate's read and management operations over plain HTTP, next
to the MCP endpoint, behind the same authentication. Today the binary has
two surfaces named confusingly: `serve -api` is *ingestion* (public,
ingest-key authenticated, `POST /api/events`) and `serve -mcp` is the
private, bearer-authenticated MCP endpoint. After this change:

- **ingest** is the public surface: events in, SDK scripts out.
- **api** is the private surface: `/mcp` and `/api/*`, one auth system,
  one set of operations behind both.

This is a cut-off release. Nothing old keeps working: env vars, flags, the
ingest path and already-issued login tokens all change (§9).

## 2. Non-goals

- **API versioning.** Routes live under `/api/`, not `/api/v1/`. The next
  breaking change is another cut-off like this one.
- **REST-only operations.** REST mirrors the MCP tools; nothing is added
  that MCP lacks (no `GET /api/projects/{alias}`, no delete).
- **Agent guidance over REST.** `integration_guide` and the `docs://`
  resources stay MCP-only: they are reading material for a model.
- **CORS on the API.** Bearer-token callers are servers, scripts and
  notebooks, not browsers on other origins.
- **Scopes or a read-only token tier.** One token reads and manages, as
  MCP does today.
- **OpenAPI document.** The route table in `docs/twillingate.md` is the
  contract.

## 3. Surfaces, flags and configuration

`serve` runs both surfaces; `serve -ingest` runs ingestion only; `serve
-api` runs the API only. Bare `serve` stays lenient: without
`API_AUTH_DSN` it skips the API with a warning. Naming `-api` makes
missing or broken auth a hard error, as `-mcp` does today.

| Old | New | Notes |
|---|---|---|
| `LISTEN_ADDR` | `INGEST_ADDR` | default `127.0.0.1:8080` |
| `MCP_ADDR` | `API_ADDR` | defaults to `INGEST_ADDR` (shared listener, warned) |
| `MCP_AUTH_DSN` | `API_AUTH_DSN` | same `token://` / `oauth://` grammar |
| `MCP_DB_PATH` | `API_DB_PATH` | read-only pool for every read operation |
| `MCP_QUERY_TIMEOUT` | `API_QUERY_TIMEOUT` | default `10s` |
| `MCP_QUERY_MAX_ROWS` | `API_QUERY_MAX_ROWS` | default 1000 |
| `serve -api` | `serve -ingest` | |
| `serve -mcp` | `serve -api` | |
| `keygen -mcp` | `keygen -api` | prints `API_AUTH_DSN=token://…`; `MintMCPToken` → `MintAPIToken` |

`config.MCPConfig` becomes `config.APIConfig` (`cfg.API`), `Config.Listen`
becomes `Config.IngestAddr`, `ValidateMCP` becomes `ValidateAPI`.

**Renamed-variable guard.** If any of `LISTEN_ADDR`, `MCP_ADDR`,
`MCP_AUTH_DSN`, `MCP_DB_PATH`, `MCP_QUERY_TIMEOUT` or `MCP_QUERY_MAX_ROWS`
is set, config loading fails with `config: MCP_AUTH_DSN was renamed to
API_AUTH_DSN` (one message per variable, first found). An in-place upgrade
must not silently turn the API off under lenient bare `serve`.

## 4. URLs

| Surface | Route | Auth |
|---|---|---|
| ingest | `POST /ingest/events`, `OPTIONS /ingest/events` | ingest key, CORS per project |
| ingest | `GET /js/twillingate.js`, `GET /js/plausible-shim.js` | none |
| both | `GET /healthz` | none |
| api | `/mcp` | bearer |
| api | `/api/…` (§5) | bearer |
| api | `GET /.well-known/oauth-protected-resource` | none |
| api | `GET /.well-known/oauth-authorization-server`, `/oauth/*` | none; `token://` with `password=` only |

`/api/events` is gone. The SDK (`sdk/src/twillingate.ts`), the Plausible
shim and its README, and the native-app wire docs move to
`/ingest/events`. A reverse proxy can split the surfaces by prefix: public
is `/ingest/`, `/js/`; everything else is private.

When both surfaces share a listener, one mux carries both route sets; the
ingest handler no longer mounts at `/` as a catch-all but registers its
own patterns, so an unknown path is a plain 404 from the shared mux.

## 5. REST routes

Every route calls the same operation as the MCP tool beside it.

| MCP | Method and path | Input |
|---|---|---|
| `list_projects` | `GET /api/projects` | — |
| `create_project` | `POST /api/projects` | JSON body: `alias`, `name`, `identity`, `allowed_origins`, `attributes`, `skip_key` |
| `update_project` | `PATCH /api/projects/{alias}` | JSON body, merge semantics as the tool |
| `archive_project` | `POST /api/projects/{alias}/archive` | — |
| `restore_project` | `POST /api/projects/{alias}/restore` | — |
| `list_ingest_keys` | `GET /api/keys` | query: `project` |
| `issue_ingest_key` | `POST /api/projects/{project}/keys` | JSON body: `label` |
| `disable_ingest_key` | `POST /api/projects/{project}/keys/{label}/disable` | — |
| `enable_ingest_key` | `POST /api/projects/{project}/keys/{label}/enable` | — |
| `web_overview` | `GET /api/projects/{project}/web/overview` | query: `from`, `to` |
| `web_breakdown` | `GET /api/projects/{project}/web/breakdown` | query: `from`, `to`, `dimension`, `limit` |
| `app_overview` | `GET /api/projects/{project}/app/overview` | query: `from`, `to` |
| `app_breakdown` | `GET /api/projects/{project}/app/breakdown` | query: `from`, `to`, `dimension`, `limit` |
| `product_events` | `GET /api/projects/{project}/product/events` | query: `from`, `to`, `event` |
| `product_attributes` | `GET /api/projects/{project}/product/attributes` | query: `from`, `to`, `event` |
| `retention` | `GET /api/projects/{project}/retention` | query: `from`, `to`, `surface` |
| `identities` | `GET /api/projects/{project}/identities` | query: `from`, `to`, `kind`, `limit` |
| `query` | `POST /api/query` | JSON body: `sql` |
| resource `schema://views` | `GET /api/schema/views` | — (`text/plain`) |
| `integration_guide` | *MCP only* | |
| resources `docs://twillingate`, `docs://deployment`, `schema://projects` | *MCP only* | |

Path wildcard names equal the input struct's JSON field names (`alias` or
`project`), so decoding is mechanical.

**Responses.** 200 (201 for `POST /api/projects` and issuing a key) with
the tool's structured output as JSON, byte-for-byte the shape MCP returns
in `structuredContent`.

**Errors.** `{"error": {"code": "…", "message": "…"}}`, `Content-Type:
application/json`:

| Condition | Status | code |
|---|---|---|
| `errors.Is(err, manage.ErrInvalid)` | 400 | `invalid` |
| `errors.Is(err, manage.ErrNotFound)` | 404 | `not_found` |
| `errors.Is(err, manage.ErrConflict)` | 409 | `conflict` |
| missing or bad bearer token | 401 | from the SDK's bearer middleware, with `WWW-Authenticate` |
| undecodable body, unknown query parameter, unknown body field | 400 | `invalid` |
| anything else | 500 | `internal`; message is generic, the error is logged |

The messages written for a model to recover from ("unknown project "x";
valid aliases: …", "unknown dimension …; valid: …", "query exceeded 10s;
narrow the date range") are kept verbatim and returned as `message` on
both transports. To make them typed, the read-side validation errors wrap
the manage sentinels: range/dimension/surface/kind/SQL errors and query
timeouts wrap `ErrInvalid`; unknown project wraps `ErrNotFound`. The text
still says nothing a client should match on.

## 6. Package layout

`internal/mcpserver` is renamed `internal/apiserver`; it stays the single
rank-2 surface (archtest rank table and CLAUDE.md layout updated; commit
scope `apiserver` replaces `mcpserver`).

```
internal/apiserver/
  server.go      Build / Mount: read DB, host, MCP server, REST mux, auth wrap, login routes
  expose.go      spec type, expose[In, Out], MCP adapter, REST adapter, error mapping
  ops_read.go    list_projects, web_*, app_*           (was tools_read.go)
  ops_product.go product_*, retention, identities       (was tools_product.go)
  ops_query.go   query                                  (was query.go)
  ops_manage.go  project and key operations             (was tools_manage.go)
  guide.go       integration_guide (MCP only)
  resources.go   MCP resources; schemaViews also served at GET /api/schema/views
  auth.go, oauth*.go, readdb.go   unchanged apart from config and resource (§7)
```

**Operations are transport-neutral.** Each is
`func (h *host) webOverview(ctx context.Context, in rangeIn) (tableOut, error)`
— no `*mcp.CallToolRequest`, no `*mcp.CallToolResult`.

**One registration call per operation.**

```go
type spec struct {
	Name        string              // MCP tool name
	Description string
	Annotations *mcp.ToolAnnotations
	Method      string              // "" = MCP only
	Path        string
}

func expose[In, Out any](r *registrar, s spec, fn func(context.Context, In) (Out, error))
```

`expose` adds the MCP tool (`mcp.AddTool` with an adapter that returns
`nil, out, err`) and, when `Method` is set, the REST route. A tool cannot
be added to one transport by accident and forgotten on the other; the MCP-
only exceptions are visible as `Method: ""` in one file. The registrar
records every spec so tests and `docs_sync_test` can enumerate them.

**REST adapter.** For `GET`, `In` is filled from path wildcards and query
parameters matched to JSON field names (strings and ints; embedded structs
such as `rangeIn` are promoted as `encoding/json` does); a query parameter
that names no field is a 400. For `POST`/`PATCH`, the body is decoded with
`DisallowUnknownFields` (an empty body is allowed) and path wildcards are
then written over their fields, so the path is authoritative. Bodies are
capped at 1 MiB.

**Audit actor.** Management operations take the actor from the context:
the MCP adapter sets `mcp`, the REST adapter `api`. `audit_log` tells the
two apart.

## 7. Authentication

`wrapAuth` builds one verifier per mode and returns middleware that `Build`
applies to `/mcp` and `/api/` separately, each instance carrying that
prefix's own metadata URL. Every mode — the static token, login-issued
tokens, `oauth://` JWTs — is accepted on both prefixes.

**The resource identifier becomes the API origin.** The default
`resource` is `PUBLIC_URL` (was `PUBLIC_URL + "/mcp"`); `resource=` in
`API_AUTH_DSN` overrides it and must be an origin with no path (config
rejects a path).

**Each prefix has its own protected-resource metadata.** In oauth mode and
in token-login mode (password set) two RFC 9728 documents are served, with
the same `authorization_servers`:

- `/.well-known/oauth-protected-resource` names `resource` = the origin,
  for `/api/`.
- `/.well-known/oauth-protected-resource/mcp` names `resource` =
  origin + `/mcp`: the RFC 9728 §3.1 suffix form for the URL MCP clients
  connect to, so the §3.3 check (the document's `resource` is the
  identifier the well-known suffix was inserted into) holds for both.

Plain `token://` without a password serves neither. `Build` auth-wraps the
MCP handler and the REST mux separately, with one verifier per mode, so
each prefix's 401 names its own document: `/mcp` →
`resource_metadata="<origin>/.well-known/oauth-protected-resource/mcp"`,
`/api/…` → `resource_metadata="<origin>/.well-known/oauth-protected-resource"`.

- `oauth://`: without `audience=`, a JWT's `aud` must contain the origin
  or origin/mcp, since an IdP mints `aud` from the resource the client
  asked for. An explicit `audience=` is the only accepted value.
- `token://` login server: a client's `resource` parameter (authorize and
  token requests) is accepted when it equals the origin or is a URL under
  it (`https://host/mcp`, `https://host/api`). Access and refresh tokens are
  issued with `aud` = [origin, origin/mcp], and verification requires the
  origin. One token works on both prefixes.

When the API runs on its own hostname (`API_ADDR` plus a second DNS name),
`resource=https://api.example.com` names it, as today.

**Why two documents (go-sdk evidence).** go-sdk v1.7.0
(`auth/authorization_code.go`, `getProtectedResourceMetadata`) fetches the
challenge's `resource_metadata` URL first and requires the document's
`resource` to equal the URL it connected to (`…/mcp`). With a single
origin document, that check failed. The SDK then tried
`/.well-known/oauth-protected-resource/mcp` (404), and only succeeded on
its last fallback, the root document checked against the origin. The
end-to-end test recorded the fetches as `[root, /mcp, root]`. A client as
strict as the first step, and claude.ai may be one, would not have
connected. With per-prefix documents the SDK succeeds on its first fetch
and sends `resource=<origin>/mcp`. `oauth_e2e_test.go` asserts that the
`/mcp` document is the only one fetched. Confirming claude.ai against a
deployed instance remains a pre-merge check.

## 8. Wiring (`internal/app`, `cmd/twillingate`)

`app.Serve(ctx, cfg, logger, ingest, api bool)`. Shared listener: one mux,
`server.Mount(mux)` registers the ingest routes and `/healthz`,
`apiserver.Mount(mux, withHealthz=false)` the API routes. Separate
listeners: each surface mounts on its own mux with its own `/healthz`.
Shutdown order is unchanged. `cmd/twillingate/serve.go` parses `-ingest`
and `-api`, keeps the lenient bare-serve rule, and warns when `API_ADDR`
is unset.

## 9. Upgrade consequences (release notes)

One `feat!:` PR. The operator of an existing install must:

1. Rename the env vars in `/etc/twillingate/twillingate.env` (§3); the
   collector refuses to start until they do.
2. Replace `serve -api` / `serve -mcp` in any split units with `-ingest` /
   `-api`.
3. Point native apps and any hand-written ingest calls at
   `/ingest/events`. Sites using the served SDK or Plausible shim pick it
   up when the script is re-fetched; a cached old script posts to
   `/api/events` and gets 404 (401 where the API shares the listener)
   until then.
4. Reconnect MCP clients that logged in with the password: tokens carrying
   `aud=…/mcp` no longer verify. The connector URL `/mcp` is unchanged.
   Static-token clients are unaffected.
5. Edit `API_AUTH_DSN` if it sets `resource=…/mcp`, or set `resource=` if
   `PUBLIC_URL` has a path: `resource` must be an origin, and config
   refuses to start otherwise.

## 10. Documentation

- `docs/twillingate.md`: ingest path; a new "HTTP API" section with the §5
  route table (status codes, error shape, curl example); MCP tools table
  unchanged apart from naming the API.
- `docs/deployment.md`: env var table, `-ingest`/`-api`, `keygen -api`,
  "The MCP endpoint" becomes "The API endpoint" covering both transports,
  hostname/process split, `resource=` as an origin, the upgrade steps of §9.
- `docs/plausible/README.md`: new ingest path.
- `CLAUDE.md`: layout (`apiserver`), commit scopes, the doc-trigger list
  (`internal/apiserver/ops_*.go`, `expose.go`).
- `deploy/`: compose comments, `install.sh` comment about
  `twillingate-mcp.service`.

## 11. Testing

- **Parity:** every registered spec with a `Method` has a distinct
  method+path; every MCP tool is either routed or in the MCP-only set
  (`integration_guide`), so a new tool must choose.
- **REST adapter:** path + query decoding into embedded structs, int
  parsing error, unknown query parameter → 400, unknown body field → 400,
  path overrides body, body cap.
- **Error mapping:** each sentinel → status/code; unknown project keeps the
  valid-aliases message; untyped error → 500 with generic message.
- **Per route:** a table test calling each REST route against the seeded
  fixture DB and comparing its JSON to the MCP tool's `structuredContent`
  for the same input.
- **Auth:** unauthenticated `/api/…` → 401 with `WWW-Authenticate`; static
  token works on both; the login end-to-end test obtains a token with
  `resource=https://host/mcp` and uses it on `/mcp` and `/api/projects`;
  a `resource` outside the origin is `invalid_target`; `oauth://` audience
  default.
- **Config:** each renamed variable trips the guard; `resource=` with a
  path is rejected; defaults.
- **Audit:** a REST write records actor `api`.
- **docs_sync_test:** the "HTTP API" table in `docs/twillingate.md`
  matches the registered routes in both directions (method, path, tool);
  env var check picks up the `API_*`/`INGEST_ADDR` names; ingest path.
- **Ingest:** existing tests move to `/ingest/events`; `/api/events`
  returns 404; SDK tests updated.
- `make check` green (archtest with `internal/apiserver` in the rank table).
