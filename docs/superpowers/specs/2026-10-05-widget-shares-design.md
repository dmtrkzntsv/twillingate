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
  | `GET /share/{id}` | An HTML page (D4): the image, the widget title, the project name and the range in words, and the footer link "Built with twillingate.dev" (below). Head: `og:title`, `og:type=website`, `og:url`, `og:image` (absolute `CONSOLE_URL/share/{id}.png`), `og:image:width` 1200, `og:image:height` 630, `og:image:alt`, `twitter:card=summary_large_image`, `<meta name="robots" content="noindex">`. Inline CSS and the app's icon, no app bundle and no script. |
  | `GET /share/{id}.png` | The 1200×630 rendition (D4), the one `og:image` names. `Content-Type: image/png`, `Cache-Control: public, max-age=3600`, `X-Robots-Tag: noindex`. |
  | `GET /share/{id}@2x.png` | The 2400×1260 rendition, for the page's `srcset`, the embed and Download PNG. Same headers. |

  An unknown or archived id answers 404 on all three routes, and so does one
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
     srcset="CONSOLE_URL/share/<id>@2x.png 2x"
     alt="<title>" width="600" height="315"></a>
  ```

  Nothing anywhere needs to allow framing.

- **D4. The images are designed, not screenshots.** A share's images are
  what strangers see in a feed, so they get the same care as the console.
  The widget card menu (`web/src/components/WidgetCard.tsx`) gains
  **Share…** and **Download PNG**. Both lay the widget out afresh on a
  social card. They never screenshot the dashboard tile.

  **The card.** 1200×630 CSS pixels, captured twice: at pixel ratio 1 for
  `og:image` (`/share/{id}.png`) and at 2 for the page, the embed and
  Download PNG (`/share/{id}@2x.png`). The layout:
  - 56px padding all round, which also keeps everything inside the
    1200×600 band that X crops a large card to.
  - The widget title at the top, 44px semibold, at most two lines, then
    truncated with an ellipsis.
  - Below it, the project name and the range in words (`Sep 5 – Oct 4,
    2026`), 22px, muted.
  - The chart fills the rest, about 1088×420. It is drawn at that size
    rather than scaled from a tile, so lines and text stay sharp. Its type
    is scaled for a card seen small in a feed: axis labels and legends at
    about 1.4 times the dashboard's size, with fewer ticks.
  - **The watermark:** the twillingate iceberg logo (`IcebergLogo`, 20px)
    and `twillingate.dev` in 16px type, bottom right inside the padding,
    at about 55% opacity. It never sits over the chart. It is the only
    branding on the image.
  - **The sharer's theme.** The card is drawn in the theme the console
    shows when Share… or Download PNG is clicked, light or dark, with the
    console's own background, text and chart colours for that theme. The
    watermark takes the theme's muted foreground. The dialog's preview is
    the captured image itself, so what you see is what is posted. A social
    preview is one image for every reader, so it cannot follow each
    reader's theme; to post the other theme, switch the console's theme
    and share again.

  **Every machine draws the same card** for a given theme. The console uses the system font
  stack, which differs from one OS to the next. The card instead uses one
  bundled font (Inter, through `@fontsource`, a frontend library and so
  allowed), loaded only by the card. The capture waits for
  `document.fonts.ready` and inlines the font. It uses `html-to-image`.

  **Per component.** Each component gets a card mode with no interactive
  chrome: no tooltips, menus, filters, pagination or scrollbars. A table
  shows the rows that fit, with "and N more" under the last one. A stat
  shows its number large and centred. Markdown is set in the card's type.
  A component whose content cannot fit (a sankey with hundreds of nodes)
  draws what fits, as its dashboard tile does.

  **Where "great" gets judged.** The components gallery
  (`/app/gallery/components`) gains a **Share card** view that renders
  every component's fixtures as cards, at their real size. That is the
  review surface for the look, and it follows the gallery's theme, so both
  themes get reviewed. The PR that ships this feature includes every
  card in both themes for review. A Playwright test captures each
  component's card and checks the two renditions' dimensions and that the
  1x PNG stays under 300 KB, the size above which WhatsApp drops the
  preview.

  **The share page** is designed too: the 2x image centred at up to 1200px
  wide, the title as `<h1>`, the project and range under it, and the
  footer credit. The page around the image follows the visitor's
  `prefers-color-scheme`. The image keeps the theme it was shared in.
  The page does not scroll sideways at 360px.

  **Download PNG** saves the 2x rendition as
  `<widget-name>-<from>-<to>.png` and stores nothing. **Share…** opens a
  small dialog:
  - a preview of the card, an **Archive after** choice (D7: 1 week,
    **1 month** (the default), 3 months, 1 year, Project lifetime) and a **Create
    link** button,
  - after creation: **Copy link**, **Copy embed code** and **Open**,
  - a line, "This widget has N other links", leading to the Shares page
    filtered to the widget (D8), when there are any.

  The dialog follows the web rules: buttons get the pointer cursor from
  `index.css`, and it does not scroll sideways at 360px.

- **D5. Migration 032: table `widget_shares`.**

  ```sql
  CREATE TABLE widget_shares (
      id         TEXT PRIMARY KEY,   -- UUIDv7, canonical form
      widget_id  INTEGER REFERENCES widgets(id) ON DELETE SET NULL,  -- NULL once the widget is gone
      project_id INTEGER NOT NULL REFERENCES projects(id),
      range_from TEXT NOT NULL,          -- YYYY-MM-DD
      range_to   TEXT NOT NULL,          -- YYYY-MM-DD
      title      TEXT NOT NULL,
      project_name TEXT NOT NULL,
      image      BLOB NOT NULL,          -- 1200×630, og:image
      image_2x   BLOB NOT NULL,          -- 2400×1260
      created_at TEXT NOT NULL DEFAULT (datetime('now')),
      archive_at  TEXT,                  -- NULL = project lifetime (D7)
      archived_at TEXT                   -- NULL = live
  );
  CREATE INDEX idx_widget_shares_widget ON widget_shares(widget_id);
  CREATE INDEX idx_widget_shares_archive ON widget_shares(archive_at) WHERE archive_at IS NOT NULL;
  ```

  The name leaves room for a later `dashboard_shares`. The API keeps the
  shorter `shares` (`list_shares`, `/api/shares`) since a share's kind
  shows in its fields.

  `project_id` is the project the chart shows. **A share lives as long
  as its project, and no longer:**
  - **Project deleted.** `widget_shares` joins `projectTables`
    (`internal/store/sqlite/registry.go:228`), so `deleteProject` removes a
    project's shares, live and archived, in the transaction that deletes
    the project. That covers both ways a project goes: the CLI's
    `project delete`, and the daily pass's purge of a project archived
    longer than `RETENTION_ARCHIVED_DAYS`. The foreign key backs this up.
  - **Widget gone, share stays.** A share is a picture with its own copy
    of everything it shows, so it needs nothing from its widget once
    created. Archiving, purging or deleting the widget, or its dashboard,
    leaves the share up. The purge sets `widget_id` to `NULL`
    (`ON DELETE SET NULL`), and the Shares page then shows the share
    without its widget link (D8).
  - **Archiving the project** leaves its shares up too, since archiving is
    reversible. They go when the purge deletes the project.

  `title` and `project_name` are copied at capture, so the page shows
  what the picture shows, even after a rename.

  The daily pass purges a share archived longer than
  `RETENTION_ARCHIVED_DAYS` (audit `share.purge`, actor `retention`), as
  it purges dashboards and widgets. `RETENTION_ARCHIVED_DAYS=0` keeps
  archived shares forever.

- **D6. Console operations.** In `internal/reporting` (`ops_share.go`),
  exposed by `internal/api/ops_reporting.go`:

  | Operation | MCP tool | REST | Notes |
  | --- | --- | --- | --- |
  | Create | none | `POST /api/widgets/{widget_id}/shares` | `multipart/form-data`: `image` (1200×630 PNG), `image_2x` (2400×1260 PNG), `project_id`, `from`, `to`, `archive_after` (`7d`, `30d`, `90d`, `365d` or `project`; default `30d`). REST only, since an agent has no browser to capture with. Answers 201 with `{id, url, image_url, ...}` |
  | List | `list_shares` | `GET /api/shares?widget_id=&archived=` | `widget_id` optional; every share without it. Each row: `id`, `url`, `image_url`, `image_2x_url`, `widget_id`, `dashboard_id`, `dashboard_title`, `project_id`, `project_name`, `from`, `to`, `title`, `created_at`, `archive_at` (`null` = project lifetime), `archived_at` (`null` = live). `widget_id`, `dashboard_id` and `dashboard_title` are `null` once the widget is gone. `archived: false` lists live shares only (the Shares page), `true` archived ones only (the Archive page); omitted, both |
  | Change its archive date | `update_share` | `PATCH /api/shares/{id}` | body: `archive_after` (as in Create), counted from now. Live shares only |
  | Archive | `archive_share` | `POST /api/shares/{id}/archive` | Takes it down at once (404). Answers the share |
  | Restore | `restore_share` | `POST /api/shares/{id}/restore` | body: `archive_after`, default `30d` from now, since the old date has usually passed. Answers the share |

  There is no delete over the API, as for dashboards and widgets: the
  daily pass deletes a share archived for `RETENTION_ARCHIVED_DAYS` (D5).

  Create validates and refuses with typed errors:
  - The widget is unknown or archived: `ErrNotFound`.
  - Neither `CONSOLE_URL` nor `PUBLIC_URL` is set: `ErrInvalid` with "set
    CONSOLE_URL to share widgets".
  - Either upload is not a PNG (magic bytes plus `image/png.DecodeConfig`),
    is not exactly its size (1200×630 and 2400×1260), or is over 5 MB:
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
  (default), 3 months, 1 year or **Project lifetime**, and Create stores
  `archive_at = created_at + the period`, or `NULL` for project lifetime.
  There is no "forever": a share with no date lives exactly as long as
  its project does (D5), and goes with it. The API calls that
  choice `project`.
  - **It can be changed.** On a live share, the same choice on the Shares
    page (D8, `update_share`) sets `archive_at` to now + the period,
    or `NULL`. On an archived share, **Restore** on the Archive page
    (D9) asks for it again (`restore_share`).
  - **The page stops at `archive_at`, not at the next daily pass.** Both
    `/share/` routes compare `archive_at` with the clock on each request
    and answer 404 once it has passed, as for an archived share. With
    `max-age=3600` on the image, a CDN may serve it for up to an hour
    longer.
  - **The daily pass archives** each live share whose `archive_at` has
    passed: it sets `archived_at = archive_at`, writing `share.archive`
    with actor `retention`. From then on the share is like one archived
    by hand. It shows on the Archive page (D9), it can be restored, and it
    is purged `RETENTION_ARCHIVED_DAYS` later (D5).
  - **Feeds keep their copy.** A platform that already unfurled the link
    keeps the card image in its own cache, and the click-through then
    gives a 404. The dialog says so beside the choice.

- **D8. A Shares page in the console.** `/app/shares`, in the sidebar
  under Projects (left out in read-only mode, as Projects is). It lists
  every live share, newest first:

  | Column | Shows |
  | --- | --- |
  | Image | The 1x image as a thumbnail, linking to the share page |
  | Widget | The share's title, and under it the dashboard and project it came from, linking to the dashboard. Once the widget is gone, the copied title and project name, without a link |
  | Range | The range in words |
  | Created | The date |
  | Archive after | "Nov 4" or "Project lifetime", changeable in place (D7, `update_share`) |
  | Actions | **Copy link**, **Copy embed code**, **Archive** |

  `?widget=<id>` narrows it to one widget, with a chip to clear the
  filter. That is where the Share dialog's "N other links" leads. Empty,
  the page says how to share: "Share… in a widget's menu". Below `sm` the
  table folds: the thumbnail, the widget and the actions stay, and range,
  created and Archive after move into a line under the widget. Archiving
  asks no confirmation, since Restore undoes it.

- **D9. Archived shares on the Archive page.** The Archive page gains a
  **Shares** section under the dashboards. It lists archived shares,
  most recently archived first, each with its thumbnail, title, project,
  and "archived · deleted on <date>", using the `purge_after_days` and
  `purgeDate` the page already uses for dashboards. Each has **Restore**,
  which asks **Archive after** (default 1 month) before restoring
  (`restore_share`). The page's intro gains "Archived shares answer 404
  until restored." With nothing archived, the section is left out.

## Docs

- `docs/reporting.md`: the Share dialog, Download PNG, the card and its
  watermark, the Shares page, archived shares on the Archive page,
  Archive after, and
  the `list_shares`, `update_share`, `archive_share` and `restore_share`
  tools with their REST routes.
- `docs/twillingate.md`: the three `/share/` routes in the `serve -console` row,
  as the console's one unauthenticated content, with the footer credit.
- `docs/deployment.md`: the Caddy example that exposes only `/share/*` of
  a private console, and that `RETENTION_ARCHIVED_DAYS` covers shares.
- `deploy/UPGRADES.md`: migration 032, which only adds a table and needs
  no pre-check.

## Tests

- **Store.** Insert, get, list by widget, update `archive_at`, archive,
  restore. Deleting a widget keeps its shares and sets their `widget_id`
  to `NULL`, and they still serve. `deleteProject` removes
  the project's shares, live and archived, through both the CLI's
  `project delete` and the purge of an archived project, and leaves
  other projects' shares alone.
- **Daily pass.** It archives live shares past `archive_at`, and only
  those. It purges shares archived longer than `RETENTION_ARCHIVED_DAYS`,
  and none when it is 0. Purging an archived widget or dashboard leaves
  its shares up.
- **Reporting.** Each refusal in D6. Each `archive_after` value gives the
  right `archive_at` on create, change and restore (`project` gives
  `NULL`), and an unknown one is
  refused. Changing an archived share is a conflict.
- **Share routes** (`reporting`, mounted through `api`). They answer
  without a token while `/api/` still answers 401. The page's meta tags,
  `noindex`, the CSP, no `<script>`, and the footer link to
  `https://twillingate.dev`, the `og:image` size tags and the `srcset`.
  The PNGs' types and cache headers. All three routes answer 404 for an unknown id, for an archived one, and once
  `archive_at` has passed (before the daily pass runs), and answer again
  after a restore.
- **API.** The multipart create route, and the list, change, archive and
  restore routes. `docs_sync` picks up the new tools and routes.
- **Web.** Vitests for:
  - the Share dialog: create, copy, open, the choice defaulting to 1
    month, and the "N other links" line;
  - the Shares page: list, the widget filter, changing Archive after,
    archive, a share whose widget is gone, the empty state, the folded columns below `sm`;
  - the Archive page's Shares section: the purge date, and Restore asking
    Archive after;
  - the card layout of each component, in both themes, and the capture
    taking the console's current theme.

  The gallery's Share card view renders every component. A Playwright
  test captures each card in both themes and checks both renditions'
  sizes and the 300 KB budget of the 1x one. A Playwright e2e that shares a seeded
  widget, finds it on the Shares page, opens `/share/<id>`, checks the
  meta tags, the footer link and that both images load, archives it and
  gets a 404, finds it on the Archive page, restores it and gets the page
  back. The cursor and phone specs cover the dialog, the Shares page, the
  Archive page's section and the share page.

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
