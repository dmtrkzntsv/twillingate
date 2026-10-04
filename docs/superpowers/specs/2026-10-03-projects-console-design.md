# Projects in the console

Status: draft
Date: 2026-10-03

## Problem

- **Projects and ingest keys can only be managed over MCP, REST or the
  CLI.** The console shows projects only in a dashboard's project switcher;
  there is no place to see a project's origins, declared attributes or
  keys, create one, or issue and disable a key.
- **The caps are invisible.** `PRODUCT_ATTRIBUTES_TOP_N`,
  `VIEWS_DIMENSIONS_TOP_N` and `IDENTITIES_TOP_N` (#119) decide what the
  aggregates keep, but nothing reports their values or how close a
  project's data comes to them. Answering "does the cap cost us data" took
  ad hoc SQL against production.
- **Usage is invisible.** How much a project sends, when it last sent
  anything, and how much disk it takes are not shown anywhere.
- **The key list's JSON is inconsistent.** `list_ingest_keys` answers
  `Label`, `Key` and `State` (no json tags) where every other field is
  snake_case.

## Decisions

- **D1. Full management in the UI.** Everything the API can do for
  projects and keys: create, edit (name, origins, attributes), archive and
  restore projects; issue, disable and enable keys. The UI calls the
  existing routes; it adds no write path of its own.
- **D2. A list page and a page per project.** `/projects` lists every
  project; `/projects/:id` shows one. The sidebar gains a "Projects" link at
  the top, and `/` opens the list.
- **D3. Two new read-only operations for usage and caps, one for the
  settings.** `limits`, `cap_usage` and `project_stats`, each an MCP tool
  and a REST route through `expose`, so MCP clients get the same answers.
  All compute on request from the `v_*` views (which since #119 read only
  the raw days a range covers); project sizes are the one exception, read
  from `server_stats`, which the daily pass fills (see `project_stats`).
- **D4. Caps are shown, not edited.** They are environment settings; the
  UI shows the value in force, the default, what each caps, and that 0
  means no cap, and points at `twillingate.env`.
- **D5. `list_ingest_keys` answers snake_case.** `label`, `key`, `state`.
  A breaking change (`feat!`), noted in `deploy/UPGRADES.md`.
- **D6. A separate PR, stacked on #119.** It needs #119's cap settings.

## Server

### `limits`

`GET /api/limits`, MCP `limits`. No arguments.

```json
{"limits": [
  {"setting": "VIEWS_DIMENSIONS_TOP_N", "value": 1000, "default": 1000,
   "caps": "values per views breakdown and kinds, per project and day; the rest fold into (other)"},
  {"setting": "PRODUCT_ATTRIBUTES_TOP_N", "value": 100, "default": 100,
   "caps": "values per attribute key, per project, day and event; the rest fold into (other)"},
  {"setting": "IDENTITIES_TOP_N", "value": 1000, "default": 1000,
   "caps": "users, and groups, per project and day; the rest are dropped"}
]}
```

`value` is the running config's; 0 means no cap. `default` comes from the
same constants config uses, so the two cannot drift.

### `cap_usage`

`GET /api/projects/{project_id}/cap-usage?from=&to=`, MCP `cap_usage`.
`from`/`to` are `YYYY-MM-DD`, default the last 30 days ending today (UTC);
`from` after `to` or a range over 400 days is `invalid`; an unknown
project is `not_found`.

```json
{"project_id": 6, "from": "2026-09-03", "to": "2026-10-02", "dimensions": [
  {"setting": "VIEWS_DIMENSIONS_TOP_N", "dimension": "paths", "cap": 1000,
   "max_values_per_day": 501, "max_day": "2026-09-11",
   "days": 30, "days_capped": 4, "folded_share": 0.41}
]}
```

One row per capped dimension that has data in the range:

- **Views** (`VIEWS_DIMENSIONS_TOP_N`): `paths`, `hosts`, `referrers`,
  `utm`, `countries`, `platforms`, `os`, `browsers`, `app_versions`,
  `devices`, `displays`, `locales` from their `v_views_*` view, and `kinds`
  from `v_views_daily`. `consent` is left out: three values at most. A
  value count per day is the view's rows that day, the `(other)` row
  included; a day is capped when it has an `(other)` row; `folded_share`
  is the `(other)` rows' views over all views in the range.
- **Attributes** (`PRODUCT_ATTRIBUTES_TOP_N`): one row per attribute key
  (`dimension` is the key, e.g. `plan` or `$os`), over every event, from
  `v_product_attrs` and `v_measures_attrs`. Values per day is the largest
  per-event count that day (the cap is per event); capped means an
  `(other)` row on that day for any event; `folded_share` is `(other)`'s
  share of the key's `count` (product) and `samples` (measures).
- **Identities** (`IDENTITIES_TOP_N`): `users` and `groups` from
  `v_identity_daily`. There is no `(other)` row, so a day is capped when
  its row count reaches the cap (some ids may have been dropped), and
  `folded_share` is null.

For days already rolled up the views only hold the kept values and their
`(other)` rows, so "values per day" stays near the cap there (cap + 1 for
single-key breakdowns and attributes, cap plus one per leading key for
two-key breakdowns); the true distinct count of a folded day is not
recoverable. The UI says so.

A cap of 0 reports `cap: 0`. A day rolled up under an older cap keeps its `(other)` row and still counts as capped; identities, which keep no trace, report no capped day under no cap.

### `project_stats`

`GET /api/stats?project_id=&from=&to=`, MCP `project_stats`. `project_id`
is optional (as on `/api/keys`): without it, every project, archived ones
included, is returned, which is what the list page reads. Range rules as
`cap_usage`.

```json
{"from": "2026-09-03", "to": "2026-10-02", "database_bytes": 2254000000,
 "projects": [{
   "project_id": 4,
   "series": [{"day": "2026-09-03", "views": 449, "events": 0, "measures": 12,
               "total_bytes": 2240000}],
   "totals": {"views": 2765, "events": 0, "measures": 310},
   "last_received_at": "2026-10-03T16:02:11Z",
   "first_day": "2026-08-25", "raw_days": 30, "rolled_up_days": 9,
   "size": {"raw_bytes": 812000, "aggregate_bytes": 1450000, "total_bytes": 2262000,
            "measured_at": "2026-10-03"},
   "unused_attributes": ["self_hosted"]
 }]}
```

- **series / totals:** `views` from `v_views_daily` (sum of `views`),
  `events` from `v_product_totals` (`total_events`), `measures` from
  `v_measures_daily` (sum of `samples`). Days before the newest daily pass
  come from the counts it stored in `server_stats` (`views`, `events`,
  `measures` per project and day; a day with no row counts 0), which
  outlive the aggregates' retention; that day and later are counted live.
  The pass counts every day before its own that the aggregates or the raw
  rows still hold, again each night, so a late event on a raw day is
  counted. Every day in the range is present,
  zeros included, so a chart needs no gap filling. `total_bytes` is the
  project's size measured that day (raw plus aggregate), null on a day
  not measured.
- **last_received_at:** the newest `received_at` among the project's raw
  rows of every family; null when it has none.
- **first_day / raw_days / rolled_up_days:** the oldest day with any data
  (raw, aggregated, or counted by the daily pass, whose counts outlive the
  aggregates), the number of distinct raw days, and of rolled-up
  days (`agg_views_daily`, `agg_product_totals`, `agg_measures_daily`
  days).
- **size:** an estimate, measured by the daily pass and once after the
  server starts (the pass runs at boot), stored in `server_stats` (`key`,
  `project_id`, `measured_at` — the UTC day —, `value`; primary key
  `key, project_id, measured_at`) and read by requests: no `dbstat` at
  request time. The table is a daily history, kept until the project is
  purged (two rows per project and day); a second run on the same day
  replaces that day's rows. `size` is the newest measured day's. Each table's bytes come from
  `dbstat` (`aggregate = TRUE`); a project's share of a table is its rows
  over the table's rows. `raw_bytes` covers `events`; `aggregate_bytes`
  every table keyed by `project_id` (the `agg_*` tables, `actors`,
  `identities`). `measured_at` is the day; `size` is null until the project
  has a measurement, or when the newest measurement found none of its rows,
  and the rest of the answer stands.
- **attributes per day:** each series day carries `declared_attributes`
  (on a day the pass measured) and, counted by the pass while the day's
  rows are raw and kept once they are rolled up, `attribute_keys` and
  `attribute_values` (distinct custom keys and key/value pairs received,
  declared or not) and `attribute_values_folded` (values past
  `PRODUCT_ATTRIBUTES_TOP_N` in every event, measure and aggregated key;
  exact for product events, an upper bound for measures, whose ranking
  shares places on ties). Null where not stored. The project page's Usage
  shows them as an Attributes tile. The pass also stores the caps in force
  each day (project 0) so a day's folds read against the cap that made
  them.
- **database_bytes / database_series:** `page_count × page_size` of the
  database file now, and per day of the range as the daily pass stored it
  (`database_bytes`, project 0; null on days not measured). The list page
  shows the growth over the range ("+12 MB since Sep 4").
- **unused_attributes:** declared keys with no row in `v_product_attrs`
  or `v_measures_attrs` in the range.

A cold `dbstat` read took seconds on a production database of about 2 GB,
which is why the measurement moved out of the request.

### `list_ingest_keys`

Fields become `project_id`, `label`, `key`, `state`.

### Errors

New operations use the existing typed refusals (`manage.ErrNotFound` →
404 `not_found`, `manage.ErrInvalid` → 400 `invalid`), matched with
`errors.Is`.

## UI

### Sidebar

"Projects" comes first: a plain link to `/projects`, active on the list and
on any project page; the projects are not listed under it. Then
"Dashboards", in the style of the old "Yours" heading, holds every
dashboard: the system ones first, each with a "Built-in" badge and a
tooltip saying it is always listed first, then the user's, which still
drag to a new order. "Gallery" is closed until opened (open on a gallery
page, and always with the sidebar down to icons, where its heading is
hidden). Archive sits at the bottom, above Log out.

### Top bar

Each page's top bar shows its breadcrumbs, every crumb but the last a
link: `Projects › dev` on a project page, `Gallery › Components` in the
gallery.

### Landing

`/` opens `/projects`. The dashboard picker that used to answer `/` (the
last dashboard opened on this device, else the first system one) moves to
`/dashboards`, where archiving a dashboard's last tab and "Go to your
dashboards" land. A dashboards preview (`reporting dev`), which hides the
projects, still opens the dashboards.

### `/projects`

- A header: the project count and `database_bytes` ("6 projects · 2.1 GB
  on disk"), and a "New project" button.
- A grid of cards, one per active project: name, a live dot with "last
  event 2 min ago" (muted after a day of silence), origins, a sparkline of
  the last 30 days' events (views + events + measures), the 30-day total,
  size, active key count and declared attribute count. A card links to the
  project page.
- The Limits panel: each setting with its value, default and what it
  caps, "0 = no cap", "set in twillingate.env".
- "Archived (n)", collapsed: archived projects' cards, each with Restore.
- "New project" opens a dialog (name, origins, attributes). Its result
  shows the first key and the install snippet with copy buttons, the one
  time the snippet is shown.

### `/projects/:id`

A header with the name, an archived badge, and Archive (with a
confirmation naming the purge date from `purge_after_days`) or Restore.
Then:

1. **Usage.** The dashboards' range picker; a stacked bar chart of events
   per day by family; a line of the measured size per day; stat tiles for total events, last received, data
   size (raw and aggregates), days kept (raw and rolled up); unused
   attributes listed under the tiles when there are any.
2. **Details.** Name, allowed origins and declared attributes as chips
   (`*` allowed for origins); Edit saves through `PATCH`; server
   refusals show inline.
3. **Ingest keys.** A table of label, key (truncated, copy button) and
   state. "Issue key" asks for a label and shows the key with its snippet.
   Disable (with a confirmation: ingest from any site using it stops) and
   Enable are row actions.
4. **Cap impact.** Shares the Usage range. One row per dimension:
   dimension, cap, busiest day (values and date), days capped out of days
   with data, folded share with a bar. Capped rows first, highlighted. A
   note says that rolled-up days keep only the kept values and `(other)`.

Data flows through React Query (`queries.ts`, `endpoints` in `api.ts`).
Mutations invalidate the projects, keys and stats queries, so the
dashboards' project switcher sees changes too. Every mutation reports its
result in a toast. A section whose data fails to load shows the error and
a retry; the rest of the page works. Built from the existing shadcn
components and the widgets' chart setup; no new library.

## Testing

- **Go:** `limits` against the config, 0 included; `cap_usage` on a
  fixture with capped and uncapped days for paths, an attribute key and
  users (max per day, days capped, folded share, the cap-0 case);
  `project_stats` (series sums equal the views', every day present,
  sizes read from `server_stats`' newest day and null before the first
  measurement, the size history in the series (null on unmeasured days),
  last event, unused attributes, all projects without `project_id`); the
  key fields in snake_case over REST and MCP; REST/MCP parity and docs-sync cover the
  new operations.
- **Web (vitest):** sidebar Projects first as a plain link and the built-in dashboards; breadcrumbs that link;
  cards from fixtures (live and muted dots); project page sections;
  form bodies sent on edit; key actions and confirmations; a failed
  section shows retry.
- **Playwright:** create a project and see its key and snippet; edit its
  origins; issue and disable a key; archive and restore; open a seeded
  project's usage chart and cap impact.

## Docs

- `docs/twillingate.md`: `limits`, `cap_usage`, `project_stats` (tools
  and routes tables), the key field rename.
- `deploy/UPGRADES.md`: the key field rename.

## Out of scope

- Editing caps from the UI (environment settings).
- Per-key ingest counts: events do not record their key, and the ingest
  counters live only in the ingest process's memory.
- The true distinct count of days already rolled up.
