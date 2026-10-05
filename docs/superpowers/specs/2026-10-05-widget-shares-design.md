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

- **D1. A share is a frozen image under an unlisted token.** The browser
  captures one widget as a PNG and uploads it. The server stores the PNG
  under 128 random bits and never queries the widget again. Neither the
  project nor the range can change after capture, since both are part of
  the picture. A share is immutable: a new picture means a new link.
  Deleting a share is the only way to take it down.

- **D2. Shares live on the ingest host, at `PUBLIC_URL/s/<token>`.** The
  ingest host is the one that is always public. Unlike the console, it
  runs no reporting code for shares: it reads one row by primary key.

  | Route | Answers |
  | --- | --- |
  | `GET /s/{token}` | An HTML page: the image, the widget title, the project name and the range in words, and a "Made with twillingate" link. Head: `og:title`, `og:type=website`, `og:url`, `og:image` (absolute `PUBLIC_URL/s/{token}.png`), `og:image:width`, `og:image:height`, `og:image:alt`, `twitter:card=summary_large_image`, `<meta name="robots" content="noindex">`. It loads `twillingate.js` (D6). |
  | `GET /s/{token}.png` | The image, `Content-Type: image/png`, `Cache-Control: public, max-age=3600`, plus `X-Robots-Tag: noindex`. |

  An unknown or deleted token answers 404 on both routes. The page
  answers `Cache-Control: public, max-age=300`. The hour on the image
  bounds how long a CDN keeps serving a deleted share. The page carries
  `Content-Security-Policy: default-src 'none'; img-src 'self';
  script-src 'self'; connect-src 'self'; style-src 'unsafe-inline';
  base-uri 'none'; frame-ancestors 'none'`.

  The routes are registered on the ingest mux in `internal/server`, which
  reads through its own `server.ShareStore` interface (`Share(ctx, token)`
  for the row, and `ShareKey(ctx)` for the key D7 picks), and `app` passes
  the store. `server` does
  not import `reporting` or `api`.

- **D3. Embedding is an image link, not an iframe.** The Share dialog's
  "Copy embed code" gives:

  ```html
  <a href="PUBLIC_URL/s/<token>"><img src="PUBLIC_URL/s/<token>.png"
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
      token      TEXT PRIMARY KEY,
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
  | Create | none | `POST /api/widgets/{widget_id}/shares` | `multipart/form-data`: `image` (the PNG), `project_id`, `from`, `to`. REST only, since an agent has no browser to capture with. Answers 201 with `{token, url, image_url, ...}` |
  | List | `list_shares` | `GET /api/shares?widget_id=` | `widget_id` optional; every share without it. Each row: `token`, `url`, `image_url`, `widget_id`, `project_id`, `from`, `to`, `title`, `created_at`, `opens` |
  | Delete | `delete_share` | `DELETE /api/shares/{token}` | Hard delete. Answers 204 |

  Create validates and refuses with typed errors:
  - The widget is unknown or archived: `ErrNotFound`.
  - `PUBLIC_URL` is unset: `ErrInvalid` with "set PUBLIC_URL to share widgets".
  - The upload is not a PNG (magic bytes plus `image/png.DecodeConfig`), is
    over 5 MB, or is not 1200×630 in shape (at pixel ratio 1, 2 or 3):
    `ErrInvalid`.
  - The range is more than 365 days or ends in the future: `ErrInvalid`.

  Only the stdlib decodes the PNG; there is no new Go dependency. Create
  and delete each write an audit row, as other console writes do.

- **D7. Opens are page views sent by `twillingate.js` to a share project.**
  The share page loads the collector's own SDK:

  ```html
  <script defer src="/js/twillingate.js" data-key="<key>"></script>
  ```

  A click-through is then an ordinary page view of
  `PUBLIC_URL/s/<token>`, with its referrer (t.co, lnkd.in, a Mastodon
  instance), country and browser, and the visitor counts twillingate
  already computes. Unfurlers run no JavaScript, and the collector's
  `IsBot` check drops the rest, so preview fetches do not count. An
  `<img>` embed runs no script: it counts only when someone clicks
  through. The views dashboards, filtered to the share project, serve as
  the share analytics, and need no new reporting code.

  **Which project.** The share project's id is the `meta` row
  `share_project_id`.
  - **Created automatically by the first share.** If the row is absent,
    Create (D6) makes a project named `Shares` with
    `allowed_origins = [<PUBLIC_URL's origin>]`, issues it an ingest key
    labelled `shares`, and writes the row. All of this runs in the same
    transaction as the first share.
  - **The operator can change it.** `SHARE_PROJECT_ID` (env, read in
    `internal/config`) names an existing project and, when set, is written
    to the row at start-up, as the caps are (`internal/app/app.go:97-108`).
    `SHARE_PROJECT_ID=0` turns counting off: the page loads no script and
    nothing is created. Unset leaves the row as it is.
  - The operator owns that project's `allowed_origins`. If they point
    `SHARE_PROJECT_ID` at a project, they must add `PUBLIC_URL`'s origin to
    it, and `docs/deployment.md` says so.

  **The page picks its key** as the share project's first active ingest
  key, by label. If the project is archived or has no active key, the page
  loads no script and the server logs one warning per process.

  **Opens in the dialog** are the share project's page views whose path
  is `/s/<token>`. They are counted from the share's creation to today,
  capped at the last 365 days, through the same views the dashboards
  read. `list_shares` returns the number as `opens`, or `null` when
  counting is off.

## Docs

- `docs/reporting.md`: the Share dialog, Download PNG, and the
  `list_shares` and `delete_share` tools with their REST routes.
- `docs/twillingate.md`: the `/s/` routes on the ingest host, in the
  `serve -ingest` row and the route list.
- `docs/deployment.md`: `SHARE_PROJECT_ID`, the auto-created `Shares`
  project, and a reverse proxy in front of the ingest host forwarding
  `/s/`.
- `deploy/UPGRADES.md`: migration 032, which only adds a table and needs
  no pre-check.

## Tests

- **Store.** Insert, get, list by widget, delete. The cascade from a hard
  widget delete. Reading `share_project_id` from `meta`.
- **Reporting.** Each refusal in D6. The first share creates the `Shares`
  project, its key and the meta row in one transaction, and a second
  share reuses them. `SHARE_PROJECT_ID=0` creates nothing. `opens` counts
  only `/s/<token>` views of the share project.
- **Server.** The page's meta tags, `noindex`, the CSP, and the script tag
  present or absent (counting on, off, or no active key). The PNG's type
  and cache headers. Both routes answer 404 for an unknown token and
  after delete.
- **API.** The multipart create, list and delete routes. `docs_sync`
  picks up the new tools, routes and env var.
- **Web.** A vitest for the Share dialog (create, copy, list, delete) and
  one for the capture card layout. A Playwright e2e that shares a seeded
  widget, opens `/s/<token>`, checks the meta tags and that the image
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
