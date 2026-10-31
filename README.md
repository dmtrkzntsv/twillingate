<p align="center">
  <img src="docs/logo.svg" width="140" alt="An iceberg, most of it below the waterline">
</p>

<h1 align="center">twillingate</h1>

<p align="center"><em>Named after <a href="https://en.wikipedia.org/wiki/Twillingate">Twillingate</a>,
the Canadian town where tourists come to watch icebergs —
this one surfaces the insights beneath your data.</em></p>

Web, app and product analytics as one Go binary and one SQLite file —
cookieless, and the served SDK sends nothing identifying by default
([details](#privacy-and-gdpr)). It holds
about 15 MB of memory, needs no database server and no cluster, and is happy on
a Raspberry Pi from day one. An MCP endpoint — with the same operations also
callable as a plain REST API — means your coding agent or a script can set it
up and answer questions about it, so the dashboards are there when you want
them rather than being the point.

**Ask your agent to integrate it.** With MCP enabled, "set up analytics for
this app" is the whole task: the agent creates the project, issues an ingest
key and asks `integration_guide` for paste-ready setup for your platform, then
wires it into your code itself.

**Then ask it for the numbers.** "How many visitors did myapp get last week, by
country" beats clicking through a dashboard, and read tools plus a guarded SQL
`query` tool (SQL needs an external SQL engine, see [deployment](docs/deployment.md#external-sql-engine)) answer it. If you like charts, the same agent builds dashboards,
served at `/app/` on the console beside MCP and REST.

## Run it

One file: ingestion, the tracker script and — once `CONSOLE_AUTH_DSN` is set,
see [The console](docs/deployment.md#the-console) — the console (MCP,
REST and the dashboards at `/app/`), all on `:8080`:

```bash
mkdir twillingate && cd twillingate
base=https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/compose
curl -fsSLO $base/docker-compose.yml
docker compose up -d
docker compose exec twillingate twillingate project create -alias myapp
docker compose exec twillingate twillingate key issue -project myapp -label web
```

That prints a snippet to paste; an agent with MCP access can do the same two
steps for you. Put Caddy, nginx or a Cloudflare tunnel in front of `:8080` for
TLS.

Back up the one SQLite file in its `data` volume, or stream it to object
storage with [litestream](docs/litestream.md).
[docs/deployment.md](docs/deployment.md) is the runbook.

## Track something

`twillingate key issue` prints this ready to paste:

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_9f3c…"></script>
```

Pageviews are automatic, SPAs included. The same file is a full SDK for
web, product and app analytics from code:

```js
twillingate.track("signup", { plan: "pro" });   // product event
twillingate.screen("/settings");                // app screen view
twillingate.identify("user-123", "Ada");        // on an identified tag
twillingate.group("org-9", "Acme Corp");
twillingate.reset();                            // on logout
```

Native apps and backends skip the SDK and POST batches straight to
`/ingest/events`. Details: [Instrument a website](docs/twillingate.md#instrument-a-website)
and [The wire format](docs/twillingate.md#the-wire-format).

## Documentation

Two pages. [docs/twillingate.md](docs/twillingate.md) is everything needed
to set up a project, get it tracking, and answer questions from the data —
and is what the MCP endpoint serves to agents as `docs://twillingate`, so
the text you read and the text they read are the same bytes.
[docs/deployment.md](docs/deployment.md) is everything needed to run
twillingate on your own server: installing it, configuring the collector,
the console and its dashboards, and backups. It is served as `docs://deployment`, so an agent can help with an
install too.

| Section | Covers |
| --- | --- |
| [Set up a project](docs/twillingate.md#set-up-a-project) | Projects, allowed origins, retention, ingest keys |
| [Instrument a website](docs/twillingate.md#instrument-a-website) | twillingate.js: snippet and code modes, masking, routing, consent and storage |
| [The event model](docs/twillingate.md#the-event-model) | The three event families, for native apps and backends too |
| [The wire format](docs/twillingate.md#the-wire-format) | The normative contract for `/ingest/events` |
| [Answer questions with the data](docs/twillingate.md#answer-questions-with-the-data) | The MCP tools, the HTTP API, the views, and the caveats needed to write correct SQL |
| [Install](docs/deployment.md#install) | systemd (`install.sh`; with ship, see [Deploy with ship](#deploy-with-ship)) and docker compose, verifying ingestion |
| [Configure the collector](docs/deployment.md#configure-the-collector) | Every environment variable, low-resource tuning |
| [The console](docs/deployment.md#the-console) | The browser login, and pointing claude.ai, Desktop or Claude Code at it |
| [Dashboards at /app/](docs/deployment.md#dashboards-at-app) | Where the dashboards are served and how they log in |
| [Operate and recover](docs/deployment.md#operate-and-recover) | Upgrades, backup and restore, schema upgrades in deploy/UPGRADES.md |
| [docs/litestream.md](docs/litestream.md) | Continuous backups with litestream under systemd or docker compose, drills, disaster recovery |
| [docs/plausible/](docs/plausible/) | The Plausible class-tagging shim |

## Deploy with ship

Each release is also published, as plain binaries with a unit template, to
the R2 bucket [ship](https://github.com/dmtrkzntsv/ship) installs from.
The bucket is the maintainer's and not public: elsewhere use `install.sh`
or docker compose ([Install](docs/deployment.md#install)). The private SQL
engine, [twillingate-sql](https://github.com/dmtrkzntsv/twillingate-sql),
is published the same way and installed into the same service.

### Publish

```sh
make ship-dist && make ship-publish VERSION=v1.2.3 CHANNELS=latest
```

`ship-dist` stages `dist/ship/`: both static binaries and
[deploy/ship/](deploy/ship/)'s `twillingate.service` (the unit template)
and `twillingate.env.example`. `ship-publish` uploads it, with
`--version` for a `VERSION` other than `dev` and `--channels` for
`CHANNELS`, one of them required; it needs ship's R2 write credentials.
The release workflow does both after the GitHub release.

### Install

On a host with ship and its read-only R2 key:

```sh
sudo ship install twillingate --version latest
sudo ship install twillingate-sql --service twillingate --version latest
sudo vi /etc/twillingate/twillingate.env        # INGEST_URL, GEO_DSN, CONSOLE_AUTH_DSN …
sudo ship run twillingate -- project create -name myapp
sudo ship run twillingate -- key issue -project-id 1 -label web
sudo ship start twillingate                     # first: it creates /var/lib/twillingate/data
sudo ship start twillingate-sql --service twillingate
```

Both run as user `twillingate` in service `twillingate` (a
`--service twillingate-beta` beside it gets its own everything). The unit
runs in the data dir `/var/lib/twillingate`, which holds the env example's
`DATABASE_DSN=sqlite://twillingate.db` and, at the default `DATA_DIR`, the
event files twillingate-sql reads (leave `DATA_DIR` unset). It sets
`SQL_ENGINE_SOCKET=/run/twillingate/sql-engine.sock`, where twillingate-sql
listens; without the engine, set `SQL_ENGINE_SOCKET=` (empty) in the env
file to refuse SQL rather than fail it. ship installs one unit per
project, so the split install (`twillingate@.service`), the litestream
unit and the logrotate file are not covered: set them up by hand if you
need them, with `/var/lib/twillingate` and `/etc/twillingate/twillingate.env`
for the paths in [docs/litestream.md](docs/litestream.md).

### Operate

```sh
sudo ship update twillingate                    # restarts; rolls back by itself if it does not stay up
sudo ship rollback twillingate
sudo ship logs twillingate
sudo ship status
sudo ship run twillingate -- <command>          # the CLI with the env file and user, in the data dir
```

twillingate-sql takes the same commands with `twillingate-sql --service twillingate`.

### Moving an existing host

A host set up by `install.sh` moves over once, keeping its data, with
ship's recipe [Moving a hand-installed service to
ship](https://github.com/dmtrkzntsv/ship#moving-a-hand-installed-service-to-ship)
(service `twillingate`, run once for each project). The values:

| | twillingate | twillingate-sql |
| --- | --- | --- |
| old unit | `twillingate.service` (and any `twillingate@` instance, `litestream.service`) | `twillingate-sql.service` |
| old binary | `/usr/local/bin/twillingate` | `/usr/local/bin/twillingate-sql` |
| old env file | `/etc/twillingate/twillingate.env` | `/etc/twillingate-sql/twillingate-sql.env` |
| user | `twillingate` (kept) | `twillingate-sql` (removed; the engine now runs as `twillingate`) |
| data dir | `/var/lib/twillingate`, reused as it is | none; `/var/cache/twillingate-sql` is removed |
| key files | none | none |

Before starting, delete `SQL_ENGINE_SOCKET` from the restored
`twillingate.env`: the env file overrides the unit, which points it at the
new socket. Don't restore twillingate-sql's old env file, whose
`TWSQL_DATA_DIR` the unit sets; its tuning lines (`TWSQL_MEMORY_LIMIT` …)
can go into `/etc/twillingate/twillingate-sql.env`. Once both run, remove
the old binaries, units, `/etc/twillingate-sql`, `/var/cache/twillingate-sql`
and user and group `twillingate-sql`.

## Development

```bash
make check       # what PR CI runs: vet + coverage gate
make test        # the full suite under -race (slow; the release runs it)
make build       # single static binary, no cgo (needs Go and Node 22: it builds web/ first)
make run         # local server on 127.0.0.1:8080 with a dev project
make smoke       # boot the real binary, POST a batch, verify rows land
make seed-demo   # 180 days of demo traffic in local/twillingate.db
cd sdk && npm ci && npm test   # the twillingate.js SDK suite
```

The SDK bundle is committed (`internal/server/twillingate.js`); after
editing `sdk/src/`, run `npm run build` there and commit the result — CI
fails on drift. Releases are cut by hand from the `release` workflow and tagged with semver;
the project is pre-1.0, so breaking changes bump the minor. Commit messages
follow Conventional Commits and become the release notes.

## Privacy and GDPR

**What the collector stores.** What it is sent. The served SDK's `anonymous`
default sends nothing identifying: no cookies, nothing on the device unless
the tag declares consent (`data-consent`), and then only its failed-batch
retry queue — no consent banner is needed for pageview tracking on its own.
A tag with `data-identity="identified"` sends `$user_id`, `$user_name` and
`$install_id`, and they are stored as given; `$group_id`/`$group_name` are
stored raw in every case (a group is an organization, not a person). A
visitor who sends no id is a hash of the connection under a key that rotates
every 24 hours; the previous key is overwritten, so linking across days is
impossible rather than prohibited. IPs and full User-Agents are never stored
or logged (a test scans the database file and the log output). Query strings
are stripped to a UTM allowlist, referrers reduced to a source name, bots
dropped at ingestion. Paths are stored verbatim — strip personal data from
URL schemes before it reaches the tracker.

**What the SDK keeps on the device.** Nothing without consent. With consent
(`data-consent="true"`, or the name of a global the consent manager
maintains) it persists the retry queue and, on an identified tag, the
visitor id, user and group in `localStorage` — terminal-equipment storage
under ePrivacy, the same legal category as a cookie. See
[docs/twillingate.md](docs/twillingate.md) "Consent and storage".

**Access and erasure.** Enabling the console exposes every stored id to every
valid token holder, over MCP and the REST routes alike; complete erasure is
`twillingate project delete`, deliberately CLI-only.

## License

twillingate is licensed under the [GNU Affero General Public License v3.0](LICENSE)
(AGPL-3.0-only). The browser SDK in [`sdk/`](sdk/), which ships inside other
people's sites and apps, and the [Plausible shim](docs/plausible/) are
licensed under [MIT](sdk/LICENSE).
