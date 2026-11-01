package monitor

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/google/uuid"
)

type domainKey struct {
	id     int64
	domain string
}

type fakeStore struct {
	mu       sync.Mutex
	monitors []store.Monitor
	certs    map[int64]store.CertState
	domains  map[domainKey]store.DomainState
	// certWrites counts every stored reading, certOrigins their origins, and
	// certErrs holds the failures, stored apart.
	certWrites  int
	certOrigins []string
	certErrs    map[int64]string
	// redirects holds the stored hop counts, redirectWrites every write.
	redirects      map[int64]int
	redirectWrites int
	// finished holds each stored manual check's result; finishCtxErr the
	// context error the last one was stored under.
	finished     map[monitorKey][]store.MonitorCheckResult
	finishCtxErr error
	// onList, when set, runs after the list is read and before it is returned.
	onList func()
}

func (f *fakeStore) FinishMonitorCheck(ctx context.Context, id int64, origin string, res store.MonitorCheckResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finished == nil {
		f.finished = map[monitorKey][]store.MonitorCheckResult{}
	}
	k := monitorKey{id, origin}
	f.finished[k] = append(f.finished[k], res)
	f.finishCtxErr = ctx.Err()
	for i := range f.monitors {
		if f.monitors[i].ProjectID == id && f.monitors[i].Origin == origin {
			f.monitors[i].CheckFinishedAt = res.FinishedAt
		}
	}
	return nil
}

func (f *fakeStore) results(id int64, origin string) []store.MonitorCheckResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.MonitorCheckResult(nil), f.finished[monitorKey{id, origin}]...)
}

func (f *fakeStore) SetMonitorRedirects(_ context.Context, id int64, origin string, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.redirects == nil {
		f.redirects = map[int64]int{}
	}
	f.redirects[id] = n
	f.redirectWrites++
	for i := range f.monitors {
		if f.monitors[i].ProjectID == id && f.monitors[i].Origin == origin {
			f.monitors[i].Redirects = n
		}
	}
	return nil
}

func (f *fakeStore) ListEnabledMonitors(context.Context) ([]store.Monitor, error) {
	f.mu.Lock()
	out := append([]store.Monitor(nil), f.monitors...)
	hook := f.onList
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return out, nil
}

func (f *fakeStore) SetMonitorCert(_ context.Context, id int64, origin string, c store.CertState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.certs == nil {
		f.certs = map[int64]store.CertState{}
	}
	f.certs[id] = c
	f.certWrites++
	f.certOrigins = append(f.certOrigins, origin)
	for i := range f.monitors {
		if f.monitors[i].ProjectID == id && f.monitors[i].Origin == origin {
			f.monitors[i].CertCheckedAt = c.CheckedAt
		}
	}
	return nil
}

func (f *fakeStore) SetMonitorCertError(_ context.Context, id int64, origin, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.certErrs == nil {
		f.certErrs = map[int64]string{}
	}
	f.certErrs[id] = msg
	return nil
}

func (f *fakeStore) ListMonitorDomains(_ context.Context, id int64) ([]store.MonitorDomain, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.MonitorDomain
	for k, d := range f.domains {
		if k.id == id {
			out = append(out, store.MonitorDomain{ProjectID: id, Domain: k.domain, ExpiresAt: d.ExpiresAt, CheckedAt: d.CheckedAt})
		}
	}
	return out, nil
}

func (f *fakeStore) SetMonitorDomain(_ context.Context, id int64, domain string, d store.DomainState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.domains == nil {
		f.domains = map[domainKey]store.DomainState{}
	}
	f.domains[domainKey{id, domain}] = d
	return nil
}

func (f *fakeStore) domain(id int64, domain string) (store.DomainState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.domains[domainKey{id, domain}]
	return d, ok
}

// setEnabled replaces the enabled monitors, as an edit would.
func (f *fakeStore) setEnabled(ms ...store.Monitor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.monitors = ms
}

type fakeLog struct {
	mu      sync.Mutex
	batches [][]store.Event
	err     error
}

func (f *fakeLog) WriteEvents(_ context.Context, evs []store.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.batches = append(f.batches, append([]store.Event(nil), evs...))
	return nil
}

func (f *fakeLog) all() []store.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Event
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

type allowAll struct{}

func (allowAll) RedirectAllowed(int64, string, string) bool { return true }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// noReads leaves the daily reads out of a test about the slots.
func noReads(r *Runner) *Runner {
	r.readCert = func(context.Context, store.Monitor, time.Time) []store.Event { return nil }
	r.readDomain = func(context.Context, int64, string, time.Time) []store.Event { return nil }
	return r
}

