# Project attributes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A per-project attributes catalog with a breakdown flag (replacing `projects.attributes`), attribute rules (rename, merge, block) stored beside event rules in one `rules` table, `received_attributes` renamed to `usage_attributes` (table) and `list_attributes` (tool), and one Attributes table on the console's Settings page.

**Architecture:** Migration 040 does all the schema work: it rebuilds `event_rules` as `rules`, renames `received_attributes` to `usage_attributes`, creates `project_attributes`, and drops `projects.attributes`. The store owns the catalog upkeep, the attribute rewrite and the insert-time mapping. `manage` validates, audits and carries the rules in the snapshot. The ingest handler maps keys from the snapshot. `api` exposes `list_attributes` and four rule tools. The web app replaces `BreakdownsSection` with `AttributesSection`.

**Tech Stack:** Go with modernc SQLite (`internal/store/sqlite`); React + TypeScript + TanStack Query + shadcn/ui (`web/`); Playwright e2e.

**Spec:** `docs/superpowers/specs/2026-10-11-project-attributes-design.md` (D1–D17). Read it before any task. The event rules this mirrors are `internal/store/sqlite/event_rules.go`, `internal/manage/event_rules.go`, `internal/api/ops_event_rules.go` and `web/src/components/projects/EventsSection.tsx`. Read the matching one before writing its attribute twin.

## Global Constraints

- Go is at `/usr/local/go/bin`, which is not on PATH. Run `export PATH=$PATH:/usr/local/go/bin GOTMPDIR=$HOME/.cache/gotmp` first.
- The machine is loaded: run vitest with `--testTimeout=30000`.
- **Rules apply to custom keys only.** An empty key, a key starting with `$`, and a key containing `"` are `manage.ErrInvalid` (D5). Rules cover product and measures rows, never views.
- Refusals are typed: `manage.ErrInvalid`, `manage.ErrNotFound`, `manage.ErrConflict` (the latter two are `store.ErrNotFound` and `store.ErrConflict`), matched with `errors.Is`, never on message text.
- **The target wins.** When a row or an event carries both `from` and `to`, `to`'s value is kept and `from` is removed. When several keys renamed to one target arrive without it, the first in sorted key order wins.
- Nothing is stripped from stored raw rows on request (D11). The only new deletes:
  - the `from` key during a rename or merge;
  - the blocked keys' aggregate rows in `DropBlockedAggregates`;
  - the catalog prune in the daily pass.
- **Performance (user requirement):** a project with no attribute rules must pay at most one map lookup per event at ingest, and one query per project per flushed batch at the store. `BenchmarkWriteEvents` must show no regression in time or allocations against `~/.cache/twbench/write-base.txt`, which was taken on main before this work.
- Every rule write goes through `auditAndBump` (an audit row plus a `config_version` bump).
- A test that needs a migrated database copies one: `newTestDB`/`snapshotAt` inside `store/sqlite`, `storetest.Open` elsewhere. It never migrates its own (CLAUDE.md, race-suite timeouts).
- **Migration tests pin their ceiling.** A test for migration N migrates only to N, so a later migration cannot break it. Any existing test that writes `projects.attributes`, `event_rules` or `received_attributes` at the latest schema must move to the new tables.
- SQL passed to `database/sql` is constant strings where possible. `internal/sqlcheck` prepares each one against the migrated schema in `make check`; SQL built at run time goes in its `dynamic` list with the reason.
- **Commits** use Conventional Commits with the scopes from CLAUDE.md, and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. The PR title already carries the `!`, so commits on the branch use plain `feat`/`refactor`/`test`.
- Web: every clickable element is a `button`, a link or `role="button"`. Nothing scrolls sideways on a 360px phone.
- Docs change in the same commit as the code: `docs/twillingate.md` (bound by `internal/api/docs_sync_test.go`) and `deploy/UPGRADES.md`.

## Review Focus

1. **Keys with `/`, spaces, unicode or dots** (`checkout/v2`, `plan type`, `é`, `a.b`). They must rename and remove through REST bodies, and the raw rewrite must address the key itself, not a nested path: `a.b` must not touch `{"a":{"b":..}}`. JSON paths are quoted as `$."a.b"`. Test owner: Task 3 (store), with a REST case in Task 6.
2. **A batch where one event carries both `from` and `to`, another carries only `from`, and another carries a blocked key.** The target wins on the first, the second moves, the blocked key is stripped, and there is exactly one block warning per key per batch. Test owner: Task 5.
3. **Renaming back after a chain** (A→B, B→C, then C→A). The rules end as B→A and C→A, with no rule on A, so A's events aren't mapped away. Test owner: Task 3.
4. **A breakdown key renamed or merged.** Its catalog row moves to `to` with `breakdown = 1`, `list_projects` shows `to`, `v_events_flat` gains `attr_to`, and the budget count is unchanged. Merging two breakdown keys frees one. Test owner: Task 3 (store) and Task 4 (manage).
5. **A project without rules, written at volume.** No extra query per event, no extra allocations: the benchmark guards it. Test owner: Task 3.

---

### Task 1: Migration 040 and the move to `rules`, `usage_attributes`, `project_attributes` (no behaviour change)

