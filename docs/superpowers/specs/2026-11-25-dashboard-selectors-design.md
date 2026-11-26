# Dashboard selectors

Date: 2026-11-25. Status: draft, awaiting approval.

## Intent

Today a dashboard has two switchers, project and date range, and neither is
stored: a dashboard shows one when some widget reads `:project` or
`:from`/`:to`. A dashboard cannot be narrowed by anything else while it is
being viewed. The only way to see one segment is to copy widgets with a
fixed filter: `duplicate_dashboard`/`copy_widget` with `where`, or by hand.

The motivating case is the owner's own "econumo app" dashboard (1010).
About a third of its widgets come in pairs, "Cloud · X" and
"Self-hosted · X", each with its own segment filter hard-coded. With
dashboard selectors, one widget replaces each pair, and a "Deployment"
dropdown at the top picks Cloud, Self-hosted, both, or all. The second
example is a UTM campaign picker on a marketing dashboard.

**Success:** a dashboard author (usually an agent over MCP) can add a
selector on any attribute (`attr_<key>`) or built-in filter column. A viewer
can then pick one or more of its values and have every widget that follows
the selector narrow to them, structured-query and SQL widgets alike.

## Goals

- A dashboard declares 0 to 5 **selectors**. Each one is a name, a label and
  a column.
- The column is any column a structured query's filter may name on
  `events`: a built-in one (`utm_campaign`, `host`, `country`,
  `app_version`, …), a computed text one (`device_label`, `user_status`,
  …), or `attr_<key>` for **any** attribute key, whether or not any event
  has sent it yet.
- The viewer picks **several values** at once from a checklist. Nothing
  picked means "All" and leaves the dashboard exactly as it is today.
- **Structured-query widgets follow every selector automatically.** On
  load, the server prepends `{column, op: "in", values}` to the query's
  `where`. This is the live counterpart of `narrow.go`.
- **SQL widgets opt in** by reading the selector's name as a parameter
  (`:deployment`), as they read `:project` today.
- A widget that does not follow a selector says so on its card, so a
  filtered dashboard never looks filtered where it is not.
- The selection is in the page address, so a link reproduces what the
  viewer saw.

## Non-goals

- **Named options defined by filters** ("Cloud" = host is app.econumo.com).
  The data already carries `attr_deployment = cloud | self-hosted`, so a
  value picker covers both examples. Named options can be added later as a
  second selector kind without changing this design.
- **Rewriting SQL automatically** to apply a selector. Wrapping arbitrary
  SQL breaks on CTEs, joins and the `identities` table.
- **Changing existing dashboards.** Nothing migrates or rewrites dashboard
  1010 or any other. Folding its pairs into single widgets is the owner's
  own later step.
- **Selectors on system dashboards.** They are read-only, and duplicating
  one is how you customize it. The copy can then take selectors.
- **Editing selectors in the web UI** in this version. See "Left to the
  builder / open calls".

## Anti-goals (a failure even if it works)

- A widget that silently ignores a selector while the page says it is
  filtered.
- A SQL widget filtered against its author's intent: opt-in only, never
  inferred.
- Any change in what an existing dashboard shows when no selector is set,
  or when a selector is set to "All".
- An attribute that some project lacks breaking a widget. A query widget
  over a project without `attr_x` reads no rows for a picked value, as a
  filter on a missing attribute does today. It does not error.

## Constraints from the codebase

- Layering (`CLAUDE.md`, `internal/archtest`): the work lives in
  `internal/reporting` (validation, load, cache), `internal/api` (MCP
  tools, REST), `internal/store` + `store/sqlite` (one migration),
  `internal/reporting/sqlbind` (binding a list), and `web/`.
- `sqlbind` binds **strings only**, cast to a bare type in the SQL
  (`CAST($tw_x AS Type)`), and its `typeName` check accepts only bare
  identifiers. So `sqlbind` itself changes: it accepts the one extra type
  `VARCHAR[]`, and a list goes in as text cast to it. The contract that
  only a cast is added around a parameter stays.
- Today SQL widgets may use only `:project`, `:from` and `:to`
  (`checkSQLParams`, `allowedParams`). Validation now has to know the
  widget's dashboard to accept a selector's name.
- `widget_data` already takes `filters` for a remote table's column
  filters. Selectors need a different argument name: `selectors`.
- `internal/sqlcheck` must still resolve every SQL string. The migration
  should be a plain `.sql` file.
- Docs in the same commit (`CLAUDE.md` table): `docs/reporting.md`
  (concepts, parameters, tools, HTTP API, the dashboard JSON), and
  `docs/twillingate.md` where MCP tools and REST routes are listed.
  `docs_sync_test.go` binds tool names, routes and parameters.
