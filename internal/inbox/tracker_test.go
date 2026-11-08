package inbox_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/inbox"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// fakeStore is an in-memory TrackerStore: states by key, the
// notifications PutNotifyState inserted, the monitors it lists.
type fakeStore struct {
	mu       sync.Mutex
	states   map[string]store.NotifyState
	sent     []store.NewNotification
	monitors []store.Monitor
	flushed  []time.Time
	failPut  bool
	failDel  bool
}

func newFake(monitors ...store.Monitor) *fakeStore {
	return &fakeStore{states: map[string]store.NotifyState{}, monitors: monitors}
}

func stateKey(projectID int64, kind, subject string) string {
	return fmt.Sprint(projectID, "|", kind, "|", subject)
}

func (f *fakeStore) ListNotifyStates(context.Context) ([]store.NotifyState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.NotifyState, 0, len(f.states))
	for _, s := range f.states {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeStore) PutNotifyState(_ context.Context, s store.NotifyState, n *store.NewNotification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPut {
		return errors.New("disk full")
	}
	f.states[stateKey(s.ProjectID, s.Kind, s.Subject)] = s
	if n != nil {
		f.sent = append(f.sent, *n)
	}
	return nil
}

func (f *fakeStore) DeleteNotifyState(_ context.Context, projectID int64, kind, subject string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDel {
		return errors.New("disk full")
	}
	delete(f.states, stateKey(projectID, kind, subject))
	return nil
}

func (f *fakeStore) FlushFormBursts(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushed = append(f.flushed, now)
	return 0, nil
}

func (f *fakeStore) ListEnabledMonitors(context.Context) ([]store.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Monitor(nil), f.monitors...), nil
}

// take returns the notifications sent since the last take.
func (f *fakeStore) take() []store.NewNotification {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

func (f *fakeStore) has(projectID int64, kind, subject string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.states[stateKey(projectID, kind, subject)]
	return ok
}

const (
	origin = "https://example.com"
	domain = "example.com"
)

var (
	t0      = time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC)
	ctx     = context.Background()
	monitor = store.Monitor{ProjectID: 1, Origin: origin, Enabled: true}
)

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newTracker(f *fakeStore) *inbox.Tracker { return inbox.NewTracker(f, 4*time.Hour, quiet()) }

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// one asserts exactly one notification of typ was sent and returns it.
func one(t *testing.T, f *fakeStore, typ string) store.NewNotification {
	t.Helper()
	got := f.take()
	if len(got) != 1 || got[0].Type != typ {
		t.Fatalf("sent %+v, want one %s", got, typ)
	}
	return got[0]
}

func none(t *testing.T, f *fakeStore) {
	t.Helper()
	if got := f.take(); len(got) != 0 {
		t.Fatalf("sent %+v, want nothing", got)
	}
}

func TestUptimeDebounce(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "dial tcp: i/o timeout", t0)
	none(t, f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "dial tcp: i/o timeout", t0.Add(time.Minute))
	n := one(t, f, store.NotifyUptimeDown)
	if n.ProjectID != 1 || n.Subject != origin || n.Fields["since"] != rfc(t0) || n.Fields["reason"] != "timeout" {
		t.Fatalf("uptime_down = %+v", n)
	}
	if !n.At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("At = %v", n.At)
	}
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(2*time.Minute))
	none(t, f)
}

func TestUptimeSuccessResetsCount(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0)
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(time.Minute))
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(2*time.Minute))
	none(t, f)
	if !f.has(1, "uptime", origin) {
		t.Fatal("pending failure not stored")
	}
}

func TestUptimeReminderAndRecovery(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "status", "", t0)
	tr.Uptime(ctx, 1, origin, false, "status", "", t0.Add(time.Minute))
	one(t, f, store.NotifyUptimeDown)

	tr.Tick(ctx, t0.Add(4*time.Hour))
	n := one(t, f, store.NotifyUptimeStillDown)
	if n.Fields["since"] != rfc(t0) || n.Subject != origin {
		t.Fatalf("still_down = %+v", n)
	}
	tr.Tick(ctx, t0.Add(5*time.Hour))
	none(t, f)
	tr.Tick(ctx, t0.Add(8*time.Hour+time.Minute))
	one(t, f, store.NotifyUptimeStillDown)

	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(9*time.Hour))
	n = one(t, f, store.NotifyUptimeUp)
	if n.Fields["since"] != rfc(t0) || n.Fields["until"] != rfc(t0.Add(9*time.Hour)) {
		t.Fatalf("uptime_up = %+v", n)
	}
	if f.has(1, "uptime", origin) {
		t.Fatal("state kept after recovery")
	}
	tr.Tick(ctx, t0.Add(20*time.Hour))
	none(t, f)
}

