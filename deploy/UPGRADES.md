# Schema upgrades

One section per migration that needs more than `docker compose pull` or a
re-run of the installer: what to check before the upgrade, and what changes
on the day. Newest last. Snapshot the database first in every case.

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

Then stop the service, copy the database (or take a Litestream
snapshot), run the installer, and check `journalctl` for migration 014.
`list_projects` (or `twillingate project list`) shows the new ids: they
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

Then stop the service, copy the database (or take a Litestream snapshot),
run the installer, and check `journalctl` for migration 015.

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
