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

It must be super simple: switch a site on, done. Optionally a text the page
must contain.

## Goals

- One HTTP availability monitor per origin of a project, each checked every
  fixed interval, set per install.
- Every check result recorded in the project's event files, in a new `checks`
  family that the traffic counts leave out as a whole and `usage` reports
  separately; a hosted plan may count them at a discount.
- A failure reason from a closed vocabulary on every failed check, so a pie
  chart of reasons is a plain breakdown.
- A daily HTTPS certificate check per origin and a daily domain expiry check per
  main domain, keeping the current dates in SQLite.
- A system dashboard showing all of it.

## Non-goals

- Notifications of any kind (email, webhook, push). Not yet.
- Custom URLs, paths, ports, methods, headers, or a domain that is not one of
  the project's origins.
- Other monitor kinds (TCP, ping, DNS, keyword-absent, JSON value, …).
- A user-chosen interval, retries, a "pending" state, or incident tracking.
  Outages are worked out when reading, not stored as state.
- Showing a history of certificate and domain checks (the daily rows exist, but
  nothing charts them).

## Anti-goals

- A settings form with more than: on/off per origin, optional text. If it grows
  knobs, it has failed even if it works.
- Check rows counting in the traffic counts of `usage` or `limits`, or showing up
  as product events or measures anywhere.
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

- A project has at most one monitor per origin, any number in all: `enabled` and
  an optional `contains` text.
- An origin qualifies if it is one of the project's `allowed_origins`, exact
  `http://` or `https://`: no wildcards, no bare `*`, no `tauri://`, `app://` or
  `null`. A non-default port is allowed as listed. An `http://` origin is checked
  over plain HTTP, so it gets no certificate check (the domain check still runs).
  (My calls.)
- Exposed in the web UI as a **Monitoring** section in project settings, next to
  origins: one row per eligible origin with a switch that saves at once, and per
  row an optional "page must contain" field with its own Save. Also over MCP and
  the CLI, as every project field is.
- Removing an origin deletes its monitor in the same transaction (audited
  `monitor.remove`), and the domain reading no monitor left uses.
- The URL checked is the origin itself (`https://host/`), nothing else.

### The availability check

`MONITOR_INTERVAL` is the time between the checks of **each** enabled monitor,
independently: a monitor's slot is at `hash(project_id, origin) % interval`, so
checks spread across the interval, and a project's rows grow with its monitors. A
monitor whose last check is still running skips its slot; the pool is 16 workers
across the install, and a slot that finds none free is skipped, never queued. Each
check:

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
| attribute `origin` | the origin checked |
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
site's own traffic, and reported separately, as `checks` in `usage`, which a hosted
plan may count at a discount: `usage`'s traffic counts (samples and
`last_received_at`, which would otherwise keep a silent site looking alive),
`limits`' `events_30d`, the `measures` and `product_events` tools,
`list_attributes`, and the console's event-name lists. Disk size per project
includes them; that is fine (every monitor is checked every interval: at
30 s, about 86,400 small rows a month per monitor; 21,600 at 120 s).

### Daily certificate and domain checks

On automatically whenever a monitor is enabled. They run in their own small
bounded pool (4 workers, across projects), not inside the availability slot, so
however many origins there are they neither delay an availability check nor hold a
monitor's slot.

- **Certificate, once a day per origin**, and right after its monitor is enabled:
  TLS handshake to the origin host; store the leaf certificate's `not_after`,
  issuer common name, whether it verified, and the check time.
- **Domain, once a day per main domain per project** (`shop.example.com` and
  `example.com` share one lookup and one date), and as soon as an enabled monitor's
  main domain has no fresh reading; enabling a monitor clears no other's. Take the
  registrable domain of the origin host (public suffix
  list: `shop.example.co.uk` → `example.co.uk`), look up the RDAP server for
  its TLD via the IANA bootstrap (`https://data.iana.org/rdap/dns.json`, cached
  for a day), query `/domain/<name>`, store the `expiration` event date and the
  check time. A TLD without RDAP, or a reply without an expiration, stores
  "unknown"; that is not a failure.
- The current state is kept in SQLite (migration 044) for the settings section
  and the API: the certificate on the monitor's row (`monitors`, keyed by project
  and origin), the domain in `monitor_domains`, keyed by project and domain.
- **Amended during planning (my call):** each daily check also writes one
  `checks` row, `cert` or `domain`, value = whole days left (absent when
  unknown), attributes `expires` (YYYY-MM-DD), `origin` on `cert` and `domain` on
  `domain`, and for `cert` `issuer`, `valid` (`"1"`/`"0"`) and `error`. It is a few
  rows a day, and it lets the dashboard read them with a structured
  query's `latest` instead of a new widget source over SQLite.

### Where it shows

