# Project tabs

Status: draft
Date: 2026-10-05

## Problem

- **A project page shows no analytics.** `/projects/:id` holds usage,
  origins, breakdowns, keys and cap impact. To see a project's traffic you
  leave it for `/dashboards/:id`, then pick the project in the switcher.
- **Dashboards are not tied to projects.** Every dashboard is global, with a
  project switcher. A dashboard you wrote for one app shows up with
  every project's switcher, and no project can say "these are my tabs".
- **`archived_at` means two things.** On a user dashboard it means
  archived. On a system dashboard it means "hidden from the sidebar"
  ("Hide"), and the system group comes back with "Show in sidebar".
  Every reader branches on the owner to tell the two apart.

## Decisions

- **D1. A project page is tabs: Setup, then built-ins, then your own,
  then +.**
  - **Setup** is always first and can't be removed. It holds what
    `/projects/:id` shows today, plus the Archive / Restore button.
  - **Built-in tabs** come next, in release order. They can be removed
    from a project and added back, but not reordered.
  - **Your own tabs** come after the built-ins. You can reorder them per
    project.
  - The **+** button is last. It adds an existing dashboard (D7).
  - Opening a project lands on Setup. The analytics are one click away,
    in the tabs next to it.

- **D2. Each project keeps its own list of tabs.** In migration 032:

  ```sql
  CREATE TABLE project_tabs (
      project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
      dashboard_id INTEGER NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
      sort_key     TEXT NOT NULL,
      PRIMARY KEY (project_id, dashboard_id)
  );
  ```

  - A row means "this project has this tab". Removing a tab deletes the
    row, and adding it back inserts a new one.
  - `sort_key` orders your own tabs within a project. Built-in tabs sort
    by `dashboards.sort_key`, the release order, and their row's
    `sort_key` is ignored.
  - Deleting a project or a dashboard removes its rows by `ON DELETE
    CASCADE`. The store opens SQLite with `foreign_keys(1)`
    (`sqlite.go:52`).

- **D3. Two placement flags on every dashboard.** Migration 032 adds:

  ```sql
  ALTER TABLE dashboards ADD COLUMN sidebar     INTEGER NOT NULL DEFAULT 1;
  ALTER TABLE dashboards ADD COLUMN project_tab INTEGER NOT NULL DEFAULT 0;
  ```

  - `sidebar`: the dashboard's group is in the sidebar. It is written for
    the whole group, since the sidebar shows groups, so every tab in a
    group has the same value.
  - `project_tab`: a **new** project gets this dashboard as a tab (D4).
    It does not add or remove tabs on existing projects.

- **D4. Rows are seeded by triggers, so every surface (console, MCP,
  CLI) gets them.**

  ```sql
  CREATE TRIGGER project_tabs_new_project AFTER INSERT ON projects
  BEGIN
    INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
      SELECT NEW.id, d.id, d.sort_key FROM dashboards d
      WHERE d.project_tab = 1 AND d.archived_at IS NULL;
  END;
  CREATE TRIGGER project_tabs_new_builtin AFTER INSERT ON dashboards
  WHEN NEW.owner = 'system' AND NEW.project_tab = 1
  BEGIN
    INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
      SELECT p.id, NEW.id, NEW.sort_key FROM projects p;
  END;
  ```

  - A built-in that a release adds is inserted once, when the release
    sync first writes it. Later boots never insert it again, so a tab you
    removed stays removed.
  - Your own dashboard with `project_tab = 1` is seeded into new projects
    only. To put it on existing projects, use the checklist in D8.
  - The migration comment notes that a rebuild of `projects` or
    `dashboards` must recreate these triggers, as it must 031's.

