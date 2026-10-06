# Widget shares

Status: draft
Date: 2026-10-05

## Problem

- **A chart cannot leave the console.** Every byte of the console sits
  behind one credential (`internal/api/server.go:38-79`), and the app
  refuses to be framed (`internal/reporting/ui.go:80-82`). To post a chart
  on X, LinkedIn, Mastodon or Telegram, or to put one on a blog, a user
  takes a screenshot by hand, and nothing links back.
- **A public widget cannot run live without opening the database.** A
  widget's SQL is arbitrary read SQL over the whole file
  (`internal/shared/readsql/check.go:290-296`); it is limited to a project
  only by `:project`, and the server does not check the `project_id` a
  caller sends (`internal/reporting/data.go:196-248`). Running widgets for
  anonymous visitors would need a sandbox we do not have.
- **The binary cannot draw a chart.** No Go file imports `image/*` or
  writes SVG, and a headless browser is too heavy a dependency. The
  browser, however, already draws every widget.

## Scope

Widgets only. Public dashboards, live public data and iframe embeds are
out of scope.

## Decisions

- **D1. A share is a frozen image under an unlisted UUIDv7.** The browser
  captures one widget as a PNG and uploads it. The server stores the PNG
  under a UUIDv7 and never queries the widget again. Neither the project
  nor the range can change after capture, since both are part of the
  picture. The picture is immutable: a new picture means a new link. A
  share comes down when it is archived, by hand or by its **Archive
  after** date (D9), and the daily pass deletes it once it has been
  archived for `RETENTION_ARCHIVED_DAYS`, like any archived dashboard or
  widget.

  The id is also what keeps an unlisted link unlisted, so how much of it
  is random matters. `google/uuid` (already a dependency, and the
  generator of event ids in `internal/server/handlers.go:27`) fills
  `rand_a` with a sub-millisecond sequence, which leaves 62 random bits in
  `rand_b`. Even with the millisecond known, a guess is one in 2^62
  (4.6·10^18): a year of 1,000 guesses a second finds a given share with
  odds below 10^-8. The 48-bit timestamp tells a holder of the link when
  the share was made, which the page does not hide anyway. In return, the
  id sorts by creation time, so the primary key appends instead of
  scattering, and it is one id scheme across the codebase. The id is
  stored and shown in canonical form (36 characters). If `NewV7` fails
  (entropy exhaustion only), it falls back to a v4, as `newID` does.

- **D2. Shares live on the console, at `CONSOLE_URL/share/<id>`.** Shares
  are a reporting feature: the console creates, lists and archives them,
  and `reporting` already embeds the app's icon and look. The ingest
  surface stays a pure collector (`POST` events, serve the SDK), and it can
  keep running as its own process. A blocklisted collector hostname, which
  is likely for a privacy-minded audience, costs only the opens count
  (D7), never the share itself. And the console's hostname is the
  product's face, which reads better in a post than a tracker domain.
  Links are built from `CONSOLE_URL`, which defaults to `PUBLIC_URL`, so
  there is no new setting.

  | Route | Answers |
  | --- | --- |
  | `GET /share/{id}` | An HTML page: the image, the widget title, the project name and the range in words, and a "Made with twillingate" link. Head: `og:title`, `og:type=website`, `og:url`, `og:image` (absolute `CONSOLE_URL/share/{id}.png`), `og:image:width`, `og:image:height`, `og:image:alt`, `twitter:card=summary_large_image`, `<meta name="robots" content="noindex">`. Inline CSS and the app's icon, no app bundle. It loads `twillingate.js` (D7). |
  | `GET /share/{id}.png` | The image, `Content-Type: image/png`, `Cache-Control: public, max-age=3600`, plus `X-Robots-Tag: noindex`. |

  An unknown or archived id answers 404 on both routes, and so does one
  whose `archive_at` has passed (D9). The page
  answers `Cache-Control: public, max-age=300`. The hour on the image
  bounds how long a CDN keeps serving an archived share. The page carries
  `Content-Security-Policy: default-src 'none'; img-src 'self';
  script-src <collector origin>; connect-src <collector origin>;
  style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'`,
  where the collector origin is `PUBLIC_URL`'s.

  `reporting` serves both routes (`reporting.Shares()`), and `api.RegisterOn`
  mounts them unauthenticated beside `/app/`, `/healthz` and the login
  routes (`internal/api/server.go:106`). Nothing in `internal/server`
  changes. On a shared listener, only the console registers `/share/`.

  **A private console.** When the console sits on a LAN or tailnet, links
  open only there. The fix is a proxy rule, not code: expose only `/share/*`
  of the console listener to the internet and keep `/app`, `/api`, `/mcp`
  and the login private. `docs/deployment.md` shows it as a Caddy
  `handle /share/*` block. A console that is already public (one claude.ai
  reaches over MCP, or a hosted plan) needs nothing.

