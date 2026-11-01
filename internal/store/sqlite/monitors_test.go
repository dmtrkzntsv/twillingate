package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func monitorProject(t *testing.T, d *DB, name string) int64 {
	t.Helper()
	id, err := d.CreateProject(context.Background(), store.RegistryProject{Name: name, AllowedOrigins: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var monAudit = store.AuditEntry{Actor: "test", Action: "monitor.set", Subject: "monitor"}

func TestMonitorRoundTrip(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")

	m, err := d.GetMonitor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.Monitor{ProjectID: id}); m != want {
		t.Fatalf("no row: %+v, want %+v", m, want)
	}

	got, err := d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: true, Origin: "https://a.example", Contains: "ok"}, monAudit)
	if err != nil {
		t.Fatal(err)
	}
	want := store.Monitor{ProjectID: id, Enabled: true, Origin: "https://a.example", Contains: "ok"}
	if got != want {
		t.Fatalf("SetMonitor = %+v, want %+v", got, want)
	}
	if m, _ = d.GetMonitor(ctx, id); m != want {
		t.Fatalf("GetMonitor = %+v, want %+v", m, want)
	}
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action='monitor.set'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d, %v", n, err)
	}
}

func TestSetMonitorOriginChangeClearsState(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	set := func(s store.MonitorSettings) store.Monitor {
		t.Helper()
		m, err := d.SetMonitor(ctx, id, s, monAudit)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	set(store.MonitorSettings{Enabled: true, Origin: "https://a.example"})
	if err := d.SetMonitorCert(ctx, id, "https://a.example", store.CertState{
		ExpiresAt: "2027-01-01T00:00:00Z", Issuer: "R3", Valid: true, CheckedAt: "2026-11-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorDomain(ctx, id, "https://a.example", store.DomainState{
		ExpiresAt: "2027-02-02", CheckedAt: "2026-11-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	m, _ := d.GetMonitor(ctx, id)
	if m.CertIssuer != "R3" || !m.CertValid || m.CertExpiresAt == "" || m.DomainExpiresAt != "2027-02-02" || m.DomainCheckedAt == "" {
		t.Fatalf("state not stored: %+v", m)
	}

	// Same origin keeps the state, whatever else changes.
	m = set(store.MonitorSettings{Enabled: false, Origin: "https://a.example", Contains: "x"})
	if m.CertIssuer != "R3" || m.DomainExpiresAt != "2027-02-02" || m.Enabled || m.Contains != "x" {
		t.Fatalf("same-origin edit changed state: %+v", m)
	}
	// A new origin clears it.
	m = set(store.MonitorSettings{Enabled: true, Origin: "https://b.example"})
	want := store.Monitor{ProjectID: id, Enabled: true, Origin: "https://b.example"}
	if m != want {
		t.Fatalf("origin change: %+v, want %+v", m, want)
	}
}

func TestSetMonitorEnablingClearsCheckTimesOnly(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	const origin, at = "https://a.example", "2026-11-01T00:00:00Z"
	set := func(enabled bool) store.Monitor {
		t.Helper()
		m, err := d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: enabled, Origin: origin}, monAudit)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	set(true)
	if err := d.SetMonitorCert(ctx, id, origin, store.CertState{ExpiresAt: "2027-01-01T00:00:00Z", Issuer: "R3", Valid: true, CheckedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorDomain(ctx, id, origin, store.DomainState{ExpiresAt: "2027-02-02", CheckedAt: at}); err != nil {
		t.Fatal(err)
	}
	// Staying on, or going off, keeps the check times.
	for _, on := range []bool{true, false, false} {
		if m := set(on); m.CertCheckedAt != at || m.DomainCheckedAt != at {
			t.Fatalf("enabled=%v: %+v, want the check times kept", on, m)
		}
	}
	m := set(true)
	if m.CertCheckedAt != "" || m.DomainCheckedAt != "" {
		t.Errorf("enabling kept the check times: %+v", m)
	}
	if m.CertExpiresAt == "" || m.CertIssuer != "R3" || !m.CertValid || m.DomainExpiresAt != "2027-02-02" {
		t.Errorf("enabling lost the readings: %+v", m)
	}
}

func TestSetMonitorCertErrorKeepsTheReading(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	const origin, at = "https://a.example", "2026-11-01T00:00:00Z"
	if _, err := d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: true, Origin: origin}, monAudit); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorCert(ctx, id, origin, store.CertState{ExpiresAt: "2027-01-01T00:00:00Z", Issuer: "R3", Valid: true, CheckedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorCertError(ctx, id, origin, "connection refused"); err != nil {
		t.Fatal(err)
	}
	m, _ := d.GetMonitor(ctx, id)
	if m.CertError != "connection refused" || m.CertExpiresAt == "" || m.CertIssuer != "R3" || !m.CertValid || m.CertCheckedAt != at {
		t.Errorf("after a failed read: %+v", m)
	}
	// Another origin's failure is not this one's.
	if err := d.SetMonitorCertError(ctx, id, "https://old.example", "other"); err != nil {
		t.Fatal(err)
	}
	if m, _ = d.GetMonitor(ctx, id); m.CertError != "connection refused" {
		t.Errorf("stale origin wrote: %+v", m)
	}
	// The next reading clears it.
	if err := d.SetMonitorCert(ctx, id, origin, store.CertState{ExpiresAt: "2027-01-01T00:00:00Z", Valid: true, CheckedAt: at}); err != nil {
		t.Fatal(err)
	}
	if m, _ = d.GetMonitor(ctx, id); m.CertError != "" {
		t.Errorf("after a reading: %+v", m)
	}
}

func TestUpdateProjectDisablesMonitorInItsTransaction(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	const origin = "https://a.example"
	audits := func() (n int) {
		t.Helper()
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action='monitor.disable'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	update := func(eligible []string) {
		t.Helper()
		if err := d.UpdateProject(ctx, store.RegistryProject{ID: id, Name: "a", AllowedOrigins: "[]", MonitorOrigins: eligible},
			store.AuditEntry{Actor: "t", Action: "project.update", Subject: "1"}); err != nil {
			t.Fatal(err)
		}
	}
	enabled := func() bool {
		t.Helper()
		m, err := d.GetMonitor(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return m.Enabled
	}
	if _, err := d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: true, Origin: origin}, monAudit); err != nil {
		t.Fatal(err)
	}
	update(nil) // the origins did not change
	update([]string{origin, "https://b.example"})
	if !enabled() || audits() != 0 {
		t.Fatalf("origin kept: enabled %v, %d audit rows", enabled(), audits())
	}
	update([]string{"https://b.example"})
	if enabled() || audits() != 1 {
		t.Fatalf("origin dropped: enabled %v, %d audit rows", enabled(), audits())
	}
	// Already off: nothing to disable, nothing to audit.
	update([]string{"https://c.example"})
	update([]string{})
	if audits() != 1 {
		t.Errorf("%d audit rows after updates of a disabled monitor, want 1", audits())
	}
	// No eligible origin left: off whatever the row says.
	if _, err := d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: true, Origin: origin}, monAudit); err != nil {
		t.Fatal(err)
	}
	update([]string{})
	if enabled() || audits() != 2 {
		t.Errorf("no eligible origin: enabled %v, %d audit rows", enabled(), audits())
	}
}

func TestListEnabledMonitors(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	a, b, c, e := monitorProject(t, d, "a"), monitorProject(t, d, "b"), monitorProject(t, d, "c"), monitorProject(t, d, "d")
	for id, on := range map[int64]bool{a: true, b: false, c: true, e: true} {
		if _, err := d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: on, Origin: "https://x.example"}, monAudit); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetProjectArchived(ctx, c, true, store.AuditEntry{Actor: "t", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}
	ms, err := d.ListEnabledMonitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ProjectID != a || ms[1].ProjectID != e {
		t.Fatalf("ListEnabledMonitors = %+v, want projects %d, %d", ms, a, e)
	}
}

func TestMonitorStateWriteWithStaleOriginIsIgnored(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	if _, err := d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: true, Origin: "https://new.example"}, monAudit); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorCert(ctx, id, "https://old.example", store.CertState{Issuer: "R3", Valid: true}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMonitorDomain(ctx, id, "https://old.example", store.DomainState{ExpiresAt: "2027-01-01"}); err != nil {
		t.Fatal(err)
	}
	// A project with no row at all is a no-op too.
	if err := d.SetMonitorCert(ctx, id+100, "https://old.example", store.CertState{Issuer: "R3"}); err != nil {
		t.Fatal(err)
	}
	m, _ := d.GetMonitor(ctx, id)
	if want := (store.Monitor{ProjectID: id, Enabled: true, Origin: "https://new.example"}); m != want {
		t.Fatalf("stale write landed: %+v", m)
	}
}

func TestSetMonitorRedirects(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	set := func(s store.MonitorSettings) store.Monitor {
		t.Helper()
		m, err := d.SetMonitor(ctx, id, s, monAudit)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	set(store.MonitorSettings{Enabled: true, Origin: "https://a.example"})
	if err := d.SetMonitorRedirects(ctx, id, "https://a.example", 4); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.GetMonitor(ctx, id); m.Redirects != 4 {
		t.Fatalf("Redirects %d, want 4", m.Redirects)
	}
	// A write for an origin the row no longer has is ignored.
	if err := d.SetMonitorRedirects(ctx, id, "https://old.example", 9); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.GetMonitor(ctx, id); m.Redirects != 4 {
		t.Errorf("stale write landed: Redirects %d", m.Redirects)
	}
	// The same origin keeps it, listed too; a new origin resets it.
	if m := set(store.MonitorSettings{Enabled: true, Origin: "https://a.example", Contains: "x"}); m.Redirects != 4 {
		t.Errorf("same-origin edit: Redirects %d, want 4", m.Redirects)
	}
	if ms, err := d.ListEnabledMonitors(ctx); err != nil || len(ms) != 1 || ms[0].Redirects != 4 {
		t.Errorf("ListEnabledMonitors = %+v, %v, want Redirects 4", ms, err)
	}
	if m := set(store.MonitorSettings{Enabled: true, Origin: "https://b.example"}); m.Redirects != 0 {
		t.Errorf("origin change: Redirects %d, want 0", m.Redirects)
	}
}

func TestMonitorUnknownProject(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.GetMonitor(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetMonitor: %v, want ErrNotFound", err)
	}
	if _, err := d.SetMonitor(ctx, 999, store.MonitorSettings{Origin: "https://x.example"}, monAudit); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetMonitor: %v, want ErrNotFound", err)
	}
}

func TestDeleteProjectRemovesMonitor(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	if _, err := d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: true, Origin: "https://a.example"}, monAudit); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteProjectData(ctx, id, store.AuditEntry{Actor: "t", Action: "project.delete"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM monitors`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("monitors rows = %d, %v", n, err)
	}
}

func TestMonitorCheckRequestAndFinish(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	id := monitorProject(t, d, "a")
	const origin = "https://a.example"
	audit := store.AuditEntry{Actor: "test", Action: "monitor.check", Subject: "1"}
	request := func(at string) error { return d.RequestMonitorCheck(ctx, id, at, audit) }

	// No monitor to check: refused, and nothing audited.
	if err := request("2026-11-01T10:00:00Z"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("request with no monitor: %v, want ErrInvalid", err)
	}
	if err := d.RequestMonitorCheck(ctx, 99, "2026-11-01T10:00:00Z", audit); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("request on an unknown project: %v, want ErrNotFound", err)
	}
	if _, err := d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: true, Origin: origin}, monAudit); err != nil {
		t.Fatal(err)
	}
	if err := request("2026-11-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	m, err := d.GetMonitor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.CheckRequestedAt != "2026-11-01T10:00:00Z" || m.CheckFinishedAt != "" {
		t.Fatalf("after the request: %+v", m)
	}
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action='monitor.check'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d, %v", n, err)
	}
	// One pending: two callers cannot both pass.
	if err := request("2026-11-01T10:00:01Z"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("second request: %v, want ErrBusy", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action='monitor.check'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows after the refusal = %d, %v", n, err)
	}

	// The runner lists it.
	ms, err := d.ListEnabledMonitors(ctx)
	if err != nil || len(ms) != 1 || ms[0].CheckRequestedAt != "2026-11-01T10:00:00Z" {
		t.Fatalf("listed %+v, %v", ms, err)
	}

	res := store.MonitorCheckResult{FinishedAt: "2026-11-01T10:00:03Z", Up: false, Status: 503, Reason: "http_5xx", Error: "status 503", MS: 123}
	// A check of another origin raced an edit: not recorded.
	if err := d.FinishMonitorCheck(ctx, id, "https://old.example", res); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.GetMonitor(ctx, id); m.CheckFinishedAt != "" {
		t.Fatalf("a stale origin's result was stored: %+v", m)
	}
	if err := d.FinishMonitorCheck(ctx, id, origin, res); err != nil {
		t.Fatal(err)
	}
	m, _ = d.GetMonitor(ctx, id)
	if m.CheckFinishedAt != res.FinishedAt || m.CheckUp || m.CheckStatus != 503 || m.CheckReason != "http_5xx" || m.CheckError != "status 503" || m.CheckMS != 123 {
		t.Fatalf("after finish: %+v", m)
	}
	// Finishing is not audited; requesting again is allowed by the store.
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action='monitor.check'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows after finish = %d, %v", n, err)
	}
	up := store.MonitorCheckResult{FinishedAt: "2026-11-01T10:01:03Z", Up: true, Status: 200, MS: 5}
	if err := d.FinishMonitorCheck(ctx, id, origin, up); err != nil {
		t.Fatal(err)
	}
	if m, _ = d.GetMonitor(ctx, id); !m.CheckUp || m.CheckReason != "" || m.CheckError != "" {
		t.Fatalf("second result left the first's fields: %+v", m)
	}

	// The same origin keeps it, a new one clears every field.
	if m, err = d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: false, Origin: origin}, monAudit); err != nil || m.CheckFinishedAt == "" {
		t.Fatalf("same origin: %+v, %v", m, err)
	}
	if err := request("2026-11-01T10:02:00Z"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("request on a disabled monitor: %v, want ErrInvalid", err)
	}
	m, err = d.SetMonitor(ctx, id, store.MonitorSettings{Enabled: true, Origin: "https://b.example"}, monAudit)
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.Monitor{ProjectID: id, Enabled: true, Origin: "https://b.example"}); m != want {
		t.Fatalf("origin change: %+v, want %+v", m, want)
	}
}
