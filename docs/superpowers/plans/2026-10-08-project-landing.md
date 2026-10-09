# Project Landing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A project opens on the last dashboard tab you used there; every tab (built-ins too) can be reordered; Setup becomes Settings behind a gear; Forms becomes an inbox icon with a count of new submissions.

**Architecture:** Migration 037 puts every project tab in one `project_tabs.sort_key` order and adds `forms.seen_at`. `internal/reporting` orders and places tabs by row key alone. `internal/api` adds two REST-only routes (mark a form seen; per-project activity) and two `list_forms` fields. The web app keeps the last tab in localStorage, decides the landing tab in `ProjectIndex`, and moves Settings and Forms out of the tab row into icon buttons.

**Tech Stack:** Go 1.24+, modernc SQLite, React 19 + TypeScript + TanStack Query + react-router + dnd-kit, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-08-project-landing-design.md`

## Global Constraints

- Go is at `/usr/local/go/bin` (not on PATH): prefix commands with `PATH=/usr/local/go/bin:$PATH`.
- `make check` before every push; vitest on this machine needs `--testTimeout=30000`.
- Commits: Conventional Commits, scopes from CLAUDE.md (`store`, `reporting`, `api`, `web`, …). End every commit message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Docs change in the same commit as the code they describe (CLAUDE.md table).
- Refusals are typed (`store.Refuse(store.ErrInvalid, …)`), matched with `errors.Is`.
- Everything clickable is a `button`, a link or `role="button"` (pointer cursor rule); nothing scrolls sideways at 360px.
- localStorage key: `twillingate.project.last_tab`, JSON object `{ "<project id>": <dashboard id> }`.
- Settings route: `/projects/:id/settings`; `/projects/:id/setup` redirects there keeping the query string.
- Settings button: tooltip and aria-label "Settings". Forms button: aria-label "Forms" or "Forms, N new"; badge hidden at 0, "99+" above 99.
- No-data notice text: "No events received yet" with a link "Set up this project" to Settings.
- Migration number: 037 (`internal/store/sqlite/migrations/037_project_landing.sql`).

## Spec corrections made by this plan (Task 3 updates the spec file)

- **D6 moves off `list_projects`.** `listProjects` reads the registry snapshot and nothing else on purpose ("the web app waits on it before loading any widget", `internal/api/ops_read.go:106`). The two numbers go on a new REST-only route, `GET /api/project-activity`, answering `{"projects": [{"project_id", "last_event_day", "new_submissions"}]}` for every live project.
- **`last_event_day` is not from `actors`.** `actors` holds only user/install-identified actors and is filled by the nightly pass (`retention.go` `UpsertActors`), so a web-only project would never show data. It is the newest `day` across `raw_views`, `raw_product`, `raw_measures`, `agg_views_daily`, `agg_product_totals`, `agg_measures_daily` for the project (the pattern `usage` uses, `internal/api/ops_usage.go:198`).
- **`until` is the form's `last_submitted_at`** as the form page loaded it (from `list_forms`), not a row of the table: the table can be filtered or sorted, the form record cannot.

## Review Focus

- **A remembered tab that is gone** (removed from the project, archived, or a dashboard deleted): the project opens on its first tab, never on a "isn't a tab" notice. Test: `landingPath` vitest (Task 4).
- **localStorage blocked or holding junk** (private mode, a non-object, a string id): opening a project falls back to the first tab without throwing. Test: `last-tab.test.ts` (Task 4).
- **A stale page marking a form seen with an older `until`** after a newer page already did: `seen_at` never moves back. Test: `TestMarkFormSeenNeverMovesBack` (Task 1).
- **A release adding a built-in to a project whose tabs were reordered:** it lands last, not wedged between the user's tabs by its `dashboards.sort_key`. Test: `TestMigration037NewBuiltinGoesLast` (Task 1).
- **The phone tab select while Settings or Forms is open:** no tab is current; the select shows the placeholder "Tabs", not an empty box or a wrong tab. Test: `ProjectTabBar.test.tsx` (Task 5).

Known, accepted: timestamps are whole seconds, so a submission received in the same second as `seen_at`, after the page loaded, counts as seen.

---

### Task 1: Migration 037 and the store's seen state

**Files:**
- Create: `internal/store/sqlite/migrations/037_project_landing.sql`
- Create: `internal/store/sqlite/migration037_test.go`
- Modify: `internal/store/forms.go` (`Form` gains `SeenAt`, `NewSubmissions`)
- Modify: `internal/store/store.go` (interface: `MarkFormSeen`)
- Modify: `internal/store/sqlite/forms.go` (`formColumns`, `scanForm`, `ListForms`, new `MarkFormSeen`)
- Modify: `internal/manage/store.go` (`manage.Store` gains `MarkFormSeen`)
- Test: `internal/store/sqlite/forms_test.go`
- Modify: `deploy/UPGRADES.md`

**Interfaces:**
- Produces:
  - `store.Form.SeenAt *time.Time` (nil: never seen) and `store.Form.NewSubmissions int` (filled by `ListForms` only, like `Submissions`).
  - `(*sqlite.DB).MarkFormSeen(ctx context.Context, projectID int64, name string, until time.Time) error` — `ErrNotFound` (via `unknownForm`) for an unknown form; never audited.
  - Table `forms` column `seen_at TEXT` (tsFormat `2006-01-02T15:04:05Z`).
  - `project_tabs.sort_key` is the only order of a project's tabs from here on.

- [ ] **Step 1: Write the failing migration tests**

`internal/store/sqlite/migration037_test.go` (helpers `newTestDBAt`, `execAll`, `migrateThrough` exist — see `migration036_test.go`):

```go
package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
)

