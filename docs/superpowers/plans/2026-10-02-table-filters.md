# Table Filters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every `table` widget gets a filter bar, server-backed sort and paging; a `mode: "remote"` table runs filters, sort and paging in SQL over its whole result.

**Architecture:** `readsql` (the one package that wraps custom SQL) gains two deterministic SQL functions (`tw_cell`, `tw_num`) and `QueryPage`, a generic wrap that filters, sorts, counts and pages a checked query in one run. `reporting.WidgetData` routes a remote table's request through it and echoes a `page` block; `widget_data` and its REST route take `filters`, `sort`, `distinct`, `offset`, `limit`. The web app gets one TS executor with the same rules (`web/src/lib/table-view.ts`), used by local tables and the gallery, and a filter bar shared by both modes; `WidgetCard` owns the view state and sends it for remote tables.

**Tech Stack:** Go 1.x, modernc.org/sqlite (pure-Go SQLite), React + TypeScript, TanStack Query, shadcn/ui (combobox, popover), vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-02-table-filters-design.md` — read it before any task; this plan argues from it.

## Global Constraints

- Go is at `/usr/local/go/bin` (not on PATH): `export PATH=$PATH:/usr/local/go/bin` in every shell.
- No new Go dependency. Web: use the shadcn components already in `web/src/components/ui/` (`combobox.tsx`, `popover.tsx`, `badge.tsx`, `button.tsx`, `input.tsx`, `select.tsx`); no new npm package.
- `readsql` stays a rank-0 generic leaf (`internal/archtest`): no twillingate domain words (widget, table, dashboard) in its API or messages.
- Refusals: `readsql` returns errors wrapping `readsql.ErrRefused`; `reporting` and `api` surface them as `store.ErrInvalid` (via `store.Refuse`), matched with `errors.Is`, never by text.
- No new caps or env vars. The page size is `CONSOLE_QUERY_MAX_ROWS` (`(*readsql.DB).MaxRows()`): `limit` defaults to it and may not exceed it.
- Every value is bound (`?`); every column name is quoted as an identifier with `"` doubled, after being checked against the query's own result columns.
- Docs change in the same commit as the code they describe (CLAUDE.md table): `docs/reporting.md` for `widget_data` arguments and the `table` component; `internal/reporting/ui/components.json` regenerated (`cd web && npm run build`) and committed when the table's contract changes.
- Conventional Commits, lower case, imperative, scope from the tree; every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Shared machine: run at most one `make check` at a time.
- Comments match the surrounding code's density and voice (full sentences, say why).

## Review Focus

