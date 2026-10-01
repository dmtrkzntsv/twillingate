# Reporting

How an AI agent builds and changes the dashboards twillingate serves at `/app/`.
The page is read-only: a person opens it, picks a project and a date range, and
reads. Agents are its only authors, through the tools and routes below. The MCP
endpoint serves this file verbatim as `docs://reporting`; `reporting_guide`
returns its live parts (components, views, projects, dashboards) with its
[Workflow](#workflow) and [Rules](#rules) in one call.

Collecting data and querying it with `query` are in
[twillingate.md](twillingate.md); running the collector and enabling the API
are in [deployment.md](deployment.md).

- [Concepts](#concepts)
- [Workflow](#workflow)
- [Rules](#rules)
- [Tools](#tools)
- [HTTP API](#http-api)
- [Components](#components)
- [Examples](#examples)
- [Parameters and ranges](#parameters-and-ranges)
- [Layout](#layout)
- [Archiving and the purge](#archiving-and-the-purge)
- [Refusals and fixes](#refusals-and-fixes)
- [When widgets break after an update](#when-widgets-break-after-an-update)
- [Local development](#local-development)

---

## Concepts

**A dashboard** is a titled, ordered list of widgets. Its `owner` is `system` or
`user`:

- **System dashboards** ship with each release, have ids 1–999, and change only
  when the release does. A system group is archived and restored whole, with
  `whole_group`, and is never purged; every other write refuses them. To
  customize one, call `duplicate_dashboard`: the copy is a user dashboard you
  can edit. Duplicating never archives anything; to take the system group out
  of the sidebar, `archive_dashboard` it with `whole_group` too.
- **User dashboards** are what agents create. Their ids start at 1001.

**A dashboard is in a group.** Dashboards sharing a `group_id` are one
sidebar entry, drawn as tabs in tab order, named by the group's first live
dashboard; a group of one shows no tab bar. A dashboard made on its own
starts as a group of one, its `group_id` its own id; beyond that, a
`group_id` is just a number a group's dashboards share — read it from
`list_dashboards` or `get_dashboard`, never assume it names a member.
System dashboards are one group (`group_id` 1): its sidebar entry reads
"Views", with tabs Views · Product · Users · Groups · Retention.

**A widget** is a component plus a source:

- the **component** is how it is drawn (`line`, `stat`, `table`, …; see
  [Components](#components));
- the **source** is `{"type": "sql" | "md", "content": "…"}`: a read-only query
  against the `v_*` views, or Markdown text. The component says which source
  types it accepts and which columns its query must return.

A widget also has a `name`, unique on its dashboard (derived from the title
when you omit it: lower case, `-` for each run of other characters, `-2`, `-3`,
… added when taken, `widget` when nothing is left), an optional `title` (the
card shows a header only when there is one), `props` (the component's options,
checked against its props schema), and a `width` and `height` (see
[Layout](#layout)).

**A widget's SQL decides what it follows.** A query that uses `:project`
follows the page's project switcher; one that uses `:from` or `:to` follows the
range switcher; one that uses neither is fixed, and shows whatever its SQL
names. A dashboard shows a switcher only when at least one of its live widgets
follows it. `get_dashboard` reports `follows_project` and `follows_range` for
the dashboard and for each widget.

**Every write validates the whole result** and refuses with a message that
says what to change: the component exists and accepts the source type; the SQL
uses only the three parameters, runs, and returns the columns the component
reads (checked on a sample project and range); Markdown is not empty; props
match the schema; the size is in range. Refusals come back as tool errors over
MCP and as `400 invalid`, `404 not_found` or `409 conflict` over HTTP. Every
write except the viewer's selection is recorded in the audit log with the edge
it came through (`mcp` or `api`).

**Loading a widget** (`widget_data`) answers in one envelope, whatever the
component:

```json
{ "widget_id": 42, "source_type": "sql",
  "project_id": 7, "from": "2026-08-27", "to": "2026-09-25",
  "cached_at": "2026-09-25T10:00:00Z", "refresh_after": "2026-09-25T10:01:00Z",
  "removed": false,
  "data": { "columns": ["x", "y"], "rows": [["2026-08-27", "40"]], "truncated": false } }
```

- `project_id` appears only for a widget that follows the project, `from` and
  `to` only for one that follows the range; both echo the values applied.
- `data` is `{columns, rows, truncated}` for `sql` (every value a string, as
  `query` returns them) and `{markdown}` for `md`.
- `truncated: true` means the result hit `API_QUERY_MAX_ROWS`: group the query
  or narrow it.
- `removed: true` with `data: null` means the widget's component left the code
  in a release; switch it to another with `update_widget`, or archive it.
- A query that no longer runs, or rows that no longer fit the component, is a
  refusal (never data) ending "if this started after an update, see the
  release notes at https://github.com/dmtrkzntsv/twillingate/releases".

`sql` results are cached per widget and per value it follows. An ordinary load
reuses a result up to `REPORTING_CACHE_SECONDS` old (default 900); `fresh=true`
reuses one only up to `REPORTING_REFRESH_SECONDS` old (default 60), and
`refresh_after` says when a fresh load would run the query again. Changing a
widget's source starts it on new entries. Widget queries run under the same
guards as `query`: read-only, `API_QUERY_TIMEOUT` and `API_QUERY_MAX_ROWS`.

## Workflow

1. **Call `reporting_guide`.** It names the running version, the components
   and the columns each one reads, the views, the active projects and the
   dashboards there are.
2. **Explore with `query`** against the `v_*` views (`schema://views` lists
   their columns), for one project and a few days, until the numbers are the
   ones the widget should show.
3. **Pick a component** and alias the query's columns to its inputs:
   `SELECT day AS x, SUM(views) AS y …` for `line`. [Examples](#examples) has
   one query per component.
4. **Choose what the widget follows:** `WHERE project_id = :project` follows
   the project switcher, `day BETWEEN :from AND :to` the range switcher;
   without them the widget is fixed, and its title says to what ("Signups,
   all time").
5. **Write it.** `create_dashboard` with its widgets in order, or `add_widget`
   on an existing user dashboard with `after`, `width` and `height`.
   `create_dashboard` with `group_id` adds the dashboard as a tab of that
   group. To change a system dashboard, `duplicate_dashboard` it with
   `whole_group`, work on the copy, then `archive_dashboard` the original
   with `whole_group` to take it out of the sidebar.
6. **Check it with `widget_data`** for a project and a range, as the page
   loads it: the envelope echoes what the widget followed, and a refusal says
   what to change.
7. **Fix with `update_widget`; undo with `archive_widget`**, which
   `restore_widget` reverses. Nothing is deleted on request.

## Rules

- System dashboards (ids 1–999) change only with a release:
  `archive_dashboard`/`restore_dashboard` with `whole_group` hide or show a
  system group, `duplicate_dashboard` makes an editable copy (it never
  archives anything), and `copy_widget` copies one system widget onto a user
  dashboard.
- A query returns exactly the columns the component reads, named by alias; a
  column marked optional may be left out, and only `table` takes any columns.
- The only parameters are `:project`, `:from` and `:to`. `day` is text,
  `YYYY-MM-DD` in UTC: compare it as a string (`day BETWEEN :from AND :to`).
- Read the `v_*` views. Custom SQL may read any table except `meta` and
  SQLite's own (`sqlite_*`, `pragma_*`, `dbstat`), and only reads. SQLite
  takes a quoted string as a name, so a single-quoted string that is one
  of those names (`'meta'`, `'sqlite_master'`) is refused too; `'metadata'`
  and `'%meta%'` pass.
- Unquoted names are ASCII: quote any other (`"визиты"`). Strings and
  comments may hold any text.
- Group in SQL: a widget gets at most `API_QUERY_MAX_ROWS` rows (default
  1000) and `API_QUERY_TIMEOUT` (default 10s). A result cut at the cap draws
  as "partial". A pie keeps to about seven slices, the rest summed as
  `Other`.
- A widget that follows neither switcher says in its title what it is fixed
  to.
- Widgets are not reordered once placed: to move one, `copy_widget` it with
  the `after` you want and archive the original. Names are unique on a
  dashboard, archived widgets included.
- Markdown renders without raw HTML.
- Undo is archiving. Archived user dashboards and widgets are deleted
  `RETENTION_ARCHIVED_DAYS` after archiving (default 30); an archived system
  group is never purged.

## Tools

| Tool | Input | Returns |
| --- | --- | --- |
| `reporting_guide` | none | markdown: the running version and its release notes, the source types and components, the views, the active projects and the dashboards, and this document's [Workflow](#workflow) and [Rules](#rules). MCP only |
| `list_components` | none | `source_types` and `components`: each one's `description`, `accepts`, `inputs`, `props` schema, `default_width` and `default_height` |
| `list_dashboards` | none | `timezone` and `dashboards` in sidebar order (system, then user), archived ones included: `dashboard_id`, `title`, `owner`, `group_id`, stored `project_id` and `range`, live `widgets` count, `archived_at`; plus `purge_after_days`, how long an archived user dashboard is kept before it is deleted (absent: kept forever) |
| `get_dashboard` | `dashboard_id` | the dashboard, its `group_id`, its `follows_project` and `follows_range`, its `tabs` (the group's live dashboards, this one included, in tab order), and its live `widgets` in order |
| `list_widgets` | `dashboard_id`, `component` (both optional; they combine) | `widgets`, archived ones included, each with its `dashboard` and 1-based `position` there |
| `widget_data` | `widget_id`, `project_id`, `from`, `to`, `fresh` | the envelope above |
| `create_dashboard` | `title`, `range` (default `7d`), `group_id`, `after`, `widgets` | the new dashboard, as `get_dashboard` returns it; one invalid widget creates nothing |
| `update_dashboard` | `dashboard_id`, `title`, `group_id`, `after` | the dashboard, as `list_dashboards` lists it |
| `duplicate_dashboard` | `dashboard_id`, `whole_group` | a user copy with copies of its live widgets. A user dashboard's copy joins its group as the next tab; a system dashboard's copy is a new dashboard last in the sidebar, also from an archived system dashboard. An archived user dashboard is refused (restore it first). `whole_group` copies the group as a new dashboard with the same tabs: a system group whole, a user group's live tabs. Duplicating never archives: to replace a system group, `archive_dashboard` it with `whole_group` |
| `archive_dashboard` | `dashboard_id`, `whole_group` | hides it (`whole_group`: every live member of its group); see [Archiving and the purge](#archiving-and-the-purge) |
| `restore_dashboard` | `dashboard_id`, `whole_group` | unhides it (`whole_group`: every archived member of its group) |
| `add_widget` | `dashboard_id`, `component`, `source`, `title`, `name`, `props`, `width`, `height`, `after` | the widget |
| `update_widget` | `widget_id` and any of `name`, `component`, `title`, `props`, `source`, `width`, `height` | the widget; omitted fields are kept |
| `copy_widget` | `widget_id`, `dashboard_id`, `after` | the independent copy, same size; the original may be on a system dashboard |
| `archive_widget` | `widget_id` | hides it in place |
| `restore_widget` | `widget_id` | puts it back where it was |

`create_dashboard`, `add_widget`, `update_widget` and `copy_widget` expect you
to have read `reporting_guide` first: it names the components, the views and
the projects you are writing against.

**Resources:** `docs://reporting` (this document), and three live JSON
snapshots built on each read by the same code as their list tool:
`schema://components` (`list_components`), `schema://dashboards`
(`list_dashboards`) and `schema://widgets` (`list_widgets`, unfiltered).

## HTTP API

Every tool above except `reporting_guide`, which is MCP-only, is also a REST
route under `/api/`, with the same bearer token, JSON and error shape as the
routes in [twillingate.md](twillingate.md#http-api). One route has no tool:
the page stores the viewer's selection with `PUT …/view`, which is viewer
state rather than a definition, so it is allowed on system dashboards and not
audited.

| Method | Path | Mirrors | Input |
|---|---|---|---|
| `GET` | `/api/components` | `list_components` | — |
| `GET` | `/api/dashboards` | `list_dashboards` | — |
| `GET` | `/api/dashboards/{dashboard_id}` | `get_dashboard` | — |
| `GET` | `/api/widgets` | `list_widgets` | query: `dashboard_id`, `component` |
| `GET` | `/api/widgets/{widget_id}/data` | `widget_data` | query: `project_id`, `from`, `to`, `fresh` |
| `POST` | `/api/dashboards` | `create_dashboard` | body: `title`, `range`, `group_id`, `after`, `widgets` → 201 |
| `PATCH` | `/api/dashboards/{dashboard_id}` | `update_dashboard` | body: `title`, `group_id`, `after` |
| `POST` | `/api/dashboards/{dashboard_id}/duplicate` | `duplicate_dashboard` | optional body `{whole_group}` → 201 |
| `POST` | `/api/dashboards/{dashboard_id}/archive` | `archive_dashboard` | body: `whole_group` (optional) |
| `POST` | `/api/dashboards/{dashboard_id}/restore` | `restore_dashboard` | body: `whole_group` (optional) |
| `POST` | `/api/dashboards/{dashboard_id}/widgets` | `add_widget` | body: the widget, `after` → 201 |
| `PATCH` | `/api/widgets/{widget_id}` | `update_widget` | body: fields to change |
| `POST` | `/api/widgets/{widget_id}/copy` | `copy_widget` | body: `dashboard_id`, `after` → 201 |
| `POST` | `/api/widgets/{widget_id}/archive` | `archive_widget` | — |
| `POST` | `/api/widgets/{widget_id}/restore` | `restore_widget` | — |
| `PUT` | `/api/dashboards/{dashboard_id}/view` | `view`, REST only: no MCP tool | body: `project_id`, `range`, and `from`/`to` for `custom` → `{"status":"saved"}` |

The view route takes `project_id` exactly when the dashboard has a project
switcher and `range` exactly when it has a range switcher, and refuses either
when the dashboard has none. It stores the preset, not its dates, so "Last
week" stays rolling.

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "https://t.example.com/api/widgets/42/data?project_id=1&from=2026-09-01&to=2026-09-13"
```

## Components

Each component reads the columns its query returns, by name: alias them
(`SELECT day AS x, SUM(visitors) AS y …`). A column type is `number`, `text` or
`day` (`YYYY-MM-DD`). A column marked optional may be left out; a column the
component does not read is refused, except by `table`, which shows whatever
the query returns. `format` is `number`, `percent` or `duration` everywhere it
appears. `width` and `height` default to the component's size below.

| Component | Accepts | Inputs: the columns the query returns | Props | Default width × height |
| --- | --- | --- | --- | --- |
| `stat` | `sql` | `value` number; `previous` number, optional (shows the change); `x` day, optional (a sparkline under the number, which becomes the series' `aggregate`) | `format`, `aggregate` (`sum`, `last`, `avg`) | 3 × 3 |
| `line` | `sql` | `x` day or text; `y` number; `series` text, optional (one line per value) | `format`, `curve` (`linear`, `monotone`, `step`) | 6 × 8 |
| `area` | `sql` | as `line` | `format`, `curve`, `stacked` | 6 × 8 |
| `bar` | `sql` | `x` text or day; `y` number; `series` text, optional | `format`, `horizontal`, `stacked` | 6 × 8 |
| `bar_list` | `sql` | `label` text; `value` number (a ranked list with an inline bar) | `format` | 6 × 8 |
| `pie` | `sql` | `label` text; `value` number; keep it to ~7 slices and group the rest as 'Other' in SQL | `format`, `donut` | 4 × 8 |
| `radar` | `sql` | `axis` text; `value` number; `series` text, optional | `format` | 4 × 8 |
| `radial` | `sql` | `label` text; `value` number; `max` number, optional (a ring toward a target) | `format` | 4 × 8 |
| `scatter` | `sql` | `x` number; `y` number; `series` text, optional; `size` number, optional | `format` | 6 × 8 |
| `funnel` | `sql` | `step` text; `value` number, in query order | `format` | 6 × 8 |
| `combo` | `sql` | `x` day or text; `bar` number; `line` number (two units on two axes) | `bar_format`, `line_format` | 6 × 8 |
| `heatmap` | `sql` | `x` text or day; `y` text or day; `value` number (a retention grid: cohort day × days since) | `format`, `labels` (print values in cells) | 6 × 10 |
| `calendar` | `sql` | `day` day; `value` number (a year of days) | `format` | 12 × 4 |
| `map` | `sql` | `country` text, ISO alpha-2; `value` number (unknown codes are listed under the map) | `format` | 6 × 8 |
| `treemap` | `sql` | `label` text; `value` number; `parent` text, optional (two levels, e.g. browser → version) | `format` | 6 × 8 |
| `table` | `sql` | any columns, shown in query order until a viewer sorts by a header | `formats` (column → format), `colorscale` (columns shaded by value) | 6 × 10 |
| `markdown` | `md` | none | none | 12 × 2 |

`list_components` is the authority: it returns each component's props as a
JSON schema, and its `description` says when to use it.

The dashboard app draws every component with sample data at
`/app/gallery/components`, with each one's contract and a copyable
`add_widget` snippet: point the owner there to pick one by name.

**When a release removes a component,** its widgets stay, with `component`
null: the card says "component removed" and `widget_data` answers
`removed: true`. Such a widget can be switched to another component with
`update_widget`, resized or archived; nothing else about it can change, and it
cannot be copied, until it has a component again. A component that comes back
in a later release does not reattach itself.

## Examples

One widget per component, each ready for `add_widget`: the component, the
source (the block below, as `{"type": "sql", "content": "…"}`, or `md` for
the Markdown one) and the props shown. Unless the title says otherwise, each
follows both switchers.

### `stat`

Views in the range, with a sparkline of the days under the number:

```sql
SELECT day AS x, SUM(views) AS value
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY day
ORDER BY day
```

Props: `{"format": "number", "aggregate": "sum"}`

### `line`

Visitors per day, one line per kind (`web`, `app`, …):

```sql
SELECT day AS x, kind AS series, visitors AS y
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to
ORDER BY day
```

Props: `{"curve": "monotone"}`

### `area`

Views per day, stacked by platform:

```sql
SELECT day AS x, platform AS series, views AS y
FROM v_views_platforms
WHERE project_id = :project AND day BETWEEN :from AND :to
ORDER BY day
```

Props: `{"stacked": true}`

### `bar`

The ten most frequent product events, as horizontal bars:

```sql
SELECT event_name AS x, SUM(count) AS y
FROM v_product_daily
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY event_name
ORDER BY y DESC
LIMIT 10
```

Props: `{"horizontal": true}`

### `bar_list`

Top pages by views:

```sql
SELECT path AS label, SUM(views) AS value
FROM v_views_paths
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY path
ORDER BY value DESC
LIMIT 10
```

Props: `{"format": "number"}`

### `pie`

Visitors by device, the six largest and the rest as `Other`:

```sql
WITH d AS (
  SELECT device, SUM(visitors) AS v
  FROM v_views_devices
  WHERE project_id = :project AND day BETWEEN :from AND :to
  GROUP BY device
), ranked AS (
  SELECT device, v, ROW_NUMBER() OVER (ORDER BY v DESC) AS n FROM d
)
SELECT CASE WHEN n <= 6 THEN device ELSE 'Other' END AS label, SUM(v) AS value
FROM ranked
GROUP BY label
ORDER BY value DESC
```

Props: `{"donut": true}`

### `radar`

Views by day of the week:

```sql
SELECT substr('SunMonTueWedThuFriSat', 1 + 3 * strftime('%w', day), 3) AS axis,
       SUM(views) AS value
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY strftime('%w', day)
ORDER BY strftime('%w', day)
```

Props: `{"format": "number"}`

### `radial`

"Signups this month, goal 100": follows the project, fixed to the calendar
month:

```sql
SELECT 'Signups' AS label, COALESCE(SUM(count), 0) AS value, 100 AS max
FROM v_product_daily
WHERE project_id = :project AND event_name = 'signup'
  AND day >= date('now', 'start of month')
```

Props: `{"format": "number"}`

### `scatter`

Pages, visitors against views:

```sql
SELECT SUM(visitors) AS x, SUM(views) AS y
FROM v_views_paths
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY path
```

Props: `{"format": "number"}`

### `funnel`

Visitors, then users who signed up, then users who subscribed. The steps
come in query order, so the query orders them and returns only `step` and
`value`:

```sql
SELECT step, value FROM (
  SELECT 1 AS n, 'Visited' AS step, COALESCE(SUM(visitors), 0) AS value
  FROM v_views_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to
  UNION ALL
  SELECT 2, 'Signed up', COALESCE(SUM(unique_users), 0)
  FROM v_product_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to AND event_name = 'signup'
  UNION ALL
  SELECT 3, 'Subscribed', COALESCE(SUM(unique_users), 0)
  FROM v_product_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to AND event_name = 'subscribed'
)
ORDER BY n
```

Props: `{"format": "number"}`

### `combo`

Visitors as bars and the bounce rate as a line, on two axes. `percent`
formats a fraction, so `0.25` reads 25%:

```sql
SELECT day AS x, SUM(visitors) AS bar,
       SUM(bounces) * 1.0 / NULLIF(SUM(sessions), 0) AS line
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY day
ORDER BY day
```

Props: `{"bar_format": "number", "line_format": "percent"}`

### `heatmap`

Retention of signed-in users: cohorts that started in the range, by days
since. A cohort's later days are absent until they have happened:

```sql
SELECT cohort_day AS x, day_offset AS y, actors * 1.0 / cohort_size AS value
FROM v_retention
WHERE project_id = :project AND actor_kind = 'user'
  AND day_offset IN (0, 1, 7, 14, 30)
  AND cohort_day BETWEEN :from AND :to
ORDER BY cohort_day, day_offset
```

Props: `{"format": "percent", "labels": true}`

### `calendar`

"Views, last 12 months": follows the project, fixed to the last year:

```sql
SELECT day, SUM(views) AS value
FROM v_views_daily
WHERE project_id = :project AND day >= date('now', '-364 days')
GROUP BY day
ORDER BY day
```

Props: `{"format": "number"}`

### `map`

Visitors by country:

```sql
SELECT country, SUM(visitors) AS value
FROM v_views_countries
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY country
```

Props: `{"format": "number"}`

### `treemap`

Visitors by browser, then version:

```sql
SELECT browser AS parent, browser_version AS label, SUM(visitors) AS value
FROM v_views_browsers
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY browser, browser_version
```

Props: `{"format": "number"}`

### `table`

Pages with visitors and views, the views column shaded. Column names are
the headers, so quote them as they should read. The query's `ORDER BY` is
the order a table opens in; a viewer who clicks a header sorts the rows on
the page (a third click restores query order), and that browser remembers
the sort per widget. It sorts only the rows returned, so a `LIMIT`ed query
still decides which rows those are:

```sql
SELECT path AS "Page", SUM(visitors) AS "Visitors", SUM(views) AS "Views"
FROM v_views_paths
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY path
ORDER BY 3 DESC
LIMIT 50
```

Props: `{"formats": {"Visitors": "number", "Views": "number"}, "colorscale": ["Views"]}`

### `markdown`

A note at the top of a dashboard, as an `md` source:

```markdown
**Visitors** count each person once per day. Figures for today and
yesterday are live and settle after the 03:00 UTC pass.
```

Props: `{}`

## Parameters and ranges

A `sql` source can use three named parameters, and no others:

| Parameter | Value | Using it means |
| --- | --- | --- |
| `:project` | the selected project's id | the widget follows the project switcher |
| `:from` | the first day of the selected range, `YYYY-MM-DD` | the widget follows the range switcher |
| `:to` | the last day of the selected range, `YYYY-MM-DD` | the widget follows the range switcher |

A widget that uses none of them is fixed, and should say what it is fixed to
in its title:

| | SQL uses it: follows the switcher | SQL does not: fixed |
| --- | --- | --- |
| **project** | `WHERE project_id = :project` | one project (`WHERE project_id = 7`), several, or all, e.g. one `series` per project from `projects.name` |
| **range** | `WHERE day BETWEEN :from AND :to` | all time, a window relative to now (`date('now', '-12 months')`), or set dates |

The page offers these range presets; a dashboard remembers the viewer's last
choice, starting from the `range` it was created with. The multi-day presets
are whole days and leave out today, which is still filling:

| Id | Label | `:from` → `:to` |
| --- | --- | --- |
| `today` | Today | today → today |
| `yesterday` | Yesterday | yesterday → yesterday |
| `7d` | Last week | today − 7 → yesterday |
| `30d` | Last month | today − 30 → yesterday |
| `90d` | Last 90 days | today − 90 → yesterday |
| `custom` | Custom… | the dates the viewer picks |

`create_dashboard` takes any preset but `custom`. The API itself never takes a
preset for data: `widget_data` takes `from` and `to`, required for a widget
that follows the range and ignored otherwise, with `from` ≤ `to`, at most 365
days apart, and `from` not after today; a `to` after today is clamped to
today. Days are the instance's days, which are UTC (`timezone` on
`list_dashboards`): "today" is the UTC date everywhere, whatever the viewer's
clock says.

## Layout

A dashboard's widgets are one ordered list that fills a 12-column grid left to
right, wrapping to the next row when a widget does not fit. Nothing backfills a
gap, so a 6 × 8 chart followed by four 3 × 3 stats puts the stats in a 2 × 2
block beside the chart.

- **`width`** is columns out of 12, from 1 to 12: 3 is a quarter, 4 a third, 6
  a half, 12 the full width.
- **`height`** is rows of 40px, from 1 to 12: 3 is about 120px, 8 about 320px.
  Heights keep their pixels at every width, so charts stay readable on a phone.
- **`after`** places a new widget: a widget id on the same dashboard puts it
  right after that one, `0` puts it first, and leaving it out puts it last.
  Widgets are not reordered once placed; to move one, `copy_widget` it with
  the `after` you want and archive the original.
- Dashboards sit in groups, drawn as tabs of one sidebar entry (see
  [Concepts](#concepts)). `after`, on `create_dashboard` and
  `update_dashboard`, follows what the id it names is: a dashboard in the
  *same* group moves this one among that group's tabs; a dashboard in
  *another* group moves this one's *whole group* to sit right after that
  other group in the sidebar; `0` moves it (or its whole group) to the top;
  naming itself is a no-op. On `create_dashboard` nothing moves: the new
  dashboard goes right after the named dashboard's whole group, or, with a
  `group_id`, right after that tab.
- `group_id` says which group a dashboard is in. On `create_dashboard`, a
  `group_id` from `list_dashboards` adds the new dashboard as a tab of that
  group (`after` then names a tab there, `0` first); omitted, it starts a
  new group of its own, its `group_id` its own id. On `update_dashboard`,
  a `group_id` moves the dashboard into that group as a tab; `group_id: 0`
  takes it out as a group of one, its `group_id` its own id (a dashboard
  already alone keeps the one it has). When a dashboard leaves a group
  whose `group_id` is its own id, by `group_id: 0` or by joining another
  group, the tabs left behind take the id of their first live tab: a
  group's id can change, so read it from `list_dashboards` or
  `get_dashboard` before using it.

A two-tab dashboard, in two calls:

```
create_dashboard {"title": "Overview"}                  → id 1001, group_id 1001
create_dashboard {"title": "Detail", "group_id": 1001}   → id 1002, group_id 1001
```

The sidebar now shows one entry, "Overview" (the group's first live
dashboard), with two tabs: "Overview" and "Detail".

## Archiving and the purge

Archiving is how to undo, and the only way to remove anything:

- `archive_widget` hides a widget in place; `restore_widget` puts it back where
  it was. An archived widget keeps its name, so the name stays taken, and it
  cannot be updated or copied until restored.
- `archive_dashboard` hides a dashboard with its widgets; `whole_group`
  hides every live member of its group. `restore_dashboard` brings a
  dashboard back where it was in the sidebar; `whole_group` brings back
  every archived member of its group, including one archived on its own,
  earlier, before the rest.
- `list_dashboards` and `list_widgets` include archived items with their
  `archived_at`, so you can find one to restore. `get_dashboard` still opens
  an archived dashboard, with only its live widgets and its group's live
  tabs. The page's sidebar shows live dashboards only; one opened by its URL
  says "Archived: not in the sidebar" and offers Restore, and the dashboard
  gallery (`/app/gallery/dashboards`) lists every archived one.
- A system group is archived and restored whole (`whole_group`) and is never
  purged; system widgets cannot be archived.

Archived projects, dashboards and widgets are **deleted
`RETENTION_ARCHIVED_DAYS` after archiving** (default 30; `0` keeps them
forever), by the daily pass: on the first start after upgrading, then daily
at 03:00 UTC. A purged dashboard takes its widgets
with it. A purged project takes all of its data: its events, aggregates and
ingest keys are gone, and so is anything a widget's SQL could have shown of
it. Nothing returns the purge date; it follows from `archived_at`.

## Refusals and fixes

A refusal is a tool error over MCP and `400 invalid` over HTTP unless noted;
its message names what to change. In `create_dashboard` it starts with the
widget's name (`widget visitors: …`), and nothing is created.

| Refusal | Fix |
| --- | --- |
| component gauge does not exist; list_components names the ones there are | Use a name from `list_components` or `reporting_guide`. |
| line does not accept md; it accepts sql | `markdown` takes `md`; every other component takes `sql`. |
| source type csv does not exist; there are md and sql | Set `source.type` to `sql` or `md`. |
| sql uses :path; widgets get only :project, :from and :to | Write the value into the SQL, or use one of the three. |
| line needs y (number); columns are x, visitors | Alias the column to the input: `visitors AS y`. |
| visitors is not an input of line | Drop the column, or alias it to an input; only `table` takes any columns. |
| line.y: "n/a" is not number | Return a number, a `YYYY-MM-DD` day or text as the input asks; `CAST(… AS REAL)` where needed. An empty value (SQL `NULL`) always passes. |
| refused: sql reads meta, which custom SQL may not read | Read the `v_*` views instead. The same holds for `sqlite_*`, `pragma_*` and `dbstat`, as a name or as a single-quoted string equal to one (`'meta'`, `'sqlite_master'`), and for `ATTACH`. |
| refused: sql may use non-ASCII characters only inside quotes or comments | Quote the name: `"визиты"`. |
| query exceeded API_QUERY_TIMEOUT (10s); narrow the range or group the query | Group in SQL or read fewer days; the operator can raise `API_QUERY_TIMEOUT`. |
| SQLite's own error, such as a column that does not exist | Fix the query; try it with `query` first. |
| markdown text is empty | Give the Markdown text. |
| stat: … (a props schema error) | Match the props schema `list_components` returns; unknown props are refused. |
| width is columns out of 12, from 1 to 12 | A whole number 1–12. `height` is the same, in rows of 40px. |
| after 7 is not a widget on dashboard 1001 | Name a widget on the same dashboard, `0` for first, or leave `after` out for last. |
| after 7 is archived; name a live widget | `after` never names an archived widget: name a live one, or `restore_widget` it first. |
| group 5 has no live user dashboard | Give a `group_id` from `list_dashboards` naming a group with a live user dashboard; a system group cannot be joined this way. |
| after 7 is not a member of group 5 | For `after` naming a tab of the `group_id` given: name a live dashboard in that same group, `0` for its first tab, or leave `after` out for last. |
| after 7 is not a user dashboard | For `after` with no `group_id` (moving a whole group by the dashboard after it lands): name a live user dashboard, `0` for the top, or leave `after` out for last. |
| after 7 is archived; name a live dashboard | `after` never names an archived dashboard: name a live one next to where it should go, or `restore_dashboard` it first. |
| widget name visitors is already used on this dashboard (`409 conflict`) | Choose another name. An archived widget keeps its name; restore or rename it to reuse the name. |
| dashboard 1 is a system dashboard and changes only with a release; duplicate_dashboard makes an editable copy | `duplicate_dashboard`, then change the copy. |
| dashboard 12 is a system dashboard, archived and restored with its group; pass whole_group | Pass `whole_group: true`; the whole system group is archived or restored. |
| dashboard 1001 is archived; restore_dashboard first | `restore_dashboard` (updating, adding to or duplicating an archived dashboard is refused). For a widget: widget 42 is archived; restore_widget first. |
| widget 42's component was removed; set component first | `update_widget` with a `component` (and resize or archive as needed); see [When widgets break after an update](#when-widgets-break-after-an-update). |
| widget 42 follows the project switcher; pass project_id | Pass `project_id` to `widget_data`. For the range: widget 42 follows the date range; pass from and to. |
| from 2026-09-10 is after to 2026-09-01; from 2026-01-01 to 2027-02-01 spans more than 365 days; from 2026-10-01 is after today | Pass `from` ≤ `to`, at most 365 days apart, `from` no later than today. |
| range must be one of today, yesterday, 7d, 30d, 90d, custom | Use a preset id. `create_dashboard` refuses `custom`: create with a preset; the viewer picks custom dates. |
| title must not be empty; nothing to update; give title, after or group_id | Give a title, or something to change. |
| dashboard 1001 changed while placing this widget; try again (`409 conflict`) | Another write placed a widget at the same spot; call again. |
| the dashboard order changed while placing this dashboard; try again (`409 conflict`) | Another write moved or placed a dashboard at the same spot; call again. |
| `404 not_found` | The dashboard or widget id does not exist; `list_dashboards` and `list_widgets` name them. `widget_data` on an archived widget is a 404 too. |

## When widgets break after an update

A release can rename a view's columns or remove a component, and widgets
written against the old ones stop working:

- a query that no longer runs, or whose rows no longer fit the component,
  refuses in `widget_data` with the reason, ending "if this started after an
  update, see the release notes at
  https://github.com/dmtrkzntsv/twillingate/releases";
- a widget whose component was removed answers `removed: true`, and its card
  says "component removed".

To fix them:

1. Find the running version: `reporting_guide` names it, with a link to its
   release notes.
2. Read the notes of every release between the last version the widgets
   worked on and the running one, at
   https://github.com/dmtrkzntsv/twillingate/releases (one page per tag,
   `…/releases/tag/v<version>`). They name renamed view columns and removed
   components.
3. Rewrite the SQL with `update_widget` (check the new columns in
   `schema://views`), or switch a removed component's widgets to another
   component. `list_widgets` with `component` finds every widget on one.
4. Check each with `widget_data`.

A component that comes back in a later release does not reattach itself: set
it again with `update_widget`. System dashboards are fixed by the release
itself.

## Local development

`twillingate reporting dev` previews dashboard files, the form system
dashboards take in the repository, against a real database before they
ship:

```bash
twillingate reporting dev <dir>… [-db <path>] [-addr 127.0.0.1:3100]
```

Open `http://127.0.0.1:3100/`, which redirects to the dashboards at `/app/`.

- Each `<dir>` is a dashboard directory (it has a `dashboard.json`) or a
  parent of several. A `dashboard.json` without an `id` gets 1001, 1002, …
  in argument order. One with an `id` from 1 to 999 previews as a system
  dashboard, in its group: a tab of the dashboard its `group` names, or its
  own sidebar entry when it names none.
- `-db` defaults to `DATABASE_DSN`'s path. The database is opened read-only
  and never written: the view route answers but stores nothing, and the page
  offers no writes (no "…" menus, dragging, Restore or copy buttons).
- `-addr` is refused unless it is a loopback address, and a request naming
  any other host (`Host:`) gets `403`, so a page elsewhere cannot reach it
  by pointing its own name at `127.0.0.1`. There is no login: the page finds
  `GET /api/dashboards` answering without a token and skips the login.
- Files are read again on every request, and the page polls
  `/api/dev/version` twice a second and reloads when they change.
- A directory that does not load shows as a banner (the `errors` array of
  `GET /api/dashboards`) while the others load. A widget is validated when
  its data is requested: a refusal is a `400` shown in its card.
- In `web/`, `npm run dev` proxies `/api` to it (`127.0.0.1:3100`), so
  components hot-reload against real data.
