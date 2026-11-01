package manage

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestMonitorOriginsFilters(t *testing.T) {
	got := MonitorOrigins([]string{"*", "https://*.x.com", "tauri://localhost", "null",
		"http://a.com:8080", "https://b.com", "https://b.com/", "https://c.com/path", "", "https://[fe80::1%25eth0]"})
	want := []string{"http://a.com:8080", "https://b.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MonitorOrigins = %q, want %q", got, want)
	}
}

func boolp(b bool) *bool    { return &b }
func strp(s string) *string { return &s }

func TestSetMonitorPicksTheOnlyOrigin(t *testing.T) {
	ops, _, _ := newOps(t)
	ctx := context.Background()
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A",
		AllowedOrigins: []string{"*", "https://a.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := ops.SetMonitor(ctx, "cli", p.ID, MonitorSpec{Enabled: boolp(true), Contains: strp("  hello  ")})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Enabled || m.Origin != "https://a.example.com" || m.Contains != "hello" {
		t.Fatalf("monitor = %+v", m)
	}
	got, err := ops.Monitor(ctx, p.ID)
	if err != nil || got != m {
		t.Fatalf("Monitor = %+v, %v", got, err)
	}

	// two eligible origins and none chosen: refuse
	q, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "B",
		AllowedOrigins: []string{"https://b.example.com", "https://c.example.com"}})
	_, err = ops.SetMonitor(ctx, "cli", q.ID, MonitorSpec{Enabled: boolp(true)})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "pick one of the project's origins") {
		t.Fatalf("err = %v", err)
	}
	// disabled with no origin is fine
	if _, err := ops.SetMonitor(ctx, "cli", q.ID, MonitorSpec{Enabled: boolp(false)}); err != nil {
		t.Fatal(err)
	}
}

func TestSetMonitorRefusesWildcardAndForeignOrigin(t *testing.T) {
	ops, _, _ := newOps(t)
	ctx := context.Background()
	p, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A",
		AllowedOrigins: []string{"https://*.x.com", "https://a.example.com"}})
	for _, o := range []string{"https://*.x.com", "https://other.example.com", "*", "",
		"https://a.example.com/status", "https://a.example.com/?q=1", "https://a.example.com/#top"} {
		_, err := ops.SetMonitor(ctx, "cli", p.ID, MonitorSpec{Origin: strp(o)})
		if o == "" {
			if err != nil {
				t.Fatalf("empty origin clears: %v", err)
			}
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("origin %q: err = %v", o, err)
		}
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, MonitorSpec{Contains: strp(strings.Repeat("x", 201))}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long contains: %v", err)
	}
	if _, err := ops.SetMonitor(ctx, "cli", 999, MonitorSpec{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := ops.ArchiveProject(ctx, "cli", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, MonitorSpec{Origin: strp("https://a.example.com")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("archived: %v", err)
	}
}

func TestSetMonitorNamesTheNeedWithNoEligibleOrigin(t *testing.T) {
	ops, _, _ := newOps(t)
	ctx := context.Background()
	p, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A", AllowedOrigins: []string{"https://*.x.com"}})
	_, err := ops.SetMonitor(ctx, "cli", p.ID, MonitorSpec{Enabled: boolp(true)})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "needs one exact http(s) origin") {
		t.Fatalf("err = %v", err)
	}
}

func TestSnapshotMonitorRedirect(t *testing.T) {
	ops, _, _ := newOps(t)
	ctx := context.Background()
	p, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A",
		AllowedOrigins: []string{"https://Shop.example.com", "https://www.example.com:443", "https://*.x.com"}})
	q, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "B", AllowedOrigins: []string{"*"}})
	snap := ops.Reg.Snapshot(ctx)
	for _, c := range []struct {
		project     int64
		target, req string
		want        bool
	}{
		{p.ID, "https://shop.example.com/login", "https://shop.example.com", true}, // listed in another spelling
		{p.ID, "https://www.example.com/", "https://shop.example.com", true},       // the same, by the default port
		{p.ID, "https://a.x.com/", "https://shop.example.com", true},               // the wildcard still counts
		{p.ID, "https://other.example.com/", "https://shop.example.com", false},
		{p.ID, "http://shop.example.com/", "https://shop.example.com", false},
		{q.ID, "https://elsewhere.example/", "https://site.example", false}, // a bare * is no list
		{q.ID, "https://site.example/a", "https://site.example", true},
		{999, "https://shop.example.com/", "", false},
	} {
		if got := snap.MonitorRedirectAllowed(c.project, c.target, c.req); got != c.want {
			t.Errorf("project %d, %s: %v, want %v", c.project, c.target, got, c.want)
		}
	}
}

func TestSetMonitorNoChangeWritesNoAudit(t *testing.T) {
	ops, _, _, p, path := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p, AllowedOrigins: []string{"https://a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	first, err := ops.SetMonitor(ctx, "alice", p, MonitorSpec{Enabled: boolp(true)})
	if err != nil {
		t.Fatal(err)
	}
	again, err := ops.SetMonitor(ctx, "alice", p, MonitorSpec{Enabled: boolp(true), Origin: strp("https://a.example.com"), Contains: strp("")})
	if err != nil || again != first {
		t.Fatalf("no-op = %+v, %v, want %+v", again, err, first)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'monitor.update'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("monitor.update rows = %d, %v, want 1", n, err)
	}
}

func TestUpdateProjectDisablesMonitorOnOriginRemoval(t *testing.T) {
	ops, st, _ := newOps(t)
	ctx := context.Background()
	p, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A",
		AllowedOrigins: []string{"https://a.example.com", "https://b.example.com"}})
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, MonitorSpec{Enabled: boolp(true), Origin: strp("https://a.example.com")}); err != nil {
		t.Fatal(err)
	}
	// keeping the origin (trailing slash, case) leaves the monitor on
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p.ID,
		AllowedOrigins: []string{"https://A.example.com/"}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetMonitor(ctx, p.ID); !m.Enabled {
		t.Fatalf("monitor disabled though origin kept: %+v", m)
	}
	// a rename alone leaves it on
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p.ID, Name: "A2"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetMonitor(ctx, p.ID); !m.Enabled {
		t.Fatalf("monitor disabled by rename: %+v", m)
	}
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p.ID,
		AllowedOrigins: []string{"https://b.example.com"}}); err != nil {
		t.Fatal(err)
	}
	m, err := st.GetMonitor(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled || m.Origin != "https://a.example.com" {
		t.Fatalf("monitor = %+v, want disabled with origin kept", m)
	}
}

func TestMonitorAuditTrail(t *testing.T) {
	ops, _, _, p, path := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p, AllowedOrigins: []string{"https://a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.SetMonitor(ctx, "alice", p, MonitorSpec{Enabled: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.UpdateProject(ctx, "bob", ProjectSpec{ID: p, AllowedOrigins: []string{}}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT actor, action, subject, COALESCE(detail, '') FROM audit_log
		WHERE action LIKE 'monitor.%' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][4]string
	for rows.Next() {
		var r [4]string
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	id := strconv.FormatInt(p, 10)
	want := [][4]string{
		{"alice", "monitor.update", id, "enabled=true origin=https://a.example.com"},
		{"bob", "monitor.disable", id, "origin removed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("audit = %q, want %q", got, want)
	}
}
