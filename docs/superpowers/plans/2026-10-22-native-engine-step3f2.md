# Native engine, step 3f-2: the built-in dashboards on structured queries — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every SQL widget of the built-in dashboards (59, under `internal/reporting/system/`) becomes a `.query` widget answered by the native engine. Each one's answer is checked against the old SQL's answer on the system fixture, within a short list of reviewed, documented differences. No built-in widget reads DuckDB any more.

**Architecture:**
- **The old SQL is kept as test data.** Before conversion, each widget's `.sql` file moves to `internal/reporting/testdata/system-sql/<group>/<name>.sql` (test-only).
- **A parity harness.** `TestSystemWidgetsMatchSQL` runs every converted widget through `WidgetData` and the old SQL through the DuckDB reader, on the system fixture, for the presets 7d, 30d and 90d. It compares the two after `rename`/`labels`/`columns`, against a per-widget allowance table that names each accepted difference.
- **Conversion, dashboard group by group.** Each group's widgets are converted, along with the reshapes, its Markdown text, and the docs that describe the built-ins.
- **Fixed tools last.** The analytics MCP tools move onto native presets, or are dropped in favour of `query`, per the spec ("thin presets over the engine or are dropped … lean towards dropping").

**The survey of all widgets** (exact query JSON per widget, gaps, reshapes) is `.superpowers/native-3f-mapping.md` in this worktree. It is scratch: an implementer reads it for the JSON, and the plan below is authoritative where they differ. Features added in 3f-1 since the survey are `ratio`, `min`/`max` of `day`/`ts`/`received_at`, `user_label`/`group_label`/`user_first_seen`/`group_first_seen`, `days`, trend `fill` and multi-metric trends, the Other rule, and source `labels`/`columns`.

