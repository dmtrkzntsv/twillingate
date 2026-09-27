# Reporting

How an AI agent builds and changes the dashboards twillingate serves at `/app/`.
The page is read-only: a person opens it, picks a project and a date range, and
reads. Agents are its only authors, through the tools and routes below. The MCP
endpoint serves this file verbatim as `docs://reporting`; `reporting_guide`
returns its live parts (components, views, projects, dashboards) in one call.

Collecting data and querying it with `query` are in
[twillingate.md](twillingate.md); running the collector and enabling the API
are in [deployment.md](deployment.md).

- [Concepts](#concepts)
- [Tools](#tools)
- [HTTP API](#http-api)
- [Components](#components)
- [Parameters and ranges](#parameters-and-ranges)
- [Layout](#layout)
- [Archiving and the purge](#archiving-and-the-purge)

---

## Concepts

**A dashboard** is a titled, ordered list of widgets. Its `owner` is `system` or
`user`:

- **System dashboards** ship with each release, have ids 1–999, and change only
  when the release does. Every write tool refuses them. To customize one, call
  `duplicate_dashboard`: the copy is a user dashboard you can edit.
- **User dashboards** are what agents create. Their ids start at 1001.

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

## Tools

| Tool | Input | Returns |
| --- | --- | --- |
| `list_components` | none | `source_types` and `components`: each one's `description`, `accepts`, `inputs`, `props` schema, `default_width` and `default_height` |
| `list_dashboards` | none | `timezone` and `dashboards` in sidebar order (system, then user), archived ones included: `dashboard_id`, `title`, `owner`, stored `project_id` and `range`, live `widgets` count, `archived_at` |
| `get_dashboard` | `dashboard_id` | the dashboard, its `follows_project` and `follows_range`, and its live `widgets` in order |
| `list_widgets` | `dashboard_id`, `component` (both optional; they combine) | `widgets`, archived ones included, each with its `dashboard` and 1-based `position` there |
| `widget_data` | `widget_id`, `project_id`, `from`, `to`, `fresh` | the envelope above |
| `create_dashboard` | `title`, `range` (default `7d`), `after`, `widgets` | the new dashboard, as `get_dashboard` returns it; one invalid widget creates nothing |
| `update_dashboard` | `dashboard_id`, `title`, `after` | the dashboard |
| `duplicate_dashboard` | `dashboard_id` | a user copy with copies of the live widgets, titled "… (copy)"; works on system dashboards |
| `archive_dashboard` | `dashboard_id` | hides it; see [Archiving and the purge](#archiving-and-the-purge) |
| `restore_dashboard` | `dashboard_id` | unhides it |
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

Every tool above is also a REST route under `/api/`, with the same bearer token,
JSON and error shape as the routes in
[twillingate.md](twillingate.md#http-api). One route has no tool: the page
stores the viewer's selection with `PUT …/view`, which is viewer state rather
than a definition, so it is allowed on system dashboards and not audited.

| Method | Path | Mirrors | Input |
|---|---|---|---|
| `GET` | `/api/components` | `list_components` | — |
| `GET` | `/api/dashboards` | `list_dashboards` | — |
| `GET` | `/api/dashboards/{dashboard_id}` | `get_dashboard` | — |
| `GET` | `/api/widgets` | `list_widgets` | query: `dashboard_id`, `component` |
| `GET` | `/api/widgets/{widget_id}/data` | `widget_data` | query: `project_id`, `from`, `to`, `fresh` |
| `POST` | `/api/dashboards` | `create_dashboard` | body: `title`, `range`, `after`, `widgets` → 201 |
| `PATCH` | `/api/dashboards/{dashboard_id}` | `update_dashboard` | body: `title`, `after` |
| `POST` | `/api/dashboards/{dashboard_id}/duplicate` | `duplicate_dashboard` | — → 201 |
| `POST` | `/api/dashboards/{dashboard_id}/archive` | `archive_dashboard` | — |
| `POST` | `/api/dashboards/{dashboard_id}/restore` | `restore_dashboard` | — |
| `POST` | `/api/dashboards/{dashboard_id}/widgets` | `add_widget` | body: the widget, `after` → 201 |
| `PATCH` | `/api/widgets/{widget_id}` | `update_widget` | body: fields to change |
| `POST` | `/api/widgets/{widget_id}/copy` | `copy_widget` | body: `dashboard_id`, `after` → 201 |
| `POST` | `/api/widgets/{widget_id}/archive` | `archive_widget` | — |
| `POST` | `/api/widgets/{widget_id}/restore` | `restore_widget` | — |
| `PUT` | `/api/dashboards/{dashboard_id}/view` | `view` | body: `project_id`, `range`, and `from`/`to` for `custom` → `{"status":"saved"}` |

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
| `table` | `sql` | any columns, shown in query order | `formats` (column → format), `colorscale` (columns shaded by value) | 6 × 10 |
| `markdown` | `md` | none | none | 12 × 2 |

`list_components` is the authority: it returns each component's props as a
JSON schema, and its `description` says when to use it.

**When a release removes a component,** its widgets stay, with `component`
null: the card says "component removed" and `widget_data` answers
`removed: true`. Such a widget can be switched to another component with
`update_widget`, resized or archived; nothing else about it can change, and it
cannot be copied, until it has a component again. A component that comes back
in a later release does not reattach itself.

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
choice, starting from the `range` it was created with:

| Id | Label | `:from` → `:to` |
| --- | --- | --- |
| `today` | Today | today → today |
| `yesterday` | Yesterday | yesterday → yesterday |
| `7d` | Last week | today − 6 → today |
| `30d` | Last month | today − 29 → today |
| `90d` | Last 90 days | today − 89 → today |
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
- Dashboards take `after` the same way in the sidebar, among user dashboards
  (system dashboards always come first); `update_dashboard` moves one.

## Archiving and the purge

Archiving is how to undo, and the only way to remove anything:

- `archive_widget` hides a widget in place; `restore_widget` puts it back where
  it was. An archived widget keeps its name, so the name stays taken, and it
  cannot be updated or copied until restored.
- `archive_dashboard` hides a dashboard with its widgets; `restore_dashboard`
  brings it back where it was in the sidebar.
- `list_dashboards` and `list_widgets` include archived items with their
  `archived_at`, so you can find one to restore; `get_dashboard` and the page
  show live ones only.
- System dashboards and their widgets cannot be archived.

Archived projects, dashboards and widgets are **deleted
`RETENTION_ARCHIVED_DAYS` after archiving** (default 30; `0` keeps them
forever), by the daily pass at 03:00 UTC. A purged dashboard takes its widgets
with it. A purged project takes all of its data: its events, aggregates and
ingest keys are gone, and so is anything a widget's SQL could have shown of
it. Nothing returns the purge date; it follows from `archived_at`.
