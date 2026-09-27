# Reporting Dashboards Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve a read-only dashboard UI at `/app/` on the API listener. Agents build its dashboards and widgets over MCP and REST. The Evidence pages are ported to five system dashboards. Evidence itself is left untouched: its removal is PR2.

**Architecture:**
- **Two leaf packages.** `internal/shared/sortkey` generates fractional keys. `internal/shared/readsql` holds the read-only SQL guard; today's guards in `internal/api` move into it.
- **One rank-1 package, `internal/reporting`,** holds:
  - models, validation and operations;
  - the source-type registry (`sql`, `md`);
  - the cache;
  - the system-definition migrator;
  - the embedded UI;
  - `reporting dev`.
- **The store owns every row.** Row types live in `store`, and `store/sqlite` implements them behind `reporting.Store`.
- **`api` exposes the operations** through `expose()` and mounts `/app/`.
- **The UI is a React app in `web/`.** It builds into `internal/reporting/ui/`, which is committed and drift-checked like the SDK bundle.

**Tech Stack:**
- Go 1.25 with modernc SQLite. `github.com/google/jsonschema-go` validates props and `golang.org/x/sync/singleflight` merges identical loads. Both are already in `go.sum` through the MCP SDK and become direct.
- React 19, Vite, TypeScript, Tailwind v4, shadcn (every component), Recharts, TanStack Query, react-router, react-markdown, d3-geo, topojson-client, world-atlas.
- Vitest with Testing Library, and Playwright.

**Spec:** `docs/superpowers/specs/2026-09-25-reporting-dashboards-design.md` (committed on this branch). Read it before any task: the decision numbers below (D1–D51) refer to it.

## Global Constraints

- Go is at `/usr/local/go/bin`. Run `export PATH=$PATH:/usr/local/go/bin` before any `go` or `make`.
- **Implementers do not commit.** Each task ends with a report; the controller reviews it and commits with the message given in the task, staging only the task's paths.
- `make check` passes at the end of every Go task. `npm run typecheck && npm test && npm run build` in `web/` passes at the end of every web task.
- **Dependencies.**
  - Go gains no dependency that is not already in `go.sum`. The two promoted from indirect to direct are `github.com/google/jsonschema-go` and `golang.org/x/sync`. Everything else is our own code (D4, D10).
  - Frontend libraries are listed in the tech stack; add others only if a task says so.
- **Package ranks** (`internal/archtest/archtest_test.go`): `internal/shared/sortkey` 0, `internal/shared/readsql` 0, `internal/reporting` 1.
- **Refusals** are `store.ErrInvalid`, `store.ErrNotFound` and `store.ErrConflict`. Each is built with `store.Refuse(kind, format, args…)`, whose message carries no sentinel prefix. `manage.ErrInvalid` is `store.ErrInvalid`.
- **Migration file:** `internal/store/sqlite/migrations/021_reporting.sql`. Its test pins the ceiling with `newTestDBAt(t, 21)` (or `migrateThrough(ctx, 21)`) and calls `db.Migrate(ctx)` before using current Go store code.
- Timestamps in the new tables are TEXT `strftime('%Y-%m-%dT%H:%M:%SZ','now')` (RFC 3339, UTC).
- **Owners** are exactly `system` and `user` (`store.OwnerSystem`, `store.OwnerUser`). **Source types** are exactly `sql` and `md` in this PR.
- **Range presets:** `today`, `yesterday`, `7d`, `30d`, `90d`, `custom`. API URLs take only `from`/`to` (`YYYY-MM-DD`), a span of at most 365 days, with a `to` after today (UTC) clamped to today.
- **SQL parameters** are exactly `:project`, `:from` and `:to`.
- **Grid:** `width` 1–12 columns; `height` 1–12 rows of 40px.
- **Settings:**

  | Setting | Default | Meaning |
  | --- | --- | --- |
  | `REPORTING_CACHE_SECONDS` | 900 | how long an ordinary request reuses a result |
  | `REPORTING_REFRESH_SECONDS` | 60 | how long a `fresh=true` request reuses one; a value above a non-zero cache age refuses the boot |
  | `RETENTION_ARCHIVED_DAYS` | 30 | days before archived items are purged; `0` keeps them |

  Widget queries use `API_QUERY_TIMEOUT` and `API_QUERY_MAX_ROWS`.