1. **Column aliases that are not plain words** (`"Users (at least)"`, a name with `"` in it, non-ASCII) in `filters`, `sort` and `distinct` — expected to work exactly like plain names. Pinned in Task 2 (`a"b`, `Users (at least)`) and Task 3 (the system widget's own aliases).
2. **Refresh and auto-refresh on a remote table** — expected to refetch the page the viewer is on with the same filters and sort, not page 1 unfiltered. Pinned in Task 7 (`refreshWidget` gets the view).
3. **Range or project change while on page 3** — expected to go back to page 1 with filters and sort kept. Pinned in Task 7.
4. **A stored filter on a column the query no longer returns** (widget SQL edited, column renamed) — expected: the chip shows greyed, "not in this table", it is not sent, and the table loads. Pinned in Task 6 (local) and Task 7 (remote request omits it).
5. **`distinct` on a high-cardinality column** (`Value` with thousands of values) — expected: the picker offers the most frequent `limit` values with their counts and says it is showing the most frequent ones, rather than freezing or silently hiding the rest. Pinned in Task 6 (picker note) and Task 2 (`distinct` honours `limit`/`offset`).

---

## File Structure

| File | Responsibility |
| --- | --- |
| `internal/shared/readsql/cell.go` (new) | `tw_cell` / `tw_num` registration and the Go rules (`Cell`, `CellNumber`, `IsDecimal`) |
| `internal/shared/readsql/page.go` (new) | `Filter`, `Sort`, `Page`, `PageResult`, `(*DB).QueryPage` |
| `internal/shared/readsql/cell_test.go`, `page_test.go` (new) | unit tests, plus the shared cases file runner |
| `internal/reporting/testdata/table-filters.json` (new) | cases both executors run |
| `internal/reporting/view.go` (new) | parse and check `filters`/`sort`/`distinct`/`offset`/`limit`; `PageInfo` |
| `internal/reporting/data.go` | remote path in `WidgetData`, cache key, envelope |
| `internal/reporting/dev.go` | dev data handler passes the view through |
| `internal/api/ops_reporting.go` | `widgetDataIn` arguments and tool description |
| `internal/reporting/system/product/attribute-values.{sql,json}` | remote mode, every row |
| `web/src/lib/table-view.ts` (new) | TS executor: `cellNumber`, `compareCells`, `applyView`, `distinctValues` |
| `web/src/components/widgets/table-filters.tsx` (new) | `FilterBar`, chips, `FilterEditor` popover, footer |
| `web/src/components/widgets/table.tsx` | contract `mode`, local executor, bar, footer, remote rendering |
| `web/src/components/widgets/types.ts` | `TableView`, `WidgetProps.view/onView/fetchDistinct/viewError/reloading` |
| `web/src/components/WidgetCard.tsx`, `web/src/lib/widget-query.ts`, `web/src/lib/api.ts` | view state, request, keep-previous, refresh with view |
| `web/e2e/app.spec.ts` | e2e: filter, page, reload |
| `docs/reporting.md` | arguments, rules, component row, remote example |

---

### Task 1: `tw_cell` and `tw_num` in readsql, and the shared cases file

**Files:**
- Create: `internal/shared/readsql/cell.go`
- Create: `internal/shared/readsql/cell_test.go`
- Create: `internal/reporting/testdata/table-filters.json`

**Interfaces:**
- Produces (Go, package `readsql`):
  - `func Cell(v any) string` — the scan conversion: exactly what `sql.NullString.Scan(v)` yields, `""` for nil.
  - `func IsDecimal(s string) bool` — `^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`.
  - `func CellNumber(s string) (float64, bool)` — `IsDecimal(s)` and `strconv.ParseFloat` succeeds with a finite result.
  - SQL functions on every connection the `sqlite` driver opens after package init: `tw_cell(x)` → TEXT (`Cell(x)`), `tw_num(x)` → REAL or NULL (`CellNumber(Cell(x))`).
- Produces (file): `internal/reporting/testdata/table-filters.json`, schema below; Task 2 (Go) and Task 5 (TS) read it.

- [ ] **Step 1: Write the failing test**

`internal/shared/readsql/cell_test.go`:

```go
package readsql

import (
	"context"
	"testing"
	"time"
)

// TestCellMatchesScan: tw_cell must render a value exactly as Run's scan
// does (a sql.NullString per cell), or a filter would compare against
// text the table never shows.
func TestCellMatchesScan(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 100)
	q := `SELECT 100, 1.5, 0.1 + 0.2, 1e21, 0.000001, -3, 'abc', '', NULL, x'6869'`
	res, err := db.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	cells, err := db.Query(context.Background(),
		`SELECT tw_cell(100), tw_cell(1.5), tw_cell(0.1 + 0.2), tw_cell(1e21), tw_cell(0.000001),
		        tw_cell(-3), tw_cell('abc'), tw_cell(''), tw_cell(NULL), tw_cell(x'6869')`)
	if err != nil {
		t.Fatal(err)
	}
	for i := range res.Rows[0] {
		if got, want := cells.Rows[0][i], res.Rows[0][i]; got != want {
			t.Errorf("column %d: tw_cell = %q, scan = %q", i, got, want)
		}
	}
}

func TestCellNumber(t *testing.T) {
	for s, want := range map[string]bool{
		"0": true, "-12": true, "1.5": true, "1e21": true, "1e+21": true, "1e-06": true, "-0.25E3": true,
		"": false, " 1": false, "1 ": false, "+1": false, "1.": false, ".5": false, "0x10": false,
		"1e999": false, "NaN": false, "Infinity": false, "12abc": false, "2026-09-25": false, "v1.5.1": false,
	} {
		if _, ok := CellNumber(s); ok != want {
			t.Errorf("CellNumber(%q) ok = %v, want %v", s, ok, want)
		}
	}
	if n, _ := CellNumber("-0.25E3"); n != -250 {
		t.Errorf("CellNumber(-0.25E3) = %v", n)
	}
}

func TestTwNumInSQL(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 100)
	res, err := db.Query(context.Background(),
		`SELECT tw_num(100), tw_num('2026'), tw_num('12abc'), tw_num(NULL), tw_num(''), typeof(tw_num('x'))`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"100", "2026", "", "", "", "null"}
	for i, w := range want {
		if res.Rows[0][i] != w {
			t.Errorf("column %d = %q, want %q", i, res.Rows[0][i], w)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/shared/readsql && go test -run 'TestCell|TestTwNum' .`
Expected: FAIL — `undefined: CellNumber` (and `no such function: tw_cell` once it compiles).

- [ ] **Step 3: Write minimal implementation**

`internal/shared/readsql/cell.go`:

```go
package readsql

import (
	"database/sql"
	"database/sql/driver"
	"math"
	"regexp"
	"strconv"

	"modernc.org/sqlite"
)

// Two SQL functions that compare cells the way a reader sees them, not
// the way SQLite's type affinity does. SQLite compares a TEXT-affinity
// column with a bound number as text, but the same text produced by an
// expression orders after every number; a caller filtering a result it
// did not write cannot know which applies, and a browser repeating the
// filter on the rows it holds cannot either. Comparing through these two
// gives one answer for both.
//
//   - tw_cell(x): x as Run's scan renders it (Cell).
//   - tw_num(x): tw_cell(x) as a number when it is a decimal number, else NULL.
func init() {
	if err := sqlite.RegisterDeterministicScalarFunction("tw_cell", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return Cell(args[0]), nil
		}); err != nil {
		panic(err)
	}
	if err := sqlite.RegisterDeterministicScalarFunction("tw_num", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if n, ok := CellNumber(Cell(args[0])); ok {
				return n, nil
			}
			return nil, nil
		}); err != nil {
		panic(err)
	}
}

// Cell renders one value as Run does: through sql.NullString.Scan, so
// integers in decimal, reals in Go's shortest form, text and blobs as
// is, NULL as "".
func Cell(v any) string {
	var ns sql.NullString
	if err := ns.Scan(v); err != nil || !ns.Valid {
		return ""
	}
	return ns.String
}

var decimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// IsDecimal reports whether s is a decimal number as a cell shows one:
// optional minus, digits, optional fraction and exponent, no spaces.
func IsDecimal(s string) bool { return decimal.MatchString(s) }

// CellNumber is s as a number when it is a finite decimal number.
func CellNumber(s string) (float64, bool) {
	if !IsDecimal(s) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, false
	}
	return n, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/shared/readsql && go test -run 'TestCell|TestTwNum' -v .`
Expected: PASS. If `TestCellMatchesScan` fails on a blob or real, fix `Cell`, never the test: the scan is the reference.

- [ ] **Step 5: Write the shared cases file**

`internal/reporting/testdata/table-filters.json`. `rows` are SQL-typed (JSON integer → SQL integer, JSON number with `.` or exponent → SQL real, string → text, `null` → NULL); `cells` are the same rows as the table shows them (Task 2 asserts `cells` equals what readsql returns, so the file cannot drift). Every case names the expected rows as indexes into `rows`, in order. A case without `sort` expects the query's order.

```json
{
  "columns": ["Attribute", "Day", "Value", "Count", "Users (at least)"],
  "rows": [
    ["$os",      "2026-09-24", "other",  1069, 33],
    ["$os",      "2026-09-26", "iOS",     100, null],
    ["plan",     "2026-09-25", "2026",    100, 7],
    ["plan",     "2026-09-26", "",        12.5, 2],
    ["auth_sso", "2026-09-26", "off",     587, 6],
    ["auth_sso", "2026-09-27", "on",      9, 6],
    ["$os",      "2026-09-27", null,      100, 1],
    ["v",        "2026-09-27", "v1.10",   3, 1],
    ["v",        "2026-09-27", "v1.9",    4, 1],
    ["v",        "2026-09-28", "Ångström", 5, 1]
  ],
  "cells": [
    ["$os",      "2026-09-24", "other",  "1069", "33"],
    ["$os",      "2026-09-26", "iOS",    "100",  ""],
    ["plan",     "2026-09-25", "2026",   "100",  "7"],
    ["plan",     "2026-09-26", "",       "12.5", "2"],
    ["auth_sso", "2026-09-26", "off",    "587",  "6"],
    ["auth_sso", "2026-09-27", "on",     "9",    "6"],
    ["$os",      "2026-09-27", "",       "100",  "1"],
    ["v",        "2026-09-27", "v1.10",  "3",    "1"],
    ["v",        "2026-09-27", "v1.9",   "4",    "1"],
    ["v",        "2026-09-28", "Ångström", "5",  "1"]
  ],
  "cases": [
    {"name": "no filters keeps query order", "filters": [], "expect": [0, 1, 2, 3, 4, 5, 6, 7, 8, 9]},
    {"name": "in", "filters": [{"column": "Attribute", "op": "in", "value": ["$os", "plan"]}], "expect": [0, 1, 2, 3, 6]},
    {"name": "not in keeps all but the listed", "filters": [{"column": "Attribute", "op": "not in", "value": ["$os", "plan", "v"]}], "expect": [4, 5]},
    {"name": "= on an integer equals its text", "filters": [{"column": "Count", "op": "=", "value": "100"}], "expect": [1, 2, 6]},
    {"name": "= is exact and case-sensitive", "filters": [{"column": "Value", "op": "=", "value": "ios"}], "expect": []},
    {"name": "= never matches an empty cell", "filters": [{"column": "Value", "op": "=", "value": ""}], "expect": []},
    {"name": "!= matches empty cells", "filters": [{"column": "Value", "op": "!=", "value": "off"}], "expect": [0, 1, 2, 3, 5, 6, 7, 8, 9]},
    {"name": "not in matches empty cells", "filters": [{"column": "Users (at least)", "op": "not in", "value": ["1"]}], "expect": [0, 1, 2, 3, 4, 5]},
    {"name": "> numeric", "filters": [{"column": "Count", "op": ">", "value": "99"}], "expect": [0, 1, 2, 4, 6]},
    {"name": "< numeric with a real", "filters": [{"column": "Count", "op": "<", "value": "12.5"}], "expect": [5, 7, 8, 9]},
    {"name": "> numeric skips empty and text cells", "filters": [{"column": "Value", "op": ">", "value": "2000"}], "expect": [2]},
    {"name": "> text on days", "filters": [{"column": "Day", "op": ">", "value": "2026-09-26"}], "expect": [5, 6, 7, 8, 9]},
    {"name": "< text value compares as text, numbers included", "filters": [{"column": "Count", "op": "<", "value": "2x"}], "expect": [0, 1, 2, 3, 6]},
    {"name": "and of two on one column", "filters": [{"column": "Count", "op": ">", "value": "5"}, {"column": "Count", "op": "<", "value": "200"}], "expect": [1, 2, 3, 5, 6]},
    {"name": "sort numeric desc, ties by every column", "filters": [], "sort": {"column": "Count", "dir": "desc"}, "expect": [0, 4, 1, 6, 2, 3, 5, 9, 8, 7]},
    {"name": "sort text asc by code point, empty last", "filters": [], "sort": {"column": "Value", "dir": "asc"}, "expect": [2, 1, 4, 5, 0, 7, 8, 9, 6, 3]},
    {"name": "sort text desc, empty still last", "filters": [], "sort": {"column": "Value", "dir": "desc"}, "expect": [2, 9, 8, 7, 0, 5, 4, 1, 6, 3]},
    {"name": "sort numbers before text, in both directions", "filters": [{"column": "Attribute", "op": "in", "value": ["plan", "$os"]}], "sort": {"column": "Value", "dir": "desc"}, "expect": [2, 0, 1, 6, 3]},
    {"name": "sort empty numeric cells last", "filters": [], "sort": {"column": "Users (at least)", "dir": "asc"}, "expect": [6, 7, 8, 9, 3, 4, 5, 2, 0, 1]}
  ],
  "distinct": [
    {"name": "values by frequency, then by value", "column": "Attribute", "filters": [], "expect": [["$os", 3], ["v", 3], ["auth_sso", 2], ["plan", 2]]},
    {"name": "ignores the filter on its own column", "column": "Attribute", "filters": [{"column": "Attribute", "op": "in", "value": ["plan"]}, {"column": "Count", "op": ">", "value": "99"}], "expect": [["$os", 3], ["auth_sso", 1], ["plan", 1]]},
    {"name": "empty cells are a value, last among equal counts", "column": "Value", "filters": [{"column": "Attribute", "op": "=", "value": "$os"}], "expect": [["iOS", 1], ["other", 1], ["", 1]]}
  ]
}
```

The expectations were derived from the spec's rules by a reference implementation (empty = `''` after `tw_cell`; equality on `tw_cell`; `<`/`>` numeric when the value `IsDecimal`, else text by code point with empty never matching; sort: non-empty first, numbers before text in both directions, numbers by value, text by code point, ties by every column ascending by the same rule; `distinct`: count desc, then the value by the sort rule ascending, so `''` is last among equal counts). Do not edit an `expect` list to make an implementation pass: if Go or TS disagrees with this file, the implementation is wrong unless you can show the list contradicts the spec, in which case fix the list and say so in the commit body.

- [ ] **Step 6: Commit**

```bash
git add internal/shared/readsql/cell.go internal/shared/readsql/cell_test.go internal/reporting/testdata/table-filters.json
git commit -m "feat(shared): compare result cells as shown, with tw_cell and tw_num

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `QueryPage` in readsql

**Files:**
- Create: `internal/shared/readsql/page.go`
- Create: `internal/shared/readsql/page_test.go`

**Interfaces:**
- Consumes: `Cell`, `IsDecimal`, `tw_cell`, `tw_num` (Task 1); `Check`, `Run`, `QueryLimit`, `ErrRefused`, `MaxRows` (existing).
- Produces (package `readsql`):

```go
type Filter struct {
	Column string
	Op     string   // "=", "!=", "<", ">", "in", "not in"
	Values []string // exactly one for =, !=, <, >; at least one for in, not in
}
type Sort struct {
	Column string
	Desc   bool
}
type Page struct {
	Filters  []Filter
	Sort     *Sort  // nil: the query's own order
	Distinct string // "": rows; a column: [value, rows] of that column
	Offset   int    // >= 0
	Limit    int    // 1..MaxRows(); 0 means MaxRows()
}
type PageResult struct {
	Result         // Columns, Rows (the page), Truncated = Matched > Offset+len(Rows)
	Matched, Total int
}
func (d *DB) QueryPage(ctx context.Context, q string, p Page, args ...any) (PageResult, error)
```

Refusals wrap `ErrRefused` with messages naming what to change. `Distinct` with `Sort` is refused. For `Distinct`, `Columns` is `["value", "rows"]`, `Matched` is the number of distinct values, `Total` is the number of rows before any filter.

- [ ] **Step 1: Write the failing tests**

`internal/shared/readsql/page_test.go` — the cases runner plus targeted tests:

```go
package readsql

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type pageCases struct {
	Columns []string            `json:"columns"`
	Rows    [][]json.RawMessage `json:"rows"`
	Cells   [][]string          `json:"cells"`
	Cases   []struct {
		Name    string           `json:"name"`
		Filters []map[string]any `json:"filters"`
		Sort    *struct{ Column, Dir string } `json:"sort"`
		Expect  []int            `json:"expect"`
	} `json:"cases"`
	Distinct []struct {
		Name    string           `json:"name"`
		Column  string           `json:"column"`
		Filters []map[string]any `json:"filters"`
		Expect  [][]any          `json:"expect"`
	} `json:"distinct"`
}

func loadPageCases(t *testing.T) pageCases {
	t.Helper()
	b, err := os.ReadFile("../../reporting/testdata/table-filters.json")
	if err != nil {
		t.Fatal(err)
	}
	var c pageCases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// valuesSQL renders the cases' rows as one SELECT ... UNION ALL ...
// with SQL-typed literals, aliased to the cases' column names, so the
// wrap sees the same shapes a widget query produces.
func valuesSQL(t *testing.T, c pageCases) string {
	t.Helper()
	var sel []string
	for ri, row := range c.Rows {
		var cols []string
		for ci, raw := range row {
			lit := "NULL"
			s := string(raw)
			switch {
			case s == "null":
			case strings.HasPrefix(s, `"`):
				var v string
				json.Unmarshal(raw, &v)
				lit = "'" + strings.ReplaceAll(v, "'", "''") + "'"
			case strings.ContainsAny(s, ".eE"):
				lit = "CAST(" + s + " AS REAL)"
			default:
				lit = s
			}
			if ri == 0 {
				lit += ` AS "` + strings.ReplaceAll(c.Columns[ci], `"`, `""`) + `"`
			}
			cols = append(cols, lit)
		}
		sel = append(sel, "SELECT "+strings.Join(cols, ", "))
	}
	return strings.Join(sel, "\nUNION ALL\n")
}

func toFilters(in []map[string]any) []Filter {
	var out []Filter
	for _, f := range in {
		var vals []string
		switch v := f["value"].(type) {
		case string:
			vals = []string{v}
		case []any:
			for _, x := range v {
				vals = append(vals, x.(string))
			}
		}
		out = append(out, Filter{Column: f["column"].(string), Op: f["op"].(string), Values: vals})
	}
	return out
}

func TestQueryPageCases(t *testing.T) {
	c := loadPageCases(t)
	db, _ := newTestDB(t, 5*time.Second, 1000)
	ctx := context.Background()
	q := valuesSQL(t, c)

	all, err := db.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all.Rows, c.Cells) {
		t.Fatalf("the cases file's cells disagree with the scan:\n got  %q\n want %q", all.Rows, c.Cells)
	}
	for _, tc := range c.Cases {
		p := Page{Filters: toFilters(tc.Filters)}
		if tc.Sort != nil {
			p.Sort = &Sort{Column: tc.Sort.Column, Desc: tc.Sort.Dir == "desc"}
		}
		got, err := db.QueryPage(ctx, q, p)
		if err != nil {
			t.Errorf("%s: %v", tc.Name, err)
			continue
		}
		want := [][]string{}
		for _, i := range tc.Expect {
			want = append(want, c.Cells[i])
		}
		if !reflect.DeepEqual(got.Rows, want) {
			t.Errorf("%s:\n got  %q\n want %q", tc.Name, got.Rows, want)
		}
		if got.Matched != len(tc.Expect) || got.Total != len(c.Rows) {
			t.Errorf("%s: matched %d total %d, want %d %d", tc.Name, got.Matched, got.Total, len(tc.Expect), len(c.Rows))
		}
	}
	for _, tc := range c.Distinct {
		got, err := db.QueryPage(ctx, q, Page{Distinct: tc.Column, Filters: toFilters(tc.Filters)})
		if err != nil {
			t.Errorf("%s: %v", tc.Name, err)
			continue
		}
		var want [][]string
		for _, e := range tc.Expect {
			want = append(want, []string{e[0].(string), strconv.Itoa(int(e[1].(float64)))})
		}
		if !reflect.DeepEqual(got.Columns, []string{"value", "rows"}) || !reflect.DeepEqual(got.Rows, want) {
			t.Errorf("%s:\n got  %v %q\n want %q", tc.Name, got.Columns, got.Rows, want)
		}
	}
}

// Joined pages equal the whole, for page sizes that do and do not
// divide the total, so a viewer paging through never sees a row twice
// or misses one.
func TestQueryPageJoinedPagesEqualTheWhole(t *testing.T) {
	c := loadPageCases(t)
	db, _ := newTestDB(t, 5*time.Second, 1000)
	ctx := context.Background()
	q := valuesSQL(t, c)
	sort := &Sort{Column: "Count", Desc: true}
	whole, err := db.QueryPage(ctx, q, Page{Sort: sort})
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 3, 7, len(c.Rows)} {
		var joined [][]string
		for off := 0; off < len(c.Rows); off += size {
			p, err := db.QueryPage(ctx, q, Page{Sort: sort, Offset: off, Limit: size})
			if err != nil {
				t.Fatal(err)
			}
			if want := p.Matched > off+len(p.Rows); p.Truncated != want {
				t.Errorf("size %d offset %d: truncated %v, want %v", size, off, p.Truncated, want)
			}
			joined = append(joined, p.Rows...)
		}
		if !reflect.DeepEqual(joined, whole.Rows) {
			t.Errorf("size %d: joined pages differ from the whole", size)
		}
	}
}

