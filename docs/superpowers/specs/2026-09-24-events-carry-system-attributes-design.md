# Product events carry every system attribute

Status: draft
Date: 2026-09-24

## Sequencing

Lands after the locale split (PR #56, migration 019) and owns migration
`020_event_environment.sql`. It uses `$browser_locale` and `$app_locale`
as #56 defines them.

## Problem

A view keeps every reserved key it is sent. A product event keeps eight:
the actor, `$group_id`, `$platform`, `$os`, `$app_version`, `$app_locale`
and `$consent`. Ingest drops the other seventeen without a warning. That
causes three problems:

- **The server throws away what it is sent.** The JS SDK already sends the
  whole environment (`$browser`, `$device`, `$os_version`,
  `$browser_locale`, …) on every batch, product events included, and the
  collector drops it. That contradicts the rule #52 settled: the server
  stores what it is sent.
- **Common product questions have no data.** "Signups by device",
  "errors by browser version", "which page the upgrade button converts
  on": none can be answered, because events have no browser, device,
  host or path.
- **Opting out doesn't work for most keys.** `null` in `attrs()` or on
  one call drops a key, but only keys that sit on the event itself
  (location and custom keys). The environment is added to the whole batch
  at flush time and never looks at `attrs()`, so
  `attrs({ $browser: null })` has no effect. There is also no way to
  switch automatic values off wholesale, and no short way to drop a
  family: hiding UTM takes three nulls.

## Decisions

1. **Events store every reserved key views store.** `events` gains the
   views' environment and location columns, plus `country` from the same
   geo lookup. Every column is validated at ingest exactly as on views:
   closed vocabularies for `$os`, `$browser` and `$device` (an
   unrecognised value is stored as `other`, with a warning); `$referrer`
   cleaned to a source; display sizes parsed; `$screen` as the path
   fallback. Nothing a client sends is dropped any more, except unknown
   `$` keys.
2. **The SDK attaches the current location to product events**: `$host`
   and `$path` (masked by `maskUrl`, in the instance's routing mode) on
   the web kind, and `$screen` (the current route) on any other kind, plus
   display size. This mirrors what a view of the same page carries.
   **`$referrer` and `$utm_*` are not attached automatically.** They
   describe how the visitor arrived, and they exist only in the landing
   page's URL. On an event they would be filled when it fires on the
   landing page and empty everywhere else, so "signups by campaign" would
   quietly undercount. Attribution means remembering the first referrer
   and UTM tags across pages, which needs consent-gated storage. That is a
   later spec. A client that sends them anyway has them stored.
3. **`autoAttributes: false` switches off every automatic value.** It is
   an `init()` option, code-only (the tag's eight `data-` attributes stay
   eight), and defaults to `true`.
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
4. **`null` drops a key wherever it came from.** A null in `attrs()`
   defaults, or in one call's attributes, removes the key from that event
   whether the key is per-event (location, custom) or batch-level
   (environment). The SDK leaves an `attrs()` null's key out of the batch
   attributes. A per-call null on a batch-level key goes on the wire as
   JSON `null` on that event.
5. **A null on a prefix drops the family.** `$x: null` drops `$x` and
   every `$x_*` key: `$utm: null` drops the three UTM keys, `$display:
   null` both sizes, `$os: null` also `$os_version` and `$os_name`, and
   `$browser: null` also `$browser_version` and `$browser_locale`. The rule
   is literal (chosen over a hand-kept family table), so `$app: null`
   drops `$app_version` and `$app_locale`, and `$user: null` / `$group:
   null` reach identity: an identified event sent with `$user: null` falls
   back to its `$install_id` actor. The SDK expands every family before
   sending; the server only ever sees exact keys.
6. **The server treats an event-level `null` as "not sent".**
   `mergeAttributes` deletes a key whose event value is `null` instead of
   laying it over the batch default. Today a null custom attribute is
   stored as `""`. After this change it is absent, and a null reserved key
   reads as undeclared (`unknown` for the closed vocabularies). A batch-level
   `null` is the same as leaving the key out.
7. **Rollups: always-on for the low-cardinality keys, declared for the
   rest.**
   - `product_attributes` always includes `$kind`, `$browser`, `$device`
     and `$browser_locale`, beside today's `$platform`, `$os`,
     `$app_version` and `$app_locale`.
   - A project may **declare** any other stored reserved key in its
     `attributes`, like a custom key, to get its per-value breakdown:
     `$host`, `$path`, `$referrer`, `$utm_source`, `$utm_medium`,
     `$utm_campaign`, `$os_version`, `$browser_version`, `$device_model`.
     The top-N cap and the `(other)` row apply as they do to custom keys.
     This reverses today's "`$` keys must not be declared" rule, under which
     such a declaration silently extracted nothing.
   - `manage` refuses (`ErrInvalid`) a declaration of any other `$` key:
     one that is always on, an identity key, `$session_id`, `$consent`,
     `$os_name`, the display sizes, or an unknown one. The error message
     lists the declarable set.
8. **Every new column is in the flat events view**, so it can be queried
   raw within `RETENTION_PRODUCT_RAW_DAYS` whether or not it is rolled up.

## Server

- **Migration 020.** `ALTER TABLE events ADD COLUMN` for `kind`,
  `session_id`, `os_version`, `os_name`, `browser`, `browser_version`,
  `browser_locale`, `device`, `device_model`, `host`, `path`,
  `referrer_source`, `utm_source`, `utm_medium`, `utm_campaign`,
  `display_width`, `display_height` and `country`. Each is
  `NOT NULL DEFAULT ''` (0 for the sizes), like the matching `views`
  column. There is no backfill: pre-020 rows never kept these values.
  Rows stored before 020 read empty `browser` and `device`, not
  `unknown`, which is what they are.
- **`v_product_attrs` is recreated.** It gets four more system arms. The
  declared arm resolves a `$` key to its column through a `CASE` on
  `attr_key` rather than `json_extract`. `systemDims` gains the same four
  keys, and the declared-key rollup takes the same column mapping, so the
  live half and the rollup cannot drift.
- **`ProductEvent` gains the fields.** `handleEvents` fills them through
  the code path views already use, so the two cannot drift. The comment
  "the rest are views-only and are resolved and dropped on a product
  event" goes.
- **`mergeAttributes`**: an event-level `nil` deletes the key (decision 6).

## SDK

- `track()` merges the current location and display size under the call's
  attributes, as `page()` does, unless `autoAttributes` is false. A
  per-call `$path` (or `$host`, `$screen`) wins, as it does for views.
- `batchAttributes()` skips detection and `$browser_locale` when
  `autoAttributes` is false, and skips any key the `attrs()` defaults null
  out, family expansion included.
- The final null pass in `emit()` expands families. It keeps a
  batch-level key's null as JSON `null` on the event, and drops any other
  null key as today.
- `InitOptions.autoAttributes?: boolean`.

## Docs

- `docs/twillingate.md`:
  - the reserved-key table gets views/events columns and a "sent
    automatically" column;
  - the Declaring-the-environment section loses "resolved and dropped on
    a product event";
  - the null rules gain the family rule and the batch-level case;
  - `init()` gains `autoAttributes`;
  - `product_attributes` lists the eight always-on keys and the declarable
    set.
- `schemaViews` and the `product_attributes` tool description change to
  match. `docs_sync_test` binds `autoAttributes`.
- `deploy/UPGRADES.md` gets a 020 section:
  - the pre-check query `SELECT id, attributes FROM projects WHERE
    attributes LIKE '%"$%'`. A hit is a `$` declaration that extracted
    nothing until now: it either starts working or is refused on the next
    edit;
  - pages on the cached old SDK send no location on events for up to a
    day, so the day's path breakdown is thin;
  - product raw rows grow.

## Tests

- **Migration 020** pinned at 20: the columns exist, and a pre-020 event
  reads empty environment columns.
- **Ingest:** a product event keeps each reserved key, with the same
  normalisation and warnings as a view; an event-level `null` removes a
  batch default; a null custom key is absent, not `""`.
- **Rollup:**
  - the live half and the rollup of `v_product_attrs` stay identical
    across the boundary for the four new always-on keys and for a declared
    `$path` past the cap;
  - an undeclared `$path` produces no rows.
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
- **Bench:** `v_product_attrs` live half at 150k raw events before and
  after. The four new arms each scan the raw window, so record the cost.

## Out of scope

- First-touch referrer and UTM attribution for product events.
- A `$country` key in `product_attributes`. Country is stored and
  queryable raw; a rollup can follow if asked for.
- Evidence dashboard changes.
- Merging `views` and `events` into one table.
