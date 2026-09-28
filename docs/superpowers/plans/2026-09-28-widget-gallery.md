# Widget Gallery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A page at `/app/gallery/components` that renders every dashboard component from built-in fixtures, with its contract and a copyable `add_widget` snippet.

**Architecture:** Each widget module in `web/src/components/widgets/` exports `examples` next to `contract`. The card chrome and the 12-column grid are split out of `WidgetCard`/`WidgetGrid` into presentational components (`WidgetFrame`, `LayoutGrid`), so the gallery renders examples in exactly the frame and grid a dashboard uses without fetching anything. A generic `GalleryLayout` (app shell, top bar, title, jump list) hosts the components page; a templates page can reuse it later.

**Tech Stack:** React 19, TypeScript, react-router 8, Tailwind 4, shadcn/ui, Recharts 3, vitest + Testing Library, Playwright.

**Spec:** `docs/superpowers/specs/2026-09-28-widget-gallery-design.md` (PR 1 only: decisions 1–9, the gallery tests, the PR 1 doc line). PR 2 (curation) is not part of this plan.

## Global Constraints

- All work is under `web/` except one line in `docs/reporting.md`. No Go change: `internal/reporting/ui.go` already serves `index.html` for any `/app/` path.
- Routes live under the router's `basename="/app"`: `/gallery/components`, `/gallery` (redirect), `/gallery/*` (redirect).
- Sidebar: a "Gallery" group below "Yours" with one entry, "Components".
- Fixtures are deterministic literals; no `Math.random`, no `Date.now`.
- `internal/reporting/ui/components.json` must not change (run `npm run build` in `web/` and check `git status` shows no diff on it). `scripts/manifest.ts` reads only `contract`.
- Copy `add_widget` JSON is exactly `JSON.stringify({ component: name, props: example.props }, null, 2)`.
- Commits: Conventional Commits, scope `web` (`feat(web): …`, `test(web): …`, `refactor(web): …`), lower case, imperative, no trailing period, ending with the line `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` after a blank line.
- Checks per task: `cd web && npx tsc -b --noEmit && npx vitest run` must pass. Match the surrounding code's comment density and idiom (short doc comments on exported things, no chatty comments).

## Review Focus

- **Clipboard unavailable** (the app served over plain `http://` on a LAN, where `navigator.clipboard` is undefined, or `writeText` rejecting): the copy buttons must not throw; they show "Copy failed" and the JSON stays visible and selectable in the contract details. Test in Task 3.
- **Phone width (390px)**: the gallery grid collapses like a dashboard's (`span()`), the contract block wraps instead of scrolling the page sideways. Test in Task 5 (e2e at phone viewport asserts `document.documentElement.scrollWidth <= innerWidth`).
- **Signed-out deep link** to `/app/gallery/components`: the login bounce returns to the gallery, not to a dashboard. Test in Task 5.
- **Unknown gallery path** (`/app/gallery/templates` before templates exist, a typo): redirects to `/gallery/components` rather than a blank page. Test in Task 3.
- **A component added later without examples, or an example that stops fitting its contract** after a contract change: vitest fails naming the component. Test in Task 1.

---

## File Structure

- `web/src/components/widgets/types.ts` — add `Example`, extend `WidgetModule` with `examples`.
- `web/src/components/widgets/*.tsx` (17 modules) — each exports `examples: Example[]`.
- `web/src/components/widgets/examples.test.tsx` — every module has examples; each fits its contract and renders.
- `web/src/components/WidgetFrame.tsx` — the card chrome (was inline in `WidgetCard`).
- `web/src/components/LayoutGrid.tsx` — the 12-column grid of arbitrary cells (was inline in `WidgetGrid`).
- `web/src/components/WidgetCard.tsx`, `WidgetGrid.tsx` — use the two above; behaviour unchanged.
- `web/src/pages/gallery/GalleryLayout.tsx` — generic gallery page shell.
- `web/src/pages/gallery/ComponentsGallery.tsx` — the components page.
- `web/src/pages/gallery/ComponentEntry.tsx` — one component: header, contract details, copy actions, examples in the grid.
- `web/src/pages/gallery/ComponentsGallery.test.tsx` — page tests.
- `web/src/App.tsx` — routes.
- `web/src/components/AppSidebar.tsx` (+ test) — Gallery group.
- `web/e2e/gallery.spec.ts` — e2e.
- `docs/reporting.md` — one line.

---

### Task 1: Examples on every widget module

**Files:**
- Modify: `web/src/components/widgets/types.ts`
- Modify: all 17 modules in `web/src/components/widgets/` (`area bar bar_list calendar combo funnel heatmap line map markdown pie radar radial scatter stat table treemap`)
- Create: `web/src/components/widgets/examples.test.tsx`

**Interfaces:**
- Produces: `export interface Example { title: string; props: Record<string, unknown>; data: SqlData | MarkdownData }` in `types.ts`; `WidgetModule` gains `examples: Example[]`; every module exports `export const examples: Example[]`.

- [ ] **Step 1: Add the type**

In `types.ts`, after `WidgetProps`:

```ts
/**
 * A worked example the gallery renders: a result shaped exactly as the
 * server returns it, and the props to show it with.
 */
export interface Example {
  title: string
  props: Record<string, unknown>
  data: SqlData | MarkdownData
}
```

and in `WidgetModule` add, after `contract`:

```ts
  /** At least one; the gallery shows each at the default size. */
  examples: Example[]
```

- [ ] **Step 2: Write the failing test**

