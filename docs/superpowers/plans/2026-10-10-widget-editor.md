# Widget Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the console create a dashboard from a project, and add, edit (visualization, SQL with Run query, props), rename, archive and restore widgets on the user's own dashboards.

**Architecture:** One new REST-only route, `POST /api/widgets/preview`, runs an unsaved widget's SQL with the given project and range and returns the rows plus the first fit problem, reusing reporting's own checks. `update_widget` stops refusing title/props/source on a removed-component widget when a component comes with them. The web app gets a lazy-loaded editor modal (CodeMirror 6, a props form generated from each component's contract, a Preview/Table results pane), a page-level `WidgetEditingProvider` context that the widget card, the dashboard header and the empty state read, an "Archived widgets" section on the Archive page, and "New dashboard…" in a project's "+" dialog.

**Tech Stack:** Go 1.x (`internal/reporting`, `internal/api`), React 19 + TypeScript, TanStack Query 5, shadcn/radix UI, CodeMirror 6 (`@codemirror/*`), vitest + Testing Library, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-10-widget-editor-design.md` (decisions D1–D20). Read it before your task.

## Global Constraints

- Go is at `/usr/local/go/bin` (not on PATH): `export PATH=$PATH:/usr/local/go/bin`.
- `make check` must pass before every push; vitest needs `--testTimeout=30000` on this machine (`cd web && npx vitest run --testTimeout=30000 <files>`).
- Tests needing a migrated DB copy one (`newTestService`, `newTestHost` already do); never call Migrate per test.
- Refusals are `store.Refuse(store.ErrInvalid, …)` and matched with `errors.Is`, never by text.
- The preview route is REST only: not an MCP tool, not in `tools/list` (tool count stays 56).
- Everything clickable is a `button`, a link, or `role="button"` (no `cursor-pointer` classes); `web/e2e/cursor.spec.ts` enforces it.
- Nothing scrolls sideways at 360px; a single-column grid says `grid-cols-1`; result tables and charts scroll inside their own box.
- Docs change in the same commit as the code they describe (`docs/reporting.md` for the route and the update_widget rule).
- Commits: Conventional Commits, lower case, imperative, scopes from CLAUDE.md, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Match surrounding code: comment density, naming, idiom. Go comments are full sentences explaining why; TS uses `/** */` on exports.
- Copy (exact strings): menu items `Edit…`, `Rename`, `Archive`; button `Add widget`; toast `Widget archived` with action `Undo`; Archive section heading `Archived widgets`; dialog row `New dashboard…`; results tabs `Preview`, `Table`; stale note `Changed since last run`; confirm `Discard changes?`; run button `Run query`.

## Review Focus

1. **`:project` inside a string literal or comment** (`SELECT ':project' AS x -- :from`): the editor must not show project/range controls for it, and must show them for a real use; the server is the authority (it binds exactly Check's list), so a client miss surfaces as the server's 400, never a crash. → Task 4 `usesParams` tests.
2. **Escape while CodeMirror's autocomplete is open** must close the completion list, not the dialog (and not trigger "Discard changes?"). → Task 7 `shouldKeepOpenOnEscape` test.
3. **Double Save** (Enter twice, slow server): exactly one request; Save is disabled while saving. → Task 7 test.
4. **Rename to an unchanged or whitespace-only title**: no request, the field closes. → Task 8 test.
5. **A wide result on a 360px phone** (20 columns, long text): the editor scrolls nothing sideways; the table scrolls inside its box. → Task 11 phone.spec.

---

### Task 1: `reporting.Service.Preview`

**Files:**
- Create: `internal/reporting/preview.go`
- Create: `internal/reporting/preview_test.go`
- Modify: `internal/reporting/source.go` (extract the parameter allow-list check from `sqlSource.Validate`)

**Interfaces:**
- Produces:
  ```go
  type PreviewIn struct {
      Component string
      Props     json.RawMessage // nil or empty means {}
      Source    Source
      ProjectID int64
      From, To  string
  }
  type Preview struct {
      readsql.Result        // columns, rows, truncated (flattened in JSON)
      Problem   string `json:"problem,omitempty"`
      ElapsedMS int64  `json:"elapsed_ms"`
  }
  func (s *Service) Preview(ctx context.Context, in PreviewIn) (Preview, error)
  func checkSQLParams(db *readsql.DB, content string) ([]string, error) // source.go
  ```

- [ ] **Step 1: Write the failing tests** in `internal/reporting/preview_test.go`. Use the helpers in `testutil_test.go` / `data_test.go` (`newTestServiceOpts`, `seedViewsDaily`, `wantRefusal`, `rawExec`) and testdata components (`stat`, `line`, `table`, `markdown`, `pie`; see `testdata/components.json`). A project must exist for `:project`: insert one the way `data_test.go` does (grep `INSERT INTO projects` there and copy it).

```go
package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// previewClock pins today to 2026-09-14 so ranges are deterministic.
func previewService(t *testing.T, maxRows int) (*Service, store.Store) {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
	return newTestServiceOpts(t, Options{Now: now}, maxRows)
}

func sqlPreview(component, content string) PreviewIn {
	return PreviewIn{Component: component, Source: Source{Type: "sql", Content: content}}
}

func TestPreviewBindsTheGivenValues(t *testing.T) {
	svc, st := previewService(t, 1000)
	seedViewsDaily(t, st, 1, "2026-09-01", "2026-09-02", "2026-09-10")
	seedViewsDaily(t, st, 2, "2026-09-02")
	in := sqlPreview("line", "SELECT day AS x, views AS y FROM agg_views_daily WHERE project_id=:project AND day BETWEEN :from AND :to ORDER BY day")
	in.ProjectID, in.From, in.To = 1, "2026-09-01", "2026-09-02"
	got, err := svc.Preview(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Problem != "" || len(got.Rows) != 2 || got.Rows[0][0] != "2026-09-01" || got.Columns[1] != "y" {
		t.Errorf("preview = %+v", got)
	}
}

func TestPreviewReturnsRowsWithAProblem(t *testing.T) {
	svc, _ := previewService(t, 1000)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		in   PreviewIn
		want string
	}{
		"missing column": {sqlPreview("line", "SELECT 'a' AS x"), "line needs y (number); columns are x"},
		"extra column":   {sqlPreview("pie", "SELECT 'a' AS label, 1 AS value, 2 AS z"), "z is not an input of pie"},
		"row type":       {sqlPreview("stat", "SELECT 'many' AS value"), `stat.value: "many" is not number`},
		"props": {PreviewIn{Component: "stat", Props: json.RawMessage(`{"format":"weird"}`),
			Source: Source{Type: "sql", Content: "SELECT 1 AS value"}}, ""}, // message from jsonschema; checked by prefix below
	} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.Preview(ctx, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Rows) != 1 {
				t.Errorf("rows = %v, want the one row back", got.Rows)
			}
			if tc.want != "" && got.Problem != tc.want {
				t.Errorf("problem = %q, want %q", got.Problem, tc.want)
			}
			if tc.want == "" && (len(got.Problem) < 6 || got.Problem[:6] != "stat: ") {
				t.Errorf("problem = %q, want a stat props refusal", got.Problem)
			}
		})
	}
}

func TestPreviewNoRowsIsNoProblem(t *testing.T) {
	svc, _ := previewService(t, 1000)
	got, err := svc.Preview(context.Background(), sqlPreview("line", "SELECT day AS x, views AS y FROM agg_views_daily WHERE 0"))
	if err != nil || got.Problem != "" || len(got.Rows) != 0 || len(got.Columns) != 2 {
		t.Errorf("preview = %+v, %v", got, err)
	}
}

func TestPreviewTruncates(t *testing.T) {
	svc, _ := previewService(t, 2)
	got, err := svc.Preview(context.Background(), sqlPreview("table",
		"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<5) SELECT i FROM n"))
	if err != nil || !got.Truncated || len(got.Rows) != 2 {
		t.Errorf("preview = %+v, %v", got, err)
	}
}

func TestPreviewRefusals(t *testing.T) {
	svc, _ := previewService(t, 1000)
	ctx := context.Background()
	withRange := func(in PreviewIn, from, to string) PreviewIn { in.From, in.To = from, to; return in }
	for name, tc := range map[string]struct {
		in   PreviewIn
		want string
	}{
		"unknown component": {sqlPreview("nope", "SELECT 1"), "component nope does not exist; list_components names the ones there are"},
		"md source":         {PreviewIn{Component: "markdown", Source: Source{Type: "md", Content: "# hi"}}, "preview runs sql only; markdown is drawn as it is typed"},
		"md-only component": {sqlPreview("markdown", "SELECT 1"), "markdown does not accept sql; it accepts md"},
		"param":             {sqlPreview("table", "SELECT :other AS x"), "sql uses :other; widgets get only :project, :from and :to"},
		"no project":        {sqlPreview("table", "SELECT :project AS p"), "the sql uses :project; pass project_id"},
		"no range":          {sqlPreview("table", "SELECT :from AS f"), "the sql uses :from or :to; pass from and to"},
		"bad range":         {withRange(sqlPreview("table", "SELECT :from AS f"), "2026-09-10", "2026-09-01"), ""},
		"sqlite error":      {sqlPreview("table", "SELECT nope FROM nowhere"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Preview(ctx, tc.in)
			if !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if tc.want != "" && err.Error() != tc.want {
				t.Errorf("err = %q, want %q", err, tc.want)
			}
		})
	}
}

