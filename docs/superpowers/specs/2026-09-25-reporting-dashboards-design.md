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
   (decision 32).
2. **Agents are the only authors.** Dashboards and widgets are created and
   changed through MCP tools and REST routes (decisions 25–27). The UI has
   no editing.
3. **Installable as a PWA.** A manifest and an app-shell service worker
   make "Install app" (Chrome, Edge) and "Add to Dock" (Safari) give it its
   own window. No native download in this work.
4. **One package, `internal/reporting`,** at rank 1 next to `manage`: the
   models, validation, operations, the system-definition migrator, the
   cache, and the embedded UI. It declares `reporting.Store`, the slice of
   the store it uses, which `store/sqlite` implements. `api` exposes its
   operations through `expose()` and mounts `/app/`.
5. **Every dashboard is project agnostic.** Nothing in a dashboard names a
   project; the viewer picks one.

### Model

6. **Migration 021 creates three tables and a history table:**

   ```
   components            name PK, description, accepts (JSON), inputs (JSON), props (JSON),
                         default_height, removed_at
   dashboards            id INTEGER PK AUTOINCREMENT, owner ('system'|'user'), title, position,
                         default_range, layout (JSON), last_project_id, last_range,
                         created_at, updated_at, archived_at
   widgets               id INTEGER PK AUTOINCREMENT, dashboard_id, name, component, title,
                         props (JSON), source_type ('sql'|'md'), source (TEXT),
                         created_at, updated_at
   reporting_migrations  id INTEGER PK AUTOINCREMENT, hash, version, applied_at
   ```

   No checks, foreign keys or triggers: every rule below is enforced in Go
   (the standing rule, no validation in the database). `widgets` is unique
   on (`dashboard_id`, `name`) as an index, because the migrator matches on
   it.
7. **Ids 1–999 are reserved for system dashboards.** Migration 021 sets the
   `dashboards` sequence to 1000, so agent-made dashboards start at 1001 and
   can never collide with a system id.
8. **A widget is a component plus a source.** `source_type` is `sql` (a
   query) or `md` (Markdown text). `title` is optional; the card shows a
   header only when it has one. `name` is unique within the dashboard and
   is how the layout refers to the widget; an agent gives it or it is
   derived from the title (lower case, `-` for runs of other characters),
   with `-2`, `-3`, … added when taken.
9. **Layout is rows, stored on the dashboard.** `layout` is
   `[{"height": 1..3, "widgets": ["name", …]}, …]`: 1–3 widgets per row,
   side by side at equal width, every widget of the dashboard placed exactly
   once. A row with no `height` takes the largest `default_height` among its
   widgets. One height unit is about 140px.
10. **`owner = 'system'` rows change only through the migrator** (decisions
    17–22). Every write operation refuses them with `ErrInvalid`.

### Components

11. **A component is a React component in `web/` plus its contract.** Each
    `web/src/components/widgets/<name>.tsx` exports the component and its
    `accepts` (source types), `inputs` (for `sql`), `props` (a JSON schema)
    and `defaultHeight`. The web build writes `components.json` from these
    exports. Nothing is declared twice.
12. **The first set:**

    | Component | Accepts | Inputs: the columns the query returns | Props | Default height |
    | --- | --- | --- | --- | --- |
    | `stat` | `sql` | `value` number; `previous` number, optional (shows the change) | `format`: `number`\|`percent`\|`duration` | 1 |
    | `line` | `sql` | `x` day or text; `y` number; `series` text, optional (one line per value) | `format` | 2 |
    | `area` | `sql` | as `line` | `format`, `stacked` | 2 |
    | `bar` | `sql` | `x` text; `y` number; `series` text, optional | `format`, `horizontal`, `stacked` | 2 |
    | `table` | `sql` | open: any columns, shown in query order | `formats`: column → format | 3 |
    | `markdown` | `md` | none | none | 1 |

    Queries satisfy inputs by alias: `SELECT day AS x, visitors AS y …`.
    Returning a column a closed interface does not declare is refused.
