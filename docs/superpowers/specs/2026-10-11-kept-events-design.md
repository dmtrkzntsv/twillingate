# Kept events: keep an event's raw rows past the raw window

Status: draft
Date: 2026-10-11

## Problem

- **An event's detail lives `RETENTION_RAW_DAYS`; only its counts live
  longer.** The daily pass rolls a day up into counts per name, value, user
  and group, then deletes its raw rows. Some events are facts about one
  user rather than traffic: an analytics opt-out, a plan change, a
  cancellation, the settings an account had when it left. Those need to be
  read per user — who, which group, with which attributes, in which order —
  for as long as the aggregates are kept, and on a 30-day raw window they
  cannot be. The first econumo opt-outs are already gone at user level.
- **The only lever is global.** Raising `RETENTION_RAW_DAYS` keeps every
  event of every project raw, a year of page views to preserve a few hundred
  rows, and makes the nightly identity and cohort rollups scan a year of raw
  days.
- **Rows cannot simply be left in `events`.** Nothing records that a day
  was rolled up: a day is aggregated exactly when its raw rows are gone,
  and the live half of every `v_*` view, the nightly identity, cohort and
  usage rollups and the raw-day counters all rely on it. A kept row on a
  rolled-up day would be counted twice by the views and would make the
  daily pass re-roll the day from the kept rows alone, overwriting its
  totals, cohorts and per-user activity with partial numbers.

So kept rows go to their own table, which nothing that produces today's
numbers reads.

## Decisions

### The rule

