# Event rules (rename and block) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Per-project rules that rename an event (rewriting its stored history and mapping it at ingest) or block it (dropped at ingest, never aggregated), managed from the console's Settings page, MCP tools and REST routes.

**Architecture:** One table, `event_rules` (migration 038). The store owns the rewrite, the merge, the rule-table upkeep and the insert-time rename mapping. `manage` validates input, writes the audit rows and carries the rules in the registry snapshot. The ingest handler resolves each name through the snapshot and drops blocked events. The daily pass deletes blocked raw rows inside the aggregation transaction and drops their old aggregates. `api` exposes four tools plus a REST-only `event_names` read. The web app adds an "Events" section to `SetupTab`.

**Tech Stack:** Go 1.x with modernc SQLite (`internal/store/sqlite`); React + TypeScript + TanStack Query + shadcn/ui (`web/`); Playwright e2e.

**Spec:** `docs/superpowers/specs/2026-10-11-event-rules-design.md` (decisions D1–D14). Read it before any task.

## Global Constraints

- Go is at `/usr/local/go/bin` (not on PATH): prefix commands with `export PATH=$PATH:/usr/local/go/bin`.
- Rules apply to the families `product` and `measures` only. Views and every name starting with `$` are refused on both sides of a rule (D2).
- Refusals are typed: `manage.ErrInvalid`, `manage.ErrNotFound`, `manage.ErrConflict`, matched with `errors.Is`, never on message text.
- A rename onto a name that has stored rows is refused with `ErrConflict` unless `merge` is true (D6).
- Blocking never deletes anything on request (D8). The only new deletes are the blocked raw rows of the day being aggregated, inside the aggregation transaction (D10), and `DropBlockedAggregates` (D11).
- Every rule write calls `auditAndBump` (audit row plus `config_version` bump), so other processes reload the snapshot.
- A test that needs a migrated database copies one: `newTestDB`/`snapshotAt` inside `store/sqlite`, `storetest.Open` elsewhere. It never migrates its own.
- SQL passed to `database/sql` must be constant strings where possible: `internal/sqlcheck` prepares every one against the migrated schema in `make check`.
- Commits follow Conventional Commits with the scopes from CLAUDE.md. Every commit ends with the line `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Web: every clickable element is a `button`, a link or `role="button"` (pointer-cursor rule, `e2e/cursor.spec.ts`). Nothing scrolls sideways on a 360px phone (`e2e/phone.spec.ts`).
- Docs are updated in the same commit as the change: tools and routes go in `docs/twillingate.md` (`internal/api/docs_sync_test.go` checks them).

## Review Focus

1. **Event names with `/`, spaces, quotes or unicode** (`checkout/v2`, `Sign up`, `é`): rename and block, then remove through REST. The remove body (not the path) must carry them unchanged. Covered in Task 5's API test.
2. **Renaming back after a chain** (A→B, then B→C, then C→A): the rules must end with B→A and C→A and no rule for A, so A's events are not mapped away. Covered in Task 1.
3. **Merging aggregates where only one side has a day**, or where both have the same day: no unique-key error, counts summed, and the source rows are gone. Covered in Task 1.
4. **A batch mixing blocked, renamed and ordinary events:** only the blocked one is dropped, it still counts as accepted, the response carries exactly one warning for it, and the renamed one is stored under its new name. Covered in Task 4.
5. **Blocking a name whose day is entirely blocked rows:** aggregation must still delete the day and write no `agg_product_totals` row with zero counts. Covered in Task 2.

---

### Task 1: Store — `event_rules` table, rule writes, rename rewrite, insert-time mapping

**Files:**
- Create: `internal/store/sqlite/migrations/038_event_rules.sql`
- Create: `internal/store/sqlite/event_rules.go`
- Create: `internal/store/sqlite/event_rules_test.go`
- Create: `internal/store/sqlite/migration038_test.go`
- Modify: `internal/store/store.go` (add the `EventRule` type and the action constants next to `RegistryProject`)
- Modify: `internal/store/sqlite/registry.go` (add `"event_rules"` to `projectTables`)
- Modify: `internal/store/sqlite/write.go` (`insertEvents`: map the name through the rename rules)

**Interfaces:**
- Produces, in `internal/store/store.go`:
  ```go
  // EventRule is one row of event_rules (migration 038): a rename (To set)
  // or a block of one event name in one project and family.
  type EventRule struct {
  	ProjectID int64
  	Family    Family // FamilyProduct or FamilyMeasures
  	Name      string
  	Action    string // EventRuleRename or EventRuleBlock
  	To        string // the new name; "" for a block
  	CreatedAt string // RFC 3339 UTC
  }

  const (
  	EventRuleRename = "rename"
  	EventRuleBlock  = "block"
  )
  ```
- Produces, on `*sqlite.DB`:
  - `LoadEventRules(ctx context.Context) ([]store.EventRule, error)`: every rule, ordered by project_id, family, name.
  - `RenameEvent(ctx context.Context, projectID int64, family store.Family, from, to string, merge bool, a store.AuditEntry) (map[string]int64, error)`: the rows rewritten per table name. Returns `store.ErrConflict` when `to` has rows and `merge` is false.
  - `BlockEvent(ctx context.Context, projectID int64, family store.Family, name string, a store.AuditEntry) error`
  - `RemoveEventRule(ctx context.Context, projectID int64, family store.Family, name string, a store.AuditEntry) error`: `store.ErrNotFound` when there is no such rule.

- [ ] **Step 1: Write the migration**

`internal/store/sqlite/migrations/038_event_rules.sql`:

```sql
-- 038: event rules (spec 2026-10-11-event-rules). A rename maps an event
-- name to another, and its history was rewritten when the rule was made;
-- a block drops the name at ingest and keeps it out of the aggregates.
-- One rule per name (the primary key): a block replaces a rename and the
-- other way round. Rename targets are never themselves a rule's name of
-- action rename (event_rules.go keeps chains collapsed), so one lookup
-- resolves a name.
CREATE TABLE event_rules (
    project_id INTEGER NOT NULL REFERENCES projects(id),
    family     TEXT    NOT NULL CHECK (family IN ('product', 'measures')),
    name       TEXT    NOT NULL,
    action     TEXT    NOT NULL CHECK (action IN ('rename', 'block')),
    to_name    TEXT,
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    PRIMARY KEY (project_id, family, name),
    CHECK ((action = 'rename') = (to_name IS NOT NULL))
) WITHOUT ROWID;
```

Check how migrations are listed in `internal/store/sqlite/migrate.go`. If files are embedded by glob, nothing more is needed. If there is an explicit list, add 038 to it.

- [ ] **Step 2: Write the migration test**

Model it on `migration037_test.go`: open a snapshot at 037 (`snapshotAt`), migrate to 038, and assert the following.
- `event_rules` exists.
- A `rename` row without `to_name` fails the CHECK.
- A `block` row with `to_name` fails the CHECK.
- Family `views` fails the CHECK.

Also add `"event_rules"` to `projectTables` in `registry.go`, after `"ingest_keys"`. `TestProjectTablesMatchesSchema` fails until you do.

- [ ] **Step 3: Write the failing store tests** in `internal/store/sqlite/event_rules_test.go`

Use the package's existing helpers (`newTestDB`, and the event-writing helpers in `write_test.go` or `aggregate_product_test.go`; find one that writes `store.Event` rows with `WriteEvents`). Insert aggregate rows with plain `db.db.ExecContext` INSERTs. Tests:

1. `TestRenameEventRewritesRawAndAggregates`: product events `a` ×3 and `b` ×1 in project 1, plus one `a` in project 2; one `agg_product_daily` row and one `agg_product_attrs` row for `a` (an older day). `RenameEvent(1, product, "a", "c", false)` gives:
   - raw rows in project 1 named `c` ×3; project 2 untouched;
   - aggregates moved to `c`;
   - result `{"events":3,"agg_product_daily":1,"agg_product_attrs":1}`;
   - `LoadEventRules` returns `{1, product, a, rename, c}`;
   - an `audit_log` row with the action passed in.
2. `TestRenameEventRefusesExistingTargetWithoutMerge`: renaming `a` onto `b` (which has raw rows) without merge returns an error matching `errors.Is(err, store.ErrConflict)`, and nothing changes: raw names, aggregates and rules are as before. Repeat with `b` existing only in `agg_product_daily`; still a conflict.
3. `TestRenameEventMergeSumsAggregates`: `agg_product_daily` holds a, day D: count 5, users 2; b, day D: count 7, users 3; and a, day E: count 1, users 1. After `RenameEvent(a→b, merge=true)`: b/D = 12/5, b/E = 1/1, and no `a` rows. Same for `agg_measures_daily` (samples, weight and sum summed) with family measures. For `agg_product_attrs`, `unique_groups` NULL on either side gives NULL.
4. `TestRenameEventChainsCollapseAndRenameBack`: A→B, then B→C. Rules: A→C and B→C. Then C→A: the rules are B→A and C→A, and there is no rule named A.
5. `TestRenameEventReplacesBlockAndKeepsTargetBlock`: block X, then rename X→Y: X's rule is now a rename. Block Z, then rename W→Z: Z stays blocked and W→Z exists.
6. `TestBlockEventIsIdempotentAndReplacesRename`: block twice gives one rule with an unchanged `created_at`. Rename P→Q, then block P: P's rule is a block with an empty `To`.
7. `TestRemoveEventRule`: removing an existing rule deletes it and audits it. Removing a missing rule returns `store.ErrNotFound`.
8. `TestWriteEventsAppliesRenameRules`: after `RenameEvent(a→c)`, `WriteEvents` with a product event named `a` stores `c`; a measures event named `a` stays `a` (other family); an event in another project stays `a`.
9. `TestRenameEventNoRowsWritesRuleOnly`: renaming a name with no rows gives the result `{"events":0,...}` and the rule exists.
10. `TestDeleteProjectDataRemovesEventRules`: `DeleteProjectData` on a project with a rule leaves no rule. The FK would otherwise refuse the delete.

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/sqlite -run 'EventRule|RenameEvent|BlockEvent|RemoveEventRule|WriteEventsAppliesRename|Migration038|ProjectTables|DeleteProjectDataRemovesEventRules' -count=1`
Expected: FAIL (undefined methods).

