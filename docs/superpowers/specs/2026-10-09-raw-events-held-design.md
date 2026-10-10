# Raw events held and an ingest switch

Status: proposed
Date: 2026-10-09 (replaces the monthly events cap of 2026-10-08)

## Problem

- **Nobody can read how many raw events a server holds.** That one number
  drives what a server costs: the disk the raw rows take and the time every
  dashboard spends scanning the raw window. `usage` answers per project and
  per day, from raw rows and the daily pass's stored counts; nothing
  answers the total stored raw right now, across every project and family.
- **Whoever runs a server needs it.** A hosting operator who runs one
  twillingate per customer reads it to see which servers grow; a
  self-hoster reads it to size the box. Both read it from outside, through
  the console API, without SQL.

- **Nobody can stop a server taking data without stopping its console.**
  An operator pausing ingest for a migration, a restore or an abuse
  incident, or a hosting portal putting a customer's server read-only, has
  only the proxy. Doing it there duplicates twillingate's ingest routes
  (three, one of them legacy) and its CORS answers, gets the status wrong
  easily (a 5xx makes the SDK queue every batch in each visitor's
  localStorage), and hides the reason from the console: the operator's own
  Projects page says nothing while every event is refused.

A count per calendar month (the first version of this design) answered
the first two problems poorly: it needed a table of its own, kept apart
from the data to survive rollup, and measured what arrived rather than
what is held.

Quotas and refusing ingest for volume are out of scope. twillingate adds no
quota and never decides by itself to refuse events: a quota to show or a
ceiling to enforce is for whoever runs the server, reading `limits` from
outside, and the ingest switch (D5) is what they turn when they decide.

## Decisions

- **D1. `limits` reports the raw events held.** Its output gains
  `raw_events: {held, window_days}`: `held` is the rows of the raw events
  table stored now, every family (views, product events, measures) and
  every project; `window_days` is `RETENTION_EVENTS_RAW_DAYS`, so a reader
  can say "in the last D days". MCP's `limits` and REST's `GET /api/limits`
  answer it for anyone with console access. It sits beside the limits it is
  read against (retention), not in a tool of its own.
- **D2. The count is derived, counted at most every 5 minutes.** No
  migration, no setting, nothing on the ingest surface. The count is a
  `COUNT(*)` of `v_events_flat`, the view of every raw row (raw rows are
  read only through the family views and `v_events_flat`, never the
  `events` table; SQLite flattens the view to one scan of the table),
  through the console's own read handle, so at most one pipeline flush
  behind ingest. The console host caches it for 5 minutes: concurrent reads
  share one count in flight, a read past the 5 minutes answers the previous
  count at once while a refresh runs in the background, and the console
  starts the first count when it boots, so a read that arrives before that
  first count ends waits for it. A count older than 10 minutes (nobody read
  for a while) is not answered: that read waits for the refresh too, so a
  reader polling rarely never gets an hours-old number. The nightly rollup
  shows within 10 minutes of the pass.
- **D3. A count that cannot be read costs only itself.** `raw_events` is
  then left out; the failure is logged at error once and kept for the same
  5 minutes, so a count that times out logs once per window, not per read.
  The limits still answer.
- **D4. The console shows it as one line.** The Projects page's limits
  panel says "Raw events held: N (the last D days)" under its heading
  ("the last day" for 1, "today" for 0: `RETENTION_EVENTS_RAW_DAYS=0` keeps
  only today's rows raw, the daily pass rolling up every earlier day and
  ingest clamping older timestamps to the time received), and nothing when
  the count is absent.