- **D1. `keep` is a third rule action.** `rules` (migration 040, #172)
  gains `action = 'keep'` with `to_name` NULL, for `kind = 'event'` only:
  an attribute rule keeps nothing. SQLite cannot alter a CHECK, so
  migration 041 rebuilds the table. A name has at most one rule, as before:
  keep, block and rename are exclusive on a name.
- **D2. Scope is the other rules' scope.** `family` is `product` or
  `measures`; never views, never a `$` name (`$form_submit` included: it is
  the form's conversion and lives with the submission). A keep for a name
  with no stored rows is accepted, for an app version not yet shipped.
- **D3. Typed refusals.** `manage.ErrConflict` for a keep on a blocked name
  ("block it or keep it") and for a keep on a name that is a rename's
  source (its events are stored under the target: keep the target). A keep
  on a kept name is idempotent. Blocking a kept name replaces the keep, as a
  block replaces any rule, and the name's kept rows go at the next daily
  pass (D9).
- **D4. Rename and merge carry the keep.** The one `UPDATE … SET
  event_name` a rename or merge runs on `events` runs on `events_kept` too,
  in the same transaction, and the keep moves to the target: renaming kept
  `A` to `B` leaves `A → B` and `B kept`; merging kept `A` into `B` makes
  `B` kept (the user chose to keep those events; the merge must not quietly
  stop). A merge onto a blocked name is refused without `merge`, as today,
  and with it the kept rows go with the aggregates at the next pass.
  `hasEventRows`, which decides whether a rename is a merge, counts
  `events_kept` as stored rows.

### Storage

- **D5. `events_kept` is `events`, column for column.** Same columns, same
  types, same generated `bucket`, same primary key `(family, project_id,
  day, id)`, `WITHOUT ROWID`. A row is in exactly one of the two tables at
  any moment. A test asserts the two column sets are equal, so a later
  `events` migration (the size encoding) has to carry both. The table is in
  `projectTables`, so an archived project's purge deletes its kept rows.
- **D6. The rollup copies kept rows, then deletes the day as before.**
  `AggregateProductDay` and `AggregateMeasureDay` already delete the day's
  blocked rows, roll the rest up and delete them in one transaction. Between
  the rollup and the delete they now run
  `INSERT OR IGNORE INTO events_kept SELECT * FROM events WHERE family=?
  AND project_id=? AND day=? AND event_name IN (SELECT name FROM rules
  WHERE project_id=? AND kind='event' AND family=? AND action='keep')`. Same
  transaction as the delete, so a crash leaves the day either raw or rolled
  up with its kept rows; `OR IGNORE` keeps a re-run idempotent. The kept
  rows were rolled up with the rest, so the aggregates count them as they
  always did.
- **D7. A keep reaches back as far as the raw window, no further.** Rows
  already rolled up exist only as aggregates and cannot be recovered; rows
  of days still raw are copied when their day rolls up. The console dialog
  and the docs say so.
- **D8. Kept rows age out with the aggregates.** A new store step,
  `PruneKept(ctx, projectID, before)`, deletes `events_kept` rows with
  `day < today - RETENTION_AGGREGATE_DAYS`; the daily pass runs it beside
  `PruneAggregates`. Removing a keep stops the copying and deletes nothing:
  the rows already kept stay until they expire, readable with `kept = 1`
  (D10). Nothing is deleted on request, as for a block.
- **D9. A blocked name leaves `events_kept`.** `DropBlockedAggregates`,
  which deletes a blocked name's aggregates at the next daily pass, deletes
  its `events_kept` rows too: once blocked, nothing of the name remains past
  the raw window.
- **D10. `v_events_flat` is the one place kept rows are read.** The
  process-owned view becomes `SELECT …, 0 AS kept FROM events UNION ALL
  SELECT …, 1 FROM events_kept`; `kept` joins `flatViewBaseColumns`, and
  the `attr_*` columns apply to both halves. The union never overlaps (D5).
  `raw_views`, `raw_product` and `raw_measures` stay views over `events`
  alone: they mean "not yet rolled up" to the fourteen store and API
  readers and to every `v_*` live half, and a kept row on a rolled-up day
  there is the double counting the problem describes. The `v_*` views are
  unchanged: their aggregate halves already hold the kept rows' counts.
  Reading a kept name's whole history is one query:

  ```sql
  SELECT ts, user_id, group_id, attributes
  FROM v_events_flat
  WHERE project_id = :project AND event_name = 'user_update_analytics'
  ORDER BY ts
  ```

  A query over every event still thins out past the raw window, with only
  kept names beyond it; the docs say to filter by `event_name`.
- **D11. Counts tell kept rows apart.** `limits` keeps `raw_events: {held,
  window_days}` for rows inside the raw window (`WHERE kept = 0`) and adds
  `kept_events: {held, window_days}` with `RETENTION_AGGREGATE_DAYS`.
  `usage`'s `size` gains `kept_bytes` beside `raw_bytes` and
  `aggregate_bytes`, measured by the same daily pass; `total_bytes`
  includes it. The nightly identity, cohort, usage-history and
  `received_attributes` rollups read `raw_*` and never see kept rows, which
  were counted when their day was raw.

### Surfaces

- **D12. One tool, one route,** beside the four of event rules, audited like
  them:

  | Tool | Route | Body / result |
  | --- | --- | --- |
  | `keep_event` (idem) | `POST /api/projects/{project_id}/event-rules/keep` | `{family, name}` → the rule |

  `remove_event_rule` removes a keep like any rule (D8); `list_event_rules`
  answers `action: "keep"` with no `to`. No CLI, as for the other rules.
- **D13. Settings page, Events section.** The event menu gains **Keep…** on
  an unkept name and **Stop keeping** on a kept one; a kept name shows a
  "kept" badge, listed even with nothing in the range, as a blocked name is.
  - **Keep dialog:** "Keep X past the raw window? Its events are copied when
    their day is rolled up, after N raw days, and kept for M days with every
    column. Events already rolled up cannot be recovered. Read them in
    `v_events_flat` (`kept = 1`)." N and M are the two retention settings.
  - **Stop keeping:** no dialog; the menu item removes the rule, and the
    badge goes. The rows stay until they expire (D8).
  Clickable rows follow the pointer-cursor rule; the section fits a 360px
  phone.

### Not done

- **Keeping views** (`$page_view`, `$screen_view`): a view is traffic, and
  the rule model excludes `$` names.
- **Keeping `$form_submit`:** the submission is the record; the event is
  its conversion count.
- **Recovering rows already rolled up** (D7).
- **Deleting kept rows on request** (D8). Removing the rule and waiting is
  the way, as for a block.
- **Kept rows in `raw_product`** (D10). Possible later by pointing the
  `v_*` live halves at `events` directly, at the cost of a documented trap
  for widgets that add `agg_*` and `raw_*` by hand.
- **A per-user summary** (first seen, last seen, count, latest attributes
  per user and event). A different feature; it can be derived from
  `events_kept` when wanted.
- **The size encoding of `events`** (separate spec). It applies to both
  tables (D5).

## Documentation

- `docs/twillingate.md`: the event rules section (`keep_event`, D3, D4,
  D7, D8), the views section (`v_events_flat`'s `kept` column and the
  history query, `raw_*` unchanged), `limits` (`kept_events`) and `usage`
  (`kept_bytes`).
- `docs/deployment.md`: `RETENTION_AGGREGATE_DAYS` also bounds kept rows.
- `deploy/UPGRADES.md`: migration 041 rebuilds `rules` and adds
  `events_kept`; no pre-checks, nothing visible on upgrade day.
- `internal/api/docs_sync_test.go` binds the tool name and route.

## Testing

- Store: a rolled-up day copies exactly the kept names' rows and deletes
  the day, in one transaction; a re-run is a no-op; the aggregates of the
  day are unchanged by the keep; a blocked name is neither aggregated nor
  kept; `PruneKept` deletes by day and leaves younger rows; rename and
  merge rewrite `events_kept` and move the keep (D4);
  `DropBlockedAggregates` deletes a blocked name's kept rows; the column
  sets of `events` and `events_kept` are equal; `projectTables` covers the
  table (the existing cross-check).
- Manage: the refusals of D3, idempotent keep, keep carried by rename and
  merge.
- Flat view: `kept` is 0 on raw rows and 1 on kept rows; `attr_*` columns
  read both halves; `raw_product` does not carry kept rows.
- API: `keep_event` over MCP and REST, `list_event_rules` showing it,
  `limits` and `usage` counts.
- Jobs: the daily pass keeps, prunes and drops in the documented order.
- `TestSystemDashboards` and the `v_*` view tests: numbers unchanged with
  kept rows present on rolled-up days.
- Web: the menu items, badge and dialog; `cursor.spec.ts` and
  `phone.spec.ts` pass.