- [ ] **Step 4: Add the types** to `internal/store/store.go`, exactly as in Interfaces above.

- [ ] **Step 5: Implement `internal/store/sqlite/event_rules.go`**

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// LoadEventRules is every event rule (migration 038), for the registry
// snapshot and list_event_rules.
func (d *DB) LoadEventRules(ctx context.Context) ([]store.EventRule, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, family, name, action,
		COALESCE(to_name, ''), created_at FROM event_rules ORDER BY project_id, family, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.EventRule
	for rows.Next() {
		var r store.EventRule
		var fam string
		if err := rows.Scan(&r.ProjectID, &fam, &r.Name, &r.Action, &r.To, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Family = store.Family(fam)
		out = append(out, r)
	}
	return out, rows.Err()
}
```

`RenameEvent`: one `d.tx`. In order:

1. **Conflict check**, unless `merge`: does `to` have rows?
   - product: `SELECT EXISTS(SELECT 1 FROM events WHERE family='product' AND project_id=?1 AND event_name=?2) OR EXISTS(SELECT 1 FROM agg_product_daily WHERE project_id=?1 AND event_name=?2) OR EXISTS(SELECT 1 FROM agg_product_attrs WHERE project_id=?1 AND event_name=?2)`
   - measures: the same with `family='measures'`, `agg_measures_daily` and `agg_measures_attrs`.

   If any exist, return `fmt.Errorf("rename %s to %s: %s already has events: %w", from, to, to, store.ErrConflict)`.
2. **Raw rows:** `UPDATE events SET event_name=?3 WHERE family=?4 AND project_id=?1 AND event_name=?2`. Record `RowsAffected` under `"events"`.
3. **Aggregates**, per table of the family: an upsert of the source rows under the new name, then a delete of the source rows. Record the upsert's `RowsAffected` under the table name. Use constant statements, one per table, with no Sprintf, so `sqlcheck` reads them. The `WHERE true` is required by SQLite's parser for an upsert from a SELECT:

```go
// renameAgg moves a family's aggregate rows from one name to another,
// summing rows that meet on the primary key (a merge, D6). unique_users
// and unique_groups summed are an upper bound; NULL unique_groups stays
// NULL (not tracked).
var renameAgg = map[store.Family][]struct{ table, upsert, remove string }{
	store.FamilyProduct: {
		{"agg_product_daily",
			`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users)
			 SELECT project_id, day, ?3, count, unique_users FROM agg_product_daily
			 WHERE project_id=?1 AND event_name=?2 AND true
			 ON CONFLICT (project_id, day, event_name) DO UPDATE SET
			   count = count + excluded.count, unique_users = unique_users + excluded.unique_users`,
			`DELETE FROM agg_product_daily WHERE project_id=?1 AND event_name=?2`},
		{"agg_product_attrs",
			`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
			 SELECT project_id, day, ?3, attr_key, attr_value, count, unique_users, unique_groups FROM agg_product_attrs
			 WHERE project_id=?1 AND event_name=?2 AND true
			 ON CONFLICT (project_id, day, event_name, attr_key, attr_value) DO UPDATE SET
			   count = count + excluded.count, unique_users = unique_users + excluded.unique_users,
			   unique_groups = unique_groups + excluded.unique_groups`,
			`DELETE FROM agg_product_attrs WHERE project_id=?1 AND event_name=?2`},
	},
	store.FamilyMeasures: {
		{"agg_measures_daily",
			`INSERT INTO agg_measures_daily (project_id, day, event_name, measure, bucket, samples, weight, sum)
			 SELECT project_id, day, ?3, measure, bucket, samples, weight, sum FROM agg_measures_daily
			 WHERE project_id=?1 AND event_name=?2 AND true
			 ON CONFLICT (project_id, day, event_name, measure, bucket) DO UPDATE SET
			   samples = samples + excluded.samples, weight = weight + excluded.weight, sum = sum + excluded.sum`,
			`DELETE FROM agg_measures_daily WHERE project_id=?1 AND event_name=?2`},
		{"agg_measures_attrs",
			`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
			 SELECT project_id, day, ?3, measure, attr_key, attr_value, bucket, samples, weight, sum FROM agg_measures_attrs
			 WHERE project_id=?1 AND event_name=?2 AND true
			 ON CONFLICT (project_id, day, event_name, measure, attr_key, attr_value, bucket) DO UPDATE SET
			   samples = samples + excluded.samples, weight = weight + excluded.weight, sum = sum + excluded.sum`,
			`DELETE FROM agg_measures_attrs WHERE project_id=?1 AND event_name=?2`},
	},
}
```

If `sqlcheck` cannot resolve statements read from a map or struct field, inline them as consts in the function instead. Do not put the function in the `dynamic` list.

4. **Rules** (D5), all with `project_id=?1 AND family=?2`:
   - `DELETE FROM event_rules WHERE project_id=?1 AND family=?2 AND name=?3 AND action='rename'` (with `?3 = to`): the target is no longer mapped away. A block on `to` stays.
   - `UPDATE event_rules SET to_name=?4 WHERE project_id=?1 AND family=?2 AND action='rename' AND to_name=?3` (with `?3 = from`, `?4 = to`): retarget the chains.
   - `INSERT INTO event_rules (project_id, family, name, action, to_name) VALUES (?1, ?2, ?3, 'rename', ?4) ON CONFLICT (project_id, family, name) DO UPDATE SET action='rename', to_name=excluded.to_name, created_at=excluded.created_at`
5. `auditAndBump(ctx, tx, a)`.

`BlockEvent`: one tx:
- `INSERT INTO event_rules (project_id, family, name, action) VALUES (?,?,?,'block') ON CONFLICT (project_id, family, name) DO UPDATE SET action='block', to_name=NULL, created_at=excluded.created_at WHERE action <> 'block'`
- then `auditAndBump`.

`RemoveEventRule`: `DELETE FROM event_rules WHERE project_id=? AND family=? AND name=?`. If no row was affected, return `fmt.Errorf("event rule %s/%s: %w", family, name, store.ErrNotFound)`. Otherwise `auditAndBump`.

Every error on the rename path is wrapped with the table name (`fmt.Errorf("rename %s: %w", table, err)`).

- [ ] **Step 6: Map names at insert** in `write.go` `insertEvents`

Replace the `event_name` placeholder in the INSERT's VALUES list with the subquery below. The VALUES list keeps its position; the argument list gains four arguments at that spot.

```sql
COALESCE((SELECT to_name FROM event_rules WHERE project_id=? AND family=? AND name=? AND action='rename'), ?)
```

The args at that position become `e.ProjectID, string(e.Family), e.EventName, e.EventName`. Count the `?`s: the VALUES list goes from 38 placeholders to 41. Update the doc comment of `WriteEvents` with one sentence: "A product or measures name with a rename rule is stored under its new name (D7 of the event rules spec), so rows queued before a rename are mapped too."

- [ ] **Step 7: Run the tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/... ./internal/sqlcheck/... -count=1`
Expected: PASS. `sqlcheck` must accept every new statement.

- [ ] **Step 8: Commit**

```bash
git add internal/store
git commit -m "feat(store): event rules table with rename rewrite and block rows

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Store and jobs — blocked names are never aggregated

**Files:**
- Modify: `internal/store/sqlite/aggregate_product.go` (`AggregateProductDay`)
- Modify: `internal/store/sqlite/aggregate_measures.go` (`AggregateMeasureDay`)
- Modify: `internal/store/sqlite/event_rules.go` (add `DropBlockedAggregates`)
- Modify: `internal/jobs/jobs.go` (`Store` interface plus a call in `RunDailyPass`)
- Test: `internal/store/sqlite/event_rules_test.go`, `internal/jobs/jobs_test.go`

**Interfaces:**
- Consumes: the `event_rules` table and `BlockEvent` from Task 1.
- Produces: `(*DB).DropBlockedAggregates(ctx context.Context, projectID int64) error`, added to `jobs.Store`.

- [ ] **Step 1: Write the failing tests**

In `event_rules_test.go`:
1. `TestAggregateProductDaySkipsBlocked`: on an old day, product events `keep` ×2 and `spam` ×3. Block `spam`, then run `AggregateProductDay`. Results:
   - `agg_product_daily` has only `keep` (2);
   - `agg_product_totals.total_events` is 2;
   - no raw product rows are left for the day;
   - `agg_product_attrs` has no `spam` row.
2. `TestAggregateProductDayAllBlocked`: only `spam` rows on the day, and `spam` is blocked. After aggregation there are no raw rows and no `agg_product_totals` row for the day (Review Focus 5).
3. `TestAggregateMeasureDaySkipsBlocked`: the same idea for measures with `agg_measures_daily`.
4. `TestDropBlockedAggregates`: aggregate rows for `spam` and `keep` in all four per-event tables, plus an `agg_product_totals` row. Block `spam` (product) and `slow` (measures), then `DropBlockedAggregates(1)`. All `spam` and `slow` rows are gone; `keep` rows, totals and other projects' rows are intact. Running it again is a no-op.

In `internal/jobs/jobs_test.go`: find how the fake store records calls (look for `AggregateProductDay` in the fake). Add `DropBlockedAggregates` to the fake, recording the project id. Add `TestDailyPassDropsBlockedAggregates`, asserting it runs once per project id, before that project's `AggregateProductDay`.

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/sqlite ./internal/jobs -run 'Blocked|DailyPassDropsBlocked' -count=1`
Expected: FAIL.

- [ ] **Step 2: Implement**

In `AggregateProductDay`, inside the tx and before the `COUNT(*)`:

```go
// Blocked names (event rules D10) leave with the day before it is rolled
// up: the same transaction deletes every raw row of the day below, so
// this deletes nothing that would have stayed.
if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE family='product' AND project_id=? AND day=?
	AND event_name IN (SELECT name FROM event_rules WHERE project_id=? AND family='product' AND action='block')`,
	projectID, day.String(), projectID); err != nil {
	return err
}
```

The existing `n == 0` path then returns before any rollup. That path already ends the transaction with nothing left to delete, because the rows it counted are gone, so the commit keeps the delete. Do the same in `AggregateMeasureDay` with `family='measures'`.

`DropBlockedAggregates` gets four constant DELETEs in one tx, each like:

```sql
DELETE FROM agg_product_daily WHERE project_id=?1 AND event_name IN
  (SELECT name FROM event_rules WHERE project_id=?1 AND family='product' AND action='block')
