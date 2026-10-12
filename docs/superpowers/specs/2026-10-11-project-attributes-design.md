# Project attributes: a catalog, one rules table, rename, merge and block

Status: draft
Date: 2026-10-11

## Problem

- **An attribute key cannot be changed once events carry it.** A typo or a
  new naming convention (`planType` → `plan`) leaves two keys for one fact:
  history under the old one, new events under the new one, and every app
  version in the field still sending the old one. Breakdowns, the `attr_*`
  columns of `v_events_flat` and widget SQL see two keys. Today the only fix
  is SQL by hand against `events`, two aggregate tables and
  `received_attributes`.
- **An attribute cannot be refused.** A key sent by mistake (a session id,
  a debug blob) keeps being stored on every event; the only lever is
  shipping new app code, which old installs never get.
- **A project's keys have no table of their own.** Which keys a project
  breaks down is a JSON list on `projects` (`projects.attributes`), read by
  `json_each` in two views, the usage history and the registry; which keys
  it has is only derivable from per-day rows in `received_attributes`.
- **`received_attributes` is named for where its rows come from, not what
  they are**: per-day measurements of a project's keys, taken by the daily
  pass, the same kind of data as `usage_history`. Its tool lists the
  project's attributes, which is what the console and a model want from it.

Event rules (#164, #171) solved the first two for event names. This applies
the same rules to attribute keys, from the same table, and gives a project's
keys a catalog with the breakdown flag on each.

## Decisions

### Tables

- **D1. `rules` replaces `event_rules`; attribute rules live beside event
  rules.** Migration 040 rebuilds `event_rules` (039, unreleased) as:

  ```sql
  CREATE TABLE rules (
      project_id INTEGER NOT NULL REFERENCES projects(id),
      kind       TEXT    NOT NULL CHECK (kind IN ('event', 'attribute')),
      family     TEXT    NOT NULL,
      name       TEXT    NOT NULL,
      action     TEXT    NOT NULL CHECK (action IN ('rename', 'block')),
      to_name    TEXT,
      created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
      PRIMARY KEY (project_id, kind, family, name),
      CHECK ((action = 'rename') = (to_name IS NOT NULL)),
      CHECK ((kind = 'event' AND family IN ('product', 'measures'))
          OR (kind = 'attribute' AND family = 'event'))
  ) WITHOUT ROWID;
  ```

  Every `event_rules` row is copied with `kind = 'event'`, then
  `event_rules` is dropped. An attribute rule has `family = 'event'`: a rule
  on an attribute of events, product and measures alike. `user` and `group`
  are left for a later spec that gives those attributes a kind of their own.
  Every event-rule behaviour is unchanged; its SQL reads `rules` with
  `kind = 'event'`.
- **D2. `received_attributes` is renamed `usage_attributes`**, columns and
  behaviour unchanged: one row per (project, day, key) for the raw window,
  inserted at ingest the first time a key arrives that day and counted by
  the daily pass (`events`, `max_values`).
- **D3. `project_attributes` is the catalog: one row per key a project
  has, with its breakdown flag.**

  ```sql
  CREATE TABLE project_attributes (
      project_id INTEGER NOT NULL REFERENCES projects(id),
      key        TEXT    NOT NULL,
      breakdown  INTEGER NOT NULL DEFAULT 0 CHECK (breakdown IN (0, 1)),
      created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
      PRIMARY KEY (project_id, key)
  ) WITHOUT ROWID;
  ```

  Its rows are every key in the project's `usage_attributes` (custom keys,
  and the nine declarable `$` keys when events send them) plus every
  breakdown key, received or not.
  - **Ingest** adds a row in the transaction that inserts the day's
    `usage_attributes` row, and only then: `INSERT OR IGNORE` for the
    (project, day, key) triples the `receivedSeen` cache has not seen. A
    key already seen today costs nothing more than it does now.
  - **The daily pass**, in the transaction that recounts `usage_attributes`
    (`countReceived`), inserts any key of `usage_attributes` missing from
    the catalog (so the catalog heals if a write was lost) and deletes the
    rows with `breakdown = 0` that have no `usage_attributes` row left: a
    key not received within the raw window and not a breakdown leaves the
    catalog with its last day. The table stays the size of the window's
    keys plus the breakdowns.