func TestTrackerSurvivesRestart(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "status", "", t0)
	tr.Uptime(ctx, 1, origin, false, "status", "", t0.Add(time.Minute))
	one(t, f, store.NotifyUptimeDown)
	tr.Tick(ctx, t0.Add(4*time.Hour))
	one(t, f, store.NotifyUptimeStillDown)

	tr = newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "status", "", t0.Add(4*time.Hour+time.Minute))
	none(t, f)
	tr.Tick(ctx, t0.Add(7*time.Hour))
	none(t, f)
	tr.Tick(ctx, t0.Add(8*time.Hour))
	n := one(t, f, store.NotifyUptimeStillDown)
	if n.Fields["since"] != rfc(t0) {
		t.Fatalf("since = %q after restart", n.Fields["since"])
	}
}

func TestStoreFailureRetries(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "status", "", t0)
	f.failPut = true
	tr.Uptime(ctx, 1, origin, false, "status", "", t0.Add(time.Minute))
	none(t, f)
	f.failPut = false
	tr.Uptime(ctx, 1, origin, false, "status", "", t0.Add(2*time.Minute))
	if n := one(t, f, store.NotifyUptimeDown); n.Fields["since"] != rfc(t0) {
		t.Fatalf("since = %q", n.Fields["since"])
	}
}

func TestCertInvalidImmediate(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Error: "x509: certificate has expired"}, t0)
	n := one(t, f, store.NotifyCertFailing)
	if n.Fields["since"] != rfc(t0) || n.Fields["error"] != "x509: certificate has expired" || n.Subject != origin {
		t.Fatalf("cert_failing = %+v", n)
	}
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Error: "x509: certificate has expired"}, t0.Add(time.Hour))
	none(t, f)

	tr.Tick(ctx, t0.Add(4*time.Hour))
	n = one(t, f, store.NotifyCertStillFailing)
	if n.Fields["error"] == "" || n.Fields["since"] != rfc(t0) {
		t.Fatalf("cert_still_failing = %+v", n)
	}

	exp := rfc(t0.Add(90 * 24 * time.Hour))
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Valid: true, ExpiresAt: exp}, t0.Add(5*time.Hour))
	n = one(t, f, store.NotifyCertRecovered)
	if n.Fields["since"] != rfc(t0) || n.Fields["until"] != rfc(t0.Add(5*time.Hour)) {
		t.Fatalf("cert_recovered = %+v", n)
	}
	if f.has(1, "cert", origin) {
		t.Fatal("cert state kept after recovery")
	}
}

func TestCertUnreadableAfter24h(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	unread := inbox.CertReading{Error: "tls: handshake timeout"}
	for h := 0; h < 24; h++ {
		tr.Cert(ctx, 1, origin, unread, t0.Add(time.Duration(h)*time.Hour))
	}
	none(t, f)
	tr.Tick(ctx, t0.Add(23*time.Hour+30*time.Minute))
	none(t, f)
	tr.Cert(ctx, 1, origin, unread, t0.Add(24*time.Hour))
	n := one(t, f, store.NotifyCertFailing)
	if n.Fields["since"] != rfc(t0) || n.Fields["error"] != "tls: handshake timeout" {
		t.Fatalf("cert_failing = %+v", n)
	}
	// The reminder comes a full interval after the announcement, not at once.
	tr.Tick(ctx, t0.Add(24*time.Hour+time.Minute))
	none(t, f)
	tr.Tick(ctx, t0.Add(28*time.Hour))
	one(t, f, store.NotifyCertStillFailing)
}

