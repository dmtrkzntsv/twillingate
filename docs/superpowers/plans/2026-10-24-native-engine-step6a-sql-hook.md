# Native engine, step 6a: the external SQL engine hook — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** All SQL in twillingate goes through an external SQL engine reached over a Unix socket: SQL widgets (validate, load, remote-table pages and counts), the `query` tool's `sql` mode, the start-up check of users' SQL widgets, and `reporting dev`. Without the socket set, SQL is refused with a typed error, and the console hides SQL. DuckDB stays in the tree in this step, only as the oracle that freezes golden answers. Step 6b removes it.

**Architecture:**
- **Protocol package.** A new public package, `sqlengine/` at the repository root, holds the protocol types, the table spec and a client. An engine in another module imports it.
- **Client side.** `internal/app` builds the client from `SQL_ENGINE_SOCKET`. `reporting` and `api` use the client where they used `archive.Reader` and `internal/engine`.
- **Engine side.** The engine receives SQL, its args, the mode (rows, text, shape, page or count), the scope and a table spec built from `archive.ListFiles` plus the rules. The engine owns everything DuckDB-specific: building the seven tables, confinement, the text, shape and page wraps, and the `tw_*` macros.
- **Test engine.** A fake engine, an HTTP server on a socket in tests, exercises the client and every caller.

