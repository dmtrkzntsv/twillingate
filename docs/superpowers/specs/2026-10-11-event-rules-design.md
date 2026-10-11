# Event rules: rename and block

Status: draft
Date: 2026-10-11

## Problem

- **An event name cannot be changed once events arrive.** A typo or a new
  naming convention in the app leaves two names for one action: history
  under the old one, new events under the new one, and every app version
  still in the field sending the old one. Today the only fix is SQL by hand
  against `events` and four aggregate tables.
- **An event cannot be refused.** A noisy or abandoned event keeps being
  stored, aggregated and kept for `RETENTION_AGGREGATE_DAYS`, and the only
  lever is shipping new app code, which old installs never get.

Both are project settings: the console's Settings page, an MCP tool and a
REST route each.

## Decisions

### Rules

- **D1. One table, two actions.** Migration 038 adds
  `event_rules (project_id, family, name, action, to_name, created_at)`,
  primary key `(project_id, family, name)`, `WITHOUT ROWID`.
  `action` is `rename` (with `to_name`) or `block` (`to_name` NULL). A name
  has at most one rule: blocking a name that has a rename rule replaces it,
  and renaming a blocked name replaces the block. Rows go with the project
  on purge.
- **D2. Scope: product and measures, never views, never `$`.** `family` is
  `product` or `measures`; a rule is per project and per family. Neither
  side of a rule may start with `$` (`$page_view`, `$form_submit`, the Web
  Vitals): those belong to the server. A name must be non-empty; there is
  no length cap ingest does not already have.
- **D3. Typed refusals.** `manage.ErrInvalid` for: a family other than
  product or measures, an empty or `$` name, `from` equal to `to`.
  `manage.ErrNotFound` for an unknown project, or removing a rule that does
  not exist. `manage.ErrConflict` for a rename onto a name with stored rows
  without `merge` (D6). An archived project takes rules like any other
  setting.

### Rename

- **D4. A rename rewrites history and keeps a rule, in one transaction.**
  `UPDATE … SET event_name = to` for the project and family in `events`,
  then in the family's aggregates (`agg_product_daily`, `agg_product_attrs`
  for product; `agg_measures_daily`, `agg_measures_attrs` for measures);
  upsert the rule; write the audit row. The result reports the rows
  rewritten per table. A name with no stored rows is accepted: the rule
  alone is written, for an app version not yet shipped.
- **D5. Chains collapse, and renaming back works.** Renaming B→C retargets
  every rule `X→B` to `X→C`, so a rule's target is never itself a rule's
  source. Renaming onto a name that is a rule's source (`A→B` exists, then
  `B→A`) deletes that rule first, so A is no longer mapped away. A rename
  onto a blocked name keeps the block: the renamed events are dropped from
  then on, as D9 says.
- **D6. Renaming onto an existing name merges, only when asked.** If `to`
  has stored rows in `events` or the family's aggregates, the rename is
  refused (`ErrConflict`) unless `merge: true`: a merge cannot be split
  back. Merging sums aggregate rows that meet on the primary key
  (`INSERT … ON CONFLICT DO UPDATE SET count = count + excluded.count`, the
  same for `unique_users`, `unique_groups`, `samples`, `weight`, `sum`).
  Aggregates only hold days older than the raw window, so they cannot be
  rebuilt from raw: counts, samples, weight and sum stay exact,
  `unique_users` and `unique_groups` become an upper bound on merged days
  (someone who sent both names counts twice), and `agg_product_attrs` may
  hold more than the top-N values for a merged day. Raw rows merge exactly.
- **D7. Renames apply at the store, so the write buffer cannot slip
  through.** `insertEvents` stores
  `COALESCE((SELECT to_name FROM event_rules WHERE project_id=? AND family=?
  AND name=? AND action='rename'), ?)` as `event_name`. Any row written
  after the rename commits is mapped, including rows queued before it. One
  primary-key lookup per row. Ingest (D9) also resolves renames from the
  snapshot, which is idempotent with this because of D5.

### Block

- **D8. A block never deletes raw events.** Deleting stored rows on
  request is out (too risky); blocked raw rows stay readable in the live
  half of dashboards until their day leaves the raw window.
- **D9. Ingest drops blocked events before they are queued.** The registry
  snapshot carries each project's rules. `handleEvents` resolves the name
  through the rename rules, then drops the event if the result is blocked:
  the batch still answers 2xx (a 4xx is a poison batch to clients), the
  event is not counted as stored, and the response carries a warning
  "event `X` is blocked in this project, dropped". Blocking B therefore
  also drops events renamed to B. No store-side check: events queued in
  the seconds before a block commits may land, which is harmless.
