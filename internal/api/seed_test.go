package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newTestHost seeds two projects in a fixed order — blog (id 1,
// identified) then docs (id 2, anonymous) — two days of web aggregates and
// one raw hit, and returns a connected in-memory MCP client session
// against the assembled tool host. A project a test creates on top is id 3.
func newTestHost(t *testing.T) (*host, *mcp.ClientSession) {
	t.Helper()
	path := t.TempDir() + "/mcp.db"
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ name, identity string }{{"blog", "identified"}, {"docs", "anonymous"}} {
		if _, err := st.CreateProject(ctx, store.RegistryProject{
			Name: p.name, Identity: p.identity, AllowedOrigins: "[]", Attributes: "[]"},
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
	      VALUES (1,'2026-08-20','iOS','17.4',5,12), (1,'2026-08-20','Windows','',7,13)`)
	seed(`INSERT INTO agg_views_browsers (project_id, day, browser, browser_version, visitors, views)
	      VALUES (1,'2026-08-20','Chrome','126',7,13)`)
	seed(`INSERT INTO agg_views_app_versions (project_id, day, os, app_version, visitors, views)
	      VALUES (1,'2026-08-20','iOS','2.4.1',5,12)`)
	seed(`INSERT INTO agg_views_devices (project_id, day, device, device_model, visitors, views)
	      VALUES (1,'2026-08-20','desktop','',7,13), (1,'2026-08-20','','iPhone15,3',5,12)`)
	seed(`INSERT INTO agg_views_countries (project_id, day, country, visitors, views)
	      VALUES (1,'2026-08-20','US',12,25)`)
	seed(`INSERT INTO agg_views_displays (project_id, day, display, visitors, views)
	      VALUES (1,'2026-08-20','1920x1080',6,11)`)
	seed(`INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind, user_id, path)
	      VALUES ('h1',1,'2026-08-26T10:00:00Z','2026-08-26T10:00:00Z','web','a1','user','u1','/live')`)
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
	seed(`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users)
	      VALUES (1,'2026-08-20','signup','plan','pro',3,3)`)

	db, err := OpenReadDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, logger)
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	h := &host{db: db, reg: reg, ops: manage.NewOps(reg, st),
		publicURL: "https://collector.test",
		timeout:   5 * time.Second, maxRows: 1000, logger: logger}

	srv := mcp.NewServer(&mcp.Implementation{Name: "analytics", Version: "test"}, nil)
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
func rawExec(st store.Store, q string, args ...any) (sql.Result, error) {
	return st.(interface {
		ExecForTest(string, ...any) (sql.Result, error)
	}).ExecForTest(q, args...)
}

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
