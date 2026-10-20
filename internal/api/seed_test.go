package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain serves as the engine child when the test hosts' engines spawn
// it (archivetest.Child), and otherwise runs the package's tests, then
// removes the seeded template's temp dir (see hostTemplate) once every
// test has finished with it.
func TestMain(m *testing.M) {
	if archivetest.Child() {
		return
	}
	code := m.Run()
	if hostTemplateDir != "" {
		os.RemoveAll(hostTemplateDir)
	}
	os.Exit(code)
}

var (
	hostTemplateOnce sync.Once
	hostTemplateDir  string
	hostTemplatePath string
	// hostTemplateRaw is the template's event files, which each host
	// copies (newHostFiles).
	hostTemplateRaw string
)

// hostTemplate builds newTestHost's database and event files once per
// test binary and returns the database's path: migrated, seeded, and with
// the release's components and system dashboards installed, as
// app.Migrate leaves a real one, beside raw/ (hostTemplateRaw), the same
// history as events (hostEvents). Validating every system widget's SQL
// takes seconds under -race, which per test would put this package past
// go test's default timeout, so newTestHost copies these instead.
func hostTemplate(t *testing.T) string {
	t.Helper()
	hostTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "twillingate-api-template-")
		if err != nil {
			t.Fatal(err)
		}
		hostTemplateDir = dir
		path := filepath.Join(dir, "template.db")
		storetest.Copy(t, path)
		st, err := store.Open("sqlite://" + path)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for _, name := range []string{"blog", "docs"} {
			if _, err := st.CreateProject(ctx, store.RegistryProject{
				Name: name, AllowedOrigins: "[]"},
				store.AuditEntry{Actor: "test", Action: "project.create"}); err != nil {
				t.Fatal(err)
			}
		}
		seed := func(q string, args ...any) {
			t.Helper()
			if _, err := rawExec(st, q, args...); err != nil {
				t.Fatal(err)
			}
		}
		seed(`UPDATE projects SET allowed_origins = '["https://blog.example.com"]' WHERE id=1`)

		raw := filepath.Join(dir, "raw")
		if err := writeHostFiles(ctx, filepath.Join(dir, "engine"), raw); err != nil {
			t.Fatal(err)
		}
		files, closeFiles, err := templateFiles(ctx, filepath.Join(dir, "engine"), raw, st)
		if err != nil {
			t.Fatal(err)
		}
		err = reporting.Migrate(ctx, st, files)
		closeFiles()
		if err != nil {
			t.Fatal(err)
		}
		hostTemplateRaw = raw
		// Closing checkpoints the WAL into the main file, so copying that
		// one file carries everything.
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		hostTemplatePath = path
	})
	if hostTemplatePath == "" {
		t.Fatal("the test host's template database was not built")
	}
	return hostTemplatePath
}

// newTestHost seeds two projects in a fixed order — blog (id 1) then
// docs (id 2) — two days of web aggregates and one raw hit, and returns a
// connected in-memory MCP client session against the assembled tool host,
// on its own copy of hostTemplate (so with the system dashboards, ids 1
// up) and of its event files, where the widgets run. A project a test
// creates on top is id 3.
func newTestHost(t *testing.T) (*host, *mcp.ClientSession) {
	t.Helper()
	path := t.TempDir() + "/mcp.db"
	data, err := os.ReadFile(hostTemplate(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	db, err := readsql.Open(path, 5*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	subs, err := readsql.Open(path, 5*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { subs.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, logger)
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	files := newHostFiles(t, reg)
	h := &host{db: db, subs: subs, reg: reg, ops: manage.NewOps(reg, st), files: files.reader,
		eventsDir: files.dir, queryTimeout: 5 * time.Second, queryRows: 1000,
		rep:       reporting.New(st, files, reporting.Options{CacheAge: time.Minute, RefreshAge: time.Second, ShareBaseURL: "https://c.example"}),
		ingestURL: "https://collector.test", logger: logger, limits: limitsFrom(&config.Config{})}
	// host itself carries no path (production has no need for one once
	// opened); setGuards needs it to reopen with different guards, so the
	// test side remembers it here, keyed by the host it belongs to.
	testDBPaths[h] = path
	h.events = h.newHostEventCount()
	h.ops.Presence = hostPresence{files}

	srv := mcp.NewServer(&mcp.Implementation{Name: "analytics", Version: "test"},
		&mcp.ServerOptions{Instructions: serverInstructions})
	h.register(&registrar{mcp: srv, logger: logger})
	h.registerResources(srv)
	ct, stEnd := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, stEnd, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return h, cs
}

// seedDB copies a migrated database (see storetest) and returns its
// path, with one project (My blog) and no data: the minimal fixture for
// tests that only need a database file to open, not seeded aggregates.
func seedDB(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/read.db"
	storetest.Copy(t, path)
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject(context.Background(), store.RegistryProject{
		Name: "My blog", AllowedOrigins: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	return path
}

// testDBPaths remembers the file each test host's database was opened
// from, so setGuards can reopen it with different guards; see
// newTestHost. Production's host carries no such field.
var testDBPaths = map[*host]string{}

// setGuards sets h's timeout and row cap for one test: the event files'
// (queryTimeout, queryRows), and SQLite's, by reopening h's database with
// them (readsql.DB.Timeout/MaxRows) and closing the replaced handle on
// cleanup. h.register already bound its tool methods to h itself, so
// swapping them here is visible to the next tool call.
func setGuards(t *testing.T, h *host, timeout time.Duration, maxRows int) {
	t.Helper()
	path, ok := testDBPaths[h]
	if !ok {
		t.Fatal("setGuards: h was not built by newTestHost")
	}
	db, err := readsql.Open(path, timeout, maxRows)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h.db, h.queryTimeout, h.queryRows = db, timeout, maxRows
}

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

// rawExec reaches the underlying *sql.DB of the sqlite store for seeding.
// The store interface deliberately has no Exec; ExecForTest is a
// test-only accessor added to internal/store/sqlite/sqlite.go.
func rawExec(st any, q string, args ...any) (sql.Result, error) {
	return st.(execForTest).ExecForTest(q, args...)
}

// execForTest is the sqlite store's test-only Exec, which neither
// store.Store nor manage.Store declares, so callers hold those and rawExec
// asserts down. The assertion below keeps the method's signature checked at
// compile time.
type execForTest interface {
	ExecForTest(string, ...any) (sql.Result, error)
}

var _ execForTest = (*sqlite.DB)(nil)

// callTool invokes a tool over the session and fails the test on
// protocol errors; tool errors come back in the result.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func textOf(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}

// projectIDOf reads the project_id a create_project call returned, so a
// test can address the project it just made (the fixture's own are 1 and 2).
func projectIDOf(t *testing.T, res *mcp.CallToolResult) int64 {
	t.Helper()
	var out struct {
		ProjectID int64 `json:"project_id"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil || out.ProjectID == 0 {
		t.Fatalf("create_project returned no project_id: %v %s", err, textOf(res))
	}
	return out.ProjectID
}
