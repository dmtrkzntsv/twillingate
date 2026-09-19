# One views family: merge web and app analytics — design

Date: 2026-09-17
Status: implemented
Supersedes the family split introduced by `2026-08-23-app-analytics-design.md`.

## 1. Purpose

Twillingate stores three event families: web (`$page_view` → `web_hits`),
app (`$screen_view` → `app_views`) and product (everything else). Web and
app are the same thing built twice: a view stream with an actor, a
session, a location and environment dimensions, feeding the same overview,
breakdown and retention questions. The differences are which columns are
filled and where the environment comes from. Keeping them apart costs two
aggregators, thirteen aggregate tables, fifteen stitch views, two Evidence
pages, four MCP tools, four REST routes, two retention windows and two
sections of the contract page — and the split is decided by the event
name, which is the wrong axis (an Electron app is a web page *and* an app).

After this change there are two families:

- **views** — `$page_view` and `$screen_view`, one table, one aggregator,
  one set of views, one Evidence page, two tools. The client declares a
  free-form `kind` (`web`, `app`, `cli`, …); only `web` has server-side
  meaning.
- **product** — unchanged, except for one column that retention needs.

This is the model PostHog and Amplitude use: one event stream, environment
stamped on every event by the client, geo added by the server, and "web
analytics" as a lens over it rather than a separate store.

## 2. Non-goals

- Client-generated session ids in the browser SDK. The merged sessionizer
  treats a client `$session_id` as authoritative, so the server is ready
  if the SDK later sends one; sending one is a storage/ePrivacy decision
  taken separately.
- Scroll depth, time on page, page-leave events.
- `utm_term`, `utm_content`, ad click ids.
- Person merging on `identify` (joining an anonymous history to a user).
- OS version parsed from the User-Agent (frozen by modern browsers; the
  parsed value would be a constant).
- Viewport size. The physical display is recorded; the rendered area is
  not.
- Changing product-event aggregation or the declared-attributes mechanism.

## 3. Decisions

| Decision | Choice |
|---|---|
| Family name | `views` — tables `views`, `agg_views_*`; stitch views `v_views_*`; tools `views_overview`, `views_breakdown`; env `RETENTION_VIEWS_*` |
| Kind | Free-form string per row, from `$kind`; defaults from the event name (`$page_view`→`web`, `$screen_view`→`app`); validated `^[a-z][a-z0-9_]{0,15}$` |
| Wire names | `$page_view` (renamed from `$pageview`, which stays a silent alias because every deployed tag sends it) and `$screen_view`; neither selects a table any more |
| OS | `$os` replaces `$platform` as the wire key (`$platform` stays a silent alias). It is the OS, stored in `os`, normalised to the parser's vocabulary. Product events follow: column `platform`→`os`, rollup key `$platform`→`$os` |
| App version | `$app_version` is unchanged. "App" means the client application that sent the event, whatever its kind; a CLI's version lands here too. Column `app_version` on views and product events; dimension `app_versions` |
| Sessions | The app rule: client `$session_id` authoritative, else a >30 min gap per actor. Bounce = session with one view |
| Dimension cap | 500 values per day per dimension, trailing key collapses into `(other)` — the app rule applied everywhere, including paths (new for web) |
| Retention population | `actor_kind` ∈ {`user`,`install`,`connection`} recorded at ingest on views and product events; only `user` and `install` actors are cohorted |
| Raw window | One: `RETENTION_VIEWS_RAW_DAYS` (default 30); the event-age clamp derives from it |
| New dimensions | `display_width` + `display_height` (raw integers, reported as one `WxH` resolution), `browser_version` (parsed major), `locale` now sent by the browser SDK |
| Migration | 012 folds both old families into the new tables in one transaction and drops the old ones; irreversible |
| Delivery | One branch, one `feat!` commit; migration, tools, docs and dashboards land together |

## 4. Wire format

`POST /ingest/events` is unchanged in shape. Two additions, two renames
and one reinterpretation. Names are consistent: event names and
attribute keys are all `snake_case` (`$page_view`, `$screen_view`,
`$app_version`), and a key is named for the column it lands in.

