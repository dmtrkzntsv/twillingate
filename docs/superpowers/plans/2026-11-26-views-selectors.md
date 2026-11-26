# Views Selectors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship four selectors (Host, Source, Medium, Campaign) on the built-in Views dashboard, and make `sessions` widgets follow a `host` selector through `entry_host`.

**Architecture:**
- A built-in's `dashboard.json` gains an optional `selectors` list. The loader validates it with `checkSelectors`, and the release sync writes it to the system row on every start.
- On `sessions`, a selector's column maps to the session column that means the same thing, so `host` maps to `entry_host`.

**Tech Stack:** Go (`internal/reporting`, `internal/store/sqlite`).

**Spec:** `docs/superpowers/specs/2026-11-26-views-selectors-design.md`

## Global Constraints

- Views selectors, in this order:
  - `{"name":"host","label":"Host","column":"host"}`
  - `{"name":"source","label":"Source","column":"utm_source"}`
  - `{"name":"medium","label":"Medium","column":"utm_medium"}`
  - `{"name":"campaign","label":"Campaign","column":"utm_campaign"}`
- With no selection, Views answers and cache keys are exactly as today.
- The API keeps refusing selector writes on system dashboards (`refuseSystem`). Only the sync writes them.
- Docs change in the same commit as the code: `docs/reporting.md`.
- Conventional Commits. Trailer: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Locally, run only the touched packages' tests. CI runs `make check`.

## Review Focus

1. **A user's own dashboard with a `host` selector and a `sessions` widget** now narrows that widget by `entry_host`. Test it in Task 1.
2. **A release that removes or changes a built-in's selectors** must replace the stored list, not merge into it. Test it in Task 2.
3. **A user who hid Views** (`sidebar: false`) keeps it hidden after the sync writes selectors. Test it in Task 2.

---

### Task 1: `sessions` follows `host` through `entry_host`

**Files:**
- Modify: `internal/reporting/selectors.go`
  - replace `sessionSelectorColumns map[string]bool` with `sessionSelectorColumns map[string]string`, which maps a selector column to its sessions column: `utm_source`, `utm_medium`, `utm_campaign` and `referrer_source` map to themselves, and `host` maps to `entry_host`;
  - `queryFollows` and `selectorFilters` use the mapped column on `sessions`.
- Modify: `docs/reporting.md`, wherever the sessions following rule is stated.
- Test: `internal/reporting/selectors_test.go` (`TestFollowedSelectors`), `internal/reporting/data_test.go`

**Interfaces:**
- Produces: `func selectorColumnOn(table, column string) (string, bool)`, which gives the column to filter on that table and whether the table follows that selector column at all. Its rules:
  - `identities` → `("", false)`;
  - `sessions` → the map above;
  - any other table → `(column, true)`, after which the native engine's `Validate` still decides.

- [ ] **Step 1: Write the failing tests.**
  - In `TestFollowedSelectors`, a `sessions` query now follows a `host` selector.
  - `TestSessionsHostFiltersEntryHost` (in data_test.go, with the fake `Native` capturing the query): a `sessions` stat widget with `host` = `["a.example"]` gets `{column: "entry_host", op: "in", values: ["a.example"]}` prepended. A `views` widget with the same selection gets `host`.
- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/reporting/ -run 'FollowedSelectors|SessionsHost'`
- [ ] **Step 3: Implement** `selectorColumnOn`, and use it in `queryFollows` (validate with the mapped column) and in `selectorFilters` (filter on the mapped column). `selectionKey` keeps the selector's own column.
- [ ] **Step 4: Update the docs, then run the tests and confirm they pass:** `go test ./internal/reporting/... ./internal/api/`
- [ ] **Step 5: Commit.** `feat(reporting): let session widgets follow a host selector through their entry host`

### Task 2: Built-in selectors, and Views ships four

**Files:**
- Modify: `internal/reporting/files.go`
  - `fileDashboardDoc` gains `Selectors []Selector` (`json:"selectors"`);
  - `fileDashboard` gains `Selectors []Selector`;
  - `loadDashboard` runs `checkSelectors` and wraps any error with the file path.
- Modify: `internal/reporting/migrate.go`: fill the new `store.SystemDashboard.Selectors` with the JSON text (`"[]"` when none).
- Modify: `internal/store/reporting.go`: add `SystemDashboard.Selectors string`.
- Modify: `internal/store/sqlite/reporting_sync.go`: the upsert writes `selectors` both on insert and in `DO UPDATE SET`.
- Modify: `internal/reporting/system/views/dashboard.json`: add `"selectors"` with the four entries from Global Constraints, after `"project_tab"`.
- Modify: `docs/reporting.md`: the system dashboards paragraph in Concepts says Views ships Host, Source, Medium and Campaign selectors, set by the release.
- Test: `internal/reporting/files_test.go`, a sync test in `internal/store/sqlite/` (beside the existing reporting sync tests), `internal/reporting/system_test.go`

**Interfaces:**
- Consumes: `checkSelectors` and `selectorColumnOn` (Task 1).
- Produces: `store.SystemDashboard.Selectors string` (JSON array text).

- [ ] **Step 1: Write the failing tests:**
  - `TestLoadDashboardSelectors`: a fixture `dashboard.json` with a valid list loads with it. One with `"column":"day"` fails, and the error names the file.
  - `TestSystemViewsSelectors`: the embedded definitions give Views (id 1) exactly the four selectors in order, and every other built-in gets none.
  - `TestSyncWritesSystemSelectors` (sqlite):
    - after a sync with selectors, `GetDashboard` returns them;
    - a second sync with `"[]"` clears them;
    - a row hidden by the user (`sidebar` 0) stays hidden.
  - `TestSystemDashboards`: a second pass loads every Views widget with `Selectors: {"host": [<a host the fixture seeds>]}` and expects no error. The three `sessions` widgets and the `views` widgets must report `follows_selectors` containing `host`.
- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/reporting/ ./internal/store/sqlite/ -run 'LoadDashboardSelectors|SystemViewsSelectors|SyncWritesSystemSelectors|SystemDashboards'`
- [ ] **Step 3: Implement** as listed under Files.
- [ ] **Step 4: Update the docs, then run the tests and confirm they pass:** `go test ./internal/reporting/... ./internal/store/... ./internal/api/ ./internal/sqlcheck/`
- [ ] **Step 5: Commit.** `feat(reporting): ship host and UTM selectors on the Views dashboard`