// waitIdle waits until at most n checks are still running.
func waitIdle(t *testing.T, r *Runner, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.inflight() > n {
		if time.Now().After(deadline) {
			t.Fatalf("still %d running, want %d", r.inflight(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSchedulerSpreadsAndSkipsOverlap(t *testing.T) {
	st := &fakeStore{}
	for id := int64(1); id <= 50; id++ {
		st.monitors = append(st.monitors, store.Monitor{ProjectID: id, Enabled: true, Origin: "https://x.example"})
	}
	interval := 60 * time.Second
	r := noReads(New(st, allowAll{}, &fakeLog{}, interval, 5*time.Second, quietLogger()))
	release := make(chan struct{})
	var mu sync.Mutex
	starts := map[int64][]time.Time{}
	var clock time.Time
	r.job = func(ctx context.Context, m store.Monitor) []store.Event {
		mu.Lock()
		starts[m.ProjectID] = append(starts[m.ProjectID], clock)
		mu.Unlock()
		if m.ProjectID == 7 {
			<-release
		}
		return nil
	}
	ctx := context.Background()
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	prev := t0
	step := func(until time.Time) {
		for now := prev.Add(time.Second); !now.After(until); now = now.Add(time.Second) {
			mu.Lock()
			clock = now
			mu.Unlock()
			r.tick(ctx, prev, now)
			prev = now
			r.mu.Lock()
			running7 := r.slots[7]
			r.mu.Unlock()
			if running7 {
				waitIdle(t, r, 1)
			} else {
				waitIdle(t, r, 0)
			}
		}
	}
	step(t0.Add(2 * interval))

	seconds := map[int]bool{}
	for id := int64(1); id <= 50; id++ {
		got := starts[id]
		if id == 7 {
			if len(got) != 1 {
				t.Errorf("monitor 7 started %d times while still running, want 1", len(got))
			}
			continue
		}
		if len(got) != 2 {
			t.Errorf("monitor %d started %d times in two intervals, want 2", id, len(got))
			continue
		}
		if d := got[1].Sub(got[0]); d != interval {
			t.Errorf("monitor %d: %v between checks, want %v", id, d, interval)
		}
		if off := got[0].Sub(t0) % interval; off != offset(id, interval) {
			t.Errorf("monitor %d at offset %v, want %v", id, off, offset(id, interval))
		}
		seconds[got[0].Second()] = true
	}
	if len(seconds) < 20 {
		t.Errorf("50 monitors start in only %d distinct seconds; want them spread", len(seconds))
	}

	close(release)
	waitIdle(t, r, 0)
	step(t0.Add(3 * interval))
	if n := len(starts[7]); n != 2 {
		t.Errorf("monitor 7 started %d times after finishing, want 2", n)
	}
}

func TestSchedulerPoolBounded(t *testing.T) {
	st := &fakeStore{}
	for id := int64(1); id <= 40; id++ {
		st.monitors = append(st.monitors, store.Monitor{ProjectID: id, Enabled: true, Origin: "https://x.example"})
	}
	r := noReads(New(st, allowAll{}, &fakeLog{}, 10*time.Second, 5*time.Second, quietLogger()))
	release := make(chan struct{})
	entered := make(chan struct{}, 40)
	var mu sync.Mutex
	active, peak := 0, 0
	r.job = func(ctx context.Context, m store.Monitor) []store.Event {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		entered <- struct{}{}
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r.tick(context.Background(), t0, t0.Add(10*time.Second))
	r.mu.Lock()
	dispatched := len(r.slots)
	r.mu.Unlock()
	if dispatched != 40 {
		t.Fatalf("%d dispatched, want 40", dispatched)
	}
	// Once the pool is full every worker slot is held, so no further job
	// can start until one is released.
	for range workers {
		<-entered
	}
	if n := len(r.sem); n != workers {
		t.Errorf("%d worker slots held, want %d", n, workers)
	}
	// Release one at a time: each release lets exactly one more in.
	for i := workers; i < 40; i++ {
		release <- struct{}{}
		<-entered
	}
	close(release)
	waitIdle(t, r, 0)
	if peak != workers {
		t.Errorf("peak %d concurrent checks, want %d", peak, workers)
	}
}

func TestRunnerWritesChecksRows(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer down.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "secure") }))
	tlsSrv.Config.ErrorLog = discardLog()
	defer tlsSrv.Close()
	rd, _ := fakeRDAP(t, nil)

	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	st := &fakeStore{monitors: []store.Monitor{
		{ProjectID: 1, Enabled: true, Origin: up.URL, CertCheckedAt: fresh},
		{ProjectID: 2, Enabled: true, Origin: down.URL, CertCheckedAt: fresh},
		{ProjectID: 3, Enabled: true, Origin: tlsSrv.URL}, // just enabled: no cert_checked_at
	}, domains: map[domainKey]store.DomainState{
		{1, "127.0.0.1"}: {CheckedAt: fresh},
		{2, "127.0.0.1"}: {CheckedAt: fresh},
	}}
	lg := &fakeLog{}
	pool := x509.NewCertPool()
	pool.AddCert(tlsSrv.Certificate())
	r := newRunner(st, allowAll{}, lg, 60*time.Second, quietLogger(), options{
		allowLoopback: true, roots: pool, bootstrapURL: rd.URL + "/dns.json",
		now: func() time.Time { return now },
	})
	ctx := context.Background()
	r.tick(ctx, now.Add(-60*time.Second), now) // every offset falls in one interval
	waitIdle(t, r, 0)
	r.tick(ctx, now, now) // flushes what finished
	evs := lg.all()
	if len(lg.batches) != 1 {
		t.Errorf("%d batches, want 1 per tick", len(lg.batches))
	}

	byKey := map[string]store.Event{}
	for _, e := range evs {
		if e.Family != store.FamilyChecks {
			t.Errorf("family %q", e.Family)
		}
		if e.TS.IsZero() || !e.TS.Equal(e.ReceivedAt) || e.TS.Location() != time.UTC {
			t.Errorf("ts %v received %v", e.TS, e.ReceivedAt)
		}
		if id, err := uuid.Parse(e.ID); err != nil || id.Version() != 7 {
			t.Errorf("id %q not a v7 uuid", e.ID)
		}
		for k, v := range e.Attributes {
			if k == "" || k[0] == '$' || len(v) > 200 {
				t.Errorf("attribute %q=%q", k, v)
			}
		}
		byKey[fmt.Sprintf("%d/%s", e.ProjectID, e.EventName)] = e
	}
	if len(evs) != 5 {
		t.Fatalf("%d rows, want 3 uptime + cert + domain: %+v", len(evs), evs)
	}
	e := byKey["1/uptime"]
	if a := e.Attributes; a["origin"] != up.URL || a["up"] != "1" || a["status"] != "200" || a["reason"] != "" || a["error"] != "" || e.Value == nil {
		t.Errorf("up row %+v value %v", a, e.Value)
	}
	e = byKey["2/uptime"]
	if a := e.Attributes; a["origin"] != down.URL || a["up"] != "0" || a["status"] != "404" || a["reason"] != "http_4xx" || a["error"] == "" {
		t.Errorf("down row %+v", a)
	}
	if a := byKey["3/uptime"].Attributes; a["up"] != "1" {
		t.Errorf("tls row %+v", a)
	}
	c := byKey["3/cert"]
	wantExp := tlsSrv.Certificate().NotAfter.UTC().Format("2006-01-02")
	if a := c.Attributes; a["origin"] != tlsSrv.URL || a["expires"] != wantExp || a["valid"] != "1" || a["error"] != "" || c.Value == nil {
		t.Errorf("cert row %+v value %v", a, c.Value)
	}
	d := byKey["3/domain"]
	if _, ok := d.Attributes["expires"]; ok || d.Value != nil || d.Attributes["domain"] != "127.0.0.1" {
		t.Errorf("domain row for an address origin: %+v value %v, want unknown", d.Attributes, d.Value)
	}
	if cs := st.certs[3]; !cs.Valid || cs.CheckedAt != "2026-11-01T10:00:00Z" {
		t.Errorf("stored cert %+v", cs)
	}
	if ds, ok := st.domain(3, "127.0.0.1"); !ok || ds.ExpiresAt != "" || ds.CheckedAt == "" {
		t.Errorf("stored domain %+v %v", ds, ok)
	}
	if _, ok := st.certs[1]; ok {
		t.Error("cert re-checked within a day")
	}

	// A day later both daily checks are due again on every monitor; an
	// http:// origin stores a check time and writes a cert row with no value.
	now = now.Add(25 * time.Hour)
	r.tick(ctx, now.Add(-60*time.Second), now)
	waitIdle(t, r, 0)
	r.tick(ctx, now, now)
	if _, ok := st.certs[1]; !ok {
		t.Error("cert not re-checked after a day")
	}
	if st.certs[1].ExpiresAt != "" {
		t.Errorf("http origin cert %+v", st.certs[1])
	}
	certRows := map[int64]int{}
	for _, e := range lg.all() {
		if e.EventName != "cert" {
			continue
		}
		certRows[e.ProjectID]++
		if e.ProjectID != 3 && (e.Value != nil || len(e.Attributes) != 1 || e.Attributes["origin"] == "") {
			t.Errorf("http origin's cert row: value %v, attrs %v, want the origin alone", e.Value, e.Attributes)
		}
	}
	if certRows[1] != 1 || certRows[2] != 1 || certRows[3] != 2 {
		t.Errorf("cert rows per project = %v, want one each for the http origins, two for project 3", certRows)
	}
}

func TestRunnerRedirectRuleUsesMonitorOrigin(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer other.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer site.Close()
	o := &recordOrigins{}
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	fresh := now.Format(time.RFC3339)
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 9, Enabled: true, Origin: site.URL, CertCheckedAt: fresh}},
		domains: map[domainKey]store.DomainState{{9, "127.0.0.1"}: {CheckedAt: fresh}}}
	lg := &fakeLog{}
	r := newRunner(st, o, lg, 60*time.Second, quietLogger(), options{allowLoopback: true, now: func() time.Time { return now }})
	r.tick(context.Background(), now.Add(-time.Minute), now)
	waitIdle(t, r, 0)
	r.tick(context.Background(), now, now)
	if len(o.calls) != 1 || o.calls[0] != [3]string{"9", other.URL, site.URL} {
		t.Errorf("RedirectAllowed calls %v", o.calls)
	}
	if evs := lg.all(); len(evs) != 1 || evs[0].Attributes["reason"] != "redirect" {
		t.Errorf("rows %+v", evs)
	}
}

type recordOrigins struct {
	mu    sync.Mutex
	calls [][3]string
}

