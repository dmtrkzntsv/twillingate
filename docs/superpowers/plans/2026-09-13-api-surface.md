# One private API surface (MCP + REST) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve Twillingate's read and management operations as a REST API under `/api/` beside `/mcp`, behind the same auth, with ingestion renamed to its own public surface.

**Architecture:** `internal/mcpserver` becomes `internal/apiserver`. Every operation becomes a transport-neutral `func(ctx, In) (Out, error)`; one generic `expose` call registers it as an MCP tool and (unless MCP-only) a REST route. One auth-wrapped handler serves `/mcp` and `/api/`; the OAuth resource identifier becomes the API origin.

**Tech Stack:** Go 1.26 (`/usr/local/go/bin`, not on PATH — prefix commands with `export PATH=$PATH:/usr/local/go/bin;`), `github.com/modelcontextprotocol/go-sdk` (mcp, auth, oauthex), `golang-jwt/jwt/v5`, SQLite, TypeScript SDK built with esbuild (`sdk/`, `npm ci && npm run build`).

**Spec:** `docs/superpowers/specs/2026-09-13-api-surface-design.md` — read it before starting any task.

## Global Constraints

- Work in `/home/dmitry/dev/lab/twillingate/.claude/worktrees/bridge-cse_01RMs8eGMEZY75AMBSu8y7cs`. Never `cd` to the main checkout. Never bare `git stash`.
- Layering (CLAUDE.md "Layout", enforced by `internal/archtest`): surfaces never import each other; `app` wires them.
- Env vars: `INGEST_ADDR` (default `127.0.0.1:8080`), `API_ADDR` (default `INGEST_ADDR`), `API_AUTH_DSN`, `API_DB_PATH`, `API_QUERY_TIMEOUT` (default `10s`), `API_QUERY_MAX_ROWS` (default 1000). Old names `LISTEN_ADDR`, `MCP_ADDR`, `MCP_AUTH_DSN`, `MCP_DB_PATH`, `MCP_QUERY_TIMEOUT`, `MCP_QUERY_MAX_ROWS` fail config loading with `config: <OLD> was renamed to <NEW>`.
- Flags: `serve -ingest`, `serve -api`, `keygen -api`.
- Ingest route: `POST /ingest/events`, `OPTIONS /ingest/events`. `/api/events` no longer exists.
- REST routes live under `/api/` with no version segment. `integration_guide`, `docs://twillingate`, `docs://deployment`, `schema://projects` are MCP-only.
- REST error body: `{"error":{"code":"invalid|not_found|conflict|internal","message":"…"}}`; statuses 400/404/409/500. Mapping only via `errors.Is` against `manage.ErrInvalid`, `manage.ErrNotFound`, `manage.ErrConflict`.
- Audit actor: `mcp` for MCP calls, `api` for REST calls.
- Commit messages: Conventional Commits (CLAUDE.md), ending with the line `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- Verification per task: `export PATH=$PATH:/usr/local/go/bin; go build ./... && go vet ./... && go test ./...` must pass. `make check`'s restore step needs `sqlite3`, which is not installed locally — run `scripts/coverage.sh` for the coverage gate (90% total and for `internal/apiserver`) instead.
- Docs rules (CLAUDE.md "Documentation"): `docs/twillingate.md` and `docs/deployment.md` change in the same commit as the code they describe; `docs_sync_test.go` checks tables, not prose.

---

## File map

| File | Responsibility | Task |
|---|---|---|
| `internal/apiserver/*` (moved from `internal/mcpserver/`) | the API surface | 1 |
| `internal/config/config.go` | `APIConfig`, `IngestAddr`, rename guard; resource-as-origin | 2, 7 |
| `cmd/twillingate/keygen.go`, `internal/manage/ops.go` | `keygen -api`, `MintAPIToken` | 2 |
| `internal/server/server.go`, `sdk/src/twillingate.ts`, `internal/server/twillingate.js` | `/ingest/events` | 3 |
| `internal/apiserver/refusal.go` | typed errors that keep model-facing text | 4 |
| `internal/apiserver/expose.go` | `spec`, `registrar`, `expose`, actor context, MCP adapter | 5 |
| `internal/apiserver/ops_read.go`, `ops_product.go`, `ops_query.go`, `ops_manage.go` | transport-neutral operations (renamed from `tools_*.go`, `query.go`) | 5 |
| `internal/apiserver/rest.go` | request decoding, error mapping, `/api/schema/views` | 6 |
| `internal/apiserver/server.go` | one auth-wrapped handler for `/mcp` + `/api/`; `Mount` | 6, 8 |
| `internal/apiserver/oauth*.go` | resource prefix acceptance, `aud` = origin | 7 |
| `internal/app/app.go`, `cmd/twillingate/serve.go` | `-ingest`/`-api` wiring, shared mux | 8 |
| `docs/twillingate.md`, `docs/deployment.md`, `README.md`, `.env.example`, `deploy/`, `CLAUDE.md` | contract docs | 2, 3, 9 |

---

### Task 1: Rename `internal/mcpserver` to `internal/apiserver`

Pure mechanical rename; no behaviour change.

**Files:**
- Move: `internal/mcpserver/` → `internal/apiserver/` (all files)
- Modify: `internal/app/app.go` (import + identifiers), `internal/archtest/archtest_test.go` (rank table), `scripts/coverage.sh:21` (core list), `CLAUDE.md` (layout line, commit scope list, doc-trigger paths), comments in `internal/manage/coverage_test.go:17`, `internal/store/sqlite/coverage_test.go:261`

**Interfaces:**
- Produces: package `apiserver` at `github.com/dmtrkzntsv/twillingate/internal/apiserver` with the same exported API as before (`Build`, `NewHandler`, `RegisterOn`, `OpenReadDB`, `StaticVerifier`, …).

- [ ] **Step 1: Move the directory and rewrite package clauses**

```bash
git mv internal/mcpserver internal/apiserver
sed -i 's/^package mcpserver$/package apiserver/' internal/apiserver/*.go
grep -rl 'internal/mcpserver' --include=*.go . | xargs sed -i 's#internal/mcpserver#internal/apiserver#g'
sed -i 's/\bmcpserver\./apiserver./g' internal/app/app.go
```

- [ ] **Step 2: Update rank table, coverage script and CLAUDE.md**

In `internal/archtest/archtest_test.go` replace `"internal/mcpserver":         2,` with `"internal/apiserver":         2,` and the package comment's `mcpserver` with `apiserver`. In `scripts/coverage.sh` replace `internal/mcpserver` with `internal/apiserver`. In `CLAUDE.md`:
- layout line becomes `internal/apiserver/ .... private API: MCP endpoint and REST routes over the same operations, one auth`
- surfaces list `(server, mcpserver, jobs, pipeline, dashboards)` → `(server, apiserver, jobs, pipeline, dashboards)`
- commit scope list: `mcpserver` → `apiserver`
- doc trigger `internal/mcpserver/tools_*.go`, `resources.go` → `internal/apiserver/` operations and `resources.go`; `internal/mcpserver/auth.go` → `internal/apiserver/auth.go`; `schemaViews in internal/mcpserver/resources.go` → `internal/apiserver/resources.go`

Then: `git grep -n mcpserver -- ':!docs/superpowers'` must print nothing.

- [ ] **Step 3: Verify**

Run: `export PATH=$PATH:/usr/local/go/bin; go build ./... && go vet ./... && go test ./...`
Expected: all PASS (archtest included).

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(apiserver): rename mcpserver to apiserver

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: Rename configuration to `INGEST_ADDR` / `API_*`, refuse the old names, `keygen -api`

Resource semantics do NOT change in this task (still `PUBLIC_URL + "/mcp"`); Task 7 changes them.

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `internal/config/example_config_test.go`, `internal/config/configtest/configtest_test.go`
- Modify callers: `internal/apiserver/*.go` (`cfg.MCP` → `cfg.API`, `config.MCPConfig` → `config.APIConfig`, `ValidateMCP` → `ValidateAPI`, `"MCP_AUTH_DSN"` → `"API_AUTH_DSN"` in test env maps and error text), `internal/app/*.go` (`cfg.Listen` → `cfg.IngestAddr`, `cfg.MCP` → `cfg.API`, `LISTEN_ADDR`/`MCP_*` keys in test env maps), `cmd/twillingate/serve.go`, `cmd/twillingate/commands_test.go`, `cmd/twillingate/keygen.go`, `cmd/twillingate/key_test.go`, `internal/manage/ops.go` (`MintMCPToken` → `MintAPIToken`) and its tests
- Modify non-Go: `Dockerfile` (`ENV LISTEN_ADDR` → `ENV INGEST_ADDR`), `scripts/smoke.sh`, `scripts/test-compose.sh`, `scripts/test-install*.sh` if they set the old names, `deploy/systemd/install.sh` if it writes them, `.env.example` (lines 54–63), `docs/deployment.md` (variable table under `## Configure the collector`, every prose mention, `keygen -mcp` → `keygen -api`), `docs/twillingate.md` and `README.md` wherever they name `MCP_AUTH_DSN`
- Modify: `internal/apiserver/docs_sync_test.go` (`TestDeploymentResourceServed` wants `API_AUTH_DSN`)

**Interfaces:**
- Produces:
  - `config.Config.IngestAddr string` (was `Listen`)
  - `config.Config.API config.APIConfig` (was `MCP MCPConfig`); `APIConfig` has the same fields as `MCPConfig` (`Addr, DBPath, AuthDSN, AuthMode, ResourceURL, Issuer, Audience, Token, RedirectHosts, Password, QueryTimeout, QueryMaxRows`)
  - `func (m APIConfig) LoginEnabled() bool`
  - `func (c *Config) ValidateAPI() error` — message `config: -api requires API_AUTH_DSN (token://<token> or oauth://<issuer-host>)`
  - `func manage.MintAPIToken() (string, error)`

- [ ] **Step 1: Write the failing config tests**

Append to `internal/config/config_test.go`:

```go
func TestRenamedVariablesRefuse(t *testing.T) {
	for old, repl := range map[string]string{
		"LISTEN_ADDR":        "INGEST_ADDR",
		"MCP_ADDR":           "API_ADDR",
		"MCP_AUTH_DSN":       "API_AUTH_DSN",
		"MCP_DB_PATH":        "API_DB_PATH",
		"MCP_QUERY_TIMEOUT":  "API_QUERY_TIMEOUT",
		"MCP_QUERY_MAX_ROWS": "API_QUERY_MAX_ROWS",
	} {
		env := map[string]string{"DATABASE_DSN": "sqlite:///tmp/x.db", old: "x"}
		_, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		want := "config: " + old + " was renamed to " + repl
		if err == nil || err.Error() != want {
			t.Errorf("%s set: err = %v, want %q", old, err, want)
		}
	}
}

func TestAPIDefaults(t *testing.T) {
	env := map[string]string{"DATABASE_DSN": "sqlite:///tmp/x.db", "INGEST_ADDR": "127.0.0.1:9"}
	c, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if c.IngestAddr != "127.0.0.1:9" || c.API.Addr != "127.0.0.1:9" || c.API.DBPath != "/tmp/x.db" ||
		c.API.QueryTimeout != 10*time.Second || c.API.QueryMaxRows != 1000 {
		t.Errorf("defaults = %+v / %+v", c.IngestAddr, c.API)
	}
	if err := c.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "API_AUTH_DSN") {
		t.Errorf("ValidateAPI without DSN = %v", err)
	}
}
```

In `cmd/twillingate/key_test.go` change the keygen test to run `keygen -api` and match `API_AUTH_DSN=token://ar_[0-9a-f]{64}`.

- [ ] **Step 2: Run to see them fail**

Run: `export PATH=$PATH:/usr/local/go/bin; go test ./internal/config/ ./cmd/twillingate/`
Expected: compile errors (`c.IngestAddr`, `c.API`, `ValidateAPI` undefined).

- [ ] **Step 3: Implement in `internal/config/config.go`**

Rename the type `MCPConfig` → `APIConfig` (doc comment: "carries the -api surface settings. Authentication comes from the single API_AUTH_DSN…"), field `Config.MCP` → `Config.API`, `Config.Listen` → `Config.IngestAddr`, method `ValidateMCP` → `ValidateAPI`, `parseMCPAuthDSN` → `parseAPIAuthDSN`. In `parse`:

```go
func parse(lookup func(string) (string, bool), dashboards bool) (*Config, error) {
	if err := refuseRenamed(lookup); err != nil {
		return nil, err
	}
	e := &env{lookup: lookup}
	c := &Config{
		IngestAddr: e.str("INGEST_ADDR", "127.0.0.1:8080"),
		// … unchanged …
	}
	c.API = APIConfig{
		Addr:         e.str("API_ADDR", c.IngestAddr),
		DBPath:       e.str("API_DB_PATH", strings.TrimPrefix(c.Database, "sqlite://")),
		AuthDSN:      e.str("API_AUTH_DSN", ""),
		QueryTimeout: e.dur("API_QUERY_TIMEOUT", 10*time.Second),
		QueryMaxRows: e.num("API_QUERY_MAX_ROWS", 1000),
	}
	if c.API.AuthDSN != "" {
		c.API.authErr = c.parseAPIAuthDSN()
	}
	// … rest unchanged …
}

// renamed maps each variable this release retired to its replacement. A
// set old name refuses the boot: bare `serve` is lenient about API auth,
// so an unrenamed MCP_AUTH_DSN would otherwise switch the API off silently.
var renamed = []struct{ old, repl string }{
	{"LISTEN_ADDR", "INGEST_ADDR"},
	{"MCP_ADDR", "API_ADDR"},
	{"MCP_AUTH_DSN", "API_AUTH_DSN"},
	{"MCP_DB_PATH", "API_DB_PATH"},
	{"MCP_QUERY_TIMEOUT", "API_QUERY_TIMEOUT"},
	{"MCP_QUERY_MAX_ROWS", "API_QUERY_MAX_ROWS"},
}

// refuseRenamed treats an empty value as unset, as env.str does, so a
// leftover `MCP_ADDR=` line does not block the boot.
func refuseRenamed(lookup func(string) (string, bool)) error {
	for _, r := range renamed {
		if v, ok := lookup(r.old); ok && v != "" {
			return fmt.Errorf("config: %s was renamed to %s", r.old, r.repl)
		}
	}
	return nil
}
```

Replace every `MCP_AUTH_DSN` in error strings with `API_AUTH_DSN`, `keygen -mcp` with `keygen -api`, `-mcp requires` with `-api requires`.

Note: `docs_sync_test.go`'s env-var regexp only matches `.str|num|dur|bool("NAME"` calls, so the `renamed` table's strings are not counted as read variables — keep them as a struct literal, not `e.str` calls.

- [ ] **Step 4: Fix every caller**

```bash
grep -rl --include=*.go -E 'cfg\.MCP|c\.MCP|config\.MCPConfig|ValidateMCP|cfg\.Listen|MintMCPToken' . \
  | xargs sed -i -E 's/\bcfg\.MCP\b/cfg.API/g; s/\bc\.MCP\b/c.API/g; s/config\.MCPConfig/config.APIConfig/g; s/ValidateMCP/ValidateAPI/g; s/cfg\.Listen\b/cfg.IngestAddr/g; s/MintMCPToken/MintAPIToken/g'
grep -rl --include=*.go -E '"(MCP_AUTH_DSN|MCP_ADDR|MCP_DB_PATH|MCP_QUERY_TIMEOUT|MCP_QUERY_MAX_ROWS|LISTEN_ADDR)"' . \
  | xargs sed -i -E 's/"MCP_AUTH_DSN"/"API_AUTH_DSN"/g; s/"MCP_ADDR"/"API_ADDR"/g; s/"MCP_DB_PATH"/"API_DB_PATH"/g; s/"MCP_QUERY_TIMEOUT"/"API_QUERY_TIMEOUT"/g; s/"MCP_QUERY_MAX_ROWS"/"API_QUERY_MAX_ROWS"/g; s/"LISTEN_ADDR"/"INGEST_ADDR"/g'
```

Then fix by hand: `func (m MCPConfig)` receivers inside config.go if sed missed them; `m config.MCPConfig` params in `apiserver/server.go`, `oauth.go`, `oauth_test.go` (`over func(*config.MCPConfig)`); `cmd/twillingate/keygen.go` flag `-api` ("mint the API access token instead") printing `API_AUTH_DSN=token://%s`; `cmd/twillingate/serve.go` warning text `API_ADDR not set; API shares the ingestion listener` (flags stay `-api`/`-mcp` until Task 8); `internal/config/configtest/configtest_test.go` (`INGEST_ADDR`, `cfg.IngestAddr`); `commands_test.go` expectation `API_AUTH_DSN`. Any shell script, Dockerfile, compose or install.sh line that sets `LISTEN_ADDR=` or `MCP_*=` gets the new name.

`git grep -n -E 'LISTEN_ADDR|MCP_(ADDR|AUTH_DSN|DB_PATH|QUERY_TIMEOUT|QUERY_MAX_ROWS)|keygen -mcp|MintMCPToken' -- ':!docs/superpowers'` must print only `internal/config/config.go` (the `renamed` table) and `config_test.go`.

- [ ] **Step 5: Update docs in the same commit**

- `docs/deployment.md`: in the `## Configure the collector` table rename rows `LISTEN_ADDR` → `INGEST_ADDR`, `MCP_AUTH_DSN` → `API_AUTH_DSN`, `MCP_ADDR` → `API_ADDR` ("Give the API (MCP and REST) its own listener. Defaults to `INGEST_ADDR` (shared)."), `MCP_DB_PATH` → `API_DB_PATH`, `MCP_QUERY_TIMEOUT` → `API_QUERY_TIMEOUT` ("Per-query guard on reads and the `query` operation"), `MCP_QUERY_MAX_ROWS` → `API_QUERY_MAX_ROWS`. Replace every prose `MCP_AUTH_DSN`/`MCP_ADDR`/`MCP_DB_PATH`/`LISTEN_ADDR` and `keygen -mcp`. (Section restructuring waits for Task 9.)
- `.env.example`, `README.md`, `docs/twillingate.md`: same renames.

- [ ] **Step 6: Verify**

Run: `export PATH=$PATH:/usr/local/go/bin; go build ./... && go vet ./... && go test ./...`
Expected: PASS, including `TestRenamedVariablesRefuse`, `TestAPIDefaults`, `TestDeploymentDocumentsEveryEnvVar`.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(config)!: rename LISTEN_ADDR and MCP_* to INGEST_ADDR and API_*

The collector refuses to start while an old name is set.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: Move ingestion to `POST /ingest/events`

**Files:**
- Modify: `internal/server/server.go:88-89`, `internal/server/server_test.go`, `internal/server/twillingate_script_test.go` (if it asserts the path)
- Modify: `sdk/src/twillingate.ts` (line ~2 comment, line ~452 `"/api/events"`), `sdk/src/twillingate.test.ts` (4 places); rebuild `internal/server/twillingate.js`
- Modify: `internal/app/app_test.go` (7 places), `scripts/smoke.sh`, `scripts/test-compose.sh`
- Modify: `internal/apiserver/guide.go` + `guide_test.go`, `internal/apiserver/resources.go:67` (docs resource description), `internal/manage/ops.go:283` comment, `.env.example:12`
- Modify docs: `docs/twillingate.md` (every `/api/events`), `docs/deployment.md` (3 places: verifying ingestion curls etc.), `docs/plausible/README.md:75,127`, `README.md:84,104`

**Interfaces:**
- Produces: ingest routes `POST /ingest/events`, `OPTIONS /ingest/events`.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/server_test.go` (reuse the file's existing server-construction helper; read the top of the file for its name and signature):

```go
func TestOldIngestPathIsGone(t *testing.T) {
	s := newTestServer(t) // use the helper this file already defines
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/events", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /api/events = %d, want 404", rec.Code)
	}
}
```

and change every existing `/api/events` request in that file to `/ingest/events`.

- [ ] **Step 2: Run to see it fail**

Run: `export PATH=$PATH:/usr/local/go/bin; go test ./internal/server/`
Expected: FAIL — `/ingest/events` returns 404 and `/api/events` returns non-404.

- [ ] **Step 3: Implement**

`internal/server/server.go`:

```go
	mux.HandleFunc("POST /ingest/events", s.handleEvents)
	mux.HandleFunc("OPTIONS /ingest/events", s.handlePreflight)
```

`sdk/src/twillingate.ts`: `const endpoint = this.url + "/ingest/events";` and the header comment `collector's POST /ingest/events`. Update `sdk/src/twillingate.test.ts` expectations to `URL_BASE + "/ingest/events"` and the test title.

Rebuild the bundle:

```bash
cd sdk && npm ci && npm test && npm run typecheck && npm run build && cd ..
git diff --stat internal/server/twillingate.js   # must show a change containing /ingest/events
```

Then `git grep -n 'api/events' -- ':!docs/superpowers'` and replace every remaining hit with `ingest/events` (Go tests, scripts, guide text, docs, README, comments). In `docs/twillingate.md`, wherever the wire format is introduced, add one sentence: "`/api/events` was the path before this release and now returns 404."

- [ ] **Step 4: Verify**

Run: `export PATH=$PATH:/usr/local/go/bin; go build ./... && go vet ./... && go test ./...` and `git grep -n 'api/events' -- ':!docs/superpowers'`
Expected: tests PASS; grep shows only the one "was the path before" sentence in `docs/twillingate.md`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(server)!: accept events at /ingest/events instead of /api/events

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: Typed refusals for read-side validation

Make every validation error in the operations matchable with `errors.Is` while keeping the exact message a model reads.

**Files:**
- Create: `internal/apiserver/refusal.go`, `internal/apiserver/refusal_test.go`
- Modify: `internal/apiserver/tools_read.go` (`checkRange`, `unknownProjectErr`, `table`, both breakdown dimension errors), `tools_product.go` (surface, kind errors), `query.go` (empty sql, ATTACH, timeout, SQL error), `guide.go` (platform validation, if any)

**Interfaces:**
- Produces:
  - `func invalidf(format string, args ...any) error` — `errors.Is(err, manage.ErrInvalid)`; `err.Error()` is exactly the formatted text
  - `func notFoundf(format string, args ...any) error` — `errors.Is(err, manage.ErrNotFound)`; same message rule

- [ ] **Step 1: Write the failing tests**

`internal/apiserver/refusal_test.go`:

```go
package apiserver

import (
	"context"
	"errors"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

func TestRefusalKeepsMessage(t *testing.T) {
	err := invalidf("from and to must be YYYY-MM-DD, got %q and %q", "x", "y")
	if !errors.Is(err, manage.ErrInvalid) || err.Error() != `from and to must be YYYY-MM-DD, got "x" and "y"` {
		t.Errorf("invalidf = %v (Is invalid: %v)", err, errors.Is(err, manage.ErrInvalid))
	}
	nf := notFoundf("unknown project %q", "zzz")
	if !errors.Is(nf, manage.ErrNotFound) || errors.Is(nf, manage.ErrInvalid) || nf.Error() != `unknown project "zzz"` {
		t.Errorf("notFoundf = %v", nf)
	}
}

func TestOperationRefusalsAreTyped(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	cases := map[string]struct {
		err  error
		kind error
	}{
		"bad day":        {h.checkRange(ctx, rangeIn{Project: "blog", From: "yesterday", To: "2026-08-21"}), manage.ErrInvalid},
		"unknown project": {h.checkRange(ctx, rangeIn{Project: "nope", From: "2026-08-20", To: "2026-08-21"}), manage.ErrNotFound},
	}
	for name, c := range cases {
		if !errors.Is(c.err, c.kind) {
			t.Errorf("%s: %v is not %v", name, c.err, c.kind)
		}
	}
}
```

- [ ] **Step 2: Run to see it fail**

Run: `export PATH=$PATH:/usr/local/go/bin; go test ./internal/apiserver/ -run 'Refusal'`
Expected: compile error, `invalidf` undefined.

- [ ] **Step 3: Implement `internal/apiserver/refusal.go`**

```go
package apiserver

import (
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// refusal is an operation error an edge can classify with errors.Is while
// its text stays the sentence written for a model to recover from: wrapping
// with fmt.Errorf("%w: …") would prepend the sentinel's own text.
type refusal struct {
	kind error
	msg  string
}

func (e *refusal) Error() string { return e.msg }
func (e *refusal) Unwrap() error { return e.kind }

func invalidf(format string, args ...any) error {
	return &refusal{kind: manage.ErrInvalid, msg: fmt.Sprintf(format, args...)}
}

func notFoundf(format string, args ...any) error {
	return &refusal{kind: manage.ErrNotFound, msg: fmt.Sprintf(format, args...)}
}
```

Replace, keeping message text identical:
- `tools_read.go` `checkRange`: `fmt.Errorf("from and to must be …")` → `invalidf(…)`
- `unknownProjectErr`: `fmt.Errorf("unknown project %q; valid aliases: %s", …)` → `notFoundf(…)`
- `table`: the timeout error → `invalidf("query exceeded %s; narrow the date range", h.timeout)`
- `webBreakdown`/`appBreakdown` unknown dimension → `invalidf`
- `tools_product.go`: `surface must be web or app…`, `kind must be user or group…`, the anonymous-retention error → `invalidf`
- `query.go`: `sql must not be empty`, `ATTACH is not allowed`, the timeout message, the `SQL error (…)` message → `invalidf`
- `guide.go`: its platform/unknown-project validation errors → `invalidf`/`notFoundf` respectively (read the file; keep text)

- [ ] **Step 4: Verify**

Run: `export PATH=$PATH:/usr/local/go/bin; go vet ./internal/apiserver/ && go test ./internal/apiserver/`
Expected: PASS — existing tool tests assert message text and must be unchanged.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(apiserver): type read-side refusals with the manage sentinels

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: Transport-neutral operations and `expose`

Strip MCP types out of every operation. MCP behaviour is unchanged; REST routes are declared in the specs but not yet mounted (Task 6 adds the adapter).

**Files:**
- Create: `internal/apiserver/expose.go`, `internal/apiserver/expose_test.go`
- Rename: `tools_read.go` → `ops_read.go`, `tools_product.go` → `ops_product.go`, `query.go` → `ops_query.go`, `tools_manage.go` → `ops_manage.go` (and their `_test.go` files likewise) with `git mv`
- Modify: `internal/apiserver/server.go` (`Build` constructs the registrar), `guide.go`, `docs_sync_test.go` (`TestDocumentNamesEveryTool` enumerates specs instead of regex over files)

**Interfaces:**
- Consumes: `invalidf`, `notFoundf` (Task 4).
- Produces:

```go
type spec struct {
	Name        string               // MCP tool name
	Description string
	Annotations *mcp.ToolAnnotations
	Method      string               // HTTP method; "" = MCP only
	Path        string               // ServeMux pattern path, e.g. "/api/projects/{project}/web/overview"
	Status      int                  // REST success status; 0 = 200
}

type registrar struct {
	mcp    *mcp.Server
	rest   *http.ServeMux // nil until Task 6 mounts routes
	logger *slog.Logger
	specs  []spec
}

func expose[In, Out any](r *registrar, s spec, fn func(context.Context, In) (Out, error))
func withActor(ctx context.Context, actor string) context.Context
func actorFrom(ctx context.Context) string // "unknown" when unset
func (h *host) register(r *registrar)       // replaces register(*mcp.Server)
```

Operation signatures after this task (all methods on `*host`):
`listProjects(ctx, struct{}) (listProjectsOut, error)`, `webOverview(ctx, rangeIn) (tableOut, error)`, `webBreakdown(ctx, breakdownIn) (tableOut, error)`, `appOverview(ctx, rangeIn) (tableOut, error)`, `appBreakdown(ctx, breakdownIn) (tableOut, error)`, `productEvents(ctx, productEventsIn) (productEventsOut, error)`, `productAttributes(ctx, productEventsIn) (tableOut, error)`, `retention(ctx, retentionIn) (retentionOut, error)`, `identities(ctx, identitiesIn) (tableOut, error)`, `runQuery(ctx, queryIn) (tableOut, error)`, `createProject(ctx, projectIn) (projectToolOut, error)`, `updateProject(ctx, projectIn) (projectToolOut, error)`, `archiveProject(ctx, aliasIn) (okOut, error)`, `restoreProject(ctx, aliasIn) (okOut, error)`, `issueKey(ctx, keyIn) (keyOut, error)`, `disableKey(ctx, keyIn) (okOut, error)`, `enableKey(ctx, keyIn) (okOut, error)`, `listKeys(ctx, listKeysIn) (listKeysOut, error)`, `integrationGuide(ctx, guideIn) (guideOut, error)`.

- [ ] **Step 1: Write the failing tests**

`internal/apiserver/expose_test.go`:

```go
package apiserver

import (
	"context"
	"testing"
)

func TestActorContext(t *testing.T) {
	if got := actorFrom(context.Background()); got != "unknown" {
		t.Errorf("unset actor = %q", got)
	}
	if got := actorFrom(withActor(context.Background(), "api")); got != "api" {
		t.Errorf("actor = %q", got)
	}
}

// TestEveryToolChoosesATransport: a tool either has a REST route or is on
// the MCP-only list, and routes are unique. A new tool must decide.
func TestEveryToolChoosesATransport(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	mcpOnly := map[string]bool{"integration_guide": true}
	seen := map[string]string{}
	for _, s := range r.specs {
		if s.Method == "" {
			if !mcpOnly[s.Name] {
				t.Errorf("tool %s has no REST route and is not MCP-only", s.Name)
			}
			continue
		}
		if mcpOnly[s.Name] {
			t.Errorf("tool %s is MCP-only but has route %s %s", s.Name, s.Method, s.Path)
		}
		key := s.Method + " " + s.Path
		if other, dup := seen[key]; dup {
			t.Errorf("%s and %s share route %s", s.Name, other, key)
		}
		seen[key] = s.Name
	}
	if len(r.specs) != 19 {
		t.Errorf("registered %d tools, want 19", len(r.specs))
	}
}
```

Add to `seed_test.go` a helper used above:

```go
// newTestRegistrar registers h's operations on a fresh MCP server and REST
// mux, the way Build does, so tests can inspect specs and hit routes.
func newTestRegistrar(t *testing.T, h *host) *registrar {
	t.Helper()
	r := &registrar{
		mcp:    mcp.NewServer(&mcp.Implementation{Name: "twillingate", Version: "test"}, nil),
		rest:   http.NewServeMux(),
		logger: slog.New(slog.DiscardHandler),
	}
	h.register(r)
	return r
}
```

Also add to `ops_manage_test.go` (after the rename) an audit assertion that an MCP write records actor `mcp`: call `create_project` through the existing `callTool` helper, then `rawExec`/query `SELECT actor FROM audit_log WHERE action='project.create' AND subject=?` via `h.ops.St` (read how existing tests reach the store) and expect `mcp`.

- [ ] **Step 2: Run to see it fail**

Run: `export PATH=$PATH:/usr/local/go/bin; go test ./internal/apiserver/ -run 'Actor|ChoosesATransport'`
Expected: compile errors.

- [ ] **Step 3: Implement `internal/apiserver/expose.go`**

```go
package apiserver

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// spec describes one operation on both transports. Method "" keeps it
// MCP-only; the parity test makes that an explicit choice.
type spec struct {
	Name        string
	Description string
	Annotations *mcp.ToolAnnotations
	Method      string
	Path        string
	Status      int
}

// registrar collects the operations for both transports. specs is what
// the parity and docs tests enumerate.
type registrar struct {
	mcp    *mcp.Server
	rest   *http.ServeMux
	logger *slog.Logger
	specs  []spec
}

type actorKey struct{}

// withActor names the edge an operation arrived through; management
// operations record it in audit_log.
func withActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

func actorFrom(ctx context.Context) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok {
		return a
	}
	return "unknown"
}

// expose registers fn as an MCP tool and, when s.Method is set, as a REST
// route. One call per operation, so neither transport can drift.
func expose[In, Out any](r *registrar, s spec, fn func(context.Context, In) (Out, error)) {
	r.specs = append(r.specs, s)
	mcp.AddTool(r.mcp, &mcp.Tool{Name: s.Name, Description: s.Description, Annotations: s.Annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			out, err := fn(withActor(ctx, "mcp"), in)
			return nil, out, err
		})
	if s.Method != "" && r.rest != nil {
		r.rest.HandleFunc(s.Method+" "+s.Path, restHandler(r, s, fn))
	}
}
```

Add a temporary stub at the bottom of `expose.go` so the package compiles until Task 6 replaces it (Task 6 deletes this stub and adds the real one in `rest.go`):

```go
func restHandler[In, Out any](r *registrar, s spec, fn func(context.Context, In) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }
}
```

- [ ] **Step 4: Convert the operations**

For each operation, drop the `*mcp.CallToolRequest` parameter and the `*mcp.CallToolResult` return: `func (h *host) webOverview(ctx context.Context, _ *mcp.CallToolRequest, in rangeIn) (*mcp.CallToolResult, tableOut, error)` becomes `func (h *host) webOverview(ctx context.Context, in rangeIn) (tableOut, error)`, and each `return nil, out, err` becomes `return out, err`.

In `ops_manage.go` replace every literal `"mcp"` actor argument with `actorFrom(ctx)`.

Replace `register(s *mcp.Server)` in `ops_read.go` with the single registration list below, and delete `registerProduct`, `registerQuery`, `registerManage`, `registerGuide`. Copy each `Description` string verbatim from the current `mcp.AddTool` calls (shown abbreviated here as `…` only because they are long — use the full existing strings):

```go
func (h *host) register(r *registrar) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	no := false // DestructiveHint is *bool in the SDK; nothing here destroys
	write := &mcp.ToolAnnotations{DestructiveHint: &no}
	idem := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}
	const p = "/api/projects/{project}"

	expose(r, spec{Name: "list_projects", Annotations: ro, Method: "GET", Path: "/api/projects", Description: "List projects with identity mode and data coverage. …"}, h.listProjects)
	expose(r, spec{Name: "web_overview", Annotations: ro, Method: "GET", Path: p + "/web/overview", Description: "Daily web traffic for one project: …"}, h.webOverview)
	expose(r, spec{Name: "web_breakdown", Annotations: ro, Method: "GET", Path: p + "/web/breakdown", Description: "Top pages, hosts, referrers, …"}, h.webBreakdown)
	expose(r, spec{Name: "app_overview", Annotations: ro, Method: "GET", Path: p + "/app/overview", Description: "Daily app usage for one project: …"}, h.appOverview)
	expose(r, spec{Name: "app_breakdown", Annotations: ro, Method: "GET", Path: p + "/app/breakdown", Description: "Top screens, versions, …"}, h.appBreakdown)
	expose(r, spec{Name: "product_events", Annotations: ro, Method: "GET", Path: p + "/product/events", Description: "Product events per day: …"}, h.productEvents)
	expose(r, spec{Name: "product_attributes", Annotations: ro, Method: "GET", Path: p + "/product/attributes", Description: "Attribute breakdowns for product events. …"}, h.productAttributes)
	expose(r, spec{Name: "retention", Annotations: ro, Method: "GET", Path: p + "/retention", Description: "D1/D7/D30-style cohort curves …"}, h.retention)
	expose(r, spec{Name: "identities", Annotations: ro, Method: "GET", Path: p + "/identities", Description: "Per-user or per-group activity …"}, h.identities)
	expose(r, spec{Name: "query", Annotations: ro, Method: "POST", Path: "/api/query", Description: "Escape hatch: run one read-only SELECT/WITH …"}, h.runQuery)

	expose(r, spec{Name: "create_project", Annotations: write, Method: "POST", Path: "/api/projects", Status: http.StatusCreated, Description: "Create a project and (by default) its first ingest key; …"}, h.createProject)
	expose(r, spec{Name: "update_project", Annotations: write, Method: "PATCH", Path: "/api/projects/{alias}", Description: "Update a project's name, …"}, h.updateProject)
	expose(r, spec{Name: "archive_project", Annotations: idem, Method: "POST", Path: "/api/projects/{alias}/archive", Description: "Archive a project: …"}, h.archiveProject)
	expose(r, spec{Name: "restore_project", Annotations: idem, Method: "POST", Path: "/api/projects/{alias}/restore", Description: "Restore an archived project."}, h.restoreProject)
	expose(r, spec{Name: "list_ingest_keys", Annotations: ro, Method: "GET", Path: "/api/keys", Description: "List ingest keys with their state, including disabled ones."}, h.listKeys)
	expose(r, spec{Name: "issue_ingest_key", Annotations: write, Method: "POST", Path: p + "/keys", Status: http.StatusCreated, Description: "Issue a new ingest key for a project. …"}, h.issueKey)
	expose(r, spec{Name: "disable_ingest_key", Annotations: idem, Method: "POST", Path: p + "/keys/{label}/disable", Description: "Disable an ingest key by project and label; …"}, h.disableKey)
	expose(r, spec{Name: "enable_ingest_key", Annotations: idem, Method: "POST", Path: p + "/keys/{label}/enable", Description: "Re-enable a disabled ingest key."}, h.enableKey)

	expose(r, spec{Name: "integration_guide", Description: "…existing guide description…"}, h.integrationGuide) // MCP only
}
```

Tool descriptions that say "over MCP" (archive_project: "There is no delete over MCP — deletion requires the CLI.") become "There is no delete over the API — deletion requires the CLI."

`registerResources(s *mcp.Server)` stays as is.

In `server.go` `Build`, replace `h.register(srv)` with:

```go
	r := &registrar{mcp: srv, logger: logger}
	h.register(r)
```

(`rest` stays nil in this task.)

In `docs_sync_test.go`, replace the file-regex loop in `TestDocumentNamesEveryTool` with:

```go
	h, _ := newTestHost(t)
	registered := map[string]bool{}
	for _, s := range newTestRegistrar(t, h).specs {
		registered[s.Name] = true
	}
```

Existing tests that call operations directly (not via `callTool`) must be updated to the new signatures; run `go vet` to find them.

- [ ] **Step 5: Verify**

Run: `export PATH=$PATH:/usr/local/go/bin; go build ./... && go vet ./... && go test ./...`
Expected: PASS — every existing MCP tool test unchanged in behaviour, plus the new tests.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(apiserver): register each operation once for every transport

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: REST adapter, routes under auth, `GET /api/schema/views`

**Files:**
- Create: `internal/apiserver/rest.go`, `internal/apiserver/rest_test.go`
- Modify: `internal/apiserver/expose.go` (delete the stub `restHandler`), `internal/apiserver/server.go` (auth wraps one mux serving `/mcp` and `/api/`; `RegisterOn` mounts both), `internal/apiserver/server_test.go`, `internal/apiserver/resources.go` (nothing but the schema route registration helper, see below)

**Interfaces:**
- Consumes: `spec`, `registrar`, `expose`, `withActor` (Task 5); `invalidf` (Task 4); `writeJSON(w, status, v)` (existing, `oauth.go`).
- Produces:
  - `func restHandler[In, Out any](r *registrar, s spec, fn func(context.Context, In) (Out, error)) http.HandlerFunc`
  - `func decodeRequest(r *http.Request, dst any) error` — errors satisfy `errors.Is(err, manage.ErrInvalid)`
  - `func writeError(w http.ResponseWriter, logger *slog.Logger, err error)`
  - `const maxAPIBody = 1 << 20`
  - `Build` returns a handler that serves both `/mcp` and `/api/` behind auth; `RegisterOn(mux, protected, cfg, withHealthz, logger)` mounts `protected` at `/mcp` and `/api/`.

- [ ] **Step 1: Write the failing tests**

`internal/apiserver/rest_test.go`:

```go
package apiserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serveREST(t *testing.T, r *registrar, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	r.rest.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("error body %q: %v", rec.Body.String(), err)
	}
	return b.Error.Code
}

func TestDecodeRequestFillsPathAndQuery(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/projects/blog/web/breakdown?from=2026-08-20&to=2026-08-21&dimension=pages&limit=5", nil)
	req.SetPathValue("project", "blog")
	var in breakdownIn
	if err := decodeRequest(req, &in); err != nil {
		t.Fatal(err)
	}
	if in.Project != "blog" || in.From != "2026-08-20" || in.To != "2026-08-21" || in.Dimension != "pages" || in.Limit != 5 {
		t.Errorf("decoded %+v", in)
	}
}

func TestDecodeRequestRefusals(t *testing.T) {
	cases := map[string]*http.Request{
		"unknown query parameter": httptest.NewRequest("GET", "/x?form=2026-08-20", nil),
		"bad int":                 httptest.NewRequest("GET", "/x?limit=many", nil),
		"unknown body field":      httptest.NewRequest("POST", "/x", strings.NewReader(`{"alias":"a","colour":"red"}`)),
		"not json":                httptest.NewRequest("POST", "/x", strings.NewReader(`alias=a`)),
		"query on a POST":         httptest.NewRequest("POST", "/x?alias=a", strings.NewReader(`{}`)),
		"body too large":          httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"`+strings.Repeat("a", maxAPIBody)+`"}`)),
	}
	for name, req := range cases {
		var in breakdownIn
		if name == "unknown body field" || name == "not json" || name == "query on a POST" || name == "body too large" {
			var pin projectIn
			if err := decodeRequest(req, &pin); !errors.Is(err, manage.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
			continue
		}
		if err := decodeRequest(req, &in); !errors.Is(err, manage.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestDecodeRequestPathOverridesBody(t *testing.T) {
	req := httptest.NewRequest("PATCH", "/api/projects/blog", strings.NewReader(`{"alias":"other","name":"Blog"}`))
	req.SetPathValue("alias", "blog")
	var in projectIn
	if err := decodeRequest(req, &in); err != nil {
		t.Fatal(err)
	}
	if in.Alias != "blog" || in.Name != "Blog" {
		t.Errorf("decoded %+v", in)
	}
}

func TestWriteErrorMapping(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{invalidf("bad"), 400, "invalid"},
		{notFoundf("gone"), 404, "not_found"},
		{fmt.Errorf("wrapped: %w", manage.ErrConflict), 409, "conflict"},
		{errors.New("disk on fire"), 500, "internal"},
	} {
		rec := httptest.NewRecorder()
		writeError(rec, slog.New(slog.DiscardHandler), c.err)
		if rec.Code != c.status || errorCode(t, rec) != c.code {
			t.Errorf("%v → %d %s, want %d %s", c.err, rec.Code, rec.Body.String(), c.status, c.code)
		}
		if c.code == "internal" && strings.Contains(rec.Body.String(), "disk on fire") {
			t.Errorf("internal error text leaked: %s", rec.Body.String())
		}
	}
}

// TestRESTMatchesMCP calls each routed read through both transports with
// the same input and requires identical JSON.
func TestRESTMatchesMCP(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	rng := "from=2026-08-20&to=2026-08-21"
	args := map[string]any{"project": "blog", "from": "2026-08-20", "to": "2026-08-21"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range args {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		tool, target string
		args         map[string]any
	}{
		{"list_projects", "/api/projects", map[string]any{}},
		{"web_overview", "/api/projects/blog/web/overview?" + rng, args},
		{"web_breakdown", "/api/projects/blog/web/breakdown?dimension=pages&" + rng, with(map[string]any{"dimension": "pages"})},
		{"app_overview", "/api/projects/blog/app/overview?" + rng, args},
		{"app_breakdown", "/api/projects/blog/app/breakdown?dimension=screens&" + rng, with(map[string]any{"dimension": "screens"})},
		{"product_events", "/api/projects/blog/product/events?" + rng, args},
		{"product_attributes", "/api/projects/blog/product/attributes?" + rng, args},
		{"retention", "/api/projects/blog/retention?surface=web&" + rng, with(map[string]any{"surface": "web"})},
		{"identities", "/api/projects/blog/identities?kind=user&" + rng, with(map[string]any{"kind": "user"})},
		{"list_ingest_keys", "/api/keys", map[string]any{}},
	} {
		rec := serveREST(t, r, "GET", c.target, "")
		if rec.Code != 200 {
			t.Errorf("%s: GET %s = %d %s", c.tool, c.target, rec.Code, rec.Body.String())
			continue
		}
		res := callTool(t, cs, c.tool, c.args)
		want, _ := json.Marshal(res.StructuredContent)
		var gotV, wantV any
		json.Unmarshal(rec.Body.Bytes(), &gotV)
		json.Unmarshal(want, &wantV)
		if fmt.Sprint(gotV) != fmt.Sprint(wantV) {
			t.Errorf("%s: REST %s\n MCP %s", c.tool, rec.Body.String(), want)
		}
	}
}

func TestRESTWritesAndRefusals(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)

	rec := serveREST(t, r, "POST", "/api/projects", `{"alias":"shop","allowed_origins":["https://shop.example.com"]}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"key":"ak_`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects", `{"alias":"shop"}`); rec.Code != 409 || errorCode(t, rec) != "conflict" {
		t.Errorf("duplicate create = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "PATCH", "/api/projects/shop", `{"name":"Shop"}`); rec.Code != 200 {
		t.Errorf("patch = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects/shop/keys", `{"label":"ios"}`); rec.Code != http.StatusCreated {
		t.Errorf("issue key = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects/shop/keys/ios/disable", ""); rec.Code != 200 {
		t.Errorf("disable key = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/projects/shop/archive", ""); rec.Code != 200 {
		t.Errorf("archive = %d %s", rec.Code, rec.Body.String())
	}
	rec = serveREST(t, r, "GET", "/api/projects/nope/web/overview?from=2026-08-20&to=2026-08-21", "")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "valid aliases") {
		t.Errorf("unknown project = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "GET", "/api/projects/blog/web/overview?from=2026-08-20&to=2026-08-21&colour=red", ""); rec.Code != 400 {
		t.Errorf("unknown parameter = %d", rec.Code)
	}
	if rec := serveREST(t, r, "POST", "/api/query", `{"sql":"SELECT day, visitors FROM v_web_daily WHERE project='blog'"}`); rec.Code != 200 {
		t.Errorf("query = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "POST", "/api/query", `{"sql":"ATTACH 'x' AS y"}`); rec.Code != 400 {
		t.Errorf("attach = %d", rec.Code)
	}
	if rec := serveREST(t, r, "GET", "/api/schema/views", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "v_web_daily") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("schema/views = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := serveREST(t, r, "GET", "/api/projects/blog/integration-guide?platform=web", ""); rec.Code != 404 {
		t.Errorf("integration guide must be MCP-only, got %d", rec.Code)
	}

	var actor string
	if err := h.db.QueryRow(`SELECT actor FROM audit_log WHERE action='project.create' AND subject='shop'`).Scan(&actor); err != nil || actor != "api" {
		t.Errorf("audit actor = %q, %v; want api", actor, err)
	}
	_ = mcp.ToolAnnotations{} // keep the import if unused elsewhere in this file
}
```

If `h.db` is the read-only pool and cannot see the just-written row (WAL snapshot semantics), read the audit row through the store instead — check how `ops_manage_test.go` inspects audit rows and use the same approach. Adjust the seeded fixture expectations (`app_overview` over a range with no app data returns empty rows; that is still a valid equality).

In `server_test.go` add:

```go
func TestAPIRequiresAuth(t *testing.T) {
	h := newHandlerFixture(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/projects", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/api/projects", nil)
	req.Header.Set("Authorization", "Bearer ar_testtoken")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"projects"`) {
		t.Fatalf("static token on /api/projects: %d %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `export PATH=$PATH:/usr/local/go/bin; go test ./internal/apiserver/ -run 'Decode|WriteError|REST|APIRequiresAuth'`
Expected: compile errors (`decodeRequest`, `writeError`, `maxAPIBody` undefined).

- [ ] **Step 3: Implement `internal/apiserver/rest.go`**

Delete the stub `restHandler` from `expose.go`, then:

```go
package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

const maxAPIBody = 1 << 20

func restHandler[In, Out any](r *registrar, s spec, fn func(context.Context, In) (Out, error)) http.HandlerFunc {
	status := s.Status
	if status == 0 {
		status = http.StatusOK
	}
	return func(w http.ResponseWriter, req *http.Request) {
		var in In
		if err := decodeRequest(req, &in); err != nil {
			writeError(w, r.logger, err)
			return
		}
		out, err := fn(withActor(req.Context(), "api"), in)
		if err != nil {
			writeError(w, r.logger, err)
			return
		}
		writeJSON(w, status, out)
	}
}

// decodeRequest fills dst from the request: the JSON body for POST and
// PATCH, query parameters for GET, then path wildcards over both — the path
// is authoritative. Anything that names no field is refused, so a typo
// fails loudly rather than being ignored.
func decodeRequest(req *http.Request, dst any) error {
	fields := jsonFields(reflect.ValueOf(dst).Elem())
	if req.Method == http.MethodGet {
		for name, vals := range req.URL.Query() {
			f, ok := fields[name]
			if !ok {
				return invalidf("unknown query parameter %q; valid: %s", name, fieldNames(fields))
			}
			if err := setField(f, name, vals[len(vals)-1]); err != nil {
				return err
			}
		}
	} else {
		if req.URL.RawQuery != "" {
			return invalidf("%s takes a JSON body, not query parameters", req.Method)
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, maxAPIBody+1))
		if err != nil {
			return invalidf("reading request body: %v", err)
		}
		if len(body) > maxAPIBody {
			return invalidf("request body exceeds %d bytes", maxAPIBody)
		}
		if len(bytes.TrimSpace(body)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.DisallowUnknownFields()
			if err := dec.Decode(dst); err != nil {
				return invalidf("request body: %v", err)
			}
		}
	}
	for name, f := range fields {
		if v := req.PathValue(name); v != "" {
			if err := setField(f, name, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonFields maps JSON names to settable fields, promoting embedded structs
// the way encoding/json does (rangeIn inside breakdownIn).
func jsonFields(v reflect.Value) map[string]reflect.Value {
	out := map[string]reflect.Value{}
	if v.Kind() != reflect.Struct {
		return out
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.Anonymous && sf.Type.Kind() == reflect.Struct {
			for k, f := range jsonFields(v.Field(i)) {
				out[k] = f
			}
			continue
		}
		if !sf.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = v.Field(i)
	}
	return out
}

func fieldNames(fields map[string]reflect.Value) string {
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func setField(f reflect.Value, name, raw string) error {
	switch f.Kind() {
	case reflect.String:
		f.SetString(raw)
	case reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return invalidf("%s must be an integer, got %q", name, raw)
		}
		f.SetInt(int64(n))
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return invalidf("%s must be true or false, got %q", name, raw)
		}
		f.SetBool(b)
	default:
		return invalidf("%s cannot be set from the URL; send it in a JSON body", name)
	}
	return nil
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError maps a refusal to its status by errors.Is. Untyped errors are
// logged and answered generically: their text may carry internals.
func writeError(w http.ResponseWriter, logger *slog.Logger, err error) {
	status, code, msg := http.StatusInternalServerError, "internal", "internal error"
	switch {
	case errors.Is(err, manage.ErrInvalid):
		status, code, msg = http.StatusBadRequest, "invalid", err.Error()
	case errors.Is(err, manage.ErrNotFound):
		status, code, msg = http.StatusNotFound, "not_found", err.Error()
	case errors.Is(err, manage.ErrConflict):
		status, code, msg = http.StatusConflict, "conflict", err.Error()
	default:
		logger.Error("api request failed", "error", err)
	}
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}

// registerSchemaRoute serves schema://views over REST: POST /api/query
// callers need the same column reference MCP clients read.
func registerSchemaRoute(r *registrar) {
	if r.rest == nil {
		return
	}
	r.rest.HandleFunc("GET /api/schema/views", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, schemaViews)
	})
}
```

Check `manage` ops' own validation errors: `manage.ErrInvalid` is already returned wrapped by `Ops` for bad specs (see `internal/manage/ops.go`), so create/update refusals map to 400 without change.

Call `registerSchemaRoute(r)` at the end of `(h *host) register`. Update `newTestRegistrar` if needed (it already sets `rest`).

- [ ] **Step 4: Serve `/mcp` and `/api/` from one auth-wrapped handler (`server.go`)**

```go
func Build(ctx context.Context, cfg *config.Config, reg *manage.Registry, ops *manage.Ops, logger *slog.Logger) (http.Handler, func() error, error) {
	db, err := OpenReadDB(cfg.API.DBPath)
	if err != nil {
		return nil, nil, err
	}
	h := &host{db: db, reg: reg, ops: ops,
		timeout: cfg.API.QueryTimeout, maxRows: cfg.API.QueryMaxRows,
		publicURL: cfg.PublicURL, logger: logger}
	srv := mcp.NewServer(&mcp.Implementation{Name: "twillingate", Version: "1.0.0"}, nil)
	inner := http.NewServeMux()
	h.register(&registrar{mcp: srv, rest: inner, logger: logger})
	h.registerResources(srv)
	inner.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv }, nil))
	inner.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]apiError{"error": {Code: "not_found", Message: "no such API route"}})
	})

	protected, err := wrapAuth(ctx, cfg.API, inner)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return protected, db.Close, nil
}
```

In `RegisterOn`, mount both prefixes:

```go
	mux.Handle("/mcp", protected)
	mux.Handle("/api/", protected)
```

Update the `Build`/`RegisterOn` doc comments to say they serve the API surface (`/mcp` and `/api/`).

- [ ] **Step 5: Verify**

Run: `export PATH=$PATH:/usr/local/go/bin; go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(apiserver): serve the MCP tools as a REST API under /api

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 7: The resource identifier is the API origin

**Files:**
- Modify: `internal/config/config.go` (`parseAPIAuthDSN`, `parseTokenLogin`), `internal/config/config_test.go`
- Modify: `internal/apiserver/oauth.go` (`loginServer.resource`, `issuer`), `oauth_authorize.go:49`, `oauth_token.go:39`, `server.go` (`metadataURLFor`, `RegisterOn` PRM resource)
- Modify tests: `server_test.go` (`loginDSN`, resource URLs `…/mcp` → origin), `oauth_test.go`, `oauth_authorize_test.go`, `oauth_token_test.go`, `oauth_e2e_test.go`, `internal/app/errors_test.go` (resource values)

**Interfaces:**
- Consumes: `config.APIConfig` (Task 2); `/api/` behind auth (Task 6).
- Produces:
  - `APIConfig.ResourceURL` is always an origin `scheme://host[:port]` with no path.
  - `func (s *loginServer) resourceAccepted(r string) bool` — true when `r` equals `s.resource` or starts with `s.resource + "/"`.

- [ ] **Step 1: Write the failing tests**

`internal/config/config_test.go`:

```go
func TestResourceIsTheAPIOrigin(t *testing.T) {
	load := func(env map[string]string) (*Config, error) {
		env["DATABASE_DSN"] = "sqlite:///tmp/x.db"
		c, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		if err != nil {
			return nil, err
		}
		return c, c.ValidateAPI()
	}
	c, err := load(map[string]string{"PUBLIC_URL": "https://t.example.com/", "API_AUTH_DSN": "token://ar_x?password=pw"})
	if err != nil || c.API.ResourceURL != "https://t.example.com" {
		t.Errorf("token login default resource = %q, %v", c.API.ResourceURL, err)
	}
	c, err = load(map[string]string{"PUBLIC_URL": "https://t.example.com", "API_AUTH_DSN": "oauth://idp.example.com"})
	if err != nil || c.API.ResourceURL != "https://t.example.com" || c.API.Audience != "https://t.example.com" {
		t.Errorf("oauth default resource/audience = %q/%q, %v", c.API.ResourceURL, c.API.Audience, err)
	}
	for _, dsn := range []string{
		"token://ar_x?password=pw&resource=https://api.example.com/mcp",
		"oauth://idp.example.com?resource=https://api.example.com/api",
	} {
		if _, err := load(map[string]string{"API_AUTH_DSN": dsn}); err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("%s: err = %v, want an origin-only refusal", dsn, err)
		}
	}
	if c, err := load(map[string]string{"API_AUTH_DSN": "token://ar_x?password=pw&resource=https://api.example.com/"}); err != nil || c.API.ResourceURL != "https://api.example.com" {
		t.Errorf("trailing slash origin = %q, %v", c.API.ResourceURL, err)
	}
}
```

`internal/apiserver/oauth_token_test.go` (uses the existing `loginFixture`; read `oauth_test.go:113` for `newLoginFixture` and set its resource to `https://mcp.example.com`):

```go
func TestResourceParameterMayNameAPathUnderTheOrigin(t *testing.T) {
	f := newLoginFixture(t, nil)
	for r, want := range map[string]bool{
		"https://mcp.example.com":          true,
		"https://mcp.example.com/mcp":      true,
		"https://mcp.example.com/api":      true,
		"https://mcp.example.com.evil.com": false,
		"https://other.example.com/mcp":    false,
		"http://mcp.example.com/mcp":       false,
	} {
		if got := f.srv.resourceAccepted(r); got != want {
			t.Errorf("resourceAccepted(%q) = %v, want %v", r, got, want)
		}
	}
}
```

(Use the fixture's actual field name for the `*loginServer`; read `loginFixture` in `oauth_test.go`.)

`internal/apiserver/oauth_e2e_test.go`: extend the existing end-to-end test so that after the go-sdk client completes login against `resource=https://…/mcp` it also issues `GET /api/projects` with the obtained access token as `Authorization: Bearer …` and expects 200. Read the test to find where the access token is available (the oauth2 token source / transport the client uses); if only an `http.Client` is exposed, use that client for the `/api/projects` request.

- [ ] **Step 2: Run to see them fail**

Run: `export PATH=$PATH:/usr/local/go/bin; go test ./internal/config/ ./internal/apiserver/ -run 'Resource|E2E|e2e'`
Expected: FAIL (default resource still ends in `/mcp`; `resourceAccepted` undefined).

- [ ] **Step 3: Implement**

`internal/config/config.go` — add:

```go
// resourceOrigin reads a resource= value as the API origin: an absolute
// http(s) URL with no path beyond "/", returned without the slash. One
// identifier covers /mcp and /api/, and matches the host-rooted RFC 9728
// metadata the API serves.
func resourceOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("must be an absolute http(s) origin such as https://api.example.com")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("must be an origin with no path, such as https://api.example.com (the API serves /mcp and /api/ under it)")
	}
	return u.Scheme + "://" + u.Host, nil
}
```

In `parseAPIAuthDSN` (oauth branch) and `parseTokenLogin`:
- default `m.ResourceURL = c.PublicURL` (was `c.PublicURL + "/mcp"`); `PublicURL` is already trailing-slash-trimmed. If `PUBLIC_URL` itself has a path, run it through `resourceOrigin` too and use the result's error.
- when `resource=` is given: `m.ResourceURL, err = resourceOrigin(q.Get("resource"))`, error text `config: API_AUTH_DSN … resource=%q %v`.
- token login keeps `checkLoginURL(m.ResourceURL)` after that.
- oauth `Audience` default stays `m.ResourceURL` (now the origin).
- update the `APIConfig` doc comment: "resource defaults to PUBLIC_URL and must be an origin".

`internal/apiserver/oauth.go`:

```go
// resourceAccepted admits a client's resource parameter naming this API:
// the origin itself or any URL under it, since MCP clients send the URL
// they connected to (…/mcp). Tokens are still issued for the origin.
func (s *loginServer) resourceAccepted(r string) bool {
	return r == s.resource || strings.HasPrefix(r, s.resource+"/")
}
```

`issuer: originOf(m.ResourceURL)` stays (now equal to the resource). `oauth_authorize.go:49` → `case q.Has("resource") && !s.resourceAccepted(q.Get("resource")):`. `oauth_token.go:39` → `if form.Has("resource") && !s.resourceAccepted(form.Get("resource")) {`. Audience in issued tokens and verification stay `s.resource` (the origin).

`server.go`: `metadataURLFor(resourceURL)` → `resourceURL + "/.well-known/oauth-protected-resource"`; delete the "revisit if a path-scoped resource" comment and explain that the resource is the origin, so the host-rooted form is the conformant one. Keep `originOf` (used by the login server).

Update test fixtures: every `resource=https://…/mcp` in `server_test.go`, `oauth*_test.go`, `internal/app/errors_test.go` becomes the bare origin; `TestIssuedAccessTokenNeedsTheLoginServer` uses `ResourceURL: "https://mcp.example.com"` and `Audience: {"https://mcp.example.com"}`. Tests that asserted the `/mcp`-suffixed resource in PRM bodies now assert the origin.

- [ ] **Step 4: Verify**

Run: `export PATH=$PATH:/usr/local/go/bin; go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Update `docs/deployment.md` in the same commit**

Everywhere it documents `resource=` or the default resource: the default is `PUBLIC_URL`; `resource=` names the API origin with no path (`resource=https://api.example.com`); an existing password login must reconnect once after upgrading because tokens issued for `…/mcp` no longer verify.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(apiserver)!: one token for /mcp and /api, issued for the API origin

Clients that logged in with the password must reconnect once.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 8: Wire `serve -ingest` / `serve -api`

**Files:**
- Modify: `internal/server/server.go` (add `Mount`), `internal/server/server_test.go`
- Modify: `internal/apiserver/server.go` (`NewHandler`/`RegisterOn` comments; no signature change), `internal/apiserver/oauth_e2e_test.go` (`e2eHandler` shared case uses `server`-style patterns instead of a `/` catch-all — keep `http.NotFoundHandler()` at `/` out; mount nothing at `/`)
- Modify: `internal/app/app.go`, `internal/app/app_test.go`, `internal/app/errors_test.go`
- Modify: `cmd/twillingate/serve.go`, `cmd/twillingate/commands_test.go`

**Interfaces:**
- Consumes: `apiserver.Build`, `apiserver.NewHandler`, `apiserver.RegisterOn(mux, protected, cfg, withHealthz bool, logger)` (Task 6).
- Produces:
  - `func (s *Server) Mount(mux *http.ServeMux)` — registers `POST /ingest/events`, `OPTIONS /ingest/events`, `GET /healthz`, and the `/js/*` script routes on `mux`. `New` calls `s.Mount(s.mux)`.
  - `func app.Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger, ingest, api bool) error`
  - `func resolveSurfaces(ingest, api bool) (runIngest, runAPI, lenient bool)`

- [ ] **Step 1: Write the failing tests**

`internal/server/server_test.go`:

```go
func TestMountOnSharedMux(t *testing.T) {
	s := newTestServer(t) // the file's existing helper
	mux := http.NewServeMux()
	s.Mount(mux)
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	for target, want := range map[string]int{"/healthz": 200, "/js/twillingate.js": 200, "/api/projects": 299, "/nope": 404} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", target, rec.Code, want)
		}
	}
}
```

`cmd/twillingate/commands_test.go`: rewrite the `resolveSurfaces` table for `(ingest, api)` — `(false,false)→(true,true,lenient)`, `(true,false)→(true,false,strict)`, `(false,true)→(false,true,strict)`, `(true,true)→(true,true,strict)`; the "explicit -mcp without auth" test becomes `run([]string{"serve", "-api"}, &out)` exit 1 with `API_AUTH_DSN` in output; `argsFor["serve"]` becomes `{"serve", "-ingest"}`; add a case that `serve -mcp` exits 2 (unknown flag).

`internal/app/app_test.go`: in the shared/split listener test, `Serve(ctx, cfg, logger, true, true)`; assert on the shared listener that `GET /api/projects` without a token is 401 and `POST /ingest/events` is not 404; on split listeners `GET /api/projects` on the ingest port is 404 and on `cfg.API.Addr` is 401.

- [ ] **Step 2: Run to see them fail**

Run: `export PATH=$PATH:/usr/local/go/bin; go test ./internal/server/ ./internal/app/ ./cmd/twillingate/`
Expected: compile errors (`Mount` undefined) and failing flag tests.

- [ ] **Step 3: Implement**

`internal/server/server.go`:

```go
func New(cfg *config.Config, reg *manage.Registry, q Enqueuer, g geo.Provider, salt Salt, names NameStore, logger *slog.Logger) *Server {
	s := &Server{cfg: cfg, reg: reg, queue: q, geo: g, salt: salt, names: names,
		counters: newKeyCounters(), logger: logger, mux: http.NewServeMux()}
	s.Mount(s.mux)
	return s
}

// Mount registers the ingest surface's routes on mux: its own when the
// surface has a listener to itself, the shared one beside the API otherwise.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /ingest/events", s.handleEvents)
	mux.HandleFunc("OPTIONS /ingest/events", s.handlePreflight)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	s.registerScript(mux)
}
```

`internal/app/app.go` — rename parameters `api, mcpOn` → `ingest, api` and rewrite the surface assembly:

```go
	var ingestHandler *server.Server
	if ingest {
		ingestHandler = server.New(cfg, reg, buf, geoProvider, salter, st, logger)
	}

	// Shared address: one mux carries both surfaces, each registering its
	// own patterns. Otherwise each surface gets a listener of its own.
	var surfaces []httpSurface
	var apiClose func() error
	switch {
	case api && ingest && cfg.API.Addr == cfg.IngestAddr:
		protected, closeDB, err := apiserver.Build(ctx, cfg, reg, manage.NewOps(reg, st), logger)
		if err != nil {
			stopBackground()
			return err
		}
		apiClose = closeDB
		mux := http.NewServeMux()
		ingestHandler.Mount(mux)
		apiserver.RegisterOn(mux, protected, cfg, false, logger)
		surfaces = append(surfaces, httpSurface{cfg.IngestAddr, mux})
	case api:
		h, closeDB, err := apiserver.NewHandler(ctx, cfg, reg, manage.NewOps(reg, st), logger)
		if err != nil {
			stopBackground()
			return err
		}
		apiClose = closeDB
		if ingest {
			surfaces = append(surfaces, httpSurface{cfg.IngestAddr, ingestHandler})
		}
		surfaces = append(surfaces, httpSurface{cfg.API.Addr, h})
	default:
		surfaces = append(surfaces, httpSurface{cfg.IngestAddr, ingestHandler})
	}
	if apiClose != nil {
		defer apiClose()
	}
```

Replace the remaining `if api {` guarding the ingest summary goroutine with `if ingest {`. Update the `Serve` and `httpSurface` doc comments (`-ingest`/`-api`, "MCP and REST"), and the "no projects configured" warning ("… or an API management operation").

`cmd/twillingate/serve.go`:

```go
// resolveSurfaces maps the -ingest/-api flags to the surfaces to run. Bare
// `serve` runs both, leniently: an API without auth config is skipped with
// a warning rather than failing the process. Naming a flag is an explicit
// request, so misconfiguration of a requested surface stays a hard error.
func resolveSurfaces(ingest, api bool) (runIngest, runAPI, lenient bool) {
	if !ingest && !api {
		return true, true, true
	}
	return ingest, api, false
}

func cmdServe(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	ingest := fs.Bool("ingest", false, "run only the ingest surface (events and the SDK)")
	api := fs.Bool("api", false, "run only the API surface (MCP and REST)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	runIngest, runAPI, lenient := resolveSurfaces(*ingest, *api)
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	logger := app.NewLogger(cfg.Log)
	if runAPI {
		if err := cfg.ValidateAPI(); err != nil {
			if !lenient {
				fmt.Fprintln(stdout, err)
				return 1
			}
			logger.Warn("API disabled", "reason", err.Error())
			runAPI = false
		}
	}
	if runIngest && runAPI && cfg.API.Addr == cfg.IngestAddr {
		logger.Warn("API_ADDR not set; the API shares the ingestion listener", "addr", cfg.IngestAddr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Serve(ctx, cfg, logger, runIngest, runAPI); err != nil {
		logger.Error("serve failed", "error", err)
		return 1
	}
	return 0
}
```

Update every `Serve(…, true, false)` / `(false, true)` call in app tests — argument order is now `(ingest, api)`, which matches the old `(api, mcpOn)` positions, so values stay; only comments and names change. In `oauth_e2e_test.go`'s shared case, drop the `/` catch-all line and mount a stand-in `GET /healthz` instead, since `RegisterOn(…, false, …)` omits it.

- [ ] **Step 4: Verify**

Run: `export PATH=$PATH:/usr/local/go/bin; go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(cmd)!: serve -ingest and serve -api replace -api and -mcp

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 9: Documentation contract, drift test, deploy files

**Files:**
- Modify: `docs/twillingate.md`, `docs/deployment.md`, `README.md`, `.env.example`, `deploy/compose/docker-compose.yml:1-2`, `deploy/systemd/install.sh:144`, `CLAUDE.md`
- Modify: `internal/apiserver/docs_sync_test.go` (new route-table test), `internal/apiserver/resources.go` (`docs://deployment` description)

**Interfaces:**
- Consumes: `registrar.specs` via `newTestRegistrar` (Task 5/6).
- Produces: `docs/twillingate.md` section `### HTTP API` whose table rows have the form ``| `GET` | `/api/projects` | `list_projects` | … |``.

- [ ] **Step 1: Write the failing drift test**

Append to `internal/apiserver/docs_sync_test.go`:

```go
// TestDocumentMatchesRoutes binds the HTTP API table in docs/twillingate.md
// to the registered routes, in both directions: method, path and the tool
// each route mirrors.
func TestDocumentMatchesRoutes(t *testing.T) {
	h, _ := newTestHost(t)
	inCode := map[string]string{} // "GET /api/projects" -> tool
	for _, s := range newTestRegistrar(t, h).specs {
		if s.Method != "" {
			inCode[s.Method+" "+s.Path] = s.Name
		}
	}
	inCode["GET /api/schema/views"] = "schema://views"

	const heading = "### HTTP API"
	i := strings.Index(docs.Twillingate, heading)
	if i < 0 {
		t.Fatal("docs/twillingate.md has no '### HTTP API' section")
	}
	section := docs.Twillingate[i+len(heading):]
	if j := strings.Index(section, "\n### "); j >= 0 {
		section = section[:j]
	}
	row := regexp.MustCompile("^\\| `(GET|POST|PATCH)` \\| `(/api/[^`]*)` \\| `([a-z_:/]+)` \\|")
	documented := map[string]string{}
	for _, line := range strings.Split(section, "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			documented[m[1]+" "+m[2]] = m[3]
		}
	}
	for route, tool := range inCode {
		if documented[route] != tool {
			t.Errorf("route %s (%s) is registered but the HTTP API table says %q", route, tool, documented[route])
		}
	}
	for route := range documented {
		if _, ok := inCode[route]; !ok {
			t.Errorf("the HTTP API table lists %s, which is not registered", route)
		}
	}
}
```

- [ ] **Step 2: Run to see it fail**

Run: `export PATH=$PATH:/usr/local/go/bin; go test ./internal/apiserver/ -run TestDocumentMatchesRoutes`
Expected: FAIL — no `### HTTP API` section.

- [ ] **Step 3: Write `docs/twillingate.md` "HTTP API"**

Read the document's structure first (`grep -n '^#' docs/twillingate.md`). Under the section that introduces the MCP tools ("Answer questions with the data"), add `### HTTP API` containing:

1. One paragraph: the API surface serves the same operations as the MCP tools at `/api/`, with the same bearer token (`Authorization: Bearer …`), JSON in and out; `integration_guide` and the `docs://` resources are MCP-only.
2. A curl example:

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "https://t.example.com/api/projects/blog/web/overview?from=2026-09-01&to=2026-09-13"
```

3. The route table, one row per registered route, exactly in this column shape (the drift test reads it):

```markdown
| Method | Path | Mirrors | Input |
|---|---|---|---|
| `GET` | `/api/projects` | `list_projects` | — |
| `POST` | `/api/projects` | `create_project` | body: `alias`, `name`, `identity`, `allowed_origins`, `attributes`, `skip_key` → 201 |
| `PATCH` | `/api/projects/{alias}` | `update_project` | body: fields to change (merge) |
| `POST` | `/api/projects/{alias}/archive` | `archive_project` | — |
| `POST` | `/api/projects/{alias}/restore` | `restore_project` | — |
| `GET` | `/api/keys` | `list_ingest_keys` | query: `project` |
| `POST` | `/api/projects/{project}/keys` | `issue_ingest_key` | body: `label` → 201 |
| `POST` | `/api/projects/{project}/keys/{label}/disable` | `disable_ingest_key` | — |
| `POST` | `/api/projects/{project}/keys/{label}/enable` | `enable_ingest_key` | — |
| `GET` | `/api/projects/{project}/web/overview` | `web_overview` | query: `from`, `to` |
| `GET` | `/api/projects/{project}/web/breakdown` | `web_breakdown` | query: `from`, `to`, `dimension`, `limit` |
| `GET` | `/api/projects/{project}/app/overview` | `app_overview` | query: `from`, `to` |
| `GET` | `/api/projects/{project}/app/breakdown` | `app_breakdown` | query: `from`, `to`, `dimension`, `limit` |
| `GET` | `/api/projects/{project}/product/events` | `product_events` | query: `from`, `to`, `event` |
| `GET` | `/api/projects/{project}/product/attributes` | `product_attributes` | query: `from`, `to`, `event` |
| `GET` | `/api/projects/{project}/retention` | `retention` | query: `from`, `to`, `surface` |
| `GET` | `/api/projects/{project}/identities` | `identities` | query: `from`, `to`, `kind`, `limit` |
| `POST` | `/api/query` | `query` | body: `sql` |
| `GET` | `/api/schema/views` | `schema://views` | — (text/plain) |
```

4. Responses and errors: success is the tool's output as JSON; errors are `{"error":{"code","message"}}` with `invalid` 400, `not_found` 404, `conflict` 409, `internal` 500; missing/bad token 401; unknown query parameters and body fields are 400.

Also in `docs/twillingate.md`: wherever it says "the MCP endpoint" as the only way in, say "the API (MCP or HTTP)"; the tool-count sentence stays accurate (`TestDocumentNamesEveryTool`).

- [ ] **Step 4: Restructure `docs/deployment.md`**

- Rename `## The MCP endpoint` → `## The API endpoint` (and its TOC link `#the-api-endpoint`); opening paragraph: one listener path serves MCP at `/mcp` and REST at `/api/`; one `API_AUTH_DSN` protects both.
- `### Hostnames and processes`: `API_ADDR` gives the API its own listener; split units use `twillingate serve -ingest` and `twillingate serve -api`; the warning about background jobs in an API-only process refers to `API_DB_PATH`; a reverse proxy can expose only `/ingest/`, `/js/` and `/healthz` publicly.
- `### Connect a client`: unchanged MCP URL `…/mcp`; add a short "Use the HTTP API" paragraph with the curl example and a link to `docs/twillingate.md#http-api`.
- Add `### Upgrading from MCP_* and serve -mcp` with the four steps of spec §9 (rename variables — the collector refuses to start until done; `-api`/`-mcp` → `-ingest`/`-api` in split units; native apps and hand-written calls move to `/ingest/events`; password-login clients reconnect once).
- `### Verifying ingestion` curls use `/ingest/events` (done in Task 3; re-check).

`resources.go`: `docs://deployment` description "… enabling and authenticating the API endpoint (MCP and REST) …"; `docs://twillingate` description mentions "the HTTP API".

`README.md`: "The MCP endpoint" link → `docs/deployment.md#the-api-endpoint`; mention the REST API in one sentence where MCP is introduced.

`.env.example`: header comment for the block `# API surface (serve -api): MCP at /mcp and REST at /api/.` and `resource defaults to PUBLIC_URL`.

`deploy/compose/docker-compose.yml:1-2`: `# Tracking: ingestion, the tracker script and — once API_AUTH_DSN is set in`, `# \`.env\` — the API (MCP and REST), all on :8080. See docs/deployment.md.`

`deploy/systemd/install.sh:144`: comment `# also catches separate twillingate-ingest/-api units.`

`CLAUDE.md` Documentation section: the MCP-tools trigger line now reads "the MCP tools, REST routes or resources offered (`internal/apiserver/ops_*.go`, `expose.go`, `rest.go`, `resources.go`)"; the auth line reads "API auth modes or client setup (`internal/apiserver/auth.go`, `oauth*.go`)"; the `docs_sync_test.go` description adds "and the HTTP API route table".

- [ ] **Step 5: Verify**

Run: `export PATH=$PATH:/usr/local/go/bin; go build ./... && go vet ./... && go test ./... && scripts/coverage.sh`
Expected: PASS; total and `internal/apiserver` coverage ≥ 90%. If `internal/apiserver` is below 90%, add tests for the uncovered REST adapter branches (`setField` bool, `restHandler` error path) until it passes.

Then: `git grep -n -i -E 'serve -mcp|keygen -mcp|MCP endpoint' -- ':!docs/superpowers'` — every remaining hit must be deliberate (the upgrade section, or MCP as a protocol name).

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "docs: document the HTTP API and the -ingest/-api split

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## After all tasks

- Run the full suite once more plus `cd sdk && npm test && npm run build && git diff --exit-code ../internal/server/twillingate.js`.
- Push to `feat/api-surface` (PR #24), update the PR checklist.
- The claude.ai staging login (spec §7 risk) is a manual step for the operator; do not mark it done.
