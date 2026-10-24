# Native engine, step 3c: percentiles, weighted means and vital ratings — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The native engine answers the measures and web-vitals questions the built-in dashboards ask:
- percentiles, weighted means and sample weights from the log-scale buckets;
- vital ratings and their shares;
- per-metric filters, so one breakdown can put several filtered metrics side by side.

Every answer equals DuckDB's.

**Architecture:** Percentiles are **metrics**, usable in `stat`, `trend` and `breakdown` on `measures`, rather than a separate kind. That lets one breakdown return samples, mean, p50, p75 and p95 together, and lets a trend draw p75 by day. A metric gains three optional fields:
- `q`, the quantile, for `percentile`;
- `round`, decimal places for any numeric metric;
- `where`, filters that apply to that metric only. With these, the per-page and per-device vitals tables become one breakdown with a filtered metric per vital.

Two computed dimensions on `measures` (`vital`, `vital_rating`) and a `share` metric cover the ratings widget.

**Tech Stack:** Go (`internal/native`, `internal/api`), `docs/twillingate.md`.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md`. The spec's kind list names `percentiles`; this plan delivers it as metrics within the existing kinds (ruling below). See also "What the built-ins need": weighted percentiles from log-scale buckets, and vitals ratings against fixed thresholds.

## Global Constraints

- **Exactness:** every answer matches the system widgets' SQL through `archive.Reader` on the same files, in the parity harness.
  - **Bucket value:** `approx_value` = `0` when `bucket = -1000`, else `2 * pow(1.04, bucket) / 2.04`.
  - **Weight:** `1 / sample_rate` per row.
  - **Percentile q over a group:**
    - sum the weights per bucket;
    - take the running sum in ascending bucket order, against the group's total weight;
    - the answer is the smallest `approx_value` whose running sum ≥ `q * total`;
    - the result is empty (NULL) when the group has no rows.
  - **Weighted mean:** `SUM(value / sample_rate) / SUM(1 / sample_rate)`.
  - **`round: n`:** applies `ROUND(x, n)` as DuckDB rounds (half away from zero). With `n = 0` the value prints as an integer, as DuckDB prints `ROUND(x)` of a DOUBLE. Match the widgets' printed text where a parity test compares it.
  - Float sums may differ from DuckDB in the last bits. The parity harness compares numbers with a tolerance (already the case for `avg`), and a percentile's chosen bucket must match: add a case at an exact boundary.
- **New metric ops (table `measures` only):**
  - `percentile` (needs `q` with 0 < q < 1; default name `p<100q>`, e.g. `p75`, or `p99.9`);
  - `mean` (the weighted mean of `value`);
  - `weight` (the sum of `1/sample_rate`);
  - `count` (unchanged) is the sample count.
- **`share` metric (any table):** `{op: share, of: count | weight}` is the group's `of` divided by the sum of `of` over its partition. In a breakdown the partition is all groups with the same first dimension, or all groups when there is only one dimension. In a trend it is the same day. `weight` is valid only on `measures`.
- **Per-metric `where`:** a metric's filters apply after the query's `where`. Rows failing them don't count toward that metric, but still count toward the others. Groups exist only where at least one row passes the query's `where`, so a group can show an empty or 0 metric. The same operators and NULL rules apply.
- **Computed dimensions on `measures`:**
  - `vital` = `upper(substr(event_name, 2))` for the five vitals (`$lcp`, `$inp`, `$cls`, `$fcp`, `$ttfb`), otherwise NULL.
  - `vital_rating` = `good` when `approx_value` ≤ good, `needs improvement` when ≤ poor, otherwise `poor`, with thresholds (good, poor) LCP (2500, 4000), INP (200, 500), CLS (0.1, 0.25), FCP (1800, 3000), TTFB (800, 1800). It is NULL for non-vitals.
- **Approximation:** percentiles come from buckets with about 2% resolution by design. The `approximate` flag is about distinct-count sketches only, so percentiles are not flagged. The docs say a percentile is the bucket boundary's value.
- Validation, the MCP schema tags, `docs/twillingate.md` "Structured queries" (bound by `TestStructuredQueryDocument`) and `ColumnTypes()` (all new metrics are number) change in the same commits.
- `make check` must pass before each commit: `PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`. Never use `/tmp`. Never mention prices, tiers, plans, margins, customers, cloud or private services.

## Review Focus

1. **The running-sum boundary.** When the running sum lands exactly on `q * total`, the bucket must be the one DuckDB picks. Pinned by Task 2's boundary case (weights 1,1,1,1 and q 0.5).
2. **The bucket −1000 (value ≤ 1e-17) and `sample_rate` other than 1.** Pinned by Task 2's fixture.
3. **A per-metric `where` that leaves a group with no rows for that metric.** The percentile is empty and the count is 0, matching `FILTER`'s NULL and `COALESCE(…, 0)` where the widget uses it. Pinned by Task 1.
4. **`share` with an Other row.** The partition total includes the folded rows. Pinned by Task 3.
5. **A trend whose percentile is empty on some days** (days without samples). Those days are absent, as in the SQL. Pinned by Task 2.

---

### Task 1: Metric `round` and per-metric `where`

**Files:**
- Modify: `internal/native/query.go` (`Metric` gains `Round *int` (0..6) and `Where []Filter`; Validate checks them, naming paths like `metrics[2].where[0].column`), `internal/native/aggregate.go` / `filter.go` (per-metric filter masks), `internal/native/engine.go` (rounding at output), the MCP schema tags, and `docs/twillingate.md`.
- Test: parity cases on events:
  - a breakdown by `path` with two metrics, `count` filtered `family eq views` (as "views") and `count` filtered `family eq product` (as "events");
  - a stat `avg value` with `round 2`;
  - a breakdown where one metric's filter matches nothing in some group (0 and empty);
  - Validate cases.

- [ ] Tests first, run and see them fail, implement, run `go test -race ./internal/native/ ./internal/api/`, then `make check`. Commit: `git commit -m "feat(native): round a metric and filter it on its own"`

### Task 2: Percentiles, weighted mean and weight on measures

**Files:**
- Modify: `internal/native/query.go` (ops `percentile` with `q`, `mean`, `weight`; table `measures` only; default names), `internal/native/aggregate.go` (per-group bucket histograms: map bucket → weight, merged across workers; percentile and mean at output; memory charged), the docs and schema.
- Test: parity cases against the widget recipes:
  - a stat `p75` of `$lcp` (vitals/lcp.sql);
  - a trend `p75` by day of `$cls` (vitals/cls-by-day.sql), with days without samples;
  - a breakdown `event_name` × `measure` with `count`, `mean` (round 3), `p50`, `p75`, `p95` (round 3), excluding `$` names, ordered by count desc (measures/metrics.sql);
  - a breakdown by `path` with filtered metrics per vital (`count` where `event_name eq $lcp`, plus p75 of each of `$lcp`, `$inp`, `$cls`), limit 50 (vitals/by-page.sql, excluding `path ''`);
  - the boundary case (Review Focus 1); bucket −1000; sample_rate 0.5.

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): compute percentiles and weighted means from measure buckets"`

### Task 3: Vital ratings and shares

**Files:**
- Modify: `internal/native/query.go` (computed dims `vital` and `vital_rating` on `measures`; metric `share` with `of`), `internal/native/aggregate.go` / `engine.go` (share computed per partition at output, Other rows included), the docs and schema.
- Test: parity with vitals/ratings.sql: a breakdown `vital` × `vital_rating` by `share of weight`, ordered by `vital` then `vital_rating`. Also a `share of count` on `events` by `country`, with `other`.

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): rate web vitals and share them out"`
