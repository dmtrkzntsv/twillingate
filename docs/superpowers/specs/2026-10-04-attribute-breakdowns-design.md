# Attribute breakdowns

Status: draft
Date: 2026-10-04

## Problem

- **Declaring an attribute is really creating a breakdown, but the console
  treats it like an allow-list.** Since #125 the server stores every
  attribute an event sends. Declaring a key only decides which keys get
  aggregate rows (`agg_product_attrs`, `agg_measures_attrs`) and appear in
  `product_attributes`. The project dialog asks for keys as free text
  ("plan, $path, …"), so the user has to know and type key names the
  server has already seen.
- **The dialog hides that it takes several values.** Allowed origins and
  attributes look like single text inputs; Enter or a comma adds a chip,
  and nothing says so.
- **Nothing bounds the number of breakdowns.** Aggregates grow with
  breakdowns × events carrying each key × `ATTRIBUTE_VALUES_TOP_N` × days
  kept (`RETENTION_EVENTS_AGGREGATE_DAYS`, a year by default). The values
  per key are capped; the number of breakdowns is not.

## Decisions

- **D1. A table of received attribute keys, beside `events`.**
  Migration 028 adds:

  ```sql
  CREATE TABLE received_attributes (
      project_id INTEGER NOT NULL,
      day        TEXT    NOT NULL,   -- the event's day, as events.day
      attr_key   TEXT    NOT NULL,
      events     INTEGER NOT NULL,   -- product and measure events carrying it
      max_values INTEGER,            -- NULL until the daily pass counts the day
      PRIMARY KEY (project_id, day, attr_key)
  ) WITHOUT ROWID;
  ```

  It covers product and measure events only, the families declared
  attributes break down. Custom keys come from `attributes`. The nine
  declarable reserved keys (`store.DeclarableAttributes`: `$host`,
  `$path`, `$referrer`, `$utm_*`, `$os_version`, `$browser_version`,
  `$device_model`) come from their columns when non-empty. The eight
  always-on keys (`$platform`, `$os`, …) are not recorded: they are
  never declared.

- **D2. Ingest counts keys in the same transaction as the events.**
  `WriteEvents` (`internal/store/sqlite/write.go`) adds 1 per key for
  each product or measure row it actually inserts (`RowsAffected() == 1`).
  A row ignored as a duplicate `id` (a retried batch) counts nothing.
  The counts are summed in memory per (project, day, key) across the
  batch, then written with one upsert per distinct triple:
  `INSERT … ON CONFLICT DO UPDATE SET events = events + excluded.events`.
  A 500-event batch with 5 keys each therefore costs a handful of
  upserts, not 2,500. Views rows are skipped before any work.