```

The other three: `agg_product_attrs` (product), and `agg_measures_daily` and `agg_measures_attrs` (measures). Its doc comment says it skips `agg_product_totals` on purpose (D11).

In `jobs.go`, add `DropBlockedAggregates(ctx context.Context, projectID int64) error` to `Store`, with a one-line comment. In `RunDailyPass`, call it per project right after `identityDays` is handled and before `ViewDaysBefore`. Log failures like the other steps: `r.logger.Error("drop blocked aggregates failed", "project", id, "error", err)`.

- [ ] **Step 3: Run the tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/... ./internal/jobs/... ./internal/sqlcheck/... -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/store internal/jobs
git commit -m "feat(jobs): never aggregate blocked events and drop their aggregates nightly

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: manage — rules in the snapshot, audited operations

**Files:**
- Create: `internal/manage/event_rules.go`
- Create: `internal/manage/event_rules_test.go`
- Modify: `internal/manage/store.go` (`Store` interface)
- Modify: `internal/manage/registry.go` (`Snapshot` gets the rules; `Reload` loads them)
- Modify: every fake implementing `manage.Store` in tests (`grep -rn "func (.*) LoadRegistry(" internal --include=*_test.go`; a fake that embeds the real store needs nothing)

**Interfaces:**
- Consumes: the store methods from Task 1.
- Produces:
  - `manage.Store` gains `LoadEventRules`, `RenameEvent`, `BlockEvent`, `RemoveEventRule` (the Task 1 signatures).
  - `func (s *Snapshot) ResolveEvent(projectID int64, family store.Family, name string) (resolved string, blocked bool)`
  - `func (o *Ops) ListEventRules(ctx context.Context, projectID int64) ([]store.EventRule, error)`
  - `func (o *Ops) RenameEvent(ctx context.Context, actor string, projectID int64, family, from, to string, merge bool) (store.EventRule, map[string]int64, error)`
  - `func (o *Ops) BlockEvent(ctx context.Context, actor string, projectID int64, family, name string) (store.EventRule, error)`
  - `func (o *Ops) RemoveEventRule(ctx context.Context, actor string, projectID int64, family, name string) error`

- [ ] **Step 1: Write the failing tests** in `internal/manage/event_rules_test.go`

Use the real store through `storetest.Open` (check `ops_test.go` and `breakdowns_test.go` for how they build `Registry` and `Ops`). Tests:

1. `TestEventRuleRefusals`: each of these returns `ErrInvalid`:
   - family `views` or `""`;
   - name `""`, a name of whitespace only, or `$page_view`;
   - to `$x`;
   - from equal to to.

   Unknown project 999 returns `ErrNotFound`. Removing a missing rule returns `ErrNotFound`. Renaming onto an existing name without merge returns `ErrConflict`.
2. `TestRenameEventReachesSnapshot`: after `RenameEvent(p, "product", "a", "b", false)`, `snap.ResolveEvent(p, store.FamilyProduct, "a")` returns `("b", false)`. A name without a rule resolves to itself, not blocked.
3. `TestBlockEventReachesSnapshot`: block `b`. Then `ResolveEvent(p, product, "b")` returns `("b", true)`, and with rename a→b in place, `ResolveEvent(p, product, "a")` returns `("b", true)`.
4. `TestRemoveEventRuleReachesSnapshot`: removing the block un-blocks.
5. `TestListEventRules`: returns only that project's rules, ordered by family then name.
6. `TestEventRuleAudit`: the `audit_log` actions are `event.rename`, `event.block` and `event.rule_remove`. The subject is `<project id>/<family>/<name>`. The rename detail is `to <to>`, or `to <to> (merge)` when merging.

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/manage -run 'EventRule|RenameEvent|BlockEvent|RemoveEventRule|ListEventRules' -count=1`
Expected: FAIL.