Create `examples.test.tsx`:

```tsx
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { toRecords } from '@/lib/records'
import { widgets } from './index'
import type { Contract, Example, SqlData } from './types'

type Schema = { type?: string; enum?: unknown[]; items?: Schema; additionalProperties?: Schema | boolean }

/** The props schema's own vocabulary (types, enums, nested maps/arrays), no more: the Go side runs full JSON Schema. */
function propErrors(props: Record<string, unknown>, contract: Contract): string[] {
  const properties = (contract.props as { properties?: Record<string, Schema> }).properties ?? {}
  return Object.entries(props).flatMap(([key, value]) => {
    const schema = properties[key]
    return schema ? valueErrors(key, value, schema) : [`unknown prop ${key}`]
  })
}

function valueErrors(path: string, value: unknown, schema: Schema): string[] {
  if (schema.enum && !schema.enum.includes(value)) return [`${path}: ${JSON.stringify(value)} not in ${JSON.stringify(schema.enum)}`]
  switch (schema.type) {
    case 'boolean':
      return typeof value === 'boolean' ? [] : [`${path}: not a boolean`]
    case 'string':
      return typeof value === 'string' ? [] : [`${path}: not a string`]
    case 'array':
      return Array.isArray(value) ? value.flatMap((v, i) => valueErrors(`${path}[${i}]`, v, schema.items ?? {})) : [`${path}: not an array`]
    case 'object':
      if (typeof value !== 'object' || value === null || Array.isArray(value)) return [`${path}: not an object`]
      return typeof schema.additionalProperties === 'object'
        ? Object.entries(value).flatMap(([k, v]) => valueErrors(`${path}.${k}`, v, schema.additionalProperties as Schema))
        : []
    default:
      return []
  }
}

/** Columns the contract does not read, required ones missing, or a number column that does not parse. */
function dataErrors(data: SqlData, contract: Contract): string[] {
  const errors: string[] = []
  if (data.rows.length === 0) errors.push('no rows')
  if (!contract.inputs.open) {
    const declared = new Map(contract.inputs.columns.map((c) => [c.name, c]))
    for (const name of data.columns) if (!declared.has(name)) errors.push(`column ${name} is not an input`)
    for (const c of contract.inputs.columns) if (!c.optional && !data.columns.includes(c.name)) errors.push(`missing column ${c.name}`)
  }
  for (const row of data.rows) if (row.length !== data.columns.length) errors.push(`row ${JSON.stringify(row)} has the wrong width`)
  const numbers = contract.inputs.columns.filter((c) => c.types.length === 1 && c.types[0] === 'number').map((c) => c.name)
  for (const record of toRecords(data, contract))
    for (const name of numbers) if (name in record && Number.isNaN(record[name])) errors.push(`${name} is not a number`)
  const days = contract.inputs.columns.filter((c) => c.types.length === 1 && c.types[0] === 'day').map((c) => c.name)
  for (const record of toRecords(data, contract))
    for (const name of days) if (name in record && !/^\d{4}-\d{2}-\d{2}$/.test(String(record[name]))) errors.push(`${name} is not YYYY-MM-DD`)
  return errors
}

const entries = Object.entries(widgets)

describe('examples', () => {
  it.each(entries)('%s has at least one', (_name, module) => {
    expect(module.examples.length).toBeGreaterThan(0)
  })

  it.each(entries)('%s: every example fits the contract', (_name, module) => {
    for (const example of module.examples as Example[]) {
      const { contract } = module
      const errors = propErrors(example.props, contract)
      if ('markdown' in example.data) {
        if (!contract.accepts.includes('md')) errors.push('markdown data for an sql component')
      } else {
        if (!contract.accepts.includes('sql')) errors.push('sql data for an md component')
        errors.push(...dataErrors(example.data, contract))
      }
      expect(errors, `${example.title}`).toEqual([])
    }
  })

  it.each(entries)('%s: every example renders', (_name, module) => {
    const Component = module.default
    for (const example of module.examples) {
      const { container, unmount } = render(
        <div style={{ width: 600, height: 400 }}>
          <Component data={example.data} props={example.props} />
        </div>
      )
      expect(container.firstElementChild?.childElementCount, example.title).toBeGreaterThan(0)
      unmount()
    }
  })
})
```

- [ ] **Step 3: Run it to see it fail**

Run: `cd web && npx vitest run src/components/widgets/examples.test.tsx`
Expected: FAIL (TypeScript/`examples` undefined: `Cannot read properties of undefined (reading 'length')`).

- [ ] **Step 4: Write the examples**

In each module, after `contract`, add `export const examples: Example[] = [...]` (import `Example` in the existing `import type { … } from './types'`). Rules for every fixture:

- Realistic web-analytics values for a small site: visitors in the hundreds to low thousands, pages like `/`, `/pricing`, `/docs/install`, referrers like `google.com`, countries as ISO alpha-2, days in September 2026 (`2026-09-01` …), durations in seconds.
- Values are strings, as the server sends them (`'1240'`, not `1240`); empty string means SQL `NULL`.
- Small: ≤ 30 rows except `calendar` (≈ 120 days, generated deterministically — see below) and `heatmap` (≈ 6 × 6).
- Props: the first example uses the props a thoughtful agent would set (e.g. `format`); where a second example is listed below, add it.
- `title` reads like a widget title on a dashboard ("Visitors per day").

Per module (columns in this order):