- **D3. Ingest must not slow storing events by more than 10%.** A new
  `BenchmarkWriteEvents` in `internal/store/sqlite/bench_test.go` writes
  500-event product batches with 0, 5 and 20 custom attributes each. It
  lands in its own commit first, so `benchstat` can compare the commit
  before the counting with the one after; the PR reports the numbers
  (ns/op and allocs). If the overhead on the 5-attribute case exceeds
  10%, the counting moves out of the write path (the daily pass alone
  writes the table, and today's keys appear the next day). We'd decide
  that from the benchmark, not by default.

- **D4. The daily pass recounts finished days and fills `max_values`.**
  For every raw day before today, the pass rewrites each project's rows
  (`INSERT OR REPLACE`). `events` is the exact count, which also corrects
  anything ingest counted for a day that later lost rows. `max_values`
  is the most distinct values the key had in any one (event name,
  measure) partition that day. That is the partition
  `ATTRIBUTE_VALUES_TOP_N` caps, so the dialog's "values" is exactly what
  the cap compares against. It runs in the same pass and transaction as
  `countAttributes` (`internal/store/sqlite/server_stats.go`), which
  already reads the same JSON. On the first start after upgrading, the
  pass fills the whole raw window, so no migration backfill is needed.

- **D5. The table follows the raw window.** The pass deletes rows with
  `day` older than `RETENTION_EVENTS_RAW_DAYS`, the same cutoff as the
  raw rollup. Selecting a key fills its breakdown from the raw days still
  held; days already rolled up don't backfill. So a key seen only before
  the raw window has nothing left to show, and the table never holds
  more (project, day, key) rows than the raw events it summarizes. It
  needs no cap of its own, even for a client that sends random keys.

- **D6. `ATTRIBUTE_BREAKDOWNS_MAX`: one limit for the whole database.**
  Default 50; 0 is no limit. It counts the attributes declared across
  all active (not archived) projects. Each opt-in `$` key counts as one;
  the eight always-on keys don't.
  - A create or update that adds keys is refused if the total would
    exceed the limit: `manage.ErrInvalid`, with a message like
    "47 of 50 attribute breakdowns are in use; this adds 4".
  - A save that keeps or removes keys always goes through, so a server
    already over the limit (after an upgrade, or a lowered setting) can
    still be cleaned up.
  - Restoring an archived project is never refused, even past the
    limit; the limit then blocks only new additions until there's room.
  - `manage.Ops` gains `BreakdownsMax int`, set from config by `app`
    and by the CLI (`cmd/twillingate/project.go`), so the CLI, MCP and
    REST all enforce it.
  - It is listed by `limits` in the `caps` group ("Attribute
    breakdowns", `zero: "no cap"`). The daily pass stores it in
    `server_stats` as `attribute_breakdowns_max` beside the other caps.

- **D7. `received_attributes(project_id, from, to)`.** A new read tool,
  REST `GET /api/projects/{project_id}/received-attributes`. `from`/`to`
  default to the raw window. It answers:

  ```jsonc
  {
    "project_id": 1, "from": "2026-09-05", "to": "2026-10-04",
    "keys": [
      // events: summed over the range; max_values: the busiest day's,
      // null when no day in the range has been counted yet (today only)
      { "key": "plan", "events": 1204, "max_values": 3, "declared": true },
      { "key": "order_id", "events": 980, "max_values": 412, "declared": false },
      { "key": "$path", "events": 1204, "max_values": 38, "declared": false }
    ],
    "values_cap": 50,          // ATTRIBUTE_VALUES_TOP_N
    "breakdowns_used": 12,     // across all active projects
    "breakdowns_max": 50       // ATTRIBUTE_BREAKDOWNS_MAX, 0 = no limit
  }
  ```

  The keys come from `received_attributes` over the range, plus every
  key the project declares even if not received (`events: 0`), sorted
  by `events` descending, then key.

- **D8. The project dialog picks breakdowns from what was received.**
  `ProjectFormDialog` changes:
  - **Allowed origins:** one input row per origin, each with its own ×,
    and an "Add origin" button. The hint reads "`*` allows any origin.
    With none, browsers can't send; native apps still can."
  - **Breakdowns** (replaces "Declared attributes"): a range switcher
    (default 30 days, no further back than the raw window) and a
    checkbox list from D7. Each row shows the key, its events, and its
    values against the cap ("3 values", or "412 values · folds past 50"
    when `max_values` exceeds `values_cap`). The header reads
    "Breakdowns · 12 of 50 in use". It counts the dialog's own changes,
    and Save is disabled with the reason when they would exceed the limit.
  - **A key not received yet** can be added from a text field below the
    list. A `$` key is refused there inline ("`$` keys appear in the
    list once received"). An unknown `$` key is already refused by
    `manage` (`internal/manage/ops.go:110`).
  - On create, the project has received nothing yet: the list is empty
    and only the text field shows.

- **D9. Details on the project page show breakdowns with their counts.**
  `DetailsSection` lists origins one per line and the declared keys as
  "Breakdowns" with each key's events and values from D7 over the page's
  range. A declared key not received in the range is marked "not
  received". The Usage section's "Declared but not sent" line goes, since
  Details now says it; the `usage` tool keeps `unused_attributes`.

## Out of scope

- Breakdowns for views. Views attributes are stored, but declared keys
  only break down product events and measures today.
- Backfilling a new breakdown into days already rolled up.
- A per-project limit (rejected in discussion: one database-wide limit
  bounds the aggregates).

## Docs

- `docs/twillingate.md`: the `received_attributes` tool and route, the
  `limits` row (the new cap), the project fields section (declaring is
  choosing a breakdown, with the limit).
- `docs/deployment.md`: `ATTRIBUTE_BREAKDOWNS_MAX`.
- `deploy/UPGRADES.md`: migration 028. The first pass after upgrading
  counts the raw window's keys. A server already over 50 breakdowns
  keeps them all but can't add more until it is under the limit or the
  setting is raised.
- `internal/api/resources.go` `schemaViews`: no change; the table is not
  a queryable view.

## Tests

- `internal/store/sqlite`: ingest counts inserted rows only (a
  duplicate batch adds nothing); views are not counted; reserved keys
  come from their columns; the pass recounts and fills `max_values` per
  (event, measure) partition; rows past the raw window are pruned. A
  migration 028 test pins its ceiling.
- `BenchmarkWriteEvents` per D3, numbers in the PR.
- `internal/manage`: a save that adds past the limit is refused with
  `ErrInvalid`; a save that keeps or removes keys while over the limit
  passes; restore past the limit passes; archived projects don't count;
  0 is no limit.
- `internal/api`: `received_attributes` merges declared and received
  keys, sorts, and reports the budget; the limits test covers the new
  cap.
- `web`: the dialog lists received keys, counts the budget with its own
  edits, refuses `$` in the text field, edits origins as rows; Details
  shows breakdowns. Playwright: the projects e2e edits a project's
  breakdowns.
