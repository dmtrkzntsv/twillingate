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
`query` tool answer it. If you like charts, the same agent builds dashboards,
served at `/app/` beside the API.

## Run it

One file: ingestion, the tracker script and — once `API_AUTH_DSN` is set,
see [The API endpoint](docs/deployment.md#the-api-endpoint) — the API (MCP
and REST) and the dashboards at `/app/`, all on `:8080`:

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

For backups the file ships a litestream service, commented out, that streams
the database to object storage. [docs/deployment.md](docs/deployment.md) is
the runbook.

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
the API endpoint and the dashboards, and litestream backups. It is served as `docs://deployment`, so an agent can help with an
install too.

| Section | Covers |
| --- | --- |
| [Set up a project](docs/twillingate.md#set-up-a-project) | Projects, allowed origins, retention, attribute breakdowns, ingest keys |
| [Instrument a website](docs/twillingate.md#instrument-a-website) | twillingate.js: snippet and code modes, masking, routing, consent and storage |
| [The event model](docs/twillingate.md#the-event-model) | The three event families, for native apps and backends too |
| [The wire format](docs/twillingate.md#the-wire-format) | The normative contract for `/ingest/events` |
| [Answer questions with the data](docs/twillingate.md#answer-questions-with-the-data) | The MCP tools, the HTTP API, the views, and the caveats needed to write correct SQL |
| [Install](docs/deployment.md#install) | systemd and docker compose, verifying ingestion |
| [Configure the collector](docs/deployment.md#configure-the-collector) | Every environment variable, low-resource tuning |
| [The API endpoint](docs/deployment.md#the-api-endpoint) | The browser login, and pointing claude.ai, Desktop or Claude Code at it |
| [Dashboards at /app/](docs/deployment.md#dashboards-at-app) | Where the dashboards are served and how they log in |
| [Operate and recover](docs/deployment.md#operate-and-recover) | Upgrades, litestream backups, backup drills, disaster recovery, schema upgrades in deploy/UPGRADES.md |
| [docs/plausible/](docs/plausible/) | The Plausible class-tagging shim |

## Development

```bash
make check       # what PR CI runs: vet + coverage gate + restore test
make test        # the full suite under -race (slow; the release runs it)
make build       # single binary (needs Go and Node 22: it builds web/ first)
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

**Access and erasure.** Enabling the API exposes every stored id to every
valid token holder, over MCP and the REST routes alike; complete erasure is
`twillingate project delete`, deliberately CLI-only.

## License

twillingate is licensed under the [GNU Affero General Public License v3.0](LICENSE)
(AGPL-3.0-only). The browser SDK in [`sdk/`](sdk/), which ships inside other
people's sites and apps, and the [Plausible shim](docs/plausible/) are
licensed under [MIT](sdk/LICENSE).