func (o *recordOrigins) RedirectAllowed(id int64, target, req string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, [3]string{fmt.Sprint(id), target, req})
	return false
}

// syncBuffer is a log sink the runner's goroutines can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunnerWriteErrorLogged(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 1, Enabled: true, Origin: "https://x.example"}}}
	lg := &fakeLog{err: errors.New("disk full")}
	var out syncBuffer
	r := noReads(New(st, allowAll{}, lg, 10*time.Second, 5*time.Second, slog.New(slog.NewTextHandler(&out, nil))))
	r.job = func(context.Context, store.Monitor) []store.Event {
		return []store.Event{{ID: "a", ProjectID: 1, Family: store.FamilyChecks, EventName: "uptime"}}
	}
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r.tick(context.Background(), t0, t0.Add(10*time.Second))
	waitIdle(t, r, 0)
	r.tick(context.Background(), t0.Add(10*time.Second), t0.Add(10*time.Second)) // must not panic
	if got := out.String(); !strings.Contains(got, "level=ERROR") || !strings.Contains(got, `msg="monitor: write check rows"`) ||
		!strings.Contains(got, `error="disk full"`) || !strings.Contains(got, "rows=1") {
		t.Errorf("log %q, want the failed write logged", got)
	}
	lg.mu.Lock()
	lg.err = nil
	lg.mu.Unlock()
	r.tick(context.Background(), t0.Add(10*time.Second), t0.Add(10*time.Second))
	if n := len(lg.all()); n != 0 {
		t.Errorf("%d rows rewritten after a failed write, want 0 (dropped)", n)
	}
}

func TestRunStopsAndFlushes(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{
		{ProjectID: 1, Enabled: true, Origin: "https://slow.example"},
		{ProjectID: 2, Enabled: true, Origin: "https://quick.example"},
	}}
	lg := &fakeLog{}
	r := noReads(New(st, allowAll{}, lg, 10*time.Second, 5*time.Second, quietLogger()))
	r.step = 5 * time.Millisecond
	slowStarted := make(chan struct{})
	quickDone := make(chan struct{})
	var once sync.Once
	r.job = func(ctx context.Context, m store.Monitor) []store.Event {
		if m.ProjectID == 2 {
			defer once.Do(func() { close(quickDone) })
			return []store.Event{{ID: uuid.NewString(), ProjectID: 2, EventName: "finished"}}
		}
		close(slowStarted)
		<-ctx.Done()
		return []store.Event{{ID: "late", ProjectID: 1}}
	}
	// Every slot is due: the clock jumps a whole interval per tick.
	var mu sync.Mutex
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { mu.Lock(); defer mu.Unlock(); clock = clock.Add(10 * time.Second); return clock }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	for _, c := range []chan struct{}{slowStarted, quickDone} {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatal("checks did not run")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	var finished int
	for _, e := range lg.all() {
		if e.ID == "late" {
			t.Error("a check cut short by the cancel was written")
		}
		if e.EventName == "finished" {
			finished++
		}
	}
	if finished == 0 {
		t.Error("rows finished before the cancel were not written")
	}
}

func TestRunFlushesPendingOnStop(t *testing.T) {
	lg := &fakeLog{}
	r := New(&fakeStore{}, allowAll{}, lg, 10*time.Second, 5*time.Second, quietLogger())
	r.step = time.Hour // no tick: only the shutdown flush can write
	r.pending = []store.Event{{ID: "done-before-stop", ProjectID: 1}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	cancel()
	<-done
	if evs := lg.all(); len(evs) != 1 || evs[0].ID != "done-before-stop" {
		t.Errorf("rows written at stop %+v", evs)
	}
}

func TestRunnerUsesTheConfiguredTimeout(t *testing.T) {
	if r := New(&fakeStore{}, allowAll{}, &fakeLog{}, 10*time.Second, 7*time.Second, quietLogger()); r.timeout != 7*time.Second || r.guard.budget != 7*time.Second {
		t.Errorf("timeout %v, dial budget %v, want 7s both", r.timeout, r.guard.budget)
	}
}

func TestSlowSiteIsATimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	r := newRunner(&fakeStore{}, allowAll{}, &fakeLog{}, 60*time.Second, quietLogger(),
		options{allowLoopback: true, timeout: time.Second})
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	start := time.Now()
	evs := r.runSlot(context.Background(), store.Monitor{ProjectID: 1, Enabled: true, Origin: srv.URL})
	if a := evs[0].Attributes; a["up"] != "0" || a["reason"] != "timeout" {
		t.Errorf("row %+v, want down with reason timeout", a)
	}
	if d := time.Since(start); d < 900*time.Millisecond || d > 4*time.Second {
		t.Errorf("gave up after %v, want about the 1 s timeout", d)
	}
}

