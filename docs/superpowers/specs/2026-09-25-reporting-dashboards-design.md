# Reporting: dashboards served by twillingate, built by agents

Status: draft
Date: 2026-09-25

## Sequencing

Lands after the one events table (PR #58, migration 020) and owns migration
`021_reporting.sql`. System widgets are written against the views as #58
left them. Ships as two PRs with a release between them (see
[Rollout](#rollout)): the first adds reporting next to Evidence, the second
removes Evidence.

## Problem

- **Dashboards need a second stack.** Reporting today is Evidence: a Node
  image with about 990 npm packages that snapshots the whole SQLite file
  and rebuilds a static site. Running it means Docker, a second compose
  file, and in the two-server topology a litestream replica at home. None
  of that is "open a page, log in, look". A hosted twillingate cloud
  cannot hand each customer that stack.
- **Only a developer can add a report.** Pages are Markdown files in the
  repo. An agent that already answers questions over MCP (`query`,
  `views_breakdown`, …) cannot save what it found as a chart; the answer
  dies with the conversation.
- **The numbers are stale by design.** A rebuild runs at most every
  `DASHBOARDS_INTERVAL` (15 minutes), and the first build after start
  answers `503` for about a minute.
- **Evidence shapes the data around its limits.** Every source carries an
  empty-database sentinel row because the connector cannot type an empty
  result; pages filter it back out.

## Decisions

### Shape

1. **twillingate serves a read-only dashboard UI at `/app/`** on the API
   listener. It lists dashboards, switches project and date range, and
   renders widgets. It changes nothing except the viewer's last selection
   (decision 35).
2. **Agents are the only authors.** Dashboards and widgets are created and
   changed through MCP tools and REST routes (decisions 28–30). The UI has
   no editing.
3. **Installable as a PWA.** A manifest and an app-shell service worker
   make "Install app" (Chrome, Edge) and "Add to Dock" (Safari) give it its
   own window. No native download in this work.
4. **One package, `internal/reporting`,** at rank 1 next to `manage`: the
   models, validation, operations, the system-definition migrator, the
   cache, and the embedded UI. It declares `reporting.Store`, the slice of
   the store it uses, which `store/sqlite` implements. `api` exposes its
   operations through `expose()` and mounts `/app/`. Generic helpers it
   needs live under `internal/shared/`, the home for small generic leaf
   packages (`internal/shared/sortkey`, decision 10),
   and the Go side adds no third-party dependency for reporting; the
   frontend uses popular libraries freely (decision 43).
5. **A widget's SQL decides its project and its range; the dashboard
   follows.** A widget whose SQL uses `:project` follows the project
   switcher; one whose SQL uses `:from` or `:to` follows the range
   switcher; otherwise it is fixed in that respect, selecting whatever its
   SQL names (decision 17). Each switcher appears only when at least one
   widget follows it; there is no dashboard-level setting to fall out of
   step with the widgets. System widgets use all three parameters, so
   every system dashboard has both switchers.

### Model

6. **Migration 021 creates three tables and a history table:**

   ```
   components            name PK, description, accepts (JSON), inputs (JSON), props (JSON),
                         default_width, default_height
   dashboards            id INTEGER PK AUTOINCREMENT, owner ('system'|'user'), title,
                         sort_key TEXT, last_project_id, last_range, last_from, last_to,
                         created_at, updated_at, archived_at
   widgets               id INTEGER PK AUTOINCREMENT,
                         dashboard_id REFERENCES dashboards(id) ON DELETE CASCADE,
                         component REFERENCES components(name) ON DELETE SET NULL,
                         sort_key TEXT, width, height, name, title,
                         props (JSON), source_type (TEXT), source (TEXT),
                         created_at, updated_at, archived_at
   reporting_migrations  id INTEGER PK AUTOINCREMENT, hash, version, applied_at
   ```

   Two foreign keys and no checks or triggers: deleting a dashboard
   deletes its widgets (cascade), and deleting a component sets
   `component` to null on the widgets that used it, which is how a widget
   knows its component was removed (decision 14). Unique
   indexes: `dashboards (owner, sort_key)` (the sidebar orders each group
   on its own), `widgets (dashboard_id, sort_key)`,
   so every order is total, and `widgets (dashboard_id, name)`, which the
   migrator matches on. Every other rule below is enforced in Go
   (the standing rule, no validation in the database).
7. **Foreign keys become enforced.** SQLite checks them only on
   connections that turn them on, and the store never has, so the two
   `REFERENCES` already in the schema (migrations 005 and 014, ingest keys
   to projects) are declarative today. The writer's connections gain
   `foreign_keys(1)`, which enforces those too:
   - `DeleteProject` deletes the `projects` row before the rows that
     reference it (`internal/store/sqlite/registry.go`), which enforcement
     refuses; it is reordered to delete dependents first.
   - Schema migrations run with enforcement off on their connection, as
     SQLite requires for table rebuilds (the pragma cannot change inside a
     transaction), and end with `PRAGMA foreign_key_check`, failing the
     migration on any violation.
   - The read-only API pool is unaffected.
8. **Ids 1–999 are reserved for system dashboards.** Migration 021 sets the
   `dashboards` sequence to 1000, so agent-made dashboards start at 1001 and
   can never collide with a system id.
9. **A widget is a component plus a source.** `source_type` names a
   source type registered in Go; the database stores the name as text and
   knows no list. Each source type implements one interface in
   `reporting`:

   ```go
   type SourceType interface {
       Name() string                                      // "sql", "md"
       Validate(ctx, content string, c Component) error   // refusals are ErrInvalid
       Load(ctx, content string, p Params) (Payload, error)
       Cacheable() bool
   }
   ```

   This work registers `sql` (a query; cacheable) and `md` (Markdown
   text; not cacheable). A new source type is Go code and a release, with
   no migration; validation refuses names that are not registered.
   `title` is optional; the card shows a
   header only when it has one. `name` is unique within the dashboard and
   is how system files and the `layout` list (decision 10) refer to the
   widget; an agent gives it or it is
   derived from the title (lower case, `-` for runs of other characters),
   with `-2`, `-3`, … added when taken.
10. **Layout is a flowing grid.** A dashboard's widgets are one ordered
    list; each has a `width` and a `height`, and they fill a 12-column
    grid left to right, wrapping to the next line when a widget does not
    fit. It is a CSS grid: 12 columns, rows of a fixed ~40px
    (`grid-auto-rows`), and each widget spans `width` columns and `height`
    rows (`grid-column: span w; grid-row: span h`). The grid places
    widgets in order into free cells without backfilling (no `dense`), so
    a 6 × 6 chart followed by four 3 × 3 stats puts the stats in a 2 × 2
    block beside the chart. Numbers map straight to spans; words or
    percentages would only add a translation (percentages would also need
    `calc()` around gaps).
    - **`width`** is the number of columns, 1–12: 3 is a quarter, 4 a
      third, 6 a half, 12 the full width, which covers the Evidence
      pages' 1-, 2-, 3- and 4-across grids.
    - **`height`** is the number of grid rows spanned, 1–12, on the same
      scale as `width`: 3 is ~120px, 8 ~320px, 12 ~480px. Heights keep
      their pixels at every width, so charts stay readable on phones.
    - Either may be omitted and takes the component's `default_width` or
      `default_height`.
    - **Order is a fractional sort key.** `sort_key` is a fractional index
      (the `fractional-indexing` scheme): a key can always be generated
      between two others (`a0`, `a0V`, `a1`). It is our own code, not a
      dependency (the Go side keeps third-party packages to a minimum):
      ~150 lines in `internal/shared/sortkey`, a leaf that knows nothing
      about dashboards, ranked 0 next to `civil`.
      Inserting a widget between two others writes one key; archiving one
      writes `archived_at`; nothing is renumbered or rebalanced. Widgets
      are not reordered once placed. An archived widget keeps its key and
      its name, so restoring it puts it back where it was; new keys are
      generated against the full order, archived widgets included, so they
      never collide with one.
    - **Agents never see keys.** Tools place a widget `after` a given
      widget id (or first, or last by default), and `reporting` generates
      the key between the neighbours.
    - At the edges (system files, `get_dashboard`, `create_dashboard`) the
      layout is the ordered list `[{"widget": "name", "width": 4,
      "height": 3}, …]`.
11. **`owner = 'system'` rows change only through the migrator** (decisions
    20–25). Every write operation refuses them with `ErrInvalid`.

### Components

12. **A component is a React component in `web/` plus its contract.** Each
    `web/src/components/widgets/<name>.tsx` exports the component and its
    `accepts` (source types), `inputs` (for `sql`), `props` (a JSON schema)
    `defaultWidth` and `defaultHeight`. The web build writes `components.json` from these
    exports. Nothing is declared twice.
13. **The first set is broad from the start.** Charts are shadcn's
    `chart` (`ChartContainer`, tooltip and legend) around Recharts, so any
    Recharts chart can be a component; `funnel` and `scatter` use
    Recharts directly, outside shadcn's gallery. `format` is
    `number`\|`percent`\|`duration` everywhere.

    | Component | Accepts | Inputs: the columns the query returns | Props | Default width × height |
    | --- | --- | --- | --- | --- |
    | `stat` (a number) | `sql` | `value` number; `previous` number, optional (shows the change) | `format` | 3 × 3 |
    | `line` | `sql` | `x` day or text; `y` number; `series` text, optional (one line per value) | `format`, `curve`: `linear`\|`monotone`\|`step` | 6 × 8 |
    | `area` | `sql` | as `line` | `format`, `curve`, `stacked` | 6 × 8 |
    | `bar` | `sql` | `x` text or day; `y` number; `series` text, optional | `format`, `horizontal`, `stacked` | 6 × 8 |
    | `bar_list` | `sql` | `label` text; `value` number (a ranked list with an inline bar: top pages, referrers) | `format` | 6 × 8 |
    | `pie` | `sql` | `label` text; `value` number | `format`, `donut` | 4 × 8 |
    | `radar` | `sql` | `axis` text; `value` number; `series` text, optional | `format` | 4 × 8 |
    | `radial` | `sql` | `label` text; `value` number; `max` number, optional (rings toward a target) | `format` | 4 × 8 |
    | `scatter` | `sql` | `x` number; `y` number; `series` text, optional; `size` number, optional | `format` | 6 × 8 |
    | `funnel` | `sql` | `step` text; `value` number, in query order | `format` | 6 × 8 |
    | `table` | `sql` | open: any columns, shown in query order | `formats`: column → format; `colorscale`: columns shaded by value | 6 × 10 |
    | `markdown` | `md` | none | none | 12 × 2 |

    Each component's `description` in the manifest says when to use it
    and its limits (a pie past ~7 slices should group the rest as
    "Other" in SQL); `reporting_guide` and `list_components` return it.

    Queries satisfy inputs by alias: `SELECT day AS x, visitors AS y …`.
    Returning a column a closed interface does not declare is refused.
14. **A component that leaves the code is deleted; its widgets keep
    going with `component` null.** The migrator deletes components the
    manifest no longer has, and the foreign key sets `component` to null
    on every widget that used one. A null component is the whole signal:
    there is no removed state to track and nothing to clean up later.
15. **A widget with no component** renders a "component removed" card,
    and `widget_data` answers `{"widget_id", "removed": true}`. It can be
    switched to a live component (`update_widget`), resized, or archived;
    its other fields cannot be edited and it cannot be copied, until it
    has a component again.
16. **A component that comes back does not restore its widgets.** The
    null holds no name, so when a later release (or an upgrade after a
    rollback, decision 25) brings a component back, the widgets that lost
    it stay without one until an agent sets it with `update_widget`.
    Accepted: removing a component is rare, and the round trip rarer.

### Parameters and ranges

17. **A `sql` source can use three named parameters:** `:project`
    (project id), `:from` and `:to` (days, `YYYY-MM-DD`). Using any other
    parameter is refused. Which it uses decides what the widget follows,
    independently for project and range:

    | | SQL uses it: follows the switcher | SQL does not: fixed |
    | --- | --- | --- |
    | **project** | `:project` | one project (`WHERE project_id = 7`), several, or all, e.g. grouped by project with `series` from `projects.name` |
    | **range** | `:from` and/or `:to` | all time, a window relative to now (`date('now', '-12 months')`), or set dates |

    So a widget follows both switchers, one, or neither: "All-time
    signups, iOS app" follows neither. A fixed widget says what it is
    fixed to in its title. A deleted project leaves a widget whose SQL
    names it with fewer or no rows, like any SQL; nothing can dangle,
    because nothing is stored besides the SQL.
18. **Presets live in the UI; the API takes dates.** Range presets are a
    closed vocabulary used by the dashboard page's URL, the range switcher
    and the dashboard's stored selection. The UI resolves a preset
    to `from` and `to` in the instance timezone (below) before calling
    the API. The rolling
    presets include today, the window the Evidence pages use:

    | Id | Label | `:from` → `:to` |
    | --- | --- | --- |
    | `today` | Today | today → today |
    | `yesterday` | Yesterday | yesterday → yesterday |
    | `7d` | Last week | today − 6 → today |
    | `30d` | Last month | today − 29 → today |
    | `90d` | Last 90 days | today − 89 → today |
    | `custom` | Custom… | the given `from` → `to` |

    **API URLs take only `from` and `to`** (`YYYY-MM-DD`), never a preset.
    For a widget that follows the range, both are required and
    `from ≤ to`, spanning at most 365 days;
    otherwise `ErrInvalid`. A `to` after today (UTC) is clamped to today,
    before the cache key is built, so a browser clock slightly ahead of
    the server near midnight still gets data. The Go side knows the
    preset ids only to validate the stored selection.

    **One stored range per dashboard, no separate default.** A
    dashboard's selection (`last_range`, with `last_from`/`last_to` for
    `custom`, and `last_project_id`) starts at the value it is created
    with and then follows the viewer: `create_dashboard` takes an optional
    `range` (default `7d`), and a system dashboard's `dashboard.json`
    gives `range`, which the migrator writes only when it inserts the
    dashboard, never over a viewer's choice.

19. **Days are the instance's days; for now that is UTC.** `v_*` views
    group by `day`, the UTC date of `ts`. The API reports the timezone
    days are grouped in (`timezone` on `list_dashboards`, `UTC` until a
    `TIMEZONE` setting exists; see [Out of scope](#out-of-scope)). The UI
    resolves presets and labels dates in that timezone, not the
    browser's, so everyone viewing an instance sees the same "Today".
    Moments (`cached_at`, "data as of") show in the browser's local time.

### System dashboards: files, migrated on every run

20. **System dashboards are files in the repo**, embedded with `go:embed`:

    ```
    internal/reporting/system/<dir>/
      dashboard.json     {"id": 1, "title": "Views", "range": "7d",
                          "layout": [{"widget": "visitors", "width": 3, "height": 3},
                                     {"widget": "trend", "width": 6}, …]}
      <name>.json        {"component": "stat", "title": "Visitors", "props": {"format": "number"}}
      <name>.sql         the query, plain SQL: runs in sqlite3 as it is
      <name>.md          or Markdown, for a markdown widget
    ```

    A widget is `<name>.json` plus exactly one `<name>.sql` or `<name>.md`;
    the extension is the source type. A config with no data file or two, a
    data file with no config, a layout naming a missing widget or one
    twice, or a widget the layout leaves out, is an error. The directory name is for people;
    only `id` identifies the dashboard.
21. **Matching keys:** a system dashboard by `id`; a widget by
    (`dashboard_id`, file name without extension); a component by name.
22. **The migrator runs where the schema migrates.** `app.Migrate(ctx, st)`
    runs `st.Migrate`, then `reporting.Migrate`. `serve` (via `app`), the
    `migrate` command and the `project` command, the three places that
    migrate today, call it. `cmd` keeps importing only `app`.
23. **It is tracked like `schema_migrations`.** The hash covers every
    embedded `dashboard.json`, widget file and `components.json`. If the
    latest `reporting_migrations` row has that hash, nothing runs.
    Otherwise, in one transaction:
    1. components: upsert by name; delete the ones missing from the
       manifest, which sets `component` to null on their widgets;
    2. system dashboards: upsert by `id` (title, and a sort key in `id`
       order); on insert, the selection from the file's `range`; on
       update, the stored selection is kept; delete system
       dashboards no file claims, with their widgets;
    3. widgets, per system dashboard: upsert by name, keeping the id, with
       width, height and a sort key from the file's order (keys are
       regenerated evenly, since system widgets change only here); insert
       new ones; delete ones with no file;
    4. append a `reporting_migrations` row (hash, build version) and one
       `audit_log` entry, actor `release`, listing what was added, changed
       and removed.

    Every system definition passes the same validation as
    `create_dashboard` (decision 27) before anything is written.
24. **A failure stops the run** the way a failed schema migration does: the
    transaction rolls back and `serve` does not start. The system dashboard
    test (see [Tests](#tests)) runs the same validation in `make check`.
25. **Rolling the binary back re-migrates to the older definitions,** since
    the hash differs: components the older binary does not know are
    deleted, and their widgets lose their component for good (decision
    16): upgrading again does not restore them. `deploy/UPGRADES.md` says
    so, and suggests `list_widgets` before a rollback to note which
    widgets use components the older release lacks.
26. **The five Evidence pages become system dashboards:** Views (id 1),
    Product (2), Users (3), Groups (4), Retention (5), each starting on
    the Evidence page's default range (`7d`; Retention `90d`) and with its stats,
    charts and tables. The drill-down into a single page
    (`views/[project]/page.md`) is dropped: it needs a path parameter that
    decision 17 does not provide. The empty-database sentinel rows are not
    carried over.

### Validation

27. **Every write validates the whole result** and refuses with
    `ErrInvalid`, in words an agent can act on:
    - the component exists and accepts the source type:
      "component `pie` does not exist; list_components names the 6 there
      are";
    - `sql`: only the three parameters ("sql uses `:path`; widgets get only
      `:project`, `:from` and `:to`"); the query runs as
      `SELECT * FROM (…) LIMIT 0` on the read pool with sample values bound
      and the guards and timeout `query` applies (read-only, no
      `ATTACH`, `API_QUERY_TIMEOUT`);
      its columns satisfy the inputs ("line needs y (number); columns are
      x, visitor"); value types are checked on a few rows: for a widget
      using `:project`, of the most recently active project; if there are
      no rows, column names only, with the full check on first render;
    - `md`: the text is not empty;
    - props match the component's `props` schema;
    - size: `width` a whole number 1–12 ("width is columns out of 12,
      from 1 to 12"), `height` a whole number 1–12;
      `after` names a widget on the same dashboard;
    - system rows: "dashboard 3 is a system dashboard and changes only with
      a release; duplicate_dashboard makes an editable copy".

### Agent surface

28. **Read tools:**

    | Tool | Route | Returns |
    | --- | --- | --- |
    | `reporting_guide` | MCP only | live components, source types, views, projects and dashboards, with the workflow and rules (decision 50) |
    | `list_components` | `GET /api/components` | the registered source types, and per component: name, description, accepts, inputs, props, default width and height |
    | `list_dashboards` | `GET /api/dashboards` | `timezone` (the instance's, decision 19), and per dashboard, in sidebar order: id, title, owner, stored project and range, widget count, archived |
    | `get_dashboard` | `GET /api/dashboards/{dashboard_id}` | the dashboard and its live widgets in order, each with id, name, width, height, component, title, props, source type and source |
    | `list_widgets` | `GET /api/widgets?dashboard_id=&component=` | every widget with its dashboard (id, title, owner, archived) and place; the two filters combine |
    | `widget_data` | `GET /api/widgets/{widget_id}/data?project_id=&from=&to=&fresh=` | the widget's content for the dates, and for `project_id` when its SQL uses `:project` (decision 32) |

29. **Write tools,** each refused on system dashboards and recorded in
    `audit_log` with actor `mcp` or `rest`:

    | Tool | Route | Effect |
    | --- | --- | --- |
    | `create_dashboard` | `POST /api/dashboards` → 201 | title, optional `range` (the starting selection, default `7d`), optional `after` (a dashboard id; `null` first; omitted, last), optional widgets in order; all or nothing |
    | `update_dashboard` | `PATCH /api/dashboards/{dashboard_id}` | title; `after` moves it in the sidebar (one sort key written) |
    | `duplicate_dashboard` | `POST /api/dashboards/{dashboard_id}/duplicate` → 201 | a user copy of any dashboard, system ones included |
    | `archive_dashboard` / `restore_dashboard` | `POST /api/dashboards/{dashboard_id}/archive` / `…/restore` | hide or unhide; purged after `RETENTION_ARCHIVED_DAYS` (decision 48) |
    | `add_widget` | `POST /api/dashboards/{dashboard_id}/widgets` → 201 | optional `after` (a widget id; `null` puts it first; omitted, last), `width`, `height`; one sort key written |
    | `update_widget` | `PATCH /api/widgets/{widget_id}` | name, component, title, props, source, width, height |
    | `copy_widget` | `POST /api/widgets/{widget_id}/copy` → 201 | an independent copy into `dashboard_id`, optional `after`; keeps width and height; the source may be a system widget |
    | `archive_widget` / `restore_widget` | `POST /api/widgets/{widget_id}/archive` / `…/restore` | hide or unhide, in place: restoring is the undo; purged after `RETENTION_ARCHIVED_DAYS` (decision 48). An archived widget cannot be updated or copied until restored |

    A source is `{"type": "sql"|"md", "content": "…"}`.
30. **REST only:** `PUT /api/dashboards/{dashboard_id}/view` with a JSON
    body of `project_id`, `range` (a preset) and, for `custom`, `from` and
    `to`, which sets `last_project_id`, `last_range`, `last_from` and
    `last_to`. It stores the preset, not its dates, so "Last week" stays
    rolling when the dashboard is opened again; its URL carries no
    range. Each part is required when the dashboard has that switcher and
    refused when it has none: `project_id` for the project switcher,
    `range` (and `from`/`to`) for the range switcher.
    It is allowed on system dashboards (it is viewer state, not
    definition) and writes no audit entry. MCP-only or REST-only is an
    explicit choice the parity test checks.
31. **One error vocabulary.** `ErrInvalid` moves to `store`, next to
    `ErrNotFound` and `ErrConflict`, and `manage.ErrInvalid` becomes that
    same value, so `errors.Is` keeps working and `api` maps `manage` and
    `reporting` refusals through one path.

### Data and cache

32. **`widget_data` returns one widget's content:**

    ```
    { "widget_id": 42, "from": "2026-08-27", "to": "2026-09-25",
      "cached_at": "…", "refresh_after": "…",
      "columns": ["x", "y"], "rows": [["2026-08-27", "40"], …] }
    ```

    `project_id` is required for a widget whose SQL uses `:project`, and
    `from`/`to` for one whose SQL uses `:from` or `:to`; each is ignored by
    a widget fixed in that respect. Fixed-range SQL is bounded by
    `API_QUERY_TIMEOUT`. The body is what the source type's `Load` returns: rows
    for `sql`; `{"widget_id", "markdown"}` for `md`, which ignores project
    and dates. Only cacheable source types are cached. A widget whose
    component was removed answers `{"widget_id", "removed": true}`. A query that no longer runs, or whose
    rows no longer satisfy the inputs (a release changed a view), is
    `ErrInvalid` with the reason. Queries run on the read pool with the
    same guards and limits as `query`: read-only, no `ATTACH`,
    `API_QUERY_TIMEOUT` (default 10s) and `API_QUERY_MAX_ROWS` (default
    1000); reporting adds no settings of its own. A result cut at the row
    cap answers `"truncated": true`, and the card says "partial: narrow
    the range or group the query" instead of drawing a chart that
    silently stops (a 5-series line over 365 days needs 1,825 rows, so
    operators with long multi-series charts raise `API_QUERY_MAX_ROWS`).
    A timeout is `ErrInvalid` naming `API_QUERY_TIMEOUT`.
33. **One cache, two ages.** An in-memory entry per (widget, project,
    `from`, `to`) records when it was computed:

    | Setting | Default | Meaning |
    | --- | --- | --- |
    | `REPORTING_CACHE_MINUTES` | 15 | an ordinary request reuses an entry younger than this; `0` recomputes every time |
    | `REPORTING_REFRESH_MINUTES` | 1 | a `fresh=true` request reuses an entry younger than this |

    Past the relevant age, the request recomputes and stores the result.
    Entries live for the longer of the two. A refresh age longer than a
    non-zero cache age refuses the boot. `refresh_after` is `cached_at`
    plus the refresh age. Identical requests in flight share one run
    (`singleflight`, same key). A widget's key holds only what it
    follows: `(widget)`, `(widget, project)`, `(widget, from, to)` or all
    three, so a switcher never splits the entries of a widget that ignores
    it. A fixed widget with `date('now', …)` in its SQL is no staler than
    the cache age allows. `update_widget` drops that
    widget's
    entries; a copy starts empty; the migrator drops entries of widgets it
    changed.

### UI

34. **Routes:** `/app/` goes to the last dashboard opened on this device,
    else the first system dashboard;
    `/app/dashboards/{id}?project=&range=` (plus `from` and `to` for
    `custom`) shows one, e.g. `?project=7&range=7d` or
    `?project=7&range=custom&from=2026-08-01&to=2026-08-31`; a shared link
    with a preset shows that preset as of the day it is opened.
    `/app/callback` completes login. Without
    `project` or `range` in the URL, the dashboard's stored last selection
    applies, then the first active project and `7d`. A
    dashboard without a project switcher (decision 5) takes no `project`
    and stores no last project; one without a range switcher takes no
    `range` and stores no last range.
35. **Selection is remembered per dashboard, server side.** Changing the
    project or range updates the URL and calls the view route
    (decision 30). Moving between report tabs (decision 36) carries the
    current project and range to the next tab and saves them there, so
    the five reports behave as one selection while storage stays per
    dashboard.
36. **Two shells, chosen by `owner`.** Both render widgets in the same
    grid (decision 10); only the page around them differs.
    - **System dashboards are reports:** one sidebar entry, **Reports**,
      opens them as tabs across the top (Views · Product · Users · Groups
      · Retention, in sort-key order), as Evidence's report navigation
      does today, under one header whose project and range apply to all
      of them (decision 35).
    - **User dashboards are standalone:** listed under **Yours** in the
      sidebar (by sort key, archived ones hidden), each a page with its
      own header.

    The header holds the title, the project switcher when the dashboard has one
    (active projects, archived ones in a collapsed group), the range
    switcher when it has one ("Custom…" opens a
    date-range `Calendar`, in a `Popover` on desktop and a `Sheet` on
    phones), "data as of" (the
    oldest `cached_at` on screen) and a dashboard refresh button; then the
    grid.
37. **Adaptive.** Widths adapt to the grid's own width through container
    queries (Tailwind's `@container`), not to the screen, because the
    sidebar changes the space the grid gets. The chrome adapts to the
    screen:

    | Grid width | Widths |
    | --- | --- |
    | ≥ 1024px | as defined |
    | 640–1023px | up to 6 becomes 6; above 6 becomes 12 |
    | < 640px | up to 3 becomes 6 (two stats across); above 3 becomes 12 |

    | Screen | Chrome |
    | --- | --- |
    | ≥ 1024px | sidebar; report tabs in a row; switchers in the header |
    | 640–1023px | sidebar collapses to icons; report tabs scroll sideways |
    | < 640px | sidebar in a drawer; report tabs become a select; switchers as two selects |

    The grid keeps its order at every width; only spans change, so a
    narrow screen is the same list wrapped sooner.

    Heights keep their row spans everywhere; charts thin their axis ticks
    on narrow screens; tables scroll inside their card.
38. **Each widget loads on its own** and has its own state: skeleton at the
    widget's height while loading; the component with data; "No data for this
    range"; "component removed"; "query no longer runs" with the error
    folded; "couldn't load" with a retry. A truncated result renders
    with a "partial" note over it.
39. **Refresh.** Each `sql` widget has a refresh icon (on hover on desktop,
    always on touch) that requests `fresh=true`; the dashboard button does
    it for every widget past its `refresh_after`. The icon is disabled
    until `refresh_after`, its tooltip showing the age and the wait.
    Markdown widgets have none.
40. **Markdown renders with `react-markdown`, raw HTML off:** without
    `rehype-raw` it never renders HTML, which is its default.
41. **Login is the existing OAuth server,** with the page as one more
    client: it registers (client id kept in `localStorage`), runs PKCE
    against the password page, and returns to `https://<api-host>/app/callback`.
    The access token stays in memory, the 30-day refresh token in
    `localStorage`; a 401 refreshes once, then asks to log in again.
    `redirectAllowed` accepts the API's own host. In `oauth://` mode the
    identity provider must allow that redirect. With a bare `token://` and
    no password, the page asks for the token.
42. **PWA:** `manifest.webmanifest` (`display: standalone`, `start_url`
    and `scope` `/app/`) and a service worker caching the app shell only,
    never API responses. Offline, the installed app opens and says so.
43. **Stack:** React, Vite, TypeScript, Tailwind, Recharts and shadcn,
    added whole up front (`npx shadcn@latest add --all` puts every general
    component and `chart` into `web/src/components/ui/` as source, so
    Vite's tree-shaking keeps the bundle to what is rendered),
    `react-markdown`, light and dark following the system. Popular frontend libraries are fine; the Go side stays lean.
    `web/`
    builds into `internal/reporting/ui/` (bundle and `components.json`),
    which is committed and drift-checked in CI like the SDK bundle.

### Local development

44. **`twillingate reporting dev <dir>… [--db <path>] [--addr <host:port>]`**
    serves the embedded UI on a loopback address (default
    `127.0.0.1:3100`, clear of Evidence's 3000) with no login and no
    cache, reading dashboards from the given directories (each a
    dashboard or a parent of several) instead of the database, and
    running them against `--db`. It
    polls the files twice a second and reloads the page on change.
    Validation errors show in the widget's card, or as a banner for a
    broken `dashboard.json`. In `web/`, `npm run dev` proxies `/api` to it
    for hot-reloading components against real data.
45. **The command is `twillingate reporting dev`**, not under `dashboards`, which still
    runs Evidence until the second PR.

### Custom SQL only reads

46. **Every piece of custom SQL runs through one read-only guard.**
    Widget SQL (on `widget_data`, on validation, in the migrator's check
    of system widgets, in `reporting dev`) and the `query` operation over
    MCP and REST share `internal/shared/readsql`, which today's guards in
    `internal/api` (`readdb.go`, `ops_query.go`) move into:

    | Layer | Stops |
    | --- | --- |
    | the pool opens the file `mode=ro` | any write through the connection |
    | `query_only(1)` on the pool | writes, even through a path that finds one |
    | `_defensive=1` on the pool (new) | the schema-corrupting operations SQLite's defensive mode blocks |
    | the SQL runs wrapped as `SELECT * FROM (…) LIMIT n` | DML, DDL, `PRAGMA`, `VACUUM` and several statements: all become syntax errors |
    | `ATTACH` refused as a keyword by the tokenizer (decision 47) | attaching another file, which `mode=ro` does not prevent |
    | timeout and row cap (`API_QUERY_*`) | runaway reads |

    The driver (`modernc.org/sqlite`) exposes no authorizer, so these
    layers are the guarantee. `reporting`'s executor accepts only
    `readsql`'s read-only handle, a distinct type, so handing it the
    store's writer does not compile. `reporting dev` opens `--db` through
    the same function.
47. **Reads reach every table except `meta` and SQLite's own.** A
    database holds one tenant, and whoever holds the API login controls
    that tenant's data, so custom SQL may read any table, not only the
    `v_*` views; ingest keys in particular are public by design (every
    tracked page carries one) and an agent reads them to install the
    snippet. Two kinds of name are refused:
    - `meta`, which holds the daily visitor salt: with it, custom SQL
      could brute-force visitor IPs from `sha256(salt, ip, user_agent,
      project)`;
    - SQLite's internals: every `sqlite_*` name (`sqlite_schema` and
      `sqlite_master`, `sqlite_temp_schema`, `sqlite_sequence`,
      `sqlite_stat1`–`4`, and `sqlite_dbpage`, whose raw pages would
      bypass any table rule), every `pragma_*` table-valued function, and
      `dbstat`. Agents learn the views from `schema://views`, not from the
      schema table.

    `readsql` tokenizes the SQL (skipping string literals and comments)
    and refuses an identifier with one of those names, quoted or not,
    with or without a schema prefix: "sql reads meta, which custom SQL
    may not read". A tokenizer, not a substring match, so
    `WHERE event_name = 'meta'` and a column named `attachment` pass;
    `ATTACH` moves into the same tokenizer as a keyword. Without an
    authorizer this is sound because custom SQL cannot create views, the
    only way to reach a table is to name it, and SQLite identifiers have
    no escapes; the one indirect path, an existing view that reads a
    refused table, is closed by a test over every `v_*` definition.


### Archive and purge

48. **Archived projects, dashboards and widgets are deleted after
    `RETENTION_ARCHIVED_DAYS`** (default 30; `0` keeps them forever), one
    value for all three. Every day the daily pass's prune step (03:00 UTC)
    deletes whatever has `archived_at + RETENTION_ARCHIVED_DAYS` in the
    past; nothing else is tracked. Each deletion runs in its own
    transaction with an `audit_log` entry, actor `retention`:
    - a widget: its row;
    - a dashboard: its row, and its widgets by cascade;
    - a project: everything `DeleteProject` deletes (its events,
      aggregates, ingest keys and registry row), then a registry reload.
      This is new: until now an archived project kept its data
      indefinitely.

    System dashboards and widgets cannot be archived, so the purge never
    touches them. Archived items expose only `archived_at`; the purge
    date follows from it and is documented, not returned. There is no
    grace period on upgrade: the first night after it, projects archived
    more than `RETENTION_ARCHIVED_DAYS` ago go with their data, which
    `deploy/UPGRADES.md` states as an instruction.
49. **The UI never shows archived items.** `list_widgets` returns archived
    widgets with their `archived_at`, so an agent can find one to
    restore; `get_dashboard` and the grid show live widgets only.


### Guidance for agents

50. **Agents learn reporting from one contract page, delivered three
    ways.**
    - **`docs/reporting.md`, served as `docs://reporting`:** a third
      contract page beside `docs://twillingate` and `docs://deployment`.
      It holds the concepts, the authoring workflow, the component table
      (inputs, props, default sizes), parameters and what a widget
      follows, range presets, the grid, archiving and the purge, one worked
      SQL example per component, and each refusal with its fix. The
      reporting material lives here rather than in `docs/twillingate.md`,
      which stays about collecting and querying data.
    - **`reporting_guide`, an MCP-only tool** like `integration_guide`,
      because many clients never read resources: one call returns the
      registered components and source types (live, from the database),
      the `v_*` views (`schema://views`), the active projects, the
      existing dashboards (id, title, owner, archived), and the workflow
      and rules sections of `docs/reporting.md`. The descriptions of
      `create_dashboard`, `add_widget`, `update_widget` and `copy_widget`
      begin "Call reporting_guide first."
    - **Live resources,** following `schema://projects` (a JSON snapshot
      built on each read), for clients that attach resources: each
      returns what its list tool returns, from the same code, so no REST
      route is added.

      | Resource | Content |
      | --- | --- |
      | `schema://components` | the registered source types, and per component: name, description, accepts, inputs, props, default width and height (`list_components`) |
      | `schema://dashboards` | the instance `timezone`, and every dashboard in sidebar order: id, title, owner, stored selection, widget count, `archived_at` (`list_dashboards`) |
      | `schema://widgets` | every widget with its dashboard, component, title, size, source type and source, and `archived_at` (`list_widgets`, unfiltered) |
    - **MCP server `instructions`** (sent on connect, which the server
      does not set today): "To integrate a site or app, call
      integration_guide. To build or change dashboards, call
      reporting_guide. System dashboards are read-only; duplicate one to
      customize it."

    The workflow it teaches: explore with `query` against the `v_*`
    views; pick a component and alias columns to its inputs
    (`day AS x`); choose what the widget follows (`:project`,
    `:from`/`:to`, or fixed); `add_widget` with `after`, `width` and
    `height`; check `widget_data`; fix with `update_widget`; archive to
    undo; duplicate a system dashboard to customize it.

## Migration 021

`021_reporting.sql` creates `components`, `dashboards`, `widgets` and
`reporting_migrations`, the unique indexes of decision 6, and the
foreign keys from widgets to dashboards (cascading) and to components,
and sets
`sqlite_sequence` for `dashboards` to 1000. It copies nothing. The
system dashboards arrive through the migrator on the same run. Its test
pins the ceiling at 21 and migrates to latest before calling current Go
code, per the standing rules.

## Tests

| Area | Proves |
| --- | --- |
| validation | each refusal in decision 27 fires with its sentinel and message |
| following | for project and for range independently: a widget whose SQL uses the parameters requires them in `widget_data` and keys its cache by them; a fixed widget ignores them and keys without them; each switcher is present exactly when a widget follows it; the view route requires and refuses each part to match; a widget following neither is cached per widget only |
| order | the key generator: a key between any two keys sorts strictly between them, before the first and after the last, including adjacent and long keys; dashboards: `after` on create and update writes one key; system dashboards sort in `id` order |
| layout | a 6 × 6 widget followed by four 3 × 3 widgets renders them as a 2 × 2 block beside it (browser test); insert first, last and `after` (one key written, no other record touched); removal touches no other record; many inserts at one spot keep keys ordered and short enough; the `layout` list round-trips; widths and heights default from the component; the migrator keeps widget ids when order or size changes |
| foreign keys | deleting a dashboard deletes its widgets; deleting a component sets `component` null on its widgets; writer connections report `foreign_keys = 1`; `DeleteProject` succeeds with keys present; a migration leaving a violation fails `foreign_key_check`; existing databases pass the check after 021 |
| source types | an unregistered `source_type` is refused; a component's `accepts` naming an unregistered type fails the migrator; `sql` and `md` each validate and load through the registry |
| archive | archiving a widget hides it and restoring puts it back in the same place; a widget inserted next to an archived one gets a key that does not collide with it; archived widgets keep their names; an archived widget refuses update and copy; `list_widgets` shows archived ones with `archived_at` |
| purge | with `RETENTION_ARCHIVED_DAYS=30`, a widget, a dashboard (with its widgets) and a project (with its events, aggregates and keys) archived 31 days ago go and one archived 29 days ago stays; `0` keeps everything; an item archived before the upgrade is judged by its own `archived_at`; system dashboards are never archived; each purge writes an audit entry and the registry reloads after a project goes |
| components | a component gone from the manifest is deleted and its widgets get a null `component`; such a widget answers `removed`, accepts only a component switch, resize or archiving, and cannot be copied; a component that returns leaves them null |
| migrator | upserts, deletions, reserved ids, widget ids stable across edits and moves, `last_*` kept, a second run with the same hash writes nothing, a failure writes nothing |
| **system dashboards** | every system widget, on a database migrated to latest with seeded data, validates and runs for every preset range, and its rows satisfy its component. The load-bearing test |
| files | pairing errors (no data file, two, orphan data file, unplaced or missing widget, duplicate or out-of-range `id`) |
| ranges | the UI resolves each preset to the dates in decision 18 (in the reported timezone, around its midnight); the API refuses a missing date, `from > to` and spans over 365 days, and clamps a future `to`; unknown presets are rejected by `create_dashboard` and the view route; a new dashboard starts on its given range (or `7d`), and the migrator sets a system dashboard's range only on insert |
| limits | a widget query past `API_QUERY_TIMEOUT` is refused naming it; a result past `API_QUERY_MAX_ROWS` is cut and answers `truncated` |
| cache | the age rules of decision 33, invalidation, one run for simultaneous requests, the boot refusal |
| manifest | `components.json` matches the widget files in `web/`, and Go loads it |
| component render | Vitest with Testing Library, per component: renders from sample rows, from no rows, and with each prop; a closed interface's missing optional input (`series`, `previous`, `max`, `size`) renders the simpler form |
| guide | `reporting_guide` returns the live components, source types, views, projects and dashboards plus the workflow section; the server's `instructions` name both guides; `docs://reporting` is served; `schema://components`, `schema://dashboards` and `schema://widgets` return exactly what `list_components`, `list_dashboards` and unfiltered `list_widgets` return, reflecting a write made just before |
| api | MCP ↔ REST parity (the view route REST-only by choice, `reporting_guide` MCP-only by choice); `docs_sync_test` reads `docs/reporting.md` and binds its component table to `components.json` in both directions, and gains the tools, routes, both `REPORTING_*` settings and the range vocabulary; `redirectAllowed` accepts the API host; OAuth end to end through `/app/callback` |
| read-only | one table of write attempts through `query` and through `widget_data` (`INSERT`, `UPDATE`, `DELETE`, `REPLACE`, `DROP`, `CREATE [TEMP] TABLE`, `WITH … INSERT`, `PRAGMA x = y`, `ATTACH`, `VACUUM INTO`, two statements, `load_extension()`): every one is refused and the database file's checksum is unchanged afterwards |
| refused names | `meta`, `sqlite_master`, `sqlite_schema`, `sqlite_sequence`, `sqlite_stat1`, `sqlite_dbpage`, `pragma_table_info(…)` and `dbstat` are refused through `query` and `widget_data` in every spelling (quoted `"meta"`, `` `meta` ``, `[meta]`, `main.meta`, upper case); `'meta'` in a string literal, `meta` in a comment, and columns such as `metadata` and `attachment` pass; no `v_*` view definition references a refused name |
| archtest | `internal/reporting` at rank 1, `internal/shared/sortkey` and `internal/shared/readsql` at rank 0 |
| browser | Playwright against a seeded `serve`: log in, open every system dashboard at desktop and phone width, no error cards; report tabs carry project and range from tab to tab; a user dashboard opens in the standalone shell |

## Docs

In the same PR as the change:

- `docs/reporting.md` (new, `docs://reporting`, decision 50): dashboards,
  widgets and components; the component table (decision 13); the
  parameters and range vocabulary; the tools, routes and the
  `schema://components|dashboards|widgets` resources; the authoring
  workflow with a worked example per component; what happens to widgets
  when a release removes a component; archiving as undo, and the purge of
  archived projects, dashboards and widgets after
  `RETENTION_ARCHIVED_DAYS`, including that a purged project's data is
  gone.
- `docs/twillingate.md`: a pointer to `docs://reporting` and
  `reporting_guide` in "Answer questions with the data"; the purge of
  archived projects in the project lifecycle.
- `docs/deployment.md`: `/app/` and installing it; the redirect an
  `oauth://` provider must allow; `REPORTING_CACHE_MINUTES`,
  `REPORTING_REFRESH_MINUTES` and `RETENTION_ARCHIVED_DAYS`; that `API_QUERY_TIMEOUT` and
  `API_QUERY_MAX_ROWS` also bound widget queries; `twillingate reporting
  dev`.
- `deploy/UPGRADES.md`: 021 adds `/app/`; archived projects, dashboards
  and widgets are now deleted `RETENTION_ARCHIVED_DAYS` after archiving,
  and the first night after upgrading deletes projects archived longer
  ago than that, with their data: before upgrading, restore the ones to
  keep, or set `RETENTION_ARCHIVED_DAYS=0`; a release that removes a
  component leaves its widgets showing "component removed" until an agent
  switches or archives them; a binary rollback re-migrates system
  dashboards and permanently clears the component of widgets on
  components the older release lacks (decision 25).
- `CLAUDE.md`: three contract pages, not two (`docs/reporting.md` joins,
  and the "do not add files there" rule names it), with its rows in the
  docs table (reporting tools, components, views used by system
  dashboards); `internal/reporting`, `internal/shared/` (small generic
  leaf packages: `sortkey`, `readsql`) and `web/` in the layout; the
  build-and-commit rule extended to `web/`; the docs table rows.

## Rollout

1. **`feat: serve read-only dashboards at /app/ that agents build over MCP`.**
   Everything above. Evidence is untouched.
2. **Parity check on prod** after that release: each system dashboard
   next to its Evidence page, same project and range, same numbers.
3. **`feat!: replace the Evidence dashboards with /app/`.** Deletes
   `evidence/`, `internal/dashboards`, the `dashboards` subcommand, the
   `DASHBOARDS_*` settings, `docker-compose.evidence.yml` and the Evidence
   image job. `docs/deployment.md` loses the Evidence section and the
   "dashboards at home" topology (the litestream replica stays, for backup
   and restore). `deploy/UPGRADES.md` says to drop the Evidence file from
   `COMPOSE_FILE`. Breaking, so it bumps the minor. The homelab dashboards
   container retires with it.

## Open before planning

- **Cold-load timing.** Run the five system dashboards' queries against a
  copy of the prod database with an empty cache, per range. The result
  sets the `REPORTING_CACHE_MINUTES` default and says whether any system
  widget needs a cheaper query.

## Out of scope

- Multi-tenant cloud: accounts, billing, one database per tenant.
- A native desktop app (Wails or Tauri) around the same bundle.
- Any editing in the UI.
- Deleting on request. Agents archive and restore projects, dashboards
  and widgets; deletion happens only through the purge (decision 48) and
  the migrator's removal of system dashboards gone from the code.
- Source types other than `sql` and `md` (images and others).
- Import or export of dashboards as files.
- Drill-down between dashboards, and parameters beyond the three.
- Automatic refresh timers.
- Moving the existing small generic packages (`internal/civil`,
  `internal/version`) under `internal/shared/`: a later `refactor:` PR,
  after the first reporting PR, including the version path in the
  Makefile's `-ldflags -X`.
- A `TIMEZONE` setting that groups days by a local timezone: its own
  spec, after this one. It moves `events.day` from a generated UTC column
  to one ingest fills, salt rotation and the daily pass to local
  midnight, and records timezone changes with the day they took effect.
  Reporting needs no change for it beyond reading `timezone`.