- **`$page_view`** replaces `$pageview` as the web view event name.
  `$pageview` is accepted as a silent alias and not documented. This
  alias is the one that matters: every deployed tag and any cached copy
  of the SDK send it, and the SDK is served by the collector so it
  upgrades with the server but not before.
- **`$os`** replaces `$platform` for the declared operating system.
  `$platform` is accepted as a silent alias and not documented.
- **`$app_version`** keeps its name. It is the version of the client
  application that sent the event — a site build, an app release, a CLI
  version — and is not tied to `kind: app`; the docs say so in one
  sentence.
- **`$kind`** (new reserved key). Free-form, usually a batch attribute.
  Absent → defaults from the event name. Invalid (fails the pattern) →
  warning `invalid $kind %q, using %q` and the default is used, so a typo
  cannot create a dimension.
- **`$display_width`**, **`$display_height`** (new reserved keys). Integer
  pixels of the physical display. Non-integer or ≤ 0 → warning, stored
  as 0 (absent). Named `display` rather than `screen` because `$screen`
  is the screen *name*.
- **`$screen`** is now an alias for `$path` on any view: a `$screen_view`
  without `$path` takes its location from `$screen`. A view with neither
  is rejected `view requires $path or $screen`, replacing the two
  per-name messages.

Reserved event names table becomes:

| name | family | default kind | location key |
|---|---|---|---|
| `$page_view` | views | `web` | `$path` |
| `$screen_view` | views | `app` | `$screen` (or `$path`) |
| anything else | product | — | — |

Reserved attribute keys after the change (groups as documented):

| Group | Keys |
|---|---|
| Identity | `$install_id` `$user_id` `$user_name` `$group_id` `$group_name` `$session_id` |
| Environment | `$kind` `$os` `$os_version` `$app_version` `$device_model` `$locale` `$display_width` `$display_height` |
| Location | `$host` `$path` `$screen` `$utm_source` `$utm_medium` `$utm_campaign` `$referrer` |

The `App` group in the reserved-keys table is gone; `$screen` moves to
Location. `docs_sync_test.go` reads that table and requires every key in
it to appear in `ingest.go` and vice versa; the two aliases are excluded
from that check by an explicit `aliasKeys` list in the test, the way
`removedKeys` already excludes `$url`.

## 5. Ingest rules

`handleEvents` keeps one loop; the `switch` on name becomes: product if
the name is not one of the two view names, else view. The view path:

1. Resolve `kind`: `$kind` if valid, else the default for the name.
2. Resolve location: `$path`, else `$screen`; reject if both empty.
3. Resolve actor as today (`resolveIdentity`), and additionally record
   `actor_kind`: `user` if `$user_id` was present, `install` if
   `$install_id` was present (and no user id), else `connection`.
   `resolveIdentity` returns the kind alongside the ids; product events
   record it too.
4. If `kind == "web"`:
   - if `enrich.IsBot(ua)` → accepted and ignored (unchanged behaviour,
     now keyed on kind rather than name);
   - `device, browser, browserVersion, os := enrich.ParseUserAgent(ua)`
     (the parser gains the major browser version);
   - `referrer_source = enrich.CleanReferrer($referrer, $host)`.
   Otherwise no parsing, no filtering, and `$referrer` is stored through
   `CleanReferrer` with an empty host (face value), so an app deep link
   can still carry a referrer.
5. Declared keys override: `$os` (normalised) → `os`; `$os_version`,
   `$device_model`, `$locale`, `$app_version` stored as sent;
   `$display_width`, `$display_height` parsed. A declared `$os` on a web
   row replaces
   the parsed OS; a parsed browser on a non-web row never happens because
   step 4 skipped the parse.
6. Enqueue `store.View`.

OS normalisation (`enrich.NormalizeOS`): `ios`→`iOS`,
`android`→`Android`, `macos`→`macOS`, `windows`→`Windows`,
`linux`→`Linux`, `chromeos`→`ChromeOS`; case-insensitive on input; any
other value is stored as sent. Product events store the same normalised
value in their `os` column (renamed from `platform`); the product
rollup's system dimension keys become `$os` and `$app_version`.

