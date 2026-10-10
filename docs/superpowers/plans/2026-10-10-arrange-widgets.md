# Arranging Widgets: Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Draw widths as defined from a 900px grid (so a 1280px laptop with the sidebar shows, and can resize, the designed layout), announce sizes to screen readers while resizing, and cover the keyboard move end to end.

**Architecture:** Web only, on branch `feat/arrange-widgets` (PR #157), which already has the move/resize feature (D1–D6). `lib/grid.ts` owns the full-layout width; `WidgetGrid` and `span` read it. `WidgetCell` gains a live region. Playwright gains one keyboard test.

**Tech Stack:** React 19 + TypeScript, dnd-kit (core 6, sortable 10), Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-10-arrange-widgets-design.md` (D7–D9).

## Global Constraints

- Work in `/home/dmitry/dev/lab/twillingate/.claude/worktrees/bridge-cse_01KeZBL4DcvufhzMU9gTNpSo` on branch `feat/arrange-widgets`. `web/node_modules` is installed.
- Go is at `/usr/local/go/bin` (not on PATH): prefix Go/make commands with `PATH=/usr/local/go/bin:$PATH`.
- Vitest on this machine needs `--testTimeout=30000`: `cd web && npx vitest run <files> --testTimeout=30000`. Typecheck: `cd web && npm run typecheck`.
- Playwright: `cd web && CI=1 npx playwright test --project=arrange -g "<test name>" e2e/arrange.spec.ts` (the `arrange` project depends on `chromium`, so the whole suite runs first; about 5 minutes). Port 18080 must be free (`curl -s http://127.0.0.1:18080/healthz` prints nothing). Delete `web/test-results/` afterwards.
- Commits: Conventional Commits, lower case, imperative, no trailing period; scope `web` (or `docs`). End every message with a blank line and `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Do not push.
- Match the surrounding code: comments explain why, in full sentences, as in `WidgetCell.tsx` and `lib/grid.ts`; no new dependencies.
- Exact values: `FULL_GRID_PX = 900`; the live-region text is `` `${label}: ${width} of 12 columns wide, ${height} rows tall` `` where `label` is `widget.title ?? widget.name`.

## Review Focus

- **The two widths agree:** the resize corner appears exactly when `span` draws widths as defined (both read `FULL_GRID_PX`); no second copy of 900 or 1024 for the grid remains in `web/src` (AppShell's `(min-width: 1024px)` media query is about the sidebar and stays).
- **The live region is quiet when nothing is being sized:** it is empty unless a draft size exists, so a page of cards does not read every size on load.
- **The keyboard test drives real keys** (Space, ArrowRight, Space) on the grip, not a pointer, and checks the order through the API.

---

### Task 1: Draw widths as defined from a 900px grid (D7)

**Files:**
- Modify: `web/src/lib/grid.ts`
- Modify: `web/src/lib/grid.test.ts`
- Modify: `web/src/components/WidgetGrid.tsx`
- Modify: `docs/reporting.md` (Layout section)

- [ ] **Step 1: Change the tests first.** In `web/src/lib/grid.test.ts`, the boundary test becomes:

```ts
  it('switches exactly at 640 and FULL_GRID_PX (900)', () => {
    expect(FULL_GRID_PX).toBe(900)
    expect(span(4, 900)).toBe(4)
    expect(span(4, 899)).toBe(6)
    expect(span(4, 640)).toBe(6)
    expect(span(4, 639)).toBe(12)
  })
```

and add a case that a 976px grid (a 1280px screen with the sidebar open) keeps every width:

```ts
  it('keeps every width at 976px, a 1280px screen beside the sidebar', () => {
    for (let w = 1; w <= 12; w++) expect(span(w, 976)).toBe(w)
  })
```

Import `FULL_GRID_PX` alongside `span`. Run `npx vitest run src/lib/grid.test.ts --testTimeout=30000`: it fails.

- [ ] **Step 2: `lib/grid.ts`.** Add, above `span`:

```ts
/**
 * The grid width from which every widget spans the columns it defines (D7):
 * a 1280px screen with the sidebar open has a grid of about 976px, which
 * should show a dashboard as its owner laid it out. Below it the spans
 * are narrowed, so the page offers no resizing there either.
 */
export const FULL_GRID_PX = 900
```

and make `span` read it (`if (gridPx >= FULL_GRID_PX) return width`), updating its doc comment from "from 1024px" to "from `FULL_GRID_PX`". Run the grid test: it passes.

- [ ] **Step 3: `WidgetGrid.tsx`.** Delete the local `FULL_GRID_PX` constant and its comment; import `FULL_GRID_PX` from `@/lib/grid`. Keep the comment's point where `resizable` is computed, in one line: resizing needs the widths drawn as saved, and moving works at every width.

- [ ] **Step 4: Docs.** In `docs/reporting.md`'s `## Layout` section, the `width` bullet gains one sentence saying the page draws widths as defined when its grid is at least 900px wide (a 1280px screen with the sidebar open); narrower, a widget up to 6 wide takes half the row and a wider one the whole row, and below 640px two widgets up to 3 wide share a row and others take the whole row. Keep the existing wording otherwise.

- [ ] **Step 5: Verify and commit.** `npx vitest run src/lib/grid.test.ts src/components/WidgetGrid.test.tsx src/components/LayoutGrid.test.tsx --testTimeout=30000` and `npm run typecheck` pass; `grep -rn "1024" web/src` shows only AppShell's sidebar query, its comment, `SidebarGroupMenu.test.tsx` and `LimitsPanel.tsx`'s KiB. Commit: `feat(web): draw widget widths as defined from a 900px grid`.

### Task 2: Announce sizes while resizing, and test the keyboard move (D8, D9)

**Files:**
- Modify: `web/src/components/WidgetCell.tsx`
- Modify: `web/src/components/WidgetGrid.test.tsx`
- Modify: `web/e2e/arrange.spec.ts`

- [ ] **Step 1: Test the live region (vitest).** In `WidgetGrid.test.tsx`, add a test: with `arrange` and the 1200px grid, no element with `role="status"` inside the cell has text before any key; after `ArrowRight` on "Resize Note 1" (a 6 × 4 note) the cell's status reads exactly `Note 1: 7 of 12 columns wide, 4 rows tall`; after `Escape` it is empty again. Run it: it fails.

- [ ] **Step 2: The live region.** In `WidgetCell`, render inside the cell (not inside the card, so it survives the card's re-renders) a `<span role="status" aria-live="polite" className="sr-only">` whose text is the exact string from Global Constraints while `draft` is set, and empty otherwise. It is rendered whenever `onResize` is set (a live region must exist before its text changes to be announced). Keep the existing `aria-describedby` description. Run the vitest file: it passes.

- [ ] **Step 3: The keyboard move (Playwright).** In `web/e2e/arrange.spec.ts`, after the existing "moves a widget by its grip and resizes it by its corner" test, add `test('moves a widget from the keyboard', …)`: 1600 × 1000 viewport, a dashboard from `createWidgetDashboard(request, 'E2E Keys')` (pushed to `toArchive`), log in, open it, wait for network idle, then focus the button "Move E2E W1" (`.focus()`), press `Space`, `ArrowRight`, `Space`, and `expect.poll(() => widgetLayout(request, id))` to equal `['E2E W2 4x3', 'E2E W1 4x3', 'E2E W3 4x3']`.

- [ ] **Step 4: Make it pass.** Run it (command in Global Constraints). If it fails because dnd-kit's `sortableKeyboardCoordinates` does not move to the neighbouring card when cards do not shift (`WidgetGrid`'s `inPlace` strategy), give `useReorder` callers in a grid a coordinate getter that moves the dragged rect onto the next card in the arrow's direction (the closest droppable whose center lies in that direction), in `web/src/hooks/use-reorder.ts` beside `keyboardCodes`, used only for `axis === 'xy'`, with a vitest in `use-reorder.test.ts`. Do not change the tab or sidebar lists' behaviour. If it passes as is, change nothing there.

- [ ] **Step 5: Verify and commit.** The vitest files touched, `npm run typecheck`, and the Playwright run pass. Commit: `feat(web): announce a widget's size while resizing it` (plus, if Step 4 changed code, `fix(web): move a widget to its neighbour from the keyboard` as its own commit).
