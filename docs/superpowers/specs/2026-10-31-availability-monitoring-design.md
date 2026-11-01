# Availability monitoring — design

Date: 2026-10-31. Status: draft, awaiting approval.

## Intent

twillingate is offered as a hosted service in tiers ($5, $20). A project owner
wants to know three things about their site without another tool:

1. **Is it up?** So that a conversion drop can be lined up against "the site
   was down or slow then" in the same event files, the same queries and the
   same dashboards as the conversions. This is why check results live in the
   event files, not in a separate store.
2. **When does the HTTPS certificate expire?**
3. **When does the domain need renewing?** The main point of this check is that
   the owner never misses a renewal: the stored fact is the renewal (expiry)
   date.

It must be super simple: enable, pick a domain, save, done. Optionally a text
the page must contain.

## Goals

- One HTTP availability monitor per project, on one of the project's existing
  origins, at a fixed interval set per install.
- Every check result recorded in the project's event files, in a new `checks`
  family that the event allowance and traffic counts leave out as a whole.
- A failure reason from a closed vocabulary on every failed check, so a pie
  chart of reasons is a plain breakdown.
- A daily HTTPS certificate check and a daily domain expiry check on the same
  origin, keeping the current dates in SQLite.
- A system dashboard showing all of it.

## Non-goals

- Notifications of any kind (email, webhook, push). Not yet.
- More than one monitor per project.
- Custom URLs, paths, ports, methods, headers, or a domain that is not one of
  the project's origins.
- Other monitor kinds (TCP, ping, DNS, keyword-absent, JSON value, …).
- A user-chosen interval, retries, a "pending" state, or incident tracking.
  Outages are worked out when reading, not stored as state.
- Showing a history of certificate and domain checks (the daily rows exist, but
  nothing charts them).

## Anti-goals

- A settings form with more than: on/off, origin, optional text. If it grows
  knobs, it has failed even if it works.
- Check rows counting toward a customer's event allowance, `usage` or
  `limits`, or showing up as product events or measures anywhere.
- The checker becoming a way to make the hosted machine request internal
  addresses (SSRF).
- Following a redirect to a host outside the project's origins.

## Constraints

- The interval is a **fixed** value from the environment, not a floor:
  `MONITOR_INTERVAL` (seconds; my call on the name, default 120). The $5 tier
  sets 120, the $20 tier 30. Documented in `docs/deployment.md`, validated in
  `internal/config` (my call: refuse below 10 and above 3600).
- The check timeout is `MONITOR_TIMEOUT` (seconds, default 5, 1 to 60, strictly
  below the interval; refused at startup otherwise).
- One static binary, no cgo, no new third-party dependency without a very
  reputable vendor (CLAUDE.md). `golang.org/x/net/publicsuffix` (Go team) for
  the registrable domain is my call and acceptable under that rule; RDAP is
  plain JSON over HTTPS with the standard library.
- Layering per CLAUDE.md: the checker is new behaviour beside `jobs`, wired by
  `app`; it takes interfaces for what it needs (the event log, the project
  registry, its slice of the store). New packages go into the archtest rank
  table.
- Docs, components and env vars updated in the same commit per the CLAUDE.md
  table; `docs_sync_test` will bind several of them.

## What was decided

### Setup

- A project has at most one monitor: `enabled`, `origin`, and an optional
  `contains` text.
- `origin` is picked from the project's `allowed_origins`, offering only exact
  `http://` or `https://` origins: no wildcards, no bare `*`, no `tauri://`,
  `app://` or `null`. A non-default port is allowed as listed. An `http://`
  origin is checked over plain HTTP, so it gets no certificate check (the
  domain check still runs). (My calls.)
- Exposed in the web UI as a **Monitoring** section in project settings, next to
  origins: switch, origin picker (hidden when the project has one origin, which
  is then used), optional "page must contain" field, Save. Also over MCP and
  the CLI, as every project field is.
- Removing an origin that the monitor uses: my call — the monitor is turned off
  (the change is refused would block origin edits; turning off is quieter and
  visible in the section). The builder may choose refusal instead if the
  registry's patterns make that clearly more consistent; say which in the PR.
- The URL checked is the origin itself (`https://host/`), nothing else.

### The availability check

Every `MONITOR_INTERVAL` seconds, per enabled monitor:

- `GET` the origin. Redirects are followed, but **every hop must be allowed
  by the project's origins**, matched with the existing `manage.matchOrigin`
  (scheme, host, port, wildcard patterns included). A bare `*` does not count,
  as with forms' `return_url`. A redirect anywhere else stops the check
  without following it and is a failure with reason `redirect` and the target
  in `error`, e.g. `redirected off-site to https://www.example.com — add it to
  the project's origins if it is yours`. This is strict on purpose: a project
  listing only `https://example.com` whose site redirects to
  `https://www.example.com` shows as down until it adds the `www` origin
  (my call; the user asked that it never redirect outside the added domains).
  More than 5 hops is also `redirect`.
