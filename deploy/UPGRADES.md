# Schema upgrades

One section per migration (or other change) that needs more than `docker
compose pull` or a re-run of the installer: what to check before the
upgrade, and what changes on the day. Newest last. Snapshot the database
first in every case.

### Upgrading to integer project ids (migration 014)

Projects are keyed by an integer id from this migration on; the alias is
gone, and so are `config import`/`export` and per-project retention.
Before upgrading, run these against the live database — each hit is
something the migration refuses or the first daily pass will prune:

```sql
-- 1. Per-project retention overrides. Anything returned is data the first
--    daily pass after the upgrade will prune to the global window. Raise
--    the matching RETENTION_* variable first if that data must be kept.
SELECT alias, retention FROM projects WHERE retention IS NOT NULL;

-- 2. Rows whose project has no registry row. Any hit aborts migration 014.
--    Repeat for every table that has a project column.
SELECT DISTINCT project FROM views
WHERE project NOT IN (SELECT alias FROM projects);

-- 3. Duplicate key labels. Any hit aborts migration 014.
SELECT project, label, COUNT(*) FROM ingest_keys
GROUP BY project, label HAVING COUNT(*) > 1;
```

Then stop the service, copy the database, run the installer, and check
`journalctl` for migration 014. `list_projects` (or `twillingate project list`) shows the new ids: they
follow creation order, starting at 1. Agents and scripts that stored
aliases need those ids.

Two things change on the day: retention is global from now on
(`RETENTION_*`), and anonymous actor hashes are computed from the id
instead of the alias, so an anonymous visitor seen before and after the
upgrade counts twice in that day's uniques and a session spanning it
splits. The salt rotates at midnight anyway, so the seam is one day.

### Upgrading to the declared environment (migration 015)

From this migration the client declares its operating system, browser
and device class and the server only validates them, against closed
lower-case vocabularies; the User-Agent is read for nothing but the
crawler drop. `platform` becomes its own column, aggregate and breakdown
dimension, and `v_views_app_versions` is keyed by it instead of `os`.
Before upgrading, run these against the live database:

```sql
-- 1. Two spellings of one OS on one aggregate key. Any hit aborts
--    migration 015 (lower-casing them would collide) and leaves the
--    database at 014. Merge or delete the duplicate by hand first.
--    Scoped to the vocabulary (the same list as check 2): a case variant
--    of an OS outside it does not abort, it just stays as it is.
SELECT project_id, day, lower(os), os_version, COUNT(*) FROM agg_views_os
WHERE lower(os) IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown')
GROUP BY 1, 2, 3, 4 HAVING COUNT(*) > 1;
SELECT project_id, day, event_name, lower(attr_value), COUNT(*) FROM agg_product_attrs
WHERE attr_key = '$os' AND lower(attr_value) IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown')
GROUP BY 1, 2, 3, 4 HAVING COUNT(*) > 1;

-- 1b. Two spellings of one OS under one app version. These do NOT abort:
--    the app-versions rekey sums their visitors into one row, which can
--    overcount. Merge or delete the duplicate first if that matters.
SELECT project_id, day, lower(os), app_version, COUNT(*) FROM agg_views_app_versions
GROUP BY 1, 2, 3, 4 HAVING COUNT(*) > 1;

-- 1c. An empty os beside a literal 'unknown' on one key. The migration
--    relabels '' to 'unknown', so these collide and abort like case
--    variants do.
SELECT project_id, day, os_version FROM agg_views_os WHERE os = ''
INTERSECT
SELECT project_id, day, os_version FROM agg_views_os WHERE os = 'unknown';
--    agg_product_attrs needs no ''-vs-unknown check: the $os rollup has
--    never written an empty value.

-- 2. OS values outside the vocabulary. Raw rows fold to 'other' and keep
--    the original in os_name; aggregate history keeps these spellings as
--    they are, so decide now whether any deserves a client-side fix.
SELECT os, COUNT(*) FROM views WHERE lower(os) NOT IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown')
GROUP BY os;
```

Then stop the service, copy the database, run the installer, and check
`journalctl` for migration 015.

What changes on the day:

- **Every stored `os`, `browser` and `device` value is lower-case**
  (`ios`, `chrome`, `samsung_internet`), rows that were empty are
  `unknown`, and `v_views_app_versions` has a `platform` column where
  `os` was. Saved SQL and dashboards comparing against `'iOS'` or
  `'Chrome'`, or selecting `os` from the app-versions view, return
  nothing until rewritten.
- **Clients that are not the served JS SDK** — a backend relay, a native
  app, a custom SDK — record `unknown` for OS, browser and device until
  they declare `$os`, `$browser` and `$device` (and `$platform`). Snippet
  sites pick up detection with the upgrade because the collector serves
  the SDK; bundled or npm consumers when they update.
- **Deployed app clients that sent `$platform` as an alias for `$os`**
  lose their OS dimension until they ship an update that sends both; their
  platform dimension starts working at once.
- iPad traffic moves from `ios` to `ipados`, Brave from `chrome` to
  `brave`, and consoles and TVs from `desktop` to `other`, so those
  series step on the upgrade day.
- **Rows that were empty are `unknown` now**, so app and CLI traffic
  appears as an `unknown` bar in the Browsers and Operating-systems
  dashboard charts, and `product_attributes` for `$os` gains an `unknown`
  bucket over re-aggregated history — correct by design, and visible on
  upgrade day.
- `agg_views_app_versions` history is rekeyed by `platform = lower(os)`,
  which is value-preserving; going forward, versions from clients that
  do not yet send `$platform` roll up under `unknown` until they update.

### Upgrading to group counts (migration 016)

`agg_product_attrs` gains a nullable `unique_groups` — the distinct groups
behind each attribute value — and `v_product_attrs`, the
`product_attributes` tool and the product dashboard's two breakdown tables
carry it. There is nothing to check first: the migration is one `ALTER` and
one view, no value is rewritten, and it runs in well under a second.

What changes on the day:

- **Days rolled up before the upgrade stay unmeasured.** Aggregation
  deletes a day's raw rows in the same transaction that writes its rollup,
  so there is nothing left to count groups from. Those rows hold NULL, not
  0, and stay that way: `product_attributes` returns an empty cell and the
  dashboard's "Groups (at least)" column is blank for any range that has no
  measured day. `0` only ever means measured, none.
- Days still within `RETENTION_PRODUCT_RAW_DAYS` at upgrade time are
  measured by the live half at once and keep the figure when they roll up.
- Saved SQL that reads `v_product_attrs` by position gets the new column
  last; by name, nothing changes. A `SUM()` or `MAX()` over it skips the
  NULLs; do not `COALESCE` them to 0.

There is no down migration. An older binary still runs against the upgraded
file — it writes the seven columns it knows and leaves `unique_groups`
NULL — but a day it rolls up is then unmeasured for good.

### Upgrading to storing what is sent (migration 017)

The project's identity mode is gone. The collector stores `$user_id`,
`$user_name` and `$install_id` exactly as a client sends them, for every
project; what is sent is decided by the tag (`data-identity`, anonymous by
default) or by whatever a hand-written client posts. The migration cleans
up what the old anonymous mode hid before dropping the column with
`ALTER TABLE … DROP COLUMN`; see below for exactly what it rewrites.