- Web: the pointer cursor and 360px phone rules (`cursor.spec.ts`,
  `phone.spec.ts`). A row of several selectors must wrap, not scroll.

## Approaches considered

| | What | Trade-off |
| --- | --- | --- |
| A | Only structured-query widgets follow. SQL widgets never do and carry a note. | Least new code. But on 1010 the retention block (cohort SQL that no structured query expresses) would ignore the switch. |
| **B (chosen)** | A, plus SQL widgets opt in through a named list parameter. | More work: list binding, validation tied to the dashboard, cache keys, docs. Every widget on a dashboard like 1010 can follow. |
| C | Rewrite any SQL to apply the filter. | Dropped: unsafe on CTEs, joins and `identities`. It would also filter SQL without the author asking. |

The owner picked **B**, because their most-used dashboard relies on SQL for
retention and opt-in keeps SQL from being filtered by surprise.

## Design

### The selector

Stored on the dashboard (a new `selectors` JSON column on `dashboards`,
one migration, default `[]`). Returned by `get_dashboard` and
`list_dashboards`.

```json
{"selectors": [
  {"name": "deployment", "label": "Deployment", "column": "attr_deployment"},
  {"name": "campaign",   "label": "Campaign",   "column": "utm_campaign"}
]}
```

- `name`: a lower-case identifier (`[a-z][a-z0-9_]{0,31}`), unique on the
  dashboard. It is not `project`, `from` or `to`. It is the SQL parameter
  (`:deployment`) and the key in the address and the API.
- `label`: what the dropdown shows. Defaults to the column, without
  `attr_`.
- `column`: any filter column of `events` as listed in "Structured
  queries" (`docs/twillingate.md`), or `attr_<key>` for any key. A day
  column (`day`, `*_first_seen`), a numeric one and a `def_` one are
  refused: values are text and picked from a list.
- At most 5 per dashboard.

Set through `create_dashboard` and `update_dashboard` (MCP and REST) as
`selectors`, the whole list at once (an empty list clears it). A group's
tabs each have their own selectors.

### What a widget follows

`WidgetInfo` gains `follows_selectors: ["deployment", …]`, and the
dashboard `selectors` (its declared list). Each widget reports which
selectors it follows:

- **query**: on `events`, `views`, `product`, `measures` and `checks`,
  every selector whose column the table takes (`events`, `views` and
  `product` take them all; `measures` and `checks` take the event columns
  they carry, attributes included). On `sessions`, only `utm_source`,
  `utm_medium`, `utm_campaign` and `referrer_source`, which mean the same
  thing there. On `identities`, none, because its `kind` and `day` mean
  something else. A selector the table cannot take is not followed, and
  the card says so.
- **sql**: the selectors whose `:name` its SQL reads.
- **md**: none.

`get_dashboard` reports this per widget, as it reports
`follows_project`/`follows_range` today.

### Applying it on load

`DataRequest` gains `Selectors map[string][]string`: name → picked values.
An absent name, or an empty list, means "All".

- **query widget**: for each followed selector with values, prepend
  `{column, op: "in", values}` to `query.where`, ahead of the widget's own
  filters. The same code path as `narrowed()`, applied per load rather
  than at copy time. On a funnel this narrows every step (the query's
  `where` already does). On a `paths` query it narrows the steps, as
  `where` does.
- **sql widget**: `:name` binds to a `VARCHAR[]` of the picked values, or
  an **empty list** for "All", never NULL. The documented idiom is:

  ```sql
  AND (len(:deployment) = 0 OR list_contains(:deployment, attr_deployment))
  ```

  The builder picks the list's text encoding and the cast that DuckDB
  parses safely (quotes, commas, brackets in values). It must round-trip
  any string, and a test must cover hostile values.
- **Cache key**: includes the followed selectors' sorted values, as it
  includes project and range only when followed.
- A name the widget's dashboard does not declare is refused (`ErrInvalid`,
  "this dashboard has no selector x"). Values for a declared selector the
  widget does not follow are ignored, so a page can send one `selectors`
  object to every widget.

`widget_data` (MCP) and `GET /api/widgets/{id}/data` take `selectors` as a
JSON object (`{"deployment": ["cloud"]}`). `POST /api/widgets/stream` is a
list of independent items with no dashboard, so `selectors` goes **on each
item**, beside its `project_id`, `from` and `to`, and is checked against
that item's widget's dashboard. A request that mixes dashboards keeps
working.

`preview_widget` (MCP) and its REST route gain an optional `dashboard_id`
and `selectors`. With `dashboard_id`, a SQL draft may read that
dashboard's selector names, and binds the given values (or empty lists).
Without it, only `:project`, `:from` and `:to`, as today. The widget
editor passes the dashboard it edits on, and the page's current
selection.

