# Dashboard tabs

Status: draft
Date: 2026-09-30

## Sequencing

Two PRs:

1. **Tabs for agents** (this spec) — `feat(reporting)`: dashboards can be
   grouped into one tabbed sidebar entry, agents build, move, archive and
   copy groups over MCP and REST, and the page shows them. Nothing that
   exists changes meaning, so it is not breaking.
2. **Editing in the page** — a later spec: create, rename, archive and
   duplicate from `/app/`, which today writes nothing but the viewer's
   selection.

## Problem

- **Tabs exist only in the page.** The five system dashboards are five
  rows; `ReportTabs` shows every live `owner = 'system'` dashboard as one
  "Reports" entry with tabs. Nothing in the data says they belong
  together, so nothing else can be grouped that way.
- **User dashboards cannot have tabs.** An agent asked for "a marketing
  dashboard with a funnel tab and a retention tab" can only make three
  sidebar entries.
- **Reports cannot be copied as a whole.** `duplicate_dashboard` copies
  one dashboard; customising Reports means five duplicates that land as
  five unrelated entries.

## Decisions

### Model

1. **A group is the dashboards that share a `group_id`.** `dashboards`
   gains `group_id INTEGER NOT NULL`. Every dashboard is in exactly one
   group; a group of one is a dashboard as it is today. There is no
   group table and no group row: a group has no name, position or state
   of its own, so nothing about it can be out of step with its members.
2. **A new group's id is its first dashboard's id.** A dashboard created
   without joining a group gets `group_id` = its own id, written in the
   insert's transaction. Dashboard ids are never reused
   (`AUTOINCREMENT`), so group ids are unique without a counter. The
   number stays with the group if that dashboard later leaves it.
3. **One order per owner, groups contiguous.** `sort_key` keeps its one
   order per owner and its index, `UNIQUE (owner, sort_key)`. The rows of
   a group, archived ones included, are always adjacent in that order;
   every write that places a dashboard keeps them so.
4. **The sidebar shows each group's first live dashboard.** Live
   dashboards in `sort_key` order, one entry per group: the first live
   member, by its title, linking to it. The group's live members, in the
   same order, are its tabs. Archiving the first tab makes the next one
   the entry, with no write beyond the archive itself.
5. **One owner per group.** A user dashboard cannot join a system group,
   and a system dashboard is never moved or regrouped. Refused with
   `ErrInvalid`, or the existing system-dashboard refusal.

### Placing and moving

6. **`after` names a dashboard; which list it moves in follows from
   which dashboard it names.** Without `group_id`:
   - `after: X`, X in the same group: this dashboard moves right after X
     among the tabs.
   - `after: X`, X in another group: this dashboard's whole group moves
     in the sidebar, right after X's group. Every member's key is
     rewritten in one transaction (`sortkey.Spread` between the two
     neighbouring groups).
   - `after: 0`: the whole group moves to the top of the user
     dashboards, as today.

   For a dashboard alone in its group, which is every user dashboard
   before this change, all three behave exactly as today.
7. **`group_id` names the list explicitly.**
   - `create_dashboard {group_id: G}` adds the new dashboard to G, last,
     or where `after` says: `after` must then be a member of G, and `0`
     makes it the first tab. Without `group_id`, a new dashboard is a new
     group, placed by `after` in the sidebar, as today.
   - `update_dashboard {group_id: G}` moves the dashboard into G, placed
     the same way. G may be its own group, which is how a tab moves to
     the front: `{group_id: <own group>, after: 0}`.
   - `update_dashboard {group_id: 0}` takes the dashboard out of its
     group as a new group of one (`group_id` = its own id), placed by
     `after` in the sidebar, or right after the group it left.
8. **Refusals.**
   - `after: X` with X archived, missing, or another owner's:
     `ErrInvalid`, as today.
   - `group_id: G` with no live dashboard in G, or G another owner's:
     `ErrInvalid`.
   - `group_id: G` with an `after` that is not a member of G:
     `ErrInvalid`.
   - A lost race on the order: `ErrConflict` after the existing
     `retryConflict`.

### Acting on a whole group

9. **`duplicate_dashboard`, `archive_dashboard` and `restore_dashboard`
   take exactly one of `dashboard_id` and `group_id`.** `dashboard_id`
   acts on that dashboard, `group_id` on the group. Both or neither is
   refused (`ErrInvalid`); a `group_id` with no dashboard in it is
   `ErrNotFound`, and a system group is refused by archive and restore as
   a system dashboard is. Over REST the group forms are their own routes
   (Surfaces), since the dashboard routes carry the id in the path.

### Copying

10. **`duplicate_dashboard {dashboard_id}` copies one dashboard.** A copy
    of a user dashboard joins the original's group, right after it. A
    copy of a system dashboard is a new user group, last in the sidebar
    (decision 5). Titled "… (copy)", as today.
11. **`duplicate_dashboard {group_id}` copies the group.** Every live
    member, each with its live widgets, in one transaction, as a new user
    group last in the sidebar, in the same tab order. The first copy is
    titled "… (copy)"; the others keep their titles. On group 1 this
    copies all of Reports into one editable dashboard with five tabs.
    Returns the first copy, as `get_dashboard` does, `tabs` included.

### Archiving

