# Native engine, step 3b: sessions, identities and computed dimensions — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The native engine answers questions about sessions, about new versus returning users and groups, and about the computed dimensions the built-in dashboards group by. Every answer equals DuckDB's.

**Architecture:** `internal/native` gains:
- the `sessions` table, read from the derived session Parquet files, plus sessions derived in Go for the days no current file covers (`archive.Files.SessionGaps`), with `derive.go`'s exact rules;
- the `identities` table, read from the derived file plus live rows for the open logs, as `Reader.build`'s `derivedView` combines them;
- computed dimensions and filter columns: `resolution`, `device_label`, `platform_version`, `consent_state`, `user_status`, `group_status`;
- session metrics: `bounces`, `bounce_rate`.

The language, validation, MCP schema and `docs/twillingate.md` "Structured queries" change in the same commits.

**Tech Stack:** Go; the existing `internal/native`, `internal/archive`, `internal/shared/hll`.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md` ("The structured query language": sessions metrics, computed dimensions, `first_seen_day`; "What the built-ins need").

## Global Constraints

- Every answer is checked against DuckDB through `archive.Reader` on the same files (the step 2 parity harness in `internal/native/engine_test.go`). Exact answers must match row for row.
- **Rules:** derived tables (`sessions`, `identities`) ignore rules, as in DuckDB (derive.go:16, twillingate.md). Rules still apply to the event tables.
- **`sessions` columns and types** are exactly `sessionsSelect`'s (internal/archive/derive.go:41-74):

  | Columns | Type |
  |---|---|
  | `project_id`, `duration_sec`, `views` | BIGINT |
  | `day` | DATE |
  | `kind`, `actor_id`, `actor_kind`, `session_id`, `entry_host`, `entry_path`, `exit_path`, `referrer_source`, `utm_source`, `utm_medium`, `utm_campaign` | VARCHAR |
  | `started_at`, `ended_at` | TIMESTAMP |

  **Live derivation** works on the `views` family of the gap days only:
  - Use `t = floor(epoch seconds of ts)`.
  - Rows are partitioned by (project_id, day, kind, actor_id) and ordered by t, then id.
  - A new session starts when `session_id` is empty and (it is the first row, or `t − previous t > 1800`).
  - The key is `'id:'||session_id`, or `'gap:'||running count`.
  - Values per session:
    - `first(…)` columns are taken ordered by (ts, id);
    - `session_id` is `nullif(max(session_id), '')`;
    - `duration_sec` is `max(t) − min(t)`;
    - `views` is `count(*)`.
- **`identities` columns and types:** `project_id` BIGINT; `kind` ('user'|'group'), `id` and `name` VARCHAR; `first_seen` and `last_seen` TIMESTAMP; `views` and `events` BIGINT. They are combined as `identitiesCombine` does (derive.go:81-120), and sourced as `derivedView` does (reader.go ~389-399): the derived file when current, plus live rows from the open logs; when stale, live rows from all the project's events.
- **Computed dimensions:** exact SQL equivalents, from the system widgets.
  - `resolution` = `display_width || 'x' || display_height` when both are > 0, else NULL.
  - `device_label` = `device_model` when it is non-empty, else `device` when that is non-empty, else NULL.
  - `platform_version` = `coalesce(platform,'') || ' ' || app_version` when `app_version` is non-empty, else NULL.
  - `consent_state` = `given` (1), `none` (0), otherwise `unknown`.
  - `user_status` (on `events`, `views`, `product`) = `new` when `user_id` is non-empty and the row's `day` equals `first_seen::DATE` of that user in `identities` (kind `user`); `returning` when the user is non-empty and day > first-seen day; NULL when `user_id` is empty.
  - `group_status` is the same over `group_id` and kind `group`.
- **Session metrics** (table `sessions` only):
  - `count` counts sessions.
  - `bounces` counts sessions with `views = 1`.
  - `bounce_rate` = bounces / sessions as a DOUBLE, formatted like DuckDB, and empty when there are no sessions.
  - `sum`, `avg`, `min`, `max` take `duration_sec` or `views`.
  - `actors` counts distinct `actor_id` over sessions.
- Memory stays under the step 2 budget (`MaxBytes`). The live session derivation keeps per-actor state only for one day of one project at a time.
- `make check` must pass before each commit: `PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`. Never use `/tmp`. Never mention prices, tiers, plans, margins, customers, cloud or private services.

## Review Focus

1. **A session crossing midnight UTC.** It is cut at the day boundary, as DuckDB's partition by day does. Pinned by Task 2's parity fixture (an actor viewing at 23:50 and 00:10).
2. **A declared `session_id` mixed with undeclared views of the same actor on one day.** The keys differ (`id:` against `gap:`), so they are separate sessions. Pinned by Task 2.
3. **A day covered by a current session file *and* by a late log** (a gap). The gap must replace that day, not add to it. Pinned by Task 2 (late events written after `Maintain`).
4. **A user first seen before the query's range.** `user_status` must be `returning` for every day in range, which needs history outside the range (identities). Pinned by Task 4.
5. **Identities when the derived file is stale or missing.** Fall back to live derivation over all files. Pinned by Task 4 (delete `identities.parquet` and compare).

---

### Task 1: Computed dimensions

**Files:**
- Modify: `internal/native/query.go` (dimension and filter vocabulary: the four computed columns, available on `events` and `views`; `device_label`, `platform_version` and `consent_state` also on `product`; type text), `internal/native/aggregate.go` / `filter.go` (compute per row from source columns; request the source columns in the scan), the MCP schema description in `internal/api/ops_query.go`, `docs/twillingate.md` "Structured queries", and `ColumnTypes()` (text).
- Test: parity cases in `engine_test.go`. The fixture needs rows with display sizes (including 0), device/model mixes, platform/app_version, and consent 1/0/NULL. Add breakdowns by each computed dimension, plus a filter `consent_state eq given`.

- [ ] Tests first, run them and see them fail, implement, run `go test -race ./internal/native/`, then `make check`. Commit: `git commit -m "feat(native): group by resolution, device, platform version and consent"`

### Task 2: The `sessions` table

**Files:**
- Create: `internal/native/sessions.go` (read the derived session files for the range, plus live derivation of the gap days within the range from the events Parquet and logs), `internal/native/sessions_test.go`
- Modify: `internal/native/files.go` (expose the project's `Sessions` files and `SessionGaps` from `archive.ListFiles`, filtered by range), `internal/native/engine.go` (table `sessions` scans sessions rows), `internal/native/query.go` (`Tables` gains `sessions`; its columns are the table above, minus `started_at`/`ended_at` as dimensions; the numeric ones are `duration_sec` and `views`)
- Test: `TestSessionsRowsMatchDuckDB` (every column, sorted, against `SELECT * FROM sessions WHERE project_id=…`). It covers derived files only, gaps only (unmaintained logs), both, the midnight cut, declared plus undeclared session ids, and a late log on a current day.

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): read and derive sessions"`