### Validation on write

- **SQL widget**: may read `:project`, `:from`, `:to`, and the names of its
  dashboard's selectors. Any other name is refused, and the message names
  the selectors the dashboard has. The write check runs the SQL once with
  every selector at "All" (empty lists). The builder decides whether it
  also runs once with a sample value.
- **Removing a selector, or renaming its `name`,** while a live SQL widget
  on the dashboard reads it: refused (`ErrConflict`), naming the widgets.
  Changing only `label` or `column` is allowed. Query widgets need nothing.
- **`copy_widget` of a SQL widget** to a dashboard without the selectors
  it reads: refused, naming the missing ones. (`update_widget` cannot move
  a widget between dashboards, so it needs no check.)
- **`duplicate_dashboard`** copies selectors with the dashboard. A
  `whole_group` copy copies each tab's.
- Restoring an archived SQL widget whose selector has since been removed:
  refused, same message.

### Values for the checklist

A new read, `selector_values` (MCP) and
`GET /api/dashboards/{id}/selectors/{name}/values` (REST), with
`project_id`, `from`, `to` and an optional `search`. It returns the
column's values over that project and range, by event count, the busiest
first, up to 100, through the native engine (a `breakdown` on `events`, or
the selector column's table). This keeps it as cheap as any widget.

Which projects: the page passes them, one or more `project_id`s, and the
values are merged across them (one native call each, capped at 10
projects). The page sends the switcher's project when the dashboard shows
the switcher, plus every project a query widget on it fixes
(`"project_id": 9`). When that set is empty (only SQL widgets that read
every project), it sends every live project, up to the cap, newest first. Values already picked always stay in the list, even when absent
from the range, so a picked value can be unticked.

### The page

- Next to the range and project switchers, one button per selector:
  "Deployment: All", "Deployment: cloud", or "Campaign: 3 selected". It
  opens a checklist with a search box when there are more than about 10
  values, "Clear" at the top, and the counts from `selector_values`.
- The selection lives in the address: `?s.deployment=cloud&s.campaign=a,b`.
  The builder picks the exact encoding, which must round-trip commas.
  Unlike the range, it is **not** kept in browser storage. A dashboard
  opened from the sidebar starts at "All". Reason: a forgotten filter on a
  dashboard is worse than a forgotten range, because the numbers look
  plausible.
- A widget card shows a small note when a selector is set that the widget
  does not follow ("Not filtered by Deployment"), so nothing looks
  filtered that is not.
- The same selectors show on a project page's tab, where the dashboard is
  shown as a project tab.
- **Widget shares and Download PNG** capture the card as it looks on
  screen (`web/src/lib/capture.ts`), so a share made under a selection
  shows the filtered numbers. The caption names the selection the way it
  already names project and range: the share stores the selection it was
  made under (only the selectors the widget follows, with values), and the
  card caption reads, for example, "Deployment: cloud". A share made at
  "All" stores and captions nothing. This needs a column on the shares
  table, in the same migration.
- Phone (360px): the switcher row wraps. A long label truncates with an
  ellipsis, and the checklist is a full-width sheet.

### Docs

- `docs/reporting.md`:
  - "Concepts": a widget follows selectors, as it follows switchers.
  - "Parameters and ranges": selector parameters, the `len(...) = 0 OR
    list_contains(...)` idiom, and the empty list for "All".
  - "Tools" and "HTTP API": `selectors` on `create_dashboard`,
    `update_dashboard`, `widget_data` and the stream, plus the new
    `selector_values`.
  - A worked example: a Deployment selector, a query widget and a SQL
    widget both following it.
- `docs/twillingate.md`: tool and route tables, where they list reporting
  routes.

## Left to the builder / open calls

Settled by me, as the designer, and open to the owner's correction:

1. **Address, not storage**, for the selection (above).
2. **No web UI for adding or editing selectors** in this version. Agents
   add them over MCP, as they build most dashboards here. The dashboard
   menu could gain "Selectors…" later.
3. **Shares capture what is on screen**, captioned with the selection,
   following the existing project and range captions.
4. **The stream and preview take `selectors` per request item**, and
   preview takes `dashboard_id`.
5. **`identities` follows no selector, and `sessions` follows only the
   UTM and referrer columns.**
6. **Five selectors, 100 values**, as limits.

Left to the builder:

- The list's text encoding and cast for `sqlbind`, and its tests.
- The exact address encoding.
- Whether the write check also runs a SQL widget with a sample value.
- Component layout of the checklist, within the cursor and phone rules.
