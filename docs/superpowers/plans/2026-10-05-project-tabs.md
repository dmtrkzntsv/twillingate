# Project tabs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A project page becomes a row of tabs: Setup (today's page), the
built-in dashboards, the user's own dashboards, and **+**. Each project keeps
its own list of tabs, and built-in dashboards are never archived any more:
hiding one is a `sidebar` flag.

**Architecture:** Migration 032 adds `dashboards.sidebar` and
`dashboards.project_tab`, plus a `project_tabs(project_id, dashboard_id,
sort_key)` table that SQLite triggers seed:
- a new project gets every dashboard with `project_tab = 1`;
- a new built-in dashboard goes into every existing project.

`reporting.Service` gains the project-tab operations and the
placement fields on `update_dashboard`. `internal/api` exposes four new tools
over MCP and REST. The web app renders `/projects/:id/setup` and
`/projects/:id/dashboards/:dashId` with the project pinned.

**Tech Stack:** Go 1.x with modernc SQLite (`internal/store/sqlite`); MCP
go-sdk via `internal/api/expose.go`; React + TypeScript, TanStack Query,
react-router, shadcn/ui, Vitest and Playwright in `web/`.

**Spec:** `docs/superpowers/specs/2026-10-05-project-tabs-design.md`. Read it
before every task. D-numbers below refer to it.

## Global Constraints

- **Migration number:** `032_project_tabs.sql`. 031 is the latest on `main`.
- **Commit subjects:** Conventional Commits, `<type>(<scope>): <subject>`,
  imperative, lower case, no trailing period.
  - Scopes: `store`, `reporting`, `api`, `web`.
  - Use `feat` only for user-visible behaviour; `test`/`refactor`/`docs`
    otherwise.
- **Refusals** are typed: `store.Refuse(store.ErrInvalid|ErrNotFound|ErrConflict, …)`,
  matched with `errors.Is`. Never match message text.
- **Go toolchain** is at `/usr/local/go/bin`, which is not on PATH: run
  `export PATH=$PATH:/usr/local/go/bin` first.
- **Checks:** `make check` must pass before the branch is pushed. Web unit
  tests: `cd web && npx vitest run --testTimeout=30000`. Web typecheck:
  `cd web && npx tsc -b`. e2e: `cd web && npm run e2e`.
- **Clickable elements:** every new one is a `button`, a link, or has
  `role="button"`, never a `cursor-pointer` class (CLAUDE.md, `web/e2e/cursor.spec.ts`).
- **Phones:** nothing scrolls sideways at 360px. A single-column grid says
  `grid-cols-1`.
- **Docs in the same commit:**
  - reporting tools and routes → `docs/reporting.md`;
  - the tool count → `docs/twillingate.md` ("thirty-eight tools" becomes
    "forty-two tools");
  - upgrade-visible change → `deploy/UPGRADES.md`.
- **Copy:** the first tab is called **Setup**. A built-in group's sidebar
  action is **Hide**; the gallery's is **Show in sidebar**. The tab menu
  entries are **Remove from this project** and **Open as dashboard**.
- **Code style:** match the surrounding code's comment density and idiom.
  Comments explain why, citing spec D-numbers as the existing code does.
- **Model ids:** never put one in commits, code or docs.

## Review Focus

1. **A project created by MCP or the CLI, not the console, still gets the
   built-in tabs.** The trigger handles it; Task 1's trigger test inserts a
   project with plain SQL.
2. **Upgrading an install whose built-in group was hidden.** After 032 the
   group is `sidebar = 0`, not archived. It is absent from the sidebar and
   from the Archive page, and its tabs are on every project. Task 1's
   migration test covers this.
3. **A release adds a built-in to a group the user hid.** The new member
   arrives `sidebar = 0`, the old D3 rule rewritten, and it is seeded into
   every project once. A resync doesn't re-add a removed tab. Task 1's sync
   tests cover this.
4. **Removing the last project tab of a user dashboard that isn't in the
   sidebar** is refused rather than leaving it unreachable. So is setting
   `sidebar=false` on a user group whose members have no tabs. Task 4's
   tests cover both.
5. **A project page opened on a dashboard id that isn't one of its tabs,
   or one that was archived since.** The page shows "isn't a tab of"
   with an Add tab button, not a blank grid or a crash. Task 7's Vitest
   covers this.

---

### Task 1: Store — migration 032, placement columns, release sync

**Files:**
- Create: `internal/store/sqlite/migrations/032_project_tabs.sql`
- Create: `internal/store/sqlite/migration032_test.go`
- Modify: `internal/store/reporting.go`. `Dashboard` gains `Sidebar`
  and `ProjectTab bool`; `SystemDashboard` gains `Sidebar` and
  `ProjectTab bool`.
- Modify: `internal/store/sqlite/reporting.go`. `dashboardCols` and
  `scanDashboard` read the two columns. `insertDashboardRow` writes them,
  as does `InsertDashboardGroup`'s insert if it doesn't go through
  `insertDashboardRow`.
- Modify: `internal/store/sqlite/reporting_sync.go`. In
  `syncDashboards`, the INSERT carries `sidebar` and `project_tab`.
  ON CONFLICT updates `project_tab` but never `sidebar`. The D3
  post-pass sets `sidebar=0`, not `archived_at`.
- Modify: `internal/reporting/ops_dashboard.go`. Every `store.Dashboard`
  built for insert gets `Sidebar: true`: `CreateDashboard`,
  `duplicateOne` and `duplicateGroup`. Without it, user dashboards would
  be inserted hidden from the moment the store writes the column, and the
  tree would stay red until Task 3. Also grep `internal/` for other
  `store.Dashboard{` literals that get inserted, test fixtures included.
- Test: `internal/store/sqlite/reporting_sync_test.go`. Update the
  existing D3 test and add new ones.

**Interfaces:**
- Produces (used by Tasks 2–4):
  - `store.Dashboard{… Sidebar bool; ProjectTab bool}`. Reads fill them;
    `InsertDashboard` writes them.
  - Callers that build a `store.Dashboard` for insert must set
    `Sidebar: true` themselves: `false` is the Go zero value, so a user
    dashboard would otherwise land hidden.
  - `store.SystemDashboard{… Sidebar bool; ProjectTab bool}`.

- [ ] **Step 1: Write the failing migration test**

`internal/store/sqlite/migration032_test.go` (reuse `newTestDBAt` and `execAll` from the 012 and 031 tests):

```go
package sqlite

import (
	"context"
	"testing"
)

func projectTabRows(t *testing.T, db *DB) map[[2]int64]bool {
	t.Helper()
	rows, err := db.db.QueryContext(context.Background(), `SELECT project_id, dashboard_id FROM project_tabs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[[2]int64]bool{}
	for rows.Next() {
		var p, d int64
		if err := rows.Scan(&p, &d); err != nil {
			t.Fatal(err)
		}
		got[[2]int64{p, d}] = true
	}
	return got
}

// TestMigration032Backfills: every project gets every built-in as a tab,
// built-ins get project_tab 1, and a hidden (archived) built-in group
// becomes sidebar 0 and live.
func TestMigration032Backfills(t *testing.T) {
	db := newTestDBAt(t, 31)
	execAll(t, db,
		`INSERT INTO projects (name) VALUES ('One'), ('Two')`,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, archived_at) VALUES
			(1, 'system', 'Views', 'a0', 1, NULL),
			(2, 'system', 'Product', 'a1', 1, NULL),
			(8, 'system', 'Reach', 'a2', 8, '2026-09-01T00:00:00Z'),
			(1001, 'user', 'Mine', 'a0', 1001, NULL)`)
	if err := db.migrateThrough(context.Background(), 32); err != nil {
		t.Fatal(err)
	}
	var one, two int64
	execScan(t, db, `SELECT id FROM projects WHERE name='One'`, &one)
	execScan(t, db, `SELECT id FROM projects WHERE name='Two'`, &two)
	got := projectTabRows(t, db)
	for _, p := range []int64{one, two} {
		for _, d := range []int64{1, 2, 8} {
			if !got[[2]int64{p, d}] {
				t.Errorf("project %d lacks built-in tab %d: %v", p, d, got)
			}
		}
		if got[[2]int64{p, 1001}] {
			t.Errorf("project %d got user dashboard 1001 as a tab", p)
		}
	}
	var sidebar, projectTab int
	var archived *string
	execScan(t, db, `SELECT sidebar, project_tab, archived_at FROM dashboards WHERE id=8`, &sidebar, &projectTab, &archived)
	if sidebar != 0 || projectTab != 1 || archived != nil {
		t.Errorf("hidden built-in 8 = sidebar %d project_tab %d archived %v, want 0 1 nil", sidebar, projectTab, archived)
	}
	execScan(t, db, `SELECT sidebar, project_tab FROM dashboards WHERE id=1001`, &sidebar, &projectTab)
	if sidebar != 1 || projectTab != 0 {
		t.Errorf("user 1001 = sidebar %d project_tab %d, want 1 0", sidebar, projectTab)
	}
}

// TestMigration032SeedsNewProject: the trigger gives a project inserted by
// plain SQL (as the CLI and MCP paths do) every live project_tab dashboard.
func TestMigration032SeedsNewProject(t *testing.T) {
	db := newTestDBAt(t, 32)
	execAll(t, db,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, project_tab) VALUES
			(1, 'system', 'Views', 'a0', 1, 1),
			(1001, 'user', 'Mine', 'a0', 1001, 1),
			(1002, 'user', 'Gone', 'a1', 1002, 1),
			(1003, 'user', 'Off', 'a2', 1003, 0)`,
		`UPDATE dashboards SET archived_at='2026-09-01T00:00:00Z' WHERE id=1002`,
		`INSERT INTO projects (name) VALUES ('New')`)
	var p int64
	execScan(t, db, `SELECT id FROM projects WHERE name='New'`, &p)
	got := projectTabRows(t, db)
	want := map[[2]int64]bool{{p, 1}: true, {p, 1001}: true}
	if len(got) != len(want) || !got[[2]int64{p, 1}] || !got[[2]int64{p, 1001}] {
		t.Fatalf("tabs = %v, want %v", got, want)
	}
}