- **D5. Built-in dashboards are never archived. `archived_at` applies to
  your own dashboards only.**
  - "Hide" on a built-in group sets `sidebar = 0` for the group. The
    gallery's "Add to sidebar" sets it back to 1.
  - `archive_dashboard` and `restore_dashboard` refuse a built-in
    (`ErrInvalid`, "use update_dashboard sidebar").
  - Archiving your own dashboard takes it out of the sidebar and off every
    project page. Its `project_tabs` rows are kept, so restoring it brings
    its tabs back where they were.
  - The Archive page lists only your own dashboards.
  - A live dashboard of your own with `sidebar = 0` and no
    `project_tabs` row would be unreachable, so any write that would
    leave it in that state is refused (`ErrInvalid`, "archive it
    instead").

- **D6. `dashboard.json` gains two required fields.**

  ```json
  "sidebar": true,
  "project_tab": true
  ```

  - Both are required, like `range`: the release states its choice
    rather than relying on a default.
  - `sidebar` is written **on insert only**, after which it belongs to the
    install (Hide / Add to sidebar). `false` ships a built-in that appears
    only on project pages.
  - `project_tab` is **release-owned**, re-synced on every release like
    `title`, and can't be changed for a built-in (`ErrInvalid`).
  - All seven built-ins ship with `true` / `true`.

- **D7. Operations.** Each is one `reporting.Service` method, exposed over
  MCP and REST like the existing dashboard operations.
  - `list_project_tabs(project_id)` lists the tabs in shown order. Each
    item carries `dashboard_id`, `title`, `owner` and `group_id`.
    Setup is not a row and is not listed.
  - `add_project_tab(project_id, dashboard_id, after?)`. A built-in goes
    back to its fixed place, and `after` is refused for it. Your own
    dashboard goes after `after`, or last when `after` is omitted. Adding
    a tab the project already has is refused (`ErrConflict`), and so is
    adding an archived dashboard (`ErrInvalid`).
  - `remove_project_tab(project_id, dashboard_id)`.
  - `move_project_tab(project_id, dashboard_id, after)`, for your own
    tabs only; a built-in is refused (`ErrInvalid`). Moves are after-only,
    as dashboard moves are.
  - `update_dashboard` gains `sidebar` (always applied to the whole group)
    and `project_tab` (your own dashboards only).
  - `list_dashboards` and `get_dashboard` return `sidebar` and
    `project_tab`.
  - The MCP server instructions change from "archive the original with
    archive_dashboard whole_group" to "set `sidebar: false` on the
    original with update_dashboard".

- **D8. Routes and the page** (under `/app`).
  - `/projects/:id` redirects to `/projects/:id/setup` (D1).
  - `/projects/:id/setup` is the Setup tab.
  - `/projects/:id/dashboards/:dashId` is a dashboard tab with the project
    pinned. There is no project switcher, and every widget that follows
    `:project` gets `project_id = :id`. A widget that doesn't follow it
    shows the same data on every project page; `docs/reporting.md` says
    so.
  - `?range=&from=&to=` stays in the URL across tabs, Setup included.
    A tab opened without it uses that dashboard's own range.
  - Viewing a project never writes a dashboard's saved view (`PUT …/view`).
  - A `:dashId` that isn't one of the project's tabs shows "*Title* isn't
    a tab of *Project*" with an **Add tab** button.
  - **Layout:**
    - The top bar has the crumbs `Projects › name`.
    - The header has the project name (rename in place) and the Archived
      badge.
    - Then the tab row: `ReportTabs` reused, a row on wide screens and a
      select on phones. It gains a fixed prefix (Setup and the
      built-ins, not draggable) and a trailing **+**.
    - A dashboard tab's body is `DashboardHeader` (title, freshness,
      refresh, range switcher) plus `WidgetGrid`. Auto-refresh works as
      it does on the dashboard page.
  - **+** opens a picker with two sections: built-ins not on this project,
    and your own live dashboards not on it. There is no "new dashboard"
    entry, because there is no way yet to create an empty one in the UI.
  - **A tab's "…" menu:**
    - **Remove from this project.** Refused, with the server's message,
      when it would leave a sidebar-less dashboard unreachable (D5).
    - **Open as dashboard**, which goes to `/dashboards/:id?project=:id`.
    - **Move left / right**, for your own tabs on phones.
  - **On the dashboard page,** your own dashboard's tab menu gains
    **Project tabs…**: a checklist of projects (checked = the project has
    the tab) and an **Add to new projects** toggle (`project_tab`).
    Built-ins have no such entry; they are managed from each project page.

- **D9. Elsewhere in the app.**
  - **Sidebar:** shows groups with `sidebar = 1` that aren't archived.
  - **`Home`:** the "first system dashboard" fallback skips hidden ones.
  - **Gallery (`/gallery/dashboards`):** shows every built-in. **Add to
    sidebar** appears on those with `sidebar = 0`.
  - **Duplicating** a dashboard (built-in or your own) gives a copy with
    `sidebar = 1`, `project_tab = 0` and no project tabs.
  - **Projects list:** cards still link to `/projects/:id`, which opens
    on Setup, the same content as today.

## Upgrade (migration 032)

- Add the two columns and `project_tabs`.
- Backfill `project_tab = 1` on built-ins, and give every existing project
  a row for every built-in.
- Built-ins that are archived today (hidden) become `archived_at = NULL,
  sidebar = 0`. The sidebar looks the same as before the upgrade.
- Create the D4 triggers last, after the backfill.
- `deploy/UPGRADES.md`: project pages gain dashboard tabs next to Setup. Hidden
  built-in groups stay hidden, and the Archive page no longer lists them;
  "Add to sidebar" in the gallery brings them back.
- 032 is the next free number on `main` today. If another branch merges a
  032 first, renumber at rebase.

## Docs

- `docs/reporting.md`:
  - the two `dashboard.json` fields;
  - placement: sidebar, project tabs and archive;
  - the seeding rules;
  - the four new tools and the new `update_dashboard` fields;
  - widgets that don't follow `:project` on project pages.
- `docs/twillingate.md`: the new tool and route names, as
  `docs_sync_test` requires for each table that claims them.

## Tests

- **Go**
  - Migration 032:
    - the backfill;
    - hidden built-ins becoming `sidebar = 0`;
    - both triggers;
    - rows removed with their project or dashboard.
  - The release sync: `sidebar` applied on insert only, `project_tab`
    re-synced, and a release adding a built-in seeding it into existing
    projects exactly once.
  - Operations and their refusals:
    - archiving or restoring a built-in;
    - moving a built-in tab, or adding one with `after`;
    - a duplicate add;
    - adding an archived dashboard;
    - the unreachable state;
    - `project_tab` on a built-in.
  - `docs_sync` for the new tools.
  - `TestSystemDashboards` still passes.
- **Vitest**
  - The `/projects/:id` redirect.
  - Tab order: Setup, built-ins, your own, then +.
  - The fixed prefix in `ReportTabs`.
  - The picker's two sections.
  - The sidebar filter, and the gallery's Add to sidebar.
  - The Project tabs… checklist.
- **e2e**
  - `phone.spec` visits a project's dashboard tab and its Setup tab, with
    its long-name project.
  - `cursor.spec` covers **+** and the picker items.
  - A new `project-tabs.spec`:
    - remove a built-in tab and add it back (it returns to its place);
    - add your own dashboard, reorder it, open the Setup tab;
    - archive your own dashboard and see its tab go.

## Out of scope

- Creating an empty dashboard from the UI, and so from the picker.
- Editing a built-in's widgets in place. Duplicate it, then add the copy
  and remove the built-in.
- Reordering built-in tabs, or putting your own tabs before them.
- Per-project saved selections (range) on project tabs.
- Public share links to a project's tabs.
