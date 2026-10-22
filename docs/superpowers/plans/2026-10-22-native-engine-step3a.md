# Native engine, step 3a: widgets with a structured query source — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A dashboard widget can take its data from a structured query run by the native engine (`source.type = "query"`), end to end: Go reporting, system dashboard files, the web contracts and editor, and the docs.

**Architecture:** `internal/reporting` registers a third source type, `query`. Its content is JSON:

```json
{"query": {…native.Query…}, "rename": {"day": "x", "previous_value": "previous"}}
```

The source runs the query through an injected `Native` interface, with rules from an injected function. It renames output columns with `rename`, so the result fits a component's inputs, and returns a `readsql.Result` that goes through the normal column and row checks.

Every component that accepts `sql` also accepts `query`. System dashboard widgets may be stored as `<name>.query` (that JSON) next to `<name>.json`. The web editor edits a query widget's JSON in a code editor and previews it. Step 3 is split into parts; this one (3a) adds no new query kinds or tables.

**Tech Stack:** Go (`internal/reporting`, `internal/api`, `internal/app`); React and TypeScript in `web/` (vitest; Playwright e2e). It uses the existing `internal/native` from step 2.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md` ("The structured query language": a widget source becomes `{"type": "query", …}`; "What changes in the repo").

## Global Constraints

- The order of source-type names in messages and lists stays sorted: `md`, `query`, `sql`.
- **A `query` source always follows the project and the range,** so `Follows` returns `(true, true)`. A widget with a `query` source needs a project, like an SQL widget using `:project`.
- **Validation at save:**
  - the content parses (unknown fields refused);
  - `query.Validate()` passes;
  - `rename` keys are output columns of that query, and the values are distinct non-empty names;
  - the renamed columns satisfy the component's inputs (`checkColumns`). The output columns are computed from the query alone (`native` gains `func (q Query) Columns() []string`), with no data read.

  Errors wrap `manage.ErrInvalid` through the reporting validation path, as other source errors do, and name the field.
- **Results are cached** like SQL results (`Cacheable() == true`). They are capped at `Files.MaxRows()` with `Truncated` and run under `Files.Timeout()`.
- Remote tables (`table` with mode `remote`) do not accept `query` in this step. Refuse it at save with a message.
- **Docs, in the same commit as each change:**
  - `docs/reporting.md`: the components table's Accepts column; the prose naming source types (~lines 70, 88, 1168, 1169); and a worked `query` example for at least `stat`, `line` and `bar_list`, bound by `TestReportingExamplesWork`, which must accept ```` ```json ```` blocks as `query` sources.
  - `docs/twillingate.md`: anywhere it lists widget source types.
- After changing widget contracts, regenerate and commit `internal/reporting/ui/components.json` (`make ui`), as CLAUDE.md requires.
- Web: everything clickable keeps the pointer cursor (`web/e2e/cursor.spec.ts`), and nothing scrolls sideways at 360px (`web/e2e/phone.spec.ts`).
- `make check` must pass before each commit (it needs Node 22 and Go on PATH: `PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`). Web changes also run `cd web && npm test` and `npm run e2e` (Playwright, Chromium at `/opt/pw-browsers` or the system one).
- Never mention prices, tiers, plans, margins, customers, cloud or private services in committed text.

## Review Focus

1. **A query widget on a project with no files yet.** It must return no rows, not an error. Pinned by Task 1's `TestQuerySourceEmptyProject`.
2. **A `rename` that maps two columns to one name, or names a column the query doesn't produce.** Refused at save, naming `rename`. Pinned by Task 1's validation table.
3. **The stat with `compare: previous`.** The output must reach the component as `previous` through `rename`. Pinned by the docs example in Task 3, which loads through `widget_data`.
4. **A system dashboard file pair with both `.sql` and `.query` for one widget.** Refused by the loader. Pinned by Task 2's `TestSystemLoaderRefusesTwoSources`.
5. **The editor switching a widget from SQL to query, or back.** The draft must keep the right content per type and preview the right type. Pinned by Task 4's vitest.

---

### Task 1: The `query` source type in reporting

**Files:**
- Create: `internal/reporting/query_source.go`, `internal/reporting/query_source_test.go`
- Modify: `internal/reporting/source.go` (`newSources` gains the native dependency; `validSourceType` keeps working with nil deps), `internal/reporting/reporting.go` (`Options.Native`, `Options.Rules`), `internal/reporting/types.go` (`Source` jsonschema description: "sql, query or md"), `internal/reporting/preview.go` (accept `query`), `internal/native/query.go` (`func (q Query) Columns() []string`, with a test in `query_test.go`), `internal/api/server.go` and `internal/app/app.go` (pass `nat` and a rules func into `reporting.Options`), plus callers that build sources (`Migrate`, `CheckUserWidgets`, `DevHandler`; app/migrate.go, app/widgets.go, app/reporting_dev.go). Update the tests that pin source lists: ops_test.go:1105, dev_test.go:223, validate_test.go:104/113, schema_test.go:45.

