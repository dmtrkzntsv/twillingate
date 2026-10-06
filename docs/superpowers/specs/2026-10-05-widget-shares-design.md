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
  picture. A share is immutable: a new picture means a new link. Deleting
  a share is the only way to take it down.

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
  are a reporting feature: the console creates, lists and deletes them,
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

  An unknown or deleted id answers 404 on both routes. The page
  answers `Cache-Control: public, max-age=300`. The hour on the image
  bounds how long a CDN keeps serving a deleted share. The page carries
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
  - a preview of the card and a **Create link** button,
  - after creation: **Copy link** and **Copy embed code**,
  - this widget's existing shares, newest first, each with its range, its
    creation date, its opens (D7) and **Delete** (with confirmation).

  The dialog follows the web rules: buttons get the pointer cursor from
  `index.css`, and it does not scroll sideways at 360px.

- **D5. Table `shares`, migration 032.**

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
      created_at TEXT NOT NULL DEFAULT (datetime('now'))
  );
  CREATE INDEX idx_shares_widget ON shares(widget_id);
  ```

  `project_id` has no foreign key: the picture outlives a deleted
  project, as a post does. Archiving a widget, dashboard or project
  leaves its shares up. The daily pass's purge of a long-archived
  widget cascades and takes them down. `title` and the project name are
  copied at capture, and so is `project_name`: the page shows what the
  picture shows, even after a rename.

- **D6. Console operations.** In `internal/reporting` (`ops_share.go`),
  exposed by `internal/api/ops_reporting.go`:

  | Operation | MCP tool | REST | Notes |
  | --- | --- | --- | --- |
  | Create | none | `POST /api/widgets/{widget_id}/shares` | `multipart/form-data`: `image` (the PNG), `project_id`, `from`, `to`. REST only, since an agent has no browser to capture with. Answers 201 with `{id, url, image_url, ...}` |
  | List | `list_shares` | `GET /api/shares?widget_id=` | `widget_id` optional; every share without it. Each row: `id`, `url`, `image_url`, `widget_id`, `project_id`, `from`, `to`, `title`, `created_at`, `opens` |
  | Delete | `delete_share` | `DELETE /api/shares/{id}` | Hard delete. Answers 204 |

  Create validates and refuses with typed errors:
  - The widget is unknown or archived: `ErrNotFound`.
  - Neither `CONSOLE_URL` nor `PUBLIC_URL` is set: `ErrInvalid` with "set
    CONSOLE_URL to share widgets".
  - The upload is not a PNG (magic bytes plus `image/png.DecodeConfig`), is
    over 5 MB, or is not 1200×630 in shape (at pixel ratio 1, 2 or 3):
    `ErrInvalid`.
  - The range is more than 365 days or ends in the future: `ErrInvalid`.

  Only the stdlib decodes the PNG; there is no new Go dependency. Create
  and delete each write an audit row, as other console writes do.

- **D7. Opens are page views sent by `twillingate.js` to a share project.**
  The share page loads the collector's own SDK, cross-origin, as any site
  does:

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
  only their open goes uncounted. The views dashboards, filtered to the share project, serve as
  the share analytics, and need no new reporting code.

  **Which project.** The share project's id is the `meta` row
  `share_project_id`.
  - **Created automatically by the first share.** If the row is absent,
    Create (D6) makes a project named `Shares` with
    `allowed_origins = [<CONSOLE_URL's origin>]`, issues it an ingest key
    labelled `shares`, and writes the row. All of this runs in the same
    transaction as the first share.
  - **The operator can change it.** `SHARE_PROJECT_ID` (env, read in
    `internal/config`) names an existing project and, when set, is written
    to the row at start-up, as the caps are (`internal/app/app.go:97-108`).
    `SHARE_PROJECT_ID=0` turns counting off: the page loads no script and
    nothing is created. Unset leaves the row as it is.
  - The operator owns that project's `allowed_origins`. If they point
    `SHARE_PROJECT_ID` at a project, they must add `CONSOLE_URL`'s origin
    to it, and `docs/deployment.md` says so.

  **The page picks its key** as the share project's first active ingest
  key, by label. If the project is archived or has no active key, the page
  loads no script and the server logs one warning per process.

  **Opens in the dialog** are the share project's page views whose path
  is `/share/<id>`. They are counted from the share's creation to today,
  capped at the last 365 days, through the same views the dashboards
  read. `list_shares` returns the number as `opens`, or `null` when
  counting is off.

## Docs

- `docs/reporting.md`: the Share dialog, Download PNG, and the
  `list_shares` and `delete_share` tools with their REST routes.
- `docs/twillingate.md`: the `/share/` routes in the `serve -console` row, as
  the console's one unauthenticated content.
- `docs/deployment.md`: `SHARE_PROJECT_ID`, the auto-created `Shares`
  project, and the Caddy example that exposes only `/share/*` of a private
  console.
- `deploy/UPGRADES.md`: migration 032, which only adds a table and needs
  no pre-check.

## Tests

- **Store.** Insert, get, list by widget, delete. The cascade from a hard
  widget delete. Reading `share_project_id` from `meta`.
- **Reporting.** Each refusal in D6. The first share creates the `Shares`
  project, its key and the meta row in one transaction, and a second
  share reuses them. `SHARE_PROJECT_ID=0` creates nothing. `opens` counts
  only `/share/<id>` views of the share project.
- **Share routes** (`reporting`, mounted through `api`). They answer
  without a token while `/api/` still answers 401. The page's meta tags, `noindex`, the CSP, and the script tag
  present or absent (counting on, off, or no active key). The PNG's type
  and cache headers. Both routes answer 404 for an unknown id and
  after delete.
- **API.** The multipart create, list and delete routes. `docs_sync`
  picks up the new tools, routes and env var.
- **Web.** A vitest for the Share dialog (create, copy, list, delete) and
  one for the capture card layout. A Playwright e2e that shares a seeded
  widget, opens `/share/<id>`, checks the meta tags and that the image
  loads, deletes the share and gets a 404. The cursor and phone specs
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
