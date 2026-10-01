# Customizing and arranging dashboards

Status: draft
Date: 2026-09-30

## Problem

- **Customizing leaves two copies in the sidebar.** The system dashboards
  are the zero-setup default: a fresh install shows Reports (group 1, five
  tabs) with nothing to configure. To change one, an agent calls
  `duplicate_dashboard {whole_group: true}`, and the copy lands under
  "Yours" next to the original, which can never be removed. The sidebar
  keeps both "Views" and "Views (copy)" forever.
- **The page cannot arrange anything.** Duplicating, archiving and
  reordering dashboards and tabs all need an agent, even though each one
  is one existing REST call.

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
  dashboard only to borrow it would hide Reports as a surprise.
- **A separate `hidden` flag.** It would duplicate what archiving already
  is: a dashboard kept but left out of the sidebar, brought back with
  `restore_dashboard`.
- **Archiving one system tab.** A system group is one unit: it is
  archived, restored and replaced whole, and never moved.

## Decisions

### System groups are archived whole

1. **`archive_dashboard` and `restore_dashboard` accept a system
   dashboard with `whole_group: true`**, acting on every member of its
   group. Without `whole_group` they are refused on a system dashboard
   (`ErrInvalid`: "a system dashboard is archived with its group; pass
   whole_group"). Every other write to a system dashboard (update, move,
   regroup, adding or changing widgets) stays refused.
2. **System widgets still cannot be archived.** The sync sets each system
   widget's `archived_at` back to NULL on every start, so archiving one
   would not stick. `archive_widget` on one stays refused.
3. **Archiving a system group survives releases.** The sync's upsert of a
   system dashboard writes its title, key and group, never `archived_at`,
   so an archived one stays archived. No change to the sync, only a test
   that pins this. A dashboard new in a release arrives live, even in an
   archived group; since a release that adds a tab to an archived group
   would then show that tab alone, the sync archives a new system
   dashboard whose group's existing members are all archived.
4. **Archived system dashboards are never purged.** The purge already
   selects `owner = 'user'` only (`TestPurgeArchivedNeverSelectsSystemDashboards`).
   No change.
5. **Audited as for user dashboards**: `dashboard.archive` and
   `dashboard.restore`, with the actor.

### Duplicating a system group replaces it