**Upgrade at least a day after the release that carries the SDK factory
(#48) has been on this collector.** The served SDK is cached for a day; a
page still running the previous SDK sends ids from `identify()` even under
an anonymous tag, and from upgrade day those would be stored raw.

There is no query to run. The check is a question: for every project that
was `anonymous`, confirm no client posts `$user_id` or `$install_id` by
hand, because from upgrade day they are stored as sent. Before the upgrade
`twillingate project list` still shows the mode column, which is how to
find those projects.

What changes on the day:

- The mode column is gone; `list_projects`, `schema://projects` and
  `twillingate project list` stop showing it, and a `create_project` or
  `update_project` call that still sends `identity` is refused as an
  unknown field.
- Retention appears for any project whose clients send `$user_id` or
  `$install_id`, and is empty for the rest.
- Ids hashed before the upgrade stay hashed and never link to ids received
  after it. Raw rows an `anonymous` project received with a `$user_id` or
  `$install_id` are re-kinded to `connection` by the migration, so the
  daily pass does not turn those rotating hashes into cohorts; those same
  rows have their hashed `user_id` cleared, and their per-day `user` rows
  in `agg_identity_daily` are deleted, so the users page and the
  `identities` tool never list an old rotating hash as a person. The
  migration leaves group rows alone, but the next daily pass recomputes
  every day still inside the raw window, and with the user ids cleared
  each group's `users` count for those days becomes 0 — for a project that
  was always `anonymous` that replaces a count of rotating hashes; for a
  project switched from `identified` to `anonymous` before the upgrade it
  replaces real user ids, which the migration has also cleared from its
  raw rows for good. Days already rolled up keep their counts. Nothing
  else is rewritten and there is nothing to backfill.
- The collector logs `project receives ids` (with the project id and the
  kind, `user` or `install`) once per project and kind per process, the
  first time a stored view or event carries one. Watch for it after the
  upgrade on a project that should send none.

There is no down migration. The previous binary reads a column that no
longer exists and refuses to start against the upgraded file.

### Upgrading to the consent flag (migration 018)

Views and product events gain a `consent` column: `1` when the client said
it had consent to keep anything on the device, `0` when it said it had
not, NULL when it said nothing. A `consent` views breakdown
(`v_views_consent`, `views_breakdown` with `dimension: "consent"`) reads it
as `given`, `none` and `unknown`.

Nothing to check first on a single-binary install; the migration adds
columns and a table and rewrites no rows. On a two-server setup, upgrade the
writer first: a dashboards image newer than its replica cannot read
`v_views_consent` until the replica carries migration 018, so it keeps
serving the previous build, or answers 503 on a fresh start, until then.

What changes on the day:

- Every existing view and event reads `unknown`: there is no record to
  backfill from, and `none` would claim a refusal nobody recorded. Rolled-up
  days get one `unknown` row carrying the day's totals.
- The `unknown` share shrinks as pages pick up the new SDK, which sends
  `$consent` on every batch (the served SDK is cached for a day).
- A hand-built client sends `$consent` itself or stays `unknown`.

### Upgrading to two locales (migration 019)

`$locale` becomes `$browser_locale`, and `$app_locale` is new: the language
the product itself is shown in, declared by the client (`appLocale` in the
SDK) the way `$app_version` is. The migration renames `views.locale` to
`browser_locale`, adds `app_locale` to views and events, and adds a
`locales` views breakdown (`v_views_locales`) and an `$app_locale` arm in
`product_attributes`. It rewrites no values and runs in well under a
second.

The check is a question: does any client other than the served JS SDK send
`$locale`? From upgrade day `$locale` is an unknown reserved key: dropped,
with a warning in the ingest response, so that client stores no locale
until it sends `$browser_locale`.

What changes on the day:

- Pages still running yesterday's SDK (it is cached for a day) send
  `$locale`, so their views carry no browser locale until they pick up the
  new file. Purge the CDN copy of `/js/twillingate.js` on upgrade day if a
  CDN sits in front of the collector.
- `locales` has no rows for days rolled up before the upgrade: no locale was
  ever aggregated. Days still raw at upgrade time appear at once.
- SQL reading `views.locale` directly (the CLI's database, not the `query`
  tool, which only sees `v_*` views) needs `browser_locale`.

There is no down migration. The previous binary writes a `locale` column
that no longer exists, so every view it receives fails to store.

### Upgrading to one events table (migration 020)

Views and product events move into one raw table, `events`, with a `family`
column (`views` or `product`). Every `v_*` view, aggregate table and tool
answers exactly as before, except as listed below. The migration copies the
raw window (days not yet rolled up, 30 by default) and drops the `views`
table: seconds on a month of traffic. While it runs the file briefly holds
the raw window twice, so keep that much free disk.

Before upgrading, run this against the live database:

```sql
-- A project declaring a $ key. Until now such a declaration extracted
-- nothing. From 020, $host, $path, $referrer, $utm_source, $utm_medium,
-- $utm_campaign, $os_version, $browser_version and $device_model start
-- working; any other $ key is refused on the project's next edit.
SELECT id, attributes FROM projects WHERE attributes LIKE '%"$%';
```

What changes on the day:

- `v_events_flat` returns view rows too, and every typed column of the raw
  row (`path`, `os`, `country`, …). Saved SQL over it adds
  `WHERE family = 'product'` to keep its old answer.
- SQL reading the `views` table directly (the CLI's database, not the
  `query` tool) reads `events WHERE family = 'views'`.
- SQL reading the `events` table directly now also gets view rows; it adds
  `WHERE family = 'product'` to keep its old answer.
- `product_attributes` always includes `$kind`, `$browser`, `$device` and
  `$browser_locale`. They are empty for product events stored before the
  upgrade, which never kept them.
- Product events keep every reserved key they are sent (`$browser`, `$device`, `$host`, `$path`, `$referrer`, UTM, display size, `$session_id`, country), and views keep their custom attributes. Rows stored before the upgrade have empty columns for what was dropped then.
- A `null` per-event attribute now removes the batch value instead of storing `""`.
- The served SDK adds the page's `$host` and `$path` (masked like a view's) to every product event, and accepts `autoAttributes: false` and `null` families. A product event carries the location of the last view the page sent, so an `onPage` redaction recipe applies to product events too. Pages on the cached old SDK send events without location for up to a day; purge the CDN copy of `/js/twillingate.js` if one sits in front of the collector.

There is no down migration, and the previous binary cannot run against the
upgraded file: its ingest fails for both views and product events (it writes
a `views` table that no longer exists and `events` rows without a family) and
its daily pass aborts. Rolling back means restoring the pre-upgrade copy,
so take one before upgrading.

### Upgrading to reporting dashboards (migration 021)

Adds `components`, `dashboards` and `widgets`, and the daily pass starts
purging archived rows: once a project, dashboard or widget has been archived
longer than `RETENTION_ARCHIVED_DAYS` (default 30), it is deleted on the first
start after upgrading, then daily at 03:00 UTC — for a project, with all its
data. Before upgrading, restore any archived project you mean to keep, or set
`RETENTION_ARCHIVED_DAYS=0` (keeps archived items forever) until you have
reviewed them.

What changes on the day:

- `/app/` serves the dashboards wherever the API is served, without a
  login for the page itself; its data comes through `/api/` with the API's
  login. A `token://` login needs no new `redirect=` entry on the API's
  host. If the API has a hostname of its own (not `PUBLIC_URL`), set
  `API_URL=https://<api-host>`; a `resource=` already in `API_AUTH_DSN`
  keeps working. In `oauth://` mode, allow `https://<api-host>/app/callback`
  as a redirect at the identity provider (see `docs/deployment.md`,
  Dashboards at /app/).
- A later release that removes a component leaves the widgets using it
  showing "component removed" until an agent switches them to another
  component or archives them.
- Rolling the binary back to an older release re-migrates the system
  dashboards to that release's definitions and permanently clears the
  component of every widget on a component the older release lacks;
  upgrading again does not restore it. Before rolling back, run
  `list_widgets` and note which widgets use components the older release
  does not have.
- Rolling back to a binary from before this migration leaves the new tables
  unused (and nothing purged); upgrading again is safe.

### Upgrading to dashboard groups (migration 022)

Adds `group_id` to `dashboards`: dashboards sharing one are tabs of a single
sidebar entry, named by the first live one. Nothing to check before: the
migration adds the column, one index and one trigger, then sets every
existing row's `group_id` to its own id, so every dashboard starts as a group
of one. The trigger, `dashboards_own_group`, gives a row inserted without a
`group_id` its own id as its group.

What changes on the day:

- The sidebar's "Reports" entry reads "Views", with the same five tabs
  (Views, Product, Users, Groups, Retention) it always had — the system
  dashboards become one group.
- Every existing user dashboard becomes a group of one, so nothing else
  changes: `list_dashboards` and `get_dashboard` gain `group_id` (and
  `get_dashboard` a `tabs` list), but a dashboard alone shows no tab bar and
  moves exactly as before.
- `duplicate_dashboard` on a user dashboard no longer makes a separate
  dashboard: the copy joins the source's group as the next tab
  (`update_dashboard` with `group_id: 0` makes it its own sidebar entry). A
  system dashboard's copy is still a new dashboard, and `whole_group` copies
  a whole group as a new dashboard with the same tabs.

There is no down migration, but the column is additive and the previous
binary starts against the upgraded file. It inserts dashboards without
naming `group_id`; the trigger gives each one its own id as its group, so
dashboards it creates or duplicates stay separate entries when the upgrade
is applied again. Its `update_dashboard` with `after` moves one dashboard,
not its group, so it can leave a group's tabs split around another
dashboard in the order: the sidebar and the tab bar key on `group_id`, so
the group still shows as one entry with all its tabs, and the next move of
that group to a new place, on this binary, joins its tabs up again.

### Upgrading past the Evidence dashboards and litestream

The Evidence site, the `twillingate-evidence` image, the `dashboards`
subcommand and `docker-compose.evidence.yml` are gone; the dashboards at
`/app/`, served by the console, replace them. The litestream files go with
them. No schema changes.

