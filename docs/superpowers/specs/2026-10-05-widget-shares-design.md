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
  after** date (D7), and the daily pass deletes it once it has been
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
  keep running as its own process. A collector hostname is the kind that
  privacy-minded readers' blockers list, and a share on it would fail for
  exactly them. And the console's hostname is the product's face, which
  reads better in a post than a tracker domain.
  Links are built from `CONSOLE_URL`, which defaults to `PUBLIC_URL`, so
  there is no new setting.

  | Route | Answers |
  | --- | --- |
  | `GET /share/{id}` | An HTML page: the image, the widget title, the project name and the range in words, and the footer link "Built with twillingate.dev" (below). Head: `og:title`, `og:type=website`, `og:url`, `og:image` (absolute `CONSOLE_URL/share/{id}.png`), `og:image:width`, `og:image:height`, `og:image:alt`, `twitter:card=summary_large_image`, `<meta name="robots" content="noindex">`. Inline CSS and the app's icon, no app bundle and no script. |
  | `GET /share/{id}.png` | The image, `Content-Type: image/png`, `Cache-Control: public, max-age=3600`, plus `X-Robots-Tag: noindex`. |

  An unknown or archived id answers 404 on both routes, and so does one
  whose `archive_at` has passed (D7). The page
  answers `Cache-Control: public, max-age=300`. The hour on the image
  bounds how long a CDN keeps serving an archived share. The page carries
  `Content-Security-Policy: default-src 'none'; img-src 'self';
  style-src 'unsafe-inline'; base-uri 'none'; form-action 'none';
  frame-ancestors 'none'`. The page runs no script at all.

  **"Built with twillingate.dev".** Every share page ends with a footer
  line, `Built with <a href="https://twillingate.dev">twillingate.dev</a>`,
  the same on every install, hosted or self-hosted, with no setting to
  change or hide it. The link is plain: no UTM parameters, since the
  referrer already names the share's host, and no `nofollow`, since
  `noindex` keeps the page itself out of search and the link is an honest
  credit. It carries `rel="noopener"`. The card image does not carry it:
  its small twillingate mark (D4) stays a mark, not a URL.

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
  - a preview of the card, an **Archive after** choice (D7: 1 week,
    **1 month** (the default), 3 months, 1 year, Never) and a **Create
    link** button,
  - after creation: **Copy link** and **Copy embed code**,
  - this widget's existing shares, newest first, each with its range, its
    creation date, its **Archive after** ("archives Nov 4" or "never
    archives"; changeable in place, D7) and **Archive**.
  - this widget's archived shares, below the live ones and folded by
    default, each with **Restore** and the date the daily pass deletes
    it.

  The dialog follows the web rules: buttons get the pointer cursor from
  `index.css`, and it does not scroll sideways at 360px.

- **D5. Migration 032: table `shares`.**

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
      archive_at  TEXT,                  -- NULL = never (D7)
      archived_at TEXT                   -- NULL = live
  );
  CREATE INDEX idx_shares_widget ON shares(widget_id);
  CREATE INDEX idx_shares_archive ON shares(archive_at) WHERE archive_at IS NOT NULL;
  ```

  `project_id` is the project the chart shows. It has no foreign key: the
  picture outlives a deleted project, as a post does. Archiving a widget,
  dashboard or project leaves its shares up. The daily pass's purge of a
  long-archived widget cascades and takes them down. `title` and
  `project_name` are copied at capture, so the page shows what the
  picture shows, even after a rename.

  The daily pass purges a share archived longer than
  `RETENTION_ARCHIVED_DAYS` (audit `share.purge`, actor `retention`), as
  it purges dashboards and widgets. `RETENTION_ARCHIVED_DAYS=0` keeps
  archived shares forever.

- **D6. Console operations.** In `internal/reporting` (`ops_share.go`),
  exposed by `internal/api/ops_reporting.go`:

  | Operation | MCP tool | REST | Notes |
  | --- | --- | --- | --- |
  | Create | none | `POST /api/widgets/{widget_id}/shares` | `multipart/form-data`: `image` (the PNG), `project_id`, `from`, `to`, `archive_after` (`7d`, `30d`, `90d`, `365d` or `never`; default `30d`). REST only, since an agent has no browser to capture with. Answers 201 with `{id, url, image_url, ...}` |
  | List | `list_shares` | `GET /api/shares?widget_id=` | `widget_id` optional; every share without it. Each row: `id`, `url`, `image_url`, `widget_id`, `project_id`, `from`, `to`, `title`, `created_at`, `archive_at` (`null` = never), `archived_at` (`null` = live). Archived shares are included; `archived: false` leaves them out |
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

- **D7. A share archives itself after a month unless the user says
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

## Docs

- `docs/reporting.md`: the Share dialog, Download PNG, Archive after, and
  the `list_shares`, `update_share`, `archive_share` and `restore_share`
  tools with their REST routes.
- `docs/twillingate.md`: the `/share/` routes in the `serve -console` row,
  as the console's one unauthenticated content, with the footer credit.
- `docs/deployment.md`: the Caddy example that exposes only `/share/*` of
  a private console, and that `RETENTION_ARCHIVED_DAYS` covers shares.
- `deploy/UPGRADES.md`: migration 032, which only adds a table and needs
  no pre-check.

## Tests

- **Store.** Insert, get, list by widget, update `archive_at`, archive,
  restore. The cascade from a hard widget delete.
- **Daily pass.** It archives live shares past `archive_at`, and only
  those. It purges shares archived longer than `RETENTION_ARCHIVED_DAYS`,
  and none when it is 0. Purging an archived widget takes its shares.
- **Reporting.** Each refusal in D6. Each `archive_after` value gives the
  right `archive_at` on create, change and restore, and an unknown one is
  refused. Changing an archived share is a conflict.
- **Share routes** (`reporting`, mounted through `api`). They answer
  without a token while `/api/` still answers 401. The page's meta tags,
  `noindex`, the CSP, no `<script>`, and the footer link to
  `https://twillingate.dev`. The PNG's type and cache headers. Both routes
  answer 404 for an unknown id, for an archived one, and once
  `archive_at` has passed (before the daily pass runs), and answer again
  after a restore.
- **API.** The multipart create route, and the list, change, archive and
  restore routes. `docs_sync` picks up the new tools and routes.
- **Web.** A vitest for the Share dialog (create, copy, list, change
  Archive after, archive, restore, the choice defaulting to 1 month) and
  one for the capture card layout. A Playwright e2e that shares a seeded
  widget, opens `/share/<id>`, checks the meta tags, the footer link and
  that the image loads, archives the share and gets a 404, then restores
  it and gets the page back. The cursor and phone specs cover the dialog
  and the share page.

## Out of scope

- Public or unlisted dashboards, and live public widgets.
- Iframe embeds.
- Server-side rendering, and refreshing the image automatically.
- "Share to X" intent buttons: copying the link is enough, and the
  platforms unfurl it themselves.
- Hiding the project name on the card.
- Counting opens and share analytics: #140 keeps the design worked out
  for them (a hidden `share_widget` project per widget, its own
  dashboard, a usage line).

## Ships as

`feat(reporting): share a widget as an image link`.