6. **`duplicate_dashboard` takes `archive_source`**, a boolean that
   defaults to `true` when the source is a system dashboard and to `false`
   when it is a user one.
   - **System source, `archive_source` true:** the whole group is copied,
     whatever `whole_group` says, as one new user group last in the
     sidebar, and the system group is archived. The result is the copy of
     the dashboard named, so the page opens on the tab it was on.
   - **System source, `archive_source: false`:** copies what was asked
     (the dashboard, or its group with `whole_group`), archiving nothing.
     An agent borrowing a system dashboard as a starting point uses this.
   - **User source, `archive_source: true`:** archives what was copied
     (the dashboard, or its group's live members with `whole_group`).
7. **Copy and archive are one transaction.** The store's dashboard insert
   takes the ids to archive and writes them with the copy, so a failure
   leaves neither. The audit keeps its one `dashboard.duplicate` row, whose
   detail notes the archived ids.
8. **An archived user source is still refused**, as today
   ("restore_dashboard first"): its copy would land in a group that may
   have no live member left. **An archived system source is accepted**:
   its copy is always a new user group, so the gallery can copy a tab of
   Reports after Reports was replaced. With `archive_source` true, members
   already archived stay archived.
9. **Over REST**, `archive_source` joins `whole_group` in the optional body
   of `POST /api/dashboards/{dashboard_id}/duplicate`. The OpenAPI route
   spec carries it.

### Menus in the page

10. **The sidebar entry of a group has a "…" menu** (shown on hover, always
    on phones), acting on the whole group:
    - System group: **Duplicate** ("your copy replaces it in the sidebar")
      and **Archive**.
    - User group: **Duplicate** (`whole_group`, a new entry last in
      "Yours"), **Archive** (`whole_group`), **Move up** and **Move down**.
11. **The dashboard header has a "…" menu next to Refresh on user
    dashboards only**, acting on the dashboard shown. System dashboards
    have no header menu.
    - In a group of more than one: **Duplicate tab** (the copy joins the
      group right after the original, as `duplicate_dashboard` does today),
      **Archive tab**, **Move left**, **Move right** and **Move to**.
    - In a group of one: **Duplicate** and **Archive**, the same as the
      sidebar entry's (a new sidebar entry, not a second tab), and **Move
      to**.
    - **Move to** is a submenu: every other live user group, by its
      sidebar title, and, in a group of more than one, **Own dashboard**.
      A group takes the tab as its last tab (`update_dashboard
      {group_id: G}`); Own dashboard makes it a sidebar entry of its own,
      right after the group it left (`{group_id: 0}`). The page stays on
      the dashboard, now among its new group's tabs. Moving a group's last
      tab out ends that group, which is what the tabs spec already does.
12. **After an action** the page refetches the dashboard list and the
    dashboard shown, so the sidebar and the tabs follow:
    - Duplicate opens the copy.
    - Archive moves to the group's next live tab, else to `/`, and shows a
      toast, "Archived 'Marketing'. Undo", whose Undo restores it.
    - A refusal shows its message in a toast.
    Sonner is already a dependency; the app mounts its `<Toaster>`.
13. **No confirmation dialogs.** Every action is undone in one step:
    archive by Undo or the gallery, duplicate by archiving the copy, a move
    by the opposite move.

### Reordering

14. **User groups reorder by dragging in the sidebar, and user tabs by
    dragging in the tab bar**, with dnd-kit (pointer and keyboard sensors).
    The phone layout, where tabs are a select, uses the Move items of the
    menus. System entries and system tabs cannot be dragged, and user
    groups cannot be dropped above them.
15. **A move is one `update_dashboard {after}`**, which already moves by
    what it names (tabs spec D6–D7):
    - a tab: `after` the tab before its new place in the same group, or
      `{group_id: <own group>, after: 0}` to become the first tab;
    - a group: `after` the last dashboard of the group before its new
      place, or `after: 0` to become the first in "Yours".
    The dragged item stays where it was dropped while the request runs,
    and snaps back with a toast if it is refused.
16. **Moving a tab between groups is a menu action only**, on every
    layout. Dragging a tab from the tab bar onto a sidebar entry is not in
    this change. Creating and renaming dashboards stay in the tabs spec's
    PR 2.

### Dashboard gallery

17. **`/app/gallery/dashboards`**, next to Components in the sidebar's
    Gallery section, lists:
    - **System**: every system group, archived or not, by its first
      dashboard's title with its tabs, each linking to its dashboard; an
      **Archive** button on a live group and a **Restore** button on an
      archived one. Each tab has **Copy as a dashboard**:
      `duplicate_dashboard {archive_source: false}`, a live, standalone
      user dashboard last in "Yours", titled "… (copy)", which the page
      then opens. The system group stays as it was, live or archived.
    - **Archived**: every archived user dashboard, with its group's title
      when it has one, when it will be purged ("deleted on 30 Oct",
      from `archived_at` plus `RETENTION_ARCHIVED_DAYS`, which
      `list_dashboards` gains as `purge_after_days`, absent when 0), and a
      **Restore** button (the dashboard alone).
    So everything the page archives can be restored from the page.
18. **An archived dashboard opened by URL says so.** `/dashboards/{id}`
    still opens it, since `get_dashboard` and widget data serve archived
    dashboards. A line above the grid reads "Archived: not in the
    sidebar", with a Restore button (with `whole_group` on a system one).

### Page writes

19. **The page now writes definitions.** Until now it wrote only the
    viewer's selection (`PUT …/view`). Duplicate, archive, restore and
    move are the existing, audited routes, with the page's bearer token,
    the same credential an agent uses over REST. The page adds no route.

## Surfaces

| Tool | Change | REST |
| --- | --- | --- |
| `archive_dashboard` | accepts a system dashboard with `whole_group` | `POST /api/dashboards/{dashboard_id}/archive` |
| `restore_dashboard` | accepts a system dashboard with `whole_group` | `POST /api/dashboards/{dashboard_id}/restore` |
| `duplicate_dashboard` | `archive_source`; a system source copies and archives its whole group by default; an archived system source is accepted | `POST /api/dashboards/{dashboard_id}/duplicate`, optional body |
| `list_dashboards` | returns `purge_after_days` | `GET /api/dashboards` |

No tool or route is added, removed or renamed. No migration:
`archived_at` already exists on every dashboard.

The MCP server instructions (`serverInstructions` in
`internal/api/guide_reporting.go`) change from "System dashboards are
read-only; duplicate one to customize it." to "To customize a system
dashboard, duplicate it: the copy replaces its group in the sidebar."

## Documentation

`docs/reporting.md`, same commit:

- Concepts: system groups change only with a release, are archived and
  restored whole, and duplicating one replaces it unless
  `archive_source: false`.
- Workflow step 5 and Rules: the same, replacing "read-only".
- Tools table: `archive_source`; archive and restore on system
  dashboards; `purge_after_days`.
- HTTP API: the duplicate body's `archive_source`.
- Archiving and the purge: "System dashboards and their widgets cannot be
  archived" becomes "A system group is archived and restored whole and
  never purged; system widgets cannot be archived."
- Refusals and fixes: the new refusal (archive a system dashboard without
  `whole_group`).

No `deploy/UPGRADES.md` entry: nothing changes on upgrade day.

## Tests

- **reporting (Go)**:
  - Archive and restore a system group with `whole_group`; without it,
    refused.
  - `archive_widget` on a system widget is still refused.
  - `duplicate_dashboard` from a system tab copies and archives the whole
    group by default and returns the copy of that tab; with
    `archive_source: false` it copies only the tab and archives nothing;
    from a user source it archives nothing by default, and what it copied
    with `archive_source: true`.
  - A failed insert archives nothing.
  - An archived user source is still refused; an archived system source
    is copied, and its group stays archived.
- **store**: a sync keeps a system dashboard's `archived_at`; a new system
  dashboard arrives live, and archived when every existing member of its
  group is archived.
- **api**: the duplicate route with `{"archive_source": false}`; archive
  and restore on a system id with and without `whole_group`;
  `purge_after_days` in the list.
- **web (unit)**: which menu items show for a system group, a user group,
  a user tab and a lone user dashboard; the move items compute the right
  `after`; Move to lists the other user groups and Own dashboard only in
  a group of more than one; the gallery lists system groups and archived
  user dashboards with the right buttons; the archived line.
- **e2e**: duplicate Reports from the sidebar menu, land on the copy, no
  Reports in the sidebar, restore it from the gallery; archive a user tab
  and undo from the toast; drag a user tab and a user group, and reload
  to see the order kept; move a tab into another group and back out to its
  own dashboard; copy a system tab from the gallery while Reports is
  archived.

## Out of scope

- Telling a copy that its system source changed in a later release.
- Dragging a tab between groups; creating and renaming dashboards.
- Archiving one system tab, or system widgets.
- Per-viewer visibility or order: an install has one operator, so both
  are global.