Bot filtering keyed on kind means a `web` row from a non-browser client is
filtered exactly as today, and an `app` or `cli` row is never filtered
regardless of its User-Agent — the same guarantee the old split gave,
stated explicitly.

## 6. Data model

### 6.1 `views` (raw; replaces `web_hits` and `app_views`)

```sql
CREATE TABLE views (
    id              TEXT PRIMARY KEY,
    project         TEXT NOT NULL,
    ts              TEXT NOT NULL,
    received_at     TEXT NOT NULL,
    kind            TEXT NOT NULL,
    actor_id        TEXT NOT NULL,
    actor_kind      TEXT NOT NULL,              -- user | install | connection
    user_id         TEXT NOT NULL DEFAULT '',
    group_id        TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    host            TEXT NOT NULL DEFAULT '',
    path            TEXT NOT NULL,
    referrer_source TEXT NOT NULL DEFAULT '',
    utm_source      TEXT NOT NULL DEFAULT '',
    utm_medium      TEXT NOT NULL DEFAULT '',
    utm_campaign    TEXT NOT NULL DEFAULT '',
    os              TEXT NOT NULL DEFAULT '',
    os_version      TEXT NOT NULL DEFAULT '',
    browser         TEXT NOT NULL DEFAULT '',
    browser_version TEXT NOT NULL DEFAULT '',
    app_version     TEXT NOT NULL DEFAULT '',
    device          TEXT NOT NULL DEFAULT '',   -- desktop | mobile | tablet | ''
    device_model    TEXT NOT NULL DEFAULT '',
    locale          TEXT NOT NULL DEFAULT '',
    display_width   INTEGER NOT NULL DEFAULT 0, -- 0 = absent
    display_height  INTEGER NOT NULL DEFAULT 0,
    country         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_views_project_ts ON views(project, ts);
CREATE INDEX idx_views_actor      ON views(project, actor_id, ts);
CREATE INDEX idx_views_session    ON views(project, session_id, ts);
```

`store.WebHit` and `store.AppView` become one `store.View` with the same
fields. The pipeline carries two item kinds (`view`, `event`); its flush
labels are `views` and `product_events`.

### 6.2 `product_events`

Gains `actor_kind TEXT NOT NULL DEFAULT ''`. Existing rows stay `''`,
which the actor upsert treats like `connection` (skipped). The `platform`
column is renamed `os`, and `agg_product_attrs` rows with
`attr_key='$platform'` are rewritten to `'$os'` (§11). The rollup itself
is unchanged.

### 6.3 Aggregates

All `WITHOUT ROWID`, all metrics are counts (`visitors` =
`COUNT(DISTINCT actor_id)`, `views` = `COUNT(*)`):

```sql
agg_views_daily     (project, day, kind, visitors, views, sessions, bounces, duration_sec)
                    PRIMARY KEY (project, day, kind)
agg_views_paths     (project, day, path, visitors, views)
agg_views_hosts     (project, day, host, visitors, views)
agg_views_referrers (project, day, source, visitors, views)
agg_views_utm       (project, day, utm_source, utm_medium, utm_campaign, visitors, views)
agg_views_countries (project, day, country, visitors, views)
agg_views_os        (project, day, os, os_version, visitors, views)
agg_views_browsers  (project, day, browser, browser_version, visitors, views)
agg_views_app_versions (project, day, os, app_version, visitors, views)
agg_views_devices   (project, day, device, device_model, visitors, views)
agg_views_displays  (project, day, display, visitors, views)
```

Row filters: `utm` keeps rows where any of the three is non-empty (as
today); `app_versions` keeps rows with `app_version <> ''`; `displays`
keeps rows with both `display_width > 0` and `display_height > 0`;
`hosts` keeps the empty host as a real bucket (migration-008 history
plus every non-web row). Everything else groups every row, empty string
included.

Kinds have no dimension table: a kind breakdown reads `agg_views_daily`.

The display key is one expression shared by the aggregator and the live
half (`sqlite.displaySQL`): `display_width || 'x' || display_height`,
giving `1920x1080`. Real resolutions are a naturally small set, and the
500 cap bounds the rest.

