# Dashboard Tabs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let dashboards be grouped into one tabbed sidebar entry by a shared `group_id`, with agents creating, moving, copying, archiving and restoring groups over MCP and REST, and the page showing each group's tabs.

**Architecture:** `dashboards.group_id` (migration 022) marks membership; a new group's id is its first dashboard's id. `sort_key` stays one order per owner, and a group's rows (archived included) are always contiguous in it: the sidebar shows each group's first live member, the group's live members are its tabs. `after` names a dashboard; whether a call moves a tab or a whole group follows from which dashboard it names. `whole_group: true` on duplicate/archive/restore acts on the dashboard's group.

**Tech Stack:** Go (store/sqlite, reporting, api), SQLite migrations, React + TypeScript (web/), Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-09-30-dashboard-tabs-design.md` — read it first; decision numbers (D1–D23) below refer to it.

## Global Constraints

- Go is at `/usr/local/go/bin` (not on PATH): `export PATH=$PATH:/usr/local/go/bin` in every shell.
- One `make check` at a time on this machine (shared).
- Conventional Commits, scope `reporting`/`store`/`web`/`api`/`docs`; the squashed PR title will be `feat(reporting): group dashboards into tabs`. Not breaking: no `!`.
- Every commit message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Refusals are typed (`store.ErrInvalid`, `store.ErrNotFound`, `store.ErrConflict` via `store.Refuse`), matched with `errors.Is`, never by text.
- No new Go dependencies. No new MCP tools or REST routes (spec Surfaces).
- `docs/reporting.md` and `deploy/UPGRADES.md` change in the same PR as the behaviour (it is squash-merged into one commit, which satisfies CLAUDE.md's same-commit table); Task 8 writes them.
- Match the surrounding code's comment density and idiom; comments explain why.

## Review Focus

1. **`after` naming the dashboard itself** (`update_dashboard {dashboard_id: 7, after: 7}`): today a no-op; must stay a no-op, never refuse or move the group. Test in Task 3.
2. **Moving a group next to itself** (`after: X` where X is the last member of the group right before this one, i.e. already the position): must succeed without a UNIQUE conflict (same keys). Test in Task 3.
3. **`group_id: G` where G is the dashboard's own group and no `after`**: moves it to the last tab; must not refuse as "already in group". Test in Task 3.
4. **Archiving the last live member of a group** (default, not `whole_group`): the entry disappears from the sidebar; restoring brings it back at the same sidebar position. Test in Task 4.
5. **A system dashboard in a request that could touch it**: `update_dashboard {group_id: 1}` for a user dashboard, and `after: <system id>` for a user dashboard, must be refused, never move a system row. Test in Task 3.

---

## File map

| File | Change |
| --- | --- |
| `internal/store/sqlite/migrations/022_dashboard_groups.sql` | new: the column and index |
| `internal/store/sqlite/migration022_test.go` | new |
| `internal/store/reporting.go` | `Dashboard.GroupID`, `SystemDashboard.GroupID`, `DashboardKey` |
| `internal/store/store.go` | new store methods on the interface |
| `internal/store/sqlite/reporting.go` | scan/insert/update `group_id`; `MoveDashboards`, `InsertDashboardGroup`, `SetDashboardsArchived` |
| `internal/store/sqlite/reporting_sync.go` | write `group_id` for system dashboards |
| `internal/reporting/reporting.go` | `Store` interface gains the new methods |
| `internal/reporting/order.go` | new: the sidebar order as groups, and every placement computation |
| `internal/reporting/order_test.go` | new |
| `internal/reporting/ops_dashboard.go` | create/update/duplicate/archive/restore with groups |
| `internal/reporting/read.go` | `DashboardInfo.GroupID`, `DashboardDetail.Tabs` |
| `internal/reporting/files.go`, `migrate.go`, `dev.go` | `"group"` in `dashboard.json` |
| `internal/reporting/system/{product,users,groups,retention}/dashboard.json` | `"group": 1` |
| `internal/api/ops_reporting.go` | new input fields and descriptions |
| `web/src/lib/api.ts`, `components/AppSidebar.tsx`, `components/ReportTabs.tsx`, `pages/Dashboard.tsx`, `hooks/use-dashboard-selection.ts` | groups in the page |
| `web/e2e/app.spec.ts` | e2e |
| `docs/reporting.md`, `deploy/UPGRADES.md` | docs |

---

### Task 1: Store — migration 022 and group-aware rows

**Files:**
- Create: `internal/store/sqlite/migrations/022_dashboard_groups.sql`
- Create: `internal/store/sqlite/migration022_test.go`
- Modify: `internal/store/reporting.go`, `internal/store/store.go`, `internal/store/sqlite/reporting.go`, `internal/store/sqlite/reporting_sync.go`
- Test: `internal/store/sqlite/reporting_test.go`, `internal/store/sqlite/reporting_sync_test.go`

**Interfaces:**
- Produces (in `package store`):
  ```go
  // Dashboard gains:
  GroupID int64 // the group it is a tab of; 0 on insert means "a new group: its own id"
  // SystemDashboard gains:
  GroupID int64 // 0 = its own id
  // DashboardKey is one row's new place: group and sort key.
  type DashboardKey struct {
      ID, GroupID int64
      SortKey     string
  }
  ```
- Produces (methods on `*sqlite.DB`, added to `store.Store` in `store.go` next to the other reporting methods):
  ```go
  // MoveDashboards rewrites group_id and sort_key of every row in ks in one
  // transaction, with one audit row a (Subject "dashboard/<first id>").
  MoveDashboards(ctx context.Context, ks []store.DashboardKey, a store.AuditEntry) error
  // InsertDashboardGroup inserts dashboards as one new group, in one
  // transaction: the first gets group_id = its own id, the rest that id.
  // ws[i] are dashboards[i]'s widgets. Returns the new ids in order.
  InsertDashboardGroup(ctx context.Context, ds []store.Dashboard, ws [][]store.Widget, a store.AuditEntry) ([]int64, error)
  // SetDashboardsArchived archives (only rows currently live) or restores
  // (only rows currently archived) ids in one transaction, one audit row
  // per id. Unknown ids are ErrNotFound and nothing is written.
  SetDashboardsArchived(ctx context.Context, ids []int64, archived bool, a store.AuditEntry) error
  ```
- `InsertDashboard` keeps its signature; `dash.GroupID == 0` → `group_id = id`, otherwise the given group.
- `UpdateDashboard` keeps its signature and now also writes `group_id` (so a caller passes the row it read, with any change).

- [ ] **Step 1: Write the migration test**

`internal/store/sqlite/migration022_test.go`, modelled on `migration021_test.go` (migration tests pin their ceiling with `newTestDBAt` and step with `migrateThrough`):

```go
package sqlite

