# Performance measures and Web Vitals

Status: draft
Date: 2026-09-30

## Sequencing

Three PRs, in this order, each releasable on its own:

1. `feat(config)!: keep one retention pair for all events`
2. `perf(store): cluster events by family, project and day`
3. `feat: record performance measures and Web Vitals`

PR 2 owns migration 023 and PR 3 owns 024. 022 is left to #91 (dashboard
tabs), whose branch already carries `022_dashboard_groups.sql`: two files
with one number would share a version, and the migrator would silently skip
the second.

## Problem

- **Nothing in twillingate measures how fast anything is.** A row in
  `events` is a view or a product event, and neither carries a number. A
  site owner cannot see how fast pages load for real visitors, and a
  backend has nowhere to send how long a request took.
- **A number in `attributes` is not enough.** `track("checkout",
  { ms: 340 })` stores the number as JSON inside a product event. Nothing
  aggregates it, so after the raw window it is gone. Before that, a
  percentile means sorting raw JSON values in hand-written SQL. It also
  puts timings into `product`, where they swamp event counts and top-event
  lists.
- **Retention is set per family, and ingest clamps against the wrong
  window.** `MaxEventAge()` (`internal/config/config.go:322`) follows the
  views raw window only, so with `RETENTION_PRODUCT_RAW_DAYS` below
  `RETENTION_VIEWS_RAW_DAYS` a late product event lands in a day already
  rolled up and deleted, and is silently lost at the next prune. A third
  family with its own window would make this worse.
- **The raw table stores families interleaved.** `events` is a rowid table
  in arrival order. A views query reads index entries for its family but
  fetches rows from pages shared with other families. Measures would make
  that the common case: Web Vitals add up to five rows per page view.

## Decisions

### What a measure is

1. **A measure is an event in a third family, `measures`, with a numeric
   `value`.** The mental model is a name, a value and a time. The family is
   declared by the event and never inferred: a product event cannot become
   a measure because of what it carries (chosen). Rejected: an event is a
   measure when it carries `$value` (the family would follow from an
   attribute, and a `purchase` with an amount would leave `product`).
2. **Three kinds, set by `measure`, each with a fixed unit:**

   | `measure` | Unit | Examples |
   | --- | --- | --- |
   | `time` | milliseconds | request duration, LCP |
   | `size` | bytes | payload, file, memory |
   | `number` | none | queue depth, retries, CLS |

   The unit belongs to the kind, so a client cannot send seconds and there
   is no unit vocabulary. Money is not a kind: send it as a `number` and put
   the currency in the name (`cart_value_eur`).
3. **Web Vitals are reserved metric names with a fixed kind:**

   | Name | Metric | `measure` | Good (p75) | Poor (p75) |
   | --- | --- | --- | --- | --- |
   | `$lcp` | Largest Contentful Paint | `time` | ≤ 2500 | > 4000 |
   | `$inp` | Interaction to Next Paint | `time` | ≤ 200 | > 500 |
   | `$cls` | Cumulative Layout Shift | `number` | ≤ 0.1 | > 0.25 |
   | `$fcp` | First Contentful Paint | `time` | ≤ 1800 | > 3000 |
   | `$ttfb` | Time to First Byte | `time` | ≤ 800 | > 1800 |

   The thresholds are Google's and live only in the system dashboard's SQL,
   never in ingest or the database, because they change (INP replaced FID
   in 2024).
4. **Rejected: a Graphite or StatsD format.** Those clients aggregate at the
   source, so percentiles cannot be combined afterwards. They put
   dimensions into the metric name, and the plaintext protocol has no
   authentication or idempotency. Only the name-value-time model is kept.

### Wire format

5. **An event is `{id, ts, family, name, value, measure, attributes}`.**
   `family`, `value` and `measure` are top-level fields beside `name`,
   because they are part of what the event is, not a description of it.

   | Field | `views`, `product` | `measures` |
   | --- | --- | --- |
   | `family` | optional | required, never inferred |
   | `name` | as today | the metric: any name, or a reserved `$` name |
   | `value` | dropped with a warning | required, a finite JSON number ≥ 0 |
   | `measure` | dropped with a warning | required: `time`, `size` or `number` |

