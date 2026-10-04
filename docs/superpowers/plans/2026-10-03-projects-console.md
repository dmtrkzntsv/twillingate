# Projects in the Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Manage projects and ingest keys from the console web app, and show each project's usage and how the caps affect its data.

**Architecture:** Three new read-only operations (`limits`, `cap_usage`, `project_stats`) are added to `internal/api` through the existing `expose` mechanism (MCP tool + REST route + OpenAPI), computing on request from the `v_*` views and `dbstat`. The React app (`web/`) gets a `/projects` list page, a `/projects/:id` page and a collapsible sidebar group; every write goes through the existing project and key routes. `list_ingest_keys` switches to snake_case fields.

**Tech Stack:** Go 1.26 (net/http, modernc SQLite through `internal/shared/readsql`), React + TypeScript, TanStack Query, react-router, shadcn/ui, recharts, sonner, vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-03-projects-console-design.md`

## Global Constraints

- Branch `feat/projects-console`, stacked on `perf/product-attrs-live-half` (PR #119); draft PR #120.
- Conventional Commits, scopes from CLAUDE.md (`api`, `web`, `config`, `store`); the key rename commit is `feat(api)!:`.
- Every commit message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Go toolchain: `export PATH=$PATH:/usr/local/go/bin` before any `go` or `make` command.
- `make check` must pass before the final push (vet, coverage gate, docs-sync, web typecheck/tests/build).
- Docs change in the same commit as the code they describe (CLAUDE.md table): new tools and routes → `docs/twillingate.md` tool table, "Managing" paragraph count, HTTP API table; breaking rename → `deploy/UPGRADES.md`.
- Ranges for the new operations: `YYYY-MM-DD`, default the 30 days ending today (UTC, `from` = today − 29), refused (`invalid`) when `from` > `to` or longer than 400 days.
- Unknown project → `not_found` (404); bad input → `invalid` (400); use the existing `invalidf`/`notFoundf` helpers and `h.unknownProjectErr`.
- Cap 0 means no cap; the UI shows "no cap", never "0".
- No new third-party dependency (Go or npm).
- Spec deviation decided while planning: `days_capped` for views and attributes counts days carrying an `(other)` row whatever the current cap, since days rolled up under an older cap keep their folding; for identities with cap 0 it is 0. Task 3 updates the spec's sentence.

## Review Focus

- A project with no data at all (just created): `project_stats` returns a full series of zeros, `last_received_at`/`first_day` null, `raw_days`/`rolled_up_days` 0, size zeros; `cap_usage` returns `dimensions: []`; the UI shows empty states, not errors. Tests in Tasks 3, 4, 8, 10.
- An archived project: its page shows Restore instead of Archive and still shows usage and keys; issuing a key on it shows the server's refusal as a toast. Tests in Tasks 9 and 11.
- A key label with spaces, `/` or `%`: disable/enable must URL-encode the label in the path. Test in Task 6.
- Cap 0 in `limits` and `cap_usage`: shown as "no cap" in the Limits panel and the cap impact table. Tests in Tasks 2, 3, 8, 10.
- Many projects (tens): the sidebar group stays collapsed by default and the all-projects `project_stats` call computes table sizes once per call (cached), not per project. Tests in Tasks 5 and 7.

---

## File Structure

Server (`internal/api`, `internal/config`):
- `internal/config/config.go` — exported default cap constants.
- `internal/api/ops_limits.go` (new) — `limits`, the shared usage range parser, `cap_usage`.
- `internal/api/ops_stats.go` (new) — `project_stats` and the table size cache.
- `internal/api/ops_read.go` — registration of the three operations.
- `internal/api/server.go`, `internal/api/seed_test.go` — host gains `limits` and `sizes`.
- `internal/api/ops_manage.go` — `keyRow` json tags.
- Tests: `internal/api/ops_limits_test.go`, `internal/api/ops_stats_test.go`, `internal/api/ops_manage_test.go`.

Web (`web/src`):
- `lib/api.ts` — types and endpoints for projects, keys, limits, stats, cap usage.
- `lib/queries.ts` — query definitions.
- `lib/units.ts` (new) — `formatBytes`, `formatAgo`.
- `hooks/use-project-actions.ts` (new) — every project and key write, toasts, invalidation.
- `components/projects/` (new): `Sparkline.tsx`, `ProjectCard.tsx`, `LimitsPanel.tsx`, `ChipsInput.tsx`, `CopyButton.tsx`, `ProjectFormDialog.tsx`, `IssueKeyDialog.tsx`, `KeysSection.tsx`, `DetailsSection.tsx`, `UsageSection.tsx`, `CapImpactSection.tsx`.
- `pages/Projects.tsx`, `pages/Project.tsx` (new); `App.tsx` routes; `components/AppSidebar.tsx` group.
- Tests next to each file (`*.test.tsx`), `web/e2e/projects.spec.ts`.

---

### Task 1: `list_ingest_keys` answers snake_case

**Files:**
- Modify: `internal/api/ops_manage.go` (type `keyRow`)
- Modify: `docs/twillingate.md` (section `### Ingest keys`), `deploy/UPGRADES.md` (migration 025 section)
- Test: `internal/api/ops_manage_test.go`

**Interfaces:**
- Produces: `GET /api/keys` → `{"keys":[{"project_id":1,"label":"web","key":"ak_…","state":"active"}]}`; Task 6 types this as `IngestKey`.

- [ ] **Step 1: Write the failing test** — append to `internal/api/ops_manage_test.go`:

```go
// list_ingest_keys names its fields like every other tool: project_id,
// label, key, state. Before the rename three of them were capitalized.
func TestListKeysUsesSnakeCase(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "issue_ingest_key", map[string]any{"project_id": 1, "label": "web"})
	if res.IsError {
		t.Fatalf("issue: %s", textOf(res))
	}
	out := textOf(callTool(t, cs, "list_ingest_keys", map[string]any{"project_id": 1}))
	var got struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(got.Keys) == 0 {
		t.Fatalf("no keys: %s", out)
	}
	for _, field := range []string{"project_id", "label", "key", "state"} {
		if _, ok := got.Keys[0][field]; !ok {
			t.Errorf("key row has no %q field: %v", field, got.Keys[0])
		}
	}
	for _, field := range []string{"Label", "Key", "State"} {
		if _, ok := got.Keys[0][field]; ok {
			t.Errorf("key row still carries %q: %v", field, got.Keys[0])
		}
	}
}
```

