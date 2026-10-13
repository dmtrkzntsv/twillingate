# Engine Child Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The binary can run DuckDB in a child process of itself, locked to one directory, bounded across processes, and ships for linux/amd64 and linux/arm64 only, with nothing else changed for users.

**Architecture:** A new leaf package `internal/engine` holds both ends of a pipe. The child end, `ServeChild`, opens an in-memory DuckDB, locks it to the allowed directories, and answers one JSON object per line. The parent end, `Engine`, spawns `<binary> engine …` on the first query and multiplexes requests over the pipe. It cancels a query by interrupting it, takes a slot from a lock-file semaphore before spawning, and closes the child's stdin after an idle period. `app.EngineOptions` maps the config onto the engine. `twillingate engine -check` spawns a child and runs one query, which is how the build, the image and the release prove the child works. No server path uses the engine yet: PR 3 (the log and the dump) is its first caller.

**Tech Stack:** Go 1.25 with cgo, `github.com/duckdb/duckdb-go/v2` v2.10506.0 (DuckDB v1.5.6, static library with parquet, json and icu compiled in), `syscall.Flock`, GitHub Actions `ubuntu-22.04` and `ubuntu-22.04-arm` runners, `debian:bookworm-slim`.

**Spec:** `docs/superpowers/specs/2026-10-12-raw-first-design.md`, D8 (the child, its settings, the machine-wide cap), D10 (32-bit ARM dropped), D13 (the dependency, glibc only, release targets), D23 (`ENGINE_MEMORY_LIMIT`, `ENGINE_MAX_CHILDREN`). Roadmap: `docs/superpowers/plans/2026-10-12-raw-first-roadmap.md`, PR 2.

## Facts this plan rests on (probed 2026-10-12 against v2.10506.0, linux/amd64)

- `SET GLOBAL allowed_directories = ['<dir>/']`, then `SET GLOBAL enable_external_access = false` and `SET GLOBAL lock_configuration = true` hold on every pooled connection. With them in place:
  - reading a file outside the list (`/etc/hostname`, `<dir>x/in.csv`) fails with `Permission Error: Cannot access file … file system operations are disabled by configuration`;
  - `COPY … TO` outside the list, `ATTACH` and `INSTALL httpfs` fail the same way;
  - `SET memory_limit` and `SET enable_external_access=true` fail with `Cannot change configuration option … the configuration has been locked`.
- **`COPY … TO` inside an allowed directory succeeds.** So the allowed directory must never be the one holding the SQLite file.
- `temp_directory` need not be allowed: a 64 MB child spilled a 30M-row sort to a directory outside the list.
- `QueryContext` with a cancelled context interrupts the running query (a 300 ms deadline returned at 301 ms, `INTERRUPT Error: Interrupted!`).
- Scanning a row into `[]any` gives: `int32`/`int64`, `float64`, `duckdb.Decimal` (has `String()`), `*big.Int` (HUGEINT), `time.Time` (DATE, TIME, TIMESTAMP, TIMESTAMPTZ), `[]uint8` (UUID as 16 bytes, BLOB), `[]any`, `map[string]any` (STRUCT), `duckdb.OrderedMap`, `duckdb.Interval`, `nil`.
- A cgo build takes 14 s and gives a 59 MB stripped binary (the probe; the current binary is ~15 MB).
- Linking: libstdc++ stays dynamic (the driver links it itself), so `-static-libstdc++` does nothing. Built on Debian 12, the binary needs `GLIBC_2.35` at most. It runs on `debian:bookworm-slim`, `ubuntu:22.04` and `gcr.io/distroless/cc-debian12`. Built on a glibc 2.43 host it needs 2.43, so **release binaries must be built on glibc ≤ 2.35** (Ubuntu 22.04 runners).
- `memory_limit = '256MB'` reads back as `244.1 MiB`: DuckDB's MB is 10^6 bytes.

## Global Constraints

- New dependency: `github.com/duckdb/duckdb-go/v2` (D13), pinned at v2.10506.0. No other new Go dependency.
- Release targets: linux/amd64 and linux/arm64; 32-bit ARM (`linux/arm`) dropped (D10); glibc only, so the image leaves Alpine (D13).
- `ENGINE_MEMORY_LIMIT`, default `512MB` (D8, D23). `ENGINE_MAX_CHILDREN`, the cap on engine children, default 2 (D23). No variable for threads (they follow the CPUs), the raw directory or the spill directory (D23).
- Child settings: `memory_limit` from `ENGINE_MEMORY_LIMIT`, `threads` equal to the CPUs, a spill directory (D8); `enable_external_access=false` with `allowed_directories` (roadmap). Widget SQL never gets a path to the SQLite file (D8).
- Children run one or two widgets at a time on small servers (D8): `Parallel` is 1 at 2 threads or fewer, else 2.
- `internal/engine` is a leaf, rank 0 in `internal/archtest`; `app` imports it, `cmd/twillingate` imports `app` and `engine` (CLAUDE.md Layout).
- Docs in the same commit as the change (CLAUDE.md "Documentation"): env vars → `docs/deployment.md` (bound by `TestDeploymentDocumentsEveryEnvVar`); install, image or platform → `docs/deployment.md` and `deploy/UPGRADES.md`.
- Conventional Commits; scopes from the tree, plus `engine` for the new package. The drop of 32-bit ARM is a breaking change: its commit carries `!`.
- `make check` passes before every push. Workflow files are pushed over SSH (the `gh` token lacks the `workflow` scope).
- Done when (roadmap): the binary runs on linux/amd64 and arm64 with the child answering `SELECT 1`, and nothing else changed for users. Measured on landing: spawn-to-ready on the release runners (target under 100 ms), binary size.

## Decisions this plan takes (review them)

1. **The directory layout under the data directory** (the directory holding the SQLite file):
   - `raw/` is the child's only allowed directory. PR 3 writes the log and the Parquet files there.
   - `engine/spill/child-*` gets one spill directory per child, removed when the child exits.
   - `engine/slots/slot-N` holds the semaphore's lock files.

   The SQLite file, its WAL, the geo database and `engine/` all sit outside `raw/`.
2. **The semaphore spans the data directory, not the host.** The roadmap puts the lock files in the data directory. D8's "fifty customers on one host" would need one directory shared by every install, and D23 allows no variable to name it. This plan follows the roadmap. A split install (ingest and console processes on one data directory) shares one cap. A host running many installs relies on D8's backstop, a `systemd` slice with `MemoryMax`. If you want the host-wide cap, it needs one more variable (or a fixed path such as `/run/twillingate`), plus the permission scheme for several service users. Say so and Task 2 gains it.
3. **The child exits when its stdin closes.** It exits there even with queries in flight, cancelling them. That is how a dead server never leaves a child running: the kernel closes the pipe. No `Pdeathsig`: it is tied to the spawning OS thread in Go, and stdin EOF already covers it.
4. **`engine -check`** is hidden from the usage line, documented in `docs/deployment.md`, and run by the smoke test, the Dockerfile and the release workflow.
5. **Release builds run on `ubuntu-22.04` and `ubuntu-22.04-arm` runners,** for the glibc 2.35 floor (Debian 12, Ubuntu 22.04 and later). The image builds per architecture on a native runner from `golang:1.25-bookworm` into `debian:bookworm-slim`. The two image digests are joined into one index at release.
6. **`release.yml` gains a `dry_run` input.** It runs every job except the tag and publish job, so this PR can prove the workflow before a real release.

## Review Focus