- [ ] **Step 2: Snapshot**

In `registry.go`:
- Add `rules map[int64]map[ruleKey]store.EventRule` to `Snapshot`, with `type ruleKey struct{ family store.Family; name string }`.
- In `Reload`, after `LoadRegistry`, call `r.st.LoadEventRules(ctx)` (on error: `fmt.Errorf("manage: load event rules: %w", err)`) and fill the map.
- Initialise `rules` in `New` too, as an empty map.
- `withhold` copies the struct, so `rules` is kept.

```go
// ResolveEvent is the name an event is stored under and whether it is
// dropped (event rules D9): a rename rule maps the name once (targets are
// never renamed again, the store keeps chains collapsed), then a block on
// the result drops it.
func (s *Snapshot) ResolveEvent(projectID int64, family store.Family, name string) (string, bool) {
	rs := s.rules[projectID]
	if r, ok := rs[ruleKey{family, name}]; ok && r.Action == store.EventRuleRename {
		name = r.To
	}
	r, ok := rs[ruleKey{family, name}]
	return name, ok && r.Action == store.EventRuleBlock
}
```

`registry.go` must import `store` if it doesn't already.

- [ ] **Step 3: Operations** in `internal/manage/event_rules.go`

```go
package manage

import (
	"context"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ruleFamily is the family a rule may name (event rules D2).
func ruleFamily(f string) (store.Family, error) {
	switch store.Family(f) {
	case store.FamilyProduct, store.FamilyMeasures:
		return store.Family(f), nil
	}
	return "", fmt.Errorf("family %q: rules apply to product and measures events: %w", f, ErrInvalid)
}

// ruleName refuses a name no rule may carry: empty, or reserved ($
// belongs to the server).
func ruleName(n string) error {
	if strings.TrimSpace(n) == "" {
		return fmt.Errorf("event name is required: %w", ErrInvalid)
	}
	if strings.HasPrefix(n, "$") {
		return fmt.Errorf("event %q: $ names are reserved: %w", n, ErrInvalid)
	}
	return nil
}

func ruleSubject(id int64, f store.Family, name string) string {
	return fmt.Sprintf("%d/%s/%s", id, f, name)
}
```