| Module | Example(s) |
| --- | --- |
| `stat` | "Visitors" `value`,`previous`,`x`: 14 days, `x` days, `previous` = last period's same-day values; props `{ format: 'number', aggregate: 'sum' }`. Second: "Bounce rate" `value`,`previous` one row `'0.42'`,`'0.47'`, props `{ format: 'percent' }` |
| `line` | "Visitors per day" `x`,`y` 14 days, props `{ format: 'number', curve: 'monotone' }`. Second: "Visitors per day by kind" `x`,`series`,`y`, series `web`/`app`, props `{ curve: 'monotone' }` |
| `area` | "Views per day by platform" `x`,`series`,`y` 14 days, series `desktop`/`mobile`/`tablet`, props `{ stacked: true }` |
| `bar` | "Daily active users, new and returning" `x`,`series`,`y` 7 days, series `new`/`returning`, props `{ stacked: true }`. Second: "Top product events" `x`,`y` 6 events (`signup`, `checkout_started`, …), props `{ horizontal: true }` |
| `bar_list` | "Top pages" `label`,`value` 8 paths, descending, props `{ format: 'number' }` |
| `pie` | "Visitors by device" `label`,`value` desktop/mobile/tablet/Other, props `{ donut: true }` |
| `radar` | "Visitors by weekday" `axis`,`series`,`value` Mon…Sun × `this week`/`last week`, props `{}` |
| `radial` | "Signups toward goal" `label`,`value`,`max` 3 rows (`September`, `August`, `July`), props `{}` |
| `scatter` | "Pages: views vs time on page" `x`,`y`,`series`,`size` 12 pages, `series` `docs`/`marketing`, props `{ format: 'duration' }` (y seconds) |
| `funnel` | "Signup funnel" `step`,`value` Visited / Viewed pricing / Started signup / Signed up, props `{ format: 'number' }` |
| `combo` | "Visitors and bounce rate" `x`,`bar`,`line` 14 days, props `{ bar_format: 'number', line_format: 'percent' }` |
| `heatmap` | "Retention by cohort" `x` (cohort day, 6 days), `y` (days since, `'0'`…`'5'` as text), `value` fractions falling from 1 — only cells where cohort day + days since ≤ the last day; props `{ format: 'percent', labels: true }` |
| `calendar` | "Visitors per day" `day`,`value` for 120 days ending `2026-09-27`, built with a deterministic helper in the module file's example section: `Array.from({ length: 120 }, (_, i) => …)` with value `String(200 + ((i * 37) % 90) + (dayOfWeek >= 5 ? -80 : 0))`; days computed with `Date.UTC` + `toISOString().slice(0, 10)`; props `{ format: 'number' }` |
| `map` | "Visitors by country" `country`,`value` 10 countries (`US`, `DE`, `GB`, `FR`, `IN`, `BR`, `CA`, `NL`, `JP`, `AU`), props `{ format: 'number' }` |
| `treemap` | "Browsers and versions" `label`,`value`,`parent` two levels: parents with `parent` `''`, children naming them (check `treemap.tsx` for how parents are expressed and follow it), props `{ format: 'number' }` |
| `table` | "Top referrers" columns `referrer`,`visitors`,`bounce_rate` 6 rows, props `{ formats: { visitors: 'number', bounce_rate: 'percent' }, colorscale: ['visitors'] }` |
| `markdown` | "Notes" data `{ markdown: '## Weekly notes\n\nVisitors are **up 12%** week over week, mostly from `google.com`.\n\n- Pricing page redesign shipped on Sep 22\n- Docs traffic keeps growing' }`, props `{}` |

Before writing a fixture, read that module's component function to confirm which columns it reads and how (e.g. `treemap` parents, `stat` `aggregate`, `heatmap` `y` typing) and follow the component, not this table, if they disagree. Keep each `examples` block after `contract` and before the component function.

- [ ] **Step 5: Run the tests and typecheck**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: all pass (the new file included). If a component renders nothing for its example, fix the fixture, not the test.

- [ ] **Step 6: Confirm the manifest is unchanged**

Run: `cd web && npx tsx scripts/manifest.ts && git status --short ../internal/reporting/ui/components.json`
Expected: no output from `git status` (file unchanged).

- [ ] **Step 7: Commit**

```bash
git add web/src/components/widgets
git commit -m "feat(web): give every component a worked example

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Split the card frame and the grid out of the dashboard

**Files:**
- Create: `web/src/components/WidgetFrame.tsx`, `web/src/components/LayoutGrid.tsx`
- Modify: `web/src/components/WidgetCard.tsx`, `web/src/components/WidgetGrid.tsx`
- Test: `web/src/components/LayoutGrid.test.tsx` (new); existing `WidgetCard.test.tsx`, `pages/Dashboard.test.tsx` must pass unchanged

**Interfaces:**
- Produces:
  - `export default function WidgetFrame(props: { title?: string | null; badge?: ReactNode; actions?: ReactNode; children: ReactNode }): ReactElement` — the `<Card data-slot="widget-card" …>` with the title row (shown when `title` or `badge`), the scrolling body, and `actions` absolutely positioned top-right. When `actions` is given, the title row gets `pr-8` (as today when refreshable).
  - `export interface GridCell { key: string | number; width: number; height: number; node: ReactNode }` and `export default function LayoutGrid(props: { cells: GridCell[] }): ReactElement` — the `data-slot="widget-grid"` 12-column grid with `span()` column spans, `data-span`, and row spans, exactly as `WidgetGrid` renders today.

- [ ] **Step 1: Write the failing test**

`LayoutGrid.test.tsx`:

```tsx
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import LayoutGrid from './LayoutGrid'