6. **Ingest rules, all per event.** A rejected event is listed in the
   `202`'s `errors` and the rest of the batch is stored, so no rule here
   produces a 4xx.
   - **Missing `family`:** `$page_view` and `$screen_view` (and the
     `$pageview` alias) are `views`, anything else is `product`, as today.
     Clients built before this change keep working unchanged.
   - **Unknown `family`:** rejected, not stored as `product`. A newer
     client talking to an older server loses that event rather than
     polluting `product`.
   - **Contradictions are rejected:** `views` with a name that is not a
     view name; `measures` without a valid `value` or `measure`; a reserved
     metric with the wrong kind (`$cls` must be `number`, the other four
     `time`).
   - **Unknown `$` metric names** (a future `$tbt`) are stored with a
     warning, as unknown `$` product names are today.
   - **`$sample_rate`** is a new reserved attribute key, only meaningful on
     measures: a number in (0, 1], treated as 1 when absent. An invalid
     value is stored as 1 with a warning. On any other family it is dropped
     with a warning. A backend can set it once in the batch `attributes`.
   - **Everything else is unchanged for measures:** timestamp clamping,
     limits, origin checks, the attribute merge, and every identity,
     environment and location key. Measures carry the same actor, user,
     group and session as other events, so vitals can be joined to
     sessions and users in SQL.
7. **Release order.** The decoder ignores unknown top-level fields
   (`internal/server/ingest.go:67`), so an older server stores a measure as
   a product event without a warning. The collector serves the SDK, so the
   script and the server always match. The docs tell backends and
   self-hosted SDK copies to upgrade the server first.

### Retention (PR 1)

8. **One retention pair for every family:**
   `RETENTION_EVENTS_RAW_DAYS` (default 30) and
   `RETENTION_EVENTS_AGGREGATE_DAYS` (default 365). The aggregate window
   also covers actors, cohorts and identities, as the views pair does
   today. `MaxEventAge()` reads the one raw window, which removes the
   clamp bug.
9. **The old names are removed, with no refusal and no mention left:**
   `RETENTION_VIEWS_*`, `RETENTION_PRODUCT_*`, `RETENTION_WEB_*` and
   `RETENTION_APP_*` go from the code, the `renamed` table and the docs
   (chosen). A server that still sets one falls back to the defaults
   without a warning. The one exception is this release's
   `deploy/UPGRADES.md` entry, which tells operators to rename them before
   upgrading. Older runbook entries are history and stay as written.
   `RETENTION_ARCHIVED_DAYS` is unrelated and stays.

### Storage (PR 2: clustering)

10. **`events` becomes a `WITHOUT ROWID` table with primary key
    `(family, project_id, day, id)`** (chosen). Each family's rows for a
    project and day are stored together, and the key replaces both current
    indexes: the `id` uniqueness index and `idx_events_family`. Measured on
    the real schema with 10 days of 300k views, 150k product events and
    1.5M vitals (CPU seconds, median of 3):

    | | today | 3 tables | **clustered** | 3 tables, clustered |
    | --- | --- | --- | --- | --- |
    | file size | 647 MB | 629 MB | **485 MB** | 494 MB |
    | `v_views_daily` | 6.05 | 3.79 | **4.52** | 4.68 |
    | `v_views_paths` | 1.86 | 1.00 | **1.24** | 1.23 |
    | `raw_views` per-day count | 0.65 | 0.27 | **0.32** | 0.33 |
    | `v_product_daily` | 0.46 | 0.24 | **0.27** | 0.27 |
    | p75 LCP by day | 0.84 | 0.53 | **0.58** | 0.58 |

    Rejected: a table per family. It is the fastest, but saves only 3% of
    disk because every table keeps its own indexes. Splitting a clustered
    table adds nothing. One table also keeps identity joins and every
    filter on one read path, which #58 set out to have.
