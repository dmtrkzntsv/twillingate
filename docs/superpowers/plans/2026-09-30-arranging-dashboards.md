# Arranging Dashboards Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** System groups can be archived and replaced by a copy. The page can duplicate, archive, restore, reorder and regroup dashboards, and the dashboard gallery can restore and copy them.

**Architecture:** The server work stays in `internal/reporting` (the service), `internal/store/sqlite` (the transactions) and `internal/api` (thin tool adapters). It reuses `archived_at`, `update_dashboard {after, group_id}` and the existing routes, so nothing needs a migration or a new route. The page gets one hook for every write (`use-dashboard-actions.ts`), and the move maths lives in one pure module (`lib/arrange.ts`). Two menus (the sidebar entry and the header), drag and drop (dnd-kit), and a gallery page call that hook.

**Tech Stack:** Go 1.x and SQLite (modernc). React 19, TanStack Query, react-router, shadcn/ui (DropdownMenu, Sidebar, Sonner) and `@dnd-kit/core` + `@dnd-kit/sortable`. Vitest and Playwright for tests.

**Spec:** `docs/superpowers/specs/2026-09-30-arranging-dashboards-design.md`. Decisions are cited as D1–D19.

## Global Constraints

- Go is at `/usr/local/go/bin`, which is not on PATH: run `export PATH=/usr/local/go/bin:$PATH` first.
- Only one `make check` may run at a time, because the machine is shared.
- `make check` must pass before the branch is pushed. `cd web && npm run typecheck && npm test` must pass for every web task.
- **Refusals:**
  - Every refusal is `store.Refuse(store.ErrInvalid|ErrNotFound|ErrConflict, …)`, and tests match it with `errors.Is` and the exact message (`wantRefusal`).
  - The message for archiving or restoring a single system dashboard is exactly `dashboard %d is a system dashboard, archived and restored with its group; pass whole_group`.
- **Docs in the same commit:**
  - Any change to a tool, its input or its output updates `docs/reporting.md` in the same commit, and `internal/api/docs_sync_test.go` must stay green.
  - `serverInstructions` in `internal/api/guide_reporting.go` becomes exactly: `To integrate a site or app, call integration_guide. To build or change dashboards, call reporting_guide. To customize a system dashboard, duplicate it: the copy replaces its group in the sidebar. If widgets broke after an update, read the release notes at https://github.com/dmtrkzntsv/twillingate/releases.`
- **Commits:** Conventional Commits with scope `reporting`, `store`, `api` or `web`. Server tasks use `feat(reporting)`, web tasks `feat(web)`. Each commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Web code style:** match the existing files (2-space indent, single quotes, no semicolons, `@/` imports, `/** … */` doc comments citing decisions).
- **UI copy, verbatim:**
  - Sidebar menu, system group: "Duplicate" with the hint "Your copy replaces it in the sidebar", and "Archive".
  - Sidebar menu, user group: "Duplicate", "Archive", "Move up", "Move down".
  - Header menu, tab in a group of more than one: "Duplicate tab", "Archive tab", "Move left", "Move right", "Move to". In a group of one: "Duplicate", "Archive", "Move to".
  - Move to submenu: other user groups by title, plus "Own dashboard" (only in a group of more than one).
  - Archive toast: `Archived '<title>'` with an "Undo" action.
  - Line on an archived dashboard: "Archived: not in the sidebar" with a "Restore" button.
  - Gallery: page title "Dashboards"; sections "System" and "Archived"; buttons "Archive", "Restore", "Copy as a dashboard"; purge line `deleted on <d MMM>`.

## Review Focus

1. **Duplicating a system tab while Reports is already archived** (an earlier copy replaced it). The copy is still made, and Reports stays archived rather than erroring because "nothing live to archive". Pinned in Task 3.
2. **Archiving the tab you are viewing, where it is the group's last live tab.** The page goes to `/`, not to a 404 or the archived page. Pinned in Task 6 (unit test on `nextAfterArchive`).
3. **Moving a group up when the group above has archived members.** `after` must name a *live* dashboard (tabs spec D8 refuses an archived one), so the move maths only uses live ids. Pinned in Task 5 (`arrange.test.ts`).
4. **A drag or move that the server refuses** (a lost race, for example). The item snaps back, a toast shows the message, and the list refetches. Pinned in Task 8 (unit test: a rejected mutation leaves the order as the server has it).
5. **`purge_after_days` 0** (`RETENTION_ARCHIVED_DAYS=0` keeps archived rows forever). The gallery shows no purge date rather than "deleted on" today's date. Pinned in Task 4 (Go: the field is omitted) and Task 9 (web: no line).

---

## File map

| File | Responsibility | Tasks |
|---|---|---|
| `internal/reporting/ops_dashboard.go` | archive/restore of system groups; `DuplicateDashboard` with `archiveSource` | 1, 3 |
| `internal/reporting/reporting.go` | `Store` slice signature; `Options.ArchivedDays` | 3, 4 |
| `internal/reporting/read.go` | `Dashboards.PurgeAfterDays` | 4 |
| `internal/store/store.go`, `internal/store/sqlite/reporting.go` | inserts take `archive []int64` in the same transaction | 3 |
| `internal/store/sqlite/reporting_sync.go` | a new system dashboard joins its all-archived group archived | 2 |
| `internal/api/ops_reporting.go`, `guide_reporting.go`, `server.go` | tool inputs, descriptions, instructions, wiring `ArchivedDays` | 1, 3, 4 |
| `docs/reporting.md` | the contract | 1, 3, 4 |
| `web/src/lib/api.ts` | endpoints `duplicate`, `archive`, `restore`, `move`; `purge_after_days` | 5 |
| `web/src/lib/arrange.ts` (+ test) | pure move maths and group helpers | 5 |
| `web/src/hooks/use-dashboard-actions.ts` (+ test) | every page write: mutate, invalidate, toast, undo | 5 |
| `web/src/App.tsx` | mounts `<Toaster>`; route `/gallery/dashboards` | 5, 9 |
| `web/src/components/AppSidebar.tsx`, `SidebarGroupMenu.tsx` (+ tests) | the "…" menu on sidebar entries; drag of user groups | 6, 8 |
| `web/src/components/DashboardMenu.tsx` (+ test), `DashboardHeader.tsx`, `pages/Dashboard.tsx` | the header "…" menu; the archived line | 7, 9 |
| `web/src/components/ReportTabs.tsx` | drag of user tabs | 8 |
| `web/src/pages/gallery/DashboardsGallery.tsx` (+ test) | the dashboard gallery | 9 |
| `web/e2e/arrange.spec.ts` | end-to-end flows | 10 |