- **D5. `INGEST_DISABLED` stops ingest completely.** A boolean (Go's
  spellings: `true`/`false`, `1`/`0`, `t`/`f`; anything else refuses the
  boot rather than guessing), default off. When on, every route that takes
  data refuses: `POST /ingest/events`, the legacy `POST /api/events` and
  `POST /ingest/forms/{name}` (the Plausible shim posts to
  `/ingest/events`; it has no route of its own). The answer is **429**,
  the body `ingest is disabled` and `Retry-After: 3600`: a 4xx on purpose,
  since the SDK drops a batch refused with a 4xx and queues one refused
  with a 5xx in localStorage on every visitor's device. The refusal comes
  first, before the body is read or the key looked up: it is cheap, writes
  nothing, and a disabled server has nothing to protect a key check for (a
  bad key gets the 429 too). With no key there is no project to check the
  `Origin` against, so CORS is answered as for a preflight: an `Origin`
  any project allows gets `Access-Control-Allow-Origin`, so the browser can
  read the 429. A form post gets the plain 429, not the error redirect,
  for the same reason. Preflights, the SDK scripts and `/healthz` answer as
  usual. The boot logs a warning. `limits` reports it as
  `ingest_disabled: true|false`, a field of its own rather than a row of
  `limits`: it is a switch with no value, unit or default to read against,
  and the `ingest` group is the wire format's fixed limits, the same on
  every server. The console's Projects page says, at the top: "Ingest is
  disabled on this server: new events and form submissions are refused."

## Cost

A count of every raw row reads every page of the clustered `events` table
(`WITHOUT ROWID`, no secondary index since 023). Measured on a synthetic
table of 5 million rows (1.7 GB; six projects, three families, 30 days)
through the console's read handle (modernc), warm cache, on a shared
machine (load average about 15, so the figures are noisy): `COUNT(*)` of
`v_events_flat` 2.0 to 2.6 s, the same as a bare `COUNT(*)` of `events`
(2.2 to 3.0 s in the same run); summing `raw_views`, `raw_product` and
`raw_measures` 3.3 to 4.0 s. An earlier, quieter run timed the bare count
at 1.7 to 2.1 s, and 1.1 to 1.5 s with the sqlite3 CLI. A covering index
on `day` brings the CLI count to 0.1 s but adds 330 MB (about 20 %) to the
file and a write to every insert. With the count cached for 5 minutes
(D2) the scan runs at most 12 times an hour, on a read connection that
does not block the writer (WAL), and a read waits for it only on the
first count or after 10 minutes without a read; no index is added. By extrapolation (not measured), the count reaches
`CONSOLE_QUERY_TIMEOUT` (10 s by default) somewhere past 20 to 25 million
rows; `raw_events` is then left out and the timeout logged once per 5
minutes (D3).

## Changes

- `internal/api/ops_limits.go`: `raw_events` (D1); `internal/api/rawcount.go`:
  the cached count (D2, D3).
- `web/`: the line in the limits panel (D4) and the ingest banner (D5).
- `internal/config`: `INGEST_DISABLED`; `internal/server`: the refusal on
  every ingest route; `internal/api`: `ingest_disabled` (D5).

Docs in the same commit: `docs/twillingate.md` (the `limits` fields, the
429 for events and forms) and `docs/deployment.md` (`INGEST_DISABLED`). No
`deploy/UPGRADES.md` entry: there is no migration, and the one setting is
off by default.

## Testing

- `limits`: `raw_events.held` matches the stored rows across families and
  moves with a write once the cached count is past its 5 minutes, through
  MCP too; `window_days` is the raw window; without a readable count it is
  left out, the error logged once over two calls, the limits still
  answered; the count reads `v_events_flat`, not `events`.
- The cache: two reads within the TTL run one count, concurrent first
  reads share one, a stale count answers while a refresh runs, one older
  than twice the TTL waits for the refresh, a failure is reported once per
  TTL, a reader that gives up does not cancel the count.
- The console: the line with the count and window ("today" for 0), none
  without a count.
- The switch: each ingest route refused with 429, the body, `Retry-After`
  and CORS for an allowed `Origin` (none for another), the body never read,
  nothing enqueued or written, a bad key refused the same; preflights, the
  scripts and `/healthz` unchanged; off (unset, `false`, `0`) unchanged;
  config's spellings and refusal; `limits` and `GET /api/limits` report
  it; the console banner shows only when it is on.