1. Widget SQL reaching the SQLite file or anything beside `raw/`: a person writing `SELECT * FROM read_text('<data>/twillingate.db')` or `COPY … TO '<data>/twillingate.db'` must get a permission error. Pinned by Task 1 `TestChildConfinedToAllowedDirs` (a file beside the allowed directory) and Task 3 `TestEngineOptionsKeepTheDatabaseOutOfReach` (no allowed directory contains the database's directory or the database).
2. A child killed mid-query (out of memory, `MemoryMax`): the caller gets `ErrChildExited`, not a hang, and the next query starts a fresh child with the slot freed. Pinned by Task 2 `TestCrashedChildIsReplaced`.
3. A widget past `CONSOLE_QUERY_TIMEOUT`: its query must stop using CPU, and the child must answer the next query at once. Pinned by Task 1 `TestChildCancelInterrupts` and Task 2 `TestCancelInterruptsTheQuery`.
4. The server dying or restarting mid-query must not leave a child running. Pinned by Task 1 `TestChildExitsOnEOF` (a long query in flight, stdin closed, `ServeChild` returns).
5. An operator on 32-bit ARM, Alpine (musl) or a pre-2.35 glibc must get a refusal that names the reason from the installer, not a binary that fails with `not found`. Pinned by Task 4's `install.sh` check, exercised by `scripts/test-install.sh` (Debian 12, passes) and an `alpine` run of the same check (refused).

Also pinned, beyond the five: integers past 2^53 survive the pipe (Task 1 `TestChildKeepsTypes`); `raw` versus `rawx` (Task 1 `TestChildConfinedToAllowedDirs`).

---

### Task 1: the child end, `ServeChild`

**Files:**
- Create: `internal/engine/protocol.go`, `internal/engine/child.go`, `internal/engine/values.go`, `internal/engine/child_test.go`
- Modify: `go.mod`, `go.sum`
- Modify: `internal/archtest/archtest_test.go` (rank table)
- Modify: `internal/sqlcheck/sqlcheck_test.go` (`scanInternal`)
- Modify: `Makefile` (`vet`)
- Modify: `CLAUDE.md` (Layout block, Commits scopes)

**Interfaces:**
- Produces: `engine.ServeChild(args []string, in io.Reader, out io.Writer) error`. The child flags are `-threads N -memory S -spill DIR -parallel N -allow DIR` (`-allow` repeats). Also the unexported `request` and `reply` types (below) and `const DefaultMemoryLimit = "512MB"`. Task 2 spawns the child with exactly these flags and decodes `reply`.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/duckdb/duckdb-go/v2@v2.10506.0`
Expected: `go.mod` lists `github.com/duckdb/duckdb-go/v2 v2.10506.0`; the `go` directive stays `1.25.0` (the module needs 1.24). `go mod tidy` would drop it until code imports it; run tidy at Step 7.

- [ ] **Step 2: Write the protocol types**

`internal/engine/protocol.go`:

```go
package engine

// request is one line the parent writes to the child's stdin. Cancel
// interrupts the request with the same ID; it gets no reply of its own.
type request struct {
	ID      uint64 `json:"id"`
	SQL     string `json:"sql,omitempty"`
	Exec    bool   `json:"exec,omitempty"`
	MaxRows int    `json:"max_rows,omitempty"`
	Cancel  bool   `json:"cancel,omitempty"`
}

// reply is one line the child writes to stdout: first {"ready":true} (or
// {"err":…} and the child exits), then one per request, in the order the
// requests finish. Numbers travel as JSON numbers and are decoded with
// UseNumber, so an int64 past 2^53 arrives whole.
type reply struct {
	ID        uint64   `json:"id,omitempty"`
	Ready     bool     `json:"ready,omitempty"`
	Columns   []string `json:"columns,omitempty"`
	Rows      [][]any  `json:"rows,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Err       string   `json:"err,omitempty"`
}
```

- [ ] **Step 3: Write the failing child tests**

`internal/engine/child_test.go`:

```go
package engine

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testChild runs ServeChild in-process on pipes: the protocol without a
// process, so these tests need no binary.
type testChild struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	enc  *json.Encoder
	dec  *json.Decoder
	done chan error
}

// childArgs allows dir/raw only and spills into dir/spill.
func childArgs(dir string) []string {
	return []string{"-threads", "1", "-memory", "256MB", "-spill", filepath.Join(dir, "spill"),
		"-parallel", "1", "-allow", filepath.Join(dir, "raw")}
}

func startChild(t *testing.T, args []string) *testChild {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &testChild{in: inW, out: outR, enc: json.NewEncoder(inW), dec: json.NewDecoder(outR), done: make(chan error, 1)}
	c.dec.UseNumber()
	go func() {
		c.done <- ServeChild(args, inR, outW)
		outW.Close()
	}()
	var first reply
	if err := c.dec.Decode(&first); err != nil || !first.Ready {
		t.Fatalf("first line = %+v, %v; want ready", first, err)
	}
	t.Cleanup(func() { inW.Close(); <-c.done })
	return c
}

func (c *testChild) ask(t *testing.T, r request) reply {
	t.Helper()
	if err := c.enc.Encode(r); err != nil {
		t.Fatal(err)
	}
	var rep reply
	if err := c.dec.Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.ID != r.ID {
		t.Fatalf("reply id = %d, want %d", rep.ID, r.ID)
	}
	return rep
}

func TestChildAnswers(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	rep := c.ask(t, request{ID: 1, SQL: "SELECT 42 AS answer"})
	if rep.Err != "" || len(rep.Columns) != 1 || rep.Columns[0] != "answer" ||
		len(rep.Rows) != 1 || rep.Rows[0][0] != json.Number("42") {
		t.Fatalf("reply = %+v", rep)
	}
}

// TestChildKeepsTypes: what DuckDB scans into Go values reaches the parent
// as JSON a reader can trust: integers whole, decimals exact, dates and
// times as text, UUIDs as text, NULL as null.
func TestChildKeepsTypes(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	rep := c.ask(t, request{ID: 1, SQL: `SELECT 9007199254740993::BIGINT, 12.34::DECIMAL(10,2),
		170141183460469231731687303715884105727::HUGEINT, DATE '2026-10-12',
		TIMESTAMP '2026-10-12 01:02:03', TIME '01:02:03', '0af59e70-fb48-4e2b-8bc0-03e93be19ea0'::UUID,
		NULL::VARCHAR, 'x', true, 1.5::DOUBLE`})
	if rep.Err != "" {
		t.Fatal(rep.Err)
	}
	want := []any{json.Number("9007199254740993"), json.Number("12.34"),
		json.Number("170141183460469231731687303715884105727"), "2026-10-12",
		"2026-10-12T01:02:03Z", "01:02:03", "0af59e70-fb48-4e2b-8bc0-03e93be19ea0",
		nil, "x", true, json.Number("1.5")}
	for i, w := range want {
		if rep.Rows[0][i] != w {
			t.Errorf("column %d = %#v, want %#v", i, rep.Rows[0][i], w)
		}
	}
}

func TestChildReportsSQLErrorsAndCarriesOn(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	if rep := c.ask(t, request{ID: 1, SQL: "SELECT nope"}); !strings.Contains(rep.Err, "nope") {
		t.Fatalf("err = %q, want one naming nope", rep.Err)
	}
	if rep := c.ask(t, request{ID: 2, SQL: "SELECT 1"}); rep.Err != "" {
		t.Fatalf("after an error: %q", rep.Err)
	}
}

func TestChildCapsRows(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	rep := c.ask(t, request{ID: 1, SQL: "SELECT * FROM range(10)", MaxRows: 3})
	if len(rep.Rows) != 3 || !rep.Truncated {
		t.Fatalf("rows = %d, truncated = %v; want 3 and true", len(rep.Rows), rep.Truncated)
	}
	rep = c.ask(t, request{ID: 2, SQL: "SELECT * FROM range(10)"})
	if len(rep.Rows) != 10 || rep.Truncated {
		t.Fatalf("rows = %d, truncated = %v; want 10 and false", len(rep.Rows), rep.Truncated)
	}
}

// TestChildExecKeepsState: an in-memory table made by Exec stays for later
// queries (PR 3 loads the open log this way, once per child).
func TestChildExecKeepsState(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	if rep := c.ask(t, request{ID: 1, SQL: "CREATE TABLE hot AS SELECT 7 AS n", Exec: true}); rep.Err != "" {
		t.Fatal(rep.Err)
	}
	if rep := c.ask(t, request{ID: 2, SQL: "SELECT n FROM hot"}); rep.Err != "" || rep.Rows[0][0] != json.Number("7") {
		t.Fatalf("reply = %+v", rep)
	}
}

// TestChildConfinedToAllowedDirs is Review Focus 1: SQL reads and writes
// inside the allowed directory and nowhere else, a sibling whose name
// starts the same included, and cannot lift the lock.
func TestChildConfinedToAllowedDirs(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"raw/in.csv", "rawx/in.csv", "twillingate.db"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("a\n1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := startChild(t, childArgs(dir))
	id := uint64(0)
	run := func(sql string) string {
		id++
		return c.ask(t, request{ID: id, SQL: sql}).Err
	}
	if err := run("SELECT * FROM read_csv('" + filepath.Join(dir, "raw/in.csv") + "')"); err != "" {
		t.Errorf("reading inside raw: %s", err)
	}
	if err := run("COPY (SELECT 1 AS a) TO '" + filepath.Join(dir, "raw/out.parquet") + "' (FORMAT parquet)"); err != "" {
		t.Errorf("writing inside raw: %s", err)
	}
	for _, sql := range []string{
		"SELECT * FROM read_csv('" + filepath.Join(dir, "rawx/in.csv") + "')",
		"SELECT * FROM read_text('" + filepath.Join(dir, "twillingate.db") + "')",
		"COPY (SELECT 1 AS a) TO '" + filepath.Join(dir, "twillingate.db") + "' (FORMAT csv)",
		"ATTACH '" + filepath.Join(dir, "twillingate.db") + "' AS meta",
		"SELECT * FROM read_text('/etc/hostname')",
		"INSTALL httpfs",
	} {
		if err := run(sql); !strings.Contains(err, "Permission Error") {
			t.Errorf("%s: err = %q, want a permission error", sql, err)
		}
	}
	for _, sql := range []string{"SET enable_external_access = true", "SET memory_limit = '4GB'"} {
		if err := run(sql); !strings.Contains(err, "locked") {
			t.Errorf("%s: err = %q, want the configuration locked", sql, err)
		}
	}
}

// slowSQL runs for minutes unless interrupted.
const slowSQL = "SELECT count(*) FROM range(1000000000000) t(i) WHERE i % 7 = 3"

// TestChildCancelInterrupts is Review Focus 3 at the child: a cancelled
// query stops and the child answers the next one at once.
func TestChildCancelInterrupts(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	if err := c.enc.Encode(request{ID: 1, SQL: slowSQL}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := c.enc.Encode(request{ID: 1, Cancel: true}); err != nil {
		t.Fatal(err)
	}
	var rep reply
	if err := c.dec.Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.ID != 1 || rep.Err == "" || time.Since(start) > 5*time.Second {
		t.Fatalf("reply = %+v after %v; want an error for id 1 within 5s", rep, time.Since(start))
	}
	if rep := c.ask(t, request{ID: 2, SQL: "SELECT 1"}); rep.Err != "" {
		t.Fatalf("after a cancel: %q", rep.Err)
	}
}

// TestChildExitsOnEOF is Review Focus 4: the parent's end of stdin closing
// (the server exited) ends the child even with a query in flight.
func TestChildExitsOnEOF(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	if err := c.enc.Encode(request{ID: 1, SQL: slowSQL}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	go io.Copy(io.Discard, c.out) // a pipe write blocks until read: take the cancelled reply
	c.in.Close()
	select {
	case err := <-c.done:
		c.done <- err // for the cleanup's receive
		if err != nil {
			t.Fatalf("ServeChild = %v, want nil on EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeChild still running 5s after stdin closed")
	}
}

func TestChildRefusesABadSetup(t *testing.T) {
	inR, _ := io.Pipe()
	var out strings.Builder
	args := append(childArgs(t.TempDir()), "-memory", "lots")
	if err := ServeChild(args, inR, &out); err == nil {
		t.Fatal("ServeChild = nil, want an error for memory_limit 'lots'")
	}
	var rep reply
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil || rep.Ready || rep.Err == "" {
		t.Fatalf("output = %q, want one error line", out.String())
	}
}
```

Note on `TestChildExitsOnEOF`: after stdin closes, the child writes the cancelled query's reply before it returns. An `io.Pipe` write blocks until something reads, so the test drains `c.out` first; nothing else reads the decoder by then. A real process writes into the OS pipe's buffer and does not block.

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./internal/engine 2>&1 | tail -5`
Expected: compile error, `undefined: ServeChild`.

- [ ] **Step 5: Write the child**

`internal/engine/child.go`:

```go
// Package engine runs DuckDB in a child process of this binary (raw-first
// spec D8): the ingest process never initializes DuckDB, a crash or an
// out-of-memory error in widget SQL takes down only the child, a child
// that exits gives its memory back, and the child can reach one directory
// and nothing else. ServeChild is the child's end of the pipe; Engine
// (engine.go) is the parent's.
package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync"

	_ "github.com/duckdb/duckdb-go/v2" // registers the "duckdb" driver
)

// DefaultMemoryLimit is ENGINE_MEMORY_LIMIT's default (D8): at 256 MB a
// COUNT(DISTINCT) over 3 years fails.
const DefaultMemoryLimit = "512MB"

// ServeChild is `twillingate engine`: it opens an in-memory DuckDB locked
// to the -allow directories, writes {"ready":true}, then answers one
// request per line until stdin closes, when it cancels what is in flight
// and returns nil. A setup failure is written as one {"err":…} line and
// returned.
func ServeChild(args []string, in io.Reader, out io.Writer) error {
	w := &replyWriter{enc: json.NewEncoder(out)}
	fs := flag.NewFlagSet("engine", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	threads := fs.Int("threads", 1, "DuckDB threads")
	memory := fs.String("memory", DefaultMemoryLimit, "DuckDB memory_limit")
	spill := fs.String("spill", "", "DuckDB temp_directory")
	parallel := fs.Int("parallel", 1, "requests run at once")
	var allow dirList
	fs.Var(&allow, "allow", "a directory SQL may read and write (repeatable)")
	if err := fs.Parse(args); err != nil {
		return w.fail(fmt.Errorf("engine: %w", err))
	}
	if *parallel < 1 {
		*parallel = 1
	}
	db, err := open(*threads, *memory, *spill, allow, *parallel)
	if err != nil {
		return w.fail(err)
	}
	defer db.Close()
	w.send(reply{Ready: true})
	return serve(db, in, w, *parallel)
}

// open starts an in-memory DuckDB and locks it down. Every setting is
// GLOBAL, so it holds on each connection the pool opens later, and
// lock_configuration stops SQL from changing any of them back. Directories
// get a trailing slash, so allowing raw does not allow rawx.
func open(threads int, memory, spill string, allow []string, parallel int) (*sql.DB, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	db.SetMaxOpenConns(parallel)
	dirs := make([]string, len(allow))
	for i, d := range allow {
		dirs[i] = sqlString(strings.TrimSuffix(d, "/") + "/")
	}
	setup := []string{
		fmt.Sprintf("SET GLOBAL threads = %d", threads),
		"SET GLOBAL memory_limit = " + sqlString(memory),
	}
	if spill != "" {
		setup = append(setup, "SET GLOBAL temp_directory = "+sqlString(spill))
	}
	setup = append(setup,
		"SET GLOBAL allowed_directories = ["+strings.Join(dirs, ", ")+"]",
		"SET GLOBAL autoinstall_known_extensions = false",
		"SET GLOBAL autoload_known_extensions = false",
		"SET GLOBAL enable_external_access = false",
		"SET GLOBAL lock_configuration = true",
	)
	for _, s := range setup {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("engine: %s: %w", s, err)
		}
	}
	return db, nil
}

// serve answers requests until in ends. Each request runs in its own
// goroutine, at most parallel at once (D8: one or two widgets at a time
// per child on small servers); a cancel line interrupts the request with
// its id.
func serve(db *sql.DB, in io.Reader, w *replyWriter, parallel int) error {
	dec := json.NewDecoder(in)
	var mu sync.Mutex
	cancels := map[uint64]context.CancelFunc{}
	running := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	defer func() {
		mu.Lock()
		for _, cancel := range cancels {
			cancel()
		}
		mu.Unlock()
		wg.Wait()
	}()
	for {
		var r request
		if err := dec.Decode(&r); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("engine: reading a request: %w", err)
		}
		if r.Cancel {
			mu.Lock()
			if cancel := cancels[r.ID]; cancel != nil {
				cancel()
			}
			mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		mu.Lock()
		cancels[r.ID] = cancel
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				mu.Lock()
				delete(cancels, r.ID)
				mu.Unlock()
				cancel()
			}()
			select {
			case running <- struct{}{}:
			case <-ctx.Done():
				w.send(reply{ID: r.ID, Err: ctx.Err().Error()})
				return
			}
			defer func() { <-running }()
			w.send(run(ctx, db, r))
		}()
	}
}