**Interfaces:**
- Produces:
  ```go
  // in internal/reporting
  type Native interface {
      Answer(ctx context.Context, q native.Query, p native.Params, rules archive.Rules) (native.Result, error)
  }
  // Options gains:
  //   Native Native                                         // nil: the query source refuses with "structured queries are not available"
  //   Rules  func(ctx context.Context, project int64) archive.Rules  // nil: no rules
  type queryContent struct {
      Query  native.Query      `json:"query"`
      Rename map[string]string `json:"rename,omitempty"`
  }
  ```
  `internal/reporting` (rank 1) may import `internal/native` (rank 0).

- [ ] **Step 1: Write the failing tests**

Use an `archivetest` fixture with a few events and a real `native.Engine`:
- `TestQuerySourceLoads`: a `bar_list` widget with a breakdown `country` by actors, renamed `{country: label, actors: value}`, returns the expected rows.
- `TestQuerySourceStatPrevious`: `stat` with `As: value`, `compare: previous`, and `rename {previous_value: previous}` passes `checkColumns`.
- `TestQuerySourceValidate`, a table of refusals:
  - JSON that does not parse;
  - an unknown field;
  - an invalid query;
  - a `rename` key that is not an output column;
  - two `rename` keys mapped to one name;
  - columns that don't fit the component;
  - a remote table.
- `TestQuerySourceEmptyProject`: no files gives zero rows and no error.
- `TestColumns` in native, one case per kind.
- `TestSourceTypes`: `[md query sql]`.

- [ ] **Step 2: Run and verify they fail.**

- [ ] **Step 3: Implement**

`Load` parses the content, maps `Params` (string dates) to `native.Params`, calls `Answer` under `Files.Timeout()`, applies `rename`, caps rows at `Files.MaxRows()`, and returns `readsql.Result`. `native.ErrInvalid` and `native.ErrTooLarge` surface as reporting's invalid refusal.

- [ ] **Step 4: `make check`**

Expected: PASS. `components.json` has not changed yet, so the `query` source is registered before any component names it.

- [ ] **Step 5: Commit**

`git commit -m "feat(reporting): add a structured query widget source"`

### Task 2: System dashboard widgets stored as `.query`

**Files:**
- Modify: `internal/reporting/files.go` (the loader allows the `.query` extension, maps it to source type `query`, and refuses a widget with two source files)
- Test: `internal/reporting/files_test.go` (or the existing loader test file): `TestSystemLoaderReadsQuery`, `TestSystemLoaderRefusesTwoSources`
- Modify: `internal/reporting/system_test.go` (wire a `native.Engine` over the fixture's files into `Options.Native`, so `TestSystemDashboards` can run `.query` widgets once they exist)

- [ ] Write the failing tests, run them, implement, run `make check`, then commit: `git commit -m "feat(reporting): let system dashboards store structured query widgets"`

### Task 3: Components accept `query`; docs and examples

**Files:**
- Modify: every widget in `web/src/components/widgets/*.tsx` whose `accepts` is `['sql']` becomes `['sql', 'query']`, except `table`: its contract accepts `['sql', 'query']`, but remote mode refuses query (Task 1). Also `web/src/components/widgets/types.ts` (`accepts: ('sql' | 'query' | 'md')[]`) and `web/src/lib/api.ts` (`WidgetSource.type`). Update each widget's `*.test.tsx` `accepts` expectation, `records.test.ts`'s fixture, and `examples.test.tsx`.
- Regenerate: `internal/reporting/ui/components.json` (`make ui`).
- Modify: `docs/reporting.md` (the Accepts column, the source-type prose, and a `query` worked example for `stat` (with previous), `line` and `bar_list`, each as a ```` ```json ```` block plus `Props:`). Also `internal/api/docs_sync_test.go`'s `TestReportingExamplesWork`: accept ```` ```json ```` as a `query` source, and allow more than one example per component (key by component plus index; still require at least one per component).

- [ ] Update the tests first (vitest expectations; the docs examples), run them to see them fail, implement, run `cd web && npm test`, `make ui`, then `make check`. Commit: `git commit -m "feat(web): components accept structured query sources"`

### Task 4: The editor edits and previews query widgets

**Files:**
- Modify:
  - `web/src/lib/widget-draft.ts`: `Draft` gains `query: string`, the JSON text. `sourceTypeOf` returns the widget's current source type when editing an existing widget, otherwise `accepts[0]`. `sourceOf` builds `{type: 'query', content}`. `usesParams` is true for query.
  - `web/src/components/editor/WidgetEditor.tsx`: a source-type toggle (SQL / Query) shown when the component accepts both. Query uses `<CodeEditor language="json">`, and preview posts `type: 'query'`. The header label shows "Query".
  - `web/src/components/editor/CodeEditor.tsx`: a `json` language, if it needs adding.
  - `web/src/lib/props-form.ts:63`.
- Test:
  - vitest for `widget-draft` (switching types keeps each content; `sourceOf` per type);
  - a Playwright case in `web/e2e/` that creates a `stat` widget with a query source through the editor and sees a number. The e2e harness seeds events; check `web/e2e/` fixtures.

- [ ] Tests first, then implement. Run `cd web && npm test && npm run e2e`, then `make check`. Commit: `git commit -m "feat(web): edit and preview structured query widgets"`