- Up means: the final status is 2xx and, when `contains` is set, the text
  appears in the first 1 MB of the body (plain substring, case-sensitive; my
  call).
- Timeout: `MONITOR_TIMEOUT` seconds for the whole check, default 5 (user's
  decision, amended; see below).
- No retries. Each check is one row.
- **Private addresses refused**: an origin (or any redirect hop) that resolves
  to loopback, private, link-local, CGNAT or unspecified addresses fails with
  reason `blocked`, checked at dial time so DNS rebinding cannot get around it.
  This applies to every install, including self-hosted ones; a LAN-only site
  cannot be monitored (my call, stated to the user).

Each check is written to the project's event log as one row of a **new fourth
family, `checks`**, beside `views`, `product` and `measures` (the user's
decision, over storing them as measures and filtering a name, or folding up
checks into hourly rows):

| Field | Value |
| --- | --- |
| family | `checks` |
| name | `uptime` (the only name for now; future monitor kinds get their own) |
| value | response time in ms (time to the end of the body read, or to the failure) |
| attribute `up` | `"1"` or `"0"` |
| attribute `status` | final HTTP status code, absent when there was no response |
| attribute `reason` | absent when up; otherwise one of the table below |
| attribute `error` | short human text when down (e.g. `redirected off-site to evil.example`) |
| attribute `redirects` | redirect hops followed, `"0"` when none; on a failed check, the count reached |

The details are ordinary attributes on the row, so the files' column shape
does not change (my call). The family must be added wherever the family list
lives: `store.Family`, the event files and their table spec for the SQL engine
(a `checks` table, as `views`/`product`/`measures` are), native queries,
`schemaFiles` in `internal/api/resources.go`, and `docs/twillingate.md`'s
families table. Ingest refuses `family: "checks"`: only the server writes it.
Event and attribute rules (rename, merge, block) do not apply to it (my call).

`reason` vocabulary (closed; documented in `docs/twillingate.md`):

| `reason` | When |
| --- | --- |
| `dns` | the name does not resolve |
| `connect` | connection refused or unreachable |
| `timeout` | no complete answer within the timeout |
| `tls` | certificate invalid, expired, or for another name |
| `http_4xx` | final status 400–499 |
| `http_5xx` | final status 500–599 (and any other non-2xx, my call: 1xx/3xx-without-location count here) |
| `redirect` | redirect off the project's origins, or more than 5 hops |
| `content` | `contains` text missing |
| `blocked` | resolves to a private address |

The `checks` family is left out **as a whole** of everything that counts the
site's own traffic and of any event allowance: `usage` (samples and
`last_received_at`, which would otherwise keep a silent site looking alive),
`limits`' `events_30d`, the `measures` and `product_events` tools,
`list_attributes`, and the console's event-name lists. Disk size per project
includes them; that is fine (at 30 s, about 86,400 small rows a month per
project; 21,600 at 120 s).

### Daily certificate and domain checks