12. **`archive_dashboard {dashboard_id}` archives one dashboard.**
    Nothing is relinked: the row keeps its `group_id` and its key inside
    the group's block, and the sidebar entry moves to the next live
    member if this one was first (decision 4).
13. **`archive_dashboard {group_id}` archives the group**: every live
    member, in one transaction.
14. **`restore_dashboard {dashboard_id}` restores one dashboard**, in
    place: its key is still in its group's block, since moves carry
    archived members along (decision 3). **`restore_dashboard
    {group_id}` restores every archived member of the group**, in one
    transaction; a member archived on its own earlier comes back too,
    and can be archived again.
15. **The purge is unchanged.** It deletes each archived user dashboard
    with its widgets. Rows point at nothing, so nothing can dangle; a
    group whose members are all purged is simply gone.

### System dashboards

16. **Reports is group 1.** `dashboard.json` gains an optional
    `"group": <id>`; Product, Users, Groups and Retention name `1`. The
    migrator checks that the group's id is a dashboard in the same
    release, and writes `group_id` in `SyncReporting`'s transaction (a
    dashboard without `"group"` is its own group). Keys are spread in
    file order, as today, so Views comes first and names the sidebar
    entry: "Views", with the same five tabs. Renaming it is not part of
    this change.
17. **`reporting dev` honours `"group"`**, so a system tab previews as it
    will ship.

### Reading

18. **`get_dashboard` adds `group_id` and `tabs`**: the group's live
    members in order, each `{dashboard_id, title}`, the same list from
    every member. The page draws the tab bar from it.
19. **`list_dashboards` adds `group_id`.** Its order is already
    `sort_key` order, which now keeps groups together.
    `schema://dashboards` and `reporting_guide` follow, as they are built
    from it.

### Page

20. **The sidebar lists each group's first live dashboard**: system ones,
    then "Yours". An entry is active on any of its group's pages. The
    special "Reports" entry and its system-only filter go.
21. **The tab bar is `ReportTabs`, fed from `get_dashboard.tabs`,** shown
    when the group has more than one live member, system or user; a
    select on phones, as today.
22. **Selection stays per dashboard.** Moving between tabs carries the
    project and range and saves them on the tab opened (`openReport`,
    decision 35 of the reporting spec), now for every group, not only
    the system one. No server change.
23. **URLs do not change.** `/dashboards/{id}` opens that tab.

## Surfaces

| Tool | Change | REST |
| --- | --- | --- |
| `create_dashboard` | `group_id`; `after` then places among its tabs | `POST /api/dashboards` |
| `update_dashboard` | `group_id`; `after` moves a tab or the whole group, by what it names | `PATCH /api/dashboards/{dashboard_id}` |
| `duplicate_dashboard` | `dashboard_id` or `group_id` | `POST /api/dashboards/{dashboard_id}/duplicate`, new `POST /api/groups/{group_id}/duplicate` |
| `archive_dashboard` | `dashboard_id` or `group_id` | `POST /api/dashboards/{dashboard_id}/archive`, new `POST /api/groups/{group_id}/archive` |
| `restore_dashboard` | `dashboard_id` or `group_id` | `POST /api/dashboards/{dashboard_id}/restore`, new `POST /api/groups/{group_id}/restore` |
| `get_dashboard` | returns `group_id`, `tabs` | `GET /api/dashboards/{dashboard_id}` |
| `list_dashboards` | returns `group_id` | `GET /api/dashboards` |

No tool is added, removed or renamed, and no widget tool changes. The
three group routes are new; the OpenAPI route specs carry them and the
new fields.

## Migration 022

```sql
ALTER TABLE dashboards ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0;
UPDATE dashboards SET group_id = id;
CREATE INDEX dashboards_group ON dashboards (group_id);
```

Every existing dashboard becomes a group of one; the order index and the
keys do not change. The system migrator groups Reports on its next run
(the release's system hash changes with the `dashboard.json` files).
`deploy/UPGRADES.md` notes the visible change: the sidebar's "Reports"
becomes "Views", with the same five tabs.

## Documentation

`docs/reporting.md`, same commit: Concepts (groups and tabs), Tools and
HTTP API (the new fields and routes), Layout (`after` and `group_id`),
Archiving and the purge (acting on a group), Refusals and fixes (the new
refusals). `docs_sync_test` stays green.

## Tests

- **reporting (Go)**: each placement in decisions 6–7 and each refusal in
  decisions 8–9; groups stay contiguous after every move, archived
  members included; moving a group carries its archived members; a new
  dashboard's `group_id` is its id; leaving a group keeps the old group's
  id with it; `duplicate_dashboard` by dashboard and by group, from a
  user and a system group, copying no archived widgets or members;
  archive and restore by dashboard and by group; `get_dashboard.tabs` is
  the same from every member.
- **api**: the three group routes, and both-or-neither refused.
- **migrate**: `"group"` sets `group_id`; a group naming a dashboard not
  in the release fails the migration; a dashboard without it is its own
  group.
- **store**: migration 022 sets `group_id = id` on existing rows.
- **web (unit)**: the sidebar shows one entry per group, named by its
  first live member, active on every member; the tab bar shows for a
  group of two and not for a group of one.
- **e2e**: an agent-made group shows its tab bar; a duplicated group 1
  opens with five tabs; project and range carry across tabs.

## Out of scope

- Editing from the page (PR 2).
- A group name other than its first tab's title.
- Moving a whole group into another group in one call.