// TestMigration032SeedsNewBuiltinOnce: a system dashboard inserted with
// project_tab 1 lands on every existing project; an upsert's update path
// (a later release) inserts nothing, so a removed tab stays removed.
func TestMigration032SeedsNewBuiltinOnce(t *testing.T) {
	db := newTestDBAt(t, 32)
	execAll(t, db,
		`INSERT INTO projects (name) VALUES ('A'), ('B')`,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, project_tab) VALUES (9, 'system', 'New', 'a9', 9, 1)`)
	if n := len(projectTabRows(t, db)); n != 2 {
		t.Fatalf("rows after insert = %d, want 2", n)
	}
	execAll(t, db,
		`DELETE FROM project_tabs WHERE dashboard_id=9 AND project_id=(SELECT id FROM projects WHERE name='A')`,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, project_tab) VALUES (9, 'system', 'New!', 'a9', 9, 1)
		 ON CONFLICT(id) DO UPDATE SET title=excluded.title, project_tab=excluded.project_tab`)
	if n := len(projectTabRows(t, db)); n != 1 {
		t.Fatalf("rows after upsert = %d, want 1 (the removed tab stays removed)", n)
	}
}

// TestMigration032Cascades: rows go with their project or dashboard.
func TestMigration032Cascades(t *testing.T) {
	db := newTestDBAt(t, 32)
	execAll(t, db,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, project_tab) VALUES (1, 'system', 'Views', 'a0', 1, 1),
			(1001, 'user', 'Mine', 'a0', 1001, 1)`,
		`INSERT INTO projects (name) VALUES ('A'), ('B')`,
		`DELETE FROM projects WHERE name='A'`,
		`DELETE FROM dashboards WHERE id=1001`)
	got := projectTabRows(t, db)
	if len(got) != 1 {
		t.Fatalf("rows = %v, want only B's tab of dashboard 1", got)
	}
}
```

If no `execScan` helper exists yet, add it to this file:

```go
func execScan(t *testing.T, db *DB, q string, dest ...any) {
	t.Helper()
	if err := db.db.QueryRowContext(context.Background(), q).Scan(dest...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
```

Check that `newTestDBAt` opens with foreign keys on, as `sqlite.go:52`
does. If `openAt` doesn't, the cascade test needs `PRAGMA foreign_keys=ON`.
Look at `fk_test.go` to see how existing tests get it.

- [ ] **Step 2: Run the test and check that it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/sqlite -run TestMigration032 -v`
Expected: FAIL (`migrateThrough` has no 32, or `no such table: project_tabs`).

- [ ] **Step 3: Write the migration**

`internal/store/sqlite/migrations/032_project_tabs.sql`:

```sql
-- 032: project tabs (spec 2026-10-05). A project page shows the
-- dashboards listed for it here. Each dashboard says whether it is in
-- the sidebar (sidebar) and whether a new project gets it as a tab
-- (project_tab); a built-in dashboard is never archived any more: hiding
-- one from the sidebar is sidebar = 0 (D5).
--
-- The triggers below seed rows (D4): a new project gets every live
-- dashboard with project_tab = 1, and a new built-in (an INSERT, which
-- the release sync's upsert does only the first time) goes onto every
-- existing project. A rebuild of projects or dashboards must recreate
-- them, as it must 022's dashboards_own_group and 031's triggers.
ALTER TABLE dashboards ADD COLUMN sidebar INTEGER NOT NULL DEFAULT 1;
ALTER TABLE dashboards ADD COLUMN project_tab INTEGER NOT NULL DEFAULT 0;

CREATE TABLE project_tabs (
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    dashboard_id INTEGER NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
    sort_key     TEXT NOT NULL,
    PRIMARY KEY (project_id, dashboard_id)
);
CREATE INDEX project_tabs_dashboard ON project_tabs (dashboard_id);

-- Built-ins hidden today (archived, the old Hide) become live and out of
-- the sidebar: the sidebar looks the same after the upgrade.
UPDATE dashboards SET sidebar = 0, archived_at = NULL
  WHERE owner = 'system' AND archived_at IS NOT NULL;
UPDATE dashboards SET project_tab = 1 WHERE owner = 'system';
INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
  SELECT p.id, d.id, d.sort_key FROM projects p, dashboards d WHERE d.owner = 'system';

CREATE TRIGGER project_tabs_new_project AFTER INSERT ON projects
BEGIN
  INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
    SELECT NEW.id, d.id, d.sort_key FROM dashboards d
    WHERE d.project_tab = 1 AND d.archived_at IS NULL;
END;

CREATE TRIGGER project_tabs_new_builtin AFTER INSERT ON dashboards
WHEN NEW.owner = 'system' AND NEW.project_tab = 1
BEGIN
  INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
    SELECT p.id, NEW.id, NEW.sort_key FROM projects p;
END;
```

Check that `migrate.go` picks migrations up by file name (`ls
internal/store/sqlite/migrations`, read `migrate.go`). If a list of
versions or a ceiling constant exists (look for `31` in `migrate.go` and
`sqlite_test.go`), bump it to 32.

- [ ] **Step 4: Run the migration tests and check that they pass**

Run: `go test ./internal/store/sqlite -run TestMigration032 -v`
Expected: PASS.

- [ ] **Step 5: Add the store fields and the read/write columns**

In `internal/store/reporting.go`, `Dashboard` gains:

```go
	Sidebar    bool // in the sidebar (migration 032); a built-in's Hide sets it false
	ProjectTab bool // a new project gets this dashboard as a tab (032 D3)
```

`SystemDashboard` gains:

```go
	Sidebar    bool // the fixture's "sidebar": written on insert only, then the install's (D6)
	ProjectTab bool // the fixture's "project_tab": the release's, re-synced every time (D6)
```

In `internal/store/sqlite/reporting.go`, append `d.sidebar, d.project_tab`
to `dashboardCols`, and scan them in `scanDashboard` in the same position.
`insertDashboardRow` writes `sidebar` and `project_tab` in both INSERT
forms. Find every other `INSERT INTO dashboards` in the package
(`grep -n "INSERT INTO dashboards" internal/store/sqlite/*.go`) and make
it write both too.

- [ ] **Step 6: Write the failing sync tests**

Add to `internal/store/sqlite/reporting_sync_test.go`, following its
existing helpers (read the file first):
- `TestSyncReportingSidebarOnInsertOnly`:
  - Sync a dashboard with `Sidebar: false, ProjectTab: true`; check that
    the row reads `Sidebar false`.
  - Run `UPDATE dashboards SET sidebar=1`, then resync with
    `Sidebar: false`; check that the row still reads `Sidebar true`.
- `TestSyncReportingProjectTabResyncs`:
  - Sync with `ProjectTab: true`, resync with `false`; the row reads
    `ProjectTab false`.
- `TestSyncReportingNewBuiltinSeedsProjectsOnce`:
  - Insert projects A and B, then sync a dashboard with
    `ProjectTab: true`. Both projects have the tab.
  - Delete A's row and resync. A still has no tab.
- Rewrite the existing D3 test (grep for the archived-group test in
  `reporting_sync_test.go`). A new member of a group whose pre-existing
  members are all `sidebar = 0` now arrives `sidebar = 0` with
  `archived_at` empty, and a brand-new group arrives as its fixture says.

- [ ] **Step 7: Run them and check that they fail**

Run: `go test ./internal/store/sqlite -run 'TestSyncReporting' -v`
Expected: the new tests FAIL.

- [ ] **Step 8: Implement the sync changes**

In `syncDashboards`, change the upsert to:

```go
		if _, err := tx.ExecContext(ctx, `INSERT INTO dashboards (id, owner, title, sort_key, group_id, last_range, sidebar, project_tab)
			VALUES (?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET title=excluded.title, sort_key=excluded.sort_key,
				group_id=excluded.group_id, project_tab=excluded.project_tab,
				updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
			WHERE dashboards.owner=?`,
			dash.ID, store.OwnerSystem, dash.Title, dash.SortKey, groupID, dash.Range,
			dash.Sidebar, dash.ProjectTab, store.OwnerSystem,
		); err != nil {
```

Rewrite the D3 block: same WHERE logic, judged on `sidebar` instead of
`archived_at`.

```go
		if _, err := tx.ExecContext(ctx, `UPDATE dashboards SET sidebar=0
			WHERE owner=? AND id IN (`+placeholders(len(newIDs))+`)
			  AND EXISTS (SELECT 1 FROM dashboards other WHERE other.group_id=dashboards.group_id
					AND other.owner=? AND other.id<>dashboards.id AND other.id NOT IN (`+placeholders(len(newIDs))+`))
			  AND NOT EXISTS (SELECT 1 FROM dashboards other WHERE other.group_id=dashboards.group_id
					AND other.owner=? AND other.id<>dashboards.id AND other.id NOT IN (`+placeholders(len(newIDs))+`)
					AND other.sidebar=1)`,
			args...); err != nil {
			return 0, 0, fmt.Errorf("reporting sync: hide new dashboards of a hidden group: %w", err)
		}
```

Rewrite the D3 comment above it to say "hidden (sidebar 0)" where it says
archived.

- [ ] **Step 9: Run the whole store package and check that it passes**

Run: `go test ./internal/... -count=1`
Expected: PASS. A failing older test that inserts dashboards and compares
whole rows probably needs `Sidebar: true` in its expectations. Fix the
expectation, not the code.

- [ ] **Step 10: Commit**

```bash
git add internal/store internal/reporting
git commit -m "feat(store): add project tabs and sidebar placement (migration 032)"
```

---

### Task 2: Store — project-tab rows and placement writes

**Files:**
- Modify: `internal/store/reporting.go`. Add the `ProjectTabRow` type.
- Create: `internal/store/sqlite/project_tabs.go`
- Create: `internal/store/sqlite/project_tabs_test.go`

**Interfaces:**
- Consumes: Task 1's columns and table.
- Produces (used by Tasks 3–4):

```go
// store
type ProjectTabRow struct {
	ProjectID, DashboardID int64
	SortKey                string
}

// *sqlite.DB methods
func (d *DB) ListProjectTabs(ctx context.Context, projectID int64) ([]store.ProjectTabRow, error)      // ErrNotFound if no such project; ordered by sort_key, dashboard_id
func (d *DB) ListDashboardProjects(ctx context.Context, dashboardID int64) ([]int64, error)            // project ids having it, ascending
func (d *DB) InsertProjectTab(ctx context.Context, r store.ProjectTabRow, a store.AuditEntry) error     // ErrNotFound (project/dashboard), ErrConflict (exists)
func (d *DB) DeleteProjectTab(ctx context.Context, projectID, dashboardID int64, a store.AuditEntry) error // ErrNotFound when no row
func (d *DB) MoveProjectTab(ctx context.Context, r store.ProjectTabRow, a store.AuditEntry) error       // rewrites sort_key; ErrNotFound when no row
func (d *DB) SetDashboardsSidebar(ctx context.Context, ids []int64, sidebar bool, a store.AuditEntry) error // ErrNotFound if any id unknown; one audit row per id
func (d *DB) SetDashboardProjectTab(ctx context.Context, id int64, on bool, a store.AuditEntry) error  // ErrNotFound when unknown
```

Audit subjects:
- `"project/<id>/tab/<dashboard_id>"`, with actions `project.tab.add`,
  `project.tab.remove` and `project.tab.move`;
- `"dashboard/<id>"`, with actions `dashboard.sidebar.show`,
  `dashboard.sidebar.hide` and `dashboard.project_tab`.

All of these use `audit()`, not `auditAndBump()`: they aren't
managed-config.

- [ ] **Step 1: Write the failing tests**

`internal/store/sqlite/project_tabs_test.go` (use `newTestDB(t)`, which is
fully migrated). It needs a project and dashboards: insert them with SQL
through `execAll`, as Task 1's tests do. Cover:
- `ListProjectTabs`:
  - an unknown project returns `ErrNotFound` (`errors.Is`);
  - a known project with no rows returns an empty slice and nil;
  - rows come ordered by `sort_key`, then `dashboard_id`.
- `InsertProjectTab`:
  - inserting twice returns `ErrConflict`;
  - an unknown project returns `ErrNotFound`;
  - an unknown dashboard returns `ErrNotFound`;
  - one `audit_log` row is written with action `project.tab.add`.
- `DeleteProjectTab`: a missing row returns `ErrNotFound`; otherwise the
  row is gone.
- `MoveProjectTab`: the row's `sort_key` changes; a missing row returns
  `ErrNotFound`.
- `ListDashboardProjects` returns project ids in ascending order.
- `SetDashboardsSidebar`:
  - `[1, 2], false` sets both rows' `sidebar` to 0;
  - an unknown id leaves every row untouched and returns `ErrNotFound`.
- `SetDashboardProjectTab`: on and off are both read back.

- [ ] **Step 2: Run them and check that they fail**

Run: `go test ./internal/store/sqlite -run 'ProjectTab|DashboardsSidebar|DashboardProjects' -v`
Expected: FAIL (undefined methods).

- [ ] **Step 3: Implement**

`internal/store/sqlite/project_tabs.go`:

```go
// Project tab rows (migration 032, spec 2026-10-05 D2): which dashboards
// a project page shows, and the placement flags on dashboards. Writes
// use audit, not auditAndBump: they are not managed-config.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func (d *DB) ListProjectTabs(ctx context.Context, projectID int64) ([]store.ProjectTabRow, error) {
	var one int
	err := d.db.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=?`, projectID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.Refuse(store.ErrNotFound, "project %d: not found", projectID)
	}
	if err != nil {
		return nil, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, dashboard_id, sort_key FROM project_tabs
		WHERE project_id=? ORDER BY sort_key, dashboard_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ProjectTabRow{}
	for rows.Next() {
		var r store.ProjectTabRow
		if err := rows.Scan(&r.ProjectID, &r.DashboardID, &r.SortKey); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) ListDashboardProjects(ctx context.Context, dashboardID int64) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT project_id FROM project_tabs WHERE dashboard_id=? ORDER BY project_id`, dashboardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) InsertProjectTab(ctx context.Context, r store.ProjectTabRow, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO project_tabs (project_id, dashboard_id, sort_key) VALUES (?,?,?)`,
			r.ProjectID, r.DashboardID, r.SortKey)
		switch {
		case err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: project_tabs"):
			return store.Refuse(store.ErrConflict, "project %d already has dashboard %d as a tab", r.ProjectID, r.DashboardID)
		case err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed"):
			return store.Refuse(store.ErrNotFound, "project %d or dashboard %d: not found", r.ProjectID, r.DashboardID)
		case err != nil:
			return fmt.Errorf("insert project tab: %w", err)
		}
		a.Subject = fmt.Sprintf("project/%d/tab/%d", r.ProjectID, r.DashboardID)
		return audit(ctx, tx, a)
	})
}
```

`DeleteProjectTab`, `MoveProjectTab` and `SetDashboardProjectTab` follow
`UpdateDashboard`'s shape: one statement, `RowsAffected() == 0` →
`ErrNotFound`, then `audit`.

`SetDashboardsSidebar` follows `SetDashboardsArchived`'s shape:
- check that every id exists first;
- then run `UPDATE dashboards SET sidebar=?, updated_at=… WHERE id=?`
  for each id;
- write one audit row per id, with action `dashboard.sidebar.show` or
  `dashboard.sidebar.hide` as passed in `a.Action`.

If SQLite reports the duplicate primary key as `UNIQUE constraint failed:
project_tabs.project_id, project_tabs.dashboard_id`, the `Contains` check
above matches it. Confirm it in the test.

- [ ] **Step 4: Run them and check that they pass**

Run: `go test ./internal/store/sqlite -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): read and write project tabs and dashboard placement"
```

---

### Task 3: Reporting — dashboard.json flags, placement fields, built-ins never archived

**Files:**
- Modify: `internal/reporting/files.go`. `fileDashboardDoc` gains
  `Sidebar *bool \`json:"sidebar"\`` and
  `ProjectTab *bool \`json:"project_tab"\``; `FileDashboard` gains
  `Sidebar, ProjectTab bool`. Both are required: nil returns an error
  naming the file.
- Modify: `internal/reporting/migrate.go`. Pass `Sidebar` and
  `ProjectTab` into `store.SystemDashboard`. `checkGroups` refuses a group
  whose members disagree on `sidebar`.
- Modify: `internal/reporting/system/*/dashboard.json` (all seven). Add
  `"sidebar": true, "project_tab": true` after `"range"`.
- Modify: `internal/reporting/read.go`. `DashboardInfo` gains
  `Sidebar bool \`json:"sidebar"\`` and
  `ProjectTab bool \`json:"project_tab"\``; `DashboardDetail` gains
  `ProjectIDs []int64 \`json:"project_ids"\`` (always non-nil);
  `dashboardInfo()` copies both flags; `Dashboard()` fills `ProjectIDs`.
- Modify: `internal/reporting/reporting.go`. The `Store` interface gains
  Task 2's seven methods.
- Modify: `internal/reporting/ops_dashboard.go`:
  - `UpdateDashboard` gains `Sidebar *bool` and `ProjectTab *bool`, with
    a new `setPlacement`;
  - `setDashboardArchived` refuses a built-in;
  - update the doc comments that mention archiving a system group.
- Modify: the reporting test doubles. If a fake `Store` exists in
  `internal/reporting/*_test.go`, add the seven methods to it.
- Test: `internal/reporting/ops_test.go`, `internal/reporting/migrate_test.go`, `internal/reporting/files_test.go`

**Interfaces:**
- Consumes: Task 2's store methods.
- Produces:

```go
type UpdateDashboard struct {
	ID         int64
	Title      string
	GroupID    *int64
	After      *int64
	WholeGroup bool
	Sidebar    *bool // the whole group in or out of the sidebar; allowed on a built-in (D5, D7)
	ProjectTab *bool // a new project gets it as a tab; user dashboards only (D3)
}
// DashboardInfo JSON adds: "sidebar": bool, "project_tab": bool
// DashboardDetail JSON adds: "project_ids": []int64
```

Rules for `UpdateDashboard` when `Sidebar` or `ProjectTab` is set:
- They take no `Title`, `After` or `GroupID`; giving one is `ErrInvalid`
  ("sidebar and project_tab go on their own").
- `WholeGroup` is accepted and ignored, since `sidebar` always applies to
  the whole group.
- An archived dashboard is refused with `ErrInvalid`, as
  `editableDashboard` refuses it.
- `ProjectTab` on a built-in is `ErrInvalid`: "project_tab of a built-in
  dashboard is the release's".
- `Sidebar` applies to every live member of the dashboard's group with
  the same owner.
- `Sidebar = false` on a user group is refused (`ErrInvalid`) when any
  member has no project tab (`ListDashboardProjects` is empty). The
  message: "dashboard %d would be unreachable: not in the sidebar and on
  no project's tabs; add it to a project first, or archive it".
- Audit actions: `dashboard.sidebar.show` / `.hide`, and
  `dashboard.project_tab`.
- Returns `dashboardInfo` of the re-read row.

`setDashboardArchived` on a built-in returns `ErrInvalid`: "dashboard %d is
a built-in dashboard and is never archived; update_dashboard {sidebar:
false} takes its group out of the sidebar". This applies with or without
`whole_group`.

- [ ] **Step 1: Write the failing tests**

In `internal/reporting/ops_test.go` (read its helpers first: it builds a
`Service` over a real migrated DB or a fake; follow whichever it does):
- `TestUpdateDashboardSidebarHidesBuiltinGroup`: `{ID: 1, Sidebar:
  &false}`. Every member of system group 1 reads `Sidebar false`;
  `ArchivedAt` stays empty.
- `TestUpdateDashboardSidebarRefusesUnreachableUserGroup`: a user group
  with no project tabs gets `ErrInvalid`. After adding a tab row through
  the store, the same call succeeds.
- `TestUpdateDashboardProjectTabRefusedOnBuiltin`: `ErrInvalid`.
- `TestUpdateDashboardProjectTabOnUser`: the row reads `ProjectTab true`.
- `TestUpdateDashboardPlacementTakesNoTitle`: `{Sidebar: &true, Title:
  "x"}` gets `ErrInvalid`.
- `TestArchiveDashboardRefusesBuiltin`: `ArchiveDashboard(…, 1, true)`
  and `(…, 1, false)` both get `ErrInvalid`.
- `TestDuplicateIsInSidebar`: a duplicate of built-in 1 reads
  `Sidebar true`, `ProjectTab false`, and has no project tabs.
- `TestDashboardDetailProjectIDs`: after inserting tab rows for projects
  P1 and P2, `Dashboard(ctx, 1).ProjectIDs == []int64{P1, P2}`.

In `migrate_test.go` / `files_test.go`:
- a `dashboard.json` missing `sidebar` (or `project_tab`) fails to load,
  and the error names the file and the field;
- two members of a group with different `sidebar` values are refused by
  `checkGroups`.

- [ ] **Step 2: Run them and check that they fail**

Run: `go test ./internal/reporting -run 'Sidebar|ProjectTab|ArchiveDashboardRefusesBuiltin|DuplicateIsInSidebar|ProjectIDs|Missing' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`setPlacement` in `ops_dashboard.go` (called first in `UpdateDashboard`
when either flag is set, before the `WholeGroup` rename branch):

```go
// setPlacement writes the placement flags (spec 2026-10-05 D3, D5, D7):
// sidebar for the dashboard's whole group, project_tab for a user
// dashboard alone. A user group leaving the sidebar must keep a way in:
// each member needs a project tab, or it would be unreachable.
func (s *Service) setPlacement(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error) {
	if in.Title != "" || in.After != nil || in.GroupID != nil {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "sidebar and project_tab go on their own; give title, after or group_id in another call")
	}
	d, err := s.st.GetDashboard(ctx, in.ID)
	if err != nil {
		return DashboardInfo{}, err
	}
	if d.ArchivedAt != "" {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "dashboard %d is archived; restore_dashboard first", d.ID)
	}
	system := d.Owner == store.OwnerSystem
	if in.ProjectTab != nil && system {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "project_tab of a built-in dashboard is the release's")
	}
	if in.Sidebar != nil {
		all, err := s.st.ListDashboards(ctx)
		if err != nil {
			return DashboardInfo{}, err
		}
		var ids []int64
		for _, m := range all {
			if m.Owner != d.Owner || m.GroupID != d.GroupID || m.ArchivedAt != "" {
				continue
			}
			if !*in.Sidebar && !system {
				ps, err := s.st.ListDashboardProjects(ctx, m.ID)
				if err != nil {
					return DashboardInfo{}, err
				}
				if len(ps) == 0 {
					return DashboardInfo{}, store.Refuse(store.ErrInvalid,
						"dashboard %d would be unreachable: not in the sidebar and on no project's tabs; add it to a project first, or archive it", m.ID)
				}
			}
			ids = append(ids, m.ID)
		}
		action := "dashboard.sidebar.show"
		if !*in.Sidebar {
			action = "dashboard.sidebar.hide"
		}
		if err := s.st.SetDashboardsSidebar(ctx, ids, *in.Sidebar, store.AuditEntry{Actor: actor, Action: action}); err != nil {
			return DashboardInfo{}, err
		}
	}
	if in.ProjectTab != nil {
		if err := s.st.SetDashboardProjectTab(ctx, d.ID, *in.ProjectTab, store.AuditEntry{Actor: actor, Action: "dashboard.project_tab"}); err != nil {
			return DashboardInfo{}, err
		}
	}
	d, err = s.st.GetDashboard(ctx, d.ID)
	return dashboardInfo(d), err
}
```

At the top of `UpdateDashboard`:

```go
	if in.Sidebar != nil || in.ProjectTab != nil {
		return s.setPlacement(ctx, actor, in)
	}