**Tech Stack:** Go (`internal/reporting`, `internal/api`), JSON widget files, `docs/`.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md` (order of work step 3: rewrite the system widgets, parity check against DuckDB on the same files; "What changes in the repo": fixed tools become presets or are dropped).

## Global Constraints

- **Allowed differences.** A widget's native answer must equal the old SQL's answer for every preset on the system fixture, except for these accepted differences (survey ids). The allowance table names, per widget, which of them apply:
  - **G2:** a stat over no data is blank, never 0.
  - **G11:** trends with `fill` show zero days.
  - **G13:** at most 50 series.
  - **G14:** distinct counts over a range past 1,024 ids are estimated and marked ≈. The allowance is ±2% on those columns.
  - **G15:** `daily_*` is counted per day and group, not per day and a finer key.
  - **G10:** new/returning labels (unless relabelled to match).
  - **The reshapes, which change the widget:**
    - **G6:** app versions show true distinct counts, and the "(at least)" header text goes.
    - **G7:** attribute values becomes a local table, capped at the row limit.
    - **G8:** p75 by day keeps time measures only (`measure eq time`); its title and Markdown say so.
    - **G9:** by-device-browser becomes two widgets, "By device" and "By browser", added to the vitals dashboard layout.
  - **Float tails** compare as numbers, as the parity harness already does.
- **No widget may silently differ.** The allowance table is the only place a difference is accepted, and each entry names its gap id.
- **Display unchanged.** Each converted widget keeps its component, title, props and size (`.json`) unless a reshape says otherwise. `rename` maps the native columns to the old column names, including display names with spaces such as "Visitors" and "LCP p75 (ms)", so props such as `formats` keyed by column keep working.
- **The system loader** (`internal/reporting/files.go`) already reads `.query` and refuses two data files per widget. Migration of existing installs (Migrate syncing system dashboards) must replace a system widget's source type and content in place, keeping its id. Check `internal/reporting/migrate.go`: if the sync only updates content and not the type, extend it, and test an upgrade from an SQL system widget to a query one.
- **`TestSystemDashboards`** keeps passing: every widget, every preset, non-empty on 7d (the `mayBeEmpty` list is updated only with a reason).
- **Docs:** `docs/reporting.md` (anything describing built-in widgets or naming their SQL), `docs/twillingate.md` (fixed tools), and `deploy/UPGRADES.md`. The last one needs a short entry: built-in dashboards now read the native engine; visible differences G2, G6–G11, G13–G15; nothing to do.
- **Commits:** `feat(reporting)!` only if a stored user-visible contract breaks; otherwise `feat(reporting)`. Tools dropped are `feat(api)!` (breaking for agents).
- **Checks:** `make check` must pass before each commit (`PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`), and `cd web && npm run e2e` for tasks that change layouts. Never use `/tmp`. Never mention prices, tiers, plans, margins, customers, cloud or private services.

## Review Focus

1. **A widget whose `formats` prop is keyed by a column name.** `rename` must produce exactly that name, or the format silently stops applying. Pinned by the parity harness comparing column names too.
2. **Upgrading an install whose system dashboards were customised** (copied, or with a hidden group). Only the system originals change; user copies keep their SQL until step 6. Pinned by Task 1's migrate test.
3. **A preset where the old SQL returns no row and native returns a blank stat** (G2). The allowance must be on that widget only. Pinned by the harness.
4. **The vitals layout after splitting by-device-browser.** The 360px phone e2e and the layout tests stay green. Pinned by Task 6's e2e run.
5. **A dropped tool.** It is removed from the MCP list, the REST routes, `docs/twillingate.md` and the tool count in one commit (`docs_sync_test.go`). Pinned by Task 7.

---

### Task 1: The parity harness and the migration path

**Files:**
- Create: `internal/reporting/system_parity_test.go` (runs each `.query` system widget and, for every widget listed in the allowance table, its old SQL from `testdata/system-sql/`; compares as above), `internal/reporting/testdata/system-sql/` (empty for now).
- Modify: `internal/reporting/migrate.go`, if needed, so a system widget's source type changes in place on sync. Test `TestMigrateSystemWidgetSourceType`.

The harness is a no-op until a widget is converted. Write it with one fake converted widget in a temporary system dir to prove it detects a difference and honours an allowance.

- [ ] Tests first, run and see them fail, implement, then `make check`. Commit: `git commit -m "test(reporting): check native system widgets against their old SQL"`

### Tasks 2–6: Convert one dashboard group each

Every group task does the same thing:
1. Move each `<name>.sql` to `testdata/system-sql/<group>/<name>.sql` with `git mv`.
2. Write `<name>.query` from the survey's JSON, adjusted to 3f-1 features: `rename` to the old column names, `labels`, `columns`.
3. Apply the group's reshapes, editing the `.json`, the `.md` and `dashboard.json` as needed.
4. Add the group's rows to the allowance table.
5. Run `go test ./internal/reporting/ -run 'System'` and `make check`.

| Task | Group(s) | Widgets | Reshapes / notes |
|---|---|---|---|
| 2 | `views` | 20 | G2 on visitors/bounce/avg-session; G12 browsers/OS; G5 visitors-and-views (metrics order Visitors then Views); G15 allowances |
| 3 | `product` | 10 | G6 app-versions and users-by-app-version (+ md); G7 attribute-values local table |
| 4 | `users`, `groups` | 12 | top-users/top-groups with `columns` and labels; DAU/DAG with `fill` and `labels` (New users / Returning users); ratio widgets; G14 |
| 5 | `retention` | 5 | retention kind outputs renamed to the old column names (Cohort, Identified by, Day, Size, Returned, Retention; x/y; D7/D30/D45) |
| 6 | `measures`, `vitals` | 12 | G8 p75-by-day time only (+ title/md); G9 split by-device-browser into two widgets (+ dashboard.json layout); G2 on the five vital stats; run `npm run e2e` |

- [ ] Each task commits as `feat(reporting): answer the <group> dashboard natively`.

### Task 7: The fixed analytics tools

**Files:** `internal/api/ops_read.go`, `ops_product.go`, `ops_measures.go`, `ops_attributes.go`, `ops_usage.go` and `ops_event_rules.go` (the tools: `views_overview`, `views_breakdown`, `product_events`, `product_attributes`, `measures`, `retention`, `identities`, `list_attributes`, `usage`, `event_names`), `docs/twillingate.md`, `internal/api/docs_sync_test.go`, and tests.

**Rule:** a tool whose answer the survey maps exactly or cosmetically becomes a thin preset. It builds a native query (or a few) and keeps its input and output shape, so agents and the console see no change. A tool with a real gap (G16 OR filter in `views_breakdown`, G18 retention past 90 offsets, G19 every project in `usage`) is handled as follows:
- **`usage`:** loops over projects in Go, one query each.
- **`retention` tool:** becomes the `retention` kind's grid with offsets capped at 90. Document the change.
- **`views_breakdown`'s OR presets (utm, locales):** use two queries merged in Go, if they can be made exact; otherwise drop those two presets and document it.
- **`event_names`:** REST-only and used by the console, so it becomes a preset.

Every tool keeps its MCP name and REST route unless it is dropped. A dropped tool goes from the tool list, the routes, the docs tables and the tool count together. Prefer keeping (as presets) over dropping, since the console depends on several of them (`usage`, `list_attributes`, `event_names`).

**Tests:** each tool's existing tests stay green. Add a parity case per tool against its old SQL (kept in the test, the way Task 1 keeps the widget SQL).

- [ ] Tests first, then implement and run `make check`. Commit: `feat(api): answer the analytics tools natively` (or `feat(api)!` if a tool or preset is dropped).
