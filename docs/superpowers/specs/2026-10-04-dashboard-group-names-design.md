# Dashboard group names

Status: draft
Date: 2026-10-04

## Problem

- **A group has no name of its own.** The sidebar, the Archive page and
  the "Move to" menu show a group by its first live tab's title. To
  rename a group, you have to rename that tab, and its tab label changes
  with it.
- **The system group of tabs reads "Views".** Views, Product, Users,
  Groups, Retention, Web Vitals and Measures are one group (group 1), and
  the sidebar names it after its first tab. It was "Reports" before tabs (#95).
- **Group ids are not stable.** When the dashboard whose id a group uses
  leaves the group, the members left take the id of the first live one
  (`order.handOver`). Anything stored against a group id has to follow
  that change.

## Decisions

- **D1. A name-only table, one row per named group.** Migration 031:

  ```sql
  CREATE TABLE dashboard_groups (
      group_id INTEGER PRIMARY KEY,
      title    TEXT NOT NULL
  );
  ```

  A row exists only for a group someone named. There is no foreign key,
  because `dashboards.group_id` is not unique; D3 and D4 keep the table
  in step instead. There is no surrogate id: nothing refers to a row
  except by `group_id`. Sort order stays on `dashboards.sort_key`, and
  an unnamed group, including every group of one nobody renamed, has no
  row.

  A row is written in exactly three places:
  1. **The first rename** (D5) inserts it. A later rename updates it.
     Both are one upsert (`INSERT … ON CONFLICT (group_id) DO UPDATE SET
     title`) in the same transaction as the audit row.
  2. **Duplicating a whole named group** (D7) inserts
     `"<name> (copy)"` for the new group, in `InsertDashboardGroup`'s
     transaction. A copy of an unnamed group gets no row.
  3. **The release sync** (D6) upserts the rows for system groups whose
     founding dashboard's fixture has `group_title`.

  Nothing else creates a row: not creating a dashboard, not joining,
  leaving or reordering, not archiving or restoring. Migration 031 only
  creates the table and its triggers. The system rows come from the
  first sync after the upgrade. A rekey (D3) moves a row and never
  creates one.

- **D2. No row means the current fallback.** A group with no row shows
  its first live tab's title, as it does today. Existing groups look the
  same after the upgrade, except system group 1 (D6).

- **D3. Rekey with the members, in the same transaction.** `handOver` is
  the only place an existing group's id changes. `writePlaced` passes
  the old and the new id to `MoveDashboards`, which runs
  `UPDATE dashboard_groups SET group_id = :new WHERE group_id = :old`
  before it repoints the rows, in the same transaction. The new id can't
  collide with an existing row: it is a member's own id, and a group
  that member founded and left was handed over when it left.

  The name stays with the members left behind, not with the dashboard
  that leaves. A founding dashboard that leaves keeps its id as the id
  of its new group of one, and that group has no name.

- **D4. Delete the row when its group has no dashboards left, with
  triggers.** Migration 031 adds two triggers on `dashboards`:

  ```sql
  CREATE TRIGGER dashboard_groups_gone_delete AFTER DELETE ON dashboards
  BEGIN
    DELETE FROM dashboard_groups WHERE group_id = OLD.group_id
      AND NOT EXISTS (SELECT 1 FROM dashboards WHERE group_id = OLD.group_id);
  END;
  CREATE TRIGGER dashboard_groups_gone_update AFTER UPDATE OF group_id ON dashboards
  WHEN NEW.group_id <> OLD.group_id
  BEGIN
    DELETE FROM dashboard_groups WHERE group_id = OLD.group_id
      AND NOT EXISTS (SELECT 1 FROM dashboards WHERE group_id = OLD.group_id);
  END;
  ```

  They cover every way a group loses its last dashboard: the purge of
  archived dashboards (`purge.go`), a system dashboard dropped by a
  release (`reporting_sync.go`), a group of one joining another group,
  and deletion by an older binary after a rollback. They do not cover an
  older binary rekeying a group's first tab out of it: the older binary
  does not move names, so the new group of one takes the name with it
  (see UPGRADES.md). Migration 022 is the
  precedent for a trigger on this table. A rebuild of `dashboards` must
  recreate these triggers, as it must recreate 022's.

  Archived members count as members. A group whose tabs are all archived
  keeps its name until the last of them is purged, so the Archive page
  shows it under that name.

- **D5. Rename with `update_dashboard {dashboard_id, whole_group: true,
  title}`.** This reuses the flag that duplicate, archive and restore
  already take, so there is no new tool or route. With `whole_group`,
  `title` names the group of `dashboard_id`, and `after` and `group_id`
  are refused (to move a whole group, `after` already does that without
  the flag). The usual refusals apply: system groups and archived
  dashboards. A group name is trimmed and must have at least 2
  characters (runes, not bytes); anything shorter is refused with
  `ErrInvalid`. The check is in Go, not the table, like every other
  rule in the store. The web form enforces the same minimum before it
  sends, and `checkGroups` applies it to fixture names (D6). A title
  equal to the first tab's still writes a row; the fallback only applies
  when no row exists.

  **A name can't be cleared.** With `whole_group`, `title` is required,
  and a missing or empty `title` is refused with `ErrInvalid`, the same
  as one under 2 characters. No call deletes a name row. A group loses
  its name only when it loses its last dashboard (D4). The web Rename
  field refuses an empty value as it refuses a 1-character one.

- **D6. System groups are named in their fixtures.** A founding
  dashboard's `dashboard.json` may carry `"group_title"`. `views` gets
  `"group_title": "Reports"`. The release sync upserts the rows for
  system groups and deletes those a release no longer names.
  `checkGroups` refuses a `group_title` on a dashboard that isn't a
  group's founder.

- **D7. Duplicating a whole named group copies the name with "
  (copy)".** This matches how tab titles are copied. A single-tab copy
  carries no group name: it is a group of one, or a tab of an existing
  group.

- **D8. Reads carry the stored name.** `DashboardInfo` gains
  `group_title` (omitempty), set on every row of a named group. Clients
  apply the fallback themselves, so they can tell a named group from an
  unnamed one (the rename field shows the fallback as a placeholder).

- **D9. Web.** The sidebar entry, Archive cards, the "Move to" menu and
  the gallery's Dashboards page show `group_title`, or the first live
  tab's title when it is absent. The sidebar group's "…" menu gets
  Rename, editing in place like the project name in #132. Rename names
  only the group and never changes a dashboard's title, even in a group
  of one. The sidebar then shows the group name, and the page header and
  tab show the dashboard title.

- **D10. Tests.**
  - An invariant check after every placement operation (create,
    update, leave, join, move, duplicate, archive, restore), the purge
    and the release sync: no `dashboard_groups` row whose `group_id` no
    dashboard has.
  - Handing over a named group keeps the name with the members left,
    and the leaver's new group has none.
  - Group names: "" and " " are refused; one character (including one
    multi-byte character such as "é") is refused; two characters are
    accepted; surrounding spaces are trimmed before the count and the
    write. `whole_group` with a missing or empty `title` is refused and
    leaves the name as it was. The same cases for dashboard titles on
    create and update (D11).
  - Migration test pinned at 031, including the triggers firing on a
    purge and on a group of one joining another group.

- **D11. Dashboard titles get the same 2-character minimum.**
  `create_dashboard` and `update_dashboard` (`ops_dashboard.go:64` and
  `:132`, which today refuse only a blank title) trim the title, count
  its characters, and refuse fewer than 2 with `ErrInvalid`. They store
  the trimmed title; today it is stored untrimmed. One helper checks
  both dashboard titles and group names. Copies are always long enough,
  since a copy's title is `"<title> (copy)"`. The release check applies
  the minimum to system dashboard titles in the fixture files. Existing
  rows with a shorter title stay as they are until someone renames
  them. Today the web app only renames groups (D9); any later
  dashboard-title field checks the same minimum.

- **D12. Ships as `feat(reporting)`**, not breaking. Refusing a
  1-character dashboard title narrows `create_dashboard` and
  `update_dashboard`, but stored data and every realistic call are
  unaffected, so this doesn't get a `!`. `docs/reporting.md`
  covers `whole_group` with `title` on `update_dashboard`, the
  `group_title` field and the 2-character minimum. The `title`
  descriptions in the tool schemas state the minimum too.
  `deploy/UPGRADES.md` gets one line: after the upgrade, the system
  group's sidebar entry reads "Reports" instead of "Views".