```

In `setDashboardArchived`, replace the system `!wholeGroup` refusal with:

```go
	if d.Owner == store.OwnerSystem {
		return store.Refuse(store.ErrInvalid,
			"dashboard %d is a built-in dashboard and is never archived; update_dashboard {sidebar: false} takes its group out of the sidebar", d.ID)
	}
```

`duplicateGroup` copied archived system members because hidden groups were
archived. Built-ins are never archived now, so the
`system && archived` branch is dead; keep the behaviour of copying every
system member. Task 1 already sets `Sidebar: true` on every inserted
`store.Dashboard`.

`files.go`: after decoding, check the two flags:

```go
	if doc.Sidebar == nil || doc.ProjectTab == nil {
		return FileDashboard{}, fmt.Errorf("%s: dashboard.json needs \"sidebar\" and \"project_tab\" (true or false)", dir)
	}
```

Match how `files.go` already names the file in errors. In `checkGroups`,
for every file with `fd.Group != 0`, compare `fd.Sidebar` with the
founder's. A mismatch returns `fmt.Errorf("reporting: system dashboard %d:
sidebar differs from its group's (%d)", fd.ID, fd.Group)`.

- [ ] **Step 4: Run the reporting package and check that it passes**

Run: `go test ./internal/reporting/... -count=1`
Expected: PASS. That includes `TestSystemDashboards` and any test that
loads the real `system/` directory; Step 3 added the flags to the seven
JSON files.

- [ ] **Step 5: Commit**

```bash
git add internal/reporting
git commit -m "feat(reporting): place dashboards by sidebar and project_tab, never archive built-ins"
```

---

### Task 4: Reporting — project tab operations

**Files:**
- Create: `internal/reporting/project_tabs.go`
- Create: `internal/reporting/project_tabs_test.go`

**Interfaces:**
- Consumes: Task 2's store methods; `sortkey.Between(a, b string) (string,
  error)`, where `""` is an open end; `s.placeDashboards` from `place.go`
  serialises writes.
- Produces (used by Task 5):

```go
// ProjectTab is one tab of a project page (spec 2026-10-05 D1), in shown order.
type ProjectTab struct {
	ID      int64  `json:"dashboard_id"`
	Title   string `json:"title"`
	Owner   string `json:"owner"`
	GroupID int64  `json:"group_id"`
}

