# Project landing

Status: draft
Date: 2026-10-08

## Problem

- **A project opens on its configuration.** The sidebar, the project cards
  and `/projects/:id` all land on Setup (`ProjectIndex`,
  `SidebarProjects.tsx`, `ProjectCard.tsx`). Most visits are to read the
  data, so every visit starts with a click away from the page shown.
- **The tab row says "configuration first".** Setup and Forms are the
  first two tabs, ahead of every dashboard (`ProjectTabBar.tsx`). Setup is
  a destination, not something you browse past.
- **Built-in tabs can't be reordered.** They keep the release's order, and
  only your own tabs move (`MoveProjectTab` refuses a built-in). If you
  look at Retention more than at Views, you can't put it first.
- **New submissions are invisible.** Nothing records which submissions
  you have seen, so the only way to notice a lead is to open Forms and
  read the dates.

## Decisions

- **D1. One order for all of a project's tabs.** Built-in and your own
  tabs are ordered together by `project_tabs.sort_key`; any tab can be
  dragged anywhere.
  - `shownTabs` sorts every row by its own `sort_key` (ties by dashboard
    id), with no built-ins-first rule. `dashboards.sort_key` no longer
    decides a project's tab order.
  - `move_project_tab` accepts a built-in. `after` is 0 (first) or any
    live tab of the project. The refusal "built-in tabs keep the
    release's order" goes away.
  - `add_project_tab` accepts `after` for a built-in too. Without
    `after`, any tab goes last. The refusal "a built-in goes back to its
    own place" goes away.
  - `ownTabKey` becomes `tabKey` over all rows of the project. It keeps
    the respread for neighbours that share a key.
  - A built-in that a release adds goes last on every project
    (`project_tabs_new_builtin` gives it the project's largest key with
    `V` appended, which is a valid fractional key sorting after it; no
    tabs gives `a0`). A new project still gets the built-ins in release
    order (`project_tabs_new_project` is unchanged: its keys are
    `dashboards.sort_key`, which are distinct among built-ins).
  - This relaxes two refusals and removes no input or output, so it is
    `feat(reporting)` without `!`.

- **D2. Migration 037 rewrites every project's keys into today's order.**
  Built-ins first by `dashboards.sort_key`, then your own by their row
  key, archived user rows kept at their place (as `shownTabs` keeps
  them). New keys are `b` followed by the two-digit base-62 rank
  (`b00`, `b01`, …), valid fractional keys for up to 3,844 tabs on one
  project. Nothing moves on screen on upgrade day.

