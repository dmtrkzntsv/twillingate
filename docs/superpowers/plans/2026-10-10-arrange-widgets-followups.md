# Arranging Widgets: Fast and Accurate Follow-ups Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No widget edit undoes another (D10); a drag redraws only the cards it touches (D11); a save refetches only its dashboard and never flashes an intermediate size (D12).

**Architecture:** On branch `feat/arrange-widgets` (PR #157). Go: `store.Store.UpdateWidget` gains a column selection and `SetWidgetLayout` goes. Web: `use-widget-actions.ts` scopes its refetch and resolves after it; `WidgetCell.tsx` keeps the last asked size until its save returns; the card body is kept out of drag re-renders.

**Tech Stack:** Go 1.24+, modernc SQLite; React 19 + TypeScript, TanStack Query, dnd-kit; Vitest; Playwright.

**Spec:** `docs/superpowers/specs/2026-10-10-arrange-widgets-design.md` (D10–D12).

## Global Constraints

- Work in `/home/dmitry/dev/lab/twillingate/.claude/worktrees/bridge-cse_01KeZBL4DcvufhzMU9gTNpSo` on branch `feat/arrange-widgets`. `web/node_modules` is installed. Do not push.
- Go is at `/usr/local/go/bin`: prefix with `PATH=/usr/local/go/bin:$PATH`. A test needing a migrated database copies one (`internal/store/storetest`, or `snapshotAt` inside `store/sqlite`), never migrates its own.
- Vitest: `cd web && npx vitest run <files> --testTimeout=30000`; typecheck `cd web && npm run typecheck`.
- Playwright: `cd web && CI=1 npx playwright test --project=arrange -g "<name>" e2e/arrange.spec.ts` (runs the chromium suite first, ~5 min; 15-minute timeout). Port 18080 must be free. Delete `web/test-results/` afterwards; leave no stray spec files.
- Before the last commit of a task that touches Go: `PATH=/usr/local/go/bin:$PATH make check`.
- Commits: Conventional Commits, lower case, imperative, no trailing period; scopes `store`, `reporting`, `web`. Only `feat`/`fix`/`perf` reach release notes. End every message with a blank line and `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Match the surrounding code: comments explain why, in full sentences, at the density of the file; typed refusals (`store.Refuse(store.ErrConflict|ErrNotFound, …)`); each consumer declares its own store slice (`reporting.Store` in `internal/reporting/reporting.go`); no new dependencies.

## Review Focus

- **Content stays consistent with itself:** the content columns are written together, never one at a time, because they were validated together.
- **A layout column the call did not set is never written,** on either path (layout-only or content), including the retry path of a move.
- **The memo boundary is real:** a test fails if a card's body re-renders when only drag state changes; the measured numbers are from the production build, before and after.
- **The shown size never goes back to an older asked size,** and a refusal still snaps back at once.

---

### Task 1: Write only the columns an update changes (D10)

**Files:**
- Modify: `internal/store/store.go` (the Store interface and a new `WidgetColumns` type beside `Widget`)
- Modify: `internal/store/sqlite/reporting.go` (`UpdateWidget`; delete `SetWidgetLayout`)
- Modify: `internal/reporting/reporting.go` (the reporting Store slice), `internal/reporting/ops_widget.go`
- Modify tests: `internal/store/sqlite/*_test.go` (where `SetWidgetLayout` and `UpdateWidget` are tested), `internal/reporting/ops_test.go` (`editingStore`, `TestUpdateWidgetLayoutKeepsAConcurrentEdit`, and every `svc.st.UpdateWidget` call)

- [ ] **Step 1: The type.** In `internal/store/store.go`:

```go
// WidgetColumns names what a widget update writes. Content is the
// component, name, title, props and source, written together since they
// are validated together; the layout columns go one by one, so a move
// never writes a size back and a resize never writes a place back.
type WidgetColumns struct {
	Content, SortKey, Width, Height bool
}
```

and the interface method becomes `UpdateWidget(ctx context.Context, w Widget, cols WidgetColumns, a AuditEntry) error`; `SetWidgetLayout` is removed from both interfaces.

- [ ] **Step 2: Failing tests.** In the sqlite package, a test that `UpdateWidget` with `{Width: true}` changes only `width` (title, props, source, sort_key, height as they were), with `{Content: true}` changes the content columns and not width/height/sort_key, that a call with no column set is refused `ErrInvalid` (a programming error, refused rather than a silent audit row), an unknown id is `ErrNotFound`, a sort-key or name UNIQUE failure is `ErrConflict`, and the audit row's subject is `widget/<id>`. In `internal/reporting/ops_test.go`, keep `TestUpdateWidgetLayoutKeepsAConcurrentEdit` and add its mirror `TestUpdateWidgetContentKeepsAConcurrentLayout`: a store wrapper changes width, height and sort_key between the service's read and its write while the call changes only `title`; after it, the title is the new one and width, height and place are the wrapper's. Add a third: a call changing only `width` while the wrapper changes `height` keeps the wrapper's height. Run them: they fail (or do not compile).

- [ ] **Step 3: The store.** `UpdateWidget` builds its `SET` from `cols` (fixed column names, values as parameters; `updated_at` always), refuses an empty selection with `store.Refuse(store.ErrInvalid, "update widget %d: no column to write", id)`, and keeps today's conflict and not-found mapping. Delete `SetWidgetLayout` and its test (its cases move into Step 2's).

- [ ] **Step 4: The service.** In `Service.UpdateWidget`, compute `cols` from the input: `Content` when any of name, component, title, props, source is set; `Width`/`Height` when set; `SortKey` when `after` moves it (not when `after` names the widget itself). The `write` closure becomes one call, `s.st.UpdateWidget(ctx, w, cols, a)`, on both the plain and the `retryConflict` path. An empty input (no field at all) writes nothing and returns the widget as it is, without an audit row. Keep the refusal mapping as it is. Update the doc comment's last sentence to say every update writes only the columns it changes.

- [ ] **Step 5: Verify and commit.** `go test ./internal/store/... ./internal/reporting/...` and `make check` pass. Commit: `fix(reporting): write only the widget columns an update changes`.

### Task 2: Refetch only the widget's dashboard and keep the last asked size (D12)

**Files:**
- Modify: `web/src/hooks/use-widget-actions.ts`, `web/src/hooks/use-widget-actions.test.tsx`
- Modify: `web/src/components/WidgetCell.tsx`, `web/src/components/WidgetGrid.tsx` (only if the move/resize signature needs the dashboard id), `web/src/components/WidgetGrid.test.tsx`
- Modify: `web/e2e/arrange.spec.ts` (the corner-vs-body assertion)

- [ ] **Step 1: Scoped refetch.** `move` and `resize` take the widget's dashboard id (or the widget) and invalidate `['dashboard', dashboardId]` only, awaiting it in `finally` as today, so each still resolves after its refetch. Update the hook's tests: the invalidation is called with `{ queryKey: ['dashboard', <id>] }`, and another cached dashboard's query is not refetched (seed two queries in the client and check the other one's `dataUpdatedAt` or fetch count is unchanged).

- [ ] **Step 2: Failing test for the flash.** In `WidgetGrid.test.tsx`: a 6 × 4 note; resize to 8 (pointer), whose save stays pending; resize back to 6, whose save stays pending; then re-render with the server's 8 (the first save's refetch) and check the cell still spans 6; resolve the first save (true) and check it still spans 6; re-render with the server's 6 and resolve the second; it spans 6. Also: a refused save (resolves false) still snaps back to the stored size at once. Run: the first fails on today's code.

- [ ] **Step 3: The fix in `WidgetCell`.** Drop the rule that clears `saving` when the stored size changes. Keep `saving` (the last asked size) until its own save resolves: on `true` clear it if it is still the current one (the refetch has landed by then, since the hook resolves after it), on `false` clear it at once. An older save resolving never clears a newer one. Keep the `shown`-based comparison in `save`.

- [ ] **Step 4: Tolerant e2e geometry.** In `web/e2e/arrange.spec.ts`, the check that the corner covers none of the body compares with a half-pixel tolerance (`corner.x >= body.x + body.width - 0.5 || corner.y >= body.y + body.height - 0.5`), with a comment saying layout values are fractional.

- [ ] **Step 5: Verify and commit.** The touched vitest files, typecheck, and the Playwright arrange project pass. Commits: `perf(web): refetch only the dashboard a widget was moved or resized on` and `fix(web): keep the last size asked for while it saves` (the e2e tolerance may go with either, or as `test(web): …`).

### Task 3: A drag redraws only the cards it touches (D11)

**Files:**
- Modify: `web/src/components/WidgetCell.tsx`, `web/src/components/WidgetCard.tsx`, `web/src/components/WidgetGrid.tsx` as needed
- Create or modify tests: `web/src/components/WidgetGrid.test.tsx` (or a new `WidgetCell.test.tsx`)

- [ ] **Step 1: Measure first.** In the production build (`make build`'s UI, or the Playwright web server), with a throwaway Playwright script outside `web/e2e` committed nowhere (or a temporary spec deleted afterwards): create a dashboard of 20 SQL chart widgets over the seeded project (copy them from the system dashboards with `copy_widget`, so they render real charts with data), open it at 1600 × 1000, and drag one card's grip slowly across five others (30+ pointer steps) and resize one card by four columns. Record: the number of card-body renders during the drag (count them by wrapping the body in a `React.Profiler` only in the temporary build, or by counting with a `PerformanceObserver` on long tasks and `performance.now()` around frames — say which), and the longest task and total scripting time from a Chrome trace or `PerformanceObserver('longtask')`. Write the numbers in the report.

- [ ] **Step 2: Failing test.** A vitest that fails while a card's body re-renders when only drag or hover state changes: e.g. mock one widget component module to count its renders, render the grid with two cards, then change only what a drag changes for the second card (start a keyboard drag on the first card's grip with Space and ArrowRight, as the e2e keyboard test does, or re-render the grid with an unchanged widgets array and a new `busy`/over state) and assert the other card's body render count is unchanged. Pick the setup that genuinely exercises dnd-kit's context changes in jsdom; if none does, say so in the report and rely on Step 1's measurement plus a memo-boundary test (same props in, no body render).

- [ ] **Step 3: The memo boundary.** Make the card's body (everything below the frame's handles: the component render, its data query and table view) a memoized component whose props are stable across drag renders (stable callbacks, the widget object, params from `paramsFor`, no new JSX passed in). The grip, the corner, the badge, the ring and the drop bar stay outside it. `WidgetGrid`'s `paramsFor` results must be stable per widget across renders (memoize per widget id and the page's selection if they are not). Do not change behaviour: refresh, share, download, table views and the existing tests all still pass.

- [ ] **Step 4: Measure again** with the same script and record the after numbers next to the before ones. Target: during the drag, only the dragged card, the card under it and the card it left re-render their bodies (ideally none of them re-renders its body at all); the longest task during the drag drops measurably.

- [ ] **Step 5: Verify and commit.** The full vitest suite, typecheck and the Playwright arrange project pass. Commit: `perf(web): redraw only the cards a drag touches`.

### Task 4: A drop redraws only the card that moved (D11, added after Task 3)

Task 3 measured that a drop still redraws every card whose index shifted (15–16 of 20; longest task ~370 ms): TanStack Query's structural sharing matches `dashboard.widgets` by index, so after a reorder each shifted widget is a new object and fails the card's memo.

**Files:**
- Modify: `web/src/lib/queries.ts` (`dashboardQuery`), its test (create `web/src/lib/queries.test.ts` if none)
- Modify: `web/src/components/WidgetGrid.tsx` (the interned params map)

- [ ] **Step 1: Failing test.** Given old and new dashboard details whose widgets are the same set reordered (one widget's content also changed), the dashboard query's structural sharing returns a new detail whose unchanged widgets are the *same objects* as before (by `widget_id`), the changed widget is a new object, and the order is the new one; equal data returns the old detail object itself.
- [ ] **Step 2: Implement** a `structuralSharing` for `dashboardQuery` that runs TanStack's `replaceEqualDeep` on the detail, then replaces each widget with the previous widget of the same `widget_id` when `replaceEqualDeep(prev, next) === prev`. Keep it small, typed and commented (why: a drop reorders the array, and index-based sharing would hand every shifted card a new widget).
- [ ] **Step 3: Prune the params map** in `WidgetGrid`: keep only the entries used in the current render (rebuild from the previous map each render), and note in its comment that keys come from `JSON.stringify`, which drops `undefined` fields (correct because absent and undefined mean the same query).
- [ ] **Step 4: Measure** the drop with the same method as Task 3 (body renders on drop, longest task) before and after, and record both.
- [ ] **Step 5: Verify and commit.** Touched vitest files, the full vitest suite, typecheck, and the Playwright arrange project pass. Commit: `perf(web): keep unchanged widgets when a dashboard is reordered`.