type AddProjectTab struct {
	ProjectID, DashboardID int64
	After                  *int64 // user tabs only: nil last among the user's tabs, 0 first among them, an id right after that user tab
}

type MoveProjectTab struct {
	ProjectID, DashboardID int64
	After                  int64 // 0: first among the user's tabs; otherwise a user tab of this project
}

func (s *Service) ProjectTabs(ctx context.Context, projectID int64) ([]ProjectTab, error)
func (s *Service) AddProjectTab(ctx context.Context, actor string, in AddProjectTab) ([]ProjectTab, error)
func (s *Service) RemoveProjectTab(ctx context.Context, actor string, projectID, dashboardID int64) ([]ProjectTab, error)
func (s *Service) MoveProjectTab(ctx context.Context, actor string, in MoveProjectTab) ([]ProjectTab, error)
```

**Order (D1):**
- Built-in tabs come first, ordered by `dashboards.sort_key`, which is
  release order.
- Your own tabs come next, ordered by the row's `sort_key`, then
  `dashboard_id`.
- Archived dashboards are left out, but their rows are kept.
- `ProjectTabs` returns `[]ProjectTab{}` (never nil) for a project with
  no tabs, and `ErrNotFound` for an unknown project (from
  `ListProjectTabs`).

**Refusals:**
- **Add:**
  - an archived dashboard → `ErrInvalid`;
  - an unknown dashboard → `ErrNotFound`;
  - a tab the project already has → `ErrConflict` (from the store);
  - `After` on a built-in → `ErrInvalid` ("a built-in tab goes back to
    its own place; drop after");
  - `After` naming a dashboard that isn't one of this project's own
    tabs → `ErrInvalid`.
- **Remove:**
  - no such tab → `ErrNotFound`;
  - a user dashboard with `Sidebar == false` whose only project is this
    one → `ErrInvalid`, with the same "would be unreachable" wording as
    Task 3.
- **Move:**
  - a built-in → `ErrInvalid` ("built-in tabs keep the release's order");
  - not a tab of this project → `ErrNotFound`;
  - `After` naming something other than one of this project's own
    tabs → `ErrInvalid`;
  - `After == DashboardID` → no change; return the current list.

**Keys:**
- A built-in's row gets `d.SortKey`, which is unused for ordering.
- A user tab's key is `sortkey.Between(prevKey, nextKey)` among this
  project's user rows in shown order. Moving or adding after `X` puts
  it between `X`'s key and the key of the user tab after `X`, ignoring
  the moving tab itself. `After == 0` puts it before the first one.
- Two rows can share a key (seeding copies `dashboards.sort_key`, and
  the user's and the system's keys come from separate namespaces).
  `Between` refuses `a >= b`, so when the neighbours' keys are equal,
  respread: give every user row of the project a fresh key with
  `sortkey.Spread("", "", n)` in the current order, write them with
  `MoveProjectTab`, then compute again. This is rare; test it.
- Run every write inside `s.placeDashboards(func() error {…})`.

- [ ] **Step 1: Write the failing tests**

`internal/reporting/project_tabs_test.go`, using the same Service setup
as `ops_test.go`, with projects inserted through the store. Cover:
- `TestProjectTabsOrder`:
  - project P has built-ins 1 and 2, seeded by the trigger;
  - add user dashboards U1 and U2;
  - the order is [1, 2, U1, U2];
  - move U2 with `After 0` and the order is [1, 2, U2, U1].
- `TestProjectTabsSkipsArchived`:
  - archive U1 and it's absent from the list;
  - restore it and it's back in the same place.
- `TestRemoveAndReAddBuiltin`:
  - remove 2, then add 2 back;
  - the order is [1, 2, U…]; it returned to its place.
- `TestAddProjectTabRefusals`: one case per refusal above, each checked
  with `errors.Is`.
- `TestRemoveProjectTabUnreachable`:
  - a user dashboard with sidebar off and only this project's tab is
    refused with `ErrInvalid`;
  - with a second project's tab, removal succeeds.
- `TestMoveProjectTabRefusesBuiltin`.
- `TestMoveProjectTabRespreadsEqualKeys`:
  - force two user rows to the same `sort_key` with SQL;
  - a move between them succeeds and the order is as asked.
- `TestProjectTabsUnknownProject` → `ErrNotFound`.

- [ ] **Step 2: Run them and check that they fail**

Run: `go test ./internal/reporting -run 'ProjectTab|ReAddBuiltin' -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `project_tabs.go`**