func TestCertUnreadableRecoversSilently(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Error: "timeout"}, t0)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Valid: true}, t0.Add(time.Hour))
	tr.Cert(ctx, 1, origin, inbox.CertReading{Error: "timeout"}, t0.Add(20*time.Hour))
	tr.Cert(ctx, 1, origin, inbox.CertReading{Error: "timeout"}, t0.Add(25*time.Hour))
	none(t, f)
}

func TestCertSilentWhileDown(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Error: "x509: expired"}, t0)
	one(t, f, store.NotifyCertFailing)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(time.Minute))
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(2*time.Minute))
	one(t, f, store.NotifyUptimeDown)

	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Error: "x509: unknown authority"}, t0.Add(time.Hour))
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Valid: true}, t0.Add(2*time.Hour))
	none(t, f)
	tr.Tick(ctx, t0.Add(4*time.Hour+2*time.Minute))
	one(t, f, store.NotifyUptimeStillDown) // and no cert reminder

	// A fresh origin's invalid certificate during an outage pages nothing.
	g := newFake(monitor)
	tr = newTracker(g)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(time.Minute))
	one(t, g, store.NotifyUptimeDown)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Error: "x509: expired"}, t0.Add(time.Hour))
	none(t, g)
	if g.has(1, "cert", origin) {
		t.Fatal("cert state stored while down")
	}
}

func TestDomainLookupFailingAfter24h(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	for h := 0; h < 24; h++ {
		tr.Domain(ctx, 1, domain, false, "rdap: 503", "", t0.Add(time.Duration(h)*time.Hour))
	}
	none(t, f)
	tr.Domain(ctx, 1, domain, false, "rdap: 503", "", t0.Add(24*time.Hour))
	n := one(t, f, store.NotifyDomainFailing)
	if n.Subject != domain || n.Fields["since"] != rfc(t0) || n.Fields["error"] != "rdap: 503" {
		t.Fatalf("domain_failing = %+v", n)
	}
	tr.Tick(ctx, t0.Add(28*time.Hour))
	one(t, f, store.NotifyDomainStillFailing)

	tr.Domain(ctx, 1, domain, true, "", "", t0.Add(29*time.Hour))
	n = one(t, f, store.NotifyDomainRecovered)
	if n.Fields["since"] != rfc(t0) || n.Fields["until"] != rfc(t0.Add(29*time.Hour)) {
		t.Fatalf("domain_recovered = %+v", n)
	}
	if f.has(1, "domain", domain) {
		t.Fatal("domain state kept after recovery")
	}
}

func TestExpirySteps(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	exp := t0.Add(20 * 24 * time.Hour)
	reading := func(e time.Time) inbox.CertReading {
		return inbox.CertReading{Read: true, Valid: true, ExpiresAt: rfc(e)}
	}
	tr.Cert(ctx, 1, origin, reading(exp), t0) // 20 days left
	none(t, f)
	at := exp.Add(-(13*24 + 12) * time.Hour) // 13.5 days left
	tr.Cert(ctx, 1, origin, reading(exp), at)
	n := one(t, f, store.NotifyCertExpiring)
	if n.Fields["days"] != "13" || n.Fields["expires"] != rfc(exp) || n.Subject != origin {
		t.Fatalf("cert_expiring = %+v", n)
	}
	tr.Cert(ctx, 1, origin, reading(exp), exp.Add(-12*24*time.Hour))
	none(t, f)
	tr.Cert(ctx, 1, origin, reading(exp), exp.Add(-6*24*time.Hour))
	if n := one(t, f, store.NotifyCertExpiring); n.Fields["days"] != "6" {
		t.Fatalf("days = %q", n.Fields["days"])
	}

	// Renewal resets the steps.
	now := exp.Add(-6 * 24 * time.Hour)
	renewed := now.Add(90 * 24 * time.Hour)
	tr.Cert(ctx, 1, origin, reading(renewed), now)
	none(t, f)
	tr.Cert(ctx, 1, origin, reading(renewed), renewed.Add(-13*24*time.Hour))
	if n := one(t, f, store.NotifyCertExpiring); n.Fields["days"] != "13" {
		t.Fatalf("days after renewal = %q", n.Fields["days"])
	}

	// First seen at 3 days left: one notification, for step 7.
	g := newFake(monitor)
	tr = newTracker(g)
	late := t0.Add(3*24*time.Hour + time.Hour)
	tr.Cert(ctx, 1, origin, reading(late), t0)
	if n := one(t, g, store.NotifyCertExpiring); n.Fields["days"] != "3" {
		t.Fatalf("days = %q", n.Fields["days"])
	}
	tr.Cert(ctx, 1, origin, reading(late), t0.Add(time.Hour))
	none(t, g)
	tr.Cert(ctx, 1, origin, reading(late), late.Add(-12*time.Hour)) // 0 days: step 1
	if n := one(t, g, store.NotifyCertExpiring); n.Fields["days"] != "0" {
		t.Fatalf("days = %q", n.Fields["days"])
	}
	tr.Cert(ctx, 1, origin, reading(late), late.Add(36*time.Hour)) // expired: step 1 already sent
	none(t, g)
}