- **compose:** take `docker-compose.evidence.yml` out of `COMPOSE_FILE` in
  `.env` (or drop its `-f`) *before* pulling: the new release publishes no
  `twillingate-evidence` image, and a compose project still naming the file
  keeps running the last one it pulled. Then `docker compose up -d
  --remove-orphans` stops the old `dashboards` container, and the file can
  be deleted. Its volume is the shared `data` one, so nothing is lost.
- **A dashboards-only host** (the old two-server topology) has nothing left
  to run: stop its compose project and remove its restore cron or `restore`
  service.
- **litestream:** the release no longer ships `litestream.yml`,
  `litestream.service` or `restore.sh`, and the installer no longer writes
  them. One already installed keeps running untouched: the installer neither
  updates nor removes it, so keep it as your backup or take it out yourself.
  `docs/litestream.md` now holds the configuration, the unit and the
  compose service, to compare against or set up again.
- **`DASHBOARDS_*`** variables are no longer read; delete them from
  `twillingate.env` or `.env` at leisure. A leftover one is ignored.
- Open the dashboards at `/app/` on the console's host. That needs
  `CONSOLE_AUTH_DSN` set, which a reporting-only install may not have had:
  see "The console" in `docs/deployment.md`.

### Upgrading to one retention pair (no migration)

Retention is one pair for every family: `RETENTION_EVENTS_RAW_DAYS`
(default 30) and `RETENTION_EVENTS_AGGREGATE_DAYS` (default 365). The old
per-family variables are no longer read and no longer refuse the boot, so a
leftover one is silently ignored and its family falls back to the defaults.
Before upgrading, look for them in `twillingate.env`:

```sh
grep -E '^RETENTION_(VIEWS|PRODUCT|WEB|APP)_' /etc/twillingate/twillingate.env
```

Replace them with the two new names, choosing one value where views and
product differed. A lower aggregate window than before deletes the older
aggregates on the first daily pass.

### Upgrading to a clustered events table (migration 023)

The raw `events` table is rebuilt so each family's rows for a project and
day are stored together, keyed by `(family, project_id, day, id)`. Its two
indexes go away. Every view, tool, dashboard and saved query answers exactly
as before. The migration writes a full new copy of the raw window (30 days
by default), and the WAL holds that copy too until it is checkpointed: keep
free disk of about twice the raw window's size at peak. The file does not
shrink afterwards: with `auto_vacuum=INCREMENTAL` each daily pass returns
only about 4 MB, so if the space matters, stop the service after the upgrade
and run `sqlite3 /var/lib/twillingate/twillingate.db 'VACUUM'` once
(it needs as much free disk again while it runs). Litestream replicates the
whole new copy as WAL, so expect one upload of about the raw window's size.

What changes on the day:

- Duplicates are detected on `(family, project_id, day, id)`. A retried
  batch is still ignored; the one gap is a retry of an event whose timestamp
  was clamped (more than 5 minutes ahead, or older than the raw window) that
  arrives on the other side of midnight: it is stored twice.