func TestRunnerKeepsDomainOnLookupFailure(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dns.json" {
			fmt.Fprintf(w, `{"services":[[["com"],["%s/rdap/"]]]}`, srv.URL)
			return
		}
		hits.Add(1)
		if failing.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"events":[{"eventAction":"expiration","eventDate":"2028-05-05T00:00:00Z"}]}`)
	}))
	defer srv.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer site.Close()
	_, port, _ := net.SplitHostPort(site.Listener.Addr().String())
	origin := "http://shop.example.com:" + port

	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	dayOld := now.Add(-25 * time.Hour).Format(time.RFC3339)
	const domain = "example.com"
	last := store.DomainState{ExpiresAt: "2027-03-14", CheckedAt: dayOld}
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 5, Enabled: true, Origin: origin, CertCheckedAt: dayOld}},
		domains: map[domainKey]store.DomainState{{5, domain}: last}}
	lg := &fakeLog{}
	var mu sync.Mutex
	r := newRunner(st, allowAll{}, lg, 60*time.Second, quietLogger(), options{
		resolver: fakeResolver{"shop.example.com": {"127.0.0.1"}}, allowLoopback: true,
		bootstrapURL: srv.URL + "/dns.json", now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
	})
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		at := now
		mu.Unlock()
		r.tick(context.Background(), at.Add(-time.Minute), at)
		waitIdle(t, r, 0)
		r.tick(context.Background(), at, at)
	}
	domainRows := func() (n int) {
		for _, e := range lg.all() {
			if e.EventName == "domain" {
				n++
			}
		}
		return n
	}

	advance(0)
	if hits.Load() != 1 {
		t.Fatalf("%d RDAP queries, want 1", hits.Load())
	}
	if d, _ := st.domain(5, domain); d != last {
		t.Errorf("a failed lookup stored %+v", d)
	}
	if n := domainRows(); n != 0 {
		t.Errorf("%d domain rows after a failed lookup, want 0", n)
	}

	advance(30 * time.Minute) // within dailyRetry: no new lookup
	if hits.Load() != 1 {
		t.Errorf("%d RDAP queries within the retry wait, want 1", hits.Load())
	}

	failing.Store(false)
	advance(31 * time.Minute) // past dailyRetry: retried
	if hits.Load() != 2 {
		t.Errorf("%d RDAP queries after the retry wait, want 2", hits.Load())
	}
	if d, _ := st.domain(5, domain); d.ExpiresAt != "2028-05-05" {
		t.Errorf("stored domain after the retry %+v", d)
	}
	if n := domainRows(); n != 1 {
		t.Errorf("%d domain rows after the retry, want 1", n)
	}
}

// A failed certificate read says nothing about the certificate: the last
// date stands, the failure is stored apart, no row is written, and it is
// tried again after dailyRetry. The domain check keeps its own schedule.
func TestRunnerKeepsCertOnFailedRead(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	origin := "https://" + addr

	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	dayOld := now.Add(-25 * time.Hour).Format(time.RFC3339)
	fresh := now.Format(time.RFC3339)
	st := &fakeStore{monitors: []store.Monitor{
		{ProjectID: 5, Enabled: true, Origin: origin, CertExpiresAt: "2027-03-14T00:00:00Z", CertIssuer: "R3",
			CertValid: true, CertCheckedAt: dayOld},
		{ProjectID: 6, Enabled: true, Origin: origin}, // no certificate read yet
	}, domains: map[domainKey]store.DomainState{{5, "127.0.0.1"}: {CheckedAt: fresh}, {6, "127.0.0.1"}: {CheckedAt: fresh}}}
	lg := &fakeLog{}
	var mu sync.Mutex
	r := newRunner(st, allowAll{}, lg, 60*time.Second, quietLogger(), options{
		allowLoopback: true, bootstrapURL: "http://127.0.0.1:1/dns.json",
		now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
	})
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		at := now
		mu.Unlock()
		r.tick(context.Background(), at.Add(-time.Minute), at)
		waitIdle(t, r, 0)
		r.tick(context.Background(), at, at)
	}
	certRows := func() (n int) {
		for _, e := range lg.all() {
			if e.EventName == "cert" {
				n++
			}
		}
		return n
	}

	advance(0)
	if st.certWrites != 0 {
		t.Errorf("a failed read stored readings: %+v", st.certs)
	}
	if st.monitors[0].CertCheckedAt != dayOld {
		t.Errorf("checked_at %q, want it left at %q", st.monitors[0].CertCheckedAt, dayOld)
	}
	if st.certErrs[5] == "" {
		t.Error("the failure did not reach the store")
	}
	if n := certRows(); n != 0 {
		t.Errorf("%d cert rows after a failed read, want 0", n)
	}

	delete(st.certErrs, 5)
	advance(30 * time.Minute) // within dailyRetry: not tried
	if st.certErrs[5] != "" {
		t.Error("tried again within the retry wait")
	}

	// The site comes back, on the same address.
	back, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("the port was taken: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Listener.Close()
	srv.Listener = back
	srv.Config.ErrorLog = discardLog()
	srv.StartTLS()
	defer srv.Close()

	advance(31 * time.Minute) // past dailyRetry: tried, and read
	if c := st.certs[5]; c.ExpiresAt == "" {
		t.Errorf("stored after the retry %+v", c)
	}
	if n := certRows(); n != 2 {
		t.Errorf("%d cert rows after the retry, want 2 (one per monitor)", n)
	}
	if d, _ := st.domain(6, "127.0.0.1"); d.CheckedAt != fresh {
		t.Error("a certificate never read dragged a fresh domain check along")
	}
}

// Enabling again reads the certificate and domain at once, whatever a
// recent attempt under the old monitor says.
func TestRunnerReenableChecksAtOnce(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer site.Close()
	rd, _ := fakeRDAP(t, nil)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	on := store.Monitor{ProjectID: 5, Enabled: true, Origin: site.URL}
	st := &fakeStore{monitors: []store.Monitor{on}}
	r := newRunner(st, allowAll{}, &fakeLog{}, 60*time.Second, quietLogger(), options{
		allowLoopback: true, bootstrapURL: rd.URL + "/dns.json", now: func() time.Time { return now },
	})
	ctx := context.Background()
	r.tick(ctx, now.Add(-time.Minute), now)
	waitIdle(t, r, 0)
	if st.certWrites != 1 {
		t.Fatalf("%d readings stored, want 1", st.certWrites)
	}
	st.monitors = nil // disabled
	r.tick(ctx, now, now.Add(time.Second))
	st.monitors = []store.Monitor{on} // enabled again: the store cleared the check times
	r.tick(ctx, now.Add(time.Second), now.Add(time.Minute))
	waitIdle(t, r, 0)
	if st.certWrites != 2 {
		t.Errorf("%d readings stored after enabling again, want 2", st.certWrites)
	}
}

func TestDue(t *testing.T) {
	iv := 30 * time.Second
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	off := 7 * time.Second
	if !due(base.Add(6*time.Second), base.Add(7*time.Second), iv, off) {
		t.Error("slot at the end of the window not due")
	}
	if due(base.Add(7*time.Second), base.Add(8*time.Second), iv, off) {
		t.Error("slot at the start of the window due (counted twice)")
	}
	if due(base.Add(8*time.Second), base.Add(36*time.Second), iv, off) {
		t.Error("no slot in (8s,36s] but due")
	}
	if !due(base.Add(8*time.Second), base.Add(37*time.Second), iv, off) {
		t.Error("slot at 37s not due")
	}
	for id := int64(0); id < 100; id++ {
		if o := offset(id, iv); o < 0 || o >= iv {
			t.Fatalf("offset %v outside [0,%v)", o, iv)
		}
	}
}

func TestRunnerDomainRow(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer site.Close()
	rd, _ := fakeRDAP(t, map[string]string{
		"example.com": `{"events":[{"eventAction":"expiration","eventDate":"2027-03-14T04:00:00Z"}]}`,
	})
	_, port, _ := net.SplitHostPort(site.Listener.Addr().String())
	origin := "http://shop.example.com:" + port
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 4, Enabled: true, Origin: origin}}}
	lg := &fakeLog{}
	r := newRunner(st, allowAll{}, lg, 60*time.Second, quietLogger(), options{
		resolver: fakeResolver{"shop.example.com": {"127.0.0.1"}}, allowLoopback: true,
		bootstrapURL: rd.URL + "/dns.json", now: func() time.Time { return now },
	})
	r.tick(context.Background(), now.Add(-time.Minute), now)
	waitIdle(t, r, 0)
	r.tick(context.Background(), now, now)
	var names []string
	for _, e := range lg.all() {
		names = append(names, e.EventName)
		if e.EventName == "domain" {
			if e.Attributes["domain"] != "example.com" || e.Attributes["expires"] != "2027-03-14" || e.Value == nil || *e.Value != 133 {
				t.Errorf("domain row %+v value %v", e.Attributes, e.Value)
			}
		}
		if e.EventName == "uptime" && e.Attributes["up"] != "1" {
			t.Errorf("uptime row %+v", e.Attributes)
		}
	}
	if len(names) != 3 {
		t.Errorf("rows %v, want uptime, an empty cert (http has none) and domain", names)
	}
	if d, _ := st.domain(4, "example.com"); d.ExpiresAt != "2027-03-14" {
		t.Errorf("stored domain %+v", d)
	}
	if c := st.certs[4]; c.CheckedAt == "" || c.ExpiresAt != "" {
		t.Errorf("stored cert for http origin %+v", c)
	}
}

// hopServer redirects n times (/?n=0 .. /?n=n-1) and then answers.
func hopServer(n int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var i int
		fmt.Sscanf(r.URL.Query().Get("n"), "%d", &i)
		if i < n {
			http.Redirect(w, r, fmt.Sprintf("/?n=%d", i+1), http.StatusFound)
			return
		}
		fmt.Fprint(w, "ok")
	}))
}

func redirectsRunner(st *fakeStore) *Runner {
	return newRunner(st, allowAll{}, &fakeLog{}, 60*time.Second, quietLogger(), options{allowLoopback: true})
}

func TestUptimeRowCarriesRedirects(t *testing.T) {
	srv := hopServer(3)
	defer srv.Close()
	st := &fakeStore{}
	r := redirectsRunner(st)
	m := store.Monitor{ProjectID: 1, Enabled: true, Origin: srv.URL}
	r.now = func() time.Time { return time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC) }
	evs := r.runSlot(context.Background(), m)
	if got := evs[0].Attributes["redirects"]; got != "3" {
		t.Errorf("redirects %q, want 3", got)
	}
	plain := hopServer(0)
	defer plain.Close()
	m.Origin = plain.URL
	if got := r.runSlot(context.Background(), m)[0].Attributes["redirects"]; got != "0" {
		t.Errorf("redirects %q, want 0", got)
	}
}

func TestRedirectsStoredOnlyOnChange(t *testing.T) {
	srv := hopServer(3)
	defer srv.Close()
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 1, Enabled: true, Origin: srv.URL}}}
	r := redirectsRunner(st)
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	run := func() { r.runSlot(context.Background(), st.monitors[0]) }
	run()
	if st.redirects[1] != 3 || st.redirectWrites != 1 {
		t.Fatalf("stored %v after %d writes, want 3 after 1", st.redirects, st.redirectWrites)
	}
	run()
	run()
	if st.redirectWrites != 1 {
		t.Errorf("%d writes after unchanged checks, want 1", st.redirectWrites)
	}
}

func TestRedirectsKeptThroughADialFailure(t *testing.T) {
	srv := hopServer(0)
	url := srv.URL
	srv.Close() // nothing listens: connection refused
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 1, Enabled: true, Origin: url, Redirects: 4}}}
	r := redirectsRunner(st)
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	evs := r.runSlot(context.Background(), st.monitors[0])
	if evs[0].Attributes["up"] != "0" || evs[0].Attributes["redirects"] != "0" {
		t.Errorf("row %+v, want down with redirects 0", evs[0].Attributes)
	}
	if st.redirectWrites != 0 || st.monitors[0].Redirects != 4 {
		t.Errorf("%d writes, stored %d: a dial failure must leave the count", st.redirectWrites, st.monitors[0].Redirects)
	}
}

// tickOnce ticks with no slot due, waits for what started, and flushes.
func tickOnce(t *testing.T, r *Runner, now time.Time) {
	t.Helper()
	r.tick(context.Background(), now, now)
	waitIdle(t, r, 0)
	r.tick(context.Background(), now, now)
}

func namesOf(evs []store.Event) []string {
	var names []string
	for _, e := range evs {
		names = append(names, e.EventName)
	}
	slices.Sort(names)
	return names
}

// TestManualCheckRunsAllThree: a request runs the availability, certificate
// and domain checks at once, outside the slot and although the daily readings
// are fresh, and records the availability result; it runs once.
func TestManualCheckRunsAllThree(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "ok")
	}))
	srv.Config.ErrorLog = discardLog()
	defer srv.Close()
	rd, _ := fakeRDAP(t, nil)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	const domain = "127.0.0.1"
	m := store.Monitor{ProjectID: 1, Enabled: true, Origin: srv.URL, CertCheckedAt: fresh, CheckRequestedAt: now.Add(-time.Second).Format(time.RFC3339)}
	st := &fakeStore{monitors: []store.Monitor{m}, domains: map[domainKey]store.DomainState{{1, domain}: {CheckedAt: fresh}}}
	lg := &fakeLog{}
	r := newRunner(st, allowAll{}, lg, time.Hour, quietLogger(), options{
		allowLoopback: true, roots: pool, bootstrapURL: rd.URL + "/dns.json", now: func() time.Time { return now },
	})
	// A daily retry wait does not hold it back either.
	r.tried[certKey(m)] = now
	r.tried[readKey{1, "domain", domain}] = now
	tickOnce(t, r, now)
	if got := namesOf(lg.all()); !slices.Equal(got, []string{"cert", "domain", "uptime"}) {
		t.Errorf("rows = %v, want all three", got)
	}
	if d, _ := st.domain(1, domain); st.certWrites != 1 || d.CheckedAt != now.Format(time.RFC3339) {
		t.Errorf("readings not stored: %d cert writes, domain %+v", st.certWrites, d)
	}
	res := st.results(1, srv.URL)
	if len(res) != 1 {
		t.Fatalf("%d results, want 1", len(res))
	}
	if !res[0].Up || res[0].Status != 200 || res[0].Reason != "" || res[0].Error != "" || res[0].FinishedAt != "2026-11-01T10:00:00Z" {
		t.Errorf("result = %+v", res[0])
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests to the site, want 1", hits.Load())
	}
	// It does not run again.
	tickOnce(t, r, now.Add(time.Second))
	if n := len(st.results(1, srv.URL)); n != 1 || hits.Load() != 1 {
		t.Errorf("ran again: %d results, %d requests", n, hits.Load())
	}
}

// slotClock serves a project's slots one by one, whatever its offset: a tick per
// whole interval, each given the interval's span, so one slot falls in each.
type slotClock struct {
	mu       sync.Mutex
	at       time.Time
	interval time.Duration
}

func newSlotClock(interval time.Duration) *slotClock {
	return &slotClock{at: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), interval: interval}
}

func (c *slotClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *slotClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// tick moves the clock an interval on and ticks over it.
func (c *slotClock) tick(r *Runner) {
	prev := c.now()
	c.advance(c.interval)
	r.tick(context.Background(), prev, c.now())
}

func (c *slotClock) next(t *testing.T, r *Runner) {
	t.Helper()
	c.tick(r)
	waitIdle(t, r, 0)
}

// recordJob makes r's slots record which origin each checked, by project.
func recordJob(r *Runner) func(id int64) []string {
	var mu sync.Mutex
	got := map[int64][]string{}
	r.job = func(_ context.Context, m store.Monitor) []store.Event {
		mu.Lock()
		defer mu.Unlock()
		got[m.ProjectID] = append(got[m.ProjectID], m.Origin)
		return nil
	}
	return func(id int64) []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got[id])
	}
}

func slotRunner(st *fakeStore, interval time.Duration) (*Runner, *slotClock) {
	c := newSlotClock(interval)
	r := noReads(newRunner(st, allowAll{}, &fakeLog{}, interval, quietLogger(), options{now: c.now}))
	return r, c
}

func on(id int64, origin string) store.Monitor {
	return store.Monitor{ProjectID: id, Enabled: true, Origin: origin}
}

const (
	originA = "https://a.example.com"
	originB = "https://b.example.com"
	originC = "https://c.example.com"
)

func TestOneMonitorIsCheckedEveryInterval(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{on(1, originA)}}
	r, c := slotRunner(st, time.Minute)
	checks := recordJob(r)
	for range 3 {
		c.next(t, r)
	}
	if got := checks(1); !slices.Equal(got, []string{originA, originA, originA}) {
		t.Errorf("checks = %v", got)
	}
}

// Three monitors take the project's slots in turn, so each comes round every 3
// intervals and the project still makes one check per interval.
func TestMonitorsShareTheSlotInTurn(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{on(1, originA), on(1, originB), on(1, originC)}}
	r, c := slotRunner(st, time.Minute)
	checks := recordJob(r)
	for range 7 {
		c.next(t, r)
	}
	want := []string{originA, originB, originC, originA, originB, originC, originA}
	if got := checks(1); !slices.Equal(got, want) {
		t.Errorf("checks = %v, want %v", got, want)
	}
}

// A project's rows per interval do not grow with its monitors.
func TestRowsPerIntervalDoNotGrowWithMonitors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	resolver := fakeResolver{}
	var ms []store.Monitor
	for _, host := range []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com"} {
		resolver[host] = []string{"127.0.0.1"}
		ms = append(ms, on(1, "http://"+host+":"+port))
	}
	st := &fakeStore{monitors: ms}
	lg := &fakeLog{}
	c := newSlotClock(time.Minute)
	r := noReads(newRunner(st, allowAll{}, lg, time.Minute, quietLogger(), options{resolver: resolver, allowLoopback: true, now: c.now}))
	for range 10 {
		c.next(t, r)
	}
	tickOnce(t, r, c.now())
	rows := lg.all()
	if len(rows) != 10 {
		t.Fatalf("%d rows in 10 intervals, want 10", len(rows))
	}
	seen := map[string]int{}
	for _, e := range rows {
		seen[e.Attributes["origin"]]++
	}
	if len(seen) != 5 {
		t.Errorf("rows by origin = %v, want all five, twice each", seen)
	}
	for o, n := range seen {
		if n != 2 {
			t.Errorf("%s: %d rows, want 2", o, n)
		}
	}
}

func TestDisabledMonitorDropsOutOfTheTurn(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{on(1, originA), on(1, originB), on(1, originC)}}
	r, c := slotRunner(st, time.Minute)
	checks := recordJob(r)
	c.next(t, r) // a
	c.next(t, r) // b
	st.setEnabled(on(1, originA), on(1, originC))
	c.next(t, r) // c, never tried
	c.next(t, r) // a, the oldest
	c.next(t, r) // c
	// One enabled again has lost its place: never tried, it goes first.
	st.setEnabled(on(1, originA), on(1, originB), on(1, originC))
	c.next(t, r) // b
	c.next(t, r) // a
	want := []string{originA, originB, originC, originA, originC, originB, originA}
	if got := checks(1); !slices.Equal(got, want) {
		t.Errorf("checks = %v, want %v", got, want)
	}
}

func TestProjectsDoNotShareABudget(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{on(1, originA), on(1, originB), on(1, originC), on(2, originA)}}
	r, c := slotRunner(st, time.Minute)
	checks := recordJob(r)
	for range 3 {
		c.next(t, r)
	}
	if got := checks(1); !slices.Equal(got, []string{originA, originB, originC}) {
		t.Errorf("project 1: %v", got)
	}
	if got := checks(2); !slices.Equal(got, []string{originA, originA, originA}) {
		t.Errorf("project 2: %v, want its one monitor every interval", got)
	}
}

// A project whose last check is still running skips the slot, whichever monitor
// was next; its turn is not lost.
func TestRunningCheckSkipsTheProjectsSlot(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{on(1, originA), on(1, originB)}}
	r, c := slotRunner(st, time.Minute)
	release := make(chan struct{})
	var mu sync.Mutex
	var got []string
	r.job = func(_ context.Context, m store.Monitor) []store.Event {
		mu.Lock()
		got = append(got, m.Origin)
		mu.Unlock()
		if m.Origin == originA {
			<-release
		}
		return nil
	}
	c.tick(r)
	for r.inflight() == 0 {
		time.Sleep(time.Millisecond)
	}
	c.tick(r)
	c.tick(r)
	close(release)
	waitIdle(t, r, 0)
	c.next(t, r)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(got, []string{originA, originB}) {
		t.Errorf("checks = %v, want a, then b once a finished", got)
	}
}

// Daily reads stuck on their pool hold no availability check back, in this
// project or another, however many there are, and at most readWorkers run.
func TestDailyReadsDoNotDelayAvailability(t *testing.T) {
	var ms []store.Monitor
	for i := range 30 {
		ms = append(ms, on(1, fmt.Sprintf("https://s%02d.example%d.com", i, i)))
	}
	ms = append(ms, on(2, originA))
	st := &fakeStore{monitors: ms}
	c := newSlotClock(time.Minute)
	r := newRunner(st, allowAll{}, &fakeLog{}, time.Minute, quietLogger(), options{now: c.now})
	checks := recordJob(r)
	release := make(chan struct{})
	var active, peak, reads atomic.Int32
	read := func() []store.Event {
		reads.Add(1)
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		active.Add(-1)
		return nil
	}
	r.readCert = func(context.Context, store.Monitor, time.Time) []store.Event { return read() }
	r.readDomain = func(context.Context, int64, string, time.Time) []store.Event { return read() }
	for range 3 {
		c.tick(r)
		// Every availability check of the slot ends while the reads stay stuck.
		deadline := time.Now().Add(5 * time.Second)
		for {
			r.mu.Lock()
			n := len(r.slots)
			r.mu.Unlock()
			if n == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("an availability check is stuck behind the reads")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if got := checks(1); len(got) != 3 || got[0] == got[1] {
		t.Errorf("project 1's checks = %v, want one per slot, in turn", got)
	}
	if got := checks(2); len(got) != 3 {
		t.Errorf("project 2's checks = %v, want one per slot", got)
	}
	for reads.Load() < readWorkers {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if n := reads.Load(); n != readWorkers {
		t.Errorf("%d reads started on a stuck pool, want %d", n, readWorkers)
	}
	close(release)
	waitIdle(t, r, 0)
	// 31 certificates and 31 domains (each origin here on one of its own, but
	// project 2's), each read once though they stayed due through three slots.
	if n := reads.Load(); n != 62 {
		t.Errorf("%d reads, want 62", n)
	}
	if n := peak.Load(); n != readWorkers {
		t.Errorf("peak %d reads at once, want %d", n, readWorkers)
	}
}

func TestACertificateReadPerOrigin(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{on(1, "http://a.example.com"), on(1, "http://b.example.com"), on(2, "http://a.example.com")}}
	c := newSlotClock(time.Minute)
	r := newRunner(st, allowAll{}, &fakeLog{}, time.Minute, quietLogger(), options{now: c.now})
	r.job = func(context.Context, store.Monitor) []store.Event { return nil }
	r.readDomain = func(context.Context, int64, string, time.Time) []store.Event { return nil }
	c.next(t, r)
	if st.certWrites != 3 {
		t.Fatalf("%d certificate readings, want one per origin per project: %v", st.certWrites, st.certOrigins)
	}
	// A day's wait, not one for each of a project's slots.
	c.next(t, r)
	c.next(t, r)
	if st.certWrites != 3 {
		t.Errorf("%d certificate readings after two more slots, want 3", st.certWrites)
	}
	c.advance(dailyEvery)
	c.next(t, r)
	if st.certWrites != 6 {
		t.Errorf("%d certificate readings a day later, want 6", st.certWrites)
	}
}

// Ten subdomains share one lookup and one date; another project's are its own.
func TestTenSubdomainsMakeOneDomainQuery(t *testing.T) {
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dns.json" {
			fmt.Fprintf(w, `{"services":[[["com"],["%s/rdap/"]]]}`, srv.URL)
			return
		}
		hits.Add(1)
		fmt.Fprint(w, `{"events":[{"eventAction":"expiration","eventDate":"2028-05-05T00:00:00Z"}]}`)
	}))
	defer srv.Close()
	var ms []store.Monitor
	for i := range 10 {
		ms = append(ms, on(1, fmt.Sprintf("https://s%d.example.com", i)))
	}
	ms = append(ms, on(1, "https://example.com"))
	st := &fakeStore{monitors: ms}
	lg := &fakeLog{}
	c := newSlotClock(time.Minute)
	r := newRunner(st, allowAll{}, lg, time.Minute, quietLogger(), options{allowLoopback: true, bootstrapURL: srv.URL + "/dns.json", now: c.now})
	r.job = func(context.Context, store.Monitor) []store.Event { return nil }
	r.readCert = func(context.Context, store.Monitor, time.Time) []store.Event { return nil }
	for range 3 {
		c.next(t, r)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("%d RDAP queries, want 1", n)
	}
	if d, _ := st.domain(1, "example.com"); d.ExpiresAt != "2028-05-05" {
		t.Errorf("stored %+v", d)
	}
	tickOnce(t, r, c.now())
	rows := 0
	for _, e := range lg.all() {
		if e.EventName == "domain" {
			rows++
			if e.Attributes["domain"] != "example.com" {
				t.Errorf("domain row %+v", e.Attributes)
			}
		}
	}
	if rows != 1 {
		t.Errorf("%d domain rows, want 1", rows)
	}
	// Enabling another subdomain finds the reading fresh.
	st.setEnabled(append(slices.Clone(st.monitors), on(1, "https://new.example.com"))...)
	c.next(t, r)
	if n := hits.Load(); n != 1 {
		t.Errorf("%d RDAP queries after enabling a subdomain, want 1", n)
	}
	// A day later: one again.
	c.advance(dailyEvery)
	c.next(t, r)
	if n := hits.Load(); n != 2 {
		t.Errorf("%d RDAP queries a day later, want 2", n)
	}
	// Another project's domain is its own reading.
	st.setEnabled(on(2, "https://example.com"))
	c.next(t, r)
	if n := hits.Load(); n != 3 {
		t.Errorf("%d RDAP queries with a second project, want 3", n)
	}
}

// A main domain whose last monitor was switched off has lost its retry wait:
// enabling one again reads at once.
func TestDomainRetryWaitForgottenWithItsLastMonitor(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{on(1, originA)}}
	c := newSlotClock(time.Minute)
	r := newRunner(st, allowAll{}, &fakeLog{}, time.Minute, quietLogger(), options{now: c.now})
	r.job = func(context.Context, store.Monitor) []store.Event { return nil }
	r.readCert = func(context.Context, store.Monitor, time.Time) []store.Event { return nil }
	var reads atomic.Int32
	r.readDomain = func(context.Context, int64, string, time.Time) []store.Event { // reads nothing: stays due
		reads.Add(1)
		return nil
	}
	c.next(t, r)
	c.next(t, r)
	if n := reads.Load(); n != 1 {
		t.Fatalf("%d reads within the retry wait, want 1", n)
	}
	// Another origin on the domain takes over: the wait stands.
	st.setEnabled(on(1, originB))
	c.next(t, r)
	if n := reads.Load(); n != 1 {
		t.Fatalf("%d reads with another origin on it, want 1", n)
	}
	st.setEnabled()
	c.next(t, r)
	st.setEnabled(on(1, originA))
	c.next(t, r)
	if n := reads.Load(); n != 2 {
		t.Errorf("%d reads after enabling again, want 2", n)
	}
}

// A manual check neither uses nor moves the slot: the project's next slot still
// comes, and it goes to the monitor whose turn is next.
func TestManualCheckLeavesTheSlotAlone(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{on(1, originA), on(1, originB), on(1, originC)}}
	r, c := slotRunner(st, time.Hour)
	checks := recordJob(r)
	var manual sync.Map
	r.check = func(_ context.Context, m store.Monitor) ([]store.Event, Result) {
		manual.Store(m.Origin, true)
		return nil, Result{Up: true}
	}
	c.next(t, r) // a
	// A request on c, with no slot in sight: it runs now, a slot's check not.
	st.mu.Lock()
	st.monitors[2].CheckRequestedAt = c.now().Format(time.RFC3339)
	st.mu.Unlock()
	tickOnce(t, r, c.now())
	if _, ok := manual.Load(originC); !ok {
		t.Fatal("the manual check did not run")
	}
	if got := checks(1); !slices.Equal(got, []string{originA}) {
		t.Fatalf("slots = %v, want the first alone", got)
	}
	// It counts as c's attempt: the turn goes to b, then a, then c.
	c.next(t, r)
	c.next(t, r)
	c.next(t, r)
	want := []string{originA, originB, originA, originC}
	if got := checks(1); !slices.Equal(got, want) {
		t.Errorf("slots = %v, want %v", got, want)
	}
}

// A manual check's availability, on a monitor with a project slot running, does
// not wait for it.
func TestManualCheckDoesNotWaitForTheProjectsSlot(t *testing.T) {
	st := &fakeStore{monitors: []store.Monitor{on(1, originA), on(1, originB)}}
	r, c := slotRunner(st, time.Minute)
	release := make(chan struct{})
	r.job = func(context.Context, store.Monitor) []store.Event { <-release; return nil }
	checked := make(chan string, 1)
	r.check = func(_ context.Context, m store.Monitor) ([]store.Event, Result) {
		checked <- m.Origin
		return nil, Result{Up: true}
	}
	c.tick(r) // a's slot, which blocks
	st.mu.Lock()
	st.monitors[1].CheckRequestedAt = c.now().Format(time.RFC3339)
	st.mu.Unlock()
	r.tick(context.Background(), c.now(), c.now())
	select {
	case o := <-checked:
		if o != originB {
			t.Errorf("checked %s", o)
		}
	case <-time.After(5 * time.Second):
		t.Error("the manual check waited for the slot")
	}
	close(release)
	waitIdle(t, r, 0)
}

// TestManualCheckStandsInForTheSlot: a request and a due slot in one tick on a
// project's one monitor are one check, not two.
func TestManualCheckStandsInForTheSlot(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	rd, _ := fakeRDAP(t, nil)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 1, Enabled: true, Origin: srv.URL, CheckRequestedAt: now.Format(time.RFC3339)}}}
	r := newRunner(st, allowAll{}, &fakeLog{}, 60*time.Second, quietLogger(), options{allowLoopback: true, bootstrapURL: rd.URL + "/dns.json", now: func() time.Time { return now }})
	r.tick(context.Background(), now.Add(-time.Minute), now) // the slot is due
	waitIdle(t, r, 0)
	if hits.Load() != 1 || len(st.results(1, srv.URL)) != 1 {
		t.Errorf("%d requests, %d results, want one of each", hits.Load(), len(st.results(1, srv.URL)))
	}
}

// TestManualCheckWaitsForItsOwnRun: a request found while that monitor's manual
// check is running waits for the next tick, however old it gets meanwhile.
func TestManualCheckWaitsForItsOwnRun(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	const origin = "https://x.example"
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 1, Enabled: true, Origin: origin, CheckRequestedAt: now.Format(time.RFC3339)}}}
	r := noReads(newRunner(st, allowAll{}, &fakeLog{}, time.Hour, quietLogger(), options{now: func() time.Time { return now }}))
	release := make(chan struct{})
	var checks atomic.Int32
	r.check = func(context.Context, store.Monitor) ([]store.Event, Result) {
		checks.Add(1)
		<-release
		return nil, Result{Up: true, Status: 200}
	}
	ctx := context.Background()
	r.tick(ctx, now, now) // it starts, and blocks
	for checks.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	r.tick(ctx, now, now.Add(time.Second))
	r.tick(ctx, now, now.Add(3*time.Minute)) // too old by now, but not to be judged while busy
	if checks.Load() != 1 || len(st.results(1, origin)) != 0 {
		t.Fatalf("%d checks, results %+v while busy", checks.Load(), st.results(1, origin))
	}
	close(release)
	waitIdle(t, r, 0)
	if res := st.results(1, origin); len(res) != 1 || !res[0].Up {
		t.Fatalf("results %+v after it finished", res)
	}
}

// TestManualCheckNotRepeatedByAStaleList: a check that finishes while the
// monitors are being listed still reads as pending in that list.
func TestManualCheckNotRepeatedByAStaleList(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 1, Enabled: true, Origin: "https://x.example", CheckRequestedAt: now.Format(time.RFC3339)}}}
	r := noReads(newRunner(st, allowAll{}, &fakeLog{}, time.Hour, quietLogger(), options{now: func() time.Time { return now }}))
	release := make(chan struct{})
	var checks atomic.Int32
	r.check = func(context.Context, store.Monitor) ([]store.Event, Result) {
		checks.Add(1)
		<-release
		return nil, Result{Up: true}
	}
	ctx := context.Background()
	r.tick(ctx, now, now)
	for checks.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	st.onList = func() { // the list above is read; the check finishes now
		close(release)
		waitIdle(t, r, 0)
	}
	r.tick(ctx, now, now)
	st.onList = nil
	waitIdle(t, r, 0)
	if checks.Load() != 1 {
		t.Errorf("%d checks, want 1", checks.Load())
	}
}

// TestManualCheckFailedReadsKeepTheDates: a certificate or RDAP read that fails
// keeps the last dates and writes no row, and the check still finishes, with
// the availability result.
func TestManualCheckFailedReadsKeepTheDates(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://" + ln.Addr().String()
	ln.Close()
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 1, Enabled: true, Origin: origin,
		CertExpiresAt: "2027-03-14T00:00:00Z", CertCheckedAt: fresh, CheckRequestedAt: now.Format(time.RFC3339)}},
		domains: map[domainKey]store.DomainState{{1, "127.0.0.1"}: {ExpiresAt: "2027-05-01", CheckedAt: fresh}}}
	lg := &fakeLog{}
	r := newRunner(st, allowAll{}, lg, time.Hour, quietLogger(), options{allowLoopback: true, bootstrapURL: "http://127.0.0.1:1/dns.json", now: func() time.Time { return now }})
	tickOnce(t, r, now)
	if st.certWrites != 0 || st.certErrs[1] == "" {
		t.Errorf("cert writes %d, error %q: want none and a reason", st.certWrites, st.certErrs[1])
	}
	if got := namesOf(lg.all()); !slices.Contains(got, "uptime") || slices.Contains(got, "cert") {
		t.Errorf("rows = %v, want uptime and no cert", got)
	}
	res := st.results(1, origin)
	if len(res) != 1 || res[0].Up || res[0].Reason != ReasonConnect || res[0].Error == "" {
		t.Errorf("results = %+v, want one, down on connect", res)
	}
}

// TestManualCheckTooOldIsDropped: a request older than 2 minutes is finished
// as not run, without checking anything.
func TestManualCheckTooOldIsDropped(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	const origin = "https://x.example"
	st := &fakeStore{monitors: []store.Monitor{
		{ProjectID: 1, Enabled: true, Origin: origin, CheckRequestedAt: now.Add(-2*time.Minute - time.Second).Format(time.RFC3339)},
		{ProjectID: 2, Enabled: true, Origin: origin, CheckRequestedAt: now.Add(-2 * time.Minute).Format(time.RFC3339)},
	}}
	lg := &fakeLog{}
	r := noReads(newRunner(st, allowAll{}, lg, time.Hour, quietLogger(), options{now: func() time.Time { return now }}))
	var checked sync.Map
	r.check = func(_ context.Context, m store.Monitor) ([]store.Event, Result) {
		checked.Store(m.ProjectID, true)
		return nil, Result{Up: true}
	}
	tickOnce(t, r, now)
	if _, ok := checked.Load(int64(1)); ok {
		t.Error("the old request was checked")
	}
	if _, ok := checked.Load(int64(2)); !ok {
		t.Error("the request of exactly 2 minutes was not checked")
	}
	res := st.results(1, origin)
	want := store.MonitorCheckResult{FinishedAt: "2026-11-01T10:00:00Z", Error: "the check did not run"}
	if len(res) != 1 || res[0] != want {
		t.Errorf("results = %+v, want %+v", res, want)
	}
	if len(lg.all()) != 0 {
		t.Errorf("rows %v", lg.all())
	}
}

// TestManualCheckFinishesPastItsRequest: the page waits for finished_at to
// reach requested_at, so a clock that stepped back must not leave it pending.
func TestManualCheckFinishesPastItsRequest(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	req := now.Add(time.Minute).Format(time.RFC3339)
	const origin = "https://x.example"
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 1, Enabled: true, Origin: origin, CheckRequestedAt: req}}}
	r := noReads(newRunner(st, allowAll{}, &fakeLog{}, time.Hour, quietLogger(), options{now: func() time.Time { return now }}))
	r.check = func(context.Context, store.Monitor) ([]store.Event, Result) { return nil, Result{Up: true} }
	tickOnce(t, r, now)
	if res := st.results(1, origin); len(res) != 1 || res[0].FinishedAt != req {
		t.Errorf("results = %+v, want finished at %s", res, req)
	}
}

// TestManualCheckDuringShutdown: none starts once the runner is stopping, and
// one cut short says so, under a context of its own, its rows dropped.
func TestManualCheckDuringShutdown(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	const origin = "https://x.example"
	mon := store.Monitor{ProjectID: 1, Enabled: true, Origin: origin, CheckRequestedAt: now.Format(time.RFC3339)}
	t.Run("not started", func(t *testing.T) {
		st := &fakeStore{monitors: []store.Monitor{mon}}
		r := noReads(newRunner(st, allowAll{}, &fakeLog{}, time.Hour, quietLogger(), options{now: func() time.Time { return now }}))
		var checks atomic.Int32
		r.check = func(context.Context, store.Monitor) ([]store.Event, Result) { checks.Add(1); return nil, Result{} }
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r.tick(ctx, now, now)
		waitIdle(t, r, 0)
		if checks.Load() != 0 {
			t.Errorf("%d checks started during shutdown", checks.Load())
		}
	})
	t.Run("cut short", func(t *testing.T) {
		st := &fakeStore{monitors: []store.Monitor{mon}}
		lg := &fakeLog{}
		r := noReads(newRunner(st, allowAll{}, lg, time.Hour, quietLogger(), options{now: func() time.Time { return now }}))
		entered := make(chan struct{})
		r.check = func(ctx context.Context, m store.Monitor) ([]store.Event, Result) {
			close(entered)
			<-ctx.Done()
			return []store.Event{uptimeRow(m.ProjectID, m.Origin, now, Result{})}, Result{Reason: ReasonTimeout}
		}
		ctx, cancel := context.WithCancel(context.Background())
		r.tick(ctx, now, now)
		<-entered
		cancel()
		waitIdle(t, r, 0)
		res := st.results(1, origin)
		if len(res) != 1 || res[0].Error != "the check did not run" || res[0].Reason != "" {
			t.Errorf("results = %+v", res)
		}
		if st.finishCtxErr != nil {
			t.Errorf("finished under a cancelled context: %v", st.finishCtxErr)
		}
		r.flush(context.Background())
		if len(lg.all()) != 0 {
			t.Errorf("rows kept: %v", lg.all())
		}
	})
	// Reads queued when shutdown comes never start, and write nothing.
	t.Run("queued reads", func(t *testing.T) {
		var ms []store.Monitor
		for i := range readWorkers + 3 {
			ms = append(ms, on(1, fmt.Sprintf("https://s%d.example.com", i)))
		}
		st := &fakeStore{monitors: ms}
		lg := &fakeLog{}
		r := newRunner(st, allowAll{}, lg, time.Minute, quietLogger(), options{now: func() time.Time { return now }})
		r.job = func(context.Context, store.Monitor) []store.Event { return nil }
		r.readDomain = func(context.Context, int64, string, time.Time) []store.Event { return nil }
		var started atomic.Int32
		r.readCert = func(ctx context.Context, m store.Monitor, at time.Time) []store.Event {
			started.Add(1)
			<-ctx.Done()
			return []store.Event{row(m.ProjectID, "cert", at, nil, nil)}
		}
		ctx, cancel := context.WithCancel(context.Background())
		r.tick(ctx, now.Add(-time.Minute), now)
		for started.Load() < readWorkers {
			time.Sleep(time.Millisecond)
		}
		cancel()
		waitIdle(t, r, 0)
		r.flush(context.Background())
		if len(lg.all()) != 0 {
			t.Errorf("rows kept: %v", lg.all())
		}
	})
}
