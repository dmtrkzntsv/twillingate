# Widget Input Schema Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The input schemas of `add_widget`, `update_widget` and `create_dashboard` carry the widget contract (component names, each component's props and source types, size bounds), on MCP `tools/list` and in `/api/doc`.

**Architecture:** `internal/reporting` gains `ConstrainWidget`, which tightens an inferred widget object schema in place from the parsed component manifest. `internal/api`'s `spec` gains an optional `constrain` hook that `expose` runs on the inferred input schema before the tool is registered and before the OpenAPI document reads it; `ops_reporting.go` sets it on the three widget tools.

**Tech Stack:** Go, `github.com/google/jsonschema-go` v0.4.3 (already a dependency), `github.com/modelcontextprotocol/go-sdk` v1.7.0.

**Spec:** `docs/superpowers/specs/2026-10-03-widget-input-schema-design.md`

## Global Constraints

- No new Go dependencies; no new packages (so no archtest rank change).
- Go is at `/usr/local/go/bin` (not on PATH): prefix commands with `export PATH=$PATH:/usr/local/go/bin`.
- Props schemas are inlined in each component's `then`, not put in `$defs`: `openAPI` copies the input schema into a request body, where `#/$defs/...` would resolve against the OpenAPI document root (spec D3, as amended).
- The server's own checks (`checkProps`, `checkSize`, accepts, SQL) stay unchanged.
- Commits: Conventional Commits, `<type>(<scope>): <subject>`, lower case, imperative, no period; end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Comment density and style match the surrounding code (doc comments on every func, explaining why).
- `make check` (from the repo root) must pass before the last commit; only one `make check` at a time on this machine.

## Review Focus

- `update_widget` sends `component: null` or omits it: the nullable `component` enum must include `null`, and the `if` must not fire (test in Task 1).
- `create_dashboard` with no `widgets` or `widgets: null`: still accepted (test in Task 2).
- `table`'s nested props (`formats` is an object whose values are an enum): a bad nested value is refused (test in Task 1).
- REST bodies are not checked against the input schema: `width: 0` over REST still means the default and a bad prop gets the service's message (test in Task 2).
- `markdown` with props omitted, and with `{}`: accepted (test in Task 1).

---

### Task 1: `reporting.ConstrainWidget`

**Files:**
- Create: `internal/reporting/schema.go`
- Test: `internal/reporting/schema_test.go`

**Interfaces:**
- Consumes: `Component` (`internal/reporting/types.go`: `Name string`, `Accepts []string`, `Props json.RawMessage`), `ParseManifest([]byte) ([]Component, error)` (`components.go`), `WidgetSpec` and `Source` (`ops_widget.go`, `types.go`).
- Produces: `func ConstrainWidget(widget *jsonschema.Schema, comps []Component, sourceTypes []string) error` — tightens `widget` in place; returns an error when `widget` lacks any of the properties `component`, `props`, `source` (with a `type` property), `width`, `height`, or when a component's props do not parse as a JSON Schema.

- [ ] **Step 1: Write the failing test**

Create `internal/reporting/schema_test.go`:

```go
package reporting

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// rawAsObject infers json.RawMessage as an object, as the api package's
// schemaOptions does; inferred from its Go type it is an array of bytes.
var rawAsObject = &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[json.RawMessage](): {Type: "object"},
}}

// updateShape mirrors update_widget's input: every field optional and
// nullable, so the schema cannot know the component when it is omitted.
type updateShape struct {
	Component *string         `json:"component,omitempty"`
	Props     json.RawMessage `json:"props,omitempty"`
	Source    *Source         `json:"source,omitempty"`
	Width     *int            `json:"width,omitempty"`
	Height    *int            `json:"height,omitempty"`
}

// constrained infers T's schema, tightens it with testdata's components
// and resolves it.
func constrained[T any](t *testing.T) *jsonschema.Resolved {
	t.Helper()
	b, err := os.ReadFile("testdata/components.json")
	if err != nil {
		t.Fatal(err)
	}
	comps, err := ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jsonschema.For[T](rawAsObject)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConstrainWidget(s, comps, []string{"md", "sql"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func validate(t *testing.T, r *jsonschema.Resolved, in string) error {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatal(err)
	}
	return r.Validate(v)
}

func TestConstrainWidgetSpec(t *testing.T) {
	r := constrained[WidgetSpec](t)
	const sql = `"source":{"type":"sql","content":"SELECT 1 AS value"}`
	const md = `"source":{"type":"md","content":"# Hi"}`
	for _, in := range []string{
		`{"component":"stat",` + sql + `}`,
		`{"component":"stat","props":{"format":"percent","aggregate":"avg"},` + sql + `,"width":12,"height":1}`,
		`{"component":"table","props":{"formats":{"rate":"percent"},"mode":"remote"},` + sql + `}`,
		`{"component":"markdown",` + md + `}`,
		`{"component":"markdown","props":{},` + md + `}`,
	} {
		if err := validate(t, r, in); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
	for _, c := range []struct{ in, want string }{
		{`{"component":"nope",` + sql + `}`, "nope"},
		{`{"component":"stat","props":{"curve":"step"},` + sql + `}`, "curve"},
		{`{"component":"stat","props":{"format":"pct"},` + sql + `}`, "pct"},
		{`{"component":"table","props":{"formats":{"rate":"pct"}},` + sql + `}`, "pct"},
		{`{"component":"markdown",` + sql + `}`, "sql"},
		{`{"component":"stat",` + md + `}`, "md"},
		{`{"component":"stat","source":{"type":"csv","content":"x"}}`, "csv"},
		{`{"component":"stat",` + sql + `,"width":13}`, "width"},
		{`{"component":"stat",` + sql + `,"width":0}`, "width"},
		{`{"component":"stat",` + sql + `,"height":0}`, "height"},
	} {
		err := validate(t, r, c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", c.in, err, c.want)
		}
	}
}

// TestConstrainWidgetUpdate: without a component the schema cannot pick
// the props schema, so any props object passes and the server checks it.
func TestConstrainWidgetUpdate(t *testing.T) {
	r := constrained[updateShape](t)
	for _, in := range []string{
		`{}`,
		`{"props":{"anything":1}}`,
		`{"component":null,"source":null,"width":null,"height":null}`,
		`{"component":"line","props":{"curve":"step"}}`,
	} {
		if err := validate(t, r, in); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
	for _, c := range []struct{ in, want string }{
		{`{"component":"stat","props":{"curve":"step"}}`, "curve"},
		{`{"component":"nope"}`, "nope"},
		{`{"width":13}`, "width"},
	} {
		err := validate(t, r, c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", c.in, err, c.want)
		}
	}
}

func TestConstrainWidgetRefusesOtherShapes(t *testing.T) {
	s, err := jsonschema.For[Source](nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConstrainWidget(s, nil, []string{"sql"}); err == nil {
		t.Error("a schema without component, props, width and height was tightened")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/reporting -run TestConstrainWidget`
Expected: FAIL to compile, `undefined: ConstrainWidget`.

- [ ] **Step 3: Write the implementation**

Create `internal/reporting/schema.go`:

```go
package reporting

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// ConstrainWidget tightens widget, an inferred schema for a widget's
// fields (WidgetSpec, or update_widget's input with every field
// optional), so a client sees the widget contract before calling:
// component is one of comps' names, source.type one of sourceTypes,
// width and height 1–12, and one allOf rule per component that, when
// component names it, holds props to its props schema and source.type to
// the types it accepts. Without component (update_widget keeping the
// stored one) props stays any object, and the server checks it as
// before. Props schemas are inlined rather than put in $defs: the OpenAPI
// document copies this schema into a request body, where a "#/$defs/..."
// reference would resolve against the document, not the schema.
func ConstrainWidget(widget *jsonschema.Schema, comps []Component, sourceTypes []string) error {
	props := widget.Properties
	for _, name := range []string{"component", "props", "source", "width", "height"} {
		if props[name] == nil {
			return fmt.Errorf("reporting: widget schema has no %s property", name)
		}
	}
	srcType := props["source"].Properties["type"]
	if srcType == nil {
		return fmt.Errorf("reporting: widget schema's source has no type property")
	}

	names := make([]any, 0, len(comps)+1)
	for _, c := range comps {
		names = append(names, c.Name)
	}
	// A nullable component (update_widget's *string) must still accept
	// null: enum applies whatever the type says.
	if slices.Contains(props["component"].Types, "null") {
		names = append(names, nil)
	}
	props["component"].Enum = names
	srcType.Enum = anys(sourceTypes)
	one, twelve := 1.0, 12.0
	for _, name := range []string{"width", "height"} {
		props[name].Minimum, props[name].Maximum = &one, &twelve
	}

	for _, c := range comps {
		var ps jsonschema.Schema
		if err := json.Unmarshal(c.Props, &ps); err != nil {
			return fmt.Errorf("reporting: component %s: props: %w", c.Name, err)
		}
		name := any(c.Name)
		widget.AllOf = append(widget.AllOf, &jsonschema.Schema{
			If: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"component": {Const: &name}},
				Required:   []string{"component"},
			},
			Then: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{
				"props":  &ps,
				"source": {Properties: map[string]*jsonschema.Schema{"type": {Enum: anys(c.Accepts)}}},
			}},
		})
	}
	return nil
}

// anys converts ss to the []any a schema's enum holds.
func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/reporting -run TestConstrainWidget -v`
Expected: PASS for all three tests. If a refusal's message does not contain the expected word (jsonschema-go's wording), look at the actual error text and fix the implementation, not the test, unless the test's expectation is wrong about where the word appears.

