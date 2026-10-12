# Events encoding: ids and dictionaries in the raw table

Status: draft
Date: 2026-10-11

## Problem

- **A raw row is mostly repeated text.** Prod holds 49,991 raw rows (30
  days, 2026-09-12 to 2026-10-12) at about 491 bytes each in the benchmark's
  prod-shaped data. Most of it is the same strings on every row:
  - attribute keys, written in full on every row (`"access_state"`,
    `"months_since_signup"`): 3.3 MB of prod's 9 MB of attribute JSON;
  - attribute values, which repeat even more: 436k key/value pairs on prod
    hold 1,305 distinct values;
  - 36-character UUIDs and 32-character hashes stored as text;
  - 20-character ISO timestamps, twice per row;
  - host, path, browser, OS, country and a dozen more short strings stored
    whole on every row.
- **Disk and dashboards follow the raw window.** The hosted plans keep 7 to
  60 days raw, and the raw window on prod is being raised. Long-range widgets
  scan raw rows, so a smaller row is also a faster scan.
- **Custom SQL can read every table**, `ingest_keys` (key hashes),
  `audit_log` and `widget_shares` included. An agent reading the database
  through `query` to find out what is going on is useful. A widget doing the
  same is not: a dashboard should only depend on the views.

Benchmark (1.32M synthetic rows shaped like prod, 100k events a month for
400 days, medians of 3 runs, Python's SQLite): today 491 B/row (649 MB,
134 MB under zstd); encoded 211 B/row (279 MB, 90 MB under zstd). Every
query returned identical rows on both layouts. The benchmark numbered
values across all keys; this spec numbers them per key (D6), which writes
the same or fewer digits.

| Query (ms) | Today | Encoded, widget SQL unchanged | Encoded, on ids |
| --- | --- | --- | --- |
| Visitors per day, 30 days | 26 | 24 | 18 |
| Top 20 paths, 30 days | 42 | 46 | 29 |
| Top 20 paths, 400 days | 1075 | 755 | 472 |
| Events by name per day, 400 days | 1346 | 1266 | 724 |
| One attribute's values, 400 days | 1676 | 7673 (`json_extract` on `raw_product`) | 1241 |
| ↳ through an `attr_*` column | | 1499 | |
| Views of one path, 400 days | 99 | 225 (`WHERE path = '/x'` on `raw_views`) | 63 |
| Last day's rows, all columns | 2 | 43 | 43 |

"On ids" is what the `v_*` live halves and the rollups do (D12, D13); most
system widgets read `v_*`. The two slow rows are custom SQL only.

## Decisions

### Storage

- **D1. `events` is rebuilt with encoded columns; its key keeps its shape**,
  `(family, project_id, day, id)`, `WITHOUT ROWID`.

  | Column(s) | Today | Encoded |
  | --- | --- | --- |
  | `family` | `'views'`, `'product'`, `'measures'` | `1`, `2`, `3` |
  | `project_id` | integer | unchanged |
  | `day` | `'2026-10-11'` | unchanged (D5) |
  | `id` | UUID text | 16-byte blob (D3) |
  | `ts`, `received_at` | `'2026-10-11T21:58:03Z'` | integer Unix seconds (D4) |
  | `event_name`, `measure`, `kind`, `actor_kind`, `host`, `path`, `referrer_source`, `utm_source`, `utm_medium`, `utm_campaign`, `platform`, `os`, `os_version`, `os_name`, `browser`, `browser_version`, `browser_locale`, `app_version`, `app_locale`, `device`, `device_model`, `country` | text | integer id into `strings` (D2) |
  | `actor_id`, `user_id`, `group_id`, `session_id` | text | tagged blob when it converts back exactly, text otherwise (D3) |
  | `attributes` | `{"access_state":"full_access"}` | `{"7":88}` (D6) |
  | `display_width`, `display_height`, `consent`, `value`, `sample_rate`, `bucket` | | unchanged |

  As everywhere else there are no `CHECK` constraints: ingest validates.