- `ListEventRules`: unknown project (`o.Reg.Snapshot(ctx).Project(id) == nil`) returns `ErrNotFound`. Otherwise filter `o.St.LoadEventRules` to the project, and return an empty non-nil slice when there are none.
- `RenameEvent`: validate the project, the family, both names, and `from != to`. Call the store with the AuditEntry `{Actor: actor, Action: "event.rename", Subject: ruleSubject(...), Detail: "to " + to (+ " (merge)")}`. Then `o.afterWrite(ctx, false, projectID)`, and return `store.EventRule{ProjectID, Family, Name: from, Action: rename, To: to}` with `CreatedAt` read back from `LoadEventRules`. If the read-back fails, return the rule without it.
- `BlockEvent` and `RemoveEventRule`: the same shape. Actions are `event.block` and `event.rule_remove`.

Wrap an unknown project as `fmt.Errorf("project %d: %w", id, ErrNotFound)`.

- [ ] **Step 4: Run the tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/manage/... ./internal/archtest/... -count=1 && go vet ./...`
Expected: PASS. Fix any fake that no longer satisfies `manage.Store`.

- [ ] **Step 5: Commit**

```bash
git add internal/manage
git commit -m "feat(manage): audited event rename and block rules in the registry snapshot

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: server — ingest applies renames and drops blocked events

**Files:**
- Modify: `internal/server/handlers.go` (`handleEvents`, right after `resolveFamily`)
- Test: `internal/server/` (find the existing `handleEvents` tests: `grep -ln "handleEvents\|/api/events" internal/server/*_test.go`)
- Modify: `docs/twillingate.md` (the "Responses and retry" section: one sentence on the blocked warning)

**Interfaces:**
- Consumes: `(*manage.Snapshot).ResolveEvent` from Task 3, and `manage.Ops.BlockEvent` / `RenameEvent` to set rules up in tests.

- [ ] **Step 1: Write the failing test**

