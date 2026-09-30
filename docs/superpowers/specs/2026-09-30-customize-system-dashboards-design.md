# Customizing system dashboards

Status: draft
Date: 2026-09-30

## Problem

- **Customizing leaves two copies in the sidebar.** The system dashboards
  are the zero-setup default: a fresh install shows Reports (group 1, five
  tabs) with nothing to configure. To change one, an agent calls
  `duplicate_dashboard {whole_group: true}`, and the copy lands under
  "Yours" next to the original, which can never be removed. The sidebar
  keeps both "Views" and "Views (copy)" forever.
- **A system dashboard you don't use cannot be put away.** An install that
  sends no product events still shows the Product tab.

## Considered and rejected

- **Copy the system dashboards into user dashboards at first start.**
  Everything would be editable, but the copies would stop following
  releases. System dashboards are files synced on every start precisely so
  that they follow schema changes (migrations 012, 017, 019 and 020 each
  reshaped the views they read), and `TestSystemDashboards` breaks the
  release rather than the user's install. Seeded copies would break
  silently on upgrade.
- **Hide a system dashboard while a user dashboard was copied from it**
  (a `source_id` on copies, visibility derived from it). Copying a
  dashboard only to borrow it would hide Reports as a surprise, and a
  dashboard could not be hidden without copying it.
- **A separate `hidden` flag.** It would duplicate what archiving already
  is: a dashboard kept but left out of the sidebar, brought back with
  `restore_dashboard`.

## Decisions

### Archiving system dashboards

1. **`archive_dashboard` and `restore_dashboard` accept system
   dashboards**, alone or with `whole_group`, as they do user ones. The
   refusal `refuseSystem` goes from `setDashboardArchived`; every other
   write to a system dashboard (update, move, regroup, adding or changing
   widgets) stays refused.
2. **System widgets still cannot be archived.** The sync sets each system
   widget's `archived_at` back to NULL on every start, so archiving one
   would not stick. `archive_widget` on one stays refused.
3. **Archiving a system dashboard survives releases.** The sync's upsert
   of a system dashboard writes its title, key and group, never
   `archived_at`, so an archived one stays archived. A dashboard new in a
   release arrives live; one a release drops is deleted as today. No
   change to the sync, only a test that pins this.
4. **Archived system dashboards are never purged.** The purge already
   selects `owner = 'user'` only (`TestPurgeArchivedNeverSelectsSystemDashboards`).
   No change.
5. **Audited as for user dashboards**: `dashboard.archive` and
   `dashboard.restore`, with the actor.

### Duplicating replaces the source

6. **`duplicate_dashboard` takes `archive_source`**, a boolean that
   defaults to `true` when the source is a system dashboard and to `false`
   when it is a user one. When true, the dashboards that were copied are
   archived: the source alone, or with `whole_group` every live member of
   its group, which is exactly the set copied. So one call customizes
   Reports: `duplicate_dashboard {dashboard_id: 1, whole_group: true}`
   makes a five-tab user copy and archives the original five. An agent
   borrowing a system dashboard as a starting point passes
   `archive_source: false`.
7. **Copy and archive are one transaction.** The store's dashboard insert
   takes the ids to archive and writes them with the copy, so a failure
   leaves neither. The audit keeps its one `dashboard.duplicate` row, whose
   detail notes the archived ids.