// run executes one request.
func run(ctx context.Context, db *sql.DB, r request) reply {
	rep := reply{ID: r.ID}
	if r.Exec {
		if _, err := db.ExecContext(ctx, r.SQL); err != nil {
			rep.Err = err.Error()
		}
		return rep
	}
	rows, err := db.QueryContext(ctx, r.SQL)
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	vals := make([]any, len(types))
	ptrs := make([]any, len(types))
	for i := range vals {
		ptrs[i] = &vals[i]
		rep.Columns = append(rep.Columns, types[i].Name())
	}
	for rows.Next() {
		if r.MaxRows > 0 && len(rep.Rows) == r.MaxRows {
			rep.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return reply{ID: r.ID, Err: err.Error()}
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = jsonValue(v, types[i].DatabaseTypeName())
		}
		rep.Rows = append(rep.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return reply{ID: r.ID, Err: err.Error()}
	}
	return rep
}

// replyWriter serializes lines from concurrent requests. A write error
// means the parent is gone; stdin's EOF ends the child right after.
type replyWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (w *replyWriter) send(r reply) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.enc.Encode(r)
}

func (w *replyWriter) fail(err error) error {
	w.send(reply{Err: err.Error()})
	return err
}

// dirList is a repeatable string flag.
type dirList []string

func (d *dirList) String() string     { return strings.Join(*d, ",") }
func (d *dirList) Set(v string) error { *d = append(*d, v); return nil }

// sqlString quotes s as a SQL string literal.
func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
```

`internal/engine/values.go`:

```go
package engine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/google/uuid"
)

// jsonValue turns what DuckDB scans into a value JSON carries faithfully:
// exact numbers stay numbers (json.Number, so an int64, a DECIMAL or a
// HUGEINT is never rounded through float64), dates and times become text,
// a UUID its usual text form. dbType is the column's DatabaseTypeName;
// values nested in a list or struct pass "" and keep the generic forms.
func jsonValue(v any, dbType string) any {
	switch x := v.(type) {
	case nil, bool, string, float32, float64, int8, int16, int32, int64, uint8, uint16, uint32, uint64:
		return x
	case time.Time:
		switch dbType {
		case "DATE":
			return x.Format("2006-01-02")
		case "TIME":
			return x.Format("15:04:05.999999")
		}
		return x.UTC().Format(time.RFC3339Nano)
	case []byte:
		if dbType == "UUID" && len(x) == 16 {
			return uuid.UUID(x).String()
		}
		return x // base64, as encoding/json writes bytes
	case duckdb.Decimal:
		return json.Number(x.String())
	case *big.Int:
		return json.Number(x.String())
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonValue(e, "")
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = jsonValue(e, "")
		}
		return out
	default:
		return fmt.Sprint(x) // MAP, INTERVAL, UNION: their text form
	}
}
```

- [ ] **Step 6: Run the child tests**

Run: `go test ./internal/engine -v 2>&1 | grep -E "^(--- |ok|FAIL|panic)"`
Expected: every `TestChild…` passes. TIME was not among the probed types: if it scans as something other than `time.Time`, add its case to `jsonValue` (text `HH:MM:SS`) and ledger it. If `TestChildKeepsTypes` reports `json.Number("1.5")` against a `float64`, the reply decoded without `UseNumber`: fix the test harness, not `jsonValue`.

- [ ] **Step 7: Tidy, and fail the three repo-wide checks the new package trips**

Run: `go mod tidy && go test ./internal/archtest ./internal/sqlcheck 2>&1 | grep -E "FAIL|engine" | head; make vet 2>&1 | tail -3`
Expected, three failures:
- archtest: `internal/engine: not in the rank table`;
- sqlcheck: `engine/child.go: SQL built at run time` (the setup statements, and `run`'s `r.SQL`);
- `make vet`: its `GOOS=linux GOARCH=arm` pass fails, since the DuckDB bindings have no 32-bit ARM build.

- [ ] **Step 8: Fix all three**

`internal/archtest/archtest_test.go`, rank table, after `"internal/enrich": 0,`:

```go
	"internal/engine":            0,