- [ ] **Step 5: Run the package's tests and vet**

Run: `export PATH=$PATH:/usr/local/go/bin && go vet ./internal/reporting && go test ./internal/reporting`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/reporting/schema.go internal/reporting/schema_test.go
git commit -m "feat(reporting): tighten a widget input schema from the component manifest

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Wire the schema into the widget tools, tests and docs

**Files:**
- Modify: `internal/api/expose.go` (the `spec` struct, `expose`, `restOnly`)
- Modify: `internal/api/ops_reporting.go` (`registerReporting`)
- Create: `internal/api/widget_schema_test.go`
- Modify: `docs/reporting.md` (after the paragraph at about line 345, "`list_components` is the authority: …"; and the error table at about line 828)
- Modify: `docs/superpowers/specs/2026-10-03-widget-input-schema-design.md` (D3, D5, Changes: inlined, not `$defs`)

**Interfaces:**
- Consumes: `reporting.ConstrainWidget(widget *jsonschema.Schema, comps []reporting.Component, sourceTypes []string) error` (Task 1), `reporting.ParseManifest(reporting.Manifest())`, `h.rep.SourceTypes() []string`.
- Produces: `spec.constrain func(in *jsonschema.Schema)`, applied by `expose` and `restOnly` right after `schemaFor`.

- [ ] **Step 1: Write the failing tests**

Create `internal/api/widget_schema_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// schemaAt walks a decoded JSON schema along keys (object keys only).
func schemaAt(t *testing.T, v any, keys ...string) map[string]any {
	t.Helper()
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("at %s: %T is not an object", k, v)
		}
		v = m[k]
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v: %T is not an object", keys, v)
	}
	return m
}

// widgetSchemaOK checks one widget schema carries the contract: the
// component enum and one allOf rule per component.
func widgetSchemaOK(t *testing.T, tool string, w map[string]any) {
	t.Helper()
	enum, _ := schemaAt(t, w, "properties", "component")["enum"].([]any)
	if !containsAny(enum, "stat") || !containsAny(enum, "markdown") {
		t.Errorf("%s: component enum = %v", tool, enum)
	}
	if rules, _ := w["allOf"].([]any); len(rules) != len(enum) && len(rules) != len(enum)-1 {
		t.Errorf("%s: %d allOf rules for %d enum values", tool, len(rules), len(enum))
	}
	if max := schemaAt(t, w, "properties", "width")["maximum"]; max != 12.0 {
		t.Errorf("%s: width maximum = %v", tool, max)
	}
}

func containsAny(vs []any, want any) bool {
	for _, v := range vs {
		if v == want {
			return true
		}
	}
	return false
}

// TestWidgetToolsCarryTheContract: tools/list shows the component enum,
// size bounds and per-component rules on all three widget-taking tools.
func TestWidgetToolsCarryTheContract(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range res.Tools {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var in any
		if err := json.Unmarshal(b, &in); err != nil {
			t.Fatal(err)
		}
		switch tool.Name {
		case "add_widget", "update_widget":
			widgetSchemaOK(t, tool.Name, in.(map[string]any))
			seen++
		case "create_dashboard":
			widgetSchemaOK(t, tool.Name, schemaAt(t, in, "properties", "widgets", "items"))
			seen++
		}
	}
	if seen != 3 {
		t.Errorf("found %d of the 3 widget tools", seen)
	}
}

// TestWidgetSchemaRefusesBeforeWriting: over MCP a bad prop is refused by
// the input schema and nothing is written; update_widget without a
// component is still refused, by the server.
func TestWidgetSchemaRefusesBeforeWriting(t *testing.T) {
	_, cs := newTestHost(t)
	var d struct {
		ID      int64 `json:"dashboard_id"`
		Widgets []any `json:"widgets"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Schema"}, &d)

	res := callTool(t, cs, "add_widget", map[string]any{
		"dashboard_id": d.ID, "component": "stat", "props": map[string]any{"format": "pct"},
		"source": map[string]any{"type": "sql", "content": visitorsSQL},
	})
	if !res.IsError || !strings.Contains(textOf(res), "pct") {
		t.Fatalf("add_widget with a bad prop = %s", textOf(res))
	}
	toolJSON(t, cs, "get_dashboard", map[string]any{"dashboard_id": d.ID}, &d)
	if len(d.Widgets) != 0 {
		t.Fatalf("refused add_widget wrote %d widgets", len(d.Widgets))
	}

	var w struct {
		ID int64 `json:"widget_id"`
	}
	toolJSON(t, cs, "add_widget", map[string]any{
		"dashboard_id": d.ID, "component": "stat",
		"source": map[string]any{"type": "sql", "content": visitorsSQL},
	}, &w)
	res = callTool(t, cs, "update_widget", map[string]any{"widget_id": w.ID, "props": map[string]any{"curve": "step"}})
	if !res.IsError || !strings.Contains(textOf(res), "curve") {
		t.Errorf("update_widget without component, bad prop = %s", textOf(res))
	}
	toolJSON(t, cs, "update_widget", map[string]any{"widget_id": w.ID, "component": nil, "width": 4}, nil)

	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "No widgets", "widgets": nil}, nil)
}