import (
	"context"
	"testing"
)

// TestMigration022GroupsEveryDashboardAlone checks every existing
// dashboard becomes a group of one: group_id = its own id.
func TestMigration022GroupsEveryDashboardAlone(t *testing.T) {
	db := newTestDBAt(t, 21)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `INSERT INTO dashboards (id, owner, title, sort_key) VALUES
		(1, 'system', 'Views', 'a0'), (1001, 'user', 'Mine', 'a0')`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateThrough(ctx, 22); err != nil {
		t.Fatalf("migration 022: %v", err)
	}
	rows, err := db.db.QueryContext(ctx, `SELECT id, group_id FROM dashboards ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int64]int64{}
	for rows.Next() {
		var id, g int64
		if err := rows.Scan(&id, &g); err != nil {
			t.Fatal(err)
		}
		got[id] = g
	}
	if got[1] != 1 || got[1001] != 1001 {
		t.Fatalf("group ids = %v, want 1→1 and 1001→1001", got)
	}
}
```

- [ ] **Step 2: Run it — FAIL (no migration 022)**

Run: `cd internal/store/sqlite && go test -run TestMigration022 ./...`

- [ ] **Step 3: Write the migration**

`internal/store/sqlite/migrations/022_dashboard_groups.sql`:

```sql
-- 022: dashboard groups (spec 2026-09-30). The dashboards sharing a
-- group_id are one sidebar entry, their live rows its tabs in sort_key
-- order. A new group's id is its first dashboard's id (ids are never
-- reused), so every existing dashboard starts as a group of one. No
-- foreign key: a group is only the rows that share the number, and
-- every rule (one owner per group, a group's rows contiguous in the
-- owner's order) is enforced in Go.
ALTER TABLE dashboards ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0;
UPDATE dashboards SET group_id = id;
CREATE INDEX dashboards_group ON dashboards (group_id);
```

- [ ] **Step 4: Run it — PASS**

- [ ] **Step 5: Tests for the row changes** in `reporting_test.go`:
  - `InsertDashboard` with `GroupID: 0` reads back `GroupID == ID`; with `GroupID: 1001` reads back 1001.
  - `UpdateDashboard` writes a changed `GroupID`.
  - `MoveDashboards` swapping two rows' sort keys in one call succeeds (proves the two-phase write avoids a transient UNIQUE conflict) and writes one audit row.
  - `InsertDashboardGroup` with three dashboards: ids ascending, all `group_id == ids[0]`, widgets attached to the right dashboards.
  - `SetDashboardsArchived` over two ids archives both; an unknown id in the list is `ErrNotFound` and archives neither.

- [ ] **Step 6: Run — FAIL**

- [ ] **Step 7: Implement**

In `sqlite/reporting.go`:
- `dashboardCols` gains `d.group_id` (after `d.sort_key`); `scanDashboard` scans it into `GroupID`.
- `insertDashboardRow` inserts `group_id` (`?`, `dash.GroupID`), then, when `dash.GroupID == 0`, runs `UPDATE dashboards SET group_id=id WHERE id=?` with the new id inside the same tx.
- `UpdateDashboard` sets `group_id=?` too.
- `MoveDashboards`: in one `d.tx`, first `UPDATE dashboards SET sort_key='~'||id WHERE id IN (...)` (the same trick `SyncReporting` uses at `reporting_sync.go:52`), then one `UPDATE dashboards SET group_id=?, sort_key=?, updated_at=… WHERE id=?` per key; map a UNIQUE failure with `mapDashboardConflict`; one `audit` row.
- `InsertDashboardGroup`: one `d.tx`; insert `ds[0]` with `GroupID 0` (becomes its own id), then each other with `GroupID = ids[0]`, each with its `ws[i]` via `insertWidgetRow`; one audit row, Subject `dashboard/<ids[0]>`.
- `SetDashboardsArchived`: one `d.tx`; check every id exists first (`ErrNotFound` naming the first missing one); then the same two UPDATE statements `SetDashboardArchived` uses, per id, one audit row each. Make `SetDashboardArchived` call it with `[]int64{id}` so there is one code path.

In `reporting_sync.go` `syncDashboards`: insert and update `group_id` — `CASE WHEN ? = 0 THEN id ELSE ? END` is not available in `VALUES`, so pass `groupID := dash.GroupID; if groupID == 0 { groupID = dash.ID }` and write it in both the INSERT and the `DO UPDATE SET`.

Add the three methods to the `Store` interface in `internal/store/store.go` next to `SetDashboardArchived`, with the doc comments above.

- [ ] **Step 8: Run the store package — PASS**

Run: `go test ./internal/store/...`

- [ ] **Step 9: Commit**

```bash
git add internal/store
git commit -m "feat(store): add dashboards.group_id (migration 022)"
```

---

### Task 2: The order as groups — pure placement logic

All placement arithmetic in one pure file, tested without a database, so Tasks 3–4 only load rows, call it, and write.

**Files:**
- Create: `internal/reporting/order.go`
- Create: `internal/reporting/order_test.go`

**Interfaces:**
- Consumes: `store.Dashboard` (with `GroupID`), `store.DashboardKey` (Task 1), `sortkey.Between`, `sortkey.Spread` (`internal/shared/sortkey`).
- Produces:
  ```go
  // order is one owner's dashboards in sort_key order, archived included.
  type order []store.Dashboard

  // userOrder filters ds (ListDashboards' answer) to owner user.
  func userOrder(ds []store.Dashboard) order
  // group returns the members of group g in order (archived included).
  func (o order) group(g int64) []store.Dashboard
  // find returns the row with id, or false.
  func (o order) find(id int64) (store.Dashboard, bool)
  // keyInGroup returns a sort key that puts a dashboard (self; 0 for a
  // new one) into group g: after member `after` (0 = first tab, nil =
  // last). Refuses (ErrInvalid) an after that is not a member of g.
  func (o order) keyInGroup(self, g int64, after *int64) (string, error)
  // moveGroup returns the new keys for every member of group g (archived
  // included) placed right after the group of dashboard `after` (0 = top).
  // nil keys when the group already sits there. Refuses an after outside
  // the order (ErrInvalid "after N is not a user dashboard").
  func (o order) moveGroup(g int64, after int64) ([]store.DashboardKey, error)
  // keyAfterGroup returns a key for a group of one placed right after the
  // group of dashboard `after` (0 = top, nil = last), leaving out self.
  func (o order) keyAfterGroup(self int64, after *int64) (string, error)
  ```

Algorithm notes (put them in doc comments):
- A group's rows are contiguous in `o`. Removing `self` from `o` before computing keeps contiguity.
- `keyInGroup`: members := `o.without(self).group(g)`. If `after == nil`: prev = last member's key, next = the key of the row right after the last member in `o` (or ""). If `*after == 0`: prev = key of the row right before the first member (or ""), next = first member's key. Else: find `after` among members (not found → refuse), prev = its key, next = the key of the row right after it in `o`. Return `sortkey.Between(prev, next)`. An empty `members` (g has only `self`) → place at self's current neighbours: prev/next = the rows around where self was.
- `moveGroup`: rest := `o` without g's rows; target index = 0 for `after == 0`, else one past the last row of `after`'s group in `rest` (`after` in g itself → no-op, return nil). If the rows of g already sit exactly at that index in `o`, return nil. Otherwise `sortkey.Spread(prev, next, len(members))` and pair with members in their current order, `GroupID` unchanged.
- `keyAfterGroup`: rest := `o` without self; `nil` → after the last row; `0` → before the first; X → after the last row of X's group.

- [ ] **Step 1: Write table tests** in `order_test.go` building an `order` by hand (ids, groups, keys like `"a0"`, `"a1"`), covering at least:
  - `keyInGroup` last / first / after a member, and a key that sorts between the right neighbours (assert `prev < key < next`).
  - `keyInGroup` with `after` not in the group → `errors.Is(err, store.ErrInvalid)`.
  - `keyInGroup` into a group that is not self's: result sorts inside that group's block, and after removing self the new key keeps both groups contiguous (write a helper `contiguous(o) bool` and assert it on the order with self re-keyed).
  - `moveGroup` to top, after another group, after its own member (nil), to where it already is (nil — Review Focus 2).
  - `moveGroup` carries archived members (a member with `ArchivedAt != ""` gets a key too).
  - `keyAfterGroup` nil / 0 / after a member of a 3-row group lands after that whole group.

- [ ] **Step 2: Run — FAIL** (`go test ./internal/reporting -run TestOrder`)

- [ ] **Step 3: Implement `order.go`** following the algorithm notes.

- [ ] **Step 4: Run — PASS**

- [ ] **Step 5: Commit**

```bash
git add internal/reporting/order.go internal/reporting/order_test.go
git commit -m "feat(reporting): compute dashboard placement within and between groups"
```

---

### Task 3: Reading and placing — get/list, create, update

**Files:**
- Modify: `internal/reporting/read.go`, `internal/reporting/ops_dashboard.go`, `internal/reporting/place.go`, `internal/reporting/reporting.go`
- Test: `internal/reporting/ops_test.go`

**Interfaces:**
- Consumes: Task 1 store methods, Task 2 `order` helpers.
- Produces:
  ```go
  // DashboardInfo gains:
  GroupID int64 `json:"group_id"`
  // Tab is one entry of a group's tab bar.
  type Tab struct {
      ID    int64  `json:"dashboard_id"`
      Title string `json:"title"`
  }
  // DashboardDetail gains:
  Tabs []Tab `json:"tabs"` // the group's live members in order; always non-nil
  // CreateDashboard gains:
  GroupID int64 // 0: a new group
  // UpdateDashboard gains:
  GroupID *int64 // nil: stay; 0: leave as a group of one; G: move into G
  ```
- `reporting.Store` (reporting.go) gains `MoveDashboards`, `InsertDashboardGroup`, `SetDashboardsArchived` with Task 1's signatures.

Behaviour (spec D6–D8, D18–D19):
- `dashboardInfo` fills `GroupID`. `Dashboard(ctx, id)` fills `Tabs` from `ListDashboards`: rows with `GroupID == d.GroupID` and the same owner, live, in list order; `[]Tab{}` when none (an archived dashboard's own detail still lists its group's live tabs).
- `CreateDashboard`, `GroupID == 0`: as today but the key comes from `order.keyAfterGroup(0, in.After)`. `GroupID != 0`: `refuseGroup` (below), then `keyInGroup(0, G, in.After)`, insert with `GroupID: G`.
- `UpdateDashboard`:
  - `GroupID != nil && *GroupID == 0`: new `GroupID = d.ID`; key `keyAfterGroup(d.ID, after)` where `after` defaults to the last member of the old group (so it lands right after the group it left). If `d` is alone in its group already and `After == nil`, only the title changes.
  - `GroupID != nil && *GroupID != 0`: `refuseGroup`, then `keyInGroup(d.ID, G, in.After)`; write via `UpdateDashboard` with the new `GroupID` and key.
  - `GroupID == nil && After != nil`: `*After == d.ID` → no-op (Review Focus 1). Else if `*After` is a member of `d`'s group → `keyInGroup(d.ID, d.GroupID, After)`, one-row update. Else (`*After == 0` or another group) → `moveGroup(d.GroupID, *After)`; nil keys → nothing to move; else `MoveDashboards`, then the title (if any) via `UpdateDashboard`.
  - The "nothing to update" refusal now names `title, after or group_id`.
  - All placements run inside `retryConflict(..., lostDashboardRace)`, re-reading the order each time (as `dashboardKey` does today).
- `refuseGroup(o order, g int64) error`: `ErrInvalid` "group %d has no live user dashboard" when no live user row has `GroupID == g` (this also refuses a system group — Review Focus 5).
- `after` naming a system dashboard is refused with today's message ("after %d is not a user dashboard") because `userOrder` excludes system rows (Review Focus 5).
- Remove `dashboardKey` from `place.go` once nothing calls it.

- [ ] **Step 1: Write failing service tests** in `ops_test.go` (reuse its existing helpers for creating a service and dashboards — read the top of the file), one `t.Run` per case:
  - new dashboard: `GroupID == ID`, and `Tabs` holds just itself (a group of one has one live member): `len(Tabs) == 1`.
  - `create_dashboard {group_id: A}` → last tab of A; `{group_id: A, after: 0}` → first tab; `{group_id: A, after: <non-member>}` → `ErrInvalid`.
  - `create_dashboard {group_id: 1}` (system) → `ErrInvalid`.
  - `update {after: <member>}` reorders tabs only; the group's sidebar position (its first row's neighbours) is unchanged.
  - `update {after: <other group's member>}` moves the whole group, archived members included; groups stay contiguous (assert with a helper over `ListDashboards`).
  - `update {after: 0}` on a tab of a 2-tab group moves the group to the top.
  - `update {after: self}` → no change (Review Focus 1).
  - `update {group_id: own, after: 0}` → first tab; `{group_id: own}` → last tab (Review Focus 3).
  - `update {group_id: 0}` → a group of one right after the old group; the old group keeps its id.
  - `update {after: <system id>}` → `ErrInvalid`; nothing moved.
  - `get_dashboard` from any member returns the same `Tabs`.

- [ ] **Step 2: Run — FAIL** (`go test ./internal/reporting -run 'TestCreateDashboard|TestUpdateDashboard|TestDashboard'`; match the real test names you add)

- [ ] **Step 3: Implement** per the behaviour list.

- [ ] **Step 4: Run the package — PASS** (`go test ./internal/reporting/...`)

- [ ] **Step 5: Commit**

```bash
git add internal/reporting
git commit -m "feat(reporting): create and move dashboards within and between groups"
```

---

### Task 4: Whole-group duplicate, archive and restore

**Files:**
- Modify: `internal/reporting/ops_dashboard.go`
- Test: `internal/reporting/ops_test.go`

**Interfaces:**
- Consumes: Task 1 `InsertDashboardGroup`, `SetDashboardsArchived`; Task 2 `order`; Task 3 `Tabs`.
- Produces (signature changes; callers in `internal/api` updated in Task 6):
  ```go
  func (s *Service) DuplicateDashboard(ctx context.Context, actor string, id int64, wholeGroup bool) (DashboardDetail, error)
  func (s *Service) ArchiveDashboard(ctx context.Context, actor string, id int64, wholeGroup bool) error
  func (s *Service) RestoreDashboard(ctx context.Context, actor string, id int64, wholeGroup bool) error
  ```

Behaviour (spec D9–D15):
- `DuplicateDashboard(id, false)`: as today, except a user source's copy joins the source's group right after it (`GroupID: src.GroupID`, key `keyInGroup(0, src.GroupID, &src.ID)`); a system source's copy is a new group last in the sidebar (`keyAfterGroup(0, nil)`).
- `DuplicateDashboard(id, true)`: members = live rows of `src.GroupID` (system or user) in order; each copy built exactly as the single copy is today (`freshCopy` live widgets, spread widget keys, same `Last*`); first titled `"… (copy)"`, the rest keep their titles; keys `sortkey.Spread(lastKey, "", n)` after the last user row; `InsertDashboardGroup`, audit action `"dashboard.duplicate"` with Detail `"group of dashboard/<id>"`. Return `s.Dashboard(ctx, ids[0])`.
- `ArchiveDashboard(id, false)`: as today.
- `ArchiveDashboard(id, true)`: `refuseSystem`; ids = live members of `d.GroupID`; `SetDashboardsArchived(ids, true, …)`. If none are live (all archived already), succeed as a no-op, as the single archive is idempotent.
- `RestoreDashboard(id, true)`: `refuseSystem`; ids = archived members of `d.GroupID`; `SetDashboardsArchived(ids, false, …)` (spec D14: every archived member).
- `RestoreDashboard(id, false)`: as today.

- [ ] **Step 1: Failing tests:**
  - duplicate one user tab → joins the group right after the original, titled "… (copy)".
  - duplicate one system dashboard → a new group, last.
  - duplicate `whole_group` from any system dashboard → five dashboards, one new user group, same tab order, first titled "Views (copy)", others unchanged titles, archived widgets not copied. (The ops tests seed system dashboards how? Use whatever `ops_test.go`/`testutil_test.go` already does for system rows — e.g. `SyncReporting` with a small `ReportingSync` — and group them with `GroupID`.)
  - duplicate `whole_group` of a user group with an archived member → the archived member is not copied.
  - archive one member → group keeps its other live tabs; archive the first tab → the next becomes first in `Tabs`.
  - archive the only live member, then restore → back at the same sidebar position (Review Focus 4).
  - archive `whole_group` → all live members archived; restore `whole_group` → all archived members back, including one archived on its own earlier.
  - archive/restore `whole_group` on a system dashboard → refused like the single form.

- [ ] **Step 2: Run — FAIL**

- [ ] **Step 3: Implement.** Update the existing callers inside the package (tests, `dev.go` if any) to pass `false`.

- [ ] **Step 4: Run — PASS** (`go test ./internal/reporting/...`)

- [ ] **Step 5: Commit**

```bash
git add internal/reporting
git commit -m "feat(reporting): duplicate, archive and restore a whole group"
```

---

### Task 5: System dashboards form group 1

**Files:**
- Modify: `internal/reporting/files.go`, `internal/reporting/migrate.go`, `internal/reporting/dev.go`
- Modify: `internal/reporting/system/{product,users,groups,retention}/dashboard.json`
- Test: `internal/reporting/files_test.go`, `internal/reporting/migrate_test.go`, `internal/reporting/dev_test.go`, `internal/reporting/system_test.go`

**Interfaces:**
- Consumes: `store.SystemDashboard.GroupID` (Task 1), `Tab` (Task 3).
- Produces: `FileDashboard.Group int64` (0 = its own), parsed from `"group"` in `dashboard.json` (`fileDashboardDoc.Group int64 \`json:"group"\``).

Behaviour (spec D16–D17):
- `migrateFrom`: for each file with `Group != 0`, refuse (return an error, as the other checks do) unless a file in the same release has `ID == Group`, and that file has `Group == 0 || Group == its own ID` (a group names its first dashboard, which is not itself in another group). Pass `GroupID: fd.Group` into `store.SystemDashboard`.
- Keys stay `sortkey.Spread` in file order (`LoadDashboards` order); check that order puts each group's members together — refuse a release whose groups are not contiguous in that order ("system dashboard %d: group %d is not contiguous").
- `dev.go`: `devDashboardRow` sets `GroupID` (`fd.Group`, or `fd.ID` when 0); `devGetDashboard` fills `Tabs` from `loadDevDashboards` with the same rule as `Service.Dashboard` (live = every file).
- `dashboard.json` for Product, Users, Groups and Retention gain `"group": 1` (after `"range"`).

- [ ] **Step 1: Failing tests:** `LoadDashboard` parses `"group"`; `migrateFrom` with a group naming a missing id fails; with a non-contiguous group fails; the real release (`Migrate`) leaves dashboards 2–5 with `GroupID 1` and 1 with `GroupID 1`; `devGetDashboard` returns five `Tabs` for id 3.
- [ ] **Step 2: Run — FAIL**
- [ ] **Step 3: Implement**, add `"group": 1` to the four files.
- [ ] **Step 4: Run — PASS** (`go test ./internal/reporting/...`; `TestSystemDashboards` in `system_test.go` must still pass)
- [ ] **Step 5: Commit**

```bash
git add internal/reporting
git commit -m "feat(reporting): ship the system dashboards as one group"
```

---

### Task 6: API — new fields on the existing tools and routes

**Files:**
- Modify: `internal/api/ops_reporting.go`
- Test: `internal/api/rest_test.go` (and `openapi_test.go` if it pins schemas)

**Interfaces:**
- Consumes: Task 3/4 service signatures.
- Produces (JSON inputs):
  ```go
  type createDashboardIn struct { …; GroupID int64 `json:"group_id,omitempty" jsonschema:"add it as a tab of this group (a group_id from list_dashboards); after then names a tab of that group, 0 first; omit for a dashboard of its own"` }
  type updateDashboardIn struct { …; GroupID *int64 `json:"group_id,omitempty" jsonschema:"move it into this group as a tab (after then names a tab there; 0 first); 0 takes it out as a dashboard of its own; omit to stay"` }
  type dashboardGroupIn struct {
      DashboardID int64 `json:"dashboard_id" jsonschema:"dashboard id; list_dashboards names them"`
      WholeGroup  bool  `json:"whole_group,omitempty" jsonschema:"true acts on every dashboard in this dashboard's group (all its tabs)"`
  }
  ```
  `duplicateDashboard`, `archiveDashboard`, `restoreDashboard` take `dashboardGroupIn`.
- Update descriptions: `after` on create/update ("a dashboard id: one in the same group moves this tab; one in another group moves the whole group after that group; 0 first"); `list_dashboards` mentions `group_id`; `get_dashboard` mentions `tabs`; duplicate/archive/restore mention `whole_group`.

- [ ] **Step 1: Failing REST tests:** `POST /api/dashboards/{id}/archive` with an empty body archives one; with `{"whole_group": true}` archives the group; `PATCH` with `{"group_id": G}` moves; `POST /api/dashboards` with `group_id` creates a tab; `GET /api/dashboards/{id}` has `tabs` and `group_id`.
- [ ] **Step 2: Run — FAIL** (`go test ./internal/api/...`)
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run — PASS.** `docs_sync_test` may now fail on docs; that is Task 8 — if it does, note which test and move on only after confirming the failure is the documentation check, not a code bug.
- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "feat(api): group_id and whole_group on the dashboard tools"
```

---

### Task 7: Web — sidebar per group, tab bar from `tabs`

**Files:**
- Modify: `web/src/lib/api.ts`, `web/src/components/AppSidebar.tsx`, `web/src/components/ReportTabs.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/hooks/use-dashboard-selection.ts`
- Test: `web/src/components/AppSidebar.test.tsx`, `web/src/pages/Dashboard.test.tsx`, `web/e2e/app.spec.ts`

**Interfaces:**
- Consumes: `group_id` on `DashboardInfo`, `tabs: {dashboard_id, title}[]` on `DashboardDetail` (Task 3/6).
- Produces: `DashboardInfo.group_id: number`, `DashboardTab { dashboard_id: number; title: string }`, `DashboardDetail.tabs: DashboardTab[]`; `ReportTabs` props `{ tabs: DashboardTab[]; currentId: number; onSelect }`.

Behaviour (spec D20–D23):
- `AppSidebar`: live dashboards in list order; one entry per `group_id`, the first live member (title, link, icon `LayoutDashboardIcon`; system entries keep `ChartColumnIcon`). System entries first, then "Yours" (the list is already system-first). An entry `isActive` when `currentId` is any live member of its group (build `groupOf: Map<id, group_id>`). The "Reports" entry and its system-only filter go; the component doc comment says so.
- `Dashboard.tsx` `TopBar`: `dashboard.tabs.length > 1` → `<ReportTabs tabs={dashboard.tabs} …/>`; else `owner === 'user'` → the "Yours" label; else nothing. Remove the `reports` filter.
- `use-dashboard-selection.ts`: `openReport` is unchanged in behaviour; rename it `openTab` and update its doc comment ("Opens another tab of the group…").
- `ReportTabs` aria labels: `"Tabs"` / `"Tab"` instead of `"Reports"` / `"Report"`.

- [ ] **Step 1: Update/extend unit tests:** `AppSidebar.test.tsx` — two dashboards in one group give one link named by the first; a link is active on the second member's page; system group shows "Views", no "Reports". `Dashboard.test.tsx` — replace "lists Reports and the live user dashboards" with the group version; a user dashboard with two tabs shows a `tablist`; one with one tab shows "Yours" and no `tablist`. Update fixtures to carry `group_id` and `tabs`.
- [ ] **Step 2: Run — FAIL** (`cd web && npx vitest run`)
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run — PASS**, then `npm run typecheck` (or the script `package.json` names) and `npm run build`.
- [ ] **Step 5: e2e** in `web/e2e/app.spec.ts`: keep the existing tests passing (the login test still sees a "Views" tab); add:
  - "an agent-made group shows its tab bar": POST two dashboards, the second with `group_id` of the first; open the first; `tablist` has both titles; the sidebar has one link for them.
  - "a whole-group copy of Views opens with five tabs": `POST /api/dashboards/1/duplicate` body `{"whole_group": true}`; open the returned id; five tabs.
  Run: `cd web && npm run e2e` (builds the binary via `serve.sh`).
- [ ] **Step 6: Commit**

```bash
git add web
git commit -m "feat(web): one sidebar entry per dashboard group, its tabs from the API"
```

---

### Task 8: Documentation and the full check

**Files:**
- Modify: `docs/reporting.md`, `deploy/UPGRADES.md`

- [ ] **Step 1: `docs/reporting.md`:**
  - Concepts: a dashboard is in a group; a group is one sidebar entry, its dashboards the tabs, named by its first; `group_id` equals the first dashboard's id when made; system dashboards are group 1 (sidebar "Views").
  - Tools table: `list_dashboards` returns `group_id`; `get_dashboard` returns `group_id` and `tabs`; `create_dashboard` input gains `group_id`; `update_dashboard` gains `group_id`; `duplicate_dashboard`, `archive_dashboard`, `restore_dashboard` gain `whole_group`, with what each does.
  - HTTP API table: create/update bodies gain `group_id`; duplicate/archive/restore take an optional body `whole_group`.
  - Layout: the `after` rules (same group → tab; other group → whole group; 0 → top) and `group_id` placement (0 first tab, omitted last, `group_id: 0` leaves), with a short worked example (make a two-tab dashboard in two calls).
  - Archiving and the purge: single vs whole group; restoring whole group brings back every archived member.
  - Refusals and fixes: rows for "group G has no live user dashboard" and "after N is not a tab of group G".
- [ ] **Step 2: `deploy/UPGRADES.md`**, newest last: `### Upgrading to dashboard groups (migration 022)` — nothing to check before; on the day the sidebar's "Reports" entry reads "Views" with the same five tabs; every user dashboard is a group of one, so nothing else changes.
- [ ] **Step 3: Run the doc tests:** `go test ./internal/api -run 'TestDocument|TestReporting'` — PASS.
- [ ] **Step 4: Full check:** `export PATH=$PATH:/usr/local/go/bin && make check` — PASS (vet, coverage, restore test). Then `cd web && npm run e2e` — PASS.
- [ ] **Step 5: Commit**

```bash
git add docs/reporting.md deploy/UPGRADES.md
git commit -m "docs(reporting): document dashboard groups and tabs"
```
