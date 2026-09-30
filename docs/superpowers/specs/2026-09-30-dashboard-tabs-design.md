# Dashboard tabs

Status: draft
Date: 2026-09-30

## Sequencing

Two PRs:

1. **Tabs for agents** (this spec) — `feat(reporting)`: a dashboard can
   have tabs, agents build and copy them over MCP and REST, and the page
   shows them. Nothing that exists changes meaning, so it is not breaking.
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

1. **A tab is a dashboard with a parent.** `dashboards` gains a nullable
   `parent_id`. A dashboard with `parent_id` NULL is top level: it is a
   sidebar entry. A dashboard with a parent is a tab of that parent. No
   new table, no new kind of row: widgets, the stored selection, `after`,
   archiving and every widget tool work on a tab exactly as on a
   dashboard today.
2. **The parent is the first tab.** Its widgets are the first tab's, and
   its title is both the sidebar entry and that tab's label. Its children
   follow in sort-key order. There is no container state (a parent with
   no widgets of its own is simply an empty first tab).
3. **No foreign key on `parent_id`.** Every rule is enforced in Go (as
   the rest of reporting is, 021's header), in one transaction per
   multi-row change. The invariant: **a live tab never has an archived
   parent.** It follows from decisions 11–13, and it is what makes the
   purge safe without a cascade (decision 14).
4. **One level.** A parent must be top level; a dashboard that has
   children (archived ones included) cannot be given a parent. Refused
   with `ErrInvalid`.
5. **One owner.** A tab has its parent's owner. A user dashboard cannot be
   put under a system one, and a system dashboard is never moved.
   Refused with `ErrInvalid` (user under system) or the existing
   system-dashboard refusal.
6. **Order is per set of siblings.** The order index becomes
   `UNIQUE (owner, IFNULL(parent_id, 0), sort_key)`: top-level dashboards
   keep the keys they have, and each parent's children have their own
   order. `dashboardKey` places among siblings instead of among every
   user dashboard.

### Placing and moving

7. **`create_dashboard` takes an optional `parent_id`.** Given, the new
   dashboard is a tab of that parent, placed by `after` among the tabs
   (decision 8); omitted, it is top level, as today. A parent that is
   archived is refused (`ErrConflict`), keeping the invariant.
8. **`after` orders whatever list the dashboard is in.** On a top-level
   dashboard it is the sidebar, as today. On a tab it is the tab bar,
   where the parent is always first: `after: <parent id>` puts the tab
   right after the parent, `after: <sibling id>` after that sibling, and
   **`after: 0` makes it the first tab, which means promoting it**: in
   one transaction it takes the old parent's `parent_id` (NULL) and
   sidebar `sort_key`, and the old parent and every other child
   (archived ones included, so decision 4 holds for them too) move under
   it, the old parent right after it and the rest keeping their order.
9. **`update_dashboard` takes an optional `parent_id`.** `N` moves the
   dashboard under N as its last tab, or where `after` says. `0` takes a
   tab out to the top level, placed last among the owner's dashboards or
   where `after` says. Moving a dashboard that has children is refused
   (decision 4), and so is moving one under an archived parent
   (`ErrConflict`).

### Copying

10. **`duplicate_dashboard` copies what it is given:**
    - **a parent**: the parent and every live child, each with its live
      widgets, in one transaction. The copy is a user dashboard at the
      top level; the new parent is titled "… (copy)", its tabs keep their
      titles. Works on a system parent, so Views (Reports) copies as one
      dashboard with five tabs.
    - **a tab of a user dashboard**: that tab alone, titled "… (copy)",
      placed right after it under the same parent.
    - **a tab of a system dashboard**: that tab alone, as a top-level
      user dashboard (decision 5 forbids putting it under the system
      parent).
    - **a dashboard with no tabs**: as today.

### Archiving

11. **`archive_dashboard` archives one dashboard by default.**
    - On a tab: that tab.
    - On a parent with live tabs: the next live tab is promoted
      (decision 8's promotion), then the old parent, now a tab, is
      archived. The dashboard stays in the sidebar under the promoted
      tab's title.
    - On a dashboard with no live tabs: that dashboard, as today.
12. **`archive_dashboard` with `with_tabs: true` archives the whole
    dashboard**: on a top-level dashboard, it and every live child, all
    with the same `archived_at`, in one transaction. On a tab it is refused
    (`ErrInvalid`: "dashboard N is a tab; archive its parent M with
    with_tabs"), so an agent never archives more than it named.
13. **`restore_dashboard` mirrors it.** By default it restores the one
    dashboard. `with_tabs: true` (refused on a tab, as in decision 12) on
    a top-level dashboard also restores the children
    whose `archived_at` equals the parent's, so a tab archived on its own
    earlier stays archived. Restoring a tab whose parent is archived is
    refused (`ErrConflict`: "restore dashboard M first").
14. **The purge is unchanged.** It deletes each archived user dashboard
    in its own transaction, with its widgets by their existing foreign
    key. By the invariant (decision 3), a tab is archived no later than
    its parent, so it is purged no later: no live row ever points at a
    purged parent. A `parent_id` can dangle only on an archived row
    between two passes, and nothing reads it.

### System dashboards

15. **Views is the parent of the other four.** `dashboard.json` gains an
    optional `"parent": <id>`; Product, Users, Groups and Retention name
    `1`. The system migrator checks that the parent is in the same
    release, is top level, and is not itself a tab, and writes
    `parent_id` in `SyncReporting`'s transaction. The sidebar entry is
    "Views"; renaming it is not part of this change.
16. **`reporting dev` previews a directory with `"parent"`** as a tab of
    that parent, so a system tab is seen as it will ship.

### Reading

17. **`get_dashboard` adds `parent_id` and `tabs`**: the top-level
    dashboard and its live children in order, each `{dashboard_id,
    title}`, the same list whichever tab was asked for. Empty when the
    dashboard has no live tabs. The page draws the tab bar from it.
18. **`list_dashboards` adds `parent_id`** and lists each parent's
    children right after it, so the order stays sidebar order with tabs
    in place. `schema://dashboards` and `reporting_guide` follow, as they
    are built from it.

### Page

19. **The sidebar lists live top-level dashboards**: system ones, then
    "Yours". An entry is active on its own page and on any of its tabs'.
    The special "Reports" entry and its system-only filter go.
20. **The tab bar is `ReportTabs`, fed from `get_dashboard.tabs`,** shown
    for any dashboard with at least one live tab, system or user; a
    select on phones, as today.
21. **Selection stays per dashboard.** Moving between tabs carries the
    project and range and saves them on the tab opened (`openReport`,
    decision 35 of the reporting spec), now for every tabbed dashboard,
    not only system ones. No server change.
22. **URLs do not change.** `/dashboards/{id}` opens that tab.

## Surfaces

| Tool / route | Change |
| --- | --- |
| `create_dashboard` / `POST /api/dashboards` | `parent_id` |
| `update_dashboard` / `PATCH /api/dashboards/{id}` | `parent_id`; `after` orders tabs on a tab |
| `duplicate_dashboard` / `POST …/duplicate` | copies a parent's tabs (decision 10) |
| `archive_dashboard` / `POST …/archive` | `with_tabs` |
| `restore_dashboard` / `POST …/restore` | `with_tabs` |
| `get_dashboard` | `parent_id`, `tabs` |
| `list_dashboards` | `parent_id`, children after their parent |

No tool is added, removed or renamed. The OpenAPI route specs carry the
new fields.

## Migration 022

```sql
ALTER TABLE dashboards ADD COLUMN parent_id INTEGER;
DROP INDEX dashboards_order;
CREATE UNIQUE INDEX dashboards_order
    ON dashboards (owner, IFNULL(parent_id, 0), sort_key);
```

No rows move in SQL: every existing dashboard is top level, and the
system migrator sets the four system parents on its next run (the
release's system hash changes with the `dashboard.json` files).
`deploy/UPGRADES.md` notes the visible change: the sidebar's "Reports"
becomes "Views", with the same five tabs.

## Documentation

`docs/reporting.md`, same commit: Concepts (tabs, the parent is the first
tab), Tools and HTTP API (the new fields), Layout (`after` on a tab,
promotion), Archiving and the purge (`with_tabs`, the restore rule),
Refusals and fixes (the new refusals). `docs_sync_test` stays green.

## Tests

- **reporting (Go)**: each rule in decisions 4–13 with its refusal;
  promotion moves archived children too; duplicating Views copies five
  tabs and their live widgets and none of the archived ones; archive and
  restore with and without `with_tabs`, including a tab archived earlier
  staying archived; `get_dashboard.tabs` is the same from every tab.
- **migrate**: a `"parent"` in a system directory sets `parent_id`; a
  parent that is missing, is a tab, or is not in the release fails the
  migration.
- **purge**: a parent and its tabs archived together purge in one pass;
  a tab archived earlier purges first.
- **web (unit)**: sidebar lists top-level only and marks the entry
  active on a tab; the tab bar shows for a user dashboard with tabs and
  not for one without.
- **e2e**: an agent-made dashboard with tabs shows its tab bar; a
  duplicated Views opens with five tabs; project and range carry across
  tabs.

## Out of scope

- Editing from the page (PR 2).
- A name for the Views group other than its first tab's title.
- Nesting deeper than one level.
- Moving a dashboard together with its tabs under another dashboard.
