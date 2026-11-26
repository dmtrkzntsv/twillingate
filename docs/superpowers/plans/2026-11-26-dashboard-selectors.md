# Dashboard Selectors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Dashboards declare up to five selectors (a name, a label and a column, the column any `events` filter column or `attr_<key>`). A viewer ticks one or more values, and every widget that follows a selector narrows to them: structured-query widgets automatically, SQL widgets by reading `:name`.

**Architecture:** Selectors are stored as JSON on the `dashboards` row. `internal/reporting` resolves them per load into `Params.Selectors`. The query source prepends `{column, op: in, values}` filters, the way `narrowed()` does, and the sql source binds each `:name` as a `VARCHAR[]` (an empty list for "All"). The web keeps the selection in the URL (`s.<name>=…`) and sends it with every widget load.

**Tech Stack:** Go (modernc SQLite, native engine, external DuckDB engine over `sqlengine`), React + TypeScript (Vite, Vitest, Playwright).

**Spec:** `docs/superpowers/specs/2026-11-25-dashboard-selectors-design.md`. Read it first; this plan argues from it.

## Global Constraints

- At most **5** selectors per dashboard. `selector_values` returns at most **100** values and reads at most **10** projects.
- `name` matches `^[a-z][a-z0-9_]{0,31}$`, is unique on the dashboard, and is not `project`, `from` or `to`.
- `column`: any filter column of `events` that the native engine types as **text**, including `attr_<key>` for any key. Refused: day columns (`day`, `user_first_seen`, `group_first_seen`), numeric columns, `def_*`.
- `label` defaults to the column, with the `attr_` prefix stripped.
- "All" is an absent name or an empty list. SQL then gets an **empty list, never NULL**. Documented idiom: `(len(:name) = 0 OR list_contains(:name, <column>))`.
- Following rules: `events`, `views`, `product`, `measures` and `checks` follow a selector when the native engine accepts the filter on that table. `sessions` follows only `utm_source`, `utm_medium`, `utm_campaign` and `referrer_source`. `identities` follows none. `sql` follows the selectors whose `:name` it reads. `md` follows none.
- System dashboards refuse `selectors` (existing `refuseSystem`).
- Refusals are typed (`store.ErrInvalid` → 400, `store.ErrConflict` → 409) and matched with `errors.Is`.
- The selection lives in the page URL, not in browser storage.
- Docs change in the same commit as the code (CLAUDE.md table). `internal/api/docs_sync_test.go` must pass.
- Commits: Conventional Commits, `feat(<scope>): …`, each ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Locally run only the touched packages' tests (`go test ./internal/reporting/...` and so on) and `npm test` / typecheck in `web/`. CI runs `make check` and the e2e suite.
- **Do not modify any dashboard on the owner's live instance** (no MCP writes to Twillingate Analytics).

## Review Focus