func TestPreviewClampsAFutureTo(t *testing.T) {
	svc, _ := previewService(t, 1000)
	in := sqlPreview("table", "SELECT :to AS t")
	in.From, in.To = "2026-09-10", "2026-09-20"
	got, err := svc.Preview(context.Background(), in)
	if err != nil || got.Rows[0][0] != "2026-09-14" {
		t.Errorf("preview = %+v, %v; want to clamped to today", got, err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/reporting -run 'TestPreview' -count=1`
Expected: FAIL to compile (`svc.Preview undefined`).

- [ ] **Step 3: Extract `checkSQLParams` in `source.go`** and use it from `sqlSource.Validate` (behaviour unchanged):

```go
// checkSQLParams runs Check on content and refuses any named parameter
// outside allowedParams, returning the ones it uses. Shared by a widget's
// Validate and Preview, so the two refuse the same SQL the same way.
func checkSQLParams(db *readsql.DB, content string) ([]string, error) {
	params, err := db.Check(content)
	if err != nil {
		return nil, store.Refuse(store.ErrInvalid, "%s", err)
	}
	for _, p := range params {
		if !allowedParams[p] {
			return nil, store.Refuse(store.ErrInvalid,
				"sql uses %s; widgets get only :project, :from and :to", p)
		}
	}
	return params, nil
}
```

In `Validate`, replace the first block (Check + loop) with `params, err := checkSQLParams(s.db, content); if err != nil { return err }`.

- [ ] **Step 4: Write `internal/reporting/preview.go`**

```go
package reporting

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// PreviewIn is an unsaved widget the console's editor runs (spec D1):
// its component, props and sql, and the project and range to bind.
// ProjectID and From/To matter only when the sql uses them.
type PreviewIn struct {
	Component string
	Props     json.RawMessage
	Source    Source
	ProjectID int64
	From, To  string
}

// Preview is the run's answer: the rows as widget_data returns them, and
// the first way they do not fit the component (props, columns, then
// value types), which does not withhold the rows (D4): the editor shows
// the table and why the chart will not draw.
type Preview struct {
	readsql.Result
	Problem   string `json:"problem,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// Preview runs an unsaved widget's sql with the values given rather than
// sample ones, uncached and unaudited. What keeps it from running at all
// is refused as add_widget refuses it (D2); markdown has nothing to run.
func (s *Service) Preview(ctx context.Context, in PreviewIn) (Preview, error) {
	comps, err := s.components(ctx)
	if err != nil {
		return Preview{}, err
	}
	comp, ok := comps[in.Component]
	if !ok {
		return Preview{}, store.Refuse(store.ErrInvalid,
			"component %s does not exist; list_components names the ones there are", in.Component)
	}
	if in.Source.Type != "sql" {
		return Preview{}, store.Refuse(store.ErrInvalid, "preview runs sql only; markdown is drawn as it is typed")
	}
	if !accepts(comp, "sql") {
		return Preview{}, store.Refuse(store.ErrInvalid,
			"%s does not accept sql; it accepts %s", comp.Name, joinAnd(comp.Accepts))
	}
	params, err := checkSQLParams(s.db, in.Source.Content)
	if err != nil {
		return Preview{}, err
	}
	p, err := s.previewParams(params, in)
	if err != nil {
		return Preview{}, err
	}
	start := time.Now()
	res, err := s.db.Query(ctx, in.Source.Content, bindArgs(params, p.ProjectID, p.From, p.To)...)
	if err != nil {
		return Preview{}, refuseSQLErr(s.db, err)
	}
	out := Preview{Result: res, ElapsedMS: time.Since(start).Milliseconds()}
	props := in.Props
	if len(props) == 0 {
		props = json.RawMessage(`{}`)
	}
	if err := comp.checkProps(props); err != nil {
		out.Problem = err.Error()
	} else if err := comp.checkColumns(res.Columns); err != nil {
		out.Problem = err.Error()
	} else if err := comp.checkRows(res); err != nil {
		out.Problem = err.Error()
	}
	return out, nil
}

// previewParams resolves the values to bind for exactly the parameters
// the sql uses: a missing one is refused, the range is checked and a
// future "to" clamped as widget_data does.
func (s *Service) previewParams(params []string, in PreviewIn) (Params, error) {
	var p Params
	for _, name := range params {
		switch name {
		case ":project":
			if in.ProjectID == 0 {
				return Params{}, store.Refuse(store.ErrInvalid, "the sql uses :project; pass project_id")
			}
			p.ProjectID = in.ProjectID
		case ":from", ":to":
			if p.From != "" {
				continue
			}
			if in.From == "" && in.To == "" {
				return Params{}, store.Refuse(store.ErrInvalid, "the sql uses :from or :to; pass from and to")
			}
			from, to, err := checkRange(in.From, in.To, civil.Today(s.now()))
			if err != nil {
				return Params{}, err
			}
			p.From, p.To = from, to
		}
	}
	return p, nil
}
```

`accepts` may not exist: if `validateWidget` inlines the loop, add a tiny helper in `validate.go` and use it in both places:

```go
// accepts reports whether c takes source type t.
func accepts(c Component, t string) bool {
	for _, a := range c.Accepts {
		if a == t {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/reporting -count=1`
Expected: PASS (all reporting tests, not only preview; `Validate` must behave as before). If `TestPreviewReturnsRowsWithAProblem/missing column`'s message differs, the expected string is wrong, not the code: `checkColumns` is the authority — fix the test to its exact message.

- [ ] **Step 6: Commit**

```bash
git add internal/reporting/preview.go internal/reporting/preview_test.go internal/reporting/source.go internal/reporting/validate.go
git commit -m "feat(reporting): preview an unsaved widget's sql with given parameters"
```

---

### Task 2: `update_widget` repairs a removed component in one call

**Files:**
- Modify: `internal/reporting/ops_widget.go` (the `w.Component == ""` refusal in `UpdateWidget`)
- Modify: `internal/reporting/ops_test.go` (`TestRemovedComponentUpdate`)
- Modify: `docs/reporting.md` (the rule on removed components; grep `component was removed`)

**Interfaces:**
- Produces: `UpdateWidget` accepts `{Component, Title/Props/Source}` together on a removed-component widget and validates the whole widget.

- [ ] **Step 1: Add the failing case** at the end of `TestRemovedComponentUpdate`, using a fresh removed widget:

```go
	_, id2 := removedWidget(t, svc)
	w, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id2, Component: ptr("table"), Title: ptr("Fixed"),
		Props: json.RawMessage(`{}`), Source: &Source{Type: "sql", Content: "SELECT 1 AS n"}})
	if err != nil || w.Component == nil || *w.Component != "table" || w.Title == nil || *w.Title != "Fixed" {
		t.Errorf("repair in one call = %+v, %v", w, err)
	}
	_, id3 := removedWidget(t, svc)
	_, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id3, Component: ptr("stat"),
		Source: &Source{Type: "sql", Content: "SELECT 'x' AS label"}})
	wantRefusal(t, err, store.ErrInvalid, "stat needs value (number); columns are label")
```

`removedWidget` creates dashboard "DD" each time; a second call clashes on the title only if titles must be unique — if it fails with a conflict, give `removedWidget` a title parameter or create the extra widgets with `mustAdd` on the same dashboard before the `syncReporting` drop. Check `WidgetInfo.Title`'s type (`*string` or `string`) in `read.go` and adjust the assertion.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/reporting -run TestRemovedComponentUpdate -count=1`
Expected: FAIL with `"widget N's component was removed; set component first"`.

- [ ] **Step 3: Relax the refusal** in `UpdateWidget`:

```go
	// A removed component (D14) leaves nothing to check content against,
	// so content alone is refused; with a new component in the same call
	// the whole widget is validated against it below (widget editor D7).
	if w.Component == "" && in.Component == nil && (in.Name != nil || in.Title != nil || in.Props != nil || in.Source != nil) {
		return WidgetInfo{}, refuseRemoved(w.ID)
	}
```

- [ ] **Step 4: Run** `go test ./internal/reporting -count=1` → PASS.

- [ ] **Step 5: Docs.** In `docs/reporting.md`, find where removed components are described (grep `removed`) and state: a widget whose component was removed takes a new `component`, alone or with `title`, `props` and `source` in the same call (validated whole); without a component only `width`, `height` and `after`. Keep the existing error message row as is.

- [ ] **Step 6: Commit**

```bash
git add internal/reporting/ops_widget.go internal/reporting/ops_test.go docs/reporting.md
git commit -m "fix(reporting): let update_widget set a removed widget's component with its content"
```

---

### Task 3: `POST /api/widgets/preview` (REST only)

**Files:**
- Modify: `internal/api/ops_reporting.go` (input struct, handler, `restOnly` registration)
- Modify: `internal/api/expose_test.go` (`restOnly` map gains `"preview_widget": true`)
- Modify: `internal/api/ops_reporting_test.go` (route test)
- Modify: `docs/reporting.md` (REST table row + one sentence)

**Interfaces:**
- Consumes: `reporting.PreviewIn`, `reporting.Preview`, `(*reporting.Service).Preview` (Task 1).
- Produces: `POST /api/widgets/preview`, body `{component, props?, source:{type,content}, project_id?, from?, to?}` → 200 `{columns, rows, truncated, problem?, elapsed_ms}`; 400 `{"error":{"code":"invalid","message":…}}`.

- [ ] **Step 1: Write the failing test** in `ops_reporting_test.go` (pattern: `TestViewRouteIsRESTOnly`; `serveREST`, `newTestRegistrar`, `newTestHost`):

```go
// TestPreviewWidgetRoute: the console runs unsaved SQL with its own
// project and range; rows come back with the reason they do not fit, and
// what cannot run is a 400. No MCP tool previews.
func TestPreviewWidgetRoute(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)

	rec := serveREST(t, r, "POST", "/api/widgets/preview",
		`{"component":"stat","source":{"type":"sql","content":"SELECT 'x' AS value"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST preview = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Columns   []string   `json:"columns"`
		Rows      [][]string `json:"rows"`
		Truncated bool       `json:"truncated"`
		Problem   string     `json:"problem"`
		ElapsedMS *int64     `json:"elapsed_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 1 || got.Problem == "" || got.ElapsedMS == nil {
		t.Errorf("preview = %+v", got)
	}

	rec = serveREST(t, r, "POST", "/api/widgets/preview",
		`{"component":"table","source":{"type":"sql","content":"SELECT :project AS p"}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "pass project_id") {
		t.Errorf("no project = %d %s", rec.Code, rec.Body.String())
	}

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "preview_widget" {
			t.Error("MCP lists preview_widget; the route is REST only")
		}
	}
}
```

Add `"preview_widget": true` to the `restOnly` map in `TestEveryToolChoosesATransport` with a comment `// the console editor's Run query`.

- [ ] **Step 2: Run** `go test ./internal/api -run 'TestPreviewWidgetRoute|TestEveryToolChoosesATransport' -count=1` → FAIL (404 / not registered).

- [ ] **Step 3: Implement** in `ops_reporting.go` next to the other inputs:

```go
type previewWidgetIn struct {
	Component string           `json:"component" jsonschema:"a component from list_components that accepts sql"`
	Props     json.RawMessage  `json:"props,omitempty" jsonschema:"the props to check against the component; default {}"`
	Source    reporting.Source `json:"source" jsonschema:"{type: sql, content}"`
	ProjectID int64            `json:"project_id,omitempty" jsonschema:"bound to :project; required when the sql uses it"`
	From      string           `json:"from,omitempty" jsonschema:"YYYY-MM-DD, bound to :from; with to, required when the sql uses :from or :to"`
	To        string           `json:"to,omitempty" jsonschema:"YYYY-MM-DD, bound to :to; a day after today is clamped to today"`
}

func (h *host) previewWidget(ctx context.Context, in previewWidgetIn) (reporting.Preview, error) {
	return h.rep.Preview(ctx, reporting.PreviewIn{
		Component: in.Component, Props: in.Props, Source: in.Source,
		ProjectID: in.ProjectID, From: in.From, To: in.To,
	})
}
```

Register after the `view` route in `registerReporting`:

```go
	restOnly(r, spec{Name: "preview_widget", Method: "POST", Path: "/api/widgets/preview",
		Description: "The console editor's Run query: run an unsaved widget's sql with the given project and range, uncached and saving nothing. Answers the rows and, in problem, the first way they do not fit the component; agents call add_widget, which validates."},
		h.previewWidget)
```

- [ ] **Step 4: Docs.** In `docs/reporting.md`'s REST table add, after the `view` row:

```
| `POST` | `/api/widgets/preview` | `preview_widget`, REST only: no MCP tool | body: `component`, `props`, `source`, `project_id`, `from`, `to` → `{columns, rows, truncated, problem, elapsed_ms}` |
```

and in the prose above the table (where `view` is explained as the web app's own), one sentence: "The console's widget editor runs unsaved SQL with `POST /api/widgets/preview`, which binds the project and range given and returns the rows with the first way they do not fit the component; agents call `add_widget`, which validates." Mention the new refusal texts in the error catalogue (grep `pass project_id` there; the two new ones are "the sql uses :project; pass project_id", "the sql uses :from or :to; pass from and to", "preview runs sql only; markdown is drawn as it is typed").

- [ ] **Step 5: Run** `go test ./internal/api ./internal/reporting -count=1` → PASS (includes `TestDocumentMatchesRoutes`, OpenAPI tests). Then `make check` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api docs/reporting.md
git commit -m "feat(api): run an unsaved widget's sql from the console with POST /api/widgets/preview"
```

---

### Task 4: Web API client, schema parsing, and the draft model

**Files:**
- Modify: `web/src/lib/api.ts` (types + endpoints)
- Create: `web/src/lib/schema-views.ts`, `web/src/lib/schema-views.test.ts`
- Create: `web/src/lib/widget-draft.ts`, `web/src/lib/widget-draft.test.ts`
- Modify: `web/src/lib/queries.ts` (`schemaViewsQuery`, `archivedWidgetsQuery`)

**Interfaces:**
- Produces (api.ts):
  ```ts
  export interface WidgetSource { type: 'sql' | 'md'; content: string }
  export interface WidgetBody { component: string; title?: string; props?: Record<string, unknown>; source: WidgetSource }
  export interface WidgetContentBody { component?: string; title?: string; props?: Record<string, unknown>; source?: WidgetSource }
  export interface PreviewBody { component: string; props?: Record<string, unknown>; source: { type: 'sql'; content: string }; project_id?: number; from?: string; to?: string }
  export interface PreviewResult extends SqlData { problem?: string; elapsed_ms: number }
  export interface ListedWidget extends Widget { dashboard: DashboardInfo; position: number }
  endpoints.createDashboard(body: { title: string }): Promise<DashboardDetail>
  endpoints.addWidget(dashboardId: number, body: WidgetBody): Promise<Widget>
  endpoints.updateWidget(id: number, body: WidgetLayoutBody | WidgetContentBody): Promise<Widget>
  endpoints.archiveWidget(id: number): Promise<{ status: string }>
  endpoints.restoreWidget(id: number): Promise<{ status: string }>
  endpoints.listWidgets(): Promise<{ widgets: ListedWidget[] }>
  endpoints.previewWidget(body: PreviewBody): Promise<PreviewResult>
  endpoints.schemaViews(): Promise<string>
  ```
- Produces (schema-views.ts): `export interface SchemaTable { name: string; columns: string[] }`, `parseSchemaViews(text: string): SchemaTable[]`.
- Produces (queries.ts): `schemaViewsQuery = { queryKey: ['schema-views'], queryFn: …, staleTime: Infinity }`, `archivedWidgetsQuery = { queryKey: ['widgets'], queryFn: () => endpoints.listWidgets() }`.
- Produces (widget-draft.ts):
  ```ts
  export interface Draft { title: string; component: string | null; props: Record<string, unknown>; sql: string; markdown: string }
  export function draftOf(widget?: Widget): Draft
  export function sourceTypeOf(component: string | null): 'sql' | 'md' | null  // from widgets[component].contract.accepts[0]
  export function sourceOf(d: Draft): WidgetSource | null
  export function addBody(d: Draft): WidgetBody               // throws if no component
  export function changedFields(w: Widget, d: Draft): WidgetContentBody  // only what differs; {} when nothing
  export function isDirty(w: Widget | undefined, d: Draft): boolean
  export function usesParams(sql: string): { project: boolean; range: boolean }
  ```

- [ ] **Step 1: Write failing tests.**

`schema-views.test.ts` — parse the real text: the test reads `internal/api/resources.go`, extracts the `schemaViews` raw string, and checks every `v_*` view and `identities` parses with columns:

```ts
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { parseSchemaViews } from './schema-views'

const goSource = readFileSync(resolve(__dirname, '../../../internal/api/resources.go'), 'utf8')
const text = goSource.match(/const schemaViews = `([\s\S]*?)`/)?.[1] ?? ''

describe('parseSchemaViews', () => {
  it('reads every view the API documents, with its columns', () => {
    const tables = parseSchemaViews(text)
    const names = tables.map((t) => t.name)
    const documented = [...text.matchAll(/^\s+(v_\w+|identities)\(/gm)].map((m) => m[1])
    expect(documented.length).toBeGreaterThan(15)
    expect(names).toEqual(documented)
    expect(tables.find((t) => t.name === 'v_views_daily')?.columns).toEqual([
      'project_id', 'day', 'kind', 'visitors', 'views', 'sessions', 'bounces', 'duration_sec',
    ])
  })
  it('ignores prose and trailing comments', () => {
    expect(parseSchemaViews('Facts:\n  v_x(a, b)  -- note (x)\n1. day columns (UTC)')).toEqual([{ name: 'v_x', columns: ['a', 'b'] }])
  })
})
```

`widget-draft.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import type { Widget } from './api'
import { addBody, changedFields, draftOf, isDirty, sourceOf, usesParams } from './widget-draft'

const line: Widget = {
  widget_id: 7, dashboard_id: 3, name: 'visits', component: 'line', title: 'Visits', width: 6, height: 8,
  props: { curve: 'step' }, source: { type: 'sql', content: 'SELECT day AS x, views AS y FROM v' },
  follows_project: false, follows_range: false,
}

describe('draft', () => {
  it('starts empty for a new widget and from the widget for an edit', () => {
    expect(draftOf()).toEqual({ title: '', component: null, props: {}, sql: '', markdown: '' })
    expect(draftOf(line)).toMatchObject({ title: 'Visits', component: 'line', sql: line.source.content, markdown: '' })
  })
  it('sends only what changed', () => {
    const d = draftOf(line)
    expect(changedFields(line, d)).toEqual({})
    expect(isDirty(line, d)).toBe(false)
    expect(changedFields(line, { ...d, title: 'Daily visits' })).toEqual({ title: 'Daily visits' })
    expect(changedFields(line, { ...d, props: { curve: 'linear' } })).toEqual({ props: { curve: 'linear' } })
    expect(changedFields(line, { ...d, component: 'area', sql: 'SELECT 1' })).toEqual({
      component: 'area', source: { type: 'sql', content: 'SELECT 1' },
    })
  })
  it('keeps sql and markdown apart, picking by the component', () => {
    const d = { ...draftOf(line), component: 'markdown', markdown: '# Hi' }
    expect(sourceOf(d)).toEqual({ type: 'md', content: '# Hi' })
    expect(sourceOf({ ...d, component: 'line' })).toEqual({ type: 'sql', content: line.source.content })
  })
  it('a removed component counts as a change once one is picked', () => {
    const removed = { ...line, component: null }
    expect(changedFields(removed, { ...draftOf(removed), component: 'line' })).toMatchObject({ component: 'line' })
  })
  it('builds an add body', () => {
    expect(addBody({ title: ' A ', component: 'stat', props: {}, sql: 'SELECT 1 AS value', markdown: '' })).toEqual({
      component: 'stat', title: 'A', props: {}, source: { type: 'sql', content: 'SELECT 1 AS value' },
    })
  })
})

describe('usesParams', () => {
  it('finds the parameters the sql uses', () => {
    expect(usesParams('SELECT * FROM v WHERE project_id=:project AND day BETWEEN :from AND :to')).toEqual({ project: true, range: true })
    expect(usesParams('SELECT :to')).toEqual({ project: false, range: true })
  })
  it('ignores them in strings, quoted names and comments', () => {
    expect(usesParams("SELECT ':project' AS x, \":from\" -- :to\n/* :project */ FROM v")).toEqual({ project: false, range: false })
  })
  it('does not match a longer name', () => {
    expect(usesParams('SELECT :projects, :fromage')).toEqual({ project: false, range: false })
  })
})
```

- [ ] **Step 2: Run** `cd web && npx vitest run --testTimeout=30000 src/lib/schema-views.test.ts src/lib/widget-draft.test.ts` → FAIL (modules missing).

- [ ] **Step 3: Implement.**

`schema-views.ts`:

```ts
/** A queryable view or table and its columns, for the SQL editor's completion. */
export interface SchemaTable {
  name: string
  columns: string[]
}

const line = /^\s+([a-z_][a-z0-9_]*)\(([^)]*)\)/i

/**
 * Reads the views out of `GET /api/schema/views`, whose reference lists
 * each as an indented `name(col, col, …)` line followed by an optional
 * `-- note`. Prose lines are not indented that way and are skipped.
 */
export function parseSchemaViews(text: string): SchemaTable[] {
  const out: SchemaTable[] = []
  for (const l of text.split('\n')) {
    const m = l.match(line)
    if (!m) continue
    out.push({ name: m[1], columns: m[2].split(',').map((c) => c.trim()).filter(Boolean) })
  }
  return out
}
```

`widget-draft.ts`:

```ts
import { widgets } from '@/components/widgets'
import type { Widget, WidgetBody, WidgetContentBody, WidgetSource } from './api'

/** What the editor holds while a widget is added or edited: sql and markdown are kept apart, so switching component and back loses neither. */
export interface Draft {
  title: string
  component: string | null
  props: Record<string, unknown>
  sql: string
  markdown: string
}

export function draftOf(widget?: Widget): Draft {
  if (!widget) return { title: '', component: null, props: {}, sql: '', markdown: '' }
  const md = widget.source.type === 'md'
  return {
    title: widget.title ?? '',
    component: widget.component,
    props: widget.props ?? {},
    sql: md ? '' : widget.source.content,
    markdown: md ? widget.source.content : '',
  }
}

/** The source type a component takes: every component accepts exactly one today. */
export function sourceTypeOf(component: string | null): 'sql' | 'md' | null {
  if (!component) return null
  return widgets[component]?.contract.accepts[0] ?? null
}

export function sourceOf(d: Draft): WidgetSource | null {
  const type = sourceTypeOf(d.component)
  if (!type) return null
  return { type, content: type === 'md' ? d.markdown : d.sql }
}

export function addBody(d: Draft): WidgetBody {
  const source = sourceOf(d)
  if (!d.component || !source) throw new Error('no component chosen')
  const title = d.title.trim()
  return { component: d.component, ...(title ? { title } : {}), props: d.props, source }
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

/** The update_widget body for what the draft changed, and nothing else (spec D14). */
export function changedFields(w: Widget, d: Draft): WidgetContentBody {
  const out: WidgetContentBody = {}
  const title = d.title.trim()
  if (title !== (w.title ?? '')) out.title = title
  if (d.component && d.component !== w.component) out.component = d.component
  if (!same(d.props, w.props ?? {})) out.props = d.props
  const source = sourceOf(d)
  if (source && (source.type !== w.source.type || source.content !== w.source.content)) out.source = source
  return out
}

export function isDirty(w: Widget | undefined, d: Draft): boolean {
  if (!w) return !same(d, draftOf())
  return Object.keys(changedFields(w, d)).length > 0
}

/**
 * Which switcher parameters the SQL reads, with string literals, quoted
 * names and comments blanked first; the server binds exactly what
 * readsql.Check finds, so a miss here only costs its 400.
 */
export function usesParams(sql: string): { project: boolean; range: boolean } {
  const code = sql.replace(/'(?:[^']|'')*'|"(?:[^"]|"")*"|`[^`]*`|\[[^\]]*\]|--[^\n]*|\/\*[\s\S]*?(?:\*\/|$)/g, ' ')
  return { project: /:project\b/.test(code), range: /:(?:from|to)\b/.test(code) }
}
```

Note `\b` after `:project` already rejects `:projects` (`s` is a word char).

`api.ts`: add the interfaces from the Interfaces block next to `Widget`, change `updateWidget`'s body type to `WidgetLayoutBody | WidgetContentBody` (update its doc comment: "Moves, resizes or edits a widget of the user's own dashboard"), and add to `endpoints`:

```ts
  /** Creates a user dashboard of its own group, empty (widget editor D20). */
  createDashboard: (body: { title: string }) => api<DashboardDetail>('/api/dashboards', json('POST', body)),
  /** Adds a widget last on a user dashboard; the server validates it whole. */
  addWidget: (dashboardId: number, body: WidgetBody) => api<Widget>(`/api/dashboards/${dashboardId}/widgets`, json('POST', body)),
  archiveWidget: (id: number) => api<{ status: string }>(`/api/widgets/${id}/archive`, json('POST', {})),
  restoreWidget: (id: number) => api<{ status: string }>(`/api/widgets/${id}/restore`, json('POST', {})),
  /** Every widget, archived ones included, with its dashboard. */
  listWidgets: () => api<{ widgets: ListedWidget[] }>('/api/widgets'),
  /** Runs an unsaved widget's SQL with the project and range given (the editor's Run query). */
  previewWidget: (body: PreviewBody) => api<PreviewResult>('/api/widgets/preview', json('POST', body)),
  /** The query schema reference as text; parseSchemaViews reads the views out of it. */
  schemaViews: async () => (await apiBlob('/api/schema/views')).text(),
```

`queries.ts`: add `schemaViewsQuery` and `archivedWidgetsQuery` as in Interfaces.

- [ ] **Step 4: Run** the two test files → PASS; `npx tsc -b --noEmit` → no errors (`use-widget-actions.ts` still compiles with the widened body type).

- [ ] **Step 5: Commit**

```bash
git add web/src/lib
git commit -m "feat(web): add the widget write endpoints, schema parsing and the editor's draft model"
```

---

### Task 5: Props form generated from the contract

**Files:**
- Create: `web/src/lib/props-form.ts`, `web/src/lib/props-form.test.ts`
- Create: `web/src/components/editor/PropsForm.tsx`, `web/src/components/editor/PropsForm.test.tsx`

**Interfaces:**
- Consumes: `widgets` registry (`@/components/widgets`), `Contract`.
- Produces (props-form.ts):
  ```ts
  export type PropField =
    | { key: string; kind: 'enum'; options: string[] }
    | { key: string; kind: 'boolean' }
    | { key: string; kind: 'enum-map'; options: string[] }
    | { key: string; kind: 'json' }
  export function fieldsOf(schema: Record<string, unknown>): PropField[]
  export function carryProps(props: Record<string, unknown>, schema: Record<string, unknown>): Record<string, unknown>
  export function needsHint(contract: Contract): string   // "needs: x (day or text), y (number); optional: series"
  ```
- Produces (PropsForm.tsx): `export default function PropsForm(p: { component: string; value: Record<string, unknown>; onChange(next: Record<string, unknown>): void; columns: string[]; onValidity(ok: boolean): void })`

- [ ] **Step 1: Failing tests** `props-form.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { widgets } from '@/components/widgets'
import { carryProps, fieldsOf, needsHint } from './props-form'

describe('fieldsOf', () => {
  it('turns every component contract into fields', () => {
    for (const [name, mod] of Object.entries(widgets)) {
      const fields = fieldsOf(mod.contract.props)
      const keys = Object.keys((mod.contract.props.properties as object | undefined) ?? {})
      expect(fields.map((f) => f.key), name).toEqual(keys)
    }
  })
  it('knows enums, booleans, enum maps and falls back to json', () => {
    expect(fieldsOf(widgets.table.contract.props)).toEqual([
      { key: 'formats', kind: 'enum-map', options: ['number', 'percent', 'duration'] },
      { key: 'colorscale', kind: 'json' },
      { key: 'mode', kind: 'enum', options: ['local', 'remote'] },
    ])
    expect(fieldsOf(widgets.pie.contract.props)).toContainEqual({ key: 'donut', kind: 'boolean' })
  })
})

describe('carryProps', () => {
  it('keeps what the new schema accepts and drops the rest', () => {
    const line = widgets.line.contract.props
    expect(carryProps({ format: 'percent', curve: 'step', donut: true }, widgets.pie.contract.props)).toEqual({ format: 'percent' })
    expect(carryProps({ curve: 'bogus' }, line)).toEqual({})
    expect(carryProps({ formats: { a: 'number' } }, line)).toEqual({})
  })
})

describe('needsHint', () => {
  it('names required and optional columns', () => {
    expect(needsHint(widgets.line.contract)).toBe('needs x (day or text), y (number); optional series (text)')
    expect(needsHint(widgets.table.contract)).toBe('any columns')
    expect(needsHint(widgets.markdown.contract)).toBe('markdown text')
  })
})
```

(The table `formats`/`colorscale`/`mode` order and the option lists must match `web/src/components/widgets/table.tsx`'s contract; read it and fix the expected values if they differ — the contract is the authority.)

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement** `props-form.ts`:

```ts
import type { Contract } from '@/components/widgets/types'

export type PropField =
  | { key: string; kind: 'enum'; options: string[] }
  | { key: string; kind: 'boolean' }
  | { key: string; kind: 'enum-map'; options: string[] }
  | { key: string; kind: 'json' }

type Schema = Record<string, unknown>

const enumOf = (s: Schema | undefined): string[] | null =>
  Array.isArray(s?.enum) && s.enum.every((v) => typeof v === 'string') ? (s.enum as string[]) : null

/** One form field per prop, in the schema's order (spec D11). */
export function fieldsOf(schema: Schema): PropField[] {
  const props = (schema.properties ?? {}) as Record<string, Schema>
  return Object.entries(props).map(([key, s]): PropField => {
    const options = enumOf(s)
    if (options) return { key, kind: 'enum', options }
    if (s.type === 'boolean') return { key, kind: 'boolean' }
    const values = enumOf(s.additionalProperties as Schema | undefined)
    if (s.type === 'object' && values) return { key, kind: 'enum-map', options: values }
    return { key, kind: 'json' }
  })
}

function fits(field: PropField, v: unknown): boolean {
  switch (field.kind) {
    case 'enum':
      return typeof v === 'string' && field.options.includes(v)
    case 'boolean':
      return typeof v === 'boolean'
    case 'enum-map':
      return typeof v === 'object' && v !== null && !Array.isArray(v) &&
        Object.values(v).every((x) => typeof x === 'string' && field.options.includes(x))
    case 'json':
      return true
  }
}

/** The props a component switch keeps: those whose key and value the new schema accepts (spec D10). */
export function carryProps(props: Record<string, unknown>, schema: Schema): Record<string, unknown> {
  const byKey = new Map(fieldsOf(schema).map((f) => [f.key, f]))
  return Object.fromEntries(Object.entries(props).filter(([k, v]) => {
    const f = byKey.get(k)
    return f !== undefined && fits(f, v)
  }))
}

/** One line on what the query must return, from the contract's inputs. */
export function needsHint(c: Contract): string {
  if (!c.accepts.includes('sql')) return 'markdown text'
  if (c.inputs.open) return 'any columns'
  const col = (x: { name: string; types: string[] }) => `${x.name} (${x.types.join(' or ')})`
  const req = c.inputs.columns.filter((x) => !x.optional).map(col)
  const opt = c.inputs.columns.filter((x) => x.optional).map(col)
  return `needs ${req.join(', ')}${opt.length ? `; optional ${opt.join(', ')}` : ''}`
}
```

Note: `json` kind keeps any value on carry-over; the server's `problem`/refusal catches a wrong shape.

- [ ] **Step 4: Test and build `PropsForm.tsx`.** Test first (`PropsForm.test.tsx`, Testing Library + `userEvent`):

```tsx
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import PropsForm from './PropsForm'

describe('PropsForm', () => {
  it('sets an enum and clears it back to the default', async () => {
    const onChange = vi.fn()
    render(<PropsForm component="line" value={{ curve: 'step' }} columns={[]} onChange={onChange} onValidity={() => {}} />)
    await userEvent.selectOptions(screen.getByLabelText('curve'), 'linear')
    expect(onChange).toHaveBeenLastCalledWith({ curve: 'linear' })
    await userEvent.selectOptions(screen.getByLabelText('curve'), '')
    expect(onChange).toHaveBeenLastCalledWith({})
  })
  it('toggles a boolean', async () => {
    const onChange = vi.fn()
    render(<PropsForm component="pie" value={{}} columns={[]} onChange={onChange} onValidity={() => {}} />)
    await userEvent.click(screen.getByRole('switch', { name: 'donut' }))
    expect(onChange).toHaveBeenLastCalledWith({ donut: true })
  })
  it('edits a column → format map with the run's columns offered', async () => {
    const onChange = vi.fn()
    render(<PropsForm component="table" value={{}} columns={['page', 'views']} onChange={onChange} onValidity={() => {}} />)
    await userEvent.selectOptions(screen.getByLabelText('formats: views'), 'percent')
    expect(onChange).toHaveBeenLastCalledWith({ formats: { views: 'percent' } })
  })
  it('marks bad json and reports it invalid', async () => {
    const onValidity = vi.fn()
    render(<PropsForm component="table" value={{}} columns={[]} onChange={() => {}} onValidity={onValidity} />)
    await userEvent.type(screen.getByLabelText('colorscale'), '[oops')
    expect(screen.getByText(/not valid JSON/)).toBeInTheDocument()
    expect(onValidity).toHaveBeenLastCalledWith(false)
  })
  it('edits everything as one JSON object', async () => {
    const onChange = vi.fn()
    render(<PropsForm component="line" value={{ curve: 'step' }} columns={[]} onChange={onChange} onValidity={() => {}} />)
    await userEvent.click(screen.getByRole('button', { name: 'Edit props as JSON' }))
    const box = screen.getByLabelText('Props JSON')
    await userEvent.clear(box)
    await userEvent.type(box, '{{"format":"percent"}')
    expect(onChange).toHaveBeenLastCalledWith({ format: 'percent' })
  })
})
```

Implementation notes for `PropsForm.tsx` (write it to satisfy the tests; use `@/components/ui/native-select` for selects so `selectOptions` works, `@/components/ui/switch` for booleans, `@/components/ui/textarea` for JSON, `@/components/ui/label`):
- Enum: `<NativeSelect aria-label={key}>` with a first option `value=""` labelled `default`; choosing `''` deletes the key.
- Boolean: `<Switch aria-label={key} checked={value[key] === true} onCheckedChange={(on) => …}>`; off deletes the key (the component's default is off).
- Enum map: one row per name in `union(columns, Object.keys(value[key] ?? {}))`, each a `NativeSelect aria-label={`${key}: ${col}`}` with `default` + options; an empty result deletes the entry and then the key if empty. With no columns and no entries, show the muted line "Run the query to list columns".
- JSON field: a `Textarea aria-label={key}` holding a local string (initially `JSON.stringify(value[key])` or `''`); on change parse: empty → delete key; valid → set; invalid → keep the text, show `<p className="text-xs text-destructive">not valid JSON</p>`, and call `onValidity(false)`. Track invalid keys in a `Set` and call `onValidity(set.size === 0)` whenever it changes.
- A `Button variant="link"` "Edit props as JSON" toggles one `Textarea aria-label="Props JSON"` for the whole object (parse on change, same validity rule, must be an object); its label flips to "Edit props as a form".
- A component with no props shows the muted line "No props".
- Layout: `grid grid-cols-1 gap-3`; labels above controls; everything `min-w-0`.

- [ ] **Step 5: Run** both test files → PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/lib/props-form.ts web/src/lib/props-form.test.ts web/src/components/editor
git commit -m "feat(web): generate a widget's props form from its component contract"
```

---

### Task 6: CodeMirror editor

**Files:**
- Modify: `web/package.json`, `web/package-lock.json` (`npm install @codemirror/state @codemirror/view @codemirror/commands @codemirror/language @codemirror/autocomplete @codemirror/lang-sql @codemirror/lang-markdown`; also change `"description"` to `"Source of the dashboards app, embedded from internal/reporting/ui."`)
- Create: `web/src/lib/sql-completion.ts`, `web/src/lib/sql-completion.test.ts`
- Create: `web/src/components/editor/CodeEditor.tsx`

**Interfaces:**
- Consumes: `SchemaTable` (Task 4).
- Produces:
  ```ts
  // sql-completion.ts
  export function sqlNamespace(tables: SchemaTable[]): Record<string, string[]>
  export function paramCompletions(context: CompletionContext): CompletionResult | null
  // CodeEditor.tsx (default export; lazy-loaded by the editor modal)
  export interface CodeEditorProps {
    language: 'sql' | 'markdown'
    value: string
    onChange(next: string): void
    onRun?: () => void           // Mod-Enter
    tables?: SchemaTable[]       // sql completion
    label: string                // aria-label of the editable area
  }
  ```

- [ ] **Step 1: Failing test** `sql-completion.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { EditorState } from '@codemirror/state'
import { CompletionContext } from '@codemirror/autocomplete'
import { paramCompletions, sqlNamespace } from './sql-completion'

describe('sql completion', () => {
  it('maps views to their columns', () => {
    expect(sqlNamespace([{ name: 'v_a', columns: ['day', 'n'] }])).toEqual({ v_a: ['day', 'n'] })
  })
  it('offers the three parameters after a colon', () => {
    const state = EditorState.create({ doc: 'WHERE project_id = :pr' })
    const res = paramCompletions(new CompletionContext(state, state.doc.length, false))
    expect(res?.from).toBe(19)
    expect(res?.options.map((o) => o.label)).toEqual([':project', ':from', ':to'])
  })
  it('stays quiet without a colon', () => {
    const state = EditorState.create({ doc: 'SELECT pr' })
    expect(paramCompletions(new CompletionContext(state, state.doc.length, false))).toBeNull()
  })
})
```

- [ ] **Step 2: Install deps, run** → FAIL (module missing).

- [ ] **Step 3: Implement** `sql-completion.ts`:

```ts
import type { CompletionContext, CompletionResult } from '@codemirror/autocomplete'
import type { SchemaTable } from './schema-views'

/** The views and their columns, as lang-sql's schema option takes them. */
export function sqlNamespace(tables: SchemaTable[]): Record<string, string[]> {
  return Object.fromEntries(tables.map((t) => [t.name, t.columns]))
}

const params = [
  { label: ':project', detail: 'the project shown' },
  { label: ':from', detail: 'first day, YYYY-MM-DD' },
  { label: ':to', detail: 'last day, YYYY-MM-DD' },
]

/** :project, :from and :to, the only parameters a widget gets. */
export function paramCompletions(context: CompletionContext): CompletionResult | null {
  const word = context.matchBefore(/:\w*/)
  if (!word) return null
  return { from: word.from, options: params.map((p) => ({ ...p, type: 'variable' })), validFor: /^:\w*$/ }
}
```

`CodeEditor.tsx` — a thin wrapper (no React wrapper library):

```tsx
import { useEffect, useRef } from 'react'
import { autocompletion, closeBrackets, completionKeymap } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { markdown } from '@codemirror/lang-markdown'
import { sql, SQLite } from '@codemirror/lang-sql'
import { bracketMatching, defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { Compartment, EditorState, Prec } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, placeholder } from '@codemirror/view'
import { paramCompletions, sqlNamespace } from '@/lib/sql-completion'
import type { SchemaTable } from '@/lib/schema-views'

export interface CodeEditorProps { /* as in Interfaces */ }

// Colors from the app's tokens, so light and dark both follow the theme.
const theme = EditorView.theme({
  '&': { fontSize: '13px', backgroundColor: 'var(--background)', color: 'var(--foreground)', height: '100%' },
  '.cm-content': { fontFamily: 'var(--font-mono, ui-monospace, monospace)', caretColor: 'var(--foreground)' },
  '.cm-gutters': { backgroundColor: 'var(--muted)', color: 'var(--muted-foreground)', border: 'none' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { overflow: 'auto' },
})

/**
 * CodeMirror 6 for a widget's SQL (SQLite, completion of the views, their
 * columns and the three parameters) or its markdown. Mod-Enter runs.
 * Uncontrolled inside: `value` seeds it and replaces the text only when it
 * differs from what the editor holds (a reset), so typing never loops.
 */
export default function CodeEditor({ language, value, onChange, onRun, tables, label }: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const lang = useRef(new Compartment())
  const latest = useRef({ onChange, onRun })
  latest.current = { onChange, onRun }

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(), history(), bracketMatching(), closeBrackets(),
          syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
          autocompletion(),
          keymap.of([...completionKeymap, ...defaultKeymap, ...historyKeymap]),
          Prec.highest(keymap.of([{ key: 'Mod-Enter', run: () => (latest.current.onRun?.(), true) }])),
          EditorView.lineWrapping, theme,
          placeholder(language === 'sql' ? 'SELECT …' : '# Notes'),
          EditorView.contentAttributes.of({ 'aria-label': label }),
          EditorView.updateListener.of((u) => u.docChanged && latest.current.onChange(u.state.doc.toString())),
          lang.current.of([]),
        ],
      }),
    })
    view.current = v
    return () => v.destroy()
    // eslint-disable-next-line react-hooks/exhaustive-deps -- created once; value/language follow below
  }, [])

  useEffect(() => {
    view.current?.dispatch({
      effects: lang.current.reconfigure(
        language === 'sql'
          ? [sql({ dialect: SQLite, schema: sqlNamespace(tables ?? []), upperCaseKeywords: true }),
             SQLite.language.data.of({ autocomplete: paramCompletions })]
          : markdown()
      ),
    })
  }, [language, tables])

  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
  }, [value])

  return <div ref={host} data-slot="code-editor" className="h-full min-h-40 min-w-0 overflow-hidden rounded-md border" />
}
```

If `SQLite.language.data.of` is not the way this lang-sql version adds a completion source, use `sql(...).language.data.of({ autocomplete: paramCompletions })` or `autocompletion({ override: [...] })` combined with `schemaCompletionSource` — check `node_modules/@codemirror/lang-sql/dist/index.d.ts` and keep both table/column and parameter completion working.

- [ ] **Step 4: Run** `npx vitest run --testTimeout=30000 src/lib/sql-completion.test.ts` → PASS; `npx tsc -b --noEmit` → clean.

- [ ] **Step 5: Commit**

```bash
git add web/package.json web/package-lock.json web/src/lib/sql-completion.ts web/src/lib/sql-completion.test.ts web/src/components/editor/CodeEditor.tsx
git commit -m "feat(web): add a codemirror editor with sqlite completion of views, columns and parameters"
```

---

### Task 7: The widget editor modal

**Files:**
- Create: `web/src/components/editor/WidgetEditor.tsx`
- Create: `web/src/components/editor/ResultsPane.tsx`
- Create: `web/src/components/editor/component-icons.ts`
- Create: `web/src/components/editor/WidgetEditor.test.tsx`

**Interfaces:**
- Consumes: Task 4 (`Draft`, `draftOf`, `addBody`, `changedFields`, `isDirty`, `sourceOf`, `sourceTypeOf`, `usesParams`, `endpoints.previewWidget`, `schemaViewsQuery`, `projectsQuery`), Task 5 (`PropsForm`, `carryProps`, `needsHint`), Task 6 (`CodeEditor`), `RangeSwitcher`/`RangeValue`, `widgets`, `WidgetFrame`, `ROW_PX`/`GAP_PX` from `@/lib/grid`.
- Produces:
  ```ts
  export interface EditorDefaults { projectId?: number; range: RangeValue; timezone: string }
  export interface WidgetEditorProps {
    open: boolean
    onOpenChange(open: boolean): void
    /** Absent: adding a widget. */
    widget?: Widget
    defaults: EditorDefaults
    /** Saves the draft; resolves on success (the modal closes), throws ApiError/Error to show its message in the banner. */
    onSave(draft: Draft): Promise<void>
  }
  export default function WidgetEditor(p: WidgetEditorProps): JSX.Element
  export function shouldKeepOpenOnEscape(target: EventTarget | null, doc: Document): boolean
  export const componentIcons: Record<string, LucideIcon>  // component-icons.ts
  ```

- [ ] **Step 1: Failing tests** `WidgetEditor.test.tsx`. Mock `CodeEditor` with a textarea (CodeMirror needs layout jsdom lacks) and the API:

```tsx
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, endpoints, type Widget } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import WidgetEditor, { shouldKeepOpenOnEscape } from './WidgetEditor'

vi.mock('./CodeEditor', () => ({
  default: ({ value, onChange, label, onRun }: { value: string; onChange: (v: string) => void; label: string; onRun?: () => void }) => (
    <textarea aria-label={label} value={value} onChange={(e) => onChange(e.target.value)}
      onKeyDown={(e) => e.key === 'Enter' && e.metaKey && onRun?.()} />
  ),
}))

const defaults = { projectId: 1, range: { range: '7d', from: '2026-09-08', to: '2026-09-14' }, timezone: 'UTC' }
const line: Widget = {
  widget_id: 7, dashboard_id: 3, name: 'v', component: 'line', title: 'Visits', width: 6, height: 8, props: {},
  source: { type: 'sql', content: 'SELECT day AS x, views AS y FROM v WHERE project_id=:project' },
  follows_project: true, follows_range: false,
}

beforeEach(() => {
  vi.spyOn(endpoints, 'schemaViews').mockResolvedValue('  v_views_daily(project_id, day, views)\n')
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [{ project_id: 1, name: 'Acme', allowed_origins: [] }] } as never)
})

function open(props: Partial<Parameters<typeof WidgetEditor>[0]> = {}) {
  const onSave = vi.fn().mockResolvedValue(undefined)
  const onOpenChange = vi.fn()
  renderWithProviders(<WidgetEditor open onOpenChange={onOpenChange} defaults={defaults} onSave={onSave} {...props} />)
  return { onSave, onOpenChange }
}

describe('WidgetEditor', () => {
  it('disables Save until a visualization is chosen', async () => {
    open()
    expect(await screen.findByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('runs the query with the dashboard project and shows preview, table and problem', async () => {
    const preview = vi.spyOn(endpoints, 'previewWidget').mockResolvedValue({
      columns: ['x'], rows: [['2026-09-01']], truncated: false, problem: 'line needs y (number); columns are x', elapsed_ms: 4,
    })
    open({ widget: line })
    await userEvent.click(await screen.findByRole('button', { name: /Run query/ }))
    expect(preview).toHaveBeenCalledWith(expect.objectContaining({ component: 'line', project_id: 1, source: { type: 'sql', content: line.source.content } }))
    expect(await screen.findByText('line needs y (number); columns are x')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: 'Table' }))
    expect(screen.getByRole('cell', { name: '2026-09-01' })).toBeInTheDocument()
    expect(preview.mock.calls[0][0]).not.toHaveProperty('from') // the sql reads no range
  })

  it('shows a refusal in place of the results', async () => {
    vi.spyOn(endpoints, 'previewWidget').mockRejectedValue(new ApiError(400, 'invalid', 'no such table: v'))
    open({ widget: line })
    await userEvent.click(await screen.findByRole('button', { name: /Run query/ }))
    expect(await screen.findByText('no such table: v')).toBeInTheDocument()
  })

  it('marks results stale when the sql changes after a run', async () => {
    vi.spyOn(endpoints, 'previewWidget').mockResolvedValue({ columns: ['x', 'y'], rows: [['a', '1']], truncated: false, elapsed_ms: 1 })
    open({ widget: line })
    await userEvent.click(await screen.findByRole('button', { name: /Run query/ }))
    await screen.findByText(/1 row/)
    await userEvent.type(screen.getByLabelText('SQL'), ' ')
    expect(screen.getByText('Changed since last run')).toBeInTheDocument()
  })

  it('saves once, then closes', async () => {
    let resolve!: () => void
    const onSave = vi.fn(() => new Promise<void>((r) => (resolve = r)))
    const { onOpenChange } = open({ widget: line, onSave })
    await userEvent.clear(await screen.findByLabelText('Title'))
    await userEvent.type(screen.getByLabelText('Title'), 'Daily{Enter}')
    const save = screen.getByRole('button', { name: 'Save' })
    await userEvent.click(save)
    expect(save).toBeDisabled()
    await userEvent.click(save)
    expect(onSave).toHaveBeenCalledTimes(1)
    resolve()
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it('keeps the input and shows a refusal in a banner', async () => {
    const onSave = vi.fn().mockRejectedValue(new ApiError(400, 'invalid', 'line needs y (number); columns are x'))
    open({ widget: line, onSave })
    await userEvent.type(await screen.findByLabelText('Title'), '!')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('line needs y')
    expect(screen.getByLabelText('Title')).toHaveValue('Visits!')
  })

  it('asks before discarding changes', async () => {
    const { onOpenChange } = open({ widget: line })
    await userEvent.type(await screen.findByLabelText('Title'), '!')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(await screen.findByText('Discard changes?')).toBeInTheDocument()
    expect(onOpenChange).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Discard' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('switching component keeps fitting props and the sql', async () => {
    open({ widget: { ...line, props: { format: 'percent', curve: 'step' } } })
    await userEvent.click(await screen.findByRole('combobox', { name: 'Visualization' }))
    await userEvent.click(await screen.findByRole('option', { name: /pie/ }))
    expect(screen.getByLabelText('format')).toHaveValue('percent')
    expect(screen.getByLabelText('SQL')).toHaveValue(line.source.content)
  })

  it('a markdown component has no Run and previews the text', async () => {
    open({ widget: { ...line, component: 'markdown', source: { type: 'md', content: '# Hello' }, props: {} } })
    expect(await screen.findByRole('heading', { name: 'Hello' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Run query/ })).toBeNull()
  })
})

describe('shouldKeepOpenOnEscape', () => {
  it('keeps the dialog open while an autocomplete list is showing', () => {
    document.body.innerHTML = '<div class="cm-editor"><div class="cm-content"></div><div class="cm-tooltip-autocomplete"></div></div>'
    expect(shouldKeepOpenOnEscape(document.querySelector('.cm-content'), document)).toBe(true)
    document.body.innerHTML = '<div class="cm-editor"><div class="cm-content"></div></div>'
    expect(shouldKeepOpenOnEscape(document.querySelector('.cm-content'), document)).toBe(false)
  })
})
```

Check `ApiError`'s constructor signature in `api.ts` and the `select.tsx` primitive's roles (radix Select renders `combobox` + `option`); adapt the queries, not the behaviour.

- [ ] **Step 2: Run** → FAIL (module missing).

- [ ] **Step 3: Implement.**

`component-icons.ts`: a `Record<string, LucideIcon>` for all 18 components (e.g. area `AreaChartIcon`, bar `BarChart3Icon`, bar_list `ListOrderedIcon`, calendar `CalendarDaysIcon`, combo `ChartColumnBigIcon`, funnel `FilterIcon`, heatmap `Grid3x3Icon`, line `LineChartIcon`, map `MapIcon`, markdown `TextIcon`, pie `PieChartIcon`, radar `RadarIcon`, radial `CircleGaugeIcon`, sankey `WorkflowIcon`, scatter `ScatterChartIcon`, stat `HashIcon`, table `TableIcon`, treemap `LayoutDashboardIcon`); use names that exist in the installed lucide-react (check `node_modules/lucide-react/dist/lucide-react.d.ts`), fallback `SquareIcon`. A vitest line in WidgetEditor.test asserting every `widgets` key has an icon is welcome.

`ResultsPane.tsx` — props `{ component: string | null; props: Record<string, unknown>; height: number; state: RunState; stale: boolean }` where

```ts
export type RunState =
  | { kind: 'idle' }
  | { kind: 'running'; last?: PreviewResult }
  | { kind: 'done'; result: PreviewResult }
  | { kind: 'failed'; message: string }
```

It renders: idle → muted "Run the query to see its rows" (⌘↵ hint); failed → an `Alert variant="destructive"` with the message; done/running → a status line (`{n} row(s) · {elapsed} ms`, plus "truncated to {n} rows" when `truncated`), a warning `Alert` with `problem` when set, `Tabs` with `Preview` (default) and `Table`. Preview: a `WidgetFrame` of fixed height `height * ROW_PX + (height - 1) * GAP_PX` px, with `<Suspense fallback={<WidgetSkeleton widget={{ component, props }} />}><Component data={result} props={props} /></Suspense>` (as `pages/gallery/ComponentEntry.tsx` does; a remote-mode table is drawn as local: pass `{ ...props, mode: undefined }` for `table`). Table: the `@/components/ui/table` primitives inside `overflow-auto max-h-80` with `whitespace-nowrap` cells. When `stale`, wrap both in `opacity-60` and show `Changed since last run` next to the status line. While `running`, show a spinner in the status line and keep `last` dimmed.

`WidgetEditor.tsx` — structure:

```tsx
export function shouldKeepOpenOnEscape(target: EventTarget | null, doc: Document): boolean {
  const inEditor = target instanceof Element && target.closest('.cm-editor') !== null
  return inEditor && doc.querySelector('.cm-tooltip-autocomplete') !== null
}

const CodeEditor = lazy(() => import('./CodeEditor'))
```

State: `draft` (`useState(() => draftOf(widget))`, reset when `open` turns true or `widget?.widget_id` changes), `runSel` (`{ projectId: defaults.projectId ?? firstLive(projects)?.project_id, range: defaults.range }`), `run: RunState`, `ranWith` (JSON of `{component? no — sql, projectId, from, to}` the last run used: stale = `ranWith !== current`), `propsValid` (from PropsForm), `saving`, `saveError`, `confirmDiscard`.

- Header: `DialogTitle` "Add widget" / "Edit widget"; `Input aria-label="Title"` (Enter in it does nothing special — the form's submit is Save).
- Left column: a radix `Select` labelled `Visualization` listing `Object.keys(widgets).sort()` with `componentIcons[name]` and the name; changing it sets `component` and `props = carryProps(draft.props, widgets[next].contract.props)`. Below: `needsHint(contract)` muted, then `PropsForm` with `columns = last result's columns`.
- Right column, sql: a row with label "SQL", and when `usesParams(draft.sql).project` a project `Select` (`aria-label="Project"`, live projects), when `.range` the `RangeSwitcher` (value `runSel.range`, `timezone`); then `<Suspense fallback={<Skeleton className="h-40" />}><CodeEditor language="sql" label="SQL" …/></Suspense>` in a box `h-48 lg:h-56`; then `Button` `Run query` with `Kbd` ⌘↵; then `ResultsPane`.
- Right column, markdown (`sourceTypeOf(component) === 'md'`): `CodeEditor language="markdown" label="Markdown"` beside the rendered `widgets.markdown.default` with `data={{ markdown: draft.markdown }}` — stacked on phone, `grid-cols-1 lg:grid-cols-2`.
- No component chosen: right column shows the SQL editor anyway (the user may write SQL first); Run is disabled with a tooltip-free muted note "Choose a visualization to run".
- Run: `endpoints.previewWidget({ component, props: draft.props, source: { type: 'sql', content: draft.sql }, ...(u.project ? { project_id } : {}), ...(u.range ? { from, to } : {}) })` → `done`; `ApiError` → `failed` with its message; `TypeError` → `failed` "Couldn't reach the server".
- Footer: `Cancel` and `Save` (`disabled={!draft.component || !propsValid || saving || (widget && !isDirty(widget, draft))}`; for an add, also disabled when the source content is blank). Save: `setSaving(true)`; `await onSave(draft)` → `onOpenChange(false)`; catch → `saveError = message` shown in `<Alert role="alert" variant="destructive">` above the footer; finally `setSaving(false)`. Guard re-entry with a ref so a second click while saving does nothing.
- Close (✕, Escape, overlay, Cancel): if `isDirty(widget, draft)` → `confirmDiscard = true` (an `AlertDialog` "Discard changes?" with `Keep editing` / `Discard`), else `onOpenChange(false)`. `DialogContent onEscapeKeyDown={(e) => shouldKeepOpenOnEscape(e.target, document) && e.preventDefault()}` and `onInteractOutside` routed through the same dirty check (`e.preventDefault()` then ask).
- Layout classes: `DialogContent className="flex max-h-[85vh] w-full flex-col gap-4 sm:max-w-[1100px] max-sm:h-dvh max-sm:max-h-dvh max-sm:max-w-none max-sm:rounded-none"`; body `grid min-h-0 flex-1 grid-cols-1 gap-4 overflow-y-auto lg:grid-cols-[minmax(0,320px)_minmax(0,1fr)]`; every column `min-w-0`.

- [ ] **Step 4: Run** `npx vitest run --testTimeout=30000 src/components/editor` → PASS; `npx tsc -b --noEmit` → clean.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/editor
git commit -m "feat(web): add the widget editor with run query, preview and table results"
```

---

### Task 8: Entry points on the dashboard: Add widget, Edit…, Rename, Archive

**Files:**
- Create: `web/src/hooks/use-widget-edits.ts`, `web/src/hooks/use-widget-edits.test.tsx`
- Create: `web/src/components/editor/WidgetEditing.tsx` (provider, context, `AddWidgetButton`)
- Create: `web/src/components/WidgetTitleField.tsx`, `web/src/components/WidgetTitleField.test.tsx`
- Modify: `web/src/components/WidgetFrame.tsx` (optional `titleSlot`)
- Modify: `web/src/components/WidgetCard.tsx` (menu items, inline rename)
- Modify: `web/src/components/PageStates.tsx` (`NoWidgets` takes an optional `action`)
- Modify: `web/src/pages/Dashboard.tsx`, `web/src/components/projects/ProjectDashboardTab.tsx` (wrap in the provider when arrangeable; header button; empty state)
- Modify: `web/src/pages/Dashboard.test.tsx` (entry points shown only on editable dashboards)

**Interfaces:**
- Consumes: `WidgetEditor`, `EditorDefaults` (Task 7); `addBody`, `changedFields`, `Draft` (Task 4); endpoints (Task 4).
- Produces:
  ```ts
  // use-widget-edits.ts
  export interface WidgetEdits {
    add(dashboardId: number, draft: Draft): Promise<void>            // throws on refusal (the editor's banner shows it)
    update(widget: Widget, draft: Draft): Promise<void>              // throws on refusal; no request when nothing changed
    rename(widget: Widget, title: string): Promise<boolean>          // toast on refusal; true when saved or unchanged
    archive(widget: Widget): Promise<void>                           // toast "Widget archived" + Undo
    restore(widget: { widget_id: number; dashboard_id: number }): Promise<boolean>
  }
  export function useWidgetEdits(): WidgetEdits
  // WidgetEditing.tsx
  export interface WidgetEditing { edit(widget?: Widget): void; rename(widget: Widget, title: string): Promise<boolean>; archive(widget: Widget): void }
  export function WidgetEditingProvider(p: { dashboardId: number; defaults: EditorDefaults; children: ReactNode }): JSX.Element
  export function useWidgetEditing(): WidgetEditing | null   // null outside an editable dashboard
  export function AddWidgetButton(): JSX.Element | null
  ```

- [ ] **Step 1: Failing tests.**

`use-widget-edits.test.tsx` (pattern: `use-widget-actions.test.tsx`; `renderHook` with the query client wrapper from `@/test/render`):
- `add` posts `addBody(draft)` to `endpoints.addWidget(dashboardId, …)` and invalidates `['dashboard', dashboardId]` and `['dashboards']` (live widget counts change);
- `update` sends exactly `changedFields` and skips the request when it is `{}`;
- `rename(widget, '  ')` and `rename(widget, widget.title)` make no request and resolve `true`; `rename(widget, 'New')` PATCHes `{ title: 'New' }`; a refusal toasts its message and resolves `false`;
- `archive` calls `archiveWidget`, invalidates the dashboard, and toasts `Widget archived` whose `Undo` action calls `restoreWidget` (spy on `toast` from `sonner` as `use-dashboard-actions.test.tsx` does);
- `restore` invalidates `['widgets']` and the dashboard.

`WidgetTitleField.test.tsx`: Enter saves the trimmed title; Escape cancels without a call; blur with an unchanged value cancels; an empty value cancels; a save resolving `false` keeps the field open.

`Dashboard.test.tsx`: add cases that a user dashboard shows `Add widget` (header and empty state) and the card menu shows `Edit…`, `Rename`, `Archive`; a system dashboard and `list.dev` show none of them. Follow the existing test's fixtures/mocks for `endpoints.dashboard`, `widgetData`.

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement.**

`use-widget-edits.ts`: like `use-widget-actions.ts`/`use-dashboard-actions.ts` (toasts via `sonner`, `ApiError` messages, `TypeError` → "Couldn't reach the server"). `add`/`update` rethrow after nothing (they don't toast: the editor shows the banner). Invalidate `['dashboard', id]`, `['dashboards']`, and `['widgets']` where relevant. `archive`: `toast('Widget archived', { action: { label: 'Undo', onClick: () => void restore(widget) } })`.

`WidgetEditing.tsx`:

```tsx
const Ctx = createContext<WidgetEditing | null>(null)
const WidgetEditor = lazy(() => import('./WidgetEditor'))

/**
 * Makes a user dashboard's widgets editable from the page (spec D8): the
 * header's Add widget, the empty state's, and each card's Edit…, Rename
 * and Archive read this; outside it (a system dashboard, reporting dev,
 * a page being left) they are not shown. The editor loads on first open.
 */
export function WidgetEditingProvider({ dashboardId, defaults, children }: Props) {
  const edits = useWidgetEdits()
  const [editing, setEditing] = useState<{ widget?: Widget } | null>(null)
  const [mounted, setMounted] = useState(false)
  const value = useMemo<WidgetEditing>(() => ({
    edit: (widget) => { setMounted(true); setEditing({ widget }) },
    rename: (widget, title) => edits.rename(widget, title),
    archive: (widget) => void edits.archive(widget),
  }), [edits])
  return (
    <Ctx.Provider value={value}>
      {children}
      {mounted && (
        <Suspense fallback={null}>
          <WidgetEditor
            open={editing !== null}
            onOpenChange={(o) => !o && setEditing(null)}
            widget={editing?.widget}
            defaults={defaults}
            onSave={(draft) => (editing?.widget ? edits.update(editing.widget, draft) : edits.add(dashboardId, draft))}
          />
        </Suspense>
      )}
    </Ctx.Provider>
  )
}

export function useWidgetEditing() { return useContext(Ctx) }

export function AddWidgetButton() {
  const editing = useWidgetEditing()
  if (!editing) return null
  return <Button variant="outline" size="sm" onClick={() => editing.edit()}><PlusIcon />Add widget</Button>
}
```

Make sure `useWidgetEdits()` returns a stable object (`useMemo` over `useCallback`s) so the context value does not change every render — `WidgetCard` is memoized and reads it.

`WidgetFrame.tsx`: add `titleSlot?: ReactNode` — when set it replaces the `<h3>` (the row renders when `title || badge || titleSlot`).

`WidgetTitleField.tsx`: the `ProjectName` pattern scaled to a card title (`h-7 text-sm`, `aria-label="Widget title"`, ✓ `aria-label="Save title"`, × `aria-label="Cancel rename"`); props `{ title: string; onSave(title: string): Promise<boolean>; onDone(): void }`; blur closes when unchanged.

`WidgetCard.tsx`:
- `const editing = useWidgetEditing()`; `const [renaming, setRenaming] = useState(false)`.
- The menu renders when `share || editing`; `slots` stays `(share || editing ? 2 : 1) + (grip ? 1 : 0)`.
- `WidgetMenu` gains optional `onEdit`, `onRename`, `onArchive`: when given, items `Edit…` (`PencilIcon`), `Rename` (`TextCursorInputIcon`), then the existing share/download items, `DropdownMenuSeparator`, `Archive` (`ArchiveIcon`, `variant="destructive"` if the primitive supports it). Download/Share keep their `disabled` rules; Edit/Rename/Archive are never disabled (a broken query is exactly what Edit fixes).
- `titleSlot={renaming ? <WidgetTitleField title={widget.title ?? ''} onSave={(t) => editing!.rename(widget, t)} onDone={() => setRenaming(false)} /> : undefined}`.

`PageStates.tsx`: `NoWidgets({ action }: { action?: ReactNode })` — with an action, title "No widgets yet" and the action in the notice body; without, today's text.

`Dashboard.tsx` (`DashboardView`): when `arrangeable`, wrap the header + grid in `<WidgetEditingProvider dashboardId={dashboard.dashboard_id} defaults={{ projectId: sel.projectId, range: sel.range ? { range: sel.range, from, to } : { range: '7d', from, to }, timezone: list.timezone }}>`; put `<AddWidgetButton />` first among `DashboardHeader`'s children; `NoWidgets action={arrangeable ? <AddWidgetButton /> : undefined}`. Keep the tree shape stable: render the provider always and pass an `enabled` flag (`useWidgetEditing` returns null when not enabled) if wrapping conditionally would remount the grid when `frozen` flips — #157 relies on the grid keeping its state while freezing (see the comment above `<DndContext>` in WidgetGrid). So: `WidgetEditingProvider` takes `enabled: boolean` and provides `null` when false.

`ProjectDashboardTab.tsx` (`TabView`): same, `enabled={arrange !== undefined}`, `defaults={{ projectId, range: { range: sel.range ?? '7d', from: shareRange.from, to: shareRange.to }, timezone: list.timezone }}`.

- [ ] **Step 4: Run** `npx vitest run --testTimeout=30000 src/hooks/use-widget-edits.test.tsx src/components src/pages/Dashboard.test.tsx` → PASS; typecheck clean.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): add, edit, rename and archive widgets from the dashboard"
```

---

### Task 9: Archive page — Archived widgets

**Files:**
- Modify: `web/src/pages/Archive.tsx`
- Modify: `web/src/pages/Archive.test.tsx`

**Interfaces:**
- Consumes: `archivedWidgetsQuery` (Task 4), `useWidgetEdits().restore` (Task 8), `ListedWidget`.

- [ ] **Step 1: Failing tests** in `Archive.test.tsx` (mock `endpoints.listWidgets`; mock `@/hooks/use-widget-edits` like the other hooks there):
  - archived widgets of a live user dashboard show under the heading `Archived widgets`, grouped under the dashboard's title, each labelled by `title ?? name`, with `Restore`; clicking Restore calls `restore({ widget_id, dashboard_id })`;
  - a widget of an archived dashboard, a live widget, and a widget of a system dashboard are not listed;
  - with none, the section is absent and, with nothing else archived, `Nothing archived.` still shows.

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement.** Query `archivedWidgetsQuery` (`enabled: data !== undefined && writable`, like `sharesQ`). Filter `w.archived_at && !w.dashboard.archived_at && w.dashboard.owner === 'user'`, group by `w.dashboard.dashboard_id` keeping first-seen order, sort widgets in a group by `position`. Render with the page's existing section/row components (match how dashboard groups render: the same row component with `meta` showing "archived · deleted on …" via `purgeDate(w.archived_at, purgeDays)`), placed after the dashboards section and before Shares. Include it in the "Nothing archived." condition and the loading guard.

- [ ] **Step 4: Run** `npx vitest run --testTimeout=30000 src/pages/Archive.test.tsx` → PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/Archive.tsx web/src/pages/Archive.test.tsx
git commit -m "feat(web): restore archived widgets from the archive page"
```

---

### Task 10: New dashboard from a project's "+" dialog

**Files:**
- Modify: `web/src/hooks/use-project-tab-actions.ts` (+ its test)
- Modify: `web/src/components/projects/AddTabDialog.tsx`
- Modify: `web/src/components/projects/ProjectTabBar.tsx` (+ `ProjectTabBar.test.tsx`)

**Interfaces:**
- Consumes: `endpoints.createDashboard`, `endpoints.addProjectTab`, `tabPath` from `@/lib/project-tabs`.
- Produces: `ProjectTabActions.create(projectId: number, title: string): Promise<boolean>`; `AddTabDialog` prop `onCreate(title: string): Promise<boolean>`.

- [ ] **Step 1: Failing tests.**
  - `use-project-tab-actions.test.tsx`: `create(4, 'Launch')` calls `createDashboard({ title: 'Launch' })` then `addProjectTab(4, { dashboard_id: <new id> })`, invalidates `['dashboards']` and the project's tabs query, navigates to `tabPath(4, newId)`, resolves `true`; a refusal of `createDashboard` toasts its message and resolves `false` with no tab call; a refusal of `addProjectTab` toasts `Created 'Launch' but couldn't add it as a tab: <message>` and resolves `false`.
  - `ProjectTabBar.test.tsx` (or a new `AddTabDialog.test.tsx`): open "+", click `New dashboard…`, type `A` → `Create` disabled with the hint "At least 2 characters"; type `Launch`, Enter → `onCreate('Launch')`; resolving `true` closes the dialog.

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement.**
  - `create` in `use-project-tab-actions.ts`, reusing its `run`/pending pattern and query keys (read the file: the tabs query key is `projectTabsQuery(projectId).queryKey`). Use `useNavigate`.
  - `AddTabDialog`: a first row `Button variant="ghost"` with `PlusIcon` "New dashboard…" that switches the dialog body to a `<form>`: `Input aria-label="Dashboard title" autoFocus`, the hint, `Back` and `Create` buttons (`Create` disabled while `title.trim().length < 2` or pending). Reset to the picker when the dialog closes. Update its doc comment (it no longer "only adds existing dashboards"). The "Every dashboard is already a tab" empty text stays, below the new row.
  - `ProjectTabBar`: pass `onCreate={(title) => actions.create(projectId, title)}`; the dialog closes when it resolves `true`.

- [ ] **Step 4: Run** the touched tests → PASS; typecheck clean.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): create a dashboard from a project's add tab dialog"
```

---

### Task 11: End-to-end, phone layout, and the full gate

**Files:**
- Create: `web/e2e/widget-editor.spec.ts`
- Modify: `web/e2e/phone.spec.ts` (open the editor at 360px)
- Possibly modify: `web/e2e/cursor.spec.ts` (only if a new control needs an `ALLOWED` entry — it should not)

**Interfaces:**
- Consumes: everything above, through the UI.

- [ ] **Step 1: Write `widget-editor.spec.ts`** (copy `login`/`authHeaders` from `arrange.spec.ts`; it runs in the `chromium` project). It creates its own project over REST (`POST /api/projects` with a unique name; read `projects.spec.ts` for the body) so other specs' tab counts are untouched:

```ts
test('builds a dashboard from a project and edits its widget', async ({ page, request }) => {
  const project = await createProject(request, `Editor ${Date.now()}`)
  await login(page)
  await page.goto(`/app/projects/${project.id}`)
  await page.getByRole('button', { name: 'Add tab' }).click()
  await page.getByRole('button', { name: 'New dashboard…' }).click()
  await page.getByLabel('Dashboard title').fill('Editor flow')
  await page.getByRole('button', { name: 'Create' }).click()
  await expect(page).toHaveURL(new RegExp(`/projects/${project.id}/dashboards/\\d+`))

  await page.getByRole('button', { name: 'Add widget' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Title').fill('Daily views')
  await dialog.getByRole('combobox', { name: 'Visualization' }).click()
  await page.getByRole('option', { name: /line/ }).click()
  await dialog.getByLabel('SQL').click()
  await page.keyboard.insertText('SELECT day AS x, SUM(views) AS y FROM v_views_daily WHERE project_id = :project AND day BETWEEN :from AND :to GROUP BY day')
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect(dialog.getByText(/\d+ rows?/)).toBeVisible()
  await expect(dialog.getByRole('tab', { name: 'Preview' })).toHaveAttribute('aria-selected', 'true')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()
  const card = page.locator('[data-slot="widget-card"]', { hasText: 'Daily views' })
  await expect(card).toBeVisible()

  // Edit the SQL: a problem shows with the rows.
  await card.hover()
  await card.getByRole('button', { name: 'Widget actions' }).click()
  await page.getByRole('menuitem', { name: 'Edit…' }).click()
  await dialog.getByLabel('SQL').click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.insertText('SELECT day AS x FROM v_views_daily WHERE project_id = :project LIMIT 3')
  await dialog.getByRole('button', { name: /Run query/ }).click()
  await expect(dialog.getByText(/line needs y/)).toBeVisible()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.insertText('SELECT day AS x, views AS y FROM v_views_daily WHERE project_id = :project LIMIT 3')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()

  // Rename inline.
  await card.hover()
  await card.getByRole('button', { name: 'Widget actions' }).click()
  await page.getByRole('menuitem', { name: 'Rename' }).click()
  await page.getByLabel('Widget title').fill('Views per day')
  await page.keyboard.press('Enter')
  const renamed = page.locator('[data-slot="widget-card"]', { hasText: 'Views per day' })
  await expect(renamed).toBeVisible()

  // Archive, undo, archive, restore from the Archive page.
  await renamed.hover()
  await renamed.getByRole('button', { name: 'Widget actions' }).click()
  await page.getByRole('menuitem', { name: 'Archive' }).click()
  await expect(renamed).toBeHidden()
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(renamed).toBeVisible()
  await renamed.hover()
  await renamed.getByRole('button', { name: 'Widget actions' }).click()
  await page.getByRole('menuitem', { name: 'Archive' }).click()
  await expect(renamed).toBeHidden()
  await page.goto('/app/archive')
  const section = page.getByRole('region', { name: 'Archived widgets' })  // or locate by heading; match Archive.tsx's markup
  await section.getByRole('button', { name: 'Restore' }).first().click()
  await page.goto(`/app/projects/${project.id}`)
  await expect(page.locator('[data-slot="widget-card"]', { hasText: 'Views per day' })).toBeVisible()
})
```

Adjust locators to the real markup (the dialog's labels, the Archive section's structure) rather than changing the UI for the test; keep `ControlOrMeta`.

- [ ] **Step 2: phone.spec.ts.** Add a step that, at 360px on a user dashboard (create one over REST with one table widget whose SQL returns 20 columns of long text), opens `Add widget`/`Edit…`, runs the query, and asserts the page and the dialog have no horizontal overflow (reuse the spec's existing overflow helper) while the result table's own box may.

- [ ] **Step 3: Build and run e2e.** `cd web && npm run build && npm run e2e` (Playwright builds and seeds via `e2e/serve.sh`; port 18080 may be held by another session's stale server — if so, find and stop only a process you started, or ask). Expected: all pass, including `cursor.spec.ts`.

- [ ] **Step 4: Full gate.** `cd web && npx tsc -b --noEmit && npx vitest run --testTimeout=30000`, then from the repo root `make check`. Expected: all green. `internal/reporting/ui/components.json` must be unchanged (no contract changed) — `git status` shows it clean.

- [ ] **Step 5: Commit**

```bash
git add web/e2e
git commit -m "test(web): cover the widget editor end to end and on a phone"
```

---

## Self-review notes

- Spec coverage: D1–D6 → Tasks 1, 3; D7 → Task 2; D8 → Task 8; D9–D16 → Tasks 4–7; D17–D18 → Task 8; D19 → Task 9; D20 → Task 10; testing and docs → Tasks 1–3, 11; `web/package.json` description → Task 6.
- Review Focus owners: 1 → Task 4 (`usesParams` tests); 2 → Task 7 (`shouldKeepOpenOnEscape`); 3 → Task 7 ("saves once"); 4 → Task 8 (`rename` and `WidgetTitleField` tests); 5 → Task 11 (phone.spec).