11. **`day` becomes an ordinary column written by ingest** from the clamped
    `ts`. A generated column cannot be part of a primary key. Nothing that
    reads `day` changes.
12. **Duplicates are detected on `(family, project_id, day, id)`, not on
    `id` alone.** A retry repeats the id, `ts`, family and project, so it
    is still ignored. The known gap: a retry of an event whose `ts` was
    clamped, arriving on the other side of midnight, clamps to a different
    `day` and is stored twice. A unique index on `id` would close it at the
    cost of the index this change removes (97 MB in the benchmark), so the
    gap is documented instead.
13. **Nothing that reads `events` changes.** `raw_views`, `raw_product`,
    every `v_*` view, dashboards and saved SQL keep their columns. The
    prune and rollup deletes already filter on
    `family = ? AND project_id = ? AND day = ?`
    (`aggregate_views.go:178`, `aggregate_product.go:47`), which becomes a
    contiguous range delete. Nothing in the code uses `rowid`.

### Storage (PR 3: measures)

14. **New columns on `events`:**
    ```sql
    value        REAL,                        -- NULL outside measures
    measure      TEXT NOT NULL DEFAULT '',    -- 'time' | 'size' | 'number'
    sample_rate  REAL NOT NULL DEFAULT 1,     -- as sent, in (0, 1]
    bucket       INTEGER GENERATED ALWAYS AS
                 (CASE WHEN value IS NULL THEN NULL
                       WHEN value > 1e-17 THEN CAST(ceil(log(value) / log(1.04)) AS INTEGER)
                       ELSE -1000 END) VIRTUAL
    ```
    plus `CREATE VIEW raw_measures AS SELECT * FROM events WHERE family =
    'measures'`. As everywhere else, there are no `CHECK` constraints:
    ingest validates. `v_events_flat` gains `value`, `measure` and
    `sample_rate`.
15. **Percentiles come from log-scale buckets** (chosen). The driver's
    SQLite (3.53 via modernc) has `log()`, `ceil()` and `pow()`, but no
    `percentile()` or `median()`, so the same bucketing is used on raw rows
    and in the aggregates. Base 1.04 gives about ±2% relative precision:
    340 ms falls in bucket 149 (about 332–345), and 1 ms to one hour spans
    about 380 buckets, of which only those with data are stored. CLS values
    below 1 get negative buckets (0.08 is bucket −64). A value of 0 (or
    anything up to 10⁻¹⁷) goes to the zero bucket, −1000, below every real
    bucket. It is a number, not `NULL`, because a `WITHOUT ROWID` primary key
    makes every key column `NOT NULL`. The virtual column works on a
    `WITHOUT ROWID` table (checked). Buckets combine exactly across days and
    dimensions, so a p75 over a quarter is still correct. Rejected:
    percentiles only within the raw window (no long-term trend); daily
    precomputed percentiles (they cannot be combined across days or
    dimensions).