- **D3. A project opens on the last tab you used there, else its first
  tab, else Settings.**
  - The last tab is kept in the browser, per project:
    localStorage key `twillingate.project.last_tab`, a JSON object from project id
    to dashboard id, read and written through `useStoredState`'s
    try/catch.
  - Only dashboard tabs are remembered. Opening Forms or Settings leaves
    it as it was.
  - It is honoured only while that dashboard is a live tab of the
    project (in `projectTabs`); otherwise the first tab is used. With no
    tabs at all, the project opens on Settings.
  - One helper, `landingPath(projectId, tabs, search)`, is used by
    `ProjectIndex`, the sidebar project links and the project cards. The
    sidebar and cards link to `/projects/:id` (keeping the range on the
    project's own pages, as today), and `ProjectIndex` decides once the
    tabs are loaded.
  - Per device by design: the phone and the laptop can open different
    tabs.

- **D4. Setup becomes Settings, behind a gear.**
  - It leaves the tab row. A gear button on the right of the tab row,
    outside the scrolling tabs, opens it; tooltip and aria-label
    "Settings". Its content is unchanged.
  - The route is `/projects/:id/settings`. `/projects/:id/setup` redirects
    there, keeping the query string, so old links and bookmarks work.
  - `SETUP_ID` is renamed `SETTINGS_ID`; `tabPath` maps it to
    `/settings`.
  - The gear shows as current (`aria-current="page"`, the active style)
    while Settings is open, since no tab is.

- **D5. Forms becomes an icon with a count of new submissions.**
  - An inbox button sits left of the gear, outside the scrolling tabs;
    tooltip and aria-label "Forms", or "Forms, N new". It shows a badge
    with the project's new submissions, hidden at 0 and capped at "99+".
  - Migration 037 adds `forms.seen_at TEXT` (NULL: never seen). Existing
    forms get `seen_at` = the upgrade time, so the upgrade shows nothing
    as new. A form created later starts at NULL.
  - New submissions of a form: `received_at > COALESCE(seen_at, '')`,
    served by the existing `submissions_form` index. Draft forms count
    (a new draft is exactly what you want to notice); archived forms
    don't.
  - Opening a form's page (`/projects/:id/forms/:name`) marks it seen up
    to the newest submission that page loaded: `POST
    /api/projects/:id/forms/:name/seen` with `{"until": "<last_submitted_at>"}`,
    the form's `last_submitted_at` as `list_forms` gave it to the page
    (not a row of the submissions table).
    The server sets `seen_at = MAX(COALESCE(seen_at, ''), until)`, so a
    submission arriving while the page is open stays new, and an older
    tab can't move `seen_at` back. With no submissions there is no call.
  - The Forms list shows an "N new" chip on each form with new
    submissions.
  - Marking seen is console state: a REST route only, not audited, no MCP
    tool. Reading submissions over MCP (`list_submissions`,
    `get_submission`, `find_submissions`) never clears the badge.

- **D6. A new route says how much is new and whether data arrives.**
  `GET /api/projects` and `list_projects` read the registry snapshot and
  nothing else on purpose: the web app waits on them before loading any
  widget. The two numbers go on a REST-only route, `GET
  /api/project-activity`, answering `{"projects": [{"project_id",
  "last_event_day", "new_submissions"}]}` for every live project, in list
  order:
  - `new_submissions`: the sum of D5's count over its live forms.
  - `last_event_day`: the newest day across `raw_views`, `raw_product`,
    `raw_measures`, `agg_views_daily`, `agg_product_totals` and
    `agg_measures_daily` for the project (the pattern `usage` uses), or
    null with none. Not from `actors`, which holds only user- and
    install-identified actors and is filled by the nightly pass: a
    web-only project would never show data.

  `list_forms` gains `seen_at` and `new_submissions` on each form.

- **D7. A project with no data says so.** While `last_event_day` (from
  `GET /api/project-activity`) is null the project page shows a notice
  above the tabs: "No events received yet" with a "Set up this project"
  link to Settings. It goes away with the first event (the activity
  query is refetched).

## Out of scope

- A per-project "default tab" stored on the server; the last tab covers
  it.
- An Overview tab, a status chip by the project name, live project
  cards and a cross-project submissions inbox, which came up in the
  discussion; each can follow on its own.
- New submissions in the sidebar.

## Documentation

In the same commit as the change (CLAUDE.md's table):

- `docs/twillingate.md`: `list_forms` fields, the `GET
  /api/project-activity` and `POST /api/projects/:id/forms/:name/seen`
  routes, `move_project_tab` and `add_project_tab` taking built-ins.
- `docs/reporting.md`: the project tab order (one order, any tab
  movable, a new built-in goes last).
- `deploy/UPGRADES.md`: migration 037 marks every existing form seen;
  tab order is unchanged.

## Testing

- **Go.**
  - `project_tabs_test.go`: moving a built-in before and after your own
    tabs; adding a built-in back with and without `after`; a release's
    new built-in goes last on every project; the old refusals are gone.
  - A `migration037_test.go` pinning its ceiling: keys rewritten into
    the old shown order, archived user rows kept in place, every key a
    valid `sortkey`, existing forms seen.
  - Store tests: `new_submissions` per form and per project (drafts in,
    archived out); `seen` never moves back; `last_event_day` from
    the raw and rolled-up tables (a web-only project has a day).
  - `docs_sync_test.go` passes with the new route and fields.
- **Vitest.** `landingPath`: a remembered live tab, a remembered tab that
  was removed, no memory, no tabs. Tab bar: gear and inbox outside the
  tabs, badge hidden at 0 and capped at 99+. `/setup` redirects.
- **E2E.** `project-tabs.spec.ts` (drag a built-in, land on the last
  tab), `cursor.spec.ts` (the two buttons), `phone.spec.ts` (the icons
  fit at 360px with the long project name).