```

`internal/sqlcheck/sqlcheck_test.go`: below `migrationFile`, add

```go
// duckdbDir holds the code whose SQL is DuckDB's, not SQLite's: the engine
// child (raw-first spec D8). Preparing it against SQLite proves nothing;
// internal/engine's tests run it against DuckDB.
const duckdbDir = "engine/"
```

and in `scanInternal` replace `if !migrationSteps[siteFile(s)] {` with

```go
		if f := siteFile(s); !migrationSteps[f] && !strings.HasPrefix(f, duckdbDir) {
```

Then add this test after `TestDynamicSitesAreListed`:

```go
// TestDuckDBSitesAreNotPrepared: the engine's SQL is found by the scan and
// left out of the SQLite checks, rather than missed.
func TestDuckDBSitesAreNotPrepared(t *testing.T) {
	all, err := scanSource("..")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range all {
		found = found || strings.HasPrefix(siteFile(s), duckdbDir)
	}
	if !found {
		t.Fatalf("no SQL site under internal/%s: the scan no longer sees the engine, so this exclusion guards nothing", duckdbDir)
	}
	for _, s := range scanInternal(t) {
		if strings.HasPrefix(siteFile(s), duckdbDir) {
			t.Errorf("%s: DuckDB SQL reached the SQLite checks", s.pos)
		}
	}
}
```

`Makefile`: replace the `vet` target and its comment with

```make
# vet covers the two release targets' code paths alike: both are 64-bit,
# and 32-bit ARM is no longer built (DuckDB has no build for it, raw-first
# spec D10).
vet:
	go vet $(GOPKGS)
```

`CLAUDE.md`, Layout block: after the `internal/geo/` line add

```
internal/engine/     DuckDB in a child process of the binary (`twillingate engine`): the pipe's two ends, the slot semaphore
```

and in "Commits and releases", add `engine` to the scope list after `pipeline`.

- [ ] **Step 9: Run the checks again**

Run: `go test ./internal/engine ./internal/archtest ./internal/sqlcheck 2>&1 | tail -4 && make vet 2>&1 | tail -2`
Expected: three `ok` lines; vet prints nothing after the command echo.

- [ ] **Step 10: Commit**

```bash
git add go.mod go.sum internal/engine internal/archtest/archtest_test.go internal/sqlcheck/sqlcheck_test.go Makefile CLAUDE.md
git commit -m "feat(engine): run DuckDB locked to one directory behind a line protocol"
```

---

### Task 2: the parent end, `Engine`, and the slot semaphore

**Files:**
- Create: `internal/engine/engine.go`, `internal/engine/slots.go`, `internal/engine/engine_test.go`, `internal/engine/slots_test.go`

**Interfaces:**
- Consumes: `ServeChild`, `request`, `reply`, `DefaultMemoryLimit` (Task 1).
- Produces (Task 3 uses all of these):

```go
type Options struct {
	Path        string        // the executable to spawn: the running binary
	Args        []string      // before the child's flags: {"engine"}
	Env         []string      // added to the child's environment
	AllowedDirs []string      // the only directories the child's SQL may read or write
	SpillRoot   string        // each child spills into its own directory under this
	SlotDir     string        // lock files bounding children across processes
	MaxChildren int           // ENGINE_MAX_CHILDREN; at least 1
	MemoryLimit string        // ENGINE_MEMORY_LIMIT, DuckDB's syntax
	Threads     int           // 0: the CPUs
	Parallel    int           // 0: 1 at 2 threads or fewer, else 2
	IdleExit    time.Duration // 0: DefaultIdleExit
}
const DefaultIdleExit = 3 * time.Minute
func New(o Options) (*Engine, error)
func (e *Engine) Query(ctx context.Context, sql string, maxRows int) (Result, error) // maxRows ≤ 0: every row
func (e *Engine) Exec(ctx context.Context, sql string) error
func (e *Engine) Close() error
type Result struct { Columns []string; Rows [][]any; Truncated bool } // numbers as json.Number
type QueryError struct{ Msg string }        // the SQL failed; the child is fine
var ErrSpawn = errors.New("engine: the child did not start")
var ErrChildExited = errors.New("engine: the child exited")
var ErrClosed = errors.New("engine: closed")
```

- [ ] **Step 1: Write the failing slot tests**

`internal/engine/slots_test.go`:

```go
package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSlotsBoundHolders(t *testing.T) {
	dir := t.TempDir()
	a, err := acquireSlot(context.Background(), dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := acquireSlot(ctx, dir, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire = %v, want the deadline: the one slot is held", err)
	}
	a.release()
	b, err := acquireSlot(context.Background(), dir, 1)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	b.release()
}

func TestSlotsAllowMaxHolders(t *testing.T) {
	dir := t.TempDir()
	var held []*slot
	for i := 0; i < 3; i++ {
		s, err := acquireSlot(context.Background(), dir, 3)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		held = append(held, s)
	}
	for _, s := range held {
		s.release()
	}
}
```

- [ ] **Step 2: Write the failing engine tests**

`internal/engine/engine_test.go`:

```go
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// childEnv makes this test binary act as `twillingate engine` when an
// Engine spawns it, so these tests exercise a real process and pipe.
const childEnv = "ENGINE_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		if err := ServeChild(os.Args[1:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testOptions(t *testing.T) Options {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return Options{Path: self, Env: []string{childEnv + "=1"},
		AllowedDirs: []string{filepath.Join(dir, "raw")}, SpillRoot: filepath.Join(dir, "spill"),
		SlotDir: filepath.Join(dir, "slots"), MaxChildren: 1, MemoryLimit: "256MB", Threads: 1,
		IdleExit: time.Minute}
}

func newEngine(t *testing.T, o Options) *Engine {
	t.Helper()
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// eventually polls cond for up to 5s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("after 5s: %s", what)
}

func TestQuerySpawnsAndReusesAChild(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if e.pid() != 0 {
		t.Fatal("a child before the first query")
	}
	res, err := e.Query(context.Background(), "SELECT 42 AS answer", 0)
	if err != nil || len(res.Rows) != 1 || fmt.Sprint(res.Rows[0][0]) != "42" {
		t.Fatalf("Query = %+v, %v", res, err)
	}
	pid := e.pid()
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil || e.pid() != pid {
		t.Fatalf("second query: err %v, pid %d → %d; want the same child", err, pid, e.pid())
	}
}

func TestQueryErrorIsTypedAndKeepsTheChild(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	pid := e.pid()
	_, err := e.Query(context.Background(), "SELECT nope", 0)
	var qe *QueryError
	if !errors.As(err, &qe) || e.pid() != pid {
		t.Fatalf("err = %v (pid %d → %d), want a *QueryError from the same child", err, pid, e.pid())
	}
}

func TestExecThenQuery(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if err := e.Exec(context.Background(), "CREATE TABLE hot AS SELECT 7 AS n"); err != nil {
		t.Fatal(err)
	}
	res, err := e.Query(context.Background(), "SELECT n FROM hot", 0)
	if err != nil || fmt.Sprint(res.Rows[0][0]) != "7" {
		t.Fatalf("Query = %+v, %v", res, err)
	}
}

// TestIdleChildExits: an idle child leaves, freeing its slot and its spill
// directory, and the next query starts another.
func TestIdleChildExits(t *testing.T) {
	o := testOptions(t)
	o.IdleExit = 200 * time.Millisecond
	e := newEngine(t, o)
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	pid := e.pid()
	eventually(t, "the idle child gone", func() bool { return e.pid() == 0 })
	eventually(t, "the slot free", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		s, err := acquireSlot(ctx, o.SlotDir, 1)
		if err == nil {
			s.release()
		}
		return err == nil
	})
	eventually(t, "the spill directory removed", func() bool {
		entries, err := os.ReadDir(o.SpillRoot)
		return err == nil && len(entries) == 0
	})
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil || e.pid() == 0 || e.pid() == pid {
		t.Fatalf("after idle: err %v, pid %d (was %d); want a new child", err, e.pid(), pid)
	}
}

// TestCrashedChildIsReplaced is Review Focus 2.
func TestCrashedChildIsReplaced(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	pid := e.pid()
	errc := make(chan error, 1)
	go func() {
		_, err := e.Query(context.Background(), slowSQL, 0)
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, ErrChildExited) {
			t.Fatalf("in-flight query = %v, want ErrChildExited", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight query still waiting 5s after its child was killed")
	}
	eventually(t, "the dead child cleared", func() bool { return e.pid() == 0 })
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil || e.pid() == pid {
		t.Fatalf("after the crash: err %v, pid %d (was %d); want a new child", err, e.pid(), pid)
	}
}

// TestCancelInterruptsTheQuery is Review Focus 3: the caller gets its
// context's error, and the child, which runs one query at a time here,
// answers the next query at once.
func TestCancelInterruptsTheQuery(t *testing.T) {
	e := newEngine(t, testOptions(t))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := e.Query(ctx, slowSQL, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	start := time.Now()
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("next query: %v after %v; the slow one was not interrupted", err, time.Since(start))
	}
}

// TestSlotsBoundChildrenAcrossEngines: two engines on one slot directory
// with one slot run one child between them.
func TestSlotsBoundChildrenAcrossEngines(t *testing.T) {
	o := testOptions(t)
	a, b := newEngine(t, o), newEngine(t, o)
	if _, err := a.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := b.Query(ctx, "SELECT 1", 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second engine = %v, want the deadline while the only slot is held", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatalf("after the first engine closed: %v", err)
	}
}

func TestBadMemoryLimitFailsTheSpawnAndFreesTheSlot(t *testing.T) {
	o := testOptions(t)
	o.MemoryLimit = "lots"
	e := newEngine(t, o)
	if _, err := e.Query(context.Background(), "SELECT 1", 0); !errors.Is(err, ErrSpawn) {
		t.Fatalf("err = %v, want ErrSpawn", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := acquireSlot(ctx, o.SlotDir, 1)
	if err != nil {
		t.Fatalf("slot still held after a failed spawn: %v", err)
	}
	s.release()
}

func TestClosedEngineRefuses(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Query(context.Background(), "SELECT 1", 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/engine 2>&1 | tail -5`
Expected: compile errors, `undefined: acquireSlot`, `undefined: Options`.

- [ ] **Step 4: Write the semaphore**

`internal/engine/slots.go`:

```go
package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// slot is one of max lock files in a directory, held with flock for as
// long as its child lives: every process on the data directory counts the
// same files, so ENGINE_MAX_CHILDREN bounds children across processes (D8).
// The kernel drops the lock when the file closes, a crashed process
// included. The file is opened close-on-exec, so the child never holds it.
type slot struct{ f *os.File }

// acquireSlot takes a free slot, polling until ctx ends.
func acquireSlot(ctx context.Context, dir string, max int) (*slot, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	for {
		for i := 0; i < max; i++ {
			f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("slot-%d", i)), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				return nil, fmt.Errorf("engine: %w", err)
			}
			if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
				return &slot{f: f}, nil
			}
			f.Close()
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("engine: all %d slots busy (ENGINE_MAX_CHILDREN): %w", max, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *slot) release() { s.f.Close() }
```

- [ ] **Step 5: Write the engine**

`internal/engine/engine.go`:

```go
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"
)

// DefaultIdleExit is how long a child waits without a request before it
// exits and gives its memory back (D8: "a few idle minutes").
const DefaultIdleExit = 3 * time.Minute

var (
	// ErrSpawn: the child could not be started or did not get ready.
	ErrSpawn = errors.New("engine: the child did not start")
	// ErrChildExited: the child died with the request in flight (killed for
	// memory, crashed); the next request starts another.
	ErrChildExited = errors.New("engine: the child exited")
	// ErrClosed: the engine was closed.
	ErrClosed = errors.New("engine: closed")
)

// QueryError is SQL the child ran and DuckDB refused; the child is fine.
type QueryError struct{ Msg string }

func (e *QueryError) Error() string { return e.Msg }

// Result is a query's rows. Numbers are json.Number; dates, times and
// UUIDs are strings; NULL is nil.
type Result struct {
	Columns   []string
	Rows      [][]any
	Truncated bool // maxRows cut the result short
}

// Options configure the children an Engine spawns. AllowedDirs must not
// hold the SQLite file or anything else widget SQL must not reach: SQL in
// the child can read and write there (app.EngineOptions allows raw/ only).
type Options struct {
	Path        string
	Args        []string
	Env         []string
	AllowedDirs []string
	SpillRoot   string
	SlotDir     string
	MaxChildren int
	MemoryLimit string
	Threads     int
	Parallel    int
	IdleExit    time.Duration
}

// Engine runs requests on one child at a time, spawned on the first
// request, kept while requests come, and told to exit after IdleExit
// without one.
type Engine struct {
	o       Options
	spawnMu sync.Mutex // one spawn at a time
	mu      sync.Mutex // guards everything below and each child's mutable fields
	next    uint64
	child   *child
	closed  bool
}

type child struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	enc     *json.Encoder
	pending map[uint64]chan reply
	busy    int
	idle    *time.Timer
	slot    *slot
	spill   string
	done    chan struct{} // closed after exit, once the slot and spill are released
}

// New fills the defaults and makes the directories absolute: the child
// resolves paths from its own working directory, DuckDB's allow list
// needs them whole.
func New(o Options) (*Engine, error) {
	if o.Threads <= 0 {
		o.Threads = runtime.NumCPU()
	}
	if o.Parallel <= 0 {
		o.Parallel = 1
		if o.Threads > 2 {
			o.Parallel = 2
		}
	}
	if o.MaxChildren < 1 {
		o.MaxChildren = 1
	}
	if o.MemoryLimit == "" {
		o.MemoryLimit = DefaultMemoryLimit
	}
	if o.IdleExit <= 0 {
		o.IdleExit = DefaultIdleExit
	}
	o.AllowedDirs = slices.Clone(o.AllowedDirs)
	for i, d := range o.AllowedDirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, fmt.Errorf("engine: %w", err)
		}
		o.AllowedDirs[i] = abs
	}
	for _, p := range []*string{&o.SpillRoot, &o.SlotDir} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return nil, fmt.Errorf("engine: %w", err)
		}
		*p = abs
	}
	return &Engine{o: o}, nil
}

