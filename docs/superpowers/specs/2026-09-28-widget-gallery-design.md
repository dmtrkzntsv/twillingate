# Widget gallery and curated chart styles

Status: draft
Date: 2026-09-28

## Sequencing

Two PRs, gallery first:

1. **Gallery** — `feat(web)`: a page at `/app/gallery/components` that
   renders every component from built-in fixtures. No contract change,
   nothing breaking.
2. **Curation** — `feat(reporting)!`: one fixed, shadcn-derived look per
   chart component, style props removed, meaning-changing variants split
   into their own components, existing widgets rewritten. Its starting
   point is [Curation](#curation-pr-2) below; each look is settled on the
   gallery before the contracts change.

## Problem

- **Components have a look nobody chose.** The six chart components
  (`area`, `bar`, `line`, `pie`, `radar`, `radial`) and their shared
  tooltip render close to Recharts defaults. shadcn's chart catalogue
  (ui.shadcn.com/charts: area, bar, line, pie, radar, radial, tooltip —
  70 variants) shows what they could look like.
- **Style is a knob the agent turns, badly.** `curve`, `stacked`,
  `horizontal` and `donut` are props an agent must know to set. Some are
  cosmetic (`curve`), some change what the chart means (`stacked` turns
  "each series" into "parts of a total"); the contract treats both the
  same.
- **There is nowhere to see the components.** An owner who wants "that
  stacked bar chart" has to know the component name and props from
  `docs/reporting.md` or `list_components`, and has never seen what they
  render. Tuning a look means building a dashboard with data to see it.

## Decisions

### Gallery (PR 1)

1. **Route: `/app/gallery/components`.** `/app/gallery` redirects there
   while components are the only gallery; dashboard templates are expected
   later at `/app/gallery/templates`, and `/app/gallery` becomes an index
   of the two then. The server needs no change: `internal/reporting/ui.go`
   already serves `index.html` for any `/app/` path that is not a file.
2. **Sidebar: a "Gallery" group** under "Yours", holding one entry,
   "Components". Templates join it later.
3. **Fixtures, not live data.** Each widget module exports `examples`
   next to `contract`:

   ```ts
   export const examples: Example[] = [
     { title: 'Visitors per day', props: { format: 'number' }, data: { columns: [...], rows: [...], truncated: false } },
   ]
   ```

   `data` is the same `SqlData` (or `MarkdownData`) the server returns, so
   the gallery renders through the exact code path a dashboard uses. Most
   components have one example; a second where it shows something the
   first cannot (`line` with and without `series`). Rows are realistic
   analytics values (visitors, pages, countries), deterministic, and small
   enough to read. The gallery works on an empty install and looks the
   same for everyone, which is what makes it the reference for tuning.
4. **Layout mirrors a dashboard.** Examples render in the dashboard's own
   widget card on the 12-column grid at the component's default width ×
   height, so the gallery shows what a dashboard shows. A jump list of
   component names sits at the top.
5. **Each card carries the contract.** Under the card: the component name
   (copyable), its `description`, a collapsible contract (input columns
   with types and optional marks; props with their allowed values; default
   size) and a **Copy `add_widget` JSON** button yielding
   `{"component": "<name>", "props": {…}}` with the example's props — the
   thing to paste into a request to an agent.
6. **The gallery shell is generic.** The page layout (jump list, grid,
   card chrome with copy actions) is a gallery layout the components page
   fills; a templates page can reuse it with a dashboard preview instead
   of one widget. Only what the components page needs is built now.
7. **Theme follows the app.** Light and dark both render; no gallery-only
   theme switch.
8. **Access is the app's.** The page sits behind the same sign-in as the
   dashboards (the sidebar lists them); it reads no widget data.
9. **Agents keep `list_components` as the authority.** `docs/reporting.md`
   gains one line pointing owners at `/app/gallery/components`; nothing is
   served over MCP.

### Curation (PR 2)

10. **A component answers one question and has one look.** Its props are
    content only (`format`, and a caption where a component prints one).
    Cosmetic variation is chosen once, here; a variation that changes what
    the chart means is a separate component. A few touches follow the
    data automatically (below) rather than a prop.
11. **The chart components** (the shadcn variant each look comes from in
    brackets):

    | Component | Look | Replaces |
    | --- | --- | --- |
    | `line` | smooth 2px lines, no dots [line-default]; dots when ≤ 12 points [line-dots]; legend with `series` | `line` (drops `curve`) |
    | `area` | vertical gradient fill [area-gradient]; overlapping series at lighter fill | `area` without `stacked` |
    | `area_stacked` | stacked gradient, legend, Total row in the tooltip | `area` + `stacked` |
    | `area_share` | 100 % stacked [area-stacked-expand], Y axis in percent: each series' share over time | new |
    | `bar` | rounded bars, grouped by `series`; values printed above when one series and ≤ 12 bars [bar-label]; negative bars in a second colour [bar-negative] | `bar` |
    | `bar_stacked` | stacked, only the top segment rounded [bar-stacked], legend, Total row in the tooltip | `bar` + `stacked` |
    | `bar_horizontal` | name inside the bar, value at its end, no axes [bar-label-custom] | `bar` + `horizontal` |
    | `pie` | slices separated by a background-coloured gap, legend grid beneath [pie-legend], no slice labels | `pie` |
    | `donut` | donut with the formatted total centred [pie-donut-text] | `pie` + `donut` |
    | `radar` | circular grid, vertex dots [radar-grid-circle]; one series filled, several as outlines over a light fill | `radar` |
    | `radial` | concentric rings, name at each ring's start, track behind [radial-label] | `radial` |
    | `gauge` | one `value` toward `max` on a semicircle, big number centred [radial-text / radial-stacked]; the arc is the value's share of `max`, not a fixed angle | new |

    Columns stay as they are for every renamed component (`bar_stacked`
    and `bar_horizontal` read `x`, `y`, optional `series` like `bar`), so
    no widget's SQL changes. `gauge` reads `value` and `max`, one row.
12. **One house tooltip** [tooltip page]: values through `format`; day
    labels read `Sep 28, 2026` [tooltip-label-formatter]; a line indicator
    on `line`/`area*`, a dot elsewhere; a Total row on the stacked
    components [tooltip-advanced]; no header on `pie`, `donut`, `radial`,
    `gauge`.
13. **Deliberately not taken:** interactive variants (series tabs with
    totals, time-range select — the dashboard's switchers own the range);
    an active or highlighted slice/bar (no selection concept); icon
    legends (no icon data); the two-ring pie; custom radar ticks and the
    radius axis; `linear` and `step` curves.
14. **Existing widgets are rewritten on upgrade**, by the next free
    migration (022 at the time of writing):
    - `bar` + `stacked: true` → `bar_stacked` (also when `horizontal`:
      stacking is meaning, orientation is style);
    - `bar` + `horizontal: true` → `bar_horizontal`;
    - `area` + `stacked: true` → `area_stacked`;
    - `pie` + `donut: true` → `donut`;
    - `curve`, `stacked`, `horizontal`, `donut` removed from every
      widget's props.

    `widgets.component` references `components(name)`, and component rows
    come from the manifest sync, so the rewrite must run where the new
    names exist: either the migration inserts placeholder rows the sync
    then overwrites, or the sync applies a rename table. The plan picks
    one after reading the sync. The two system widgets with
    `stacked: true` (`users/daily-active-users.json`,
    `groups/daily-active-groups.json`) change their JSON to `bar_stacked`.
15. **Breaking, pre-1.0 minor.** An agent sending a removed prop is
    refused by the props schema; the release notes name the renames. Ships
    as `feat(reporting)!` with a `deploy/UPGRADES.md` entry.

## Documentation

- PR 1: one line in `docs/reporting.md` naming `/app/gallery/components`.
- PR 2: the component table and one worked example per new component in
  `docs/reporting.md` (bound by `internal/api/docs_sync_test.go`), the
  regenerated `internal/reporting/ui/components.json`, and
  `deploy/UPGRADES.md` for the widget rewrite.

## Testing

- **Fixtures match contracts (vitest):** every component in
  `widgets/index.ts` has at least one example; each example's data passes
  `toRecords` against the component's own contract, and its props use only
  the schema's properties with allowed values (the Go side validates the
  same schema; the web app has no JSON Schema validator and gets none). A fixture cannot drift from its contract.
- **Every example renders (vitest):** each renders without throwing and
  produces output.
- **Gallery page (vitest):** lists every component, the jump list links to
  each, the copy button writes the expected `add_widget` JSON.
- **Gallery e2e (Playwright):** `/app/gallery/components` loads behind
  sign-in, every component's card renders a chart, `/app/gallery`
  redirects; a full-page screenshot in light and dark is kept as a test
  artifact for visual review.
- **PR 2 migration (Go):** a widget of each rewritten shape comes out with
  the new component and cleaned props; widgets with no style props are
  untouched; `TestSystemDashboards` passes with the edited system JSON.

## Out of scope

- The templates gallery (only the route is reserved).
- Live data in the gallery.
- Per-widget or per-dashboard style settings.
- Components other than the chart types above (`stat`, `table`, `map`,
  … keep their look; the gallery shows them as they are).
