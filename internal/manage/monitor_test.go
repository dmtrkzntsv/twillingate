package manage

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
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

const (
	monA = "https://a.example.com"
	monB = "https://b.example.com"
	monC = "https://c.other.org"
)

func TestListMonitorsReadsEveryEligibleOrigin(t *testing.T) {
	ops, st, _ := newOps(t)
	ctx := context.Background()
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A",
		AllowedOrigins: []string{monB, "*", "https://*.x.com", monA, monC, "tauri://localhost"}})
	if err != nil {
		t.Fatal(err)
	}
	l, err := ops.ListMonitors(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Monitor{{ProjectID: p.ID, Origin: monB}, {ProjectID: p.ID, Origin: monA}, {ProjectID: p.ID, Origin: monC}}
	if !reflect.DeepEqual(l.Monitors, want) || l.Domains != nil || l.Enabled() != 0 {
		t.Fatalf("nothing on: %+v", l)
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, monA, MonitorSpec{Enabled: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, monB, MonitorSpec{Enabled: boolp(true), Contains: strp("  hi  ")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, monC, MonitorSpec{Contains: strp("off")}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMonitorDomain(ctx, p.ID, "example.com", store.DomainState{ExpiresAt: "2027-08-13", CheckedAt: "2026-11-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMonitorDomain(ctx, p.ID, "other.org", store.DomainState{ExpiresAt: "2028-01-01", CheckedAt: "2026-11-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	l, err = ops.ListMonitors(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Monitors) != 3 || l.Monitors[0].Origin != monB || l.Monitors[0].Contains != "hi" || !l.Monitors[0].Enabled ||
		l.Monitors[2].Enabled || l.Monitors[2].Contains != "off" || l.Enabled() != 2 {
		t.Errorf("monitors = %+v", l.Monitors)
	}
	// The main domains of the enabled monitors only, each with its origins.
	wantD := []MonitorDomain{{Domain: "example.com", Origins: []string{monB, monA}, ExpiresAt: "2027-08-13", CheckedAt: "2026-11-01T00:00:00Z"}}
	if !reflect.DeepEqual(l.Domains, wantD) {
		t.Errorf("domains = %+v, want %+v", l.Domains, wantD)
	}
	// A domain with no reading yet is listed, unknown.
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, "https://c.other.org/", MonitorSpec{Enabled: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p.ID, AllowedOrigins: []string{monA, monB, monC, "https://z.example.net"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, "https://z.example.net", MonitorSpec{Enabled: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	l, _ = ops.ListMonitors(ctx, p.ID)
	if len(l.Domains) != 3 || l.Domains[1].Domain != "example.net" || l.Domains[1].ExpiresAt != "" || l.Domains[2].ExpiresAt != "2028-01-01" {
		t.Errorf("domains = %+v", l.Domains)
	}
	if _, err := ops.ListMonitors(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func TestSetMonitorRefusesForeignOrigins(t *testing.T) {
	ops, _, _ := newOps(t)
	ctx := context.Background()
	p, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A",
		AllowedOrigins: []string{"https://*.x.com", monA}})
	for _, o := range []string{"https://*.x.com", "https://other.example.com", "*", "",
		monA + "/status", monA + "/?q=1", monA + "/#top"} {
		if _, err := ops.SetMonitor(ctx, "cli", p.ID, o, MonitorSpec{Enabled: boolp(true)}); !errors.Is(err, ErrInvalid) {
			t.Errorf("origin %q: err = %v", o, err)
		}
	}
	if l, _ := ops.ListMonitors(ctx, p.ID); l.Enabled() != 0 {
		t.Errorf("a refused origin was set: %+v", l)
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, monA, MonitorSpec{Contains: strp(strings.Repeat("x", 201))}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long contains: %v", err)
	}
	if _, err := ops.SetMonitor(ctx, "cli", 999, monA, MonitorSpec{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := ops.ArchiveProject(ctx, "cli", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, monA, MonitorSpec{Enabled: boolp(true)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("archived: %v", err)
	}
}

// An origin is taken in any spelling of itself, and stored normalized.
func TestSetMonitorNormalizesTheOrigin(t *testing.T) {
	ops, _, _ := newOps(t)
	ctx := context.Background()
	p, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A", AllowedOrigins: []string{"https://A.example.com:443/"}})
	l, err := ops.SetMonitor(ctx, "cli", p.ID, " https://a.EXAMPLE.com/ ", MonitorSpec{Enabled: boolp(true)})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Monitors) != 1 || !l.Monitors[0].Enabled || l.Monitors[0].Origin != monA {
		t.Fatalf("list = %+v", l)
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

// A redirect between the Unicode and the A-label spelling of a host is the
// same site, whichever one the project lists.
func TestSnapshotMonitorRedirectIDN(t *testing.T) {
	ops, _, _ := newOps(t)
	ctx := context.Background()
	u, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "U", AllowedOrigins: []string{"https://bücher.com"}})
	a, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A", AllowedOrigins: []string{"https://xn--bcher-kva.com"}})
	snap := ops.Reg.Snapshot(ctx)
	for _, c := range []struct {
		project int64
		target  string
		want    bool
	}{
		{u.ID, "https://xn--bcher-kva.com/login", true},
		{u.ID, "https://bücher.com/login", true},
		{u.ID, "https://xn--other-kva.com/", false},
		{a.ID, "https://bücher.com/login", true},
		{a.ID, "https://BÜCHER.com/login", true},
		{a.ID, "http://bücher.com/login", false},
	} {
		if got := snap.MonitorRedirectAllowed(c.project, c.target, "https://bücher.com"); got != c.want {
			t.Errorf("project %d, %s: %v, want %v", c.project, c.target, got, c.want)
		}
	}
}

func TestSetMonitorNoChangeWritesNoAudit(t *testing.T) {
	ops, _, _, p, path := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p, AllowedOrigins: []string{monA, monB}}); err != nil {
		t.Fatal(err)
	}
	// An origin with no row, staying off: no row is made.
	if _, err := ops.SetMonitor(ctx, "alice", p, monB, MonitorSpec{Enabled: boolp(false), Contains: strp("")}); err != nil {
		t.Fatal(err)
	}
	first, err := ops.SetMonitor(ctx, "alice", p, monA, MonitorSpec{Enabled: boolp(true)})
	if err != nil {
		t.Fatal(err)
	}
	again, err := ops.SetMonitor(ctx, "alice", p, monA, MonitorSpec{Enabled: boolp(true), Contains: strp("")})
	if err != nil || !reflect.DeepEqual(again, first) {
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
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM monitors WHERE origin = ?`, monB).Scan(&n); err != nil || n != 0 {
		t.Errorf("monitors rows for the untouched origin = %d, %v, want 0", n, err)
	}
}

func TestUpdateProjectRemovesMonitorOnOriginRemoval(t *testing.T) {
	ops, st, _ := newOps(t)
	ctx := context.Background()
	p, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A", AllowedOrigins: []string{monA, monB}})
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, monA, MonitorSpec{Enabled: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	rows := func() []store.Monitor {
		t.Helper()
		ms, err := st.ListMonitors(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ms
	}
	// keeping the origin (trailing slash, case) leaves the monitor on
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p.ID, AllowedOrigins: []string{"https://A.example.com/"}}); err != nil {
		t.Fatal(err)
	}
	if ms := rows(); len(ms) != 1 || !ms[0].Enabled {
		t.Fatalf("monitor changed though origin kept: %+v", ms)
	}
	// a rename alone leaves it on
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p.ID, Name: "A2"}); err != nil {
		t.Fatal(err)
	}
	if ms := rows(); len(ms) != 1 || !ms[0].Enabled {
		t.Fatalf("monitor changed by rename: %+v", ms)
	}
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p.ID, AllowedOrigins: []string{monB}}); err != nil {
		t.Fatal(err)
	}
	if ms := rows(); len(ms) != 0 {
		t.Fatalf("monitor = %+v, want the row gone", ms)
	}
	// Adding the origin back starts it off.
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p.ID, AllowedOrigins: []string{monA}}); err != nil {
		t.Fatal(err)
	}
	if l, _ := ops.ListMonitors(ctx, p.ID); len(l.Monitors) != 1 || l.Monitors[0].Enabled {
		t.Fatalf("list = %+v, want it off", l)
	}
}

// An archived project is not checked, so its monitors claim no frequency.
func TestCheckEveryOfAnArchivedProject(t *testing.T) {
	ops, _, _, p, _ := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p, AllowedOrigins: []string{"https://a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	l, err := ops.SetMonitor(ctx, "alice", p, monA, MonitorSpec{Enabled: boolp(true)})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.CheckEvery(time.Minute); got != time.Minute {
		t.Errorf("live project: every %v, want 1m", got)
	}
	if err := ops.ArchiveProject(ctx, "alice", p); err != nil {
		t.Fatal(err)
	}
	if l, err = ops.ListMonitors(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := l.CheckEvery(time.Minute); got != 0 {
		t.Errorf("archived project: every %v, want 0", got)
	}
}

func TestMonitorAuditTrail(t *testing.T) {
	ops, _, _, p, path := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p, AllowedOrigins: []string{"https://a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.SetMonitor(ctx, "alice", p, monA, MonitorSpec{Enabled: boolp(true)}); err != nil {
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
		{"alice", "monitor.update", id, "origin=https://a.example.com enabled=true"},
		{"bob", "monitor.remove", id, "origin removed: https://a.example.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("audit = %q, want %q", got, want)
	}
}

func TestCheckMonitor(t *testing.T) {
	ops, st, _, id, path := ruleOps(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	ops.now = func() time.Time { return now }
	p, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: id, AllowedOrigins: []string{monA, monB}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CheckMonitor(ctx, "cli", p.ID, monA); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "turn the monitor on first") {
		t.Fatalf("never set up: %v", err)
	}
	if _, err := ops.CheckMonitor(ctx, "cli", 999, monA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	if _, err := ops.CheckMonitor(ctx, "cli", p.ID, "https://elsewhere.example"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "allowed_origins") {
		t.Fatalf("foreign origin: %v", err)
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, monA, MonitorSpec{Enabled: boolp(false), Contains: strp("x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CheckMonitor(ctx, "cli", p.ID, monA); !errors.Is(err, ErrInvalid) {
		t.Fatalf("disabled: %v", err)
	}
	for _, o := range []string{monA, monB} {
		if _, err := ops.SetMonitor(ctx, "cli", p.ID, o, MonitorSpec{Enabled: boolp(true)}); err != nil {
			t.Fatal(err)
		}
	}
	l, err := ops.CheckMonitor(ctx, "alice", p.ID, monA)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := l.Monitor(monA); m.CheckRequestedAt != "2026-11-01T10:00:00Z" {
		t.Fatalf("requested at %q", m.CheckRequestedAt)
	}
	if m, _ := l.Monitor(monB); m.CheckRequestedAt != "" {
		t.Fatalf("the other monitor was asked too: %+v", m)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var actor, subject, detail string
	if err := db.QueryRowContext(ctx, `SELECT actor, subject, detail FROM audit_log WHERE action='monitor.check'`).Scan(&actor, &subject, &detail); err != nil {
		t.Fatal(err)
	}
	if actor != "alice" || subject != strconv.FormatInt(id, 10) || detail != monA {
		t.Fatalf("audit = %s %s %s", actor, subject, detail)
	}

	// Pending, even long after the cooldown; the other monitor is free.
	now = now.Add(5 * time.Minute)
	if _, err := ops.CheckMonitor(ctx, "alice", p.ID, monA); !errors.Is(err, ErrBusy) {
		t.Fatalf("pending: %v, want ErrBusy", err)
	}
	if _, err := ops.CheckMonitor(ctx, "alice", p.ID, monB); err != nil {
		t.Fatalf("the other monitor: %v", err)
	}
	// Finished, but the last request was under 60 s ago: the cooldown runs
	// from the request, for everyone, per monitor.
	now = time.Date(2026, 11, 1, 10, 0, 30, 0, time.UTC)
	if err := st.FinishMonitorCheck(ctx, p.ID, monA, "2026-11-01T10:00:00Z", "2026-11-01T10:00:02Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CheckMonitor(ctx, "bob", p.ID, monA); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "30 s") {
		t.Fatalf("cooldown: %v, want ErrBusy naming the 30 s left", err)
	}
	now = time.Date(2026, 11, 1, 10, 1, 0, 0, time.UTC)
	if _, err := ops.CheckMonitor(ctx, "bob", p.ID, monA); err != nil {
		t.Fatalf("past the cooldown: %v", err)
	}

	if err := ops.ArchiveProject(ctx, "cli", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CheckMonitor(ctx, "bob", p.ID, monA); !errors.Is(err, ErrInvalid) {
		t.Fatalf("archived: %v, want ErrInvalid", err)
	}
}

// Each monitor has its own cooldown: checking one leaves its neighbour free.
func TestCheckMonitorCooldownIsPerMonitor(t *testing.T) {
	ops, st, _ := newOps(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	ops.now = func() time.Time { return now }
	p, _ := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A", AllowedOrigins: []string{monA, monB}})
	for _, o := range []string{monA, monB} {
		if _, err := ops.SetMonitor(ctx, "cli", p.ID, o, MonitorSpec{Enabled: boolp(true)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ops.CheckMonitor(ctx, "cli", p.ID, monA); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishMonitorCheck(ctx, p.ID, monA, "2026-11-01T10:00:00Z", "2026-11-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CheckMonitor(ctx, "cli", p.ID, monA); !errors.Is(err, ErrBusy) {
		t.Fatalf("the same monitor: %v, want ErrBusy", err)
	}
	if _, err := ops.CheckMonitor(ctx, "cli", p.ID, monB); err != nil {
		t.Fatalf("its neighbour: %v", err)
	}
}

// A paused install refuses a check on demand, before anything is asked.
func TestCheckMonitorRefusedWhilePaused(t *testing.T) {
	ops, st, _, id, _ := ruleOps(t)
	ctx := context.Background()
	p, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: id, AllowedOrigins: []string{monA}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, monA, MonitorSpec{Enabled: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	ops.MonitorPaused = true
	if _, err := ops.CheckMonitor(ctx, "cli", p.ID, monA); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "monitoring is paused on this install") {
		t.Fatalf("paused: %v, want ErrInvalid with the reason", err)
	}
	ms, _ := st.ListMonitors(ctx, p.ID)
	if len(ms) != 1 || ms[0].CheckRequestedAt != "" {
		t.Errorf("a request was stored: %+v", ms)
	}
	// Editing still works.
	if _, err := ops.SetMonitor(ctx, "cli", p.ID, monA, MonitorSpec{Contains: strp("x")}); err != nil {
		t.Errorf("edit while paused: %v", err)
	}
}

// Two edits of different fields, at once, both land: the merge is the store's,
// in its transaction, not a read here followed by a write of both fields.
func TestSetMonitorConcurrentEditsOfDifferentFieldsBothLand(t *testing.T) {
	ops, _, _ := newOps(t)
	ctx := context.Background()
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "A", AllowedOrigins: []string{monA}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 25 {
		if _, err := ops.SetMonitor(ctx, "cli", p.ID, monA, MonitorSpec{Enabled: boolp(true), Contains: strp("")}); err != nil {
			t.Fatal(err)
		}
		errs := make(chan error, 2)
		go func() { _, err := ops.SetMonitor(ctx, "a", p.ID, monA, MonitorSpec{Contains: strp("c")}); errs <- err }()
		go func() {
			_, err := ops.SetMonitor(ctx, "b", p.ID, monA, MonitorSpec{Enabled: boolp(false)})
			errs <- err
		}()
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
		}
		l, err := ops.ListMonitors(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if m, _ := l.Monitor(monA); m.Enabled || m.Contains != "c" {
			t.Fatalf("round %d: %+v, want off with contains c", i, m)
		}
	}
}