func TestQueryPageZeroMatchesStillCounts(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	got, err := db.QueryPage(context.Background(), `SELECT 1 AS a UNION ALL SELECT 2`,
		Page{Filters: []Filter{{Column: "a", Op: ">", Values: []string{"5"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 0 || got.Matched != 0 || got.Total != 2 || got.Truncated {
		t.Errorf("got %+v", got)
	}
	if !reflect.DeepEqual(got.Columns, []string{"a"}) {
		t.Errorf("columns %v, want [a] (no counting columns)", got.Columns)
	}
}

// Names and values are inert: a column alias with a quote in it filters
// and sorts like any other, and a value is bound, never spliced.
func TestQueryPageNamesAndValuesAreInert(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	ctx := context.Background()
	q := `SELECT 'x''; DROP TABLE meta; --' AS "a""b", 3 AS "Users (at least)" UNION ALL SELECT 'y', 1`
	got, err := db.QueryPage(ctx, q, Page{
		Filters: []Filter{{Column: `a"b`, Op: "in", Values: []string{"x'; DROP TABLE meta; --", "z"}}},
		Sort:    &Sort{Column: "Users (at least)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 1 || got.Rows[0][0] != "x'; DROP TABLE meta; --" {
		t.Errorf("rows %q", got.Rows)
	}
	if !reflect.DeepEqual(got.Columns, []string{`a"b`, "Users (at least)"}) {
		t.Errorf("columns %v", got.Columns)
	}
}

// A widget column named like a counting column must not collide.
func TestQueryPageCountingColumnsDoNotCollide(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	got, err := db.QueryPage(context.Background(),
		`SELECT 1 AS __tw_total, 2 AS __tw_matched, 3 AS __tw_total_1`, Page{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Rows, [][]string{{"1", "2", "3"}}) || got.Total != 1 || got.Matched != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestQueryPageRefusals(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 10)
	ctx := context.Background()
	q := `SELECT 1 AS a, 'x' AS b`
	for name, p := range map[string]Page{
		"unknown column lists valid ones": {Filters: []Filter{{Column: "c", Op: "=", Values: []string{"1"}}}},
		"unknown operator":                {Filters: []Filter{{Column: "a", Op: "~", Values: []string{"1"}}}},
		"list for a single-value operator": {Filters: []Filter{{Column: "a", Op: "=", Values: []string{"1", "2"}}}},
		"empty in":                        {Filters: []Filter{{Column: "a", Op: "in"}}},
		"unknown sort column":             {Sort: &Sort{Column: "c"}},
		"unknown distinct column":         {Distinct: "c"},
		"distinct with sort":              {Distinct: "a", Sort: &Sort{Column: "a"}},
		"negative offset":                 {Offset: -1},
		"limit above the cap":             {Limit: 11},
		"negative limit":                  {Limit: -1},
	} {
		_, err := db.QueryPage(ctx, q, p)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", name, err)
		}
		if name == "unknown column lists valid ones" && (err == nil || !strings.Contains(err.Error(), "a, b")) {
			t.Errorf("%s: message %v does not list a, b", name, err)
		}
	}
	// What Check refuses, QueryPage refuses too: the wrap is no way around it.
	if _, err := db.QueryPage(ctx, `SELECT * FROM meta`, Page{}); !errors.Is(err, ErrRefused) {
		t.Errorf("meta: err = %v, want ErrRefused", err)
	}
	if _, err := db.QueryPage(ctx, `SELECT 1) UNION SELECT key FROM meta --`, Page{}); err == nil {
		t.Error("an escape from the wrap ran")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd internal/shared/readsql && go test -run TestQueryPage .`
Expected: FAIL — `db.QueryPage undefined`.

- [ ] **Step 3: Write the implementation**

`internal/shared/readsql/page.go`. Shape (write it out fully; keep each helper small):

```go
package readsql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Filter, Sort, Page, PageResult as in Interfaces above, each with a doc comment.

// QueryPage runs q wrapped so the database filters, sorts, counts and
// pages it in one statement (Run's deadline applies). It is QueryLimit's
// guard 3 extended, not a second path around it: q passes Check first,
// and the only text added around it is this package's own, with column
// names checked against q's result and quoted, and every value bound.
// Comparisons go through tw_cell and tw_num (cell.go), never SQLite's
// affinity, so they mean what the cells show.
func (d *DB) QueryPage(ctx context.Context, q string, p Page, args ...any) (PageResult, error) {
	// 1. Check limits: Offset >= 0; Limit 0 → d.maxRows; Limit < 0 or > d.maxRows refused
	//    ("limit must be between 1 and %d"). Distinct with Sort refused.
	// 2. cols: d.QueryLimit(ctx, q, 0, args...) — the result's columns (also runs Check).
	// 3. Validate every Filter (column in cols, op known, value count) and Sort/Distinct
	//    column; unknown → fmt.Errorf("%w: no column %q; columns are %s", ErrRefused, name,
	//    strings.Join(cols, ", ")).
	// 4. Pick counting names: "__tw_total", "__tw_matched", adding "_1", "_2", … while
	//    either collides with a column.
	// 5. Build, with ident(c) = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`, and
	//    trimmed = strings.TrimRight(q, "; \t\r\n\f") exactly as QueryLimit does:
	//
	//    rows:     SELECT <ident(cols)...>, <matched>, <total> FROM (
	//                SELECT *, COUNT(*) OVER () AS <matched> FROM (
	//                  SELECT *, COUNT(*) OVER () AS <total> FROM (<trimmed>
	//                  )) WHERE <where>)
	//              ORDER BY <order> LIMIT ? OFFSET ?
	//    distinct: SELECT tw_cell(<c>) AS value, COUNT(*) AS rows, MAX(<matched>)… — see below
	//
	//    <where>: "1" when no filters; else each filter ANDed:
	//      =        tw_cell(c) <> '' AND tw_cell(c) = ?
	//      !=       (tw_cell(c) = '' OR tw_cell(c) <> ?)
	//      in       tw_cell(c) <> '' AND tw_cell(c) IN (?, ?, …)
	//      not in   (tw_cell(c) = '' OR tw_cell(c) NOT IN (?, ?, …))
	//      < / >    IsDecimal(v): tw_num(c) IS NOT NULL AND tw_num(c) < ?   (bind the float64)
	//               otherwise:    tw_cell(c) <> '' AND tw_cell(c) < ?       (bind the string;
	//               SQLite's BINARY collation over UTF-8 is code-point order)
	//    <order>: sortKeys(sort column, desc) then sortKeys(every column, asc), where
	//      sortKeys(c, desc) = (tw_cell(c) = '') ASC, (tw_num(c) IS NULL) ASC,
	//                          tw_num(c) <dir>, tw_cell(c) <dir>
	//      With no Sort: the query's own order — SQLite keeps a subquery's ORDER BY
	//      through these wrappers in practice; order by nothing extra (do NOT add the
	//      every-column tiebreak, it would replace the query's order).
	// 6. Run with args followed by the bound values, then Limit, Offset. Strip the two
	//    counting columns from Columns and every row; Matched/Total from the first row.
	// 7. Zero rows returned: if Offset == 0, Matched = 0; Total from a second run of
	//    SELECT COUNT(*) FROM (<trimmed>\n); if Offset > 0, run the count-only form of
	//    the filtered wrap (SELECT COUNT(*) … WHERE <where>) for Matched and the
	//    unfiltered COUNT(*) for Total.
	// 8. Truncated = Matched > Offset + len(Rows).
	//
	// Distinct: the other filters only (drop every filter whose Column == Distinct):
	//    SELECT tw_cell(c) AS value, COUNT(*) AS rows, COUNT(*) OVER () AS <matched>,
	//           MAX(<total>) OVER () AS <total>
	//    FROM (SELECT *, COUNT(*) OVER () AS <total> FROM (<trimmed>
	//    )) WHERE <where>
	//    GROUP BY tw_cell(c)
	//    ORDER BY rows DESC, (value = '') ASC, (tw_num(value) IS NULL) ASC, tw_num(value) ASC, value ASC
	//    LIMIT ? OFFSET ?
	//  Columns are ["value", "rows"]; Matched is the number of distinct values;
	//  Total the rows before filters (a zero-row answer falls back to step 7's counts).
	//  The empty value orders by the sort rule: last among equal counts.
	return PageResult{}, nil
}
```

Write the real code for each numbered step; the comment block above is the specification of what that code does and should be reduced to the doc comment plus short why-comments when you are done. In particular `MAX(total) OVER ()` in the distinct query is needed because `total` is constant per row but not grouped.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd internal/shared/readsql && go test -v -run 'TestQueryPage|TestCell|TestTwNum' . && go test .`
Expected: PASS, and the whole package still passes.

If a case in the cases file fails, the implementation is wrong unless you can show the expected list contradicts the spec (see the note under the cases file in Task 1); then fix the list and say so in the commit body.

- [ ] **Step 5: Commit**

```bash
git add internal/shared/readsql/page.go internal/shared/readsql/page_test.go internal/reporting/testdata/table-filters.json
git commit -m "feat(shared): filter, sort and page a checked query in one statement

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Remote tables in `reporting` and `widget_data`

**Files:**
- Create: `internal/reporting/view.go`
- Create: `internal/reporting/view_test.go`
- Modify: `internal/reporting/data.go` (`DataRequest`, `WidgetData`, `WidgetData()`, `cacheKey`)
- Modify: `internal/reporting/dev.go:~290-310` (dev data handler)
- Modify: `internal/api/ops_reporting.go:55-60` (`widgetDataIn`), `:127-130` (`widgetData`), `:227-229` (description)
- Modify: `internal/api/` tests for the route if a REST arguments test exists (search `widget_data` in `internal/api/*_test.go`)
- Modify: `docs/reporting.md` (envelope section ~line 86–100, tool table line ~187, HTTP API table line ~223, and a new "Filtering and paging a table" subsection after the envelope bullets)

**Interfaces:**
- Consumes: `readsql.Filter`, `readsql.Sort`, `readsql.Page`, `readsql.PageResult`, `(*readsql.DB).QueryPage`, `MaxRows()` (Task 2).
- Produces (package `reporting`):

```go
// DataRequest gains (all optional; zero means absent):
Filters  string // JSON: [{"column": "...", "op": "...", "value": "..." | ["..."]}]
Sort     string // "<column>:asc" | "<column>:desc", split on the last ':'
Distinct string // a column name
Offset   int
Limit    int    // 0: MaxRows()

// WidgetData gains:
Page *PageInfo `json:"page,omitempty"`

type FilterSpec struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  any    `json:"value"` // string, or []string for in / not in
}
type PageInfo struct {
	Offset   int          `json:"offset"`
	Limit    int          `json:"limit"`
	Matched  int          `json:"matched"`
	Total    int          `json:"total"`
	Sort     string       `json:"sort,omitempty"`
	Distinct string       `json:"distinct,omitempty"`
	Filters  []FilterSpec `json:"filters"`
}

// view.go
type view struct {
	page    readsql.Page
	echo    PageInfo // Matched/Total filled after the run
	present bool     // any of Filters/Sort/Distinct/Offset/Limit was given
}
func parseView(in DataRequest, maxRows int) (view, error) // store.ErrInvalid refusals
func remoteTable(w store.Widget) bool // w.Component == "table" && props.mode == "remote"
func (v view) cacheSuffix() string    // canonical, "" when !present
```

- api (`widgetDataIn`) gains, with these `jsonschema` texts:
  - `Filters string json:"filters,omitempty"` — `"remote tables only (props.mode \"remote\"): a JSON list of {column, op, value}; op is =, !=, <, >, in or not in; in/not in take a list. See docs://reporting, Filtering and paging a table"`
  - `Sort string json:"sort,omitempty"` — `"remote tables only: <column>:asc or <column>:desc; the whole result is sorted, not the page"`
  - `Distinct string json:"distinct,omitempty"` — `"remote tables only: return [value, rows] for this column among rows matching the other filters, most frequent first"`
  - `Offset int json:"offset,omitempty"` — `"remote tables only: rows to skip"`
  - `Limit int json:"limit,omitempty"` — `"remote tables only: page size, 1 to CONSOLE_QUERY_MAX_ROWS (the default)"`

- [ ] **Step 1: Write the failing tests**

`internal/reporting/view_test.go`, using the package's existing helpers (`newTestStoreAndReadDB`, `mustCreateProject`, `rawExec`; read `helpers_test.go` and `ops_test.go` for how a test creates a user dashboard with a widget — reuse that, do not invent a new fixture). Tests to write, each a `func Test…`:

1. `TestRemoteTablePagesFiltersAndSorts` — a user dashboard with a `table` widget, props `{"mode":"remote"}`, SQL `SELECT 'a' AS "Key", 3 AS "N" UNION ALL SELECT 'b', 1 UNION ALL SELECT 'c', 2`. `WidgetData` with `Filters: `[{"column":"N","op":">","value":"1"}]``, `Sort: "N:desc"`, `Limit: 1` returns rows `[["a","3"]]`, `Page` = `{Offset:0, Limit:1, Matched:2, Total:3, Sort:"N:desc", Filters:[{N > 1}]}`, and `data.truncated == true`. Offset 1 returns `[["c","2"]]`, truncated false.
2. `TestRemoteTableDefaultsToTheCap` — no paging arguments: `Page.Limit == db.MaxRows()`, every row returned, `Page` present (a remote table always echoes `page`).
3. `TestPagingArgumentsRefusedOffRemoteTables` — the same SQL as a plain `table` (no props), and `SELECT 'a' AS label, 3 AS value` as a `bar_list` (inputs `label` text, `value` number): each of `Filters`, `Sort`, `Distinct`, `Offset: 1`, `Limit: 5` alone is `errors.Is(err, store.ErrInvalid)`, message contains `mode`.
4. `TestViewRefusals` — on the remote widget, each is `store.ErrInvalid`: `Filters: "not json"`, `Filters: `[{"column":"N","op":"in","value":"1"}]`` (single value to in), `Filters: `[{"column":"N","op":"=","value":["1"]}]``, `Sort: "N:up"`, `Sort: "N"`, `Sort: "Zed:asc"` (message lists `Key, N`), `Offset: -1`, `Limit: db.MaxRows()+1`, `Distinct: "N", Sort: "N:asc"`.
5. `TestRemoteTableCacheKeyPerView` — with the cache on (`Options{CacheAge: time.Minute, RefreshAge: time.Second}`, see how `cache_test.go` builds a Service with a clock), two different views cache separately (different rows), the same view twice is one load (count loads by pointing the widget at a query whose result changes after an `rawExec` insert into a scratch table, as existing cache tests do — follow their pattern).
6. `TestRemoteTableSortSplitsOnTheLastColon` — a column aliased `"a:b"`: `Sort: "a:b:desc"` sorts by `a:b` descending.
7. `TestRemoteTableDistinct` — `Distinct: "Key"` returns columns `["value","rows"]`, `Page.Distinct == "Key"`.

- [ ] **Step 2: Run them to verify they fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/reporting/ -run 'TestRemote|TestPaging|TestView'`
Expected: FAIL — unknown fields `Filters`, `Page`.

- [ ] **Step 3: Implement**

`view.go`:
- `remoteTable`: unmarshal `w.Props` into `struct{ Mode string \`json:"mode"\` }`; ignore an unmarshal error (props were validated on save).
- `parseView`: `present` if any field non-zero. Parse `Filters` with `json.Unmarshal` into `[]struct{Column, Op string; Value json.RawMessage}`; `Value` must be a JSON string for `=`, `!=`, `<`, `>` and a non-empty JSON array of strings for `in`/`not in` (messages: `filter on %q: %s takes one value, not a list`, `… takes a list of values`, `… needs at least one value`). Unknown op: `filter on %q: op %q is not one of =, !=, <, >, in, not in`. `Sort`: `i := strings.LastIndex(s, ":")`; `i <= 0` or dir not `asc`/`desc` → `sort %q: use <column>:asc or <column>:desc`. `Offset < 0` → refused; `Limit < 0 || Limit > maxRows` → `limit must be between 1 and %d (CONSOLE_QUERY_MAX_ROWS)`; `Limit == 0` → `maxRows`. Column existence is left to `QueryPage` (it knows the columns); its `ErrRefused` becomes `store.ErrInvalid` through `refuseSQLErr`.
- `cacheSuffix`: `fmt.Sprintf(":view=%x", sha256(json.Marshal(struct{F []FilterSpec; S, D string; O, L int}{…})))` over the parsed (not raw) values, so whitespace in the JSON does not split the cache.

`data.go`, in `WidgetData` after the component is known:
- `remote := remoteTable(w)`; `v, err := parseView(in, s.db.MaxRows())` (find the `*readsql.DB` the Service holds; `New(st, db, Options{})` — use that field).
- `if v.present && !remote` → `store.Refuse(store.ErrInvalid, "widget %d is not a remote table; filters, sort, distinct, offset and limit need a table with props.mode \"remote\"", w.ID)`.
- For a remote table, `load` calls `s.db.QueryPage(loadCtx, w.Source, v.page, bindArgs(params, …)...)` (Check the source first for its params exactly as `sqlSource.Load` does; put a `LoadPage` method on `*sqlSource` next to `Load` rather than reaching into readsql from `data.go`), returns the `PageResult`; after the cache, set `out.Data = res.Result`, `out.Page = &v.echo` with `Matched`/`Total` filled. Cache the whole `PageResult` (so the counts come from the cache too).
- `cacheKey(...)` gains a trailing `suffix string` argument; pass `v.cacheSuffix()` (empty for every non-remote widget, so existing keys are unchanged).
- `checkRows`: skip for a `Distinct` answer (its columns are not the widget's); a table accepts any rows anyway, so keep the call for the rows answer.

`dev.go`: build the same `DataRequest` fields from the query string (`q.Get("filters")`, `sort`, `distinct`, `offset`, `limit` with `strconv.Atoi`, refusing a non-integer as `store.ErrInvalid`) and route a remote table through the same `LoadPage`. If the dev handler cannot reach `parseView` without duplicating `WidgetData`, extract the shared part into one function both call.

`api/ops_reporting.go`: the five fields on `widgetDataIn`, passed through in `widgetData`; append to the `widget_data` description: `" A table with props.mode \"remote\" also takes filters, sort, distinct, offset and limit, applied in SQL over its whole result; the answer's page block echoes them with matched and total counts (docs://reporting, Filtering and paging a table)."`

- [ ] **Step 4: Docs, same commit**

`docs/reporting.md`:
- Envelope bullets: add `- \`page\` appears only for a remote table: \`{offset, limit, matched, total, sort, distinct, filters}\`, what was applied. \`data.truncated\` is then \`matched > offset + limit\`: there are more rows.`
- Tool table row `widget_data`: arguments become `` `widget_id`, `project_id`, `from`, `to`, `fresh`; for a remote table `filters`, `sort`, `distinct`, `offset`, `limit` ``.
- HTTP API row: `query: \`project_id\`, \`from\`, \`to\`, \`fresh\`, and for a remote table \`filters\`, \`sort\`, \`distinct\`, \`offset\`, \`limit\``.
- New subsection `### Filtering and paging a table` right after the envelope bullets: one paragraph on the two modes (detail lands in Task 6's component docs), then the rules verbatim in meaning from the spec's Semantics (`tw_cell` text equality; `<`/`>` numeric when the value is a decimal number, else text; empty cells; sort rule; refusals), and one request example:

```
widget_data {"widget_id": 42, "project_id": 7, "from": "2026-09-01", "to": "2026-09-30",
             "filters": "[{\"column\":\"Attribute\",\"op\":\"in\",\"value\":[\"$os\",\"plan\"]}]",
             "sort": "Count:desc", "limit": 100}
```

Keep `TestDocumentMatchesRoutes` green: it matches the route row by its first three cells only, so the last cell's text is free.

- [ ] **Step 5: Run the tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/reporting/ ./internal/api/ ./internal/shared/readsql/`
Expected: PASS (the `ui_test.go` UI tests need `make ui` once in this worktree: run `make ui` first if they fail with "no built asset").

- [ ] **Step 6: Commit**

```bash
git add internal/reporting/view.go internal/reporting/view_test.go internal/reporting/data.go internal/reporting/source.go internal/reporting/dev.go internal/api/ops_reporting.go docs/reporting.md
git commit -m "feat(reporting): filter, sort and page remote tables in widget_data

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: The attribute table becomes remote

**Files:**
- Modify: `internal/reporting/system/product/attribute-values.sql`
- Modify: `internal/reporting/system/product/attribute-values.json`
- Modify: `internal/reporting/system_test.go` (`TestAttributeValuesKeepEveryDay`, added by #110; if this branch does not have it yet, add it as below)

**Interfaces:**
- Consumes: remote `WidgetData` with `Offset`/`Limit` and `Page.Matched` (Task 3).

- [ ] **Step 1: Rewrite the test first**

`TestAttributeValuesKeepEveryDay` (keep its doc comment's intent; reword it for paging): seed the burst day as #110's version does (120 values of `ref` with count 1000 on `today-10`), then for every preset page through the widget with `Limit: 50`, collect the `Day` column over all pages, and assert: every distinct `day` in `v_product_attrs` for the range appears; the number of rows collected equals `Page.Matched`; `Page.Matched` equals `SELECT COUNT(*) FROM (SELECT 1 FROM v_product_attrs WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY attr_key, day, attr_value)`. Add `TestAttributeValuesFilterByAliases`: `Filters: [{"column":"Attribute","op":"in","value":["$app_version","plan"]}]` and `Sort: "Users (at least):desc"` load without error and every row's `Attribute` is one of the two (Review Focus 1).

If `newSystemFixture` has no `st` field yet (#110 adds it), add `st store.Store` to `systemFixture` and set it in `newSystemFixture`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/reporting/ -run 'TestAttributeValues'`
Expected: FAIL — the widget is not remote (`not a remote table`).

- [ ] **Step 3: Change the widget**

`attribute-values.json`: add `"mode": "remote"` to `props`.

`attribute-values.sql` (keep the first comment paragraph about summing and the `(at least)` floors; replace the ranking paragraph):

```sql
-- Summed across events: a value's count is how often it appeared on any
-- event that day. unique_users and unique_groups are per event and cannot
-- be summed (one person or one group firing two events would count twice),
-- so the largest single-event figure is shown, a floor on the true number.
-- unique_groups is NULL for days rolled up before the collector measured
-- it; MAX() skips those, so such a day shows an empty cell, not 0.
--
-- Every (attribute, day, value) in the range: the table is remote, so the
-- viewer's filters, sort and page run over all of them in SQL.
SELECT attr_key AS "Attribute", day AS "Day", attr_value AS "Value", SUM(count) AS "Count",
       MAX(unique_users) AS "Users (at least)", MAX(unique_groups) AS "Groups (at least)"
FROM v_product_attrs
WHERE project_id = :project
  AND day BETWEEN :from AND :to
GROUP BY attr_key, day, attr_value
ORDER BY attr_key, day DESC, "Count" DESC, attr_value
```

`attribute-breakdowns.md` (the markdown card above the table): add one sentence: `Filter the table by attribute, day or value; it pages through every row in the range.`

- [ ] **Step 4: Run the reporting suite**

Run: `go test ./internal/reporting/`
Expected: PASS, including `TestSystemDashboards` (it loads every system widget for every preset; a remote table with no paging arguments returns its first `MaxRows()` rows).

- [ ] **Step 5: Commit**

```bash
git add internal/reporting/system/product/ internal/reporting/system_test.go
git commit -m "feat(reporting): page through every attribute value on the product dashboard

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: The TS executor

**Files:**
- Create: `web/src/lib/table-view.ts`
- Create: `web/src/lib/table-view.test.ts`

**Interfaces:**
- Consumes: `internal/reporting/testdata/table-filters.json` (Task 1/2, read from disk in the test with `node:fs`; do not `import` it, it is outside the Vite root).
- Produces (`web/src/lib/table-view.ts`):

```ts
export type FilterOp = '=' | '!=' | '<' | '>' | 'in' | 'not in'
export interface Filter { column: string; op: FilterOp; value: string | string[] }
export interface Sort { column: string; dir: 'asc' | 'desc' }
export interface TableView { filters: Filter[]; sort: Sort | null; offset: number }
export const emptyView: TableView
export const OPS: FilterOp[] // in menu order: =, !=, <, >, in, not in
export function isDecimal(cell: string): boolean       // same regex as readsql.IsDecimal
export function cellNumber(cell: string): number | null // finite decimal → number
export function compareText(a: string, b: string): number // by code point, not UTF-16 unit
export function matches(row: string[], columns: string[], f: Filter): boolean
export function compareRows(a: string[], b: string[], columns: string[], sort: Sort | null): number
export function applyView(rows: string[][], columns: string[], view: TableView, limit: number):
  { rows: string[][]; matched: number; total: number }
export function distinctValues(rows: string[][], columns: string[], column: string, filters: Filter[]):
  { value: string; rows: number }[]
export function liveFilters(view: TableView, columns: string[]): Filter[] // drops filters on absent columns
export function parseView(stored: unknown): TableView | null // for useStoredState; tolerant
```

Rules are the spec's Semantics, mirrored from `readsql/page.go`: equality on the cell text with empty never equal; `<`/`>` numeric when `isDecimal(value)` (cell must be a number), else text with empty never matching; sort: empty last, numbers before text, numbers by value, text by `compareText`, ties by every column ascending by the same rule; with `sort: null`, query order. `applyView` filters (only `liveFilters`), sorts, slices `[offset, offset+limit)`. `distinctValues` drops filters on its own column, counts by cell text, orders by count desc then the sort rule ascending on the value.

- [ ] **Step 1: Write the failing tests**

```ts
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { applyView, cellNumber, compareText, distinctValues, emptyView, isDecimal, liveFilters, parseView, type Filter, type Sort } from './table-view'

const file = fileURLToPath(new URL('../../../internal/reporting/testdata/table-filters.json', import.meta.url))
const cases = JSON.parse(readFileSync(file, 'utf8')) as {
  columns: string[]
  cells: string[][]
  cases: { name: string; filters: Filter[]; sort?: Sort; expect: number[] }[]
  distinct: { name: string; column: string; filters: Filter[]; expect: [string, number][] }[]
}

describe('the shared cases (internal/reporting/testdata/table-filters.json)', () => {
  for (const c of cases.cases) {
    it(c.name, () => {
      const got = applyView(cases.cells, cases.columns, { filters: c.filters, sort: c.sort ?? null, offset: 0 }, 1000)
      expect(got.rows).toEqual(c.expect.map((i) => cases.cells[i]))
      expect(got.matched).toBe(c.expect.length)
      expect(got.total).toBe(cases.cells.length)
    })
  }
  for (const c of cases.distinct) {
    it(`distinct: ${c.name}`, () => {
      expect(distinctValues(cases.cells, cases.columns, c.column, c.filters).map((d) => [d.value, d.rows])).toEqual(c.expect)
    })
  }
})

describe('numbers', () => {
  it('reads decimals only', () => {
    for (const s of ['0', '-12', '1.5', '1e21', '1e+21', '1e-06']) expect(isDecimal(s)).toBe(true)
    for (const s of ['', ' 1', '+1', '1.', '.5', '0x10', 'NaN', 'Infinity', '12abc', '2026-09-25']) expect(isDecimal(s)).toBe(false)
    expect(cellNumber('1e999')).toBeNull()
    expect(cellNumber('-0.25E3')).toBe(-250)
  })
})

describe('text', () => {
  it('compares by code point, not UTF-16 unit', () => {
    // U+FF5E (BMP, high) sorts before U+1F600 (astral) by code point; by UTF-16 unit it would not.
    expect(compareText('\uFF5E', '\u{1F600}')).toBeLessThan(0)
  })
})

describe('paging and stored views', () => {
  it('slices after filtering and sorting', () => {
    const got = applyView(cases.cells, cases.columns, { filters: [], sort: { column: 'Count', dir: 'desc' }, offset: 2 }, 3)
    expect(got.rows).toHaveLength(3)
    expect(got.matched).toBe(cases.cells.length)
  })
  it('ignores filters on columns the result does not have', () => {
    const view = { ...emptyView, filters: [{ column: 'Gone', op: '=' as const, value: 'x' }] }
    expect(liveFilters(view, cases.columns)).toEqual([])
    expect(applyView(cases.cells, cases.columns, view, 1000).matched).toBe(cases.cells.length)
  })
  it('parses stored views tolerantly', () => {
    expect(parseView(null)).toBeNull()
    expect(parseView({ filters: 'x' })).toEqual(emptyView)
    expect(parseView({ filters: [{ column: 'a', op: 'in', value: ['1'] }, { column: 'b', op: '~', value: '1' }], sort: { column: 'a', dir: 'asc' } }))
      .toEqual({ filters: [{ column: 'a', op: 'in', value: ['1'] }], sort: { column: 'a', dir: 'asc' }, offset: 0 })
  })
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd web && npx vitest run src/lib/table-view.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement `table-view.ts`**

Write the functions above. `compareText`: iterate both strings with `for…of` (code points) comparing `codePointAt(0)`; shorter prefix first. `parseView` never stores `offset` (always returns `offset: 0`), drops filters with an unknown op or wrong value shape, and returns `emptyView` fields for anything malformed.

- [ ] **Step 4: Run to verify it passes**

Run: `cd web && npx vitest run src/lib/table-view.test.ts && npm run typecheck`
Expected: PASS. A shared case that fails here but passes in Go means the TS rule is wrong: fix TS.

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/table-view.ts web/src/lib/table-view.test.ts
git commit -m "feat(web): filter, sort and page table rows by the server's rules

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: The filter bar and local mode in the table component

**Files:**
- Create: `web/src/components/widgets/table-filters.tsx`
- Modify: `web/src/components/widgets/table.tsx`
- Modify: `web/src/components/widgets/types.ts`
- Modify: `web/src/components/widgets/table.test.tsx`
- Create: `web/src/components/widgets/table-filters.test.tsx`
- Modify: `internal/reporting/ui/components.json` (regenerated)
- Modify: `docs/reporting.md` (component table row for `table`, `### \`table\`` example section)

**Interfaces:**
- Consumes: everything in `web/src/lib/table-view.ts` (Task 5).
- Produces:

```ts
// types.ts — WidgetProps gains (all optional; only table reads them):
view?: TableView                 // controlled view; absent → the table keeps its own (gallery)
onView?: (next: TableView) => void
/** Remote mode: the card's loader for distinct values; absent → computed from the loaded rows. */
fetchDistinct?: (column: string, filters: Filter[]) => Promise<{ value: string; rows: number; capped: boolean }[]>
/** Remote mode: what the server answered for this page. */
page?: { offset: number; limit: number; matched: number; total: number }
/** Remote mode: the server refused the last view (message shown under the bar). */
viewError?: string
/** A later load is in flight: show current rows dimmed. */
reloading?: boolean

// table-filters.tsx
export function FilterBar(props: {
  columns: string[]; numeric: Set<string>; view: TableView; onView: (v: TableView) => void
  options: (column: string, filters: Filter[]) => Promise<{ value: string; rows: number; capped: boolean }[]>
  error?: string
}): JSX.Element
export function PageFooter(props: { offset: number; limit: number; matched: number; onOffset: (o: number) => void; note?: string }): JSX.Element | null
```

Contract change in `table.tsx`: `props.properties.mode = { enum: ['local', 'remote'] }`; description appends `" Viewers filter it by column; with mode \"remote\" filters, sort and paging run on the server over the whole result."`. `TableProps` gains `mode?: 'local' | 'remote'`.

Behaviour (spec, Page section):
- Bar above the header row on every table; chips `Attribute in $os, plan ×` (values joined by `, `, cut to the first three with `+N`), `Count > 100 ×`; click edits; **+ Filter**; **Clear all** with two or more chips. A chip on a column not in `columns` renders greyed with the tooltip `not in this table` (Review Focus 4).
- `FilterEditor` (popover, `@/components/ui/popover`): column select (`@/components/ui/select`), operator select (`OPS`, labels `=`, `≠`, `<`, `>`, `in`, `not in`), value: `in`/`not in` → multi-select combobox (`@/components/ui/combobox`) over `options(column, otherFilters)`, each option showing its row count; `=`/`!=` → single-choice combobox that also accepts typed text; `<`/`>` → `Input`. Apply/Cancel buttons. When `options` returns `capped: true`, the list ends with the note `Showing the most frequent values` (Review Focus 5).
- `error` renders under the bar in `text-destructive text-xs`.
- Sort stays the header click with today's cycle; in local mode it sorts via `compareRows` (replacing `sortRows`/`Intl.Collator`); `isNumericCell` is replaced by `isDecimal`-or-empty for alignment.
- Local mode: `applyView(sql.rows, sql.columns, view, 1000)`; footer when `matched > 1000`; when `sql.truncated`, footer note `Filters apply to the loaded rows; this table needs mode "remote"`.
- Remote mode: rows are `sql.rows` as given (the server applied the view); footer from `page`; `options` = `fetchDistinct`.
- No rows after filtering (local) or `page.matched === 0` with filters (remote): bar plus `No rows match these filters`, never `null`.
- View state when `view`/`onView` are absent (gallery): internal `useState(emptyView)`. When present, the table is controlled.
- Stored state: the table no longer reads `stateKey` itself for sort — the card owns the view (Task 7). For the gallery (no `stateKey`) nothing is stored. Keep reading the old `${stateKey}.sort` key once for migration in Task 7, not here.
- `reloading` → the table body gets `opacity-60 transition-opacity`.

- [ ] **Step 1: Write the failing tests**

`table-filters.test.tsx` (React Testing Library, as the other widget tests use — read `table.test.tsx` for render helpers):
1. Adding `Attribute in [$os, plan]` through the editor calls `onView` with that filter and `offset: 0`.
2. A chip for a column not in `columns` is greyed and has the `not in this table` tooltip text.
3. Clicking a chip's × removes only that filter; **Clear all** appears with two chips and empties filters.
4. With `options` resolving `capped: true`, the combobox shows `Showing the most frequent values`.
5. `error="no column \"x\""` renders under the bar.
6. `PageFooter` renders `1–1,000 of 5,335` and next calls `onOffset(1000)`; returns null when `matched <= limit`.

`table.test.tsx` additions:
7. Local mode filters the loaded rows (`Count > 100` hides smaller rows) without calling `fetchDistinct`.
8. Local mode with `truncated: true` shows the `needs mode "remote"` note.
9. A filter matching nothing shows `No rows match these filters` and the bar.
10. Remote mode renders `data.rows` unfiltered (the server's job) and the footer from `page`.
11. Text sort is by code point: `v1.10` before `v1.9` ascending.
Update any existing test that asserted the `Intl.Collator` natural order.

- [ ] **Step 2: Run to verify they fail**

Run: `cd web && npx vitest run src/components/widgets/table`
Expected: FAIL.

- [ ] **Step 3: Implement** `table-filters.tsx` and the `table.tsx` changes above. Keep `table.tsx` the renderer and move every filter UI piece into `table-filters.tsx`.

- [ ] **Step 4: Gallery example and docs**

In `table.tsx` `examples`, add a second example titled `Filterable rows` with eight rows and a column worth filtering (reuse the attribute shape: `Attribute`, `Day`, `Value`, `Count`), props `{ formats: { Count: 'number' } }` (local; the gallery has no server).

`docs/reporting.md`:
- Component table row for `table`: inputs cell unchanged; behaviour cell `any columns, shown in query order until a viewer sorts by a header or filters by a column`; props cell append `` `mode` (`local`, the default, filters the loaded rows; `remote` filters, sorts and pages the whole result on the server) ``; size `6 × 10` unchanged (keeps `TestReportingDocumentMatchesComponents` green).
- `### \`table\`` section: replace "It sorts only the rows returned, so a `LIMIT`ed query still decides which rows those are" with two sentences: a local table filters and sorts only the rows returned (at most `CONSOLE_QUERY_MAX_ROWS`), so a `LIMIT`ed query decides which rows those are; a remote table (`"mode": "remote"`) runs the viewer's filters, sort and page in SQL over every row the query returns, so leave the `LIMIT` off. Keep the existing worked example (it is the one `TestReportingExamplesWork` adds and loads) unchanged.

- [ ] **Step 5: Regenerate the manifest, run everything web**

Run: `cd web && npm run build && npm run typecheck && npx vitest run && cd .. && git diff --stat internal/reporting/ui/components.json`
Expected: build passes; `components.json` shows the `mode` enum and new description; all web tests pass.
Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/api/ ./internal/reporting/`
Expected: PASS (docs sync, manifest tests, examples).

- [ ] **Step 6: Commit**

```bash
git add web/src/components/widgets/ internal/reporting/ui/components.json docs/reporting.md
git commit -m "feat(web): filter tables by column, with a local and a remote mode

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: The card drives remote tables; e2e

**Files:**
- Modify: `web/src/lib/api.ts` (`WidgetData.page`, `WidgetDataQuery` fields)
- Modify: `web/src/lib/widget-query.ts` (`widgetQuery`, `refreshWidget` take the view)
- Modify: `web/src/components/WidgetCard.tsx`
- Modify: `web/src/components/WidgetCard.test.tsx` (or the card's existing test file; find it with `ls web/src/components/*.test.tsx`)
- Modify: `web/src/lib/widget-query.test.ts` if present
- Modify: `web/e2e/app.spec.ts`
- Modify: any auto-refresh caller of `refreshWidget` (grep `refreshWidget(`)

**Interfaces:**
- Consumes: `TableView`, `Filter`, `emptyView`, `liveFilters`, `parseView` (Task 5); `WidgetProps.view/onView/fetchDistinct/page/viewError/reloading` (Task 6); the server's `page` envelope and arguments (Task 3).
- Produces:

```ts
// api.ts
export interface PageInfo { offset: number; limit: number; matched: number; total: number; sort?: string; distinct?: string; filters: Filter[] }
// WidgetData gains: page?: PageInfo
// WidgetDataQuery gains: filters?: string; sort?: string; distinct?: string; offset?: number; limit?: number

// widget-query.ts
export function isRemoteTable(widget: Widget): boolean // component 'table' && props.mode === 'remote'
export function viewQuery(widget: Widget, view: TableView, columns: string[] | undefined): Partial<WidgetDataQuery>
  // {} for a non-remote widget; else filters (JSON of liveFilters, omitted when empty),
  // sort ("col:dir", omitted when null), offset (omitted when 0)
export function widgetQuery(widget: Widget, params: WidgetDataQuery, idle?: boolean, view?: Partial<WidgetDataQuery>)
  // queryKey gains the view's filters/sort/offset; placeholderData: keepPreviousData for remote tables
export function refreshWidget(client: QueryClient, widget: Widget, params: WidgetDataQuery, view?: Partial<WidgetDataQuery>): Promise<WidgetData>
```

Behaviour:
- `WidgetCard` holds the view: `useStoredState(\`${stateKey}.view\`, parseView)` for filters and sort (stored), `useState(0)` for the offset (never stored). On first mount, if `.view` is absent and the old `${stateKey}.sort` exists, seed the view's sort from it once (so existing viewers keep their sort) — then remove the old key.
- The offset resets to 0 when filters or sort change (in `onView`) and when `params.project_id`, `params.from` or `params.to` change (an effect keyed on them) — Review Focus 3.
- Remote table: `viewQuery` goes into the query and the key; `placeholderData: keepPreviousData` so the old page stays while the new one loads; pass `reloading={query.isPlaceholderData || (query.isFetching && !!answer)}`.
- Remote refusal: when the query errors with an `ApiError` status 400 and the card has an earlier answer for this widget (keep the last successful `WidgetData` in a `useRef`), render the component with that answer and `viewError={error.message}` instead of `FailedState`. A 400 with no earlier answer still shows `FailedState` (the query itself no longer runs).
- Empty state: for a remote table whose view has live filters, render the component even with zero rows (it shows "No rows match these filters"); `isEmpty` applies only when there are no live filters.
- `fetchDistinct` for a remote table: `endpoints.widgetData(id, {...params, filters: JSON of the other filters, distinct: column, limit: undefined})` → `data.rows.map(([value, rows]) => ({ value, rows: Number(rows), capped: answer.page!.matched > answer.page!.offset + data.rows.length }))`.
- Refresh (button and auto-refresh) passes the same `view` to `refreshWidget` — Review Focus 2.
- Local tables: the card passes `view`/`onView` too (so local filters and sort are stored under the same key), but nothing is added to the request.

- [ ] **Step 1: Write the failing tests**

Card tests (mock `endpoints.widgetData` as the existing card tests do):
1. A remote table's first request has no `filters`/`sort`/`offset`; after `onView` with a filter, the request has `filters` as JSON and `offset` absent.
2. After moving to offset 1000, changing `params.from` sends a request without `offset` (page 1) and with the same `filters`.
3. Refresh sends `fresh=true` with the current `filters`, `sort` and `offset`.
4. A 400 after a successful load keeps the rows and shows the message under the bar.
5. A remote table with a filter and `page.matched: 0` renders "No rows match these filters", not "No data for this range".
6. A stored filter on a column the answer does not have is not sent.
7. An old `${stateKey}.sort` value seeds the sort once.
8. A local table's requests never carry view arguments.

- [ ] **Step 2: Run to verify they fail**

Run: `cd web && npx vitest run src/components src/lib`
Expected: FAIL.

- [ ] **Step 3: Implement** the `api.ts`, `widget-query.ts` and `WidgetCard.tsx` changes above.

- [ ] **Step 4: e2e**

`web/e2e/app.spec.ts`: a test `the attribute table filters and pages on the server`. Read `web/e2e/serve.sh` and the existing Product-dashboard tests first to learn which project the e2e seed fills with product attributes and how tests pick a range. Steps: open the Product dashboard, range 90d; in "Top attribute values by day" add a filter `Attribute in` two attribute values offered by the picker; assert every visible `Attribute` cell is one of the two; if the footer shows more than one page, go to page 2 and assert the range text starts past 1; reload; assert the two chips are still there and the footer (if any) reads from `1–`.

Run: `make build && cd web && npm run e2e`
Expected: PASS, including the existing suite.

- [ ] **Step 5: Full check**

Run: `export PATH=$PATH:/usr/local/go/bin && make check`
Expected: PASS (vet, coverage gate, race subset).

- [ ] **Step 6: Commit**

```bash
git add web/src web/e2e
git commit -m "feat(web): keep remote tables' filters, sort and page in the card

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
