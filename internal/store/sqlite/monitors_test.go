package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func monitorProject(t *testing.T, d *DB, name string) int64 {
	t.Helper()
	id, err := d.CreateProject(context.Background(), store.RegistryProject{Name: name, AllowedOrigins: `["https://a.example.com","https://b.example.com","https://c.other.org"]`},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// monEligible is every listed origin: the tests list only exact origins.
func monEligible(allowed []string) []string { return allowed }

var monAudit = store.AuditEntry{Actor: "test", Action: "monitor.set", Subject: "monitor"}

const (
	monA  = "https://a.example.com"
	monB  = "https://b.example.com"
	monC  = "https://c.other.org"
	monAt = "2026-11-01T00:00:00Z"
)

func setMon(t *testing.T, d *DB, id int64, origin string, s store.MonitorSettings) store.Monitor {
	t.Helper()
	m, err := d.SetMonitor(context.Background(), id, origin, s, monEligible, monAudit)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// getMon is the project's row for origin; the zero Monitor when it has none.
func getMon(t *testing.T, d *DB, id int64, origin string) store.Monitor {
	t.Helper()
	ms, err := d.ListMonitors(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Origin == origin {
			return m
		}
	}
	return store.Monitor{}
}

func auditCount(t *testing.T, d *DB, action string) (n int) {
	t.Helper()
	if err := d.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE action=?`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMonitorRoundTrip(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")

	if ms, err := d.ListMonitors(ctx, id); err != nil || len(ms) != 0 {
		t.Fatalf("no rows: %+v, %v", ms, err)
	}
	got := setMon(t, d, id, monA, store.MonitorSettings{Enabled: true, Contains: "ok"})
	want := store.Monitor{ProjectID: id, Origin: monA, Enabled: true, Contains: "ok"}
	if got != want {
		t.Fatalf("SetMonitor = %+v, want %+v", got, want)
	}
	if m := getMon(t, d, id, monA); m != want {
		t.Fatalf("ListMonitors = %+v, want %+v", m, want)
	}
	if n := auditCount(t, d, "monitor.set"); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
}

// The key is the project and the origin: a project holds many, by origin, and
// two projects may hold the same one.
func TestMonitorsAreKeyedByProjectAndOrigin(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	a, b := monitorProject(t, d, "a"), monitorProject(t, d, "b")
	setMon(t, d, a, monB, store.MonitorSettings{Enabled: true})
	setMon(t, d, a, monA, store.MonitorSettings{Enabled: false, Contains: "x"})
	setMon(t, d, b, monA, store.MonitorSettings{Enabled: true})
	// Writing one again updates it.
	setMon(t, d, a, monB, store.MonitorSettings{Enabled: true, Contains: "y"})

	ms, err := d.ListMonitors(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Origin != monA || ms[1].Origin != monB {
		t.Fatalf("ListMonitors = %+v, want a project's two, by origin", ms)
	}
	if ms[0].Enabled || ms[0].Contains != "x" || !ms[1].Enabled || ms[1].Contains != "y" {
		t.Errorf("rows mixed up: %+v", ms)
	}
	if ms, _ := d.ListMonitors(ctx, b); len(ms) != 1 || ms[0].Contains != "" {
		t.Errorf("the other project's: %+v", ms)
	}
}

func TestSetMonitorEnablingClearsCertCheckTimeOnly(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	if err := d.SetMonitorCert(ctx, id, monA, store.CertState{ExpiresAt: "2027-01-01T00:00:00Z", Issuer: "R3", Valid: true, CheckedAt: monAt}); err != nil {
		t.Fatal(err)
	}
	// Staying on, or going off, keeps the check time.
	for _, on := range []bool{true, false, false} {
		if m := setMon(t, d, id, monA, store.MonitorSettings{Enabled: on}); m.CertCheckedAt != monAt {
			t.Fatalf("enabled=%v: %+v, want the check time kept", on, m)
		}
	}
	m := setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	if m.CertCheckedAt != "" {
		t.Errorf("enabling kept the check time: %+v", m)
	}
	if m.CertExpiresAt == "" || m.CertIssuer != "R3" || !m.CertValid {
		t.Errorf("enabling lost the reading: %+v", m)
	}
}

// A domain's reading is shared: enabling a monitor does not clear it.
func TestSetMonitorEnablingKeepsTheSharedDomainReading(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	if err := d.SetMonitorDomain(ctx, id, "example.com", store.DomainState{ExpiresAt: "2027-02-02", CheckedAt: monAt}); err != nil {
		t.Fatal(err)
	}
	setMon(t, d, id, monB, store.MonitorSettings{Enabled: true})
	ds, err := d.ListMonitorDomains(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.MonitorDomain{ProjectID: id, Domain: "example.com", ExpiresAt: "2027-02-02", CheckedAt: monAt}); len(ds) != 1 || ds[0] != want {
		t.Fatalf("domains = %+v, want %+v", ds, want)
	}
}

func TestSetMonitorDomain(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id, other := monitorProject(t, d, "a"), monitorProject(t, d, "b")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	setMon(t, d, id, monC, store.MonitorSettings{})
	setMon(t, d, other, monA, store.MonitorSettings{Enabled: true})
	for _, w := range []struct {
		project int64
		domain  string
		state   store.DomainState
	}{
		{id, "example.com", store.DomainState{ExpiresAt: "2027-02-02", CheckedAt: monAt}},
		{id, "other.org", store.DomainState{CheckedAt: monAt}},
		{other, "example.com", store.DomainState{ExpiresAt: "2028-02-02", CheckedAt: monAt}},
		// The later reading replaces the earlier.
		{id, "example.com", store.DomainState{ExpiresAt: "2029-02-02", CheckedAt: "2026-11-02T00:00:00Z"}},
		// One no monitor's origin uses (it raced the origin's removal) lands nowhere.
		{id, "gone.example", store.DomainState{ExpiresAt: "2030-01-01", CheckedAt: monAt}},
		{id + 100, "example.com", store.DomainState{ExpiresAt: "2030-01-01", CheckedAt: monAt}},
	} {
		if err := d.SetMonitorDomain(ctx, w.project, w.domain, w.state); err != nil {
			t.Fatal(err)
		}
	}
	ds, err := d.ListMonitorDomains(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[0].Domain != "example.com" || ds[0].ExpiresAt != "2029-02-02" || ds[1].Domain != "other.org" || ds[1].ExpiresAt != "" {
		t.Fatalf("domains = %+v", ds)
	}
	if ds, _ := d.ListMonitorDomains(ctx, other); len(ds) != 1 || ds[0].ExpiresAt != "2028-02-02" {
		t.Errorf("the other project's = %+v", ds)
	}
}

func TestSetMonitorCertErrorKeepsTheReading(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	if err := d.SetMonitorCert(ctx, id, monA, store.CertState{ExpiresAt: "2027-01-01T00:00:00Z", Issuer: "R3", Valid: true, CheckedAt: monAt}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorCertError(ctx, id, monA, "connection refused"); err != nil {
		t.Fatal(err)
	}
	m := getMon(t, d, id, monA)
	if m.CertError != "connection refused" || m.CertExpiresAt == "" || m.CertIssuer != "R3" || !m.CertValid || m.CertCheckedAt != monAt {
		t.Errorf("after a failed read: %+v", m)
	}
	// The next reading clears it.
	if err := d.SetMonitorCert(ctx, id, monA, store.CertState{ExpiresAt: "2027-01-01T00:00:00Z", Valid: true, CheckedAt: monAt}); err != nil {
		t.Fatal(err)
	}
	if m = getMon(t, d, id, monA); m.CertError != "" {
		t.Errorf("after a reading: %+v", m)
	}
}

// update is an origins edit: eligible are the origins a monitor may now use.
func updateOrigins(t *testing.T, d *DB, id int64, eligible []string) {
	t.Helper()
	if err := d.UpdateProject(context.Background(), store.RegistryProject{ID: id, Name: "a", AllowedOrigins: "[]", MonitorOrigins: eligible},
		store.AuditEntry{Actor: "t", Action: "project.update", Subject: "1"}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateProjectDeletesMonitorsOfRemovedOrigins(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id, other := monitorProject(t, d, "a"), monitorProject(t, d, "b")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	setMon(t, d, id, monB, store.MonitorSettings{Enabled: false}) // off: still a row, still removed
	setMon(t, d, id, monC, store.MonitorSettings{Enabled: true})
	setMon(t, d, other, monA, store.MonitorSettings{Enabled: true})

	updateOrigins(t, d, id, nil) // the origins did not change
	updateOrigins(t, d, id, []string{monA, monB, monC, "https://new.example"})
	if ms, _ := d.ListMonitors(ctx, id); len(ms) != 3 || auditCount(t, d, "monitor.remove") != 0 {
		t.Fatalf("origins kept: %+v, %d audit rows", ms, auditCount(t, d, "monitor.remove"))
	}

	updateOrigins(t, d, id, []string{monC})
	ms, _ := d.ListMonitors(ctx, id)
	if len(ms) != 1 || ms[0].Origin != monC {
		t.Fatalf("after the removal: %+v", ms)
	}
	if n := auditCount(t, d, "monitor.remove"); n != 2 {
		t.Fatalf("%d monitor.remove rows, want one per monitor", n)
	}
	var detail, subject string
	if err := d.db.QueryRowContext(ctx, `SELECT subject, detail FROM audit_log WHERE action='monitor.remove' ORDER BY rowid LIMIT 1`).Scan(&subject, &detail); err != nil {
		t.Fatal(err)
	}
	if subject != "1" || detail != "origin removed: "+monA {
		t.Errorf("audit subject %q detail %q", subject, detail)
	}
	if o, _ := d.ListMonitors(ctx, other); len(o) != 1 {
		t.Errorf("the other project's monitor: %+v", o)
	}

	// No eligible origin left: none stays.
	updateOrigins(t, d, id, []string{})
	if ms, _ := d.ListMonitors(ctx, id); len(ms) != 0 || auditCount(t, d, "monitor.remove") != 3 {
		t.Errorf("no eligible origin: %+v, %d audit rows", ms, auditCount(t, d, "monitor.remove"))
	}
}

func TestUpdateProjectDeletesDomainsNoMonitorUses(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id, other := monitorProject(t, d, "a"), monitorProject(t, d, "b")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	setMon(t, d, id, monB, store.MonitorSettings{Enabled: true})
	setMon(t, d, id, monC, store.MonitorSettings{Enabled: true})
	setMon(t, d, other, monC, store.MonitorSettings{Enabled: true})
	for _, w := range []struct {
		id     int64
		domain string
	}{{id, "example.com"}, {id, "other.org"}, {other, "other.org"}} {
		if err := d.SetMonitorDomain(ctx, w.id, w.domain, store.DomainState{CheckedAt: monAt}); err != nil {
			t.Fatal(err)
		}
	}
	domains := func(p int64) (out []string) {
		t.Helper()
		ds, err := d.ListMonitorDomains(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range ds {
			out = append(out, x.Domain)
		}
		return out
	}
	// One of two origins on a domain goes: the domain stays.
	updateOrigins(t, d, id, []string{monB, monC})
	if got := domains(id); len(got) != 2 {
		t.Fatalf("domains = %v, want both", got)
	}
	// The last goes: so does the domain, in this project only.
	updateOrigins(t, d, id, []string{monB})
	if got := domains(id); len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("domains = %v, want example.com", got)
	}
	if got := domains(other); len(got) != 1 {
		t.Errorf("the other project's = %v", got)
	}
	updateOrigins(t, d, id, []string{"https://elsewhere.net"})
	if got := domains(id); len(got) != 0 {
		t.Errorf("domains = %v, want none", got)
	}
}

func TestListEnabledMonitors(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	a, b, c, e := monitorProject(t, d, "a"), monitorProject(t, d, "b"), monitorProject(t, d, "c"), monitorProject(t, d, "d")
	setMon(t, d, a, monB, store.MonitorSettings{Enabled: true})
	setMon(t, d, a, monA, store.MonitorSettings{Enabled: true})
	setMon(t, d, a, monC, store.MonitorSettings{Enabled: false})
	setMon(t, d, b, monA, store.MonitorSettings{Enabled: false})
	setMon(t, d, c, monA, store.MonitorSettings{Enabled: true})
	setMon(t, d, e, monA, store.MonitorSettings{Enabled: true})
	if err := d.SetProjectArchived(ctx, c, true, store.AuditEntry{Actor: "t", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}
	ms, err := d.ListEnabledMonitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 || ms[0].ProjectID != a || ms[0].Origin != monA || ms[1].ProjectID != a || ms[1].Origin != monB || ms[2].ProjectID != e {
		t.Fatalf("ListEnabledMonitors = %+v, want %d's two by origin, then %d's", ms, a, e)
	}
}

// A check that raced the row's removal writes nothing, and brings nothing back.
func TestMonitorStateWritesOfARemovedMonitorAreIgnored(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	const old = "https://old.example"
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	if err := d.SetMonitorCert(ctx, id, old, store.CertState{Issuer: "R3", Valid: true}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorCertError(ctx, id, old, "other"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorRedirects(ctx, id, old, 9); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishMonitorCheck(ctx, id, old, store.MonitorCheckResult{FinishedAt: monAt}); err != nil {
		t.Fatal(err)
	}
	// A project with no row at all is a no-op too.
	if err := d.SetMonitorCert(ctx, id+100, old, store.CertState{Issuer: "R3"}); err != nil {
		t.Fatal(err)
	}
	ms, _ := d.ListMonitors(ctx, id)
	if want := (store.Monitor{ProjectID: id, Origin: monA, Enabled: true}); len(ms) != 1 || ms[0] != want {
		t.Fatalf("a stale write landed: %+v", ms)
	}
}

// Writes touch their own origin's row, not the project's others.
func TestMonitorStateWritesStayOnTheirOrigin(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	setMon(t, d, id, monB, store.MonitorSettings{Enabled: true})
	if err := d.SetMonitorCert(ctx, id, monA, store.CertState{Issuer: "R3", CheckedAt: monAt}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorRedirects(ctx, id, monA, 4); err != nil {
		t.Fatal(err)
	}
	if err := d.RequestMonitorCheck(ctx, id, monA, monAt, 0, store.AuditEntry{Actor: "t", Action: "monitor.check"}); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishMonitorCheck(ctx, id, monA, store.MonitorCheckResult{FinishedAt: monAt, Up: true}); err != nil {
		t.Fatal(err)
	}
	if m := getMon(t, d, id, monA); m.CertIssuer != "R3" || m.Redirects != 4 || !m.CheckUp || m.CheckFinishedAt != monAt {
		t.Errorf("the written row: %+v", m)
	}
	if m, want := getMon(t, d, id, monB), (store.Monitor{ProjectID: id, Origin: monB, Enabled: true}); m != want {
		t.Errorf("the other row: %+v", m)
	}
}

func TestSetMonitorRedirectsKeptByAnEdit(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	if err := d.SetMonitorRedirects(ctx, id, monA, 4); err != nil {
		t.Fatal(err)
	}
	if m := setMon(t, d, id, monA, store.MonitorSettings{Enabled: true, Contains: "x"}); m.Redirects != 4 {
		t.Errorf("an edit: Redirects %d, want 4", m.Redirects)
	}
	if ms, err := d.ListEnabledMonitors(ctx); err != nil || len(ms) != 1 || ms[0].Redirects != 4 {
		t.Errorf("ListEnabledMonitors = %+v, %v, want Redirects 4", ms, err)
	}
}

func TestMonitorUnknownProject(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.SetMonitor(context.Background(), 999, monA, store.MonitorSettings{}, monEligible, monAudit); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetMonitor: %v, want ErrNotFound", err)
	}
}

func TestDeleteProjectRemovesMonitorsAndDomains(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	if err := d.SetMonitorDomain(ctx, id, "example.com", store.DomainState{CheckedAt: monAt}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteProjectData(ctx, id, store.AuditEntry{Actor: "t", Action: "project.delete"}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"monitors", "monitor_domains"} {
		var n int
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows = %d, %v", table, n, err)
		}
	}
}

func TestMonitorCheckRequestAndFinish(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	audit := store.AuditEntry{Actor: "test", Action: "monitor.check", Subject: "1"}
	request := func(origin, at string) error { return d.RequestMonitorCheck(ctx, id, origin, at, 0, audit) }

	// No monitor to check: refused, and nothing audited.
	if err := request(monA, "2026-11-01T10:00:00Z"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("request with no monitor: %v, want ErrInvalid", err)
	}
	if err := d.RequestMonitorCheck(ctx, 99, monA, "2026-11-01T10:00:00Z", 0, audit); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("request on an unknown project: %v, want ErrNotFound", err)
	}
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	setMon(t, d, id, monB, store.MonitorSettings{Enabled: true})
	setMon(t, d, id, monC, store.MonitorSettings{Enabled: false})
	if err := request(monA, "2026-11-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	m := getMon(t, d, id, monA)
	if m.CheckRequestedAt != "2026-11-01T10:00:00Z" || m.CheckFinishedAt != "" {
		t.Fatalf("after the request: %+v", m)
	}
	if n := auditCount(t, d, "monitor.check"); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
	// One pending: two callers cannot both pass.
	if err := request(monA, "2026-11-01T10:00:01Z"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("second request: %v, want ErrBusy", err)
	}
	if n := auditCount(t, d, "monitor.check"); n != 1 {
		t.Fatalf("audit rows after the refusal = %d", n)
	}
	// Pending on one monitor holds back only that one.
	if err := request(monB, "2026-11-01T10:00:01Z"); err != nil {
		t.Errorf("the other monitor: %v", err)
	}
	if err := request(monC, "2026-11-01T10:00:01Z"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("a disabled monitor: %v, want ErrInvalid", err)
	}

	// The runner lists it.
	ms, err := d.ListEnabledMonitors(ctx)
	if err != nil || len(ms) != 2 || ms[0].CheckRequestedAt != "2026-11-01T10:00:00Z" {
		t.Fatalf("listed %+v, %v", ms, err)
	}

	res := store.MonitorCheckResult{FinishedAt: "2026-11-01T10:00:03Z", Up: false, Status: 503, Reason: "http_5xx", Error: "status 503", MS: 123}
	if err := d.FinishMonitorCheck(ctx, id, monA, res); err != nil {
		t.Fatal(err)
	}
	m = getMon(t, d, id, monA)
	if m.CheckFinishedAt != res.FinishedAt || m.CheckUp || m.CheckStatus != 503 || m.CheckReason != "http_5xx" || m.CheckError != "status 503" || m.CheckMS != 123 {
		t.Fatalf("after finish: %+v", m)
	}
	// Finishing is not audited.
	if n := auditCount(t, d, "monitor.check"); n != 2 {
		t.Fatalf("audit rows after finish = %d, want 2", n)
	}
	up := store.MonitorCheckResult{FinishedAt: "2026-11-01T10:01:03Z", Up: true, Status: 200, MS: 5}
	if err := d.FinishMonitorCheck(ctx, id, monA, up); err != nil {
		t.Fatal(err)
	}
	if m = getMon(t, d, id, monA); !m.CheckUp || m.CheckReason != "" || m.CheckError != "" {
		t.Fatalf("second result left the first's fields: %+v", m)
	}
	// An edit keeps it.
	if m := setMon(t, d, id, monA, store.MonitorSettings{Enabled: false}); m.CheckFinishedAt == "" {
		t.Fatalf("an edit: %+v", m)
	}
	if err := request(monA, "2026-11-01T10:02:00Z"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("request on a disabled monitor: %v, want ErrInvalid", err)
	}
}

// An origins edit that commits after manage's check removes the origin; the
// write must not bring its row back.
func TestSetMonitorRefusesAnOriginNoLongerListed(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	if err := d.UpdateProject(ctx, store.RegistryProject{ID: id, Name: "a", AllowedOrigins: `["https://b.example.com"]`,
		MonitorOrigins: []string{monB}}, store.AuditEntry{Actor: "t", Action: "project.update", Subject: "1"}); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, false} {
		if _, err := d.SetMonitor(ctx, id, monA, store.MonitorSettings{Enabled: on}, monEligible, monAudit); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("enabled=%t on a removed origin: %v, want ErrInvalid", on, err)
		}
	}
	if m := getMon(t, d, id, monA); m != (store.Monitor{}) {
		t.Errorf("the row came back: %+v", m)
	}
	// Turning an existing monitor off stays allowed whatever the origins say.
	setMon(t, d, id, monB, store.MonitorSettings{Enabled: true})
	none := func([]string) []string { return nil }
	if m, err := d.SetMonitor(ctx, id, monB, store.MonitorSettings{Enabled: false}, none, monAudit); err != nil || m.Enabled {
		t.Errorf("turning off: %+v, %v", m, err)
	}
	if _, err := d.SetMonitor(ctx, id, monB, store.MonitorSettings{Enabled: true}, none, monAudit); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("enabling when not eligible: %v, want ErrInvalid", err)
	}
}

func TestRequestMonitorCheckCooldownIsInTheWrite(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	audit := store.AuditEntry{Actor: "t", Action: "monitor.check", Subject: "1"}
	cool := 60 * time.Second
	if err := d.RequestMonitorCheck(ctx, id, monA, "2026-11-01T10:00:00Z", cool, audit); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishMonitorCheck(ctx, id, monA, store.MonitorCheckResult{FinishedAt: "2026-11-01T10:00:02Z", Up: true}); err != nil {
		t.Fatal(err)
	}
	if err := d.RequestMonitorCheck(ctx, id, monA, "2026-11-01T10:00:59Z", cool, audit); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("inside the cooldown: %v, want ErrBusy", err)
	}
	if err := d.RequestMonitorCheck(ctx, id, monA, "2026-11-01T10:01:00Z", cool, audit); err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
	if n := auditCount(t, d, "monitor.check"); n != 2 {
		t.Errorf("audit rows = %d, want 2", n)
	}
}

// A request in the second the last check finished is still pending, and the
// previous result stays readable while it runs.
func TestRequestMonitorCheckInTheSecondOfTheLastFinish(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	audit := store.AuditEntry{Actor: "t", Action: "monitor.check", Subject: "1"}
	if err := d.RequestMonitorCheck(ctx, id, monA, "2026-11-01T10:00:00Z", 0, audit); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishMonitorCheck(ctx, id, monA, store.MonitorCheckResult{FinishedAt: "2026-11-01T10:00:05Z", Up: false, Status: 503, Error: "e", MS: 7}); err != nil {
		t.Fatal(err)
	}
	if m := getMon(t, d, id, monA); m.CheckPending() {
		t.Fatalf("finished but pending: %+v", m)
	}
	if err := d.RequestMonitorCheck(ctx, id, monA, "2026-11-01T10:00:05Z", 0, audit); err != nil {
		t.Fatal(err)
	}
	m := getMon(t, d, id, monA)
	if !m.CheckPending() || m.CheckFinishedAt != "" {
		t.Fatalf("after the request: %+v, want pending with no finish time", m)
	}
	if m.CheckStatus != 503 || m.CheckError != "e" || m.CheckMS != 7 {
		t.Errorf("the previous result was dropped: %+v", m)
	}
	if err := d.RequestMonitorCheck(ctx, id, monA, "2026-11-01T10:00:05Z", 0, audit); !errors.Is(err, store.ErrBusy) {
		t.Errorf("a second request while pending: %v, want ErrBusy", err)
	}
	if err := d.FinishMonitorCheck(ctx, id, monA, store.MonitorCheckResult{FinishedAt: "2026-11-01T10:00:05Z", Up: true}); err != nil {
		t.Fatal(err)
	}
	if m := getMon(t, d, id, monA); m.CheckPending() {
		t.Errorf("finished in the same second, still pending: %+v", m)
	}
}

func TestPendingCheckEndsWhenTheMonitorIsTurnedOff(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	setMon(t, d, id, monB, store.MonitorSettings{Enabled: true})
	audit := store.AuditEntry{Actor: "t", Action: "monitor.check", Subject: "1"}
	for _, o := range []string{monA, monB} {
		if err := d.RequestMonitorCheck(ctx, id, o, "2026-11-01T10:00:00Z", 0, audit); err != nil {
			t.Fatal(err)
		}
	}
	m := setMon(t, d, id, monA, store.MonitorSettings{Enabled: false})
	if m.CheckPending() || m.CheckFinishedAt < m.CheckRequestedAt || m.CheckError != "the check did not run" || m.CheckUp {
		t.Errorf("turned off: %+v, want finished with the not-run error", m)
	}
	if b := getMon(t, d, id, monB); !b.CheckPending() {
		t.Errorf("the other monitor: %+v, want still pending", b)
	}
}

func TestPendingCheckEndsWhenTheProjectIsArchived(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id, other := monitorProject(t, d, "a"), monitorProject(t, d, "b")
	setMon(t, d, id, monA, store.MonitorSettings{Enabled: true})
	setMon(t, d, other, monA, store.MonitorSettings{Enabled: true})
	audit := store.AuditEntry{Actor: "t", Action: "monitor.check", Subject: "1"}
	for _, p := range []int64{id, other} {
		if err := d.RequestMonitorCheck(ctx, p, monA, "2026-11-01T10:00:00Z", 0, audit); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetProjectArchived(ctx, id, true, store.AuditEntry{Actor: "t", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}
	m := getMon(t, d, id, monA)
	if m.CheckPending() || m.CheckError != "the check did not run" {
		t.Errorf("archived: %+v, want finished with the not-run error", m)
	}
	if o := getMon(t, d, other, monA); !o.CheckPending() {
		t.Errorf("the other project: %+v, want still pending", o)
	}
}