// TestWidgetSchemaLeavesRESTToTheServer: REST bodies are not checked
// against the input schema, so width 0 still means the default and a bad
// prop gets the service's refusal.
func TestWidgetSchemaLeavesRESTToTheServer(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	var d struct {
		ID int64 `json:"dashboard_id"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "REST"}, &d)
	target := "/api/dashboards/" + itoa(d.ID) + "/widgets"
	src := `"source":{"type":"sql","content":"SELECT 1 AS value"}`

	rec := serveREST(t, r, "POST", target, `{"component":"stat","width":0,`+src+`}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("POST width 0 = %d %s", rec.Code, rec.Body.String())
	}
	rec = serveREST(t, r, "POST", target, `{"component":"stat","props":{"format":"pct"},`+src+`}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "stat: ") {
		t.Errorf("POST bad prop = %d %s", rec.Code, rec.Body.String())
	}
}

// TestOpenAPICarriesTheWidgetContract: /api/doc's add_widget body has the
// component enum and the per-component rules.
func TestOpenAPICarriesTheWidgetContract(t *testing.T) {
	h, _ := newTestHost(t)
	raw, err := openAPI(newTestRegistrar(t, h).specs)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	body := schemaAt(t, doc, "paths", "/api/dashboards/{dashboard_id}/widgets", "post",
		"requestBody", "content", "application/json", "schema")
	widgetSchemaOK(t, "POST widgets", body)
}
```

Before running, check whether an `itoa`-style helper already exists in the package's tests (`grep -n "func itoa\|strconv.FormatInt" internal/api/*_test.go`); if none does, use `strconv.FormatInt(d.ID, 10)` and import `strconv` instead of calling `itoa`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/api -run 'TestWidgetTools|TestWidgetSchema|TestOpenAPICarries'`
Expected: FAIL — `TestWidgetToolsCarryTheContract` and `TestOpenAPICarriesTheWidgetContract` find no `enum`; `TestWidgetSchemaRefusesBeforeWriting` may pass its first check already (the server refuses too), which is fine.

- [ ] **Step 3: Add the hook to `spec`**

In `internal/api/expose.go`, add a field to `spec` after `RESTOnly`:

```go
	// constrain, when set, tightens the inferred input schema before the
	// tool is registered and the OpenAPI document reads it: what a Go
	// type cannot say, such as the widget contract.
	constrain func(in *jsonschema.Schema)
```

In both `expose` and `restOnly`, right after `s.in, s.out = schemaFor[In](), schemaFor[Out]()`, add:

```go
	if s.constrain != nil {
		s.constrain(s.in)
	}
```

- [ ] **Step 4: Set the hook on the three tools**

In `internal/api/ops_reporting.go`, add `"fmt"` and `"github.com/google/jsonschema-go/jsonschema"` to the imports. At the top of `registerReporting`, after the `const w = ...` line, add:

```go
	// The widget tools' input schemas carry the widget contract, from
	// this build's own component manifest: the one migrate syncs.
	comps, err := reporting.ParseManifest(reporting.Manifest())
	if err != nil {
		panic("api: " + err.Error()) // embedded at build time: this build is broken
	}
	widget := func(pick func(*jsonschema.Schema) *jsonschema.Schema) func(*jsonschema.Schema) {
		return func(in *jsonschema.Schema) {
			if err := reporting.ConstrainWidget(pick(in), comps, h.rep.SourceTypes()); err != nil {
				panic(fmt.Sprintf("api: widget schema: %v", err))
			}
		}
	}
	self := func(in *jsonschema.Schema) *jsonschema.Schema { return in }
	items := func(in *jsonschema.Schema) *jsonschema.Schema { return in.Properties["widgets"].Items }
```

Then add `constrain: widget(items)` to the `create_dashboard` spec literal, and `constrain: widget(self)` to the `add_widget` and `update_widget` spec literals.

- [ ] **Step 5: Run the new tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/api -run 'TestWidgetTools|TestWidgetSchema|TestOpenAPICarries' -v`
Expected: PASS. If `allOf` count differs, it is because `update_widget`'s enum carries `null` (that is why the helper accepts `len(enum)-1`).

- [ ] **Step 6: Run the whole api package (docs_sync's worked examples go through add_widget)**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/api`
Expected: PASS. A failure in `docs_sync_test.go`'s worked-example test means an example in `docs/reporting.md` breaks the schema: read the refusal and fix whichever is wrong, the example or the schema.

- [ ] **Step 7: Update docs/reporting.md**

After the paragraph "`list_components` is the authority: it returns each component's props as a JSON schema, and its `description` says when to use it." add:

```markdown
The input schemas of `add_widget`, `update_widget` and `create_dashboard`
carry the same contract: the component names, each component's props and
source types, and `width` and `height` from 1 to 12. An MCP client sees it
in `tools/list`, and the server refuses a call that breaks it before
running it, with `validating "arguments": …` and the field at fault.
`update_widget` without `component` keeps the stored one, so its props are
checked by the server instead.
```

In the error table (the row `| stat: … (a props schema error) | … |`), add a row right after it:

```markdown
| validating "arguments": … (an MCP input schema error) | The path and message name the field; match `list_components`. Over REST the same mistake gets the server's own refusal. |
```

- [ ] **Step 8: Amend the spec**

In `docs/superpowers/specs/2026-10-03-widget-input-schema-design.md`: in D1 replace `{$ref: "#/$defs/X_props"}` with `X's props schema`; replace D3 with:

```markdown
- **D3. Props are inlined per component.** Each component's props
  schema sits in its rule's `then`. `$defs` was the first choice, for a
  refusal path naming the component, but the OpenAPI document copies
  the input schema into a request body, where `#/$defs/...` resolves
  against the document root, not the schema. A refusal's path reads
  `/allOf/<n>/then/properties/props/...`; the caller knows which
  component it sent.
```

In D5 replace the signature and its `$defs` sentence with: `reporting.ConstrainWidget(widget *jsonschema.Schema, comps []Component, sourceTypes []string) error` tightens, in place, `widget`, an object schema carrying `component`, `props`, `source`, `width` and `height` (for `create_dashboard`, `properties.widgets.items`). In D7 replace the example path with `validating /allOf/1/then/properties/props/properties/format: enum: pct does not equal any of: [number percent duration]`. In Changes, replace "with the `$defs` on its root" with nothing (delete that clause). In Tests, replace "carries the component enum and the `$defs`" with "carries the component enum and the per-component rules".

- [ ] **Step 9: Run make check**

Run: `export PATH=$PATH:/usr/local/go/bin && make check`
Expected: PASS (vet, the coverage gate, the SQLite-free race tests). It needs Node 22 for `make ui`. If the coverage gate fails, add tests for the uncovered lines rather than lowering the gate.

- [ ] **Step 10: Commit**

```bash
git add internal/api/expose.go internal/api/ops_reporting.go internal/api/widget_schema_test.go docs/reporting.md docs/superpowers/specs/2026-10-03-widget-input-schema-design.md
git commit -m "feat(api): type widget props per component in the tool input schemas

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