- **D3. Embedding is an image link, not an iframe.** The Share dialog's
  "Copy embed code" gives:

  ```html
  <a href="CONSOLE_URL/share/<id>"><img src="CONSOLE_URL/share/<id>.png"
     alt="<title>" width="600" height="315"></a>
  ```

  Nothing anywhere needs to allow framing.

- **D4. Capture in the browser, on a fixed social card.** The widget card
  menu (`web/src/components/WidgetCard.tsx`) gains **Share…** and
  **Download PNG**. Both render the widget off-screen into a 1200×630
  card at a pixel ratio of 2 (a 2400×1260 PNG). The card holds:
  - the widget title,
  - the chart, drawn from the data the console already loaded for it,
  - the project name and the range in words (`Sep 5 – Oct 4, 2026`),
  - a small twillingate mark in the footer.

  The card always uses the light theme, whatever theme the console shows.
  The capture uses `html-to-image` (a frontend library, so allowed).
  **Download PNG** saves `<widget-name>-<from>-<to>.png` and stores
  nothing. **Share…** opens a dialog with:
  - a preview of the card, an **Archive after** choice (D9: 1 week,
    **1 month** (the default), 3 months, 1 year, Never) and a **Create
    link** button,
  - after creation: **Copy link** and **Copy embed code**,
  - this widget's existing shares, newest first, each with its range, its
    creation date, its **Archive after** ("archives Nov 4" or "never
    archives"; changeable in place, D9), its opens (D7), **View
    analytics** (D8) and **Archive**.
  - this widget's archived shares, below the live ones and folded by
    default, each with **Restore** and the date the daily pass deletes
    it. When the widget has no other share, live or archived, a note says
    the widget's share analytics go with it on that date.

  The dialog follows the web rules: buttons get the pointer cursor from
  `index.css`, and it does not scroll sideways at 360px.

- **D5. Migration 032: table `shares`, share projects.**

  ```sql
  CREATE TABLE shares (
      id         TEXT PRIMARY KEY,   -- UUIDv7, canonical form
      widget_id  INTEGER NOT NULL REFERENCES widgets(id) ON DELETE CASCADE,
      project_id INTEGER NOT NULL,
      range_from TEXT NOT NULL,          -- YYYY-MM-DD
      range_to   TEXT NOT NULL,          -- YYYY-MM-DD
      title      TEXT NOT NULL,
      project_name TEXT NOT NULL,
      image      BLOB NOT NULL,
      width      INTEGER NOT NULL,
      height     INTEGER NOT NULL,
      created_at TEXT NOT NULL DEFAULT (datetime('now')),
      archive_at  TEXT,                  -- NULL = never (D9)
      archived_at TEXT                   -- NULL = live
  );
  CREATE INDEX idx_shares_widget ON shares(widget_id);
  CREATE INDEX idx_shares_archive ON shares(archive_at) WHERE archive_at IS NOT NULL;

  ALTER TABLE projects ADD COLUMN kind TEXT NOT NULL DEFAULT 'regular';
  ALTER TABLE dashboards ADD COLUMN project_kind TEXT NOT NULL DEFAULT 'regular';  -- D8

  CREATE TABLE widget_share_projects (
      widget_id  INTEGER PRIMARY KEY REFERENCES widgets(id) ON DELETE CASCADE,
      project_id INTEGER NOT NULL UNIQUE REFERENCES projects(id)
  );
  ```

  `shares.project_id` is the project the chart shows. The share project
  that counts its opens (D7) is found through `widget_share_projects`.
  `kind` is `regular` or `share_widget`, with no `CHECK`, since the
  database does no validation: `manage` writes it and nothing else does.
  The name leaves room for later kinds, such as a `share_dashboard`.

  `project_id` has no foreign key: the picture outlives a deleted
  project, as a post does. Archiving a widget, dashboard or project
  leaves its shares up. The daily pass's purge of a long-archived
  widget cascades and takes them down. `title` and the project name are
  copied at capture, and so is `project_name`: the page shows what the
  picture shows, even after a rename.

  **A share project lives exactly as long as its widget has shares,
  live or archived.** The daily pass's purge of a share archived longer
  than `RETENTION_ARCHIVED_DAYS` deletes, when it was the widget's last
  share, its share project in the same transaction: the project row, its
  key, its events and aggregates (through the store's `deleteProject`, the
  one the CLI's project delete and the retention purge of projects use),
  and the `widget_share_projects` row. The audit rows are `share.purge`
  and `project.purge`, both with actor `retention`. The registry is then
  reloaded, so the collector refuses the key at once. While a widget's
  only share is archived, its analytics stay readable, and restoring the
  share picks up where it left off. The purge of a long-archived widget
  takes its shares and their project in the same way, in one transaction
  per widget. `RETENTION_ARCHIVED_DAYS=0` keeps archived shares, and so
  their project, forever.

  Events still in the write buffer for a deleted project (up to one
  flush, 10s) are a case the CLI's project delete already has. The plan
  checks what the flush does with them and makes sure no orphan rows are
  left.

- **D6. Console operations.** In `internal/reporting` (`ops_share.go`),
  exposed by `internal/api/ops_reporting.go`:

  | Operation | MCP tool | REST | Notes |
  | --- | --- | --- | --- |
  | Create | none | `POST /api/widgets/{widget_id}/shares` | `multipart/form-data`: `image` (the PNG), `project_id`, `from`, `to`, `archive_after` (`7d`, `30d`, `90d`, `365d` or `never`; default `30d`). REST only, since an agent has no browser to capture with. Answers 201 with `{id, url, image_url, ...}` |
  | List | `list_shares` | `GET /api/shares?widget_id=` | `widget_id` optional; every share without it. Each row: `id`, `url`, `image_url`, `widget_id`, `project_id`, `from`, `to`, `title`, `created_at`, `archive_at` (`null` = never), `archived_at` (`null` = live), `opens`, `analytics_url`. Archived shares are included; `archived: false` leaves them out |
  | Change its archive date | `update_share` | `PATCH /api/shares/{id}` | body: `archive_after` (as in Create), counted from now. Live shares only |
  | Archive | `archive_share` | `POST /api/shares/{id}/archive` | Takes it down at once (404). Answers the share |
  | Restore | `restore_share` | `POST /api/shares/{id}/restore` | body: `archive_after`, default `30d` from now, since the old date has usually passed. Answers the share |

  There is no delete over the API, as for dashboards and widgets: the
  daily pass deletes a share archived for `RETENTION_ARCHIVED_DAYS` (D5).

  Create validates and refuses with typed errors:
  - The widget is unknown or archived: `ErrNotFound`.
  - Neither `CONSOLE_URL` nor `PUBLIC_URL` is set: `ErrInvalid` with "set
    CONSOLE_URL to share widgets".
  - The upload is not a PNG (magic bytes plus `image/png.DecodeConfig`), is
    over 5 MB, or is not 1200×630 in shape (at pixel ratio 1, 2 or 3):
    `ErrInvalid`.
  - The range is more than 365 days or ends in the future: `ErrInvalid`.
  - `archive_after` is not one of the five values: `ErrInvalid`. The same
    goes for Change and Restore.

  Only the stdlib decodes the PNG; there is no new Go dependency. Create,
  change, archive and restore each write an audit row (`share.create`,
  `share.update`, `share.archive`, `share.restore`), as other console
  writes do. An unknown share is `ErrNotFound`, and changing an archived
  one is `ErrConflict`.

- **D7. Opens are page views sent by `twillingate.js` to the widget's own
  share project.** The share page loads the collector's own SDK,
  cross-origin, as any site does:

  ```html
  <script defer src="PUBLIC_URL/js/twillingate.js" data-key="<key>"></script>
  ```

  A click-through is then an ordinary page view of
  `CONSOLE_URL/share/<id>`, with its referrer (t.co, lnkd.in, a Mastodon
  instance), country and browser, and the visitor counts twillingate
  already computes. Unfurlers run no JavaScript, and the collector's
  `IsBot` check drops the rest, so preview fetches do not count. An
  `<img>` embed runs no script: it counts only when someone clicks
  through. A visitor whose blocker drops the script still sees the share;
  only their open goes uncounted.

  **One project per shared widget, not per link and not one for all.** A
  dashboard takes three parameters, `:project`, `:from` and `:to`
  (`internal/reporting/source.go:127`). A project per widget is what lets
  an ordinary system dashboard (D8) serve as one widget's share analytics,
  with the widget's links side by side under its top links. One project
  for all shares would need a path filter added to every widget instead.

  **Created by the widget's first share,** in the same transaction:
  - a project with `kind = 'share_widget'`, named `Share: <widget title>`
    (`#<widget id>` appended if the name is taken), with
    `allowed_origins = [<CONSOLE_URL's origin>]`,
  - its ingest key, labelled `share`,
  - the `widget_share_projects` row,
  - the audit rows, as the console's project operations write them.

  Later shares of the widget reuse all of it. A store method does the
  whole transaction (`InsertShare`), since `reporting` and `manage` share
  a rank and cannot import each other. `reporting` then reloads the
  registry through an interface `app` satisfies with
  `manage.Registry.Reload`, so the collector accepts the new key at once.

  **The page picks its key** as the share project's first active key, by
  label. If the project is archived or has no active key, the page loads
  no script and the server logs one warning per process.

  **The operator can turn counting off.** `SHARE_ANALYTICS=off` (env, read
  in `internal/config`; default `on`). The page loads no script, and Create
  makes no project. Existing share projects keep their data until their
  widget's last share is purged, as usual.

  **Opens in the dialog** are the share project's page views, per link:
  the views of path `/share/<id>`, from the share's creation to today,
  capped at the last 365 days, through the same views the dashboards
  read. `list_shares` returns the number as `opens`, or `null` when
  counting is off.

- **D8. Share analytics are a system dashboard of their own.** A new
  system dashboard, `internal/reporting/system/shares/` (id 8, a group of
  its own, range `30d`), is the Views dashboard cut down to what a shared
  widget needs:

  | Widget | Shows |
  | --- | --- |
  | Visitors, Opens | Stat tiles: unique visitors and page views |
  | Opens over time | Visitors and views per day |
  | Links | Views per `/share/<id>` path, so each link is a row |
  | Referrers | Where the clicks came from (t.co, lnkd.in, …) |
  | Countries | Visitors by country |
  | Devices, Browsers | Visitors by device and by browser |

  Its widget files reuse the Views dashboard's SQL where the query is the
  same. The plan decides between copying the `.sql` files and letting a
  layout item name another directory's widget.

  **Which projects a dashboard is for.** Its `dashboard.json` declares
  that with `"project_kind": "share_widget"`. When the field is absent,
  the dashboard is for `regular` projects, as all of today's are. A field
  that names a kind, rather than a `widget: true` flag, leaves room for a
  later `share_dashboard`. The kind is stored on the dashboard row
  (`dashboards.project_kind`, migration 032, default `regular`), read
  from the fixture by the release sync, and returned by
  `list_dashboards` and `get_dashboard`. The rules:
  - The sidebar, the dashboards list and the Templates gallery show only
    `regular` dashboards. A `share_widget` dashboard opens only from a
    share's **View analytics**.
  - A dashboard's project switcher lists only projects of its kind. On a
    `share_widget` dashboard, the switcher gives way to a heading,
    `Share: <widget title>`, and the range switcher works as usual.
  - Duplicating a `share_widget` dashboard keeps its kind, so a user can
    make their own share analytics. Users can't change a dashboard's kind.
  - `analytics_url` in `list_shares` is that dashboard with
    `?project=<share project>&range=30d`.

- **D9. A share archives itself after a month unless the user says
  otherwise.** The dialog asks **Archive after** with 1 week, **1 month**
  (default), 3 months, 1 year or Never, and Create stores `archive_at =
  created_at + the period`, or `NULL` for never.
  - **It can be changed.** On a live share, the same choice in the
    dialog's list (`update_share`) sets `archive_at` to now + the period,
    or `NULL`. On an archived share, **Restore** asks for it again
    (`restore_share`).
  - **The page stops at `archive_at`, not at the next daily pass.** Both
    `/share/` routes compare `archive_at` with the clock on each request
    and answer 404 once it has passed, as for an archived share. With
    `max-age=3600` on the image, a CDN may serve it for up to an hour
    longer.
  - **The daily pass archives** each live share whose `archive_at` has
    passed: it sets `archived_at = archive_at`, writing `share.archive`
    with actor `retention`. From then on the share is like one archived
    by hand. It shows under the dialog's archived shares, it can be
    restored, and it is purged `RETENTION_ARCHIVED_DAYS` later (D5).
  - **Feeds keep their copy.** A platform that already unfurled the link
    keeps the card image in its own cache, and the click-through then
    gives a 404. The dialog says so beside the choice.

- **D10. Only regular projects are listed by default, and share projects
  count as usage.**
  - **Listing.** The project switcher, the Projects page,
    `list_projects`, `GET /api/projects` and `twillingate project list` list
    only `regular` projects. `list_projects {kind}`,
    `GET /api/projects?kind=` and `project list -kind` take one kind, or
    `all`. The docs mention the argument once, mark it as not needed for
    everyday use, and describe a share project as managed through its
    widget.
  - **Managed through the widget only.** `update_project`,
    `archive_project`, `restore_project` and the key operations refuse a
    non-`regular` project with `ErrInvalid`.
  - **Counted.** Their events are real rows that the server stores and
    aggregates, so they count toward `usage`, `limits` and `cap_usage`, and
    so toward a hosted plan's monthly events. Those surfaces show them as
    one `Shares` line, the sum over every share project, rather than one
    row per widget. A viral share is then visible without cluttering the
    list.

## Docs

- `docs/reporting.md`: the Share dialog, Download PNG, View analytics,
  Archive after, and the `list_shares`, `update_share`, `archive_share`
  and `restore_share` tools with their REST routes,
  the share analytics dashboard, and `project_kind` in `dashboard.json` and
  in `list_dashboards`/`get_dashboard`.
- `docs/twillingate.md`: the `/share/` routes in the `serve -console` row, as
  the console's one unauthenticated content. Project `kind` (`regular`,
  `share_widget`), the `kind` argument of `list_projects` and
  `project list` (marked not needed for everyday use), and the refusals
  for share projects. The `Shares` line in `usage`, `limits` and `cap_usage`.
- `docs/deployment.md`: `SHARE_ANALYTICS`, the share projects that
  sharing creates, and the Caddy example that exposes only `/share/*` of a private
  console.
- `deploy/UPGRADES.md`: migration 032, which adds two tables and two
  columns defaulting to `regular`, and needs no pre-check.

## Tests

- **Store.** Insert, get, list by widget, update `archive_at`, archive,
  restore. `InsertShare` creates the project, key, link row and audit rows
  once per widget, and rolls all of it back when the share fails.
- **Daily pass.** It archives live shares past `archive_at`, and only
  those. Purging an archived share that isn't the widget's last keeps the
  project. Purging the last one deletes the project, its key, events and
  aggregates and the link row, in one transaction. A widget whose only
  share is archived keeps its project until the purge.
  `RETENTION_ARCHIVED_DAYS=0` purges nothing. Purging an archived widget
  takes its shares and project. No orphan rows
  are left by events buffered for a deleted share project.
- **Reporting.** Each refusal in D6. A second share of a widget reuses
  its project, and two widgets get two projects. The registry is reloaded,
  so an event sent with the new key is accepted. `SHARE_ANALYTICS=off`
  creates nothing. `opens` counts only that link's views. Each
  `archive_after` value gives the right `archive_at` on create, change and
  restore, and an unknown one is refused. Changing an archived share is a
  conflict.
- **Manage and API.** Share projects are absent from `list_projects`,
  `project list` and `GET /api/projects`, and present with
  `kind=share_widget` or `all`. The refusals of D10. `usage`, `limits` and
  `cap_usage` sum them into one `Shares` line.
- **System dashboards.** `TestSystemDashboards` runs the share analytics
  dashboard on a share project. The release sync stores `project_kind`,
  and a duplicate keeps it.
- **Share routes** (`reporting`, mounted through `api`). They answer
  without a token while `/api/` still answers 401. The page's meta tags, `noindex`, the CSP, and the script tag
  present or absent (counting on, off, or no active key). The PNG's type
  and cache headers. Both routes answer 404 for an unknown id, for an
  archived one, and once `archive_at` has passed, before the daily pass
  runs, and answer again after a restore.
- **API.** The multipart create route, and the list, change, archive and
  restore routes. `docs_sync`
  picks up the new tools, routes and env var.
- **Web.** A vitest for the Share dialog (create, copy, list, change
  Archive after, archive, restore, View analytics, the choice defaulting
  to 1 month) and one for the
  capture card layout. The sidebar and switchers leave share projects and
  `share_widget` dashboards out, and the share analytics dashboard shows
  the share's heading in place of the switcher. A Playwright e2e that shares a seeded
  widget, opens `/share/<id>`, checks the meta tags and that the image
  loads, archives the share and gets a 404, then restores it and gets
  the page back. The cursor and phone specs
  cover the dialog.

## Out of scope

- Public or unlisted dashboards, and live public widgets.
- Iframe embeds.
- Server-side rendering, and refreshing the image automatically.
- "Share to X" intent buttons: copying the link is enough, and the
  platforms unfurl it themselves.
- Hiding the project name on the card.

## Ships as

`feat(reporting): share a widget as an image link`.