13. **A removed component is kept while it is used.** When a component
    leaves the code, the migrator sets `removed_at`. A removed component
    cannot be used by `add_widget`, `update_widget` (switching to it) or
    `copy_widget` (copying a widget on it). Widgets already on it render a
    "component removed" card, and `widget_data` answers `{"removed": true}`.
    Such a widget's fields cannot be edited except to switch it to a live
    component; it can still be moved in the layout, or removed.
14. **A removed component is deleted with its last widget.** Every
    transaction that can drop the last reference (`remove_widget`,
    `update_widget` switching component, the migrator deleting a system
    dashboard or widget) ends by deleting removed components no widget
    uses. Widgets on archived dashboards count as uses, since the dashboard
    can be restored.
15. **A component that comes back is live again:** the migrator clears
    `removed_at` on upsert.

### Parameters and ranges

16. **A `sql` source gets exactly three named parameters:** `:project`
    (project id), `:from` and `:to` (UTC days, `YYYY-MM-DD`). Using any
    other parameter is refused. Ranges are a closed vocabulary: `1d`, `7d`,
    `30d`, `90d`, `180d`, `365d`. For `Nd`, `:to` is today (UTC) and
    `:from` is today − (N − 1) days, the window the Evidence pages use.
    Each dashboard has a `default_range`.

### System dashboards: files, migrated on every run

17. **System dashboards are files in the repo**, embedded with `go:embed`:

    ```
    internal/reporting/system/<dir>/
      dashboard.json     {"id": 1, "title": "Views", "position": 1, "default_range": "7d",
                          "layout": [{"height": 1, "widgets": ["visitors", "views", "bounce"]}, …]}
      <name>.json        {"component": "stat", "title": "Visitors", "props": {"format": "number"}}
      <name>.sql         the query, plain SQL: runs in sqlite3 as it is
      <name>.md          or Markdown, for a markdown widget
    ```

    A widget is `<name>.json` plus exactly one `<name>.sql` or `<name>.md`;
    the extension is the source type. A config with no data file or two, a
    data file with no config, a layout naming a missing widget, or a widget
    the layout leaves out, is an error. The directory name is for people;
    only `id` identifies the dashboard.
18. **Matching keys:** a system dashboard by `id`; a widget by
    (`dashboard_id`, file name without extension); a component by name.
19. **The migrator runs where the schema migrates.** `app.Migrate(ctx, st)`
    runs `st.Migrate`, then `reporting.Migrate`. `serve` (via `app`), the
    `migrate` command and the `project` command, the three places that
    migrate today, call it. `cmd` keeps importing only `app`.
20. **It is tracked like `schema_migrations`.** The hash covers every
    embedded `dashboard.json`, widget file and `components.json`. If the
    latest `reporting_migrations` row has that hash, nothing runs.
    Otherwise, in one transaction:
    1. components: upsert by name and clear `removed_at`; set `removed_at`
       on the ones missing from the manifest;
    2. system dashboards: upsert by `id` (title, position, default range,
       layout), keeping `last_project_id` and `last_range`; delete system
       dashboards no file claims, with their widgets;
    3. widgets, per system dashboard: upsert by name, keeping the id;
       insert new ones; delete ones with no file;
    4. delete removed components no widget uses;
    5. append a `reporting_migrations` row (hash, build version) and one
       `audit_log` entry, actor `release`, listing what was added, changed
       and removed.

    Every system definition passes the same validation as
    `create_dashboard` (decision 24) before anything is written.
