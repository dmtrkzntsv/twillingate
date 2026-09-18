package api

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var testRetention = config.Retention{
	Views:   config.RetentionClass{RawDays: 30, AggregateDays: 365},
	Product: config.RetentionClass{RawDays: 30, AggregateDays: 365},
}

// newTestHost seeds two projects (blog: identified, docs: anonymous),
// two days of web aggregates and one raw hit, and returns a connected
// in-memory MCP client session against the assembled tool host.
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
	for alias, identity := range map[string]string{"blog": "identified", "docs": "anonymous"} {
		if err := st.CreateProject(ctx, store.RegistryProject{
			Alias: alias, Name: alias, Identity: identity, AllowedOrigins: "[]"},
			store.AuditEntry{Actor: "test", Action: "project.create", Subject: alias}); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(q string, args ...any) {
		t.Helper()
		if _, err := rawExec(st, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO agg_views_daily (project, day, kind, visitors, views, sessions, bounces, duration_sec)
	      VALUES ('blog','2026-08-20','web',10,25,12,3,600), ('blog','2026-08-21','web',12,30,14,4,720),
	             ('blog','2026-08-20','app',6,20,8,0,480)`)
	seed(`INSERT INTO agg_views_paths (project, day, path, visitors, views)
	      VALUES ('blog','2026-08-20','/post-1',8,15), ('blog','2026-08-20','/post-2',4,10), ('blog','2026-08-20','/settings',5,12)`)
	seed(`INSERT INTO agg_views_hosts (project, day, host, visitors, views)
	      VALUES ('blog','2026-08-20','blog.example.com',9,20), ('blog','2026-08-20','shop.example.com',3,5)`)
	seed(`INSERT INTO agg_views_utm (project, day, utm_source, utm_medium, utm_campaign, visitors, views)
	      VALUES ('blog','2026-08-20','newsletter','email','august',6,9)`)
	seed(`INSERT INTO agg_views_os (project, day, os, os_version, visitors, views)
	      VALUES ('blog','2026-08-20','iOS','17.4',5,12), ('blog','2026-08-20','Windows','',7,13)`)
	seed(`INSERT INTO agg_views_browsers (project, day, browser, browser_version, visitors, views)
	      VALUES ('blog','2026-08-20','Chrome','126',7,13)`)
	seed(`INSERT INTO agg_views_app_versions (project, day, os, app_version, visitors, views)
	      VALUES ('blog','2026-08-20','iOS','2.4.1',5,12)`)
	seed(`INSERT INTO agg_views_devices (project, day, device, device_model, visitors, views)
	      VALUES ('blog','2026-08-20','desktop','',7,13), ('blog','2026-08-20','','iPhone15,3',5,12)`)
	seed(`INSERT INTO agg_views_countries (project, day, country, visitors, views)
	      VALUES ('blog','2026-08-20','US',12,25)`)
	seed(`INSERT INTO agg_views_displays (project, day, display, visitors, views)
	      VALUES ('blog','2026-08-20','1920x1080',6,11)`)
	seed(`INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind, user_id, path)
	      VALUES ('h1','blog','2026-08-26T10:00:00Z','2026-08-26T10:00:00Z','web','a1','user','u1','/live')`)
	seed(`INSERT INTO agg_product_daily (project, day, event_name, count, unique_users)
	      VALUES ('blog','2026-08-20','signup',5,4)`)
	seed(`INSERT INTO agg_product_totals (project, day, total_events, active_users)
	      VALUES ('blog','2026-08-20',5,4)`)
	seed(`INSERT INTO agg_retention (project, actor_kind, cohort_day, day_offset, actors)
	      VALUES ('blog','user','2026-08-01',0,10), ('blog','user','2026-08-01',7,4)`)
	seed(`INSERT INTO agg_identity_daily (project, day, kind, id, actors, users, views, events)
	      VALUES ('blog','2026-08-20','user','u1',1,1,5,2)`)
	seed(`INSERT INTO identities (project, kind, id, name) VALUES ('blog','user','u1','Jane Doe')`)
	seed(`INSERT INTO agg_identity_daily (project, day, kind, id, actors, users, views, events)
	      VALUES ('blog','2026-08-20','group','g1',1,0,5,2)`)
	seed(`INSERT INTO identities (project, kind, id, name) VALUES ('blog','group','g1','Acme Inc')`)
	// attributes declared for blog only: docs stays at the default (no
	// attributes declared), which TestProductAttributesReturnsEmptyForUndeclared
	// depends on. Rollups are unconditional now (no enabled flag).
	seed(`UPDATE projects SET attributes = '["plan"]' WHERE alias='blog'`)
	seed(`UPDATE projects SET allowed_origins = '["https://blog.example.com"]' WHERE alias='blog'`)
	seed(`INSERT INTO agg_product_attrs (project, day, event_name, attr_key, attr_value, count, unique_users)
	      VALUES ('blog','2026-08-20','signup','plan','pro',3,3)`)

	db, err := OpenReadDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, testRetention, logger)
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