(Add `"encoding/json"` to the file's imports if absent.)

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/api -run TestListKeysUsesSnakeCase -count=1`
Expected: FAIL with `key row has no "label" field`.

- [ ] **Step 3: Implement** — in `internal/api/ops_manage.go` replace `keyRow`:

```go
type keyRow struct {
	ProjectID int64  `json:"project_id"`
	Label     string `json:"label"`
	Key       string `json:"key"`
	State     string `json:"state"` // active or disabled
}
```

Search the repo for remaining readers of the old names and fix them: `grep -rn '"Label"\|"State"\|\.Keys\[' internal web/src web/e2e scripts`.

- [ ] **Step 4: Docs** — in `docs/twillingate.md` under `### Ingest keys`, add after the paragraph that introduces `list_ingest_keys` (or at the end of the section):

```markdown
`list_ingest_keys` answers one row per key: `project_id`, `label`, `key`
and `state` (`active` or `disabled`).
```

In `deploy/UPGRADES.md`, in the section `### Upgrading to range-bounded views and configurable caps (migration 025)`, append:

```markdown
`list_ingest_keys` (and `GET /api/keys`) now answers `label`, `key` and
`state` instead of `Label`, `Key` and `State`; update any script that reads
them.
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/api -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api docs/twillingate.md deploy/UPGRADES.md
git commit -m "feat(api)!: name list_ingest_keys fields in snake_case

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `limits`

**Files:**
- Modify: `internal/config/config.go` (default constants used by `parse`)
- Create: `internal/api/ops_limits.go`
- Modify: `internal/api/ops_read.go` (`register`), `internal/api/server.go` (`Build`), `internal/api/seed_test.go` (`newTestHost`)
- Modify: `docs/twillingate.md` (tool count sentence, tool table, HTTP API table)
- Test: `internal/api/ops_limits_test.go`

**Interfaces:**
- Produces (Go): `config.DefaultProductAttributesTopN = 100`, `config.DefaultViewsDimensionsTopN = 1000`, `config.DefaultIdentitiesTopN = 1000`; `limitsFrom(cfg *config.Config) []limitOut`; `(h *host) capOf(setting string) int`; constants `settingViews`, `settingAttrs`, `settingIdentities`; host field `limits []limitOut`.
- Produces (HTTP): `GET /api/limits` → `{"limits":[{"setting","value","default","caps"}]}` in the order views, attributes, identities.

- [ ] **Step 1: Config constants** — in `internal/config/config.go`, above `type Config`, add:

```go
// The caps' defaults (PRODUCT_ATTRIBUTES_TOP_N, VIEWS_DIMENSIONS_TOP_N,
// IDENTITIES_TOP_N). The console's limits tool reports them beside the
// values in force, so they live here rather than as literals in parse.
const (
	DefaultProductAttributesTopN = 100
	DefaultViewsDimensionsTopN   = 1000
	DefaultIdentitiesTopN        = 1000
)
```

and in `parse` replace the three literals:

```go
		ProductAttributesTopN: e.num("PRODUCT_ATTRIBUTES_TOP_N", DefaultProductAttributesTopN),
		ViewsDimensionsTopN:   e.num("VIEWS_DIMENSIONS_TOP_N", DefaultViewsDimensionsTopN),
		IdentitiesTopN:        e.num("IDENTITIES_TOP_N", DefaultIdentitiesTopN),
```

- [ ] **Step 2: Write the failing test** — create `internal/api/ops_limits_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

// limits reports the caps in force beside their defaults, 0 included
// (no cap), in a fixed order: views, attributes, identities.
func TestLimitsReportsTheCapsInForce(t *testing.T) {
	h, cs := newTestHost(t)
	h.limits = limitsFrom(&config.Config{ProductAttributesTopN: 7, ViewsDimensionsTopN: 0, IdentitiesTopN: 2000})
	out, err := h.listLimits(context.Background(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		setting   string
		value, df int
	}{
		{"VIEWS_DIMENSIONS_TOP_N", 0, config.DefaultViewsDimensionsTopN},
		{"PRODUCT_ATTRIBUTES_TOP_N", 7, config.DefaultProductAttributesTopN},
		{"IDENTITIES_TOP_N", 2000, config.DefaultIdentitiesTopN},
	}
	if len(out.Limits) != len(want) {
		t.Fatalf("limits = %+v", out.Limits)
	}
	for i, w := range want {
		l := out.Limits[i]
		if l.Setting != w.setting || l.Value != w.value || l.Default != w.df || l.Caps == "" {
			t.Errorf("limits[%d] = %+v, want %s %d (default %d) with a description", i, l, w.setting, w.value, w.df)
		}
	}
	if h.capOf(settingViews) != 0 || h.capOf(settingAttrs) != 7 || h.capOf(settingIdentities) != 2000 {
		t.Errorf("capOf disagrees with limits: %+v", out.Limits)
	}
	// The MCP tool answers the same.
	var mcpOut limitsOut
	if err := json.Unmarshal([]byte(textOf(callTool(t, cs, "limits", map[string]any{}))), &mcpOut); err != nil {
		t.Fatal(err)
	}
	if len(mcpOut.Limits) != 3 {
		t.Errorf("MCP limits = %+v", mcpOut.Limits)
	}
}
```

- [ ] **Step 3: Run it to see it fail**

Run: `go test ./internal/api -run TestLimitsReportsTheCapsInForce -count=1`
Expected: FAIL to compile (`h.limits undefined`, `limitsFrom undefined`).

- [ ] **Step 4: Implement** — create `internal/api/ops_limits.go`:

```go
package api

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

// The caps, by the setting that sets each.
const (
	settingViews      = "VIEWS_DIMENSIONS_TOP_N"
	settingAttrs      = "PRODUCT_ATTRIBUTES_TOP_N"
	settingIdentities = "IDENTITIES_TOP_N"
)

// ---- limits ----

type limitOut struct {
	Setting string `json:"setting"`
	Value   int    `json:"value" jsonschema:"the cap in force; 0 means no cap"`
	Default int    `json:"default"`
	Caps    string `json:"caps" jsonschema:"what the setting caps"`
}

type limitsOut struct {
	Limits []limitOut `json:"limits"`
}

// limitsFrom reads the caps in force from the running config, in the order
// the console shows them.
func limitsFrom(cfg *config.Config) []limitOut {
	return []limitOut{
		{settingViews, cfg.ViewsDimensionsTopN, config.DefaultViewsDimensionsTopN,
			"values per views breakdown and kinds, per project and day; the rest fold into (other)"},
		{settingAttrs, cfg.ProductAttributesTopN, config.DefaultProductAttributesTopN,
			"values per attribute key, per project, day and event; the rest fold into (other)"},
		{settingIdentities, cfg.IdentitiesTopN, config.DefaultIdentitiesTopN,
			"users, and groups, per project and day; the rest are dropped"},
	}
}

func (h *host) listLimits(_ context.Context, _ struct{}) (limitsOut, error) {
	return limitsOut{Limits: h.limits}, nil
}

// capOf is the cap in force for a setting; 0 (no cap) for an unknown one.
func (h *host) capOf(setting string) int {
	for _, l := range h.limits {
		if l.Setting == setting {
			return l.Value
		}
	}
	return 0
}
```

In `internal/api/ops_read.go`, add the field to `host`:

```go
	// limits are the caps in force (limitsFrom); limits and cap_usage read them.
	limits []limitOut
```

and register after `list_projects`:

```go
	expose(r, spec{Name: "limits", Annotations: ro, Method: "GET", Path: "/api/limits",
		Description: "The caps in force: values kept per views breakdown and day (VIEWS_DIMENSIONS_TOP_N), per attribute key, event and day (PRODUCT_ATTRIBUTES_TOP_N), and users and groups per day (IDENTITIES_TOP_N), each with its default. 0 means no cap. Set in the server's environment, not here."},
		h.listLimits)
```

In `internal/api/server.go` `Build`, set `limits: limitsFrom(cfg)` in the `&host{...}` literal. In `internal/api/seed_test.go` `newTestHost`, set `limits: limitsFrom(&config.Config{ProductAttributesTopN: config.DefaultProductAttributesTopN, ViewsDimensionsTopN: config.DefaultViewsDimensionsTopN, IdentitiesTopN: config.DefaultIdentitiesTopN})` (add the `config` import).

- [ ] **Step 5: Docs** — in `docs/twillingate.md`, section `## Answer questions with the data`:
  - change "A connected session gets thirty-four tools: the eighteen below" to "A connected session gets thirty-five tools: the nineteen below";
  - add a tool table row after `list_projects`:

```markdown
| `limits` | none (no `project_id`) | The caps in force — `VIEWS_DIMENSIONS_TOP_N`, `PRODUCT_ATTRIBUTES_TOP_N`, `IDENTITIES_TOP_N` — each with its `value` (0 = no cap), `default` and what it caps |
```

  - add to the `### HTTP API` table after the `GET /api/projects` row:

```markdown
| `GET` | `/api/limits` | `limits` | — |
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/api ./internal/config -count=1`
Expected: PASS (including `TestDocumentNamesEveryTool` and `TestDocumentMatchesRoutes`).

- [ ] **Step 7: Commit**

```bash
git add internal/config internal/api docs/twillingate.md
git commit -m "feat(api): report the caps in force with limits

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: `cap_usage`

**Files:**
- Modify: `internal/api/ops_limits.go` (range parser, `cap_usage`)
- Modify: `internal/api/ops_read.go` (`register`)
- Modify: `docs/twillingate.md`, `docs/superpowers/specs/2026-10-03-projects-console-design.md` (the `days_capped` sentence)
- Test: `internal/api/ops_limits_test.go`

**Interfaces:**
- Consumes: `h.capOf`, `settingViews`, `settingAttrs`, `settingIdentities` (Task 2).
- Produces (Go): `type usageRangeIn struct { From, To string }` (json `from`, `to`, both `omitempty`); `func usageRange(in usageRangeIn, now time.Time) (civil.Date, civil.Date, error)` — Task 4 reuses both.
- Produces (HTTP): `GET /api/projects/{project_id}/cap-usage?from=&to=` → `{"project_id","from","to","dimensions":[{"setting","dimension","cap","max_values_per_day","max_day","days","days_capped","folded_share"}]}`; `folded_share` is a number or null.

- [ ] **Step 1: Write the failing tests** — append to `internal/api/ops_limits_test.go`:

```go
// The usage tools default to the 30 days ending today and refuse
// backwards or overlong ranges.
func TestUsageRange(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	from, to, err := usageRange(usageRangeIn{}, now)
	if err != nil || from.String() != "2026-09-04" || to.String() != "2026-10-03" {
		t.Errorf("default = %s..%s, %v; want 2026-09-04..2026-10-03", from, to, err)
	}
	from, to, err = usageRange(usageRangeIn{To: "2026-09-10"}, now)
	if err != nil || from.String() != "2026-08-12" || to.String() != "2026-09-10" {
		t.Errorf("to only = %s..%s, %v", from, to, err)
	}
	for _, in := range []usageRangeIn{
		{From: "2026-09-10", To: "2026-09-01"},
		{From: "2025-01-01", To: "2026-09-01"},
		{From: "yesterday"},
	} {
		if _, _, err := usageRange(in, now); !errors.Is(err, manage.ErrInvalid) {
			t.Errorf("%+v: err = %v, want invalid", in, err)
		}
	}
}

// cap_usage reports, per capped dimension, the busiest day, the days with
// data, the days that folded into (other) and the share folded. The test
// host has blog (1) with aggregated days 2026-08-20/21 and a raw view on
// 2026-08-26; this adds a folded paths day, a folded plan day and caps of
// 1 so the identities seeded on 2026-08-20 reach theirs.
func TestCapUsage(t *testing.T) {
	h, _ := newTestHost(t)
	h.limits = limitsFrom(&config.Config{ProductAttributesTopN: 1, ViewsDimensionsTopN: 2, IdentitiesTopN: 1})
	for _, q := range []string{
		`INSERT INTO agg_views_paths (project_id, day, path, visitors, views) VALUES
		 (1,'2026-08-22','/a',3,10), (1,'2026-08-22','/b',2,5), (1,'2026-08-22','(other)',4,15)`,
		`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups) VALUES
		 (1,'2026-08-22','signup','plan','basic',6,5,1), (1,'2026-08-22','signup','plan','(other)',4,3,1)`,
	} {
		if _, err := rawExec(h.ops.St, q); err != nil {
			t.Fatal(err)
		}
	}
	out, err := h.capUsage(context.Background(), capUsageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-26"}})
	if err != nil {
		t.Fatal(err)
	}
	byDim := map[string]capUsageRow{}
	for _, d := range out.Dimensions {
		byDim[d.Dimension] = d
	}
	paths := byDim["paths"]
	// Days 08-20 (3 paths, 37 views), 08-22 (3 rows, 30 views, 15 folded),
	// 08-26 (the raw /live view): the busiest is the earliest of the two 3s.
	if paths.Setting != settingViews || paths.Cap != 2 || paths.MaxValuesPerDay != 3 || paths.MaxDay != "2026-08-20" ||
		paths.Days != 3 || paths.DaysCapped != 1 || paths.FoldedShare == nil || math.Abs(*paths.FoldedShare-15.0/68) > 1e-9 {
		t.Errorf("paths = %+v (share %v)", paths, paths.FoldedShare)
	}
	plan := byDim["plan"]
	// pro (08-20, 3), team (08-21, 2), basic + (other) (08-22, 6 + 4).
	if plan.Setting != settingAttrs || plan.Cap != 1 || plan.MaxValuesPerDay != 2 || plan.MaxDay != "2026-08-22" ||
		plan.Days != 3 || plan.DaysCapped != 1 || plan.FoldedShare == nil || math.Abs(*plan.FoldedShare-4.0/15) > 1e-9 {
		t.Errorf("plan = %+v (share %v)", plan, plan.FoldedShare)
	}
	users := byDim["users"]
	if users.Setting != settingIdentities || users.Cap != 1 || users.MaxValuesPerDay != 1 ||
		users.Days != 1 || users.DaysCapped != 1 || users.FoldedShare != nil {
		t.Errorf("users = %+v", users)
	}
	if _, ok := byDim["consent"]; ok {
		t.Error("consent is listed; it is never capped")
	}
	// Views dimensions come first, in a fixed order; identities last.
	if out.Dimensions[0].Setting != settingViews || out.Dimensions[len(out.Dimensions)-1].Setting != settingIdentities {
		t.Errorf("order: first %+v, last %+v", out.Dimensions[0], out.Dimensions[len(out.Dimensions)-1])
	}
}

// With no cap (0) the rows report cap 0; a day rolled up under an older
// cap keeps its (other) row and still counts as capped; identities, which
// keep no trace, report no capped day.
func TestCapUsageWithNoCap(t *testing.T) {
	h, _ := newTestHost(t)
	h.limits = limitsFrom(&config.Config{})
	if _, err := rawExec(h.ops.St, `INSERT INTO agg_views_paths (project_id, day, path, visitors, views) VALUES (1,'2026-08-22','(other)',4,15)`); err != nil {
		t.Fatal(err)
	}
	out, err := h.capUsage(context.Background(), capUsageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-26"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range out.Dimensions {
		if d.Cap != 0 {
			t.Errorf("%s cap = %d, want 0", d.Dimension, d.Cap)
		}
		if d.Dimension == "paths" && d.DaysCapped != 1 {
			t.Errorf("paths days_capped = %d, want 1 (the folded day stays folded)", d.DaysCapped)
		}
		if (d.Dimension == "users" || d.Dimension == "groups") && d.DaysCapped != 0 {
			t.Errorf("%s days_capped = %d, want 0", d.Dimension, d.DaysCapped)
		}
	}
}

// A project with no data in the range answers no dimensions; an unknown
// one is not_found.
func TestCapUsageEmptyAndUnknown(t *testing.T) {
	h, _ := newTestHost(t)
	out, err := h.capUsage(context.Background(), capUsageIn{ProjectID: 2, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-26"}})
	if err != nil || len(out.Dimensions) != 0 || out.Dimensions == nil {
		t.Errorf("docs = %+v, %v; want an empty, non-nil list", out, err)
	}
	if _, err := h.capUsage(context.Background(), capUsageIn{ProjectID: 99}); !errors.Is(err, manage.ErrNotFound) {
		t.Errorf("unknown project: %v, want not_found", err)
	}
}
```

Add imports: `errors`, `math`, `time`, `github.com/dmtrkzntsv/twillingate/internal/manage`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/api -run 'TestUsageRange|TestCapUsage' -count=1`
Expected: FAIL to compile (`usageRange`, `capUsage`, `capUsageIn`, `capUsageRow` undefined).

- [ ] **Step 3: Implement** — append to `internal/api/ops_limits.go` (extend imports with `fmt`, `sort`, `strconv`, `time`, `github.com/dmtrkzntsv/twillingate/internal/shared/civil`):

```go
// ---- the usage tools' range ----

type usageRangeIn struct {
	From string `json:"from,omitempty" jsonschema:"start day inclusive, YYYY-MM-DD; default 29 days before to"`
	To   string `json:"to,omitempty" jsonschema:"end day inclusive, YYYY-MM-DD; default today (UTC)"`
}

// maxUsageDays bounds a usage range: a year and a bit, so a 365-day range
// fits whatever day it starts.
const maxUsageDays = 400

// usageRange resolves the usage tools' range: to defaults to today (UTC),
// from to 29 days before to, so the default is the last 30 days.
func usageRange(in usageRangeIn, now time.Time) (civil.Date, civil.Date, error) {
	to := civil.Today(now)
	if in.To != "" {
		d, err := civil.Parse(in.To)
		if err != nil || !dayRe.MatchString(in.To) {
			return civil.Date{}, civil.Date{}, invalidf("to must be YYYY-MM-DD, got %q", in.To)
		}
		to = d
	}
	from := to.AddDays(-29)
	if in.From != "" {
		d, err := civil.Parse(in.From)
		if err != nil || !dayRe.MatchString(in.From) {
			return civil.Date{}, civil.Date{}, invalidf("from must be YYYY-MM-DD, got %q", in.From)
		}
		from = d
	}
	if to.Before(from) {
		return civil.Date{}, civil.Date{}, invalidf("from %s is after to %s", from, to)
	}
	if from.AddDays(maxUsageDays).Before(to) {
		return civil.Date{}, civil.Date{}, invalidf("a range runs at most %d days; %s..%s is longer", maxUsageDays, from, to)
	}
	return from, to, nil
}

// ---- cap_usage ----

type capUsageIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
	usageRangeIn
}

type capUsageRow struct {
	Setting         string   `json:"setting"`
	Dimension       string   `json:"dimension" jsonschema:"a views breakdown (paths, …, kinds), an attribute key, or users/groups"`
	Cap             int      `json:"cap" jsonschema:"the cap in force; 0 means no cap"`
	MaxValuesPerDay int      `json:"max_values_per_day" jsonschema:"the most values one day held, the (other) row included"`
	MaxDay          string   `json:"max_day"`
	Days            int      `json:"days" jsonschema:"days with data in the range"`
	DaysCapped      int      `json:"days_capped" jsonschema:"days with an (other) row; for users and groups, days that reached the cap"`
	FoldedShare     *float64 `json:"folded_share" jsonschema:"the (other) rows' share of views, counts or samples; null for users and groups"`
}

type capUsageOut struct {
	ProjectID  int64         `json:"project_id"`
	From       string        `json:"from"`
	To         string        `json:"to"`
	Dimensions []capUsageRow `json:"dimensions"`
}

// capViews are the views breakdowns VIEWS_DIMENSIONS_TOP_N caps, each with
// the column that folds into (other). consent is not here: three values.
var capViews = []struct{ dimension, view, last string }{
	{"paths", "v_views_paths", "path"},
	{"hosts", "v_views_hosts", "host"},
	{"referrers", "v_views_referrers", "source"},
	{"utm", "v_views_utm", "utm_campaign"},
	{"countries", "v_views_countries", "country"},
	{"platforms", "v_views_platforms", "platform"},
	{"os", "v_views_os", "os_version"},
	{"browsers", "v_views_browsers", "browser_version"},
	{"app_versions", "v_views_app_versions", "app_version"},
	{"devices", "v_views_devices", "device_model"},
	{"displays", "v_views_displays", "display"},
	{"locales", "v_views_locales", "app_locale"},
	{"kinds", "v_views_daily", "kind"},
}

// dayUsage is one day of one dimension: its values, whether it folded,
// and the measure (views, count, samples) total and folded.
type dayUsage struct {
	day            string
	values         int
	folded         bool
	total, foldedN float64
}

// summarize turns a dimension's days into its row. Days come ordered by
// day, so the busiest day is the earliest of equals.
func summarize(setting, dimension string, cap int, days []dayUsage, share bool) capUsageRow {
	row := capUsageRow{Setting: setting, Dimension: dimension, Cap: cap, Days: len(days)}
	var total, folded float64
	for _, d := range days {
		if d.values > row.MaxValuesPerDay {
			row.MaxValuesPerDay, row.MaxDay = d.values, d.day
		}
		if d.folded {
			row.DaysCapped++
		}
		total += d.total
		folded += d.foldedN
	}
	if share && total > 0 {
		s := folded / total
		row.FoldedShare = &s
	}
	return row
}

func (h *host) capUsage(ctx context.Context, in capUsageIn) (capUsageOut, error) {
	if h.reg.Snapshot(ctx).Project(in.ProjectID) == nil {
		return capUsageOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	fromD, toD, err := usageRange(in.usageRangeIn, time.Now())
	if err != nil {
		return capUsageOut{}, err
	}
	from, to := fromD.String(), toD.String()
	out := capUsageOut{ProjectID: in.ProjectID, From: from, To: to, Dimensions: []capUsageRow{}}

	for _, v := range capViews {
		res, err := h.db.Run(ctx, fmt.Sprintf(`SELECT day, COUNT(*), MAX(%[1]s = '(other)'),
			SUM(views), SUM(CASE WHEN %[1]s = '(other)' THEN views ELSE 0 END)
			FROM %[2]s WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY day ORDER BY day`, v.last, v.view),
			in.ProjectID, from, to)
		if err != nil {
			return capUsageOut{}, err
		}
		if days := daysOf(res.Rows); len(days) > 0 {
			out.Dimensions = append(out.Dimensions, summarize(settingViews, v.dimension, h.capOf(settingViews), days, true))
		}
	}

	// Attributes: the cap is per event (and per measure), so a day's values
	// are its busiest event's; it folded if any event did.
	res, err := h.db.Run(ctx, `SELECT attr_key, day, MAX(n), MAX(other), SUM(c), SUM(oc) FROM (
		  SELECT attr_key, day, COUNT(*) AS n, MAX(attr_value = '(other)') AS other,
		         SUM(count) AS c, SUM(CASE WHEN attr_value = '(other)' THEN count ELSE 0 END) AS oc
		  FROM v_product_attrs WHERE project_id = ? AND day BETWEEN ? AND ?
		  GROUP BY attr_key, day, event_name
		  UNION ALL
		  SELECT attr_key, day, COUNT(DISTINCT attr_value), MAX(attr_value = '(other)'),
		         SUM(samples), SUM(CASE WHEN attr_value = '(other)' THEN samples ELSE 0 END)
		  FROM v_measures_attrs WHERE project_id = ? AND day BETWEEN ? AND ?
		  GROUP BY attr_key, day, event_name, measure
		) GROUP BY attr_key, day ORDER BY attr_key, day`,
		in.ProjectID, from, to, in.ProjectID, from, to)
	if err != nil {
		return capUsageOut{}, err
	}
	byKey := map[string][]dayUsage{}
	var keys []string
	for _, r := range res.Rows {
		if _, seen := byKey[r[0]]; !seen {
			keys = append(keys, r[0])
		}
		byKey[r[0]] = append(byKey[r[0]], daysOf([][]string{r[1:]})...)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Dimensions = append(out.Dimensions, summarize(settingAttrs, k, h.capOf(settingAttrs), byKey[k], true))
	}

	// Identities keep no (other) row: a day is capped when it reached the cap.
	idCap := h.capOf(settingIdentities)
	for _, kind := range []struct{ kind, dimension string }{{"user", "users"}, {"group", "groups"}} {
		res, err := h.db.Run(ctx, `SELECT day, COUNT(*) FROM v_identity_daily
			WHERE project_id = ? AND day BETWEEN ? AND ? AND kind = ? GROUP BY day ORDER BY day`,
			in.ProjectID, from, to, kind.kind)
		if err != nil {
			return capUsageOut{}, err
		}
		var days []dayUsage
		for _, r := range res.Rows {
			n, _ := strconv.Atoi(r[1])
			days = append(days, dayUsage{day: r[0], values: n, folded: idCap > 0 && n >= idCap})
		}
		if len(days) > 0 {
			out.Dimensions = append(out.Dimensions, summarize(settingIdentities, kind.dimension, idCap, days, false))
		}
	}
	return out, nil
}

// daysOf parses rows of (day, values, folded, total, folded total).
func daysOf(rows [][]string) []dayUsage {
	out := make([]dayUsage, 0, len(rows))
	for _, r := range rows {
		n, _ := strconv.Atoi(r[1])
		total, _ := strconv.ParseFloat(r[3], 64)
		folded, _ := strconv.ParseFloat(r[4], 64)
		out = append(out, dayUsage{day: r[0], values: n, folded: r[2] == "1", total: total, foldedN: folded})
	}
	return out
}
```

Register in `internal/api/ops_read.go` after `limits`:

```go
	expose(r, spec{Name: "cap_usage", Annotations: ro, Method: "GET", Path: p + "/cap-usage",
		Description: "How a project's data meets the caps over a range (default the last 30 days, at most 400): per views breakdown, attribute key, and users/groups, the busiest day's values against the cap, days with data, days folded into (other) (users and groups: days that reached the cap), and the share of views, counts or samples folded. Days already rolled up keep only the kept values and the (other) row, so values per day is at most cap + 1 there."},
		h.capUsage)
```

- [ ] **Step 4: Docs and spec**
  - `docs/twillingate.md`: "thirty-five tools: the nineteen below" → "thirty-six tools: the twenty below"; tool table row after `limits`:

```markdown
| `cap_usage` | `from`, `to` (optional: the last 30 days) | Per capped dimension — views breakdowns and kinds, attribute keys, `users`/`groups` — the busiest day's values against the `cap`, `days` with data, `days_capped` (an `(other)` row; for users and groups, the cap reached) and `folded_share` |
```

  - HTTP API table row after `/api/limits`:

```markdown
| `GET` | `/api/projects/{project_id}/cap-usage` | `cap_usage` | query: `from`, `to` |
```

  - Spec `docs/superpowers/specs/2026-10-03-projects-console-design.md`: replace "A cap of 0 reports `cap: 0`, and `days_capped` is 0." with "A cap of 0 reports `cap: 0`. A day rolled up under an older cap keeps its `(other)` row and still counts as capped; identities, which keep no trace, report no capped day under no cap."

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/api -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api docs
git commit -m "feat(api): report how a project's data meets the caps with cap_usage

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `project_stats` (series, freshness, days, unused attributes)

**Files:**
- Create: `internal/api/ops_stats.go`
- Modify: `internal/api/ops_read.go` (`register`), `docs/twillingate.md`
- Test: `internal/api/ops_stats_test.go`

**Interfaces:**
- Consumes: `usageRangeIn`, `usageRange` (Task 3).
- Produces (Go): `statsIn`, `statsOut`, `projectStats`, `statsDay`, `statsTotals`, `statsSize`, `(h *host) projectStats(ctx, statsIn) (statsOut, error)`; `projectStats.Size` and `statsOut.DatabaseBytes` stay unset until Task 5.
- Produces (HTTP): `GET /api/stats?project_id=&from=&to=` → `{"from","to","database_bytes","projects":[{"project_id","series":[{"day","views","events","measures"}],"totals":{"views","events","measures"},"last_received_at","first_day","raw_days","rolled_up_days","size":{"raw_bytes","aggregate_bytes","total_bytes"}|null,"unused_attributes":[]}]}`.

- [ ] **Step 1: Write the failing tests** — create `internal/api/ops_stats_test.go`:

```go
package api

import (
	"context"
	"errors"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// The test host's blog (1): views aggregated on 08-20 (45) and 08-21 (30),
// a raw view on 08-26, signup x5 on 08-20, measures aggregated on 08-20 (5
// samples) and raw on 08-21 (2), plan declared and carried on 08-20/21.
func TestProjectStatsOneProject(t *testing.T) {
	h, _ := newTestHost(t)
	out, err := h.projectStats(context.Background(), statsIn{ProjectID: 1,
		usageRangeIn: usageRangeIn{From: "2026-08-19", To: "2026-08-27"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.From != "2026-08-19" || out.To != "2026-08-27" || len(out.Projects) != 1 {
		t.Fatalf("out = %+v", out)
	}
	p := out.Projects[0]
	if len(p.Series) != 9 || p.Series[0].Day != "2026-08-19" || p.Series[8].Day != "2026-08-27" {
		t.Fatalf("series must hold every day of the range: %+v", p.Series)
	}
	day := map[string]statsDay{}
	for _, d := range p.Series {
		day[d.Day] = d
	}
	if day["2026-08-20"].Views != 45 || day["2026-08-21"].Views != 30 || day["2026-08-26"].Views != 1 ||
		day["2026-08-20"].Events != 5 || day["2026-08-20"].Measures != 5 || day["2026-08-21"].Measures != 2 ||
		day["2026-08-19"] != (statsDay{Day: "2026-08-19"}) {
		t.Errorf("series = %+v", p.Series)
	}
	if p.Totals != (statsTotals{Views: 76, Events: 5, Measures: 7}) {
		t.Errorf("totals = %+v", p.Totals)
	}
	if p.LastReceivedAt == nil || *p.LastReceivedAt < "2026-08-26" {
		t.Errorf("last_received_at = %v", p.LastReceivedAt)
	}
	if p.FirstDay == nil || *p.FirstDay != "2026-08-20" || p.RawDays != 2 || p.RolledUpDays != 2 {
		t.Errorf("first_day %v raw %d rolled up %d", p.FirstDay, p.RawDays, p.RolledUpDays)
	}
	if len(p.UnusedAttributes) != 0 {
		t.Errorf("plan is carried, yet unused = %v", p.UnusedAttributes)
	}
}

// Without project_id every project comes back; a project with no data has
// a full series of zeros and nulls, and its declared keys are unused.
func TestProjectStatsAllProjectsAndEmpty(t *testing.T) {
	h, cs := newTestHost(t)
	res := callTool(t, cs, "update_project", map[string]any{"project_id": 2, "attributes": []string{"plan"}})
	if res.IsError {
		t.Fatal(textOf(res))
	}
	out, err := h.projectStats(context.Background(), statsIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Projects) != 2 {
		t.Fatalf("projects = %+v", out.Projects)
	}
	docs := out.Projects[1]
	if docs.ProjectID != 2 || len(docs.Series) != 3 || docs.Totals != (statsTotals{}) ||
		docs.LastReceivedAt != nil || docs.FirstDay != nil || docs.RawDays != 0 || docs.RolledUpDays != 0 {
		t.Errorf("docs = %+v", docs)
	}
	if len(docs.UnusedAttributes) != 1 || docs.UnusedAttributes[0] != "plan" {
		t.Errorf("docs unused = %v, want [plan]", docs.UnusedAttributes)
	}
	if _, err := h.projectStats(context.Background(), statsIn{ProjectID: 99}); !errors.Is(err, manage.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	if _, err := h.projectStats(context.Background(), statsIn{usageRangeIn: usageRangeIn{From: "2026-09-02", To: "2026-09-01"}}); !errors.Is(err, manage.ErrInvalid) {
		t.Errorf("backwards range: %v", err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/api -run TestProjectStats -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement** — create `internal/api/ops_stats.go`:

```go
package api

import (
	"context"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// ---- project_stats ----

type statsIn struct {
	ProjectID int64 `json:"project_id,omitempty" jsonschema:"one project; absent answers every project"`
	usageRangeIn
}

type statsDay struct {
	Day      string `json:"day"`
	Views    int64  `json:"views"`
	Events   int64  `json:"events"`
	Measures int64  `json:"measures"`
}

type statsTotals struct {
	Views    int64 `json:"views"`
	Events   int64 `json:"events"`
	Measures int64 `json:"measures"`
}

type statsSize struct {
	RawBytes       int64 `json:"raw_bytes"`
	AggregateBytes int64 `json:"aggregate_bytes"`
	TotalBytes     int64 `json:"total_bytes"`
}

type projectStats struct {
	ProjectID        int64       `json:"project_id"`
	Series           []statsDay  `json:"series" jsonschema:"every day of the range, zeros included"`
	Totals           statsTotals `json:"totals"`
	LastReceivedAt   *string     `json:"last_received_at" jsonschema:"when the newest raw row arrived; null with none"`
	FirstDay         *string     `json:"first_day" jsonschema:"the oldest day with data, raw or rolled up"`
	RawDays          int         `json:"raw_days"`
	RolledUpDays     int         `json:"rolled_up_days"`
	Size             *statsSize  `json:"size" jsonschema:"an estimate from table sizes; null when they cannot be read"`
	UnusedAttributes []string    `json:"unused_attributes" jsonschema:"declared keys no event carried in the range"`
}

type statsOut struct {
	From          string         `json:"from"`
	To            string         `json:"to"`
	DatabaseBytes int64          `json:"database_bytes"`
	Projects      []projectStats `json:"projects"`
}

func (h *host) projectStats(ctx context.Context, in statsIn) (statsOut, error) {
	snap := h.reg.Snapshot(ctx)
	var projects []*manage.Project
	if in.ProjectID != 0 {
		p := snap.Project(in.ProjectID)
		if p == nil {
			return statsOut{}, h.unknownProjectErr(ctx, in.ProjectID)
		}
		projects = append(projects, p)
	} else {
		projects = snap.Projects()
	}
	fromD, toD, err := usageRange(in.usageRangeIn, time.Now())
	if err != nil {
		return statsOut{}, err
	}
	out := statsOut{From: fromD.String(), To: toD.String(), Projects: []projectStats{}}
	for _, p := range projects {
		ps, err := h.statsFor(ctx, p, fromD, toD)
		if err != nil {
			return statsOut{}, err
		}
		out.Projects = append(out.Projects, ps)
	}
	return out, nil
}

func (h *host) statsFor(ctx context.Context, p *manage.Project, fromD, toD civil.Date) (projectStats, error) {
	ps := projectStats{ProjectID: p.ID, UnusedAttributes: []string{}}
	from, to := fromD.String(), toD.String()
	index := map[string]int{}
	for d := fromD; !toD.Before(d); d = d.AddDays(1) {
		index[d.String()] = len(ps.Series)
		ps.Series = append(ps.Series, statsDay{Day: d.String()})
	}
	for _, s := range []struct {
		q   string
		set func(*statsDay, int64)
	}{
		{`SELECT day, SUM(views) FROM v_views_daily WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY day`,
			func(d *statsDay, n int64) { d.Views = n }},
		{`SELECT day, SUM(total_events) FROM v_product_totals WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY day`,
			func(d *statsDay, n int64) { d.Events = n }},
		{`SELECT day, SUM(samples) FROM v_measures_daily WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY day`,
			func(d *statsDay, n int64) { d.Measures = n }},
	} {
		res, err := h.db.Run(ctx, s.q, p.ID, from, to)
		if err != nil {
			return projectStats{}, err
		}
		for _, r := range res.Rows {
			if i, ok := index[r[0]]; ok {
				n, _ := strconv.ParseInt(r[1], 10, 64)
				s.set(&ps.Series[i], n)
			}
		}
	}
	for _, d := range ps.Series {
		ps.Totals.Views += d.Views
		ps.Totals.Events += d.Events
		ps.Totals.Measures += d.Measures
	}

	res, err := h.db.Run(ctx, `SELECT
		  (SELECT MAX(r) FROM (SELECT MAX(received_at) AS r FROM raw_views WHERE project_id = ?1
		                        UNION ALL SELECT MAX(received_at) FROM raw_product WHERE project_id = ?1
		                        UNION ALL SELECT MAX(received_at) FROM raw_measures WHERE project_id = ?1)),
		  (SELECT MIN(d) FROM (SELECT MIN(day) AS d FROM agg_views_daily WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM agg_product_totals WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM agg_measures_daily WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM raw_views WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM raw_product WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM raw_measures WHERE project_id = ?1)),
		  (SELECT COUNT(*) FROM (SELECT day FROM raw_views WHERE project_id = ?1
		                          UNION SELECT day FROM raw_product WHERE project_id = ?1
		                          UNION SELECT day FROM raw_measures WHERE project_id = ?1)),
		  (SELECT COUNT(*) FROM (SELECT day FROM agg_views_daily WHERE project_id = ?1
		                          UNION SELECT day FROM agg_product_totals WHERE project_id = ?1
		                          UNION SELECT day FROM agg_measures_daily WHERE project_id = ?1))`, p.ID)
	if err != nil {
		return projectStats{}, err
	}
	r := res.Rows[0]
	if r[0] != "" {
		ps.LastReceivedAt = &r[0]
	}
	if r[1] != "" {
		ps.FirstDay = &r[1]
	}
	ps.RawDays, _ = strconv.Atoi(r[2])
	ps.RolledUpDays, _ = strconv.Atoi(r[3])

	if len(p.Attributes) > 0 {
		res, err := h.db.Run(ctx, `SELECT attr_key FROM v_product_attrs WHERE project_id = ?1 AND day BETWEEN ?2 AND ?3
			UNION SELECT attr_key FROM v_measures_attrs WHERE project_id = ?1 AND day BETWEEN ?2 AND ?3`, p.ID, from, to)
		if err != nil {
			return projectStats{}, err
		}
		carried := map[string]bool{}
		for _, r := range res.Rows {
			carried[r[0]] = true
		}
		for _, k := range p.Attributes {
			if !carried[k] {
				ps.UnusedAttributes = append(ps.UnusedAttributes, k)
			}
		}
	}
	return ps, nil
}
```

Check the registry type: if `snap.Projects()` returns `[]manage.Project` rather than `[]*manage.Project`, adapt (`for i := range list { projects = append(projects, &list[i]) }`); `p.Attributes` is the declared key list as `listProjects` reads it. If the raw-row `received_at` column holds `''` for old rows, `MAX` still returns the newest non-empty value; keep `r[0] != ""`.

Register in `internal/api/ops_read.go` after `cap_usage`:

```go
	expose(r, spec{Name: "project_stats", Annotations: ro, Method: "GET", Path: "/api/stats",
		Description: "Usage per project over a range (default the last 30 days, at most 400): views, product events and measure samples per day (every day, zeros included) and in total, when the newest raw row arrived, the oldest day with data, raw and rolled-up day counts, an estimate of the disk the project's rows take, and declared attributes no event carried. Without project_id, every project."},
		h.projectStats)
```

- [ ] **Step 4: Docs** — `docs/twillingate.md`: "thirty-six tools: the twenty below" → "thirty-seven tools: the twenty-one below"; tool table row after `cap_usage`:

```markdown
| `project_stats` | `project_id` (optional: every project), `from`, `to` (optional: the last 30 days) | Per project: `views`, product `events` and measure `samples` per day and in total, `last_received_at`, `first_day`, `raw_days`, `rolled_up_days`, an estimated `size` (raw rows and aggregates, `null` when it cannot be read), `unused_attributes`; plus the database's size on disk |
```

HTTP API table row after `/api/limits`:

```markdown
| `GET` | `/api/stats` | `project_stats` | query: `project_id`, `from`, `to` |
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/api -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api docs/twillingate.md
git commit -m "feat(api): report each project's usage with project_stats

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `project_stats` sizes from `dbstat`, cached

**Files:**
- Modify: `internal/api/ops_stats.go`, `internal/api/ops_read.go` (host field), `internal/api/server.go`
- Test: `internal/api/ops_stats_test.go`

**Interfaces:**
- Consumes: `projectStats`, `statsSize`, `statsOut` (Task 4).
- Produces (Go): host field `sizes *sizeCache`; `newSizeCache(ttl time.Duration) *sizeCache`; `(c *sizeCache) get(ctx, db *readsql.DB) (*tableSizes, error)`; `tableSizes.size(projectID int64) *statsSize`; `sizeCache.loads int` (test-visible counter); package var `tableBytesSQL string`.

- [ ] **Step 1: Measure first** — on a synthetic database the size of a busy install, time the dbstat query cold, so the cache TTL is grounded. In a scratch Go test (not committed) under `internal/store/sqlite`, write 1,000,000 views with `WriteEvents` into a fresh DB, aggregate nothing, then time:

```sql
SELECT s.tbl_name, SUM(d.pgsize) FROM (SELECT name, pgsize FROM dbstat WHERE aggregate = TRUE) d
JOIN sqlite_schema s ON s.name = d.name GROUP BY s.tbl_name
```

and `SELECT project_id, COUNT(*) FROM v_events_flat GROUP BY project_id`. Record both timings in the commit message body. If either exceeds 1 s, keep the 10-minute TTL (the plan's default); if both are under 100 ms, still keep the cache (all-projects calls multiply the cost).

- [ ] **Step 2: Write the failing tests** — append to `internal/api/ops_stats_test.go` (add imports `time`):

```go
// Sizes split each table's bytes by the project's share of its rows: blog
// has raw rows and aggregates, docs has neither. The database's size comes
// back too, and table sizes are read once for every project.
func TestProjectStatsSizes(t *testing.T) {
	h, _ := newTestHost(t)
	out, err := h.projectStats(context.Background(), statsIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.DatabaseBytes <= 0 {
		t.Errorf("database_bytes = %d", out.DatabaseBytes)
	}
	blog, docs := out.Projects[0], out.Projects[1]
	if blog.Size == nil || blog.Size.RawBytes <= 0 || blog.Size.AggregateBytes <= 0 ||
		blog.Size.TotalBytes != blog.Size.RawBytes+blog.Size.AggregateBytes {
		t.Errorf("blog size = %+v", blog.Size)
	}
	if docs.Size == nil || docs.Size.TotalBytes != 0 {
		t.Errorf("docs size = %+v, want zeros", docs.Size)
	}
	if h.sizes.loads != 1 {
		t.Errorf("table sizes loaded %d times for two projects, want 1", h.sizes.loads)
	}
	if _, err := h.projectStats(context.Background(), statsIn{ProjectID: 1}); err != nil {
		t.Fatal(err)
	}
	if h.sizes.loads != 1 {
		t.Errorf("a second call within the TTL reloaded: %d", h.sizes.loads)
	}
}

// When dbstat cannot be read, size is null and the rest still answers.
func TestProjectStatsWithoutDbstat(t *testing.T) {
	h, _ := newTestHost(t)
	old := tableBytesSQL
	tableBytesSQL = `SELECT name, 0 FROM no_such_table`
	t.Cleanup(func() { tableBytesSQL = old })
	h.sizes = newSizeCache(time.Minute)
	out, err := h.projectStats(context.Background(), statsIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Projects[0].Size != nil || out.Projects[0].Totals.Views == 0 {
		t.Errorf("size %+v totals %+v; want null size and the totals", out.Projects[0].Size, out.Projects[0].Totals)
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/api -run 'TestProjectStatsSizes|TestProjectStatsWithoutDbstat' -count=1`
Expected: FAIL to compile (`h.sizes`, `newSizeCache`, `tableBytesSQL` undefined).

- [ ] **Step 4: Implement** — append to `internal/api/ops_stats.go` (imports: `sync`, `strings`, `github.com/dmtrkzntsv/twillingate/internal/shared/readsql`):

```go
// tableBytesSQL reads each table's bytes, its indexes included. dbstat
// walks every page, so sizeCache keeps the answer. A var so a test can
// make it fail.
var tableBytesSQL = `SELECT s.tbl_name, SUM(d.pgsize)
	FROM (SELECT name, pgsize FROM dbstat WHERE aggregate = TRUE) d
	JOIN sqlite_schema s ON s.name = d.name
	GROUP BY s.tbl_name`

// tableSizes is one reading: every table holding project rows, with its
// bytes and its rows per project.
type tableSizes struct {
	tables []tableSize
}

type tableSize struct {
	name   string
	raw    bool // events: raw rows; everything else is an aggregate
	bytes  int64
	rows   int64
	per    map[int64]int64
}

// size is a project's share of each table's bytes by its share of rows.
func (s *tableSizes) size(projectID int64) *statsSize {
	out := &statsSize{}
	for _, t := range s.tables {
		if t.rows == 0 {
			continue
		}
		b := t.bytes * t.per[projectID] / t.rows
		if t.raw {
			out.RawBytes += b
		} else {
			out.AggregateBytes += b
		}
	}
	out.TotalBytes = out.RawBytes + out.AggregateBytes
	return out
}

type sizeCache struct {
	ttl   time.Duration
	mu    sync.Mutex
	at    time.Time
	val   *tableSizes
	loads int // readings taken; tests check the cache is used
}

func newSizeCache(ttl time.Duration) *sizeCache { return &sizeCache{ttl: ttl} }

// get answers the cached reading, or takes a new one when it is older than
// ttl. A failed reading is not cached.
func (c *sizeCache) get(ctx context.Context, db *readsql.DB) (*tableSizes, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.val != nil && time.Since(c.at) < c.ttl {
		return c.val, nil
	}
	c.loads++
	val, err := readTableSizes(ctx, db)
	if err != nil {
		return nil, err
	}
	c.val, c.at = val, time.Now()
	return val, nil
}

func readTableSizes(ctx context.Context, db *readsql.DB) (*tableSizes, error) {
	res, err := db.Run(ctx, tableBytesSQL)
	if err != nil {
		return nil, err
	}
	bytes := map[string]int64{}
	for _, r := range res.Rows {
		bytes[r[0]], _ = strconv.ParseInt(r[1], 10, 64)
	}
	// A project's data: its raw rows (events, read through v_events_flat,
	// the raw read path for every family) and every rollup keyed by
	// project_id. Dashboards and the registry are not data.
	res, err = db.Run(ctx, `SELECT m.name FROM sqlite_schema m, pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name = 'project_id'
		  AND (m.name = 'events' OR m.name LIKE 'agg\_%' ESCAPE '\' OR m.name IN ('actors', 'identities'))
		ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	out := &tableSizes{}
	for _, r := range res.Rows {
		name := r[0]
		src := name
		if name == "events" {
			src = "v_events_flat"
		}
		if strings.ContainsAny(src, "\"'` ") {
			continue // never quote-splice an odd name
		}
		counts, err := db.Run(ctx, `SELECT project_id, COUNT(*) FROM "`+src+`" GROUP BY project_id`)
		if err != nil {
			return nil, err
		}
		t := tableSize{name: name, raw: name == "events", bytes: bytes[name], per: map[int64]int64{}}
		for _, c := range counts.Rows {
			id, _ := strconv.ParseInt(c[0], 10, 64)
			n, _ := strconv.ParseInt(c[1], 10, 64)
			t.per[id] = n
			t.rows += n
		}
		out.tables = append(out.tables, t)
	}
	return out, nil
}
```

Note: `db.Run` caps rows at `maxRows`; per-project counts return one row per project, well under it.

In `projectStats` (Task 4's function), before the loop add:

```go
	if res, err := h.db.Run(ctx, `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`); err == nil && len(res.Rows) == 1 {
		out.DatabaseBytes, _ = strconv.ParseInt(res.Rows[0][0], 10, 64)
	}
	sizes, sizeErr := h.sizes.get(ctx, h.db)
	if sizeErr != nil {
		h.logger.Warn("project sizes unavailable", "error", sizeErr)
	}
```

and inside the loop after `statsFor`: `if sizes != nil { ps.Size = sizes.size(p.ID) }`.

Add the host field in `internal/api/ops_read.go`:

```go
	// sizes caches table sizes for project_stats: dbstat reads every page.
	sizes *sizeCache
```

Set `sizes: newSizeCache(10 * time.Minute)` in `server.go` `Build` and in `seed_test.go` `newTestHost` (import `time` there if absent).

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/api -count=1`
Expected: PASS.

- [ ] **Step 6: Commit** (body: the Step 1 timings)

```bash
git add internal/api
git commit -m "feat(api): estimate each project's disk use in project_stats

<dbstat and row-count timings on 1M raw rows from Step 1>

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Web API client, queries and project actions

**Files:**
- Modify: `web/src/lib/api.ts`, `web/src/lib/queries.ts`
- Create: `web/src/lib/units.ts`, `web/src/hooks/use-project-actions.ts`
- Test: `web/src/lib/api.test.ts` (append), `web/src/lib/units.test.ts`, `web/src/hooks/use-project-actions.test.tsx`

**Interfaces:**
- Consumes: the HTTP shapes of Tasks 1–5.
- Produces (TS, `@/lib/api`): `Project { project_id; name; archived?; allowed_origins: string[]; attributes?: string[] }`, `IngestKey { project_id; label; key; state: 'active'|'disabled' }`, `Limit { setting; value; default; caps }`, `CapUsageRow`, `CapUsage`, `StatsDay`, `ProjectStats`, `StatsResponse`, `CreateProjectBody { name; allowed_origins?; attributes? }`, `CreatedProject { project_id; key?; snippet?; note? }`, `IssuedKey { key; snippet?; status; note? }`; `endpoints.keys(projectId?)`, `.createProject(body)`, `.updateProject(id, body)`, `.archiveProject(id)`, `.restoreProject(id)`, `.issueKey(id, label)`, `.disableKey(id, label)`, `.enableKey(id, label)`, `.limits()`, `.stats(q)`, `.capUsage(id, q)`.
- Produces (`@/lib/queries`): `keysQuery(projectId?)`, `limitsQuery`, `statsQuery(q: {project_id?: number; from?: string; to?: string})`, `capUsageQuery(id, q)`. `projectsQuery` keeps its key `['projects']`.
- Produces (`@/lib/units`): `formatBytes(n: number): string`, `formatAgo(iso: string, now?: Date): string`.
- Produces (`@/hooks/use-project-actions`): `useProjectActions(): { create, update, archive, restore, issueKey, disableKey, enableKey, pending }` — each returns the API result or `undefined` after a toast on failure; all invalidate `['projects']`, `['keys']`, `['stats']`, `['cap-usage']`.

- [ ] **Step 1: Write the failing tests**

`web/src/lib/units.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { formatAgo, formatBytes } from './units'

describe('formatBytes', () => {
  it('uses binary-free decimal units, one decimal past the first', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(999)).toBe('999 B')
    expect(formatBytes(2_262_000)).toBe('2.3 MB')
    expect(formatBytes(2_254_000_000)).toBe('2.3 GB')
  })
})

describe('formatAgo', () => {
  const now = new Date('2026-10-03T16:05:00Z')
  it('says just now, minutes, hours and days', () => {
    expect(formatAgo('2026-10-03T16:04:40Z', now)).toBe('just now')
    expect(formatAgo('2026-10-03T16:02:11Z', now)).toBe('2 min ago')
    expect(formatAgo('2026-10-03T13:00:00Z', now)).toBe('3 h ago')
    expect(formatAgo('2026-09-30T16:05:00Z', now)).toBe('3 days ago')
  })
})
```

Append to `web/src/lib/api.test.ts` (follow the file's existing fetch-mocking helper; if it stubs `global.fetch`, reuse that stub):

```ts
describe('project endpoints', () => {
  it('encodes a key label with spaces and slashes in the path', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{"status":"disabled"}', { status: 200 }))
    await endpoints.disableKey(7, 'web / staging 100%')
    expect(fetchMock.mock.calls[0][0]).toBe('/api/projects/7/keys/web%20%2F%20staging%20100%25/disable')
  })

  it('sends only the fields given to update', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{"project_id":7}', { status: 200 }))
    await endpoints.updateProject(7, { allowed_origins: [] })
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body as string)).toEqual({ allowed_origins: [] })
  })

  it('asks for stats with the query it was given', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{"from":"a","to":"b","database_bytes":0,"projects":[]}', { status: 200 }))
    await endpoints.stats({ project_id: 4, from: '2026-09-01', to: '2026-09-30' })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/stats?project_id=4&from=2026-09-01&to=2026-09-30')
  })
})
```

`web/src/hooks/use-project-actions.test.tsx`:

```tsx
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { ApiError, endpoints } from '@/lib/api'
import { testClient } from '@/test/render'
import { useProjectActions } from './use-project-actions'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