16. **Aggregates follow the product rules** (chosen):
    ```sql
    CREATE TABLE agg_measures_daily (             -- ≈ agg_product_daily
        project_id INTEGER NOT NULL, day TEXT NOT NULL,
        event_name TEXT NOT NULL, measure TEXT NOT NULL,
        bucket INTEGER NOT NULL,                  -- -1000 = zero bucket
        samples INTEGER NOT NULL,                 -- rows received
        weight  REAL    NOT NULL,                 -- Σ 1/sample_rate: estimated true count
        sum     REAL    NOT NULL,                 -- Σ value/sample_rate: exact mean
        PRIMARY KEY (project_id, day, event_name, measure, bucket)
    ) WITHOUT ROWID;

    CREATE TABLE agg_measures_attrs (             -- ≈ agg_product_attrs
        project_id INTEGER NOT NULL, day TEXT NOT NULL,
        event_name TEXT NOT NULL, measure TEXT NOT NULL,
        attr_key TEXT NOT NULL, attr_value TEXT NOT NULL,
        bucket INTEGER NOT NULL,
        samples INTEGER NOT NULL, weight REAL NOT NULL, sum REAL NOT NULL,
        PRIMARY KEY (project_id, day, event_name, measure, attr_key, attr_value, bucket)
    ) WITHOUT ROWID;
    ```
    - **The same keys as product:** the 8 system dimensions
      (`store.SystemAttributes`) always, plus the project's declared keys
      (custom, and the 9 declarable reserved ones such as `$path`). One
      declaration serves product and measures.
    - **The same cap:** `PRODUCT_ATTRIBUTES_TOP_N` values per key per
      metric per day, ranked by `samples`, with the rest in `(other)`.
      Histogram rows add up, so `(other)` is the sum of the rest, with
      nothing to recompute from raw.
    - **`measure` is in the key**, so a name sent as two kinds stays two
      series.
    - **`unit` is not stored.** It follows from `measure`.
    - **No totals table**, since "all measures in a day" is not a number
      anyone reads. **No unique users**, since they don't add up across
      buckets. Questions per user or session go to the raw window.
    - **Consequences for vitals:** by page beyond the raw window only when
      `$path` is declared (with the top-N cap), and by country only within
      the raw window, since country is not a product dimension. Rejected: a
      measures-only dimension list with an automatic `path` (cap 500) and
      `country`, which would be a second set of rules to document.
17. **The daily job rolls up and prunes measures in the same pass as
    product**, under the one retention pair. Measures do not feed the actor,
    cohort or identity passes (`allRawDays`): a backend's connection hash is
    a server, not a visitor, and a page's vitals always come with its view.
    Widgets show `time` in milliseconds with the `number` format, because
    the `duration` format renders seconds.

### Reading

18. **Queryable views**, listed in `docs/twillingate.md` and in `schemaViews`:
    - `raw_measures`: every `events` column for the family.
    - `v_measures_daily`: `project_id, day, event_name, measure, bucket,
      approx_value, samples, weight, sum`. The aggregates joined
      (`UNION ALL`) with the same computation run live over `raw_measures`.
    - `v_measures_attrs`: the same, plus `attr_key` and `attr_value`, with
      the live half capped like `v_product_attrs`.

    `approx_value` is the value with the smallest relative error for the
    bucket, `2 · 1.04^bucket / 2.04`, and 0 for the zero bucket (−1000), so SQL never
    needs the bucket formula.
19. **Percentiles are plain SQL, in one documented pattern** in
    `docs/reporting.md`. On 1,000 test values (10% zeros) it gave a p75 of
    712.9 against an exact 722:
    ```sql
    WITH h AS (SELECT event_name, bucket, approx_value, SUM(weight) AS w FROM v_measures_daily
               WHERE project_id = :project AND day BETWEEN :from AND :to GROUP BY 1, 2, 3),
         c AS (SELECT *, SUM(w) OVER (PARTITION BY event_name ORDER BY bucket) AS run,
                         SUM(w) OVER (PARTITION BY event_name) AS total FROM h)
    SELECT event_name, MIN(approx_value) FILTER (WHERE run >= 0.75 * total) AS p75,
           SUM(w) AS est_count
    FROM c GROUP BY 1;
    ```
20. **A `measures` MCP tool and REST route.** For a project and range it
    returns, per metric: `measure`, samples, estimated count, mean, p50,
    p75 and p95. An optional `attr_key` breaks each metric down by that
    key's values. It mirrors `product_events` and `product_attributes`, and
    does the percentile math so agents don't have to. It is documented in
    `docs/twillingate.md` and in the OpenAPI document.