- **Releases URL** (verbatim, everywhere): `https://github.com/dmtrkzntsv/twillingate/releases`.
- **Tool names** as in D28/D29.
- **Output ids** are named `dashboard_id` and `widget_id`, like `project_id`.
- **REST actors:** REST edges record actor `api` and MCP edges `mcp` (the code's existing values; the spec's "rest" means `api`).
- **Docs** change in the same task as the behaviour (CLAUDE.md table), and `internal/api/docs_sync_test.go` must pass.
- Conventional Commits. No model identifiers in any repo file or commit. Commit trailer: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` (controller adds it).
- **Keep it simple.**
  - No speculative options, no interfaces with one implementation beyond `SourceType` (D9) and the `Store` slices.
  - Comments explain *why*, at the density of the surrounding code.

## Deviations from the spec (small, deliberate)

1. **`after` placement uses `0` for "first"** rather than JSON `null`. `null` and "omitted" both decode to a nil `*int64`, and the MCP schema generator cannot express a null-or-number type. So `after` is a number: omitted means last, `0` means first, and an id means after that item. Same power, one type.
2. **Cache keys carry a hash of the widget's source** instead of being dropped on update. An updated widget reads new keys and its old entries age out. The same holds after the migrator changes a widget, and across processes. The result is the same as D33's "update_widget drops that widget's entries", with no invalidation code.
3. **`SourceType` gains `Follows(content) (project, rng bool)`**, so the service can tell what a widget follows without knowing SQL.
4. **The migrator validates system SQL with `LIMIT 0` only** (columns, no sample rows). Sample-row type checks would run every system query on each release's first boot. The load-bearing system-dashboards test (Task 14) runs the full check, rows included, in `make check`.
5. **Cold-load timing** (the spec's "open before planning") is measured on the demo seed in Task 14 by an opt-in timing test. The prod measurement is part of the parity check after release (Rollout step 2).

## Review Focus

Most likely to bite first:

1. **Custom SQL ending in a line comment or a semicolon** (`… -- by day`, `…;`) must still run. The wrapper puts `\n)` after the text and trims trailing `;`. Pinned in Task 2 (`TestQueryTrailingCommentAndSemicolon`).
2. **Viewing near midnight from a browser in another timezone.** "Today" is the instance's UTC day, not the browser's. At 23:30 in UTC-8 the preset `today` must resolve to the next UTC date. Pinned in Task 18 (`ranges.test.ts`, "resolves in the instance timezone").
3. **A fresh install with no projects.** A system dashboard must show a "create a project first" state and never call `widget_data` with no `project_id`. Pinned in Task 19 (`Dashboard.test.tsx`, "no projects").
4. **A stored `last_project_id` that was archived or purged.** The page falls back to the first active project instead of querying a missing one. Pinned in Task 18 (`selection.test.ts`, "stale stored project").
5. **Titles without ASCII letters** ("Посетители", "📈"). Deriving a name must not produce `""` or collide, and it falls back to `widget`, `widget-2`, …. Pinned in Task 7 (`TestDeriveName`).

---

## File Structure

```
internal/shared/sortkey/sortkey.go            fractional keys (Between, Spread)
internal/shared/readsql/readsql.go            read-only pool, Run (trusted SQL), Query (custom SQL)
internal/shared/readsql/check.go              tokenizer: refused names, ATTACH, parameters
internal/store/errors.go                      + ErrInvalid, Refusal, Refuse
internal/store/reporting.go                   row types: Component, Dashboard, Widget, ReportingSync, PurgeResult
internal/store/store.go                       Store gains the reporting and purge methods
internal/store/sqlite/sqlite.go               writer DSN gains foreign_keys(1)
internal/store/sqlite/migrate.go              migrations run with FKs off, then foreign_key_check
internal/store/sqlite/registry.go             DeleteProjectData deletes dependents first
internal/store/sqlite/migrations/021_reporting.sql
internal/store/sqlite/reporting.go            reporting reads/writes
internal/store/sqlite/reporting_sync.go       SyncReporting, ReportingHash
internal/store/sqlite/purge.go                PurgeArchived
internal/reporting/reporting.go               package doc, Service, Options, Store
internal/reporting/types.go                   edge types (JSON) and parsed Component
internal/reporting/components.go              manifest parsing, props schema, input checks
internal/reporting/source.go                  SourceType, sqlSource, mdSource
internal/reporting/validate.go                widget/dashboard validation, names
internal/reporting/ops.go                     write operations and placement
internal/reporting/read.go                    list/get operations
internal/reporting/data.go                    WidgetData, following, ranges
internal/reporting/cache.go                   two-age cache with singleflight
internal/reporting/files.go                   dashboard directory loader (system + dev)
internal/reporting/migrate.go                 Migrate: hash, validate, SyncReporting
internal/reporting/system/<dir>/...           five system dashboards
internal/reporting/ui.go                      embedded UI handler
internal/reporting/ui/                        web build output (committed)
internal/reporting/dev.go                     reporting dev handler
internal/api/ops_reporting.go                 tools/routes for reporting
internal/api/guide_reporting.go               reporting_guide
internal/app/migrate.go                       app.Migrate
internal/app/reporting_dev.go                 app.ReportingDev
cmd/twillingate/reporting.go                  `twillingate reporting dev`
docs/reporting.md                             contract page (docs://reporting)
web/                                          React app
```

---

## Go track

### Task 1: `internal/shared/sortkey`

**Files:**
- Create: `internal/shared/sortkey/sortkey.go`, `internal/shared/sortkey/sortkey_test.go`
- Modify: `internal/archtest/archtest_test.go` (rank table: `"internal/shared/sortkey": 0`)

**Interfaces:**
- Produces:
  - `func Between(a, b string) (string, error)`: `""` is an open end; both empty gives `"a0"`.
  - `func Spread(a, b string, n int) ([]string, error)`: n ascending keys strictly between a and b.

- [ ] **Step 1: Write the failing test** (`sortkey_test.go`)

```go
package sortkey

import (
	"strings"
	"testing"
)

func TestBetweenVectors(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", "a0"},
		{"", "a0", "Zz"},
		{"", "Zz", "Zy"},
		{"a0", "", "a1"},
		{"a1", "", "a2"},
		{"a0", "a1", "a0V"},
		{"a1", "a2", "a1V"},
		{"a0V", "a1", "a0l"},
		{"Zz", "a0", "ZzV"},
		{"Zz", "a1", "a0"},
		{"", "Y00", "Xzzz"},
		{"bzz", "", "c000"},
		{"a0", "a0V", "a0G"},
		{"a0", "a0G", "a08"},
		{"b125", "b129", "b127"},
		{"a0", "a1V", "a1"},
		{"Zz", "a01", "a0"},
		{"", "a0V", "a0"},
		{"", "b999", "b99"},
		{"", "A000000000000000000000000001", "A000000000000000000000000000V"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzy", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzz", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzzV"},
	}
	for _, c := range cases {
		got, err := Between(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Between(%q, %q) = %q, %v; want %q", c.a, c.b, got, err, c.want)
		}
	}
}

func TestBetweenRefuses(t *testing.T) {
	for _, c := range [][2]string{
		{"", "A00000000000000000000000000"}, // the smallest integer is not a key
		{"a00", ""}, {"a00", "a1"},           // trailing zero
		{"0", "1"},                           // invalid head
		{"a1", "a0"}, {"a1", "a1"},           // not before
		{"a!", ""},                           // not a base-62 digit
	} {
		if got, err := Between(c[0], c[1]); err == nil {
			t.Errorf("Between(%q, %q) = %q; want an error", c[0], c[1], got)
		}
	}
}

// Inserting at one spot many times keeps keys ordered and short.
func TestRepeatedInsertStaysOrderedAndShort(t *testing.T) {
	lo, hi := "a0", "a1"
	for i := 0; i < 500; i++ {
		k, err := Between(lo, hi)
		if err != nil || !(lo < k && k < hi) {
			t.Fatalf("step %d: Between(%q, %q) = %q, %v", i, lo, hi, k, err)
		}
		if i%2 == 0 {
			lo = k
		} else {
			hi = k
		}
	}
	if len(lo) > 100 {
		t.Errorf("key grew to %d chars", len(lo))
	}
	// Appending at the end is the common case and must stay short.
	k := ""
	for i := 0; i < 1000; i++ {
		next, err := Between(k, "")
		if err != nil || next <= k {
			t.Fatalf("append %d: %q after %q: %v", i, next, k, err)
		}
		k = next
	}
	if len(k) > 4 {
		t.Errorf("1000 appends gave %q", k)
	}
}

func TestSpread(t *testing.T) {
	for _, c := range []struct{ a, b string }{{"", ""}, {"a0", ""}, {"", "a0"}, {"a0", "a1"}} {
		keys, err := Spread(c.a, c.b, 7)
		if err != nil || len(keys) != 7 {
			t.Fatalf("Spread(%q, %q, 7) = %q, %v", c.a, c.b, keys, err)
		}
		prev := c.a
		for _, k := range keys {
			if k <= prev || (c.b != "" && k >= c.b) {
				t.Errorf("Spread(%q, %q): %q out of order in %s", c.a, c.b, k, strings.Join(keys, ","))
			}
			prev = k
		}
	}
	if keys, _ := Spread("", "", 0); len(keys) != 0 {
		t.Errorf("Spread n=0 = %q", keys)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/shared/sortkey/`
Expected: FAIL (undefined: Between, Spread)

- [ ] **Step 3: Implement** (`sortkey.go`, a port of the fractional-indexing algorithm; our own code)

```go
// Package sortkey makes fractional sort keys: strings that sort in byte
// order where a key can always be made between any two others without
// touching them, so inserting never renumbers. The scheme is David
// Greenspan's "Implementing Fractional Indexing" (keys look like a0, a0V,
// a1): an integer part whose first character encodes its length, then a
// base-62 fraction that never ends in '0'. Appending stays short because
// the integer part grows first; only inserts between neighbours lengthen
// the fraction.
package sortkey

import (
	"errors"
	"fmt"
	"strings"
)

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// smallestInteger is the lowest integer part. It is not a key itself,
// or nothing could ever sort before it.
var smallestInteger = "A" + strings.Repeat("0", 26)

// Between returns a key sorting strictly after a and before b. An empty
// a means "before everything", an empty b "after everything".
func Between(a, b string) (string, error) {
	for _, k := range []string{a, b} {
		if k != "" {
			if err := validate(k); err != nil {
				return "", err
			}
		}
	}
	if a != "" && b != "" && a >= b {
		return "", fmt.Errorf("sortkey: %q is not before %q", a, b)
	}
	if a == "" {
		if b == "" {
			return "a0", nil
		}
		ib := integerPart(b)
		if ib == smallestInteger {
			m, err := midpoint("", b[len(ib):])
			return ib + m, err
		}
		if ib < b {
			return ib, nil
		}
		if d, ok := decrement(ib); ok {
			return d, nil
		}
		return "", fmt.Errorf("sortkey: nothing sorts before %q", b)
	}
	ia := integerPart(a)
	fa := a[len(ia):]
	if b == "" {
		if i, ok := increment(ia); ok {
			return i, nil
		}
		m, err := midpoint(fa, "")
		return ia + m, err
	}
	ib := integerPart(b)
	if ia == ib {
		m, err := midpoint(fa, b[len(ib):])
		return ia + m, err
	}
	i, ok := increment(ia)
	if !ok {
		return "", fmt.Errorf("sortkey: nothing sorts after %q", a)
	}
	if i < b {
		return i, nil
	}
	m, err := midpoint(fa, "")
	return ia + m, err
}

// Spread returns n ascending keys strictly between a and b (either may be
// "" for an open end), splitting the gap evenly so later inserts between
// them stay short.
func Spread(a, b string, n int) ([]string, error) {
	switch {
	case n <= 0:
		return nil, nil
	case n == 1:
		k, err := Between(a, b)
		if err != nil {
			return nil, err
		}
		return []string{k}, nil
	case b == "":
		out := make([]string, 0, n)
		k := a
		for range n {
			var err error
			if k, err = Between(k, ""); err != nil {
				return nil, err
			}
			out = append(out, k)
		}
		return out, nil
	case a == "":
		out := make([]string, n)
		k := b
		for i := n - 1; i >= 0; i-- {
			var err error
			if k, err = Between("", k); err != nil {
				return nil, err
			}
			out[i] = k
		}
		return out, nil
	}
	mid := n / 2
	c, err := Between(a, b)
	if err != nil {
		return nil, err
	}
	left, err := Spread(a, c, mid)
	if err != nil {
		return nil, err
	}
	right, err := Spread(c, b, n-mid-1)
	if err != nil {
		return nil, err
	}
	return append(append(left, c), right...), nil
}

// midpoint returns a fraction strictly between a and b, where b == ""
// means no upper bound. A bounded b is never "" here: callers only pass a
// fraction that follows an equal integer part, which is non-empty.
func midpoint(a, b string) (string, error) {
	if b != "" && a >= b {
		return "", fmt.Errorf("sortkey: fraction %q is not before %q", a, b)
	}
	if strings.HasSuffix(a, "0") || strings.HasSuffix(b, "0") {
		return "", errors.New("sortkey: fraction ends in 0")
	}
	if b != "" {
		n := 0 // the common prefix, reading a as 0-padded
		for n < len(b) && digitAt(a, n) == b[n] {
			n++
		}
		if n > 0 {
			m, err := midpoint(a[min(n, len(a)):], b[n:])
			return b[:n] + m, err
		}
	}
	da, db := 0, len(digits)
	if a != "" {
		da = strings.IndexByte(digits, a[0])
	}
	if b != "" {
		db = strings.IndexByte(digits, b[0])
	}
	if db-da > 1 {
		return string(digits[(da+db+1)/2]), nil
	}
	if len(b) > 1 {
		return b[:1], nil
	}
	m, err := midpoint(a[min(1, len(a)):], "")
	return string(digits[da]) + m, err
}

func digitAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return '0'
}

// integerLength is how long an integer part whose first character is
// head is: a–z are 2–27 characters (non-negative), Z–A the same lengths
// mirrored (negative), so byte order is numeric order.
func integerLength(head byte) int {
	switch {
	case head >= 'a' && head <= 'z':
		return int(head-'a') + 2
	case head >= 'A' && head <= 'Z':
		return int('Z'-head) + 2
	}
	return 0
}

// integerPart assumes a validated key.
func integerPart(key string) string { return key[:integerLength(key[0])] }

func validate(key string) error {
	n := integerLength(key[0])
	if n == 0 || n > len(key) || key == smallestInteger {
		return fmt.Errorf("sortkey: invalid key %q", key)
	}
	for i := 1; i < len(key); i++ {
		if strings.IndexByte(digits, key[i]) < 0 {
			return fmt.Errorf("sortkey: invalid key %q", key)
		}
	}
	if strings.HasSuffix(key[n:], "0") {
		return fmt.Errorf("sortkey: invalid key %q (fraction ends in 0)", key)
	}
	return nil
}

func increment(x string) (string, bool) {
	head, digs := x[0], []byte(x[1:])
	for i := len(digs) - 1; i >= 0; i-- {
		d := strings.IndexByte(digits, digs[i]) + 1
		if d < len(digits) {
			digs[i] = digits[d]
			return string(head) + string(digs), true
		}
		digs[i] = '0'
	}
	switch head {
	case 'Z':
		return "a0", true
	case 'z':
		return "", false
	}
	h := head + 1
	if h > 'a' {
		digs = append(digs, '0')
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs), true
}

func decrement(x string) (string, bool) {
	head, digs := x[0], []byte(x[1:])
	for i := len(digs) - 1; i >= 0; i-- {
		d := strings.IndexByte(digits, digs[i]) - 1
		if d >= 0 {
			digs[i] = digits[d]
			return string(head) + string(digs), true
		}
		digs[i] = digits[len(digits)-1]
	}
	switch head {
	case 'a':
		return "Z" + digits[len(digits)-1:], true
	case 'A':
		return "", false
	}
	h := head - 1
	if h < 'Z' {
		digs = append(digs, digits[len(digits)-1])
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs), true
}
```

- [ ] **Step 4: Add to the rank table**, run `go test ./internal/shared/sortkey/ ./internal/archtest/` and then `make check`. Expected: PASS. If a vector fails, the port differs from the reference algorithm. Fix the port, never the vector.

- [ ] **Step 5: Commit** — `refactor: add fractional sort keys in internal/shared/sortkey`

---

### Task 2: `internal/shared/readsql` (one read-only guard for all custom SQL)

**Files:**
- Create: `internal/shared/readsql/readsql.go`, `check.go`, `readsql_test.go`, `check_test.go`
- Modify:
  - `internal/api/readdb.go`: delete it; its contents move.
  - `internal/api/ops_query.go`: use `readsql.Query`.
  - `internal/api/ops_read.go`: `host.db` becomes `*readsql.DB`; `table()` uses `Run`.
  - `internal/api/server.go`: `Build` opens `readsql.Open`.
  - `internal/api/seed_test.go` and every test calling `OpenReadDB`/`queryRows`.
  - `internal/archtest/archtest_test.go`: `"internal/shared/readsql": 0`.
  - `internal/store/sqlite/views_test.go` (or a new `refused_test.go` in `store/sqlite`): the v_* view test below.
  - `docs/twillingate.md`: in the `query` tool row, say that `meta` and SQLite's internal tables are refused.

**Interfaces:**
- Produces (package `readsql`):

```go
var (
	ErrRefused = errors.New("refused")          // Check refused the text
	ErrTimeout = errors.New("query timed out")   // the deadline passed
)
type Result struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"` // NULL reads as ""
	Truncated bool       `json:"truncated"`
}
type DB struct{ /* *sql.DB, timeout, maxRows */ }
func Open(path string, timeout time.Duration, maxRows int) (*DB, error)
func (d *DB) Close() error
func (d *DB) Timeout() time.Duration
func (d *DB) MaxRows() int
func (d *DB) Run(ctx context.Context, q string, args ...any) (Result, error)   // SQL written in Go: deadline and row cap only
func (d *DB) Query(ctx context.Context, q string, args ...any) (Result, error) // custom SQL: Check, subquery wrap, then Run
func Check(q string) (params []string, err error) // named parameters with sigil (":project"), in first-use order
```

- [ ] **Step 1: Write the failing tests.**

`check_test.go` is table-driven:
- **Refused**, errors.Is `ErrRefused`, and the message names the identifier:
  - `SELECT * FROM meta`, `select * from "meta"`, ``select * from `meta` ``, `select * from [meta]`, `select * from main.meta`, `SELECT * FROM META`;
  - `select * from sqlite_master`, `sqlite_schema`, `sqlite_sequence`, `sqlite_stat1`, `sqlite_dbpage`;
  - `select * from pragma_table_info('events')`, `select * from dbstat`;
  - `ATTACH 'x.db' AS y`, `select 1; attach 'x' as y`.
- **Passed:**
  - `select 'meta' as x`, `select 1 -- meta`, `select 1 /* sqlite_master */`;
  - `select metadata, attachment from v_views_paths`, `select "it''s" as ok`, `select x'00'`.
- **Parameters:** `select :project, :from, @x, $y, ?, ?2, :project` returns `[":project", ":from", "@x", "$y", "?", "?2"]`.

`readsql_test.go` builds a temp database with the writer store (`store.Open("sqlite://"+path)`, `Migrate`, insert one project), then opens `readsql.Open(path, 2*time.Second, 3)`. Tests:
- `TestQueryReturnsRowsAndTruncates`: `select value from json_each('[1,2,3,4]')` returns 3 rows and `Truncated: true`.
- `TestQueryTrailingCommentAndSemicolon`: `select 1 as x -- trailing` and `select 1 as x;` both return `[["1"]]`.
- `TestQueryNamedParams`: `select :project as p` with `sql.Named("project", 7)` returns `[["7"]]`.
- `TestQueryRefusesWrites`: a table of write attempts:
  - `INSERT INTO projects(name) VALUES('x')`, `UPDATE projects SET name='x'`, `DELETE FROM projects`, `REPLACE INTO projects(id,name) VALUES(1,'x')`;
  - `DROP TABLE projects`, `CREATE TABLE t(x)`, `CREATE TEMP TABLE t(x)`, `WITH x AS (SELECT 1) INSERT INTO projects(name) SELECT 'x'`;
  - `PRAGMA user_version = 5`, `ATTACH 'y.db' AS y`, `VACUUM INTO 'z.db'`, `SELECT 1; SELECT 2`, `SELECT load_extension('x')`.

  Each is an error, and the database file's SHA-256 is unchanged after the loop (checkpoint via the writer first; hash the `-wal` too if present).
- `TestRunTimeout`: a recursive CTE counting to 1e9 with a 50ms timeout returns `errors.Is(err, ErrTimeout)`.

In `internal/store/sqlite` add `TestViewsReferenceNoRefusedName`. For every `name LIKE 'v\_%' ESCAPE '\'` in `sqlite_master` of type view, `readsql.Check(sql)` must pass. The one allowed exception is `v_product_attrs`, whose definition may name `meta` only through `key='product_attributes_top_n'`: assert the text contains that and no other `FROM meta`. `store/sqlite` importing `readsql` in a test is fine; archtest checks production imports only.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/shared/readsql/` → FAIL (package missing).

- [ ] **Step 3: Implement `check.go`**

```go
package readsql

import (
	"fmt"
	"strings"
)

// Check tokenizes custom SQL and refuses what may not appear: the ATTACH
// keyword (mode=ro does not stop attaching another file), and names of
// tables custom SQL may not read — meta, which holds the visitor salt,
// and SQLite's internals. A tokenizer rather than a substring search, so
// 'meta' in a string, a comment, or a column called metadata passes.
// Without an authorizer (the driver exposes none) this is sound because
// custom SQL cannot create views, identifiers have no escapes, and the
// only indirect path, an existing view, is pinned by a test on every v_*.
//
// It returns the named parameters the text uses, sigil included, in
// order of first use, so a caller can refuse ones it does not bind.
func Check(q string) ([]string, error) {
	var params []string
	seen := map[string]bool{}
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '\'':
			i = skipQuoted(q, i, '\'')
		case c == '-' && strings.HasPrefix(q[i:], "--"):
			if j := strings.IndexByte(q[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				i = len(q)
			}
		case c == '/' && strings.HasPrefix(q[i:], "/*"):
			if j := strings.Index(q[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(q)
			}
		case c == '"' || c == '`':
			end := skipQuoted(q, i, c)
			inner := q[i+1 : max(i+1, end-1)]
			if err := checkName(strings.ReplaceAll(inner, string([]byte{c, c}), string(c))); err != nil {
				return nil, err
			}
			i = end
		case c == '[':
			end := len(q)
			if j := strings.IndexByte(q[i:], ']'); j >= 0 {
				end = i + j + 1
			}
			if err := checkName(q[i+1 : max(i+1, end-1)]); err != nil {
				return nil, err
			}
			i = end
		case c == ':' || c == '@' || c == '$' || c == '?':
			j := i + 1
			for j < len(q) && isIdent(q[j]) {
				j++
			}
			if p := q[i:j]; c == '?' || j > i+1 {
				if !seen[p] {
					seen[p] = true
					params = append(params, p)
				}
			}
			i = j
		case c >= '0' && c <= '9':
			j := i + 1 // numbers like 1e5 or 0x1F are not identifiers
			for j < len(q) && (isIdent(q[j]) || q[j] == '.') {
				j++
			}
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < len(q) && isIdent(q[j]) {
				j++
			}
			word := q[i:j]
			if strings.EqualFold(word, "attach") {
				return nil, fmt.Errorf("%w: ATTACH is not allowed", ErrRefused)
			}
			if err := checkName(word); err != nil {
				return nil, err
			}
			i = j
		default:
			i++
		}
	}
	return params, nil
}

