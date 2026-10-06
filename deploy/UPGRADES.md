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
an older binary ignores the new columns and table, so every group hidden
from the sidebar, a built-in one included, shows there again under it.