function wrapper(client = testClient()) {
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  }
}

beforeEach(() => vi.restoreAllMocks())

describe('useProjectActions', () => {
  it('issues a key, toasts and invalidates the keys', async () => {
    vi.spyOn(endpoints, 'issueKey').mockResolvedValue({ key: 'ak_x', status: 'issued' })
    const { client, wrapper: w } = wrapper()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    let issued: unknown
    await act(async () => {
      issued = await result.current.issueKey(7, 'web')
    })
    expect(issued).toEqual({ key: 'ak_x', status: 'issued' })
    expect(toast.success).toHaveBeenCalled()
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['keys'] })
  })

  it('shows a refusal as a toast and resolves undefined', async () => {
    vi.spyOn(endpoints, 'archiveProject').mockRejectedValue(new ApiError(409, 'project is already archived', 'conflict'))
    const { wrapper: w } = wrapper()
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    let out: unknown = 'unset'
    await act(async () => {
      out = await result.current.archive(7)
    })
    expect(out).toBeUndefined()
    expect(toast.error).toHaveBeenCalledWith('project is already archived')
  })
})
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd web && npx vitest run src/lib/units.test.ts src/lib/api.test.ts src/hooks/use-project-actions.test.tsx`
Expected: FAIL (modules and endpoints missing).

- [ ] **Step 3: Implement**

`web/src/lib/units.ts`:

```ts
/** Bytes in decimal units: 999 B, 2.3 MB, 2.3 GB. */
export function formatBytes(n: number): string {
  if (n < 1000) return `${n} B`
  const units = ['kB', 'MB', 'GB', 'TB']
  let v = n
  let i = -1
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

/** How long ago an ISO time was: just now, 2 min ago, 3 h ago, 3 days ago. */
export function formatAgo(iso: string, now: Date = new Date()): string {
  const s = Math.max(0, (now.getTime() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  const d = Math.floor(s / 86400)
  return `${d} ${d === 1 ? 'day' : 'days'} ago`
}
```

In `web/src/lib/api.ts`, replace `Project` and add the types and endpoints:

```ts
export interface Project {
  project_id: number
  name: string
  archived?: boolean
  allowed_origins: string[]
  attributes?: string[]
}

export interface IngestKey {
  project_id: number
  label: string
  key: string
  state: 'active' | 'disabled'
}

export interface Limit {
  setting: 'VIEWS_DIMENSIONS_TOP_N' | 'PRODUCT_ATTRIBUTES_TOP_N' | 'IDENTITIES_TOP_N'
  /** The cap in force; 0 means no cap. */
  value: number
  default: number
  caps: string
}

export interface CapUsageRow {
  setting: Limit['setting']
  dimension: string
  cap: number
  max_values_per_day: number
  max_day: string
  days: number
  days_capped: number
  folded_share: number | null
}

export interface CapUsage {
  project_id: number
  from: string
  to: string
  dimensions: CapUsageRow[]
}

export interface StatsDay {
  day: string
  views: number
  events: number
  measures: number
}

export interface ProjectStats {
  project_id: number
  series: StatsDay[]
  totals: { views: number; events: number; measures: number }
  last_received_at: string | null
  first_day: string | null
  raw_days: number
  rolled_up_days: number
  size: { raw_bytes: number; aggregate_bytes: number; total_bytes: number } | null
  unused_attributes: string[]
}

export interface StatsResponse {
  from: string
  to: string
  database_bytes: number
  projects: ProjectStats[]
}

export interface CreateProjectBody {
  name: string
  allowed_origins?: string[]
  attributes?: string[]
}

export interface CreatedProject {
  project_id: number
  key?: string
  snippet?: string
  note?: string
}

export interface IssuedKey {
  key: string
  snippet?: string
  status: string
  note?: string
}

export interface RangeQuery {
  from?: string
  to?: string
}
```

In the `endpoints` object add (a local `json` helper keeps the bodies short):

```ts
  keys: (projectId?: number) => api<{ keys: IngestKey[] }>(`/api/keys${toQuery({ project_id: projectId })}`),
  createProject: (body: CreateProjectBody) => api<CreatedProject>('/api/projects', json('POST', body)),
  updateProject: (id: number, body: Partial<CreateProjectBody>) => api<{ project_id: number }>(`/api/projects/${id}`, json('PATCH', body)),
  archiveProject: (id: number) => api<{ status: string }>(`/api/projects/${id}/archive`, json('POST', {})),
  restoreProject: (id: number) => api<{ status: string }>(`/api/projects/${id}/restore`, json('POST', {})),
  issueKey: (id: number, label: string) => api<IssuedKey>(`/api/projects/${id}/keys`, json('POST', { label })),
  disableKey: (id: number, label: string) =>
    api<{ status: string }>(`/api/projects/${id}/keys/${encodeURIComponent(label)}/disable`, json('POST', {})),
  enableKey: (id: number, label: string) =>
    api<{ status: string }>(`/api/projects/${id}/keys/${encodeURIComponent(label)}/enable`, json('POST', {})),
  limits: () => api<{ limits: Limit[] }>('/api/limits'),
  stats: (q: RangeQuery & { project_id?: number }) => api<StatsResponse>(`/api/stats${toQuery(q)}`),
  capUsage: (id: number, q: RangeQuery) => api<CapUsage>(`/api/projects/${id}/cap-usage${toQuery(q)}`),
```

with, above `endpoints`:

```ts
function json(method: string, body: unknown): RequestInit {
  return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
}
```

`toQuery` already drops `undefined`, and the stats test expects `project_id, from, to` order — pass `{ project_id, from, to }` in that order from callers.

In `web/src/lib/queries.ts` update the `projectsQuery` comment ("Projects change through this app, MCP and the CLI; the project actions invalidate it") and add:

```ts
import type { RangeQuery } from './api'

export const keysQuery = (projectId?: number) => ({
  queryKey: ['keys', projectId ?? 'all'],
  queryFn: () => endpoints.keys(projectId),
})

export const limitsQuery = { queryKey: ['limits'], queryFn: () => endpoints.limits(), staleTime: 5 * 60_000 }

export const statsQuery = (q: RangeQuery & { project_id?: number }) => ({
  queryKey: ['stats', q.project_id ?? 'all', q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.stats({ project_id: q.project_id, from: q.from, to: q.to }),
})

export const capUsageQuery = (id: number, q: RangeQuery) => ({
  queryKey: ['cap-usage', id, q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.capUsage(id, q),
})
```

`web/src/hooks/use-project-actions.ts`:

```ts
import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type CreateProjectBody, type CreatedProject, type IssuedKey } from '@/lib/api'

export interface ProjectActions {
  create(body: CreateProjectBody): Promise<CreatedProject | undefined>
  update(id: number, body: Partial<CreateProjectBody>): Promise<boolean>
  archive(id: number): Promise<boolean | undefined>
  restore(id: number): Promise<boolean | undefined>
  issueKey(id: number, label: string): Promise<IssuedKey | undefined>
  disableKey(id: number, label: string): Promise<boolean | undefined>
  enableKey(id: number, label: string): Promise<boolean | undefined>
  /** True while an action runs; buttons wait on it. */
  pending: boolean
}

/**
 * Every project and key write, each the existing audited route. A success
 * toasts and refetches projects, keys, stats and cap usage, so the
 * sidebar, the list and the dashboards' project switcher follow; a
 * failure toasts its message and resolves undefined (false for update)
 * instead of throwing.
 */
export function useProjectActions(): ProjectActions {
  const client = useQueryClient()
  const [pending, setPending] = useState(false)

  const run = useCallback(
    async <T,>(fn: () => Promise<T>, done: string): Promise<T | undefined> => {
      setPending(true)
      try {
        const out = await fn()
        toast.success(done)
        return out
      } catch (err) {
        if (err instanceof ApiError) toast.error(err.message)
        else if (err instanceof TypeError) toast.error("Couldn't reach the server")
        else toast.error(err instanceof Error ? err.message : String(err))
        return undefined
      } finally {
        for (const key of ['projects', 'keys', 'stats', 'cap-usage']) void client.invalidateQueries({ queryKey: [key] })
        setPending(false)
      }
    },
    [client]
  )

  return {
    pending,
    create: (body) => run(() => endpoints.createProject(body), `Created ${body.name}`),
    update: async (id, body) => (await run(() => endpoints.updateProject(id, body), 'Saved')) !== undefined,
    archive: (id) => run(async () => (await endpoints.archiveProject(id), true), 'Archived'),
    restore: (id) => run(async () => (await endpoints.restoreProject(id), true), 'Restored'),
    issueKey: (id, label) => run(() => endpoints.issueKey(id, label), `Issued ${label}`),
    disableKey: (id, label) => run(async () => (await endpoints.disableKey(id, label), true), `Disabled ${label}`),
    enableKey: (id, label) => run(async () => (await endpoints.enableKey(id, label), true), `Enabled ${label}`),
  }
}
```

Also update `web/src/test/fixtures.ts` `projects` so each entry has `allowed_origins` (e.g. `['https://shop.example']`, `['https://blog.example']`, `[]`), keeping types happy.

- [ ] **Step 4: Run the tests and the typecheck**

Run: `cd web && npx vitest run src/lib src/hooks && npx tsc -b`
Expected: PASS, no type errors.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): add the project, key, limit and usage endpoints

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Sidebar "Projects" group

**Files:**
- Modify: `web/src/components/AppSidebar.tsx`
- Test: `web/src/components/AppSidebar.test.tsx` (append)

**Interfaces:**
- Consumes: `projectsQuery` (`@/lib/queries`), `Project` (Task 6).
- Produces: links `/projects` and `/projects/:id`; localStorage key `twillingate.sidebar.projects` (`'open'` or absent).

- [ ] **Step 1: Write the failing tests** — append to `web/src/components/AppSidebar.test.tsx`, reusing the file's existing render helper (it already wraps the sidebar in its providers and a router; mock `endpoints.projects` the way the file mocks other endpoints):

```tsx
describe('Projects group', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.spyOn(endpoints, 'projects').mockResolvedValue({
      projects: Array.from({ length: 40 }, (_, i) => ({ project_id: i + 1, name: `site-${i + 1}`, allowed_origins: [] }))
        .concat([{ project_id: 99, name: 'gone', archived: true, allowed_origins: [] }]),
    })
  })

  it('is collapsed by default, however many projects there are', async () => {
    renderSidebar()
    expect(await screen.findByRole('link', { name: 'Projects' })).toHaveAttribute('href', '/projects')
    expect(screen.queryByRole('link', { name: 'site-1' })).not.toBeInTheDocument()
  })

  it('opens to the active projects and remembers it', async () => {
    const user = userEvent.setup()
    renderSidebar()
    await user.click(await screen.findByRole('button', { name: 'Show projects' }))
    expect(await screen.findByRole('link', { name: 'site-40' })).toHaveAttribute('href', '/projects/40')
    expect(screen.queryByRole('link', { name: 'gone' })).not.toBeInTheDocument()
    expect(localStorage.getItem('twillingate.sidebar.projects')).toBe('open')
  })
})
```

(`renderSidebar` is the file's existing helper; if it is named differently, use that name.)

- [ ] **Step 2: Run them to see them fail**

Run: `cd web && npx vitest run src/components/AppSidebar.test.tsx`
Expected: FAIL (no Projects link).

- [ ] **Step 3: Implement** — in `AppSidebar.tsx` import `useState`, `useQuery`, `ChevronRightIcon`, `FolderIcon`, `Collapsible`, `CollapsibleContent`, `CollapsibleTrigger` (`@/components/ui/collapsible`), `SidebarMenuAction`, `SidebarMenuSub`, `SidebarMenuSubButton`, `SidebarMenuSubItem`, and `projectsQuery`. Add a `ProjectsGroup` component in the same file and render it in its own `SidebarGroup` just above the Archive group:

```tsx
const PROJECTS_OPEN = 'twillingate.sidebar.projects'

/** "Projects": a link to the list, opening to the active projects; closed until opened, then remembered. */
function ProjectsGroup({ pathname, close }: { pathname: string; close: () => void }) {
  const { data } = useQuery(projectsQuery)
  const [open, setOpen] = useState(() => localStorage.getItem(PROJECTS_OPEN) === 'open')
  const toggle = (next: boolean) => {
    setOpen(next)
    if (next) localStorage.setItem(PROJECTS_OPEN, 'open')
    else localStorage.removeItem(PROJECTS_OPEN)
  }
  const active = (data?.projects ?? []).filter((p) => !p.archived)
  return (
    <Collapsible open={open} onOpenChange={toggle} asChild>
      <SidebarMenuItem>
        <SidebarMenuButton asChild isActive={pathname === '/projects'} tooltip="Projects" className={item}>
          <Link to="/projects" onClick={close}>
            <FolderIcon />
            <span>Projects</span>
          </Link>
        </SidebarMenuButton>
        {active.length > 0 && (
          <>
            <CollapsibleTrigger asChild>
              <SidebarMenuAction className="data-[state=open]:rotate-90" aria-label={open ? 'Hide projects' : 'Show projects'}>
                <ChevronRightIcon />
              </SidebarMenuAction>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <SidebarMenuSub>
                {active.map((p) => (
                  <SidebarMenuSubItem key={p.project_id}>
                    <SidebarMenuSubButton asChild isActive={pathname === `/projects/${p.project_id}`}>
                      <Link to={`/projects/${p.project_id}`} onClick={close}>
                        <span>{p.name}</span>
                      </Link>
                    </SidebarMenuSubButton>
                  </SidebarMenuSubItem>
                ))}
              </SidebarMenuSub>
            </CollapsibleContent>
          </>
        )}
      </SidebarMenuItem>
    </Collapsible>
  )
}
```

Render, above the Archive `SidebarGroup`:

```tsx
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              <ProjectsGroup pathname={pathname} close={close} />
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
```

Update the component's doc comment to list Projects between "Yours" and Archive.

- [ ] **Step 4: Run the tests**

Run: `cd web && npx vitest run src/components && npx tsc -b`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/components
git commit -m "feat(web): list projects in a collapsible sidebar group

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: `/projects` list page

**Files:**
- Create: `web/src/components/projects/Sparkline.tsx`, `ProjectCard.tsx`, `LimitsPanel.tsx`, `ChipsInput.tsx`, `CopyButton.tsx`, `ProjectFormDialog.tsx`
- Create: `web/src/pages/Projects.tsx`
- Modify: `web/src/App.tsx` (route `/projects`)
- Test: `web/src/pages/Projects.test.tsx`, `web/src/components/projects/ChipsInput.test.tsx`

**Interfaces:**
- Consumes: `projectsQuery`, `statsQuery`, `keysQuery`, `limitsQuery`, `dashboardsQuery`, `useProjectActions`, `formatBytes`, `formatAgo` (Tasks 6).
- Produces: `CopyButton({ value, label })`, `ChipsInput({ value, onChange, placeholder, label })`, `ProjectFormDialog({ open, onOpenChange, initial?, onSubmit, title, submitLabel })` — Task 9 reuses all three; `LimitsPanel({ limits })`.

- [ ] **Step 1: Write the failing tests**

`web/src/components/projects/ChipsInput.test.tsx`:

```tsx
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ChipsInput from './ChipsInput'

describe('ChipsInput', () => {
  it('adds a trimmed value on Enter or comma, never twice, and removes one', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    const { rerender } = render(<ChipsInput label="Origins" value={['https://a.example']} onChange={onChange} />)
    await user.type(screen.getByLabelText('Origins'), '  https://b.example {Enter}')
    expect(onChange).toHaveBeenLastCalledWith(['https://a.example', 'https://b.example'])
    await user.type(screen.getByLabelText('Origins'), 'https://a.example,')
    expect(onChange).toHaveBeenCalledTimes(1)
    rerender(<ChipsInput label="Origins" value={['https://a.example', 'https://b.example']} onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: 'Remove https://a.example' }))
    expect(onChange).toHaveBeenLastCalledWith(['https://b.example'])
  })
})
```

`web/src/pages/Projects.test.tsx`:

```tsx
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type ProjectStats } from '@/lib/api'
import { dashboardsList } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import Projects from './Projects'

