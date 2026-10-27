# Deployment

The operator's runbook: getting twillingate onto a host, keeping it backed
up, getting it back after the host is gone. Using it is
[twillingate.md](twillingate.md); both are served over MCP, this one as
`docs://deployment`. One process serves two surfaces — ingest, and the
console (MCP, REST, the login, the dashboards at `/app/` and shared
widgets at `/share/`) — and its whole state is one SQLite file.

- [Install](#install)
- [Configure the collector](#configure-the-collector)
- [The console](#the-console)
- [Dashboards at /app/](#dashboards-at-app)
- [Operate and recover](#operate-and-recover) — including backups

## Install

Linux on amd64 or arm64, or Docker on amd64 or arm64. The binary is
static (pure Go, no cgo), so any Linux distribution runs it, whatever its C
library; `install.sh` sets it up as a systemd service, so it needs systemd. 32-bit ARM is not built. Plan on 1 GB
of memory: the server stays small, near its soft memory target
(`GOMEMLIMIT`, which the unit and compose files set to 256MiB). SQL runs
on an [external SQL engine](#external-sql-engine), a process of its own
whose memory is not counted here. The daily pass
writes the event files in the server process, in Go, and starts no child;
on a month of a million events it peaks near 150 MB of resident memory
(measured in a process of 25 MB at rest), on the day a month is merged.
Its memory follows rows, not bytes: the dump holds about 45 bytes a line
of the day's log (a day of a million lines, about 900 MB of log, peaks
near 110 MB), and the merge about 40 bytes a row of the month. At volume
the console's reads, not the pass, set the server's peak: a year-long
Views or Product dashboard at a million events a month peaks at about
283 to 365 MB in the console process when the page loads it (one
streamed request), and 387 to 405 MB when its widgets are asked for one
by one. The built-in widgets' reader, in the console process, holds at
most about 128 MiB of identities, 128 MiB of today's logs decoded
(dropped after five idle minutes) and 256 MiB of aggregations (two scans
at once, 128 MiB each), counted approximately. `serve` starts no other
process: SQL (a widget's, `query`'s and the console's check of the users' SQL
widgets at start) goes to the external SQL engine when `SQL_ENGINE_SOCKET`
is set, and is refused when it is not, while the form-attribution lookup,
the daily pass, the built-in widgets and the check before a rename or merge
rule is saved read the files in Go.

### docker compose

Ingestion, the SDK and — once `CONSOLE_AUTH_DSN` is set — the console: MCP,
REST and the [dashboards at /app/](#dashboards-at-app).
The image (`ghcr.io/dmtrkzntsv/twillingate`, amd64 and arm64) is the
static binary on a distroless base, with CA certificates and time zone
data and nothing else: no shell, no package manager. It runs as uid and
gid 10001 and keeps the database and the event files in the
`/var/lib/twillingate` volume; run CLI commands through
`docker compose exec twillingate twillingate …`.

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
the collector falls back to the scheme of `INGEST_URL`, so a single-hostname
install with `INGEST_URL=https://…` works unconfigured. A `Host` that is not
a plain hostname (optional port aside) leaves the file originless and the
SDK dormant with a console warning. Snippets use `INGEST_URL`; change the
`src` per hostname, and keep the console on one — OAuth and `cloudflare://` bind
to it.

A dashboard page loads its widgets over one streamed response
(`POST /api/widgets/stream`, `text/event-stream`), so a proxy in front of the
console must not buffer it, or the cards appear only once the last one is
ready. Caddy's `reverse_proxy` and cloudflared flush `text/event-stream` as it
comes. With Caddy's `encode` in front of the console, check that a
dashboard's cards appear one by one, or leave `text/event-stream` out of what
it compresses. nginx honours the `X-Accel-Buffering: no` header the console
sends; if your configuration ignores it (`proxy_ignore_headers
X-Accel-Buffering`), add `proxy_buffering off;` to the console's `location`.
The stream sends a keepalive every 15 seconds, well inside Cloudflare's
100-second proxy timeout; a proxy read timeout under 15 seconds cuts it.

## Configure the collector

| Variable | Meaning |
| --- | --- |
| `INGEST_ADDR` | Address to bind. Default `127.0.0.1:8080` (the docker image sets `0.0.0.0:8080`). |
| `INGEST_DISABLED` | `true` (or `1`) stops ingest completely while the console keeps running: `POST /ingest/events`, the legacy `POST /api/events` and `POST /ingest/forms/{name}` refuse every request with `429`, `ingest is disabled` and `Retry-After: 3600`, before the body is read or the key looked up, so nothing is written. The SDK drops a batch refused with a 4xx rather than queueing it. Preflights, the SDK scripts and `/healthz` answer as usual; `limits` reports `ingest_disabled` and the console's Projects page says so. For a migration, a restore or an abuse incident. When the ingest and console surfaces run as separate processes, set it for both: the refusal is the ingest process's setting, while the console's banner and `limits` read the console process's own. Takes `true`/`false`, `1`/`0`; anything else refuses the boot. Default `false`. |
| `INGEST_URL` | The collector's public base URL (`https://twillingate.example.com`). Embed snippets and MCP integration guidance are built from it; unset, they carry a placeholder. Also the default for `CONSOLE_URL`. With [several hostnames](#one-collector-several-hostnames), the default one. |
| `DATABASE_DSN` | Store DSN. Only `sqlite://<path>` today. Required. |
| `DATA_DIR` | Directory of the event files: a log per project and day, then Parquet day and month files. Default `data/` beside the database's file (`/var/lib/twillingate/data`); where only the `raw/` of an older release is there, the ingest process moves it to `data/` at start (where `raw/` is a mount point it refuses instead: set `DATA_DIR` to the `raw/` path), and a process without ingest reads `data/` from its start. Its name before this release is ignored, with a warning at start (see `deploy/UPGRADES.md`). The engine reads and writes everything in it, so it may not hold the database. On a split install, give both processes the same value. It must be on a filesystem that supports `flock` (most local filesystems; not some FUSE or SMB mounts): the ingest process holds `.lock` in it, and refuses to start where it cannot. A second ingest process refuses while one runs. To move it, stop the service, move the directory, set the variable, start. |
| `GEO_DSN` | Country lookup: `cloudflare://` (header), `maxmind://<license-key>`, or `none://`. |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error`. Default `info`. |
| `LOG_FORMAT` | `json` or `text`. Default `json`. |
| `LOG_FILE` | Log to this path instead of stdout. |
| `BUFFER_FLUSH_MAX_EVENTS` | Flush once this many events are buffered. Default 1000. |
| `BUFFER_FLUSH_INTERVAL` | Flush at least this often. Default `5s`. |
| `BUFFER_CAPACITY` | Bounded queue size; excess is dropped rather than growing memory. Default 10000. |
| `INGEST_LATE_DAYS` | Days back a client timestamp may lie; an older one is clamped to this edge, counted, never dropped. At least 1 and at most `RETENTION_DAYS`, so a late event never lands in a day whose file is already deleted. Default 1. |
| `RETENTION_DAYS` | Days the raw event files under `DATA_DIR` are kept, in whole months: a month file is deleted once its last day is older. At least 1. Default 400. |
| `RETENTION_FORMS_DRAFT_DAYS` | Days a form created by its first submission accepts submissions as a draft; unless approved by then, it refuses them and the daily pass archives it. Read when a draft is created or restored, so a change moves no existing draft's window. At least 1. Default 7. |
| `ATTRIBUTION_DAYS` | How far before a form submission its source is looked for: the submitter's last visit with a referrer or a UTM that started within that many days (`docs/twillingate.md`, "Form submissions"). Read when a submission is attributed, about a minute after it arrives. From 1 to `RETENTION_DAYS`. Default 7, or `RETENTION_DAYS` when that is smaller. |
| `RETENTION_ARCHIVED_DAYS` | Days after archiving that a project (with all its data and its widget shares), a dashboard, a widget or a widget share is deleted by the daily pass. 0 keeps archived items forever. Default 30. |
| `CONSOLE_AUTH_DSN` | Authentication for the console (MCP, REST and the dashboards' data): `token://<token>?password=…` for the built-in browser login (see [The console](#the-console)), or `oauth://<issuer-host>` for your own identity provider. Unset, bare `serve` skips the console with a warning. |
| `CONSOLE_ADDR` | Give the console its own listener. Defaults to `INGEST_ADDR` (shared). |
| `CONSOLE_URL` | The console's public origin when it has a hostname of its own (`https://console.example.com`), no path. The login's resource, issuer and the dashboards' callback follow it, so that host needs no `redirect=` entry, and share links are built on it. Defaults to `INGEST_URL`. |
| `CONSOLE_DATABASE_DSN` | Database the console reads for queries, as `sqlite://<path>` (a copy or replica of the store, say). Defaults to `DATABASE_DSN`. |
| `CONSOLE_QUERY_TIMEOUT` | Per-query guard on reads and the `query` operation; also bounds a reporting widget's sql. Default `30s`. |
| `CONSOLE_QUERY_ROWS_MAX` | Row cap on the `query` operation; also bounds a reporting widget's sql. Default 1000. |
| `SQL_ENGINE_SOCKET` | Path of the Unix socket of the [external SQL engine](#external-sql-engine) that answers SQL: a widget's and `query`'s. Unset or empty, there is none: SQL widgets and `query`'s `sql` are refused, `sql` leaves the component list, and everything else works. Give the console's process the same value as `reporting dev`. Default empty. |
| `REPORTING_CACHE_MAX_AGE` | How long an ordinary widget data request reuses a sql widget's loaded value before loading again. 0 turns the cache off: every request loads again. Default `15m`. |
| `REPORTING_REFRESH_MAX_AGE` | A `fresh=true` request reuses a result younger than this instead of `REPORTING_CACHE_MAX_AGE`; must not exceed it when that is non-zero. Default `1m`. A dashboard with auto-refresh on reloads every max(`REPORTING_CACHE_MAX_AGE`, `REPORTING_REFRESH_MAX_AGE`) while its window has focus; both 0 removes the option. |

Day counts end in `_DAYS` and take a whole number; every other time
(`BUFFER_FLUSH_INTERVAL`, `CONSOLE_QUERY_TIMEOUT`, `REPORTING_*_MAX_AGE`)
is a duration with a unit: `90s`, `15m`, `1h`.

The old names refuse the boot, naming their replacement: `LISTEN_ADDR`
(now `INGEST_ADDR`), the six `API_*` names (now `CONSOLE_*`), and
`PUBLIC_URL`, `FORMS_DRAFT_DAYS`, `CONSOLE_DB_PATH`, `CONSOLE_QUERY_MAX_ROWS`,
`REPORTING_CACHE_SECONDS` and `REPORTING_REFRESH_SECONDS` (see
`deploy/UPGRADES.md`). The older `MCP_*` names are no longer checked; a
leftover one is ignored, so rename any still in `twillingate.env`.

### Raspberry Pi and low-resource hosts

- A 64-bit OS (Raspberry Pi OS 64-bit on a Pi 3, 4, 5 or Zero 2 W); 32-bit ARM is not built. twillingate runs as one process: SQL goes to the external SQL engine, if you run one, and the built-in dashboards need none.
- Raise `BUFFER_FLUSH_INTERVAL` (say `30s`): fewer, larger writes.
- Keep `GOMEMLIMIT` (unit and compose files ship `256MiB`) and keep `GEO_DSN` off `maxmind://`, which holds a database in memory. `GOMEMLIMIT` is a soft target, not a bound: the garbage collector works harder near it, and the process may go past it. The daily pass peaks near 150 MB on a month of a million events, and at about 230 to 400 MB on one with 2,000 distinct attribute keys (lower under a lower target, at more CPU).
- Plan on 1 GB of memory, as in [Install](#install): `GOMEMLIMIT` is the server's own target (the daily pass included: it runs in the server). An external SQL engine on the same host needs memory of its own on top.
- Lower `RETENTION_DAYS` if the event files outgrow the card: a month file is deleted once its last day is older, and nothing else holds events.

## The console

The console is the authenticated surface. `serve -console` answers both
transports on one listener: streamable HTTP MCP at
`https://twillingate.example.com/mcp`, REST under
`https://twillingate.example.com/api/`. There is no stdio server — every
client talks to the running collector over the network — and one
`CONSOLE_AUTH_DSN` protects both.

> **A connected session reads every non-archived project — including
> personal data on projects whose clients send ids — and can use the
> management tools.** There is no per-project scoping. The token and the
> password are the whole of the access control. Treat both as admin
> credentials.

### Set it up

The binary runs its own login — no identity provider to run — and every
client connects through a browser page that asks for a password.

1. Mint the token — `keygen -console` mints it as `ar_` plus hex. It prints
   `CONSOLE_AUTH_DSN=token://ar_…`:
   ```bash
   sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate keygen -console'
   ```
2. Set it in `/etc/twillingate/twillingate.env` (compose: `.env`), in single
   quotes — these commands load the file with `sh`, where an unquoted `&`
   cuts the value short:
   ```
   CONSOLE_AUTH_DSN='token://ar_…?password=<password>'
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
| `resource` | The console origin, with no path (`resource=https://console.example.com`); one login covers `/mcp` and `/api/`. Defaults to `CONSOLE_URL`, which defaults to `INGEST_URL`: set `CONSOLE_URL` when the console has its own hostname, and keep `resource=` for the rare case where the two must differ. MCP clients read `/.well-known/oauth-protected-resource/mcp`, which names `<origin>/mcp`; `/api/` clients read `/.well-known/oauth-protected-resource`, which names the origin. A value carrying a path refuses the boot, in both `token://` and `oauth://` mode. |
| `redirect` | An extra host clients may return to, such as `redirect=app.example.com`; repeat once per host. |
| `insecure` | `insecure=1` lets the console origin (`resource`, or the `CONSOLE_URL`/`INGEST_URL` it defaults to) be plain `http` on any host, such as `http://homelab:8290` on a tailnet; without it, `http` refuses the boot except on `localhost`, `127.0.0.1` and `[::1]`. The password and tokens then cross the network as sent, so use it only where the link encrypts them itself (Tailscale, WireGuard, a VPN), never on an open network. The boot logs a warning. MCP web connectors such as claude.ai still need `https`. |

Accepted without `redirect=`, at any port and path: `localhost`,
`127.0.0.1` and `[::1]` over `http` or `https`, plus `claude.ai` and
`chatgpt.com` over `https`, plus the dashboards' own `<resource>/app/callback`
over the resource's scheme (`http` with `insecure=1`). Any other host is refused until added as
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
logging in; a bare `CONSOLE_AUTH_DSN='token://ar_…'` turns the login off and
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

`CONSOLE_ADDR` gives the console its own listener; unset, it shares the ingestion
one. Put it on its own hostname when you can, with `resource=` set to that
origin: only `/ingest/`, `/js/` and `/healthz` have to be public. For
separate processes, use the `twillingate@.service` template rendered beside
the main unit: its instances run `serve -ingest` and `serve -console`, and
naming a flag makes that surface's misconfiguration a hard error rather than
the warn-and-skip of a bare `serve`.

```bash
sudo systemctl disable --now twillingate
sudo systemctl enable --now twillingate@ingest twillingate@console
```

Disable the bare unit first: enabling both `twillingate.service` and an
instance binds the same listeners at the next boot. An upgrade restarts
running instances and leaves the bare unit disabled; reverse to go back.

> **A console-only process still runs the daily pass against
> `DATABASE_DSN`** — `-console` only makes the HTTP listener conditional,
> not the background jobs. Set `CONSOLE_DATABASE_DSN` (what the console reads)
> and `DATABASE_DSN` (what the pass writes) deliberately: aimed at a copy of
> the database, a console-only unit writes to that copy on every pass — or
> accept that a two-process topology runs the idempotent daily pass twice.

### External SQL engine

twillingate answers SQL (SQL widgets, `query`'s `sql` and the console's
start-up check of the users' SQL widgets) on an external SQL engine: a
separate process that serves a small HTTP and JSON protocol on a Unix
socket and reads the event files from its own read-only mount of
`DATA_DIR`. Set `SQL_ENGINE_SOCKET` to its socket. Without one, SQL is
refused as `invalid` with "SQL needs an external SQL engine
(SQL_ENGINE_SOCKET)": `/api/components` and `list_components` leave `sql`
out of `source_types`, a stored SQL widget stays on its dashboard (not
archived) and its card shows that refusal while the rest loads, and the
start-up check skips SQL widgets. Structured queries, the analytics tools
and the built-in dashboards need no engine.

At start, a `serve` with the console calls the engine's
`GET /v1/health`; an ingest-only `serve` and the other commands that open
the database call it only to check a system widget that reads SQL (none
today), so they run whatever the engine's state. An engine that speaks
another protocol version stops the start, naming both; one that does not answer is logged as a
warning and the start goes on, each query trying it again, so the engine
may start or restart after twillingate. A query the engine cannot answer
then is refused with "the SQL engine failed (SQL_ENGINE_SOCKET)", and the
cause is logged.

The protocol, version 1 (the `sqlengine` package holds its types, a
client and fixtures: [sqlengine/protocol.go](https://github.com/dmtrkzntsv/twillingate/blob/main/sqlengine/protocol.go)):

- `GET /v1/health` answers `{"protocol": 1, "engine": "<name> <version>"}`.
- `POST /v1/query` takes `{"sql", "args", "max_rows", "mode", "page",
  "scope", "confine", "tables"}`. `args` binds the statement's `$tw_*`
  names (strings). `mode` is `rows` (the statement's rows), `text` (every
  cell as text), `shape` (the columns only), `page` (a filtered, sorted
  page of a remote table, with `__tw_matched` and `__tw_total` appended) or
  `count` (`["total","matched"]` for a page with no row). In mode `page`,
  `max_rows` caps the page size: a `page.limit` of 0 means `max_rows`, and
  a page whose limit exceeds it is refused. `scope` is a
  project id or `null` for every project; `confine` keeps the SQL on the
  seven tables. `tables` names the files to read: per project its Parquet
  files, logs, session files and the days they do not cover yet,
  identities and aliases, every path relative to `DATA_DIR` (the engine
  refuses one that is absolute or climbs out), with the event and
  attribute rules to apply. The deadline travels as `X-Timeout-Ms`, a
  cancelled request as the connection closing.
- A success is `{"columns", "rows", "truncated"}`: each cell JSON null, a
  boolean, a number (a JSON number) or a string (dates, times, timestamps,
  UUIDs, NaN and the infinities). twillingate reads at most 256 MiB of an
  answer and refuses a longer one.
- An error is `{"error": {"kind", "message"}}` with status 400 (`refused`,
  `sql`), 409 (`stale`: a file the table spec names is gone), 503 with
  `Retry-After` (`busy`), 504 (`timeout`) or 500 (`out_of_memory`,
  `out_of_memory_loading_logs`, `exited`, `internal`). twillingate words
  each kind for whoever asked: the engine's own message for `sql` and
  `refused`, a timeout as its own `timeout` refusal naming
  `CONSOLE_QUERY_TIMEOUT` (504 over REST), what to narrow for memory, and
  `busy` as its own `busy` refusal, 429 over REST with `Retry-After`: try
  again later. A busy engine is not retried within the
  request. twillingate sends each query as it comes (a dashboard's SQL
  widgets together) and waits for the answer, up to
  `CONSOLE_QUERY_TIMEOUT`; an engine that runs fewer at once queues the
  rest and answers `busy` only when it cannot. A `stale` one is: the daily pass deletes a day's log once it is
  dumped and a month's day files once they merge, so a spec built just
  before can name a file that is gone when the engine reads it.
  twillingate then builds the spec afresh and sends the query again, after
  50 ms and then 100 ms, three attempts in all within the request's
  timeout.

The SQL is in DuckDB's dialect, which the docs and the built-in examples
assume.

#### Engine requirements

What an engine must do to answer twillingate as it expects. Where a page
already defines a behaviour, it is linked rather than repeated.

- **Unknown fields.** Ignore any field of a request (top level or nested)
  that version 1 does not define: version 1 grows by adding fields, and
  an engine that refuses one breaks on the next release. twillingate
  ignores unknown fields of an answer the same way.
- **The seven tables.** `events`, `views`, `product`, `measures`,
  `sessions`, `identities` and `aliases`, with the columns, types and
  meanings in [Writing SQL against the event files](twillingate.md#writing-sql-against-the-event-files)
  and `schema://views`. `events` is the union, by column name, of the
  spec's Parquet files and open logs, with one `attr_<key>` VARCHAR column
  per attribute key any of them carries; `views`, `product` and
  `measures` are its rows of one `family`, `measures` with `bucket` added
  as defined there. A file's columns may differ from another's: a column
  a file lacks reads NULL. The `v` key of a log line (its format version)
  is no column.
- **Rules on read.** Apply `tables.rules` to `events` and the three
  family tables, never to `sessions`, `identities` or `aliases`, which
  describe the events as stored. Their meaning is in
  [Event rules](twillingate.md#event-rules) and
  [Attribute rules](twillingate.md#attribute-rules); on the wire, a merge
  is a `rename` (both read the same), and these details are written down
  only here:
  - an event rule matches the project, the family and the exact name,
    case-sensitively; a `rename` reads the event under `to`, a `block`
    leaves it out, and so does a `rename` whose `to` is itself blocked in
    the same project and family;
  - an attribute rule covers the project's `product` and `measures` rows
    only, and matches its key's column as DuckDB matches column names,
    case-insensitively, so it also covers keys that differ only in case.
    The renamed key's column reads NULL on those rows, as a blocked key's
    does; the target's column reads the target's own value, else the
    values of the keys renamed into it, in key order (a `coalesce`). A
    target no file has yet is added as a column. A rename whose key and
    target are the same column changes nothing, and a rename onto a
    blocked key only blanks the renamed key.
- **The open logs.** Each `.log` is NDJSON, one event per line, shaped
  like its Parquet row: the column names above, attributes flat as
  `attr_<key>` with the key as sent, empty fields left out. Keep the first
  line per `id`, in file order: a resend appends the same `id` again. Read
  the fixed keys at the types the column table gives (`ts` and
  `received_at` as TIMESTAMP, `day` as DATE, `project_id`,
  `display_width`, `display_height` and `consent` as BIGINT, `value` and
  `sample_rate` as DOUBLE) and every other key as text, never inferring a
  type from the values (a value that looks like a UUID or a time stays as
  sent). Skip a line that does not parse: ingest may be writing the last
  one. Of keys equal but for case, the first in byte order keeps its name
  and each next one is named `<key>_<n>`, the first `n` free
  (`attr_Plan`, `attr_plan_1`).
- **Sessions.** `sessions` is the union of the spec's session files and,
  for each project's `session_gaps` (days the files do not cover yet,
  both ends inclusive), the sessions derived from that project's views of
  those days, before the rules, as
  [the sessions table](twillingate.md#writing-sql-against-the-event-files)
  defines them: a view with a `session_id` belongs to that declared
  session; any other starts a new one when it is the actor's first view of
  the day or comes more than 30 minutes (1,800 seconds) after the
  actor's previous view; the day is part of the session's key, so midnight
  UTC cuts a session. Views at the same second are ordered by `id`;
  `duration_sec` is the whole seconds between the first and the last
  view. The built-in dashboards count sessions the same way, natively
  (`internal/native`), and the session files are written to the same rule
  (`internal/archive/derive.go`).
- **Identities and aliases.** When the spec names a project's file, use it
  and fold in what the project's open logs add; when it names none (`""`),
  derive the table from every event of the project. Folding keeps one row
  per key: the earliest `first_seen`, the latest `last_seen`, the counts
  added up and the `name` sent with the latest event that carried one.
- **`scope`.** With a project id, each of the seven tables reads only that
  project's rows, as if the SQL had filtered `project_id` itself; `null`
  reads every project the spec names. Scope keeps SQL on one project, but
  only `confine` stops SQL written to leave it.
- **`confine`.** Refuse, as `refused` and before anything runs, SQL that
  reads anything but the seven tables and its own `WITH` names: a table
  function other than `range`, `generate_series` and `unnest`, a file
  named in `FROM`, a name with a schema or catalog (`main.events`), any
  other table or view, and `SHOW`; the list is in
  [Writing SQL against the event files](twillingate.md#writing-sql-against-the-event-files).
  A path inside a string compared as text is just text. Whatever
  `confine` says, run one read-only statement, never write, and read no
  file the spec does not name.
- **Arguments.** `args` binds `$tw_<name>` parameters, always as strings;
  the SQL casts them (`CAST($tw_project AS BIGINT)`). A `$tw_p<n>` name is
  the page's own.
- **`text` and `page` cells.** Every cell as text, with column names and
  order and row order kept: NULL as `""`, a DOUBLE, FLOAT or DECIMAL as
  its DOUBLE's shortest text without a trailing `.0` (`100`, `3.5`), any
  other value as DuckDB casts it to VARCHAR.
- **`page` and `count`.** Filters and sort compare those cells, with the
  semantics in [Filtering and paging a table](reporting.md#filtering-and-paging-a-table):
  equality on the exact text, `<` and `>` as numbers when the value is a
  decimal number and as code-point text otherwise, empty cells matching
  only `!=` and `not in`, non-empty before empty, numbers before text,
  ties broken by every column in order; without `sort`, the query's own
  order. A `distinct` page answers the columns `value` (the cell),
  `rows` (its row count, as text), then the two counts, one row per value
  among the rows that pass the filters on the other columns (a filter on
  the distinct column itself is ignored), the most rows first and then
  by value in sort order; its `__tw_matched` counts values.
- **Status and deadline.** Answer each kind with its status
  (`Kind.Status`), `stale` whenever a spec file is missing or vanishes
  mid-read. Stop a query when `X-Timeout-Ms` passes (504 `timeout`) or the
  connection closes.

**Running an external SQL engine next to twillingate.** Put the socket in a
directory both processes can reach (a shared directory, or a volume mounted
in both containers) and give twillingate its path as `SQL_ENGINE_SOCKET`.
The socket is unauthenticated: anyone who can connect to it reads every
project's events. Put it in a directory only the twillingate service user
(or a group the two services share) can traverse, mode `0750`, with the
socket itself at `0660`, and never on a path other users or containers
reach. Mount `DATA_DIR` (`data/`) into the engine read-only: it only
reads, and twillingate alone writes the files. The engine may start
before or after twillingate; at start twillingate checks the protocol
version against `GET /v1/health` and refuses to start on a mismatch,
while an engine that is down at that moment is only logged and retried by
each query.

### Other auth modes

```bash
CONSOLE_AUTH_DSN='oauth://auth.example.com[?resource=<origin>][&audience=<aud>]'
```

For an IdP you already run or rent (Keycloak, Auth0, Authentik, …): the
server validates the JWTs it issues and serves no login page. `resource`
defaults to `CONSOLE_URL` (then `INGEST_URL`) and must be an origin with no path;
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
| `404` on `/mcp` or `/api/` | The console is off — usually a DSN that does not parse. `journalctl -u twillingate \| grep 'console disabled'` names the reason. |
| `401` on connect | Run the curl from [Set it up](#set-it-up). A `401` carrying the `resource_metadata` challenge means the server is fine: the client or the password is the problem. |
| Connects but no tools | Wrong path: the endpoint is `/mcp`, not the bare hostname. |
| Login page: redirect URI's host not allowed | The client returns to a host that is not built in. The page shows the URI; add its host as `redirect=<host>` and restart. |
| Login page: password not recognised, though it is right | A `+`, `&`, `#`, `%` or `;` in the password must be percent-encoded in the DSN. "Too many attempts" instead means five wrong passwords this minute; wait for the next one. |
| Login loops in `oauth://` mode | The IdP issues tokens without the expected `aud`: the origin, `<origin>/mcp`, or the `audience=` value. |
| `/app/` login: redirect URI host not allowed | The page is open on a host other than `CONSOLE_URL`'s. Set `CONSOLE_URL` to it, or add it as `redirect=<host>`, and restart. |

## Dashboards at /app/

Wherever the console is served, `/app/` serves the dashboards beside it:
`https://twillingate.example.com/app/`, on the console's own listener with
`CONSOLE_ADDR`, else on the shared one. The root of that listener redirects to
`/app/`; an ingest-only listener answers 404 there. The page is read-only; agents build the
dashboards over MCP ([reporting.md](reporting.md)). It loads without a login
and reads everything through `/api/` with the same login as any other
client:

- **`token://` with a password:** the page shows the login page on its own
  host and returns to `/app/callback` there. On the console's host, `CONSOLE_URL`
  (default `INGEST_URL`), that needs no entry: the login accepts exactly
  `<CONSOLE_URL>/app/callback`, whatever `Host` a proxy passes on. So when the
  console has its own hostname, set `CONSOLE_URL=https://console.example.com` and open
  the dashboards there. Any further name needs `redirect=<host>` in
  `CONSOLE_AUTH_DSN`. The login trusts only configured hosts, never a request
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

Widget queries run under `CONSOLE_QUERY_TIMEOUT` and `CONSOLE_QUERY_ROWS_MAX`, and
their results are cached for `REPORTING_CACHE_MAX_AGE`
([Configure the collector](#configure-the-collector)).

### Shared widgets on a private console

A shared widget is public at `CONSOLE_URL/share/<id>`: a page and two
images that need no token ([reporting.md](reporting.md#sharing-a-widget)).
`CONSOLE_URL` is also the login's origin (its resource, issuer and the
dashboards' callback), so a console on a LAN or tailnet keeps one public
hostname for both: `CONSOLE_URL` names it, the internet reaches only
`/share/*` there, and `/app/`, `/api/`, `/mcp` and the login answer under
the same hostname only to private addresses. Your own machines must then
reach the proxy over the private network: split DNS, or the proxy's
tailnet address for that name. With Caddy in front of a console on
`127.0.0.1:8080`:

```
console.example.com {
	@private remote_ip private_ranges 100.64.0.0/10
	handle /share/* {
		reverse_proxy 127.0.0.1:8080
	}
	handle @private {
		reverse_proxy 127.0.0.1:8080
	}
	handle {
		respond 404
	}
}
```

`private_ranges` covers the loopback and private IPv4 and IPv6 ranges;
`100.64.0.0/10` adds a tailnet. `remote_ip` reads the connection's own
address, so behind a further proxy or CDN use `client_ip` with
`trusted_proxies` instead. A console that is already public needs nothing.

### Previewing dashboard files

`twillingate reporting dev` previews dashboard files against a database on
a loopback address, without a login; its usage is in
[reporting.md](reporting.md#local-development). Its SQL widgets run on the
external SQL engine at `SQL_ENGINE_SOCKET` in its environment, the
server's; without it they are refused, and its other widgets load.

## Operate and recover

### Routine operations

| Task | Command |
| --- | --- |
| Logs | `journalctl -u twillingate -f` |
| Restart | `systemctl restart twillingate` |
| Upgrade (systemd) | `curl -fsSL …/install.sh \| sudo bash` — restarts the running service and reports the old and new version |
| Upgrade (compose) | `docker compose pull && docker compose up -d`. Never `down -v`: the database lives in the named volume. Pin with `TWILLINGATE_VERSION=v0.9.2` in `.env`. |
| Apply migrations only | `twillingate migrate` |
| Upgrade an install still below migration 042 (a release before v0.21.0) | upgrade to v0.21.x first and run `twillingate events import` there, as [UPGRADES.md](https://github.com/dmtrkzntsv/twillingate/blob/main/deploy/UPGRADES.md) says; this release has no import, and its migration 042 refuses a database still holding events that were never imported |
| Upgrade across a schema change | [Back up](#back-up-and-restore) first: migrations 012, 014, 015, 016 and 042 are irreversible. Pre-checks and what changes on the day: <https://github.com/dmtrkzntsv/twillingate/blob/main/deploy/UPGRADES.md> (not served over MCP; open it in the repository) |
| Database size | `du -h /var/lib/twillingate/twillingate.db` |
| Recent config changes | `sqlite3 …/twillingate.db "SELECT * FROM audit_log ORDER BY ts DESC LIMIT 20"` |
| Preview dashboard files against real data | `twillingate reporting dev <dir>... [-db <path>] [-addr 127.0.0.1:3100]` — a local, no-login server over the built UI; `-db` defaults to `DATABASE_DSN`'s path, `-addr` is refused unless it is loopback |

Every CLI command on a systemd host needs the unit's environment:
`sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project list'`.
The daily pass runs at 03:00 UTC: it archives the widget shares and draft
forms past their date, purges what was archived `RETENTION_ARCHIVED_DAYS`
ago, runs an incremental vacuum and records the daily history in
`usage_history` (the database's size and, in the process that keeps the
event files, each project's file bytes, per day, which `usage` reads). The
database holds no events, so nothing is rolled up. The visitor salt
rotates at 00:00 UTC; a catch-up pass at startup means downtime
across those times skips no day. With `LOG_FILE` set, install
`deploy/logrotate/twillingate` into `/etc/logrotate.d/`.

The ingest process also keeps every event as files under `DATA_DIR` (`data/` beside the database unless set): a log per project and day, written as events arrive, turned into a Parquet day file after the day ends and into a month file once the month is final. Events from before the files began were copied in once, on the upgrade to v0.21, by that release's `twillingate events import`. The same pass derives three small tables from them in each project's directory, `sessions/`, `identities.parquet` and `aliases.parquet`, rewritten when the events under them change. A month file is deleted once the month's last day is `RETENTION_DAYS` old. That file work runs in the daily pass, in Go in the ingest process: the scheduled 03:00 UTC pass waits a random delay of up to 30 minutes first, the pass at start does not. Each run logs one line at `info`, `archive: pass` with what it did (`projects`, `dumped`, `merged`, `expired_months`, `derived`, `failed_projects`, `took`); a project whose step failed logs `archive: maintenance` with the error, and the next pass retries it. The files are written `0640`, like the logs. The dashboards (the built-in widgets answered without the engine, a widget's SQL on the external SQL engine), `query`, the analytics tools (`views_overview` through `identities`), the count of events in `limits`, `list_attributes`, `usage` and the console's event names and newest days read them and the tables derived from them.

### Back up and restore

Everything twillingate needs to run is in one SQLite file,
`/var/lib/twillingate/twillingate.db` (in compose, `twillingate.db` in the
`data` volume). SQLite's online backup copies it safely while the service
runs; a plain `cp` of a live database can catch a half-written page and
misses whatever is still in the `-wal` file beside it. Ship the copy off the
host however you ship files, and check it now and then. For a continuous
off-host copy instead, seconds behind, run litestream beside the collector:
<https://github.com/dmtrkzntsv/twillingate/blob/main/docs/litestream.md>
sets it up for systemd and docker compose, with the restore drill and
disaster recovery (not served over MCP; open it in the repository).

Beside the database, `/var/lib/twillingate` also holds `data/`, the event files (or `DATA_DIR` holds them, when set; read `data/` below as that directory). Back them up with the database: copy the SQLite file first (`.backup`, as below), then `data/` with any tool (`cp -a`, `rsync`, `restic`). The derived files (`sessions/`, `identities.parquet`, `aliases.parquet` in each project's directory) may be left out: the next pass rebuilds what is missing. The daily pass rewrites `data/` (it renames closed logs, writes day files, merges a month's day files into the month file and deletes them), and a tool that lists the files before copying them can miss a month while it runs. Copy `data/` outside the pass: not between 03:00 and 03:45 UTC, and not right after a start. Or run the copy again until it changes nothing. Between passes nothing changes there but the logs, which only grow, so a copy taken while the service runs is correct; a log's last line may be cut, and the next start drops it. Restore both before starting the service, and restore them from the same backup: the daily pass deletes the `data/` directory of any project the database does not know, so a database older than a project's creation would delete that project's files.

```bash
sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db \
  ".backup '/var/lib/twillingate/backup.db'"
sqlite3 /var/lib/twillingate/backup.db 'PRAGMA quick_check;'         # expect: ok
sqlite3 /var/lib/twillingate/backup.db "SELECT COUNT(*) FROM projects;"   # expect: your project count
```

List each project's newest dated file (a day file, or a month file once the month is final); it should be today's or yesterday's. The files are `0640`, so run it as the service user:

```bash
sudo -u twillingate sh -c 'for d in /var/lib/twillingate/data/*/; do echo "$d $(ls "$d" | grep -E "^[0-9]{4}-[0-9]{2}(-[0-9]{2})?\." | sort | tail -1)"; done'
# compose: the image has no shell, so list them from a throwaway container
docker run --rm --volumes-from "$(docker compose ps -q twillingate)" alpine:3.20 sh -c 'for d in /var/lib/twillingate/data/*/; do echo "$d $(ls "$d" | grep -E "^[0-9]{4}-[0-9]{2}(-[0-9]{2})?\." | sort | tail -1)"; done'
```

The image carries no `sqlite3`: under compose, `docker compose stop
twillingate`, copy `twillingate.db` out of the volume, and start it again.

To restore, on the old host or a fresh one after [installing](#install):
stop the service, put the copy at `/var/lib/twillingate/twillingate.db`
owned by the service user, delete any `twillingate.db-wal` and
`twillingate.db-shm` beside it, and start the service. A copy from an older
release migrates on start.

A database from before migration 042 (a Litestream restore of an old
backup, say) still holds events, which this release cannot import: restore
it with v0.21.x, run `twillingate events import -force` there as the service
user before starting the service, then upgrade. Started on this release, a
database whose events were never imported is refused by migration 042 and
left as it was; one imported before the backup was taken migrates, and the
events it holds that the restored `data/` lacks are lost. A database past
042 holds no events.

Any `sqlite3` that opens the file (the backup and check above, a host cron,
litestream's restore drill) must be SQLite 3.35+ built with math functions:
`sqlite3 :memory: 'select log(10)'` answers `1.0`. The `bucket` column of
`events` calls `log()` and `ceil()`, and without them even
`PRAGMA quick_check` fails with `unknown function: ceil()`. Debian 12's and
Alpine's packages have them, as does the `litestream/litestream:0.5` image.

Two things do not survive: the visitor salt (rotated daily anyway, so at
most a day of continuity) and whatever arrived after the copy was taken,
including events buffered in memory, bounded by `BUFFER_FLUSH_INTERVAL`.