21. **A failure stops the run** the way a failed schema migration does: the
    transaction rolls back and `serve` does not start. The system dashboard
    test (see [Tests](#tests)) runs the same validation in `make check`.
22. **Rolling the binary back re-migrates to the older definitions,** since
    the hash differs: components the older binary does not know get
    `removed_at`, and their widgets show "component removed" until the
    upgrade returns. `deploy/UPGRADES.md` says so.
23. **The five Evidence pages become system dashboards:** Views (id 1),
    Product (2), Users (3), Groups (4), Retention (5), each with the
    Evidence page's default range (`7d`; Retention `90d`) and its stats,
    charts and tables. The drill-down into a single page
    (`views/[project]/page.md`) is dropped: it needs a path parameter that
    decision 16 does not provide. The empty-database sentinel rows are not
    carried over.

### Validation

24. **Every write validates the whole result** and refuses with
    `ErrInvalid`, in words an agent can act on:
    - the component exists, is not removed, and accepts the source type:
      "component `pie` does not exist; list_components names the 6 there
      are";
    - `sql`: only the three parameters ("sql uses `:path`; widgets get only
      `:project`, `:from` and `:to`"); the query runs as
      `SELECT * FROM (…) LIMIT 0` on the read pool with sample values bound
      and the guards `query` applies (read-only, no `ATTACH`, the timeout);
      its columns satisfy the inputs ("line needs y (number); columns are
      x, visitor"); value types are checked on a few rows of a project that
      has data: the call's optional `project_id`, else the most recently
      active project, else column names only, with the full check on first
      render;
    - `md`: the text is not empty;
    - props match the component's `props` schema;
    - the layout: 1–3 widgets per row, height 1–3, every widget placed once
      ("row 2 already holds 3 widgets; omit row to start a new one");
    - system rows: "dashboard 3 is a system dashboard and changes only with
      a release; duplicate_dashboard makes an editable copy".

### Agent surface

25. **Read tools:**

    | Tool | Route | Returns |
    | --- | --- | --- |
    | `list_components` | `GET /api/components` | name, description, accepts, inputs, props, default height; `include_removed` adds removed ones |
    | `list_dashboards` | `GET /api/dashboards` | id, title, owner, position, default range, last project and range, widget count, archived |
    | `get_dashboard` | `GET /api/dashboards/{dashboard_id}` | the dashboard with its layout, and every widget's component, title, props, source type and source |
    | `list_widgets` | `GET /api/widgets?dashboard_id=&component=&removed=` | every widget with its dashboard (id, title, owner, archived); filters combine; `removed=true` lists widgets on removed components |
    | `widget_data` | `GET /api/widgets/{widget_id}/data?project_id=&range=&fresh=` | the widget's content for a project and range (decision 29) |

26. **Write tools,** each refused on system dashboards and recorded in
    `audit_log` with actor `mcp` or `rest`:

    | Tool | Route | Effect |
    | --- | --- | --- |
    | `create_dashboard` | `POST /api/dashboards` → 201 | title, default range, optional widgets and layout; all or nothing |
    | `update_dashboard` | `PATCH /api/dashboards/{dashboard_id}` | title, default range, position, `layout` (rearrange and resize in one call) |
    | `duplicate_dashboard` | `POST /api/dashboards/{dashboard_id}/duplicate` → 201 | a user copy of any dashboard, system ones included |
    | `archive_dashboard` / `restore_dashboard` | `POST /api/dashboards/{dashboard_id}/archive` / `…/restore` | hide or unhide |
    | `add_widget` | `POST /api/dashboards/{dashboard_id}/widgets` → 201 | no `row`: a new last row; `row` alone: append; `row` and `col`: insert and shift right |
    | `update_widget` | `PATCH /api/widgets/{widget_id}` | name, component, title, props, source |
    | `copy_widget` | `POST /api/widgets/{widget_id}/copy` → 201 | an independent copy into `dashboard_id`, optional `row`/`col`; the source may be a system widget |
    | `remove_widget` | `DELETE /api/widgets/{widget_id}` | removes it from the layout; a row left empty disappears |

    A source is `{"type": "sql"|"md", "content": "…"}`.
27. **REST only:** `PUT /api/dashboards/{dashboard_id}/view` with
    `project_id` and `range`, which sets `last_project_id` and `last_range`.
    It is allowed on system dashboards (it is viewer state, not
    definition) and writes no audit entry. MCP-only or REST-only is an
    explicit choice the parity test checks.
28. **One error vocabulary.** `ErrInvalid` moves to `store`, next to
    `ErrNotFound` and `ErrConflict`, and `manage.ErrInvalid` becomes that
    same value, so `errors.Is` keeps working and `api` maps `manage` and
    `reporting` refusals through one path.

### Data and cache

29. **`widget_data` returns one widget's content:**

    ```
    { "widget_id": 42, "from": "2026-08-27", "to": "2026-09-25",
      "cached_at": "…", "refresh_after": "…",
      "columns": ["x", "y"], "rows": [["2026-08-27", "40"], …] }
    ```

    `md` widgets answer `{"widget_id", "markdown"}`, ignore project and
    range, and are not cached. A widget on a removed component answers
    `{"widget_id", "removed": true}`. A query that no longer runs, or whose
    rows no longer satisfy the inputs (a release changed a view), is
    `ErrInvalid` with the reason. Queries run on the read pool with the
    `query` guards, row cap and timeout.
30. **One cache, two ages.** An in-memory entry per (widget, project,
    range) records when it was computed:

    | Setting | Default | Meaning |
    | --- | --- | --- |
    | `REPORTING_CACHE_MINUTES` | 15 | an ordinary request reuses an entry younger than this; `0` recomputes every time |
    | `REPORTING_REFRESH_MINUTES` | 1 | a `fresh=true` request reuses an entry younger than this |

    Past the relevant age, the request recomputes and stores the result.
    Entries live for the longer of the two. A refresh age longer than a
    non-zero cache age refuses the boot. `refresh_after` is `cached_at`
    plus the refresh age. Identical requests in flight share one run
    (`singleflight`, same key). `update_widget` drops that widget's
    entries; a copy starts empty; the migrator drops entries of widgets it
    changed.

### UI

31. **Routes:** `/app/` goes to the last dashboard opened on this device,
    else the first system dashboard; `/app/dashboards/{id}?project=&range=`
    shows one; `/app/callback` completes login. Without `project` or
    `range` in the URL, the dashboard's `last_project_id` and `last_range`
    apply, then the first active project and `default_range`.
32. **Selection is remembered per dashboard, server side.** Changing the
    project or range updates the URL and calls the view route
    (decision 27).
33. **Layout:** a sidebar with **Built-in** (system dashboards by position)
    and **Yours** (user dashboards by position), archived ones hidden; a
    header with the title, the project switcher (active projects, archived
    ones in a collapsed group), the range switcher, "data as of" (the
    oldest `cached_at` on screen) and a dashboard refresh button; then the
    rows.
34. **Adaptive:**

    | Width | Rows | Chrome |
    | --- | --- | --- |
    | ≥ 1024px | as defined | sidebar; switchers in the header |
    | 640–1023px | 3-widget rows wrap to 2 + 1 | sidebar collapses to icons |
    | < 640px | one widget per row, in order; a row of only `stat` widgets goes 2 across | sidebar in a drawer; switchers as two selects |

    Heights keep their pixel size everywhere; charts thin their axis ticks
    on narrow screens; tables scroll inside their card.
35. **Each widget loads on its own** and has its own state: skeleton at the
    row's height while loading; the component with data; "No data for this
    range"; "component removed"; "query no longer runs" with the error
    folded; "couldn't load" with a retry.
36. **Refresh.** Each `sql` widget has a refresh icon (on hover on desktop,
    always on touch) that requests `fresh=true`; the dashboard button does
    it for every widget past its `refresh_after`. The icon is disabled
    until `refresh_after`, its tooltip showing the age and the wait.
    Markdown widgets have none.
37. **Markdown renders with raw HTML off.**
38. **Login is the existing OAuth server,** with the page as one more
    client: it registers (client id kept in `localStorage`), runs PKCE
    against the password page, and returns to `https://<api-host>/app/callback`.
    The access token stays in memory, the 30-day refresh token in
    `localStorage`; a 401 refreshes once, then asks to log in again.
    `redirectAllowed` accepts the API's own host. In `oauth://` mode the
    identity provider must allow that redirect. With a bare `token://` and
    no password, the page asks for the token.
39. **PWA:** `manifest.webmanifest` (`display: standalone`, `start_url`
    and `scope` `/app/`) and a service worker caching the app shell only,
    never API responses. Offline, the installed app opens and says so.
40. **Stack:** React, Vite, TypeScript, Tailwind and shadcn (`Sidebar`,
    `Sheet`, `Card`, `Select`, `ToggleGroup`, `Table`, `Chart` on
    Recharts, `Skeleton`), light and dark following the system. `web/`
    builds into `internal/reporting/ui/` (bundle and `components.json`),
    which is committed and drift-checked in CI like the SDK bundle.

### Local development

41. **`twillingate reporting run <dir> [--widget <name>] --project <id> --range <r> [--db <path>]`**
    validates a dashboard directory and runs its widgets against a
    database, printing rows as text. It needs no server, and is what an
    agent in a terminal runs.
42. **`twillingate reporting dev <dir>… [--db <path>] [--addr <host:port>]`**
    serves the embedded UI on a loopback address (default
    `127.0.0.1:3100`, clear of Evidence's 3000) with no login and no
    cache, reading
    dashboards from the given directories (each a dashboard or a parent of
    several) instead of the database, and running them against `--db`. It
    polls the files twice a second and reloads the page on change.
    Validation errors show in the widget's card, or as a banner for a
    broken `dashboard.json`. In `web/`, `npm run dev` proxies `/api` to it
    for hot-reloading components against real data.
43. **The command group is `reporting`**, not `dashboards`, which still
    runs Evidence until the second PR.

## Migration 021

`021_reporting.sql` creates `components`, `dashboards`, `widgets` and
`reporting_migrations`, the unique index on `widgets (dashboard_id, name)`,
and sets `sqlite_sequence` for `dashboards` to 1000. It copies nothing. The
system dashboards arrive through the migrator on the same run. Its test
pins the ceiling at 21 and migrates to latest before calling current Go
code, per the standing rules.

## Tests

| Area | Proves |
| --- | --- |
| validation | each refusal in decision 24 fires with its sentinel and message |
| layout | add, copy, remove and `update_dashboard` layouts keep rows at 1–3 widgets with every widget placed once; append, insert-with-shift, empty-row removal |
| components | a removed component is refused for new use, answers `removed`, and is deleted with its last widget, including when the migrator deletes a system dashboard |
| migrator | upserts, deletions, reserved ids, widget ids stable across edits and moves, `last_*` kept, a second run with the same hash writes nothing, a failure writes nothing |
| **system dashboards** | every system widget, on a database migrated to latest with seeded data, validates and runs for every range, and its rows satisfy its component. The load-bearing test |
| files | pairing errors (no data file, two, orphan data file, unplaced or missing widget, duplicate or out-of-range `id`) |
| cache | the age rules of decision 30, invalidation, one run for simultaneous requests, the boot refusal |
| manifest | `components.json` matches the widget files in `web/`, and Go loads it |
| api | MCP ↔ REST parity (the view route REST-only by choice); `docs_sync_test` gains the tools, routes, both settings and the range vocabulary; `redirectAllowed` accepts the API host; OAuth end to end through `/app/callback` |
| archtest | `internal/reporting` at rank 1 |
| browser | Playwright against a seeded `serve`: log in, open every system dashboard at desktop and phone width, no error cards |

## Docs

In the same PR as the change:

- `docs/twillingate.md`: dashboards, widgets and components; the
  component table (decision 12); the parameters and range vocabulary; the
  tools and routes; the removed-component lifecycle.
- `docs/deployment.md`: `/app/` and installing it; the redirect an
  `oauth://` provider must allow; `REPORTING_CACHE_MINUTES` and
  `REPORTING_REFRESH_MINUTES`; `twillingate reporting run|dev`.
- `deploy/UPGRADES.md`: 021 adds `/app/`; `list_widgets removed=true` is
  the upgrade-day list of widgets to fix; a binary rollback re-migrates
  system dashboards (decision 22).
- `CLAUDE.md`: `internal/reporting` and `web/` in the layout; the
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
- Source types other than `sql` and `md` (images and others).
- Import or export of dashboards as files.
- Drill-down between dashboards, and parameters beyond the three.
- Automatic refresh timers.