### 6.4 Retention and identities

```sql
actors        (project, actor_id, actor_kind, first_seen_day, last_seen_day)
              PRIMARY KEY (project, actor_id)
agg_retention (project, actor_kind, cohort_day, day_offset, actors)
              PRIMARY KEY (project, actor_kind, cohort_day, day_offset)
agg_identity_daily (project, day, kind, id, actors, users, views, events)
```

`surface` is gone from both retention tables. `agg_identity_daily`
drops `hits`; `views` now counts view rows of every kind. (`kind` in this
table is the identity kind, `user`/`group`, unchanged.)

## 7. Aggregation

`AggregateViewDay(ctx, project, day)` replaces `AggregateWebDay` and
`AggregateAppDay`. One transaction:

1. Early exit if no raw rows for the day (idempotent no-op).
2. `agg_views_daily`: the app session CTE, grouped by `kind`. Sessions:
   `session_id <> ''` is authoritative, else `LAG(t) > 1800` splits per
   actor. `bounces = SUM(session has one view)`. `duration_sec =
   COALESCE(SUM(MAX(t)-MIN(t)), 0)`.
3. Every dimension in §6.3 via the generalised `aggAppDimension`: rank
   values by `COUNT(*)` desc then key asc, `rn <= 500` keeps the value,
   the rest maps the *trailing* key to `(other)` with the leading key
   intact, `visitors` recomputed as `COUNT(DISTINCT actor_id)` over the
   grouped raw rows (never summed). Kinds in `agg_views_daily` use the
   same cap so a hostile client cannot grow the daily table.
4. `DELETE FROM views` for the day.

Day selection uses `dayRange` (half-open timestamps) everywhere; the
`substr(ts,1,10)` form used by the app aggregator goes away.

## 8. Stitch views

Every aggregate table gets a `v_views_*` view: aggregate rows `UNION ALL`
the same shape computed live from `views` for days not yet rolled up.
The live halves apply the same rank/cap/`(other)` logic and the same
display expression as §7 — the app views never did, which meant an app
breakdown could jump at the aggregation boundary; the merged views close
that. `v_identity_daily` has two arms (views, product events) instead of
three. `v_retention` exposes `actor_kind` instead of `surface`.

`views_test.go` gains `TestStitchViewsInvariantViewsDaily` (per kind) and
`TestStitchViewsInvariantAllViewsDimensions` covering all ten
dimension views, including hosts, which the old web list omitted.

## 9. Retention

`UpsertActors` reads `views` and `product_events`, inserts actors whose
`actor_kind` is `user` or `install`, and skips `connection` and `''`:
a connection hash rotates with the salt, so it can never appear in a
later cohort and recording it only ever produced an offset-0 row.
`AggregateRetentionDay` groups by `actor_kind`. Identified projects only,
as today (the daily pass skips anonymous ones).

The `retention` tool parameter `surface` (`web`|`app`) becomes `actor`
(`user`|`install`), REST query `actor`.

## 10. Configuration

| Variable | Default | Replaces |
|---|---|---|
| `RETENTION_VIEWS_RAW_DAYS` | 30 | `RETENTION_WEB_RAW_DAYS` (7), `RETENTION_APP_RAW_DAYS` (30) |
| `RETENTION_VIEWS_AGGREGATE_DAYS` | 365 | `RETENTION_WEB_AGGREGATE_DAYS`, `RETENTION_APP_AGGREGATE_DAYS` |

The four old names join `config.renamed` and refuse the boot with the
replacement named, the existing convention. `MaxEventAge()` derives from
`Retention.Views.RawDays`. `config.Retention{Views, Product}`;
`RetentionOverride{Views, Product}` with legacy JSON keys `web` and `app`
decoded into `Views` (the larger of the two wins if both are set) so a
stored per-project override keeps working until the project is next
updated, at which point it is written back under `views`.

`PruneAggregates(ctx, project, viewsBefore, productBefore)`. Actors,
`agg_retention` and identities prune on `Retention.Views.AggregateDays`.

