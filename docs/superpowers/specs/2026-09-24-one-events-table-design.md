# One events table, carrying every system attribute

Status: draft
Date: 2026-09-24

## Sequencing

Lands after the locale split (PR #56, migration 019) and owns migration
`020_one_events_table.sql`. It uses `$browser_locale` and `$app_locale`
as #56 defines them.

## Problem

Views and product events come in through the same request, as the same
`{name, attributes}` shape, from the same SDK. They are stored in two raw
tables that have drifted apart:

- **Events drop what they are sent.** A view keeps every reserved key it
  is sent. A product event keeps eight: the actor, `$group_id`,
  `$platform`, `$os`, `$app_version`, `$app_locale` and `$consent`.
  Ingest drops the other seventeen without a warning. The JS SDK already
  sends the whole environment (`$browser`, `$device`, `$os_version`,
  `$browser_locale`, …) on every batch, and the collector throws it away
  on events. That contradicts the rule #52 settled: the server stores
  what it is sent.
- **Views drop what they are sent too.** A view has no attributes column,
  so `page("/x", { plan: "pro" })` and every `attrs()` default are
  discarded on views.
- **Common product questions have no data.** "Signups by device",
  "errors by browser version", "which page the upgrade button converts
  on": none can be answered, because events have no browser, device,
  host or path.
- **Two write paths, two schemas, two sets of indexes** for what is one
  kind of row. Each new key has to be threaded through both, and #56 had
  to do exactly that.
- **Opting out doesn't work for most keys.** `null` in `attrs()` or on
  one call drops a key, but only keys that sit on the event itself
  (location and custom keys). The environment is added to the whole batch
  at flush time and never looks at `attrs()`, so
  `attrs({ $browser: null })` has no effect. There is also no way to
  switch automatic values off wholesale, and no short way to drop a
  family: hiding UTM takes three nulls.

## Decisions

### Storage

1. **One raw table, `events`, holds views and product events.** Migration
   020 rebuilds `events` with every column `views` has, plus `event_name`,
   `attributes` and a `family` column, copies both raw tables in, and drops
   `views`.
   - **Column set:** the union of the two tables today. Each column keeps
     the type and default it has on `views`.
   - **`family`** is `views` or `product`, the prefix of everything the
     row feeds (`v_views_*` / `v_product_*`, `agg_views_*` /
     `agg_product_*`, `views_overview` / `product_events`). A text column
     rather than a 0/1 flag, so a raw row says what it is and a third
     family later needs no new column (chosen). Ingest writes it, from the
     same `viewName()` that routes today; the database holds no list of
     families or view names.
   - **`day`** is a stored generated column, as on `views` today.
2. **What moves.** Raw tables only hold days not yet rolled up (30 days by
   default), so the copy is about a month of rows, not history.
   - Copied views get `event_name` `$page_view` for kind `web` and
     `$screen_view` otherwise, and `attributes` `'{}'`. The original name
     was never stored, and `viewName()` pairs them the same way.
   - Copied views get `family` `views`; copied product events get
     `product`, and empty environment and
     location columns: they never kept those values.
3. **Only the raw table merges; the two families stay.**
   - Every `agg_views_*` and `agg_product_*` table, every `v_*` view name
     and column, and every tool keeps its meaning and numbers.
   - The views family reads `family = 'views'`. The product family
     (`product_events`, `product_attributes`, `v_product_*`) reads
     `family = 'product'` (chosen: views do not count as product events).
   - Identity and retention read both, as they read both tables today.
   - `v_events_flat` holds both, with `family` among its base columns.
   - **Code never reads `events` directly.** Two internal views,
     `raw_views` (`WHERE family = 'views'`) and `raw_product`
     (`WHERE family = 'product'`), are the only read path for Go code and
     for every `v_*` definition. The table itself is only written to,
     deleted from and read by these two views. Views outnumber product
     events by about 20:1, so a product query that forgot the filter
     would silently count every pageview; with no unfiltered read path,
     there is no filter to forget. SQLite flattens a simple filtering view
     into the outer query, so the indexes still apply; the query-plan
     test checks that. `raw_views` and `raw_product` are not `v_*` names,
     so the `query` tool never exposes them.
4. **Indexes**, sized to what the two families filter on:
   - `(project_id, family, day)` serves every live half, replacing
     `idx_views_project_day`;
   - `(project_id, event_name, day)` serves everything keyed by event and
     date: the per-event rollups (`rollupAttrValue` and the daily counts),
     the `v_product_*` live halves, which group by event and day, and a
     `product_events` range for one name. It replaces
     `(project_id, event_name, ts)`: no query orders or ranges events by
     timestamp within a name;
   - `(project_id, actor_id, ts)` and `(project_id, session_id, ts)` carry
     over from the two tables.
   - **All raw SQL filters on `day`**, never on `ts` ranges or
     `substr(ts,1,10)`. Today the product rollup ranges on `ts`, and
     identities and retention compare `substr(ts,1,10)`, which no index can
     serve. The generated column already equals `substr(ts,1,10)`, so the
     answers are unchanged and the indexes apply.
5. **Raw retention and aggregation keep their two settings.**
   - `AggregateViewDay` rolls up and deletes the day's `views` rows;
     `AggregateProductDay` does the same for the `product` rows.
   - `RETENTION_VIEWS_RAW_DAYS` and `RETENTION_PRODUCT_RAW_DAYS` apply by
     flag.
   - Sessionization runs over view rows only, as it does now.
6. **One row type.** `store.View` and `store.ProductEvent` become
   `store.Event`. The pipeline has one queue and the store one
   `WriteEvents`. Consumers keep declaring the slice of the store they use.

### What is stored

7. **Every row stores every reserved key it is sent**, validated exactly
   as views are today:
   - closed vocabularies for `$os`, `$browser` and `$device`, with an
     unrecognised value stored as `other` and a warning;
   - `$referrer` cleaned to a source;
   - display sizes parsed;
   - `$screen` as the path fallback;
   - `country` from the geo lookup, on every row.

   The bot check keeps its scope: web views only. Nothing a client sends
   is dropped any more, except unknown `$` keys.
8. **Views keep their custom attributes** in `attributes` (chosen). The
   views family does not roll them up. They are queryable raw through
   `v_events_flat` within the raw window.

### SDK

9. **The SDK attaches the current location to product events**, mirroring
   what a view of the same page carries:
   - on the web kind, `$host` and `$path`, masked by `maskUrl` in the
     instance's routing mode;
   - on any other kind, `$screen` (the current route);
   - on every kind, the display size.

   **`$referrer` and `$utm_*` are not attached automatically.** They
   describe how the visitor arrived, and they exist only in the landing
   page's URL. On an event they would be filled when it fires on the
   landing page and empty everywhere else, so "signups by campaign" would
   quietly undercount. Attribution means remembering the first referrer
   and UTM tags across pages, which needs consent-gated storage. That is a
   later spec. A client that sends them anyway has them stored.
10. **`autoAttributes: false` switches off every automatic value.** It is
    an `init()` option, code-only (the tag's eight `data-` attributes stay
    eight), and **defaults to `true`**, which is today's behaviour plus
    decision 9.
    - **Off:** OS, browser and device detection (with their versions,
      names and the async platform-version probe), `$browser_locale`,
      display size, `$referrer` and `$utm_*` on views, and the location on
      product events.
    - **Kept:** what a view needs to exist (`$host` / `$path`, or
      `$screen`), `$kind`, the `$platform` default for kind `web`,
      `$consent`, and identity.
    - **Still sent:** values the site sets itself (`appVersion`,
      `appLocale`, `platform`, `attrs()`, per-call attributes).
    - The server then records `unknown` for OS, browser and device, as it
      does for any client that declares nothing.
11. **`null` drops a key wherever it came from.** A null in `attrs()`
    defaults, or in one call's attributes, removes the key from that event
    whether the key is per-event (location, custom) or batch-level
    (environment). The SDK leaves an `attrs()` null's key out of the batch
    attributes. A per-call null on a batch-level key goes on the wire as
    JSON `null` on that event.
12. **A null on a prefix drops the family.** `$x: null` drops `$x` and
    every `$x_*` key: `$utm: null` drops the three UTM keys, `$display:
    null` both sizes, `$os: null` also `$os_version` and `$os_name`, and
    `$browser: null` also `$browser_version` and `$browser_locale`. The
    rule is literal (chosen over a hand-kept family table), so `$app: null`
    drops `$app_version` and `$app_locale`, and `$user: null` / `$group:
    null` reach identity: an identified event sent with `$user: null`
    falls back to its `$install_id` actor. The SDK expands every family
    before sending; the server only ever sees exact keys.

### Server rules

13. **An event-level `null` means "not sent".** `mergeAttributes` deletes
    a key whose event value is `null` instead of laying it over the batch
    default. Today a null custom attribute is stored as `""`. After this
    change it is absent, and a null reserved key reads as undeclared
    (`unknown` for the closed vocabularies). A batch-level `null` is the
    same as leaving the key out.
14. **Product attributes: always-on for the low-cardinality keys, declared
    for the rest.**
    - `product_attributes` always includes `$kind`, `$browser`, `$device`
      and `$browser_locale`, beside today's `$platform`, `$os`,
      `$app_version` and `$app_locale`.
    - A project may **declare** any other stored reserved key in its
      `attributes`, like a custom key, to get its per-value breakdown:
      `$host`, `$path`, `$referrer`, `$utm_source`, `$utm_medium`,
      `$utm_campaign`, `$os_version`, `$browser_version`,
      `$device_model`. The top-N cap and the `(other)` row apply as they do
      to custom keys. This reverses today's "`$` keys must not be declared"
      rule, under which such a declaration silently extracted nothing.
    - `manage` refuses (`ErrInvalid`) a declaration of any other `$` key:
      one that is always on, an identity key, `$session_id`, `$consent`,
      `$os_name`, the display sizes, or an unknown one. The error message
      lists the declarable set.

## Migration 020

1. Create `events_new` with the full column set and `family`.
2. `INSERT` the rows of `views`, then the rows of `events`, as decision 2
   says.
3. Drop both tables, rename `events_new` to `events`, and create the
   indexes.
4. Create `raw_views` and `raw_product`.
5. Recreate every view that read either table (`v_views_*`,
   `v_identity_daily`, `v_product_daily`, `v_product_totals`,
   `v_product_attrs`) against `raw_views` or `raw_product` (identity reads
   both). The SQL is otherwise today's.
6. `v_events_flat` is rebuilt by Go at boot, as today, from the new base
   columns.
7. `v_product_attrs` also gains the four new system arms. Its declared arm
   resolves a `$` key to its column through a `CASE` on `attr_key` rather
   than `json_extract`. `systemDims` and the declared-key rollup take the
   same mapping, so the live half and the rollup cannot drift.

The migration runs in one transaction, like every migration. There is no
down migration.

## Stopping rule

The merge must not slow the live halves down. Suppose the bench (see
Tests) shows the views live halves or the `v_product_attrs` live half
noticeably slower than on two tables, and index changes do not win it
back. Then the implementation falls back to two raw tables with identical
columns, built from one Go column list and one row builder. That fallback
keeps every other decision in this spec (storage of every key, SDK,
nulls, `autoAttributes`, product attributes) unchanged. The PR says which
way it went and shows the numbers.

## Docs

- `docs/twillingate.md`:
  - the reserved-key table gets views/events columns and a "sent
    automatically" column;
  - "resolved and dropped on a product event" goes;
  - the null rules gain the family rule and the batch-level case;
  - `init()` gains `autoAttributes`;
  - `product_attributes` lists the eight always-on keys and the declarable
    set;
  - `v_events_flat` is described as holding views too.
- `schemaViews` and the `product_attributes` tool description change to
  match. `docs_sync_test` binds `autoAttributes`.
- `deploy/UPGRADES.md` gets a 020 section:
  - the pre-check query `SELECT id, attributes FROM projects WHERE
    attributes LIKE '%"$%'`. A hit is a `$` declaration that extracted
    nothing until now: it either starts working or is refused on the next
    edit;
  - `v_events_flat` now returns view rows, so saved SQL over it adds
    `WHERE family = 'product'` to keep today's answer;
  - pages on the cached old SDK send no location on events for up to a
    day;
  - raw rows grow; the migration copies the raw window, seconds on a
    month of traffic.

## Tests

- **Migration 020, the load-bearing test.** Build a database at 19 with
  views and events across raw and rolled-up days, snapshot every `v_*`
  view, migrate through 20, and require every snapshot unchanged (except
  `v_events_flat`, which is checked for the added view rows). Also check
  that copied rows carry the derived `event_name` and `family`.
- **Query plans:** `TestViewsLiveHalvesUseTheDayIndex` moves to the new
  index through `raw_views`. The product live halves and the per-event
  rollup get the same check through `raw_product`, and must search
  `(project_id, event_name, day)` with the day bound.
- **No unfiltered reads:** a test scans the Go sources and every `v_*`
  definition in `sqlite_schema` and fails on any read of `events` outside
  `raw_views` and `raw_product`.
- **Ingest:**
  - a product event keeps each reserved key, with the same normalisation
    and warnings as a view;
  - a view keeps its custom attributes;
  - an event-level `null` removes a batch default;
  - a null custom key is absent, not `""`.
- **Rollups:**
  - the live half and the rollup of each family ignore the other
    family's rows;
  - `v_product_attrs` stays identical across the boundary for the four
    new always-on keys and for a declared `$path` past the cap;
  - an undeclared `$path` produces no rows;
  - raw retention deletes each family by its own setting.
- **Manage:** a declarable `$` key is accepted; each class of
  non-declarable one is refused with `ErrInvalid`.
- **SDK:**
  - `track()` carries `$host` / `$path` on web and `$screen` on an app
    kind;
  - `autoAttributes: false` removes every listed key and keeps the listed
    ones;
  - `attrs({ $browser: null })` removes `$browser`, `$browser_version`
    and `$browser_locale` from the batch;
  - a per-call `$utm: null` drops all three UTM keys from a view;
  - a per-call `$os: null` sends `"$os": null` on that event only.
- **Bench:**
  - the views live halves and the `v_product_attrs` live half at 150k
    raw rows, before and after;
  - the merged table is larger than each old one, and the four new
    product arms each scan the raw window, so record both costs.

## Out of scope

- First-touch referrer and UTM attribution for product events.
- Rolling up views' custom attributes.
- A `$country` key in `product_attributes`. Country is stored and
  queryable raw; a rollup can follow if asked for.
- Evidence dashboard changes.