**Files:**
- Create: `internal/store/sqlite/migrations/040_project_attributes.sql`, `internal/store/sqlite/migration040_test.go`
- Rename: `internal/store/sqlite/received_attributes.go` → `usage_attributes.go`, `received_attributes_test.go` → `usage_attributes_test.go`. Keep the Go identifiers (`receivedKey`, `receivedSeen`, `writeReceived`, `countReceived`), which name what ingest receives; only the table name changes in their SQL and comments.
- Modify: `internal/store/sqlite/event_rules.go`, `write.go`, `aggregate_product.go`, `aggregate_measures.go` (`event_rules` → `rules` with `kind='event'`)
- Modify: `internal/store/sqlite/registry.go`: `LoadRegistry`, `insertProject`, `UpdateProject`; `projectTables` (`event_rules` → `rules`, `received_attributes` → `usage_attributes`, add `project_attributes`)
- Modify: `internal/store/sqlite/usage_history.go`: `countDeclared` and the key-set CTE near line 220, which read `projects.attributes`; the comments naming `received_attributes`
- Modify: `internal/reporting/dev.go`: `devListProjects` reads breakdown keys from the catalog
- Modify: `internal/store/store.go`: the `RegistryProject.Attributes` comment; the `StatDeclaredAttributes` comment if it names the column
- Modify: `internal/api/ops_received.go`, `ops_usage.go`: table name in SQL only (the tool is renamed in Task 6)
- Modify: tests that write `projects.attributes`, `event_rules` or `received_attributes` at the latest schema: `internal/api/ops_received_test.go`, `seed_test.go`, `internal/app/migrate_test.go`, `internal/reporting/system_test.go`, `internal/jobs/jobs_test.go`, `internal/store/sqlite/views_test.go`, `forms_test.go`, `bench_test.go`, `event_rules_test.go`, `event_rules_errors_test.go`, `migration039_test.go` (pin to 039 if it isn't already), `migration028_test.go` (pin to 028), `internal/sqlcheck/sqlcheck_test.go`. Find the rest with `grep -rn "received_attributes\|event_rules\|attributes *= *'\|attributes) VALUES" internal --include=*_test.go`.

**Interfaces:**
- `store.RegistryProject.Attributes` stays a JSON array string: the store builds it from `project_attributes WHERE breakdown=1 ORDER BY key` on load, and syncs the catalog from it on create and update. Callers above the store don't change.
- Produces the tables in D1–D4 exactly as the spec writes them.

- [ ] **Step 1: Write migration 040**

`internal/store/sqlite/migrations/040_project_attributes.sql` does the following, in this order:

```sql
-- 040: project attributes (spec 2026-10-11-project-attributes). event_rules
-- becomes rules, with attribute rules beside event rules; received_attributes
-- becomes usage_attributes; project_attributes is each project's catalog of
-- keys with the breakdown flag that projects.attributes held.
CREATE TABLE rules ( ... exactly D1 ... ) WITHOUT ROWID;
INSERT INTO rules (project_id, kind, family, name, action, to_name, created_at)
  SELECT project_id, 'event', family, name, action, to_name, created_at FROM event_rules;
DROP TABLE event_rules;

ALTER TABLE received_attributes RENAME TO usage_attributes;

CREATE TABLE project_attributes ( ... exactly D3 ... ) WITHOUT ROWID;
INSERT OR IGNORE INTO project_attributes (project_id, key)
  SELECT DISTINCT project_id, attr_key FROM usage_attributes;
INSERT INTO project_attributes (project_id, key, breakdown)
  SELECT DISTINCT p.id, j.value, 1
  FROM projects p, json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
  WHERE j.type = 'text' AND true
  ON CONFLICT (project_id, key) DO UPDATE SET breakdown = 1;

DROP VIEW v_product_attrs;
DROP VIEW v_measures_attrs;
ALTER TABLE projects DROP COLUMN attributes;
-- v_product_attrs and v_measures_attrs: 038's text, with the declared-key
-- source read from the catalog.
CREATE VIEW v_product_attrs AS ...;
CREATE VIEW v_measures_attrs AS ...;
```

Copy both views' full text from `038_product_country.sql`. In each, replace the subquery
`SELECT p.id AS project_id, j.value AS attr_key FROM projects p, json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j WHERE j.type = 'text'`
(read the exact text there) with
`SELECT project_id, key AS attr_key FROM project_attributes WHERE breakdown = 1`.
Leave everything else byte-identical. If `DROP COLUMN` fails because another schema object names `projects.attributes`, find it with `SELECT name, sql FROM sqlite_master WHERE sql LIKE '%attributes%'` and recreate it the same way. Check `migrate.go` for how migrations are listed; 039 shows the pattern.

- [ ] **Step 2: Write `migration040_test.go`** (pinned at 040; model on `migration039_test.go`, starting from `snapshotAt(t, 39)`). Seed:
  - two projects, one with `attributes = '["plan","$host"]'` and one with `'not json'`;
  - an `event_rules` rename and block;
  - `received_attributes` rows for `plan`, `tier` (project 1) and `x` (project 2);
  - raw product rows carrying `plan`, so `v_product_attrs` answers.

  Record `SELECT * FROM v_product_attrs ORDER BY 1,2,3,4,5` before migrating. Migrate to 40 and assert:
  - `rules` holds both event rules with `kind='event'`, and `event_rules` is gone;
  - `usage_attributes` holds the three rows, and `received_attributes` is gone;
  - `project_attributes` holds (1,plan,1), (1,$host,1), (1,tier,0), (2,x,0), with nothing for project 2's malformed list;
  - `pragma_table_info('projects')` has no `attributes`;
  - `v_product_attrs` answers the same rows as before;
  - inserting a `kind='attribute', family='product'` rule fails the CHECK, and `kind='attribute', family='event'` succeeds.

- [ ] **Step 3: Run** `go test ./internal/store/sqlite/ -run 'Migration040|ProjectTables' -count=1`. Expected: FAIL until Steps 1–2 and the `projectTables` edit are in.

- [ ] **Step 4: Move the store code.**
  - Every `event_rules` query → `rules` with `kind='event'` in WHERE and INSERT. For `write.go`'s lookup: `SELECT to_name FROM rules WHERE project_id=? AND kind='event' AND family=? AND name=? AND action='rename'`. Every `ON CONFLICT (project_id, family, name)` → `(project_id, kind, family, name)`.
  - Every `received_attributes` → `usage_attributes`.
  - `LoadRegistry` selects `COALESCE((SELECT json_group_array(key) FROM (SELECT key FROM project_attributes a WHERE a.project_id = p.id AND a.breakdown = 1 ORDER BY key)), '[]')` in place of `attributes`, aliasing `projects p`.
  - `insertProject` and `UpdateProject` drop `attributes` from their INSERT/UPDATE and call a new helper in the same tx:

```go
// syncBreakdowns makes the catalog's breakdown flags match list (a JSON
// array of keys): listed keys get a row with breakdown 1, the project's
// other rows get 0. Rows left with no breakdown stay while usage_attributes
// still holds the key; the daily pass prunes the rest (spec D3).
func syncBreakdowns(ctx context.Context, tx *sql.Tx, projectID int64, list string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE project_attributes SET breakdown = 0
		WHERE project_id = ?1 AND breakdown = 1 AND key NOT IN (SELECT value FROM json_each(?2))`, projectID, list); err != nil {
		return fmt.Errorf("breakdowns: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_attributes (project_id, key, breakdown)
		SELECT ?1, value, 1 FROM json_each(?2) WHERE type = 'text' AND true
		ON CONFLICT (project_id, key) DO UPDATE SET breakdown = 1`, projectID, list); err != nil {
		return fmt.Errorf("breakdowns: %w", err)
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM project_attributes WHERE project_id = ?1 AND breakdown = 0
		AND NOT EXISTS (SELECT 1 FROM usage_attributes u WHERE u.project_id = ?1 AND u.attr_key = project_attributes.key)`, projectID)
	return err
}
```

  - **`usage_history.go`:**
    - `countDeclared` counts `project_attributes WHERE breakdown = 1` per project, keeping a 0 for projects without any: `SELECT ?1, p.id, ?2, (SELECT COUNT(*) FROM project_attributes a WHERE a.project_id = p.id AND a.breakdown = 1) FROM projects p`.
    - The `keys` CTE's first arm becomes `SELECT project_id, key AS attr_key FROM project_attributes WHERE breakdown = 1`.
  - **`reporting/dev.go`:** select the same `json_group_array` expression as `LoadRegistry`, so the unmarshal stays.

- [ ] **Step 5: Fix the tests that wrote the old tables.** Replace `UPDATE projects SET attributes = '["plan"]' WHERE id = 1` with `INSERT INTO project_attributes (project_id, key, breakdown) VALUES (1, 'plan', 1)`, or go through `UpdateProject` where the test already has a store. Replace `INSERT INTO projects (…, attributes) VALUES (…, '[…]')` by dropping the column and adding catalog rows. A migration test for an older version stays as it is, as long as it pins its ceiling.

- [ ] **Step 6: Run** `make check`. Expected: PASS. `sqlcheck` prepares the new SQL. A failure that names a test still writing an old table means Step 5 missed it.

- [ ] **Step 7: Commit** `refactor(store): keep breakdowns in a project_attributes catalog, rules beside event rules, usage_attributes`

---

### Task 2: Catalog upkeep at ingest and in the daily pass

**Files:**
- Modify: `internal/store/sqlite/usage_attributes.go`: `writeReceived` also inserts catalog rows; `countReceived` heals and prunes the catalog
- Test: `internal/store/sqlite/usage_attributes_test.go`

**Interfaces:** none new.

- [ ] **Step 1: Write the failing tests:**
  - `TestWriteEventsAddsCatalogRows`: `WriteEvents` with a product event carrying `plan` and `$host`, plus a views event carrying `x`. `project_attributes` then holds `plan` and `$host` with breakdown 0, and no `x`.
  - `TestCountReceivedHealsAndPrunesCatalog`:
    - `usage_attributes` has a row for `a` with no catalog row → after the pass, `a` is in the catalog;
    - catalog row `gone`, breakdown 0, no usage row → deleted;
    - catalog row `kept`, breakdown 1, no usage row → stays.

  Use the existing `RecordUsageHistory` entry point that calls `countReceived`, the way the existing tests in that file do.
- [ ] **Step 2: Run** `go test ./internal/store/sqlite/ -run 'CatalogRows|HealsAndPrunes' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - In `writeReceived`, prepare a second statement `INSERT OR IGNORE INTO project_attributes (project_id, key) VALUES (?, ?)` and execute it for each fresh triple. It costs nothing for keys already seen today, since `unseen` filters them out first.
  - At the end of `countReceived`:

```go
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_attributes (project_id, key)
		SELECT DISTINCT project_id, attr_key FROM usage_attributes`); err != nil {
		return fmt.Errorf("attribute catalog: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_attributes WHERE breakdown = 0
		AND NOT EXISTS (SELECT 1 FROM usage_attributes u
		                WHERE u.project_id = project_attributes.project_id AND u.attr_key = project_attributes.key)`); err != nil {
		return fmt.Errorf("attribute catalog: %w", err)
	}
```

- [ ] **Step 4: Run** the two tests, then `go test ./internal/store/sqlite/ -count=1`. Expected: PASS.
- [ ] **Step 5: Run** `go test ./internal/store/sqlite/ -run '^$' -bench 'BenchmarkWriteEvents|BenchmarkCountReceived' -benchmem -count 6` and compare with `~/.cache/twbench/write-base.txt`: medians and allocs/op. allocs/op for `attrs=0/5/20` must not rise by more than the per-fresh-key insert. The benchmark reuses keys, so after the first batch it should not rise at all. Note the numbers in the commit body.
- [ ] **Step 6: Commit** `feat(store): keep the attribute catalog current at ingest and in the daily pass`

---

### Task 3: Store: attribute rules, the rewrite, the insert-time mapping

**Files:**
- Modify: `internal/store/store.go`: add the types below
- Create: `internal/store/attributes.go` (the pure mapping function) and `internal/store/attributes_test.go`
- Create: `internal/store/sqlite/attribute_rules.go`, `attribute_rules_test.go`
- Modify: `internal/store/sqlite/event_rules.go`: `DropBlockedAggregates` also drops blocked keys
- Modify: `internal/store/sqlite/write.go`: `insertEvents` maps attributes
- Modify: `internal/store/sqlite/usage_attributes.go`: `receivedSeen.forget(projectID)`
- Modify: `internal/store/sqlite/bench_test.go`: the rules case and a rewrite benchmark

**Interfaces (produces):**

```go
// internal/store/store.go

// AttributeRule is an attribute rule (rules.kind = 'attribute', family
// 'event'): a rename of Key to To, or a block of Key (To "").
type AttributeRule struct {
	ProjectID int64
	Key       string
	Action    string // EventRuleRename or EventRuleBlock: the same two actions
	To        string
	CreatedAt string // RFC 3339 UTC
}

// AttributeRuleSet is one project's attribute rules as the write path
// applies them: Renamed maps a key to its target, Blocked holds the
// blocked keys. The zero value maps nothing.
type AttributeRuleSet struct {
	Renamed map[string]string
	Blocked map[string]bool
}

// Empty reports whether the set maps nothing, the fast path.
func (r AttributeRuleSet) Empty() bool { return len(r.Renamed) == 0 && len(r.Blocked) == 0 }
```

```go
// internal/store/attributes.go

// MapAttributes applies a project's attribute rules to an event's custom
// keys (spec D9): a key renamed away moves to its target unless the event
// carries the target (the target wins; several keys renamed to one target
// with none sent: the first in key order wins), then a blocked key is
// removed. It returns attrs itself when nothing applies — no allocation —
// and otherwise a new map; attrs is never written. dropped lists the
// blocked keys removed, sorted.
func MapAttributes(attrs map[string]string, r AttributeRuleSet) (out map[string]string, dropped []string)
```

On `*sqlite.DB`:
- `LoadAttributeRules(ctx) ([]store.AttributeRule, error)`: every attribute rule, ordered by project_id, name.
- `RenameAttribute(ctx, projectID int64, from, to string, merge bool, a store.AuditEntry) (map[string]int64, error)`
- `BlockAttribute(ctx, projectID int64, key string, a store.AuditEntry) error`
- `RemoveAttributeRule(ctx, projectID int64, key string, a store.AuditEntry) error`: `store.ErrNotFound` when there is no rule.

- [ ] **Step 1: Test and write `MapAttributes`** (`internal/store/attributes_test.go`), table-driven:
  - empty set → the same map (check with `reflect.ValueOf(out).Pointer() == reflect.ValueOf(in).Pointer()`, no allocation);
  - no key affected → the same map;
  - `{a:1}` with `a→b` → `{b:1}`;
  - `{a:1,b:2}` with `a→b` → `{b:2}` (the target wins);
  - `{a1:1,a2:2}` with `a1→b, a2→b` → `{b:1}`;
  - `{x:1,y:2}` with `x` blocked → `{y:2}`, dropped `[x]`;
  - `{a:1}` with `a→b` and `b` blocked → `{}`, dropped `[b]`;
  - the input map unchanged in every case.

  Add `BenchmarkMapAttributesNoRules` (20 keys, empty set) and `BenchmarkMapAttributesUnaffected` (20 keys, rules on other keys). Both must report 0 allocs/op.

```go
func MapAttributes(attrs map[string]string, r AttributeRuleSet) (map[string]string, []string) {
	if r.Empty() || len(attrs) == 0 {
		return attrs, nil
	}
	hit := false
	for k := range attrs {
		if _, ok := r.Renamed[k]; ok || r.Blocked[k] {
			hit = true
			break
		}
	}
	if !hit {
		return attrs, nil
	}
	out := make(map[string]string, len(attrs))
	var moved []string
	for k, v := range attrs {
		if _, ok := r.Renamed[k]; ok {
			moved = append(moved, k)
			continue
		}
		out[k] = v
	}
	sort.Strings(moved)
	for _, k := range moved {
		if to := r.Renamed[k]; to != "" {
			if _, taken := out[to]; !taken {
				out[to] = attrs[k]
			}
		}
	}
	var dropped []string
	for k := range out {
		if r.Blocked[k] {
			dropped = append(dropped, k)
		}
	}
	for _, k := range dropped {
		delete(out, k)
	}
	sort.Strings(dropped)
	return out, dropped
}
```

- [ ] **Step 2: Write the failing store tests** in `attribute_rules_test.go` (helpers: `newTestDB`, `WriteEvents`, plain INSERTs for aggregates):
  1. `TestRenameAttributeRewritesEverything`: raw product rows `{a:"1"}`×2, `{a:"2",b:"9"}`×1, a measures row `{a:"3"}`, and a views row `{a:"v"}` (untouched); `agg_product_attrs` and `agg_measures_attrs` rows with `attr_key='a'`; `usage_attributes` rows for `a`; catalog `a` with breakdown 1. `RenameAttribute(1,"a","c",false)` gives:
     - raw rows with `c` (the row that also carried `b` keeps `b`), and the views row unchanged;
     - aggregates under `c`;
     - usage rows under `c`;
     - catalog `c` with breakdown 1, and no row for `a`;
     - the rule `{1, a, rename, c}` from `LoadAttributeRules`;
     - an audit row;
     - result counts `{"events":4,"agg_product_attrs":N,"agg_measures_attrs":M,"usage_attributes":K}`.
  2. `TestRenameAttributeTargetWins`: a row `{a:"1", b:"2"}` merged a→b becomes `{b:"2"}`.
  3. `TestRenameAttributeRefusesTakenTarget`: `b` has a usage row, then only an `agg_product_attrs` row. Rename without merge is `ErrConflict` both times, and nothing changes.
  4. `TestMergeAttributeSumsAggregates`: meeting `agg_product_attrs` rows sum `count`, `unique_users` and `unique_groups` (NULL stays NULL); `agg_measures_attrs` sums `samples`, `weight` and `sum`; `usage_attributes` sums `events` and keeps the larger `max_values`, with NULL yielding to a number; catalog breakdown is the OR of both.
  5. `TestRenameAttributeOntoBlockedIsConflict`: with and without merge.
  6. `TestAttributeRuleChains`: a→b, then b→c gives rules a→c and b→c; then c→a gives a→c deleted, b→a and c→a.
  7. `TestAttributeKeyWithDotAndSlash`: keys `a.b` and `x/y`. Renaming `a.b`→`c` leaves a nested `{"a":{"b":"z"}}` row alone; the raw rewrite uses the quoted path.
  8. `TestBlockAttribute`: block `a` (a breakdown with a rename rule pointing at it from `z`) → the rule is `block`, catalog breakdown 0, the rename `z→a` stays. Blocking a renamed-away key replaces its rename. `RemoveAttributeRule` on a key with no rule is `ErrNotFound`.
  9. `TestWriteEventsMapsAttributes`: rule `a→b` and block `x`; `WriteEvents` with `{a:"1", x:"2", y:"3"}` stores `{b:"1", y:"3"}`, and `usage_attributes` and the catalog get `b` and `y`, not `a` or `x`. A second project with no rules stores its attributes unchanged.
  10. `TestDropBlockedAggregatesDropsBlockedKeys`: agg rows for blocked `x` are deleted; other keys and the event aggregates are untouched.
  11. Extend the purge test (`TestProjectTablesMatchesSchema` already covers `rules`): a purged project leaves no `rules` or `project_attributes` rows.
- [ ] **Step 3: Run** `go test ./internal/store/... -run 'Attribute' -count=1`. Expected: FAIL (undefined).
- [ ] **Step 4: Implement `attribute_rules.go`**, mirroring `event_rules.go`'s structure.
  - `jsonPath(key) = "$.\"" + key + "\""`.
  - Raw rewrite:

```sql
UPDATE events SET attributes = CASE
    WHEN json_type(attributes, ?3) IS NULL
      THEN json_set(json_remove(attributes, ?2), ?3, json_extract(attributes, ?2))
    ELSE json_remove(attributes, ?2) END
WHERE project_id = ?1 AND family IN ('product', 'measures') AND json_type(attributes, ?2) IS NOT NULL
```

  with `?2 = jsonPath(from)` and `?3 = jsonPath(to)`.
  - Aggregate moves follow `aggregateMoves`' pattern, keyed on `attr_key`:
    - `agg_product_attrs` PK: `(project_id, day, event_name, attr_key, attr_value)`;
    - `agg_measures_attrs` PK: `(project_id, day, event_name, measure, attr_key, attr_value, bucket)`;
    - `usage_attributes` PK: `(project_id, day, attr_key)`, with `max_values = CASE WHEN max_values IS NULL THEN excluded.max_values WHEN excluded.max_values IS NULL THEN max_values ELSE max(max_values, excluded.max_values) END`.
  - Conflict check without merge: `EXISTS usage_attributes(project, to) OR EXISTS agg_product_attrs(project, attr_key=to) OR EXISTS agg_measures_attrs(project, attr_key=to)`. A blocked `to` is always a conflict.
  - Catalog: read `from`'s breakdown; `DELETE` `from`'s row; if it was a breakdown, `INSERT … ON CONFLICT DO UPDATE SET breakdown = 1` for `to`; otherwise `INSERT OR IGNORE` `to` (it now has the data).
  - Rules, in this order:
    1. `DELETE FROM rules WHERE project_id=?1 AND kind='attribute' AND family='event' AND name=?2 AND action='rename'` with `?2 = to`;
    2. `UPDATE rules SET to_name=?3 WHERE … AND action='rename' AND to_name=?2` (from → to);
    3. upsert `from → to` with `ON CONFLICT (project_id, kind, family, name) DO UPDATE SET action='rename', to_name=excluded.to_name, created_at=excluded.created_at`.
  - Then `auditAndBump`. After commit, `d.seen.forget(projectID)`. Add `forget` to `receivedSeen`: delete every triple whose project matches.
  - `BlockAttribute`: upsert the block, as `BlockEvent` does; then `UPDATE project_attributes SET breakdown = 0 WHERE project_id=? AND key=?`; `auditAndBump`; `forget`.
  - `RemoveAttributeRule`: as `RemoveEventRule`.
  - `DropBlockedAggregates`: append two statements:

```sql
DELETE FROM agg_product_attrs WHERE project_id=?1 AND attr_key IN
    (SELECT name FROM rules WHERE project_id=?1 AND kind='attribute' AND family='event' AND action='block')
DELETE FROM agg_measures_attrs WHERE project_id=?1 AND attr_key IN
    (SELECT name FROM rules WHERE project_id=?1 AND kind='attribute' AND family='event' AND action='block')
```

- [ ] **Step 5: Map at insert** (`write.go`). Before the loop in `insertEvents`, collect the distinct project ids of the product and measures events and load each one's set:

```go
// attributeRules reads the batch's projects' attribute rules in the
// flush's transaction (spec D9): one primary-key range query per project,
// so a rule committed before the flush applies to rows queued earlier.
func attributeRules(ctx context.Context, tx *sql.Tx, evs []store.Event) (map[int64]store.AttributeRuleSet, error)
```

  It runs `SELECT name, action, COALESCE(to_name, '') FROM rules WHERE project_id=? AND kind='attribute' AND family='event'`, and stores a project only when it has rules. Inside the loop, for product and measures events:

```go
		attrs := e.Attributes
		if rs, ok := rules[e.ProjectID]; ok && e.Family != store.FamilyViews {
			attrs, _ = store.MapAttributes(attrs, rs)
			e.Attributes = attrs // e is the loop's copy; received.add reads it
		}
```

  Make sure `received.add(e, day)` sees the mapped `e`.

- [ ] **Step 6: Benchmarks** in `bench_test.go`:
  - **`BenchmarkWriteEvents`** gains a sub-benchmark `attrs=5/rules` that inserts 10 attribute rules for `benchProject` (renames and blocks on keys the events do not carry, plus one on `key01`) before the timer. The existing `attrs=0/5/20` cases stay the no-rules baseline.
  - **`BenchmarkRenameAttribute`:** seed 30 days × 2,000 product events with 5 attributes (reuse `BenchmarkCountReceived`'s seeding), then time one `RenameAttribute` of a key present on every event. Use `b.N` iterations alternating `key01→keyX` and `keyX→key01`.
- [ ] **Step 7: Run** `go test ./internal/store/... -count=1` (PASS), then:

```bash
go test ./internal/store/sqlite/ -run '^$' -bench 'BenchmarkWriteEvents|BenchmarkRenameAttribute' -benchmem -count 6 > ~/.cache/twbench/write-task3.txt
go test ./internal/store/ -run '^$' -bench MapAttributes -benchmem
```

  `attrs=0/5/20` allocs/op must equal the baseline's, give or take 0.5% for the per-batch rules query. A larger rise is a failure to fix, not to note. Put the medians, both files' allocs and the rename time in the commit body.

- [ ] **Step 8: Commit** `feat(store): rename, merge and block attribute keys, mapped at insert`

---

### Task 4: Manage: snapshot rules, operations, breakdown refusals

**Files:**
- Create: `internal/manage/attribute_rules.go`, `attribute_rules_test.go`
- Modify: `internal/manage/store.go` (interface), `registry.go` (snapshot), `ops.go` (`validate`/`checkBreakdowns` refusal)
- Modify: every fake implementing `manage.Store` in tests (`grep -rn "LoadEventRules" internal --include=*_test.go`)

**Interfaces:**
- Consumes the Task 3 store methods.
- Produces:

```go
// Snapshot
func (s *Snapshot) AttributeRules(projectID int64) store.AttributeRuleSet // zero value when none
func (s *Snapshot) AttributeRuleList(projectID int64) []store.AttributeRule // ordered by key

// Ops
func (o *Ops) RenameAttribute(ctx context.Context, actor string, projectID int64, from, to string) (store.AttributeRule, map[string]int64, error)
func (o *Ops) MergeAttribute(ctx context.Context, actor string, projectID int64, from, to string) (store.AttributeRule, map[string]int64, error)
func (o *Ops) BlockAttribute(ctx context.Context, actor string, projectID int64, key string) (store.AttributeRule, error)
func (o *Ops) RemoveAttributeRule(ctx context.Context, actor string, projectID int64, key string) error
```

Audit actions: `attribute.rename`, `attribute.merge`, `attribute.block`, `attribute.rule_remove`. The subject is `"<id>/<key>"`, and Detail is `"to " + to` for rename and merge. Each write calls `o.afterWrite(ctx, true, projectID)`, so `v_events_flat` is rebuilt (a breakdown may have moved).

- [ ] **Step 1: Write the failing tests** (model on `event_rules_test.go` in manage, using its fake or a real store as it does):
  - **refusals:**
    - `""`, `" "`, `"$host"`, `"a\"b"` on either side → `ErrInvalid`;
    - `from == to` → `ErrInvalid`;
    - unknown project → `ErrNotFound`;
    - rename onto a key with data → `ErrConflict`, while merge passes;
    - remove with no rule → `ErrNotFound`;
  - **snapshot:** after a rename and a block, `AttributeRules(id)` maps and blocks, and another project's set is empty;
  - **breakdowns:** `UpdateProject` with `attributes` listing a blocked key or a renamed-away key → `ErrConflict`. A list that doesn't is fine, and comes back ordered by key: `["tier","plan"]` reads back `["plan","tier"]`;
  - **budget:** merging two breakdown keys leaves `BreakdownsInUse` one lower.
- [ ] **Step 2: Run** `go test ./internal/manage/ -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - `Reload` loads `LoadAttributeRules` into `attrRules map[int64]store.AttributeRuleSet` and `attrRuleList map[int64][]store.AttributeRule`, initialized empty in `New` like `rules`.
  - Key validation: `attrKey(k string) error`.
  - In `UpdateProject` and `create`, after `validate`, call `o.checkRuledBreakdowns(snap, projectID, spec.Attributes)`. It returns `ErrConflict` naming `key is blocked` or `key is renamed to X` (create has no rules, so skip it there).
  - `writtenAttributeRule` reads `CreatedAt` back, like `writtenRule`.
- [ ] **Step 4: Run** `go test ./internal/manage/ ./internal/... -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(manage): attribute rules in the registry snapshot, audited operations, breakdown refusals`

---

### Task 5: Ingest: map keys, warn on blocked ones

**Files:**
- Modify: `internal/server/handlers.go` (`handleEvents`)
- Test: `internal/server/server_test.go`

- [ ] **Step 1: Write the failing test** `TestIngestAppliesAttributeRules`. Set rules through the test's registry (the event-rules ingest test shows how): `a→b`, block `x`. Post one batch:
  - event 0 `{a:"1",b:"2"}`;
  - event 1 `{a:"3"}`;
  - event 2 `{x:"4",y:"5"}`;
  - event 3 `{x:"6"}`.

  Assert:
  - all four are accepted;
  - the stored attributes are `{b:"2"}`, `{b:"3"}`, `{y:"5"}` and `{}`;
  - `warnings` holds exactly one entry with index 2: ``attribute "x" is blocked in this project, dropped``;
  - a second project in the same test with no rules stores `{a:"1"}` unchanged.
- [ ] **Step 2: Run** `go test ./internal/server/ -run AttributeRules -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.** Before the loop:

```go
	attrRules := snap.AttributeRules(p.ID)
	var warnedBlocked map[string]bool
```

  Just before `e := store.Event{…}`:

```go
		custom := rv.Custom
		if !attrRules.Empty() {
			var dropped []string
			custom, dropped = store.MapAttributes(custom, attrRules)
			for _, k := range dropped {
				if !warnedBlocked[k] {
					if warnedBlocked == nil {
						warnedBlocked = map[string]bool{}
					}
					warnedBlocked[k] = true
					res.warn(i, "attribute %q is blocked in this project, dropped", k)
				}
			}
		}
```

  Use `Attributes: custom` in the literal.
- [ ] **Step 4: Run** `go test ./internal/server/ ./internal/jobs/ -count=1`. Expected: PASS.
- [ ] **Step 5: Jobs test.** In `internal/jobs/jobs_test.go`, add `TestDailyPassDropsBlockedAttributeAggregates`: an `agg_product_attrs` row for blocked key `x` and one for `y`. After `RunDailyPass`, only `y` remains. It goes through the existing `DropBlockedAggregates` call; no jobs code change is expected. Run it.
- [ ] **Step 6: Commit** `feat(server): map renamed attribute keys and strip blocked ones at ingest`

---

### Task 6: API: `list_attributes`, the four rule tools, docs

**Files:**
- Rename: `internal/api/ops_received.go` → `ops_attributes.go`, and its test → `ops_attributes_test.go`
- Create: `internal/api/ops_attribute_rules.go`, `ops_attribute_rules_test.go`
- Modify: `internal/api/ops_read.go` (the `expose` registrations and the `received_attributes` description), `ops_usage.go` (`unused_attributes` comment), `ops_manage.go` (the `attributes` jsonschema: "pick keys from list_attributes"), `expose_test.go` (tool and route counts), `docs_sync_test.go` if it lists tools
- Modify: `docs/twillingate.md`, `deploy/UPGRADES.md`

**Interfaces:**
- `list_attributes`: `GET /api/projects/{project_id}/attributes`, input `{project_id, from, to}` (the `rangeIn` pattern; `project_id` from the path), ro.

```go
type attributeOut struct {
	Key       string   `json:"key"`
	Events    int64    `json:"events" jsonschema:"…as received_attributes said…"`
	MaxValues *int64   `json:"max_values" jsonschema:"…"`
	Received  bool     `json:"received" jsonschema:"whether any event in the range carried the key; false for a breakdown or blocked key none carried"`
	Breakdown bool     `json:"breakdown" jsonschema:"whether the project breaks the key down; switch it with update_project's attributes"`
	Blocked   bool     `json:"blocked" jsonschema:"whether block_attribute strips the key from events"`
	Aliases   []string `json:"aliases,omitempty" jsonschema:"keys renamed or merged into this one; events sent with them are stored under it"`
}
type listAttributesOut struct {
	ProjectID      int64          `json:"project_id"`
	From, To       string         // json from, to
	Attributes     []attributeOut `json:"attributes" jsonschema:"the 500 busiest keys received in the range, every breakdown and every blocked key, busiest first"`
	KeysTotal      int64          `json:"keys_total"`
	ValuesCap      int            `json:"values_cap"`
	BreakdownsUsed int            `json:"breakdowns_used"`
	BreakdownsMax  int            `json:"breakdowns_max"`
}
```

  The list key is `attributes`, which replaces `keys`. Port `receivedAttributes` into a `listAttributes` that:
  - reads `usage_attributes`;
  - takes breakdown keys from `p.Attributes` and blocked keys and aliases from `snap.AttributeRuleList(id)`;
  - adds blocked keys that weren't received, with `received` false;
  - leaves out a key that is renamed away, even if it has a usage row.
- **Rule tools**, mirroring `ops_event_rules.go`:
  - inputs `{project_id, from, to}` and `{project_id, key}`;
  - outputs `attributeRuleOut{Key, Action, To (omitempty), CreatedAt}`, `{rule, rewritten}`, `{rule}` and `okOut{Status:"removed"}`;
  - `ruleErr` reused for the typed refusals;
  - registered next to the event rules with the annotations from the spec D16 table: `write`, `destroy`, `destroyIdem`, `idem`;
  - paths `p + "/attribute-rules/rename|merge|block|remove"`.
  The old `received_attributes` registration is deleted.

- [ ] **Step 1: Write the failing tests:**
  - `ops_attributes_test.go`: port the received tests to the new tool, name and route. Add:
    - a blocked key with no events listed with `blocked:true, received:false`;
    - `aliases` on the target;
    - the renamed-away key absent;
    - `GET /api/received-attributes` → 404.
  - `ops_attribute_rules_test.go`: each tool over MCP and REST, including:
    - 400 for a `$` key;
    - 404 for an unknown project, and for removing with no rule;
    - 409 for a rename onto a key with data;
    - a key `checkout/v2` renamed and removed through REST bodies;
    - the audit rows.
- [ ] **Step 2: Run** `go test ./internal/api/ -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement**, then update `docs/twillingate.md`:
  - the "Attribute breakdowns" section says breakdowns are rows of the project's attributes catalog, `attributes` comes back ordered by key, and "pick keys from `list_attributes`";
  - a new "Attribute rules" section after "Event rules": D5–D14 in the style of the event rules section, including the target-wins rule, the merge upper bounds, the ingest warning, that widget SQL is not rewritten, and that a block strips the key and never the events;
  - the setup tool table: a row each for rename, merge and block of an attribute, and for removing an attribute rule;
  - the "Answer questions with the data" tool list and count: `received_attributes` is replaced by `list_attributes` and the four rule tools are added, so 61 − 1 + 5 = 65 tools in the count sentence (recount the "twenty-eight below" figure);
  - the REST table: replace the `/api/received-attributes` row, and add the four rule routes after the event-rules ones;
  - every remaining mention of `received_attributes`, which now reads `list_attributes`. The table is `usage_attributes`; mention it only where SQL is discussed.

  `deploy/UPGRADES.md`: an entry for 040 at the top, in the file's existing style, with the renames from the spec's Documentation section and "no pre-checks".
- [ ] **Step 4: Run** `go test ./internal/api/ -count=1` (`docs_sync_test` included), then `make check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(api): list_attributes and attribute rule tools, replacing received_attributes`

---

### Task 7: Web: Attributes table and generalized rule dialogs

**Files:**
- Modify: `web/src/lib/api.ts`:
  - `ReceivedKey` → `ProjectAttribute`, with `breakdown` (was `declared`), `blocked` and `aliases?`;
  - the response type uses `attributes` (was `keys`);
  - endpoints `listAttributes(id, range)` → `/api/projects/${id}/attributes${toQuery(q)}`;
  - `renameAttribute`, `mergeAttribute`, `blockAttribute` and `removeAttributeRule` against `/attribute-rules/...`;
  - `receivedAttributes` removed.
- Modify: `web/src/lib/queries.ts`: `attributesQuery(id, range)` replaces `receivedAttributesQuery`, with key `['attributes', id, from, to]`.
- Modify: `web/src/hooks/use-project-actions.ts` (+ test): `renameAttribute` (`boolean | 'conflict'`, as `renameEvent`), `mergeAttribute`, `blockAttribute`, `removeAttributeRule` and `addAttributeAlias` (a merge from the alias). `ATTRIBUTE_WRITE = ['attributes', 'projects', 'usage']`; the project update that switches a breakdown also invalidates `attributes`.
- Rename and generalize: `RenameEventDialog.tsx` → `RenameDialog.tsx`, `MergeEventDialog.tsx` → `MergeDialog.tsx`, `AddAliasDialog.tsx` → `AliasDialog.tsx`. Props become:

```ts
type RuleNoun = 'event' | 'attribute'
interface Target { name: string; /** shown after the name, e.g. "product"; omitted for attributes */ context?: string }
// RenameDialog: { open, onOpenChange, noun, target, taken: readonly string[] /* names with data */, blocked: readonly string[], pending, onRename }
// MergeDialog:  { open, onOpenChange, noun, target, into: { name: string; count: number }[], pending, onMerge }
// AliasDialog:  { open, onOpenChange, noun, target, taken: readonly string[], aliases: readonly string[], pending, onAdd }
```

  The wording keeps the event texts word for word for `noun='event'`, and swaps "events arriving as X" for "events carrying X" and "event" for "attribute" for attributes. Put the copy in one `ruleCopy(noun)` helper in `rule-copy.ts`. `EventsSection` passes `taken` and `blocked` filtered to the target's family, `into` as `{name, count: events}`, and `context: family`.
- Create: `web/src/components/projects/AttributesSection.tsx` (+ `.test.tsx`)
- Delete: `BreakdownsSection.tsx` (+ test), `AddBreakdownDialog.tsx` (+ test)
- Modify: `SetupTab.tsx`: `AttributesSection` in place of `BreakdownsSection`, after `EventsSection`.
- Modify: `EventsSection.tsx` and its test, to use the generalized dialogs.

**`AttributesSection`** props: `{ project, range, rawDays?, pending?, onSaveBreakdowns(attributes: string[]): Promise<boolean>, onRename, onMerge, onBlock, onRemove, onAddAlias }`, where the last five take `{from,to}`, `{key}` and `{alias,to}` as in `EventsSection`.
- **Data:** `useQuery(attributesQuery(project.project_id, range))`.
- **Rows:** `data.attributes`, already ordered busiest first by the server.
- **Columns:**
  - **Attribute:** `<code>` of the key, the Blocked badge, and the alias chips with ×/+, exactly as `EventsSection` draws them. Below `sm`, a `<span className="sm:hidden block text-xs text-muted-foreground">` repeats the Received text under the key.
  - **Received:** `hidden sm:table-cell`, showing `describeKey(row, data.values_cap)`.
  - **Breakdown:** `<Switch aria-label={`Break down ${key}`} checked={row.breakdown} disabled={pending || row.blocked || (!row.breakdown && atLimit)} />`. Switching on saves `[...project.attributes, key]`. Switching off opens the confirm that `BreakdownsSection` used, with the same wording, then saves the list without the key.
  - **Menu:** only for keys not starting with `$`, the same four items as `EventsSection`.
- **Header:** `Attributes` with ` · ${usedLabel(used, max)}`.
- **Empty state:** "No attributes received in this range."
- The table uses `grid`/`table` classes so nothing overflows at 360px. Follow `EventsSection`'s `whitespace-normal` and `break-all` usage.

- [ ] **Step 1: Write the failing vitest tests** in `AttributesSection.test.tsx`, modelled on `EventsSection.test.tsx`'s mocking:
  - rows render with Received text and switch state;
  - switching on calls `onSaveBreakdowns` with the key added;
  - switching off confirms, then saves without it;
  - the switch is disabled at the limit and on blocked rows;
  - a `$` key row has no menu;
  - the menu opens the rename, merge and alias dialogs with attribute wording;
  - Block confirms and calls `onBlock`; Unblock calls `onRemove`;
  - alias chips render, × confirms and calls `onRemove({key: alias})`.

  Port the dialog tests to the generalized names. `EventsSection.test.tsx` must keep passing unchanged except for imports.
- [ ] **Step 2: Run** `cd web && npx vitest run src/components/projects --testTimeout=30000`. Expected: FAIL.
- [ ] **Step 3: Implement** as above. Run `npx tsc -b` (or the repo's typecheck script in `package.json`) and `npx eslint src`.
- [ ] **Step 4: Run** the vitest suite for `src/components/projects`, `src/hooks` and `src/pages`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): one attributes table with breakdown switches and rename, merge and block`

---

### Task 8: Web e2e, phone and cursor coverage

**Files:**
- Create: `web/e2e/attribute-rules.spec.ts`, modelled on `web/e2e/event-rules.spec.ts` and how it seeds events and opens Settings
- Modify: `web/e2e/projects.spec.ts`, which uses the old Breakdowns UI; move its breakdown steps to the switch
- Modify: `web/e2e/phone.spec.ts`, so the long-names project also sends long attribute keys and the Attributes section is visited

- [ ] **Step 1: Write the e2e test.** Seed product events carrying `planType` and `plan`, then on Settings:
  1. switch `plan`'s breakdown on, and check `list_projects` through the page's state or a reload shows it on;
  2. rename `planType` to `plan_type`;
  3. merge `plan_type` into `plan`: the row is gone and `plan` shows the alias chip `planType`… (aliases follow the chain, so check both chips);
  4. block `debug`: the row shows Blocked and the switch is disabled;
  5. unblock.
- [ ] **Step 2: Build and run.** `make build`, then `cd web && npx playwright test e2e/attribute-rules.spec.ts e2e/projects.spec.ts e2e/phone.spec.ts e2e/cursor.spec.ts`. Port 18080 may be held by a stale server from another session; check with `ss -ltnp | grep 18080` and use the suite's port variable if one exists. Expected: PASS.
- [ ] **Step 3: Commit** `test(web): attribute rules end to end, on a phone and with pointer cursors`

---

### Final: verification (controller)

- `make check` from a clean tree.
- Run `make test`, or at least `go test -race ./internal/store/... ./internal/api/...`, to confirm no package nears the 10-minute race limit.
- `cd web && npx vitest run --testTimeout=30000`, then the whole e2e suite.
- Benchmarks: rerun `BenchmarkWriteEvents` with `-count 6` and put a before/after table in the PR body: the main baseline against the branch for `attrs=0/5/20`, plus `attrs=5/rules`, `BenchmarkRenameAttribute` and `BenchmarkMapAttributes*`.
- Start a test instance (the `twillingate-test-instance` skill) from this branch, with attribute rules seeded on one project, for the user to review the UI.