Structure:

```go
// Project tabs (spec 2026-10-05): the dashboards a project page shows,
// Setup aside (the web app's own tab, not a row). Built-in tabs keep the
// release's order and can only be removed and added back; the user's own
// follow, ordered per project.
package reporting

// shownTabs joins rows to dashboards: built-ins by dashboard sort key,
// then the user's by row key; archived dashboards left out (their rows
// kept, so a restore brings the tab back where it was).
func (s *Service) shownTabs(ctx context.Context, projectID int64) ([]ProjectTab, []store.ProjectTabRow, map[int64]store.Dashboard, error)
```

`shownTabs` returns:
- the tabs;
- the user rows in shown order, for key arithmetic;
- the dashboards by id.

It sorts with `slices.SortStableFunc`: the system/user split first, then
the system dashboard's `SortKey`, or the user row's `SortKey` then id.
`ProjectTabs`, `AddProjectTab`, `RemoveProjectTab` and `MoveProjectTab`
each end with `return s.ProjectTabs(ctx, projectID)`.

- [ ] **Step 4: Run the reporting package and check that it passes**

Run: `go test ./internal/reporting/... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reporting
git commit -m "feat(reporting): list, add, remove and move a project's tabs"
```

---

### Task 5: API and docs — four tools, placement on update_dashboard

**Files:**
- Modify: `internal/api/ops_reporting.go`. Add input types, four
  adapters and four `expose` calls in `registerReporting`.
  `updateDashboardIn` gains `Sidebar *bool \`json:"sidebar,omitempty"\``
  and `ProjectTab *bool \`json:"project_tab,omitempty"\``. Update the
  descriptions of `update_dashboard`, `duplicate_dashboard`,
  `archive_dashboard`, `restore_dashboard` and `list_dashboards`.
- Modify: `internal/api/guide_reporting.go`, `serverInstructions`: "To
  customize a system dashboard, duplicate it with whole_group, then take
  the original out of the sidebar with update_dashboard {sidebar: false};
  add your copy to projects with add_project_tab."
- Modify: `internal/api/docs_sync_test.go`. Widen `spellOut` to `n > 50`
  and add the forties (`tens` map entry `4: "forty"` exists; just raise the
  upper bound to 49).
- Modify: `docs/reporting.md`:
  - the tool table: rows for the four tools, and `update_dashboard`'s
    `sidebar` and `project_tab`;
  - the routes table: four rows;
  - a new section, "Project tabs and the sidebar", stating D1, D3, D4
    and D5 in the doc's voice;
  - the passages on lines ~41, ~207, ~218, ~240, ~746, ~801 and ~816
    that describe hiding a system group by archiving: it's now
    `update_dashboard {sidebar: false}`;
  - a widget that doesn't follow `:project` shows the same data on every
    project page.