---

### Task 1: Archive and restore system groups whole

**Files:**
- Modify: `internal/reporting/ops_dashboard.go` (`setDashboardArchived`, ~line 411)
- Modify: `internal/api/ops_reporting.go` (descriptions of `archive_dashboard` and `restore_dashboard`, ~lines 231–236)
- Modify: `docs/reporting.md` (Concepts ~line 35, Rules ~line 138, Archiving ~line 644, Refusals table ~line 683)
- Test: `internal/reporting/ops_test.go` (replace `TestArchiveRestoreWholeGroupRefusesSystem`, ~line 1633)

**Interfaces:**
- Produces: `ArchiveDashboard(ctx, actor, id, wholeGroup)` and `RestoreDashboard(…)` succeed on a system dashboard when `wholeGroup` is true. Without it they refuse with `ErrInvalid` and the exact message from the Global Constraints.

- [ ] **Step 1: Write the failing test.** Replace `TestArchiveRestoreWholeGroupRefusesSystem` with:

```go
// D1: a system group is archived and restored whole; one system
// dashboard alone is refused.
func TestArchiveRestoreSystemGroup(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()
	const one = "dashboard 12 is a system dashboard, archived and restored with its group; pass whole_group"
	wantRefusal(t, svc.ArchiveDashboard(ctx, "test", 12, false), store.ErrInvalid, one)

	if err := svc.ArchiveDashboard(ctx, "test", 12, true); err != nil {
		t.Fatal(err)
	}
	for id := int64(10); id <= 14; id++ {
		d, err := svc.st.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.ArchivedAt == "" {
			t.Errorf("system dashboard %d live after archive whole_group, want archived", id)
		}
	}
	wantRefusal(t, svc.RestoreDashboard(ctx, "test", 12, false), store.ErrInvalid, one)
	if err := svc.RestoreDashboard(ctx, "test", 10, true); err != nil {
		t.Fatal(err)
	}
	for id := int64(10); id <= 14; id++ {
		if d, _ := svc.st.GetDashboard(ctx, id); d.ArchivedAt != "" {
			t.Errorf("system dashboard %d archived after restore whole_group, want live", id)
		}
	}
	// Writes other than archive/restore stay refused.
	_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: 10, Title: "X"})
	if !errors.Is(err, store.ErrInvalid) {
		t.Errorf("update on a system dashboard: err = %v, want ErrInvalid", err)
	}
}
```

Add `"errors"` to the imports if it is missing.

- [ ] **Step 2: Run the test and confirm it fails.** Run `cd internal/reporting && go test -run TestArchiveRestoreSystemGroup .`. Expected: FAIL, because the current refusal is `systemRefusal`.

- [ ] **Step 3: Implement it.** In `setDashboardArchived`, replace the `refuseSystem` call with:

```go
	if d.Owner == store.OwnerSystem && !wholeGroup {
		return store.Refuse(store.ErrInvalid,
			"dashboard %d is a system dashboard, archived and restored with its group; pass whole_group", d.ID)
	}
```

Update its doc comment: "A system dashboard is archived and restored only with its whole group (D1); every other write to one stays refused." Every other caller of `refuseSystem` stays as it is.

- [ ] **Step 4: Update the tool descriptions and the docs.**
  - `archive_dashboard`: "Hide a dashboard and its widgets. Reversible with restore_dashboard; a user dashboard is purged, with its widgets, RETENTION_ARCHIVED_DAYS (default 30) after archiving unless restored. whole_group archives every tab of its group. A system dashboard is archived only with whole_group, is never purged, and keeps its archive across releases."
  - `restore_dashboard`: "Unhide an archived dashboard, where it was in the sidebar. whole_group restores every archived tab of its group; a system dashboard is restored only with whole_group."
  - `docs/reporting.md` Concepts, system bullet: "System dashboards ship with each release, have ids 1–999, and change only when the release does. A system group is archived and restored whole, with `whole_group`, and is never purged; every other write refuses them. To customize one, call `duplicate_dashboard`: the copy is a user dashboard you can edit, and it replaces the system group in the sidebar (see `archive_source`)."
  - Rules: "System dashboards (ids 1–999) change only with a release: `archive_dashboard`/`restore_dashboard` with `whole_group` hide or show a system group, `duplicate_dashboard` makes an editable copy that replaces it, and `copy_widget` copies one system widget onto a user dashboard."
  - Archiving: replace "System dashboards and their widgets cannot be archived." with "A system group is archived and restored whole (`whole_group`) and is never purged; system widgets cannot be archived."
  - Refusals table: add the row `| dashboard 12 is a system dashboard, archived and restored with its group; pass whole_group | Pass `whole_group: true`; the whole system group is archived or restored. |`

- [ ] **Step 5: Run the tests.** Run `go test ./internal/reporting/ ./internal/api/`. Expected: PASS. If an api test asserted the old system refusal on archive, change it to the new message.

- [ ] **Step 6: Commit.**

```bash
git add internal/reporting internal/api docs/reporting.md
git commit -m "feat(reporting): archive and restore a system group whole"
```

---

### Task 2: A new system tab of an archived group arrives archived

**Files:**
- Modify: `internal/store/sqlite/reporting_sync.go` (`syncDashboards`, ~line 146)
- Test: `internal/store/sqlite/reporting_sync_test.go`