const create = vi.fn()
vi.mock('@/hooks/use-project-actions', () => ({
  useProjectActions: () => ({ create, restore: vi.fn(), pending: false }),
}))

function stats(project_id: number, over: Partial<ProjectStats> = {}): ProjectStats {
  return {
    project_id,
    series: [{ day: '2026-10-02', views: 5, events: 2, measures: 0 }],
    totals: { views: 5, events: 2, measures: 0 },
    last_received_at: new Date(Date.now() - 120_000).toISOString(),
    first_day: '2026-09-01', raw_days: 30, rolled_up_days: 2,
    size: { raw_bytes: 1_000_000, aggregate_bytes: 1_300_000, total_bytes: 2_300_000 },
    unused_attributes: [],
    ...over,
  }
}

beforeEach(() => {
  vi.restoreAllMocks()
  create.mockReset()
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList())
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [
    { project_id: 4, name: 'econumo.com', allowed_origins: ['https://econumo.com'] },
    { project_id: 5, name: 'quiet.dev', allowed_origins: [], attributes: ['plan'] },
    { project_id: 3, name: 'legacy', archived: true, allowed_origins: [] },
  ] })
  vi.spyOn(endpoints, 'stats').mockResolvedValue({ from: '2026-09-03', to: '2026-10-02', database_bytes: 2_100_000_000, projects: [
    stats(4),
    stats(5, { last_received_at: null, totals: { views: 0, events: 0, measures: 0 }, size: null }),
    stats(3),
  ] })
  vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [{ project_id: 4, label: 'web', key: 'ak_1', state: 'active' }] })
  vi.spyOn(endpoints, 'limits').mockResolvedValue({ limits: [
    { setting: 'VIEWS_DIMENSIONS_TOP_N', value: 0, default: 1000, caps: 'values per views breakdown' },
    { setting: 'PRODUCT_ATTRIBUTES_TOP_N', value: 100, default: 100, caps: 'values per attribute key' },
    { setting: 'IDENTITIES_TOP_N', value: 1000, default: 1000, caps: 'users and groups' },
  ] })
})