- **D4. `projects.attributes` goes.** Migration 040 fills
  `project_attributes` from `usage_attributes` (distinct keys) and from each
  project's list (`breakdown = 1`; distinct text values, a malformed list
  copies nothing, as the views read it today); recreates
  `v_product_attrs` and `v_measures_attrs` from their 038 text with the key
  source `SELECT project_id, key FROM project_attributes WHERE breakdown =
  1` (dropped before the column, created after: SQLite refuses to drop a
  column a view names); and drops the column. Every other reader moves to
  the catalog: the registry load, `usage_history.go` (the declared count
  and the cap-usage key set), `reporting/dev.go`. Rows go with the project
  on purge, as `rules` rows do.

  The API keeps its shape: `create_project`, `update_project`,
  `list_projects`, `schema://projects` and `project update -attr` still
  carry `attributes`, the project's breakdown keys, now **ordered by key**
  (the declared order only ordered the `attr_*` columns of
  `v_events_flat`). Writing `attributes` sets `breakdown = 1` on the listed
  keys (inserting their rows) and `0` on the rest, in the project write's
  transaction. `ATTRIBUTE_BREAKDOWNS_MAX` counts breakdown rows across
  active projects, as it counted list entries.
- **D5. Which keys take rules.** A custom key only, on both sides: an
  empty key, a `$` key (a typed column, not a key in the `attributes`
  JSON) and a key containing `"` (a SQLite JSON path cannot quote it) are
  `invalid`. Rules cover the product and measures rows; views are left to
  the server, as for event rules.

### Rename and merge

- **D6. Rename and merge, as for events.**
  - `rename_attribute {from, to}` gives a key a name not in use. `to` with
    stored data (a `usage_attributes` row, which covers the raw window, or
    rows in `agg_product_attrs` or `agg_measures_attrs`) is `conflict`,
    pointing to merge.
  - `merge_attribute {from, to}` moves `from` into a key that may already
    have data. `from` needs no stored data: that is how an alias is added
    for an app version not yet shipped.
  - Neither goes onto a blocked key (`conflict`): to stop receiving a key,
    block it.
  - `from` equal to `to` is `invalid`; so is a key D5 refuses.
- **D7. One transaction rewrites everything that names `from`.**
  - **Raw rows** of the project's product and measures families that carry
    `from`: where `to` is absent, `from`'s value moves to `to`; where both
    are present, `to`'s value is kept and `from` is removed (**the target
    wins**). Attribute values are stored as strings, so nothing else
    changes.
  - **`agg_product_attrs`** and **`agg_measures_attrs`**: `attr_key` `from`
    becomes `to`; rows that meet on the primary key add up (`count`,
    `unique_users`, `unique_groups`; `samples`, `weight`, `sum`), as an
    event merge does.
  - **`usage_attributes`**: `(project, day, from)` becomes `to`; meeting
    rows add `events` and keep the larger `max_values` (a NULL, not yet
    counted, yields to a number).
  - **`project_attributes`**: `from`'s row goes; `to`'s row exists after,
    a breakdown if either was. No breakdown is gained, so the budget is
    never exceeded by a rename or a merge.
  - **`rules`**: the attribute rule `from → to` is upserted; every rule
    pointing at `from` is retargeted to `to`, so a target is never itself
    renamed away; a rename rule on `to` (`A → B` exists, then `B → A`) is
    deleted first. A block on `from` is replaced by the rename.
  - The audit row (`attribute.rename` or `attribute.merge`) and the config
    bump; the result is the rule and the rows rewritten per table.
  - The store forgets the project's `receivedSeen` entries, so `to`'s
    usage row is inserted again if needed.
  - When a breakdown moved, `v_events_flat` is rebuilt, as on any registry
    write.
- **D8. What merged days keep.** Raw rows merge exactly. Aggregates hold
  days older than the raw window and cannot be rebuilt: counts, samples,
  weight and sum stay exact; `unique_users` and `unique_groups` on a merged
  day become an upper bound (one person whose events carried both keys
  counts twice); a merged day may hold more than `ATTRIBUTE_VALUES_TOP_N`
  values for the key. `usage_attributes` is recounted from raw by the next
  daily pass, so its upper bound lasts a night.