**Interfaces:**
- Consumes: the schema only.
- Produces: after `SyncReporting`, an existing system dashboard keeps its `archived_at`. A system dashboard inserted by this sync is archived when its group already had members and every one of them is archived (D3).

- [ ] **Step 1: Write the failing test.** Find the file's helpers for building a sync (`store.ReportingSync{Hash, Version, Components, Dashboards}`) and reuse them:

```go
// D3: archiving a system group survives a sync, and a dashboard the
// release adds to that group arrives archived too.
func TestSyncKeepsArchivedSystemGroup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	group := func(n int) []store.SystemDashboard {
		out := make([]store.SystemDashboard, n)
		for i := range out {
			var g int64
			if i > 0 {
				g = 10
			}
			out[i] = store.SystemDashboard{ID: int64(10 + i), Title: fmt.Sprintf("T%d", i),
				SortKey: fmt.Sprintf("a%d", i), GroupID: g, Range: "7d"}
		}
		return out
	}
	sync := func(hash string, ds []store.SystemDashboard) {
		t.Helper()
		if err := db.SyncReporting(ctx, store.ReportingSync{Hash: hash, Version: "test", Dashboards: ds}); err != nil {
			t.Fatal(err)
		}
	}
	sync("h1", group(2))
	if err := db.SetDashboardsArchived(ctx, []int64{10, 11}, true, store.AuditEntry{Actor: "t", Action: "dashboard.archive"}); err != nil {
		t.Fatal(err)
	}
	sync("h2", append(group(3), store.SystemDashboard{ID: 20, Title: "Alone", SortKey: "b0", Range: "7d"}))
	for _, id := range []int64{10, 11, 12} {
		d, err := db.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.ArchivedAt == "" {
			t.Errorf("dashboard %d live after sync, want archived (its group is archived)", id)
		}
	}
	if d, _ := db.GetDashboard(ctx, 20); d.ArchivedAt != "" {
		t.Errorf("new dashboard 20 in its own group archived, want live")
	}
}
```

If the file's test database helper has a different name, use that name. If `SyncReporting` needs components, pass the ones the neighbouring tests pass.

- [ ] **Step 2: Run the test and confirm it fails.** Run `go test ./internal/store/sqlite/ -run TestSyncKeepsArchivedSystemGroup`. Expected: FAIL on dashboard 12, which arrives live.

- [ ] **Step 3: Implement it.** In `syncDashboards`, record before the upsert loop whether each id already existed (`existed := err == nil` in the existing owner check). After the upsert of a dashboard that did not exist, run:

```go
		if !existed {
			if _, err := tx.ExecContext(ctx, `UPDATE dashboards
				SET archived_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
				WHERE id=? AND EXISTS (SELECT 1 FROM dashboards WHERE group_id=? AND id<>? AND owner=?)
				  AND NOT EXISTS (SELECT 1 FROM dashboards WHERE group_id=? AND id<>? AND owner=? AND archived_at IS NULL)`,
				dash.ID, groupID, dash.ID, store.OwnerSystem, groupID, dash.ID, store.OwnerSystem); err != nil {
				return 0, 0, fmt.Errorf("reporting sync: archive new dashboard %d of an archived group: %w", dash.ID, err)
			}
		}
```

Because dashboards are upserted in manifest order, the group's existing members are already written when a new later member arrives. Add a comment citing D3.

- [ ] **Step 4: Run the tests.** Run `go test ./internal/store/sqlite/ ./internal/reporting/`. Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/store/sqlite
git commit -m "feat(store): keep a system group archived across releases"
```

---

### Task 3: `duplicate_dashboard` replaces a system group (`archive_source`)

**Files:**
- Modify: `internal/store/store.go` (the `InsertDashboard` and `InsertDashboardGroup` signatures and docs)
- Modify: `internal/store/sqlite/reporting.go` (`InsertDashboard` ~146, `InsertDashboardGroup` ~355)
- Modify: `internal/reporting/reporting.go` (the `Store` slice)
- Modify: `internal/reporting/ops_dashboard.go` (`CreateDashboard`'s insert call, `DuplicateDashboard`, `duplicateOne`, `duplicateGroup`)
- Modify: `internal/api/ops_reporting.go` (a new `duplicateDashboardIn`, `duplicateDashboard`, and the description)
- Modify: `internal/api/guide_reporting.go` (`serverInstructions`)
- Modify: `docs/reporting.md` (the `duplicate_dashboard` row of the tools table, the duplicate route row of the HTTP API table, Workflow step 5)
- Test: `internal/reporting/ops_test.go`, `internal/api/ops_reporting_test.go`, and any test fake or caller of the two store methods (`grep -rn "InsertDashboard(" internal`)

**Interfaces:**
- Produces (store):

```go
// InsertDashboard inserts d and its widgets and archives the dashboards
// in archive (live ones only), all in one transaction.
InsertDashboard(ctx context.Context, d Dashboard, ws []Widget, archive []int64, a AuditEntry) (int64, error)
InsertDashboardGroup(ctx context.Context, ds []Dashboard, ws [][]Widget, archive []int64, a AuditEntry) ([]int64, error)
```

- Produces (service): `DuplicateDashboard(ctx, actor string, id int64, wholeGroup bool, archiveSource *bool) (DashboardDetail, error)`. A nil `archiveSource` means "true for a system source, false for a user one" (D6).
- Produces (api): `duplicate_dashboard` input `{dashboard_id, whole_group?, archive_source?}`, where `archive_source` is a `*bool`.

- [ ] **Step 1: Write the failing service tests** in `ops_test.go`:

```go
// D6: a system source with archive_source (the default) copies its
// whole group, archives it, and returns the copy of the tab named.
func TestDuplicateSystemTabReplacesGroup(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()
	got, err := svc.DuplicateDashboard(ctx, "test", 12, false, nil) // Users
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != store.OwnerUser || got.Title != "Users" {
		t.Errorf("copy = %s %q, want the user copy of Users", got.Owner, got.Title)
	}
	titles := []string{}
	for _, tab := range got.Tabs {
		titles = append(titles, tab.Title)
	}
	if want := []string{"Views (copy)", "Product", "Users", "Groups", "Retention"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("copy's tabs = %v, want %v", titles, want)
	}
	for id := int64(10); id <= 14; id++ {
		if d, _ := svc.st.GetDashboard(ctx, id); d.ArchivedAt == "" {
			t.Errorf("system dashboard %d live, want archived", id)
		}
	}
	// Review focus 1: with Reports archived, copying a tab again works,
	// and Reports stays archived.
	again, err := svc.DuplicateDashboard(ctx, "test", 11, false, nil)
	if err != nil {
		t.Fatalf("duplicate of an archived system tab: %v", err)
	}
	if len(again.Tabs) != 5 {
		t.Errorf("second copy has %d tabs, want 5", len(again.Tabs))
	}
}

