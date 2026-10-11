# Every SQL string checked against the schema

Status: proposed
Date: 2026-10-10 (replaces adopting sqlc, considered the same day)

## Problem

- **A broken query is found only when something runs it.** Every SQL
  string in the binary is a Go literal handed to `database/sql` or to
  `readsql`. A migration that renames a column, or a typo in a query, fails
  the test that executes that query, and nothing else. The coverage gate
  is 90%, so the other 10% of paths (error branches, rarely used options)
  can carry SQL that has never been compiled.
- **Nobody checks the number of arguments.** modernc's statement reports
  `NumInput() == -1`, so `database/sql` never compares the placeholders
  with the arguments passed. A missing or extra argument at an `ExecContext`
  goes unnoticed until a value lands in the wrong column, or a test
  happens to look at it. The ingest insert binds 38 of them.
- **A multi-statement string is compiled one statement at a time.**
  modernc prepares the first statement and runs the rest as a script, so
  only the first statement is checked when it is prepared.

### Why not sqlc

sqlc generates typed Go from `.sql` files and fails the build when a query
doesn't match the schema. It was tried against this schema (v1.30.0) on
2026-10-10:

- It cannot read the migrations. Its SQLite grammar fails on 6 of the 37
  files: named `WINDOW` clauses, `FILTER (…) OVER`, and the columns of
  `json_each` (`value`, `key`), which it does not model.
- A dump of the final schema parses for every table, but only for 11 of the
  25 views. The 12 `v_views_*` breakdowns, `v_product_attrs` and
  `v_measures_attrs` fail, and a query that reads one fails with
  `relation does not exist`. SQLite reports those views' computed columns
  as `BLOB`, so stand-in declarations would need a type kept by hand for
  each column.
- Queries that use `json_each` (forms search, received attributes) fail,
  and window functions come back as `interface{}`.

sqlc would therefore check the plain table queries, the easiest to get
right, and skip the analytics SQL where errors cost the most. It would also
bring a generator, a committed schema dump, committed generated code, and
two drift checks.

SQLite accepts every query in the binary by definition, so preparing each
string against a migrated database checks all of them. A probe on
2026-10-10 found 198 call sites: 138 resolve to a constant string as
written, and every one prepares cleanly today.

## Decisions

- **D1. One test package, `internal/sqlcheck`.** It holds only
  `_test.go` files (tokenizer, scanner, checks, and the run over the tree),
  reads the source tree with `go/parser`, and runs in
  `make check` and `make test` like any other package. It uses only the
  standard library and `internal/store/storetest`, adds no Go dependency
  and no generated code, and changes no production file. archtest treats
  it like itself: `unconstrained` gains `internal/sqlcheck`.

- **D2. A site is a call that carries SQL.** The sinks are the
  `database/sql` methods `ExecContext`, `QueryContext`, `QueryRowContext`
  and `PrepareContext`, matched by name on any receiver, when the call
  has an argument at the SQL position. Every non-test `.go` file under
  `internal/` is scanned. A call that only shares a sink's name would fail
  loudly at prepare time, at its own `file:line`, so nothing narrower is
  needed. Calls without a context (`Exec`, `Query`, `QueryRow`, `Prepare`)
  are not sinks: the code uses the `…Context` methods throughout, the two
  PRAGMAs in `store/sqlite/sqlite.go` aside.
  - **Pass-through wrappers count as sinks.** A function whose SQL
    argument is one of its own parameters becomes a sink itself, at that
    parameter's position, and its callers are checked instead. This is
    repeated until nothing changes, so `readsql.Run`, `api`'s `host.run`
    and `runOn` are found without a list to maintain. Functions that
    rewrite the caller's SQL (`readsql`'s `Query`, `QueryLimit` and
    `QueryPage`) hand on a different string, so they are not sinks. The
    wrappers are found in directory order, so the result does not depend
    on map order; a function that shares a wrapper's name is taken for
    one.