- **D9. Rows written later are mapped twice over, at no cost to a project
  without attribute rules.**
  - **Ingest** maps each event's custom keys from the registry snapshot
    before queueing: a key renamed away moves to its target unless the
    event already carries the target (several keys renamed to one target
    with no target sent: the first in key order wins), then a blocked key
    is removed. A project with no attribute rules skips this with one map
    lookup.
  - **The store** does the same in `insertEvents`, before inserting and
    before recording the received keys: it reads the attribute rules of the
    batch's projects in the flush's own transaction, one primary-key range
    query per project in the batch, and a project with none maps nothing. A
    row queued before a rename or block commits is therefore stored mapped,
    as event renames are (event rules D7).
  - `BenchmarkWriteEvents` gains a case with attribute rules; the PR
    records it against the baseline taken on main, with no regression in
    time or allocations for a project without rules.

### Block

- **D10. A block strips the key from events, never the events.**
  `block_attribute {key}` writes the rule and clears the key's breakdown.
  From then on ingest and the store remove the key (D9); the event is
  stored without it. The response warns once per key per batch, on the
  first event that carried it: "attribute `X` is blocked in this project,
  dropped". Breaking down a blocked key or a key renamed away (through
  `attributes`) is `conflict`, naming the rule. Blocking a key renamed
  away replaces its rename (one rule per key).
- **D11. Stored raw rows keep the key** until their day leaves the raw
  window: nothing is stripped on request (event rules D8). The breakdown
  views do not read it, a blocked key never being a breakdown.
- **D12. The key's aggregates go at the next daily pass.**
  `DropBlockedAggregates` also deletes the `agg_product_attrs` and
  `agg_measures_attrs` rows of blocked keys. Removing the block before then
  loses nothing; after it, the aggregated breakdown is gone for good.
- **D13. `usage_attributes` keeps measuring what raw rows carry**, so a
  blocked key's counts fade with the raw window, and its catalog row with
  them (D3).
- **D14. Removing a rule.** `remove_attribute_rule {key}` deletes the
  rule: a rename stops mapping (history stays where it went), a block stops
  stripping. No rule is `not_found`.

### Surfaces

- **D15. `list_attributes` replaces `received_attributes`** (breaking:
  `feat!`). `GET /api/projects/{project_id}/attributes?from&to` replaces
  `GET /api/received-attributes`; `project_id` is required. It answers the
  project's attributes for the range, busiest first: the 500 busiest keys
  with a `usage_attributes` row in the range, plus every breakdown key and
  every blocked key, each `{key, events, max_values, received, breakdown,
  blocked, aliases}` (`breakdown` was `declared`; `aliases` are the keys
  renamed or merged into it); `keys_total`, `values_cap`,
  `breakdowns_used` and `breakdowns_max` as before. A key renamed away is
  not listed: it shows only in its target's `aliases`.
- **D16. Four rule tools and four routes**, `manage` operations audited
  like the event rules, typed refusals carried through (REST 400 / 404 /
  409), routes beside the event rules':

  | Tool | Route | Body → result | Hints |
  | --- | --- | --- | --- |
  | `rename_attribute` | `POST /api/projects/{project_id}/attribute-rules/rename` | `{from, to}` → `{rule, rewritten: {table: rows}}` | — |
  | `merge_attribute` | `POST …/attribute-rules/merge` | `{from, to}` → as rename | destructive |
  | `block_attribute` | `POST …/attribute-rules/block` | `{key}` → `{rule}` | destructive, idempotent |
  | `remove_attribute_rule` | `POST …/attribute-rules/remove` | `{key}` → `{status: "removed"}` | idempotent |

  A rule is `{key, action, to?, created_at}`. Keys travel in bodies: they
  may contain `/`. A breakdown is still switched through
  `update_project`'s `attributes`, the one way to do it. `list_event_rules`
  is unchanged and lists event rules only.