- **D10. Blocked names are never aggregated.** The rollups in
  `AggregateProductDay` and `AggregateMeasureDay` (`agg_product_daily`,
  `agg_product_totals`, `agg_product_attrs`, `agg_measures_daily`,
  `agg_measures_attrs`) skip blocked names with
  `NOT EXISTS (SELECT 1 FROM event_rules …)`. The existing end-of-window
  `DELETE FROM events … day=?` then removes the blocked raw rows with the
  rest, so they are gone within `RETENTION_RAW_DAYS` (30 by default) without a new delete.
- **D11. Existing aggregates of a blocked name go at the next daily pass.**
  A new store step, `DropBlockedAggregates(ctx, projectID)`, deletes rows
  for blocked names from the four per-event aggregate tables; the daily
  pass runs it per project before aggregating. Idempotent. Not
  `agg_product_totals`: it is not split by event, so past totals keep
  their values. Retention and identity aggregates are not touched: they
  count who was active, not what they sent.
- **D12. Unblocking.** Removing the block before the next pass loses
  nothing. After it, the aggregated history is gone for good; raw days
  still inside the window aggregate normally again.

### Surfaces

- **D13. Four tools, four routes** (`internal/api`, `manage` operations,
  audited like every project setting):

  | Tool | Route | Body / result |
  | --- | --- | --- |
  | `list_event_rules` (ro) | `GET /api/projects/{project_id}/event-rules` | `{rules: [{family, name, action, to?, created_at}]}` |
  | `rename_event` (write) | `POST …/event-rules/rename` | `{family, from, to, merge?}` → the rule and `{rewritten: {table: rows}}` |
  | `block_event` (idem) | `POST …/event-rules/block` | `{family, name}` → the rule |
  | `remove_event_rule` (idem) | `POST …/event-rules/remove` | `{family, name}`; removing a rename stops the mapping (history stays renamed), removing a block resumes storing |

  Remove takes a body, not a path segment: event names may contain `/`
  (the `delete_submissions` pattern).
- **D14. Settings page: an "Events" section** between Breakdowns and Keys.
  It lists the project's product and measure events in the page's range
  with their counts (from `product_events` and `measures`), each with a
  menu: **Rename…** and **Block…**. A blocked name that still has rows in
  the range shows a "blocked" badge. Below the list, the rules: `A → B`
  or `X blocked`, each with **Remove**.
  - **Rename dialog:** the new name; if it already has events, a warning
    that this merges the two and cannot be undone, and the button reads
    "Merge into B". It also says widget SQL naming the old name is not
    rewritten.
  - **Block dialog:** "Stop storing X? The next daily pass (03:00 UTC)
    deletes X's aggregated history; stored raw events expire within N
    days. Remove the rule to receive it again." N is the raw retention.
  Clickable rows follow the pointer-cursor rule; the section fits a 360px
  phone (`phone.spec.ts`).

### Not done

- **Rewriting widget SQL** that names an old event. The rename dialog and
  the docs say so.
- **Deleting stored events** of a blocked name on request (D8).
- **Enforcing rules in `twillingate.js`.** The server must enforce them
  anyway (native apps, backends, cached old tags all post to
  `/api/events`); a blocked event costs a few hundred bytes in a batch the
  SDK sends anyway; the bundle is one static committed file that fetches no
  config, so delivering rules means a per-page-load request or a
  per-project bundle, and cached rules would keep dropping an unblocked
  event. Renames gain nothing client-side. `onEvent` returning `false`
  already drops an event in the browser. Revisit with a cached rules
  endpoint if a chatty event ever makes bandwidth matter.

## Documentation

- `docs/twillingate.md`: the four tools and routes, rename and block
  semantics (D4–D12), the merge upper bound, the ingest warning.
- `deploy/UPGRADES.md`: nothing; 038 only adds a table.

## Testing

- **store/sqlite:** rename rewrites raw and aggregates and reports counts;
  merge sums aggregate rows; chain collapse; renaming back; a write after a
  rename lands under the new name (D7); rollups skip blocked names;
  `DropBlockedAggregates` leaves totals and other names alone; rules go
  with a purged project. Migration 038 test. `sqlcheck` covers the new SQL.
- **manage:** refusals (D3), the merge guard, one rule per name.
- **server:** a blocked event is dropped with the warning and a 2xx; a name
  renamed to a blocked one is dropped; other events in the batch are kept.
- **jobs:** the daily pass drops blocked aggregates before aggregating.
- **api:** the four tools and routes; `docs_sync_test` binds the names.
- **web:** vitest for the section and both dialogs (merge warning, button
  label); e2e renames and blocks through the Settings page.