- Modify: `docs/twillingate.md`: "thirty-eight tools" becomes
  "forty-two tools", "sixteen" the matching word for the reporting count.
  Read the sentence at line ~905 and keep it true.
- Modify: `deploy/UPGRADES.md`: add a 032 entry in the existing format.
  It covers:
  - project pages gain dashboard tabs next to Setup;
  - hidden built-in groups stay hidden and leave the Archive page;
  - "Show in sidebar" in the gallery brings a hidden group back;
  - `archive_dashboard` on a built-in is now refused.
- Test: `internal/api/ops_reporting_test.go`, `internal/api/rest_test.go`

**Interfaces:**
- Consumes: Task 4's `Service` methods and Task 3's `UpdateDashboard`
  fields.
- Produces, the REST routes the web app uses in Tasks 6–8:

| Tool | Method | Path | Body | Returns |
| --- | --- | --- | --- | --- |
| `list_project_tabs` | GET | `/api/projects/{project_id}/tabs` | — | `{"tabs": ProjectTab[]}` |
| `add_project_tab` | POST | `/api/projects/{project_id}/tabs` | `{"dashboard_id", "after"?}` | `{"tabs": …}`, 201 |
| `remove_project_tab` | POST | `/api/projects/{project_id}/tabs/{dashboard_id}/remove` | `{}` | `{"tabs": …}` |
| `move_project_tab` | POST | `/api/projects/{project_id}/tabs/{dashboard_id}/move` | `{"after"}` | `{"tabs": …}` |
| `update_dashboard` | PATCH | `/api/dashboards/{dashboard_id}` | adds `"sidebar"`, `"project_tab"` | `DashboardInfo` |

```go
type projectTabsIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
}
type addProjectTabIn struct {
	ProjectID   int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	DashboardID int64  `json:"dashboard_id" jsonschema:"the dashboard to show on this project's page; list_dashboards names them"`
	After       *int64 `json:"after,omitempty" jsonschema:"your own dashboards only: after this tab of the project (one of your own), 0 first among your own; omit to put it last. A built-in goes back to its own place and takes no after"`
}
type projectTabIn struct {
	ProjectID   int64 `json:"project_id" jsonschema:"project id"`
	DashboardID int64 `json:"dashboard_id" jsonschema:"a tab of this project"`
}
type moveProjectTabIn struct {
	ProjectID   int64 `json:"project_id" jsonschema:"project id"`
	DashboardID int64 `json:"dashboard_id" jsonschema:"one of your own tabs of this project"`
	After       int64 `json:"after" jsonschema:"one of your own tabs of this project to go after, 0 first among your own; built-in tabs keep the release's order"`
}
type projectTabsOut struct {
	Tabs []reporting.ProjectTab `json:"tabs"`
}
```

The descriptions:
- `list_project_tabs`: "A project page's tabs after Setup, in order:
  built-in dashboards (release order), then your own (ordered per
  project). Each: dashboard_id, title, owner, group_id."
- `add_project_tab`: "Show a dashboard as a tab of a project's page. A
  built-in goes back to its own place; your own goes after `after`, or
  last. A dashboard already there is refused."
- `remove_project_tab`: "Take a tab off a project's page. The dashboard
  is kept, and add_project_tab brings the tab back. Refused when it would
  leave one of your own dashboards with no way in: not in the sidebar and
  on no project."
- `move_project_tab`: "Reorder your own tabs on a project's page; built-in
  tabs keep the release's order."

Annotations: `ro` for the list; `write` for add and move; `idem` for
remove. Follow the neighbours.

- [ ] **Step 1: Write the failing tests**

In `ops_reporting_test.go`, following its pattern (read it first):
- `TestProjectTabTools`, over MCP:
  - list → add a duplicate of dashboard 1 → move it → remove it;
  - the final list equals the initial one.
- `TestUpdateDashboardSidebarTool`: `{dashboard_id: 1, sidebar: false}`
  returns `sidebar: false`; `list_dashboards` shows it for every member
  of group 1.
- `TestArchiveBuiltinRefused`: `archive_dashboard {dashboard_id: 1,
  whole_group: true}` is a 400-class refusal.

In `rest_test.go`, a table row each for the four routes, matching the
existing route tests.

- [ ] **Step 2: Run them and check that they fail**

Run: `go test ./internal/api -run 'ProjectTab|SidebarTool|ArchiveBuiltinRefused' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the adapters, the registrations and the docs**

Adapters, following `getDashboard`'s style:

```go
func (h *host) listProjectTabs(ctx context.Context, in projectTabsIn) (projectTabsOut, error) {
	ts, err := h.rep.ProjectTabs(ctx, in.ProjectID)
	return projectTabsOut{Tabs: ts}, err
}
```

`addProjectTab`, `removeProjectTab` and `moveProjectTab` work the same
way, passing `actorFrom(ctx)`. `updateDashboard` passes `Sidebar:
in.Sidebar, ProjectTab: in.ProjectTab`.

Registrations, in `registerReporting` after `restore_dashboard`:

```go
	const pt = "/api/projects/{project_id}/tabs"
	expose(r, spec{Name: "list_project_tabs", Annotations: ro, Method: "GET", Path: pt, Description: "…"}, h.listProjectTabs)
	expose(r, spec{Name: "add_project_tab", Annotations: write, Method: "POST", Path: pt, Status: http.StatusCreated, Description: "…"}, h.addProjectTab)
	expose(r, spec{Name: "remove_project_tab", Annotations: idem, Method: "POST", Path: pt + "/{dashboard_id}/remove", Description: "…"}, h.removeProjectTab)
	expose(r, spec{Name: "move_project_tab", Annotations: write, Method: "POST", Path: pt + "/{dashboard_id}/move", Description: "…"}, h.moveProjectTab)
```

Then write the docs. Before running tests, read
`TestDocumentMatchesRoutes` and `TestDocumentNamesEveryTool` in
`docs_sync_test.go` to see which tables they read, and put the rows
there.

- [ ] **Step 4: Run the full check and check that it passes**

Run: `export PATH=$PATH:/usr/local/go/bin && make check`
Expected: PASS, including `docs_sync_test`, the OpenAPI test and the
archtest.

- [ ] **Step 5: Commit**

```bash
git add internal/api docs deploy/UPGRADES.md
git commit -m "feat(api): expose project tabs and sidebar placement"
```

---

### Task 6: Web — data layer, sidebar flag, Hide / Show in sidebar

**Files:**
- Modify: `web/src/lib/api.ts`:
  - `DashboardInfo` gains `sidebar: boolean` and `project_tab: boolean`;
  - `DashboardDetail` gains `project_ids: number[]`;
  - a new `ProjectTab` type;
  - new endpoints: `projectTabs`, `addProjectTab`, `removeProjectTab`,
    `moveProjectTab`, `setSidebar` and `setProjectTab`.
- Modify: `web/src/lib/queries.ts`. Add `projectTabsQuery(id)`.
- Create: `web/src/hooks/use-project-tab-actions.ts` (+ `.test.tsx`)
- Modify: `web/src/hooks/use-dashboard-actions.ts`:
  - add `setSidebar(d, sidebar)`, with an Undo toast "Hidden 'X'" or
    "Shown in the sidebar";
  - remove the `hidden` option from `archive`.
- Modify: `web/src/components/AppSidebar.tsx`. Build groups from
  `dashboards.filter((d) => d.sidebar)`.
- Modify: `web/src/pages/Home.tsx`. `live` also requires `d.sidebar`.
- Modify: `web/src/components/SidebarGroupMenu.tsx` and
  `web/src/components/DashboardMenu.tsx` (GroupMenu). A system group's
  **Hide** calls `setSidebar(first, false)` instead of `archive`.
- Modify: `web/src/components/TemplateMenu.tsx`. **Show in sidebar**
  calls `setSidebar(d, true)`.
- Modify: `web/src/pages/gallery/DashboardsGallery.tsx`. `hidden =
  g.members.every((m) => !m.sidebar)`.
- Modify: `web/src/pages/Dashboard.tsx`. The banner shows "Hidden from the
  sidebar" with **Show in sidebar** for a built-in with `!sidebar`, and
  "Archived: not in the sidebar" with **Restore** for an archived user
  dashboard.
- Modify: `web/src/pages/Archive.tsx`. Only `owner === 'user'` groups;
  drop the system-group wording.
- Test: update every Vitest fixture that builds a `DashboardInfo`
  (`grep -rln "owner: 'system'\|'system', " web/src --include=*.test.ts*`).
  Each needs `sidebar: true, project_tab: …`; a helper default is fine.

**Interfaces:**
- Consumes: Task 5's routes.
- Produces (used by Tasks 7–8):

```ts
export interface ProjectTab { dashboard_id: number; title: string; owner: 'system' | 'user'; group_id: number }