function renderPage() {
  return renderWithProviders(<MemoryRouter><Projects /></MemoryRouter>)
}

describe('Projects', () => {
  it('shows a card per active project with its usage, and the database size', async () => {
    renderPage()
    const card = await screen.findByRole('article', { name: 'econumo.com' })
    expect(within(card).getByText('2 min ago')).toBeInTheDocument()
    expect(within(card).getByText(/7 events/)).toBeInTheDocument()
    expect(within(card).getByText(/2\.3 MB/)).toBeInTheDocument()
    expect(within(card).getByText(/1 active key/)).toBeInTheDocument()
    expect(screen.getByText(/2 projects · 2\.1 GB on disk/)).toBeInTheDocument()
  })

  it('says a project never sent anything instead of failing', async () => {
    renderPage()
    const card = await screen.findByRole('article', { name: 'quiet.dev' })
    expect(within(card).getByText('Nothing received yet')).toBeInTheDocument()
  })

  it('keeps archived projects in a collapsed group', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByRole('article', { name: 'econumo.com' })
    expect(screen.queryByRole('article', { name: 'legacy' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Archived \(1\)/ }))
    expect(screen.getByRole('article', { name: 'legacy' })).toBeInTheDocument()
  })

  it('shows the caps, 0 as no cap', async () => {
    renderPage()
    const panel = await screen.findByRole('region', { name: 'Limits' })
    expect(within(panel).getByText('VIEWS_DIMENSIONS_TOP_N')).toBeInTheDocument()
    expect(within(panel).getByText('no cap')).toBeInTheDocument()
  })

  it('creates a project and shows its key and snippet once', async () => {
    const user = userEvent.setup()
    create.mockResolvedValue({ project_id: 9, key: 'ak_new', snippet: '<script src="…"></script>' })
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'New project' }))
    await user.type(screen.getByLabelText('Name'), 'shop')
    await user.click(screen.getByRole('button', { name: 'Create' }))
    expect(create).toHaveBeenCalledWith({ name: 'shop', allowed_origins: [], attributes: [] })
    expect(await screen.findByText('ak_new')).toBeInTheDocument()
    expect(screen.getByText('<script src="…"></script>')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd web && npx vitest run src/pages/Projects.test.tsx src/components/projects`
Expected: FAIL (modules missing).

- [ ] **Step 3: Implement the components**

`web/src/components/projects/CopyButton.tsx`:

```tsx
import { CheckIcon, CopyIcon } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/button'

/** Copies `value`; the icon turns to a check for a moment. */
export default function CopyButton({ value, label }: { value: string; label: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      aria-label={label}
      onClick={() => {
        void navigator.clipboard?.writeText(value)
        setDone(true)
        setTimeout(() => setDone(false), 1200)
      }}
    >
      {done ? <CheckIcon /> : <CopyIcon />}
    </Button>
  )
}
```

`web/src/components/projects/ChipsInput.tsx`:

```tsx
import { XIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

interface Props {
  label: string
  value: string[]
  onChange: (next: string[]) => void
  placeholder?: string
}

/** A list of strings as chips: Enter or comma adds the trimmed text, never twice; × removes. */
export default function ChipsInput({ label, value, onChange, placeholder }: Props) {
  const id = useId()
  const [text, setText] = useState('')
  const add = () => {
    const v = text.trim()
    setText('')
    if (v && !value.includes(v)) onChange([...value, v])
  }
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex flex-wrap items-center gap-1.5 rounded-md border p-1.5">
        {value.map((v) => (
          <Badge key={v} variant="secondary" className="gap-1">
            {v}
            <button type="button" aria-label={`Remove ${v}`} onClick={() => onChange(value.filter((x) => x !== v))}>
              <XIcon className="size-3" />
            </button>
          </Badge>
        ))}
        <Input
          id={id}
          value={text}
          placeholder={placeholder}
          className="h-7 min-w-32 flex-1 border-0 shadow-none focus-visible:ring-0"
          onChange={(e) => {
            if (e.target.value.endsWith(',')) {
              setText(e.target.value.slice(0, -1))
              queueMicrotask(add)
            } else setText(e.target.value)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              add()
            }
          }}
          onBlur={add}
        />
      </div>
    </div>
  )
}
```

If the comma path races with `setText` in tests, replace `queueMicrotask(add)` with an inline add of `e.target.value.slice(0, -1).trim()`; the test pins behaviour, not the mechanism.

`web/src/components/projects/Sparkline.tsx`:

```tsx
import { Area, AreaChart, ResponsiveContainer } from 'recharts'
import type { StatsDay } from '@/lib/api'