21. **Two system dashboards, built only from existing components:**
    - **Web Vitals:**
      - five `stat` widgets: p75 of each vital against the previous period;
      - a stacked horizontal `bar` with the good / needs-improvement / poor
        share of samples per vital;
      - a `line` of p75 by day for the time vitals, and one for CLS;
      - a `table` by page: p75 of LCP, INP and CLS by `$path`, with samples;
      - a `table` by device and browser;
      - a `markdown` note on the thresholds and small samples.
    - **Measures:** a `table` of every non-`$` metric (measure, samples,
      mean, p50, p75, p95), and a `line` of p75 by day per metric.

### SDK

22. **`measure()`:**
    ```js
    twillingate.measure("checkout_api", 340, "time", { endpoint: "/api/checkout" })
    ```
    It validates like the server (a finite number ≥ 0, a known kind).
    Invalid calls log in debug mode and send nothing. From now on the SDK
    sends an explicit `family` on every event it sends.
23. **Web Vitals come from Google's `web-vitals` package, in a separate
    bundle** (chosen). Version 6.2.2 with all five metrics is 8.6 KB
    minified and 3.3 KB gzip, against 9.2 KB gzip for today's whole SDK.
    The collector serves `/js/twillingate-vitals.js` next to
    `/js/twillingate.js`, and the SDK loads it only when vitals are
    enabled, so sites without them pay nothing. The package's Apache-2.0
    notice goes in that bundle's header, and the main SDK stays MIT. The
    bundle is committed beside `twillingate.js` and covered by CI's drift
    check. Rejected: our own collector. LCP, FCP, TTFB and CLS would be
    about 1 KB, but INP needs interaction grouping, the worst-interaction
    approximation, and handling of back/forward-cache restores and
    prerendered pages. Values that disagree with Chrome's field data would
    undermine the feature, and the package comes from the Chrome team.
24. **Vitals are opt-in:** `data-vitals="0.2"`, or `vitals: 0.2` in code.
    Absent means off, and `1` means every page load. Turning them on by
    default would make an SDK upgrade multiply every site's raw rows by up
    to six.
25. **Sampling is decided once per page load:** one random draw against the
    rate, after which the page sends every vital it produces with
    `$sample_rate`, or none. A sampled page is complete, so joins to its
    view and session hold. Backends sample per request.
26. **Collection:**
    - **Location:** a vital carries the `$host` and `$path` of the page load
      it measures (masking applied). In a single-page app, route changes
      after the load don't move them. INP and CLS cover the page's whole
      life, as Chrome counts them.
    - **Timing:** FCP and TTFB are queued as soon as they are known; LCP,
      CLS and INP arrive when the page is hidden.
    - **Unload order:** `web-vitals` reports from its own `visibilitychange`
      listener, which can run after the SDK has sent its final batch
      (`runtime.ts:90`). A vital reported while the page is hidden is
      therefore sent at once with `sendBeacon`, not queued.
    - **Identity, consent, `optOut` and debug** apply as for every other
      event.
    - **Two instances on one page** each have their own rate and draw. The
      vitals bundle loads once and reports to every instance that enabled
      it.

## Migrations

- **PR 2 (clustering)** rebuilds `events` in the style of 020: it drops
  the dependent views, creates the new table, copies the raw window (with
  `day` from `ts`), drops the old table and its two indexes, and recreates
  the views unchanged. The copy needs free disk for a second copy of the
  raw window while it runs. The `UPGRADES.md` entry states that, and the
  midnight duplicate gap from decision 12.
- **PR 3 (measures)** adds `value`, `measure`, `sample_rate` and the
  virtual `bucket` column with `ALTER TABLE ADD COLUMN` (no rebuild),
  creates `raw_measures`, `agg_measures_daily`, `agg_measures_attrs`,
  `v_measures_daily` and `v_measures_attrs`, and recreates `v_events_flat`
  with the three new columns.

## Docs

Updated in the same commit as each change:

- **`docs/deployment.md`** (PR 1): the retention pair replaces the four old
  rows, and the tip at line 135 says `RETENTION_EVENTS_RAW_DAYS`.
- **`deploy/UPGRADES.md`:**
  - PR 1: rename the retention variables before upgrading;
  - PR 2: disk needed for the rebuild, and the duplicate gap.