The web raw window growing from 7 to 30 days is the one storage cost of
the merge. The deployment page's low-resource section tells SD-card
operators to set `RETENTION_VIEWS_RAW_DAYS=7` and accept that events
older than the window are clamped.

## 11. Migration 012

`012_views.sql`, applied by the existing runner in one transaction.
Irreversible; the deployment page's upgrade section tells operators to
take a Litestream snapshot first.

It folds from the post-011 shapes, not from schema 008: `009_product_surface`
added the `product` surface, `010_identity_daily_disjoint` made
`v_identity_daily`'s live half skip aggregated days, and
`011_retention_users` added `actors.is_user` and the nullable
`agg_retention.users`. Steps 3, 4 and 6 below account for all three.

1. Create `views` and its indexes (§6.1). Copy:
   - `web_hits` → `kind='web'`, `actor_kind = CASE WHEN user_id<>'' THEN
     'user' ELSE 'connection' END`, `browser_version=''`, `os_version=''`,
     `device_model=''`, `locale=''`, `display_width=0`, `display_height=0`,
     `app_version=''`.
   - `app_views` → `kind='app'`, `actor_kind = CASE WHEN user_id<>'' THEN
     'user' ELSE 'install' END`, `path=screen`, `os=` platform normalised
     via a `CASE` mirroring `enrich.NormalizeOS`, `device=''`, `browser=''`,
     `host=''`.
2. Create the aggregate tables (§6.3) and fold:
   - `agg_views_daily` ← `agg_web_daily` as `web`
     (`visitors, pageviews→views, sessions, bounces, duration_sec`) and
     `agg_app_daily` as `app` (`actives→visitors, views, sessions, 0,
     duration_sec`).
   - `paths` ← `agg_web_pages` ∪ `agg_app_screens`, summed on collision.
   - `hosts` ← `agg_web_hosts`. `referrers` ← `agg_web_referrers`.
     `utm` ← `agg_web_utm`. `browsers` ← `agg_web_browsers` with `''`
     version.
   - `countries` ← both, summed.
   - `os` ← `agg_web_os` (`os, ''`) ∪ `agg_app_os` (`platform→os,
     os_version`), summed.
   - `app_versions` ← `agg_app_versions` (`platform→os, app_version`).
   - `devices` ← `agg_web_devices` (`device, ''`) ∪ `agg_app_devices`
     (`'', device_model`).
   - `displays` starts empty.
   Sums are exact for `views` and exact for `visitors` where the two
   families were disjoint populations (they were: different actor ids).