endpoints.projectTabs(projectId: number): Promise<{ tabs: ProjectTab[] }>
endpoints.addProjectTab(projectId: number, body: { dashboard_id: number; after?: number }): Promise<{ tabs: ProjectTab[] }>
endpoints.removeProjectTab(projectId: number, dashboardId: number): Promise<{ tabs: ProjectTab[] }>
endpoints.moveProjectTab(projectId: number, dashboardId: number, after: number): Promise<{ tabs: ProjectTab[] }>
endpoints.setSidebar(dashboardId: number, sidebar: boolean): Promise<DashboardInfo>     // PATCH /api/dashboards/{id} {sidebar}
endpoints.setProjectTab(dashboardId: number, on: boolean): Promise<DashboardInfo>       // PATCH /api/dashboards/{id} {project_tab}

export const projectTabsQuery = (projectId: number) => ({
  queryKey: ['project-tabs', projectId],
  queryFn: () => endpoints.projectTabs(projectId),
})

// use-project-tab-actions.ts
export interface ProjectTabActions {
  add(projectId: number, dashboardId: number, after?: number): Promise<boolean>
  remove(projectId: number, tab: { dashboard_id: number; title: string }): Promise<boolean> // toast "Removed 'X'" with Undo (re-add)
  move(projectId: number, dashboardId: number, after: number): Promise<boolean>
  pending: boolean
}
export function useProjectTabActions(): ProjectTabActions
```

`useProjectTabActions` follows `useDashboardActions`'s `run` pattern:
- a refusal → `toast.error(message)` and resolve `false`;
- a dropped connection → "Couldn't reach the server";
- after each call it writes the returned `tabs` into
  `['project-tabs', projectId]` with `setQueryData`, and invalidates
  `['dashboard']` so `project_ids` refreshes.

- [ ] **Step 1: Write the failing tests**
- `use-project-tab-actions.test.tsx`, mocking `endpoints` the way
  `use-dashboard-actions.test.tsx` does:
  - `remove` shows a toast with Undo, and Undo calls `addProjectTab`;
  - a refused add resolves `false` and toasts the server's message.
- `AppSidebar.test.tsx`: a system group with `sidebar: false` isn't
  listed.
- `DashboardsGallery.test.tsx`: the "hidden" marker follows `sidebar`,
  not `archived_at`.
- `DashboardMenu.test.tsx` and `SidebarGroupMenu.test.tsx`: **Hide**
  calls `endpoints.setSidebar(id, false)`.

- [ ] **Step 2: Run them and check that they fail**

Run: `cd web && npx vitest run --testTimeout=30000 src/hooks src/components src/pages/gallery`
Expected: FAIL.

- [ ] **Step 3: Implement**, as listed under Files.

- [ ] **Step 4: Typecheck and run all the Vitest suites**

Run: `cd web && npx tsc -b && npx vitest run --testTimeout=30000`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): hide built-in dashboards with the sidebar flag"
```

---

### Task 7: Web — project page with tabs

**Files:**
- Modify: `web/src/App.tsx`. Replace the `/projects/:id` route with:
  - `/projects/:id` → `<Navigate to="setup" replace />`. Keep the search:
    use a small `ProjectIndex` component that reads `useLocation().search`
    and navigates to `setup${search}`.
  - `/projects/:id/setup` → `<Project />`.
  - `/projects/:id/dashboards/:dashId` → `<Project />`.
- Modify: `web/src/pages/Project.tsx`. It becomes the shell: crumbs, the
  header (name with rename, Archived badge), `ProjectTabBar`, then
  `SetupTab` or `ProjectDashboardTab`, by route.
- Create: `web/src/components/projects/SetupTab.tsx`. It holds today's
  sections plus the Archive / Restore button and dialog, moved out of
  `Project.tsx`.
  - The range comes from the URL `range`/`from`/`to`, else 30d.
  - Changing the range writes the URL (`setSearchParams`).
- Create: `web/src/components/projects/ProjectDashboardTab.tsx`
- Create: `web/src/components/projects/ProjectTabBar.tsx`
- Create: `web/src/components/projects/AddTabDialog.tsx`
- Create: `web/src/components/projects/ProjectTabMenu.tsx`
- Create: `web/src/lib/project-tabs.ts` (+ `.test.ts`)
- Modify: `web/src/components/ReportTabs.tsx`. Add optional props:
  - `fixedIds?: number[]`: these tabs render first, as plain
    `TabsTrigger`s, never draggable;
  - `trailing?: ReactNode`: rendered after the list, and beside the
    select on phones.
  - `useReorder` runs over the movable ids only, and `onMove(id, to)`
    gets `to` as an index among the movable ids.
- Test: `web/src/pages/Project.test.tsx`; update and extend it.
  `web/src/components/ReportTabs.test.tsx`;
  `web/src/lib/project-tabs.test.ts`

**Interfaces:**
- Consumes: Task 6's `projectTabsQuery`, `useProjectTabActions`,
  `ProjectTab`, `endpoints`; the existing `dashboardQuery`,
  `dashboardsQuery`, `projectsQuery`; `DashboardHeader`, `WidgetGrid`,
  `RangeSwitcher`, `useFreshness`, `useAutoRefresh`, `useStoredState`,
  `widgetParams`, `selectionParams`, `chooseSelection`, `resolve`.
- Produces:

```ts
// lib/project-tabs.ts
export const SETUP_ID = 0 // the Setup tab's id in ReportTabs; no dashboard has id 0
export function tabPath(projectId: number, dashboardId: number, search = ''): string // SETUP_ID → `/projects/${p}/setup`, else `/projects/${p}/dashboards/${d}`; search appended
export function rangeParams(url: URLSearchParams): URLSearchParams // only range/from/to, carried across tabs
export function pickerSections(dashboards: DashboardInfo[], tabs: ProjectTab[]): { builtin: DashboardInfo[]; own: DashboardInfo[] }
//   builtin: system dashboards not in tabs, list order; own: live user dashboards not in tabs, list order
export function userAfter(tabs: ProjectTab[], id: number, to: number): number
//   the `after` for moving user tab `id` to index `to` among the user tabs: 0 for first, else the id of the user tab before it once `id` is taken out
```

**The project page's tab row (`ProjectTabBar`):**
- The order is Setup (`SETUP_ID`, titled "Setup"), then `tabs` from
  `projectTabsQuery`.
- `fixedIds` are `SETUP_ID` plus every built-in tab's id.
- Choosing a tab navigates to `tabPath(projectId, id, rangeParams(url))`.
- `onMove(id, to)` calls `actions.move(projectId, id, userAfter(userTabs,
  id, to))`.
- `trailing` is a ghost icon `Button` with `aria-label="Add tab"` that
  opens `AddTabDialog`.

**`AddTabDialog`:**
- A `Dialog` with two lists, headed "Built-in" and "Your dashboards".
  Each item is a `button` with the dashboard's title.
- Choosing one calls `actions.add(projectId, id)`, closes the dialog and
  navigates to the new tab.
- When both lists are empty, it shows "Every dashboard is already a tab
  of this project".
- There is no "new dashboard" entry.

**`ProjectDashboardTab`:** a dashboard id with the project pinned.
- Loads `dashboardQuery(dashId)`.
- If `dashId` isn't in `tabs`, it renders "{title} isn't a tab of
  {project}", with a **Add tab** button that calls `actions.add`.
- Otherwise it renders `DashboardHeader`:
  - `title`;
  - `asOf` / refresh from `useFreshness`;
  - `menu={<ProjectTabMenu …/>}`;
  - children `<RangeSwitcher>` when `follows_range`.
- Then `WidgetGrid widgets paramsFor`.
- The selection is
  `chooseSelection(url, {range: dashboard.range, from: dashboard.from, to:
  dashboard.to}, [projectId], {project: false, range:
  dashboard.follows_range})`. `projectId` is set from the route.
  `paramsFor = (w) => widgetParams(w, {projectId}, range)`.
- Changing the range calls
  `setSearchParams(selectionParams({range, from, to}))`. It never calls
  `endpoints.saveView`: viewing a project doesn't write a dashboard's
  saved view (D8).
- Auto-refresh works as on `Dashboard.tsx`, keyed per dashboard group.

**`ProjectTabMenu`:** a `DropdownMenu` with these items:
- **Remove from this project**, which calls `actions.remove` and then
  navigates to the previous tab, or Setup;
- **Open as dashboard**, a `Link` to `/dashboards/${id}?project=${projectId}`;
- for a user tab on phones (`useIsMobile()`), **Move left** /
  **Move right**, calling `actions.move` with `userAfter`.