// D6: archive_source false on a system tab copies only that tab, as a
// standalone user dashboard (the gallery's "Copy as a dashboard"),
// archived source accepted (D8).
func TestDuplicateSystemTabWithoutArchiving(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()
	if err := svc.ArchiveDashboard(ctx, "test", 10, true); err != nil {
		t.Fatal(err)
	}
	got, err := svc.DuplicateDashboard(ctx, "test", 12, false, ptr(false))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Users (copy)" || len(got.Tabs) != 1 || got.ArchivedAt != "" {
		t.Errorf("copy = %q tabs %d archived %q, want a live standalone 'Users (copy)'", got.Title, len(got.Tabs), got.ArchivedAt)
	}
	if d, _ := svc.st.GetDashboard(ctx, 12); d.ArchivedAt == "" {
		t.Errorf("source restored by a copy, want it to stay archived")
	}
}

// D6, D8: a user source archives nothing by default and what was copied
// with archive_source true; an archived user source is still refused.
func TestDuplicateUserArchiveSource(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "A")
	b := mustJoin(t, svc, "B", a.ID)
	if _, err := svc.DuplicateDashboard(ctx, "test", a.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := svc.st.GetDashboard(ctx, a.ID); d.ArchivedAt != "" {
		t.Errorf("user source archived by default, want live")
	}
	if _, err := svc.DuplicateDashboard(ctx, "test", a.ID, true, ptr(true)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if d, _ := svc.st.GetDashboard(ctx, id); d.ArchivedAt == "" {
			t.Errorf("dashboard %d live after whole_group archive_source, want archived", id)
		}
	}
	_, err := svc.DuplicateDashboard(ctx, "test", a.ID, false, nil)
	wantRefusal(t, err, store.ErrInvalid, fmt.Sprintf("dashboard %d is archived; restore_dashboard first", a.ID))
}
```

Update every existing `DuplicateDashboard(ctx, "test", id, wholeGroup)` call in the tests to pass `nil` as the fifth argument. Existing tests that duplicate a *system* dashboard and expect it to stay live, or expect a single-tab copy, must pass `ptr(false)`, since that was the old behaviour. Read each failure to decide which case it is.

Add a store-level test in `internal/store/sqlite/reporting_test.go` showing that `InsertDashboard` with an unknown id in `archive` returns `ErrNotFound` and inserts nothing (no new row, no audit). This is the "failed insert archives nothing" case.

- [ ] **Step 2: Run the tests and confirm they fail to compile.** Run `go test ./internal/reporting/`. Expected: a compile error on the new argument.

- [ ] **Step 3: Implement the store side.** Factor the body of `SetDashboardsArchived`'s transaction into `archiveRows(ctx, tx, ids []int64, archived bool, a store.AuditEntry) error` and call it from all three places. In `InsertDashboard` and `InsertDashboardGroup`, after the rows and widgets are inserted and before the audit, call `archiveRows(ctx, tx, archive, true, store.AuditEntry{Actor: a.Actor, Action: "dashboard.archive"})` when `len(archive) > 0`. Update `CreateDashboard`'s call to pass `nil`. Update `store.Store`, `reporting.Store` and any fakes.

- [ ] **Step 4: Implement the service side.** In `ops_dashboard.go`:

```go
func (s *Service) DuplicateDashboard(ctx context.Context, actor string, id int64, wholeGroup bool, archiveSource *bool) (DashboardDetail, error) {
	src, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return DashboardDetail{}, err
	}
	system := src.Owner == store.OwnerSystem
	if src.ArchivedAt != "" && !system {
		return DashboardDetail{}, store.Refuse(store.ErrInvalid, "dashboard %d is archived; restore_dashboard first", id)
	}
	archive := system
	if archiveSource != nil {
		archive = *archiveSource
	}
	if system && archive {
		return s.duplicateGroup(ctx, actor, src, true)
	}
	if wholeGroup {
		return s.duplicateGroup(ctx, actor, src, archive)
	}
	return s.duplicateOne(ctx, actor, src, archive)
}
```

- `duplicateOne(…, archive bool)` passes `[]int64{src.ID}` when `archive` is true (and the source is live), else `nil`.
- `duplicateGroup(…, archive bool)` copies:
  - for a system source, every member of `src.GroupID` with owner system, archived or live. The copy's first tab is still titled "… (copy)", and the archived state is ignored because the system group is a unit (D6).
  - for a user source, the live members, as today.
- When `archive` is true, `duplicateGroup` passes the ids that are currently live (`ArchivedAt == ""`) as `archive`, so that an already archived Reports stays as it is (Review Focus 1).
- `duplicateGroup` returns the copy whose index matches `src`'s position among the copied members, not always `ids[0]`.
- The duplicate audit detail appends `"; archived dashboard/<id>,…"` when anything was archived.
- Update the doc comments to cite D6–D8.

- [ ] **Step 5: Implement the API side.** In `ops_reporting.go`:

```go
// duplicateDashboardIn is duplicate_dashboard's input (spec 2026-09-30 D6).
type duplicateDashboardIn struct {
	DashboardID   int64 `json:"dashboard_id" jsonschema:"dashboard id; list_dashboards names them"`
	WholeGroup    bool  `json:"whole_group,omitempty" jsonschema:"true copies every dashboard in this dashboard's group (all its tabs)"`
	ArchiveSource *bool `json:"archive_source,omitempty" jsonschema:"archive what was copied, so the copy replaces it in the sidebar. Default true for a system dashboard (its whole group is then copied and archived, whatever whole_group says) and false for a user one"`
}
```

`duplicateDashboard` takes it and passes `in.ArchiveSource`. The new description reads: "Copy a dashboard with copies of its live widgets; the copy is a user dashboard. A system dashboard's copy replaces it: by default its whole group is copied as a new dashboard last in the sidebar, with the same tabs, and the system group is archived (restore_dashboard whole_group brings it back). archive_source false copies just what was asked and archives nothing, also from an archived system dashboard. A user dashboard's copy joins its group as the next tab; an archived user dashboard is refused (restore it first). whole_group copies the group's live dashboards as a new dashboard with the same tabs; archive_source true then archives them." Set `serverInstructions` to the exact text in the Global Constraints.

- [ ] **Step 6: Update the docs and the API test.**
  - `docs/reporting.md`: the `duplicate_dashboard` tools row gains `archive_source` and describes the behaviour in the words of the description above. The HTTP API duplicate row's input becomes `optional body {whole_group, archive_source}`. Workflow step 5: "To change a system dashboard, `duplicate_dashboard` it: the copy replaces its group in the sidebar; work on the copy."
  - `ops_reporting_test.go`: add a REST case, `POST /api/dashboards/<system id>/duplicate` with body `{"archive_source":false}`. It returns 201, and the system dashboard is still live afterwards.

- [ ] **Step 7: Run the tests.** Run `go test ./internal/...`. Expected: PASS, docs_sync included.

- [ ] **Step 8: Commit.**

```bash
git add internal docs/reporting.md
git commit -m "feat(reporting): duplicating a system dashboard replaces its group"
```

---

### Task 4: `purge_after_days` in `list_dashboards`

**Files:**
- Modify: `internal/reporting/reporting.go` (`Options.ArchivedDays int`, a `Service.archivedDays` field)
- Modify: `internal/reporting/read.go` (`Dashboards.PurgeAfterDays int \`json:"purge_after_days,omitempty"\``, set in `Dashboards()`)
- Modify: `internal/api/server.go:42` (pass `ArchivedDays: cfg.Retention.ArchivedDays`)
- Modify: the `list_dashboards` description in `internal/api/ops_reporting.go`, and its row in `docs/reporting.md`
- Test: `internal/reporting/ops_test.go` (or `read` tests)