3. `actors`: rebuild with `actor_kind = CASE WHEN is_user = 1 THEN 'user'
   ELSE 'install' END`, for every surface alike — 011's `is_user` is exactly
   the axis `actor_kind` names, and the surface an actor arrived on says
   nothing about how it was identified. `agg_retention` folds across
   surfaces, grouping by `(project, cohort_day, day_offset)`: a `user` row
   holding `SUM(users)` where `SUM(COALESCE(users,0)) > 0`, and an `install`
   row holding `SUM(actors - COALESCE(users,0))` where that is > 0. A
   cohort's split is only meaningful when its own `(project, surface,
   cohort_day)` offset-0 row knows `users`; if that offset-0 row is NULL,
   every offset of that cohort is treated as NULL and folds wholly under
   `install`, even an offset the daily pass later recomputed with a known
   count.
4. `agg_identity_daily`: rebuild with `views = hits + views`. 010 already
   deleted the partial rows the old pass wrote for today, and the pass no
   longer writes them, so nothing is repeated here.
5. `ALTER TABLE product_events ADD COLUMN actor_kind TEXT NOT NULL
   DEFAULT ''`; `ALTER TABLE product_events RENAME COLUMN platform TO os`
   (values normalised in place with the same `CASE`);
   `UPDATE agg_product_attrs SET attr_key='$os' WHERE attr_key='$platform'`
   with the values normalised the same way, summing on collision (`ios`
   and `iOS` rows for one event and day become one).
6. Drop `web_hits`, `app_views`, every `agg_web_*`, every `agg_app_*`.
   Drop every `v_*` and recreate all of them (§8), product views
   included, matching the 003/004 precedent. The recreated
   `v_identity_daily` keeps 010's `NOT EXISTS` guard on its live half: the
   aggregate is written while the day's raw rows still exist, so the two
   halves are not disjoint.

`registry.go` `projectTables` lists the new tables;
`TestProjectTablesMatchesSchema` and `TestPruneAggregatesCoversAllAggTables`
enforce it. A dedicated `TestMigration012Folds` seeds a database at
schema 011 with known rows in every old table, runs the migration and
asserts every folded total. The `make check` restore test exercises it on
a real backup.

## 12. API

Tools (nineteen → seventeen; `docs/twillingate.md` says "seventeen
tools" and `docs_sync_test.go` checks the word):

| Tool | Parameters | Returns |
|---|---|---|
| `views_overview` | `project`, `from`, `to`, `kind` (optional) | Per day: `visitors, views, sessions, bounces, duration_sec, bounce_rate, avg_session_sec`. With `kind` absent rows are summed across kinds per day |
| `views_breakdown` | `project`, `from`, `to`, `dimension`, `limit` (20) | `dimension` ∈ `kinds, paths, hosts, referrers, utm, countries, os, browsers, app_versions, devices, displays`. Two-key dimensions return both key columns; `kinds` reads `v_views_daily` |
| `retention` | …, `actor` (`user`|`install`) | unchanged shape |
| `identities` | unchanged | `views`, `events` (no `hits`) |
| `list_projects` | — | `first_view_day`, `last_view_day` replace the four web/app dates |

The dimension enum in the tool's JSON schema is generated from the
dimension map at registration, fixing the shared `breakdownIn` struct
advertising the web enum for `app_breakdown` today.

REST: `GET /api/projects/{project}/views/overview` and
`…/views/breakdown`; the `web/*` and `app/*` routes are removed (404).
`schema://views` lists the eleven `v_views_*` views, `v_identity_daily`,
`v_retention (actor_kind: 'user'|'install')`, `v_product_*`,
`identities`. `guide.go`'s mobile branch keeps its payload; the web/spa
branch mentions `data-kind` for Electron/Tauri.

## 13. Dashboards (Evidence)

- `pages/views/[project].md` replaces `pages/web/[project].md` and
  `pages/app/[project].md`: BigValues (visitors, views, bounce rate, avg
  session), a kind split area chart, paths / hosts / referrers /
  campaigns, an audience block (countries, OS, browsers, devices,
  displays), and version adoption (empty for a pure website; the
  component hides when the source is empty).
- `pages/views/[project]/page.md` is the per-path drill-down, moved.
- `pages/retention/[project].md` takes `actor` instead of `surface`.
- Users and groups pages show `views` and `events`.
- `index.md` links `views` instead of `web` and `app`.
- Sources: eleven `v_views_*.sql` passthroughs with the empty-database
  sentinel; the fifteen `v_web_*`/`v_app_*` files are deleted.
  `prerender_test.go` walks `pages/`, so the route list updates itself.

## 14. SDK (`sdk/src/twillingate.ts`)

| Addition | Init option | Tag attribute | Default |
|---|---|---|---|
| kind | `kind` | `data-kind` | `web` |
| OS | `os` (renamed from `platform`, which stays as a deprecated alias) | `data-os` | none |
| app version | `appVersion` (exists) | `data-app-version` | none |

- `$kind` is a batch attribute, always sent.
- With `kind !== "web"` the auto-tracker emits `$screen_view` with
  `$screen` = the masked route path (same routing/masking rules as
  `page()`), and no `$host`, `$referrer` or `$utm_*`. With `web` it
  emits `$page_view` exactly as today.
- Every `$page_view` / `$screen_view` carries `$display_width` /
  `$display_height` from `window.screen.width` / `.height`.
- Every batch carries `$locale = navigator.language` when available.
- `page()`, `screen()`, `track()` and the rest of the public API are
  unchanged; `docs_sync_test.go`'s symbol list gains `data-kind`,
  `data-os`, `data-app-version`, `$kind`, `$os`, `$display_width`,
  `$display_height` and drops `$pageview` for `$page_view`.

## 15. Documentation (same commit)

`docs/twillingate.md`:
- The event model: one `### Views` section replaces `### Web` and
  `### App`, describing kind, the defaults, what `web` enriches, and that
  every other kind is taken as declared. Suggested kinds: `web`, `app`,
  `cli`.
- Reserved event names and reserved attribute keys tables per §4.
- Tools table, "seventeen tools", HTTP API route table, the queryable
  views paragraph (`v_views_*`), the SDK attribute table and defaults.
  The `product_attributes` row says `$os` and `$app_version` are always
  available; the attribute-breakdowns section says the same.
- Every `$pageview` and `$platform` in the page becomes `$page_view`
  and `$os`; the aliases are not documented. `docs/plausible/README.md` mentions the two view names
  in prose and follows; the shim itself only forwards custom events
  through the SDK's `track`, so its bytes do not change.
- A dated "Changed in this release" note: tool names, routes, view names,
  env vars, the retention parameter, capped paths, the app-days-have-zero-
  bounces boundary, and the empty device-class/model boundary.

`docs/deployment.md`:
- Env table: the two `RETENTION_VIEWS_*` rows replace four; a line that
  the old names refuse the boot.
- Low-resource section: lower the raw window.
- Upgrade section: snapshot before upgrading across this release.
- Dashboards section: page list.

`internal/api/resources.go` `schemaViews` per §12.

## 16. Testing

- Store: `aggregate_views_test.go` (sessions with and without client
  ids, bounces, every dimension's cap and `(other)` recompute, kinds
  cap); stitch invariants per §8; `TestMigration012Folds`; prune and
  registry coverage tests pick up the new tables.
- Ingest: kind default and validation; screen→path; reject without
  location; bot filter on `web` only; no parse on non-web; declared
  override of parsed OS; OS normalisation; both aliases accepted
  silently; display parsing;
  `actor_kind` for each identity shape; product events carry
  `actor_kind`.
- Enrich: browser major version for each browser the parser knows;
  `NormalizeOS`.
- Jobs: daily pass with the two families; retention skips connection
  actors; prune cutoffs.
- Config: new variables, old names refused, `MaxEventAge`, legacy override
  keys.
- API: both tools against seeded data; `kind` filter and summed default;
  every dimension; `retention` `actor`; REST parity; docs sync (tools,
  routes, env vars, reserved keys, SDK symbols, views dimensions —
  `TestDocumentCoversEveryWebDimension` becomes `…EveryViewsDimension`).
- SDK: `data-kind="app"` emits `$screen_view` on navigation with the
  masked path and nothing else; `data-os`/`data-app-version`
  reach the batch; `$display_width`, `$display_height` and `$locale`
  present.
- Dashboards: prerender test over the new page set.

## 17. Upgrade consequences (release notes)

Breaking:

- MCP tools `web_overview`, `web_breakdown`, `app_overview`,
  `app_breakdown` are replaced by `views_overview`, `views_breakdown`.
- REST routes `/web/*` and `/app/*` are replaced by `/views/*`.
- `retention` takes `actor` (`user`|`install`) instead of `surface`.
- Views `v_web_*` and `v_app_*` are replaced by `v_views_*`; saved SQL
  against them must be rewritten. `v_identity_daily` and `v_retention`
  change columns.
- `RETENTION_WEB_*` and `RETENTION_APP_*` refuse the boot; set
  `RETENTION_VIEWS_*`.
- `product_attributes` with `key: "$platform"` becomes `key: "$os"`;
  `v_product_attrs` rows follow.
- Migration 012 is irreversible; snapshot first.

Not breaking: the ingest wire format (`$pageview` and `$platform` are
still accepted as aliases of `$page_view` and `$os`), `$app_version`,
deployed SDK tags, ingest keys, project aliases, per-project retention
overrides (legacy keys still decode).

Behaviour changes worth a line: paths are capped at 500 per day; web
raw rows are kept 30 days by default; app breakdowns no longer jump at
the aggregation boundary.