describe('LayoutGrid', () => {
  it('places each cell with its row span and content', () => {
    const { container, getByText } = render(
      <LayoutGrid
        cells={[
          { key: 'a', width: 6, height: 8, node: <p>first</p> },
          { key: 'b', width: 12, height: 2, node: <p>second</p> },
        ]}
      />
    )
    const grid = container.querySelector('[data-slot="widget-grid"]')!
    expect(grid.children).toHaveLength(2)
    expect((grid.children[0] as HTMLElement).style.gridRow).toBe('span 8 / span 8')
    expect((grid.children[1] as HTMLElement).style.gridRow).toBe('span 2 / span 2')
    expect(getByText('first')).toBeInTheDocument()
    expect(getByText('second')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/components/LayoutGrid.test.tsx`
Expected: FAIL, cannot resolve `./LayoutGrid`.

- [ ] **Step 3: Create `LayoutGrid.tsx`**

Move the grid markup out of `WidgetGrid.tsx`:

```tsx
import { useRef, type ReactNode } from 'react'
import { useElementWidth } from '@/hooks/use-element-width'
import { span } from '@/lib/grid'

export interface GridCell {
  key: string | number
  /** Columns out of 12 at full width; `span` narrows it on smaller grids. */
  width: number
  /** 40px rows. */
  height: number
  node: ReactNode
}

/**
 * The 12-column grid (D10, D37): 40px rows, 12px gaps, cells in order.
 * Only the column spans adapt to the grid's width; rows keep their height
 * and there is no `dense` packing, so a narrow grid is the same list
 * wrapped sooner.
 */
export default function LayoutGrid({ cells }: { cells: GridCell[] }) {
  const ref = useRef<HTMLDivElement>(null)
  const width = useElementWidth(ref)
  return (
    <div ref={ref} data-slot="widget-grid" className="grid auto-rows-[40px] grid-cols-12 gap-3">
      {cells.map((cell) => {
        const columns = span(cell.width, width)
        return (
          <div
            key={cell.key}
            data-span={columns}
            className="min-w-0"
            style={{ gridColumn: `span ${columns} / span ${columns}`, gridRow: `span ${cell.height} / span ${cell.height}` }}
          >
            {cell.node}
          </div>
        )
      })}
    </div>
  )
}
```

- [ ] **Step 4: Rewrite `WidgetGrid.tsx` on it**

```tsx
import { memo } from 'react'
import type { Widget, WidgetDataQuery } from '@/lib/api'
import LayoutGrid from './LayoutGrid'
import WidgetCard from './WidgetCard'

interface Props {
  widgets: Widget[]
  /** The data query for one widget: what it follows of the page's selection. */
  paramsFor: (widget: Widget) => WidgetDataQuery
  /** Show what is cached, but load nothing (the page is about to change). */
  idle?: boolean
}

/** A dashboard's widgets on the grid, in order. */
function WidgetGrid({ widgets, paramsFor, idle = false }: Props) {
  return (
    <LayoutGrid
      cells={widgets.map((widget) => ({
        key: widget.widget_id,
        width: widget.width,
        height: widget.height,
        node: <WidgetCard widget={widget} params={paramsFor(widget)} idle={idle} />,
      }))}
    />
  )
}

// The page re-renders on a clock (for "data as of" and refresh); the grid
// only needs to when its widgets or their parameters change.
export default memo(WidgetGrid)
```

- [ ] **Step 5: Create `WidgetFrame.tsx` and use it in `WidgetCard.tsx`**

`WidgetFrame.tsx`:

```tsx
import type { ReactNode } from 'react'
import { Card } from '@/components/ui/card'

interface Props {
  title?: string | null
  /** Beside the title, e.g. the "partial" badge. */
  badge?: ReactNode
  /** Top-right controls, e.g. refresh. */
  actions?: ReactNode
  children: ReactNode
}

/** A widget's card: the title row, the body, and controls in the corner. */
export default function WidgetFrame({ title, badge, actions, children }: Props) {
  return (
    <Card
      data-slot="widget-card"
      className="relative h-full min-w-0 gap-1.5 overflow-hidden p-3.5 shadow-[0_1px_2px_rgb(11_31_54/0.04),0_6px_16px_-10px_rgb(11_31_54/0.14)] dark:shadow-none"
    >
      {(title || badge) && (
        <div className={`flex min-h-7 min-w-0 items-center gap-2 ${actions ? 'pr-8' : ''}`}>
          {title && <h3 className="truncate text-sm font-medium text-muted-foreground">{title}</h3>}
          {badge}
        </div>
      )}
      <div className="relative min-h-0 flex-1 overflow-auto">{children}</div>
      {actions && <div className="absolute top-2.5 right-2.5 flex items-center">{actions}</div>}
    </Card>
  )
}
```

In `WidgetCard.tsx`, replace the `<Card …>…</Card>` JSX returned by `WidgetCard` with:

```tsx
    <WidgetFrame
      title={widget.title}
      badge={
        truncated && (
          <Badge variant="outline" className="min-w-0 shrink text-muted-foreground">
            <span className="truncate">partial: narrow the range or group the query</span>
          </Badge>
        )
      }
      actions={
        refreshable && (
          <>
            {answer && query.isError && <StaleWarning error={query.error} />}
            <RefreshButton
              label={`Refresh ${label}`}
              data={answer}
              busy={query.isFetching}
              onRefresh={() => void refreshWidget(client, widget, params).catch(() => {})}
            />
          </>
        )
      }
    >
      {/* the existing body ternary, unchanged: removed ? … : answer ? … : … */}
    </WidgetFrame>
```

`badge`/`actions` receive `false` when their condition fails; `WidgetFrame` treats falsy as absent (`{(title || badge) && …}`, `{actions && …}`). Drop the now-unused `Card` import from `WidgetCard.tsx`; import `WidgetFrame from './WidgetFrame'`.

- [ ] **Step 6: Run everything**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: all pass, including the unchanged `WidgetCard.test.tsx` and `Dashboard.test.tsx`.

- [ ] **Step 7: Commit**

```bash
git add web/src/components
git commit -m "refactor(web): split the widget card frame and the grid from their data

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The components gallery page and its routes

**Files:**
- Create: `web/src/pages/gallery/GalleryLayout.tsx`, `web/src/pages/gallery/ComponentEntry.tsx`, `web/src/pages/gallery/ComponentsGallery.tsx`, `web/src/pages/gallery/ComponentsGallery.test.tsx`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: `widgets` (`@/components/widgets`), `Example`/`WidgetModule`/`InputColumn` (`@/components/widgets/types`), `WidgetFrame`, `LayoutGrid`/`GridCell` (Task 2), `AppShell`/`TopBar` (`@/components/AppShell`), `dashboardsQuery` (`@/lib/queries`).
- Produces:
  - `export default function GalleryLayout(props: { title: string; description: string; sections: { id: string; label: string }[]; children: ReactNode }): ReactElement`
  - `export default function ComponentEntry(props: { name: string; module: WidgetModule }): ReactElement` — renders `<section id={`component-${name}`} aria-labelledby=…>`.
  - `export default function ComponentsGallery(): ReactElement`
  - `export function addWidgetJson(name: string, example: Example): string` (in `ComponentEntry.tsx`)
  - Routes `/gallery/components`, `/gallery` and `/gallery/*` → `<Navigate to="/gallery/components" replace />`.

- [ ] **Step 1: Write the failing tests**

`ComponentsGallery.test.tsx`:

```tsx
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Navigate, Route, Routes } from 'react-router'
import { widgets } from '@/components/widgets'
import { _resetForTests } from '@/lib/auth'
import { renderWithProviders } from '@/test/render'
import { addWidgetJson } from './ComponentEntry'
import ComponentsGallery from './ComponentsGallery'

function renderAt(path: string) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/gallery/components" element={<ComponentsGallery />} />
        <Route path="/gallery" element={<Navigate to="/gallery/components" replace />} />
        <Route path="/gallery/*" element={<Navigate to="/gallery/components" replace />} />
      </Routes>
    </MemoryRouter>
  )
}

beforeEach(() => {
  localStorage.clear()
  _resetForTests()
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify({ dashboards: [], timezone: 'UTC' }), { status: 200 }))
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const names = Object.keys(widgets).sort()

describe('components gallery', () => {
  it('shows a section per component, in name order, each linked from the jump list', () => {
    renderAt('/gallery/components')
    const sections = screen.getAllByRole('region')
    expect(sections.map((s) => s.id)).toEqual(names.map((n) => `component-${n}`))
    const jump = screen.getByRole('navigation', { name: 'Components' })
    for (const name of names) {
      expect(within(jump).getByRole('link', { name })).toHaveAttribute('href', `#component-${name}`)
    }
  })

  it('shows each example in a widget card with its title', () => {
    renderAt('/gallery/components')
    const line = screen.getByRole('region', { name: 'line' })
    for (const example of widgets.line.examples) {
      expect(within(line).getByRole('heading', { name: example.title })).toBeInTheDocument()
    }
    expect(line.querySelectorAll('[data-slot="widget-card"]')).toHaveLength(widgets.line.examples.length)
  })

  it('lists the contract: columns and props', () => {
    renderAt('/gallery/components')
    const radial = screen.getByRole('region', { name: 'radial' })
    expect(within(radial).getByText(widgets.radial.contract.description)).toBeInTheDocument()
    expect(within(radial).getByText('max')).toBeInTheDocument()
    expect(within(radial).getAllByText('optional').length).toBeGreaterThan(0)
    expect(within(radial).getByText('format')).toBeInTheDocument()
  })

  it('copies the add_widget JSON of an example', async () => {
    const user = userEvent.setup()
    renderAt('/gallery/components')
    const bar = screen.getByRole('region', { name: 'bar' })
    await user.click(within(bar).getAllByRole('button', { name: /copy add_widget json/i })[0])
    expect(await navigator.clipboard.readText()).toBe(addWidgetJson('bar', widgets.bar.examples[0]))
    expect(within(bar).getAllByRole('button', { name: /copied/i })).toHaveLength(1)
  })

  it('says so when the clipboard is unavailable, and keeps the JSON readable', async () => {
    const user = userEvent.setup()
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    renderAt('/gallery/components')
    const pie = screen.getByRole('region', { name: 'pie' })
    await user.click(within(pie).getAllByRole('button', { name: /copy add_widget json/i })[0])
    expect(within(pie).getByRole('button', { name: /copy failed/i })).toBeInTheDocument()
    expect(within(pie).getByText(/"component": "pie"/)).toBeInTheDocument()
  })

  it('redirects /gallery and unknown gallery paths to the components page', () => {
    for (const path of ['/gallery', '/gallery/templates']) {
      const { unmount } = renderAt(path)
      expect(screen.getByRole('heading', { level: 1, name: 'Components' })).toBeInTheDocument()
      unmount()
    }
  })
})