func TestDomainExpirySteps(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	exp := rfc(t0.Add(40 * 24 * time.Hour))
	tr.Domain(ctx, 1, domain, true, "", exp, t0)
	none(t, f)
	tr.Domain(ctx, 1, domain, true, "", exp, t0.Add(15*24*time.Hour)) // 25 days
	n := one(t, f, store.NotifyDomainExpiring)
	if n.Subject != domain || n.Fields["days"] != "25" || n.Fields["expires"] != exp {
		t.Fatalf("domain_expiring = %+v", n)
	}
	tr.Domain(ctx, 1, domain, true, "", exp, t0.Add(20*24*time.Hour))
	none(t, f)
}

func TestExpiredDaysNegative(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	exp := t0.Add(-36 * time.Hour)
	tr.Domain(ctx, 1, domain, true, "", rfc(exp), t0)
	if n := one(t, f, store.NotifyDomainExpiring); n.Fields["days"] != "-2" {
		t.Fatalf("days = %q, want floor -2", n.Fields["days"])
	}
}

func TestTickPrunesDisabled(t *testing.T) {
	other := "https://gone.example.org"
	f := newFake(monitor)
	tr := newTracker(f)
	for _, o := range []string{origin, other} {
		tr.Uptime(ctx, 1, o, false, "timeout", "", t0)
		tr.Uptime(ctx, 1, o, false, "timeout", "", t0.Add(time.Minute))
	}
	tr.Domain(ctx, 1, "example.org", false, "rdap", "", t0)
	tr.Cert(ctx, 2, origin, inbox.CertReading{Read: true, Error: "x"}, t0) // same origin, other project
	f.take()

	tr.Tick(ctx, t0.Add(2*time.Minute))
	none(t, f)
	if f.has(1, "uptime", other) || f.has(1, "domain", "example.org") || f.has(2, "cert", origin) {
		t.Fatal("states of disabled monitors kept")
	}
	if !f.has(1, "uptime", origin) {
		t.Fatal("state of an enabled monitor pruned")
	}
	tr.Tick(ctx, t0.Add(4*time.Hour))
	one(t, f, store.NotifyUptimeStillDown)
}

func TestTickFlushesBursts(t *testing.T) {
	f := newFake()
	tr := newTracker(f)
	now := t0.Add(time.Hour)
	tr.Tick(ctx, now)
	if len(f.flushed) != 1 || !f.flushed[0].Equal(now) {
		t.Fatalf("flushed = %v", f.flushed)
	}
}

func TestTrackerConcurrent(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			at := t0.Add(time.Duration(i) * time.Minute)
			tr.Uptime(ctx, 1, origin, false, "timeout", "", at)
			tr.Cert(ctx, 1, origin, inbox.CertReading{Error: "timeout"}, at)
			tr.Tick(ctx, at)
		}(i)
	}
	wg.Wait()
	downs := 0
	for _, n := range f.take() {
		if n.Type == store.NotifyUptimeDown {
			downs++
		}
	}
	if downs != 1 {
		t.Fatalf("uptime_down sent %d times", downs)
	}
}

func (f *fakeStore) row(projectID int64, kind, subject, data string) {
	f.states[stateKey(projectID, kind, subject)] = store.NotifyState{ProjectID: projectID, Kind: kind, Subject: subject, Data: data}
}