**`Project.tsx`:**
- `AppShell currentId={0}`;
- `TopBar` with `Crumbs`: `Projects › name`;
- the header, as today, without the RangeSwitcher and Archive buttons
  (they move to `SetupTab`);
- `ProjectTabBar`;
- the body.
- An unknown project keeps today's "No project {param}".

- [ ] **Step 1: Write the failing tests**
- `project-tabs.test.ts`:
  - `tabPath` for Setup and for a dashboard, with and without a search;
  - `rangeParams` drops `project` and keeps `range`, `from` and `to`;
  - `pickerSections` leaves out dashboards already tabs, leaves out
    archived user dashboards, and keeps hidden built-ins
    (`sidebar: false`);
  - `userAfter` gives 0 for index 0, and the right neighbour after the
    moving tab is taken out.
- `ReportTabs.test.tsx`: with `fixedIds=[0, 1]` and tabs `[0, 1, 1001,
  1002]`, only 1001 and 1002 are draggable, and `trailing` renders.
- `Project.test.tsx` (mock `endpoints` as the existing test does):
  - `/projects/7` redirects to `/projects/7/setup`, and the Usage section
    renders;
  - the tab row reads Setup, Views, Product, Mine, then the
    "Add tab" button;
  - `/projects/7/dashboards/1` renders the Views title and its widgets,
    and widget data is requested with `project_id=7`;
  - `/projects/7/dashboards/1?range=30d`: switching to Product keeps
    `range=30d` in the URL;
  - `/projects/7/dashboards/1001` with 1001 not in the tabs shows
    "isn't a tab of", and Add tab calls `addProjectTab(7, {dashboard_id:
    1001})`;
  - the Add tab dialog lists a removed built-in under "Built-in";
  - `saveView` is never called.

- [ ] **Step 2: Run them and check that they fail**

Run: `cd web && npx vitest run --testTimeout=30000 src/lib/project-tabs.test.ts src/components/ReportTabs.test.tsx src/pages/Project.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement**, as above. Move code rather than copying it:
  `SetupTab` takes the sections and the archive dialog verbatim from
  `Project.tsx`.

- [ ] **Step 4: Typecheck and run all the Vitest suites**

Run: `cd web && npx tsc -b && npx vitest run --testTimeout=30000`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): show dashboards as tabs of a project, next to its Setup"
```

---

### Task 8: Web — "Project tabs…" on your own dashboards

**Files:**
- Create: `web/src/components/ProjectTabsDialog.tsx` (+ `.test.tsx`)
- Modify: `web/src/components/DashboardMenu.tsx`. `TabMenu` gains a
  **Project tabs…** item, shown only when `dashboard.owner === 'user' &&
  !dashboard.archived_at`. It opens the dialog.

**Interfaces:**
- Consumes: `DashboardDetail.project_ids`, `projectsQuery`,
  `useProjectTabActions`, `endpoints.setProjectTab`.
- Produces:
  `ProjectTabsDialog({ dashboard: DashboardDetail, open: boolean,
  onOpenChange(open: boolean): void })`.

**Behaviour:**
- A `Dialog` titled "Project tabs".
- One `Checkbox` per project: active projects first, then archived ones
  marked "(archived)". Checked when the project id is in `project_ids`.
- Checking a box calls `actions.add(projectId, dashboard.dashboard_id)`;
  unchecking calls `actions.remove(projectId, dashboard)`. A refusal
  (for example "would be unreachable") shows as a toast, and the box
  snaps back once `['dashboard', id]` refetches.
- Below the boxes, a `Switch` labelled "Add to new projects", bound to
  `dashboard.project_tab`, which calls `endpoints.setProjectTab`. It
  invalidates `['dashboard']` and `['dashboards']`.
- Every row is a `label` wrapping its control, so the click target shows
  the pointer cursor through the base rule.
- Use `grid-cols-1` for the list.

- [ ] **Step 1: Write the failing test**

`ProjectTabsDialog.test.tsx`:
- boxes are checked for the projects in `project_ids`;
- clicking an unchecked box calls `addProjectTab(p, {dashboard_id})`;
- clicking a checked one calls `removeProjectTab(p, id)`;
- toggling the switch calls `setProjectTab(id, true)`.

In `DashboardMenu.test.tsx`, the item shows for a live user dashboard,
and not for a system one.

- [ ] **Step 2: Run them and check that they fail**

Run: `cd web && npx vitest run --testTimeout=30000 src/components/ProjectTabsDialog.test.tsx src/components/DashboardMenu.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Typecheck and run all the Vitest suites**

Run: `cd web && npx tsc -b && npx vitest run --testTimeout=30000`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): choose the projects a dashboard of your own is a tab of"
```

---

### Task 9: e2e and the full check

**Files:**
- Modify: `web/e2e/phone.spec.ts`. For the long-name project it creates,
  also visit `/app/projects/${id}/setup` and
  `/app/projects/${id}/dashboards/1`.
- Modify: `web/e2e/projects.spec.ts`. After clicking the card, expect the
  URL `/projects/\d+/setup`. Everything after it is on Setup and should
  pass unchanged.
- Modify: `web/e2e/cursor.spec.ts`. Visit a project page and open the
  Add tab dialog, so the **+** button and the picker items are checked.
  Read how the spec collects pages first.
- Modify: `web/e2e/gallery.spec.ts` / `arrange.spec.ts`. Any step that
  hides a system group and then expects it on the Archive page now
  expects it absent there, and back through the gallery's **Show in
  sidebar**.
- Create: `web/e2e/project-tabs.spec.ts`

**Interfaces:**
- Consumes: everything above; `web/e2e/serve.sh`, which seeds project
  `dev`.

- [ ] **Step 1: Write `project-tabs.spec.ts`**

```ts
import { expect, test, type Page } from '@playwright/test'

const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

test('a project opens on Setup with the built-in tabs beside it; tabs are removed, re-added and reordered', async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${TOKEN}` }
  // A dashboard of our own to add: a copy of Views, made over the API.
  const copy = await request.post('/api/dashboards/1/duplicate', { headers, data: {} })
  expect(copy.ok(), await copy.text()).toBeTruthy()
  const mine = (await copy.json()) as { dashboard_id: number; title: string }

  await login(page)
  await page.getByRole('article', { name: 'dev' }).getByRole('link', { name: 'dev' }).click()
  await expect(page).toHaveURL(/\/app\/projects\/\d+\/setup$/)
  const tabs = page.getByRole('tablist', { name: 'Tabs' })
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', 'Views', 'Product', 'Users', 'Groups', 'Retention', 'Web Vitals', 'Measures'])

  await tabs.getByRole('tab', { name: 'Product' }).click()
  await expect(page).toHaveURL(/\/dashboards\/2/)
  await page.getByRole('button', { name: /Product.*actions|More/ }).first().click()
  await page.getByRole('menuitem', { name: 'Remove from this project' }).click()
  await expect(tabs.getByRole('tab', { name: 'Product' })).toHaveCount(0)

  await page.getByRole('button', { name: 'Add tab' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Product' }).click()
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', 'Views', 'Product', 'Users', 'Groups', 'Retention', 'Web Vitals', 'Measures'])

  await page.getByRole('button', { name: 'Add tab' }).click()
  await page.getByRole('dialog').getByRole('button', { name: mine.title }).click()
  await expect(tabs.getByRole('tab').last()).toHaveText(mine.title)

  await tabs.getByRole('tab', { name: 'Setup' }).click()
  await expect(page.getByRole('region', { name: 'Allowed origins' })).toBeVisible()

  // Archiving our own dashboard takes its tab away.
  const archived = await request.post(`/api/dashboards/${mine.dashboard_id}/archive`, { headers, data: {} })
  expect(archived.ok(), await archived.text()).toBeTruthy()
  await page.reload()
  await expect(tabs.getByRole('tab', { name: mine.title })).toHaveCount(0)
})
```

Adjust selectors to what Task 7 actually renders: the tab menu trigger's
`aria-label`, and the tablist's name, which comes from `ReportTabs`
`aria-label="Tabs"`. Adjust them by reading the components, not by
loosening the assertions.

- [ ] **Step 2: Run e2e and check the result**

Run: `cd web && npm run e2e`
Expected: PASS. Port 18080 may be taken by another session's stale
server. If `serve.sh` fails to bind, find and stop only that stale
`twillingate serve` of a scratch `twillingate-e2e-*` directory, then rerun.

- [ ] **Step 3: Run the full check**

Run: `export PATH=$PATH:/usr/local/go/bin && make check && cd web && npx tsc -b && npx vitest run --testTimeout=30000`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add web/e2e
git commit -m "test(web): cover project tabs end to end"
```
