# Deployment

The operator's runbook: getting twillingate onto a host, keeping it backed
up, and getting it back after the host is gone.

Everything about *using* it — setting up a project, instrumenting a site,
the wire format, answering questions from the data — lives in
[twillingate.md](twillingate.md). The two are separate because an operator
standing up a VPS and an agent asking for last week's numbers want
different things.

Both are served over MCP, this one as `docs://deployment`, so an agent
helping with an install reads the same bytes you do.

Two topologies. **One server** puts ingestion and dashboards on the same
machine — simplest, and enough for a Pi at home behind a tunnel. **Two
servers** runs ingestion on a public VPS and dashboards at home off a
restored replica, so the dashboard machine never needs to be reachable from
the internet.

Replication is not part of the application: `serve` writes a SQLite file and
`dashboards` reads one. The two-server topology needs something to move that
file, and litestream is the supported answer — see [Replication with
litestream](#replication-with-litestream). The one-server topology needs
none of it.

- [Install](#install)
- [Configure the collector](#configure-the-collector)
- [Reporting with Evidence](#reporting-with-evidence)
- [The API endpoint](#the-api-endpoint)
- [Operate and recover](#operate-and-recover) — including litestream for the two-server topology

---

## Install

### docker compose

Tracking: ingestion, the SDK, and — once `API_AUTH_DSN` is set — the API
(MCP and REST). The dashboards are a second compose file, added under
[Reporting with Evidence](#reporting-with-evidence):

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

# Or from a checkout:
git clone <repo> && cd twillingate
make build
sudo ./deploy/systemd/install.sh          # --user NAME to skip the prompt, --yes for defaults
```

The curl form detects the architecture, downloads the matching tarball from
the latest GitHub release (`--version v0.9.2` to pin) and verifies its SHA256
before installing. Releases are cut by hand, so `latest` is the newest
released version and not the newest commit on `main`.

The installer creates a system account, installs the binary to
`/usr/local/bin/twillingate`, creates `/var/lib/twillingate` (0750, owned by
the service account), installs an example `twillingate.env` loaded by both
units via `EnvironmentFile=`, renders the systemd units with the chosen user
(plus the `twillingate@.service` template for a [split
install](#hostnames-and-processes)), and enables them. Re-running the same command upgrades: it replaces the
binary and units, keeps `twillingate.env` and the service account, restarts
every `twillingate` unit that was running, and exits non-zero if one does not
come back up. A stopped service stays stopped.

Then edit the file it flagged and create your first project — projects live
in the database, not in a shipped file:

```bash
sudo vi /etc/twillingate/twillingate.env
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project create -name myapp'
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate key issue -project-id 1 -label web'
sudo systemctl start twillingate
curl -s localhost:8080/healthz            # → {"status":"ok"}
```

Put TLS in front of `127.0.0.1:8080` — Caddy, nginx, or a Cloudflare tunnel.
The service binds to loopback by default, deliberately.

### One collector, several hostnames

The collector ignores `Host`, so any number of hostnames can proxy to it
with no server config; the SDK posts to whatever origin it was loaded from.

```
t.example.com, t.example.org {
    reverse_proxy 127.0.0.1:8080
}
```

Snippets use `PUBLIC_URL`; change the `src` for sites on another hostname.
Keep the API on one hostname — OAuth and `cloudflare://` are bound to it.

### Verifying ingestion

```bash
# Expect 202 and {"accepted":1,...}
curl -i -X POST http://localhost:8080/ingest/events \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://myapp.com' \
  -d '{"key":"ak_…","events":[{"name":"$page_view",
       "attributes":{"$host":"myapp.com","$path":"/"}}]}'

# Expect 403 — the origin is not in allowed_origins
curl -i -X POST http://localhost:8080/ingest/events \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://not-allowed.com' \
  -d '{"key":"ak_…","events":[{"name":"$page_view",
       "attributes":{"$host":"myapp.com","$path":"/"}}]}'
```

## Configure the collector

| Variable | Meaning |
| --- | --- |
| `INGEST_ADDR` | Address to bind. Default `127.0.0.1:8080` (the docker image sets `0.0.0.0:8080`). |
| `PUBLIC_URL` | The collector's public base URL (`https://twillingate.example.com`). Embed snippets, MCP integration guidance and the default API resource (its origin) are built from it; unset, they carry a placeholder. With [several hostnames](#one-collector-several-hostnames), the default one. |
| `DATABASE_DSN` | Store DSN. Only `sqlite://<path>` today. Required. |
| `GEO_DSN` | Country lookup: `cloudflare://` (header), `maxmind://<license-key>`, or `none://`. |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error`. Default `info`. |
| `LOG_FORMAT` | `json` or `text`. Default `json`. |
| `LOG_FILE` | Log to this path instead of stdout. |
| `BUFFER_FLUSH_MAX_EVENTS` | Flush once this many events are buffered. Default 1000. |
| `BUFFER_FLUSH_INTERVAL` | Flush at least this often. Default `5s`. |
| `BUFFER_CAPACITY` | Bounded queue size; excess is dropped rather than growing memory. Default 10000. |
| `RETENTION_VIEWS_RAW_DAYS` | Days raw page and screen views are kept before rollup. Also the oldest client timestamp accepted: older events are clamped to this edge. Default 30. |
| `RETENTION_VIEWS_AGGREGATE_DAYS` | Days view aggregates (and actors, cohorts, identities) are kept. Default 365. |
| `RETENTION_PRODUCT_RAW_DAYS` | Days raw product events are kept before rollup. Default 30. |
| `RETENTION_PRODUCT_AGGREGATE_DAYS` | Days product aggregates are kept. Default 365. |
| `PRODUCT_ATTRIBUTES_TOP_N` | Distinct attribute values kept per (project, day, event, key) before the rest collapse into `(other)`. Default 50. |
| `DASHBOARDS_DB_PATH` | Database `dashboards` renders. Defaults to the `DATABASE_DSN` path. |
| `DASHBOARDS_ADDR` | Address the dashboards bind. Default `0.0.0.0:3000`. |
| `DASHBOARDS_INTERVAL` | Minimum spacing between Evidence rebuilds. Default `15m`. |
| `DASHBOARDS_PROJECT_DIR` | Evidence project in the image. Default `/opt/evidence`. |
| `DASHBOARDS_WORK_DIR` | Where the database snapshot is written. Default `/var/lib/dashboards`. |
| `API_AUTH_DSN` | Authentication for the API endpoint (MCP and REST): `token://<token>?password=…` for the built-in browser login (see [The API endpoint](#the-api-endpoint)), or `oauth://<issuer-host>` for your own identity provider. Unset, bare `serve` skips the API with a warning. |
| `API_ADDR` | Give the API (MCP and REST) its own listener. Defaults to `INGEST_ADDR` (shared). |
| `API_DB_PATH` | Database the API reads for queries. Defaults to the `DATABASE_DSN` path. |
| `API_QUERY_TIMEOUT` | Per-query guard on reads and the `query` operation. Default `10s`. |
| `API_QUERY_MAX_ROWS` | Row cap on the `query` operation. Default 1000. |

Litestream credentials (`LITESTREAM_ACCESS_KEY_ID`,
`LITESTREAM_SECRET_ACCESS_KEY`, `R2_BUCKET`, `R2_ENDPOINT`) live in the same
`twillingate.env`, so secrets never sit in a JSON file. Nothing in the
collector reads them.

`RETENTION_WEB_*` and `RETENTION_APP_*` were replaced by `RETENTION_VIEWS_*`
when web and app analytics merged into one family; a set old name refuses
the boot with the replacement named.

### Raspberry Pi and low-resource hosts

- Raise `BUFFER_FLUSH_INTERVAL` (for example `30s`) to trade latency for
  fewer, larger writes.
- Raise litestream's `sync-interval`.
- Set `GOMEMLIMIT` (the systemd unit and compose files ship `128MiB`).
- Keep `GEO_DSN` on `cloudflare://` or `none://`; the MaxMind provider
  downloads and holds a database in memory.
- Lower `RETENTION_VIEWS_RAW_DAYS` (for example `7`): raw view rows are
  the largest table, and the window only buys late-arrival tolerance for
  offline clients — events older than it are clamped, not lost. The live
  halves of the `v_views_*` views scan the whole raw window on every
  query, so a shorter window also makes breakdowns and the dashboard
  faster.

Maintenance is bounded on purpose: aggregation and pruning run once a day at
03:00 UTC, and free pages are reclaimed with incremental vacuum rather than a
full `VACUUM`, which would rewrite the whole file.

---

## Reporting with Evidence

`twillingate dashboards` renders an Evidence site from the database. In the
compose setup it is `docker-compose.evidence.yml`, serving port 3000.

It rebuilds within a minute of the database changing — it compares size and
modification time and does not need to be told. `DASHBOARDS_INTERVAL`
(default `15m`) sets the minimum spacing between rebuilds, and each rebuild
snapshots the database into `DASHBOARDS_WORK_DIR`, so the host needs room
for one more copy.

A rebuild that cannot read its data does not publish. A database that is not
a twillingate one — a replica the restore never filled, a volume left behind
by another stack — stops the build at `not a twillingate database`, and a
source Evidence cannot read stops it at `could not read a source`. Either
way the previous site keeps serving and no `dashboards: rebuilt` line
appears, because a build that quietly renders the last pass's data is
indistinguishable from a current one. Both point at the database rather than
the dashboards: check `docker compose logs restore`, and that `SOURCE_DB`
matches the `path:` in `litestream.yml` exactly.

### One server

Add the second file next to the tracking `docker-compose.yml` and let
`COMPOSE_FILE` join them into one project sharing one database:

```bash
curl -fsSLO https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/compose/docker-compose.evidence.yml
echo COMPOSE_FILE=docker-compose.yml:docker-compose.evidence.yml >> .env
docker compose up -d
```

`COMPOSE_FILE` is what lets every later `docker compose` command see both
files; without it, pass `-f` twice each time. Port 3000 answers `503` until
Evidence finishes its first build — roughly a minute.

### Two servers

Ingestion on a public VPS, dashboards at home off a restored replica, so the
dashboard machine never needs to be reachable from the internet. The reader
restores the database on cron and renders it:

```bash
curl -fsSLO https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/compose/docker-compose.evidence.yml
docker compose -f docker-compose.evidence.yml up -d
```

Set `DASHBOARDS_DB_PATH=/data/replica.db` in `.env`: the file defaults to
`/data/twillingate.db`, which is the shared-volume case, not this one. The
compose file also carries a commented `restore` service — use it or host
cron, never both.

A failed or corrupt restore is not fatal: `restore.sh` verifies into a
temporary file and only then renames, so the previous replica keeps serving.
An *empty* restore is checked for by name, because litestream does not treat
it as a failure: a `path:` that does not match the writer's, or a bucket
written by a different litestream major/minor, downloads a valid database
with nothing in it and exits 0. `restore.sh` counts the applied migrations
before it renames, so that lands as `restored file is not a twillingate
database` with the previous replica untouched, rather than as dashboards
quietly rebuilt from an empty database.

## The API endpoint

`serve -api` exposes one listener path that answers both transports:
streamable HTTP MCP at `https://twillingate.example.com/mcp` and REST under
`https://twillingate.example.com/api/`. There is no stdio server to
install: every client talks to the running collector over the network. One
`API_AUTH_DSN` protects both — set it up once here and it covers whichever
transport a client uses.

> **A connected session reads every non-archived project — including
> personal data on `identified` projects — and can use the management
> tools.** There is no per-project scoping. The token and the password are
> the whole of the access control. Treat both as admin credentials.

### Set it up

The binary runs its own login: give it a token and a password, and
claude.ai, ChatGPT, Claude Desktop, Claude Code and Codex connect through a
browser page that asks for the password. There is no identity provider to
run.

1. Mint the token:

   ```bash
   sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate keygen -api'
   # prints: API_AUTH_DSN=token://ar_…
   ```

2. Set it in `/etc/twillingate/twillingate.env` (compose: `.env`) with a
   password, **in single quotes**:

   ```bash
   API_AUTH_DSN='token://ar_…?password=<password>'
   ```

   The quotes matter: the CLI commands in this runbook load the file with
   `sh`, where an unquoted `&` cuts the value short.

3. Restart and check that `/mcp` asks for a login:

   ```bash
   sudo systemctl restart twillingate        # compose: docker compose up -d
   curl -si -X POST https://twillingate.example.com/mcp | grep -i www-authenticate
   # → WWW-Authenticate: Bearer resource_metadata="https://twillingate.example.com/.well-known/oauth-protected-resource/mcp"
   ```

   A `404` means the API is off: `journalctl -u twillingate | grep 'API disabled'`
   gives the reason, usually a DSN that does not parse.

| Parameter | Meaning |
| --- | --- |
| `password` | What the login page asks for. Setting it turns the login on. |
| `resource` | The API origin, with no path (`resource=https://api.example.com`); one login covers `/mcp` and `/api/`. Defaults to `PUBLIC_URL`; set it when the API has its own hostname. MCP clients read `/.well-known/oauth-protected-resource/mcp`, which names `<origin>/mcp`; `/api/` clients read `/.well-known/oauth-protected-resource`, which names the origin. |
| `redirect` | An extra host clients may return to, such as `redirect=app.example.com`; repeat it once per host. Only needed for clients not covered below. |

- **Hosts accepted without `redirect=`.** `localhost`, `127.0.0.1` and
  `[::1]` over `http` or `https`, because a code sent there only reaches the
  machine the browser runs on — that covers Claude Code, Claude Desktop and
  Codex. And `claude.ai` and `chatgpt.com` over `https`. Any port and path.
- **Any other host is refused** until it is added as `redirect=<host>`; it
  then works over `https` with any port and path. Hosts match exactly:
  `claude.ai` does not admit `foo.claude.ai`. Accepting any host would let
  anyone register a client that sends your login to their own site.
- **Encoding.** The parameters are a query string: in the password write
  `&` as `%26`, `#` as `%23`, `%` as `%25`, `;` as `%3B` and `+` as `%2B`.
  An unencoded `+` becomes a space.
- **Guessing.** Five wrong passwords in a minute lock the page for everyone
  until the minute ends; connected clients are unaffected. No minimum length
  is enforced, so a short password is only as strong as that rate allows.
- **Hand-picked tokens.** `keygen -api` mints `ar_` plus hex. A token you
  choose yourself must not contain `?`, which starts the parameters.

### Connect a client

- **claude.ai and Claude Desktop:** Settings → Connectors → **Add custom
  connector**, URL `https://twillingate.example.com/mcp`, OAuth client ID and
  secret left empty. The browser opens the login page; enter the password.
- **ChatGPT:** add a connector with the same URL and OAuth authentication.
- **Claude Code:** `claude mcp add --transport http twillingate https://twillingate.example.com/mcp`,
  then `/mcp` → *Authenticate*. **Codex:** add the URL as an MCP server and
  choose *Authenticate*.
- **Any other MCP client** connects to the same URL. If its login stops at
  "The redirect URI's host is not allowed", the page and the
  `mcp login: redirect rejected` log line show the URI it used: add its host
  as `redirect=<host>` and restart.

A client you use stays logged in: access tokens last an hour and refresh
silently, and every refresh extends the login by 30 days. Nothing about
logins is stored, so there is no per-client revocation — change the password
to cut a device off.

| Change | Effect |
| --- | --- |
| New token or new password | Every client logs in again |
| New `resource` URL | Every client logs in again |
| `password` removed | The login is off and issued tokens stop working |
| `redirect` list edited | Connected clients keep working; new logins follow the list |
| A client unused for 30 days | That client logs in again |
| Restart or upgrade | Nothing |

**Use the HTTP API.** Scripts and anything else that is not an MCP client
hit the same operations as REST routes under `/api/`, with the same bearer
token:

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "https://twillingate.example.com/api/projects/blog/views/overview?from=2026-09-01&to=2026-09-13"
```

Every route, its inputs and its error shape are in
[twillingate.md#http-api](twillingate.md#http-api).

Upgrading from a release before the API endpoint covered both transports?
See [Upgrading from MCP_* and serve
-mcp](#upgrading-from-mcp_-and-serve--mcp) — it covers the `resource=…/mcp`
case too.

### Alternative: the token as a header

A client that can send headers can skip the login and present the token
itself — useful for scripts and headless Claude Code. It works whether or
not a password is set; a bare `API_AUTH_DSN='token://ar_…'` turns the login
off and leaves only this.

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

Rotate by minting a new token, setting it and restarting; a client holding
the old header needs `claude mcp remove twillingate` and adding again.

### Hostnames and processes

`API_ADDR` gives the API its own listener; unset, it shares the ingestion
one. Put it on its own hostname when you can (`API_ADDR` plus a second DNS
name, and `resource=` set to that hostname's origin, such as
`resource=https://api.example.com`): `/ingest/events` and the `/js/*`
scripts must stay publicly reachable for ingestion, and a dedicated
hostname keeps the access-control story simple. A reverse proxy in front of
a split install only needs to expose `/ingest/`, `/js/` and `/healthz`
publicly — keep `/mcp` and `/api/` off the public hostname if nothing
outside your network needs them.

To run the surfaces as separate processes — independently restartable and
exposable — use the `twillingate@.service` template the installer renders
next to the main unit. Its instances run `twillingate serve -ingest` and
`twillingate serve -api` (naming a flag explicitly makes that surface's
misconfiguration a hard error, rather than the lenient warn-and-skip of a
bare `serve`):

```bash
sudo systemctl disable --now twillingate
sudo systemctl enable --now twillingate@ingest twillingate@api
```

Nothing to hand-edit, so nothing for an upgrade to overwrite: the installer
re-renders the template with the main unit, restarts whichever instances
were running, and leaves `twillingate.service` disabled while an instance
is enabled — enabling both would bind the same listeners at the next boot.
To go back to one process, reverse the two commands.

**An API-only process still runs the daily aggregation pass against
`DATABASE_DSN`** — `-api` only makes the HTTP listener conditional, not the
background jobs. Point an API-only unit at a litestream replica and it will
write to that replica on every pass. Set `API_DB_PATH` (what the API reads)
and `DATABASE_DSN` (what the aggregation pass writes) deliberately: either
keep `DATABASE_DSN` on a database this process is meant to own, or accept
that a two-process topology runs the idempotent daily aggregation twice.

### Other auth modes

#### `oauth://` — your own identity provider

```bash
API_AUTH_DSN='oauth://auth.example.com[?resource=<origin>][&audience=<aud>]'
```

For when you already run or rent an IdP (Keycloak, Auth0, Authentik, …).
The server is then a resource server only: it validates the JWTs the IdP
issues and serves no login page. `resource` defaults to `PUBLIC_URL` and
must be an origin with no path. Without `audience=`, a token's `aud` may be
the origin or `<origin>/mcp`, whichever resource the client asked for; with
`audience=`, only that value passes. For a plain-http IdP in development,
use `oauth+insecure://`.

The IdP must provide:

1. **RFC 8414 metadata** at `<issuer>/.well-known/oauth-authorization-server`
   with a `jwks_uri`. The server fetches it at startup and refuses to boot
   without it — check with
   `curl -s https://auth.example.com/.well-known/oauth-authorization-server | jq .jwks_uri`.
   Many IdPs publish only OIDC discovery, so confirm rather than assume.
2. **Asymmetrically signed JWT access tokens** (RS/ES/PS). HMAC, `alg=none`
   and opaque tokens are rejected.
3. **An `aud` claim containing the resource origin or `<origin>/mcp`** —
   usually by registering the server as an API with that identifier, and
   allowing both if the IdP checks the requested resource. Or set
   `audience=` to the one value the IdP issues. Without it, logins loop.
4. **For claude.ai:** Dynamic Client Registration (RFC 7591), or a client
   registered by hand with its id entered in the connector.

An unknown `kid` triggers a JWKS refetch, throttled to once a minute, so key
rotation needs no restart.

### Upgrading from MCP_* and serve -mcp

An install from before the API endpoint covered both `/mcp` and `/api/`
must:

0. **Edit `/etc/twillingate/twillingate.env` (or the compose `.env`)
   before upgrading, not after.** The running old binary does not re-read
   it, and the new binary refuses to start until the old names are
   renamed — so there is no order in which "upgrade first, edit later"
   leaves the service running.
1. **`-api` used to mean ingest-only; ingest is now `serve -ingest`, and
   `-api` now means the private API (MCP and REST).** A single-process
   install (bare `serve`) needs nothing here. A split install from before
   this release — `twillingate.service` edited to `serve -api` plus a
   hand-made `twillingate-mcp.service` — cannot be fixed after the
   installer runs, because the installer restarts every running
   `twillingate*` unit before it returns: the old `-mcp` unit fails on the
   removed flag and the re-rendered main unit serves both surfaces. Stop
   both first, so the installer has nothing to restart, then move to the
   `twillingate@` instances ([Hostnames and
   processes](#hostnames-and-processes)):

   ```bash
   sudo systemctl disable --now twillingate twillingate-mcp
   sudo rm /etc/systemd/system/twillingate-mcp.service
   # rename the variables (step 0), then run the installer as usual
   sudo systemctl disable twillingate    # the installer re-enabled it
   sudo systemctl enable --now twillingate@ingest twillingate@api
   ```

   From then on an upgrade needs no hand edits: it restarts the running
   instances and leaves the bare unit disabled.
2. **Rename `MCP_*` to `API_*`** (and `LISTEN_ADDR` to `INGEST_ADDR`, from
   the same release) in the env file, matching [Configure the
   collector](#configure-the-collector). The collector refuses to start
   on any old name — this is not a silent fallback.
3. **Reconnect clients that logged in with the password.** Tokens issued
   for the old resource (`…/mcp`) no longer verify, so each one logs in
   again; the connector URL `/mcp` itself is unchanged. Clients using the
   token as a header are unaffected.
4. **Edit `API_AUTH_DSN` or `PUBLIC_URL` if either carries a path** — most
   commonly a leftover `resource=…/mcp`, or a `PUBLIC_URL` with a path and
   no `resource=` to override it. `resource` must be a bare origin, in
   both `token://` and `oauth://` modes, and the process now refuses to
   start on either mistake — including under a bare `serve`, which is
   otherwise lenient about a *missing* `API_AUTH_DSN` but not about one
   that is set and fails to parse. This is the one place this requirement
   is written down; [Connect a client](#connect-a-client) points back
   here instead of repeating it.

### Troubleshooting a connection

**`404` on `/mcp` or `/api/`.** The API is off. `journalctl -u twillingate | grep 'API disabled'`
names the reason.

**`401` on connect.** Run the curl from "Set it up". If curl gets a `401`
with the `resource_metadata` challenge, the server is fine and the client or
the password is the problem.

**Connects but no tools.** Wrong path — the endpoint is `/mcp`, not the bare
hostname.

**Login page: redirect URI's host not allowed.** The client returns to a
host that is not built in. The page shows the URI it used; add its host to
`API_AUTH_DSN` as `redirect=<host>` and restart.

**Login page: password not recognised, though it is right.** A `+`, `&`,
`#`, `%` or `;` in the password must be percent-encoded in the DSN.
**Too many attempts** means five wrong passwords this minute; wait for the
next one.

**Login loops in `oauth://` mode.** The IdP is issuing tokens without the
expected `aud`: the origin or `<origin>/mcp`, or the `audience=` value.

---

## Operate and recover

### Routine operations

| Task | Command |
| --- | --- |
| Logs | `journalctl -u twillingate -f` |
| Restart | `systemctl restart twillingate` |
| Upgrade (systemd) | `curl -fsSL …/install.sh \| sudo bash` — restarts the running service and reports the old and new version |
| Upgrade (compose) | `docker compose pull && docker compose up -d`. Never `down -v`: the database lives in the named volume. Pin with `TWILLINGATE_VERSION=v0.9.2` in `.env`. |
| Apply migrations only | `twillingate migrate` |
| Upgrade across a schema change | Take a Litestream snapshot first (`litestream snapshots …`, or copy the db file while the service is stopped): migrations such as 012 (web and app folded into one views family) and 014 (integer project ids) are irreversible |
| Database size | `du -h /var/lib/twillingate/twillingate.db` |
| Replication status | `journalctl -u litestream --since -1h`, or `docker compose logs litestream` |
| Dashboard rebuilds | `docker compose logs dashboards` — one `dashboards: rebuilt` line per successful build |
| Recent config changes | `sqlite3 …/twillingate.db "SELECT * FROM audit_log ORDER BY ts DESC LIMIT 20"` |

On a systemd host every CLI command needs the environment the unit loads:

```bash
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project list'
```

Aggregation, pruning and incremental vacuum run daily at 03:00 UTC, and the
visitor salt rotates at 00:00 UTC. A catch-up pass runs at startup, so
downtime across those times does not skip a day.

File logging (`LOG_FILE`) is optional and off by default; if enabled,
install `deploy/logrotate/twillingate` into `/etc/logrotate.d/`.

### Upgrading to integer project ids (migration 014)

Projects are keyed by an integer id from this migration on; the alias is
gone, and so are `config import`/`export` and per-project retention.
Before upgrading, run these against the live database — each hit is
something the migration refuses or the first daily pass will prune:

```sql
-- 1. Per-project retention overrides. Anything returned is data the first
--    daily pass after the upgrade will prune to the global window. Raise
--    the matching RETENTION_* variable first if that data must be kept.
SELECT alias, retention FROM projects WHERE retention IS NOT NULL;

-- 2. Rows whose project has no registry row. Any hit aborts migration 014.
--    Repeat for every table that has a project column.
SELECT DISTINCT project FROM views
WHERE project NOT IN (SELECT alias FROM projects);

-- 3. Duplicate key labels. Any hit aborts migration 014.
SELECT project, label, COUNT(*) FROM ingest_keys
GROUP BY project, label HAVING COUNT(*) > 1;
```

Then stop the service, copy the database (or take a Litestream
snapshot), run the installer, and check `journalctl` for migration 014.
`list_projects` (or `twillingate project list`) shows the new ids: they
follow creation order, starting at 1. Agents and scripts that stored
aliases need those ids.

Two things change on the day: retention is global from now on
(`RETENTION_*`), and anonymous actor hashes are computed from the id
instead of the alias, so an anonymous visitor seen before and after the
upgrade counts twice in that day's uniques and a session spanning it
splits. The salt rotates at midnight anyway, so the seam is one day.

### Replication with litestream

The application does not replicate anything. `serve` writes a SQLite file
and `dashboards` reads one; moving that file off the machine is a deployment
choice. You do not need any of this for a single server you are willing to
back up some other way — nothing in the collector requires object storage,
and the default compose file has no credentials in it.

Litestream streams the SQLite WAL to S3-compatible storage as it is written,
so the copy in the bucket is seconds behind rather than a day behind like a
nightly dump. That buys backup, and a read replica for the two-server
topology — both sides make outbound HTTPS connections and neither needs to
reach the other.

**Bucket and credentials.** Any S3-compatible store works; these use
Cloudflare R2.

1. Create a bucket, for example `twillingate-backup`.
2. Create an API token with **Object Read & Write** — the *writer's*
   credential.
3. For a second machine, create a second token with **Object Read** only. A
   reader that cannot write cannot corrupt the backup, however wrong its
   configuration turns out to be.

Both go in `twillingate.env`, never in a config file:

```sh
LITESTREAM_ACCESS_KEY_ID=…
LITESTREAM_SECRET_ACCESS_KEY=…
R2_BUCKET=twillingate-backup
R2_ENDPOINT=https://<account_id>.r2.cloudflarestorage.com
```

**Configuration.** `deploy/litestream/litestream.yml` is the whole of it:

```yaml
dbs:
  - path: /var/lib/twillingate/twillingate.db
    replicas:
      - type: s3
        bucket: ${R2_BUCKET}
        path: litestream
        endpoint: ${R2_ENDPOINT}
        sync-interval: 5s
```

> **`path:` is the identity of the replica in the bucket.** A restore asks
> for the database by the path it had on the machine that wrote it. Every
> side — writer, reader, recovery host — must use the same value byte for
> byte, even when the file lives somewhere else locally. Getting this wrong
> produces an empty restore rather than an error, which is the single most
> common way to be surprised here.

> **Writer and reader must run the same litestream major/minor version.**
> 0.5 stores backups in a new bucket format (LTX) that 0.3 cannot see, and
> vice versa — a mismatched restore reports `no matching backups found`
> rather than an error, which looks exactly like an empty bucket. The
> compose files pin `litestream/litestream:0.5`; if you pin a different
> version anywhere, pin it everywhere.

**On the writer.** For docker, uncomment the `litestream` service in
`docker-compose.yml`, copy `litestream.yml` next to the compose files and
put the four variables in `.env`. For systemd, `install.sh` installs
`litestream.service` and `/etc/litestream.yml` but not the binary — take it
from <https://litestream.io/install/>, then `sudo systemctl enable --now
litestream`.

### Backup restore drill — do this monthly

A backup you have never restored is not a backup.

```bash
# What is in the bucket? (0.5 syntax; replaces the old snapshots/generations)
litestream ltx -config /etc/litestream.yml /var/lib/twillingate/twillingate.db

litestream restore -config /etc/litestream.yml -o /tmp/check.db \
  /var/lib/twillingate/twillingate.db

# It must be a valid database, not just a file that exists:
sqlite3 /tmp/check.db 'PRAGMA quick_check;'          # expect: ok
sqlite3 /tmp/check.db 'SELECT COUNT(*) FROM projects;'
sqlite3 /tmp/check.db "SELECT MAX(day) FROM v_views_daily;"
rm /tmp/check.db
```

`deploy/litestream/restore.sh` performs the same restore-and-verify — point
`REPLICA_PATH` at a scratch file to use it as the drill.

Check the max day is recent. A restore that succeeds but is days stale means
litestream is not replicating: inspect `journalctl -u litestream`. A restore
reporting `no matching backups found` against a bucket that is being written
usually means a version mismatch.

### Disaster recovery — the host is gone

```bash
# On a fresh host:
git clone <repo> && cd twillingate && make build
sudo ./deploy/systemd/install.sh --user twillingate --yes
sudo vi /etc/twillingate/twillingate.env       # same R2 credentials

# Restore BEFORE starting the collector, so it does not create an empty db:
sudo -u twillingate litestream restore -config /etc/litestream.yml \
  -o /var/lib/twillingate/twillingate.db /var/lib/twillingate/twillingate.db
sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db 'PRAGMA quick_check;'

sudo systemctl start twillingate litestream
```

> **Restore first, always.** Starting `twillingate serve` against an empty
> data directory creates a fresh database, and litestream would then
> replicate that empty database over the good backup.

Two things do not survive: the visitor salt (already rotated daily by
design, so at most one day of visitor continuity is lost) and any events
still buffered in memory when the host died — bounded by
`BUFFER_FLUSH_INTERVAL`.

### Migrating one server to two

1. Stand up the VPS, pointing litestream at the same bucket.
2. On the machine that will render dashboards, stop the `twillingate`
   service and any litestream writer.
3. Install `restore.sh` on cron with `SOURCE_DB` set to the database path
   **on the VPS** — it must match `path:` in `litestream.yml`, not wherever
   the file lands locally.
4. Drop `docker-compose.yml` from this machine and run
   `docker-compose.evidence.yml` alone, with `DASHBOARDS_DB_PATH` and the
   restore's `REPLICA_PATH` both set to `/data/replica.db`. Confirm the
   first restore lands before the next rebuild.
5. Repoint the tracking snippet's `src` at the VPS hostname.