func TestEmptyStateIsNew(t *testing.T) {
	for _, data := range []string{`{}`, `{"ended":"2026-11-01T00:00:00Z"}`} {
		f := newFake(monitor)
		f.row(1, "uptime", origin, data)
		f.row(1, "cert", origin, data)
		f.row(1, "domain", domain, data)
		tr := newTracker(f)
		tr.Cert(ctx, 1, origin, inbox.CertReading{Error: "timeout"}, t0)
		tr.Domain(ctx, 1, domain, false, "rdap: 503", "", t0)
		tr.Uptime(ctx, 1, origin, false, "timeout", "", t0)
		none(t, f)
	}
}

func TestFailedDeleteAfterRecovery(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(time.Minute))
	tr.Domain(ctx, 1, domain, false, "rdap: 503", "", t0.Add(-24*time.Hour))
	tr.Domain(ctx, 1, domain, false, "rdap: 503", "", t0)
	f.take()
	f.failDel = true
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(time.Hour))
	tr.Domain(ctx, 1, domain, true, "", "", t0.Add(time.Hour))
	if got := f.take(); len(got) != 2 {
		t.Fatalf("sent %+v, want uptime_up and domain_recovered", got)
	}
	f.failDel = false

	for _, tr := range []*inbox.Tracker{newTracker(f), tr} { // after a restart, and in memory
		tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(2*time.Hour))
		tr.Domain(ctx, 1, domain, false, "rdap: 503", "", t0.Add(2*time.Hour))
		none(t, f)
	}

	g := newFake(monitor)
	tr = newTracker(g)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(time.Minute))
	g.failDel = true
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(time.Hour))
	g.failDel = false
	g.take()
	tr.Tick(ctx, t0.Add(time.Hour+time.Minute)) // deletes the ended row
	if g.has(1, "uptime", origin) {
		t.Fatal("ended row kept by Tick")
	}
	none(t, g)
}

func TestPendingStreakGap(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(3*time.Hour))
	none(t, f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(4*time.Hour))
	if n := one(t, f, store.NotifyUptimeDown); n.Fields["since"] != rfc(t0.Add(3*time.Hour)) {
		t.Fatalf("since = %q, want the restarted streak", n.Fields["since"])
	}

	g := newFake(monitor)
	tr = newTracker(g)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(time.Hour))
	one(t, g, store.NotifyUptimeDown)

	h := newFake(monitor)
	tr = newTracker(h)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Error: "timeout"}, t0)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Error: "timeout"}, t0.Add(30*time.Hour))
	none(t, h)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Error: "timeout"}, t0.Add(54*time.Hour))
	if n := one(t, h, store.NotifyCertFailing); n.Fields["since"] != rfc(t0.Add(30*time.Hour)) {
		t.Fatalf("since = %q, want the restarted streak", n.Fields["since"])
	}
}

func TestCertReminderAfterOutage(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Error: "x509: expired"}, t0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(time.Minute))
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(2*time.Minute))
	for h := 1; h <= 10; h++ {
		tr.Tick(ctx, t0.Add(time.Duration(h)*time.Hour))
	}
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(10*time.Hour+time.Minute))
	f.take()
	tr.Tick(ctx, t0.Add(10*time.Hour+2*time.Minute))
	none(t, f)
	tr = newTracker(f) // the postponed reminder survives a restart
	tr.Tick(ctx, t0.Add(13*time.Hour))
	none(t, f)
	tr.Tick(ctx, t0.Add(14*time.Hour+time.Minute))
	one(t, f, store.NotifyCertStillFailing)
}

func TestExpiryOffsetSpelling(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	exp := t0.Add(20 * 24 * time.Hour)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Valid: true, ExpiresAt: rfc(exp)}, exp.Add(-13*24*time.Hour))
	one(t, f, store.NotifyCertExpiring)
	plus := exp.Format("2006-01-02T15:04:05") + "+00:00"
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Valid: true, ExpiresAt: plus}, exp.Add(-12*24*time.Hour))
	none(t, f)
}