// tabOrder lists a project's tab dashboard ids by row key alone.
func tabOrder(t *testing.T, db *DB, project int64) []int64 {
	t.Helper()
	rows, err := db.db.Query(`SELECT dashboard_id FROM project_tabs WHERE project_id=? ORDER BY sort_key, dashboard_id`, project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// TestMigration037KeepsShownOrder checks the keys are rewritten into the
// order the page showed before: built-ins by dashboards.sort_key, then the
// user's by row key, an archived user row kept at its place.
func TestMigration037KeepsShownOrder(t *testing.T) {
	db := newTestDBAt(t, 36)
	// Built-ins seeded by migrations already sit on every project; read the
	// order the page showed at 036 and add user dashboards around them.
	execAll(t, db,
		`INSERT INTO projects (id, name, sort_key) VALUES (901, 'p901', 'zz')`,
		`INSERT INTO dashboards (id, title, owner, sort_key, group_id, archived_at)
		 VALUES (9001, 'mine a', 'user', 'a0', 9001, NULL),
		        (9002, 'mine b', 'user', 'a1', 9002, '2026-10-01T00:00:00Z'),
		        (9003, 'mine c', 'user', 'a2', 9003, NULL)`,
		`INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
		 VALUES (901, 9003, 'a3'), (901, 9002, 'a2'), (901, 9001, 'a1')`)
	var builtins []int64
	rows, err := db.db.Query(`SELECT d.id FROM project_tabs pt JOIN dashboards d ON d.id = pt.dashboard_id
		WHERE pt.project_id = 901 AND d.owner = 'system' ORDER BY d.sort_key, d.id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		builtins = append(builtins, id)
	}
	rows.Close()
	want := append(builtins, 9001, 9002, 9003)

	if err := db.migrateThrough(context.Background(), 37); err != nil {
		t.Fatal(err)
	}
	if got := tabOrder(t, db, 901); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	keys, _ := db.db.Query(`SELECT sort_key FROM project_tabs`)
	defer keys.Close()
	for keys.Next() {
		var k string
		_ = keys.Scan(&k)
		if _, err := sortkey.Between(k, ""); err != nil {
			t.Errorf("key %q is not a valid sort key: %v", k, err)
		}
	}
}

// TestMigration037NewBuiltinGoesLast checks a built-in a release adds
// lands after every tab of each project, whatever its dashboards.sort_key.
func TestMigration037NewBuiltinGoesLast(t *testing.T) {
	db := newTestDBAt(t, 37)
	execAll(t, db,
		`INSERT INTO projects (id, name, sort_key) VALUES (902, 'p902', 'zz')`,
		`INSERT INTO dashboards (id, title, owner, sort_key, group_id, project_tab)
		 VALUES (9100, 'new built-in', 'system', '!', 9100, 1)`)
	got := tabOrder(t, db, 902)
	if len(got) == 0 || got[len(got)-1] != 9100 {
		t.Errorf("order = %v, want 9100 last", got)
	}
}

// TestMigration037FormsSeen checks existing forms are seen at the upgrade
// and a form created later starts unseen.
func TestMigration037FormsSeen(t *testing.T) {
	db := newTestDBAt(t, 36)
	execAll(t, db, `INSERT INTO forms (project_id, name) VALUES (1, 'old')`)
	if err := db.migrateThrough(context.Background(), 37); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, `INSERT INTO forms (project_id, name) VALUES (1, 'new')`)
	var old, fresh *string
	_ = db.db.QueryRow(`SELECT seen_at FROM forms WHERE name='old'`).Scan(&old)
	_ = db.db.QueryRow(`SELECT seen_at FROM forms WHERE name='new'`).Scan(&fresh)
	if old == nil || fresh != nil {
		t.Errorf("seen_at old=%v new=%v, want set and NULL", old, fresh)
	}
}
```

Adjust the `INSERT INTO dashboards` / `projects` column lists to the real NOT NULL columns of those tables at 036 (read `022_*`, `031_*`, `032_*`, `034_*` migrations) — the shape above is the intent.

- [ ] **Step 2: Run them to see them fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/store/sqlite/ -run 'TestMigration037' -count=1`
Expected: FAIL (no migration 37).

- [ ] **Step 3: Write the migration**

`internal/store/sqlite/migrations/037_project_landing.sql`:

```sql
-- 037: project landing (spec 2026-10-08). One order for all of a
-- project's tabs: project_tabs.sort_key, built-ins included, so a
-- built-in can be dragged like your own. The keys are rewritten here into
-- the order the page showed (built-ins by dashboards.sort_key, then your
-- own by row key, archived rows kept at their place) as 'b' + two base-62
-- digits of the rank: valid fractional keys (internal/shared/sortkey) for
-- up to 3,844 tabs on one project. A built-in a release adds goes last on
-- every project. forms.seen_at is how far the console has read a form's
-- submissions; existing forms are seen as of the upgrade.
WITH ranked AS (
  SELECT pt.project_id, pt.dashboard_id,
         ROW_NUMBER() OVER (
           PARTITION BY pt.project_id
           ORDER BY d.owner = 'system' DESC,
                    CASE WHEN d.owner = 'system' THEN d.sort_key ELSE pt.sort_key END,
                    pt.dashboard_id) - 1 AS r
    FROM project_tabs pt JOIN dashboards d ON d.id = pt.dashboard_id
)
UPDATE project_tabs SET sort_key =
    'b' || substr('0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz', ranked.r / 62 + 1, 1)
        || substr('0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz', ranked.r % 62 + 1, 1)
  FROM ranked
 WHERE project_tabs.project_id = ranked.project_id
   AND project_tabs.dashboard_id = ranked.dashboard_id;

-- A new built-in goes after the project's last tab: its largest key with
-- 'V' appended sorts after it and is still a valid key.
DROP TRIGGER project_tabs_new_builtin;
CREATE TRIGGER project_tabs_new_builtin AFTER INSERT ON dashboards
WHEN NEW.owner = 'system' AND NEW.project_tab = 1
BEGIN
  INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
    SELECT p.id, NEW.id,
           COALESCE((SELECT MAX(pt.sort_key) FROM project_tabs pt WHERE pt.project_id = p.id) || 'V', 'a0')
      FROM projects p;
END;

ALTER TABLE forms ADD COLUMN seen_at TEXT;
UPDATE forms SET seen_at = strftime('%Y-%m-%dT%H:%M:%SZ','now');
```

If the migration runner or `sqlite.go` lists migrations explicitly (check how 036 is registered), register 037 the same way. Bump any "latest schema version" constant and the snapshot cache key if tests pin one (`race-suite-snapshots`: the snapshot is per schema version, so it rebuilds itself).

- [ ] **Step 4: Run the migration tests**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/store/sqlite/ -run 'TestMigration037' -count=1`
Expected: PASS. Then run the whole package to catch tests that pin the schema version or the trigger SQL: `go test ./internal/store/sqlite/ -count=1`. Fix pinned versions (a migration test that pins its ceiling uses `newTestDBAt`, so it is unaffected).

- [ ] **Step 5: Write the failing store tests**

Append to `internal/store/sqlite/forms_test.go` (reuse its helpers for creating a form and writing submissions — read the top of the file; the names below are illustrative):

```go
func TestListFormsNewSubmissions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	submitAt(t, db, 1, "contact", "s1", t0)
	submitAt(t, db, 1, "contact", "s2", t0.Add(time.Minute))
	fs, err := db.ListForms(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if fs[0].SeenAt != nil || fs[0].NewSubmissions != 2 {
		t.Fatalf("unseen form: seen %v new %d, want nil 2", fs[0].SeenAt, fs[0].NewSubmissions)
	}
	if err := db.MarkFormSeen(ctx, 1, "contact", t0); err != nil {
		t.Fatal(err)
	}
	fs, _ = db.ListForms(ctx, 1, false)
	if fs[0].NewSubmissions != 1 || fs[0].SeenAt == nil || !fs[0].SeenAt.Equal(t0) {
		t.Fatalf("after seen at t0: seen %v new %d, want t0 1", fs[0].SeenAt, fs[0].NewSubmissions)
	}
}

func TestMarkFormSeenNeverMovesBack(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	submitAt(t, db, 1, "contact", "s1", t0)
	if err := db.MarkFormSeen(ctx, 1, "contact", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkFormSeen(ctx, 1, "contact", t0); err != nil {
		t.Fatal(err)
	}
	f, _ := db.GetForm(ctx, 1, "contact")
	if f.SeenAt == nil || !f.SeenAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("seen_at = %v, want t0+1h", f.SeenAt)
	}
}

func TestMarkFormSeenUnknownForm(t *testing.T) {
	db := newTestDB(t)
	err := db.MarkFormSeen(context.Background(), 1, "nope", time.Now())
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 6: Run them to see them fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/store/sqlite/ -run 'NewSubmissions|MarkFormSeen' -count=1`
Expected: FAIL to compile (`SeenAt`, `NewSubmissions`, `MarkFormSeen` undefined).

- [ ] **Step 7: Implement**

`internal/store/forms.go`, in `Form` after `ArchivedAt`:

```go
	SeenAt          *time.Time // how far the console has read; nil: never
	Submissions     int        // filled by ListForms only
	NewSubmissions  int        // received after SeenAt; filled by ListForms only
```

`internal/store/sqlite/forms.go`:
- Append `, seen_at` to `formColumns`; in `scanForm` add a `seen sql.NullString` scanned after `archived`, and `{seen, &f.SeenAt}` to the time list.
- `ListForms` selects a second count and scans it:

```go
	rows, err := d.db.QueryContext(ctx, `SELECT `+formColumns+`,
		(SELECT COUNT(*) FROM submissions s WHERE s.project_id=f.project_id AND s.form=f.name),
		(SELECT COUNT(*) FROM submissions s WHERE s.project_id=f.project_id AND s.form=f.name
		   AND s.received_at > COALESCE(f.seen_at, ''))
		FROM forms f WHERE f.project_id=? AND `+where+`
		ORDER BY f.status='draft' DESC, f.name`, projectID)
	…
		var n, fresh int
		f, err := scanForm(rows, &n, &fresh)
		…
		f.Submissions, f.NewSubmissions = n, fresh
```

`formColumns` is unqualified; if `ListForms` aliases the table `f`, check the columns still resolve (they do today with `f`).

- New method:

```go
// MarkFormSeen records that the console has read a form's submissions up
// to until (spec 2026-10-08 D5). seen_at only moves forward, so a page
// opened earlier can't unread what a later one read. Console state: not
// audited.
func (d *DB) MarkFormSeen(ctx context.Context, projectID int64, name string, until time.Time) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE forms SET seen_at = MAX(COALESCE(seen_at, ''), ?) WHERE project_id=? AND name=?`,
		until.UTC().Format(tsFormat), projectID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return unknownForm(projectID, name)
	}
	return nil
}
```

- `internal/store/store.go`: add to the Store interface next to `GetForm`:

```go
	// MarkFormSeen moves a form's seen_at forward to until (never back);
	// ErrNotFound for an unknown form. Not audited.
	MarkFormSeen(ctx context.Context, projectID int64, name string, until time.Time) error
```

- `internal/manage/store.go`: add the same line to `manage.Store`'s forms block. Any fake store in `internal/manage` tests must gain the method (compile errors point to them).

- [ ] **Step 8: Run the store and manage tests**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/store/... ./internal/manage/... -count=1`
Expected: PASS.

- [ ] **Step 9: UPGRADES.md**

Add a section for migration 037 to `deploy/UPGRADES.md` in the style of the 036 entry: no pre-checks; on upgrade day every existing form is marked seen (the new Forms badge starts at 0); project tab order is unchanged on screen; built-in tabs can now be moved.

- [ ] **Step 10: Commit**

```bash
git add internal/store deploy/UPGRADES.md internal/manage/store.go
git commit -m "feat(store): order every project tab by its row and record how far forms are read"
```

---

### Task 2: One tab order in the reporting service

**Files:**
- Modify: `internal/reporting/project_tabs.go`
- Modify: `internal/reporting/project_tabs_test.go`
- Modify: `internal/api/ops_reporting.go:326-335` (descriptions of `add_project_tab`, `move_project_tab`)
- Modify: `docs/reporting.md` (project tab order)
- Modify: `docs/twillingate.md` if it describes these two tools' refusals

**Interfaces:**
- Consumes: migration 037's single `sort_key` order (Task 1).
- Produces:
  - `AddProjectTab.After *int64`: nil last among all tabs, 0 first, an id right after that live tab — for built-ins and your own alike.
  - `MoveProjectTab.After int64`: 0 first, else any live tab of the project.
  - `ProjectTabs` returns tabs in row-key order (ties by dashboard id).

- [ ] **Step 1: Write the failing tests**

In `internal/reporting/project_tabs_test.go`, delete `TestMoveProjectTabRefusesBuiltin` and change the built-in `after` case in `TestAddProjectTabRefusals` (it must now succeed). Add (use the file's existing helpers for a service with built-ins, a project and user dashboards; read `TestProjectTabsOrder` first):

```go
func TestMoveBuiltinTab(t *testing.T) {
	s, project, builtins, own := tabsFixture(t) // built-ins in release order, then own tabs
	first := builtins[0]
	// Built-in to the end, after the user's last tab.
	tabs, err := s.MoveProjectTab(ctx, "t", MoveProjectTab{ProjectID: project, DashboardID: first, After: own[len(own)-1]})
	if err != nil {
		t.Fatal(err)
	}
	if got := tabs[len(tabs)-1].ID; got != first {
		t.Fatalf("last tab = %d, want %d", got, first)
	}
	// A user tab to the front, before every built-in.
	tabs, err = s.MoveProjectTab(ctx, "t", MoveProjectTab{ProjectID: project, DashboardID: own[0], After: 0})
	if err != nil {
		t.Fatal(err)
	}
	if tabs[0].ID != own[0] {
		t.Fatalf("first tab = %d, want %d", tabs[0].ID, own[0])
	}
}

func TestAddBuiltinBackWithAfter(t *testing.T) {
	s, project, builtins, own := tabsFixture(t)
	b := builtins[1]
	if _, err := s.RemoveProjectTab(ctx, "t", project, b); err != nil {
		t.Fatal(err)
	}
	after := own[0]
	tabs, err := s.AddProjectTab(ctx, "t", AddProjectTab{ProjectID: project, DashboardID: b, After: &after})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(tabs, func(x ProjectTab) bool { return x.ID == own[0] })
	if tabs[i+1].ID != b {
		t.Fatalf("tab after %d = %d, want %d", own[0], tabs[i+1].ID, b)
	}
}

func TestAddBuiltinBackGoesLast(t *testing.T) {
	s, project, builtins, _ := tabsFixture(t)
	b := builtins[0]
	_, _ = s.RemoveProjectTab(ctx, "t", project, b)
	tabs, err := s.AddProjectTab(ctx, "t", AddProjectTab{ProjectID: project, DashboardID: b})
	if err != nil {
		t.Fatal(err)
	}
	if tabs[len(tabs)-1].ID != b {
		t.Fatalf("last = %d, want %d", tabs[len(tabs)-1].ID, b)
	}
}
```

If the file has no `tabsFixture`, write it from `TestProjectTabsOrder`'s setup: it returns the service, a project id, the built-in tab ids in shown order and two user dashboards added as tabs.

Update `TestRemoveAndReAddBuiltin`: a re-added built-in now goes last, not back to its old place.

- [ ] **Step 2: Run them to see them fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/reporting/ -run 'ProjectTab|BuiltinTab|BuiltinBack' -count=1`
Expected: FAIL (refusals "built-in tabs keep the release's order", "a built-in goes back to its own place").

- [ ] **Step 3: Implement**

In `internal/reporting/project_tabs.go`:
- Package comment: "Built-in tabs keep the release's order…" becomes "Every tab, built-in or your own, is ordered per project by its row (spec 2026-10-08 D1)."
- `AddProjectTab`/`MoveProjectTab` field comments: `After` applies to all tabs.
- `shownTabs` sorts every row by `(SortKey, DashboardID)` only, and returns all rows (rename `own` → `rows`), archived user rows included at their place:

```go
	slices.SortStableFunc(rows, func(a, b store.ProjectTabRow) int {
		return cmp.Or(cmp.Compare(a.SortKey, b.SortKey), cmp.Compare(a.DashboardID, b.DashboardID))
	})
	tabs := []ProjectTab{}
	for _, r := range rows {
		d := ds[r.DashboardID]
		if d.ArchivedAt == "" {
			tabs = append(tabs, ProjectTab{ID: d.ID, Title: d.Title, Owner: d.Owner, GroupID: d.GroupID})
		}
	}
	return tabs, rows, ds, nil
```

- `AddProjectTab`: drop the built-in branch; every dashboard gets `key, err = s.tabKey(ctx, actor, in.ProjectID, rows, ds, in.After)`.
- `MoveProjectTab`: drop the `OwnerSystem` refusal; find the row among all rows.
- Rename `ownTabKey` → `tabKey`, doc "among rows, the project's tab rows in shown order without the tab being placed"; its refusal text becomes `"after %d is not one of project %d's tabs"`.
- `MoveProjectTab` doc: "places one of a project's tabs after another (0: first)".

- [ ] **Step 4: Run reporting tests**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/reporting/ -count=1`
Expected: PASS.

- [ ] **Step 5: Tool descriptions and docs**

- `internal/api/ops_reporting.go`: `add_project_tab` — any dashboard, built-in or yours, goes last, or right after `after` (0 first); `move_project_tab` — any tab of the project, built-in or yours, after `after` (0 first). Remove "built-ins keep the release's order" wording.
- `docs/reporting.md` (and `docs/twillingate.md` wherever project tabs are described): one order per project, any tab movable, a removed built-in added back and a built-in a release adds go last.
- Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/api/ -run 'Docs|Sync' -count=1` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/reporting internal/api/ops_reporting.go docs/reporting.md docs/twillingate.md
git commit -m "feat(reporting): reorder built-in project tabs like your own"
```

---

### Task 3: API — forms seen, project activity, list_forms fields

**Files:**
- Modify: `internal/manage/forms.go` (new `Ops.MarkFormSeen`)
- Modify: `internal/api/ops_forms.go` (`formOut`, `toFormOut`, `list_forms` description, `markFormSeen` + route)
- Create: `internal/api/ops_activity.go` (`GET /api/project-activity`)
- Modify: `internal/api/ops_read.go` or wherever `register` lists registrations, to register activity
- Test: `internal/api/ops_forms_test.go` (or the existing forms API test file), `internal/api/ops_activity_test.go`
- Modify: `docs/twillingate.md`; `docs/superpowers/specs/2026-10-08-project-landing-design.md` (D6 correction)

**Interfaces:**
- Consumes: `store.Form.SeenAt`, `store.Form.NewSubmissions`, `MarkFormSeen` (Task 1).
- Produces (REST, used by Task 5):
  - `list_forms` / `GET /api/projects/{project_id}/forms`: each form gains `"seen_at"` (RFC 3339, omitted when never seen) and `"new_submissions"` (int).
  - `POST /api/projects/{project_id}/forms/{name}/seen` body `{"until": "2026-10-08T10:00:00Z"}` → `{"status": "seen"}`; 404 unknown form; 400 a bad time. REST only (`restOnly`), not audited.
  - `GET /api/project-activity` → `{"projects": [{"project_id": 1, "last_event_day": "2026-10-08" | null, "new_submissions": 3}]}`, every live (not archived) project in list order. REST only.

- [ ] **Step 1: Write the failing API tests**

Read the existing forms API tests (`grep -ln "list_forms\|listForms" internal/api/*_test.go`) and follow their server setup and request helpers. Tests to add:

```go
func TestListFormsSeenFields(t *testing.T) {
	// A form with two submissions, never seen: new_submissions 2, no seen_at.
	// POST …/forms/contact/seen {"until": <first received_at>}: 200 {"status":"seen"}.
	// list_forms again: new_submissions 1, seen_at == until.
}

func TestMarkFormSeenRefusals(t *testing.T) {
	// unknown form → 404; {"until":"yesterday"} → 400; missing until → 400.
}

func TestProjectActivity(t *testing.T) {
	// Project 1 with a raw view on day D and a form with 2 unseen submissions;
	// project 2 with nothing; an archived project 3.
	// GET /api/project-activity → [{1, "D", 2}, {2, null, 0}], no project 3.
}

func TestProjectActivityNotMCP(t *testing.T) {
	// tools/list has no "project_activity" and no "mark_form_seen".
}
```

Write each body fully with the file's helpers (seeding raw rows: copy how `usage` tests insert a raw view; submissions: copy the forms tests).

- [ ] **Step 2: Run them to see them fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/api/ -run 'SeenFields|MarkFormSeen|ProjectActivity' -count=1`
Expected: FAIL (404 route / missing fields).

- [ ] **Step 3: Implement manage op**

`internal/manage/forms.go`:

```go
// MarkFormSeen moves how far the console has read a form forward to until
// (spec 2026-10-08 D5). Console state, not a change to the project: no
// actor, no audit.
func (o *Ops) MarkFormSeen(ctx context.Context, projectID int64, name string, until time.Time) error {
	if err := o.requireProject(ctx, projectID); err != nil {
		return err
	}
	return o.St.MarkFormSeen(ctx, projectID, name, until)
}
```

- [ ] **Step 4: Implement the forms route and fields**

`internal/api/ops_forms.go`:

```go
type formOut struct {
	…
	Submissions     int    `json:"submissions"`
	NewSubmissions  int    `json:"new_submissions" jsonschema:"submissions received after seen_at (all of them when never seen)"`
	SeenAt          string `json:"seen_at,omitempty" jsonschema:"how far the console has read this form's submissions; absent when never"`
	…
}

type markSeenIn struct {
	ProjectID int64  `json:"project_id"`
	Name      string `json:"name"`
	Until     string `json:"until"`
}

func (h *host) markFormSeen(ctx context.Context, in markSeenIn) (okOut, error) {
	until, err := time.Parse(time.RFC3339, in.Until)
	if err != nil {
		return okOut{}, invalidf("until %q is not an RFC 3339 time such as 2026-10-08T10:00:00Z", in.Until)
	}
	if err := h.ops.MarkFormSeen(ctx, in.ProjectID, in.Name, until); err != nil {
		return okOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return okOut{Status: "seen"}, nil
}
```

`toFormOut` fills `NewSubmissions` and `SeenAt` (format as the other times are). In `registerForms`, after `restore_form`:

```go
	restOnly(r, spec{Name: "mark_form_seen", Method: "POST", Path: f + "/seen",
		Description: "The console has read this form's submissions up to until (RFC 3339; the form's last_submitted_at as the page loaded it). seen_at only moves forward. Console state: not audited, not an MCP tool."},
		h.markFormSeen)
```

Append to `list_forms`' description: `, new_submissions (received after seen_at), seen_at (how far the console has read; reading over MCP never moves it)`.

- [ ] **Step 5: Implement project activity**

`internal/api/ops_activity.go`:

```go
package api

import (
	"context"
	"strconv"
)

// Project activity (spec 2026-10-08 D6, D7): per live project, the newest
// day with data and the form submissions the console hasn't read. Kept off
// list_projects, which reads only the registry snapshot because the web
// app waits on it before loading any widget.

type projectActivityOut struct {
	ProjectID      int64   `json:"project_id"`
	LastEventDay   *string `json:"last_event_day"`
	NewSubmissions int     `json:"new_submissions"`
}

type projectActivityListOut struct {
	Projects []projectActivityOut `json:"projects"`
}

func (h *host) projectActivity(ctx context.Context, _ struct{}) (projectActivityListOut, error) {
	out := projectActivityListOut{Projects: []projectActivityOut{}}
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		if p.Archived {
			continue
		}
		a := projectActivityOut{ProjectID: p.ID}
		// Each MAX(day) is one index seek on (…, project_id, day).
		res, err := h.run(ctx, `SELECT MAX(d) FROM (
			SELECT MAX(day) AS d FROM raw_views WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM raw_product WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM raw_measures WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM agg_views_daily WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM agg_product_totals WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM agg_measures_daily WHERE project_id = ?1)`, p.ID)
		if err != nil {
			return out, err
		}
		if d := res.Rows[0][0]; d != "" {
			a.LastEventDay = &d
		}
		// Submissions are read through h.subs, the handle that may.
		sub, err := h.runSubs(ctx, `SELECT COUNT(*) FROM submissions s JOIN forms f
			ON f.project_id = s.project_id AND f.name = s.form
			WHERE s.project_id = ?1 AND f.archived_at IS NULL
			  AND s.received_at > COALESCE(f.seen_at, '')`, p.ID)
		if err != nil {
			return out, err
		}
		a.NewSubmissions, _ = strconv.Atoi(sub.Rows[0][0])
		out.Projects = append(out.Projects, a)
	}
	return out, nil
}
```

`h.run` exists (`ops_usage.go`); check whether a helper runs on `h.subs` (`grep -n "h.subs" internal/api/*.go`). If none, add `runSubs` next to `run` with the same body on `h.subs`. Register in `register` (ops_read.go), after `list_projects`:

```go
	restOnly(r, spec{Name: "project_activity", Method: "GET", Path: "/api/project-activity",
		Description: "Per live project: last_event_day (the newest day with views, product events or measures, raw or rolled up; null with none) and new_submissions (submissions to live forms the console hasn't read). For the console's badges; not an MCP tool."},
		h.projectActivity)
```

- [ ] **Step 6: Run the API tests**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/api/ -count=1`
Expected: PASS, `docs_sync_test.go` included once the docs are updated (next step) — it names any route the docs miss.

- [ ] **Step 7: Docs and spec correction**

- `docs/twillingate.md`: `list_forms` gains `new_submissions` and `seen_at`; REST-only routes `POST /api/projects/{project_id}/forms/{name}/seen` and `GET /api/project-activity` in the table where REST-only routes are listed (find where the dashboard `view` route is documented and follow it).
- Spec `docs/superpowers/specs/2026-10-08-project-landing-design.md`: rewrite D6 to the corrected version under "Spec corrections made by this plan" above; D5's `until` is the form's `last_submitted_at`; Documentation section names the new route instead of `list_projects` fields.

- [ ] **Step 8: Commit**

```bash
git add internal/manage internal/api docs/twillingate.md docs/superpowers/specs/2026-10-08-project-landing-design.md
git commit -m "feat(api): count unread form submissions and report each project's newest day"
```

---

### Task 4: Web — Settings route, landing rule, last tab, any tab movable

**Files:**
- Create: `web/src/lib/last-tab.ts`, `web/src/lib/last-tab.test.ts`
- Modify: `web/src/lib/project-tabs.ts` (+ its test file if present: `grep -l project-tabs web/src/lib/*.test.ts`)
- Modify: `web/src/App.tsx` (routes)
- Modify: `web/src/pages/Project.tsx` (`ProjectIndex`, `SETTINGS_ID`, setup redirect)
- Modify: `web/src/components/SidebarProjects.tsx`, `web/src/components/projects/ProjectCard.tsx`
- Modify: `web/src/components/projects/ProjectDashboardTab.tsx` (remember the tab)
- Modify: `web/src/components/projects/ProjectTabMenu.tsx`, `web/src/components/projects/ProjectTabBar.tsx` (`tabAfter`)
- Modify: every test referencing `SETUP_ID`, `userAfter`, `/setup` (`grep -rln "SETUP_ID\|userAfter\|/setup" web/src web/e2e`)

**Interfaces:**
- Consumes: server tab order with built-ins movable (Task 2).
- Produces (used by Task 5):
  - `SETTINGS_ID = 0`, `FORMS_ID = -1` in `lib/project-tabs.ts`; `tabPath(projectId, SETTINGS_ID)` → `/projects/:id/settings`.
  - `landingPath(projectId: number, tabs: ProjectTab[], remembered: number | null, search: string): string`.
  - `tabAfter(tabs: ProjectTab[], id: number, to: number): number` — the `after` that moves tab `id` to index `to` among all tabs.
  - `readLastTab(projectId: number): number | null`, `writeLastTab(projectId: number, dashboardId: number): void` in `lib/last-tab.ts`.

- [ ] **Step 1: Write the failing unit tests**

`web/src/lib/last-tab.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LAST_TAB_KEY, readLastTab, writeLastTab } from './last-tab'

afterEach(() => {
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('last tab', () => {
  it('remembers one dashboard per project', () => {
    writeLastTab(1, 10)
    writeLastTab(2, 20)
    writeLastTab(1, 11)
    expect(readLastTab(1)).toBe(11)
    expect(readLastTab(2)).toBe(20)
    expect(readLastTab(3)).toBeNull()
  })

  it('ignores junk', () => {
    for (const junk of ['"x"', '[1]', '{"1":"10"}', 'not json', 'null']) {
      localStorage.setItem(LAST_TAB_KEY, junk)
      expect(readLastTab(1)).toBeNull()
    }
  })

  it('survives blocked storage', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    expect(() => writeLastTab(1, 10)).not.toThrow()
    expect(readLastTab(1)).toBeNull()
  })
})
```

In the `project-tabs` test file (create `web/src/lib/project-tabs.test.ts` if none):

```ts
import { describe, expect, it } from 'vitest'
import type { ProjectTab } from './api'
import { landingPath, tabAfter, tabPath, SETTINGS_ID, FORMS_ID } from './project-tabs'

const tab = (id: number, owner: 'system' | 'user' = 'system'): ProjectTab => ({ dashboard_id: id, title: `T${id}`, owner, group_id: id })

describe('tabPath', () => {
  it('maps the fixed pages', () => {
    expect(tabPath(3, SETTINGS_ID)).toBe('/projects/3/settings')
    expect(tabPath(3, FORMS_ID, 'range=7d')).toBe('/projects/3/forms?range=7d')
    expect(tabPath(3, 12)).toBe('/projects/3/dashboards/12')
  })
})

describe('landingPath', () => {
  const tabs = [tab(1), tab(2), tab(9, 'user')]
  it('opens the remembered tab while it is a tab', () => {
    expect(landingPath(3, tabs, 9, '')).toBe('/projects/3/dashboards/9')
  })
  it('falls back to the first tab when the remembered one is gone', () => {
    expect(landingPath(3, tabs, 42, 'range=30d')).toBe('/projects/3/dashboards/1?range=30d')
  })
  it('opens the first tab with nothing remembered', () => {
    expect(landingPath(3, tabs, null, '')).toBe('/projects/3/dashboards/1')
  })
  it('opens Settings with no tabs', () => {
    expect(landingPath(3, [], 9, '')).toBe('/projects/3/settings')
  })
})

describe('tabAfter', () => {
  const tabs = [tab(1), tab(2), tab(9, 'user'), tab(8, 'user')]
  it('is 0 for first', () => expect(tabAfter(tabs, 8, 0)).toBe(0))
  it('is the tab before the place, the moving one taken out', () => {
    expect(tabAfter(tabs, 1, 3)).toBe(8)
    expect(tabAfter(tabs, 8, 1)).toBe(1)
  })
  it('clamps past the end', () => expect(tabAfter(tabs, 1, 99)).toBe(8))
})
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd web && npx vitest run src/lib/last-tab.test.ts src/lib/project-tabs.test.ts --testTimeout=30000`
Expected: FAIL (modules/exports missing).

- [ ] **Step 3: Implement the libs**

`web/src/lib/last-tab.ts`:

```ts
/** Where each project's last dashboard tab is kept on this device (project landing D3). */
export const LAST_TAB_KEY = 'twillingate.project.last_tab'

function readAll(): Record<string, number> {
  try {
    const raw = localStorage.getItem(LAST_TAB_KEY)
    const v: unknown = raw === null ? null : JSON.parse(raw)
    if (v === null || typeof v !== 'object' || Array.isArray(v)) return {}
    const out: Record<string, number> = {}
    for (const [k, id] of Object.entries(v)) if (Number.isInteger(id) && (id as number) > 0) out[k] = id as number
    return out
  } catch {
    return {}
  }
}

/** The dashboard last opened on project `projectId` here, or null. */
export function readLastTab(projectId: number): number | null {
  return readAll()[String(projectId)] ?? null
}

/** Remembers `dashboardId` as project `projectId`'s last tab; storage full or blocked is ignored. */
export function writeLastTab(projectId: number, dashboardId: number): void {
  const all = readAll()
  if (all[String(projectId)] === dashboardId) return
  all[String(projectId)] = dashboardId
  try {
    localStorage.setItem(LAST_TAB_KEY, JSON.stringify(all))
  } catch {
    // The page still works; the next visit opens the first tab.
  }
}
```

`web/src/lib/project-tabs.ts`: rename `SETUP_ID` → `SETTINGS_ID` (doc: "Settings, behind the gear; no dashboard has id 0"), `tabPath` maps it to `/settings`; replace `userAfter` with:

```ts
/**
 * The `after` that moves tab `id` to index `to` among all of a project's
 * tabs (one order, built-ins included: project landing D1): 0 for first,
 * else the tab before that place once `id` is taken out.
 */
export function tabAfter(tabs: ProjectTab[], id: number, to: number): number {
  const rest = tabs.filter((t) => t.dashboard_id !== id)
  return to <= 0 ? 0 : (rest[Math.min(to, rest.length) - 1]?.dashboard_id ?? 0)
}

/** Where opening project `projectId` lands (D3): the remembered tab while it is one, else the first, else Settings. */
export function landingPath(projectId: number, tabs: ProjectTab[], remembered: number | null, search: string): string {
  const id = tabs.some((t) => t.dashboard_id === remembered) ? remembered! : (tabs[0]?.dashboard_id ?? SETTINGS_ID)
  return tabPath(projectId, id, search)
}
```

- [ ] **Step 4: Run the lib tests**

Run: `cd web && npx vitest run src/lib --testTimeout=30000` → PASS.

- [ ] **Step 5: Wire routes and links**

- `App.tsx`: the `/projects/:id/setup` route becomes `/projects/:id/settings` (same element); add `<Route path="/projects/:id/setup" element={<SetupRedirect />} />` exported from `pages/Project.tsx`:

```tsx
/** `/projects/:id/setup`, the old address of Settings: kept for links and bookmarks. */
export function SetupRedirect() {
  const { search } = useLocation()
  return <Navigate to={`../settings${search}`} relative="path" replace />
}
```

- `ProjectIndex` decides once the tabs are known:

```tsx
/** `/projects/:id`: the last tab used here, else the first, else Settings (project landing D3). */
export function ProjectIndex() {
  const { search } = useLocation()
  const id = Number(useParams().id)
  const valid = Number.isInteger(id) && id > 0
  const { data: dash, error: dashError } = useQuery(dashboardsQuery)
  const dev = dash?.dev === true
  const tabsQ = useQuery({ ...projectTabsQuery(id), enabled: valid && dash !== undefined && !dev })
  const params = new URLSearchParams(search)
  const range = rangeParams(params).toString()
  if (!valid || dev || dashError || tabsQ.error) return <Navigate to={`settings${search}`} replace />
  if (!tabsQ.data) return null
  return <Navigate to={landingPath(id, tabsQ.data.tabs, readLastTab(id), range)} replace />
}
```

(`Project` with an unknown or invalid id already shows "No project at this address" on `/settings`.) `ProjectIndex` must sit inside `OnlineOnly` in `App.tsx` now that it queries.

- `Project.tsx`: `SETUP_ID` → `SETTINGS_ID` everywhere; doc comment routes `/settings`.
- `SidebarProjects.tsx`: link `to={`/projects/${p.project_id}${range ? `?${range}` : ''}`}`; drop the `tabPath`/`SETUP_ID` import if unused.
- `ProjectCard.tsx`: already links to `/projects/:id` — unchanged.
- `ProjectDashboardTab.tsx` `TabView`: remember the tab once it renders:

```tsx
  useEffect(() => writeLastTab(projectId, tab.dashboard_id), [projectId, tab.dashboard_id])
```

- `ProjectTabMenu.tsx`: `userAfter` → `tabAfter`; Move left/right over all `tabs` (`const i = tabs.findIndex(…)`, bounds `tabs.length - 1`); after a removal land on the tab before, else the one after, else Settings:

```tsx
  const i = tabs.findIndex((t) => t.dashboard_id === id)
  const next = tabs[i - 1] ?? tabs[i + 1]
  const remove = async () => {
    if (await actions.remove(projectId, tab)) navigate(tabPath(projectId, next?.dashboard_id ?? SETTINGS_ID, search))
  }
  const moveTo = (to: number) => void actions.move(projectId, id, tabAfter(tabs, id, to))
```

and the phone block shows for every tab (`mobile && (…)`). Update the doc comment ("Move left/right for any tab").
- `use-project-tab-actions.ts`: `move` doc "Reorders a tab after another (0 first)".
- `ProjectTabBar.tsx`: only `userAfter` → `tabAfter` in this task (Task 5 rewrites the bar).

- [ ] **Step 6: Update existing tests**

Run `grep -rn "SETUP_ID\|userAfter\|/setup\|'Setup'" web/src web/e2e` and update each: unit tests expect `/settings`; ProjectIndex tests expect landing on the first tab. Add to `Project.test.tsx` (follow its render/mock helpers):

```tsx
it('opens the remembered tab', async () => {
  writeLastTab(1, <second tab id in the fixture>)
  renderAt('/projects/1')
  expect(await screen.findByRole(/* the second tab's dashboard title */)).toBeInTheDocument()
})
it('redirects /setup to /settings keeping the range', async () => {
  renderAt('/projects/1/setup?range=30d')
  await waitFor(() => expect(currentPath()).toBe('/projects/1/settings?range=30d'))
})
```

- [ ] **Step 7: Typecheck and test**

Run: `cd web && npx tsc -b && npx vitest run --testTimeout=30000`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add web/src
git commit -m "feat(web): open a project on its last tab and move any tab"
```

---

### Task 5: Web — gear and inbox buttons, new-submission counts, no-data notice

**Files:**
- Modify: `web/src/lib/api.ts` (types + endpoints), `web/src/lib/queries.ts` (`projectActivityQuery`)
- Modify: `web/src/components/projects/ProjectTabBar.tsx` (+ create `ProjectTabBar.test.tsx` if none)
- Modify: `web/src/components/ReportTabs.tsx` (select placeholder when no tab is current)
- Modify: `web/src/pages/Project.tsx` (pass counts; no-data notice)
- Modify: `web/src/components/forms/FormsTab.tsx` ("N new" chip), `web/src/components/forms/FormPage.tsx` (mark seen)
- Modify: `web/src/hooks/use-form-actions.ts` or create `web/src/hooks/use-mark-seen.ts`

**Interfaces:**
- Consumes: REST from Task 3; `SETTINGS_ID`, `FORMS_ID`, `tabAfter`, `tabPath` from Task 4.
- Produces: `endpoints.projectActivity(): Promise<{ projects: ProjectActivity[] }>`, `endpoints.markFormSeen(projectId, name, until)`; `projectActivityQuery` with key `['project-activity']`.

- [ ] **Step 1: API types and queries**

`lib/api.ts`:

```ts
/** project_activity's row (project landing D6): the newest day with data and the unread submissions. */
export interface ProjectActivity {
  project_id: number
  last_event_day: string | null
  new_submissions: number
}
```

`Form` gains `new_submissions: number` and `seen_at?: string`. Endpoints:

```ts
  projectActivity: () => api<{ projects: ProjectActivity[] }>('/api/project-activity'),
  markFormSeen: (projectId: number, name: string, until: string) =>
    api<{ status: string }>(`${formPath(projectId, name)}/seen`, json('POST', { until })),
```

`lib/queries.ts`:

```ts
export const projectActivityQuery = {
  queryKey: ['project-activity'],
  queryFn: () => endpoints.projectActivity(),
  staleTime: 60_000,
  refetchInterval: 60_000,
}
```

Update test fixtures/mocks that build `Form` objects (`grep -rln "last_submitted_at" web/src`) with `new_submissions: 0`.

- [ ] **Step 2: Write the failing tab bar tests**

`web/src/components/projects/ProjectTabBar.test.tsx` (follow an existing component test for router + query client wrappers):

```tsx
describe('ProjectTabBar', () => {
  it('shows every dashboard as a tab and Settings and Forms as buttons', () => {
    renderBar({ currentId: 1, newSubmissions: 0 })
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Views', 'Product', 'Mine'])
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/projects/7/settings')
    expect(screen.getByRole('link', { name: 'Forms' })).toHaveAttribute('href', '/projects/7/forms')
  })
  it('counts new submissions on the Forms button', () => {
    renderBar({ currentId: 1, newSubmissions: 3 })
    expect(screen.getByRole('link', { name: 'Forms, 3 new' })).toHaveTextContent('3')
  })
  it('caps the badge at 99+', () => {
    renderBar({ currentId: 1, newSubmissions: 140 })
    expect(screen.getByRole('link', { name: 'Forms, 140 new' })).toHaveTextContent('99+')
  })
  it('marks the gear current on Settings and leaves no tab selected', () => {
    renderBar({ currentId: SETTINGS_ID, newSubmissions: 0 })
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('aria-current', 'page')
    expect(screen.queryByRole('tab', { selected: true })).toBeNull()
  })
  it('shows the phone select placeholder when no tab is current', () => {
    renderBar({ currentId: FORMS_ID, newSubmissions: 0 })
    expect(screen.getByRole('combobox', { name: 'Tab' })).toHaveTextContent('Tabs')
  })
})
```

`renderBar` renders `ProjectTabBar` with `projectId: 7`, tabs `[Views (system), Product (system), Mine (user)]` with ids 1, 2, 9, empty `dashboards`, a stub `actions`, inside `MemoryRouter` + `TooltipProvider`.

- [ ] **Step 3: Run them to see them fail**

Run: `cd web && npx vitest run src/components/projects/ProjectTabBar.test.tsx --testTimeout=30000`
Expected: FAIL.

- [ ] **Step 4: Implement the tab bar**

`ProjectTabBar.tsx` — new prop `newSubmissions: number`; no fixed tabs; every tab sortable; Forms and Settings links after the scrolling row:

```tsx
/**
 * A project page's tab row (project landing D1, D4, D5): its dashboards in
 * the project's one order, any of them dragged to a new place, "+" after
 * them; then, outside the scrolling tabs, Forms (with the count of new
 * submissions) and Settings. Switching keeps the range in the URL.
 */
export default function ProjectTabBar({ projectId, currentId, tabs, dashboards, actions, newSubmissions, readOnly = false }: Props) {
  …
  const search = rangeParams(url).toString()
  const badge = newSubmissions > 99 ? '99+' : String(newSubmissions)
  const formsLabel = newSubmissions > 0 ? `Forms, ${newSubmissions} new` : 'Forms'
  return (
    <div className="flex h-10 min-w-0 items-center gap-1 border-b border-border/70">
      <ReportTabs
        tabs={tabs}
        currentId={currentId}
        onSelect={open}
        sortable={!readOnly}
        onMove={readOnly ? undefined : (id, to) => actions.move(projectId, id, tabAfter(tabs, id, to))}
        trailing={!readOnly && (/* the existing "+" button */)}
      />
      <div className="ml-auto flex shrink-0 items-center gap-0.5">
        {!readOnly && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button asChild variant="ghost" size="icon" className="relative size-8" aria-current={currentId === FORMS_ID ? 'page' : undefined}>
                <Link to={tabPath(projectId, FORMS_ID, search)} aria-label={formsLabel}>
                  <InboxIcon />
                  {newSubmissions > 0 && (
                    <span aria-hidden className="absolute -top-0.5 -right-0.5 min-w-4 rounded-full bg-primary px-1 text-[10px] leading-4 font-medium text-primary-foreground">
                      {badge}
                    </span>
                  )}
                </Link>
              </Button>
            </TooltipTrigger>
            <TooltipContent>{formsLabel}</TooltipContent>
          </Tooltip>
        )}
        <Tooltip>
          <TooltipTrigger asChild>
            <Button asChild variant="ghost" size="icon" className="size-8" aria-current={currentId === SETTINGS_ID ? 'page' : undefined}>
              <Link to={tabPath(projectId, SETTINGS_ID, search)} aria-label="Settings">
                <SettingsIcon />
              </Link>
            </Button>
          </TooltipTrigger>
          <TooltipContent>Settings</TooltipContent>
        </Tooltip>
      </div>
      <AddTabDialog … />
    </div>
  )
}
```

Style the current button like an active control (e.g. `aria-[current=page]:bg-accent aria-[current=page]:text-foreground`). Keep the badge inside the 32px button so nothing overflows at 360px.

`ReportTabs.tsx`: remove `fixedIds` (no caller left) and its doc; when no tab has `currentId`, the `Tabs` value is `''` (no trigger selected) and the phone `Select` shows `<SelectValue placeholder="Tabs" />` with `value=""`. Check `DashboardMenu`/dashboard page callers still type-check.

- [ ] **Step 5: Page wiring, no-data notice**

`Project.tsx`:

```tsx
  const activity = useQuery({ ...projectActivityQuery, enabled: valid && dash !== undefined && !dev })
  const mine = activity.data?.projects.find((p) => p.project_id === id)
  …
  <ProjectTabBar … newSubmissions={mine?.new_submissions ?? 0} />
  …
  {mine && mine.last_event_day === null && dashId !== SETTINGS_ID && (
    <div role="status" className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-dashed p-3 text-sm">
      <span>No events received yet</span>
      <Link to={tabPath(id, SETTINGS_ID, rangeParams(url).toString())} className="font-medium underline-offset-2 hover:underline">
        Set up this project
      </Link>
    </div>
  )}
```

Place the notice between the tab bar and the tab content.

- [ ] **Step 6: Forms chip and mark seen**

`FormsTab.tsx`, next to the submissions count of each form row:

```tsx
{form.new_submissions > 0 && <Badge variant="secondary">{form.new_submissions.toLocaleString('en-US')} new</Badge>}
```

`FormPage.tsx`: once `form` is known, mark it seen up to its `last_submitted_at`, once per value:

```tsx
  const markSeen = useMarkFormSeen()
  const until = form?.last_submitted_at
  const unseen = form !== undefined && form.new_submissions > 0
  useEffect(() => {
    if (until && unseen) markSeen(id, name, until)
  }, [id, name, until, unseen, markSeen])
```

`web/src/hooks/use-mark-seen.ts`:

```ts
import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { endpoints } from '@/lib/api'

/**
 * Marks a form read up to `until` (project landing D5), then refreshes the
 * forms list and the activity counts. Console state: a failure is silent;
 * the badge stays until the next visit marks it.
 */
export function useMarkFormSeen() {
  const queryClient = useQueryClient()
  return useCallback(
    (projectId: number, name: string, until: string) => {
      endpoints
        .markFormSeen(projectId, name, until)
        .then(() =>
          Promise.all([
            queryClient.invalidateQueries({ queryKey: ['forms', projectId] }),
            queryClient.invalidateQueries({ queryKey: ['project-activity'] }),
          ])
        )
        .catch(() => {})
    },
    [queryClient]
  )
}
```

Check `formsQuery`'s real key in `lib/queries.ts` and use it. Add a FormPage test: rendering a form with `new_submissions: 2, last_submitted_at: T` posts `{ until: T }` once; with `new_submissions: 0` posts nothing.

- [ ] **Step 7: Typecheck and test**

Run: `cd web && npx tsc -b && npx vitest run --testTimeout=30000`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add web/src
git commit -m "feat(web): put settings and forms behind icons with a count of new submissions"
```

---

### Task 6: End-to-end, docs check, full verification

**Files:**
- Modify: `web/e2e/project-tabs.spec.ts`, `web/e2e/forms.spec.ts`, `web/e2e/projects.spec.ts`, `web/e2e/phone.spec.ts`, `web/e2e/cursor.spec.ts` (only if it lists pages), any spec navigating to `/setup` or clicking a "Setup" tab
- Modify: `docs/twillingate.md` / `docs/reporting.md` wherever the console's "Setup" tab is named (now "Settings", behind the gear)

- [ ] **Step 1: Find what the change broke**

Run: `grep -rn "setup\|Setup" web/e2e docs/*.md | grep -iv "setup this project"` and update each to Settings (`getByRole('link', { name: 'Settings' })`, `/settings`).

- [ ] **Step 2: New e2e cases**

In `project-tabs.spec.ts`:

```ts
test('a project opens on the last tab used there', async ({ page }) => {
  await page.goto('/app/projects/1')
  await expect(page).toHaveURL(/\/projects\/1\/dashboards\/\d+/)
  const tabs = page.getByRole('tab')
  await tabs.nth(1).click()
  const second = page.url()
  await page.getByRole('link', { name: 'Settings' }).click()
  await page.goto('/app/projects/1')
  await expect(page).toHaveURL(second)
})

test('a built-in tab drags to the end', async ({ page }) => {
  await page.goto('/app/projects/1')
  const first = page.getByRole('tab').first()
  const title = await first.textContent()
  await first.dragTo(page.getByRole('tab').last())
  await expect(page.getByRole('tab').last()).toHaveText(title!)
  await page.reload()
  await expect(page.getByRole('tab').last()).toHaveText(title!)
})
```

Use the drag approach the existing user-tab drag test uses (copy it if `dragTo` isn't enough for dnd-kit).

In `forms.spec.ts`: after a submission is posted (copy its existing setup), the project page's Forms button reads `Forms, 1 new`; opening the form clears it (`Forms` with no count) after navigating back.

- [ ] **Step 3: Run e2e**

Run: `cd web && npm run e2e`
Expected: PASS, `cursor.spec.ts` and `phone.spec.ts` included. If port 18080 is taken by another session's stale server, stop it or use the spec's port override.

- [ ] **Step 4: Full check**

Run: `PATH=/usr/local/go/bin:$PATH make check`
Expected: PASS (vet, coverage gate, docs sync, archtest).

- [ ] **Step 5: Commit**

```bash
git add web/e2e docs
git commit -m "test(web): cover landing on the last tab, moving built-ins and the forms count"
```