1. **Values with SQL-hostile characters** (`'`, `"`, `,`, `[`, `]`, `\`, spaces, empty string, unicode). They must round-trip into SQL's `VARCHAR[]` exactly and into the URL exactly. Tests in Task 2 (sqlbind) and Task 6 (URL).
2. **A selector with no values must change nothing.** A query widget gets no prepended filter, the cache key equals today's key, and `widget_data` without `selectors` answers exactly as before. Test in Task 4.
3. **A selector column the widget's project never sent** (`attr_x` absent) must give zero rows, not an error, for a query widget. Test in Task 4.
4. **Removing or renaming a selector that a live SQL widget reads** must be refused, while changing its label or column must pass. Tests in Task 3.
5. **One stream request mixing widgets from two dashboards with different selectors**: each item is checked against its own widget's dashboard. Test in Task 4.

---

### Task 1: Store: selectors on dashboards, selection on shares

**Files:**
- Create: `internal/store/sqlite/migrations/051_dashboard_selectors.sql`
- Modify: `internal/store/reporting.go` (`Dashboard`, `WidgetShare`, `NewWidgetShare`)
- Modify: `internal/store/sqlite/reporting.go`, `internal/store/sqlite/reporting_sync.go`, and wherever the share rows are read and written (grep `widget_shares`)
- Modify: `internal/store/store.go` (doc comment of `UpdateDashboard`: it now also writes `selectors`)
- Test: `internal/store/sqlite/migration051_test.go`, plus round-trip cases in the existing reporting store tests

**Interfaces:**
- Produces: `store.Dashboard.Selectors string` (JSON array text, `"[]"` when none), written by `InsertDashboard`, `InsertDashboardGroup` and `UpdateDashboard`, and read by `GetDashboard` and `ListDashboards`. `store.WidgetShare.Selection string` and `store.NewWidgetShare.Selection string` (JSON object text, `""` when made at "All").

- [ ] **Step 1: Write the failing tests.** `TestMigration051` copies a migrated DB from `storetest` and asserts that `dashboards.selectors` defaults to `'[]'` on existing rows and that `widget_shares.selection` defaults to `''`. A round-trip test inserts a dashboard with `Selectors: `[{"name":"deployment","label":"Deployment","column":"attr_deployment"}]``, reads it back equal, then `UpdateDashboard` with `"[]"` and reads `"[]"`. A share inserted with `Selection: `{"deployment":["cloud"]}`` reads back equal.
- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/store/sqlite/ -run 'Migration051|Selectors|ShareSelection'` → FAIL (no column).
- [ ] **Step 3: Write the migration and the store code.**
  ```sql
  -- 051: a dashboard's selectors (JSON, [] = none) and the selection a share was made under ('' = All).
  ALTER TABLE dashboards ADD COLUMN selectors TEXT NOT NULL DEFAULT '[]';
  ALTER TABLE widget_shares ADD COLUMN selection TEXT NOT NULL DEFAULT '';
  ```
  Thread both columns through every SELECT, INSERT and UPDATE of those tables. An empty `Dashboard.Selectors` on insert is stored as `[]`. `internal/sqlcheck` must still pass.
- [ ] **Step 4: Run the tests and confirm they pass,** plus `go test ./internal/sqlcheck/ ./internal/store/...`.
- [ ] **Step 5: Commit.** `feat(store): store dashboard selectors and a share's selection`. No docs change yet, since nothing reads it.

### Task 2: sqlbind: bind a list as `VARCHAR[]`

**Files:**
- Modify: `internal/reporting/sqlbind/sqlbind.go`
- Test: `internal/reporting/sqlbind/sqlbind_test.go`

**Interfaces:**
- Produces: `sqlbind.ListType = "VARCHAR[]"`, accepted by `Bind` in addition to bare type names. `func sqlbind.ListText(values []string) string` returns the text that `CAST(… AS VARCHAR[])` parses back into exactly `values` (an empty slice gives an empty list).

- [ ] **Step 1: Write the failing tests.** `TestBindList`: `Bind("SELECT 1 WHERE list_contains(:d, x)", {":d": {Type: ListType, Text: ListText([]string{"cloud"})}})` gives `CAST($tw_d AS VARCHAR[])` and the args `{"tw_d": ListText(...)}`. `TestBindRefusesOtherBracketTypes`: `"INT[]"`, `"VARCHAR[];"` and `"VARCHAR[][]"` are refused. `TestListTextRoundTrip`: for each of `[]`, `["a"]`, `["a","b"]`, `["it's"]`, `["say \"hi\""]`, `["a,b"]`, `["[x]"]`, `["back\\slash"]`, `[""]`, `["  padded  "]` and `["ünïcödé"]`, parse `ListText(v)` with DuckDB's list-literal grammar and get `v` back. If the package tests can reach a real engine (see `internal/reporting/external_sql_test.go`), run `SELECT CAST($1 AS VARCHAR[])` through it instead. Otherwise write a small test-only parser of DuckDB's documented list cast rules (brackets, comma separators, double quotes, backslash escapes, trimmed unquoted elements) and note that in the test.
- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/reporting/sqlbind/`.
- [ ] **Step 3: Implement.** In `Bind`, accept `v.Type == ListType` besides `typeName`. `ListText` double-quotes every element and backslash-escapes `"` and `\`, so padding and commas survive. Keep the package's contract comment true: only `CAST(… AS Type)` is added around a parameter.
- [ ] **Step 4: Run the tests and confirm they pass.**
- [ ] **Step 5: Commit.** `feat(reporting): bind a list parameter as VARCHAR[]`.

### Task 3: Selectors on a dashboard: model, validation, writes, reads

**Files:**
- Create: `internal/reporting/selectors.go` (type, parsing, checks, following)
- Modify: `internal/reporting/ops_dashboard.go` (`CreateDashboard`, `UpdateDashboard` and their structs)
- Modify: `internal/reporting/ops_duplicate.go` (copy `Selectors`)
- Modify: `internal/reporting/ops_widget.go` (`CopyWidget`, `RestoreWidget`, `AddWidget`, `UpdateWidget` pass the dashboard's selector names to validation)
- Modify: `internal/reporting/source.go` (`SourceType.Validate` signature, `checkSQLParams`, `sqlFollows`), `query_source.go`, `validate.go` (`validateWidget`)
- Modify: `internal/reporting/read.go` (`WidgetInfo.FollowsSelectors`, `DashboardInfo.Selectors`)
- Modify: `internal/api/ops_reporting.go` (`createDashboardIn`, `updateDashboardIn` gain `selectors`; tool descriptions)
- Modify: `docs/reporting.md` ("Concepts": a widget follows selectors; "Tools"/"HTTP API": `selectors` on create/update; the dashboard JSON), `docs/twillingate.md` where it lists these tools' fields
- Test: `internal/reporting/selectors_test.go`, `internal/reporting/ops_test.go`, `internal/api/ops_reporting_test.go`

**Interfaces:**
- Consumes: `store.Dashboard.Selectors` (Task 1).
- Produces:
  ```go
  type Selector struct {
      Name   string `json:"name"`
      Label  string `json:"label"`
      Column string `json:"column"`
  }
  func parseSelectors(raw string) ([]Selector, error)           // the stored JSON; "" reads as none
  func checkSelectors(in []Selector) ([]Selector, error)        // the rules, filling Label; ErrInvalid naming the index and field
  func followedSelectors(w store.Widget, sels []Selector) []string // names w follows, in sels' order
  func selectorNames(sels []Selector) []string                  // for checkSQLParams
  ```
  `SourceType.Validate(ctx, content string, c Component, selectors []string) error`, where `selectors` are the dashboard's names. `checkSQLParams(content string, selectors []string) ([]string, error)`. `CreateDashboard.Selectors []Selector` and `UpdateDashboard.Selectors *[]Selector` (nil keeps them, empty clears them). `WidgetInfo.FollowsSelectors []string` (`json:"follows_selectors"`, always non-nil). `DashboardInfo.Selectors []Selector` (`json:"selectors"`, always non-nil).

- [ ] **Step 1: Write the failing tests** in `selectors_test.go`:
  - `TestCheckSelectors` (table-driven). Accepts `attr_deployment`, `utm_campaign`, `host`, `device_label`, and an `attr_` key no event sent. Refuses `day`, `user_first_seen`, `display_width`, `def_x`, an unknown column, a duplicate `name`, `project`/`from`/`to`, `Bad-Name`, a 33-character name, and a sixth selector. An empty label becomes `deployment` for `attr_deployment`.
  - `TestFollowedSelectors`. A query on `events` follows both `attr_deployment` and `utm_campaign`. On `sessions` it follows only `utm_campaign`. On `identities` it follows none. On `measures` it follows `attr_deployment` but not `utm_campaign`, if the native engine refuses it there (assert whatever `native.Query.Validate` decides, using it as the oracle). SQL reading `:deployment` follows `deployment` only. `md` follows none.
  - In `ops_test.go`:
    - `TestCreateDashboardSelectors` round-trips through `Dashboard()`.
    - `TestSQLWidgetReadsSelector`: `add_widget` with SQL using `:deployment` passes on a dashboard declaring it and is refused (ErrInvalid, the message names the selectors the dashboard has) on one without.
    - `TestUpdateSelectorsRefusesUsedName`: removing or renaming a selector that a live SQL widget reads → ErrConflict naming the widget. Changing its label or column → OK.
    - `TestCopyWidgetMissingSelector` → ErrInvalid.
    - `TestRestoreWidgetMissingSelector` → ErrInvalid.
    - `TestDuplicateCopiesSelectors`, also with `whole_group`.
    - `TestSystemDashboardRefusesSelectors`.
- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/reporting/ -run 'Selector|Selectors'`.
- [ ] **Step 3: Implement.**
  - Column check: build `native.Query{Kind: "breakdown", Table: "events", Dimensions: []string{col}, Metrics: []native.Metric{{Op: "count"}}}`. `Validate()` must pass, and `ColumnTypes()[0]` must be text. Refuse `def_` before that.
  - Following for query widgets: copy the query, prepend `native.Filter{Column: col, Op: "in", Values: []string{"x"}}`, and follow if `Validate()` passes, overridden by the `sessions` allow-list and `identities` = none. Put the allow-list in one `var` in `selectors.go`.
  - SQL validation runs with every selector bound to `ListText(nil)`. That binding is Task 4's `bindSQL`, so in this task, bind empty lists from inside `Validate`.
  - Thread `selectors` through `validateWidget` and every caller. `CopyWidget` validates against the target dashboard's names, and `RestoreWidget` against its own dashboard's current names.
  - `UpdateDashboard`: `Selectors != nil` alone counts as something to update, and the "nothing to update" message lists `selectors`. Store it with `st.UpdateDashboard`, audit action `dashboard.update`.
- [ ] **Step 4: Expose it in the API, then write the docs** (the `selectors` field on `create_dashboard`/`update_dashboard`, MCP and REST), and run `go test ./internal/api/ -run 'DocsSync|Dashboard'`.
- [ ] **Step 5: Run the tests and confirm they pass.** `go test ./internal/reporting/ ./internal/api/`.
- [ ] **Step 6: Commit.** `feat(reporting): declare selectors on a dashboard`.

### Task 4: Apply the selection on load

**Files:**
- Modify: `internal/reporting/source.go` (`Params.Selectors`, `bindSQL`), `query_source.go` (`plan`), `data.go` (`DataRequest.Selectors`, `prepare`, `widgetParams`, `cacheKey`), `stream.go` (`WidgetRequest.Selectors`), `preview.go` (`PreviewIn.DashboardID`, `PreviewIn.Selectors`)
- Modify: `internal/api/ops_reporting.go` (`widgetDataIn.selectors` as a JSON object string; stream items gain `selectors`; `previewWidgetIn` gains `dashboard_id`, `selectors`)
- Modify: `docs/reporting.md` ("Parameters and ranges": selector parameters, the idiom, "All" = empty list; `widget_data`, the stream and preview arguments; a worked example with a Deployment selector, one query widget and one SQL widget)
- Test: `internal/reporting/data_test.go`, `stream_test.go`, `preview_test.go`, `internal/api/ops_reporting_test.go`

**Interfaces:**
- Consumes: `Selector`, `parseSelectors`, `followedSelectors`, `sqlbind.ListType`, `sqlbind.ListText`.
- Produces:
  ```go
  type BoundSelector struct { Name, Column string; Values []string } // Values nil = All
  // Params gains: Selectors []BoundSelector — every selector of the widget's dashboard, in declared order
  // DataRequest, WidgetRequest gain: Selectors map[string][]string
  // PreviewIn gains: DashboardID int64; Selectors map[string][]string
  ```
  `widget_data` and `GET /api/widgets/{id}/data` take `selectors` as JSON text (`{"deployment":["cloud"]}`). Each `POST /api/widgets/stream` item takes `selectors` as a JSON object.

- [ ] **Step 1: Write the failing tests:**
  - `TestQueryWidgetNarrowedBySelector`. With a fake `Native` capturing the query: `Selectors {"deployment": ["cloud","self-hosted"]}` prepends `{attr_deployment in [cloud self-hosted]}` ahead of the widget's own `where`. With no values the query is unchanged.
  - `TestSelectorAllKeepsCacheKey`. The key with `Selectors` nil, `{}` or `{"deployment": []}` equals today's key format for that widget.
  - `TestSelectorValuesInCacheKey`. Different or reordered values give different and equal keys respectively (values sorted).
  - `TestSelectorUnknownNameRefused` → ErrInvalid "dashboard N has no selector x".
  - `TestSelectorNotFollowedIgnored`. An `identities` widget with `deployment` set loads unchanged, and its key has no selector part.
  - `TestSQLWidgetBindsSelector`. Bound SQL contains `CAST($tw_deployment AS VARCHAR[])` and args carry `ListText(["cloud"])`, and "All" carries `ListText(nil)`.
  - `TestQueryMissingAttributeNoRows`, against the real native engine fixtures used by the existing data tests. Selecting `attr_never_sent = x` gives zero rows, no error.
  - `TestStreamMixedDashboards`. Two items from dashboards declaring different selectors, each with its own `selectors`, both answer.
  - `TestPreviewWithDashboardSelectors`. SQL reading `:deployment` previews with `DashboardID`, and without it is refused as today.
- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/reporting/ -run 'Selector|Stream|Preview'`.
- [ ] **Step 3: Implement.**
  - `prepare` reads the widget's dashboard (`st.GetDashboard(w.DashboardID)`), parses its selectors, refuses unknown request names, and fills `params.Selectors` with every declared selector, with `Values` from the request.
  - `querySource.plan` prepends one `in` filter per followed selector with values. Factor the prepend out of `narrowed()` (`narrow.go`) so both use it.
  - `bindSQL` adds one `:name` → `{ListType, ListText(values)}` per `Params.Selectors`.
  - `cacheKey` appends, for followed selectors with values only, `name=` and the sorted values JSON-encoded.
  - `checkSQLParams` in `Load`/`LoadPage` takes the names from `Params.Selectors`.
- [ ] **Step 4: Expose it in the API and write the docs,** then `go test ./internal/api/`.
- [ ] **Step 5: Run the tests and confirm they pass.** `go test ./internal/reporting/... ./internal/api/`.
- [ ] **Step 6: Commit.** `feat(reporting): narrow widgets by the dashboard's selectors`.

### Task 5: `selector_values` and the share's selection

**Files:**
- Create: `internal/reporting/selector_values.go`
- Modify: `internal/reporting/ops_widget_share.go` (`NewShare.Selection map[string][]string`, `WidgetShareOut.Selection`), `share_page.go` / `share_page.html` (caption)
- Modify: `internal/api/ops_reporting.go` (new tool `selector_values`, `GET /api/dashboards/{id}/selectors/{name}/values`; share create takes `selection`)
- Modify: `docs/reporting.md` (tool and route tables, share caption), `docs/twillingate.md` (tool/route lists)
- Test: `internal/reporting/selector_values_test.go`, `ops_widget_share_test.go`, `share_page_test.go`, `internal/api/ops_reporting_test.go`

**Interfaces:**
- Consumes: `Selector`, `parseSelectors`, the `Native` interface.
- Produces:
  ```go
  type SelectorValuesIn struct {
      DashboardID int64; Name string; ProjectIDs []int64; From, To string; Search string
      Picked []string // kept in the answer even when absent from the range
  }
  type SelectorValue struct { Value string `json:"value"`; Count int64 `json:"count"` }
  func (s *Service) SelectorValues(ctx context.Context, in SelectorValuesIn) ([]SelectorValue, error)
  ```
  REST query: `project_id` (repeatable), `from`, `to`, `search`, `picked` (repeatable). MCP: the same fields, with `project_ids` a list.

- [ ] **Step 1: Write the failing tests:**
  - `TestSelectorValuesMergesProjects` sums counts per value across two projects and returns them by count descending, then value ascending.
  - `TestSelectorValuesCap`: 150 distinct values return 100.
  - `TestSelectorValuesProjectCap`: 11 project ids → ErrInvalid.
  - `TestSelectorValuesSearch` uses a case-insensitive `contains`.
  - `TestSelectorValuesKeepsPicked`: a picked value absent from the range is appended with count 0.
  - `TestSelectorValuesUnknownName` → ErrNotFound.
  - `TestShareStoresSelection`: a share made with `{"deployment":["cloud"]}` reads back with it, and its page caption contains `Deployment: cloud`. A share made with `{}` stores `""` and captions nothing extra.
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.** One native `breakdown` per project on `events`: dimension the column, metric `count`, `limit` 1000, `where` `contains` for `search`. Merge, sort and cut to 100, then append the missing picked values. On a share, keep only the selectors the widget follows that have values; the caption uses each selector's label, then `: `, then the values joined by `, `.
- [ ] **Step 4: Expose it in the API and write the docs,** then run `go test ./internal/reporting/ ./internal/api/`.
- [ ] **Step 5: Commit.** `feat(reporting): list a selector's values and caption shares with the selection`.

### Task 6: Web: the selector bar, the URL and the loads

**Files:**
- Create: `web/src/lib/selectors.ts` (URL encode/decode), `web/src/components/SelectorSwitcher.tsx`
- Modify: `web/src/lib/api.ts` (types `Selector`, `follows_selectors`, `selectors` on `widgetData`/stream items/preview, `selectorValues`), `web/src/pages/Dashboard.tsx`, `web/src/components/DashboardHeader.tsx`, `web/src/components/projects/ProjectDashboardTab.tsx`, the widget stream client (`web/src/lib/widget-stream.ts` or wherever items are built)
- Test: `web/src/lib/selectors.test.ts`, `web/src/components/SelectorSwitcher.test.tsx`, `web/src/pages/Dashboard.test.tsx`, `web/e2e/selectors.spec.ts`, and `web/e2e/phone.spec.ts` (fixture dashboard with two selectors whose labels and values are too long for one line)

**Interfaces:**
- Consumes: `selectors` and `follows_selectors` from `get_dashboard`, `GET …/selectors/{name}/values`, and `selectors` on data loads.
- Produces: `export type Selection = Record<string, string[]>`, `export function readSelection(params: URLSearchParams, names: string[]): Selection`, and `export function writeSelection(params: URLSearchParams, sel: Selection): URLSearchParams`. Param key `s.<name>`, the values one repeated param each (`s.campaign=a&s.campaign=b`), so commas need no escaping.

- [ ] **Step 1: Write the failing tests:**
  - `selectors.test.ts`: values round-trip with `,`, `&`, `=`, `+`, spaces, unicode and the empty string. Unknown `s.x` params are dropped by `readSelection`. Other params (range, project) are untouched.
  - `SelectorSwitcher.test.tsx`: the button reads `Deployment: All`, then `Deployment: cloud` with one value and `Deployment: 2 selected` with two. Clear empties the list. A search box appears past 10 values.
  - `Dashboard.test.tsx`: changing the selection re-requests only widgets whose `follows_selectors` contains that name, with `selectors` in the request.
  - `e2e/selectors.spec.ts`: against the built binary, create a dashboard with a selector over REST, pick a value, and see the URL carry `s.deployment` and the stat change. Reload and the selection is kept. Open the dashboard from the sidebar and it is at All.
- [ ] **Step 2: Run them and confirm they fail.** `cd web && npm test -- selectors SelectorSwitcher Dashboard`.
- [ ] **Step 3: Implement.** One button per selector next to `RangeSwitcher`, made with existing `ui/` popover and checkbox parts, so the pointer-cursor rule holds. Values come from `selectorValues`, with `project_id`s as the spec says: the switcher's project when the dashboard shows it, plus each query widget's fixed `project_id`, or every live project up to 10 when that set is empty. The switcher row wraps (`flex-wrap`), and on phone the popover is full width.
- [ ] **Step 4: Run the tests and confirm they pass,** then `npm run typecheck` and `npx playwright test e2e/selectors.spec.ts e2e/phone.spec.ts e2e/cursor.spec.ts --trace off`.
- [ ] **Step 5: Commit.** `feat(web): pick selector values on a dashboard`.

### Task 7: Web: the "Not filtered" note, the share caption, the editor preview

**Files:**
- Modify: `web/src/components/WidgetCard.tsx` (or `WidgetFrame.tsx`), `web/src/components/share/ShareDialog.tsx`, `web/src/lib/share.ts`, `web/src/components/editor/WidgetEditor.tsx`
- Test: `web/src/components/WidgetCard.test.tsx`, `web/src/components/share/ShareDialog.test.tsx`, `web/src/components/editor/WidgetEditor.test.tsx`

**Interfaces:**
- Consumes: `Selection` and `follows_selectors` (Task 6). Share create takes `selection` (Task 5). Preview takes `dashboard_id` and `selectors` (Task 4).

- [ ] **Step 1: Write the failing tests:**
  - `WidgetCard`: with `Deployment` set, a card whose `follows_selectors` lacks `deployment` shows `Not filtered by Deployment`. Two unfollowed selectors give `Not filtered by Deployment, Campaign`. With nothing set there is no note.
  - `ShareDialog`: creating a share sends `selection` with only followed, non-empty selectors, and the preview caption shows `Deployment: cloud`.
  - `WidgetEditor`: preview sends `dashboard_id` and the page's selection.
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.** The note is small muted text in the card header area, truncated with an ellipsis at 360px.
- [ ] **Step 4: Run the tests and confirm they pass,** plus typecheck and `npx playwright test e2e/phone.spec.ts --trace off`.
- [ ] **Step 5: Commit.** `feat(web): say which widgets a selection does not filter`.