// The monitor reports a domain's expiry as a bare date (RDAP's, in UTC);
// its days left count calendar days from the observation's UTC date, as
// the monitor's own daysLeft does, so the steps land on 30, 7 and 1.
func TestDomainExpiryBareDate(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	const exp = "2027-03-14"
	day := func(s string, hour int) time.Time {
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return d.Add(time.Duration(hour) * time.Hour)
	}
	tr.Domain(ctx, 1, domain, true, "", exp, day("2027-02-01", 10)) // 41 days
	none(t, f)
	tr.Domain(ctx, 1, domain, true, "", exp, day("2027-02-11", 23)) // 31 days
	none(t, f)
	tr.Domain(ctx, 1, domain, true, "", exp, day("2027-02-12", 0)) // 30 days
	n := one(t, f, store.NotifyDomainExpiring)
	if n.Fields["days"] != "30" || n.Fields["expires"] != exp || n.Subject != domain {
		t.Fatalf("domain_expiring = %+v", n)
	}
	tr.Domain(ctx, 1, domain, true, "", exp, day("2027-03-06", 23)) // 8 days
	none(t, f)
	tr.Domain(ctx, 1, domain, true, "", exp, day("2027-03-07", 1)) // 7 days
	if n := one(t, f, store.NotifyDomainExpiring); n.Fields["days"] != "7" {
		t.Fatalf("days = %q, want 7", n.Fields["days"])
	}
	tr.Domain(ctx, 1, domain, true, "", exp, day("2027-03-12", 23)) // 2 days
	none(t, f)
	tr.Domain(ctx, 1, domain, true, "", exp, day("2027-03-13", 20)) // 1 day
	if n := one(t, f, store.NotifyDomainExpiring); n.Fields["days"] != "1" {
		t.Fatalf("days = %q, want 1", n.Fields["days"])
	}
	tr.Domain(ctx, 1, domain, true, "", exp, day("2027-03-14", 12)) // the day itself: step 1 sent
	none(t, f)

	// A renewal, also a bare date, starts over.
	tr.Domain(ctx, 1, domain, true, "", "2028-03-14", day("2027-03-14", 13))
	none(t, f)
	tr.Domain(ctx, 1, domain, true, "", "2028-03-14", day("2028-03-13", 0))
	if n := one(t, f, store.NotifyDomainExpiring); n.Fields["days"] != "1" {
		t.Fatalf("days after renewal = %q, want 1", n.Fields["days"])
	}

	// Already past: negative calendar days.
	g := newFake(monitor)
	tr = newTracker(g)
	tr.Domain(ctx, 1, domain, true, "", exp, day("2027-03-16", 1))
	if n := one(t, g, store.NotifyDomainExpiring); n.Fields["days"] != "-2" {
		t.Fatalf("days = %q, want -2", n.Fields["days"])
	}
}

// With monitoring paused nothing observes a failing state any more, so no
// reminder goes; the state stays for a later run, and bursts still flush.
func TestPausedMonitoringSendsNoReminders(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(time.Minute))
	one(t, f, store.NotifyUptimeDown)

	tr.PauseMonitoring()
	now := t0.Add(9 * time.Hour)
	tr.Tick(ctx, now) // two intervals past: no reminder
	none(t, f)
	if len(f.flushed) != 1 || !f.flushed[0].Equal(now) {
		t.Fatalf("flushed = %v, want one at %v", f.flushed, now)
	}
	f.monitors = nil // a monitor switched off meanwhile: not pruned while paused
	tr.Tick(ctx, now)
	if !f.has(1, "uptime", origin) {
		t.Fatal("failing state dropped while paused")
	}

	// A later run with monitoring on continues the state.
	g := newFake(monitor)
	g.states = f.states
	tr = newTracker(g)
	tr.Tick(ctx, now)
	one(t, g, store.NotifyUptimeStillDown)
}

// A check that started earlier but reported later (a manual check beside a
// slot) is stale: it neither recovers the outage nor dates its end before
// its start.
func TestUptimeOutOfOrderIgnored(t *testing.T) {
	f := newFake(monitor)
	tr := newTracker(f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(time.Minute))
	one(t, f, store.NotifyUptimeDown)
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(-time.Minute))
	none(t, f)
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(30*time.Second)) // before the latest failure
	none(t, f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(5*time.Minute))
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(3*time.Minute)) // before the latest failure
	none(t, f)
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(6*time.Minute))
	n := one(t, f, store.NotifyUptimeUp)
	if n.Fields["since"] != rfc(t0) || n.Fields["until"] != rfc(t0.Add(6*time.Minute)) {
		t.Fatalf("uptime_up = %+v", n)
	}
}