- SQL reading `events` directly (the CLI's database, not the `query` tool)
  sees `day` as an ordinary column; its values are unchanged.
- `SELECT *` over `events`, `raw_views` and `raw_product` returns
  `family, project_id, day, id` first; the other columns follow, and every
  column keeps its name. Queries that name their columns are unaffected.

There is no down migration. Rolling back means restoring the pre-upgrade copy
or Litestream snapshot, so take one before upgrading.

### Upgrading to measures (migration 024)

A third family, `measures`, stores performance numbers: `events` gains
`value`, `measure` and `sample_rate` (no rebuild), and two aggregate tables
and two views appear. Nothing existing changes its answer.

What changes on the day:

- Two system dashboards appear as one sidebar entry, Web Vitals, with a
  Measures tab. They stay empty until a site sets `data-vitals` or a client
  sends `family: "measures"`.
- `v_events_flat` has three more columns: `value`, `measure`, `sample_rate`.
  `raw_views` and `raw_product` gain the same three plus `bucket`, so a
  `SELECT *` over any of the four returns more columns.
- The served SDK sends `family` on every event. Purge a CDN copy of
  `/js/twillingate.js` if one sits in front of the collector.
- Backends and self-hosted SDK copies must reach an upgraded server before
  sending measures: an older one stores them as product events.
- Tools that open the database file directly (`sqlite3` for a backup or a
  check, litestream's `restore.sh`) need SQLite 3.35+ built with math
  functions: the new `bucket` column calls `log()` and `ceil()`, and without
  them even `PRAGMA quick_check` fails with `unknown function: ceil()`, so
  `restore.sh` would keep the previous replica on every run. Check with
  `sqlite3 :memory: 'select log(10)'` (expect `1.0`). The litestream 0.5
  image and Debian 12's and Alpine's `sqlite3` already qualify.

### Upgrading to the console (no migration)

The authenticated surface — MCP, REST, the login and `/app/` — is called
the console, and every name that said "api" for it now says "console". URL
paths do not change: `/ingest/events`, the `/api/events` alias, `/api/`,
`/mcp`, `/oauth/*` and `/app/` stay where they are, so connected clients,
saved tokens and ingest keys keep working. Before upgrading, rename in
`twillingate.env` (compose: `.env`):

| Before | After |
| --- | --- |
| `API_AUTH_DSN` | `CONSOLE_AUTH_DSN` |
| `API_ADDR` | `CONSOLE_ADDR` |
| `API_URL` | `CONSOLE_URL` |
| `API_DB_PATH` | `CONSOLE_DB_PATH` |
| `API_QUERY_TIMEOUT` | `CONSOLE_QUERY_TIMEOUT` |
| `API_QUERY_MAX_ROWS` | `CONSOLE_QUERY_MAX_ROWS` |

```sh
sudo sed -i 's/^API_/CONSOLE_/' /etc/twillingate/twillingate.env
```

A leftover `API_*` name refuses the boot, naming its replacement, so a
missed one shows up at once rather than silently turning the console off.

What changes on the day:

- `serve -api` is `serve -console` and `keygen -api` is `keygen -console`;
  the old flags exit naming the new one. Update any compose `command:` or
  script that passes them.
- A split systemd install's `twillingate@api` becomes `twillingate@console`:
  the installer disables the old instance, enables the new one and starts it
  if the old one was running.
- The boot log labels the listeners `surfaces=ingest,console` (or
  `console`) instead of `ingest,api`, and the skip warning reads `console
  disabled`; update any log alert matching the old text.

### Upgrading to range-bounded views and configurable caps (migrations 025 and 029)

Migration 025 recreates every `v_*` view's live half so a query reads only
the raw days its range covers; no data is copied and every view answers as
before. The views breakdowns' fixed cap of 500 gives way to
`ATTRIBUTE_VALUES_TOP_N`, now one cap for every value a client picks: per
views breakdown (kinds included) and per attribute event and key. Users and
groups get their own setting, `IDENTITIES_TOP_N`, in place of a fixed 500.
Migration 029 points the views breakdowns at that one cap; it copies no
data either.

The defaults are sized for an indie site or app. `ATTRIBUTE_VALUES_TOP_N`
keeps 100 values a day per views breakdown (was 500) and per event and
attribute key (was 50); identities keep 500 (unchanged). Past the 100th
path of a day, a small site's traffic is single crawler hits, so the tables
keep every value worth ranking while a crawler day stays small. To keep 500
values per breakdown, set it before upgrading in `twillingate.env`
(compose: `.env`); this raises the attribute cap to 500 as well:

```sh
ATTRIBUTE_VALUES_TOP_N=500
```

Also check there:

- `ATTRIBUTE_VALUES_TOP_N=0` used to fall back to 50; it now keeps every
  value, in the views breakdowns too. Set a number to keep a cap.
- A negative `*_TOP_N` now refuses the boot, naming the variable.

A changed cap applies to the live days at once and to days rolled up after
the change; days already rolled up keep the `(other)` rows they were written
with.

### Upgrading to projects in the console (migration 026)

`list_ingest_keys` (and `GET /api/keys`) now answers `label`, `key` and
`state` instead of `Label`, `Key` and `State`; update any script that reads
them. The console gains `/app/projects`: every project's usage, details and
ingest keys, and how its data meets the caps. Migration 026 adds (and 030
names) `usage_history`, a daily history of each project's size and counts and of
the database's size. The first daily pass, which also runs at start, counts
every day the aggregates still hold (a year by default), so the usage
history starts there; sizes start that day and fill in one day at a time.

### Upgrading to attribute breakdowns (migration 028)

Migration 028 adds `received_attributes`, the attribute keys each
project's product events and measures carried per day. Ingest records
a key the first time it arrives each day; the daily pass counts them.
The first pass, which also runs at start, counts the raw window's days,
so the project dialog lists the keys received in the last 30 days at
once.

`ATTRIBUTE_BREAKDOWNS_MAX` (default 10) now bounds the attributes all
active projects declare together. A server already past it keeps every
declared attribute, and saving a project still works, but nothing can
add an attribute until the total is under the limit or the setting is
raised. After upgrading, the project dialog's Breakdowns header shows
"N of 10 in use", and the `received_attributes` tool (or
`GET /api/received-attributes` without `project_id`) answers
`breakdowns_used` and `breakdowns_max`.

### Upgrading to one group of system dashboards (no migration)

Web Vitals and Measures become the last two tabs of the Views group, so the
sidebar's separate Web Vitals entry goes; agents that stored `group_id` 6
should use 1. A server that archived one of the two old groups but not the
other keeps each dashboard as it was: the group's one entry shows the live tabs,
and `restore_dashboard` (or `archive_dashboard`) with `whole_group` on any
of them restores (or archives) all seven.

On the same release the Product dashboard's "Attribute values by day"
lists declared custom attributes only, "Events by name" becomes "Top
events" (the range's five busiest), and the Views dashboard's Browsers
and Operating systems are pies by name, versions summed in.

### Upgrading to dashboard group names (migration 031)

No pre-checks. Migration 031 adds `dashboard_groups`, where a dashboard
group's name lives (`list_dashboards` and `get_dashboard` answer it as
`group_title`). A group without a name still takes its first tab's title.
After the upgrade the built-in group's sidebar entry reads "Reports" instead
of "Views"; its tabs are unchanged. Dashboard titles and group names now need
at least 2 characters when set; existing one-character titles are kept.
Rollback: an older binary keeps the names, but it does not move them. If a
group's first tab leaves its group under the older binary, its new group of
one takes the name with it.

### Upgrading to project tabs (migration 032)

No pre-checks. Migration 032 adds `project_tabs`, each project's list of
dashboard tabs, and two flags on every dashboard, `sidebar` and
`project_tab`. After the upgrade a project's page opens on Setup, with the
built-in dashboards as tabs next to it: every existing project gets every
built-in.

Built-in dashboards are no longer archived. A built-in group hidden before
the upgrade stays hidden from the sidebar, but it is no longer archived, so
the Archive page no longer lists it; "Show in sidebar" in the Dashboards
gallery brings it back. `archive_dashboard` and `restore_dashboard` on a
built-in are now refused; scripts or agents that hid a built-in group with
`archive_dashboard {whole_group: true}` should call `update_dashboard`
with `sidebar: false` instead (and `sidebar: true` to show it). Rollback:
an older binary ignores the new columns and table, so a built-in group
hidden from the sidebar shows there again under it.
A built-in group hidden under the older binary comes back hidden (out of
the sidebar) on this version; "Show in sidebar" in the Dashboards gallery
shows it again.

### Upgrading to widget shares (migration 033)

No pre-checks. Migration 033 adds `widget_shares`, where a shared
widget's two PNGs and the text they show are kept. Nothing existing
changes. Rollback: an older binary ignores the table, and share links
answer 404 until the newer binary is back.

### Upgrading to forms (migration 035)

No pre-checks and no data step. Migration 035 adds `forms` and
`submissions`; nothing existing changes, and nothing happens until a page
posts a form. `FORMS_DRAFT_DAYS` is new and optional (default 7: how long
a form created by its first submission accepts submissions as a draft
before it must be approved). Submissions are personal data, kept until
deleted or until their project is; they are never readable through
`query`. Rollback: an older binary ignores both tables, and form posts
answer 404 until the newer binary is back.

### Upgrading to simpler submissions (migration 036)

No pre-checks. A submission now keeps only what the visitor typed, when it
arrived and where its visit came from (referrer and UTM source, medium and
campaign); the page, the visitor and the environment are on its
`$form_submit` event. Migration 036 copies each stored visit's referrer
and UTMs onto its `$form_submit` event while that is still in the raw
window, renames `visit` to `attribution` keeping only those four values
(empty ones left out), and drops `host`, `path`, `via`, `actor_kind` and
`actor_id` from `submissions`: a visit's landing path and view count, and
a submission's page, are gone for good.
Events written before the upgrade keep the environment they had (none
beyond the page and country); new ones take it from the visit. Tables and
CSV exports lose the `Page`, `Referrer` and `UTM` columns. Rollback: an
older binary fails on the missing columns; restore the snapshot.

### Upgrading to project landing (migration 037)

No pre-checks. Every existing form is marked seen as of the upgrade, so
the new Forms badge starts at 0 and counts submissions received after it.
Each project's tab order is unchanged on screen; migration 037 rewrites
the tab keys into one order for all of a project's tabs, so built-in tabs
can now be moved like your own. A built-in a later release adds goes last
on every project. Rollback: an older binary ignores `forms.seen_at`;
restore the snapshot to get the old tab keys back.

### Upgrading to the country breakdown (migration 038)

No pre-checks. `$country` joins the attribute keys every project breaks
down: `product_attributes`, `measures` and `v_product_attrs` /
`v_measures_attrs` answer it for every raw day still held, from the
country already stored on each event. Days rolled up before the upgrade
have no `$country` rows, so a range reaching past the raw window shows
the breakdown only for its recent days. Each event name (and each
measure) gains one more set of rolled-up rows per day, at most
`ATTRIBUTE_VALUES_TOP_N` plus `(other)`. Rollback: an older binary runs
on the migrated database, but its daily pass stops rolling `$country`
up, so rolled-up days lose it again; restore the snapshot instead.

### Upgrading to consistent variable names (no migration)

Eight environment variables are renamed so each group reads alike: the
ingest pair matches the console pair (`INGEST_ADDR`/`INGEST_URL` beside
`CONSOLE_ADDR`/`CONSOLE_URL`), every retention clock starts `RETENTION_`,
both databases are DSNs, and every time that is not a day count is a
duration with a unit. Before upgrading, rename in `twillingate.env`
(compose: `.env`):

| Before | After |
| --- | --- |
| `PUBLIC_URL` | `INGEST_URL` |
| `RETENTION_EVENTS_RAW_DAYS` | `RETENTION_RAW_DAYS` |
| `RETENTION_EVENTS_AGGREGATE_DAYS` | `RETENTION_AGGREGATE_DAYS` |
| `FORMS_DRAFT_DAYS` | `RETENTION_FORMS_DRAFT_DAYS` |
| `CONSOLE_DB_PATH=/path/to.db` | `CONSOLE_DATABASE_DSN=sqlite:///path/to.db` |
| `CONSOLE_QUERY_MAX_ROWS` | `CONSOLE_QUERY_ROWS_MAX` |
| `REPORTING_CACHE_SECONDS=900` | `REPORTING_CACHE_MAX_AGE=900s` (or `15m`) |
| `REPORTING_REFRESH_SECONDS=60` | `REPORTING_REFRESH_MAX_AGE=60s` (or `1m`) |

```sh
sudo sed -i -E \
  -e 's/^(#?)PUBLIC_URL=/\1INGEST_URL=/' \
  -e 's/^(#?)RETENTION_EVENTS_(RAW|AGGREGATE)_DAYS=/\1RETENTION_\2_DAYS=/' \
  -e 's/^(#?)FORMS_DRAFT_DAYS=/\1RETENTION_FORMS_DRAFT_DAYS=/' \
  -e 's/^(#?)CONSOLE_QUERY_MAX_ROWS=/\1CONSOLE_QUERY_ROWS_MAX=/' \
  -e 's|^(#?)CONSOLE_DB_PATH=/|\1CONSOLE_DATABASE_DSN=sqlite:///|' \
  -e 's/^(#?)CONSOLE_DB_PATH=/\1CONSOLE_DATABASE_DSN=/' \
  -e 's/^(#?)REPORTING_(CACHE|REFRESH)_SECONDS=([0-9]+)$/\1REPORTING_\2_MAX_AGE=\3s/' \
  -e 's/^(#?)REPORTING_(CACHE|REFRESH)_SECONDS=/\1REPORTING_\2_MAX_AGE=/' \
  /etc/twillingate/twillingate.env
```

A leftover old name refuses the boot, naming its replacement, so a missed
one shows up at once rather than silently falling back to a default. A
bare number in `REPORTING_*_MAX_AGE` refuses the boot too (`900` has no
unit). Nothing else changes: `limits` reports the new names in `setting`,
and connected clients, keys and data are untouched.

### Upgrading to longer default limits (no migration)

Two defaults rise; a server that sets the variable keeps its value.
`RETENTION_AGGREGATE_DAYS` goes from 365 to 400 days, so a
year-on-year comparison still finds last year's day. Days the old
default already pruned are gone, so the extra 35 days fill in over the
five weeks after the upgrade, and the aggregates grow by that much.
`ATTRIBUTE_BREAKDOWNS_MAX` goes from 10 to 50: the project dialog's
Breakdowns header reads "N of 50 in use", and a server held at 10 can
declare more at once. Set either back in `twillingate.env` (compose:
`.env`) to keep the old limit.

### Upgrading to project attributes (migration 040)

No pre-checks. The `received_attributes` tool becomes `list_attributes`,
and its route `GET /api/received-attributes` becomes
`GET /api/projects/{project_id}/attributes`, where `project_id` is
required; the old tool is gone and the old route answers 404. Its list
is `attributes` instead of `keys`, each key's `declared` is `breakdown`,
and each key also carries `blocked` and `aliases`. Migration 040 renames
the table `received_attributes` to `usage_attributes`, replaces
`event_rules` by `rules` (event rules keep their behaviour, with
`kind = 'event'`) and `projects.attributes` by `project_attributes`,
one row per key a project has, with its breakdown flag. `list_projects`
and `schema://projects` answer `attributes` ordered by key. SQL or
scripts naming any of the old names must follow.

Rollback: an older binary cannot run on schema 040, since it reads the
old table names and the dropped column. Roll back by restoring the
backup taken before the upgrade; events ingested since are lost.

### Upgrading to the late window (no migration)

The oldest client timestamp ingest accepts is now `INGEST_LATE_DAYS` (default
1 day), no longer `RETENTION_RAW_DAYS`. Older timestamps are clamped to that
edge as before. `INGEST_LATE_DAYS` must be at least 1 and at most
`RETENTION_RAW_DAYS`, so `RETENTION_RAW_DAYS=0` is refused: set it to 1 or
more.

Why: raising `RETENTION_RAW_DAYS` on a running server moved the clamp back
over days already rolled up. A late event landing in such a day became its
only raw row, and the next daily pass rewrote that day's aggregates from
that one row. If your raw window was ever raised, find the damaged days
before upgrading (nothing can rebuild them; raw rows are gone):

```sh
sqlite3 -readonly /var/lib/twillingate/twillingate.db \
  "SELECT day, SUM(count) FROM agg_product_daily GROUP BY day ORDER BY day;"
sqlite3 -readonly /var/lib/twillingate/twillingate.db \
  "SELECT day, SUM(views) FROM agg_views_daily GROUP BY day ORDER BY day;"
```

A day with a handful of events between full days, after the date the window
was raised, is one that was rewritten. The raw-first design replaces
aggregates with raw history; until then, keep `RETENTION_RAW_DAYS` where it
is.

Events 2 or more days late now land on yesterday rather than their own day.
And the duplicate check is per day: a copy of an event resent more than
`INGEST_LATE_DAYS` after it happened clamps to a new day and is stored twice,
where the 30-day window kept it on one day. The SDK of this release replays a
batch `sendBeacon` already accepted only within 20 hours of its oldest event,
so it sends no such copy; pages still running an older SDK bundle can, until
they reload it. Raise `INGEST_LATE_DAYS` (up to `RETENTION_RAW_DAYS`) to
widen both back.

Rollback: an older binary ignores `INGEST_LATE_DAYS` and clamps to the raw
window again; nothing else changes.

### Upgrading to the engine build (no migration)

The binary now carries DuckDB (cgo) for the raw-first analytics engine;
nothing uses it yet, and nothing changes for users. What changes for
operators:

- **32-bit ARM is no longer built.** A Raspberry Pi on the 32-bit OS needs
  the 64-bit OS first (a Pi 3, 4, 5 or Zero 2 W runs it); the installer
  refuses `armv7l` and `armv6l`. ARMv7 NAS boxes and boards stay on the
  previous release.
- **glibc 2.35 or newer** (Debian 12, Ubuntu 22.04 or later); the
  installer checks. Check first: `getconf GNU_LIBC_VERSION`.
- **The image is Debian-based** (`debian:bookworm-slim`, from Alpine), same
  user and ids (10001), same volume; `docker compose pull` as usual. It has
  no `linux/arm/v7` any more: a 32-bit ARM Docker host pins the previous
  release's tag (`TWILLINGATE_VERSION=` in `.env`).
- The binary grows from about 15 MB to about 80 MB.
- Two settings, `ENGINE_MEMORY_LIMIT` and `ENGINE_MAX_CHILDREN`, and an
  `engine/` directory beside the database once an engine child has run
  (`twillingate engine -check` runs one). It holds spill files and lock
  files; Litestream replicates only the database, so nothing to change.

Rollback: the previous release's binary or image; nothing on disk differs.

### Upgrading to the event files (no migration)

Ingest now also writes every event under `raw/` in the data directory: a log per project and day, a Parquet file per day after it ends, a file per month once the month is final. Nothing reads them yet; SQLite still answers every query. Expect, at the cap of 1M events a month, about 40 MB of logs a day before the dump and about 50 MB of Parquet a month after it.

- New setting `RETENTION_DAYS` (default 400): months of files older than it are deleted.
- Back up `raw/` with the database from now on (`docs/deployment.md`, "Back up and restore"); Litestream does not cover it.
- The engine child runs once a day for the dump and merge (`twillingate engine -check` shows it starts).

Rollback: an older binary ignores `raw/`; delete it if you go back for good.

### Upgrading to query on the event files

No migration, nothing to run. Two visible changes:

- `query` (MCP and `POST /api/query`) takes `source`: `sqlite`, the default,
  reads the `v_*` views and `agg_*` tables as before; `files` reads the raw
  events through DuckDB (`docs/twillingate.md`, "Writing SQL against the
  event files"). The files hold the events received since the upgrade that
  began writing `raw/`.
- `CONSOLE_QUERY_TIMEOUT` defaults to `30s` (was `10s`), for `query` and
  widget SQL alike. Set `CONSOLE_QUERY_TIMEOUT=10s` to keep the old bound.

### Upgrading to the derived tables (no migration)

No migration, nothing to run. What changes:

- The event files keep three more fields on every event: `install_id`,
  `user_name` and `group_name`. The names your clients send beside
  `$user_id` and `$group_id` now stay with each event under `raw/` for
  `RETENTION_DAYS`, not only as the latest name in the database: count
  them as personal data in the files too.
- The daily pass derives `sessions/`, `identities.parquet` and
  `aliases.parquet` in each project's directory under `raw/`. The first
  pass after the upgrade derives them for every file already there, a few
  seconds per month of events at the cap. They need no backup: a pass
  rebuilds what is missing.
- `query` with `source: "files"` reads three more tables, `sessions`,
  `identities` and `aliases` (`docs/twillingate.md`, "Writing SQL against
  the event files"). Events stored before the upgrade have no install id
  or names, so `aliases` and the names in `identities` start from the
  upgrade.

Rollback: an older binary ignores the derived files and reads the new
fields as extra columns; delete the derived files if you go back for good.

### Upgrading to imported event files (no migration, one command)

No schema migration. After upgrading (v0.19.0 is the first release with
the command), run one command once, with the ingest service stopped: it
copies into `raw/` every raw row the database holds that the files do not.

- The rows it copies are the events from before the upgrade that began
  writing `raw/`, back to the oldest day the raw window
  (`RETENTION_RAW_DAYS`) still holds, and any batch the log missed since.
- Run it soon. Each day it waits, the oldest raw day leaves
  `RETENTION_RAW_DAYS` and can no longer be imported.
- It takes about 30 s per million rows. The next start's pass turns what
  it copied into day files and derives their tables: for one month at
  the cap (985,000 rows), about 6.5 minutes on a loaded 4-core host, with
  the engine child at about 950 MB. Until that pass ends, dashboards and
  queries over the files answer slowly: each query loads the closed logs
  the pass has not reached yet, so a small host sees its memory peak
  then.
- `EVENTS_DIR` must be on a filesystem that supports `flock`: most local
  filesystems do, some FUSE and SMB mounts do not. The ingest server now
  refuses to start otherwise.

```sh
# systemd
sudo systemctl stop twillingate
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate events import'
sudo systemctl start twillingate

# compose
docker compose stop twillingate
docker compose run --rm twillingate events import
docker compose start twillingate
```

Stop the service first, whichever version runs. The command refuses
while a server of this version or later runs, since that server holds
`raw/.lock`. The release before this one holds no lock, so the command
cannot see it. On a split install, stop the ingest process; the console
may keep running. If the service was started while the import ran, it
refused to start, because `raw/` was locked: start it again after the
import (`sudo systemctl start twillingate`).

Run it as the service user, who owns the database file. The command
refuses any other user: files it wrote as root would lock the service out
of `raw/`. The systemd lines above use `sudo -u twillingate`. Compose's
`run` already uses the image's user.

The command prints one line for each project that had days to import,
and `imported N of M rows` when done. If a day fails, it says which and
exits 1; run it again, and what is already copied is skipped. Once it
has succeeded, the `meta` row `events_imported` records when, and the
command only says so. `-force` copies again, for example after restoring
an older `raw/`.

- History older than the raw window exists in the database only as daily
  aggregates (`agg_*`). They cannot become events, so they are not
  imported. Nothing changes today: dashboards still read the database.
  Once a later release moves the dashboards onto the files, they start at
  the oldest raw day the database holds now:
  `sqlite3 /var/lib/twillingate/twillingate.db "SELECT min(day) FROM events"`.
- An imported row has an install id only where the install was the actor
  (an `$install_id` outranked by a `$user_id` was never stored). It has the
  latest name the database holds for its user and group, not the name sent
  with it.
- An imported row carries its event name and attribute keys as the
  database holds them, after the rules it applied. Ingest applies the
  rules before writing either store, and a rename also rewrote the
  database's older rows. An imported row keeps a renamed name even if
  that rule is later removed.
- Check it per project, after the start, on the days both hold before
  today. The files also hold days the database has already rolled up, and
  today's counts move while ingest runs. On the files, `query` with
  `source: "files"`:
  `SELECT day, count(*) FROM events WHERE project_id = 1 AND day < current_date GROUP BY ALL ORDER BY day`.
  On the database (systemd):
  `sqlite3 /var/lib/twillingate/twillingate.db "SELECT day, count(*) FROM events WHERE project_id = 1 AND day < date('now') GROUP BY day"`.
  The compose image has no `sqlite3`: use `query` with `source: "sqlite"`
  (on a release before migration 042, which removes that source), which
  reads the raw rows through `raw_views`, `raw_product` and
  `raw_measures`, or copy the file out of the volume:
  `SELECT day, count(*) FROM (SELECT project_id, day FROM raw_views UNION ALL SELECT project_id, day FROM raw_product UNION ALL SELECT project_id, day FROM raw_measures) WHERE project_id = 1 AND day < date('now') GROUP BY day`.

Rollback: an older binary reads the imported days like any other day
file, and ignores `raw/.lock`; nothing to undo.

### Upgrading to form attribution from the files (migration 041)

Migration 041 adds four columns to `submissions`; nothing to run. What
changes:

- A submission's `attribution` is found about a minute after it arrives,
  by a job that reads the event files: the submitter's last visit with
  a referrer or a UTM within `ATTRIBUTION_DAYS` (default 7), no
  longer the current visit's landing from the database's raw views. The
  submitter includes the install ids seen with its user, so a visit made
  before signing in counts. Until the job runs, `attribution` is `{}`.
- An approved form's `$form_submit` event is written by that job, about
  a minute after the submission, with the submission's arrival as its
  `ts`.
- Forms accept `$referrer`, `$utm_source`, `$utm_medium` and
  `$utm_campaign`; any of them makes the submission's own the source.
- Submissions from before the upgrade keep their attribution and events.
- **Anonymous tags with consent now send a visitor id.** An instance
  with `identity` anonymous (the default) and consent given persists a
  visitor id and sends it as `$install_id`: pseudonymous, never a user.
  Such a site's returning visitors become one actor across days, so its
  unique visitors over more than a day drop from the upgrade on, and the
  collector logs `project receives ids` for it. A site whose privacy
  notice promises no identifier under consent should update it or keep
  consent off. A plain HTML form on such a site is matched to the views
  only through the connection hash, so it misses the visits made after
  consent: use a tagged form or send `$install_id` from it.

Rollback: an older binary ignores the new columns and attributes on
arrival again. Submissions still pending at the rollback keep `{}` and
get no event, and events still waiting to be logged at the rollback
reach SQLite but not the event files.

### Upgrading to dashboards on the event files (no migration)

No schema migration. Dashboards now read the event files under `raw/`
through the engine, not the database, and widget SQL is DuckDB SQL over
the files' tables (`docs/reporting.md`, "Tables"). Before upgrading:

- **Run the import first, on v0.19.0, and start v0.19.0 once before
  upgrading.** v0.19.0 is the first release with `twillingate events
  import`. If the import has not run (see "Upgrading to imported event
  files" above), run it with v0.19.0's binary, start the service again,
  and wait for its first pass to turn the imported days into day files.
  From an older release, upgrade to v0.19.0 first, run the import there,
  let that pass finish, then upgrade to this release. The pass is done
  when no project's directory under `EVENTS_DIR` (default `raw/` beside
  the database) holds a closed log (`.closed`), a claimed one
  (`.dumping`) or an import's partial one (`.import`). Run the check as
  the service user, since the data directory is readable by it only:

  ```sh
  # systemd
  sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env
    dir=${EVENTS_DIR:-$(dirname "${DATABASE_DSN#sqlite://}")/raw}
    left=$(find "$dir" -mindepth 2 -maxdepth 2 \( -name "*.closed" -o -name "*.dumping" -o -name "*.import" \)) || exit 1
    if [ -n "$left" ]; then echo "not dumped yet:"; echo "$left"; else echo "done: nothing left to dump in $dir"; fi'
  # compose
  docker compose exec twillingate sh -c '
    dir=${EVENTS_DIR:-$(dirname "${DATABASE_DSN#sqlite://}")/raw}
    left=$(find "$dir" -mindepth 2 -maxdepth 2 \( -name "*.closed" -o -name "*.dumping" -o -name "*.import" \)) || exit 1
    if [ -n "$left" ]; then echo "not dumped yet:"; echo "$left"; else echo "done: nothing left to dump in $dir"; fi'
  ```

  It prints `done: nothing left to dump in …` once the pass has ended,
  and otherwise the files still waiting; an error (no such directory,
  permission denied) means the check did not run. A `.log` is a day's
  open log: it is not listed, since an event that comes late for a past
  day opens that day's log again. A `.import` stays only when an import
  stopped part way; no pass reads it, so run `twillingate events import`
  again, which rewrites it and skips what is already copied; one that
  stays after the import succeeded holds nothing the files lack, and can
  be deleted. Then upgrade. This release's start
  checks the system dashboards on the files before it serves anything,
  ingest included, and its first query loads every log not yet dumped,
  which on a month at the cap takes minutes and can exceed
  `ENGINE_MEMORY_LIMIT`. If that query takes over 5 minutes or runs out
  of memory, the start logs `could not check the system dashboards on
  the event files` and goes on without the check, and the dashboards
  answer slowly until this release's own pass ends; the next start
  checks again. Until the import has run, the
  dashboards show only what the files hold, and the console logs at
  start, naming the projects whose raw rows in the database reach back
  before their files: `SQLite holds raw events older than the event
  files`.
- **Check the engine:** `twillingate engine -check`, as the service user.
  The reporting migrator now validates the system dashboards on the files
  whenever a release changes them, so an engine child that cannot start
  (say, `engine/` beside the database not writable by the service user)
  now fails the start of the console, and of the CLI commands that open
  the database (`migrate`, `project`, `key`, `form`, `events import`),
  where it used to fail only the first query on the files. A start with
  no change to the system dashboards runs no engine child for it. Run
  those commands as the service user: one that would start a child as
  anyone but the database file's owner (root, say) is refused, since the
  child's `engine/` and `raw/` would then belong to that user.

What changes on the day:

- **History older than the raw window is gone from the dashboards.** They
  start at the oldest raw day the database held at the import
  (`RETENTION_RAW_DAYS` before it); the daily aggregates older than that
  stay in the database, readable with `query` and `source: "sqlite"`
  until migration 042 drops them with the source, but no widget reads
  them. From the upgrade on, the files keep
  `RETENTION_DAYS` (default 400) of history.
- **What counts from the first day the files hold restarts there,** the
  oldest imported day (the upgrade day without an import):
  - New users, New groups and First seen on the Users and Groups
    dashboards: everyone already in that first day counts as new on it;
  - retention cohorts begin on it, so D30 and D45 stay empty until the
    first cohorts are 30 and 45 days old. With the default
    `RETENTION_RAW_DAYS` of 30, the 90d Retention dashboard is mostly
    empty at first.
- **No `(other)` rows:** view kinds, breakdowns and attribute values past
  `ATTRIBUTE_VALUES_TOP_N`, and users and groups past `IDENTITIES_TOP_N`,
  each show as their own row.
- **The Product dashboard's attribute table lists each value once over
  the range,** now "Attribute values", with exact Users and Groups for
  the range in place of "Users (at least)" and "Groups (at least)", and
  no longer a row per day.
- **Retention counts yesterday from 00:00 UTC,** as soon as the day ends,
  rather than after the 03:00 UTC pass.
- **The console's engine child serves every widget,** not only `query`
  on the files, so it runs whenever someone opens a dashboard. Its RSS
  can exceed `ENGINE_MEMORY_LIMIT`, which bounds DuckDB's own buffers:
  120–280 MB for one month at the cap, 0.8–1.4 GB over a year of it.
- **Widgets you or an agent made break** when their SQL reads SQLite's
  views. Nothing rewrites or archives them. Each shows DuckDB's error on
  its dashboard, with the hint "widget SQL is DuckDB SQL over events,
  views, product, measures, sessions, identities and aliases; see
  docs://reporting". The console also checks every one in the background
  at start and logs each that fails:

  ```sh
  journalctl -u twillingate | grep "widget SQL fails"
  docker compose logs twillingate | grep "widget SQL fails"
  ```

  Each line names `widget_id`, `dashboard_id`, `title` and the error.
  A check that runs out of time or memory, or whose engine child dies,
  says nothing of the widget's SQL: it is logged as `could not check
  widget on the event files`, with the `reason`, and the next start
  checks that widget again. A timeout (`CONSOLE_QUERY_TIMEOUT`, on the
  sample week) skips only that widget, and the check goes on. Running out
  of memory, a dead engine child, or a timeout while loading the open
  logs ends the check there (`the widgets after it go unchecked until the
  next start`). That is likely on the first start, while the boot pass
  still dumps the closed logs: restart the service once that pass has
  ended (the check above prints `done`), then grep again for the full
  list of `widget SQL fails on the event files` lines.
  Rewrite them with `update_widget` (an agent can, given
  `docs://reporting`). Three common rewrites:

  ```sql
  -- before: visitors per day (distinct per day and kind, added up)
  SELECT day AS x, SUM(visitors) AS y FROM v_views_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to GROUP BY day ORDER BY day
  -- after: the same figure
  SELECT day AS x, SUM(visitors) AS y FROM (
    SELECT day, kind, count(DISTINCT actor_id) AS visitors FROM views
    WHERE project_id = :project AND day BETWEEN :from AND :to GROUP BY day, kind)
  GROUP BY day ORDER BY day

  -- before: signups in the range and in as many days before it
  SELECT (SELECT SUM(count) FROM v_product_daily
          WHERE project_id = :project AND event_name = 'signup'
            AND day BETWEEN :from AND :to) AS value,
         (SELECT SUM(count) FROM v_product_daily
          WHERE project_id = :project AND event_name = 'signup'
            AND day BETWEEN date(:from, '-' || (julianday(:to) - julianday(:from) + 1) || ' days')
                        AND date(:from, '-1 day')) AS previous
  -- after
  SELECT count(*) FILTER (WHERE day >= :from) AS value,
         count(*) FILTER (WHERE day < :from) AS previous
  FROM product
  WHERE project_id = :project AND event_name = 'signup'
    AND day BETWEEN :from - CAST(:to - :from + 1 AS INTEGER) AND :to

  -- before: top countries (visitors distinct per day, added up)
  SELECT country AS label, SUM(visitors) AS value FROM v_views_countries
  WHERE project_id = :project AND day BETWEEN :from AND :to AND country != ''
  GROUP BY country ORDER BY value DESC LIMIT 10
  -- after: the same figure
  SELECT country AS label, SUM(visitors) AS value FROM (
    SELECT day, country, count(DISTINCT actor_id) AS visitors FROM views
    WHERE project_id = :project AND day BETWEEN :from AND :to AND country IS NOT NULL
    GROUP BY day, country)
  GROUP BY country ORDER BY value DESC LIMIT 10
  ```

  And the one-line rewrites most widgets need:

  | SQLite | DuckDB over the files |
  | --- | --- |
  | `json_extract(attributes, '$.plan')` | `attr_plan` (one column per key; quote odd keys: `"attr_plan type"`) |
  | first day from `v_identity_daily` (`MIN(day)`) | `identities.first_seen::DATE` |
  | `date(x, '-6 days', 'weekday 1')` (Monday of the week) | `date_trunc('week', x)::DATE` |
  | `date(x, '+7 days')`, `date('now', '-90 days')` | `x + 7`, `current_date - 90` |
  | `julianday(a) - julianday(b)` | `a - b` (days, BIGINT) |
  | `MAX(a, b)`, `MIN(a, b)` (two arguments) | `GREATEST(a, b)`, `LEAST(a, b)` |
  | `WITH RECURSIVE days(n) AS (…)` | `range(1, 31)` |
  | `JOIN t` with no `ON` | `CROSS JOIN t` |

  `count(DISTINCT actor_id)` over the whole range, without the per-day
  subquery, counts each visitor once instead: a different, smaller figure.

  A field the event did not send is NULL in the files, where the database
  had `''`; `day`, `:from` and `:to` are DATEs.
- `twillingate reporting dev` runs its widgets on the files in
  `EVENTS_DIR` (default `raw/` beside the `-db` database).
- **The analytics tools read the files too:** `views_overview`,
  `views_breakdown`, `product_events`, `product_attributes`, `measures`,
  `retention` and `identities` (MCP and REST) answer from the same tables
  as the dashboards, so their history starts where the dashboards' does,
  with no `(other)` rows. Their inputs and output columns are unchanged.
  What reads differently:
  - every key the events carried breaks down, with no declaration needed:
    `product_attributes` lists each custom attribute key and the reserved
    keys `$host`, `$path`, `$referrer`, `$utm_*`, `$os_version`,
    `$browser_version` and `$device_model` as well as the nine it always
    had, and `measures` takes any of them as `attr_key` (a key none
    carried gives no rows); `unique_groups` is `0`, never empty, when no
    group was involved;
  - `product_attributes` refuses a range longer than 92 days: its rows
    are per day, event and value, which over a year runs out of the
    engine's memory. Ask `query` for longer ranges;
  - `retention`'s `aggregated_through` is the newest day before today
    (UTC) with views or product events, rather than the 03:00 UTC pass's
    last day.
- **`query` reads the files by default.** Without `source`, its SQL is
  DuckDB SQL over the files' tables; a query naming a `v_*` view or an
  `agg_*` table fails with a hint to pass `source: "sqlite"`. That source
  still reads the database, deprecated: migration 042's release removes
  it.
  `schema://views` (and `GET /api/schema/views`) lists the files' tables
  first, then the deprecated views.
- **Ingest stops writing display names to the database.** The names sent
  beside `$user_id` and `$group_id` stay on each event in the files, where
  `identities`, the Users and Groups dashboards and the files' `identities`
  table read them; the database's `identities` table (`source: "sqlite"`)
  keeps the names it held at the upgrade, until migration 042 drops it.

Rollback: the previous release's binary reads the database as before.
Widgets rewritten for the files fail on it, as SQLite SQL now fails here.
Names sent while this release ran are missing from the database's
`identities` table, so the previous release's `identities` tool and Users
and Groups dashboards show them only once a client sends them again.

### Upgrading to events in the files only (migration 042)

Migration 042 drops the database's events: the raw `events` table,
`actors`, `identities`, the 21 `agg_*` tables, the `raw_*` and `v_*`
views, and the attribute tables `usage_attributes` and
`project_attributes`. **It cannot be undone.** The database keeps the
projects, keys, rules, forms, dashboards and the audit log; every event
lives in the event files under `raw/` (`EVENTS_DIR`) for `RETENTION_DAYS`.
A fresh install never creates an `events` table.

From 042 on, Litestream (or any copy of the database) holds no events: the
`raw/` copy schedule is your event backup, and a host lost between two
copies loses the events since the last one.

Before upgrading:

- **Back up the database** (`docs/deployment.md`, "Back up and restore").
  The backup is the only copy of the daily aggregates older than the raw
  window: the files never held them, and no dashboard has read them since
  the dashboards moved onto the files.
- **Run the import first, if it has not run.** While `events` holds rows
  and the `meta` row `events_imported` is unset, migration 042 refuses
  before it drops anything, and the start (or `twillingate migrate`) fails
  with:

  ```
  migration 042 drops the events table, which still holds raw events the event files may lack: stop the service, run `twillingate events import`, then start again
  ```

  Do as it says, with this release's binary: its `events import` migrates
  the database through migration 041 and no further, copies what the files
  lack (see "Upgrading to imported event files" above), and records
  `events_imported`; the next start runs 042. An install that has run since
  v0.18.0 wrote every event to both, so its import finds them all held and
  writes nothing, in seconds. There is no flag to skip the check: to drop
  rows the files lack, run the import anyway.

  ```sh
  # systemd
  sudo systemctl stop twillingate
  sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate events import'
  sudo systemctl start twillingate

  # compose
  docker compose stop twillingate
  docker compose run --rm --no-deps twillingate events import
  docker compose start twillingate
  ```

  An install that already ran `events import` (v0.19 or v0.20) needs
  nothing. Check before swapping binaries, so the installer's restart does
  not fail into "did not come back up":

  ```sh
  sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db \
    "SELECT value FROM meta WHERE key='events_imported'"
  ```

  A value means it ran; no row means run the import above.

  To see whether it ran, `twillingate events import` says when; a fresh
  install with no events needs nothing. Once the database is past 042, the
  command prints `the events table was dropped by migration 042; nothing to
  import` and exits 0. A later release removes it.

After upgrading:

- **Reclaim the space once.** The dropped tables' pages stay in the file
  as free pages, and the daily pass returns only about 4 MB of them a day
  (`auto_vacuum=INCREMENTAL`). Stop the service and run `VACUUM` once; it
  needs free disk for the new copy only, which is small:

  ```sh
  # systemd
  sudo systemctl stop twillingate
  sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db 'VACUUM'
  sudo systemctl start twillingate

  # compose: the image carries no sqlite3, so a throwaway Alpine container
  # runs it on the volume as the image's user (uid 10001)
  docker compose stop twillingate
  docker run --rm --volumes-from "$(docker compose ps -aq twillingate)" alpine:3.20 sh -c \
    'apk add -q sqlite && adduser -D -H -u 10001 twillingate &&
     su twillingate -s /bin/sh -c "sqlite3 /var/lib/twillingate/twillingate.db VACUUM"'
  docker compose start twillingate
  ```

  Litestream then uploads one new snapshot, of the small file.

What changes on the day:

- **Removed:** the `cap_usage` tool and its route
  (`GET /api/projects/{project_id}/cap-usage`, now 404); `query`'s
  `source: "sqlite"`, refused with `source "sqlite" was removed`
  (`source` itself stays for one release, so a client sending `"files"`
  keeps working); and the projects' `attributes` field, from
  `create_project`, `update_project` and `list_projects` (MCP, REST and the
  CLI's `-attr`): every attribute key breaks down in the files, with no
  declaration. A request that still sends `attributes` gets what any
  unknown field gets.
- **Fields that changed:**
  - `limits`: `events_30d {count, from, to}`, the events of the last 30
    days in the files, replaces `raw_events {held, window_days}`; the
    `caps` group is gone.
  - `list_attributes` reads the files: each key's `values` (distinct
    values over the range) replaces `max_values`, and `breakdown`,
    `breakdowns_used`, `breakdowns_max` and `values_cap` are gone.
  - `usage` reads the files: `size` is `{total_bytes, measured_at}`, the
    project's file bytes as the daily pass last measured them (`null`
    until then), and `raw_days`, `rolled_up_days`, `unused_attributes`,
    `size.raw_bytes`, `size.aggregate_bytes`, `declared_attributes`,
    `attribute_keys`, `attribute_values` and `attribute_values_folded`
    are gone.
  - `rename_event`, `merge_event`, `rename_attribute` and
    `merge_attribute` answer the rule alone: nothing is rewritten, so
    `rewritten` is gone. A rename checks the event files for the target.
- **Deleting a submission leaves its `$form_submit` event in the files.**
  The event holds no field, and the conversion counts stay.
- **The daily pass** keeps archiving shares and drafts past their date,
  the purge, the incremental vacuum, `usage_history` (the database's size,
  and each project's file bytes in the process that keeps the files) and
  the files' maintenance; it rolls nothing up any more.
- **Variables no longer used:** `RETENTION_RAW_DAYS`,
  `RETENTION_AGGREGATE_DAYS`, `ATTRIBUTE_VALUES_TOP_N`,
  `ATTRIBUTE_BREAKDOWNS_MAX` and `IDENTITIES_TOP_N` (and the older
  `RETENTION_EVENTS_RAW_DAYS` and `RETENTION_EVENTS_AGGREGATE_DAYS`):
  nothing is capped, and the files keep events for `RETENTION_DAYS`. A
  leftover one is ignored like any unknown variable; delete it from
  `twillingate.env` when convenient. `INGEST_LATE_DAYS` may not exceed
  `RETENTION_DAYS`.
- SQL or scripts reading the database's `events`, `identities`, `agg_*`,
  `raw_*` or `v_*` must read the event files instead (`query`,
  `docs/twillingate.md`, "Writing SQL against the event files").

Rollback: none past 042. An older binary fails to start, since its
start builds a view over the dropped `events` table. To go back, restore
the backup taken before the upgrade: the events ingested since are in
`raw/`, where the release before this one reads them for its dashboards,
but not in that database.

### Upgrading to widget SQL on the seven tables only (no migration)

Widget SQL and `query` now read only `events`, `views`, `product`,
`measures`, `sessions`, `identities` and `aliases`, and their own `WITH`
names. SQL that calls a table function other than `range`,
`generate_series` and `unnest` (`read_parquet`, `read_csv`, `glob`, …),
names a file in `FROM`, qualifies a table with a schema (`main.events`)
or reads any other table (`_tw_all`) is refused before it runs.

- **Before the upgrade**, nothing to check by hand.
- **On the day**, the start logs `widget SQL fails on the event files`
  for each user widget refused, with the refusal: rewrite it over the
  seven tables (`main.events` → `events`, scoped by `:project`). The
  system widgets read only the seven tables already.
- Scripts that send `query` such SQL get `invalid` with the same message.

Rollback: install the previous release; nothing on disk changed.

### Upgrading past the events import (no migration)

This release removes `twillingate events import`, so the section above
applies only through v0.21.x; its steps stay as the record.

Before upgrading:

- **An install still below migration 042 (any release before v0.21.0)
  upgrades to v0.21.x first** and follows the section above there: run
  `twillingate events import` with that release's binary, start it once
  so 042 runs, then upgrade to this one. Started directly on this release,
  a database whose `events` holds rows and whose `meta` row
  `events_imported` is unset is still refused by migration 042 before
  anything is dropped, and the start (or `twillingate migrate`) fails with:

  ```
  migration 042 drops the events table, which still holds raw events the event files may lack: upgrade to v0.21.x first and run `twillingate events import` there, then upgrade to this release
  ```

  Install v0.21.x (`TWILLINGATE_VERSION=v0.21.0` with the installer or in
  `.env`), do as it says, then upgrade again. An install already past 042
  needs nothing: the check reads nothing on it.
- **A restored backup from before 042** goes the same way: restore it with
  v0.21.x and run `twillingate events import -force` there before
  upgrading. This release migrates a pre-042 database that has
  `events_imported` set without looking at the files, so the events it
  holds that the restored `raw/` lacks would be dropped.

On the day:

- `twillingate events` is an unknown command.
- `query` takes `sql` only: `source` is an unknown field, refused like any
  other (`invalid` over MCP, `400` over REST), `"files"` included. Drop it
  from scripts and saved prompts.

Rollback: install the previous release; nothing in the database changed.

### Upgrading to built-in dashboards on the native engine (no migration)

The built-in dashboards read the event files natively: their widgets are
structured queries, no longer SQL. The start replaces each built-in
widget in place, keeping its id, except Web Vitals' "By device and
browser", which it removes for two new widgets; copies you made keep
their SQL.

- **Before the upgrade**, nothing to check by hand.
- **On the day**, these differ from the release before:
  - Stats over a range with no data are blank (–), not 0: visitors, bounce rate, avg session, actions per user and per group.
  - The five web-vitals stats show "–" over a range with no samples, where they said "No data for this range".
  - Daily active users and groups show every day of the range, with 0 on days without activity.
  - App versions shows true distinct users and groups. The header no longer says "(at least)", so a saved sort or filter on the old header resets.
  - Users by app version shows the 50 busiest versions. Within a day, series are ordered by name.
  - Attribute values is a table that loads at once, up to the console's row cap, attribute by attribute. It no longer pages through the server.
  - The p75-by-day measures chart keeps time measures only, at most 50, the slowest by p75 over the range.
  - Web vitals "by device and browser" is now two widgets, "By device" and "By browser"; a share of the old table no longer shows it.
  - The browsers and operating-systems "Other" row counts a visitor once per day across the folded browsers (it used to count once per browser).
  - Distinct counts over ranges past 1,024 ids are estimated within about ±2% and marked ≈.

Rollback: install the previous release; it restores the built-in widgets' SQL on start.

### Upgrading to analytics tools on the native engine (no migration)

`views_overview`, `views_breakdown`, `product_events` and the console's
event-names route read the event files natively, with the same inputs and
output fields.

- **Before the upgrade**, nothing to check by hand.
- **On the day**, these differ from the release before:
  - `views_overview` without `kind` counts an actor seen under two kinds on one day as one visitor, not two.
  - A distinct count past 1,024 ids in one row is estimated within about ±2%, and the table's `note` names its column.
  - Where `CONSOLE_QUERY_ROWS_MAX` was raised above 1,000 (or set to 0), `views_breakdown` returns at most 1,000 rows, and `views_overview` and `product_events` at most 1,000 rows each, marked `truncated` when cut.

Rollback: install the previous release; nothing in the database changed.