// Query runs sql and returns at most maxRows rows (all when maxRows ≤ 0).
func (e *Engine) Query(ctx context.Context, sql string, maxRows int) (Result, error) {
	rep, err := e.do(ctx, request{SQL: sql, MaxRows: maxRows})
	if err != nil {
		return Result{}, err
	}
	return Result{Columns: rep.Columns, Rows: rep.Rows, Truncated: rep.Truncated}, nil
}

// Exec runs sql for its effect on the child, such as a table later
// queries read; the effect lasts as long as the child.
func (e *Engine) Exec(ctx context.Context, sql string) error {
	_, err := e.do(ctx, request{SQL: sql, Exec: true})
	return err
}

func (e *Engine) do(ctx context.Context, r request) (reply, error) {
	c, err := e.start(ctx)
	if err != nil {
		return reply{}, err
	}
	ch := make(chan reply, 1)
	e.mu.Lock()
	e.next++
	r.ID = e.next
	c.pending[r.ID] = ch
	err = c.enc.Encode(r)
	e.mu.Unlock()
	defer e.finish(c, r.ID)
	if err != nil {
		return reply{}, fmt.Errorf("%w: %v", ErrChildExited, err)
	}
	select {
	case rep := <-ch:
		return answer(rep)
	case <-c.done:
		select {
		case rep := <-ch:
			return answer(rep)
		default:
			return reply{}, ErrChildExited
		}
	case <-ctx.Done():
		e.mu.Lock()
		_ = c.enc.Encode(request{ID: r.ID, Cancel: true})
		e.mu.Unlock()
		return reply{}, ctx.Err()
	}
}

func answer(rep reply) (reply, error) {
	if rep.Err != "" {
		return reply{}, &QueryError{Msg: rep.Err}
	}
	return rep, nil
}

// start returns the running child with one more request counted on it,
// spawning one when there is none.
func (e *Engine) start(ctx context.Context) (*child, error) {
	e.spawnMu.Lock()
	defer e.spawnMu.Unlock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, ErrClosed
	}
	if c := e.child; c != nil {
		c.busy++
		if c.idle != nil {
			c.idle.Stop()
			c.idle = nil
		}
		e.mu.Unlock()
		return c, nil
	}
	e.mu.Unlock()
	c, err := e.spawn(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		c.stdin.Close()
		return nil, ErrClosed
	}
	e.child = c
	c.busy++
	return c, nil
}

// finish forgets a request and, when it was the child's last, arms the
// idle timer.
func (e *Engine) finish(c *child, id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(c.pending, id)
	c.busy--
	if c.busy == 0 && e.child == c {
		c.idle = time.AfterFunc(e.o.IdleExit, func() { e.retire(c) })
	}
}

// retire tells an idle child to exit by closing its stdin, unless a
// request arrived since the timer fired.
func (e *Engine) retire(c *child) {
	e.mu.Lock()
	if e.child != c || c.busy > 0 {
		e.mu.Unlock()
		return
	}
	e.child = nil
	e.mu.Unlock()
	c.stdin.Close()
}

// spawn takes a slot, starts the child and waits for its ready line.
func (e *Engine) spawn(ctx context.Context) (*child, error) {
	sl, err := acquireSlot(ctx, e.o.SlotDir, e.o.MaxChildren)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			sl.release()
		}
	}()
	for _, d := range append([]string{e.o.SpillRoot}, e.o.AllowedDirs...) {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
		}
	}
	spill, err := os.MkdirTemp(e.o.SpillRoot, "child-")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	defer func() {
		if !ok {
			os.RemoveAll(spill)
		}
	}()
	args := append(slices.Clone(e.o.Args),
		"-threads", strconv.Itoa(e.o.Threads), "-memory", e.o.MemoryLimit,
		"-spill", spill, "-parallel", strconv.Itoa(e.o.Parallel))
	for _, d := range e.o.AllowedDirs {
		args = append(args, "-allow", d)
	}
	cmd := exec.Command(e.o.Path, args...)
	cmd.Env = append(os.Environ(), e.o.Env...)
	cmd.Stderr = os.Stderr // a panic in the child reaches the server's log
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	dec := json.NewDecoder(stdout)
	dec.UseNumber()
	var first reply
	readErr := make(chan error, 1)
	go func() { readErr <- dec.Decode(&first) }()
	select {
	case err = <-readErr:
		if err == nil && !first.Ready {
			err = errors.New(first.Err)
		}
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("%w: %w", ErrSpawn, err)
	}
	ok = true
	c := &child{cmd: cmd, stdin: stdin, enc: json.NewEncoder(stdin), pending: map[uint64]chan reply{},
		slot: sl, spill: spill, done: make(chan struct{})}
	go e.read(c, dec)
	return c, nil
}

// read delivers replies until the child's stdout ends, then reaps the
// child and frees what it held.
func (e *Engine) read(c *child, dec *json.Decoder) {
	for {
		var rep reply
		if err := dec.Decode(&rep); err != nil {
			break
		}
		e.mu.Lock()
		ch := c.pending[rep.ID]
		e.mu.Unlock()
		if ch != nil {
			ch <- rep
		}
	}
	c.stdin.Close()
	c.cmd.Wait()
	c.slot.release()
	os.RemoveAll(c.spill)
	e.mu.Lock()
	if e.child == c {
		e.child = nil
	}
	if c.idle != nil {
		c.idle.Stop()
	}
	e.mu.Unlock()
	close(c.done)
}

// Close tells the child to exit, kills it after 5s, and refuses later
// requests.
func (e *Engine) Close() error {
	e.mu.Lock()
	e.closed = true
	c := e.child
	e.child = nil
	e.mu.Unlock()
	if c == nil {
		return nil
	}
	c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		c.cmd.Process.Kill()
		<-c.done
	}
	return nil
}

// pid is the running child's process id, 0 when there is none (tests).
func (e *Engine) pid() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.child == nil {
		return 0
	}
	return e.child.cmd.Process.Pid
}
```

A note on the `ch <- rep` in `read`: `ch` is buffered with room for one, and each request gets at most one reply, so the send never blocks. A reply for a request that already finished (cancelled) finds no `pending` entry and is dropped.

- [ ] **Step 6: Run the engine tests, plain and under the race detector**

Run: `go test ./internal/engine 2>&1 | tail -3 && go test -race ./internal/engine 2>&1 | tail -3`
Expected: `ok` twice. A race report names a field read outside `e.mu`: fix the read, never the test.

- [ ] **Step 7: Commit**

```bash
git add internal/engine
git commit -m "feat(engine): spawn the child on demand, bounded by a slot semaphore, gone when idle"
```

---

### Task 3: config, `app.EngineOptions`, and `twillingate engine`

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Modify: `docs/deployment.md` (variable table; a troubleshooting line)
- Create: `internal/app/engine.go`, `internal/app/engine_test.go`
- Create: `cmd/twillingate/engine.go`, `cmd/twillingate/engine_test.go`
- Modify: `cmd/twillingate/main_test.go` (a `TestMain`)
- Modify: `.env.example`

**Interfaces:**
- Consumes: `engine.Options`, `engine.New`, `engine.DefaultMemoryLimit`, `engine.DefaultIdleExit`, `engine.ServeChild` (Tasks 1–2); `app.DatabasePath(dsn string) string` (exists).
- Produces: `config.Config.Engine EngineConfig{MemoryLimit string; MaxChildren int}`, `config.DefaultEngineMemoryLimit = "512MB"`, `config.DefaultEngineMaxChildren = 2`; `app.EngineOptions(cfg *config.Config) (engine.Options, error)`; `twillingate engine` (child) and `twillingate engine -check`. Task 4's Dockerfile, smoke test and installer run `engine -check`; PR 3 calls `app.EngineOptions`.

- [ ] **Step 1: Write the failing config tests**

In `internal/config/config_test.go`, extend `TestDefaultsApplied`:

```go
	if c.Engine.MemoryLimit != "512MB" || c.Engine.MaxChildren != 2 {
		t.Errorf("Engine = %+v, want 512MB and 2", c.Engine)
	}