describe('addWidgetJson', () => {
  it('is the component and the example props, pretty-printed', () => {
    expect(addWidgetJson('pie', { title: 't', props: { donut: true }, data: { columns: [], rows: [], truncated: false } })).toBe(
      '{\n  "component": "pie",\n  "props": {\n    "donut": true\n  }\n}'
    )
  })
})
```

Notes for the implementer: check `src/test/setup.ts` and `lib/api.ts` for how the dashboards list is fetched and what shape `dashboardsQuery` expects; adjust the stubbed response to that shape if it differs (keep it an empty list). If `userEvent.setup()`'s clipboard stub is replaced by the `defineProperty` in the clipboard-unavailable test, that test must run in isolation from others' clipboard — restore it in `afterEach` (`Object.defineProperty(navigator, 'clipboard', { value: original, configurable: true })` with `original` captured at module load).

- [ ] **Step 2: Run to see them fail**

Run: `cd web && npx vitest run src/pages/gallery`
Expected: FAIL, cannot resolve `./ComponentEntry`.

- [ ] **Step 3: `GalleryLayout.tsx`**

```tsx
import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import AppShell, { TopBar } from '@/components/AppShell'
import { dashboardsQuery } from '@/lib/queries'

interface Props {
  title: string
  description: string
  /** The jump list: one link per section, in page order. */
  sections: { id: string; label: string }[]
  children: ReactNode
}

