# Raw events held

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

A count per calendar month (the first version of this design) answered
neither: it needed a table of its own, kept apart from the data to survive
rollup, and measured what arrived rather than what is held.

Quotas and refusing ingest are out of scope. twillingate adds no setting
and never refuses events for volume: a quota to show or a ceiling to
enforce is for whoever runs the server, outside it (a portal reading
`limits`, a proxy in front of the ingest surface).

## Decisions

- **D1. `limits` reports the raw events held.** Its output gains
  `raw_events: {held, window_days}`: `held` is the rows of the raw events
  table stored now, every family (views, product events, measures) and
  every project; `window_days` is `RETENTION_EVENTS_RAW_DAYS`, so a reader
  can say "in the last D days". MCP's `limits` and REST's `GET /api/limits`
  answer it for anyone with console access. It sits beside the limits it is
  read against (retention), not in a tool of its own.
- **D2. The count is derived, on request.** No migration, no setting and no
  in-memory state: a `COUNT(*)` of the raw events table, taken per request
  through the console's own read handle, so it is at most one pipeline
  flush behind ingest, the nightly rollup shows in the next read, and the
  ingest surface is untouched.
- **D3. A count that cannot be read costs only itself.** `raw_events` is
  then left out and the failure logged at error; the limits still answer.
- **D4. The console shows it as one line.** The Projects page's limits
  panel says "Raw events held: N (the last D days)" under its heading, and
  nothing when the count is absent.

## Cost

`COUNT(*)` on the clustered `events` table (`WITHOUT ROWID`, no secondary
index since 023) reads every page of it. Measured on a synthetic table of
5 million rows (1.7 GB; six projects, three families, 30 days), warm cache:
1.7 to 2.1 s through the console's read handle (modernc), 1.1 to 1.5 s with
the sqlite3 CLI. A covering index on `day` brings the CLI count to 0.1 s but
adds 330 MB (about 20 %) to the file and a write to every insert; for a
count read only when someone opens the Projects page or calls `limits`, on
read connections that do not block the writer (WAL), that is the wrong
trade, so no index is added. Past roughly 25 million rows the count can
outrun `CONSOLE_QUERY_TIMEOUT` (10 s by default); `raw_events` is then left
out and the failure logged (D3).

## Changes

- `internal/api/ops_limits.go`: `raw_events` (D1-D3).
- `web/`: the line in the limits panel (D4).

Docs in the same commit: `docs/twillingate.md` (the `limits` fields). No
`deploy/UPGRADES.md` entry: there is no migration and no setting.

## Testing

- `limits`: `raw_events.held` matches the stored rows across families and
  moves with a write, through MCP too; `window_days` is the raw window;
  without a readable count it is left out, the error logged and the limits
  still answered.
- The console: the line with the count and window, none without a count.
