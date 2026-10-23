# Native engine, step 4: the daily pass in Go — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The daily pass, form attribution and the rule-save checks no longer touch DuckDB. Today they use the dump of a day's NDJSON log to Parquet, the month merge, and the derived sessions, identities and aliases. Go writes the same files, which DuckDB and the native engine read exactly as before. After this step DuckDB serves only SQL: the `query` tool's `sql` mode and SQL widgets.

**Architecture:** `internal/archive` gains a Parquet writer on `parquet-go` (already the native reader's library). It provides:
- a Go dump (`.dumping` NDJSON → day file);
- a two-pass streaming month merge;
- the derived files, written by Go derivers.

The Go derivers already exist on the read side in `internal/native` (`deriveSessions`, `deriveIdentities`, the NDJSON decode and casts). They move down into `archive`, because `native` imports `archive` and both are rank 0. `native` then calls them, and a Go aliases deriver is added. `Archive` no longer holds an engine. Form attribution and the rule-save presence checks move onto the native engine, the only other non-SQL DuckDB users.

**Tech Stack:** Go, `github.com/parquet-go/parquet-go` v0.32.0.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md` ("The daily pass without DuckDB"; order of work, step 4). The scratch survey of today's DuckDB use, with SQL, schemas and tests, is `.superpowers/native-step4-survey.md` in this worktree. The plan wins where they differ.

## Global Constraints

- **Same files.** Names, layout, staleness rules (mtime), retention, claim/rotate and atomic writes are unchanged: `.tmp`, chmod 0640, fsync, rename, then fsync the directory. Every existing test in `internal/archive/maintain_test.go` and `derive_test.go` keeps passing unchanged except for its setup. These tests read the files back through DuckDB `read_parquet`, so they prove DuckDB still reads Go-written files.
- **Parquet encoding.** Match DuckDB's types, so DuckDB's `DESCRIBE` of a Go file equals its `DESCRIBE` of today's file:
  - flat schema, every column OPTIONAL;
  - VARCHAR is BYTE_ARRAY with the String logical type;
  - BIGINT is INT64; DOUBLE is DOUBLE;
  - DATE is INT32 with the DATE logical type;
  - TIMESTAMP is INT64 `TIMESTAMP(isAdjustedToUTC=false, MICROS)`;
  - zstd compression; dictionary encoding on BYTE_ARRAY columns;
  - column order exactly as listed. Do not let parquet-go sort fields by name; `scripts/native-bench/rewrite.go` shows a node that keeps the order.
- **Statistics.** Every row group carries exact min/max (never truncated) and null counts for every column. The native reader's pruning trusts `family` and `day` bounds and null counts; a wrong value there is a wrong answer.
- **Row groups.** Within a file, rows are ordered by `family`, then `ts`. A new row group starts when the family or the day changes, or after 65,536 rows. A month file therefore has about one group per family per day, the spec's "one day per row group" starting point. Derived files cut groups at 65,536 rows. An empty derived file is written with its full schema and no row groups.
- **Ties.** A stable sort keeps input order among equal (`family`, `ts`). Sessions use `deriveSessions`' existing deterministic order. An identity name tie is settled by `identityMap`'s existing rule.
- **Semantics:**
  - **Dump:** each line is decoded and cast with today's native decoder (it mirrors DuckDB's `json_extract_string` + CAST). The first line per id in file order wins, and all NULL ids count as one id. The schema is `columnsOf(logKeys(path))`, unchanged. A non-object or unparsable line fails the dump, as today.
  - **Merge:** an id keeps the row from the earliest input (by input order), then the earliest row. The schema is the union of the inputs: columns match case-insensitively, the first spelling and first-appearance order are kept, and every fixed column in `fields` order is added when no input has it. That addition is new and harmless: readers treat a missing column as NULL either way.
  - **Derived files:** exactly `derive.go`'s `sessionsSelect`, `identitiesSelect` and `aliasesSelect`.
- **Memory.** The pass handles one project and one file at a time.
  - It never loads a month into memory. The merge streams row groups.
  - Merge dedup holds a set of 128-bit id hashes (two `hash/maphash` seeds) for the month, plus one drop bit per input row.
  - The dump loads one day's log; a day is at most a few tens of thousands of lines in the workload envelope of 100k–1M events/month.
  - Target: the pass's peak RSS on a 1M-events month (merge, then derive) stays under ~150 MB, measured in Task 6.
- **Packages.** Keep `internal/archive` at rank 0 importing only leaves. The NDJSON decode/casts and the session, identity and alias derivers move from `internal/native` into `internal/archive`, and `native` calls them. This is a pure move; native's parity tests prove behaviour is unchanged. DuckDB stays in the tree for SQL; `internal/engine` is untouched.
- **Old SQL kept as oracle.** `dumpQuery`, `mergeQuery`, `sessionsSelect`, `identitiesSelect`, `aliasesSelect` and `attributionSQL` move into `_test.go` files as references. A parity test per writer writes the same inputs both ways (DuckDB COPY and Go) and compares every row and column through DuckDB. DuckDB and these references leave in step 6.
- **Docs, in the same commit as the change:**
  - `docs/deployment.md`: what `ENGINE_*` settings now govern; the daily pass no longer uses the engine child or its memory limit or slots.
  - `deploy/UPGRADES.md`: one entry. Day and month files written from now on have smaller row groups. Older files are left as they are until retention. Nothing to do.
- **Checks.** `make check` passes before each commit: `PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`. Never use `/tmp`.
- **Commits.** Conventional Commits with scope `archive`, `native`, `jobs` or `app`. User-visible changes use `feat`/`perf`; moves use `refactor`.

## Review Focus

1. **A log whose keys differ only in case** (`attr_Plan`, `attr_plan`), across two days of one month. The month file has one column, the first spelling, and both days' values. DuckDB's union did the same. Pinned by Task 4's parity case.
2. **A crashed merge's leftover `<month>.parquet`** next to the day files. Its rows win for duplicated ids, and nothing is counted twice. Pinned by Task 4, reusing `TestMergeIncludesAnExistingMonthFile`.
3. **Statistics on a group whose `family` is all NULL, or with a NULL `day`.** The null count makes the reader filter rows instead of trusting bounds. Pinned by Task 1's stats test.
4. **A reader during a pass.** A native query running while the merge renames and deletes day files still answers once, as today's retry does. Pinned by the existing `TestRunRetriesVanishedFiles` against Go-written files.
5. **A day file from an older release** without `install_id`, `user_name` or `group_name`, merged into a month. The month gains those columns, all NULL, and identities and aliases still derive. Pinned by Task 4, reusing `TestDeriveReadsFilesWrittenBeforeTheNewColumns`' input shape.

---

### Task 1: The Parquet writer

**Files:** create `internal/archive/pqwrite.go` and `pqwrite_test.go`.

**Interfaces (produces):**
- `type colType int`, with consts `colVarchar`, `colBigint`, `colDouble`, `colDate`, `colTimestamp`.
- `type pqColumn struct{ Name string; Type colType }`.
- `type rowWriter` with `newRowWriter(path string, cols []pqColumn, cut func(prev, next row) bool) (*rowWriter, error)`, `Write(r row) error` and `Close() error`. `Close` does the whole atomic finish: write `path+".tmp"`, chmod 0640, fsync, rename, fsync the directory. `Abort()` removes the tmp file.
- `type row []value`, where `value` is one of nil, string, int64, float64, a day (`int32` days since epoch) or a timestamp (`int64` µs). Pick the exact representation; the native batch's vectors are the model.
- Groups are cut on `cut(prev, next)` returning true, or after 65,536 rows.

**Tests:**
- `TestWriterTypesMatchDuckDB`: a file with one column of each type. DuckDB's `DESCRIBE SELECT * FROM read_parquet(...)` must equal the same `DESCRIBE` of a file DuckDB COPYs from the same values. Values must read back equal through DuckDB and through `native`'s reader.
- `TestWriterKeepsColumnOrder`.
- `TestWriterStatsAreExact`: long strings are not truncated, and the null counts include an all-NULL group. Use `native`'s `statsOf`, or read the metadata with parquet-go.
- `TestWriterCutsGroups`: on the cut function and at 65,536 rows.
- `TestEmptyFileHasSchema`.
- `TestWriterFileIsAtomicAnd0640`.

- [ ] Write the tests and run them to see them fail. Implement, run `make check`, and commit: `feat(archive): write Parquet files in Go`.

### Task 2: Move the decoder and derivers down

**Files:**
- Move NDJSON line decode and casts from `internal/native/logs.go` into `internal/archive`: `text`, `parseInt`, `parseTime`, `parseDay`, the first-line-per-id rule and what `readLog` needs.
- Move `deriveSessions` (`native/sessions.go`) and the identity fold (`identityMap.see`/`fold`, `native/identities.go`) into `internal/archive`, exported where `native` needs them.
- Add `archive` aliases folding in the same style.

`native` keeps its batching and vectors; only the row-level logic moves. If a mover finds the logic too entangled with native's column vectors to move cleanly, the archive copy takes rows and native adapts them. No semantics change.

**Tests:**
- All `internal/native` parity tests pass unchanged.
- `archive` gets unit tests for the moved functions, copied with them.
- `TestAliasesFoldMatchesDuckDB` compares against `aliasesSelect`.

- [ ] Do the move, then run `go test ./internal/native/ ./internal/archive/` and `make check`. Commit: `refactor(archive): hold the row decoder and the derivers native reads with`.

### Task 3: The dump in Go

**Files:** `internal/archive/maintain.go` (`dump` uses Task 1 and Task 2 instead of `write`+`dumpQuery`); `dumpQuery`/`logSelect`'s strict use move into a test file as the oracle. `Reader` still uses `logSelect` tolerant, so it stays.

**Tests:**
- All dump tests in `maintain_test.go` pass.
- `TestGoDumpMatchesDuckDB`: for every log fixture in the dump tests, plus the 45 MB `TestDumpKeepsAnIDsFirstLine` log, the Go file equals the DuckDB COPY file in columns, types and rows (in order), compared through DuckDB.

- [ ] Write the tests first. Implement, run `make check`, and commit: `feat(archive): dump a day's log without the engine`.

### Task 4: The month merge in Go

**Files:** `internal/archive/maintain.go` (`merge`), plus a new `merge.go` if it grows. The algorithm:

- **Pass 1:** read only the `id` column of each input, in input order, keeping a set of 128-bit id hashes. Mark each row whose id was already seen (NULL ids are one id) in a per-input bitset.
- **Pass 2:** do a k-way merge of the inputs by (`family`, `ts`), each input read row group by row group. Skip marked rows, and break ties by input order, then row order. Write through Task 1, cutting groups on family or day change.
- **Unsorted input:** if an input is found not sorted by (`family`, `ts`), the merge fails with an error naming the file, and nothing is renamed or deleted. The month stays as day files, which still read correctly.
- **Schema:** follow the Global Constraints.
- **Hash collision:** a 128-bit collision would drop a row. Its probability at a month's ids is below 1e-20; state it in a comment.

**Tests:**
- All merge, retention and derive tests pass.
- `TestGoMergeMatchesDuckDB`: case-split keys across days, a leftover month file, segments, duplicate ids within and across inputs, an older day file without the newer columns, and a type-ambiguous VARCHAR. Each must equal the DuckDB merge row for row, except for the added fixed columns, which are all NULL. Order the comparison by (`family`, `ts`, `id`).
- `TestMergeRefusesAnUnsortedInput`.
- `TestMergeFailsOnAnUnmatchedInput` keeps its meaning: never drop a row silently.

- [ ] Write the tests first. Implement, run `make check`, and commit: `feat(archive): merge a month by streaming its days`.

### Task 5: The derived files in Go, and no engine in the pass

**Files:**
- `internal/archive/maintain.go` (`derive`), `derive.go`: sessions, identities and aliases are written by Task 2's derivers through Task 1. The derivers read only the columns they need, file by file.
- `archive.New` loses its engine argument.
- `internal/app` wiring and `archivetest` (`New`/`Maintain`) use the Go pass.
- The derived SELECTs move into test files as the oracle.

**Tests:**
- All `derive_test.go` tests pass.
- `TestGoDerivedFilesMatchDuckDB` covers the sessions day and month, identities and aliases over the derive fixtures, compared row for row with the DuckDB COPY of the old SELECTs.
- `TestMaintainWithNothingToDoSpawnsNoChild` becomes "the pass never spawns a child".
- Every `internal/native`, `internal/api` and `internal/reporting` parity test now runs over Go-written fixture files and must pass unchanged.

- [ ] Write the tests first. Implement, run `make check`, and commit: `feat(archive): derive sessions, identities and aliases without the engine`.

### Task 6: Measure the pass and document it

**Files:**
- `scripts/native-bench`: add a `pass` measurement. It runs the Go dump, merge and derive on the 100k and 1M datasets and records wall time and peak RSS, against the DuckDB pass on the same input.
- The spec gains a "Measurements (step 4)" section.
- `docs/deployment.md` and `deploy/UPGRADES.md` follow the Global Constraints.

If the 1M peak exceeds ~150 MB, bound it and re-measure: smaller pass-2 read batches, fewer open inputs, or a lower group cap. Record what was done.

- [ ] Measure, fix if needed, and document. Run `make check` and commit: `perf(archive): measure the Go daily pass` (or `docs:` if nothing changed in code).

### Task 7: Form attribution and the rule-save checks on the native engine

**Files:**
- `internal/native`: a method answering exactly `attributionSQL`, over rules-applied sessions (stored plus live gaps), views and aliases. It is the same contract as `archive.(*Reader).Attribute`, the `jobs` attributor interface.
- `internal/app` wiring: `jobs` gets the native implementation.
- `app.filePresence`'s `HasEvents`/`HasAttribute` answered natively.
- `Reader.Attribute` and `attributionSQL` move into test files as the oracle.

**Tests:**
- Every `internal/archive/attribution_test.go` case, ported to run against both and compared.
- `internal/app/forms_files_test.go` and the presence tests pass.
- A parity test for `HasAttribute` with case-differing keys and a key only in the open log.

- [ ] Write the tests first. Implement, run `make check`, and commit: `feat(native): attribute form submissions and check rules without the engine`.