/** A gallery page: the app shell, a title, a jump list, then the entries. */
export default function GalleryLayout({ title, description, sections, children }: Props) {
  const list = useQuery(dashboardsQuery)
  return (
    <AppShell dashboards={list.data?.dashboards ?? []} currentId={0}>
      <TopBar>
        <span className="text-sm text-muted-foreground">Gallery</span>
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        <header className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
          <p className="max-w-prose text-sm text-muted-foreground">{description}</p>
          <nav aria-label={title} className="flex flex-wrap gap-1.5">
            {sections.map((s) => (
              <a
                key={s.id}
                href={`#${s.id}`}
                className="rounded-md border border-border/70 bg-background/60 px-2 py-0.5 font-mono text-xs hover:bg-accent"
              >
                {s.label}
              </a>
            ))}
          </nav>
        </header>
        {children}
      </div>
    </AppShell>
  )
}
```

`currentId={0}` matches no dashboard, so no dashboard entry is highlighted.

- [ ] **Step 4: `ComponentEntry.tsx`**

```tsx
import { useState } from 'react'
import { CheckIcon, ChevronDownIcon, CopyIcon, TriangleAlertIcon } from 'lucide-react'
import LayoutGrid from '@/components/LayoutGrid'
import WidgetFrame from '@/components/WidgetFrame'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import type { Example, WidgetModule } from '@/components/widgets/types'

/** What to paste into a request to an agent: `add_widget`'s component and props. */
export function addWidgetJson(name: string, example: Example): string {
  return JSON.stringify({ component: name, props: example.props }, null, 2)
}

type CopyState = 'idle' | 'copied' | 'failed'

/** A button that copies `text`; plain http has no clipboard, so failing is a state, not an error. */
function CopyButton({ text, label }: { text: string; label: string }) {
  const [state, setState] = useState<CopyState>('idle')
  const copy = async () => {
    try {
      if (!navigator.clipboard) throw new Error('no clipboard')
      await navigator.clipboard.writeText(text)
      setState('copied')
    } catch {
      setState('failed')
    }
    setTimeout(() => setState('idle'), 2000)
  }
  return (
    <Button variant="outline" size="sm" className="h-7" onClick={() => void copy()}>
      {state === 'copied' ? <CheckIcon /> : state === 'failed' ? <TriangleAlertIcon /> : <CopyIcon />}
      {state === 'copied' ? 'Copied' : state === 'failed' ? 'Copy failed' : label}
    </Button>
  )
}

interface PropSchema {
  type?: string
  enum?: unknown[]
}

function propSummary(schema: PropSchema): string {
  if (schema.enum) return schema.enum.map(String).join(' · ')
  return schema.type ?? 'any'
}