```

Add to the invalid `cases` map of the test holding `"negative raw_days"`:

```go
		"zero engine children":     base(map[string]string{"ENGINE_MAX_CHILDREN": "0"}),
		"engine memory not a size": base(map[string]string{"ENGINE_MEMORY_LIMIT": "lots"}),
		"engine memory no unit":    base(map[string]string{"ENGINE_MEMORY_LIMIT": "512"}),
```

Add:

```go
func TestEngineFromEnv(t *testing.T) {
	for _, v := range []string{"1GB", "768MB", "1.5GB", "2GiB", "512mb"} {
		c, err := load(t, map[string]string{"ENGINE_MEMORY_LIMIT": v, "ENGINE_MAX_CHILDREN": "4"})
		if err != nil {
			t.Fatalf("ENGINE_MEMORY_LIMIT=%s: %v", v, err)
		}
		if c.Engine.MemoryLimit != v || c.Engine.MaxChildren != 4 {
			t.Errorf("Engine = %+v", c.Engine)
		}
	}
}
```

Run: `go test ./internal/config 2>&1 | tail -3`
Expected: compile error, `c.Engine undefined`.

- [ ] **Step 2: Add the config**

In `internal/config/config.go`, after `ReportingConfig`'s type, add:

```go
// EngineConfig sizes the DuckDB children (raw-first spec D8, D23).
type EngineConfig struct {
	// MemoryLimit (ENGINE_MEMORY_LIMIT) is each child's DuckDB
	// memory_limit, in DuckDB's units (MB is 10^6 bytes, MiB 2^20).
	MemoryLimit string
	// MaxChildren (ENGINE_MAX_CHILDREN) bounds the children running at
	// once across every process on the data directory.
	MaxChildren int
}

// The engine defaults: 512 MB is the least a COUNT(DISTINCT) over three
// years at the cap runs in, and two children fit a 2 GB host.
const (
	DefaultEngineMemoryLimit = "512MB"
	DefaultEngineMaxChildren = 2
)

// engineMemory is the memory_limit forms DuckDB takes, a unit required.
var engineMemory = regexp.MustCompile(`(?i)^[0-9]+(\.[0-9]+)?\s*(KB|MB|GB|TB|KiB|MiB|GiB|TiB)$`)
```

(add `"regexp"` to the imports if absent). Add `Engine EngineConfig` to `Config` beside `Reporting`. In the parse, beside `Reporting: ReportingConfig{…}`:

```go
		Engine: EngineConfig{
			MemoryLimit: e.str("ENGINE_MEMORY_LIMIT", DefaultEngineMemoryLimit),
			MaxChildren: e.num("ENGINE_MAX_CHILDREN", DefaultEngineMaxChildren),
		},
```

In the validation, before `return nil`:

```go
	if !engineMemory.MatchString(c.Engine.MemoryLimit) {
		return fmt.Errorf("config: ENGINE_MEMORY_LIMIT must be a size with a unit, like 512MB or 1GiB: %q", c.Engine.MemoryLimit)
	}
	if c.Engine.MaxChildren < 1 {
		return fmt.Errorf("config: ENGINE_MAX_CHILDREN must be at least 1: %d", c.Engine.MaxChildren)
	}
```

Run: `go test ./internal/config 2>&1 | tail -3`
Expected: `ok`.

- [ ] **Step 3: Document both variables in the same commit**

`docs/deployment.md`, variable table, after the `CONSOLE_QUERY_ROWS_MAX` row (or wherever `CONSOLE_*` rows end):

```markdown
| `ENGINE_MEMORY_LIMIT` | DuckDB's memory limit for each engine child, the process that answers analytics queries: `512MB`, `1GB`, `2GiB` (DuckDB's MB is 10^6 bytes). Outside `GOMEMLIMIT`, which bounds only the server. Default `512MB`. |
| `ENGINE_MAX_CHILDREN` | Engine children running at once, across every process on the data directory; a query waits for a free one. Default 2. |
```

Add to `.env.example`, after the `RETENTION_*` block:

```
#ENGINE_MEMORY_LIMIT=512MB
#ENGINE_MAX_CHILDREN=2
```

Run: `go test ./internal/api -run TestDeploymentDocumentsEveryEnvVar 2>&1 | tail -2`
Expected: `ok`.

- [ ] **Step 4: Commit the config**

```bash
git add internal/config docs/deployment.md .env.example
git commit -m "feat(config): add ENGINE_MEMORY_LIMIT and ENGINE_MAX_CHILDREN"
```

- [ ] **Step 5: Write the failing app test**

`internal/app/engine_test.go`:

```go
package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
)

// TestEngineOptionsKeepTheDatabaseOutOfReach is Review Focus 1: the child
// may write where it may read, so no allowed directory may be, or hold,
// the database's directory.
func TestEngineOptionsKeepTheDatabaseOutOfReach(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "twillingate.db")
	cfg := configtest.Load(t, map[string]string{"DATABASE_DSN": "sqlite://" + db,
		"ENGINE_MEMORY_LIMIT": "768MB", "ENGINE_MAX_CHILDREN": "3"})
	o, err := EngineOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.AllowedDirs) != 1 || o.AllowedDirs[0] != filepath.Join(dir, "raw") {
		t.Fatalf("AllowedDirs = %v, want only %s", o.AllowedDirs, filepath.Join(dir, "raw"))
	}
	for _, a := range o.AllowedDirs {
		if rel, err := filepath.Rel(a, db); err == nil && !strings.HasPrefix(rel, "..") {
			t.Errorf("allowed %s holds the database %s", a, db)
		}
	}
	if o.MemoryLimit != "768MB" || o.MaxChildren != 3 || o.Path == "" ||
		len(o.Args) != 1 || o.Args[0] != "engine" ||
		o.SlotDir != filepath.Join(dir, "engine", "slots") || o.SpillRoot != filepath.Join(dir, "engine", "spill") {
		t.Errorf("options = %+v", o)
	}
}
```

Run: `go test ./internal/app -run TestEngineOptions 2>&1 | tail -3`
Expected: compile error, `undefined: EngineOptions`.

- [ ] **Step 6: Write `EngineOptions`**

`internal/app/engine.go`:

```go
package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// EngineOptions lays the engine out beside the database: raw/ is the one
// directory its SQL reaches (PR 3 keeps the events there), engine/ holds
// the spill directories and the slot locks. The SQLite file stays outside
// raw/, so widget SQL never has a path to it (raw-first spec D8).
func EngineOptions(cfg *config.Config) (engine.Options, error) {
	self, err := os.Executable()
	if err != nil {
		return engine.Options{}, fmt.Errorf("engine: %w", err)
	}
	data := filepath.Dir(DatabasePath(cfg.Database))
	return engine.Options{
		Path:        self,
		Args:        []string{"engine"},
		AllowedDirs: []string{filepath.Join(data, "raw")},
		SpillRoot:   filepath.Join(data, "engine", "spill"),
		SlotDir:     filepath.Join(data, "engine", "slots"),
		MaxChildren: cfg.Engine.MaxChildren,
		MemoryLimit: cfg.Engine.MemoryLimit,
	}, nil
}
```

`internal/archtest` already ranks `internal/engine` 0 and `app` 3, so the import needs no table change.

Run: `go test ./internal/app -run TestEngineOptions 2>&1 | tail -3 && go test ./internal/archtest 2>&1 | tail -1`
Expected: `ok` twice.

- [ ] **Step 7: Write the failing command tests**

In `cmd/twillingate/main_test.go`, add (with `"os"` in its imports):

```go
// asBinary makes this test binary act as twillingate when a test spawns it
// as its own child: `engine -check` runs `<self> engine …`.
const asBinary = "TWILLINGATE_TEST_AS_BINARY"