- **D17. The console: one Attributes table replaces the Breakdowns
  section** on Settings, below Events, from `list_attributes` for the
  page's range. Being a breakdown is a column of that table, not a list of
  its own.
  - **Attribute:** the key; alias chips under it with `+` (Add alias…, a
    merge) and × (remove, confirmed); a blocked key struck through with the
    Blocked badge, as on Events.
  - **Received:** today's `describeKey` text.
  - **Breakdown:** a switch, on for the keys broken down: it marks and
    toggles them (`update_project`'s `attributes`); switching off is
    confirmed, as removing a breakdown is today; disabled at the budget
    (unless on) and on blocked keys; the budget ("3 of 50 in use") in the
    section header.
  - **Menu:** Rename…, Merge into…, Add alias…, Block… / Unblock. The
    rename, merge and alias dialogs are the Events section's, generalized
    over their wording rather than copied; the merge dialog offers the other
    listed keys, without blocked ones and the key itself.
  - Declared `$` keys show with the switch only (no menu).
  - The section fits a 360px phone (`phone.spec.ts`): Received folds under
    the key below `sm`; controls follow the pointer-cursor rule.

  `AddBreakdownDialog` and `BreakdownsSection` go: every listed key is a
  row with its switch.

### Not done

- **User and group attributes as their own kind.** A key describing the
  user or the group is a regular attribute sent with events, and stays
  one. A later spec can add `user` and `group` families to attribute rules
  and whatever storage they need.
- **Rewriting widget SQL** that names an old key (`json_extract(attributes,
  '$.old')`, `attr_old`). The rename and merge dialogs and the docs say so.
- **Stripping stored raw rows on request** (D11).
- **Rules on `$` keys** and on views' attributes.
- **Enforcing rules in `twillingate.js`**, for event rules' reasons.
- **Keeping the old `received_attributes` name** as an alias: renamed
  outright, pre-1.0.

## Documentation

- `docs/twillingate.md`: the catalog and breakdowns, attribute rules (D5–D14,
  with the merge upper bounds and the ingest warning), the four rule tools
  and routes, `list_attributes` replacing `received_attributes` (fields,
  route), `attributes` ordered by key, the tool count.
- `deploy/UPGRADES.md`: 040 renames the tool and its route
  (`received_attributes` → `list_attributes`,
  `/api/received-attributes` → `/api/projects/{project_id}/attributes`),
  renames the table `received_attributes` → `usage_attributes`, replaces
  `event_rules` by `rules` and `projects.attributes` by
  `project_attributes`; SQL or scripts naming any of them must follow.

## Testing

- **store/sqlite:** migration 040 test (event rules copied with kind
  `event`, `usage_attributes` renamed with its rows, the catalog filled
  from usage and from the lists, a malformed list skipped,
  `projects.attributes` gone, `v_product_attrs` / `v_measures_attrs`
  answer as before); the catalog's ingest insert and the nightly heal and
  prune; rename and merge rewrite raw rows (target wins), both aggregate
  tables (summed on meeting), `usage_attributes` and the catalog, and
  report counts; chain collapse and renaming back; a row written after a
  rename or block lands mapped (D9); blocked aggregates dropped, other keys
  untouched; rows purged with the project; `BenchmarkWriteEvents` with and
  without rules (D9) and a benchmark of the raw rewrite on a seeded
  project, both recorded in the PR. `sqlcheck` covers the new SQL.
- **manage:** refusals (D5, D6, D10, D14); `attributes` writes as
  breakdown flags, ordered by key, against the budget; the snapshot's
  attribute rules and the mapping function (target wins, several aliases,
  blocked after mapping), with a benchmark of the no-rules path.
- **server:** a renamed key stored under its target, the target winning
  when both are sent; a blocked key stripped with one warning per batch,
  the event kept.
- **jobs:** the daily pass drops blocked keys' aggregates.
- **api:** `list_attributes` (fields, aliases, blocked, the renamed-away
  key hidden) and the four rule tools and routes; the old tool and route
  are gone; `docs_sync_test` binds the names.
- **web:** vitest for the table (breakdown switch, menu, chips, blocked
  row) and the generalized dialogs; e2e renames, merges and blocks a key
  and switches a breakdown through Settings; `phone.spec.ts` and
  `cursor.spec.ts` cover the section.