### Task 3: Session metrics

**Files:**
- Modify: `internal/native/query.go` (metric ops `bounces` and `bounce_rate`, valid only on `sessions`; `sum`/`avg`/`min`/`max` columns for sessions `duration_sec` and `views`), `internal/native/aggregate.go`, the docs and the MCP schema.
- Test parity cases on `sessions`: a stat with `count`, `bounces`, `bounce_rate`, `avg duration_sec`; a trend of `count` by day; a breakdown of `entry_path` by `count`; a breakdown of `referrer_source` by `bounce_rate`; an empty range (`bounce_rate` empty).

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): count sessions, bounces and bounce rate"`

### Task 4: The `identities` table and new vs returning

**Files:**
- Create: `internal/native/identities.go` (load the project's identities as `derivedView` does, cached per process keyed by the files it read: path, size and mtime; build a map from kind+id to first-seen day)
- Modify: `internal/native/query.go` (`Tables` gains `identities`, with dimensions `kind` and `name` and the date columns as dimensions by day; computed dims `user_status`/`group_status` on `events`, `views` and `product`), `internal/native/aggregate.go` (look up the first-seen day per row), the docs and the MCP schema.
- Test:
  - `TestIdentitiesRowsMatchDuckDB`: with a current file, with a missing or stale file, and with open logs on top.
  - Parity: a trend of `users` by day with series `user_status` (the daily-active-users widget), and a stat of `users` filtered `user_status eq new` (the new-users widget, which counts distinct `user_id` where day = first seen). Include a user first seen before the range.

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): read identities and tell new users from returning ones"`