- **D3. The SQL argument resolves to one or more strings, or the site is
  dynamic.** Resolution, using only the AST:
  1. string literals, `+`, `const`s at package or function scope, and
     package variables that nothing in the package assigns again;
  2. a local variable whose every assignment in the enclosing function
     resolves gives one candidate per assignment (a `switch` that picks a
     query gives every branch);
  3. a table-driven loop, `for _, s := range <slice literal, held inline,
     by a local or by an unchanged package variable>`, gives each element:
     `s.field` for a slice of structs (`ops_usage`, `usage_history`), `s`
     itself for a slice of strings (`registry`'s project tables);
  4. a call to a known fragment helper is replaced by a stand-in:
     `placeholders(n)` becomes `?`. The map of stand-ins lives in the
     test.

  Two names are never resolved: one declared more than once in a
  function (each `:=` that introduces it, each `var` that names it), and
  one that a function's `range` loops bind to more than one slice. The
  scan cannot tell which value a call reads, so the site is dynamic, with
  the reason. Plain `=` reassignments of a name declared once still give
  every value.

  Each resolved candidate is checked (D4–D6).

- **D4. Every statement is prepared against the current schema.** The test
  copies a migrated database with `storetest.Copy` and opens it.
  - A tokenizer that understands `'…'`, `"…"`, `` `…` ``, `[…]`, `--` and
    `/* */` splits each candidate on `;` and prepares every statement with
    `PrepareContext`, so the second statement of a script is checked too.
  - A Go SQL string holding `CREATE TRIGGER` fails the test rather than
    being split wrongly. None does today; trigger bodies live only in
    migrations.
  - SQLite rejects an unknown table, view, column or function, a syntax
    error, or a wrong number of function arguments.
  - The failure names the site, `file.go:line`, the statement, and
    SQLite's message.

- **D5. Bind parameters must match the arguments.** The same tokenizer
  counts what the statement binds: anonymous `?` one each, `?NNN` up to
  the highest N, and `:x`, `@x`, `$x` once per distinct name.
  - At a site whose arguments are all written out (no `args...`), the
    count after the SQL argument must equal what the statement binds. A
    site that spreads `args...` is not counted.
  - For a statement prepared into a local (`stmt, err :=
    tx.PrepareContext(ctx, q)`), the arguments counted are those of each
    `stmt.ExecContext`, `stmt.QueryContext` or `stmt.QueryRowContext` on
    that local in the same function. This covers the ingest insert.

- **D6. Scan targets must match the columns.** A read site is a
  database/sql `QueryContext` or `QueryRowContext` call. Callers of a
  wrapper get no column check: the wrapper returns a `readsql.Result`,
  not rows. The test runs each statement once on the empty migrated database, inside a
  transaction it rolls back, with every parameter bound to `NULL`, and
  reads `rows.Columns()`. Statement text is not used to decide what is a
  read: `WITH … INSERT` starts like a query. The number of Scan
  destinations must match, where it can be found:
  - `x.QueryRowContext(…).Scan(a, b, …)` chained on the site;
  - `rows.Scan(…)` on the local that received the site's rows, in the same
    function.

  Scans that spread `dest...`, and rows handed to another function (a
  `scanX` helper), are not counted. Running a query on an empty database touches no data and
  takes about a millisecond.

- **D7. A dynamic site must be listed, with a reason.** A site whose SQL
  does not resolve fails the test unless it is in `dynamic`, a map in the
  test keyed `file.go:FuncName` (the file path relative to `internal/`)
  whose value is the reason. The map is checked in both directions, like
  `docs_sync_test.go`: an entry that matches no dynamic site fails too, so
  the list cannot rot. The scanner leaves 24 functions, in these
  groups:
  - table or column names from code: `prune`, `retention`, `identities`,
    `aggregate_views`, the attribute rollups, `ops_limits` (a dimension),
    `usage_history`'s table sizes;
  - SQL built from data or options: `flatview` (the attributes view),
    `UpdateWidget`'s set list, filters appended
    in `ListWidgets`, the forms search and the `ops_*` reads;
  - `readsql`'s wrappers (`QueryLimit`, `QueryPage`, the empty count),
    which rewrite the caller's SQL rather than pass it on. They are
    therefore not sinks, and user SQL (widget content, `query`) never
    becomes a site: `readsql` validates it when it runs;
  - SQL assembled once at startup by a function (`receivedSQL`,
    `foldedSQL`);
  - migration bodies in `migrate.go`, already compiled by every test that
    migrates.

  Adding a new dynamic site is therefore a reviewed, one-line decision.
  An entry covers its whole function, so functions that build SQL should
  stay small.

  **Migration steps.** A Go migration step (`dataSteps` in
  `store/sqlite/migrate.go`) runs inside its migration, against that
  version's schema, not the current one: migration 015's step reads
  `agg_views_app_versions_old`, which the same migration drops. Its file
  is exempt from the checks and listed in `migrationSteps`, a second map
  in the test, checked both ways like `dynamic`: a
  `store/sqlite/migrationNNN.go` that holds SQL and is not listed fails,
  and so does a listed file that holds none. Every test that migrates
  already runs these steps.

- **D8. CLAUDE.md says so.** The Checks section gains one paragraph:
  every SQL string is prepared against the migrated schema by
  `internal/sqlcheck`. A string built at run time goes in its `dynamic`
  list with the reason, or is rewritten so it resolves (constants,
  `placeholders`).

## Non-goals

- **Typed results and parameters.** The test checks counts, not Go types.
  A `string` scanned from an `INTEGER` column still converts or fails at
  run time, as today.
- **Widget SQL in the system dashboards.** `TestSystemDashboards` already
  runs it.
- **Queries in `_test.go` files.** The tests run those.
- **sqlc later.** Nothing here prevents adopting it for the plain table
  queries. This check would keep covering what sqlc cannot parse.

## Testing

- The tokenizer gets table tests of its own: quotes, comments, `?NNN`,
  named parameters, `$` inside a JSON path string (`'$."key"'`, which is
  not a parameter), and a `;` inside a string.
- Resolution gets synthetic sources, as archtest tests its rule on a
  synthetic graph: one per D3 rule, a pass-through wrapper two levels
  deep, and a dynamic site that is listed and one that is not.
- Mutation checks, run by hand once before merge and recorded in the PR:
  rename a column in a query string, drop an argument from the ingest
  insert, add a column to a `QueryRowContext(…).Scan` select list. Each
  must fail with the site named.
- `make check` and `make test`. The package migrates nothing itself
  (storetest copies the template) and should stay well under a second
  outside `-race`.

## Changes

| File | Change |
| --- | --- |
| `internal/sqlcheck/*_test.go` | new: tokenizer, scanner and resolver, the three checks, the run over `internal/` with its `dynamic` and `migrationSteps` lists; a blank import of `internal/shared/readsql` registers `tw_cell` and `tw_num`, which fixed SQL may call |
| `internal/archtest/archtest_test.go` | `unconstrained` includes `internal/sqlcheck` |
| `CLAUDE.md` | Checks: one paragraph (D8) |
| production code | none, unless the first run finds a real defect, fixed in its own `fix:` commit |