/** One component: what it is for, what it reads and takes, and its examples as they render. */
export default function ComponentEntry({ name, module }: { name: string; module: WidgetModule }) {
  const { contract, examples } = module
  const props = Object.entries((contract.props as { properties?: Record<string, PropSchema> }).properties ?? {})
  const headingId = `component-${name}-heading`
  return (
    <section id={`component-${name}`} aria-labelledby={headingId} className="flex scroll-mt-16 flex-col gap-3">
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <h2 id={headingId} className="font-mono text-base font-semibold">
            {name}
          </h2>
          <Badge variant="outline" className="text-muted-foreground">
            {contract.defaultWidth} × {contract.defaultHeight}
          </Badge>
          <CopyButton text={name} label="Copy name" />
        </div>
        <p className="max-w-prose text-sm text-muted-foreground">{contract.description}</p>
        <Collapsible>
          <CollapsibleTrigger asChild>
            <Button variant="link" size="sm" className="group h-6 px-0 text-muted-foreground">
              Contract
              <ChevronDownIcon className="transition-transform group-data-[state=open]:rotate-180" />
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent forceMount className="data-[state=closed]:hidden">
            <dl className="grid max-w-full grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
              <dt className="text-muted-foreground">Inputs</dt>
              <dd className="min-w-0 break-words">
                {contract.inputs.open
                  ? 'any columns, shown in query order'
                  : contract.inputs.columns.map((c) => (
                      <span key={c.name} className="mr-3 inline-flex gap-1">
                        <code>{c.name}</code>
                        <span className="text-muted-foreground">{c.types.join(' or ')}</span>
                        {c.optional && <span className="text-muted-foreground italic">optional</span>}
                      </span>
                    ))}
              </dd>
              <dt className="text-muted-foreground">Props</dt>
              <dd className="min-w-0 break-words">
                {props.length === 0
                  ? 'none'
                  : props.map(([key, schema]) => (
                      <span key={key} className="mr-3 inline-flex gap-1">
                        <code>{key}</code>
                        <span className="text-muted-foreground">{propSummary(schema)}</span>
                      </span>
                    ))}
              </dd>
              <dt className="text-muted-foreground">Accepts</dt>
              <dd>{contract.accepts.join(', ')}</dd>
            </dl>
          </CollapsibleContent>
        </Collapsible>
      </div>
      <LayoutGrid
        cells={examples.map((example, i) => {
          const Component = module.default
          const json = addWidgetJson(name, example)
          return {
            key: i,
            width: contract.defaultWidth,
            height: contract.defaultHeight + 3,
            node: (
              <div className="flex h-full min-w-0 flex-col gap-2">
                <div className="min-h-0 flex-1">
                  <WidgetFrame title={example.title}>
                    <Component data={example.data} props={example.props} />
                  </WidgetFrame>
                </div>
                <div className="flex min-w-0 items-start gap-2">
                  <CopyButton text={json} label="Copy add_widget JSON" />
                  <pre className="max-h-16 min-w-0 flex-1 overflow-y-auto rounded-md bg-muted px-2 py-1 font-mono text-xs break-all whitespace-pre-wrap select-all">
                    {json}
                  </pre>
                </div>
              </div>
            ),
          }
        })}
      />
    </section>
  )
}
```

The extra 3 rows hold the copy row under the card, so the card itself keeps the component's default height (`height: defaultHeight + 3`, with the card in the flexible part — check in the browser in Task 5 that the card is still `defaultHeight` rows tall; adjust the extra rows, not the default, if the copy row needs more or less). The `<pre>` shows the same pretty-printed JSON the button copies, so it stays visible and selectable when copying fails (the failure test matches `/"component": "pie"/` in it).

- [ ] **Step 5: `ComponentsGallery.tsx`**

```tsx
import { widgets } from '@/components/widgets'
import ComponentEntry from './ComponentEntry'
import GalleryLayout from './GalleryLayout'

const names = Object.keys(widgets).sort()

/** `/gallery/components`: every component, rendered from its examples, to point an agent at by name. */
export default function ComponentsGallery() {
  return (
    <GalleryLayout
      title="Components"
      description="Every component a dashboard widget can use, drawn with sample data. Ask your agent for one by name, or paste its add_widget JSON."
      sections={names.map((name) => ({ id: `component-${name}`, label: name }))}
    >
      {names.map((name) => (
        <ComponentEntry key={name} name={name} module={widgets[name]} />
      ))}
    </GalleryLayout>
  )
}
```

The jump list's `<nav aria-label={title}>` gives it the accessible name "Components" the test looks for.

- [ ] **Step 6: Routes in `App.tsx`**

Add imports `Navigate` (from `react-router`) and `ComponentsGallery from '@/pages/gallery/ComponentsGallery'`, and inside `<Routes>` after the dashboards route:

```tsx
          <Route path="/gallery/components" element={<ComponentsGallery />} />
          <Route path="/gallery" element={<Navigate to="/gallery/components" replace />} />
          <Route path="/gallery/*" element={<Navigate to="/gallery/components" replace />} />
```

The gallery is not wrapped in `OnlineOnly`: it reads no data, and offline the sidebar simply lists nothing.

- [ ] **Step 7: Run everything**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add web/src/pages/gallery web/src/App.tsx
git commit -m "feat(web): show every component at /app/gallery/components

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Gallery in the sidebar, and the docs line

**Files:**
- Modify: `web/src/components/AppSidebar.tsx`, `web/src/components/AppSidebar.test.tsx`
- Modify: `docs/reporting.md`

**Interfaces:**
- Consumes: route `/gallery/components` (Task 3).

- [ ] **Step 1: Write the failing test**

Append to `AppSidebar.test.tsx` (import `render` helpers as already done there):

```tsx
describe('AppSidebar gallery', () => {
  it('links to the components gallery, active on a gallery page', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/gallery/components']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} />
        </SidebarProvider>
      </MemoryRouter>
    )
    const link = screen.getByRole('link', { name: 'Components' })
    expect(link).toHaveAttribute('href', '/gallery/components')
    expect(link).toHaveAttribute('data-active', 'true')
    expect(screen.getByText('Gallery')).toBeInTheDocument()
  })

  it('is not active on a dashboard', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/dashboards/1']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={1} />
        </SidebarProvider>
      </MemoryRouter>
    )
    expect(screen.getByRole('link', { name: 'Components' })).not.toHaveAttribute('data-active', 'true')
  })
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/components/AppSidebar.test.tsx`
Expected: FAIL, no link named "Components".

- [ ] **Step 3: Add the group**

In `AppSidebar.tsx`: import `useLocation` from `react-router` and `ShapesIcon` from `lucide-react`; in the component, `const { pathname } = useLocation()`; after the "Yours" `SidebarGroup`, add:

```tsx
        <SidebarGroup>
          <SidebarGroupLabel className="text-sidebar-foreground/60">Gallery</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem>
                <SidebarMenuButton
                  asChild
                  isActive={pathname.startsWith('/gallery/components')}
                  tooltip="Components"
                  className={item}
                >
                  <Link to="/gallery/components" onClick={close}>
                    <ShapesIcon />
                    <span>Components</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