**Tech Stack:** Go (stdlib `net/http` over `unix`), React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md`, "SQL: the external engine hook", and order of work step 6. The scratch survey of today's SQL paths is `.superpowers/step6-survey.md`.

## Global Constraints

- **The protocol (v1).** It is HTTP/JSON over a Unix socket and must be byte-stable. The types live in `sqlengine/`, and `docs/deployment.md` documents the contract.
  - `GET /v1/health` returns `{"protocol": 1, "engine": "<name> <version>"}`. twillingate calls it at start-up when the socket is set and refuses to start on a protocol mismatch. An engine that is down at start-up is logged and retried per request, not fatal.
  - `POST /v1/query` takes:
    ```json
    {"sql": "...", "args": {"name": "value"}, "max_rows": 1000,
     "mode": "rows" | "text" | "shape" | "page" | "count",
     "page": {"filters": [...], "sort": [...], "limit": N, "offset": N},
     "scope": 12 | null, "confine": true,
     "tables": <table spec>}
    ```
    - `args` binds today's `$tw_*` names.
    - `mode` replaces `enginesql`'s `Text`, `Shape`, `Page` and `Count` wraps; `page` carries exactly today's `enginesql.Page` input.
    - `scope` replaces `scopedSQL`.
    - `confine` replaces `enginesql.Confine`.
    - The deadline travels as the `X-Timeout-Ms` header, and cancellation as connection close.
  - A success returns `{"columns": [...], "rows": [[...]], "truncated": bool}`. Values are as today's `engine.Result`: numbers as JSON numbers, while dates, times, UUIDs, NaN and ±Inf are strings.
  - Errors: the status is 400, 503 (with `Retry-After`), 504 (timeout) or 500. The body is `{"error": {"kind": "refused" | "sql" | "out_of_memory" | "out_of_memory_loading_logs" | "busy" | "timeout" | "exited" | "internal", "message": "..."}}`. `timeout` (504) is the engine's own X-Timeout-Ms passing; `exited` (500) a worker that died mid-query; `internal` (500) anything else. Page mode returns the statement's columns with `__tw_matched` and `__tw_total` appended; count mode returns `["total","matched"]` and one row, `matched` 0 at offset 0.
    - twillingate maps each kind to today's refusals and advice.
    - A 503 fails at once with the busy advice. It is not retried inside the request.
- **The table spec.**
  ```json
  {"version": 1,
   "projects": [{"id": 12,
                 "parquet": ["12/2026-09.parquet", "..."], "logs": ["12/2026-10-24.log", "..."],
                 "sessions": ["12/sessions/2026-09.parquet", "..."],
                 "session_gaps": [{"from": "2026-10-01", "to": "2026-10-24"}],
                 "identities": "12/identities.parquet" | "", "aliases": "..." | ""}],
   "rules": {"events": [{"project_id", "family", "name", "action", "to"}],
             "attributes": [{"project_id", "key", "action", "to"}]}}
  ```
  - Paths are relative to the events directory and never absolute. The engine resolves them against its own read-only mount of `raw/` and refuses any path that escapes it.
  - With `scope` set, only that project is sent, with its rules. The query tool sends every project.
  - The files are exactly what `archive.ListFiles` lists today.
  - The rules are `store.EventRule` and `store.AttributeRule` with JSON tags added in `sqlengine`.
- **No engine configured.**
  - `sql` is not a registered source type, so `/api/components` and `list_components` omit it.
  - Saving or previewing a SQL widget is refused with `ErrSQLEngine`: "SQL needs an external SQL engine (`SQL_ENGINE_SOCKET`)". It is typed and matched with `errors.Is`, and maps to code `invalid`.
  - Loading a stored SQL widget answers the same refusal, so its card shows it. The widget is not archived.
  - The `query` tool's `sql` mode answers the same refusal.
  - The start-up user-widget check skips SQL widgets.
- **Engine configured.** Every SQL path behaves as today. The proof is the existing SQL tests, run against a test engine that delegates to the in-tree DuckDB, the DuckDB-backed fake, built from today's `archive.Reader` and `enginesql` code under `internal/archive/archivetest` or a test-only package. That fake is how 6a keeps today's SQL tests green before 6b moves them out.
- **Console.**
  - The console reads `source_types` from `/api/components`.
  - Without `sql`, the editor hides the SQL/Query toggle and starts drafts on `query`. A remote table is offered only when SQL is available.
  - An existing SQL widget opens with a notice saying it needs the external SQL engine. Its card shows the server's refusal.
  - `schemaViewsQuery` is fetched only when SQL is available.
- **Goldens.** Before 6b deletes the DuckDB oracles, each parity test's oracle answers are frozen into `testdata/*.golden.json` next to the test. Each test then checks native against the golden, and keeps the DuckDB comparison too until 6b.
  - A `-update-golden` flag rewrites the goldens from the native answers, for a deliberate, reviewed change only.
  - Goldens must keep every edge case the oracle covered.
- **Docs, in the same commits:**
  - `docs/deployment.md`: `SQL_ENGINE_SOCKET` and the protocol contract.
  - `docs/twillingate.md` and `docs/reporting.md`: SQL needs an external engine; the DuckDB SQL section stays as the dialect the reference engine speaks.
  - `deploy/UPGRADES.md`: one entry.
  - `internal/api/docs_sync_test.go` stays green.
  - The docs name no particular engine product or service beyond "an external SQL engine" and the DuckDB dialect.
- **Checks.**
  - `make check` passes before each commit: `PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`.
  - Web tasks also run `cd web && npm test && npm run e2e`.
  - Never use `/tmp`. Name no engine product or service.
- **Commits.** Use Conventional Commits. The hook itself is `feat(api)!`: SQL now needs an external engine, which is breaking for installs that use SQL widgets.

## Review Focus

1. **A stored SQL widget on a dashboard after an upgrade without an engine.** The card shows the typed refusal, the rest of the dashboard loads (stream and GET), and the start-up check does not fail.
2. **The engine down mid-flight.** A 500, or a refused or broken socket connection, maps to today's "the engine failed" advice. No goroutine or connection leaks.
3. **A table spec path escaping `raw/`.** The client never sends one: `ListFiles` paths are made relative and checked. The engine-side check is in the contract.
4. **Cancellation.** A closed stream or GET cancels the HTTP request, and the fake engine sees the cancel.
5. **The confine flag on the `query` tool.** It is always true from the tool, and SQL widgets keep today's setting.

---

### Task 1: The `sqlengine` package and the fake engine

**Files:**
- Create `sqlengine/protocol.go`: the request, response, error, table spec and rules types, with JSON tags exactly as in the Global Constraints.
- Create `sqlengine/client.go`: `Dial(socket string) *Client`, `Health(ctx)` and `Query(ctx, Request) (Result, error)`. Errors are typed as `*Error{Kind, Message, Status}`, and a 503 carries `RetryAfter`.
- Tests: protocol round-trip goldens (`sqlengine/testdata/*.json`, the contract fixtures an engine reuses), plus client tests against an `httptest` server on a Unix socket under `~/.cache` (never `/tmp`): cancellation, deadline header, every status and kind.
- `internal/archtest`: give `sqlengine` rank 0. It may import only the standard library.
- Update CLAUDE.md's layout.

- [ ] Commit: `feat(api): define the external SQL engine protocol`.

### Task 2: Table specs from the files, and the DuckDB-backed test engine

**Files:**
- `internal/archive`: `TableSpec(dir string, projects []int64, rules) (sqlengine.Tables, error)`, built from `ListFiles` with relative, checked paths.
- A test-only engine, `internal/archive/archivetest/sqlfake.go` or similar, that serves `sqlengine` over a socket by running today's `archive.Reader` and `enginesql` against the spec. Its parity with today's direct Reader calls is the test.

- [ ] Commit: `test(archive): serve the external SQL protocol from the reader in tests`.

### Task 3: Every SQL path through the client

**Changes:**
- `reporting.Files` is replaced by a narrow interface that the client implements. `querySource` gets its timeout and row cap from config, not from `Files`.
- `sqlSource` maps to the modes.
- The `query` tool's sql mode, `checkUserWidgets`, `reporting dev` and the migrator use the client.
- Errors map by kind.
- With no socket, apply the Global Constraints' refusals and source-type registration.

**Tests:**
- Every existing SQL test runs against the fake engine.
- New tests cover the no-engine refusals: save, preview, a stored widget's card through GET and the stream, the query tool, and the start-up check skipping SQL widgets.
- Review Focus 1, 2 and 4.

- [ ] Commit: `feat(api)!: answer SQL through an external SQL engine`.

### Task 4: The console without SQL

**Changes:**
- Add a `componentsQuery`.
- `codeTypesOf` filters by `source_types`, and drafts default to `query` when `sql` is absent.
- Remote tables need SQL.
- An existing SQL widget shows the notice.
- The schema fetch is gated on SQL being available.

**Tests:**
- vitest.
- An e2e spec with no engine, checking that SQL is hidden and a stored SQL card shows the refusal.
- The existing e2e SQL specs run with the fake engine, or move to query widgets.

- [ ] Commit: `feat(web): hide SQL when no external engine is configured`.

### Task 5: Freeze the goldens

For each parity oracle listed in the survey, group A, write its current DuckDB answers to testdata goldens. Make the test check native against the golden, keep the DuckDB check until 6b, and add the `-update-golden` flag.

- [ ] Commit: `test: freeze the DuckDB oracles' answers as goldens`.

### Task 6: Docs and upgrade note

Update `docs/deployment.md`, `twillingate.md`, `reporting.md`, `UPGRADES.md`, `.env.example` and `README.md`, as in the Global Constraints.

- [ ] Commit: `docs: SQL needs an external engine`. Docs that change with code go in that code's commit.
