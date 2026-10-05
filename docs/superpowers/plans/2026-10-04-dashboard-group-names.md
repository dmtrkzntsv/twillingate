# Dashboard Group Names Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give a dashboard group a name of its own that the console can rename, kept in a `dashboard_groups` table that follows the group's id and disappears with its last dashboard. Dashboard titles and group names need at least 2 characters.

**Architecture:** Migration 031 adds `dashboard_groups(group_id PK, title)` and two triggers that delete a name once no dashboard has its `group_id`. Every dashboard read carries the name (`store.Dashboard.GroupTitle`, from a subquery). Writes happen in three places: rename (`SetGroupTitle`), whole-group duplicate (`InsertDashboardGroup`) and the release sync. `MoveDashboards` moves the name when `handOver` gives a group a new id. The API reuses `update_dashboard` with `whole_group: true`, and the web app shows `group_title` (falling back to the first tab's title) with a Rename item in the sidebar group menu.

**Tech Stack:** Go (modernc SQLite), React + TypeScript (Vite, vitest, Playwright).

**Spec:** `docs/superpowers/specs/2026-10-04-dashboard-group-names-design.md` (decisions D1–D12). Read it before any task.

## Global Constraints

- Branch `feat/dashboard-group-names`, stacked on `feat/archive-groups` (PR #132). Never rebase onto `main` yourself.
- Go is at `/usr/local/go/bin` and is not on PATH: run `export PATH=/usr/local/go/bin:$PATH` first.
- Migration number is **031**: `internal/store/sqlite/migrations/031_dashboard_groups.sql`.
- A name is trimmed and must have **at least 2 characters, counted in runes** (`utf8.RuneCountInString`). Refusals use `store.Refuse(store.ErrInvalid, …)` and are matched with `errors.Is`, never by message text.
- A group name can never be cleared. No API call deletes a `dashboard_groups` row; only the triggers do, when the group has no dashboards left (archived ones count).
- Renaming a group never changes any dashboard's title (D9).
- Commits use Conventional Commits with the `store`, `reporting`, `api` or `web` scope and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Intermediate commits may use `feat(scope):`; the PR is squashed.
- Comments match the surrounding density and voice: full sentences, explaining why, citing spec decisions as `(D3)`.
- `make check` must pass before the last commit of each Go task. Web: `cd web && npx vitest run --testTimeout=30000` and `npm run typecheck` (or `npx tsc -b`). The machine is loaded, so use the longer test timeout.
- Port 18080 may be held by another session's stale server. For e2e, run on another port (see `web/playwright.config.ts`, plus `vitals.spec.ts`'s hard-coded ORIGIN).

## Review Focus

1. **A founding dashboard leaves a named group** (`update_dashboard {group_id: 0}` or `{group_id: <other>}` on the group's first dashboard). The name must stay with the tabs left behind, under the heir's id, and the leaver's group has no name. Pinned by a test in Task 2.
2. **The last dashboard of a named group is purged, or a named group of one joins another group.** The name row must be gone and must not attach to the group it joined. Pinned by trigger tests in Task 1 and the invariant test in Task 2.
3. **Duplicating a named system group whole** (the gallery's Duplicate on "Reports"). The copy must read "Reports (copy)" in the sidebar, and the original's name must be untouched. Pinned in Task 2.
4. **A rename racing a handover.** The rename must land on the group's id as read inside the placement mutex, not a stale one read before it. Task 2 reads the group id inside `placeDashboards`.
5. **A 1-character or blank name typed in the sidebar field.** No request is sent and the field stays open, marked invalid. Pinned in Task 4's `GroupNameField` test.

---

### Task 1: Store — table, triggers, reads and writes of group names

**Files:**
- Create: `internal/store/sqlite/migrations/031_dashboard_groups.sql`
- Create: `internal/store/sqlite/migration031_test.go`
- Modify: `internal/store/reporting.go` (Dashboard, SystemDashboard, new GroupRekey)
- Modify: `internal/store/store.go:276-297` (interface: MoveDashboards signature, SetGroupTitle)
- Modify: `internal/store/sqlite/reporting.go` (dashboardCols/scanDashboard, MoveDashboards, InsertDashboardGroup, new SetGroupTitle)
- Modify: `internal/store/sqlite/reporting_sync.go` (syncDashboards writes system names)
- Modify: `internal/reporting/reporting.go:39` (Store slice: new MoveDashboards signature, SetGroupTitle) and `internal/reporting/ops_dashboard.go:204,259` (pass `store.GroupRekey{}` for now; Task 2 fills the second)
- Test: `internal/store/sqlite/reporting_test.go`, `internal/store/sqlite/reporting_sync_test.go`, `internal/store/sqlite/purge_test.go`

**Interfaces:**
- Produces:
  - `store.Dashboard.GroupTitle string`: "" when the group has no name; filled by every read (`ListDashboards`, `GetDashboard`). On `InsertDashboardGroup`, `ds[0].GroupTitle` names the new group.
  - `store.SystemDashboard.GroupTitle string`: the founder's fixture name; "" for none.
  - `type GroupRekey struct{ From, To int64 }`: the zero value means no rekey.
  - `MoveDashboards(ctx context.Context, ks []DashboardKey, rekey GroupRekey, a AuditEntry) error`
  - `SetGroupTitle(ctx context.Context, groupID int64, title string, a AuditEntry) error`: upserts; audits with Subject `group/<groupID>`; title stored as given (the caller validated it).

- [ ] **Step 1: Write the migration**

```sql
-- 031: dashboard group names (spec 2026-10-04). A row names one group of
-- dashboards (dashboards.group_id); a group with no row shows its first
-- live tab's title. No foreign key: dashboards.group_id is not unique.
-- The store moves a row when a group's id is handed over (D3), and these
-- triggers delete it once no dashboard, archived ones included, has its
-- group_id (D4): a purge, a system dashboard a release dropped, a group
-- of one joining another, or a write by an older binary after a
-- rollback. A rebuild of the dashboards table must recreate them, as it
-- must recreate 022's dashboards_own_group.
CREATE TABLE dashboard_groups (
    group_id INTEGER PRIMARY KEY,
    title    TEXT NOT NULL
);

CREATE TRIGGER dashboard_groups_gone_delete AFTER DELETE ON dashboards
BEGIN
  DELETE FROM dashboard_groups WHERE group_id = OLD.group_id
    AND NOT EXISTS (SELECT 1 FROM dashboards WHERE group_id = OLD.group_id);
END;

CREATE TRIGGER dashboard_groups_gone_update AFTER UPDATE OF group_id ON dashboards
WHEN NEW.group_id <> OLD.group_id
BEGIN
  DELETE FROM dashboard_groups WHERE group_id = OLD.group_id
    AND NOT EXISTS (SELECT 1 FROM dashboards WHERE group_id = OLD.group_id);
END;
```

- [ ] **Step 2: Write the migration test (pinned at 31), run it, and see it pass once the file exists**

`internal/store/sqlite/migration031_test.go`, in the style of `migration022_test.go` (`newTestDBAt(t, 31)` and raw SQL through `db.db`):
- `TestMigration031DeletesNameWithLastDashboard`: insert user dashboards 1001 and 1002 in group 1001 and a name row for 1001. Delete 1002: the name stays. Delete 1001: the name is gone.
- `TestMigration031DeletesNameWhenGroupOfOneJoinsAnother`: 1001 (group 1001, named "Alpha") and 1002 (group 1002, named "Beta"). `UPDATE dashboards SET group_id=1002 WHERE id=1001`: the "Alpha" row is gone and "Beta" is untouched.
- `TestMigration031KeepsNameWhileMembersRemain`: an update of `sort_key` only, and an update setting `group_id` to its own value, leave the name row in place.

Run: `export PATH=/usr/local/go/bin:$PATH; go test ./internal/store/sqlite -run TestMigration031 -v`. Expected: PASS.

- [ ] **Step 3: Write failing store tests in `reporting_test.go`**

Use `newTestDB` and the helpers the file already uses to insert dashboards:
- `TestSetGroupTitleUpsertsAndReadsBack`: `SetGroupTitle(ctx, g, "Ops", audit)` → `GetDashboard` on each member returns `GroupTitle == "Ops"`, and so do `ListDashboards` rows. Call it again with "Ops 2": updated, still one row (`SELECT COUNT(*) FROM dashboard_groups`). An audit row exists with action `dashboard.group.rename` and subject `group/<g>`.
- `TestMoveDashboardsRekeysName`: group 1001 has members 1001, 1002 and 1003 and the name "Ops". Call `MoveDashboards` with keys moving 1002 and 1003 to group 1002 and `GroupRekey{From: 1001, To: 1002}`. Afterwards 1002 and 1003 read "Ops", and 1001 (still group 1001) reads "".
- `TestMoveDashboardsZeroRekeyLeavesNames`: a move with `GroupRekey{}` changes no name row.
- `TestInsertDashboardGroupWritesName`: `ds[0].GroupTitle = "Ops (copy)"` → every new row reads it. With `ds[0].GroupTitle == ""`, no row is written.

Run: `go test ./internal/store/sqlite -run 'SetGroupTitle|MoveDashboards|InsertDashboardGroupWritesName' -v`. Expected: compile failure (no `GroupTitle`, `SetGroupTitle`, `GroupRekey`).

- [ ] **Step 4: Implement the store changes**

In `internal/store/reporting.go`:

```go
type Dashboard struct {
	ID                          int64
	Owner, Title, SortKey       string
	GroupID                     int64
	GroupTitle                  string // the group's name (migration 031); "" = none, the first live tab's title stands in
	...
}

// GroupRekey moves a group's name from one group id to another, in the
// same transaction as the rows that change group (spec 2026-10-04 D3):
// order.handOver gives a group a new id when the dashboard whose id it
// used leaves. The zero value moves nothing.
type GroupRekey struct{ From, To int64 }
```

Add `GroupTitle string` to `SystemDashboard`, with the comment "the group's name from the founding dashboard's fixture; "" = none".

In `internal/store/sqlite/reporting.go`, append `COALESCE((SELECT g.title FROM dashboard_groups g WHERE g.group_id=d.group_id),'')` to `dashboardCols` (after the widget count) and `&d.GroupTitle` to `scanDashboard`.

`MoveDashboards(ctx, ks, rekey, a)`: inside the transaction, **before** the per-key updates (so the triggers find no row at the old id once its last member moves):

```go
		if rekey != (store.GroupRekey{}) {
			if _, err := tx.ExecContext(ctx,
				`UPDATE dashboard_groups SET group_id=? WHERE group_id=?`, rekey.To, rekey.From); err != nil {
				return fmt.Errorf("move dashboards: rekey group %d name: %w", rekey.From, err)
			}
		}
```

Keep the existing early return for `len(ks) == 0`.

`InsertDashboardGroup`: after the loop, when `len(ds) > 0 && ds[0].GroupTitle != ""`, run `INSERT INTO dashboard_groups (group_id, title) VALUES (?, ?)` with `groupID`.

New method:

```go
// SetGroupTitle names group groupID, inserting its row on the first
// rename and updating it after (spec 2026-10-04 D1, D5). The caller has
// checked the title; no call clears a name, which goes only with the
// group's last dashboard (migration 031's triggers).
func (d *DB) SetGroupTitle(ctx context.Context, groupID int64, title string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dashboard_groups (group_id, title) VALUES (?, ?)
			 ON CONFLICT(group_id) DO UPDATE SET title=excluded.title`, groupID, title); err != nil {
			return fmt.Errorf("set group %d title: %w", groupID, err)
		}
		a.Subject = fmt.Sprintf("group/%d", groupID)
		return audit(ctx, tx, a)
	})
}
```

Add to `store.Store` (`internal/store/store.go`) and to `reporting.Store` (`internal/reporting/reporting.go`), with the new `MoveDashboards` signature in both. Update the two callers in `internal/reporting/ops_dashboard.go` (lines ~204 and ~259) to pass `store.GroupRekey{}`; Task 2 fills the second. Fix the existing test call sites in `reporting_test.go` (`MoveDashboards(ctx, keys, store.GroupRekey{}, audit)`).

- [ ] **Step 5: System names in the sync (D6)**

In `syncDashboards` (`reporting_sync.go`), after the upsert loop: delete the names of every system group, then insert the names the release gives. The user can never rename a system group, so the release owns those rows outright.

```go
	// D6: system group names are the release's: drop every one, then
	// write those the founders' fixtures give. A dropped system
	// dashboard's group loses its name by migration 031's triggers too.
	if _, err := tx.ExecContext(ctx, `DELETE FROM dashboard_groups
		WHERE group_id IN (SELECT group_id FROM dashboards WHERE owner=?)`, store.OwnerSystem); err != nil {
		return 0, 0, fmt.Errorf("reporting sync: clear system group names: %w", err)
	}
	for _, dash := range dashboards {
		if dash.GroupTitle == "" {
			continue
		}
		groupID := dash.GroupID
		if groupID == 0 {
			groupID = dash.ID
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dashboard_groups (group_id, title) VALUES (?, ?)
			 ON CONFLICT(group_id) DO UPDATE SET title=excluded.title`, groupID, dash.GroupTitle); err != nil {
			return 0, 0, fmt.Errorf("reporting sync: system group %d name: %w", groupID, err)
		}
	}
```

Tests in `reporting_sync_test.go`:
- A sync with `GroupTitle: "Reports"` on dashboard 1 makes members 1 and 2 (group 1) read "Reports".
- A second sync without it removes the name.
- A sync that drops a whole named system group leaves no row.

- [ ] **Step 6: Purge test**

In `purge_test.go`, add: a named user group of two, both archived past retention → after `PurgeArchived`, `SELECT COUNT(*) FROM dashboard_groups` is 0. A named group where only one of two members is purged keeps its row.

- [ ] **Step 7: Run and commit**

Run: `go test ./internal/store/... ./internal/reporting/... 2>&1 | tail -20`. Expected: PASS. Then run `make check`, and expect PASS.

```bash
git add internal/store internal/reporting/reporting.go internal/reporting/ops_dashboard.go
git commit -m "feat(store): keep dashboard group names in dashboard_groups (migration 031)"
```

---

### Task 2: Reporting — rename, rekey, duplicate, fixtures, title minimum

**Files:**
- Create: `internal/reporting/names.go` (the name check)
- Modify: `internal/reporting/ops_dashboard.go` (CreateDashboard, UpdateDashboard, writePlaced, DuplicateDashboard)
- Modify: `internal/reporting/read.go` (`DashboardInfo.GroupTitle`, `dashboardInfo`)
- Modify: `internal/reporting/files.go` (`group_title` in dashboard.json, title checks)
- Modify: `internal/reporting/migrate.go` (pass GroupTitle; founder-only check in `checkGroups`)
- Modify: `internal/reporting/dev.go` (`devDashboardRow` carries the founder's GroupTitle)
- Modify: `internal/reporting/system/views/dashboard.json` (`"group_title": "Reports"`)
- Test: `internal/reporting/names_test.go`, `internal/reporting/ops_test.go`, `internal/reporting/order_test.go` or a new `internal/reporting/group_names_test.go`, `internal/reporting/files_test.go`, `internal/reporting/migrate_test.go`, `internal/reporting/system_test.go`

**Interfaces:**
- Consumes (Task 1): `store.Dashboard.GroupTitle`, `store.SystemDashboard.GroupTitle`, `store.GroupRekey`, `MoveDashboards(ctx, ks, rekey, a)`, `SetGroupTitle(ctx, groupID, title, a)`.
- Produces:
  - `reporting.UpdateDashboard.WholeGroup bool`: with it, `Title` renames the group of `ID`.
  - `reporting.DashboardInfo.GroupTitle string` with JSON `group_title,omitempty`.
  - `FileDashboard.GroupTitle string` from dashboard.json `"group_title"`.
  - `func checkName(what, s string) (string, error)` in package reporting.

- [ ] **Step 1: Failing tests for `checkName`**

`internal/reporting/names_test.go`, table-driven: `""`, `" "`, `"a"`, `"é"` and `"  x  "` are refused with `errors.Is(err, store.ErrInvalid)`. `"ab"`, `"éé"` and `"  Ops  "` (→ `"Ops"`) are accepted and return the trimmed value.

Run: `go test ./internal/reporting -run TestCheckName -v`. Expected: FAIL (undefined).

- [ ] **Step 2: Implement `names.go`**

```go
package reporting

import (
	"strings"
	"unicode/utf8"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// minNameRunes is the fewest characters a dashboard title or a group
// name may have, after trimming (spec 2026-10-04 D5, D11).
const minNameRunes = 2

// checkName trims s and refuses it with ErrInvalid when it has fewer
// than minNameRunes characters; what names the field in the message.
func checkName(what, s string) (string, error) {
	t := strings.TrimSpace(s)
	if utf8.RuneCountInString(t) < minNameRunes {
		return "", store.Refuse(store.ErrInvalid, "%s must have at least %d characters", what, minNameRunes)
	}
	return t, nil
}
```

Run the test. Expected: PASS.

- [ ] **Step 3: Dashboard titles use it (D11)**

`CreateDashboard` (`ops_dashboard.go:64`): replace the blank check with `title, err := checkName("title", in.Title); if err != nil { return DashboardDetail{}, err }; in.Title = title`.

`UpdateDashboard` (`:132`): replace the `TrimSpace` check with `if in.Title != "" { t, err := checkName("title", in.Title); if err != nil {…}; in.Title = t }`.

Add tests in `ops_test.go`:
- create with "a" → ErrInvalid; create with " Ops " → stored title "Ops"
- update with "é" → ErrInvalid
- update with "  ab " → stored "ab"

Update existing tests that asserted the old "title must not be empty" refusal: they still get ErrInvalid, so only fix message comparisons if any exist.

- [ ] **Step 4: Rename a group (D5)**

Add `WholeGroup bool` to the `UpdateDashboard` input struct, with the comment "renames id's group with Title instead of the dashboard (D5); takes no After or GroupID". At the top of `Service.UpdateDashboard`, before the "nothing to update" check:

```go
	if in.WholeGroup {
		return s.renameGroup(ctx, actor, in)
	}
```

```go
// renameGroup names id's group (spec 2026-10-04 D5). The title is
// required: a name is never cleared, only replaced. The group id is read
// inside the placement mutex, so a handover running beside it can't
// leave the name on the group's old number.
func (s *Service) renameGroup(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error) {
	if in.After != nil || in.GroupID != nil {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "whole_group renames the group; it takes no after or group_id")
	}
	title, err := checkName("group title", in.Title)
	if err != nil {
		return DashboardInfo{}, err
	}
	d, err := s.editableDashboard(ctx, in.ID)
	if err != nil {
		return DashboardInfo{}, err
	}
	err = s.placeDashboards(func() error {
		o, err := s.readOrder(ctx)
		if err != nil {
			return err
		}
		row, ok := o.find(d.ID)
		if !ok {
			return store.Refuse(store.ErrNotFound, "dashboard %d: not found", d.ID)
		}
		return s.st.SetGroupTitle(ctx, row.GroupID, title, store.AuditEntry{
			Actor: actor, Action: "dashboard.group.rename", Detail: fmt.Sprintf("dashboard/%d", d.ID)})
	})
	if err != nil {
		return DashboardInfo{}, err
	}
	d, err = s.st.GetDashboard(ctx, d.ID)
	return dashboardInfo(d), err
}
```

Check `placeDashboards`' real signature in the file and match it; it is the mutex wrapper `UpdateDashboard` already uses.

Tests in `ops_test.go`:
- rename a two-tab user group → both members' `DashboardInfo.GroupTitle` is the new name, and their `Title`s are unchanged
- rename a group of one → `GroupTitle` set and `Title` unchanged
- `whole_group` with `Title: ""` → ErrInvalid, and the existing name is unchanged
- `"x"` → ErrInvalid
- with `After` set → ErrInvalid
- system dashboard → refused (the same error `editableDashboard` gives)
- archived dashboard → refused

- [ ] **Step 5: Reads carry the name (D8)**

In `read.go` add the field right after `GroupID`:

```go
	GroupTitle string `json:"group_title,omitempty"` // the group's name; omitted when it has none and the first live tab's title stands in
```

Set it in `dashboardInfo`: `GroupTitle: d.GroupTitle`.

- [ ] **Step 6: Rekey on handover (D3)**

In `writePlaced` (`ops_dashboard.go`), `heirs` is non-empty only when `row` founded its group (`handOver` returns nil otherwise), so the old group id is `row.ID`. Pass the rekey through:

```go
	rekey := store.GroupRekey{From: row.ID, To: heirs[0].GroupID}
	if err := s.st.MoveDashboards(ctx, keys, rekey, a); err != nil {
```

Update the `writePlaced` doc comment to say the name moves with the heirs (D3).

Tests (new file `internal/reporting/group_names_test.go`, with the service helpers from `testutil_test.go`):
- `TestHandOverKeepsNameWithMembersLeft`: create group A (founder F, tabs T1 and T2), rename it to "Ops", then `UpdateDashboard{ID: F, GroupID: ptr(0)}`. T1 and T2 read `GroupTitle "Ops"` and share the new group id (T1's); F reads "".
- Same, with F joining another unnamed group B: T1 and T2 keep "Ops", and B (with F in it) still has no name.
- `TestGroupOfOneJoiningLosesName`: a named group of one joins group B → no row is left for its old id (query `dashboard_groups` through the test's DB handle, or check that no `ListDashboards` row has the old name).

- [ ] **Step 7: Whole-group duplicate copies the name (D7)**

In `DuplicateDashboard`'s whole-group branch, after the `ds[i]` loop: `if src.GroupTitle != "" { ds[0].GroupTitle = src.GroupTitle + " (copy)" }`. Tab titles keep today's rule: the first tab gets " (copy)", the rest keep theirs.

Tests:
- duplicate a named user group whole → the copy's members read "<name> (copy)" and the original is unchanged
- duplicate the system group 1 whole (after Task 2 Step 8 names it) → "Reports (copy)"
- duplicate a single tab of a named group → the copy reads `GroupTitle ""`
- duplicate an unnamed group whole → ""

- [ ] **Step 8: Fixtures (D6, D11)**

`files.go`: add `GroupTitle string \`json:"group_title"\`` to `fileDashboardDoc` and `GroupTitle string` to `FileDashboard` (comment: "the group's name; only on a group's founding dashboard (D6)"). Pass it through in `LoadDashboard`'s return. In `LoadDashboard`, check `doc.Title` with `checkName("title", …)`, and check `doc.GroupTitle` with `checkName("group_title", …)` when non-empty, wrapping errors as `fmt.Errorf("reporting: %s: %w", dashPath, err)` like the file's other errors.

`migrate.go`: pass `GroupTitle: fd.GroupTitle` into `store.SystemDashboard`. In `checkGroups`, refuse a `GroupTitle` on a non-founder: `if fd.GroupTitle != "" && fd.Group != 0 && fd.Group != fd.ID { return fmt.Errorf("reporting: system dashboard %d: group_title belongs on group %d's first dashboard", fd.ID, fd.Group) }`.

`dev.go`: `devDashboardRow` takes the founder's name. Change it to `devDashboardRow(fd FileDashboard, groupTitles map[int64]string)`, with `GroupTitle: groupTitles[fd.groupID()]`. Its callers build the map once from the loaded files (`for _, x := range fds { if x.GroupTitle != "" { m[x.groupID()] = x.GroupTitle } }`).

`internal/reporting/system/views/dashboard.json`: add `"group_title": "Reports",` after `"title": "Views",`.

Tests:
- `files_test.go`: a dashboard.json with `"title": "x"` → load error; `"group_title": "é"` → load error; `"group_title": "Ops"` → `FileDashboard.GroupTitle == "Ops"`.
- `migrate_test.go`: `checkGroups` refuses a group_title on a tab.
- `system_test.go`: after Migrate, dashboards 1–5 read `GroupTitle "Reports"` and the others read "".

- [ ] **Step 9: Invariant test (D10)**

In `group_names_test.go`, `TestNoNameOutlivesItsGroup` runs this sequence on one service, with a helper `assertNoOrphanNames(t)` after each step. The helper runs `SELECT COUNT(*) FROM dashboard_groups g WHERE NOT EXISTS (SELECT 1 FROM dashboards d WHERE d.group_id = g.group_id)` through the test's DB handle (see how other reporting tests reach the `*sqlite.DB`; if none does, use `ListDashboards` to collect group ids and `SELECT group_id FROM dashboard_groups` through the same handle the service was built from). The sequence:
1. create group A and add tabs
2. rename A
3. the founder leaves
4. a tab joins another group
5. move a tab within its group
6. move the whole group
7. duplicate whole
8. archive whole
9. restore whole
10. archive whole and purge (`PurgeArchived` with a backdated `archived_at`, as `purge_test.go` does)
11. run the Migrate/sync again

- [ ] **Step 10: Run and commit**

Run: `go test ./internal/reporting/... -v 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'`, then `make check`. Expected: all ok.

```bash
git add internal/reporting
git commit -m "feat(reporting): name dashboard groups, rename them with whole_group, and require 2-character titles"
```

---

### Task 3: API and docs

**Files:**
- Modify: `internal/api/ops_reporting.go:78-83` (`updateDashboardIn`), the `update_dashboard` and `create_dashboard` descriptions, the handler that maps `updateDashboardIn` → `reporting.UpdateDashboard`
- Modify: `docs/reporting.md` (update_dashboard, `group_title`, the 2-character minimum)
- Modify: `deploy/UPGRADES.md` (one entry for 031)
- Test: `internal/api/*_test.go` (the existing update_dashboard route/tool tests), `internal/api/docs_sync_test.go` must stay green

**Interfaces:**
- Consumes (Task 2): `reporting.UpdateDashboard.WholeGroup`, `DashboardInfo.GroupTitle`.
- Produces: `PATCH /api/dashboards/{id}` and the MCP tool `update_dashboard` accept `whole_group: true` with `title` and return the dashboard with `group_title`.

- [ ] **Step 1: Failing API test**

Find the existing `update_dashboard` tests (`grep -rn "update_dashboard\|updateDashboard" internal/api/*_test.go`) and add one in their style: `PATCH /api/dashboards/<id>` with `{"whole_group": true, "title": "Ops"}` returns 200 and `"group_title":"Ops"`. `{"whole_group": true}` returns 400 (ErrInvalid's status). `{"title": "a"}` returns 400.

Run: `go test ./internal/api -run UpdateDashboard -v`. Expected: FAIL (`whole_group` unknown, or ignored).

- [ ] **Step 2: Implement**

Add to `updateDashboardIn`:

```go
	WholeGroup bool `json:"whole_group,omitempty" jsonschema:"true renames this dashboard's group with title (required, at least 2 characters) and leaves every dashboard's own title alone; takes no after or group_id"`
```

Change `Title`'s schema text to `"new title, at least 2 characters; omit to keep"`, and add the same minimum to `create_dashboard`'s title description. Map `WholeGroup` into `reporting.UpdateDashboard` in the handler. Update the `update_dashboard` description: "Rename a user dashboard and/or move it … whole_group with title renames its group instead: the sidebar name, which otherwise is the first tab's title; a group name can be replaced, never cleared. System dashboards are read-only."

- [ ] **Step 3: Docs**

In `docs/reporting.md`, find the update_dashboard and dashboard-list sections (`grep -n "update_dashboard\|group_id" docs/reporting.md`):
- document `whole_group` + `title` on `update_dashboard`, with a one-line example `{"dashboard_id": 1001, "whole_group": true, "title": "Ops"}`
- document `group_title` on dashboard rows (absent = the first live tab's title stands in; duplicating a whole group copies it with " (copy)")
- state the 2-character minimum for dashboard titles and group names

In `deploy/UPGRADES.md`, add a 031 entry in the file's existing format: no pre-checks; after the upgrade the built-in group's sidebar entry reads "Reports" instead of "Views"; group names live in `dashboard_groups`.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/api/... && make check`. Expected: PASS (including `docs_sync_test.go`).

```bash
git add internal/api docs/reporting.md deploy/UPGRADES.md
git commit -m "feat(api): rename a dashboard group with update_dashboard whole_group"
```

---

### Task 4: Web — show group names and rename from the sidebar

**Files:**
- Modify: `web/src/lib/api.ts` (`DashboardInfo.group_title`, `endpoints.renameGroup`)
- Modify: `web/src/lib/arrange.ts` (`groupName`, `nameTooShort`)
- Create: `web/src/components/GroupNameField.tsx`, `web/src/components/GroupNameField.test.tsx`
- Modify: `web/src/hooks/use-dashboard-actions.ts` (`renameGroup`)
- Modify: `web/src/components/SidebarGroupMenu.tsx` (Rename item, aria-label and archive toast use the group name)
- Modify: `web/src/components/AppSidebar.tsx` (labels, tooltips, in-place rename)
- Modify: `web/src/components/DashboardMenu.tsx:56` ("Move to" labels)
- Modify: `web/src/pages/Archive.tsx:84`, `web/src/pages/gallery/DashboardsGallery.tsx:74` (card titles)
- Test: `web/src/lib/arrange.test.ts`, `web/src/components/SidebarGroupMenu.test.tsx`, `web/src/components/AppSidebar.test.tsx`, `web/src/pages/Archive.test.tsx`, `web/src/pages/gallery/DashboardsGallery.test.tsx`, `web/e2e/arrange.spec.ts`

**Interfaces:**
- Consumes (Task 3): `PATCH /api/dashboards/{id}` with `{whole_group: true, title}` → `DashboardInfo`; `group_title?: string` on every dashboard row of a named group.
- Produces:
  - `groupName(members: DashboardInfo[]): string`: the first member's `group_title` if any member has one, else the first live member's title (else the first member's).
  - `nameTooShort(s: string): boolean`: `[...s.trim()].length < 2`.
  - `endpoints.renameGroup(id: number, title: string): Promise<DashboardInfo>`
  - `useDashboardActions().renameGroup(id: number, title: string): Promise<boolean>`

- [ ] **Step 1: `arrange.ts` helpers, test first**

In `arrange.test.ts`:
- `groupName` returns `group_title` when set (on any member)
- otherwise the first live member's title, skipping an archived first member
- otherwise `members[0].title` when all are archived

`nameTooShort`: `''`, `' '`, `'a'` and `'é'` → true; `'ab'` and `' ab '` → false.

```ts
/**
 * A group's name (group names D2, D8): its stored `group_title`, which
 * every member carries, or else its first live member's title, as the
 * sidebar named every group before groups had names.
 */
export function groupName(members: DashboardInfo[]): string {
  const named = members.find((m) => m.group_title)
  if (named?.group_title) return named.group_title
  return (members.find((m) => !m.archived_at) ?? members[0]).title
}

/** True when a dashboard title or group name has fewer than 2 characters once trimmed (D5, D11). */
export function nameTooShort(s: string): boolean {
  return [...s.trim()].length < 2
}
```

Run: `cd web && npx vitest run src/lib/arrange.test.ts --testTimeout=30000`. Expected: PASS after implementing.

- [ ] **Step 2: API client and action**

In `api.ts`, add `group_title?: string` to `DashboardInfo` (after `group_id`) and add:

```ts
  /** Names the dashboard's group (group names D5); a name is replaced, never cleared, and no dashboard title changes. */
  renameGroup: (id: number, title: string) =>
    api<DashboardInfo>(`/api/dashboards/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ whole_group: true, title }),
    }),
```

Check `endpoints.move` for the exact method and path it uses for `update_dashboard`, and match it.

In `use-dashboard-actions.ts`, add `renameGroup` to the interface, with the doc comment "Names the dashboard's group; resolves true when saved, false after the toast", and implement it like `move`:

```ts
  const renameGroup = useCallback(
    async (id: number, title: string) => {
      let ok = false
      await run(async () => {
        await endpoints.renameGroup(id, title)
        ok = true
      })
      return ok
    },
    [run]
  )
```

Return it from the hook. Add a case to `use-dashboard-actions.test.tsx` in its existing style: the PATCH body is `{whole_group: true, title}`, and a refusal resolves false with a toast.

- [ ] **Step 3: `GroupNameField`, test first**

`GroupNameField.test.tsx` (Testing Library, as `ProjectName.test.tsx` on this branch does):
- it shows `name` in an input labelled "Group name", focused
- Enter with "Ops" calls `onRename("Ops")`, and `onDone` is called when that resolves true
- Escape and × call `onDone` without `onRename`
- an unchanged name calls `onDone` without `onRename`
- `""`, `" "` and `"a"`: Enter does **not** call `onRename`, the input has `aria-invalid="true"`, the save button is disabled, and the field stays open
- `onRename` resolving false keeps the field open

```tsx
import { useState, type FormEvent } from 'react'
import { CheckIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { nameTooShort } from '@/lib/arrange'

interface Props {
  name: string
  /** Resolves true when saved, so the field closes; false keeps it open. */
  onRename: (name: string) => Promise<boolean>
  /** Closes the field: after a save, on Escape or ×, or when nothing changed. */
  onDone: () => void
  pending?: boolean
}

/**
 * A sidebar group's name as a field, in place of its entry (group names
 * D9): Enter or ✓ saves, Escape or × leaves it as it was. A name under 2
 * characters, blank included, is never sent: a group's name can be
 * replaced, not cleared (D5).
 */
export default function GroupNameField({ name, onRename, onDone, pending }: Props) {
  const [draft, setDraft] = useState(name)
  const invalid = nameTooShort(draft)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (invalid) return
    const next = draft.trim()
    if (next === name) return onDone()
    if (await onRename(next)) onDone()
  }
  return (
    <form onSubmit={submit} className="flex min-w-0 items-center gap-1 px-1">
      <Input
        aria-label="Group name"
        aria-invalid={invalid}
        autoFocus
        value={draft}
        className="h-7 min-w-0 flex-1 text-sm"
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => e.key === 'Escape' && onDone()}
      />
      <Button type="submit" variant="ghost" size="icon" className="size-7" aria-label="Save group name" disabled={pending || invalid}>
        <CheckIcon />
      </Button>
      <Button type="button" variant="ghost" size="icon" className="size-7" aria-label="Cancel rename" onClick={onDone}>
        <XIcon />
      </Button>
    </form>
  )
}
```

- [ ] **Step 4: Sidebar menu Rename item**

`SidebarGroupMenu`: add the prop `onRename?: () => void` (doc: "Opens the entry's name field; user groups only (D9)"). Render a "Rename" item with `PencilIcon` first in the menu, followed by a separator, only when `group.owner === 'user' && onRename`. Compute `const name = groupName(group.members)` and use it for the trigger's `aria-label={`${name} actions`}`. Pass `{ dashboard_id: first.dashboard_id, title: name }` to `archive` so the toast names the group.

Tests in `SidebarGroupMenu.test.tsx`:
- a user group's menu has Rename, and clicking it calls `onRename`
- a system group's menu has no Rename
- the aria-label uses `group_title` when set

- [ ] **Step 5: AppSidebar**

- Replace every `g.members[0].title` (label, tooltips, the reorder announcer's name callback, and `SortableGroupItem`'s `title`) with `groupName(g.members)`. Keep `g.members[0].dashboard_id` for links.
- Add `const [renaming, setRenaming] = useState<number | null>(null)` and `const { move, renameGroup, pending } = useDashboardActions()`.
- In the editable (`!readOnly`) list, render this for the group being renamed instead of `SortableGroupItem`:

```tsx
<SidebarMenuItem key={g.groupId}>
  <GroupNameField
    name={groupName(g.members)}
    pending={pending}
    onRename={(title) => renameGroup(g.members[0].dashboard_id, title)}
    onDone={() => setRenaming(null)}
  />
</SidebarMenuItem>
```

Pass `onRename={() => setRenaming(g.groupId)}` to each user group's `SidebarGroupMenu`. The system list passes none.

Tests in `AppSidebar.test.tsx`:
- an entry whose members carry `group_title: 'Ops'` shows "Ops", not the first tab's title
- choosing Rename from the menu shows the "Group name" field; typing "Platform" and pressing Enter sends the PATCH (mock as the file already mocks endpoints) and closes the field

- [ ] **Step 6: Other places that name a group**

- `DashboardMenu.tsx:56`: `{groupName(g.members)}` in "Move to".
- `Archive.tsx:84`: the card title is `groupName(g.members)`; keep its `groupTitle(g)` helper only for what else uses the returned dashboard, or rename that helper to `firstLive` if it then only picks the dashboard.
- `DashboardsGallery.tsx:74`: `title={groupName(g.members)}`.

Add one assertion per page test that a `group_title` shows as the card title or menu label.

- [ ] **Step 7: e2e**

In `web/e2e/arrange.spec.ts`, add a test in its style: create (through the API helper the spec uses) a user dashboard, open the sidebar entry's "…" menu, choose Rename, check that "a" can't be saved (save disabled), type "Team metrics", press Enter, and expect the sidebar entry to read "Team metrics" and the dashboard's own page heading to keep its title. `web/e2e/cursor.spec.ts` must stay green; the new buttons are `<button>`s, so they get the pointer cursor from `index.css`.

Run: `cd web && npm run e2e -- arrange.spec.ts` on a free port (see Global Constraints). Expected: PASS.

- [ ] **Step 8: Run and commit**

Run: `cd web && npx tsc -b && npx vitest run --testTimeout=30000 && npm run lint` (if a lint script exists), then `make check` from the repo root. Expected: all pass.

```bash
git add web
git commit -m "feat(web): show dashboard group names and rename a group from the sidebar"
```