On automatically whenever the monitor is enabled, run once a day (my call: in
the daily pass, or on the monitor's own loop once per 24 h per monitor;
builder's choice) and once right after the monitor is enabled or its origin
changes, so the dates appear immediately.

- **Certificate:** TLS handshake to the origin host; store the leaf
  certificate's `not_after`, issuer common name, whether it verified, and the
  check time.
- **Domain:** take the registrable domain of the origin host (public suffix
  list: `shop.example.co.uk` → `example.co.uk`), look up the RDAP server for
  its TLD via the IANA bootstrap (`https://data.iana.org/rdap/dns.json`, cached
  for a day), query `/domain/<name>`, store the `expiration` event date and the
  check time. A TLD without RDAP, or a reply without an expiration, stores
  "unknown"; that is not a failure.
- The current state is kept in SQLite on the monitor row (a new migration),
  for the settings section and the API.
- **Amended during planning (my call):** each daily check also writes one
  `checks` row, `cert` or `domain`, value = whole days left (absent when
  unknown), attributes `expires` (YYYY-MM-DD), and for `cert` `issuer`,
  `valid` (`"1"`/`"0"`) and `error`. It is two rows a day, free of quota, and it
  lets the dashboard tiles read them with a structured query's `latest`
  instead of a new widget source over SQLite.

### Where it shows

- A new system dashboard **Availability**, built like Web Vitals
  (`internal/reporting/system/<name>/`, `project_tab: true`), default range 7d:
  - stats: uptime % for the range, average response time, certificate expires
    in N days (with the date), domain renews in N days (with the date, or
    "unknown")
  - response time by hour/day (line), with down checks visible
  - pie: failed checks by `reason`
  - table: recent failed checks (time, status, reason, error)
  The certificate and domain tiles read the latest `cert`/`domain` row
  (`latest` of `value`); uptime % is a `ratio` of `count` where `attr_up` is 1
  over `count`. When the monitor is off,
  the dashboard says so and links to the Monitoring settings.
- Optional, if cheap: a one-line status on the project page
  (`up · cert 34d · domain 120d`).

## Left to the builder

- Package name and placement (e.g. `internal/monitor`), the scheduling loop
  (the jobs ticker is per minute, so the checker needs its own), concurrency
  limits across many projects, and spreading checks across the interval so
  they do not all fire at once.
- The exact SQLite schema and migration number, the manage operations and
  audit entries, MCP tool and REST route names (follow `update_project`
  patterns; a `monitor` field on the project or its own tools).
- The User-Agent string (identify as twillingate's monitor; must not be
  filtered as a crawler from our own data if that matters, and must not be
  counted as a view — it never goes through ingest, so it should not).
- How the system widgets read SQLite state.
- Tests: an `httptest` server for each `reason`, redirect-off-site, private
  address refusal (with a resolver seam), RDAP with a fake server, and the
  system dashboard in `TestSystemDashboards`.

## Amended during planning

- The external SQL engine is a separate repository (`twillingate-sql`). This
  change makes `checks` a table of structured queries (native) only; the SQL
  engine's `checks` view, and leaving `checks` rows out of its `events` view,
  are a follow-up there. Until then `schemaFiles` does not list `checks`.
- The `events` table of structured queries leaves the `checks` family out (it
  is the site's traffic); `checks` is read through its own table. This makes
  `usage`, `limits` and every `events` widget exclude checks without
  per-reader filters.
- **Redirect hops (user's decision, after planning):** every `uptime` row carries
  `redirects`, the hops followed as a string, failed checks included. The hard
  limit drops from 10 to 5 (a 6th hop is `reason=redirect`). A chain of 3 or
  more is a warning only: the check stays up, and Settings → Monitoring says
  "Redirects N times before answering, which slows every visit"; no new
  up/down state or reason. Settings reads the count from a `redirects` column
  on `monitors` (migration 044 amended, unreleased), written by the runner only
  when it changed and only from a check that got an answer; an origin change
  resets it. It is exposed as `redirects` in `get_monitor` and printed by
  `monitor get` when above 0.
- **Check timeout (user's decision, after planning):** the timeout defaults to
  5 s and is set per install by `MONITOR_TIMEOUT` (whole seconds, 1 to 60,
  strictly below `MONITOR_INTERVAL`, refused at startup otherwise). This
  replaces "10 s, or less when the interval is shorter". It bounds the
  availability check, the per-address dial share, the guard's own dial budget
  and the daily certificate read; the RDAP lookup keeps its own 15 s, as it
  talks to a registry, not the site. `get_monitor` returns `timeout_seconds`
  and `monitor get` prints it.
- **Tiles show the newest reading (user's decision, after planning):** the
  certificate and domain tiles were the day's `min(value)` with stat aggregate
  `last`, which showed an older figure after a same-day re-check, an origin
  change or a move to an `http://` or unknown-expiry origin. They are now a
  `stat` of `latest` of `value`. The native `latest` metric reads `value`, and
  for that column alone a newest row with no value reads as empty instead of
  falling back to an older row. Every reading writes a row: an `http://` origin
  a `cert` row with no value and no attributes, an unknown domain a `domain` row
  with no value as before. A certificate read that failed still writes no row
  and keeps the last reading.
- **Check now (user's decision, after planning):** a manual check of all three,
  from `check_monitor` (`POST /api/projects/{project_id}/monitor/check`),
  `twillingate monitor check` and a Check now button in Settings. The API and
  the checker can be separate processes, so it is a request on the monitor row
  (migration 044 amended, unreleased: `check_requested_at`, `check_finished_at`,
  and the result's `check_up`, `check_status`, `check_reason`, `check_error`,
  `check_ms`), audited as `monitor.check` and served by the runner's next tick
  outside the slot, whatever the daily readings' age and retry wait, on the same
  worker pool and timeout. Rows are written as usual and a failed certificate or
  RDAP read keeps the last date. A monitor that is not on is refused (400); so is
  a request while one is pending or within 60 s of the last (429), for everyone,
  to protect the site and the registries. The result always lands: a request
  older than 2 minutes, or one cut short by shutdown, finishes with an empty
  reason and error "the check did not run". An origin change clears all of it.
  `get_monitor` returns it as `check`; pending while `finished_at` is before
  `requested_at`. The page polls every 2 s and stops waiting after 45 s.