/** Events per day, every family summed, as a small area with no axes. */
export default function Sparkline({ series }: { series: StatsDay[] }) {
  const data = series.map((d) => ({ day: d.day, n: d.views + d.events + d.measures }))
  return (
    <div className="h-10 w-full" aria-hidden>
      <ResponsiveContainer>
        <AreaChart data={data} margin={{ top: 2, right: 0, bottom: 0, left: 0 }}>
          <Area type="monotone" dataKey="n" stroke="var(--chart-1)" fill="var(--chart-1)" fillOpacity={0.15} strokeWidth={1.5} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}
```

`web/src/components/projects/ProjectCard.tsx`:

```tsx
import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import type { IngestKey, Project, ProjectStats } from '@/lib/api'
import { formatValue } from '@/lib/format'
import { formatAgo, formatBytes } from '@/lib/units'
import { cn } from '@/lib/utils'
import Sparkline from './Sparkline'

const DAY = 86_400_000

interface Props {
  project: Project
  stats?: ProjectStats
  keys: IngestKey[]
  onRestore?: () => void
  pending?: boolean
}

/** One project at a glance: freshness, origins, 30 days of events, size, keys and attributes. */
export default function ProjectCard({ project, stats, keys, onRestore, pending }: Props) {
  const last = stats?.last_received_at
  const live = last !== null && last !== undefined && Date.now() - new Date(last).getTime() < DAY
  const total = stats ? stats.totals.views + stats.totals.events + stats.totals.measures : 0
  const active = keys.filter((k) => k.state === 'active').length
  const attrs = project.attributes?.length ?? 0
  return (
    <Card role="article" aria-label={project.name} className="gap-3 py-4 transition-colors hover:border-primary/40">
      <CardHeader className="flex flex-row items-start justify-between gap-2 px-4">
        <CardTitle className="truncate text-base">
          <Link to={`/projects/${project.project_id}`} className="underline-offset-2 hover:underline">{project.name}</Link>
        </CardTitle>
        <span className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
          <span className={cn('size-2 rounded-full', live ? 'bg-emerald-500' : 'bg-muted-foreground/40')} />
          {last ? formatAgo(last) : 'Nothing received yet'}
        </span>
      </CardHeader>
      <CardContent className="flex flex-col gap-2 px-4">
        <p className="truncate text-xs text-muted-foreground">{project.allowed_origins.join(', ') || 'No origins'}</p>
        {stats && <Sparkline series={stats.series} />}
        <p className="text-sm">
          <span className="font-medium">{formatValue(total)} events</span>
          <span className="text-muted-foreground"> · 30 days</span>
        </p>
        <p className="text-xs text-muted-foreground">
          {[stats?.size ? formatBytes(stats.size.total_bytes) : 'size unknown', `${active} active ${active === 1 ? 'key' : 'keys'}`, `${attrs} ${attrs === 1 ? 'attribute' : 'attributes'}`].join(' · ')}
        </p>
        {onRestore && (
          <Button variant="outline" size="sm" className="self-start" disabled={pending} onClick={onRestore}>
            Restore
          </Button>
        )}
      </CardContent>
    </Card>
  )
}
```

(`formatValue(7)` must print `7`; check `@/lib/format` and use a plain `toLocaleString()` if it formats differently. The test expects "7 events".)

`web/src/components/projects/LimitsPanel.tsx`:

```tsx
import type { Limit } from '@/lib/api'

/** The caps in force, each with its default and what it caps; 0 reads "no cap". Set in the environment. */
export default function LimitsPanel({ limits }: { limits: Limit[] }) {
  return (
    <section aria-label="Limits" className="flex flex-col gap-3 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">Limits</h2>
        <p className="text-sm text-muted-foreground">
          Values kept per day before the rest fold into (other). Set in twillingate.env; 0 keeps every value.
        </p>
      </header>
      <dl className="grid gap-3 sm:grid-cols-3">
        {limits.map((l) => (
          <div key={l.setting} className="flex flex-col gap-0.5">
            <dt className="font-mono text-xs text-muted-foreground">{l.setting}</dt>
            <dd className="text-lg font-semibold">{l.value === 0 ? 'no cap' : l.value.toLocaleString()}</dd>
            <dd className="text-xs text-muted-foreground">
              default {l.default.toLocaleString()} · {l.caps}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}
```

`web/src/components/projects/ProjectFormDialog.tsx`:

```tsx
import { useEffect, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { CreateProjectBody } from '@/lib/api'
import ChipsInput from './ChipsInput'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  submitLabel: string
  initial?: CreateProjectBody
  /** Resolves true when saved, so the dialog closes; false keeps it open with the input. */
  onSubmit: (body: Required<CreateProjectBody>) => Promise<boolean>
  pending?: boolean
}

/** Name, allowed origins (`*` for any) and declared attributes: creating a project or editing one. */
export default function ProjectFormDialog({ open, onOpenChange, title, submitLabel, initial, onSubmit, pending }: Props) {
  const [name, setName] = useState(initial?.name ?? '')
  const [origins, setOrigins] = useState<string[]>(initial?.allowed_origins ?? [])
  const [attributes, setAttributes] = useState<string[]>(initial?.attributes ?? [])
  useEffect(() => {
    if (open) {
      setName(initial?.name ?? '')
      setOrigins(initial?.allowed_origins ?? [])
      setAttributes(initial?.attributes ?? [])
    }
  }, [open, initial])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (await onSubmit({ name: name.trim(), allowed_origins: origins, attributes })) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="project-name">Name</Label>
            <Input id="project-name" value={name} onChange={(e) => setName(e.target.value)} required />
          </div>
          <ChipsInput label="Allowed origins" value={origins} onChange={setOrigins} placeholder="https://example.com or *" />
          <ChipsInput label="Declared attributes" value={attributes} onChange={setAttributes} placeholder="plan, $path, …" />
          <DialogFooter>
            <Button type="submit" disabled={pending || name.trim() === ''}>{submitLabel}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
```

- [ ] **Step 4: Implement the page** — `web/src/pages/Projects.tsx`:

```tsx
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronRightIcon, PlusIcon } from 'lucide-react'
import AppShell, { TopBar } from '@/components/AppShell'
import CopyButton from '@/components/projects/CopyButton'
import LimitsPanel from '@/components/projects/LimitsPanel'
import ProjectCard from '@/components/projects/ProjectCard'
import ProjectFormDialog from '@/components/projects/ProjectFormDialog'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useProjectActions } from '@/hooks/use-project-actions'
import type { CreatedProject } from '@/lib/api'
import { dashboardsQuery, keysQuery, limitsQuery, projectsQuery, statsQuery } from '@/lib/queries'
import { formatBytes } from '@/lib/units'

/** `/projects`: every project as a card with its last 30 days, the caps, archived projects, and New project. */
export default function Projects() {
  const { data: dash } = useQuery(dashboardsQuery)
  const { data: projectsData } = useQuery(projectsQuery)
  const { data: statsData } = useQuery(statsQuery({}))
  const { data: keysData } = useQuery(keysQuery())
  const { data: limitsData } = useQuery(limitsQuery)
  const { create, restore, pending } = useProjectActions()
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CreatedProject | null>(null)

  const projects = projectsData?.projects ?? []
  const active = projects.filter((p) => !p.archived)
  const archived = projects.filter((p) => p.archived)
  const statsOf = (id: number) => statsData?.projects.find((s) => s.project_id === id)
  const keysOf = (id: number) => (keysData?.keys ?? []).filter((k) => k.project_id === id)

  return (
    <AppShell dashboards={dash?.dashboards ?? []} currentId={0} readOnly={dash?.dev === true}>
      <TopBar>
        <span className="text-sm text-muted-foreground">Projects</span>
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        <header className="flex flex-wrap items-end justify-between gap-3">
          <div className="flex flex-col gap-1">
            <h1 className="text-xl font-semibold tracking-tight">Projects</h1>
            <p className="text-sm text-muted-foreground">
              {active.length} {active.length === 1 ? 'project' : 'projects'}
              {statsData ? ` · ${formatBytes(statsData.database_bytes)} on disk` : ''}
            </p>
          </div>
          <Button onClick={() => setCreating(true)}>
            <PlusIcon /> New project
          </Button>
        </header>
        {projectsData && active.length === 0 && <p className="text-sm text-muted-foreground">No projects yet. Create one to get an ingest key.</p>}
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {active.map((p) => (
            <ProjectCard key={p.project_id} project={p} stats={statsOf(p.project_id)} keys={keysOf(p.project_id)} />
          ))}
        </div>
        {limitsData && <LimitsPanel limits={limitsData.limits} />}
        {archived.length > 0 && (
          <Collapsible className="flex flex-col gap-3">
            <CollapsibleTrigger asChild>
              <Button variant="ghost" className="self-start data-[state=open]:[&>svg]:rotate-90">
                <ChevronRightIcon /> Archived ({archived.length})
              </Button>
            </CollapsibleTrigger>
            <CollapsibleContent className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {archived.map((p) => (
                <ProjectCard
                  key={p.project_id}
                  project={p}
                  stats={statsOf(p.project_id)}
                  keys={keysOf(p.project_id)}
                  pending={pending}
                  onRestore={() => void restore(p.project_id)}
                />
              ))}
            </CollapsibleContent>
          </Collapsible>
        )}
      </div>
      <ProjectFormDialog
        open={creating}
        onOpenChange={setCreating}
        title="New project"
        submitLabel="Create"
        pending={pending}
        onSubmit={async (body) => {
          const out = await create(body)
          if (out) setCreated(out)
          return out !== undefined
        }}
      />
      <Dialog open={created !== null} onOpenChange={(o) => !o && setCreated(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Project created</DialogTitle>
            <DialogDescription>Its first ingest key and the snippet to install it. The snippet is shown here once; the key stays on the project page.</DialogDescription>
          </DialogHeader>
          {created?.key && (
            <div className="flex items-center gap-2 rounded-md border p-2 font-mono text-sm">
              <span className="flex-1 truncate">{created.key}</span>
              <CopyButton value={created.key} label="Copy key" />
            </div>
          )}
          {created?.snippet && (
            <div className="flex items-start gap-2 rounded-md border p-2">
              <pre className="flex-1 overflow-x-auto font-mono text-xs whitespace-pre-wrap">{created.snippet}</pre>
              <CopyButton value={created.snippet} label="Copy snippet" />
            </div>
          )}
          {created?.note && <p className="text-xs text-muted-foreground">{created.note}</p>}
        </DialogContent>
      </Dialog>
    </AppShell>
  )
}
```

In `web/src/App.tsx` import `Projects` and add, after the `/archive` route:

```tsx
          <Route
            path="/projects"
            element={
              <OnlineOnly>
                <Projects />
              </OnlineOnly>
            }
          />
```

The create test expects `{ name, allowed_origins: [], attributes: [] }` — `ProjectFormDialog` sends exactly that.

- [ ] **Step 5: Run the tests**

Run: `cd web && npx vitest run src/pages/Projects.test.tsx src/components/projects && npx tsc -b`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "feat(web): list projects as cards with their usage and the caps

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: `/projects/:id` — header, details and ingest keys

**Files:**
- Create: `web/src/components/projects/DetailsSection.tsx`, `KeysSection.tsx`, `IssueKeyDialog.tsx`
- Create: `web/src/pages/Project.tsx`
- Modify: `web/src/App.tsx` (route `/projects/:id`)
- Test: `web/src/pages/Project.test.tsx`

**Interfaces:**
- Consumes: `CopyButton`, `ChipsInput`, `ProjectFormDialog` (Task 8); `useProjectActions`, `keysQuery`, `projectsQuery`, `dashboardsQuery` (Task 6).
- Produces: `pages/Project.tsx` with the header (name, Archived badge, Archive/Restore), `DetailsSection` and `KeysSection`. Task 10 adds the range switcher to the header, `UsageSection` above Details and `CapImpactSection` below Keys.

- [ ] **Step 1: Write the failing tests** — `web/src/pages/Project.test.tsx`:

```tsx
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { endpoints } from '@/lib/api'
import { dashboardsList } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import Project from './Project'

const actions = {
  update: vi.fn(), archive: vi.fn(), restore: vi.fn(), issueKey: vi.fn(),
  disableKey: vi.fn(), enableKey: vi.fn(), create: vi.fn(), pending: false,
}
vi.mock('@/hooks/use-project-actions', () => ({ useProjectActions: () => actions }))

beforeEach(() => {
  vi.restoreAllMocks()
  Object.values(actions).forEach((f) => typeof f === 'function' && f.mockReset())
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList({ purge_after_days: 30 }))
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [
    { project_id: 4, name: 'econumo.com', allowed_origins: ['https://econumo.com'], attributes: ['plan'] },
    { project_id: 3, name: 'legacy', archived: true, allowed_origins: [] },
  ] })
  vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [
    { project_id: 4, label: 'web', key: 'ak_web_123456789', state: 'active' },
    { project_id: 4, label: 'old', key: 'ak_old_123456789', state: 'disabled' },
  ] })
  vi.spyOn(endpoints, 'stats').mockResolvedValue({ from: 'a', to: 'b', database_bytes: 0, projects: [] })
  vi.spyOn(endpoints, 'capUsage').mockResolvedValue({ project_id: 4, from: 'a', to: 'b', dimensions: [] })
})

function renderAt(path: string) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Routes><Route path="/projects/:id" element={<Project />} /></Routes>
    </MemoryRouter>
  )
}