8. **An archived source is still refused**, as today ("restore_dashboard
   first"). To copy an archived system dashboard, restore it and then
   duplicate it, which archives it again by default.
9. **Over REST**, `archive_source` joins `whole_group` in the optional body
   of `POST /api/dashboards/{dashboard_id}/duplicate`. The OpenAPI route
   spec carries it.

### Page

10. **The sidebar is unchanged.** It already shows live dashboards only,
    so an archived system dashboard or group leaves it, and a group with
    some tabs archived shows the rest.
11. **`/` is unchanged.** `pickDashboard` already skips archived
    dashboards: the last one opened if it is live, else the first live
    system one, else the first live one.
12. **A dashboard gallery at `/app/gallery/dashboards`.** It lists every
    system dashboard, archived ones included, by group in sidebar order.
    Each shows its title and whether it is in the sidebar, links to it,
    and has a **Restore** button when archived and an **Archive** button
    when live. Each group of more than one member has the same pair for
    the whole group (`whole_group: true`). The buttons call the existing
    `POST /api/dashboards/{id}/archive` and `…/restore` routes, then
    refetch the dashboard list, so the sidebar follows. The Gallery
    sidebar section gains "Dashboards" next to "Components", and
    `/gallery` redirects stay on components.
13. **This is the page's first definition write.** Until now the page
    wrote only the viewer's selection (`PUT …/view`). Archive and restore
    are the existing, audited routes with the page's bearer token, which
    is the same credential an agent uses over REST. A refusal shows its
    message in a toast. Editing dashboards from the page (the tabs spec's
    PR 2) stays separate.
14. **An archived dashboard opened by URL says so.** `/dashboards/{id}`
    still opens it, since `get_dashboard` and widget data serve archived
    dashboards. The page shows a line above the grid, "Archived: not in
    the sidebar", with a Restore button (the same route, system or user).
    The tab bar shows the group's live tabs, as `get_dashboard` returns
    them.

## Surfaces

| Tool | Change | REST |
| --- | --- | --- |
| `archive_dashboard` | accepts system dashboards | `POST /api/dashboards/{dashboard_id}/archive` |
| `restore_dashboard` | accepts system dashboards | `POST /api/dashboards/{dashboard_id}/restore` |
| `duplicate_dashboard` | `archive_source`, default true for a system source | `POST /api/dashboards/{dashboard_id}/duplicate`, optional body |

No tool or route is added, removed or renamed. No migration: `archived_at`
already exists on every dashboard.

The MCP server instructions (`serverInstructions` in
`internal/api/guide_reporting.go`) change from "System dashboards are
read-only; duplicate one to customize it." to "To customize a system
dashboard, duplicate it: the copy replaces it in the sidebar."

## Documentation

`docs/reporting.md`, same commit:

- Concepts: system dashboards change only with a release but can be
  archived and restored; duplicating one archives it unless
  `archive_source: false`.
- Workflow step 5 and Rules: the same, replacing "read-only".
- Tools table: `duplicate_dashboard`'s `archive_source`; archive and
  restore accept system dashboards.
- HTTP API: the duplicate body's `archive_source`.
- Archiving and the purge: "System dashboards and their widgets cannot be
  archived" becomes "System dashboards can be archived and are never
  purged; their widgets cannot be archived."
- Refusals and fixes: the system-dashboard refusal's fix names archive as
  allowed.

No `deploy/UPGRADES.md` entry: nothing changes on upgrade day.

## Tests

- **reporting (Go)**:
  - Archive and restore a system dashboard, alone and `whole_group`.
  - `archive_widget` on a system widget is still refused.
  - `duplicate_dashboard` from a system source archives what it copied by
    default, alone and `whole_group`; with `archive_source: false` it
    archives nothing; from a user source it archives nothing by default,
    and the source with `archive_source: true`.
  - A failed insert archives nothing.
  - An archived source is still refused.
- **store**: a sync keeps a system dashboard's `archived_at`; a
  dashboard new in the release arrives live.
- **api**: the duplicate route with `{"archive_source": false}`; archive
  and restore on a system id return 204.
- **web (unit)**: the dashboard gallery lists archived and live system
  dashboards with the right button; the archived line shows on an
  archived dashboard and not on a live one.
- **e2e**: duplicate group 1 over REST, and the sidebar shows the copy
  and no Reports; restore group 1 from the gallery, and Reports is back.

## Out of scope

- Telling a copy that its system source changed in a later release.
- Duplicating from the page (the tabs spec's PR 2, editing in the page).
- Archiving system widgets.
- Per-viewer visibility: an install has one operator, so archiving is
  global.
