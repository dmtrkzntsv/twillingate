# Raw daily: per-day tables derived from the event files

Status: proposed, for review. It follows PR 5d (#188), whose measurement missed the 1-year target.
Date: 2026-10-19
Spec it extends: `2026-10-12-raw-first-design.md` (D8, D9, D16).

## Problem

Measured on 5d's branch:
- one project, 12 months at the cap (12M rows), 2 threads, 512 MB, cache off;
- `go test -tags measure ./internal/app`;
- Intel N150, load 1–5.

| Dashboard, range | One at a time | Child peak RSS | What costs |
| --- | --- | --- | --- |
| Views, 1 year | 10.8 s (target 2 s) | 0.8 GB | 16 widgets each scan a year of views for per-day `count(DISTINCT actor_id)`, 0.5–1.6 s each |
| Product, 1 year | 11.9 s | 1.0 GB | `attribute-values` **runs out of memory** (8.5 s, fails); `events`, `peak-dau`, `avg-dau` and `daily-active-users` take 0.7–0.9 s each |
| Retention, 90 days | 10.7 s (19.9 s all at once) | 1.1–1.2 GB | each of 5 widgets takes distinct (actor, day) over the whole history, every actor kind included |
| Users, 1 year | 2.3 s | 0.4 GB | `top-users` 1.0 s, `daily-active-users` 0.55 s |
| Groups, Web Vitals, Measures, 1 year | 0.1–0.2 s | 65–95 MB | fine |

At 30 days on the bench month (`~/.cache/twbench/trial.db`, 985k rows) Views takes 1.2 s: the target is met at the ranges people mostly use.

Two things hurt beyond time:
- **The child exceeds `ENGINE_MEMORY_LIMIT`:** 1.0–1.2 GB against 512 MB, on a server sized at 1 GB.
- **A system widget fails outright at 1 year** (`attribute-values`).

## The observation that makes this cheap

Every number the Views and Product dashboards show is a **sum over days of a per-day figure** (5d Decision 4: visitors = the sum of each day's distinct actors, as the `v_*` views gave). A per-day table of those figures therefore answers every range exactly, with no estimate and no cache to invalidate.

Probed on the year of synthetic files:
- **Building:** one row per (day, kind, dimension, key) for the 13 Views dimensions takes 0.9 s per month (one `GROUP BY GROUPING SETS`) and 450 MB peak.
- **Size:** 5.1 MB for the year, against 456 MB of raw files.
- **Reading:** a year of `top-pages` takes **43 ms against 1.63 s**, and visitors per day 37 ms, with identical numbers.

## Decisions (proposed)

- **D24. Per-day derived tables where measurement misses.** D16 already has one: sessions, the one query that missed in the first measurements. Two more join it, built and read the same way:

  | Table | One row per | Columns |
  | --- | --- | --- |
  | `views_daily` | project, day, kind, `dimension`, the dimension's key | `visitors`, `views` |
  | `product_daily` | project, day, event_name (NULL: the day's total) | `events`, `actors`, `users`, `groups` |
  | `product_values` | project, day, event_name, `attr_key`, `attr_value` | `events`, `actors`, `groups` |

  - **`views_daily`'s dimensions:** `all`, `path`, `host`, `referrer`, `utm` (three key columns), `country`, `os` (with its version), `browser` (with its version), `device` (with its model), `display`, `consent`, `app_version` (with platform), `locale` (browser and app). Its key columns are the events' own column names, NULL where the dimension does not use them, with `''` kept where the views showed it.
  - **`product_values` covers every attribute key and the 18 reserved `$` keys** (Decision 5 of 5d). It is the table that takes `attribute-values` out of memory trouble: one day at a time, the distinct count stays small.
  - Each is `raw/<project>/<table>/<day>.parquet`, then `<month>.parquet`, written by the daily pass after the dump or merge, as `sessions`.
  - **Current by modification time, with gaps derived on read.** A file is current when it is no older than the events file it comes from. A day with a log, or a missing or stale file, is derived on read from `_tw_all`, as `sessions` and its `SessionGaps` already do. Generalise that code rather than copy it.
  - Deleting a derived file loses nothing.
- **This is not D9's cache.** D9 rejected a per-day cache of each widget's result. That design was keyed by widget, invalidated by rules and erasure, and needed sketches for distinct users. These are tables of the events:
  - one per family and shape, not per widget;
  - exact, because the figure is per day by definition;
  - rebuilt from the files like `sessions`;
  - queryable by users' widgets too, so they are documented columns, a contract.
- **D25. Rules and the derived tables.**
  - `views_daily` does not depend on rules: views take no event rule (`$` names), and attribute rules cover product and measures only.
  - `product_daily` and `product_values` do: a renamed, merged or blocked event or key changes what they hold, and distinct actors across two merged names do not add up. So they are built with the project's rules applied (`_tw_all` through the rules, the `events` view's logic), and each file records the rules it was built under: a hash of the project's sorted rules, in a sidecar or the file's key-value metadata.
  - The reader uses a file only while its hash matches the current rules; otherwise the day is a gap, derived on read.
  - The next pass rewrites the stale files. A rule change costs about 1 s per month of history, once.
- **D26. Retention gets a predicate, not a table.** Its cost is a `SELECT DISTINCT actor_id, actor_kind, day` over every actor of the project's history. Restricting that first step to `actor_kind IN ('user', 'install')` (anonymous actors, the daily-salted connection ids, never equal a user or install id) takes Retention at 90 days from 10.7 s to **2.1 s**, and the child from 1.1 GB to **113 MB** (measured). The parity test proves it changes no number. Landed in #188 (492e75e): 2.1 s one at a time, 1.3 s all at once, child 113–181 MB, parity unchanged.
- **D27. Users stays as it is for now:** 2.3 s at a year, with no memory problem. Measure again after D24. If it still misses, `identities` already holds first and last seen, and a `users_daily` table would follow D24.

## What changes for users

- Three tables join the seven: in `docs/reporting.md` and `docs/twillingate.md`, in `schema://views`, and in the editor's completion.
- The system Views and Product widgets read them. Their output, titles and numbers are unchanged, pinned by `TestSystemWidgetsMatchSQLite`.
- A widget written against `views` or `product` keeps working, at today's cost.
- Disk: about 5 MB a year at the cap for `views_daily`. `product_values` depends on attribute cardinality: at most one row per event, typically far fewer. Measure it in the PR.

## Targets (re-measured in the PR, the same harness)

| Dashboard, 1 year | Target |
| --- | --- |
| Views | under 2 s, child under 512 MB |
| Product | under 2 s, no widget out of memory |
| Retention (90 d, after D26) | under 2 s (2.1 s measured) |
| A daily pass on a year of history, from nothing | under 1 min |

## Plan sketch

1. **#188 (5d):** D26's predicate in the five retention widgets; done (492e75e).
2. **PR 5e:**
   - the generalised day-derived machinery in `internal/archive` (listing, freshness, gaps, the rules hash);
   - `views_daily`, `product_daily`, `product_values`;
   - the reader's views over them;
   - the system Views and Product widgets ported to them, with parity;
   - the docs and schema;
   - the measurement.
3. **Later, if measured:** `users_daily` (D27).

## Open questions

- **Q12.** Long `views_daily` (one table, a `dimension` column) or one table per dimension? Long is one file per day and one derive; per-dimension tables are self-describing. Proposed: long.
- **Q13.** Should `product_values` keep `users` (`count(DISTINCT user_id)`) beside `actors`? The tools report actors only. Proposed: no, until a widget needs it.
- **Q14.** Does the rules hash belong in Parquet key-value metadata (no extra file, but read by opening the footer) or in a sidecar `<file>.rules` (a cheap stat)? Proposed: key-value metadata, read once per listing and cached by file modification time.
