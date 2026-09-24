# Deployment

The operator's runbook: getting twillingate onto a host, keeping it backed
up, getting it back after the host is gone. Using it is
[twillingate.md](twillingate.md); both are served over MCP, this one as
`docs://deployment`. **One server** runs ingestion and dashboards together;
**two servers** runs ingestion on a VPS and dashboards at home off a
replica, moved there by [litestream](#replication-with-litestream).

- [Install](#install)
- [Configure the collector](#configure-the-collector)
- [Reporting with Evidence](#reporting-with-evidence)
- [The API endpoint](#the-api-endpoint)
- [Operate and recover](#operate-and-recover) — including litestream for the two-server topology

## Install

### docker compose

Ingestion, the SDK and — once `API_AUTH_DSN` is set — the API; dashboards
are a second file, under [Reporting with Evidence](#reporting-with-evidence).

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
# Or from a checkout: git clone, make build, then
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
`twillingate.env`; nothing in the collector reads them. The old names
`MCP_*`, `LISTEN_ADDR` and `RETENTION_WEB_*`/`RETENTION_APP_*` refuse the
boot, each naming its replacement: `API_*`, `INGEST_ADDR`,
`RETENTION_VIEWS_*`.

### Raspberry Pi and low-resource hosts

- Raise `BUFFER_FLUSH_INTERVAL` (say `30s`) and litestream's `sync-interval`: fewer, larger writes.
- Set `GOMEMLIMIT` (unit and compose files ship `128MiB`) and keep `GEO_DSN` off `maxmind://`, which holds a database in memory.
- Lower `RETENTION_VIEWS_RAW_DAYS` (say `7`): raw views are the largest table, and the live halves of the `v_views_*` views scan it on every query.

## Reporting with Evidence

`twillingate dashboards` renders an Evidence site from the database; in
compose it is `docker-compose.evidence.yml`, on port 3000, which answers
`503` for about a minute until the first build finishes. It rebuilds within
a minute of the database changing (size and modification time), no closer
together than `DASHBOARDS_INTERVAL` (default `15m`), snapshotting the
database into `DASHBOARDS_WORK_DIR` each time — the host needs room for one
more copy.

A rebuild that cannot read its data does not publish: a database that is not
a twillingate one stops at `not a twillingate database`, a source Evidence
cannot read at `could not read a source`. The previous site keeps serving
and no `dashboards: rebuilt` line appears; the fault is in the database, so
check `docker compose logs restore` and that `SOURCE_DB` matches the `path:`
in `litestream.yml` exactly.

**One server.** Add the second compose file beside the tracking one;
`COMPOSE_FILE` joins them on one database (else pass `-f` to every command).

```bash
curl -fsSLO https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/compose/docker-compose.evidence.yml
echo COMPOSE_FILE=docker-compose.yml:docker-compose.evidence.yml >> .env
docker compose up -d
```

**Two servers.** Fetch the same file on the dashboard machine and run it
alone (`docker compose -f docker-compose.evidence.yml up -d`) with
`DASHBOARDS_DB_PATH=/data/replica.db` in `.env` — it defaults to
`/data/twillingate.db`, the shared-volume case. Restore the replica from the
compose file's commented `restore` service or host cron, never both. To move
an existing single server here, stand up the VPS on the same bucket, stop
the old writer, then point the reader's restore at the VPS's database path:
`REPLICA_PATH` and `DASHBOARDS_DB_PATH` must both read `/data/replica.db`,
and the tracking snippet's `src` repoints at the VPS hostname.

`restore.sh` verifies into a temporary file, renames only on success, and
counts the applied migrations, because litestream does not fail an *empty*
restore: a `path:` that does not match the writer's, or a bucket from a
different litestream major/minor, downloads a valid database with nothing in
it and exits 0. The previous replica keeps serving either way, and an empty
restore lands as `restored file is not a twillingate database`.

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
| `resource` | The API origin, with no path (`resource=https://api.example.com`); one login covers `/mcp` and `/api/`. Defaults to `PUBLIC_URL`; set it when the API has its own hostname. MCP clients read `/.well-known/oauth-protected-resource/mcp`, which names `<origin>/mcp`; `/api/` clients read `/.well-known/oauth-protected-resource`, which names the origin. A value carrying a path refuses the boot, in both `token://` and `oauth://` mode. |
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
> `DATABASE_DSN` (what the pass writes) deliberately: aimed at a litestream
> replica, an API-only unit writes to that replica on every pass — or
> accept that a two-process topology runs the idempotent daily aggregation
> twice.

### Other auth modes

```bash
API_AUTH_DSN='oauth://auth.example.com[?resource=<origin>][&audience=<aud>]'
```

For an IdP you already run or rent (Keycloak, Auth0, Authentik, …): the
server validates the JWTs it issues and serves no login page. `resource`
defaults to `PUBLIC_URL` and must be an origin with no path;
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

## Operate and recover

### Routine operations

| Task | Command |
| --- | --- |
| Logs | `journalctl -u twillingate -f` |
| Restart | `systemctl restart twillingate` |
| Upgrade (systemd) | `curl -fsSL …/install.sh \| sudo bash` — restarts the running service and reports the old and new version |
| Upgrade (compose) | `docker compose pull && docker compose up -d`. Never `down -v`: the database lives in the named volume. Pin with `TWILLINGATE_VERSION=v0.9.2` in `.env`. |
| Apply migrations only | `twillingate migrate` |
| Upgrade across a schema change | Snapshot first (`litestream snapshots …`, or copy the file while the service is stopped): migrations 012, 014, 015 and 016 are irreversible. Pre-checks and what changes on the day: <https://github.com/dmtrkzntsv/twillingate/blob/main/deploy/UPGRADES.md> (not served over MCP; open it in the repository) |
| Database size | `du -h /var/lib/twillingate/twillingate.db` |
| Replication status | `journalctl -u litestream --since -1h`, or `docker compose logs litestream` |
| Dashboard rebuilds | `docker compose logs dashboards` — one `dashboards: rebuilt` line per successful build |
| Recent config changes | `sqlite3 …/twillingate.db "SELECT * FROM audit_log ORDER BY ts DESC LIMIT 20"` |

Every CLI command on a systemd host needs the unit's environment:
`sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project list'`.
Aggregation, pruning and incremental vacuum run daily at 03:00 UTC and the
visitor salt rotates at 00:00 UTC; a catch-up pass at startup means downtime
across those times skips no day. With `LOG_FILE` set, install
`deploy/logrotate/twillingate` into `/etc/logrotate.d/`.

### Replication with litestream

Litestream streams the SQLite WAL to S3-compatible storage as it is written,
so the bucket copy is seconds behind: a backup, and a read replica, with
both sides connecting outbound. A single server you back up some other way
needs none of it. Any S3-compatible store works — these use Cloudflare R2.
Create a bucket and an **Object Read & Write** token for the writer, plus an
**Object Read** token for any reader (one that cannot write cannot corrupt
the backup), in `twillingate.env`, never in a config file:

```sh
LITESTREAM_ACCESS_KEY_ID=…
LITESTREAM_SECRET_ACCESS_KEY=…
R2_BUCKET=twillingate-backup
R2_ENDPOINT=https://<account_id>.r2.cloudflarestorage.com
```

`deploy/litestream/litestream.yml` is the whole configuration:

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
> for the database by the path it had on the machine that wrote it, so
> writer, reader and recovery host must use that value byte for byte, even
> when the file lives elsewhere locally. Getting it wrong produces an empty
> restore rather than an error.

> **Writer and reader must run the same litestream major/minor version.**
> 0.5 stores backups in a bucket format (LTX) 0.3 cannot see, and vice
> versa: a mismatched restore reports `no matching backups found`, which
> looks exactly like an empty bucket. The compose files pin
> `litestream/litestream:0.5` — pin the same version everywhere.

On the writer: for docker, uncomment the `litestream` service, copy
`litestream.yml` next to the compose files and put the four variables in
`.env`. For systemd, `install.sh` installs `litestream.service` and
`/etc/litestream.yml` but not the binary — take that from
<https://litestream.io/install/>, then enable the unit:
```bash
sudo systemctl enable --now litestream
```

### Backup restore drill — do this monthly

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

A backup you have never restored is not a backup, and
`deploy/litestream/restore.sh` runs the same restore-and-verify against a
scratch `REPLICA_PATH`. The max day must be recent: days stale means
litestream is not replicating (`journalctl -u litestream`), and `no matching
backups found` against a written bucket means a version mismatch.

### Disaster recovery — the host is gone

```bash
# On a fresh host: install, then restore BEFORE starting the collector.
git clone <repo> && cd twillingate && make build
sudo ./deploy/systemd/install.sh --user twillingate --yes
sudo vi /etc/twillingate/twillingate.env       # same R2 credentials
sudo -u twillingate litestream restore -config /etc/litestream.yml \
  -o /var/lib/twillingate/twillingate.db /var/lib/twillingate/twillingate.db
sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db 'PRAGMA quick_check;'
sudo systemctl start twillingate litestream
```

> **Restore first, always.** Starting `twillingate serve` against an empty
> data directory creates a fresh database, and litestream would then
> replicate that empty database over the good backup.

Two things do not survive: the visitor salt (rotated daily anyway, so at
most a day of continuity) and events buffered in memory when the host died,
bounded by `BUFFER_FLUSH_INTERVAL`.
