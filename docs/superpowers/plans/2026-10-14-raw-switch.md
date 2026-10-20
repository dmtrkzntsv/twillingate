# The Switch Implementation Plan (PR 5d)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Widgets, system dashboards and the analytics tools run on the event files through the engine, not on SQLite. Widget SQL becomes DuckDB SQL over `events`, `views`, `product`, `measures`, `sessions`, `identities` and `aliases`, and every system dashboard shows the numbers the SQLite path showed for the same range.

**Architecture:**
- **The engine takes arguments.** A request carries named string arguments, which the child binds as DuckDB `$name` parameters. The child also defines the `tw_cell` and `tw_num` macros and runs in UTC.
- **`internal/shared/enginesql`** is the DuckDB counterpart of `readsql`'s text side:
  - it finds `:project`, `:from` and `:to` in widget SQL and rewrites each to a typed bound parameter;
  - it builds the wraps: rows as text, the LIMIT 0 shape check, and a remote table's page (filters, sort, counts) over `tw_cell`/`tw_num`.
- **The reader scopes a query to one project.** `archive.Reader.QueryScoped` prefixes `WITH events AS NOT MATERIALIZED (… WHERE project_id = …)`, and the same for the other six tables. A widget then sees only its project, and DuckDB prunes every other project's files by their statistics. `measures` gains a computed `bucket` column.
- **reporting's sql source takes an interface** (`reporting.Files`), not `*readsql.DB`. `internal/app` implements it over the reader, the registry's rules, `CONSOLE_QUERY_TIMEOUT` and `CONSOLE_QUERY_ROWS_MAX`.
- **The 59 system widgets are rewritten** in DuckDB SQL over the files. A parity test runs the old SQLite text against SQLite and the new text against the files, both seeded with the same events, and compares the rows.
- **The tools move too.** `views_overview`, `views_breakdown`, `product_events`, `product_attributes`, `measures`, `retention` and `identities` run on the engine with their output unchanged. `query` defaults to `files`, and ingest stops calling `UpsertIdentities`.
- **Users' widgets that fail stay.** They show their error with a hint, the console logs each one at start, and `deploy/UPGRADES.md` says how to find them.

**Tech Stack:** Go 1.25, DuckDB v1.5.6 through `internal/engine` (the child process), the PR 3–5c archive (`internal/archive`), React/TypeScript for the editor (`web/`), modernc SQLite only for metadata, the deprecated `query` source and the parity tests.

**Spec:** `docs/superpowers/specs/2026-10-12-raw-first-design.md`:
- D8: dashboards on raw, through the child;
- D16–D18: sessions, identities, aliases;
- D21: `query`'s bounds;
- Q11: widget SQL sees the files only.