- **`docs/twillingate.md`** (PR 3):
  - the envelope with `family`, `value` and `measure`;
  - a families table replacing "Reserved event names";
  - the reserved metric names and `$sample_rate`;
  - `measure()` and `data-vitals` in the SDK sections;
  - the new views, and the `measures` tool and route;
  - "upgrade the server first" for backends.
- **`docs/reporting.md`** (PR 3): the percentile pattern and the two system
  dashboards.

## Tests

- **Config (PR 1):** the pair parses with its defaults. Old names have no
  effect. The clamp follows `RETENTION_EVENTS_RAW_DAYS`. `docs_sync_test`
  binds the two new variables in `docs/deployment.md`.
- **Clustering migration (PR 2):**
  - Build a database at the previous version with views and product events
    across raw and rolled-up days, snapshot every `v_*` view, migrate, and
    require every snapshot unchanged.
  - Every row and column is copied, and `day` equals `substr(ts, 1, 10)`.
  - A replayed batch is still ignored.
  - The test pins its schema version.
- **Query plans (PR 2):** `TestViewsLiveHalvesUseTheDayIndex` asserts a
  day-bounded `SEARCH` on the primary key instead of `idx_events_family`.
- **Bench (PR 2):** `bench_test.go` runs the views live halves on the
  clustered table, before and after, and records file size.
- **Ingest (PR 3):**
  - a table test per family rule: default when missing, unknown family,
    each contradiction, `value` bounds and type, unknown `measure`, a
    reserved metric with the wrong kind, `$sample_rate` valid, invalid and
    absent;
  - `value` and `measure` dropped with a warning on views and product;
  - an unknown `$` metric stored with a warning.
- **No unfiltered reads (PR 3):** the test from #58 that fails on any read
  of `events` outside `raw_views` and `raw_product` also accepts
  `raw_measures`, and nothing else.
- **Rollup (PR 3):**
  - bucket counts, `weight` with mixed sample rates, and `sum`, exact;
  - the top-N cap with `(other)`, and a declared `$path`;
  - each `v_measures_*` view returns the same numbers before and after its
    day is rolled up;
  - prune by the retention pair.
- **Percentile accuracy (PR 3):** a property test over random
  distributions. The p50, p75 and p95 from buckets stay within 2% of the
  exact values, and weighted samples match an unsampled run within
  sampling error.
- **API (PR 3):** tests for the `measures` tool and route. `docs_sync_test`
  binds the new tool, route, views, reserved key, and the family and
  measure vocabularies. `TestSystemDashboards` runs both new dashboards.
- **SDK (PR 3, vitest):**
  - `measure()` validation;
  - parsing of `data-vitals` and `vitals`;
  - all-or-nothing sampling per page load;
  - the vitals bundle loaded only when enabled;
  - a vital reported while hidden is sent by beacon at once;
  - two instances with different rates;
  - `family` on every event.
- **Browser (PR 3, Playwright):** a page with `data-vitals="1"` in Chromium
  sends `$lcp`, `$fcp`, `$ttfb` and `$cls`, and `$inp` after a click.

## After release

- Rename the retention variables in prod's environment before upgrading
  to PR 1's release.
- Before PR 2's release, check free disk on prod for the rebuild.
- After PR 3's release, set `data-vitals="1"` on kuznetsov.dev and compare
  its p75 values with CrUX or PageSpeed Insights.

## Out of scope

- Native SDKs. Apps send `family: "measures"` over the wire as it stands.
- Ingesting pre-aggregated histograms (Prometheus or OpenTelemetry shape).
  Sampling covers volume until a backend needs more.
- A Graphite- or Prometheus-compatible read API for Grafana.
- Money as a kind with a currency.
- Vitals attribution (which element was the LCP, which interaction was
  slow).
- Soft-navigation vitals for single-page apps.
- Measures-only aggregate dimensions (`country`, an automatic `$path`).
