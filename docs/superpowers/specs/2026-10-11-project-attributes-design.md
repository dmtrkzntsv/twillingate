# Project attributes: one table, rename, merge and block

Status: draft
Date: 2026-10-11

## Problem

- **An attribute key cannot be changed once events carry it.** A typo or a
  new naming convention (`planType` → `plan`) leaves two keys for one fact:
  history under the old one, new events under the new one, and every app
  version in the field still sending the old one. Breakdowns, the
  `attr_*` columns of `v_events_flat` and widget SQL see two keys. Today the
  only fix is SQL by hand against `events`, two aggregate tables and
  `received_attributes`.
- **An attribute cannot be refused.** A key sent by mistake (a session id,
  a debug blob) keeps being stored on every event; the only lever is
  shipping new app code, which old installs never get.
- **A key's settings have no home of their own.** Whether it is a
  breakdown lives in a JSON list on `projects` (`projects.attributes`),
  read by `json_each` in two views, the usage history and the registry.
  Rename and block rules would need a second place.

Event rules (#164, #171) solved the first two for event names. This does
the same for attribute keys, and gives every key's settings one row.

## Decisions

### One table

- **D1. `project_attributes` is the single source of truth for a key's
  settings.** Migration 040 adds:

  ```sql
  CREATE TABLE project_attributes (
      project_id INTEGER NOT NULL REFERENCES projects(id),
      key        TEXT    NOT NULL,
      breakdown  INTEGER NOT NULL DEFAULT 0 CHECK (breakdown IN (0, 1)),
      renamed_to TEXT,
      blocked    INTEGER NOT NULL DEFAULT 0 CHECK (blocked IN (0, 1)),
      created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
      updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
      PRIMARY KEY (project_id, key),
      CHECK (NOT (blocked = 1 AND renamed_to IS NOT NULL)),
      CHECK (breakdown = 0 OR (blocked = 0 AND renamed_to IS NULL))
  ) WITHOUT ROWID;
  ```

  The checks are structural only (a key is a rule or a breakdown, never
  both); which keys are allowed is `manage`'s (D4).
- **D2. A row exists only for a key with a setting:** a breakdown, a
  rename or a block. A write that leaves a row with none of them (no
  breakdown, no rule) deletes it. Received keys stay where they are, in
  `received_attributes` (028): ingest gains no write per key, and the
  table stays the size of what someone configured.
- **D3. `projects.attributes` goes.** Migration 040 copies each project's
  list into rows with `breakdown = 1` (distinct text values; a malformed
  list copies nothing, as the views read it today), recreates
  `v_product_attrs` and `v_measures_attrs` from their 038 text with the
  key source `SELECT project_id, key FROM project_attributes WHERE
  breakdown = 1`, and drops the column (the two views are dropped before
  it and created after, since SQLite refuses to drop a column a view
  names). Every other reader moves to the table: the registry load
  (`registry.go`), `usage_history.go` (declared counts and unused keys),
  `reporting/dev.go`. Rows go with the project on purge.

  The API keeps its shape: `create_project`, `update_project`,
  `list_projects`, `schema://projects` and `project update -attr` still
  carry `attributes`, now the project's breakdown keys **ordered by key**
  (the declared order was kept before; it only ordered the `attr_*`
  columns of `v_events_flat`). Writing `attributes` sets `breakdown` on
  the listed keys (inserting rows) and clears it on the rest (deleting
  rows left with no setting, D2). `ATTRIBUTE_BREAKDOWNS_MAX` counts breakdown
  rows across active projects, as it counted list entries.
- **D4. Which keys.**
  - A **custom key** (no `$`) takes every setting.
  - The **nine declarable reserved keys** (`$host`, `$path`, `$referrer`,
    `$utm_source`, `$utm_medium`, `$utm_campaign`, `$os_version`,
    `$browser_version`, `$device_model`) take a breakdown only: they are
    typed columns, not keys in the `attributes` JSON, so there is nothing
    to rename, merge or block.
  - Every other `$` key is refused, as declaring one is today.
  - A key containing `"` takes no rule (a SQLite JSON path cannot quote
    it); a breakdown on one stays possible, as today.

### Rename and merge

- **D5. Rename and merge, as for events.**
  - `rename_attribute {from, to}` gives a key a name not in use. `to`
    with stored data (raw rows of product or measures carrying it, rows in
    `agg_product_attrs`, `agg_measures_attrs` or `received_attributes`) is
    `conflict`, pointing to merge.
  - `merge_attribute {from, to}` moves `from` into a key that may already
    have data. `from` needs no stored data: that is how an alias is added
    for an app version not yet shipped.
  - Neither goes onto a blocked key (`conflict`): to stop receiving a key,
    block it.
  - `from` equal to `to` is `invalid`; so is a `$` key on either side
    (D4).
- **D6. One transaction rewrites everything that names `from`.**
  - **Raw rows** of the project's product and measures families that
    carry `from`: where `to` is absent, `from`'s value moves to `to` with
    its JSON type; where both are present, `to`'s value is kept and
    `from` is removed (**the target wins**).
  - **`agg_product_attrs`** and **`agg_measures_attrs`**: `attr_key`
    `from` becomes `to`. Rows that meet on the primary key add up
    (`count`, `unique_users`, `unique_groups`; `samples`, `weight`,
    `sum`), as an event merge does.
  - **`received_attributes`**: `(project, day, from)` becomes `to`;
    meeting rows add `events` and keep the larger `max_values`.
  - **The rows:** `from`'s row becomes `{renamed_to: to}`, with no
    breakdown; `to` is a breakdown if either was. No breakdown is gained, so the budget is never
    exceeded by a rename or merge.
  - **Chains collapse:** every row renamed to `from` is retargeted to
    `to`, so a target is never itself renamed away. Renaming onto a key
    that is renamed away (`A → B` exists, then `B → A`) clears `A`'s rule
    first.
  - The audit row (`attribute.rename` or `attribute.merge`), and the
    result: the rows rewritten per table.
  - When a breakdown moved, `v_events_flat` is rebuilt as on any registry
    write.
- **D7. What merged days keep.** Raw rows merge exactly. Aggregates hold
  days older than the raw window and cannot be rebuilt: counts, samples,
  weight and sum stay exact; `unique_users` and `unique_groups` on a
  merged day become an upper bound (one person whose events carried both
  keys counts twice); a merged day may hold more than
  `ATTRIBUTE_VALUES_TOP_N` values for the key; `received_attributes`'
  `events` is an upper bound too (a row carrying both keys counts twice).
- **D8. Rows written later are mapped twice over.**
  - **Ingest** resolves each event's keys from the registry snapshot
    before queueing: a renamed-away key moves to its target unless the
    event already carries it (the target wins), a blocked key is removed.
  - **The store** does the same in `insertEvents`: it loads the batch's
    projects' renames and blocks in the flush's own transaction (one query
    per batch, not per row) and applies them in Go before inserting and
    before recording the received keys. A row queued before a rename or
    block commits is therefore stored mapped, as event renames are (event
    rules D7).

### Block

- **D9. A block strips the key from events, never the events.**
  `block_attribute {key}` sets `blocked` and clears `breakdown`. From then
  on ingest and the store remove the key (D8); the event is stored
  without it. The response warns once per key per batch, on the first
  event carrying it: "attribute `X` is blocked in this project, dropped".
  A merge or rename onto a blocked key is `conflict` (D5); breaking down a
  blocked key (through `attributes`) is `conflict`, naming the block.
  Blocking a renamed-away key replaces its rename (one rule per key, as
  for events): from then on it is dropped, not mapped.
- **D10. Stored raw rows keep the key** until their day leaves the raw
  window: nothing is stripped on request (event rules D8). They are not
  read into the attribute breakdowns, which only read breakdown keys.
- **D11. The key's aggregates go at the next daily pass.**
  `DropBlockedAggregates` also deletes `agg_product_attrs` and
  `agg_measures_attrs` rows of blocked keys. Removing the block before
  then loses nothing; after it, the aggregated breakdown is gone for good.
- **D12. `received_attributes` keeps reporting what raw rows carry**, so
  a blocked key's counts fade with the raw window; it is marked blocked
  wherever it is listed.
- **D13. Removing a rule.** `remove_attribute_rule {key}` on a renamed
  key stops the mapping (history stays where it went) and deletes the row;
  on a blocked key it unblocks and deletes the row. A key with neither is `not_found`.

### Surfaces

- **D14. Reads: `received_attributes` grows, no new list tool.** Each key
  gains `blocked` and `aliases` (the keys renamed or merged into
  it, from the rows). Keys with a row but no events in the range are
  listed, as declared keys are today, with `received` false; a
  renamed-away key is not a key of its own and shows only in its target's
  `aliases`. `declared` keeps its name and means `breakdown`.
- **D15. Four tools and four routes**, `manage` operations audited like
  every project setting, typed refusals carried through (REST 400 / 404 /
  409):

  | Tool | Route | Body → result | Hints |
  | --- | --- | --- | --- |
  | `rename_attribute` | `POST /api/projects/{project_id}/attributes/rename` | `{from, to}` → the settings and `{rewritten: {table: rows}}` | — |
  | `merge_attribute` | `POST …/attributes/merge` | `{from, to}` → as rename | destructive |
  | `block_attribute` | `POST …/attributes/block` | `{key}` → the settings | destructive, idempotent |
  | `remove_attribute_rule` | `POST …/attributes/remove-rule` | `{key}` → `{status: "removed"}` | idempotent |

  Keys travel in bodies: they may contain `/`. A breakdown is still
  switched through `update_project`'s `attributes`, the one way to do it.
- **D16. The console: one Attributes table replaces the Breakdowns
  section** on Settings, below Events. Rows are the range's received keys
  (the `received_attributes` query the section already makes) plus every
  key with a row, busiest first. Being a breakdown is a column of that
  table, not a list of its own.
  - **Attribute:** the key; alias chips under it with `+` (Add alias…,
    a merge) and × (remove, confirmed); a blocked key struck through with
    the Blocked badge, as on Events.
  - **Received:** today's `describeKey` text.
  - **Breakdown:** a switch, on for the keys broken down: it marks them and
    toggles them (`update_project`'s `attributes`); disabled at the budget
    (unless on) and on blocked keys; the budget ("3 of 50 in use") in the
    section header.
  - **Menu:** Rename…, Merge into…, Add alias…, Block… / Unblock. The
    rename, merge and alias dialogs are the Events section's, generalized
    over their labels rather than copied; the merge dialog offers the other
    listed keys, without blocked ones and the key itself.
  - Declared `$` keys show with the switch only (no menu).
  - The section fits a 360px phone (`phone.spec.ts`): Received folds under the key
    below `sm`; controls follow the
    pointer-cursor rule.

  `AddBreakdownDialog` goes: every received key is already a row with its
  switch.

### Not done

- **User and group attributes as their own kind.** A key describing the
  user or the group is a regular attribute sent with events, and stays one:
  no scope label (it would change nothing), no latest value per user or
  group beside the identity, no counting users per value. A later spec
  adds a scope column with whichever behaviour it brings.
- **Rewriting widget SQL** that names an old key (`json_extract(attributes,
  '$.old')`, `attr_old`). The rename and merge dialogs and the docs say
  so.
- **Stripping stored raw rows on request** (D10).
- **Rules on `$` keys** and on views (`$page_view` attributes): event
  rules leave views to the server too, and `received_attributes` does not
  list their keys.
- **Enforcing rules in `twillingate.js`**, for event rules' reasons.

## Documentation

- `docs/twillingate.md`: the attributes table (breakdown, rules),
  the four tools and routes, rename, merge and block semantics (D5–D13),
  the merge upper bounds, the ingest warning, `received_attributes`' new
  fields, `attributes` ordered by key; the tool count.
- `deploy/UPGRADES.md`: 040 moves `projects.attributes` into
  `project_attributes` and drops the column; SQL reading
  `projects.attributes` by hand must read the table. Not otherwise
  visible.

## Testing

- **store/sqlite:** migration 040 test (lists moved, malformed list
  skipped, column gone, `v_product_attrs` / `v_measures_attrs` answer as
  before); rename and merge rewrite raw rows (target wins, JSON type
  kept), both aggregate tables (summed on meeting) and
  `received_attributes`, and report counts; chain collapse and renaming
  back; a row written after a rename or block lands mapped (D8); blocked
  aggregates dropped by `DropBlockedAggregates`, other keys untouched;
  rows purged with the project; a benchmark of the raw rewrite on a
  seeded project, its time recorded in the PR. `sqlcheck` covers the new
  SQL.
- **manage:** refusals (D4, D5, D9, D13); `attributes` writes as
  breakdown flags, ordered by key, against the budget; D2's row deletion.
- **server:** a renamed key stored under its target, the target winning
  when both are sent; a blocked key stripped with one warning per batch,
  the event kept.
- **jobs:** the daily pass drops blocked keys' aggregates.
- **api:** the four tools and routes; `received_attributes`' new fields;
  `docs_sync_test` binds the names.
- **web:** vitest for the table (breakdown switch, menu, chips,
  blocked row) and the generalized dialogs; e2e renames, merges and blocks
  a key and switches a breakdown through Settings; `phone.spec.ts` and
  `cursor.spec.ts` cover the section.