- A new system dashboard **Availability**, built like Web Vitals
  (`internal/reporting/system/<name>/`, `project_tab: true`), default range 7d:
  - stats: uptime % for the range, average response time, over all monitors
  - a table of sites: origin, uptime %, average response
  - response time by day (line), the overall average
  - pie: failed checks by `reason`
  - tables of certificates (origin, days left) and domains (main domain, days to
    renewal), each by name, `latest` of `value`
  - table: recent failed checks (origin, day, reason, status, last failure, count)
  Uptime % is a `ratio` of `count` where `attr_up` is 1 over `count`. When no
  monitor is on, the dashboard says so and links to the Monitoring settings.
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

- **No shared project budget; cost shown; a pause switch (user's decision, after
  the above; supersedes every per-project budget in this document).** Every
  enabled monitor is checked every `MONITOR_INTERVAL`, independently, at
  `hash(project_id, origin) % interval`. The pool of 16 workers stays; a slot that
  finds none free is skipped. `check_every_seconds` is gone from `list_monitors`
  and the CLI (`interval_seconds` is the answer), and Settings reads `each site is
  checked every <interval>`. A proposed `MONITOR_CHECKS_PER_EVENT` with
  `events_per_month` in `list_monitors` and an `Each site uses about N events a
  month` line in Settings was dropped. `usage` reports raw `checks`. New `MONITOR_DISABLED`
  (bool, default false): the runner still runs but only ends pending manual checks
  as "the check did not run" (in the runner, which already owns that finish), runs
  nothing and writes no row; the API keeps working for edits, `check_monitor` is
  refused with `ErrInvalid` ("monitoring is paused on this install"), and
  `list_monitors` adds `paused: true`, which Settings shows as `Monitoring is paused
  on this install` with Check now disabled.
- The external SQL engine is a separate repository (`twillingate-sql`). This
  change makes `checks` a table of structured queries (native) only; the SQL
  engine's `checks` view, and leaving `checks` rows out of its `events` view,
  are a follow-up there. Until then `schemaFiles` does not list `checks`.
  Done: the engine's 2026.11.01-2 has the table and `schemaFiles` lists it.
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
- **Several monitors per project (user's decision, after planning; replaces one
  per project throughout this document).** Unlimited monitors, one per origin.
  `MONITOR_INTERVAL` was first a per-project budget shared in turn (superseded by
  the next amendment: every monitor is checked every interval). The certificate is read
  once a day per origin, the domain once a day per project and main domain. There
  is no more picking an origin, no "disabled with origin kept": an origin leaving
  `allowed_origins` deletes its row. Migration 044 amended (unreleased): `monitors`
  is keyed `(project_id, origin)` without the domain columns, and a new
  `monitor_domains` is keyed `(project_id, domain)`. A monitor's manual check runs
  now, on its own cooldown, for that origin's availability, certificate and main
  domain; it does not move any slot, and a slot skips a monitor whose manual
  check is running. Rows gain attribute `origin` (`uptime`, `cert`) or `domain` (`domain`).
  `get_monitor` became `list_monitors`, `GET /api/projects/{project_id}/monitors`,
  and `update_monitor` and `check_monitor` take `origin`; `monitor get` became
  `monitor list`. The earlier notes above about an origin change, the `monitor`
  routes and `get_monitor` describe the one-monitor shape and are superseded by
  this one.

  The dashboard replaced its certificate and domain tiles with Sites, Certificates
  and Domains tables, three rather than one since each has its own rows and a table
  holding two would repeat them. Not expressible in a structured query, so not
  built: a line per site (a trend has no `other`, and a limit alone would hide
  sites silently, so it stays the overall average); soonest expiry first (`order` is
  descending by a metric or ascending by a dimension, so the tables are by name and
  a viewer sorts by clicking a header). A breakdown takes 3 dimensions, so Recent
  failures groups by origin, day and reason and reads the status as `latest`.
- **The latest result of any check (user's decision, after Settings showed a
  result only for manual checks; supersedes the result columns in Check now
  above).** Migration 044 amended (unreleased): `check_up`, `check_status`,
  `check_reason`, `check_error` and `check_ms` become `last_checked_at`, `last_up`,
  `last_status`, `last_reason`, `last_error` and `last_ms`, written unaudited by the
  runner after every availability check, scheduled or manual, one small write per
  check, and only to a row still there, so a check that raced its origin's removal
  brings nothing back. `check_requested_at` and `check_finished_at` stay for the
  pending state: `FinishMonitorCheck` keeps its request guard and only ends it, so
  "the check did not run" is no longer a stored error but `last.checked_at` before
  `check.requested_at`. The API gains `last: {checked_at, up, status, reason, error,
  ms}`, and `check` shrinks to `{requested_at, finished_at}`; `monitor list` prints
  `last` and `monitor check` prints it. Settings shows it on every on row, as a
  status line under the origin and its `must contain` text.