`TestIngestAppliesEventRules`: set up a project the way the existing ingest tests do. Rename `old`→`new` and block `spam` through `manage.Ops` (or the store plus `Reg.Reload`). Post one batch: `[{name:"old"}, {name:"spam"}, {name:"keep"}, {name:"spam", family:"measures", value:1, measure:"number"}]`. Expect:
- status 202, `accepted` 4, `rejected` 0;
- exactly one warning, at index 1, reading `event "spam" is blocked in this project, dropped`;
- the enqueued events (the test's fake queue) are `new` (product), `keep` (product) and `spam` (measures: the block is product-only).

Also test that a name renamed onto a blocked name is dropped: rename `x`→`spam`, post `x`, and nothing is enqueued.

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/server -run IngestAppliesEventRules -count=1`
Expected: FAIL.

- [ ] **Step 2: Implement**

After the `famWarn` block in `handleEvents`:

```go
		// Event rules (spec 2026-10-11, D9): a renamed name is stored
		// under its new one; a blocked one is accepted, so the client does
		// not retry, and dropped, with a warning for whoever integrates.
		name, blocked := snap.ResolveEvent(p.ID, family, name)
		if blocked {
			res.warn(i, "event %q is blocked in this project, dropped", name)
			res.Accepted++
			continue
		}
```

Use `=` rather than `:=` if `name` is already declared in that scope (it is, from `resolveFamily`). Views never match a rule because their family is views, and `$` names have no rules.

- [ ] **Step 3: Document** in `docs/twillingate.md` under "### Responses and retry": a short paragraph saying an event blocked in the project is accepted (counted in `accepted`), not stored, and listed in `warnings` as `event "<name>" is blocked in this project, dropped`. Link to the "Event rules" section that Task 5 adds (`[event rules](#event-rules)`).

- [ ] **Step 4: Run the tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/server/... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server docs/twillingate.md
git commit -m "feat(server): store renamed events under their new name and drop blocked ones

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: api — four tools, the `event_names` route, docs

**Files:**
- Create: `internal/api/ops_event_rules.go`
- Create: `internal/api/ops_event_rules_test.go`
- Modify: `internal/api/ops_read.go` (`register`: expose the tools after `enable_ingest_key`)
- Modify: `docs/twillingate.md` (the "Set up a project" table near line 60, a new `### Event rules` section after `### Ingest keys`, the tool list near line 1251, the HTTP API route table near line 1308)

**Interfaces:**
- Consumes: the `manage.Ops` methods from Task 3.
- Produces (JSON the web app relies on in Task 6):
  - `GET /api/projects/{project_id}/event-rules` → `{"rules":[{"family":"product","name":"a","action":"rename","to":"b","created_at":"2026-10-11T10:00:00Z"}]}` (`to` omitted for a block)
  - `POST …/event-rules/rename` body `{"family","from","to","merge"?}` → `{"rule":{…},"rewritten":{"events":3,"agg_product_daily":1,…}}`
  - `POST …/event-rules/block` body `{"family","name"}` → `{"rule":{…}}`
  - `POST …/event-rules/remove` body `{"family","name"}` → `{"status":"removed"}`
  - `GET …/event-names?from=YYYY-MM-DD&to=YYYY-MM-DD` → `{"events":[{"family":"product","name":"checkout","events":120}]}`, ordered by events descending, then name

- [ ] **Step 1: Write the failing tests** in `ops_event_rules_test.go`

Follow `ops_manage_test.go` and `rest_test.go` for building a host and calling both MCP and REST.
1. Rename over REST with name `checkout/v2 é` (Review Focus 1), then list: the rule comes back with the name unchanged. Remove over REST with the same name, then list is empty.
2. Block over MCP, then list: `action` is `block` and there is no `to` key in the JSON.
3. Refusals over REST: family `views` gives 400; unknown project gives 404 (the same shape as the other project routes); rename onto an existing name without merge gives 409; with `merge:true` it gives 200 and `rewritten` sums appear.
4. `event_names`: seed product events `a` ×2 and `b` ×1, plus a measure `m` ×3, in the range. The result is `[{measures,m,3},{product,a,2},{product,b,1}]`. A bad range gets the same refusal `product_events` gives (`checkRange`).

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/api -run 'EventRule|EventNames' -count=1`
Expected: FAIL.

- [ ] **Step 2: Implement `ops_event_rules.go`**

```go
package api

import (
	"context"
	"strconv"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

type eventRuleOut struct {
	Family    string `json:"family"`
	Name      string `json:"name"`
	Action    string `json:"action"`
	To        string `json:"to,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

func ruleOut(r store.EventRule) eventRuleOut {
	return eventRuleOut{Family: string(r.Family), Name: r.Name, Action: r.Action, To: r.To, CreatedAt: r.CreatedAt}
}

type listEventRulesOut struct {
	Rules []eventRuleOut `json:"rules"`
}

type renameEventIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Family    string `json:"family" jsonschema:"product or measures"`
	From      string `json:"from" jsonschema:"the event name to rename; not a $ name"`
	To        string `json:"to" jsonschema:"the new name; not a $ name"`
	Merge     bool   `json:"merge,omitempty" jsonschema:"set true to rename onto a name that already has events: the two merge and cannot be split again; merged days' unique counts become an upper bound"`
}

type eventNameIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Family    string `json:"family" jsonschema:"product or measures"`
	Name      string `json:"name" jsonschema:"the event name; not a $ name"`
}

type renameEventOut struct {
	Rule      eventRuleOut     `json:"rule"`
	Rewritten map[string]int64 `json:"rewritten"`
}

type blockEventOut struct {
	Rule eventRuleOut `json:"rule"`
}

type eventNameOut struct {
	Family string `json:"family"`
	Name   string `json:"name"`
	Events int64  `json:"events"`
}

type eventNamesOut struct {
	Events []eventNameOut `json:"events"`
}
```

Handlers: `listEventRules(ctx, idIn)`, `renameEvent(ctx, renameEventIn)`, `blockEvent(ctx, eventNameIn)`, `removeEventRule(ctx, eventNameIn)` (returns `okOut{Status: "removed"}`), and `eventNames(ctx, rangeIn)`. Each calls the `h.ops` method with `actorFrom(ctx)` and passes errors through `h.projectErr(ctx, in.ProjectID, err)`. Make sure `ErrInvalid` and `ErrConflict` reach the existing refusal mapping (`refusal.go`) unchanged. `eventNames` calls `h.checkRange`, then `h.run` with:

```sql
SELECT family, name, events FROM (
  SELECT 'product' AS family, event_name AS name, SUM(count) AS events
    FROM v_product_daily WHERE project_id=?1 AND day BETWEEN ?2 AND ?3 GROUP BY event_name
  UNION ALL
  SELECT 'measures', event_name, SUM(samples)
    FROM v_measures_daily WHERE project_id=?1 AND day BETWEEN ?2 AND ?3 GROUP BY event_name)
ORDER BY events DESC, name
```

Parse `events` with `strconv.ParseInt`. `Rows` are strings, so check how `projectActivity` reads `res.Rows`.

Registration in `register`, after `enable_ingest_key`:

```go
	expose(r, spec{Name: "list_event_rules", Annotations: ro, Method: "GET", Path: p + "/event-rules",
		Description: "List a project's event rules: renames (name → to) and blocks, product and measures families."},
		h.listEventRules)
	expose(r, spec{Name: "rename_event", Annotations: write, Method: "POST", Path: p + "/event-rules/rename",
		Description: "Rename a product or measures event: rewrites its stored events and aggregates, and stores later events sent under the old name under the new one. Refused onto a name with events unless merge is true (a merge cannot be undone). Widget SQL naming the old event is not rewritten."},
		h.renameEvent)
	expose(r, spec{Name: "block_event", Annotations: idem, Method: "POST", Path: p + "/event-rules/block",
		Description: "Block a product or measures event: ingest accepts and drops it (with a warning), the next daily pass deletes its aggregates, and its stored raw events expire with the raw window. Nothing else is deleted. remove_event_rule unblocks."},
		h.blockEvent)
	expose(r, spec{Name: "remove_event_rule", Annotations: idem, Method: "POST", Path: p + "/event-rules/remove",
		Description: "Remove a project's rule for an event name: a rename stops mapping (history stays renamed), a block lets the event be stored again."},
		h.removeEventRule)
	restOnly(r, spec{Name: "event_names", Method: "GET", Path: p + "/event-names",
		Description: "A project's product and measures event names in a range with their event counts, busiest first. For the console's Settings page; not an MCP tool (product_events and measures answer agents)."},
		h.eventNames)
```

- [ ] **Step 3: Docs** in `docs/twillingate.md`
- In the "Set up a project" table, add rows: "Rename an event", "Block an event", "List / remove event rules". Use `—` in the CLI column, or follow the table's convention for MCP-only rows.
- Add a `### Event rules` section after `### Ingest keys`, written as a contract, covering:
  - D2 scope;
  - rename semantics (D4–D6: history rewritten, later events mapped, merge only with `merge`, merged unique counts as an upper bound, widget SQL not rewritten, chains collapse, renaming back);
  - block semantics (D8–D12: dropped at ingest with a warning, aggregates deleted at the next daily pass, raw events expire within `RETENTION_RAW_DAYS`, nothing deleted on request, unblocking before the next pass loses nothing);
  - removing a rule.
- Add the four tool names to the tool list near line 1251.
- Add five rows to the HTTP API route table near line 1308. Mark `event_names` REST-only the way `project_activity` is marked.

Run `go test ./internal/api -run DocsSync -count=1` and fix whatever it names.

- [ ] **Step 4: Run the tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/api/... ./internal/sqlcheck/... -count=1`
Expected: PASS, including the OpenAPI, coverage and docs-sync tests.

- [ ] **Step 5: Commit**

```bash
git add internal/api docs/twillingate.md
git commit -m "feat(api): rename_event, block_event, list_event_rules and remove_event_rule

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: web — the "Events" section on the Settings page

**Files:**
- Modify: `web/src/lib/api.ts` (types and endpoints)
- Modify: `web/src/lib/queries.ts` (`eventRulesQuery`, `eventNamesQuery`)
- Modify: `web/src/hooks/use-project-actions.ts` (`renameEvent`, `blockEvent`, `removeEventRule`)
- Create: `web/src/components/projects/EventsSection.tsx`
- Create: `web/src/components/projects/RenameEventDialog.tsx`
- Create: `web/src/components/projects/EventsSection.test.tsx`
- Modify: `web/src/components/projects/SetupTab.tsx` (render between `BreakdownsSection` and `KeysSection`)

**Interfaces:**
- Consumes: the JSON from Task 5.
- Produces, in `api.ts`:
  ```ts
  export type EventFamily = 'product' | 'measures'
  export interface EventRule { family: EventFamily; name: string; action: 'rename' | 'block'; to?: string; created_at?: string }
  export interface EventName { family: EventFamily; name: string; events: number }
  // endpoints:
  eventRules: (id: number) => api<{ rules: EventRule[] }>(`/api/projects/${id}/event-rules`),
  eventNames: (id: number, q: RangeQuery) => api<{ events: EventName[] }>(`/api/projects/${id}/event-names${toQuery(q)}`),
  renameEvent: (id: number, body: { family: EventFamily; from: string; to: string; merge?: boolean }) =>
    api<{ rule: EventRule; rewritten: Record<string, number> }>(`/api/projects/${id}/event-rules/rename`, json('POST', body)),
  blockEvent: (id: number, body: { family: EventFamily; name: string }) =>
    api<{ rule: EventRule }>(`/api/projects/${id}/event-rules/block`, json('POST', body)),
  removeEventRule: (id: number, body: { family: EventFamily; name: string }) =>
    api<{ status: string }>(`/api/projects/${id}/event-rules/remove`, json('POST', body)),
  ```
- Query keys: `['event-rules', id]` and `['event-names', id, from, to]`. `ProjectActions` gains:
  - `renameEvent(id, body): Promise<boolean>`
  - `blockEvent(id, body): Promise<boolean>`
  - `removeEventRule(id, body): Promise<boolean>`

  Each invalidates `['event-rules', 'event-names', 'usage']`. The toasts read `Renamed X to Y` (or `Merged X into Y`), `Blocked X`, and `Removed the rule for X`.

- [ ] **Step 1: Write the failing tests** in `EventsSection.test.tsx`

Follow `BreakdownsSection.test.tsx` for rendering with a QueryClient and mocking `endpoints`.
1. It lists the names from `eventNames` with their counts and family; a name with a block rule shows a "blocked" badge.
2. **Rename… → dialog.** Typing a new name that is not in the list gives a button labelled "Rename". Submitting calls `onRename` with `{family, from, to}` and no `merge`.
3. **Rename onto an existing name of the same family.** The dialog shows the merge warning ("merges … cannot be undone"), the button reads `Merge into <to>`, and the call carries `merge: true`.
4. **The rename dialog always says** that widget SQL naming the old event is not rewritten.
5. **Block… → confirm dialog.** The text names the next daily pass and the raw retention days from the prop `rawDays`. Confirming calls `onBlock`.
6. **The rules list** shows `a → b` and `x blocked`, each with a Remove button (`aria-label="Remove rule for a"`) that calls `onRemove`.
7. **An empty range** shows "No product events or measures in this range."

Run: `cd web && npx vitest run src/components/projects/EventsSection.test.tsx --testTimeout=30000`
Expected: FAIL.

- [ ] **Step 2: Implement**

`EventsSection` props:

```ts
interface Props {
  project: Project
  range: { from: string; to: string }
  /** RETENTION_RAW_DAYS, for the block dialog. */
  rawDays?: number
  pending?: boolean
  onRename: (body: { family: EventFamily; from: string; to: string; merge?: boolean }) => Promise<boolean>
  onBlock: (body: { family: EventFamily; name: string }) => Promise<boolean>
  onRemove: (body: { family: EventFamily; name: string }) => Promise<boolean>
}
```

Layout, as in `BreakdownsSection`:
- `<section aria-label="Events" className="flex flex-col gap-3 rounded-lg border p-4">`, with the header `<h2 className="text-base font-semibold">Events</h2>`.
- A `Table` with the columns Event (a `code` name with `break-all`, and a muted family label under it), Received (count via the existing number formatter in `lib/format.ts`), and an actions cell. The actions cell holds a `DropdownMenu` (shadcn `dropdown-menu`, already in `components/ui`) with "Rename…" and "Block…". Its trigger is a ghost icon `Button` with `aria-label={`Actions for ${name}`}`.
- On phones, hide the family label column if needed. Run `phone.spec.ts` in Task 7.
- Below the table, a "Rules" subheading (only when there are rules), with one row per rule: `a → b` or `x blocked`, plus a ghost `Remove` button.
- Use `LoadError` for query errors, as `BreakdownsSection` does.

`RenameEventDialog`: a shadcn `Dialog` with an `Input` holding the new name (trimmed).
- Show a merge warning when the trimmed value equals another name of the same family in the list.
- Disable submit when the value is empty, starts with `$`, or equals the old name.
- The button label is `Merge into ${to}` when merging, else `Rename`.
- The description always ends with: "Widgets whose SQL names the old event are not changed."

Block confirmation: an `AlertDialog`, as in `BreakdownsSection`'s remove dialog.
- Title: `Block ${name}?`
- Description: `Events named ${name} are dropped when they arrive. The next daily pass (03:00 UTC) deletes its aggregated history; stored raw events expire within ${rawDays} days. Remove the rule to receive it again.` Without `rawDays`, say "within the raw retention window".
- Action label: "Block event".

Keep the title in state through the close animation, as `BreakdownsSection` does with `removing`.

In `SetupTab.tsx`, render `<EventsSection project={project} range={range} rawDays={…} pending={actions.pending} onRename={(b) => actions.renameEvent(id, b)} onBlock={(b) => actions.blockEvent(id, b)} onRemove={(b) => actions.removeEventRule(id, b)} />` between Breakdowns and Keys. For `rawDays`: check whether `DashboardsResponse` or `LimitsResponse` already exposes the raw retention (`grep -n "raw_days" web/src/lib/api.ts`). Use it if so; otherwise leave it undefined.

- [ ] **Step 3: Run the web checks**

Run: `cd web && npx tsc -b --noEmit 2>/dev/null || npm run typecheck; npx vitest run --testTimeout=30000`
Expected: PASS. Fix `use-project-actions.test.tsx` if its `ProjectActions` fixture needs the new members.

- [ ] **Step 4: Commit**

```bash
git add web/src
git commit -m "feat(web): rename and block events from a project's settings

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: e2e and the full check

**Files:**
- Create: `web/e2e/event-rules.spec.ts`
- Possibly modify: `web/e2e/serve.sh` or the seed it uses, if the e2e server has no product events (check `grep -n "product\|track" web/e2e/serve.sh web/e2e/*.ts`)
- Possibly modify: `web/e2e/phone.spec.ts` (only if the Settings page visit needs product events seeded to render the section)

- [ ] **Step 1: Write the e2e spec**

Follow `projects.spec.ts` for login and navigation to a project's Settings tab. Seed product events through the ingest API with the project's key (see how other specs post events; `forms.ts` or `vitals.spec.ts` may have a helper), e.g. `signup` ×2 and `spam` ×1. Then:
1. Open Settings. The Events section lists `signup` and `spam`.
2. `spam` → Block… → "Block event". A toast appears and the rules list shows `spam blocked`.
3. Post `spam` again through ingest: the response carries the blocked warning.
4. `signup` → Rename… → type `sign_up` → Rename. After the refetch, the list shows `sign_up` with 2 events and the rules show `signup → sign_up`.
5. Remove the `spam` rule; the rules list no longer shows it.

- [ ] **Step 2: Run everything**

```bash
export PATH=$PATH:/usr/local/go/bin
make check
cd web && npm run e2e -- event-rules.spec.ts phone.spec.ts cursor.spec.ts
```

Expected: all PASS. If port 18080 is held by a stale server from another session, use the e2e config's port override rather than killing it.

- [ ] **Step 3: Commit**

```bash
git add web/e2e
git commit -m "test(web): rename and block events end to end

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