```

If `ShapesIcon` does not exist in the installed `lucide-react`, use `LayoutGridIcon`. Update the component's doc comment to mention the Gallery group.

- [ ] **Step 4: The docs line**

In `docs/reporting.md`, in `## Components`, after the paragraph starting "`list_components` is the authority", add:

```markdown
The dashboard app draws every component with sample data at
`/app/gallery/components`, with each one's contract and a copyable
`add_widget` snippet: point the owner there to pick one by name.
```

- [ ] **Step 5: Run the checks**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`, then from the repo root `PATH=$PATH:/usr/local/go/bin go test ./internal/api/ -run DocsSync`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/AppSidebar.tsx web/src/components/AppSidebar.test.tsx docs/reporting.md
git commit -m "feat(web): add the components gallery to the sidebar

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: End-to-end: the gallery in a real browser

**Files:**
- Create: `web/e2e/gallery.spec.ts`

**Interfaces:**
- Consumes: the login flow of `web/e2e/app.spec.ts` (copy its `login` helper shape; do not import from a spec file), route and sidebar entry (Tasks 3–4).

- [ ] **Step 1: Write the spec**

```ts
import { expect, test, type Page } from '@playwright/test'
import { widgets } from '../src/components/widgets'

// Matches web/e2e/serve.sh's API_AUTH_DSN.
const PASSWORD = 'e2e-pass'
const NAMES = Object.keys(widgets).sort()

/** Signs in through the password page from wherever the app bounced to it. */
async function signIn(page: Page): Promise<void> {
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
}

test('a signed-out deep link comes back to the gallery after sign-in', async ({ page }) => {
  await page.goto('/app/gallery/components')
  await signIn(page)
  await page.waitForURL(/\/app\/gallery\/components/)
  await expect(page.getByRole('heading', { level: 1, name: 'Components' })).toBeVisible()
})

test('/app/gallery redirects to the components', async ({ page }) => {
  await page.goto('/app/gallery')
  await signIn(page)
  await page.waitForURL(/\/app\/gallery\/components/)
})

for (const scheme of ['light', 'dark'] as const) {
  test(`every component renders (${scheme})`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme: scheme })
    await page.goto('/app/gallery/components')
    await signIn(page)
    await page.waitForURL(/\/app\/gallery\/components/)
    for (const name of NAMES) {
      const section = page.locator(`#component-${name}`)
      await section.scrollIntoViewIfNeeded()
      const card = section.locator('[data-slot="widget-card"]').first()
      await expect(card, name).toBeVisible()
      // Not blank: a chart, a table, a list or text inside the card body.
      await expect(card.locator('svg, table, li, p, h2').first(), name).toBeVisible()
    }
    await testInfo.attach(`gallery-${scheme}.png`, {
      body: await page.screenshot({ fullPage: true }),
      contentType: 'image/png',
    })
  })
}

test('fits a phone without sideways scrolling', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/app/gallery/components')
  await signIn(page)
  await page.waitForURL(/\/app\/gallery\/components/)
  await page.locator('#component-table').getByRole('button', { name: 'Contract' }).click()
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})

test('the sidebar opens the gallery', async ({ page }) => {
  await page.goto('/app/')
  await signIn(page)
  await page.waitForURL(/\/app\/dashboards\/\d+/)
  await page.getByRole('link', { name: 'Components' }).click()
  await page.waitForURL(/\/app\/gallery\/components/)
})
```

If importing `../src/components/widgets` into the Playwright spec fails (it pulls React/Recharts modules into Node), replace `NAMES` with a literal sorted array of the 17 names copied from `web/src/components/widgets/index.ts` and add a one-line comment naming that file as the source.

- [ ] **Step 2: Run the e2e suite**

Run: `cd web && npx playwright test e2e/gallery.spec.ts` (Chromium is preinstalled; `PLAYWRIGHT_BROWSERS_PATH` is set; do not run `playwright install`). The web server builds the binary via `e2e/serve.sh` and needs Go (`/usr/local/go/bin`, which the script adds to PATH).
Expected: all pass. If a card is blank or the phone test overflows, fix the page (Task 3 code), not the assertion.

- [ ] **Step 3: Look at the screenshots**

Open the attachments under `web/test-results/` (or re-run with `--reporter=list` and find the attached PNGs) and check with the Read tool that: each card is the component's default height, the copy row sits under the card, dark mode is legible. Fix anything visibly broken in Task 3's files.

- [ ] **Step 4: Run the full e2e suite and unit tests once**

Run: `cd web && npx playwright test && npx vitest run`
Expected: all pass (the existing `app.spec.ts` must be unaffected).

- [ ] **Step 5: Commit**

```bash
git add web/e2e/gallery.spec.ts
git commit -m "test(web): cover the components gallery end to end

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Finish

- [ ] From the repo root: `PATH=$PATH:/usr/local/go/bin make check` (vet, coverage, restore test; builds the UI). Expected: pass, and `git status` shows `internal/reporting/ui/components.json` unchanged.
- [ ] Push the branch and open a draft PR `feat(web): show every component at /app/gallery/components` whose body leads with the problems from the spec.
