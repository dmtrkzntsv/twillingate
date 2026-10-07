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

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain runs the package's tests, then removes the seeded template's
// temp dir (see hostTemplate) once every test has finished with it.
func TestMain(m *testing.M) {
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
)

// hostTemplate builds newTestHost's database once per test binary and
// returns its path: migrated, seeded, and with the release's components
// and system dashboards installed, as app.Migrate leaves a real one.
// Validating every system widget's SQL takes seconds under -race, which
// per test would put this package past go test's default timeout, so
// newTestHost copies this file instead.
func hostTemplate(t *testing.T) string {
	t.Helper()
	hostTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "twillingate-api-template-")
		if err != nil {
			t.Fatal(err)
		}
		hostTemplateDir = dir
		path := filepath.Join(dir, "template.db")
		st, err := store.Open("sqlite://" + path)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"blog", "docs"} {
			if _, err := st.CreateProject(ctx, store.RegistryProject{
				Name: name, AllowedOrigins: "[]", Attributes: "[]"},
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
		seed(`INSERT INTO agg_views_daily (project_id, day, kind, visitors, views, sessions, bounces, duration_sec)
		      VALUES (1,'2026-08-20','web',10,25,12,3,600), (1,'2026-08-21','web',12,30,14,4,720),
		             (1,'2026-08-20','app',6,20,8,0,480)`)
		seed(`INSERT INTO agg_views_paths (project_id, day, path, visitors, views)
		      VALUES (1,'2026-08-20','/post-1',8,15), (1,'2026-08-20','/post-2',4,10), (1,'2026-08-20','/settings',5,12)`)
		seed(`INSERT INTO agg_views_hosts (project_id, day, host, visitors, views)
		      VALUES (1,'2026-08-20','blog.example.com',9,20), (1,'2026-08-20','shop.example.com',3,5)`)
		seed(`INSERT INTO agg_views_utm (project_id, day, utm_source, utm_medium, utm_campaign, visitors, views)
		      VALUES (1,'2026-08-20','newsletter','email','august',6,9)`)
		seed(`INSERT INTO agg_views_os (project_id, day, os, os_version, visitors, views)
		      VALUES (1,'2026-08-20','ios','17.4',5,12), (1,'2026-08-20','windows','',7,13)`)
		seed(`INSERT INTO agg_views_browsers (project_id, day, browser, browser_version, visitors, views)
		      VALUES (1,'2026-08-20','chrome','126',7,13)`)
		seed(`INSERT INTO agg_views_app_versions (project_id, day, platform, app_version, visitors, views)
		      VALUES (1,'2026-08-20','ios','2.4.1',5,12)`)
		seed(`INSERT INTO agg_views_platforms (project_id, day, platform, visitors, views)
		      VALUES (1,'2026-08-20','web',10,25), (1,'2026-08-20','ios',6,20)`)
		seed(`INSERT INTO agg_views_devices (project_id, day, device, device_model, visitors, views)
		      VALUES (1,'2026-08-20','desktop','',7,13), (1,'2026-08-20','unknown','iPhone15,3',5,12)`)
		seed(`INSERT INTO agg_views_countries (project_id, day, country, visitors, views)
		      VALUES (1,'2026-08-20','US',12,25)`)
		seed(`INSERT INTO agg_views_displays (project_id, day, display, visitors, views)
		      VALUES (1,'2026-08-20','1920x1080',6,11)`)
		seed(`INSERT INTO agg_views_locales (project_id, day, browser_locale, app_locale, visitors, views)
		      VALUES (1,'2026-08-20','de-DE','en',2,4)`)
		seed(`INSERT INTO agg_views_consent (project_id, day, consent, visitors, views)
		      VALUES (1,'2026-08-20','given',3,6)`)
		seed(`INSERT INTO events (id, project_id, ts, day, received_at, kind, actor_id, actor_kind, user_id, path, consent, family, event_name)
		      VALUES ('h1',1,'2026-08-26T10:00:00Z','2026-08-26','2026-08-26T10:00:00Z','web','a1','user','u1','/live',1,'views','$page_view')`)
		seed(`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users)
		      VALUES (1,'2026-08-20','signup',5,4)`)
		seed(`INSERT INTO agg_product_totals (project_id, day, total_events, active_users)
		      VALUES (1,'2026-08-20',5,4)`)
		seed(`INSERT INTO agg_retention (project_id, actor_kind, cohort_day, day_offset, actors)
		      VALUES (1,'user','2026-08-01',0,10), (1,'user','2026-08-01',7,4)`)
		seed(`INSERT INTO agg_identity_daily (project_id, day, kind, id, actors, users, views, events)
		      VALUES (1,'2026-08-20','user','u1',1,1,5,2)`)
		seed(`INSERT INTO identities (project_id, kind, id, name) VALUES (1,'user','u1','Jane Doe')`)
		seed(`INSERT INTO agg_identity_daily (project_id, day, kind, id, actors, users, views, events)
		      VALUES (1,'2026-08-20','group','g1',1,0,5,2)`)
		seed(`INSERT INTO identities (project_id, kind, id, name) VALUES (1,'group','g1','Acme Inc')`)
		// attributes declared for blog only: docs stays at the default (no
		// attributes declared), which TestProductAttributesReturnsEmptyForUndeclared
		// depends on. Rollups are unconditional now (no enabled flag).
		seed(`UPDATE projects SET attributes = '["plan"]' WHERE id=1`)
		seed(`UPDATE projects SET allowed_origins = '["https://blog.example.com"]' WHERE id=1`)
		// One row rolled up before migration 016 (unique_groups NULL, "not
		// measured") and one after, so the tool is seen to carry both.
		seed(`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		      VALUES (1,'2026-08-20','signup','plan','pro',3,3,NULL),
		             (1,'2026-08-21','signup','plan','team',2,2,2)`)
		// Measures: an aggregated day (2026-08-20, checkout_api/time and an
		// all-zero $cls/number) plus a live day (2026-08-21, written through
		// WriteEvents like real ingest) that TestMeasures* combines with it.
		// The $browser attrs rows plus the live rows' own browser column
		// exercise attr_key breakdown across both halves.
		seed(`INSERT INTO agg_measures_daily (project_id, day, event_name, measure, bucket, samples, weight, sum)
		      VALUES (1,'2026-08-20','checkout_api','time',135,2,2,400),
		             (1,'2026-08-20','$cls','number',-1000,3,3,0)`)
		seed(`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		      VALUES (1,'2026-08-20','checkout_api','time','$browser','chrome',135,1,1,200),
		             (1,'2026-08-20','checkout_api','time','$browser','firefox',135,1,1,200)`)
		checkoutValue, clsValue := 300.0, 0.0
		if err := st.WriteEvents(ctx, []store.Event{
			{ID: "0190dddd-0000-7000-8000-000000000001", ProjectID: 1, Family: store.FamilyMeasures,
				EventName: "checkout_api", TS: time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
				ActorID: "a1", ActorKind: "user", Browser: "chrome",
				Value: &checkoutValue, Measure: store.MeasureTime, SampleRate: 1},
			{ID: "0190dddd-0000-7000-8000-000000000002", ProjectID: 1, Family: store.FamilyMeasures,
				EventName: "$cls", TS: time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
				ActorID: "a1", ActorKind: "user", Browser: "chrome",
				Value: &clsValue, Measure: store.MeasureNumber, SampleRate: 1},
		}); err != nil {
			t.Fatal(err)
		}

		db, err := readsql.Open(path, 5*time.Second, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if err := reporting.Migrate(ctx, st, db); err != nil {
			t.Fatal(err)
		}
		// Closing checkpoints the WAL into the main file, so copying that
		// one file carries everything.
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
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
// up). A project a test creates on top is id 3.
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
	db, err := readsql.Open(path, 5*time.Second, 1000, customSQLRefused...)
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
	h := &host{db: db, subs: subs, reg: reg, ops: manage.NewOps(reg, st),
		rep:       reporting.New(st, db, reporting.Options{CacheAge: time.Minute, RefreshAge: time.Second, ShareBaseURL: "https://c.example"}),
		publicURL: "https://collector.test", logger: logger,
		limits: limitsFrom(&config.Config{AttributeValuesTopN: config.DefaultAttributeValuesTopN, IdentitiesTopN: config.DefaultIdentitiesTopN})}
	// host itself carries no path (production has no need for one once
	// opened); setGuards needs it to reopen with different guards, so the
	// test side remembers it here, keyed by the host it belongs to.
	testDBPaths[h] = path

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

// seedDB migrates a fresh database and returns its path, with one project
// (My blog) and no data: the minimal fixture for tests that only need a
// database file to open, not seeded aggregates.
func seedDB(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/read.db"
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject(context.Background(), store.RegistryProject{
		Name: "My blog", AllowedOrigins: "[]", Attributes: "[]"},
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

// setGuards reopens h's database with a different timeout and/or row cap
// for one test — host carries neither directly, only the readsql.DB does
// (readsql.DB.Timeout/MaxRows) — and closes the replaced handle on
// cleanup. h.register already bound its tool methods to h itself, so
// swapping h.db here is visible to the next tool call.
func setGuards(t *testing.T, h *host, timeout time.Duration, maxRows int) {
	t.Helper()
	path, ok := testDBPaths[h]
	if !ok {
		t.Fatal("setGuards: h was not built by newTestHost")
	}
	db, err := readsql.Open(path, timeout, maxRows, customSQLRefused...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h.db = db
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
