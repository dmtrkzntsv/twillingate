# Deployment

The operator's runbook: getting twillingate onto a host, keeping it backed
up, getting it back after the host is gone. Using it is
[twillingate.md](twillingate.md); both are served over MCP, this one as
`docs://deployment`. One process serves ingestion, the API and the
dashboards at `/app/`, and its whole state is one SQLite file.

- [Install](#install)
- [Configure the collector](#configure-the-collector)
- [The API endpoint](#the-api-endpoint)
- [Dashboards at /app/](#dashboards-at-app)
- [Operate and recover](#operate-and-recover) — including backups

## Install

### docker compose

Ingestion, the SDK and — once `API_AUTH_DSN` is set — the API and the
[dashboards at /app/](#dashboards-at-app).

```bash
mkdir twillingate && cd twillingate
curl -fsSLO https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/compose/docker-compose.yml
docker compose up -d
docker compose exec twillingate twillingate project create -name myapp
docker compose exec twillingate twillingate key issue -project-id 1 -label web
```

### systemd on a VPS

```bash
# From a published release (no checkout needed):
curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/systemd/install.sh | sudo bash
# Or from a checkout (Go and Node 22 on the host): git clone, make build, then
sudo ./deploy/systemd/install.sh          # --user NAME to skip the prompt, --yes for defaults
```

The curl form takes the tarball for this architecture from the latest
release (`--version v0.9.2` to pin) and verifies its SHA256; `latest` is the
newest release, not the newest commit. The installer creates a system
account, `/usr/local/bin/twillingate`, `/var/lib/twillingate` (0750, owned
by it), an example `twillingate.env` loaded via `EnvironmentFile=`, the
units, and the `twillingate@.service` template for a [split
install](#hostnames-and-processes). Re-running it upgrades: env file and
account kept, running units restarted, non-zero exit if one stays down.
Re-running the installer upgrades and restarts the units that were running;
a stopped service stays stopped. Then edit the file it flagged and create
your first project:

```bash
sudo vi /etc/twillingate/twillingate.env
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project create -name myapp'
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate key issue -project-id 1 -label web'
sudo systemctl start twillingate
curl -s localhost:8080/healthz            # → {"status":"ok"}
# Ingest check: 202 and {"accepted":1,...}; an Origin outside allowed_origins gets 403.
curl -i -X POST http://localhost:8080/ingest/events \
  -H 'Content-Type: application/json' -H 'Origin: https://myapp.com' \
  -d '{"key":"ak_…","events":[{"name":"$page_view","attributes":{"$host":"myapp.com","$path":"/"}}]}'
```

### One collector, several hostnames

The service binds to loopback by default, so put TLS in front of
`127.0.0.1:8080` — Caddy, nginx, or a Cloudflare tunnel. Any number of
hostnames can proxy to it: `/js/twillingate.js` is rendered per request with
the origin it was asked for baked in — the `Host` header plus the scheme the
proxy forwards.

```
t.example.com, t.example.org {
    reverse_proxy 127.0.0.1:8080
}
```

The proxy must pass `Host` through and set `X-Forwarded-Proto` (Caddy and
cloudflared do; nginx needs `proxy_set_header Host $host;` and
`proxy_set_header X-Forwarded-Proto $scheme;`). Without a forwarded scheme
the collector falls back to the scheme of `PUBLIC_URL`, so a single-hostname
install with `PUBLIC_URL=https://…` works unconfigured. A `Host` that is not
a plain hostname (optional port aside) leaves the file originless and the
SDK dormant with a console warning. Snippets use `PUBLIC_URL`; change the
`src` per hostname, and keep the API on one — OAuth and `cloudflare://` bind
to it.

## Configure the collector

| Variable | Meaning |
| --- | --- |
| `INGEST_ADDR` | Address to bind. Default `127.0.0.1:8080` (the docker image sets `0.0.0.0:8080`). |
| `PUBLIC_URL` | The collector's public base URL (`https://twillingate.example.com`). Embed snippets and MCP integration guidance are built from it; unset, they carry a placeholder. Also the default for `API_URL`. With [several hostnames](#one-collector-several-hostnames), the default one. |
| `DATABASE_DSN` | Store DSN. Only `sqlite://<path>` today. Required. |
| `GEO_DSN` | Country lookup: `cloudflare://` (header), `maxmind://<license-key>`, or `none://`. |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error`. Default `info`. |
| `LOG_FORMAT` | `json` or `text`. Default `json`. |
| `LOG_FILE` | Log to this path instead of stdout. |
| `BUFFER_FLUSH_MAX_EVENTS` | Flush once this many events are buffered. Default 1000. |
| `BUFFER_FLUSH_INTERVAL` | Flush at least this often. Default `5s`. |
| `BUFFER_CAPACITY` | Bounded queue size; excess is dropped rather than growing memory. Default 10000. |
| `RETENTION_EVENTS_RAW_DAYS` | Days raw events of every family are kept before rollup. Also the oldest client timestamp accepted: older events are clamped to this edge. Default 30. |
| `RETENTION_EVENTS_AGGREGATE_DAYS` | Days aggregates of every family (and actors, cohorts, identities) are kept. Default 365. |
| `RETENTION_ARCHIVED_DAYS` | Days after archiving that a project (with all its data), a dashboard or a widget is deleted by the daily pass. 0 keeps archived items forever. Default 30. |
| `PRODUCT_ATTRIBUTES_TOP_N` | Distinct attribute values kept per (project, day, event, key) before the rest collapse into `(other)`. Default 50. |
| `API_AUTH_DSN` | Authentication for the API endpoint (MCP and REST): `token://<token>?password=…` for the built-in browser login (see [The API endpoint](#the-api-endpoint)), or `oauth://<issuer-host>` for your own identity provider. Unset, bare `serve` skips the API with a warning. |
| `API_ADDR` | Give the API (MCP and REST) its own listener. Defaults to `INGEST_ADDR` (shared). |
| `API_URL` | The API's public origin when it has a hostname of its own (`https://api.example.com`), no path. The login's resource, issuer and the dashboards' callback follow it, so that host needs no `redirect=` entry. Defaults to `PUBLIC_URL`. |
| `API_DB_PATH` | Database the API reads for queries. Defaults to the `DATABASE_DSN` path. |
| `API_QUERY_TIMEOUT` | Per-query guard on reads and the `query` operation; also bounds a reporting widget's sql. Default `10s`. |
| `API_QUERY_MAX_ROWS` | Row cap on the `query` operation; also bounds a reporting widget's sql. Default 1000. |
| `REPORTING_CACHE_SECONDS` | How long an ordinary widget data request reuses a sql widget's loaded value before loading again. 0 turns the cache off: every request loads again. Default 900. |
| `REPORTING_REFRESH_SECONDS` | A `fresh=true` request reuses a result younger than this instead of `REPORTING_CACHE_SECONDS`; must not exceed it when that is non-zero. Default 60. A dashboard with auto-refresh on reloads every max(`REPORTING_CACHE_SECONDS`, `REPORTING_REFRESH_SECONDS`) seconds while its window has focus; both 0 removes the option. |

The old name `LISTEN_ADDR` refuses the boot, naming its replacement:
`INGEST_ADDR`. The `MCP_*` names renamed to `API_*` are no longer checked; a
leftover one is ignored, so rename any still in `twillingate.env`.

### Raspberry Pi and low-resource hosts

- Raise `BUFFER_FLUSH_INTERVAL` (say `30s`): fewer, larger writes.
- Set `GOMEMLIMIT` (unit and compose files ship `128MiB`) and keep `GEO_DSN` off `maxmind://`, which holds a database in memory.
- Lower `RETENTION_EVENTS_RAW_DAYS` (say `7`): raw events are the largest table in the file, and the live halves of the `v_*` views scan them on every query.

## The API endpoint

`serve -api` answers both transports on one listener: streamable HTTP MCP at
`https://twillingate.example.com/mcp`, REST under
`https://twillingate.example.com/api/`. There is no stdio server — every
client talks to the running collector over the network — and one
`API_AUTH_DSN` protects both.

> **A connected session reads every non-archived project — including
> personal data on projects whose clients send ids — and can use the
> management tools.** There is no per-project scoping. The token and the
> password are the whole of the access control. Treat both as admin
> credentials.

### Set it up

The binary runs its own login — no identity provider to run — and every
client connects through a browser page that asks for a password.

1. Mint the token — `keygen -api` mints it as `ar_` plus hex. It prints
   `API_AUTH_DSN=token://ar_…`:
   ```bash
   sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate keygen -api'
   ```
2. Set it in `/etc/twillingate/twillingate.env` (compose: `.env`), in single
   quotes — these commands load the file with `sh`, where an unquoted `&`
   cuts the value short:
   ```
   API_AUTH_DSN='token://ar_…?password=<password>'
   ```
3. Restart, then check that `/mcp` asks for a login:
   ```bash
   sudo systemctl restart twillingate        # compose: docker compose up -d
   curl -si -X POST https://twillingate.example.com/mcp | grep -i www-authenticate
   # → WWW-Authenticate: Bearer resource_metadata="https://twillingate.example.com/.well-known/oauth-protected-resource/mcp"
   ```

| Parameter | Meaning |
| --- | --- |
| `password` | What the login page asks for. Setting it turns the login on. Five wrong ones in a minute lock the page for everyone until the minute ends; connected clients are unaffected. No minimum length is enforced. In the DSN, percent-encode `&` as `%26`, `#` as `%23`, `%` as `%25`, `;` as `%3B` and `+` as `%2B` — an unencoded `+` becomes a space. |
| `resource` | The API origin, with no path (`resource=https://api.example.com`); one login covers `/mcp` and `/api/`. Defaults to `API_URL`, which defaults to `PUBLIC_URL`: set `API_URL` when the API has its own hostname, and keep `resource=` for the rare case where the two must differ. MCP clients read `/.well-known/oauth-protected-resource/mcp`, which names `<origin>/mcp`; `/api/` clients read `/.well-known/oauth-protected-resource`, which names the origin. A value carrying a path refuses the boot, in both `token://` and `oauth://` mode. |
| `redirect` | An extra host clients may return to, such as `redirect=app.example.com`; repeat once per host. |

Accepted without `redirect=`, at any port and path: `localhost`,
`127.0.0.1` and `[::1]` over `http` or `https`, plus `claude.ai` and
`chatgpt.com` over `https`. Any other host is refused until added as
`redirect=<host>`, then works over `https`, at any port and path, matched
exactly (`claude.ai` does not admit `foo.claude.ai`) — once the list is
edited, new logins follow it. A self-chosen token must not contain `?`.

### Connect a client

- **claude.ai, Claude Desktop:** Settings → Connectors → **Add custom
  connector**, URL `https://twillingate.example.com/mcp`, client id and
  secret empty. **ChatGPT:** same URL, OAuth authentication.
- **Claude Code:** `claude mcp add --transport http twillingate https://twillingate.example.com/mcp`,
  then `/mcp` → *Authenticate*; **Codex:** add the URL as an MCP server and
  authenticate. **Any other MCP client** uses the same URL: if its login
  stops at "The redirect URI's host is not allowed", the page and the
  `mcp login: redirect rejected` log line name the URI to add as
  `redirect=<host>`.

Access tokens last an hour and refresh silently, each refresh extending the
login by 30 days. Logins are not stored, so there is no per-client
revocation:

| Change | Effect |
| --- | --- |
| new token or password | every client logs in again |
| new `resource` | every client logs in again |
| `password` removed | issued tokens are voided |
| `redirect` edited | new logins follow the new list; connected clients are unaffected |
| client unused 30 days | it logs in again |
| restart or upgrade | connected clients are unaffected |

Anything that is not an MCP client hits the same operations as REST routes
with the same token, all of them documented in
[twillingate.md#http-api](twillingate.md#http-api):
```bash
curl -H "Authorization: Bearer $TOKEN" \
  https://twillingate.example.com/api/projects/1/views/overview?from=…&to=…
```
A client that can send headers can present the token itself instead of
logging in; a bare `API_AUTH_DSN='token://ar_…'` turns the login off and
leaves only this.

```bash
claude mcp add --transport http twillingate https://twillingate.example.com/mcp \
  --header "Authorization: Bearer ar_…"
```

The default scope is `local` — this machine, this project; `-s user` covers
every project you open, `-s project` writes a checked-in `.mcp.json`, where
the token belongs as `${TWILLINGATE_MCP_TOKEN}` in the `Authorization`
header rather than pasted in. Rotating means a new token and a restart, then
removing and re-adding any client holding the old header.

### Hostnames and processes

`API_ADDR` gives the API its own listener; unset, it shares the ingestion
one. Put it on its own hostname when you can, with `resource=` set to that
origin: only `/ingest/`, `/js/` and `/healthz` have to be public. For
separate processes, use the `twillingate@.service` template rendered beside
the main unit: its instances run `serve -ingest` and `serve -api`, and
naming a flag makes that surface's misconfiguration a hard error rather than
the warn-and-skip of a bare `serve`.

```bash
sudo systemctl disable --now twillingate
sudo systemctl enable --now twillingate@ingest twillingate@api
```

Disable the bare unit first: enabling both `twillingate.service` and an
instance binds the same listeners at the next boot. An upgrade restarts
running instances and leaves the bare unit disabled; reverse to go back.

> **An API-only process still runs the daily aggregation pass against
> `DATABASE_DSN`** — `-api` only makes the HTTP listener conditional, not
> the background jobs. Set `API_DB_PATH` (what the API reads) and
> `DATABASE_DSN` (what the pass writes) deliberately: aimed at a copy of the
> database, an API-only unit writes to that copy on every pass — or
> accept that a two-process topology runs the idempotent daily aggregation
> twice.

### Other auth modes

```bash
API_AUTH_DSN='oauth://auth.example.com[?resource=<origin>][&audience=<aud>]'
```

For an IdP you already run or rent (Keycloak, Auth0, Authentik, …): the
server validates the JWTs it issues and serves no login page. `resource`
defaults to `API_URL` (then `PUBLIC_URL`) and must be an origin with no path;
`oauth+insecure://` allows a plain-http IdP in development. It must provide:

1. **RFC 8414 metadata** at `<issuer>/.well-known/oauth-authorization-server`
   with a `jwks_uri` — many IdPs publish only OIDC discovery, so check with
   `curl -s https://auth.example.com/.well-known/oauth-authorization-server | jq .jwks_uri`.
   The server fetches it at startup, refuses to boot without it, and
   refetches on an unknown `kid` (throttled to a minute).
2. **Asymmetrically signed JWT access tokens** (RS/ES/PS); HMAC, `alg=none`
   and opaque tokens are rejected.
3. **An `aud` claim containing the resource origin or `<origin>/mcp`** —
   either passes unless `audience=` names one. Without a match, logins loop.
4. **For claude.ai:** Dynamic Client Registration (RFC 7591), or a hand-made
   client whose id is entered in the connector.

### Troubleshooting a connection

| Symptom | Cause and fix |
| --- | --- |
| `404` on `/mcp` or `/api/` | The API is off — usually a DSN that does not parse. `journalctl -u twillingate \| grep 'API disabled'` names the reason. |
| `401` on connect | Run the curl from [Set it up](#set-it-up). A `401` carrying the `resource_metadata` challenge means the server is fine: the client or the password is the problem. |
| Connects but no tools | Wrong path: the endpoint is `/mcp`, not the bare hostname. |
| Login page: redirect URI's host not allowed | The client returns to a host that is not built in. The page shows the URI; add its host as `redirect=<host>` and restart. |
| Login page: password not recognised, though it is right | A `+`, `&`, `#`, `%` or `;` in the password must be percent-encoded in the DSN. "Too many attempts" instead means five wrong passwords this minute; wait for the next one. |
| Login loops in `oauth://` mode | The IdP issues tokens without the expected `aud`: the origin, `<origin>/mcp`, or the `audience=` value. |
| `/app/` login: redirect URI host not allowed | The page is open on a host other than `API_URL`'s. Set `API_URL` to it, or add it as `redirect=<host>`, and restart. |

## Dashboards at /app/

Wherever the API is served, `/app/` serves the dashboards beside it:
`https://twillingate.example.com/app/`, on the API's own listener with
`API_ADDR`, else on the shared one. The root of that listener redirects to
`/app/`; an ingest-only listener answers 404 there. The page is read-only; agents build the
dashboards over MCP ([reporting.md](reporting.md)). It loads without a login
and reads everything through `/api/` with the same login as any other
client:

- **`token://` with a password:** the page shows the login page on its own
  host and returns to `/app/callback` there. On the API's host, `API_URL`
  (default `PUBLIC_URL`), that needs no entry: the login accepts exactly
  `<API_URL>/app/callback`, whatever `Host` a proxy passes on. So when the
  API has its own hostname, set `API_URL=https://api.example.com` and open
  the dashboards there. Any further name needs `redirect=<host>` in
  `API_AUTH_DSN`. The login trusts only configured hosts, never a request
  header. The access token stays in the
  tab and the refresh token in the browser, so a login lasts 30 days from
  last use, as for other clients.
- **A bare `token://`:** the page asks for the token.
- **`oauth://`:** the page reads the provider's RFC 8414 metadata at the
  issuer root, `<issuer>/.well-known/oauth-authorization-server`, registers
  itself there by dynamic client registration (RFC 7591) and logs in,
  returning to `/app/callback` on the host it is opened on, which the
  provider must allow. It
  calls the provider from the browser, so the provider's metadata,
  registration and token endpoints must allow cross-origin requests. A
  provider without dynamic registration, with an issuer that has a path, or
  publishing only `/.well-known/openid-configuration` gets the token prompt
  instead.

**Install it as an app:** in Chrome or Edge, *Install* in the address bar;
in Safari, *File → Add to Dock*. It opens in its own window. Its service
worker keeps the page itself, never data, so offline it opens and says so.
Files under `/app/assets/` are cached for a year (their names change with
their content); the page, `sw.js` and `manifest.webmanifest` are revalidated
on every load, so a proxy or CDN in front needs no rules of its own.

Widget queries run under `API_QUERY_TIMEOUT` and `API_QUERY_MAX_ROWS`, and
their results are cached for `REPORTING_CACHE_SECONDS`
([Configure the collector](#configure-the-collector)).

### Previewing dashboard files

`twillingate reporting dev` previews dashboard files against a database on
a loopback address, without a login; its usage is in
[reporting.md](reporting.md#local-development).

## Operate and recover

### Routine operations

| Task | Command |
| --- | --- |
| Logs | `journalctl -u twillingate -f` |
| Restart | `systemctl restart twillingate` |
| Upgrade (systemd) | `curl -fsSL …/install.sh \| sudo bash` — restarts the running service and reports the old and new version |
| Upgrade (compose) | `docker compose pull && docker compose up -d`. Never `down -v`: the database lives in the named volume. Pin with `TWILLINGATE_VERSION=v0.9.2` in `.env`. |
| Apply migrations only | `twillingate migrate` |
| Upgrade across a schema change | [Back up](#back-up-and-restore) first: migrations 012, 014, 015 and 016 are irreversible. Pre-checks and what changes on the day: <https://github.com/dmtrkzntsv/twillingate/blob/main/deploy/UPGRADES.md> (not served over MCP; open it in the repository) |
| Database size | `du -h /var/lib/twillingate/twillingate.db` |
| Recent config changes | `sqlite3 …/twillingate.db "SELECT * FROM audit_log ORDER BY ts DESC LIMIT 20"` |
| Preview dashboard files against real data | `twillingate reporting dev <dir>... [-db <path>] [-addr 127.0.0.1:3100]` — a local, no-login server over the built UI; `-db` defaults to `DATABASE_DSN`'s path, `-addr` is refused unless it is loopback |

Every CLI command on a systemd host needs the unit's environment:
`sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project list'`.
Aggregation, pruning and incremental vacuum run daily at 03:00 UTC and the
visitor salt rotates at 00:00 UTC; a catch-up pass at startup means downtime
across those times skips no day. With `LOG_FILE` set, install
`deploy/logrotate/twillingate` into `/etc/logrotate.d/`.

### Back up and restore

Everything twillingate keeps is in one SQLite file,
`/var/lib/twillingate/twillingate.db` (in compose, `twillingate.db` in the
`data` volume). SQLite's online backup copies it safely while the service
runs; a plain `cp` of a live database can catch a half-written page and
misses whatever is still in the `-wal` file beside it. Ship the copy off the
host however you ship files, and check it now and then. For a continuous
off-host copy instead, seconds behind, run litestream beside the collector:
<https://github.com/dmtrkzntsv/twillingate/blob/main/docs/litestream.md>
sets it up for systemd and docker compose, with the restore drill and
disaster recovery (not served over MCP; open it in the repository).

```bash
sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db \
  ".backup '/var/lib/twillingate/backup.db'"
sqlite3 /var/lib/twillingate/backup.db 'PRAGMA quick_check;'         # expect: ok
sqlite3 /var/lib/twillingate/backup.db "SELECT MAX(day) FROM v_views_daily;"
```

The image carries no `sqlite3`: under compose, `docker compose stop
twillingate`, copy `twillingate.db` out of the volume, and start it again.

To restore, on the old host or a fresh one after [installing](#install):
stop the service, put the copy at `/var/lib/twillingate/twillingate.db`
owned by the service user, delete any `twillingate.db-wal` and
`twillingate.db-shm` beside it, and start the service. A copy from an older
release migrates on start.

Two things do not survive: the visitor salt (rotated daily anyway, so at
most a day of continuity) and whatever arrived after the copy was taken,
including events buffered in memory, bounded by `BUFFER_FLUSH_INTERVAL`.