**Interfaces:**
- Produces: `GET /api/dashboards` carries `purge_after_days` (int, omitted when 0). The web types it as `purge_after_days?: number`.

- [ ] **Step 1: Write the failing test.**

```go
// D17, review focus 5: list_dashboards says how long an archived user
// dashboard is kept; 0 (kept forever) is omitted.
func TestDashboardsPurgeAfterDays(t *testing.T) {
	svc, _ := newTestServiceOpts(t, Options{ArchivedDays: 30}, 1000)
	got, err := svc.Dashboards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.PurgeAfterDays != 30 {
		t.Errorf("PurgeAfterDays = %d, want 30", got.PurgeAfterDays)
	}
	b, _ := json.Marshal(Dashboards{Timezone: "UTC"})
	if strings.Contains(string(b), "purge_after_days") {
		t.Errorf("0 days marshals as %s, want the field omitted", b)
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails.** Run `go test ./internal/reporting/ -run TestDashboardsPurgeAfterDays`. Expected: a compile failure.

- [ ] **Step 3: Implement it** as listed under Files. The `list_dashboards` description appends: "and purge_after_days, how long an archived user dashboard is kept before it is deleted (absent: kept forever)". The docs row gets the same addition.

- [ ] **Step 4: Run the tests.** Run `go test ./internal/reporting/ ./internal/api/`. Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal docs/reporting.md
git commit -m "feat(reporting): list_dashboards says how long archived dashboards are kept"
```

---

### Task 5: The page's write layer: endpoints, move maths, actions hook, toasts

**Files:**
- Modify: `web/src/lib/api.ts`
- Create: `web/src/lib/arrange.ts`, `web/src/lib/arrange.test.ts`
- Create: `web/src/hooks/use-dashboard-actions.ts`, `web/src/hooks/use-dashboard-actions.test.tsx`
- Modify: `web/src/App.tsx` (mount `<Toaster richColors={false} position="bottom-right" />` from `@/components/ui/sonner` inside `TooltipProvider`)
- Modify: `web/src/test/fixtures.ts` if a fixture list helps the tests

**Interfaces:**
- Produces (`api.ts`):