Roadmap: `docs/superpowers/plans/2026-10-12-raw-first-roadmap.md`, PR 5d. It is stacked on PR 5c (#183, `feat/raw-migrate`), which is stacked on 5a (#182):
- the switch needs the import, so the files hold what SQLite held;
- 5b (#184) is a sibling that touches forms, the SDK and the attribution lookup, not widgets. Whichever of 5b and 5d lands second rebases.

## Facts this plan rests on (probed 2026-10-14, DuckDB v1.5.6, duckdb-go v2.10506.0)

- **Bound parameters prune files.** `PREPARE p AS SELECT … FROM read_parquet(f) WHERE project_id = $1 AND day BETWEEN $2 AND $3` shows `Filters: project_id=1, day>=…` in the parquet scan of `EXPLAIN ANALYZE EXECUTE p(…)`. Binding loses no pushdown against literals.
- **A CTE shadows a view, inside subqueries and the user's own CTEs too.** `WITH events AS NOT MATERIALIZED (SELECT … FROM main.events WHERE …) SELECT * FROM (WITH x AS (SELECT * FROM events) SELECT * FROM x)` reads the CTE. `main.events` names the view past the CTE.
- **`SELECT CAST(COLUMNS(*) AS VARCHAR) FROM (q)` keeps q's column names and order,** and its rows keep q's `ORDER BY`: checked on 3M rows at 4 threads.
- **DuckDB's text of a value** (`CAST(x AS VARCHAR)`):

  | Value | Text |
  | --- | --- |
  | DOUBLE 1000000.0 | `1000000.0` |
  | 0.1 + 0.2 | `0.30000000000000004` |
  | 1e21 | `1e+21` |
  | DATE | `2026-01-01` |
  | TIMESTAMP | `2026-01-01 10:00:00` |
  | DECIMAL(5,2) 3.50 | `3.50` |
  | BOOLEAN | `true` |
  | LIST | `[1, 2]` |
  | NULL | NULL |

- **`5/2` is 2.5 (DOUBLE); `5//2` is 2.** A literal `1.0` is a DECIMAL.
- **Macros work:**
  - `CREATE MACRO tw_cell(x) AS coalesce(CAST(x AS VARCHAR), '')`;
  - `tw_num(x)` returns NULL for text, for a date and for `1e400`, and 12 for `'12'`.
- **The child sets no TimeZone.** With ICU compiled in, `current_date` and `now()::DATE` follow the machine's zone. Task 1 sets `TimeZone = 'UTC'`.
- **The child's `readOnly`** lets exactly one SELECT through (`json_serialize_sql`). `$name` parameters parse.
- **`readsql.Check` misreads DuckDB SQL.** `x::DATE` gives a parameter `:DATE`, `x::DECIMAL(10,2)` is refused as a `:(...)` variable, and `[` is skipped as a bracketed SQLite name. So the widget path needs its own scanner (Task 2).
- **Every system `.sql` uses `:project`, `:from` and `:to`,** and filters `day BETWEEN :from AND :to` (`cohort_day` in retention).
  - No widget uses `json_extract`, `GROUP_CONCAT` or `printf`.
  - Seven use `date()`/`julianday` for the previous window or the retention offsets.
  - Three use `strftime('%s', ts)`.
  - Four use `date('now', '-3 hours')`.
- **What the web app reads of widget data:**
  - `rows` are strings, NULL as `""`, never a JSON null.
  - A `number` column goes through `Number(raw)`.
  - A day is matched by `/^\d{4}-\d{2}-\d{2}$/`.
  - `checkRows`' `number` is `ParseFloat` with NaN and the infinities refused.
  - The browser repeats the table filters in `web/src/lib/table-view.ts`, pinned with readsql by `internal/reporting/testdata/table-filters.json`.
- **The app wiring:**
  - `api.Build` opens two readsql handles itself. `db` serves widgets, `query` and the limits, `subs` serves forms; `subs` stays.
  - `app.Serve` runs `Migrate` (which validates every system widget) before it builds the engine (`app.go:88` against `:170`).
  - `reporting.Migrate`, `reporting.DevHandler` and `reporting.New` each take a `*readsql.DB`.
- **Seeding writes SQLite only.** `web/e2e/serve.sh` and the test-instance skill run `scripts/seed-demo.py`, which INSERTs into SQLite `events` and `identities`. `twillingate events import` (5c) is the path into the files.

## Global Constraints

- **SQLite keeps being written.** No migration in this PR.
  - Events, the `agg_*` rollups, `actors`, `agg_retention` and the nightly passes all keep running until PR 6, so a downgrade to 5c still has its data.
  - Only `UpsertIdentities` stops: nothing reads `identities` after this PR but 5c's import, and an import runs once.
- **One dialect.** Widget SQL is DuckDB SQL. No SQLite fallback, no dual path, no translation (decided 2026-10-13).
- **Parameters are bound, never spliced.**
  - `:project`, `:from` and `:to` become `CAST($tw_project AS BIGINT)`, `CAST($tw_from AS DATE)` and `CAST($tw_to AS DATE)`.
  - A remote table's filter values become `$tw_pN`.
  - Widget SQL may not use `?`, `$n` or `$name`.
- **`internal/archive` stays a leaf.** `internal/shared/enginesql` is a leaf too (rank 0; it may import `engine` and `readsql`, which are leaves). New test helpers live in `internal/archive/archivetest` (rank 0); add it to `internal/archtest`'s rank table.
- **Outputs keep their shape.**
  - Every tool keeps its input and output fields.
  - Widget data keeps `{columns, rows, truncated}` with string cells, and a page keeps `matched` and `total`.
  - System widgets keep their titles, columns and props.
- **The tables' columns are a contract** (PR 4). `measures` gains `bucket` and nothing else changes. Docs change in the same commit as the code (CLAUDE.md, Documentation):
  - `docs/reporting.md`: the examples, the component table's examples, the tables;
  - `docs/twillingate.md`: tools, `query`, `schema://views`;
  - `deploy/UPGRADES.md`.
- **Commits and checks.**
  - Conventional Commits with CLAUDE.md's scopes. The PR title is `feat!: run dashboards and the analytics tools on the event files`.
  - Run `make check` before every push. Run the e2e suite (`cd web && npm run e2e`) before the last push.
- **Toolchain.** Go is at `/usr/local/go/bin`, not on `PATH`. Set `GOTMPDIR=$HOME/.cache/gotmp`, because `/tmp`'s quota fills. Every command below assumes:
  `export PATH=$PATH:/usr/local/go/bin GOTMPDIR=$HOME/.cache/gotmp`.

## Decisions this plan takes (review them)

1. **Widget SQL keeps `:project`, `:from` and `:to`.** DuckDB's own `$name` would need no rewrite, but `:name` keeps the editor's completions, the follow detection (`widget-draft.ts`) and every reader's habit. The scanner rewrites each to a bound parameter cast to its type:
   - `:project` to BIGINT;
   - `:from` and `:to` to DATE, so `day BETWEEN :from AND :to` compares dates and `:to - :from + 1` is a number of days.

   A `:name` the scanner cannot tell from a slice bound (`list[a:b]`) is refused like any other parameter; the docs say to write `list[a : b]`.
2. **A widget reads only its project.** Each scoped query is prefixed with seven CTEs, `events` … `aliases`, each the reader's view filtered on `project_id`.
   - A widget that does not use `:project` sees every project, as on SQLite.
   - The range is not scoped by file list: the Views stats read the previous period too. The SQL's own `day` condition prunes by statistics.

   The roadmap said "for its range only". This gives the isolation and the pruning without the previous-period problem.
3. **Cells are DuckDB's own text.** The widget path wraps the query as `SELECT CAST(COLUMNS(*) AS VARCHAR) FROM (…)`, and `tw_cell` is that same cast, so what the table filters compare is exactly what the cells show, by construction.
   - A DOUBLE reads `2.0` where SQLite read `2`; `Number()` and `checkRows` read both.
   - A TIMESTAMP reads `2026-01-01 10:00:00`; system widgets output dates or numbers only.
   - `query` keeps its typed rendering (`cellText`).
4. **The numbers stay what they were** (the roadmap's "done when"):
   - **visitors** over a range is the sum of each day's distinct actors, as `v_views_*`;
   - **users and groups** per day as `v_identity_daily`;
   - **sessions** from the `sessions` table (5a).

   Exact range-distinct counts, now cheap, are a later, visible change. Where the SQLite path folded values past a cap into `(other)`, the files show every value. The caps go in PR 6, so there is no fold to repeat: the parity test seeds below the caps.
5. **Every attribute key breaks down, declared or not** (D4). `attribute-values` unpivots every `attr_*` column. The catalog's `breakdown` flag stops mattering to dashboards; PR 6 removes it.
6. **Retention is computed on read, through yesterday.**
   - **Cohorts:** an actor whose `actor_kind` is `user` or `install` belongs to the cohort of its first day of views or product events in the files. It is `user` if any of its events say so, the stickiness `actors` has.
   - **Activity:** a day counts when it is before today (UTC): complete days, as the daily pass counted.
   - **Milestones:** "reached" is `cohort_day + offset < current_date`.
   - The `retention` tool's `aggregated_through` is the last complete day with activity.
7. **`first_seen` comes from `identities`** (5a). "New users" is new to the files' history, which since 5c's import is the raw history SQLite held. It is not `RETENTION_AGGREGATE_DAYS` of aggregates.
8. **`measures` gains `bucket`.** It is computed in the reader's view as `agg_measures_daily` computed it: `CASE WHEN value > 1e-17 THEN CAST(ceil(ln(value) / ln(1.04)) AS INTEGER) ELSE -1000 END`. The percentile convention in `docs/reporting.md` keeps working, and nobody has to copy the formula.
9. **Validation runs on the engine,** at save and at `Migrate`.
   - `Migrate` runs after the engine and the reader exist, so `app.Serve` builds them first.
   - `twillingate migrate` spawns an engine too.
   - Sample rows come from the live project with the newest file name (`archive.LatestDays`), not from SQLite.
10. **Users' widgets.**
    - **On load,** a widget whose SQL fails returns its error, and the release-notes pointer is added as today. When the error names a SQLite table (`v_*`, `agg_*`, `raw_*`, `identities` columns that changed) or function (`strftime`, `julianday`, `json_extract`, `date(…, '…')`), it adds: "widget SQL is DuckDB SQL over events, views, product, measures, sessions, identities and aliases; see docs://reporting".
    - **At start,** the console checks every live user sql widget with the LIMIT 0 shape query in the background, and logs each failure with its id, dashboard and error.
    - **Nothing is rewritten or archived.**
11. **`query` defaults to `files`.**
    - `source: "sqlite"` keeps working until PR 6, marked deprecated in its description and in `docs/twillingate.md`.
    - `schema://views` describes the files' tables first and keeps the SQLite views, marked as the deprecated source.
    - The editor completes from the files' tables.
12. **The tools that stay on SQLite** are `usage`, `cap_usage`, `limits`, `list_attributes`, `event_names` and `project_activity`. They read metadata, counts and caps that PR 6 replaces or removes, and SQLite still holds the same events.
13. **The start warns when the import has not run.** With no `events_imported` row in `meta` and raw rows in SQLite, the console logs: dashboards show only what the files hold; run `twillingate events import` with ingest stopped.

## Review Focus

1. **Parity.** Every system widget, every preset (today, yesterday, 7d, 30d, 90d), two projects. SQLite's rows against the files' rows on the same events, numbers within 1e-9 relative. Pinned by Task 5 `TestSystemWidgetsMatchSQLite`.
2. **Scoping.** A widget without `project_id = :project` must not read a neighbour's rows. A query that names `main.events` gets past the scope; that is fine, since a console user can read every project anyway. Pinned by Task 3 `TestScopedQuerySeesOneProject`.
3. **The scanner.** Pinned by Task 2's table test:
   - `::` casts, `:=` named arguments, strings (`'…'`, `E'…'`, `$$…$$`), quoted identifiers and comments must never yield a parameter;
   - a stray `)` must not close the wrap early.
4. **The page wrap.** The shared `table-filters.json` cases pass against DuckDB as they pass against SQLite and in the browser (Task 2 and Task 4).
5. **Boot order.** `Migrate` needs the engine. A broken engine (no glibc, a bad `ENGINE_MEMORY_LIMIT`) now fails the boot of the console process, where before it failed only the first query. The ingest-only process (split install) does not migrate reporting and does not spawn.

---

### Task 1: The engine takes arguments, runs in UTC and knows tw_cell and tw_num

**Files:**
- Modify: `internal/engine/protocol.go` (`request` gains `Args map[string]string`)
- Modify: `internal/engine/engine.go` (`Query` gains variadic `args ...Arg`)
- Modify: `internal/engine/child.go` (`run` binds `sql.Named`; `open` sets `TimeZone` and creates the macros)
- Test: `internal/engine/engine_test.go`, `internal/engine/child_test.go`

**Interfaces:**
- Produces:

  ```go
  // Arg is a named argument a query binds as $Name: a string, which the
  // SQL casts to the type it needs (CAST($tw_from AS DATE)).
  type Arg struct{ Name, Value string }

  func (e *Engine) Query(ctx context.Context, sql string, maxRows int, args ...Arg) (Result, error)
  ```

  Variadic, so every caller compiles unchanged, `archive.Engine` and 5b's lookup included.
- The child defines, once per database, before `lock_configuration`:

  ```sql
  CREATE MACRO tw_cell(x) AS coalesce(CAST(x AS VARCHAR), '');
  CREATE MACRO tw_num(x) AS CASE
    WHEN regexp_full_match(tw_cell(x), '-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?')
     AND isfinite(TRY_CAST(tw_cell(x) AS DOUBLE))
    THEN TRY_CAST(tw_cell(x) AS DOUBLE) END;
  ```

  The regex is readsql's `decimal` (cell.go), anchored by `regexp_full_match`.

- [ ] **Step 1: Failing tests.** In `engine_test.go`:
  - `TestQueryBindsNamedArguments`: `SELECT CAST($a AS BIGINT) + 1 AS n, CAST($d AS DATE) AS d` with `Arg{"a", "41"}, Arg{"d", "2026-01-02"}` returns `[[42 2026-01-02]]`.
  - `TestQueryMissingArgumentIsAQueryError`: an unbound `$a` gives a `*QueryError`.
  - `TestChildRunsInUTC`: `SELECT current_setting('TimeZone')` is `UTC`, with the test process's `TZ=America/Los_Angeles` passed through `Options.Env`.
  - `TestCellMacros`: `tw_cell(NULL)`, `tw_cell(1.5::DOUBLE)`, `tw_num('12')`, `tw_num('abc')`, `tw_num('1e400')`, `tw_num(DATE '2026-01-01')`, `tw_cell([1,2])` give `"", "1.5", 12, nil, nil, nil, "[1, 2]"`.
  - A user query cannot redefine them: `CREATE MACRO` is refused by `readOnly`, as any write.
- [ ] **Step 2: Run them, expect failures.** `go test ./internal/engine -run 'Arguments|UTC|CellMacros'`.
- [ ] **Step 3: Implement.**
  - The request carries `Args`, omitted when empty.
  - `run` passes `sql.Named(k, v)` for each, in name order, to `QueryContext`. `readOnly` gets the SQL alone, as before.
  - `open` adds `SET GLOBAL TimeZone = 'UTC'` before `lock_configuration`, and the two `CREATE MACRO` statements.
- [ ] **Step 4: Run the package's tests,** then `go vet ./internal/engine`.
- [ ] **Step 5: Commit** `feat(engine): bind named arguments, run in UTC and define tw_cell and tw_num`

### Task 2: enginesql, the widget dialect's text

**Files:**
- Create: `internal/shared/enginesql/params.go`, `params_test.go`
- Create: `internal/shared/enginesql/wrap.go`, `wrap_test.go`
- Create: `internal/shared/enginesql/page.go`, `page_test.go`
- Modify: `internal/archtest/archtest_test.go` (rank 0)

**Interfaces:**
- Produces:

  ```go
  // ErrRefused: text the scanner refuses, or a page it cannot build; the
  // message names what to change.
  var ErrRefused = errors.New("refused")

  // Params returns the :name parameters q uses, sigil included, in order of
  // first use. It refuses: a NUL byte; an unterminated string, quoted
  // identifier, dollar-quoted string or block comment; a ')' with no '('
  // before it and a '(' never closed (either would move the wrap's own
  // parens); ';' followed by anything but ';' and whitespace; '?', '$n'
  // and '$name' parameters. It never reads a parameter inside '…', E'…',
  // $tag$…$tag$, "…" or a comment, nor in '::' (a cast) or ':=' (a named
  // argument).
  func Params(q string) ([]string, error)

  // Bind rewrites each :name of q that vals has into the bound, typed
  // parameter vals gives, and returns the engine arguments. A :name vals
  // lacks is refused (the caller has already refused names outside its
  // set).
  type Value struct{ Type, Text string } // Type: BIGINT, DATE, VARCHAR…
  func Bind(q string, vals map[string]Value) (string, []engine.Arg, error)

  // Trim strips trailing ';' and ASCII whitespace, as readsql does.
  func Trim(q string) string
  // Text wraps q so every cell is DuckDB's text of its value, NULL kept.
  func Text(q string) string   // SELECT CAST(COLUMNS(*) AS VARCHAR) FROM (q\n)
  // Shape is q with no rows: its columns only.
  func Shape(q string) string  // SELECT * FROM (q\n) LIMIT 0

  // Page builds the statement QueryPage ran, in DuckDB, for readsql.Page
  // p over q, whose result columns are cols: the page's rows with the two
  // counts as the last two columns, or for Distinct the value, its rows
  // and the counts. Filter values are bound as $tw_pN. Count builds the
  // two counts for an empty page (Total, and Matched past offset 0).
  func Page(q string, cols []string, p readsql.Page, maxRows int) (string, []engine.Arg, error)
  func Count(q string, cols []string, p readsql.Page) (total string, matched string, args []engine.Arg, err error)
  ```

  `readsql.Page`, `Filter`, `Sort` and `PageResult` stay the types: forms page SQLite with them, and widgets with these.
- The page statement follows `readsql/page.go` line by line, with DuckDB's spellings:
  - `WHERE true` for no filter;
  - `tw_cell`/`tw_num` are the child's macros (Task 1);
  - the counts are `COUNT(*) OVER ()` as BIGINT;
  - `$tw_pN` placeholders, cast `CAST($tw_pN AS DOUBLE)` for `<`/`>` on a number.

  Sorting by `tw_cell` compares VARCHAR byte-wise, which is code-point order, as SQLite's BINARY and `table-view.ts`.

- [ ] **Step 1: Failing table test for `Params`** (`params_test.go`), one row each:
  - `day::DATE` gives none; `x::DECIMAL(10,2)` none; `read_csv('f', header := true)` none.
  - `'a :from b'`, `E'it\'s :to'`, `$$ :project $$`, `$q$ :x $q$`, `"col :x"`, `-- :x` and `/* :x */` give none.
  - `:project`, `:from`, `:to` in order of first use: `WHERE p = :project AND d BETWEEN :from AND :to AND :project = 1` gives `[:project :from :to]`.
  - `list[a:b]` gives `[:b]`.
  - Refused: `?`, `$1`, `$name`, `)`, `(`, `SELECT 1; SELECT 2`, an unterminated `'`, `"`, `$$` or `/*`, a NUL.
  - `SELECT 1;  ;\n` passes.
- [ ] **Step 2: Implement `Params`, `Bind`, `Trim`, `Text`, `Shape`.**
  - A byte scanner like `readsql.check`, with DuckDB's lexical rules. Dollar quotes are `$tag$` where tag is empty or an identifier.
  - `Bind` replaces at the scanner's offsets, never by string search.
  - Test `Bind` on `day BETWEEN :from AND :to` to give `day BETWEEN CAST($tw_from AS DATE) AND CAST($tw_to AS DATE)` with two args.
- [ ] **Step 3: Failing page tests** (`page_test.go`), built from `readsql/page_test.go`:
  - The Go test runs each statement through a real engine child (`TestMain` hook as `engine_test.go`).
  - It reads every case of `internal/reporting/testdata/table-filters.json` against a VALUES relation and expects the same rows, counts and order the SQLite tests expect.
  - It also covers a column named `__tw_total`, a page past the end, Distinct with a filter on another column, and refusals (unknown column, operator, distinct with sort).

  Leaf may import leaf, so the test may start an engine.
- [ ] **Step 4: Implement `Page` and `Count`;** the tests pass.
- [ ] **Step 5:** Add `"internal/shared/enginesql": 0` to the rank table. Run `go test ./internal/shared/... ./internal/archtest`.
- [ ] **Step 6: Commit** `feat(shared): enginesql, widget SQL for the engine: parameters, text cells and pages`

### Task 3: The reader scopes a query to a project; measures gain bucket; a fixture for files

**Files:**
- Modify: `internal/archive/reader.go` (`QueryScoped`, the measures view)
- Modify: `internal/archive/files.go` (`LatestDays`)
- Create: `internal/archive/archivetest/archivetest.go` (fixture used by reporting, api and app tests)
- Modify: `internal/archtest/archtest_test.go` (`internal/archive/archivetest`: 0)
- Test: `internal/archive/reader_test.go`, `internal/archive/files_test.go`
- Docs: `docs/twillingate.md` ("Writing SQL against the event files": `bucket` on `measures`)

**Interfaces:**
- Produces:

  ```go
  // Scoped is one query for QueryScoped.
  type Scoped struct {
      SQL     string
      Args    []engine.Arg
      MaxRows int
      // Project, when not 0, limits all seven tables to its rows.
      Project int64
  }

  // QueryScoped is Query with arguments and, for a project, the seven
  // tables shadowed by CTEs over its rows only:
  //   WITH events AS NOT MATERIALIZED (SELECT * FROM main.events WHERE project_id = <id>),
  //        views AS NOT MATERIALIZED (SELECT * FROM events WHERE family = 'views'), …,
  //        sessions AS NOT MATERIALIZED (SELECT * FROM main.sessions WHERE project_id = <id>), …
  //   SELECT * FROM (<sql>\n)
  // <id> is the int64 printed by the reader, not text from a caller.
  func (r *Reader) QueryScoped(ctx context.Context, q Scoped, rules Rules) (engine.Result, error)

  // LatestDays is each project's newest day with events in dir, from the
  // file names alone (a month file counts as its last day; a log as its day).
  func LatestDays(dir string) (map[int64]string, error)
  ```

- The measures view becomes `SELECT *, CASE WHEN value > 1e-17 THEN CAST(ceil(ln(value) / ln(1.04)) AS INTEGER) ELSE -1000 END AS bucket FROM events WHERE family = 'measures'`.
- **`archivetest`** (for tests in other packages):

  ```go
  // ChildEnv names the variable a test binary's TestMain checks to serve
  // as the engine child: if archivetest.Child() { return }.
  func Child() bool                  // serves engine.ServeChild and exits when the variable is set
  // New is a raw/ directory, its log, an engine on this test binary and a reader.
  func New(t testing.TB) *Files
  type Files struct{ Dir string; Log *archive.Log; Eng *engine.Engine; Reader *archive.Reader; Archive *archive.Archive }
  // Write appends events to the logs; Close dumps every day before today
  // (Maintain), so the reader reads day files, derived files and today's log.
  func (f *Files) Write(t testing.TB, evs ...store.Event)
  func (f *Files) Maintain(t testing.TB, today string)
  ```

  It is built from `maintain_test.go`'s `newFixture`; move the shared parts rather than copying them. `archive`'s own tests keep their fixture if moving it would churn them.

- [ ] **Step 1: Failing tests:**
  - `TestScopedQuerySeesOneProject`: two projects. A scoped `SELECT count(*) FROM events` counts one project. So do `views`, `sessions`, `identities` and `aliases`, and a `WITH x AS (SELECT * FROM events) SELECT count(*) FROM x`.
  - `TestScopedQueryBindsArguments`: `day = CAST($tw_d AS DATE)`.
  - `TestScopedQueryAfterAFailedBuild`: the retry paths of `Query`, vanished files and lost tables, apply to `QueryScoped` too.
  - `TestMeasuresHaveBucket`: bucket of 0, 1e-18, 1, 1.04 and 100.
  - `TestLatestDays`: a month file, day files, a segment and a log; the newest wins per project.
- [ ] **Step 2: Implement.** `Query` becomes `QueryScoped` with `Project` 0, so there is one code path; the retry stays in one place.
- [ ] **Step 3: Write `archivetest`;** a test in `archivetest` itself writes two days and reads them back.
- [ ] **Step 4:** Docs: `bucket` in the measures row of the column table, with the formula. Then `go test ./internal/archive/... ./internal/archtest`.
- [ ] **Step 5: Commit** `feat(archive): scope a query to one project, give measures their bucket`

### Task 4: Widgets run on the engine; the 59 system widgets in DuckDB

The big one: the source switch and the system widgets land together, since `Migrate` validates every system widget at boot.

**Files:**
- Modify: `internal/reporting/reporting.go`, `source.go`, `preview.go`, `data.go`, `migrate.go`, `dev.go`
- Create: `internal/reporting/files.go`'s neighbour `engine.go` (the `Files` interface, the hint)
- Modify: every `internal/reporting/system/*/*.sql` and the `.md` texts that describe caps or the 03:00 pass
- Modify: `internal/api/server.go` (`Build`/`NewHandler` pass `reporting.Files`), `internal/api/seed_test.go` (the host gets files)
- Modify: `internal/app/app.go` (engine and reader before `Migrate`), `internal/app/migrate.go`, `internal/app/reporting_dev.go`, `cmd/twillingate/migrate.go` if it builds the engine
- Create: `internal/app/widgets.go` (the `reporting.Files` adapter, the start checks)
- Test: `internal/reporting/*_test.go` (the fixtures move from SQLite `rawExec` to `archivetest`), `internal/reporting/system_test.go`, `internal/api/docs_sync_test.go`'s `TestReportingExamplesWork`
- Docs: `docs/reporting.md` (dialect, tables, every example, the component examples), `deploy/UPGRADES.md` (the 5d entry)

**Interfaces:**
- Consumes: `engine.Arg`, `enginesql.*` (Task 2), `archive.Reader.QueryScoped`, `archive.LatestDays`, `archivetest` (Task 3).
- Produces:

  ```go
  // Files is where sql widgets run: the event files through the engine.
  type Files interface {
      // Query runs one SELECT scoped to project (0: every project) under the
      // console's timeout, returning at most maxRows rows (0: all).
      Query(ctx context.Context, sql string, args []engine.Arg, maxRows int, project int64) (engine.Result, error)
      // SampleProject is the live project with the newest events, 0 when none.
      SampleProject(ctx context.Context) (int64, error)
      Timeout() time.Duration
      MaxRows() int
  }

  func New(st Store, files Files, opt Options) *Service
  func Migrate(ctx context.Context, st Store, files Files) error
  func DevHandler(dirs DevDirs, files Files, projects DevProjects) http.Handler // devListProjects keeps its SQLite query, behind DevProjects
  ```

- The sql source:
  - **Params:** `enginesql.Params`, then refuses names outside `:project`, `:from` and `:to`.
  - **Bind:** `enginesql.Bind` with `{":project": {BIGINT, id}, ":from": {DATE, from}, ":to": {DATE, to}}`.
  - **Run:** `Files.Query(enginesql.Text(bound), args, MaxRows()+1, project)`, with Truncated past `MaxRows`, as `readsql.Query` did.
  - **Shape check:** `enginesql.Shape` (no Text wrap).
  - **Pages:** Shape for the columns, then `Page` (or `Count` for an empty page).
  - `Load`/`LoadPage`/`Preview` return `readsql.Result`/`PageResult` with string rows, `""` for NULL and a non-nil `Rows`.
- Errors:
  - `context.DeadlineExceeded` is "query exceeded CONSOLE_QUERY_TIMEOUT (…)".
  - A DuckDB out-of-memory error gets `filesError`'s advice; move that function to a place both `api` and `reporting` reach. `enginesql` holds the message text, with `api` keeping its wording.
  - Any `*engine.QueryError` is ErrInvalid with its message.
  - Decision 10's hint is added for SQLite names.
- **The adapter in `app`** (`widgets.go`):
  - `Query` takes the registry's rules (`reg.Snapshot(ctx).RuleLists()`) and calls `QueryScoped` under `context.WithTimeout(CONSOLE_QUERY_TIMEOUT)`.
  - `SampleProject` takes the live projects from the registry and the newest of `archive.LatestDays`.
  - The start checks:
    - `checkUserWidgets`, in a goroutine after the console is up. For every live, non-system sql widget, it runs the shape query on project `SampleProject` and logs `widget SQL fails on the event files` with `widget_id`, `dashboard_id`, `title`, `error`.
    - `warnImport`, Decision 13. It reads the `events_imported` meta row (add a store getter if 5c added only the setter) and whether SQLite has any raw row.

**The port.** Each widget keeps its file name, component, columns and props. Translate by these rules:

| SQLite | DuckDB over the files |
| --- | --- |
| `day BETWEEN :from AND :to` | unchanged (`day`, `:from` and `:to` are DATE) |
| previous window `date(:from, '-' \|\| (julianday(:to) - julianday(:from) + 1) \|\| ' days')` | `:from - (:to - :from + 1)` (DATE − BIGINT is a DATE) |
| `date(:from, '-1 day')`, `date(x, '+' \|\| n \|\| ' days')` | `:from - 1`, `x + n` |
| `date('now', '-3 hours')` (retention's "reached") | `current_date` (UTC, Task 1; Decision 6) |
| `CAST(strftime('%s', ts) AS INTEGER)` | `CAST(floor(epoch(ts)) AS BIGINT)` |
| `CAST(… AS TEXT)` | `CAST(… AS VARCHAR)` |
| `x * 1.0 / y` | `x::DOUBLE / y` (a literal `1.0` is DECIMAL) |
| `WITH RECURSIVE offsets` 0..45 | `range(0, 46) AS offsets(day_offset)` |
| `agg_views_daily` + `raw_views` visitors, views per (day, kind) | `views`: `count(DISTINCT actor_id)`, `count(*)` per (day, kind) |
| sessions, bounces, duration from the sessionizing CTE | `sessions`: `count(*)`, `count(*) FILTER (WHERE views = 1)`, `sum(duration_sec)` per (day, kind) |
| `v_views_<dim>` (visitors, views per day and key) | `views` grouped by (day, key): `count(DISTINCT actor_id)`, `count(*)`; then summed over days as the widget did. Keys: `path`; `host`; `referrer_source` (was `source`); `country`; `display_width \|\| 'x' \|\| display_height` where both > 0; `(os, os_version)`; `(browser, browser_version)`; `(platform, app_version)` where `app_version` is set; `(device, device_model)`; the three UTMs where any is set; consent `CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END`. An absent value is NULL in the files where SQLite had `''`: write `x IS NOT NULL AND x <> ''` for `!= ''` |
| `v_product_daily` / `v_product_totals` | `product` per (day, event_name) / per day: `count(*)`, `count(DISTINCT actor_id)` |
| `v_product_attrs` for `$app_version` | `product` grouped by `app_version` |
| `v_product_attrs`, custom keys | `UNPIVOT (SELECT day, event_name, actor_id, group_id, COLUMNS('^attr_') FROM product) ON COLUMNS('^attr_') INTO NAME attr_key VALUE attr_value`, key as `substr(attr_key, 6)`; `unique_groups` is `count(DISTINCT group_id)` |
| `v_identity_daily` (kind user) | `events WHERE family IN ('views', 'product') AND user_id <> ''` per (day, user_id): `count(DISTINCT actor_id)` actors, 1 user, views and product counts |
| `v_identity_daily` (kind group) | as above on `group_id`; users = `count(DISTINCT user_id)` |
| `firsts` (MIN(day) over the history) | `identities.first_seen::DATE` for the kind (Decision 7) |
| `LEFT JOIN identities i` (name) | `LEFT JOIN identities i ON i.kind = … AND i.id = …` (`name`) |
| `v_retention` | a CTE: `first` = per actor (kind user or install, Decision 6) its first day; `active` = distinct (actor, day) of views and product events with `day < current_date`; offset = `day - first_day`; cohort size = offset 0 |
| `v_measures_daily` | `measures` per (day, event_name, measure, bucket): `count(*)` samples, `sum(1.0 / sample_rate)` weight, `sum(value / sample_rate)` sum; approx_value `CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END` |
| `v_measures_attrs` for `$device`, `$browser` | `measures` grouped by `device`, `browser` |
| `raw_measures` | `measures` |

The header comment of each file says what it reads and why, as today. The comments that explain the live half or the caps go.

- [ ] **Step 1: The interface and the source** (`engine.go`, `source.go`, `preview.go`, `data.go`, `reporting.go`), with unit tests against a fake `Files`:
  - `source_test.go` covers binding, the shape and sample checks, Truncated, the timeout message and the SQLite hint.
- [ ] **Step 2: The reporting tests' fixture.** Replace `newTestStoreAndReadDB` with a store plus an `archivetest.Files` and an adapter (a test copy of `app`'s, or `archivetest` exporting one).
  - Port `data_test`, `view_test`, `preview_test`, `ops_*_test`, `project_tabs_test`, `group_names_test` and `share` tests: SQL to DuckDB, seeds from `rawExec` into the files.
  - `TestMain` gets `if archivetest.Child() { return }`.
- [ ] **Step 3: Port the system widgets** by the table, dashboard by dashboard (views, product, users, groups, retention, measures, vitals). `system_test.go`'s fixture seeds the same scenarios as events into the files:
  - the session ids;
  - the 40-minute gaps;
  - the bounces;
  - the attributes;
  - the measures with sample rates;
  - the retention cohorts over 99 days, as events, not `agg_retention` rows;
  - the neighbour.

  `TestSystemDashboards`, `TestRetentionMilestonesMatchCurve`, `TestAttributeValuesKeepEveryDay` (the 120 extra values as events), `TestAttributeValuesFilterByAliases`, `TestTopEventsKeepsTheBusiest`, `TestBrowsersAndSystemsByName` and `TestVitalsStatsBlankWithoutSamples` keep their assertions. `TestViewsDailyEquivalence` moves to Task 5.
- [ ] **Step 4: `Migrate`, `DevHandler` and the app.**
  - `app.Serve` builds the engine and reader before `Migrate`. The ingest-only process does not.
  - `app.Migrate` and `twillingate migrate` get an engine on the same binary. Reuse `EngineOptions`; close it after.
  - Add `checkUserWidgets` and `warnImport` with tests in `internal/app`: a user widget on `v_views_daily` is logged; an import row silences the warning.
- [ ] **Step 5: `api` wiring.**
  - `Build` takes `reporting.Files` from `app`. The `db` handle stays for `query`'s sqlite source and the limits.
  - `seed_test.go`'s host gets files (a `TestMain` hook, `archivetest.New`) holding the events the template's live rows hold.
  - `TestReportingExamplesWork` runs the new examples on them.
- [ ] **Step 6: Docs.**
  - `docs/reporting.md`:
    - the dialect: DuckDB, `:project`/`:from`/`:to` typed BIGINT/DATE, no `?`/`$`;
    - the tables: the files' seven, with `bucket`;
    - "Percentiles from measures";
    - every example and component example rewritten;
    - the "Cost" notes;
    - the hint for SQLite widgets.
  - `deploy/UPGRADES.md`, the 5d entry:
    - run `twillingate events import` first;
    - history older than the raw window is gone from dashboards;
    - user widgets break: find them in the log line (`journalctl -u twillingate | grep "widget SQL fails"`) or by their error on the dashboard, and rewrite them, with three before/after examples;
    - `Migrate` now needs the engine at boot.
- [ ] **Step 7: Run `make check`.** Fix what it finds.
- [ ] **Step 8: Commit** `feat(reporting)!: run sql widgets on the event files in DuckDB SQL`

### Task 5: Parity with the SQLite path

**Files:**
- Create: `internal/reporting/testdata/system-sqlite/` (the 59 `.sql` files as they were before Task 4, copied from `git show <5c base>:internal/reporting/system/...`; PR 6 deletes it)
- Create: `internal/reporting/parity_test.go`

**Interfaces:**
- Consumes: Task 4's system widgets, `archivetest`, `storetest`, `readsql`.

- [ ] **Step 1: One event set, two stores.** A generator makes ~100 days of events for two projects:
  - views across both kinds;
  - session ids and gaps;
  - bounces;
  - every breakdown dimension (below the caps: under 100 values per day, under 1000 users);
  - product events with the declared key `plan` and `$app_version`;
  - measures with sample rates;
  - users and groups with names.

  The generator writes them twice:
  - into SQLite `events`, with identities upserted, then the daily pass (`jobs.RunDailyPass`, or `UpsertActors` + `AggregateRetentionDay` per day) so `agg_retention` exists;
  - into the files, with `Maintain` (today's log left open).

  The raw window covers all 100 days, so SQLite's views read raw rows only.
- [ ] **Step 2: `TestSystemWidgetsMatchSQLite`.** For every widget and every preset, and both projects:
  - run the old text through readsql on SQLite and the new through the reporting source on the files;
  - compare columns and rows: numbers within 1e-9 relative, `2` = `2.0`, text exact.

  Known differences are listed in the test with the reason:
  - retention's "reached" (`now - 3 hours` against `current_date`) at the edge, so seed no event in the last 3 hours of a day;
  - declared session ids that are small integers (5a);
  - the pruned actors of `agg_retention`.
- [ ] **Step 3: Fix the ports** until it passes. Each fix is in the widget's file, not in the test.
- [ ] **Step 4: Commit** `test(reporting): the system widgets give SQLite's numbers from the files`

### Task 6: The analytics tools on the engine; query defaults to files; identities stop

**Files:**
- Modify: `internal/api/ops_read.go` (`viewsOverview`, `viewsBreakdown`, `viewsDimensions`), `ops_product.go` (`productEvents`, `productAttributes`, `retention`, `identities`), `ops_measures.go`, `ops_query.go` (default), `resources.go` (`schemaViews`)
- Modify: `internal/server/handlers.go`, `server.go` (no `UpsertIdentities`; `NameStore` goes), `internal/app/app.go`
- Test: `internal/api/rest_test.go`, `coverage_test.go`, `ops_*_test.go`, `resources_test.go`, `guide_reporting_test.go`, `docs_sync_test.go` (`TestDocumentCoversEveryViewsDimension` now binds dimensions to their columns), `internal/server/server_test.go`
- Docs: `docs/twillingate.md` (the tool table, `query`, "Writing SQL against the views" marked deprecated, the files section first, Identity), `deploy/UPGRADES.md` (tools)

**Interfaces:**
- Consumes: `api.Files` gains `QueryScoped` (the reader's method); `enginesql.Bind` for `:project`/`:from`/`:to` in the tools' own SQL.
- Each tool keeps its input, output, columns and order. The SQL follows Task 4's table.
  - **`views_overview`:** views and sessions per day.
  - **`views_breakdown`:** `viewsDimensions` maps each dimension to its key expressions over `views`.
  - **`product_events`:** `product` per (day, event_name), and the totals per day.
  - **`product_attributes`:** every key, unpivoted as `attribute-values`, plus the system keys `v_product_attrs` had.
  - **`measures`:** the percentiles over `measures`, by `attr_key` when given. For a `$`-key, `attr_key` names its typed column (`$device` reads `device`).
  - **`retention`:** Decision 6's cohorts; `aggregated_through` is the newest complete day with activity.
  - **`identities`:** per id over the range: actors, users, views and events from `events`, the name from `identities`, ordered by views + events.
- `query`: an empty `source` is `files`. `sqlite` keeps working; its description says deprecated, removed in a later release.
- `schemaViews` leads with the files' seven tables (columns, `bucket`, NULL against `''`, DATE days, "the files hold the raw history"). It keeps the v_* list under "Deprecated: source sqlite". The editor's parser (Task 7) reads the first part.

- [ ] **Step 1: Failing tests on files.** The api host from Task 4 has files. Add events that give known answers for each tool, and assert the rows the SQLite seeds gave where they correspond.
- [ ] **Step 2: Rewrite the seven handlers;** the tests pass.
- [ ] **Step 3: `query`'s default,** with tests: no source runs on files; `sqlite` still works.
- [ ] **Step 4: Ingest stops writing names.** A server test pins that a batch with `$user_name` makes no `UpsertIdentities` call; the name is on the event in the files (5a).
- [ ] **Step 5: `schemaViews`, the docs and UPGRADES;** `docs_sync_test` and `resources_test` pass.
- [ ] **Step 6: Run `make check`.**
- [ ] **Step 7: Commit** `feat(api)!: answer the analytics tools from the event files; query reads the files by default`

### Task 7: The editor and the end-to-end suite

**Files:**
- Modify: `web/src/components/editor/CodeEditor.tsx` (dialect `PostgreSQL`, closest to DuckDB, from `@codemirror/lang-sql`)
- Modify: `web/src/lib/schema-views.ts` (parse the files' tables from Task 6's `schemaViews`)
- Modify: `web/e2e/widget-editor.spec.ts`, `share.spec.ts`, `phone.spec.ts`, `app.spec.ts` (DuckDB SQL: `views` for `v_views_daily`, `:from <= :to` stays valid)
- Modify: `web/e2e/serve.sh` (after `seed-demo.py`, run `twillingate events import`)
- Test: `web/src/lib/schema-views.test.ts`, the e2e suite

- [ ] **Step 1:** The parser and its test: completions offer `events`, `views`, …, their columns, and `:project`/`:from`/`:to`.
- [ ] **Step 2:** The dialect switch. `npm run typecheck && npm test` in `web/`.
- [ ] **Step 3:** `serve.sh` imports after seeding. The specs' SQL is ported. `cd web && npm run e2e` is green, `cursor.spec.ts` and `phone.spec.ts` included.
- [ ] **Step 4: Commit** `feat(web): complete and highlight widget SQL as DuckDB over the event files`

### Task 8: Measure, and the test instance

**Files:**
- Create: `internal/app/measure_dashboard_test.go` (`//go:build measure`)
- Modify: `docs/superpowers/plans/2026-10-12-raw-first-roadmap.md` (the 5d section: the plan's name, the decisions that changed it, the measurement)

- [ ] **Step 1:** Generate 1 year at the cap for one project, with archive's measure generator moved into `archivetest` if it is not there yet.
  - Run the Views dashboard's 20 widgets through `reporting.Service.WidgetData`, one at a time and two at a time, at `threads=2`, `ENGINE_MEMORY_LIMIT` 512MB.
  - Record each widget's time and the total.
  - Target: under 2 s (roadmap).
  - If it misses, record the slowest widgets and stop: the fix is a follow-up, not this PR's.
- [ ] **Step 2:** The roadmap gets `Plan: 2026-10-14-raw-switch.md` and the measured numbers.
- [ ] **Step 3:** Tell the user that the test-instance skill (`~/.claude/skills/twillingate-test-instance/scripts/start.sh`) must run `twillingate events import` after `seed-demo.py`. That file is outside the repository; edit it only if asked.
- [ ] **Step 4: Commit** `docs: measure the Views dashboard on the files and link the 5d plan`