- **D2. One `strings` dictionary for every text column.**

  ```sql
  CREATE TABLE strings (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      value         TEXT NOT NULL UNIQUE,
      last_seen_day TEXT NOT NULL
  );
  ```

  - **Shared by every column and every project.** One cache and one
    cleanup; a value like `/` or `(direct)` is stored once.
  - **`''` is id 0, which has no row.** SQLite stores the integer 0 in zero
    bytes, and decoding reads a missing row as `''`. Every column that
    defaults to `''` today costs nothing when empty.
  - **`AUTOINCREMENT`, so an id is never reused** (D7).
  - The migration inserts the most frequent values first, so the common
    ones get ids below 128 and take one byte.
- **D3. Ids are blobs when they convert back exactly.**
  - **`id` is always a UUID** (ingest refuses any other id; the server mints
    v7 when a client sends none; the SDK sends v7 since #174). It is stored
    as its 16 bytes, untagged. A stored id that does not parse (none
    expected) stays text, which the column's dynamic typing allows.
  - **`actor_id`, `user_id`, `group_id`, `session_id` have no enforced
    format.** A value becomes a blob only when the text it decodes to is
    byte-for-byte the text that was sent:
    - `x'01'` followed by 16 bytes: a lowercase canonical UUID
      (`8-4-4-4-12`), for install ids and session ids;
    - `x'02'` followed by the bytes: lowercase hex of even length, for
      hashes like econumo's 32-character actor ids and 12-character
      group ids;
    - anything else stays text: uppercase hex, odd length, emails, `''`.

    The tag costs one byte and keeps the two forms apart: a 15-byte hex
    value and a UUID would otherwise both be 16 bytes.
  - **Decoding is plain SQL**, `lower(hex(substr(x, 2)))` plus dashes for a
    UUID, so the views also work in the `sqlite3` shell and on a restored
    backup. No Go-registered function is involved.
- **D4. `ts` and `received_at` are integer Unix seconds.** Both are
  second-precision today (`2006-01-02T15:04:05Z`). Decoding is
  `strftime('%Y-%m-%dT%H:%M:%SZ', ts, 'unixepoch')`. A `received_at` of
  `''` on old rows becomes `0` and decodes back to `''`. The session-splitting
  live halves read the integer directly instead of `strftime('%s', ts)`.
- **D5. `day` stays text.** The benchmark's first round stored it as an
  integer and decoded it in the views: `day BETWEEN :from AND :to` then
  could not use the key, and 30-day widgets got 4 to 100 times slower. It
  costs about 7 bytes per row.
- **D6. Attributes are an object of key ids to value numbers**, `{"7":88}`.
  - **The key is the catalog row's id** (D8), written as a JSON object key
    (always a string in JSON).
  - **The value is the value's number within that key**, from
    `attribute_values`:

    ```sql
    CREATE TABLE attribute_values (
        key_id        INTEGER NOT NULL REFERENCES project_attributes(id) ON DELETE CASCADE,
        n             INTEGER NOT NULL,
        value         TEXT    NOT NULL,
        last_seen_day TEXT    NOT NULL,
        PRIMARY KEY (key_id, n),
        UNIQUE (key_id, value)
    ) WITHOUT ROWID;
    ```

  - **Numbers are per key**, so they stay short: 1 to 4 digits on prod,
    where the busiest key has 635 distinct values. JSON stores numbers as
    text, so every digit is a byte.
  - **`n` is never reused within a key**: it comes from a counter on the
    catalog row (`values_next`, D8), not from `MAX(n) + 1`.
  - **No cap.** The dictionary holds the distinct values the rows in the raw
    window carry (D7). A value unique to each event (an order id, a
    timestamp) costs about twice what it would inline, for as long as its
    row exists. On prod the whole dictionary is about 40 KB.
  - **A number is never ambiguous.** Ingest stores every attribute value as
    a string (all 436k values on prod are text), so a JSON number in
    `attributes` is always a value number.
  - **The order of keys in the decoded JSON is not part of the contract.**
    Decoding follows the stored order (key ids), not today's alphabetical
    order. No query should depend on it.
- **D7. Every dictionary is cleaned by one rule: `last_seen_day` against
  the oldest raw day.** This covers `strings`, the catalog (D8) and
  `attribute_values`.
  1. **Ingest raises it.** Writing a row raises each id it uses to
     `max(last_seen_day, row's day)`. A day-keyed cache, the same pattern
     as `receivedSeen` for `usage_attributes`, makes it one write per id
     per day.
  2. **The daily pass deletes below the cutoff**, after the rollup has
     deleted the expired raw days:
     - `strings`: rows whose `last_seen_day` is before the oldest `day`
       in `events`;
     - the catalog and `attribute_values`: the same against the project's
       own oldest `day` (D8 for the catalog's other conditions).

     Once `events_kept` (#173) exists, the oldest day is taken across both
     tables.
  3. **It cannot delete an id a row still uses.** A raw row exists only
     while its day is at or after the cutoff, and every id it uses has a
     `last_seen_day` at or after that day. There is no JSON scan and no
     column scan.
  4. **No lock between ingest and the cleanup.** The touch in step 1 is
     `UPDATE … SET last_seen_day = max(last_seen_day, ?) WHERE id = ? AND
     value = ?` (`key_id, n, value` for values; `id, project_id, key` for
     the catalog), in the transaction that writes the row.
     - If it changes no row, the cleanup deleted the id after ingest cached
       it. Ingest drops the id from the cache and resolves the string
       again, which inserts it under a new id. A stale id is never written.
     - SQLite runs one writer at a time, so the cleanup either ran before
       (the touch finds nothing) or runs after (the touch set
       `last_seen_day` to a day the cleanup's cutoff, computed in its own
       transaction, does not pass).
     - An id cached as touched for a day needs no touch for further rows of
       that day: its rows of that day exist until the day is rolled up, and
       rows of a rolled-up day are not written again (ingest clamps `ts`
       into the raw window).
     - Ids are never reused (`AUTOINCREMENT`, and `values_next` only grows),
       and the `value =` condition guards the touch even if one were. A key
       pruned and seen again gets a new catalog id and numbers its values
       from 1.
  5. Rewrites that move rows to other ids (rename and merge, D9) raise the
     targets' `last_seen_day` to the newest day they rewrote.
- **D8. The catalog becomes the key dictionary.** `project_attributes` is
  rebuilt as:

  ```sql
  CREATE TABLE project_attributes (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      project_id    INTEGER NOT NULL REFERENCES projects(id),
      key           TEXT    NOT NULL,
      breakdown     INTEGER NOT NULL DEFAULT 0 CHECK (breakdown IN (0, 1)),
      created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
      last_seen_day TEXT    NOT NULL DEFAULT '',
      values_next   INTEGER NOT NULL DEFAULT 1,
      UNIQUE (project_id, key)
  );
  ```

  - **Ingest already writes a key's catalog row** in the transaction that
    writes the event (`writeReceived`). That step moves ahead of the row
    insert so the id is known, and it raises `last_seen_day` (D7).
  - **View keys are recorded too.** Views carry custom attributes (econumo's
    SDK defaults ride on every pageview: 104k pairs on prod), and today the
    catalog only learns product and measure keys. Every family's keys now
    get a row.
  - **The prune keeps a row while any raw row may carry it.** A row is
    dropped only when it is no breakdown, no `usage_attributes` row holds it
    (today's rule, D3 of the attributes spec), and its `last_seen_day` is
    before the project's oldest raw day (D7). Its `attribute_values` go with
    it (`ON DELETE CASCADE`).
  - **Listing is unchanged.** `list_attributes` and `keys_total` read
    `usage_attributes` and the rules, which count product and measure keys
    only, so a key seen only on views is not listed (unless it is a
    breakdown). A test pins it.
  - **`$` keys keep their catalog rows** (`usage_attributes` records them
    from the reserved columns), though no stored JSON uses their ids:
    reserved keys are columns.

### Rules on encoded rows

- **D9. Rename, merge and block keep their current behaviour.**
  - **An attribute rename still rewrites raw rows**, from the old key's id
    to the new one's, in product and measure rows only. Keeping the id and
    renaming the catalog row would be cheaper, but it would rename the key
    on view rows as well, which keep their attributes as sent. The old
    key's catalog row is no longer deleted on rename: view rows may still
    carry it, and it ages out by D8's prune. `list_attributes` already
    hides a key renamed away by reading the rules, not the catalog.
  - **A merge** rewrites each `(from, n)` pair to `(to, n')`. `n'` is the
    same value's number under `to`, added to `attribute_values` when
    missing. A row carrying both keys keeps `to`'s value, as today.
  - **An event rename or merge** sets `event_name` to the target name's
    `strings` id.
  - **Block** strips the key at ingest, as today.
  - **Form deletion** deletes by encoded id.

### Reading

- **D10. `raw_views`, `raw_product`, `raw_measures` decode everything** and
  keep today's columns, names and text values:
  - `family` back to its name;
  - D3's ids back to text;
  - `strings` ids back to text;
  - `ts` and `received_at` back to ISO text;
  - `attributes` rebuilt as `{"access_state":"full_access"}`:

  ```sql
  (SELECT json_group_object(k.key, v.value)
     FROM json_each(e.attributes) j
     JOIN project_attributes k ON k.id = CAST(j.key AS INTEGER)
     JOIN attribute_values v ON v.key_id = k.id AND v.n = j.value) AS attributes
  ```

  A widget written against them works unchanged. The two slow paths in the
  benchmark are `json_extract` on the decoded `attributes` and a text filter
  on a decoded column; the docs steer attribute reads to `attr_*` (D12).
- **D11. A filter on `project_id` and `day` still reaches the raw rows**
  through every view, as since #119: both are stored as they are filtered.
- **D12. `v_events_flat` decodes like `raw_*`.** Each `attr_*` column
  reads one value directly:

  ```sql
  (SELECT value FROM attribute_values
    WHERE key_id = 7 AND n = json_extract(e.attributes, '$."7"')) AS attr_plan
  ```

  The process still rebuilds the view from the catalog, as today. It
  remains process-owned (`processOwned`), so the migration never names it.
- **D13. Every `v_*` live half reads `events` on ids**: it filters and
  groups by ids and decodes only the grouped rows. These are
  `v_identity_daily`, `v_product_daily`, `v_product_totals`,
  `v_product_attrs`, `v_measures_daily`, `v_measures_attrs` and the 14
  `v_views_*`. Their agg halves are unchanged. `v_product_attrs` and
  `v_measures_attrs` group by `(key id, value number)` and read the catalog
  by id. `v_retention` has no live half and does not change.
- **D14. The server's own readers move to ids.** These are the rollups
  (`aggregate_views.go`, `aggregate_product.go`, `aggregate_measures.go`),
  the attribute usage counts (`usage_attributes.go`), `usage_history.go`,
  the identity and retention passes, the rules and forms.
  - **Derived tables keep storing text**: every `agg_*` table, `actors`,
    `identities`, `usage_*`. The rollups decode as they write. Widgets
    reading `agg_*` directly do not change.
  - **The console's own reads may stay on `raw_*`** (`ops_usage.go`,
    `ops_activity.go`, `rawcount.go`), since they read `project_id`, `day`
    and `received_at`, which decode cheaply.

### Access

- **D15. Widget SQL reads only `raw_*`, `v_*` and `agg_*`; `query` keeps
  reading everything.**
  - **`query` is unchanged**: it refuses `meta`, SQLite's internals and
    `submissions`, as today. An agent can still read every table, the
    encoded `events` included, to find out what is going on. The docs give
    it the encoding (D1 to D8) to decode rows by hand.
  - **Widget SQL gets its own read-only handle on which only the allowed
    names exist.** Each connection opens an empty in-memory database,
    attaches the database file read-only under a private schema name, and
    creates a temporary view for each `raw_*`, `v_*` and `agg_*` object:
    `CREATE TEMP VIEW raw_views AS SELECT * FROM <private>.raw_views`.
    - A widget reading `events`, `identities` or `projects` gets "no such
      table". `readsql` refuses the private schema name anywhere (bare,
      quoted, bracketed, qualified), as it refuses `meta`.
    - The real views resolve inside their own schema, and `SELECT *`
      follows column changes, `v_events_flat`'s rebuilds included.
    - A word list cannot do this: `events` and `actors` are also column
      names of `v_identity_daily`.
    - **Fallback** if filters stop reaching the raw rows through the
      extra view layer (checked in the plan): a tokenizer rule on table
      positions only (after `FROM`, `JOIN`, or a comma in a `FROM` list).
  - **This covers every path that runs widget SQL**: adding, updating and
    previewing a widget, and running a stored one. The dev handle
    (`reporting_dev.go`) follows. Trusted Go SQL that reporting runs
    itself, such as `sampleProject` (`projects` and `events`), uses a
    second, full handle.
  - **`v_identities`** exposes `identities` as it is. The two system
    widgets that join `identities` (top users, top groups) read it instead.
  - **Breaking:** a user widget that reads any other table fails when it
    runs, with the "no such table" error. The release is `feat!`.

### Writing

- **D16. Ingest encodes inside the write transaction it already uses.**
  For each row:
  1. Parse the UUIDs and tag the other ids (D3).
  2. Convert `ts` and `received_at` (D4).
  3. Resolve each string through the cache, inserting into `strings` when
     new and raising `last_seen_day` once per id per day.
  4. Apply rename and block rules, then map each attribute key to its
     catalog id and each value to its number, the same way.

  The event rename that `write.go` resolves in SQL today
  (`COALESCE((SELECT to_name FROM rules …))`) resolves in Go from the same
  rules before interning. A retried batch is still ignored: the same UUID
  gives the same blob, so the key matches.

### Migration

- **D17. One migration** (042, or 041 if it lands before #173):
  1. **Pre-checks:**
     - every `attributes` is valid JSON whose values are all strings;
     - every key a raw row carries has a catalog row, adding the missing
       ones.

     A failed check stops the migration with the count, before anything
     changes.
  2. **Rebuild `project_attributes`** with `id`, `last_seen_day`
     (the newest day a raw row carries the key) and `values_next`.
  3. **Create `strings`** from the distinct values of D1's columns, most
     frequent first, and **`attribute_values`** from the distinct values of
     each key, most frequent first.
  4. **Rewrite `events` into the new shape** through a Go migration step:
     the tag rules and the per-row attribute rewrite are clearer and
     testable in Go. The step goes into `sqlcheck`'s `migrationSteps` list.
  5. **Drop and recreate every view** reading `events`, in D10 and D13's
     definitions, and add `v_identities`.
  6. Irreversible, like 012 and 020.

  `deploy/UPGRADES.md` gets the entry:
  - back up first;
  - the rebuild needs about twice the size of `events` free on disk while it
    runs;
  - it takes seconds at prod's volume and about a minute at 1.3M rows;
  - the file shrinks as the incremental vacuum reclaims free pages;
  - the two breaking changes: widget SQL limited to `raw_*`, `v_*` and
    `agg_*` (D15), and `events` encoded for anyone reading it through
    `query`.
- **D18. Kept events and profiles do not depend on this.** Derived tables
  keep text (D14), so the profiles work (identities registry, user and group
  attributes) can land before or after. `events_kept` (#173) is `events`
  column for column: whichever of the two lands second converts the other.
  - If #173 lands first, this migration rewrites `events_kept` with
    `events`, and D7's cutoff covers both tables.
  - If this lands first, #173 creates `events_kept` in the encoded shape,
    and its rollup copy needs no decoding.

### Not done

- **Value caps.** Rejected for now: the cleanup (D7) bounds the dictionary
  by the raw window. A per-key cap with an inline fallback stays possible
  later, since values are always strings and a number is never ambiguous.
- **An integer `day`** (D5).
- **Go-registered decode functions.** They would shorten the views but break
  them outside the process (D3).
- **Rename without a rewrite** (D9): it would change view rows' keys.
- **Filesystem compression.** About 3× on top of this (4.8× on today's
  file). It is a deployment note, not code; a line in `docs/deployment.md`
  can follow separately.

## Documentation

- `docs/twillingate.md`:
  - **the raw table section** (around "All three families are stored in one
    raw table"): `events` is encoded; read `raw_*` and `v_events_flat`, or
    decode by hand with a table of D1 to D8 (`strings`, the catalog ids,
    `attribute_values`, the blob tags, integer `ts`);
  - **attributes**: read declared keys through `attr_*` columns, which are
    faster than `json_extract` on `raw_product`;
  - **widget SQL** reads only `raw_*`, `v_*` and `agg_*`, while `query`
    reads every table but `meta`, SQLite's internals and `submissions`;
  - `v_identities` in the views table, and in `schemaViews`
    (`internal/api/resources.go`).
- `docs/reporting.md`: the widget SQL rule, `v_identities` instead of
  `identities`, and `attr_*` columns in the examples that read attributes.
- `deploy/UPGRADES.md`: D17.
- The `query` tool's description still says what it says today; the widget
  tools' SQL descriptions say `raw_*`, `v_*`, `agg_*`.

## Testing

- **Round trip.** On a seeded database (the `storetest` template plus rows
  covering every case), every `raw_*` row read before the migration equals
  the row read after. The cases:
  - ids that are UUIDs, lowercase hex, uppercase hex, odd-length hex and
    neither;
  - `''` in every encoded column;
  - every family;
  - `received_at = ''`.

  `attributes` is compared as parsed JSON (D6: key order is not
  contract).
- **View parity.** Every `v_*` view gives identical rows before and after
  on the same seed, across a range that spans rolled-up and raw days.
  `TestSystemDashboards` runs on the migrated database.
- **Ingest.** A batch through the pipeline reads back through `raw_*` as
  sent; a retried batch is ignored; a new string, key or value gets an id
  and its `last_seen_day`.
- **Cleanup (D7).**
  - The daily pass deletes only ids below the cutoff.
  - An id still in the cache after the cleanup deleted its row is never
    written: the next row's touch finds nothing, and the string gets a new
    id.
  - A key pruned and seen again gets a new catalog id; its values start
    at 1.
  - A catalog key carried only by view rows survives the prune while those
    rows exist.
  - Deleting a key cascades its values.
- **Rules.**
  - A rename rewrites product and measure rows and leaves view rows on the
    old key.
  - A merge maps values to the target's numbers and keeps the target's
    value on rows carrying both.
  - `attr_*` columns follow both.
  - `list_attributes` is unchanged, a view-only key unlisted.
- **Widget access (D15).**
  - Refused: `events`, `identities`, `projects`, and the private schema
    name in every form.
  - Allowed: `raw_*`, `v_*`, `agg_*`, CTE names, columns named `events`
    and `actors`.
  - `EXPLAIN QUERY PLAN` shows `project_id`/`day` reaching `events`'
    key through the temporary views.
  - `query` still reads `events`.
- **Migration.**
  - The pre-checks stop on invalid JSON and on a non-string value.
  - A missing catalog key is added.
  - The migration test pins its ceiling, as the others do.
- **Size.** A test on the seed asserts the encoded `events` takes less than
  half today's bytes per row (`dbstat`), so a regression in the encoding
  shows.
- `sqlcheck` prepares the new SQL. The race suite still copies a migrated
  database from `storetest` and never migrates per test.