func TestMain(m *testing.M) {
	if os.Getenv(asBinary) == "1" {
		os.Exit(run(os.Args[1:], os.Stdout))
	}
	os.Exit(m.Run())
}
```

`cmd/twillingate/engine_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineCheckSpawnsAChild(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATABASE_DSN", "sqlite://"+filepath.Join(dir, "twillingate.db"))
	t.Setenv(asBinary, "1")
	var out strings.Builder
	if code := run([]string{"engine", "-check"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.HasPrefix(out.String(), "engine ok: 42 in ") || !strings.Contains(out.String(), "DuckDB v") {
		t.Errorf("output = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "raw")); err != nil {
		t.Errorf("raw directory: %v", err)
	}
}

func TestEngineChildRefusesUnknownFlags(t *testing.T) {
	var out strings.Builder
	if code := run([]string{"engine", "-nope"}, &out); code != 1 || !strings.Contains(out.String(), `"err"`) {
		t.Fatalf("exit %d, output %q; want 1 and an error line", code, out.String())
	}
}
```

Run: `go test ./cmd/twillingate -run Engine 2>&1 | tail -3`
Expected: FAIL, `unknown command "engine"`.

- [ ] **Step 8: Write the subcommand**

`cmd/twillingate/engine.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// engine is left off the usage line: the server spawns it. `engine -check`
// is for operators and the build (docs/deployment.md).
func init() { commands["engine"] = cmdEngine }

func cmdEngine(args []string, stdout io.Writer) int {
	if len(args) == 1 && args[0] == "-check" {
		return engineCheck(stdout)
	}
	if err := engine.ServeChild(args, os.Stdin, stdout); err != nil {
		return 1
	}
	return 0
}

// engineCheck spawns a child the way the server will, with this
// environment's settings and data directory, and runs one query.
func engineCheck(stdout io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	opts, err := app.EngineOptions(cfg)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	e, err := engine.New(opts)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	res, err := e.Query(ctx, "SELECT 42 AS answer, version() AS duckdb", 1)
	if err != nil {
		fmt.Fprintln(stdout, "engine check failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "engine ok: %v in %d ms (spawn and first query), DuckDB %v\n",
		res.Rows[0][0], time.Since(start).Milliseconds(), res.Rows[0][1])
	return 0
}
```

Run: `go test ./cmd/twillingate 2>&1 | tail -3`
Expected: `ok`.

- [ ] **Step 9: Document `engine -check`**

`docs/deployment.md`, under "Routine operations" (the first list under `### Routine operations`), add an item:

```markdown
- `twillingate engine -check`, with the service's environment, starts an engine child the way the server does and runs one query: `engine ok: 42 in 63 ms (spawn and first query), DuckDB v1.5.6`. A failure names what stopped it (a missing library, `ENGINE_MEMORY_LIMIT`, all `ENGINE_MAX_CHILDREN` slots busy for 30 s).
```

- [ ] **Step 10: Commit**

```bash
git add internal/app/engine.go internal/app/engine_test.go cmd/twillingate/engine.go cmd/twillingate/engine_test.go cmd/twillingate/main_test.go docs/deployment.md
git commit -m "feat(cmd): add the engine subcommand and engine -check"
```

---

### Task 4: the build, the image, the installer and the docs

**Files:**
- Modify: `Makefile` (`build`, `build-all` removed, `dist`, `.PHONY`)
- Modify: `Dockerfile`
- Modify: `scripts/smoke.sh`, `scripts/test-install.sh`
- Modify: `deploy/systemd/install.sh`
- Modify: `docs/deployment.md` (Install requirements, Raspberry Pi section)
- Modify: `deploy/UPGRADES.md`
- Modify: `CLAUDE.md` ("Checks")

**Interfaces:**
- Consumes: `twillingate engine -check` (Task 3).
- Produces: `make dist` → `dist/twillingate-linux-<host arch>.tar.gz` (one per run, no `SHA256SUMS`; Task 5 sums both in the release job). Dockerfile stages `web-build`, `go-build` and `runtime` keep their names (`scripts/test-compose.sh` and `release.yml` target `runtime`).

- [ ] **Step 1: Write the failing smoke check**

`scripts/smoke.sh`: after `export DATABASE_DSN=…`, add

```bash
# The engine child (DuckDB, cgo) starts from this binary and answers.
./twillingate engine -check | grep -q '^engine ok: 42 ' || fail "engine -check failed"
```

Run: `CGO_ENABLED=0 go build -o twillingate ./cmd/twillingate 2>&1 | tail -2`
Expected: the build fails, since the DuckDB driver needs cgo. That is why `make build` must set `CGO_ENABLED=1`.

- [ ] **Step 2: Make the build cgo and host-only**

`Makefile`:
- Remove `build-all` from `.PHONY`.
- Replace `build`:

```make
# cgo: internal/engine links DuckDB's static library (raw-first spec D13),
# so a build needs a C toolchain and targets the host. Release binaries come
# from ubuntu-22.04 runners, one per architecture, for a glibc 2.35 floor
# (release.yml); a binary built here needs this host's glibc.
build: $(UI)
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/twillingate
```

- Delete the `build-all` target. Replace `dist` and its comment:

```make
# A release tarball for this host's architecture: binary + deploy/
# (installer, units, configs). release.yml runs it on an amd64 and an arm64
# runner and writes the SHA256SUMS the installer verifies over both.
ARCH = $(shell go env GOARCH)
dist: $(UI)
	@set -e; [ "$$(go env GOOS)" = linux ] || { echo "dist builds the Linux release, on Linux"; exit 1; }; \
	stage=dist/stage-$(ARCH); rm -rf $$stage; mkdir -p $$stage; \
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o $$stage/$(BIN) ./cmd/twillingate; \
	cp -r deploy $$stage/deploy; \
	cp .env.example LICENSE $$stage/; \
	tar -czf dist/$(BIN)-linux-$(ARCH).tar.gz -C $$stage .; \
	rm -rf $$stage; \
	echo "packaged dist/$(BIN)-linux-$(ARCH).tar.gz"
```

Run: `make build && make smoke 2>&1 | tail -3 && make dist && ls -la dist/ twillingate`
Expected: smoke passes; `dist/twillingate-linux-amd64.tar.gz` (on an amd64 host) exists; record the binary size for the PR (about 60 MB).

- [ ] **Step 3: The image on Debian, built natively per architecture**

`Dockerfile`: replace the header comment's sentence about cross-compiling and `CGO_ENABLED=0`, and the `go-build` and `runtime` stages:

```dockerfile
# The published image (--target runtime): the binary on debian:bookworm-slim.
# The binary links glibc and libstdc++ (DuckDB, cgo), so no Alpine. Node is a
# build-time dependency only, for the dashboards app the binary embeds.
```

```dockerfile
# No --platform=$BUILDPLATFORM: cgo does not cross-compile here, so this
# stage runs on the target's own platform. release.yml builds each
# architecture on a runner of that architecture; building the other one
# locally runs under QEMU, slowly.
FROM golang:1.25-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-build /src/internal/reporting/ui ./internal/reporting/ui
ARG VERSION=dev
# `COPY . .` changes on every commit, so this layer never comes from the layer
# cache; the build cache mount is what makes it incremental, and release.yml
# carries it between runs, since a CI builder starts empty.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X github.com/dmtrkzntsv/twillingate/internal/shared/version.Version=${VERSION}" \
    -o /out/twillingate ./cmd/twillingate

FROM debian:bookworm-slim AS runtime
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
# uid and gid 10001 as in the Alpine image, so existing volumes stay owned.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 twillingate \
    && useradd --system --no-create-home --uid 10001 --gid 10001 twillingate
COPY --from=go-build /out/twillingate /usr/local/bin/twillingate
# The engine child must start in this image before it ships.
RUN DATABASE_DSN=sqlite:///tmp/engine-check/check.db twillingate engine -check && rm -rf /tmp/engine-check
# Docker seeds a fresh named volume from the image directory, ownership
# included. Without this the volume mounts root-owned and the non-root
# process cannot create the database file on first boot.
RUN mkdir -p /var/lib/twillingate && chown twillingate:twillingate /var/lib/twillingate
VOLUME ["/var/lib/twillingate"]
USER twillingate
# Container defaults; override per-deployment via compose env_file/environment.
# INGEST_ADDR binds all interfaces here (unlike the bare-metal loopback
# default) because published ports reach the container's own IP, not lo.
ENV INGEST_ADDR=0.0.0.0:8080 \
    DATABASE_DSN=sqlite:///var/lib/twillingate/twillingate.db
ENTRYPOINT ["/usr/local/bin/twillingate"]
CMD ["serve"]
```

Remove the `ARG TARGETOS`, `ARG TARGETARCH` and `ARG TARGETVARIANT` lines and the `GOARM` comment from the old `go-build` stage (the new stage above replaces it whole). Keep the `web-build` stage as it is.

Run: `docker build --target runtime -t twillingate:enginetest . 2>&1 | tail -3 && docker run --rm -e DATABASE_DSN=sqlite:///tmp/x/t.db twillingate:enginetest engine -check && ./scripts/test-compose.sh 2>&1 | tail -3`
Expected: the build passes its `engine -check` step; the run prints `engine ok: 42 …`; the compose test passes. (Docker is required; if absent, note that in the ledger and leave it to the release dry run in Task 5.)

- [ ] **Step 4: The installer refuses what cannot run the binary**

`deploy/systemd/install.sh`, in `arch()`: replace the `armv7l|armv6l) echo arm ;;` line with nothing (the `*` branch already fails). Then, where `a="$(arch)" || die …` runs, replace that line with:

```bash
  a="$(arch)" || case "$(uname -m)" in
    armv7l|armv6l) die "32-bit ARM is no longer built; install a 64-bit OS (Raspberry Pi 3, 4, 5 and Zero 2 W run one)" ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac
  # The binary links glibc 2.35 or newer (DuckDB, cgo): Debian 12, Ubuntu
  # 22.04 or later. Alpine (musl) has none.
  glibc="$(getconf GNU_LIBC_VERSION 2>/dev/null | awk '{print $2}')"
  [ -n "$glibc" ] || die "needs glibc 2.35 or newer; this system has none (Alpine? use the Docker image)"
  [ "$(printf '%s\n2.35\n' "$glibc" | sort -V | head -n1)" = 2.35 ] ||
    die "needs glibc 2.35 or newer, found $glibc (Debian 12, Ubuntu 22.04 or later)"
```

`scripts/test-install.sh`: a host-built binary needs the host's glibc, newer than the test's Debian 12. Take the binary from the image instead. Replace the `[ -x twillingate ] || …` line and the `docker run` with:

```bash
command -v docker > /dev/null || { echo "docker is required"; exit 1; }
# The binary from the image: built on Debian 12, as the release is built on
# its glibc floor; a host build may need a newer glibc than the container has.
docker build --target runtime -t twillingate:installtest . > /dev/null
bin="$(mktemp -d)"
trap 'rm -rf "$bin"' EXIT
docker run --rm --entrypoint cat twillingate:installtest /usr/local/bin/twillingate > "$bin/twillingate"
chmod +x "$bin/twillingate"
docker run --rm -v "$PWD:/src:ro" -v "$bin/twillingate:/src-bin/twillingate:ro" debian:bookworm-slim \
  bash /src/scripts/test-install-inner.sh
```

and in `scripts/test-install-inner.sh`, wherever it copies `/src/twillingate`, read `/src-bin/twillingate` instead (`grep -n '/src/twillingate' scripts/test-install-inner.sh` finds the lines). Update the header comment's "Requires docker and a linux binary at the repo root" to "Requires docker". Then confirm the refusal on musl:

Run: `./scripts/test-install.sh 2>&1 | tail -3; docker run --rm alpine:3.20 sh -c 'getconf GNU_LIBC_VERSION 2>/dev/null || echo none'`
Expected: the install test passes; the Alpine line prints `none`, the condition the installer refuses on.

- [ ] **Step 5: Docs, the runbook, CLAUDE.md**

`docs/deployment.md`:
- At the top of `## Install`, before `### docker compose`, add:

```markdown
Linux on amd64 or arm64 with glibc 2.35 or newer (Debian 12, Ubuntu 22.04 or
later), or Docker anywhere. 32-bit ARM is not built. Plan on 1 GB
of memory: the server stays small (`GOMEMLIMIT` bounds it), and each engine
child, the process that answers analytics queries, may take up to
`ENGINE_MEMORY_LIMIT` (512 MB by default) for a few seconds; at most
`ENGINE_MAX_CHILDREN` run at once. `GOMEMLIMIT` does not cover them.
```

- In `### Raspberry Pi and low-resource hosts`, add a first item: `- A 64-bit OS (Raspberry Pi OS 64-bit on a Pi 3, 4, 5 or Zero 2 W); 32-bit ARM is not built. On 1 GB, set \`ENGINE_MAX_CHILDREN=1\`.`

`deploy/UPGRADES.md`, append:

```markdown
### Upgrading to the engine build (no migration)

The binary now carries DuckDB (cgo) for the raw-first analytics engine;
nothing uses it yet, and nothing changes for users. What changes for
operators:

- **32-bit ARM is no longer built.** A Raspberry Pi on the 32-bit OS needs
  the 64-bit OS first (a Pi 3, 4, 5 or Zero 2 W runs it); the installer
  refuses `armv7l` and `armv6l`. ARMv7 NAS boxes and boards stay on the
  previous release.
- **glibc 2.35 or newer** (Debian 12, Ubuntu 22.04 or later); the
  installer checks. Check first: `getconf GNU_LIBC_VERSION`.
- **The image is Debian-based** (`debian:bookworm-slim`, from Alpine), same
  user and ids (10001), same volume; `docker compose pull` as usual.
- The binary grows from about 15 MB to about 60 MB.
- Two settings, `ENGINE_MEMORY_LIMIT` and `ENGINE_MAX_CHILDREN`, and an
  `engine/` directory beside the database once an engine child has run
  (`twillingate engine -check` runs one). It holds spill files and lock
  files; Litestream replicates only the database, so nothing to change.

Rollback: the previous release's binary or image; nothing on disk differs.
```

`CLAUDE.md`, "Checks": after the sentence ending "`make test` runs the whole suite under `-race`, …" paragraph's first sentence (`` `make build` compiles; ``), change `` `make build` compiles; `` to `` `make build` compiles, with cgo (DuckDB, `internal/engine`), so it needs a C toolchain; ``.

- [ ] **Step 6: Run the whole check**

Run: `GOTMPDIR=$HOME/.cache/gotmp-engine make check 2>&1 | tail -15` (create the directory first; `/tmp` has a per-user quota)
Expected: vet clean, coverage gate met (`internal/engine` counts toward the 90% total), the SQLite-free packages pass under `-race`.

- [ ] **Step 7: Commit**

```bash
git add Makefile Dockerfile scripts/smoke.sh scripts/test-install.sh scripts/test-install-inner.sh deploy/systemd/install.sh docs/deployment.md deploy/UPGRADES.md CLAUDE.md
git commit -m "feat(deploy)!: build with cgo for amd64 and arm64 on glibc 2.35; drop 32-bit ARM

BREAKING CHANGE: no 32-bit ARM release; the binary needs glibc 2.35 or
newer, and the image is Debian-based."
```

---

### Task 5: the release workflow, a dry run, the PR

**Files:**
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: `make dist` for the host architecture (Task 4); `twillingate engine -check` (Task 3); the Dockerfile's `runtime` target (Task 4).

- [ ] **Step 1: Restructure the jobs**

`.github/workflows/release.yml`:

- **Inputs:** add, under `workflow_dispatch.inputs`:

```yaml
      dry_run:
        description: 'Build, check and push untagged image digests, but tag and publish nothing.'
        required: false
        type: boolean
        default: false
```

- **`version` job:** split the current `build` job's tag step into this new job. It runs on `ubuntu-latest`: checkout with `fetch-depth: 0`, then the `tag` step unchanged, with `outputs: tag`.
- **`build` job:** becomes a matrix and `needs: version`:

```yaml
  build:
    name: build (${{ matrix.arch }})
    needs: version
    strategy:
      matrix:
        include:
          # Ubuntu 22.04: its glibc 2.35 is the release's floor (Debian 12,
          # Ubuntu 22.04). The binary links it through cgo.
          - { arch: amd64, runner: ubuntu-22.04 }
          - { arch: arm64, runner: ubuntu-22.04-arm }
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod, cache: false }
      - uses: actions/setup-node@v4
        with: { node-version: 22, cache: npm, cache-dependency-path: web/package-lock.json }
      - run: make dist VERSION=${{ needs.version.outputs.tag }}
      # The roadmap's measurement: spawn to ready on each release runner.
      - name: engine check
        run: |
          mkdir -p "$RUNNER_TEMP/check"
          tar -xzf dist/twillingate-linux-${{ matrix.arch }}.tar.gz -C "$RUNNER_TEMP/check" ./twillingate
          DATABASE_DSN="sqlite://$RUNNER_TEMP/check/t.db" "$RUNNER_TEMP/check/twillingate" engine -check | tee -a "$GITHUB_STEP_SUMMARY"
          ls -l "$RUNNER_TEMP/check/twillingate" | tee -a "$GITHUB_STEP_SUMMARY"
      - uses: actions/upload-artifact@v4
        with:
          name: dist-${{ matrix.arch }}
          path: dist/*.tar.gz
          if-no-files-found: error
          retention-days: 7
```

  The Go cache restore the old `build` job had is dropped: it restored entries for the three cross-compiles. The `VERSION` comment moves along with the `make dist` line.
- **`test` job:** `needs: version` instead of `build`. Its steps stay as they are.
- **`image` job:** becomes a matrix on native runners, with no QEMU:

```yaml
  image:
    name: build docker image (${{ matrix.arch }})
    needs: version
    strategy:
      matrix:
        include:
          - { arch: amd64, runner: ubuntu-24.04 }
          - { arch: arm64, runner: ubuntu-24.04-arm }
    runs-on: ${{ matrix.runner }}
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v4
      - id: buildx
        uses: docker/setup-buildx-action@v3
      - id: go-build-cache
        uses: actions/cache@v4
        with:
          path: docker-go-build
          key: docker-go-build-${{ matrix.arch }}-${{ github.sha }}
          restore-keys: docker-go-build-${{ matrix.arch }}-
      - uses: reproducible-containers/buildkit-cache-dance@5422eac04292c961a382e0f584ea0f03ad9da723 # v3.4.0
        with:
          builder: ${{ steps.buildx.outputs.name }}
          cache-map: '{"docker-go-build": "/root/.cache/go-build"}'
          skip-extraction: ${{ steps.go-build-cache.outputs.cache-hit }}
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - id: push
        uses: docker/build-push-action@v6
        with:
          context: .
          target: runtime
          platforms: linux/${{ matrix.arch }}
          build-args: VERSION=${{ needs.version.outputs.tag }}
          outputs: type=image,name=ghcr.io/${{ github.repository_owner }}/twillingate,push-by-digest=true,name-canonical=true,push=true
          cache-from: type=gha,scope=twillingate-${{ matrix.arch }}
          cache-to: type=gha,mode=max,scope=twillingate-${{ matrix.arch }}
      # Matrix jobs cannot each set an output; the digests travel as files.
      - run: |
          mkdir -p "$RUNNER_TEMP/digests"
          d='${{ steps.push.outputs.digest }}'
          touch "$RUNNER_TEMP/digests/${d#sha256:}"
      - uses: actions/upload-artifact@v4
        with:
          name: digest-${{ matrix.arch }}
          path: ${{ runner.temp }}/digests/*
          if-no-files-found: error
          retention-days: 1
```

  Keep the existing comments that still apply: the go-build cache dance and the reason for pushing without tags. Delete the QEMU step and its rationale.
- **`release` job:** `needs: [version, build, test, image]`, `if: ${{ !inputs.dry_run }}`, `TAG: ${{ needs.version.outputs.tag }}`. Replace the `download-artifact` step and add the sums:

```yaml
      - uses: actions/download-artifact@v4
        with: { pattern: dist-*, path: dist, merge-multiple: true }
      - name: checksums
        run: cd dist && sha256sum twillingate-linux-*.tar.gz > SHA256SUMS
      - uses: actions/download-artifact@v4
        with: { pattern: digest-*, path: digests, merge-multiple: true }
```

  and replace the `tag the image` step's script with:

```yaml
        run: |
          docker buildx imagetools create -t "$IMAGE:$TAG" -t "$IMAGE:latest" \
            $(for d in digests/*; do printf '%s@sha256:%s ' "$IMAGE" "$(basename "$d")"; done)
          docker buildx imagetools inspect "$IMAGE:$TAG"
```

  and drop its `DIGEST` env.
- Update the four-stage comment at the top of `jobs:`. The jobs are now: `version`; then `build` per architecture, `test` and `image` per architecture side by side; then `release`, which publishes only once all of them pass and only when `dry_run` is off.

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml'))" && echo ok`
Expected: `ok`. Also `grep -n 'linux/arm/v7\|qemu\|build-all' .github/workflows/release.yml` prints nothing.

- [ ] **Step 2: Commit and push**

```bash
git add .github/workflows/release.yml
git commit -m "ci: build and check releases per architecture on native runners, with a dry run"
```

Push over SSH (the `gh` token lacks the `workflow` scope): `git push -u git@github.com:dmtrkzntsv/twillingate.git feat/engine-child`.

- [ ] **Step 3: Dry-run the release (ask the user first)**

The dry run pushes untagged image digests to ghcr.io, a side effect outside the repo, so confirm with the user before dispatching:

Run: `gh workflow run release.yml --ref feat/engine-child -f dry_run=true`, then `gh run watch`. Leave `version` blank: the `version` job computes the next patch and checks it is free, and a dry run creates no tag.
Expected: `version`, both `build` jobs, `test`, and both `image` jobs pass; `release` is skipped. Each build job's summary shows `engine ok: 42 in N ms` and the binary size: record both architectures' numbers for the PR. Spawn-to-ready target: under 100 ms.

- [ ] **Step 4: Open the PR**

Base `fix/late-window` (#177) while #176 and #177 are open; rebase onto `main` as they merge. Title (the squash subject, so the release notes carry the break): `feat!: run DuckDB in a child process of the binary; drop 32-bit ARM`. The body leads with the problem (raw-first needs DuckDB; DuckDB needs cgo; cgo changes the build). Then the decisions of this plan (above) for review, the measured spawn-to-ready per architecture and the binary size, a `BREAKING CHANGE` section (32-bit ARM, glibc 2.35, the Debian image), and the runbook pointer.