describe('Project', () => {
  it('shows the details and edits them through PATCH', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    renderAt('/projects/4')
    const details = await screen.findByRole('region', { name: 'Details' })
    expect(within(details).getByText('https://econumo.com')).toBeInTheDocument()
    expect(within(details).getByText('plan')).toBeInTheDocument()
    await user.click(within(details).getByRole('button', { name: 'Edit' }))
    await user.click(screen.getByRole('button', { name: 'Remove plan' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(actions.update).toHaveBeenCalledWith(4, { name: 'econumo.com', allowed_origins: ['https://econumo.com'], attributes: [] })
  })

  it('lists keys and disables one after confirming', async () => {
    const user = userEvent.setup()
    actions.disableKey.mockResolvedValue(true)
    renderAt('/projects/4')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    const row = within(keys).getByRole('row', { name: /web/ })
    expect(within(row).getByText('active')).toBeInTheDocument()
    await user.click(within(row).getByRole('button', { name: 'Disable web' }))
    expect(actions.disableKey).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Disable key' }))
    expect(actions.disableKey).toHaveBeenCalledWith(4, 'web')
    expect(within(within(keys).getByRole('row', { name: /old/ })).getByRole('button', { name: 'Enable old' })).toBeInTheDocument()
  })

  it('issues a key and shows it with its snippet', async () => {
    const user = userEvent.setup()
    actions.issueKey.mockResolvedValue({ key: 'ak_ios', snippet: 'twillingate.init(…)', status: 'issued' })
    renderAt('/projects/4')
    await user.click(await screen.findByRole('button', { name: 'Issue key' }))
    await user.type(screen.getByLabelText('Label'), 'ios')
    await user.click(screen.getByRole('button', { name: 'Issue' }))
    expect(actions.issueKey).toHaveBeenCalledWith(4, 'ios')
    expect(await screen.findByText('ak_ios')).toBeInTheDocument()
  })

  it('archives after a confirmation naming the purge window', async () => {
    const user = userEvent.setup()
    actions.archive.mockResolvedValue(true)
    renderAt('/projects/4')
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    expect(screen.getByText(/deleted after 30 days/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Archive project' }))
    expect(actions.archive).toHaveBeenCalledWith(4)
  })

  it('offers Restore on an archived project and still shows it', async () => {
    renderAt('/projects/3')
    expect(await screen.findByRole('button', { name: 'Restore' })).toBeInTheDocument()
    expect(screen.getByText('Archived')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Archive' })).not.toBeInTheDocument()
  })

  it('says so for an unknown project', async () => {
    renderAt('/projects/77')
    expect(await screen.findByText('No project 77')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd web && npx vitest run src/pages/Project.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement the sections**

`web/src/components/projects/DetailsSection.tsx`:

```tsx
import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import type { Project } from '@/lib/api'
import ProjectFormDialog from './ProjectFormDialog'

interface Props {
  project: Project
  onSave: (body: { name: string; allowed_origins: string[]; attributes: string[] }) => Promise<boolean>
  pending?: boolean
}

/** Name, allowed origins and declared attributes, with Edit. */
export default function DetailsSection({ project, onSave, pending }: Props) {
  const [editing, setEditing] = useState(false)
  const chips = (values: string[] | undefined, none: string) =>
    values && values.length > 0 ? (
      <div className="flex flex-wrap gap-1.5">{values.map((v) => <Badge key={v} variant="secondary">{v}</Badge>)}</div>
    ) : (
      <span className="text-sm text-muted-foreground">{none}</span>
    )
  return (
    <section aria-label="Details" className="flex flex-col gap-3 rounded-lg border p-4">
      <header className="flex items-center justify-between">
        <h2 className="text-base font-semibold">Details</h2>
        <Button variant="outline" size="sm" onClick={() => setEditing(true)}>Edit</Button>
      </header>
      <dl className="grid gap-3 sm:grid-cols-[10rem_1fr]">
        <dt className="text-sm text-muted-foreground">Allowed origins</dt>
        <dd>{chips(project.allowed_origins, 'None: browsers cannot send')}</dd>
        <dt className="text-sm text-muted-foreground">Declared attributes</dt>
        <dd>{chips(project.attributes, 'None')}</dd>
      </dl>
      <ProjectFormDialog
        open={editing}
        onOpenChange={setEditing}
        title={`Edit ${project.name}`}
        submitLabel="Save"
        pending={pending}
        initial={{ name: project.name, allowed_origins: project.allowed_origins, attributes: project.attributes ?? [] }}
        onSubmit={onSave}
      />
    </section>
  )
}
```

`web/src/components/projects/IssueKeyDialog.tsx`:

```tsx
import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { IssuedKey } from '@/lib/api'
import CopyButton from './CopyButton'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  onIssue: (label: string) => Promise<IssuedKey | undefined>
  pending?: boolean
}

/** Asks for a label, issues the key, then shows it with its snippet. */
export default function IssueKeyDialog({ open, onOpenChange, onIssue, pending }: Props) {
  const [label, setLabel] = useState('')
  const [issued, setIssued] = useState<IssuedKey | null>(null)
  const close = (o: boolean) => {
    if (!o) {
      setLabel('')
      setIssued(null)
    }
    onOpenChange(o)
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const out = await onIssue(label.trim())
    if (out) setIssued(out)
  }
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{issued ? 'Key issued' : 'Issue key'}</DialogTitle>
          <DialogDescription>Keys are public identifiers: they ship in page source. Retire one by disabling it.</DialogDescription>
        </DialogHeader>
        {issued ? (
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2 rounded-md border p-2 font-mono text-sm">
              <span className="flex-1 truncate">{issued.key}</span>
              <CopyButton value={issued.key} label="Copy key" />
            </div>
            {issued.snippet && (
              <div className="flex items-start gap-2 rounded-md border p-2">
                <pre className="flex-1 overflow-x-auto font-mono text-xs whitespace-pre-wrap">{issued.snippet}</pre>
                <CopyButton value={issued.snippet} label="Copy snippet" />
              </div>
            )}
            {issued.note && <p className="text-xs text-muted-foreground">{issued.note}</p>}
          </div>
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="key-label">Label</Label>
              <Input id="key-label" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="web, ios, staging" required />
            </div>
            <DialogFooter>
              <Button type="submit" disabled={pending || label.trim() === ''}>Issue</Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
```

`web/src/components/projects/KeysSection.tsx`:

```tsx
import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { IngestKey, IssuedKey } from '@/lib/api'
import CopyButton from './CopyButton'
import IssueKeyDialog from './IssueKeyDialog'

interface Props {
  keys: IngestKey[]
  onIssue: (label: string) => Promise<IssuedKey | undefined>
  onDisable: (label: string) => Promise<unknown>
  onEnable: (label: string) => Promise<unknown>
  pending?: boolean
}

/** A project's ingest keys: copy, issue, disable (confirmed) and enable. */
export default function KeysSection({ keys, onIssue, onDisable, onEnable, pending }: Props) {
  const [issuing, setIssuing] = useState(false)
  const [disabling, setDisabling] = useState<string | null>(null)
  return (
    <section aria-label="Ingest keys" className="flex flex-col gap-3 rounded-lg border p-4">
      <header className="flex items-center justify-between">
        <h2 className="text-base font-semibold">Ingest keys</h2>
        <Button variant="outline" size="sm" onClick={() => setIssuing(true)}>Issue key</Button>
      </header>
      {keys.length === 0 ? (
        <p className="text-sm text-muted-foreground">No keys: this project can receive nothing.</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow><TableHead>Label</TableHead><TableHead>Key</TableHead><TableHead>State</TableHead><TableHead /></TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((k) => (
              <TableRow key={k.label} aria-label={k.label}>
                <TableCell className="font-medium">{k.label}</TableCell>
                <TableCell>
                  <span className="flex items-center gap-1 font-mono text-xs">
                    <span className="max-w-48 truncate">{k.key}</span>
                    <CopyButton value={k.key} label={`Copy ${k.label}`} />
                  </span>
                </TableCell>
                <TableCell>
                  <Badge variant={k.state === 'active' ? 'secondary' : 'outline'}>{k.state}</Badge>
                </TableCell>
                <TableCell className="text-right">
                  {k.state === 'active' ? (
                    <Button variant="ghost" size="sm" aria-label={`Disable ${k.label}`} disabled={pending} onClick={() => setDisabling(k.label)}>Disable</Button>
                  ) : (
                    <Button variant="ghost" size="sm" aria-label={`Enable ${k.label}`} disabled={pending} onClick={() => void onEnable(k.label)}>Enable</Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <IssueKeyDialog open={issuing} onOpenChange={setIssuing} onIssue={onIssue} pending={pending} />
      <AlertDialog open={disabling !== null} onOpenChange={(o) => !o && setDisabling(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Disable {disabling}?</AlertDialogTitle>
            <AlertDialogDescription>Events sent with it are rejected within a second, from every site that uses it. You can enable it again.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => { const l = disabling!; setDisabling(null); void onDisable(l) }}>Disable key</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}
```

- [ ] **Step 4: Implement the page** — `web/src/pages/Project.tsx`:

```tsx
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useParams } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import DetailsSection from '@/components/projects/DetailsSection'
import KeysSection from '@/components/projects/KeysSection'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useProjectActions } from '@/hooks/use-project-actions'
import { dashboardsQuery, keysQuery, projectsQuery } from '@/lib/queries'

/** `/projects/:id`: one project's usage, details, keys and cap impact, with Archive or Restore. */
export default function Project() {
  const id = Number(useParams().id)
  const { data: dash } = useQuery(dashboardsQuery)
  const { data: projectsData, isLoading } = useQuery(projectsQuery)
  const { data: keysData } = useQuery(keysQuery(id))
  const actions = useProjectActions()
  const [archiving, setArchiving] = useState(false)
  const project = projectsData?.projects.find((p) => p.project_id === id)
  const purgeDays = dash?.purge_after_days

  return (
    <AppShell dashboards={dash?.dashboards ?? []} currentId={0} readOnly={dash?.dev === true}>
      <TopBar>
        <span className="text-sm text-muted-foreground">Projects / {project?.name ?? id}</span>
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1200px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        {!project ? (
          !isLoading && <p className="text-sm text-muted-foreground">No project {id}</p>
        ) : (
          <>
            <header className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex items-center gap-2">
                <h1 className="text-xl font-semibold tracking-tight">{project.name}</h1>
                {project.archived && <Badge variant="outline">Archived</Badge>}
              </div>
              {project.archived ? (
                <Button variant="outline" disabled={actions.pending} onClick={() => void actions.restore(id)}>Restore</Button>
              ) : (
                <Button variant="outline" disabled={actions.pending} onClick={() => setArchiving(true)}>Archive</Button>
              )}
            </header>
            <DetailsSection project={project} pending={actions.pending} onSave={(body) => actions.update(id, body)} />
            <KeysSection
              keys={keysData?.keys ?? []}
              pending={actions.pending}
              onIssue={(label) => actions.issueKey(id, label)}
              onDisable={(label) => actions.disableKey(id, label)}
              onEnable={(label) => actions.enableKey(id, label)}
            />
          </>
        )}
      </div>
      <AlertDialog open={archiving} onOpenChange={setArchiving}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Archive {project?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Ingestion stops; data and dashboards keep working.
              {purgeDays ? ` Unless restored, the project and its data are deleted after ${purgeDays} days.` : ' It is kept until restored.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void actions.archive(id)}>Archive project</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </AppShell>
  )
}
```

In `web/src/App.tsx` import `Project` and add after the `/projects` route:

```tsx
          <Route
            path="/projects/:id"
            element={
              <OnlineOnly>
                <Project />
              </OnlineOnly>
            }
          />
```

- [ ] **Step 5: Run the tests**

Run: `cd web && npx vitest run src/pages src/components/projects && npx tsc -b`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "feat(web): manage a project's details and ingest keys on its page

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: `/projects/:id` — usage and cap impact

**Files:**
- Create: `web/src/components/projects/UsageSection.tsx`, `web/src/components/projects/CapImpactSection.tsx`
- Modify: `web/src/pages/Project.tsx`
- Test: `web/src/components/projects/UsageSection.test.tsx`, `web/src/components/projects/CapImpactSection.test.tsx`

**Interfaces:**
- Consumes: `statsQuery`, `capUsageQuery` (Task 6), `RangeSwitcher` (`@/components/RangeSwitcher`, value `RangeValue`), `resolve` (`@/lib/ranges`), `ChartContainer`, `ChartTooltip` (`@/components/ui/chart`), `seriesConfig` (`@/lib/chart`), `formatBytes`, `formatAgo`.
- Produces: `UsageSection({ projectId, range: {from, to} })`, `CapImpactSection({ projectId, range: {from, to} })`; the page owns one `RangeValue` state (default `{ range: '30d' }`) shared by both.

- [ ] **Step 1: Write the failing tests**

`web/src/components/projects/UsageSection.test.tsx`:

```tsx
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { endpoints } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import UsageSection from './UsageSection'

beforeEach(() => vi.restoreAllMocks())

describe('UsageSection', () => {
  it('shows totals, freshness, size and unused attributes', async () => {
    vi.spyOn(endpoints, 'stats').mockResolvedValue({ from: '2026-09-01', to: '2026-09-02', database_bytes: 0, projects: [{
      project_id: 4,
      series: [{ day: '2026-09-01', views: 10, events: 3, measures: 1 }, { day: '2026-09-02', views: 20, events: 0, measures: 0 }],
      totals: { views: 30, events: 3, measures: 1 },
      last_received_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
      first_day: '2026-08-01', raw_days: 30, rolled_up_days: 2,
      size: { raw_bytes: 812_000, aggregate_bytes: 1_450_000, total_bytes: 2_262_000 },
      unused_attributes: ['self_hosted'],
    }] })
    renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-02' }} />)
    expect(await screen.findByText('34')).toBeInTheDocument()
    expect(screen.getByText('3 h ago')).toBeInTheDocument()
    expect(screen.getByText('2.3 MB')).toBeInTheDocument()
    expect(screen.getByText(/30 raw · 2 rolled up/)).toBeInTheDocument()
    expect(screen.getByText('self_hosted')).toBeInTheDocument()
    expect(endpoints.stats).toHaveBeenCalledWith({ project_id: 4, from: '2026-09-01', to: '2026-09-02' })
  })

  it('shows an empty project without errors', async () => {
    vi.spyOn(endpoints, 'stats').mockResolvedValue({ from: 'a', to: 'b', database_bytes: 0, projects: [{
      project_id: 5, series: [], totals: { views: 0, events: 0, measures: 0 }, last_received_at: null,
      first_day: null, raw_days: 0, rolled_up_days: 0, size: null, unused_attributes: [],
    }] })
    renderWithProviders(<UsageSection projectId={5} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('Nothing received yet')).toBeInTheDocument()
    expect(screen.getByText('unknown')).toBeInTheDocument()
  })

  it('offers a retry when the stats fail', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'stats').mockRejectedValue(new Error('boom'))
    renderWithProviders(<UsageSection projectId={4} range={{ from: 'a', to: 'b' }} />)
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(spy).toHaveBeenCalledTimes(2)
  })
})
```

`web/src/components/projects/CapImpactSection.test.tsx`:

```tsx
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import { endpoints } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import CapImpactSection from './CapImpactSection'

beforeEach(() => vi.restoreAllMocks())

describe('CapImpactSection', () => {
  it('puts capped dimensions first and shows no cap for 0', async () => {
    vi.spyOn(endpoints, 'capUsage').mockResolvedValue({ project_id: 6, from: 'a', to: 'b', dimensions: [
      { setting: 'VIEWS_DIMENSIONS_TOP_N', dimension: 'countries', cap: 0, max_values_per_day: 38, max_day: '2026-09-02', days: 30, days_capped: 0, folded_share: 0 },
      { setting: 'VIEWS_DIMENSIONS_TOP_N', dimension: 'paths', cap: 1000, max_values_per_day: 1001, max_day: '2026-09-11', days: 30, days_capped: 4, folded_share: 0.41 },
      { setting: 'IDENTITIES_TOP_N', dimension: 'users', cap: 1000, max_values_per_day: 33, max_day: '2026-09-03', days: 27, days_capped: 0, folded_share: null },
    ] })
    renderWithProviders(<CapImpactSection projectId={6} range={{ from: 'a', to: 'b' }} />)
    const rows = await screen.findAllByRole('row')
    expect(within(rows[1]).getByText('paths')).toBeInTheDocument()
    expect(within(rows[1]).getByText('4 of 30')).toBeInTheDocument()
    expect(within(rows[1]).getByText('41%')).toBeInTheDocument()
    const countries = screen.getByRole('row', { name: /countries/ })
    expect(within(countries).getByText('no cap')).toBeInTheDocument()
    const users = screen.getByRole('row', { name: /users/ })
    expect(within(users).getByText('—')).toBeInTheDocument()
  })

  it('says when nothing in the range is capped or there is no data', async () => {
    vi.spyOn(endpoints, 'capUsage').mockResolvedValue({ project_id: 6, from: 'a', to: 'b', dimensions: [] })
    renderWithProviders(<CapImpactSection projectId={6} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('No data in this range.')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd web && npx vitest run src/components/projects/UsageSection.test.tsx src/components/projects/CapImpactSection.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement** — `web/src/components/projects/UsageSection.tsx`:

```tsx
import { useQuery } from '@tanstack/react-query'
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from 'recharts'
import { Button } from '@/components/ui/button'
import { ChartContainer, ChartTooltip } from '@/components/ui/chart'
import { tooltip } from '@/components/chart-parts'
import { axis, formatTick } from '@/lib/chart'
import { statsQuery } from '@/lib/queries'
import { formatAgo, formatBytes } from '@/lib/units'

const config = {
  views: { label: 'Views', color: 'var(--chart-1)' },
  events: { label: 'Product events', color: 'var(--chart-2)' },
  measures: { label: 'Measures', color: 'var(--chart-3)' },
}

function Tile({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-lg border p-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-lg font-semibold">{value}</span>
      {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
    </div>
  )
}

/** Events per day by family, and tiles for total, freshness, size and days kept. */
export default function UsageSection({ projectId, range }: { projectId: number; range: { from: string; to: string } }) {
  const { data, isError, refetch } = useQuery(statsQuery({ project_id: projectId, from: range.from, to: range.to }))
  const s = data?.projects[0]
  return (
    <section aria-label="Usage" className="flex flex-col gap-3 rounded-lg border p-4">
      <h2 className="text-base font-semibold">Usage</h2>
      {isError ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          Couldn't load usage. <Button variant="outline" size="sm" onClick={() => void refetch()}>Retry</Button>
        </div>
      ) : !s ? null : (
        <>
          <ChartContainer config={config} className="h-56 w-full">
            <BarChart data={s.series} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
              <CartesianGrid vertical={false} />
              <XAxis dataKey="day" {...axis} tickFormatter={formatTick} />
              <YAxis {...axis} width={40} />
              <ChartTooltip content={tooltip('number', { total: true })} />
              <Bar dataKey="views" stackId="a" fill="var(--color-views)" />
              <Bar dataKey="events" stackId="a" fill="var(--color-events)" />
              <Bar dataKey="measures" stackId="a" fill="var(--color-measures)" radius={[4, 4, 0, 0]} />
            </BarChart>
          </ChartContainer>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Tile label="Events" value={(s.totals.views + s.totals.events + s.totals.measures).toLocaleString()}
              hint={`${s.totals.views.toLocaleString()} views · ${s.totals.events.toLocaleString()} product · ${s.totals.measures.toLocaleString()} measures`} />
            <Tile label="Last received" value={s.last_received_at ? formatAgo(s.last_received_at) : 'Nothing received yet'} />
            <Tile label="Data size" value={s.size ? formatBytes(s.size.total_bytes) : 'unknown'}
              hint={s.size ? `${formatBytes(s.size.raw_bytes)} raw · ${formatBytes(s.size.aggregate_bytes)} aggregates (estimate)` : undefined} />
            <Tile label="Days kept" value={`${s.raw_days + s.rolled_up_days}`} hint={`${s.raw_days} raw · ${s.rolled_up_days} rolled up${s.first_day ? ` · since ${s.first_day}` : ''}`} />
          </div>
          {s.unused_attributes.length > 0 && (
            <p className="text-sm text-muted-foreground">
              Declared but not sent in this range: {s.unused_attributes.map((a) => <code key={a} className="mx-0.5 rounded bg-muted px-1">{a}</code>)}
            </p>
          )}
        </>
      )}
    </section>
  )
}
```

(Check `tooltip`'s and `axis`'s real signatures in `chart-parts.tsx` / `@/lib/chart` and adapt; the Bar widget uses both. The test asserts tiles, not the chart.)

`web/src/components/projects/CapImpactSection.tsx`:

```tsx
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { CapUsageRow } from '@/lib/api'
import { capUsageQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'

/** Capped first (most capped days first), then by name. */
function order(rows: CapUsageRow[]): CapUsageRow[] {
  return [...rows].sort((a, b) => b.days_capped - a.days_capped || a.dimension.localeCompare(b.dimension))
}

/** Per capped dimension: the busiest day against the cap, days capped, and the share folded into (other). */
export default function CapImpactSection({ projectId, range }: { projectId: number; range: { from: string; to: string } }) {
  const { data, isError, refetch } = useQuery(capUsageQuery(projectId, range))
  return (
    <section aria-label="Cap impact" className="flex flex-col gap-3 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">Cap impact</h2>
        <p className="text-sm text-muted-foreground">
          Days already rolled up keep only the kept values and the (other) row, so their values never exceed the cap + 1.
        </p>
      </header>
      {isError ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          Couldn't load cap impact. <Button variant="outline" size="sm" onClick={() => void refetch()}>Retry</Button>
        </div>
      ) : !data ? null : data.dimensions.length === 0 ? (
        <p className="text-sm text-muted-foreground">No data in this range.</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Dimension</TableHead><TableHead>Cap</TableHead><TableHead>Busiest day</TableHead>
              <TableHead>Days capped</TableHead><TableHead>Folded</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {order(data.dimensions).map((d) => (
              <TableRow key={`${d.setting}/${d.dimension}`} aria-label={d.dimension} className={cn(d.days_capped > 0 && 'bg-amber-500/5')}>
                <TableCell className="font-medium">{d.dimension}</TableCell>
                <TableCell>{d.cap === 0 ? 'no cap' : d.cap.toLocaleString()}</TableCell>
                <TableCell>{d.max_values_per_day.toLocaleString()} <span className="text-xs text-muted-foreground">{d.max_day}</span></TableCell>
                <TableCell>{`${d.days_capped} of ${d.days}`}</TableCell>
                <TableCell>
                  {d.folded_share === null ? '—' : (
                    <span className="flex items-center gap-2">
                      <span className="h-1.5 w-16 overflow-hidden rounded bg-muted">
                        <span className="block h-full bg-amber-500" style={{ width: `${Math.round(d.folded_share * 100)}%` }} />
                      </span>
                      {`${Math.round(d.folded_share * 100)}%`}
                    </span>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </section>
  )
}
```

In `web/src/pages/Project.tsx`: add `RangeSwitcher` state and both sections.

```tsx
import RangeSwitcher, { type RangeValue } from '@/components/RangeSwitcher'
import CapImpactSection from '@/components/projects/CapImpactSection'
import UsageSection from '@/components/projects/UsageSection'
import { resolve } from '@/lib/ranges'
// inside the component:
const [rangeValue, setRangeValue] = useState<RangeValue>({ range: '30d' })
const tz = dash?.timezone ?? 'UTC'
const range = resolve(rangeValue.range, tz, new Date(), rangeValue.from && rangeValue.to ? { from: rangeValue.from, to: rangeValue.to } : undefined)
```

Render `<RangeSwitcher value={rangeValue} timezone={tz} onChange={setRangeValue} />` in the header (left of Archive/Restore), `<UsageSection projectId={id} range={range} />` before `DetailsSection`, and `<CapImpactSection projectId={id} range={range} />` after `KeysSection`.

- [ ] **Step 4: Run the tests**

Run: `cd web && npx vitest run && npx tsc -b && npm run build`
Expected: PASS, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): show a project's usage and cap impact on its page

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Playwright e2e

**Files:**
- Create: `web/e2e/projects.spec.ts`

**Interfaces:**
- Consumes: `serve.sh` (token `e2e-token`, password `e2e-pass`, a seeded project `dev` with demo data from `scripts/seed-demo.py`).

- [ ] **Step 1: Write the spec** — `web/e2e/projects.spec.ts` (copy `login`, `PASSWORD`, `TOKEN` and `authHeaders` from `app.spec.ts` as module-local helpers):

```ts
import { expect, test, type Page } from '@playwright/test'

const PASSWORD = 'e2e-pass'

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/dashboards\/\d+/)
}

test('creates a project, edits it, manages a key, archives and restores it', async ({ page }) => {
  await login(page)
  await page.goto('/app/projects')
  await expect(page.getByRole('article', { name: 'dev' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Limits' })).toBeVisible()

  const name = `e2e-${Date.now()}`
  await page.getByRole('button', { name: 'New project' }).click()
  await page.getByLabel('Name').fill(name)
  await page.getByLabel('Allowed origins').fill('https://e2e.example')
  await page.getByLabel('Allowed origins').press('Enter')
  await page.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText('Project created')).toBeVisible()
  await expect(page.getByText(/^ak_/)).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('article', { name }).getByRole('link', { name }).click()
  await expect(page.getByRole('heading', { name })).toBeVisible()

  const details = page.getByRole('region', { name: 'Details' })
  await details.getByRole('button', { name: 'Edit' }).click()
  await page.getByLabel('Allowed origins').fill('*')
  await page.getByLabel('Allowed origins').press('Enter')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(details.getByText('*', { exact: true })).toBeVisible()

  const keys = page.getByRole('region', { name: 'Ingest keys' })
  await keys.getByRole('button', { name: 'Issue key' }).click()
  await page.getByLabel('Label').fill('ios app')
  await page.getByRole('button', { name: 'Issue' }).click()
  await expect(page.getByText('Key issued')).toBeVisible()
  await page.keyboard.press('Escape')
  await keys.getByRole('button', { name: 'Disable ios app' }).click()
  await page.getByRole('button', { name: 'Disable key' }).click()
  await expect(keys.getByRole('row', { name: /ios app/ }).getByText('disabled')).toBeVisible()

  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await page.getByRole('button', { name: 'Archive project' }).click()
  await expect(page.getByText('Archived', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Restore' }).click()
  await expect(page.getByRole('button', { name: 'Archive', exact: true })).toBeVisible()
})

test('shows usage and cap impact for the seeded project', async ({ page }) => {
  await login(page)
  await page.goto('/app/projects')
  await page.getByRole('article', { name: 'dev' }).getByRole('link', { name: 'dev' }).click()
  const usage = page.getByRole('region', { name: 'Usage' })
  await expect(usage.getByText('Events')).toBeVisible()
  await expect(usage.locator('.recharts-bar-rectangle').first()).toBeVisible()
  await expect(page.getByRole('region', { name: 'Cap impact' }).getByRole('row').nth(1)).toBeVisible()
})

test('keeps the sidebar projects collapsed until opened', async ({ page }) => {
  await login(page)
  await expect(page.getByRole('link', { name: 'Projects' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'dev', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: 'Show projects' }).click()
  await expect(page.getByRole('link', { name: 'dev', exact: true })).toBeVisible()
})
```

If `scripts/seed-demo.py` seeds no data in the default 30-day window, adjust the second test to pick the "Last 90 days" range or assert the empty states instead; read the script first.

- [ ] **Step 2: Run the suite**

Run: `cd web && npm run e2e -- projects.spec.ts`
Expected: 3 passed. Then run the whole suite: `cd web && npm run e2e` — all pass.

- [ ] **Step 3: Commit**

```bash
git add web/e2e/projects.spec.ts
git commit -m "test(web): cover projects, keys and usage end to end

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Whole-branch check and PR

**Files:**
- Modify: PR #120 body.

- [ ] **Step 1: Full checks**

Run: `export PATH=$PATH:/usr/local/go/bin && make check` then `cd web && npm run e2e`.
Expected: both pass. Fix anything they find in the task that owns it.

- [ ] **Step 2: Manual look** — run the server on a copy of a seeded database (`web/e2e/serve.sh` or `make build` and `serve`), open `/app/projects` at 1280×800 and 390×844, check the cards, the sidebar group, a project page with data and one without, and the Limits panel. Screenshots go in the PR.

- [ ] **Step 3: PR body** — replace PR #120's body with Problems → What changes → Why → Breaking (`list_ingest_keys` fields) → Tests (counts from Step 1) → Follow-ups, ending with the Claude Code footer.

- [ ] **Step 4: Push**

```bash
git push origin feat/projects-console
```