// skipQuoted returns the index after the closing quote, treating a
// doubled quote as an escaped one; an unterminated quote runs to the end.
func skipQuoted(q string, i int, quote byte) int {
	for j := i + 1; j < len(q); j++ {
		if q[j] == quote {
			if j+1 < len(q) && q[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(q)
}

func checkName(name string) error {
	n := strings.ToLower(name)
	if n == "meta" || n == "dbstat" || strings.HasPrefix(n, "sqlite_") || strings.HasPrefix(n, "pragma_") {
		return fmt.Errorf("%w: sql reads %s, which custom SQL may not read", ErrRefused, name)
	}
	return nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isIdent(c byte) bool { return isIdentStart(c) || c == '$' || (c >= '0' && c <= '9') }
```

- [ ] **Step 4: Implement `readsql.go`.**
  - `Open`: DSN `file:<path>?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)&_defensive=1` with `SetMaxOpenConns(4)`. The doc comment lists the guard layers (D46 table).
  - `Run`: `queryRows` moved from `readdb.go`. Map a context deadline (`errors.Is(err, context.DeadlineExceeded)` or `ctx.Err() != nil`) to `fmt.Errorf("%w after %s", ErrTimeout, d.timeout)`.
  - `Query`:
    1. `Check` (ignore params);
    2. refuse empty text with `ErrRefused` ("sql must not be empty");
    3. `wrapped := fmt.Sprintf("SELECT * FROM (%s\n) LIMIT %d", strings.TrimRight(strings.TrimSpace(q), ";"), d.maxRows+1)`;
    4. `Run`.

    Package doc: "readsql is the one path custom SQL takes to the database (spec D46–47)".

- [ ] **Step 5: Move `api` onto it.**
  - `host.db` becomes `*readsql.DB`. `h.timeout`/`h.maxRows` go: use `h.db.Timeout()`/`MaxRows()`.
  - `table()` uses `h.db.Run`.
  - `runQuery` uses `h.db.Query` and keeps its messages:
    - `ErrTimeout` → `invalidf("query exceeded %s; narrow the date range or query agg_* tables directly", …)`;
    - `ErrRefused` → `invalidf("%s", err)` minus the `refused: ` prefix (build the message from the text after the sentinel: `strings.TrimPrefix(err.Error(), readsql.ErrRefused.Error()+": ")`);
    - any other → the existing "SQL error…" message.
  - Keep `tableOut` and its `Note`.
  - Update `seed_test.go` (`readsql.Open(path, 5*time.Second, 1000)`) and the existing query-guard tests: `TestQueryToolBlocksWrites` keeps passing.
  - Add a case: `query` with `select * from meta` answers the refusal text.

- [ ] **Step 6: Verify** — `go test ./internal/shared/... ./internal/api/ ./internal/store/sqlite/ -run 'Check|Query|Run|Refused|View' ` then `make check`. Expected: PASS.

- [ ] **Step 7: Commit** — `feat(api): refuse reads of meta and SQLite internals in custom SQL`. The body says the guards moved to `internal/shared/readsql`, which the query tool and widgets share.

---

### Task 3: One error vocabulary; enforce foreign keys

**Files:**
- Modify:
  - `internal/store/errors.go`: add `ErrInvalid`, `Refusal` and `Refuse`.
  - `internal/manage/errors.go`: `ErrInvalid = store.ErrInvalid`.
  - `internal/api/refusal.go`: use `store.Refuse`, keeping the `invalidf`/`notFoundf` names.
  - `internal/store/sqlite/sqlite.go`: add `_pragma=foreign_keys(1)` to the writer DSN.
  - `internal/store/sqlite/migrate.go`: FKs off while migrating, then `foreign_key_check`.
  - `internal/store/sqlite/registry.go`: `DeleteProjectData` deletes dependents first.
- Test: `internal/store/sqlite/fk_test.go` (new), `internal/manage` tests if they compare the old message text.

**Interfaces:**
- Produces:

```go
// store/errors.go
ErrInvalid = errors.New("invalid") // the request failed validation before any write
// Refusal is an error an edge classifies with errors.Is while its text
// stays the sentence written for the caller: fmt.Errorf("%w: …") would
// prepend the sentinel's own words.
type Refusal struct{ Kind error; Msg string }
func (e *Refusal) Error() string { return e.Msg }
func (e *Refusal) Unwrap() error { return e.Kind }
func Refuse(kind error, format string, args ...any) error
```

- [ ] **Step 1: Write the failing tests** (`fk_test.go`)
  - `TestWriterEnforcesForeignKeys`: `PRAGMA foreign_keys` on the writer is 1.
  - `TestDeleteProjectDataWithKeys`: create a project with a key, then `DeleteProjectData` succeeds and both rows are gone.
  - `TestMigrationViolationFails`:
    1. `newTestDBAt(t, 20)`;
    2. with FKs off (`PRAGMA foreign_keys=OFF` via ExecForTest), insert an `ingest_keys` row for project 999;
    3. `PRAGMA foreign_keys=ON`;
    4. `db.Migrate(ctx)` must fail with an error containing `foreign_key_check`.
  - `TestExistingDatabasePassesCheck`: a database seeded the way `zz_seed_test.go` seeds (projects, keys) migrates to latest with no error.
  - `manage.ErrInvalid == store.ErrInvalid`.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/store/sqlite/ -run 'ForeignKey|DeleteProjectDataWithKeys|Violation'` → FAIL.

- [ ] **Step 3: Implement**
  - DSN: add `"_pragma=foreign_keys(1)"` to the list in `openAt`. Comment it: "enforced since 021: widgets cascade with their dashboard and lose a deleted component (spec D7)".
  - `migrateThrough`:
    1. before the loop, `PRAGMA foreign_keys=OFF` (the store has one connection, `SetMaxOpenConns(1)`, so the pragma holds for the loop; it cannot change inside a transaction);
    2. `defer` turning it back on;
    3. after the loop, run `PRAGMA foreign_key_check` and fail with `fmt.Errorf("sqlite: foreign_key_check after migrating: %s row %d violates %s", table, rowid, parent)` on the first row.
  - `DeleteProjectData`:
    1. check existence first (`SELECT COUNT(*) FROM projects WHERE id=?`, 0 → `ErrNotFound`);
    2. delete from every table in `projectTables`;
    3. then delete the project row;
    4. then audit.

    Update the doc comment.
  - `api/refusal.go`: `invalidf` → `store.Refuse(store.ErrInvalid, …)`, `notFoundf` → `store.Refuse(store.ErrNotFound, …)`. Delete the local `refusal` type. `writeError` keeps matching `manage.ErrInvalid` (the same value).
  - `manage/errors.go`: the doc says all three are the store's values.

- [ ] **Step 4: Verify** — `make check`. Expected: PASS. Every existing `errors.Is(err, manage.ErrInvalid)` still holds.

- [ ] **Step 5: Commit** — `refactor(store): enforce foreign keys and share one refusal vocabulary`

---

### Task 4: Migration 021 and the reporting rows in the store

**Files:**
- Create:
  - `internal/store/sqlite/migrations/021_reporting.sql`;
  - `internal/store/reporting.go` (row types);
  - `internal/store/sqlite/reporting.go`;
  - `internal/store/sqlite/migration021_test.go`, `internal/store/sqlite/reporting_test.go`.
- Modify: `internal/store/store.go` (`Store` gains the methods below).

**Interfaces:**
- Produces (package `store`):

```go
const (
	OwnerSystem = "system"
	OwnerUser   = "user"
)

// Component is a row of components: a React component's contract, the
// JSON columns kept as the text the manifest carried.
type Component struct {
	Name, Description      string
	Accepts, Inputs, Props string // JSON
	DefaultWidth           int
	DefaultHeight          int
}

// Dashboard is a row of dashboards. The Last* fields are the viewer's
// stored selection; zero values mean none stored.
type Dashboard struct {
	ID                          int64
	Owner, Title, SortKey       string
	LastProjectID               int64
	LastRange, LastFrom, LastTo string
	CreatedAt, UpdatedAt        string
	ArchivedAt                  string // "" = live
	LiveWidgets                 int    // filled by reads: widgets not archived
}

// Widget is a row of widgets. Component "" is NULL: the component was
// removed from the code (spec D14).
type Widget struct {
	ID, DashboardID      int64
	Component, SortKey   string
	Width, Height        int
	Name, Title, Props   string // Props: a JSON object
	SourceType, Source   string
	CreatedAt, UpdatedAt string
	ArchivedAt           string
}
```

Methods added to `store.Store` and implemented on `*sqlite.DB`:

```go
ListComponents(ctx context.Context) ([]Component, error)              // by name
ListDashboards(ctx context.Context) ([]Dashboard, error)              // system group, then user; each by sort_key
GetDashboard(ctx context.Context, id int64) (Dashboard, error)        // ErrNotFound
ListWidgets(ctx context.Context, dashboardID int64) ([]Widget, error) // 0 = all; by dashboard_id, sort_key; archived included
GetWidget(ctx context.Context, id int64) (Widget, error)              // ErrNotFound
InsertDashboard(ctx context.Context, d Dashboard, ws []Widget, a AuditEntry) (int64, error)
UpdateDashboard(ctx context.Context, d Dashboard, a AuditEntry) error // title, sort_key
SetDashboardView(ctx context.Context, d Dashboard) error              // last_*; no audit
SetDashboardArchived(ctx context.Context, id int64, archived bool, a AuditEntry) error
InsertWidget(ctx context.Context, w Widget, a AuditEntry) (int64, error)
UpdateWidget(ctx context.Context, w Widget, a AuditEntry) error       // every editable column
SetWidgetArchived(ctx context.Context, id int64, archived bool, a AuditEntry) error
```

Store behaviours:
- **Inserts** fill the audit `Subject` with `dashboard/<id>` or `widget/<id>`. `InsertDashboard` inserts its widgets in the same transaction with `DashboardID` set.
- **Audit** uses a new `audit(ctx, tx, a)` helper that writes `audit_log` without bumping `config_version`. Reporting writes do not reload the project registry.
- **Unique violations** on `(dashboard_id, name)`, `(dashboard_id, sort_key)` or `(owner, sort_key)` map to `ErrConflict`.
- **Unknown ids** on update, archive or view map to `ErrNotFound`.
- **Timestamps:** `updated_at` is set on every update.
- **Archiving** sets `archived_at` only when it is NULL; restoring clears it. Both are idempotent, like `SetProjectArchived`.
- **NULLs:** `Component == ""` writes `NULL` (`NULLIF(?, '')`); reads use `COALESCE(component, '')`.
- **Stored selection:** `last_project_id` 0 is written and read as NULL (`NULLIF(?,0)`, `COALESCE(…,0)`).

- [ ] **Step 1: Write `021_reporting.sql`**

```sql
-- 021: reporting (spec 2026-09-25). Components are the React components
-- the UI ships, registered by the release; dashboards and widgets are what
-- agents build, plus the system dashboards the release migrates on every
-- run (reporting_migrations records each). Two foreign keys and no checks:
-- a dashboard's widgets go with it, and a component deleted by a release
-- leaves its widgets with component NULL ("component removed", D14).
-- Every other rule is enforced in Go.
CREATE TABLE components (
    name           TEXT PRIMARY KEY,
    description    TEXT NOT NULL,
    accepts        TEXT NOT NULL,  -- JSON array of source type names
    inputs         TEXT NOT NULL,  -- JSON {open, columns}
    props          TEXT NOT NULL,  -- JSON schema
    default_width  INTEGER NOT NULL,
    default_height INTEGER NOT NULL
);

CREATE TABLE dashboards (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    owner           TEXT NOT NULL,  -- 'system' | 'user'
    title           TEXT NOT NULL,
    sort_key        TEXT NOT NULL,
    last_project_id INTEGER,
    last_range      TEXT,
    last_from       TEXT,
    last_to         TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    archived_at     TEXT
);
CREATE UNIQUE INDEX dashboards_order ON dashboards (owner, sort_key);

CREATE TABLE widgets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    dashboard_id INTEGER NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
    component    TEXT REFERENCES components(name) ON DELETE SET NULL,
    sort_key     TEXT NOT NULL,
    width        INTEGER NOT NULL,
    height       INTEGER NOT NULL,
    name         TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    props        TEXT NOT NULL DEFAULT '{}',
    source_type  TEXT NOT NULL,
    source       TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    archived_at  TEXT
);
CREATE UNIQUE INDEX widgets_order ON widgets (dashboard_id, sort_key);
CREATE UNIQUE INDEX widgets_name ON widgets (dashboard_id, name);

CREATE TABLE reporting_migrations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    hash       TEXT NOT NULL,
    version    TEXT NOT NULL,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- Ids 1–999 are the system dashboards' (spec D8): agent-made ones start
-- at 1001, so a system id can never collide with one.
INSERT INTO sqlite_sequence (name, seq) VALUES ('dashboards', 1000);
```

- [ ] **Step 2: Write the failing tests**
  - `migration021_test.go`:
    - `newTestDBAt(t, 21)` has the four tables and three unique indexes;
    - an inserted user dashboard gets id 1001;
    - an explicit `id=3` insert works, and the next auto id is still 1002;
    - deleting a dashboard (FKs on) deletes its widgets;
    - deleting a component sets `component` NULL on its widgets;
    - a database seeded at 20 then migrated passes `foreign_key_check`.
  - `reporting_test.go` covers the behaviours listed under Interfaces:
    - an insert with widgets round-trips every field;
    - `ListDashboards` returns the system group first;
    - `LiveWidgets` counts only unarchived widgets;
    - a duplicate widget name is `ErrConflict`;
    - archive then restore round-trips, and archive is idempotent;
    - `SetDashboardView` writes only `last_*` and no audit row;
    - an unknown id is `ErrNotFound` for Get, Update and archive;
    - `Component ""` stores NULL;
    - audit subjects are `dashboard/<id>` and `widget/<id>`;
    - `config_version` is unchanged by reporting writes.

- [ ] **Step 3: Run to verify failure** — `go test ./internal/store/sqlite/ -run 'Migration021|Reporting'` → FAIL.

- [ ] **Step 4: Implement** the types, the interface additions and `sqlite/reporting.go`.
  - Follow `registry.go`'s style (`d.tx`, explicit column lists, a small `scanDashboard`/`scanWidget`).
  - `ListDashboards` query:

    ```sql
    SELECT d.…, (SELECT COUNT(*) FROM widgets w WHERE w.dashboard_id=d.id AND w.archived_at IS NULL)
    FROM dashboards d ORDER BY d.owner='user', d.sort_key
    ```

  - Map unique errors with `strings.Contains(err.Error(), "UNIQUE constraint failed: widgets.")` (and `dashboards.`) to `store.Refuse(store.ErrConflict, …)`, as `insertKey` does.

- [ ] **Step 5: Verify** — `make check` → PASS (the `projectTables` cross-check test still passes: the new tables carry no `project_id` column).

- [ ] **Step 6: Commit** — `feat(store): add the components, dashboards and widgets tables (migration 021)`

---

### Task 5: `SyncReporting` and `ReportingHash`

**Files:**
- Create: `internal/store/sqlite/reporting_sync.go`, `internal/store/sqlite/reporting_sync_test.go`
- Modify: `internal/store/reporting.go`, `internal/store/store.go`

**Interfaces:**
- Produces:

```go
// ReportingSync is the system state a release carries; SyncReporting
// makes the database match it in one transaction (spec D23).
type ReportingSync struct {
	Hash, Version string
	Components    []Component
	Dashboards    []SystemDashboard // ascending id
}
type SystemDashboard struct {
	ID              int64
	Title, SortKey  string
	Range           string   // the starting selection; written on insert only
	Widgets         []Widget // Name identifies the row; SortKey, sizes and content as in the files
}

ReportingHash(ctx context.Context) (string, error)      // hash of the latest reporting_migrations row, "" if none
SyncReporting(ctx context.Context, s ReportingSync) error
```

- [ ] **Step 1: Write the failing tests** (`reporting_sync_test.go`)
  - **First sync inserts.** Components, dashboards 1 and 2 (owner `system`, `last_range` from `Range`) and widgets. There is one `reporting_migrations` row, and one audit row with actor `release`, action `reporting.migrate`, and a detail naming the counts.
  - **Second sync with edits.** A renamed title, one widget removed, one added and one re-ordered. The kept widget keeps its id; the removed one is gone; `last_range`/`last_project_id` set by `SetDashboardView` in between survive.
  - **Components.**
    - A component dropped from the list is deleted, and a user widget using it now has `Component == ""`.
    - A component that comes back does not restore it.
  - **Dashboards.**
    - A system dashboard dropped from the list is deleted with its widgets.
    - User dashboards are never touched.
  - **Re-ordering by key swap.** Swapping two widgets' sort keys in one sync succeeds; this is the unique-index trap below.
  - **Failure.**
    - A failing sync (a widget naming a missing component) rolls back everything.
    - `ReportingHash` stays as before.

- [ ] **Step 2: Run to verify failure.**

- [ ] **Step 3: Implement** in one `d.tx`:
  1. **Components.** `INSERT … ON CONFLICT(name) DO UPDATE SET description=excluded.description, …` for each. Then `DELETE FROM components WHERE name NOT IN (…)`; with an empty list, delete all. FKs are on, so widgets get NULL.
  2. **Park the keys.**
     - Set every system dashboard's key: `UPDATE dashboards SET sort_key = '~' || id WHERE owner='system'`.
     - Set every system widget's key: `UPDATE widgets SET sort_key = '~' || id WHERE dashboard_id IN (SELECT id FROM dashboards WHERE owner='system')`.

     `~` sorts after every base-62 key and ids are unique, so re-assigning keys one row at a time can never hit the unique indexes mid-way.
  3. **Dashboards.** Upsert each system dashboard:

     ```sql
     INSERT INTO dashboards (id, owner, title, sort_key, last_range)
     VALUES (?, 'system', ?, ?, ?)
     ON CONFLICT(id) DO UPDATE SET title=excluded.title, sort_key=excluded.sort_key,
       updated_at=strftime(...) WHERE dashboards.owner='system'
     ```

     Then delete `owner='system'` rows whose id is not in the list (cascade takes their widgets).
  4. **Widgets.** Per dashboard, upsert on `(dashboard_id, name)`: `ON CONFLICT(dashboard_id, name) DO UPDATE SET component=…, sort_key=…, width=…, height=…, title=…, props=…, source_type=…, source=…, archived_at=NULL, updated_at=…`. Then delete that dashboard's widgets whose name is not in the list.
  5. **History.** Insert `reporting_migrations (hash, version)`, then `audit(ctx, tx, AuditEntry{Actor: "release", Action: "reporting.migrate", Subject: s.Hash[:12], Detail: fmt.Sprintf("%d components (%d removed), %d system dashboards (%d removed), %d widgets (%d removed)", …)})`. Take the removed counts from `RowsAffected`.

- [ ] **Step 4: Verify** — `make check` → PASS.

- [ ] **Step 5: Commit** — `feat(store): sync the release's system dashboards and components in one transaction`

---

### Task 6: Purge archived projects, dashboards and widgets

**Files:**
- Create: `internal/store/sqlite/purge.go`, `internal/store/sqlite/purge_test.go`
- Modify:
  - `internal/store/reporting.go` (`PurgeResult`) and `internal/store/store.go`;
  - `internal/jobs/jobs.go`: `Store` gains `PurgeArchived`, and `RunDailyPass` calls it first;
  - `internal/jobs/jobs_test.go` and the fake store there;
  - `internal/config/config.go` and `config_test.go`: `Retention.ArchivedDays` from `RETENTION_ARCHIVED_DAYS`, default 30, negative refused;
  - `docs/deployment.md` (variable table row);
  - `docs/twillingate.md` (project lifecycle: archived projects and their data are deleted after `RETENTION_ARCHIVED_DAYS`; `archive_project`'s description changes);
  - `internal/api/ops_read.go`: `archive_project` description "data kept, purged after RETENTION_ARCHIVED_DAYS (default 30) unless restored";
  - `deploy/UPGRADES.md` (a 021 section; this task writes the purge paragraph).

**Interfaces:**
- Produces:

```go
type PurgeResult struct{ Projects, Dashboards, Widgets []int64 }
// PurgeArchived deletes every project, dashboard and widget archived more
// than days ago, each in its own transaction with an audit row (actor
// "retention"). days <= 0 purges nothing.
PurgeArchived(ctx context.Context, days int) (PurgeResult, error)
```

- `jobs.Store` gains `PurgeArchived`. `Runner` reloads the registry (`r.reg.Reload(ctx)`) when `len(res.Projects) > 0`.

- [ ] **Step 1: Write the failing tests.**

  `purge_test.go` sets `archived_at` directly with ExecForTest:
  - **Ages.** With days=30, a widget, a dashboard (and its widgets, by cascade) and a project archived 31 days ago are gone. Ones archived 29 days ago stay.
  - **Project data.** A purged project's events, aggregates and keys are gone, reusing `DeleteProjectData`'s table list.
  - **Days = 0** purges nothing.
  - **Old timestamp format.** A project archived before the upgrade, in `datetime('now')` format, is judged by its own time: use `julianday()` for both formats.
  - **Audit.** Each purge writes one audit row, actor `retention`, action `project.purge`, `dashboard.purge` or `widget.purge`.
  - **System rows.** A system dashboard is never archived and so never purged; assert that nothing with `owner='system'` is selected even if forced archived.

  `jobs_test.go` checks the pass calls `PurgeArchived(ctx, cfg.Retention.ArchivedDays)` and reloads the registry when projects were purged.

- [ ] **Step 2: Run to verify failure.**

- [ ] **Step 3: Implement.**
  - Select ids per kind with `julianday(archived_at) < julianday('now') - ?`. Dashboards also need `owner='user'`. Widgets are only those whose dashboard is not itself being purged; a purged dashboard takes them by cascade.
  - Delete each in its own `d.tx`:
    - projects via the body of `DeleteProjectData`: refactor it into `deleteProject(ctx, tx, id)` used by both;
    - dashboards `DELETE FROM dashboards WHERE id=?`;
    - widgets `DELETE FROM widgets WHERE id=?`.
  - Project audits use `auditAndBump` (the registry changes); dashboard and widget audits use `audit`.
  - Config: `ArchivedDays int` on `Retention` (json tag `archived_days`), `e.num("RETENTION_ARCHIVED_DAYS", 30)`; `validate` refuses negatives.
  - Jobs: at the top of `RunDailyPass`, if `ret.ArchivedDays > 0`, call `PurgeArchived`. Log the counts at info; log an error without failing the pass. Reload the registry after a project purge.
  - `IncrementalVacuum` already runs at the end.

- [ ] **Step 4: Docs.**
  - `docs/deployment.md`: add the row `RETENTION_ARCHIVED_DAYS | 30 | Days after archiving that a project (with all its data), a dashboard or a widget is deleted by the daily pass. 0 keeps archived items forever.`
  - `docs/twillingate.md`: add the lifecycle sentence.
  - `deploy/UPGRADES.md`: start a `## 021` section with the purge instruction from the spec's Docs list: before upgrading, restore the projects to keep or set `RETENTION_ARCHIVED_DAYS=0`; the first night deletes projects archived longer ago than that, with their data.

- [ ] **Step 5: Verify** — `make check` → PASS (the docs sync env-var test binds the new row).

- [ ] **Step 6: Commit** — `feat(jobs): delete archived projects, dashboards and widgets after RETENTION_ARCHIVED_DAYS`

---

### Task 7: `internal/reporting` core: components, source types, validation, names

**Files:**
- Create:
  - `internal/reporting/reporting.go` (package doc, `Store`, `Service`, `Options`, `New`);
  - `types.go`, `components.go`, `source.go`, `validate.go`;
  - tests: `components_test.go`, `source_test.go`, `validate_test.go`;
  - `testdata/components.json` (a small manifest: `stat`, `line`, `table`, `markdown`, `pie`).
- Modify: `internal/archtest/archtest_test.go` (`"internal/reporting": 1`), `go.mod` (`github.com/google/jsonschema-go` direct).

**Interfaces:**
- Consumes: `store` row types (Task 4/5), `readsql.DB`/`Check`/`Result` (Task 2).
- Produces:

```go
// reporting.go
type Store interface {
	ListComponents(ctx context.Context) ([]store.Component, error)
	ListDashboards(ctx context.Context) ([]store.Dashboard, error)
	GetDashboard(ctx context.Context, id int64) (store.Dashboard, error)
	ListWidgets(ctx context.Context, dashboardID int64) ([]store.Widget, error)
	GetWidget(ctx context.Context, id int64) (store.Widget, error)
	InsertDashboard(ctx context.Context, d store.Dashboard, ws []store.Widget, a store.AuditEntry) (int64, error)
	UpdateDashboard(ctx context.Context, d store.Dashboard, a store.AuditEntry) error
	SetDashboardView(ctx context.Context, d store.Dashboard) error
	SetDashboardArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error
	InsertWidget(ctx context.Context, w store.Widget, a store.AuditEntry) (int64, error)
	UpdateWidget(ctx context.Context, w store.Widget, a store.AuditEntry) error
	SetWidgetArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error
	ReportingHash(ctx context.Context) (string, error)
	SyncReporting(ctx context.Context, s store.ReportingSync) error
}
type Options struct {
	CacheAge, RefreshAge time.Duration // REPORTING_CACHE_SECONDS, REPORTING_REFRESH_SECONDS
	Now                  func() time.Time // nil = time.Now
}
type Service struct{ st Store; db *readsql.DB; sources map[string]SourceType; cache *cache; now func() time.Time }
func New(st Store, db *readsql.DB, opt Options) *Service

// types.go: the edge types api returns as they are
type Source struct {
	Type    string `json:"type" jsonschema:"sql or md"`
	Content string `json:"content" jsonschema:"the SQL query, or the Markdown text"`
}
type Column struct {
	Name     string   `json:"name"`
	Types    []string `json:"types"` // any of number, text, day
	Optional bool     `json:"optional,omitempty"`
}
type Inputs struct {
	Open    bool     `json:"open"` // any columns (table)
	Columns []Column `json:"columns"`
}
type Component struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Accepts       []string        `json:"accepts"`
	Inputs        Inputs          `json:"inputs"`
	Props         json.RawMessage `json:"props"`
	DefaultWidth  int             `json:"default_width"`
	DefaultHeight int             `json:"default_height"`
	schema        *jsonschema.Resolved
}

// source.go
type Params struct {
	ProjectID int64
	From, To  string
}
type SourceType interface {
	Name() string
	Validate(ctx context.Context, content string, c Component) error // refusals are store.ErrInvalid
	Load(ctx context.Context, content string, p Params) (any, error)
	Cacheable() bool
	Follows(content string) (project, rng bool)
}
type Markdown struct{ Markdown string `json:"markdown"` }
func newSources(db *readsql.DB, sampleRows bool) map[string]SourceType // "sql", "md"

// components.go
func ParseManifest(b []byte) ([]Component, error)          // {"components": [...]}; checks accepts name registered types, sizes 1–12, props compiles
func fromRow(r store.Component) (Component, error)
func (c Component) row() store.Component
func (c Component) checkProps(props json.RawMessage) error  // ErrInvalid naming the first violation
func (c Component) checkColumns(cols []string) error        // required present; closed → no extras
func (c Component) checkRows(res readsql.Result) error      // each typed column's values fit its types

// validate.go
func deriveName(title string, taken map[string]bool) string
func checkSize(width, height int) error
func (s *Service) validateWidget(ctx context.Context, comps map[string]Component, w store.Widget) error
```

- [ ] **Step 1: Write the failing tests.**
  - `components_test.go`:
    - `ParseManifest(testdata)` loads 5 components.
    - Refusals: accepts naming an unregistered type (`"image"`) → error naming it; `default_width` 13 → error; props that is not a JSON schema object → error.
    - `checkProps`: `{"format":"percent"}` ok; `{"format":"bogus"}` → ErrInvalid mentioning `format`; an unknown prop → ErrInvalid (schemas use `additionalProperties: false`).
    - `checkColumns` on `line`: `x,y` ok; `x,y,series` ok; `x,visitor` → message `line needs y (number); columns are x, visitor`; `x,y,extra` → message says `extra` is not an input of line; `table` accepts anything.
    - `checkRows`: `y` "40" ok, "4.5" ok, "" ok (NULL), "abc" → ErrInvalid; day "2026-08-27" ok, "yesterday" → ErrInvalid.
  - `source_test.go`, against a temp migrated database with `readsql.Open`:
    - `sql` `Follows`: `:project` → (true, false); `:from` → (false, true); neither → (false, false).
    - `sql` `Validate`:
      - `:path` → message `sql uses :path; widgets get only :project, :from and :to`; a positional `?` is refused the same way;
      - a syntax error → ErrInvalid with SQLite's text;
      - `select * from meta` → refused;
      - columns checked via `LIMIT 0`.
    - `md` `Validate`: `""` or whitespace → ErrInvalid (`markdown text is empty`).
    - `md` `Load` returns `Markdown{…}`; `Cacheable` is false for md and true for sql.
    - `sql` `Load` binds only the params the SQL uses (`sql.Named("project", …)`) and returns `readsql.Result`.
  - `validate_test.go`:
    - `TestDeriveName`: `"Visitors & views"` → `visitors-views`; taken → `visitors-views-2`, then `-3`; `"Посетители"` → `widget`; `"📈"` → `widget`; `""` → `widget`; a 200-char title is cut to 60 chars without a trailing `-`.
    - `checkSize`: 0 and 13 → `width is columns out of 12, from 1 to 12`; height likewise (`height is rows of 40px, from 1 to 12`).
    - `validateWidget`: unknown component → `component gauge does not exist; list_components names the ones there are`; component not accepting the type (`markdown` with `sql`) → names both; unregistered source type → `source type image does not exist; there are md and sql`.

- [ ] **Step 2: Run to verify failure.**

- [ ] **Step 3: Implement.**
  - **Props schemas:** `jsonschema.Schema` unmarshal, then `Resolve(nil)`, then `Validate(map[string]any)` (unmarshal the props into `any`). The resolved schema is cached on the Component.
  - **`sqlSource`:**
    - `Follows` uses `readsql.Check` params.
    - `Validate`: params ⊆ {`:project`, `:from`, `:to`}. Then run the SQL wrapped as `SELECT * FROM (…) LIMIT 0` through `db.Query`; `Query` already wraps with `LIMIT maxRows+1`, so use `db.Run(ctx, "SELECT * FROM (" + trimmed + "\n) LIMIT 0", args)` after `readsql.Check`. Then `checkColumns`.
    - Sample values: project = `sampleProject(ctx)`, from = today−6, to = today (UTC).
    - When `sampleRows` is true, run again with `LIMIT 5` and `checkRows`; no rows means the column check only.
    - `sampleProject`: `db.Run` on

      ```sql
      SELECT p.id FROM projects p WHERE p.archived_at IS NULL
      ORDER BY MAX(COALESCE((SELECT MAX(day) FROM events WHERE family='views' AND project_id=p.id),''),
                   COALESCE((SELECT MAX(day) FROM events WHERE family='product' AND project_id=p.id),'')) DESC, p.id
      LIMIT 1
      ```

      (0 if none). Trusted SQL reading `projects`, which is allowed.
    - `Load`: `db.Query(ctx, content, named args for used params…)`.
    - Errors: `readsql.ErrRefused`/`ErrTimeout`/SQL errors → `store.Refuse(store.ErrInvalid, …)`. A timeout names `API_QUERY_TIMEOUT` (`query exceeded API_QUERY_TIMEOUT (10s); narrow the range or group the query`).
  - **`mdSource`:** trivially.
  - **`deriveName`:** lower-case ASCII letters and digits kept, runs of anything else become `-`, trimmed; cut to 60 characters; `widget` if empty; suffix `-2`… while taken.

- [ ] **Step 4: Verify** — `go test ./internal/reporting/ && make check` → PASS.

- [ ] **Step 5: Commit** — `feat(reporting): validate widgets against their component and source type`

---

### Task 8: Reporting operations (reads, writes, placement)

**Files:**
- Create: `internal/reporting/read.go`, `internal/reporting/ops.go`, `internal/reporting/ops_test.go`, `internal/reporting/testutil_test.go` (opens a temp store, migrates, syncs `testdata/components.json` via `SyncReporting` with no dashboards, and returns `*Service`)

**Interfaces:**
- Consumes: Task 7.
- Produces (every write takes `actor` for the audit row; api passes `actorFrom(ctx)`):

```go
type DashboardInfo struct {
	ID         int64  `json:"dashboard_id"`
	Title      string `json:"title"`
	Owner      string `json:"owner"`
	ProjectID  int64  `json:"project_id,omitempty"` // stored selection
	Range      string `json:"range,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Widgets    int    `json:"widgets"` // live widgets
	ArchivedAt string `json:"archived_at,omitempty"`
}
type Dashboards struct {
	Timezone   string          `json:"timezone"` // "UTC" (D19)
	Dashboards []DashboardInfo `json:"dashboards"`
	Dev        bool            `json:"dev,omitempty"` // set by reporting dev (Task 17)
}
type WidgetInfo struct {
	ID             int64           `json:"widget_id"`
	DashboardID    int64           `json:"dashboard_id"`
	Name           string          `json:"name"`
	Component      *string         `json:"component"` // null: removed from the code
	Title          string          `json:"title,omitempty"`
	Width          int             `json:"width"`
	Height         int             `json:"height"`
	Props          json.RawMessage `json:"props"`
	Source         Source          `json:"source"`
	FollowsProject bool            `json:"follows_project"`
	FollowsRange   bool            `json:"follows_range"`
	ArchivedAt     string          `json:"archived_at,omitempty"`
}
type DashboardDetail struct {
	DashboardInfo
	FollowsProject bool         `json:"follows_project"` // any live widget does: show the project switcher
	FollowsRange   bool         `json:"follows_range"`
	Widgets        []WidgetInfo `json:"widgets"` // live, in order; each carries width and height (the layout)
}
type ListedWidget struct {
	WidgetInfo
	Dashboard DashboardInfo `json:"dashboard"`
	Position  int           `json:"position"` // 1-based among the dashboard's widgets, archived included
}
type WidgetSpec struct {
	Name      string          `json:"name,omitempty" jsonschema:"unique on the dashboard; derived from the title when omitted"`
	Component string          `json:"component" jsonschema:"a component name from list_components"`
	Title     string          `json:"title,omitempty"`
	Props     json.RawMessage `json:"props,omitempty"`
	Source    Source          `json:"source"`
	Width     int             `json:"width,omitempty" jsonschema:"columns out of 12 (1–12); default from the component"`
	Height    int             `json:"height,omitempty" jsonschema:"rows of 40px (1–12); default from the component"`
}

func (s *Service) SourceTypes() []string                             // sorted
func (s *Service) Components(ctx context.Context) ([]Component, error)
func (s *Service) Dashboards(ctx context.Context) (Dashboards, error)
func (s *Service) Dashboard(ctx context.Context, id int64) (DashboardDetail, error)
func (s *Service) Widgets(ctx context.Context, dashboardID int64, component string) ([]ListedWidget, error)

type CreateDashboard struct { Title, Range string; After *int64; Widgets []WidgetSpec }
type UpdateDashboard struct { ID int64; Title string; After *int64 }
type AddWidget struct { DashboardID int64; After *int64; WidgetSpec }
type UpdateWidget struct {
	ID        int64
	Name      *string
	Component *string
	Title     *string
	Props     json.RawMessage
	Source    *Source
	Width     *int
	Height    *int
}
type CopyWidget struct { ID, DashboardID int64; After *int64 }
type View struct { DashboardID, ProjectID int64; Range, From, To string }

func (s *Service) CreateDashboard(ctx context.Context, actor string, in CreateDashboard) (DashboardDetail, error)
func (s *Service) UpdateDashboard(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error)
func (s *Service) DuplicateDashboard(ctx context.Context, actor string, id int64) (DashboardDetail, error)
func (s *Service) ArchiveDashboard(ctx context.Context, actor string, id int64) error
func (s *Service) RestoreDashboard(ctx context.Context, actor string, id int64) error
func (s *Service) AddWidget(ctx context.Context, actor string, in AddWidget) (WidgetInfo, error)
func (s *Service) UpdateWidget(ctx context.Context, actor string, in UpdateWidget) (WidgetInfo, error)
func (s *Service) CopyWidget(ctx context.Context, actor string, in CopyWidget) (WidgetInfo, error)
func (s *Service) ArchiveWidget(ctx context.Context, actor string, id int64) error
func (s *Service) RestoreWidget(ctx context.Context, actor string, id int64) error
func (s *Service) SetView(ctx context.Context, in View) error
var Presets = []string{"today", "yesterday", "7d", "30d", "90d", "custom"}
```

Rules to implement and test (`ops_test.go`, one test per bullet):
- **Placement** (Deviation 1): `After == nil` → last; `*After == 0` → first; otherwise after that id.
  - Keys are computed with `sortkey.Between(prevKey, nextKey)` over the **full** order (archived included).
  - For widgets, `after` must be a widget on the same dashboard (`after 42 is not a widget on dashboard 1001`).
  - For dashboards, `after` must be a user dashboard.
  - An insert writes one row; neighbours' keys are unchanged (assert by re-reading them).
  - On `ErrConflict` from a concurrent insert at the same spot, recompute neighbours and retry once.
- **Create** validates every widget before writing anything (all or nothing).
  - `Range` defaults to `7d`; unknown presets are refused (`range must be one of today, yesterday, 7d, 30d, 90d, custom`). `custom` via create is refused ("create with a preset; the viewer picks custom dates").
  - Widget names are derived or checked for uniqueness in the batch.
  - Keys come from `sortkey.Spread("", "", n)`.
  - Sizes default from the component.
- **System dashboards** refuse update, archive, restore, add, and update/archive/restore of their widgets: `dashboard 3 is a system dashboard and changes only with a release; duplicate_dashboard makes an editable copy`. They allow duplicate, copy-from and `SetView`.
- **Duplicate** makes a user dashboard titled `<title> (copy)`, placed last. It copies the live widgets in order with fresh keys and the same names/sizes/content, keeps the stored selection, and works on system dashboards.
- **Archived items.**
  - An archived widget refuses update and copy (`widget 42 is archived; restore_widget first`).
  - Adding to or updating an archived dashboard is refused likewise.
  - Restoring puts a widget back at its key.
  - A widget inserted next to an archived one gets a key that does not collide.
- **Removed component** (`Component == ""`):
  - update accepts only `Component` (must exist), `Width` and `Height`; anything else is refused with `widget 42's component was removed; set component first`;
  - copy is refused;
  - archive works.
- **Update** re-validates the whole resulting widget. `Name` changes are checked for uniqueness. Returns the new `WidgetInfo`.
- **Copy** into `DashboardID` (a user dashboard) keeps width and height; the name is re-derived if taken there; the source may be a system widget.
- **SetView** needs the dashboard's switchers. Compute them from its live widgets via `Follows`:
  - `project_id` is required (non-zero) when it has a project switcher and refused when it has none; likewise `range`.
  - `custom` needs valid `from ≤ to` within 365 days; `from`/`to` without `custom` are refused.
  - It is allowed on system dashboards and writes no audit.
- **Widgets filter:** `Widgets(ctx, 0, "")` returns all; the `dashboardID` and `component` filters combine.
- **Audit:** every write writes one audit row with the given actor (assert via `SELECT actor, action FROM audit_log`). Actions:
  - `dashboard.create`, `dashboard.update`, `dashboard.duplicate`, `dashboard.archive`, `dashboard.restore`;
  - `widget.add`, `widget.update`, `widget.copy`, `widget.archive`, `widget.restore`.

- [ ] **Step 1: Write the failing tests** — one `func Test…` per bullet above, using `newTestService(t)` from `testutil_test.go`. Widgets use the `markdown` component with `md` sources unless a test needs `sql`, so most tests need no data.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/reporting/ -run 'Dashboard|Widget|View|Place'` → FAIL.

- [ ] **Step 3: Implement** `read.go` and `ops.go`. Load components once per operation with `s.components(ctx)` (`map[string]Component`).

- [ ] **Step 4: Verify** — `go test -race ./internal/reporting/ && make check` → PASS.

- [ ] **Step 5: Commit** — `feat(reporting): create, update, copy and archive dashboards and widgets`

---

### Task 9: Widget data, ranges and the cache

**Files:**
- Create: `internal/reporting/data.go`, `internal/reporting/cache.go`, `internal/reporting/data_test.go`, `internal/reporting/cache_test.go`
- Modify:
  - `internal/config/config.go` and `config_test.go`: `Reporting ReportingConfig{CacheAge, RefreshAge time.Duration}` from `REPORTING_CACHE_SECONDS` (900) and `REPORTING_REFRESH_SECONDS` (60), read with `e.num` and converted to seconds. Negative is refused; refresh > cache when cache > 0 is refused (`config: REPORTING_REFRESH_SECONDS (120) exceeds REPORTING_CACHE_SECONDS (60)`).
  - `docs/deployment.md`: both rows, plus a sentence that `API_QUERY_TIMEOUT` and `API_QUERY_MAX_ROWS` also bound widget queries.
  - `go.mod`: `golang.org/x/sync` direct.

**Interfaces:**
- Produces:

```go
type DataRequest struct {
	WidgetID  int64
	ProjectID int64
	From, To  string
	Fresh     bool
}
type WidgetData struct {
	WidgetID     int64      `json:"widget_id"`
	SourceType   string     `json:"source_type"`
	ProjectID    *int64     `json:"project_id,omitempty"`
	From         string     `json:"from,omitempty"`
	To           string     `json:"to,omitempty"`
	CachedAt     *time.Time `json:"cached_at,omitempty"`
	RefreshAfter *time.Time `json:"refresh_after,omitempty"`
	Removed      bool       `json:"removed"`
	Data         any        `json:"data"` // readsql.Result (sql) | Markdown (md) | nil (removed)
}
func (s *Service) WidgetData(ctx context.Context, in DataRequest) (WidgetData, error)
const ReleasesURL = "https://github.com/dmtrkzntsv/twillingate/releases"
func checkRange(from, to string, today civil.Date) (string, string, error) // validates, clamps to, returns applied dates
```

Rules (one test each in `data_test.go`, seeding a few `agg_views_daily` rows through the test store):
- **Archived** widget → `ErrNotFound` ("widget 42 is archived"). **Unknown** id → `ErrNotFound`.
- **Removed** component → `{removed: true, data: null}` with no query run.
- **Following the project:** `project_id` required (`widget 42 follows the project switcher; pass project_id`).
- **Following the range:** `from`/`to` required, `YYYY-MM-DD`, `from ≤ to`, span ≤ 365 days, a future `to` clamped to today. The envelope echoes the applied values; a fixed widget ignores them and echoes none.
- **Load failures** — a failing query or rows that no longer satisfy the component: `ErrInvalid` whose message ends `; if this started after an update, see the release notes at https://github.com/dmtrkzntsv/twillingate/releases`.
- **Truncation:** a result cut at the row cap has `truncated: true` in `data`.
- **Markdown:** `md` returns `{markdown}` with no `cached_at`/`refresh_after`.
- **Cache** (`cache_test.go`, with an injected clock):
  - keyed by what the widget follows plus a SHA-256 of the source content (Deviation 2);
  - an ordinary request reuses an entry younger than `CacheAge` and `fresh=true` one younger than `RefreshAge`;
  - `CacheAge == 0` recomputes every time;
  - `refresh_after = cached_at + RefreshAge`;
  - 20 concurrent identical requests run the loader once (`singleflight`, counting calls through a stub `SourceType`);
  - updating a widget's source yields a miss;
  - expired entries are swept when a put happens after an interval of `max(CacheAge, RefreshAge)`.

- [ ] **Step 1: Write the failing tests** (as listed).
- [ ] **Step 2: Run to verify failure.**
- [ ] **Step 3: Implement.**
  - `cache` is `struct{ mu sync.Mutex; entries map[string]entry; sf singleflight.Group; cacheAge, refreshAge time.Duration; lastSweep time.Time; now func() time.Time }`.
  - `get(key, fresh, load func() (any, error)) (any, time.Time, error)`: look up, check the age against `refreshAge` if fresh else `cacheAge`, otherwise `sf.Do(key, …)`, store, return.
  - Rows are type-checked (`checkRows`) on every load, since a release may change a view.
- [ ] **Step 4: Verify** — `go test -race ./internal/reporting/ ./internal/config/ && make check` → PASS.
- [ ] **Step 5: Commit** — `feat(reporting): serve widget data with a two-age cache`

---

### Task 13: System-definition migrator and `app.Migrate`

(Numbered after the web track because it needs `internal/reporting/ui/components.json` from Tasks 10–12.)

**Files:**
- Create:
  - `internal/reporting/files.go`, `internal/reporting/migrate.go`, `internal/reporting/ui.go` (embeds `ui`), `files_test.go`, `migrate_test.go`;
  - `internal/reporting/testdata/system/…` (two small dashboards for tests);
  - `internal/app/migrate.go`, `internal/app/migrate_test.go`.
- Modify:
  - `internal/app/app.go`: `Serve` calls `Migrate(ctx, cfg, st)` instead of `st.Migrate`.
  - `cmd/twillingate/migrate.go` and `cmd/twillingate/project.go` (`openOps`): call `app.Migrate`.

**Interfaces:**
- Consumes: `store.SyncReporting` (Task 5), validation (Task 7), `sortkey.Spread`.
- Produces:

```go
// files.go
type FileWidget struct {
	Name, Component, Title string
	Props                  json.RawMessage
	SourceType, Source     string
	Width, Height          int // 0 = the component's default
}
type FileDashboard struct {
	ID           int64 // 0 when the file gives none (reporting dev only)
	Title, Range string
	Widgets      []FileWidget // layout order
}
func LoadDashboard(fsys fs.FS, dir string) (FileDashboard, error)
func LoadDashboards(fsys fs.FS) ([]FileDashboard, error) // every top-level directory

// migrate.go
//go:embed system
var systemFS embed.FS
func Migrate(ctx context.Context, st Store, db *readsql.DB) error // embedded definitions
func migrateFrom(ctx context.Context, st Store, db *readsql.DB, system fs.FS, manifest []byte) error

// ui.go
//go:embed all:ui
var uiFS embed.FS
func Manifest() []byte          // ui/components.json
func UI() http.Handler          // Task 16 adds the handler; here only Manifest

// internal/app/migrate.go
func Migrate(ctx context.Context, cfg *config.Config, st store.Store) error
```

File format (D20):

```
<dir>/dashboard.json   {"id": 1, "title": "Views", "range": "7d",
                        "layout": [{"widget": "visitors", "width": 3, "height": 3}, …]}
<dir>/<name>.json      {"component": "stat", "title": "Visitors", "props": {"format": "number"}}
<dir>/<name>.sql|.md   the content; the extension is the source type
```

Loader errors (one test each in `files_test.go`, using `fstest.MapFS`):
- a config with no data file, or with two;
- a data file with no config;
- the layout naming a missing widget, or one twice;
- a widget the layout leaves out;
- a broken JSON file, naming the file;
- unknown JSON keys (`DisallowUnknownFields`).

`LoadDashboards` refuses a duplicate `id` or one outside 1–999. `LoadDashboard` does not; dev accepts no id.

`Migrate`:
1. Hash `sha256` over every file path and content in `system` (sorted) plus `manifest`, hex-encoded.
2. If `st.ReportingHash` equals it, return.
3. Parse the manifest (`ParseManifest`) and `LoadDashboards(system)`.
4. Validate every widget with sources built by `newSources(db, false)` (Deviation 4), sizes defaulted from the component, the range in `Presets` and not `custom`. On failure, stop with `reporting: system dashboard <id> widget <name>: <reason>`.
5. Build `store.ReportingSync`:
   - dashboards sorted by id with keys from `sortkey.Spread("", "", n)`;
   - widget keys from `Spread` in layout order;
   - `Version: version.Version`.
6. Call `st.SyncReporting`.

`app.Migrate`:
1. `st.Migrate(ctx)`;
2. open `readsql.Open(databasePath(cfg.Database), cfg.API.QueryTimeout, cfg.API.QueryMaxRows)`. This is the writer's own file, not `API_DB_PATH`, which may be a replica;
3. `reporting.Migrate`;
4. close.

`store.Store` must satisfy `reporting.Store`: add a compile-time assertion in `app/migrate.go`, `var _ reporting.Store = store.Store(nil)`.

- [ ] **Step 1: Write the failing tests.**
  - `migrate_test.go` uses `migrateFrom` with `testdata/system` and `testdata/components.json`:
    - **First run** inserts dashboards 1 and 2.
    - **Second run** with the same inputs writes nothing: `reporting_migrations` count is unchanged.
    - **Changed widget** — a changed widget file keeps the widget id, bumps the count and keeps `last_*`.
    - **Removed component** — a manifest missing a component used by a user widget nulls it.
    - **Failure** — an invalid system widget (a column missing for the component) returns an error and writes nothing.
    - **Sort order** — system dashboards sort in id order.
    - **Accepts** — a component whose `accepts` names an unregistered type fails.
  - `app/migrate_test.go`: after `app.Migrate` on a new database, `SELECT COUNT(*) FROM components` equals the manifest count, and `serve` boots. The existing `app_test.go` boot tests keep passing.
  - `manifest_test.go` (spec Tests "manifest"): `ParseManifest(Manifest())` succeeds. Its names are exactly the `web/src/components/widgets/*.tsx` files (excluding `*.test.tsx`), read from disk via `../../web/src/components/widgets`, in both directions.
- [ ] **Step 2: Run to verify failure.**
- [ ] **Step 3: Implement** as specified. Until Task 14 lands, `internal/reporting/system/` holds one dashboard, `views/`, with one markdown widget, so the embed is non-empty and boot works.
- [ ] **Step 4: Verify** — `make check` → PASS.
- [ ] **Step 5: Commit** — `feat(reporting): migrate the release's components and system dashboards on every run`

---

### Task 14: The five system dashboards (Evidence ported)

**Files:**
- Create:
  - `internal/reporting/system/{views,product,users,groups,retention}/…`: `dashboard.json` plus a `<name>.json` and `<name>.sql` per widget;
  - `internal/reporting/system_test.go` (the load-bearing test);
  - `internal/reporting/timing_test.go` (opt-in).
- Delete: the placeholder from Task 13.

**How to port** (read each `evidence/pages/<page>/[project].md`):

| Dashboard | id | range | source page |
| --- | --- | --- | --- |
| Views | 1 | `7d` | `evidence/pages/views/[project].md` |
| Product | 2 | `7d` | `evidence/pages/product/[project].md` |
| Users | 3 | `7d` | `evidence/pages/users/[project].md` |
| Groups | 4 | `7d` | `evidence/pages/groups/[project].md` |
| Retention | 5 | `90d` | `evidence/pages/retention/[project].md` |

- **One widget per Evidence component** (BigValue, LineChart, …), in page order. Headings become `markdown` widgets (12×1) only where the page has explanatory prose (e.g. the Consent paragraph); bare `##` headings become widget titles instead.
- **The date window becomes `day BETWEEN :from AND :to`.** Evidence's `strftime((now() …) - interval …)` pair turns into that, and `project_id = '${params.project}'` into `project_id = :project`. Queries read the `v_*` views (DuckDB syntax → SQLite: `::date` casts go, `strftime` formats are SQLite's, `nullif`/`case` stay).
- **Component mapping:**

  | Evidence | Component |
  | --- | --- |
  | `BigValue` | `stat`. The query returns `value` and, for a trend, `previous`: the same total over the preceding window, `date(:from, '-' \|\| (julianday(:to) - julianday(:from) + 1) \|\| ' days')` to `date(:from, '-1 day')`. Add `previous` for Visitors, Views, Sessions and Events. |
  | `LineChart` with several `y` | `line` with `series`: `UNION ALL` one arm per measure (`'visitors' AS series`). |
  | `LineChart` / `AreaChart` / `BarChart` | `line` / `area` / `bar` (`swapXY=true` → `bar_list` when it is a top-N list of label/value, else `bar` with `horizontal: true`). |
  | `DataTable` | `table` with `formats` and `colorscale` from the column list. |
  | `Grid cols=N` | widths: 4 → 3, 3 → 4, 2 → 6, 1 → 12. |

  Heights: stats 3, charts 8, tables 10.
- **Dropped:**
  - `ReportNav`, `ButtonGroup` and the project name query;
  - the drill-down link `detail_url`: pages show `path` as text;
  - the page `views/[project]/page.md`;
  - the empty-database sentinel rows.
- **New:** a `map` of visitors by country, 6×8, on Views next to the Countries bar list. Use `v_views_countries` (`country` is ISO alpha-2).
- **Titles, formats and axis labels** follow Evidence: `fmt=pct1` → `format: percent`, `num0` → `number`, seconds → `duration`.

**Load-bearing test** (`system_test.go`, spec Tests "system dashboards"):
1. Open a temp store and `app`-style migrate: `st.Migrate` + `reporting.Migrate` with the embedded definitions.
2. Seed one project with data for the last 100 days. Put raw `events` rows in the last 3 days (so live halves compute) and aggregate rows in every `agg_*` table the system queries read for older days. Include:
   - retention cohorts;
   - identities with names;
   - app versions;
   - consent;
   - product events with attributes.
3. For every system widget and every preset (`today`, `yesterday`, `7d`, `30d`, `90d`), resolve the dates as the UI does (`today` UTC) and call `WidgetData`.
4. Assert: no error; `checkRows` passes (implicit in `WidgetData`); `Removed` false; and at least one row for `7d` on each widget except those documented in a `mayBeEmpty` set with a reason comment.

This test is the parity-before-release gate, and it runs in `make check`.

**Timing** (`timing_test.go`, Deviation 5): skipped unless `REPORTING_TIMING_DB` names a database file. It opens it with `readsql` (read-only), picks the most recent active project, and runs every system widget cold for every preset. It logs `dashboard/widget preset duration rows` sorted by duration. It fails if any single widget exceeds `API_QUERY_TIMEOUT`'s default (10s).

- [ ] **Step 1: Write `system_test.go`** against the placeholder; it fails once the real files exist and a query is wrong, which is the point. Write the seeding helper first.
- [ ] **Step 2: Port Views, then run** `go test ./internal/reporting/ -run SystemDashboards -v`. Fix until PASS.
- [ ] **Step 3: Port Product, Users, Groups and Retention** the same way, running the test after each.
- [ ] **Step 4: Timing on the demo seed:**
  1. `make seed-demo` (see the Makefile; create the five projects first with `./twillingate project create -name dev` … as the script expects);
  2. `REPORTING_TIMING_DB=$PWD/local/twillingate.db go test ./internal/reporting/ -run Timing -v`;
  3. paste the slowest ten lines into the task report. The controller puts them in the PR body.

  If any widget exceeds 2s on `90d`, rewrite it to read the `agg_*`-backed columns more cheaply, as the spec's "open" item asks.
- [ ] **Step 5: Verify** — `make check` → PASS.
- [ ] **Step 6: Commit** — `feat(reporting): port the Evidence pages to five system dashboards`

---

### Task 15: Reporting tools, routes and resources

**Files:**
- Create: `internal/api/ops_reporting.go`, `internal/api/ops_reporting_test.go`, `docs/reporting.md` (initial: concepts, tools table, routes table, component table, parameters, ranges)
- Modify:
  - `internal/api/server.go`: `Build` gains `rst reporting.Store`, opens `readsql`, and builds `reporting.New(rst, rdb, reporting.Options{CacheAge: cfg.Reporting.CacheAge, RefreshAge: cfg.Reporting.RefreshAge})`. `NewHandler` does the same.
  - `internal/api/ops_read.go`: `host` gains `rep *reporting.Service`; `register` calls `h.registerReporting(r)`.
  - `internal/api/expose.go`: `spec` gains `RESTOnly bool`; add a `restOnly` helper that registers only the route.
  - `internal/api/expose_test.go`: parity; `view` is REST-only by choice, `reporting_guide` MCP-only.
  - `internal/api/resources.go`: `docs://reporting`, `schema://components`, `schema://dashboards`, `schema://widgets`.
  - `internal/api/seed_test.go`: `newTestHost` builds `rep` after `reporting.Migrate`.
  - `internal/api/docs_sync_test.go`:
    - tools are read from both docs;
    - the route table regex accepts `PUT`, and routes are read from `docs/twillingate.md`'s `### HTTP API` and `docs/reporting.md`'s `## HTTP API`;
    - `spellOut` covers up to 40;
    - the component table in `docs/reporting.md` is bound to `reporting.Manifest()` in both directions (names, default sizes);
    - the range vocabulary appears in `docs/reporting.md`.
  - `docs/embed.go`: `//go:embed reporting.md` → `var Reporting string`.
  - `docs/twillingate.md`: the tool count sentence and a pointer in "Answer questions with the data" to `docs://reporting` and `reporting_guide`.
  - `internal/app/app.go` and the other `Build`/`NewHandler` callers: pass `st`.

**Interfaces:**
- Consumes: Service (Tasks 7–9).
- Produces tools and routes. Every handler is a thin adapter from `…In` to the Service; path ids use JSON names `dashboard_id` and `widget_id`.

| Tool | Method | Path | Status | Annotations |
| --- | --- | --- | --- | --- |
| `list_components` | GET | `/api/components` | | ro |
| `list_dashboards` | GET | `/api/dashboards` | | ro |
| `get_dashboard` | GET | `/api/dashboards/{dashboard_id}` | | ro |
| `list_widgets` | GET | `/api/widgets` | | ro |
| `widget_data` | GET | `/api/widgets/{widget_id}/data` | | ro |
| `create_dashboard` | POST | `/api/dashboards` | 201 | write |
| `update_dashboard` | PATCH | `/api/dashboards/{dashboard_id}` | | write |
| `duplicate_dashboard` | POST | `/api/dashboards/{dashboard_id}/duplicate` | 201 | write |
| `archive_dashboard` | POST | `/api/dashboards/{dashboard_id}/archive` | | idem |
| `restore_dashboard` | POST | `/api/dashboards/{dashboard_id}/restore` | | idem |
| `add_widget` | POST | `/api/dashboards/{dashboard_id}/widgets` | 201 | write |
| `update_widget` | PATCH | `/api/widgets/{widget_id}` | | write |
| `copy_widget` | POST | `/api/widgets/{widget_id}/copy` | 201 | write |
| `archive_widget` | POST | `/api/widgets/{widget_id}/archive` | | idem |
| `restore_widget` | POST | `/api/widgets/{widget_id}/restore` | | idem |
| (REST only) | PUT | `/api/dashboards/{dashboard_id}/view` | 204-as-200 `{"status":"saved"}` | |

- **Descriptions:**
  - `create_dashboard`, `add_widget`, `update_widget` and `copy_widget` begin with `Call reporting_guide first.`
  - `archive_*` mention restore and the purge after `RETENTION_ARCHIVED_DAYS`.
  - `widget_data` says what `project_id`/`from`/`to`/`fresh` do.
- **Outputs:**
  - `list_components` returns `{source_types, components}`;
  - `list_widgets` returns `{widgets}`;
  - the others return the Service types directly.
- **Resources** return `json.MarshalIndent` of what the matching list tool returns, built by the same Service call on each read (`schema://widgets` = unfiltered `list_widgets`).

- [ ] **Step 1: Write the failing tests** (`ops_reporting_test.go`, via `callTool` and REST through `newTestRegistrar`):
  - create → get → add → widget_data round trip;
  - REST 201s;
  - a system dashboard write → 400 with the D27 message;
  - an unknown id → 404;
  - the view route stores the selection and does not appear as an MCP tool;
  - each resource equals its tool's output after a write made just before;
  - `docs://reporting` is served.
- [ ] **Step 2: Run to verify failure.**
- [ ] **Step 3: Implement**, then write `docs/reporting.md`:
  - `# Reporting` intro;
  - `## Concepts`;
  - `## Tools` (table);
  - `## HTTP API` (table in the `docs/twillingate.md` row format: ``| `GET` | `/api/components` | `list_components` |``);
  - `## Components` (the D13 table with default sizes);
  - `## Parameters and ranges` (the D17/D18 tables, every preset id in backticks);
  - `## Layout`;
  - `## Archiving and the purge`.

  Task 16 adds the workflow, examples and troubleshooting sections.
- [ ] **Step 4: Verify** — `make check` → PASS.
- [ ] **Step 5: Commit** — `feat(api): expose dashboards and widgets as MCP tools and REST routes`

---

### Task 16: Guide, server instructions, `/app/` mount, login redirect, docs

**Files:**
- Create: `internal/api/guide_reporting.go`, `internal/api/guide_reporting_test.go`
- Modify:
  - `internal/api/server.go`: `mcp.NewServer(…, &mcp.ServerOptions{Instructions: serverInstructions})`; `RegisterOn` mounts `/app/` (and `GET /app` → 301 `/app/`) with `reporting.UI()`.
  - `internal/api/seed_test.go`: the same instructions on the test server.
  - `internal/api/oauth.go`: `registerClient` also accepts a redirect whose host equals the request's `Host` and whose path is `/app/callback` (https, or http on loopback).
  - `internal/api/oauth_test.go`, `oauth_e2e_test.go`: a registration with `https://<request host>/app/callback` succeeds; one to another host's `/app/callback` fails; an e2e run through PKCE with that redirect gets a token that reads `/api/dashboards`.
  - `internal/reporting/ui.go`: `UI()` serves `uiFS`:
    - files by path;
    - any other path under `/app/` → `index.html`;
    - `Cache-Control: public, max-age=31536000, immutable` for `/app/assets/*`, and `no-cache` for `index.html`, `sw.js` and `manifest.webmanifest`;
    - `components.json` not served (404).
  - `internal/reporting/ui_test.go`.
  - `docs/reporting.md`: the workflow and rules sections, a worked example per component, refusals and fixes, and `## When widgets break after an update` (D51).
  - `docs/deployment.md`: `/app/` and installing it as an app; the redirect an `oauth://` provider must allow (`https://<api-host>/app/callback`); `twillingate reporting dev` (Task 17 fills in its usage).
  - `deploy/UPGRADES.md`, section 021:
    - `/app/` is added;
    - a release removing a component leaves "component removed" widgets;
    - a binary rollback clears the component of widgets on components the older release lacks, so run `list_widgets` before rolling back.
  - `CLAUDE.md`:
    - three contract pages, naming `docs/reporting.md` in the "do not add files there" sentence;
    - layout lines for `internal/reporting/`, `internal/shared/` and `web/`;
    - the build-and-commit rule extended to `web/` (`npm run build` there regenerates `internal/reporting/ui/`);
    - docs table rows: reporting tools/routes/resources (`internal/api/ops_reporting.go`) → `docs/reporting.md`; components (`web/src/components/widgets/`) → `docs/reporting.md`'s component table; views used by system dashboards → Task 14's test.

**Interfaces:**
- `reporting_guide` (MCP only, `ro`, no input) returns `guideOut{Markdown}` containing:
  1. the running version (`version.Version`) and `ReleasesURL`, plus `…/releases/tag/v<version>`;
  2. source types, and components with description, inputs, props and default sizes (from `rep.Components`);
  3. the views (`schemaViews`);
  4. active projects (id, name);
  5. dashboards (id, title, owner, archived);
  6. the `## Workflow` and `## Rules` sections of `docs/reporting.md`, sliced by heading with the `docSection` logic moved into a small non-test helper.
- `serverInstructions` (verbatim):

  ```
  To integrate a site or app, call integration_guide. To build or change dashboards, call reporting_guide. System dashboards are read-only; duplicate one to customize it. If widgets broke after an update, read the release notes at https://github.com/dmtrkzntsv/twillingate/releases.
  ```

- [ ] **Step 1: Write the failing tests.**
  - The guide contains every component name, `v_views_daily`, a project name, a dashboard title, the version and the releases URL.
  - The server's `InitializeResult.Instructions` names `integration_guide`, `reporting_guide` and the releases URL.
  - UI: `/app/` → 200 `text/html`; `/app/dashboards/3` → `index.html`; `/app/assets/<existing file>` → immutable; `/app/components.json` → 404; `/app` → 301.
  - The OAuth tests above.
- [ ] **Step 2: Run to verify failure.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Verify** — `make check` → PASS.
- [ ] **Step 5: Commit** — `feat(api): serve the dashboards at /app/ and guide agents through reporting`

---

### Task 17: `twillingate reporting dev`

**Files:**
- Create: `internal/reporting/dev.go`, `internal/reporting/dev_test.go`, `internal/app/reporting_dev.go`, `cmd/twillingate/reporting.go`
- Modify: `cmd/twillingate/main.go` (usage text), `cmd/twillingate/commands_test.go`, `docs/deployment.md` (usage), `docs/reporting.md` (a "Local development" section)

**Interfaces:**
- Produces: `func DevHandler(dirs []string, db *readsql.DB) http.Handler` (package reporting) and `func ReportingDev(ctx context.Context, dirs []string, dbPath, addr string, logger *slog.Logger) error` (package app).

Behaviour (D44):
- **Command:** `twillingate reporting dev <dir>… [-db <path>] [-addr 127.0.0.1:3100]`. `-db` defaults to `DATABASE_DSN`'s path; `-addr` must be loopback (refuse otherwise).
- **Directories:** each arg is a dashboard directory (has `dashboard.json`) or a parent of several. Dashboards without an `id` get 1001, 1002, … in argument order.
- **Routes** answer with the same JSON types as the API, without auth:
  - `GET /api/dashboards` → `Dashboards` with `Dev: true` and an `errors` array of `{dir, message}` for directories that fail to load;
  - `GET /api/dashboards/{id}` → `DashboardDetail`;
  - `GET /api/widgets/{id}/data` → `WidgetData`, loaded fresh each time, with no cache;
  - `GET /api/components`;
  - `PUT /api/dashboards/{id}/view` → 200 no-op;
  - `GET /api/dev/version` → `{"version": "<sha256 of every file under the dirs>"}`;
  - `/app/` → `UI()`.
- **Fresh reads:** files are re-read on every request. The UI polls `/api/dev/version` twice a second when `dev` is true and reloads on change.
- **Refusals:** a validation failure answers 400 `{error:{code:"invalid",message}}`, the API's shape, which the card shows. Widget ids are `dashboardID*1000 + index`.

- [ ] **Step 1: Write the failing tests** (`dev_test.go` with `httptest` and a temp dir):
  - the list shows the dashboard;
  - editing a `.sql` file changes `/api/dev/version` and the next data call reflects the edit;
  - a broken `dashboard.json` shows in `errors` while the others load;
  - an invalid widget answers 400 with the reason.

  `commands_test.go`: a non-loopback `-addr` exits 2 with a message.
- [ ] **Step 2: Run to verify failure.**
- [ ] **Step 3: Implement.** Reuse `LoadDashboard`, `ParseManifest(Manifest())` and `newSources(db, true)`.
- [ ] **Step 4: Verify** — `make check` → PASS.
- [ ] **Step 5: Commit** — `feat(cmd): preview dashboard files against a database with twillingate reporting dev`

---

## Web track

Tasks 10–12 touch only `web/`, `internal/reporting/ui/` and `.github/workflows/ci.yml`. They may run while Tasks 1–9 run. Tasks 18–20 need Tasks 15–17.

### Task 10: `web/` scaffold, build into `internal/reporting/ui/`, CI

**Files:**
- Create:
  - `web/package.json`, `web/tsconfig*.json`, `web/vite.config.ts`, `web/index.html`, `web/components.json` (shadcn config), `web/src/main.tsx`, `web/src/App.tsx`, `web/src/index.css`;
  - `web/src/components/ui/*` (from `npx shadcn@latest add --all`), `web/src/lib/utils.ts`;
  - `web/public/manifest.webmanifest`, `web/public/sw.js`, `web/public/icon.svg`, `web/public/icon-192.png`, `web/public/icon-512.png` (from `docs/logo.svg`);
  - `web/scripts/manifest.ts` (writes `components.json`; Task 11 fills the registry);
  - `web/src/components/widgets/index.ts` (empty registry), `web/src/components/widgets/types.ts`;
  - `web/vitest.config.ts` (or `test` in vite config) and `web/src/test/setup.ts`;
  - `web/.gitignore` (`node_modules`, `test-results`, `playwright-report`);
  - `internal/reporting/ui/` (build output, committed).
- Modify: `.github/workflows/ci.yml` (a `web` job), `.gitignore` if needed.

Setup:
1. `npm create vite@latest web -- --template react-ts`, then trim to the files above.
2. `npm i tailwindcss @tailwindcss/vite`, then `npx shadcn@latest init` (style new-york, base neutral, CSS variables) and `npx shadcn@latest add --all`.
3. `npm i recharts react-router @tanstack/react-query react-markdown d3-geo topojson-client world-atlas`.
4. `npm i -D vitest jsdom @testing-library/react @testing-library/jest-dom @testing-library/user-event tsx @types/d3-geo @types/topojson-client @playwright/test`.

Settings:
- `vite.config.ts`:
  - `base: "/app/"`;
  - `build: { outDir: "../internal/reporting/ui", emptyOutDir: true, assetsDir: "assets" }`;
  - `resolve.alias["@"] = "./src"`;
  - `server.proxy = { "/api": "http://127.0.0.1:3100" }`;
  - the Tailwind plugin.
- Scripts:
  - `"dev": "vite"`, `"typecheck": "tsc -b --noEmit"`, `"test": "vitest run"`;
  - `"build": "tsc -b && vite build && tsx scripts/manifest.ts"`;
  - `"e2e": "playwright test"`.
- `manifest.webmanifest`: `name` "twillingate", `short_name` "twillingate", `start_url` "/app/", `scope` "/app/", `display` "standalone", the icons, `theme_color`/`background_color` from the neutral palette.
- `sw.js` (hand-written, short):
  - **install:** cache `/app/` and `/app/index.html`.
  - **fetch:** navigations under `/app/` go network-first and fall back to the cached `index.html`. `/app/assets/*` go cache-first and cache on fetch. Everything else, `/api/` included, is never intercepted.
  - **activate:** delete old cache versions (`const CACHE = "app-v1"`).
- `App.tsx` placeholder renders "twillingate" in a shadcn `Card` so the build has content.
- CI `web` job mirrors the `sdk` job: `npm ci`, `npm run typecheck`, `npm test`, `npm run build`, then `git diff --exit-code internal/reporting/ui`. Workflow file changes must be pushed over SSH (the gh token lacks workflow scope); the controller handles pushing.

- [ ] **Step 1:** Scaffold as above. `npm run build` writes `internal/reporting/ui/index.html`, `assets/…`, `manifest.webmanifest`, `sw.js`, icons and `components.json` (`{"components": []}` for now).
- [ ] **Step 2:** One Vitest smoke test (`App.test.tsx`: it renders). `npm run typecheck && npm test && npm run build` → PASS.
- [ ] **Step 3:** Run the build twice and check `git status`: no diff the second time (deterministic output). If not deterministic, fix it: no timestamps in output.
- [ ] **Step 4: Commit** — `build(web): scaffold the dashboards app and build it into internal/reporting/ui`

---

### Task 11: Widget contract, formats and the Recharts components

**Files:**
- Create:
  - `web/src/components/widgets/{stat,line,area,bar,bar_list,pie,radar,radial,scatter,combo}.tsx`;
  - `web/src/lib/format.ts`, `web/src/lib/records.ts`;
  - tests `web/src/components/widgets/*.test.tsx`, `web/src/lib/format.test.ts`.
- Modify: `web/src/components/widgets/index.ts`, `types.ts`, `web/scripts/manifest.ts`.

**Interfaces:**
- Produces:

```ts
// types.ts
export type ColumnType = "number" | "text" | "day";
export interface InputColumn { name: string; types: ColumnType[]; optional?: boolean }
export interface Contract {
  description: string;          // when to use it and its limits (shown to agents)
  accepts: ("sql" | "md")[];
  inputs: { open: boolean; columns: InputColumn[] };
  props: Record<string, unknown>; // JSON schema, additionalProperties false
  defaultWidth: number;           // 1–12
  defaultHeight: number;          // 1–12
}
export interface SqlData { columns: string[]; rows: string[][]; truncated: boolean }
export interface MarkdownData { markdown: string }
export interface WidgetProps<P = Record<string, unknown>> { data: SqlData | MarkdownData; props: P }
export interface WidgetModule { contract: Contract; default: (p: WidgetProps<any>) => JSX.Element }

// index.ts
export const widgets: Record<string, WidgetModule> // key = file name = component name

// format.ts
export type Format = "number" | "percent" | "duration";
export function formatValue(v: number | null, f: Format = "number"): string
// number: Intl compact above 10,000 ("12.3K"), grouped below; percent: fraction → "12.3%"; duration: seconds → "45s", "3m 20s", "2h 5m"; null → "–"

// records.ts
export function toRecords(d: SqlData, c: Contract): Record<string, string | number | null>[]
// number columns parsed (""→null); others kept as strings
export function pivot(records, x: string, series: string, y: string): { rows: Record<string, unknown>[]; keys: string[] }
```

- Each widget file exports `contract` and a default component. `scripts/manifest.ts` maps `widgets` to `{"components":[{name, description, accepts, inputs, props, default_width, default_height}]}` sorted by name, and writes `../internal/reporting/ui/components.json` (2-space JSON, trailing newline).
- **Contracts** follow the D13 table exactly: inputs, props and defaults. Every prop schema allows only its listed props. `format` is `{"enum":["number","percent","duration"]}`, and `curve` is `{"enum":["linear","monotone","step"]}`.
- **Descriptions** say when to use the component and its limits, e.g. pie: "Parts of a whole, up to ~7 slices; group the rest as 'Other' in SQL."
- **Charts** use shadcn's `ChartContainer`, `ChartTooltip` and `ChartLegend` (`@/components/ui/chart`), with colors `var(--chart-1…5)` cycling. Axis ticks use `interval="preserveStartEnd"` and `minTickGap` so narrow widths thin them.
- **`stat`:**
  - Without `x`: `value` of the first row, and when `previous` is present a delta badge (`+12.3%`, with the arrow and color by sign).
  - With `x`: rows are a series, drawn as a sparkline under the number, and the number is the `aggregate` of `value` (`sum` default, `last`, `avg`).
- **Tests** (one file per widget): it renders from sample rows (an `svg` or the number appears); `rows: []` renders nothing broken (the card handles "No data", Task 19); each prop changes output (e.g. `stacked`, `horizontal`, `donut`, `curve`); a missing optional input renders the simpler form (`series` absent → one line). Recharts needs a size in jsdom: wrap in a fixed-size container and mock `ResizeObserver` in `setup.ts`.

- [ ] **Step 1:** `format.test.ts` and `records` tests, then the implementations.
- [ ] **Step 2:** Per widget: test, then implement, then run.
- [ ] **Step 3:** `npm run typecheck && npm test && npm run build`. `components.json` lists the ten.
- [ ] **Step 4: Commit** — `feat(web): add chart components with their contracts`

---

### Task 12: The custom components

**Files:**
- Create:
  - `web/src/components/widgets/{funnel,heatmap,calendar,map,treemap,table,markdown}.tsx` and tests;
  - `web/src/lib/iso-countries.ts` (ISO alpha-2 → numeric for the world-atlas ids; generate the table once from a public ISO 3166 list and commit it; ~250 lines of data).
- Modify: `web/src/components/widgets/index.ts`.

Behaviour (inputs, props and defaults per D13):
- **`funnel`:** Recharts `FunnelChart`, steps in query order, with each step's share of the first as a label.
- **`heatmap`:** a CSS grid.
  - `x` columns and `y` rows in first-seen order; a cell's shade is `value` scaled from min to max, using `color-mix` on `var(--chart-1)`.
  - `labels: true` prints values; the tooltip is `title`.
  - A retention cohort grid is the canonical use.
- **`calendar`:** the last 53 weeks ending at the max `day`, as columns of 7 cells (Mon–Sun) shaded by `value`, like GitHub's graph, with month labels.
- **`map`:** `d3-geo` `geoEqualEarth` + `topojson-client` `feature(world, world.objects.countries)` from `world-atlas/countries-110m.json`.
  - Countries are joined by numeric id via `iso-countries.ts` and shaded by value.
  - Codes that match no shape are listed under the map ("Not on the map: XK 12, …").
- **`treemap`:** Recharts `Treemap`. With `parent`, it nests two levels.
- **`table`:** shadcn `Table` with every column in query order.
  - Numbers are right-aligned, and `formats` (column → format) applies.
  - `colorscale` columns get a background shade by value.
  - The table scrolls inside the card (`overflow-auto`).
- **`markdown`:** `react-markdown` without `rehype-raw`, so raw HTML is never rendered. Style with Tailwind `prose`-like classes (no typography plugin: a few element styles in `index.css`).

Tests:
- one per component, as in Task 11;
- `map`: every alpha-2 code in `iso-countries.ts` resolves to a feature id present in `countries-110m` (except a documented short list of territories absent at 110m), and an unknown code (`ZZ`) is listed;
- `markdown`: `<script>` in the text renders as text, never as an element.

- [ ] **Step 1:** Per component: test, then implement.
- [ ] **Step 2:** `npm run typecheck && npm test && npm run build`. `components.json` lists all 17.
- [ ] **Step 3: Commit** — `feat(web): add table, markdown, map and the other custom components`

---

### Task 18: Auth, API client, ranges and routing

**Files:**
- Create:
  - `web/src/lib/auth.ts`, `web/src/lib/api.ts`, `web/src/lib/ranges.ts`, `web/src/lib/selection.ts`;
  - `web/src/pages/Login.tsx`, `web/src/pages/Callback.tsx`;
  - tests `web/src/lib/*.test.ts`.
- Modify: `web/src/App.tsx` (router), `web/src/main.tsx` (QueryClient; register `sw.js` in production).

**Interfaces:**
- Produces:

```ts
// ranges.ts
export type Preset = "today" | "yesterday" | "7d" | "30d" | "90d" | "custom";
export const PRESETS: { id: Preset; label: string }[]; // Today, Yesterday, Last week, Last month, Last 90 days, Custom…
export function todayIn(tz: string, now?: Date): string;    // YYYY-MM-DD via Intl.DateTimeFormat("en-CA", {timeZone: tz})
export function resolve(p: Preset, tz: string, now?: Date, custom?: { from: string; to: string }): { from: string; to: string };

// selection.ts: what the page shows, from URL, stored selection and fallbacks (D34)
export interface Selection { projectId?: number; range?: Preset; from?: string; to?: string }
export function chooseSelection(
  url: URLSearchParams,
  stored: { project_id?: number; range?: string; from?: string; to?: string },
  activeProjects: number[],
  switchers: { project: boolean; range: boolean },
): Selection

// api.ts
export async function api<T>(path: string, init?: RequestInit): Promise<T>; // adds Bearer, refreshes once on 401, throws ApiError{status, code, message}
export const endpoints = { dashboards, dashboard(id), widgetData(id, q), components, saveView(id, sel), projects, devVersion };

// auth.ts
export type AuthState = { kind: "none" } | { kind: "token" } | { kind: "oauth" };
export async function detectAuth(): Promise<"open" | "login" | "paste">;
export async function beginLogin(returnTo: string): Promise<void>; // register if needed, PKCE S256, redirect
export async function completeLogin(search: string): Promise<string>; // exchange code → return path
export function setPastedToken(t: string): void;
```

Behaviour:
- **Auth (D41):**
  1. `detectAuth` calls `GET /api/dashboards` without a token. A 200 is `open` (reporting dev).
  2. Otherwise it fetches `/.well-known/oauth-protected-resource`. A 404 is `paste` (bare `token://`).
  3. Else it reads `authorization_servers[0]` + `/.well-known/oauth-authorization-server`, which is `login`.
  4. With no `registration_endpoint` it falls back to `paste`, with a note naming the provider.
- **Client registration:** client id in `localStorage["twillingate.client_id"]`; the registration posts `redirect_uris: [location.origin + "/app/callback"]`, `token_endpoint_auth_method: "none"`, `grant_types: ["authorization_code","refresh_token"]`.
- **PKCE:** verifier and state in `sessionStorage`. `resource` is the protected-resource document's `resource`, passed to authorize and token.
- **Tokens:**
  - the access token stays in memory; the refresh token goes in `localStorage["twillingate.refresh_token"]`;
  - a pasted token goes in `localStorage["twillingate.token"]`;
  - a 401 refreshes once, then routes to `/app/login`.
- **Routes** (react-router, `basename="/app"`):
  - `/` redirects to the last dashboard opened on this device (`localStorage["twillingate.last_dashboard"]`), else the first system dashboard;
  - `/dashboards/:id`, `/callback`, `/login`.
- **Ranges:** `resolve` computes in the instance timezone (`timezone` from `list_dashboards`, "UTC" today):
  - `7d` → today−6..today, `30d` → today−29..today, `90d` → today−89..today;
  - `yesterday` → the day before, both ends.

  Date arithmetic is done on UTC `Date`s built from the `YYYY-MM-DD` strings.
- **Selection:**
  1. The URL (`project`, `range`, and `from`/`to` for custom) wins.
  2. Then the stored selection, if the stored project is still in `activeProjects`.
  3. Then the first active project and `7d`.

  A part is omitted when the dashboard has no such switcher.

Tests:
- `ranges.test.ts`:
  - every preset for a fixed now;
  - "resolves in the instance timezone": `now = 2026-09-26T07:30:00Z` (23:30 on the 25th in UTC-8) gives `today` = `2026-09-26`;
  - month and year boundaries.
- `selection.test.ts`:
  - URL wins;
  - stored applies;
  - "stale stored project": a stored project not in `activeProjects` falls back to the first active one;
  - no switchers → empty selection;
  - custom needs from/to.
- `auth.test.ts` with mocked `fetch`: each `detectAuth` outcome; `beginLogin` builds an authorize URL with `code_challenge_method=S256`, `resource` and `redirect_uri=…/app/callback`; `completeLogin` rejects a mismatched state.
- `api.test.ts`: a 401 then refresh then retry succeeds; a second 401 throws `ApiError` with status 401.

- [ ] **Step 1:** Tests, then implement each module.
- [ ] **Step 2:** `npm run typecheck && npm test && npm run build` → PASS.
- [ ] **Step 3: Commit** — `feat(web): log in with the API's OAuth server and resolve ranges in the instance timezone`

---

### Task 19: Dashboard pages: shells, header, grid, cards, refresh

**Files:**
- Create:
  - `web/src/components/AppSidebar.tsx`, `ReportTabs.tsx`, `DashboardHeader.tsx`, `ProjectSwitcher.tsx`, `RangeSwitcher.tsx`, `WidgetGrid.tsx`, `WidgetCard.tsx`;
  - `web/src/pages/Dashboard.tsx`;
  - `web/src/lib/grid.ts`;
  - tests: `grid.test.ts`, `WidgetCard.test.tsx`, `Dashboard.test.tsx`.
- Modify: `web/src/App.tsx`, `web/src/index.css`.

**Interfaces:**
- `grid.ts`: `export function span(width: number, gridPx: number): number` implements D37:
  - ≥1024 → width;
  - 640–1023 → width ≤ 6 ? 6 : 12;
  - <640 → width ≤ 3 ? 6 : 12.

  The grid measures its own width with `ResizeObserver`. Container queries through Tailwind `@container` are equivalent; pick one and use it everywhere. `ResizeObserver` keeps `span` unit-testable.

Behaviour:
- **Sidebar** (shadcn `Sidebar`): a **Reports** entry (opens the first system dashboard) and **Yours** (user dashboards by sort key, archived hidden). It collapses to icons at 640–1023 and becomes a sheet below 640 (the shadcn sidebar's own behaviour).
- **Shells by `owner`:**
  - System dashboards render `ReportTabs` (Views · Product · Users · Groups · Retention, sort-key order) above one header. Tabs are a row at ≥1024, scroll sideways at 640–1023, and a `Select` below 640.
  - Moving between tabs carries `project` and `range` (and `from`/`to`) in the URL and saves them to the new tab's view route (D35).
  - User dashboards render a header of their own.
- **Header:**
  - the title;
  - `ProjectSwitcher` when `follows_project` (active projects; archived ones in a collapsed group);
  - `RangeSwitcher` when `follows_range` (presets; "Custom…" opens a range `Calendar` in a `Popover` on desktop and a `Sheet` below 640);
  - "data as of" (the oldest `cached_at` on screen, in browser-local time) and a refresh button.

  Changing a switcher updates the URL and calls `PUT …/view`.
- **Grid:** `display:grid; grid-template-columns: repeat(12, minmax(0,1fr)); grid-auto-rows: 40px; gap: 12px`. Each card spans `span(width)` columns and `height` rows. There is no `dense`.
- **Card states** (D38), each with its own TanStack query keyed `["widget", id, projectId, from, to]`:
  - a skeleton at the card's height;
  - the component;
  - "No data for this range" when `rows` is empty;
  - "Component removed" when `removed`;
  - "Query no longer runs" with the error message folded in a `Collapsible`, when the API answers 400;
  - "Couldn't load" with a Retry button for network/5xx;
  - a "partial: narrow the range or group the query" badge when `truncated`.
- **Refresh** (D39):
  - `sql` cards have a refresh icon, visible on hover (always on touch: `@media (hover: none)`), which requests `fresh=true`. It is disabled until `refresh_after`, and its tooltip shows the data's age and the wait.
  - The header button refreshes every card past its `refresh_after`.
  - Markdown cards have none.
- **Empty states:** with no active projects, a dashboard that follows the project shows "Create a project first" (with the MCP/CLI hint) and makes no `widget_data` calls. A dashboard with no widgets shows "No widgets yet — ask your agent to add some".
- **Dev mode:** when `list_dashboards` returns `dev: true`, poll `/api/dev/version` every 500ms and `location.reload()` on change. A banner lists `errors`.
- **Light and dark** follow the system (`prefers-color-scheme`, shadcn's `.dark` class set from `matchMedia`).
- **Offline:** when `navigator.onLine` is false, a banner says "Offline — showing nothing until the connection is back".

Tests (mock `api`):
- `grid.test.ts`: the D37 table at 1200, 800 and 400px.
- `WidgetCard.test.tsx`: each state above renders its text; the refresh icon is disabled before `refresh_after` and enabled after; markdown has none.
- `Dashboard.test.tsx`:
  - a system dashboard renders tabs and both switchers;
  - a user dashboard with only fixed widgets renders no switchers;
  - switching the range calls `saveView` and updates the URL;
  - moving tabs carries the selection;
  - "no projects": zero active projects shows the "Create a project first" state and `widgetData` is never called.

- [ ] **Step 1:** `grid.ts` test, then the implementation.
- [ ] **Step 2:** `WidgetCard`, then the grid, then the header and switchers, then the shells, then `Dashboard`: test first each time.
- [ ] **Step 3:** Manual check against `twillingate reporting dev internal/reporting/system -db local/twillingate.db` (after `make seed-demo`) with `npm run dev`: open each report at 1280px and 390px wide. Record anything off in the report.
- [ ] **Step 4:** `npm run typecheck && npm test && npm run build` → PASS; `make check` → PASS (the embedded UI changed).
- [ ] **Step 5: Commit** — `feat(web): render dashboards in report tabs and standalone pages`

---

### Task 20: Browser test

**Files:**
- Create: `web/playwright.config.ts`, `web/e2e/app.spec.ts`, `web/e2e/serve.sh`
- Modify: `.github/workflows/ci.yml` (an `e2e` job: Go, Node and Python; `make build`, `npm ci`, `npx playwright install --with-deps chromium` in CI only, `npm run e2e`)

`serve.sh`:
1. Build the binary into a temp dir.
2. Write an env file with `DATABASE_DSN=sqlite://<tmp>/e2e.db`, `GEO_DSN=none://`, `INGEST_ADDR=127.0.0.1:18080`, `API_AUTH_DSN=token://e2e-token?password=e2e-pass`, `PUBLIC_URL=http://127.0.0.1:18080`.
3. `twillingate migrate`.
4. Create project `dev` and seed it with `python3 scripts/seed-demo.py <db>`.
5. `exec twillingate serve`.

Playwright `webServer` runs it and waits for `/healthz`.

`app.spec.ts`:
- **Login.** Go to `/app/`, get redirected to the password page, fill `e2e-pass`, and land on the Views report.
- **Every system dashboard renders** at 1280×800 and 390×844: open each and wait for network idle. Assert no card shows "Query no longer runs", "Couldn't load" or "Component removed", and at least one chart `svg` exists.
- **Tabs carry the selection.** On Views, pick `30d`, click Product, and the URL has `range=30d` and the range switcher shows "Last month".
- **Standalone shell.** A user dashboard, created through `POST /api/dashboards` with the token header before the test, opens in the standalone shell: no report tabs.
- **Layout.** A 6 × 6 widget followed by four 3 × 3 widgets (create the dashboard via the API with markdown widgets) renders the four as a 2 × 2 block beside the first. Assert with bounding boxes: all four have `x` ≥ the first's right edge and fit within its vertical extent.

- [ ] **Step 1:** Write the config, script and spec. Run `cd web && npm run e2e` locally (Chromium at `/opt/pw-browsers` or the Playwright default; do not download if one is present) → PASS.
- [ ] **Step 2: Commit** — `test(web): log in and open every system dashboard in a browser`

---

## Final: whole-branch checks and PR

- [ ] **Step 1: Check the docs against the spec's list:**
  - `docs/reporting.md` (all sections);
  - `docs/twillingate.md` (pointer, purge, tool count, query refusals);
  - `docs/deployment.md` (`/app/`, the oauth redirect, three settings, API_QUERY_* bound widgets, `reporting dev`);
  - `deploy/UPGRADES.md` (021: purge instruction, `/app/`, removed components, rollback);
  - `CLAUDE.md`.
- [ ] **Step 2:** `make check`, `cd web && npm run typecheck && npm test && npm run build && git diff --exit-code ../internal/reporting/ui`, `npm run e2e` → all PASS.
- [ ] **Step 3: Whole-branch review.** A fresh reviewer checks the branch against the spec and this plan. Fix its findings.
- [ ] **Step 4: Push and open the draft PR**, titled `feat: serve read-only dashboards at /app/ that agents build over MCP`. The body leads with the problems (spec §Problem), then the design in brief, the deviations above, the timing table from Task 14, the upgrade notes, and the rollout (release → parity check → PR2). Close spec PR #60 in favour of this one, as with earlier specs.