```ts
export interface MoveBody { group_id?: number; after?: number }
// in DashboardsResponse:
purge_after_days?: number
// in endpoints:
duplicate: (id: number, body: { whole_group?: boolean; archive_source?: boolean } = {}) =>
  api<DashboardDetail>(`/api/dashboards/${id}/duplicate`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
archive: (id: number, wholeGroup = false) =>
  api<{ status: string }>(`/api/dashboards/${id}/archive`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(wholeGroup ? { whole_group: true } : {}) }),
restore: (id: number, wholeGroup = false) => /* same shape, /restore */,
move: (id: number, body: MoveBody) =>
  api<DashboardInfo>(`/api/dashboards/${id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
```

- Produces (`arrange.ts`), all pure:

```ts
/** One sidebar entry: a group's live members in order (tabs D4, D20). */
export interface Group { groupId: number; owner: 'system' | 'user'; members: DashboardInfo[] }
/** Live dashboards grouped, in list order. */
export function liveGroups(list: DashboardInfo[]): Group[]
/** The update_dashboard body that puts `group` at index `to` among the user groups (after removing it), or null if that is where it is. Names only live ids (review focus 3). */
export function moveGroupBody(groups: Group[], groupId: number, to: number): MoveBody | null
/** The body that puts tab `id` at index `to` among `tabs` (after removing it): {group_id: own, after: 0} for first, else {after: <tab before>}. Null if unchanged. */
export function moveTabBody(tabs: DashboardTab[], id: number, groupId: number, to: number): MoveBody | null
/** Where to go after archiving `id`: the next live tab, else the previous, else '/'. */
export function nextAfterArchive(tabs: DashboardTab[], id: number): string
/** The date an archived dashboard is purged, or undefined when days is 0/absent. */
export function purgeDate(archivedAt: string, days?: number): Date | undefined
```

The body of `moveGroupBody`:
- `to === 0` gives `{ after: 0 }`.
- Otherwise it gives `{ after: <first live member id of the user group at index to-1 in the list without the moved group> }`.
- It is sent on the group's first live member.

- Produces (`use-dashboard-actions.ts`):

```ts
export interface DashboardActions {
  duplicate(d: { dashboard_id: number; title: string }, opts?: { wholeGroup?: boolean; archiveSource?: boolean }): Promise<void> // navigates to the copy
  archive(d: { dashboard_id: number; title: string }, opts: { wholeGroup?: boolean; navigateTo?: string }): Promise<void> // toast with Undo
  restore(id: number, wholeGroup?: boolean): Promise<void>
  move(id: number, body: MoveBody): Promise<void>
  pending: boolean
}
export function useDashboardActions(): DashboardActions
```

Each action calls the endpoint and then `invalidateQueries({ queryKey: ['dashboards'] })` and `['dashboard']`. On an `ApiError` it calls `toast.error(err.message)` and refetches, then resolves; it never throws to the caller. `archive` shows `toast(\`Archived '${title}'\`, { action: { label: 'Undo', onClick: () => restore(id, wholeGroup) } })` and navigates to `navigateTo` when given.

- [ ] **Step 1: Write the failing `arrange.test.ts`.** Cover:
  - `liveGroups` skips archived rows and keeps list order.
  - `moveGroupBody`: to the top gives `{after: 0}`; down one names the first live member of the group now before it, skipping an archived first member (review focus 3); unchanged gives `null`.
  - `moveTabBody`: to first gives `{group_id: G, after: 0}`; to the end gives `{after: last}`; unchanged gives `null`.
  - `nextAfterArchive`: middle tab goes to the next, last tab to the previous, a lone tab to `'/'` (review focus 2).
  - `purgeDate`: 30 days after `2026-09-01T00:00:00Z` is 2026-10-01; days `0` or `undefined` gives `undefined`.

- [ ] **Step 2: Run the tests and confirm they fail.** Run `cd web && npx vitest run src/lib/arrange.test.ts`. Expected: FAIL, because the module is missing.

- [ ] **Step 3: Implement `arrange.ts`, then run the tests.** Expected: PASS.

- [ ] **Step 4: Write the failing hook test.** Mock `endpoints` with `vi.mock('@/lib/api', …)` and render the hook in a `QueryClientProvider` plus `MemoryRouter`. Cover:
  - `archive` calls `endpoints.archive(id, true)` and invalidates `['dashboards']`.
  - A rejected `move` (an `ApiError` 409 "conflict") calls `toast.error('conflict')`, resolves, and refetches (review focus 4).
  - `duplicate` navigates to `/dashboards/<copy id>`.

  Mock `sonner`'s `toast` with `vi.mock('sonner', …)`.

- [ ] **Step 5: Implement the hook and the endpoints, mount the Toaster, then run `npm run typecheck && npm test`.** Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add web/src
git commit -m "feat(web): add the dashboard actions the page writes with"
```

---

### Task 6: The sidebar entry's "…" menu

**Files:**
- Create: `web/src/components/SidebarGroupMenu.tsx`, `web/src/components/SidebarGroupMenu.test.tsx`
- Modify: `web/src/components/AppSidebar.tsx` (render the menu in every group's `SidebarMenuItem` via `SidebarMenuAction showOnHover`; compute `liveGroups`)
- Modify: `web/src/components/AppSidebar.test.tsx` if the structure assertions change

**Interfaces:**
- Consumes: `useDashboardActions`, `liveGroups` and `moveGroupBody` from Task 5.
- Produces: `<SidebarGroupMenu group={Group} userGroups={Group[]} currentId={number} />`.

Behaviour (D10):
- The trigger is a `MoreHorizontalIcon` button with `aria-label={\`${title} actions\`}`.
- **System group:**
  - "Duplicate" with the hint "Your copy replaces it in the sidebar" as a muted second line. It calls `duplicate(first, {})`, letting the server default apply.
  - "Archive" calls `archive(first, { wholeGroup: true, navigateTo: currentInGroup ? '/' : undefined })`.
- **User group:**
  - "Duplicate" calls `duplicate(first, { wholeGroup: true })`.
  - "Archive" calls `archive(first, { wholeGroup: true, navigateTo: <'/' if the current dashboard is in this group> })`.
  - "Move up" and "Move down" call `move(first.dashboard_id, moveGroupBody(userGroups, groupId, index∓1))`. They are disabled at the ends.
- `currentInGroup` is true when `currentId` is one of the group's members.

- [ ] **Step 1: Write the failing test.** Render with fixtures of a system group (two members) and two user groups. Open the menu (`userEvent.click(getByRole('button', {name: 'Views actions'}))`) and assert:
  - The system menu has exactly the items "Duplicate" and "Archive".
  - A user group's menu has "Duplicate", "Archive", "Move up" and "Move down", with "Move up" disabled on the first user group.
  - Clicking "Move down" on the first user group calls `move` with `{after: <second group's first id>}`.
  - Clicking "Archive" on the system group while viewing one of its tabs calls `archive` with `{wholeGroup: true, navigateTo: '/'}`.

  Mock `useDashboardActions`.

- [ ] **Step 2: Run the test and confirm it fails.** Run `npx vitest run src/components/SidebarGroupMenu.test.tsx`. Expected: FAIL.

- [ ] **Step 3: Implement it** with `DropdownMenu`, `DropdownMenuTrigger asChild`, `DropdownMenuContent side="right" align="start"`, `DropdownMenuItem` and `DropdownMenuSeparator` before the Move items. `showOnHover` keeps the trigger hidden until hover on desktop; the shadcn sidebar shows it on phones.

- [ ] **Step 4: Run `npm run typecheck && npm test`.** Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add web/src
git commit -m "feat(web): duplicate, archive and move a dashboard from the sidebar"
```

---

### Task 7: The header's "…" menu on user dashboards (including Move to)

**Files:**
- Create: `web/src/components/DashboardMenu.tsx`, `web/src/components/DashboardMenu.test.tsx`
- Modify: `web/src/components/DashboardHeader.tsx` (a `menu?: ReactNode` prop rendered after the refresh button)
- Modify: `web/src/pages/Dashboard.tsx` (pass `<DashboardMenu …/>` only when `dashboard.owner === 'user' && !dashboard.archived_at`)

**Interfaces:**
- Consumes: `useDashboardActions`, `liveGroups`, `moveTabBody` and `nextAfterArchive` from Task 5.
- Produces: `<DashboardMenu dashboard={DashboardDetail} list={DashboardInfo[]} />`.

Behaviour (D11):
- The trigger is `aria-label="Dashboard actions"`. Let `n = dashboard.tabs.length` and `i` be this dashboard's index in `tabs`.
- **`n > 1`:**
  - "Duplicate tab" calls `duplicate(d)` (default, no whole group).
  - "Archive tab" calls `archive(d, { navigateTo: nextAfterArchive(tabs, id) })`.
  - "Move left" calls `move(id, moveTabBody(tabs, id, group_id, i-1))` and "Move right" calls `move(id, moveTabBody(tabs, id, group_id, i+1))`. Each is disabled at its end.
  - "Move to" opens a submenu (`DropdownMenuSub`): every other live *user* group from `liveGroups(list)`, by its first member's title, calling `move(id, { group_id: g.groupId })`. A separator and "Own dashboard" follow, calling `move(id, { group_id: 0 })`.
- **`n === 1`:**
  - "Duplicate" calls `duplicate(d, { wholeGroup: true })`.
  - "Archive" calls `archive(d, { navigateTo: '/' })`.
  - "Move to" lists the other user groups only; "Own dashboard" is hidden.
  - "Move to" is hidden entirely when there are no other user groups.

- [ ] **Step 1: Write the failing test.** Cover:
  - The items for `n=3` with `i=0` ("Move left" disabled).
  - The items for `n=1` (no "Move left"/"Move right", no "Own dashboard").
  - "Move to" → "Marketing" calls `move(id, {group_id: <Marketing's group>})`.
  - "Own dashboard" calls `move(id, {group_id: 0})`.
  - "Archive tab" on the last tab navigates to the previous one.
  - `Dashboard.test.tsx`: a system dashboard renders no `Dashboard actions` button.

- [ ] **Step 2: Run the test and confirm it fails.** Run `npx vitest run src/components/DashboardMenu.test.tsx`. Expected: FAIL.

- [ ] **Step 3: Implement it, then run `npm run typecheck && npm test`.** Expected: PASS.

- [ ] **Step 4: Commit.**

```bash
git add web/src
git commit -m "feat(web): duplicate, archive and move a tab from the dashboard header"
```

---

### Task 8: Drag to reorder user groups and user tabs

**Files:**
- Modify: `web/package.json` and `package-lock.json` (`npm install @dnd-kit/core @dnd-kit/sortable @dnd-kit/utilities`)
- Modify: `web/src/components/AppSidebar.tsx` (wrap the "Yours" `SidebarMenu` in `DndContext` + `SortableContext` with `verticalListSortingStrategy`; each user entry `useSortable({ id: groupId })`)
- Modify: `web/src/components/ReportTabs.tsx` (an optional `onMove?: (id: number, to: number) => void`; when given, wrap the `TabsList` in `DndContext` + `SortableContext` with `horizontalListSortingStrategy`; each trigger `useSortable({ id: dashboard_id })`)
- Modify: `web/src/pages/Dashboard.tsx` (pass `onMove` to `ReportTabs` only for user dashboards)
- Test: `web/src/components/ReportTabs.test.tsx` (create), `AppSidebar.test.tsx`

**Interfaces:**
- Consumes: `moveGroupBody`, `moveTabBody` and `useDashboardActions().move`.

Behaviour (D14, D15):
- Sensors: `PointerSensor` with `activationConstraint: { distance: 6 }`, so a click still navigates, and `KeyboardSensor` with `sortableKeyboardCoordinates`.
- On `onDragEnd({active, over})`, compute the new index, keep an optimistic local order in `useState` (reset from props whenever props change), and call `move(...)`. When it resolves, the refetch replaces the props. When the server refuses, the hook toasts and refetches, which snaps the order back (review focus 4).
- System entries and system tabs have no sortable wrapper: they are not draggable, and the "Yours" context cannot drop above them because it contains only user groups.

- [ ] **Step 1: Write the failing tests.**
  - Tests drive the `onDragEnd` handlers directly, since jsdom has no layout. Export the handler logic as `reorder(ids: number[], activeId: number, overId: number): { to: number } | null` from `arrange.ts`, and test it there: moving the first id over the third gives `{ to: 2 }`, and over itself gives `null`.
  - In `ReportTabs.test.tsx`, assert that tabs render with `aria-roledescription="sortable"` only when `onMove` is given.

- [ ] **Step 2: Run the tests and confirm they fail.** Run `npx vitest run src/lib/arrange.test.ts src/components/ReportTabs.test.tsx`. Expected: FAIL.

- [ ] **Step 3: Install dnd-kit, implement it, then run `npm run typecheck && npm test && npm run build`.** Expected: PASS.

- [ ] **Step 4: Commit.**

```bash
git add web/package.json web/package-lock.json web/src
git commit -m "feat(web): drag to reorder dashboards and tabs"
```

---

### Task 9: The dashboard gallery and the archived line

**Files:**
- Create: `web/src/pages/gallery/DashboardsGallery.tsx`, `web/src/pages/gallery/DashboardsGallery.test.tsx`
- Modify: `web/src/App.tsx` (`<Route path="/gallery/dashboards" element={<OnlineOnly><DashboardsGallery /></OnlineOnly>} />` before the `/gallery/*` redirect)
- Modify: `web/src/components/AppSidebar.tsx` (a "Dashboards" item with `LayoutGridIcon` in the Gallery group, active on `/gallery/dashboards`)
- Modify: `web/src/pages/Dashboard.tsx` (the archived line above the grid)

**Interfaces:**
- Consumes: `dashboardsQuery`, `useDashboardActions`, `purgeDate` and `GalleryLayout` (props: `title`, `description`, `sections`, `children`).

Behaviour (D17, D18):
- **Gallery title and description:** "Dashboards". The description reads "The dashboards that ship with twillingate, and the ones you archived. Restore one to put it back in the sidebar, or copy a system tab as a dashboard of your own." Sections: `[{id:'system',label:'System'},{id:'archived',label:'Archived'}]`.
- **System section**, built from every system dashboard grouped by `group_id` (archived included, in list order). One card per group:
  - The first member's title, then a badge, "In the sidebar" or "Archived".
  - A button: "Archive" (`archive(first, {wholeGroup: true})`) when any member is live, else "Restore" (`restore(first.dashboard_id, true)`).
  - A list of its tabs, each a `Link` to `/dashboards/<id>` with a "Copy as a dashboard" button (`duplicate(tab, { archiveSource: false })`).
- **Archived section**, built from every archived user dashboard in list order. Each row shows:
  - its title;
  - "in <first live or first member's title>" when its group has other members;
  - `deleted on <d MMM>` from `purgeDate(archived_at, purge_after_days)`, omitted when undefined (review focus 5);
  - a "Restore" button (`restore(id)`).
  - When there are none, the section reads "Nothing archived."
- **Archived line** on `Dashboard.tsx`: when `dashboard.archived_at` is set, a muted bar above the header reads "Archived: not in the sidebar" with a "Restore" button. The button calls `restore(id, dashboard.owner === 'system')`.

- [ ] **Step 1: Write the failing tests.**
  - The gallery shows "Archive" on a live system group and "Restore" on an archived one.
  - "Copy as a dashboard" calls `duplicate(tab, {archiveSource: false})`.
  - The archived user dashboard shows `deleted on 1 Oct` given `archived_at: '2026-09-01T00:00:00Z'` and `purge_after_days: 30`, and no "deleted on" line without `purge_after_days`.
  - `Dashboard.test.tsx`: the archived line shows for an archived dashboard only.

- [ ] **Step 2: Run the tests and confirm they fail.** Run `npx vitest run src/pages/gallery/DashboardsGallery.test.tsx src/pages/Dashboard.test.tsx`. Expected: FAIL.

- [ ] **Step 3: Implement it, then run `npm run typecheck && npm test`.** Expected: PASS.

- [ ] **Step 4: Commit.**

```bash
git add web/src
git commit -m "feat(web): restore and copy dashboards from the dashboard gallery"
```

---

### Task 10: End-to-end flows

**Files:**
- Create: `web/e2e/arrange.spec.ts`
- Reference: `web/e2e/app.spec.ts` for `login`, `authHeaders` and `dashboardIds` (copy the small helpers; don't import across spec files)

Every test that changes state undoes it in `test.afterEach` over REST: restore group 1 with `whole_group`, and archive any user dashboards it made. Use `test.describe.configure({ mode: 'serial' })` so that `app.spec.ts`'s assumptions (Reports live, five tabs) still hold.

- [ ] **Step 1: Write the tests.**
  1. **Replace Reports from the sidebar.** Log in, open the "Views actions" menu, click "Duplicate", and expect the URL to be a new id with five tabs. The sidebar has no system "Views" entry, and "Views (copy)" is under "Yours". Go to `/app/gallery/dashboards`, click "Restore" on the system group, and the sidebar shows "Views" again.
  2. **Archive a user tab and undo.** Over REST, create a group "E2E" with tabs "One" and "Two". Open "Two", click "Dashboard actions", then "Archive tab". The URL becomes "One", a toast shows "Archived 'Two'", and clicking "Undo" brings the "Two" tab back.
  3. **Move a tab between groups.** Over REST, create "E2E A" (tabs A1 and A2) and "E2E B". Open A2, choose "Move to" then "E2E B", and the tab bar shows E2E B and A2. Choose "Move to" then "Own dashboard", and A2 is its own sidebar entry.
  4. **Drag reorder persists.** Over REST, create "E2E X" and "E2E Y". Drag "E2E Y" above "E2E X" in the sidebar with `page.dragTo`, or with keyboard (focus the entry, Space, ArrowUp, Space), whichever works reliably. Reload, and the order is kept.
  5. **Copy a system tab from the gallery while Reports is archived.** Over REST, archive group 1 with `whole_group`. In the gallery, click "Copy as a dashboard" on "Users", and the page opens "Users (copy)" with no tab bar.

- [ ] **Step 2: Run the suite.** Run `cd web && npm run e2e`. It builds the binary through `serve.sh`; read `web/e2e/serve.sh` for its prerequisites. Expected: all pass, including the untouched `app.spec.ts` and `gallery.spec.ts`.

- [ ] **Step 3: Commit.**

```bash
git add web/e2e
git commit -m "test(web): end-to-end arranging of dashboards"
```

---

### Task 11: Full check

- [ ] **Step 1:** Run `export PATH=/usr/local/go/bin:$PATH && make check`. Expected: PASS (vet, coverage, restore test). Fix what fails in the task that owns it.
- [ ] **Step 2:** Run `cd web && npm run typecheck && npm test && npm run build`. Expected: PASS. `internal/reporting/ui/components.json` must not change, since no widget contract changed.
