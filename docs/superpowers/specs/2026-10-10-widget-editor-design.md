# Widget editor

Status: draft
Date: 2026-10-10

## Problem

- **Only an agent can change what a dashboard shows.** The console arranges
  widgets (#157) but cannot add one, change its SQL, visualization or
  props, rename it or archive it. Every REST write for that already exists
  (`add_widget`, `update_widget`, `archive_widget`, `restore_widget`,
  `create_dashboard`); the web app calls none of them.
- **Widget SQL cannot be tried before saving.** `POST /api/query` binds no
  parameters, so SQL that uses `:project`, `:from` or `:to` fails there,
  and it does not check the result against a component. The first place
  you learn the SQL does not fit `line` is the refusal on save.
- **A widget whose component was removed cannot be repaired in one call.**
  `update_widget` refuses title, props or source on it even when a new
  component comes in the same call.
- **A new dashboard cannot be made from the console.**

The goal is adjusting the user's own dashboards from the console; creating
one follows the same path, so it comes along. System dashboards stay
read-only, as the server already enforces.

## Decisions

### Server

- **D1. `POST /api/widgets/preview`, REST only.** Not an MCP tool and not in
  `tools/list`: agents call `add_widget`, which validates (#118 rejected a
  dry-run tool for agents). The console needs rows before saving, which is
  a different need. Same console auth as every route.
  Body: `{component, props?, source: {type: "sql", content}, project_id?,
  from?, to?}`. It saves nothing, caches nothing, writes no audit row.
- **D2. Refusals with no rows are 400s, the same sentences as `add_widget`.**
  In order: unknown component; a component that does not accept `sql`
  (markdown is refused: the console renders it locally); a `readsql.Check`
  refusal or a parameter other than `:project`, `:from`, `:to`; SQL using
  `:project` without `project_id`, or `:from`/`:to` without a valid range
  (the `widget_data` rules: from ≤ to, at most 365 days, from not after
  today, a future `to` clamped); a SQLite error; a timeout.
- **D3. The run binds the given values, not sample ones.** `db.Query` on the
  console handle (read-only, `submissions` refused), capped at
  `CONSOLE_QUERY_MAX_ROWS`, timed by `CONSOLE_QUERY_TIMEOUT`.
- **D4. Fit checks do not stop the rows.** After the run: props against the
  component's schema (`checkProps`), then `checkColumns`, then `checkRows`
  over the returned rows. The first failure is `problem`; the rows come back
  anyway, so the console shows the table and why the chart will not draw.
- **D5. Response (200):** `{columns, rows, truncated, problem?, elapsed_ms}`.
  `columns` and `rows` are the shape `widget_data` returns for SQL, so the
  client renders the real component from them unchanged.
- **D6. One rule set.** `reporting.Service.Preview` lives in
  `internal/reporting/preview.go` and reuses `checkProps`, `checkColumns`,
  `checkRows`, `checkRange` and the parameter checks of `sqlSource`; the
  parameter binding `Load` does is shared, not copied.
- **D7. `update_widget` repairs a removed component in one call.** The
  refusal of title, props or source on a widget whose component was removed
  applies only when no component is given; with one, the whole widget is
  validated as usual. Title, props or source alone are still refused.

### Console

- **D8. Entry points.** Only where the page may arrange widgets (#157's
  `arrangeable`: a live user dashboard, not in reporting dev), on the
  dashboard page and on a project tab showing it:
  - an **Add widget** button in the dashboard header beside the switchers,
    and in the empty state in place of "ask your agent to add some";
  - the widget card's "⋯" menu becomes **Edit…**, **Rename**, then
    Share… and Download PNG, a separator, **Archive**. It still takes one
    top-right slot. System dashboards keep today's menu.
- **D9. The editor is one full-screen modal for add and edit.** A header
  bar with the heading, Cancel and Save. From 1024px, two halves, each
  scrolling on its own: the left holds the preview (a widget card at the
  widget's height, its title row being the title field), then the
  visualization select with a one-line hint of what the component needs
  (from the contract's `inputs`, e.g. "needs: day, ≥1 number"), then the
  props form; the right holds the SQL editor, Run query with the run's
  status, and the rows as a table filling what is left. Phone: the same
  blocks stacked, the whole scrolling; nothing scrolls sideways at 360px,
  the result table and chart scroll inside their own box.
- **D10. Visualization select** lists every component with its icon. Add
  starts with none chosen; Save is disabled until one is. Switching keeps
  the props whose key and value the new schema accepts and drops the rest;
  keeps the SQL between SQL components; switches the editor to markdown for
  a component that accepts only `md`.
- **D11. Props form generated from the contract** already in the web bundle:
  an enum is a select with a "default" (unset) option; a boolean a switch;
  an object whose values are an enum (table `formats`) is column → value
  rows, column names suggested from the last run; anything else (table
  `colorscale`) a JSON field for that prop, bad JSON marked inline and
  blocking Save. "Edit props as JSON" flips the whole form to one JSON
  editor.
- **D12. SQL editor: CodeMirror 6**, SQLite dialect, lazy-loaded with the
  modal. Autocomplete of view and column names from `GET /api/schema/views`
  (the lines `name(col, …)` are parsed; a test pins that every view
  parses), and of `:project`, `:from`, `:to`. ⌘/Ctrl+Enter runs.
- **D13. Run query** calls the preview route. Project and range controls
  show only when the SQL uses `:project` or `:from`/`:to`; they default to
  the dashboard's current values, else the first live project in the
  projects list and the last 7 days. The preview needs no click: an edit
  runs the stored query as it opens, and once anything has run, a change
  of visualization, props, project or range runs it again after 400ms.
  Typing SQL never runs it, and no automatic run reads SQL typed since the
  last run (⌘/Ctrl+Enter or Run does). The left half draws the chosen
  component from the rows; the right shows the rows bare, with row count,
  elapsed ms and truncated note beside Run. `problem` is a warning under
  the preview; a 400 shows its message in place of the rows. Editing the
  SQL after a run dims the results and marks them "Changed since last
  run"; editing the component or props redraws the preview from the same
  rows at once, dimmed and without `problem` (it was checked against the
  run's component) until the run that follows. No rows reads "The query
  returned no rows". A component that throws on the rows shows "This
  visualization can't draw these rows" in the preview, never taking the
  console down. Running is never required to save.
- **D14. Save.** Add: `add_widget` at the end of the dashboard, with the
  component's default size (resizing is on the grid). Edit: `update_widget`
  with only the fields that changed. A refusal shows as a banner in the
  modal, which keeps the input. Success closes the modal and refetches
  `['dashboard', id]`. Closing with unsaved changes asks "Discard changes?".
- **D15. Markdown widgets**: the right half is a markdown editor, the
  left previews the rendered text, live; no Run, no project or range.
- **D16. Removed component.** Edit opens with the visualization empty;
  saving sends the new component with the rest (D7).
- **D17. Rename** turns the card title into an inline field (the
  `ProjectName` pattern): Enter or ✓ saves `update_widget {title}`; Esc, ×,
  or blur without a change cancels. An empty field cancels; clearing a title
  is a modal edit. `name`, the agents' handle, never changes. A change of
  only `title` and `name` runs no query (as layout changes do), so a widget
  whose SQL broke can still be renamed.
- **D18. Archive** calls `archive_widget` at once, refetches the dashboard,
  and toasts "Widget archived" with Undo (`restore_widget`), as dashboards
  do.
- **D19. Archive page: "Archived widgets".** `list_widgets` filtered to
  `archived_at`, grouped under their dashboard's title, each with Restore.
  Widgets of an archived dashboard are left out (they come back with it).
  Hidden when empty.
- **D20. New dashboard from a project.** The project "+" dialog gets a
  "New dashboard…" row first: a title field (2 characters at least, the
  server's rule) and Create, which calls `create_dashboard {title}` (its own
  group), then `add_project_tab`, then opens the new tab, whose empty state
  offers Add widget. Two calls, not one: if the second fails a toast says
  the dashboard was made but not added; it is in the sidebar like any user
  dashboard. The sidebar and the tab menu get no "new" entry.

## Out of scope

- Width and height in the modal (the grid resizes, #157).
- Editing a widget's `name`.
- New dashboards from the sidebar or the tab menu.
- A `project_id` on `create_dashboard`.
- Restoring a single widget of an archived dashboard.

## Testing

- Go, on a migrated copy from `storetest`: preview binds the given values;
  column and row mismatches return rows and `problem`; bad props give
  `problem`; missing project or range, a disallowed parameter, a SQLite
  error, a timeout, a markdown source and an `md`-only component are 400s;
  truncation is flagged. `UpdateWidget` on a removed component: component
  with title/props/source accepted and validated; without a component still
  refused. REST: auth, 200 and 400 shapes, absent from `tools/list`;
  `docs_sync_test` passes.
- vitest: schema-text parsing covers every view; the props form for every
  component's contract, `formats` rows, the `colorscale` JSON field; props
  carried over on a component switch; editor flows against a mocked API
  (run, problem, 400, changed since last run, add vs update with only
  changed fields, refusal banner, discard confirm); inline rename; archive
  with Undo; the Archive page widgets section; the "New dashboard…" path.
- Playwright: from a project "+", create a dashboard, add a line widget,
  run, see the preview, save, edit the SQL, rename, archive, undo, archive
  again, restore from the Archive page. `phone.spec.ts` opens the editor at
  360px; `cursor.spec.ts` covers the new controls (all buttons, links or
  `role="button"`).

## Docs

`docs/reporting.md`: the preview route in the REST table ("console only;
agents call add_widget") and the `update_widget` rule on removed
components. `web/package.json` stops describing the app as read-only.
