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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/google/uuid"
)

type fakeStore struct {
	mu       sync.Mutex
	monitors []store.Monitor
	certs    map[int64]store.CertState
	domains  map[int64]store.DomainState
	// certWrites counts every stored reading, certErrs the failures stored apart.
	certWrites int
	certErrs   map[int64]string
}

func (f *fakeStore) ListEnabledMonitors(context.Context) ([]store.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Monitor(nil), f.monitors...), nil
}

func (f *fakeStore) SetMonitorCert(_ context.Context, id int64, origin string, c store.CertState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.certs == nil {
		f.certs = map[int64]store.CertState{}
	}
	f.certs[id] = c
	f.certWrites++
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

func (f *fakeStore) SetMonitorDomain(_ context.Context, id int64, origin string, d store.DomainState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.domains == nil {
		f.domains = map[int64]store.DomainState{}
	}
	f.domains[id] = d
	for i := range f.monitors {
		if f.monitors[i].ProjectID == id && f.monitors[i].Origin == origin {
			f.monitors[i].DomainCheckedAt = d.CheckedAt
		}
	}
	return nil
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

// waitIdle waits until at most n monitors are still running.
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
	r := New(st, allowAll{}, &fakeLog{}, interval, quietLogger())
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
			running7 := r.running[7]
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
	r := New(st, allowAll{}, &fakeLog{}, 10*time.Second, quietLogger())
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
	if r.inflight() != 40 {
		t.Fatalf("%d dispatched, want 40", r.inflight())
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
		{ProjectID: 1, Enabled: true, Origin: up.URL, CertCheckedAt: fresh, DomainCheckedAt: fresh},
		{ProjectID: 2, Enabled: true, Origin: down.URL, CertCheckedAt: fresh, DomainCheckedAt: fresh},
		{ProjectID: 3, Enabled: true, Origin: tlsSrv.URL}, // just enabled: no cert_checked_at
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
	if a := e.Attributes; a["up"] != "1" || a["status"] != "200" || a["reason"] != "" || a["error"] != "" || e.Value == nil {
		t.Errorf("up row %+v value %v", a, e.Value)
	}
	e = byKey["2/uptime"]
	if a := e.Attributes; a["up"] != "0" || a["status"] != "404" || a["reason"] != "http_4xx" || a["error"] == "" {
		t.Errorf("down row %+v", a)
	}
	if a := byKey["3/uptime"].Attributes; a["up"] != "1" {
		t.Errorf("tls row %+v", a)
	}
	c := byKey["3/cert"]
	wantExp := tlsSrv.Certificate().NotAfter.UTC().Format("2006-01-02")
	if a := c.Attributes; a["expires"] != wantExp || a["valid"] != "1" || a["error"] != "" || c.Value == nil {
		t.Errorf("cert row %+v value %v", a, c.Value)
	}
	d := byKey["3/domain"]
	if _, ok := d.Attributes["expires"]; ok || d.Value != nil {
		t.Errorf("domain row for an address origin: %+v value %v, want unknown", d.Attributes, d.Value)
	}
	if cs := st.certs[3]; !cs.Valid || cs.CheckedAt != "2026-11-01T10:00:00Z" {
		t.Errorf("stored cert %+v", cs)
	}
	if ds, ok := st.domains[3]; !ok || ds.ExpiresAt != "" || ds.CheckedAt == "" {
		t.Errorf("stored domain %+v %v", ds, ok)
	}
	if _, ok := st.certs[1]; ok {
		t.Error("cert re-checked within a day")
	}

	// A day later both daily checks are due again on every monitor; an
	// http:// origin stores a check time but writes no cert row.
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
	var certRows int
	for _, e := range lg.all() {
		if e.EventName == "cert" {
			certRows++
		}
	}
	if certRows != 2 {
		t.Errorf("%d cert rows, want 2 (project 3 twice, none for http origins)", certRows)
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
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 9, Enabled: true, Origin: site.URL, CertCheckedAt: fresh, DomainCheckedAt: fresh}}}
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
	r := New(st, allowAll{}, lg, 10*time.Second, slog.New(slog.NewTextHandler(&out, nil)))
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
	r := New(st, allowAll{}, lg, 10*time.Second, quietLogger())
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
	r := New(&fakeStore{}, allowAll{}, lg, 10*time.Second, quietLogger())
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

func TestCheckLimit(t *testing.T) {
	for iv, want := range map[time.Duration]time.Duration{
		10 * time.Second: 9 * time.Second, 11 * time.Second: 10 * time.Second,
		30 * time.Second: 10 * time.Second, 3600 * time.Second: 10 * time.Second,
	} {
		if got := checkLimit(iv); got != want {
			t.Errorf("interval %v: limit %v, want %v", iv, got, want)
		}
	}
	if r := New(&fakeStore{}, allowAll{}, &fakeLog{}, 10*time.Second, quietLogger()); r.timeout != 9*time.Second {
		t.Errorf("runner at the 10 s floor: timeout %v, want 9s", r.timeout)
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
	st := &fakeStore{monitors: []store.Monitor{{ProjectID: 5, Enabled: true, Origin: origin,
		CertCheckedAt: dayOld, DomainExpiresAt: "2027-03-14", DomainCheckedAt: dayOld}}}
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
	if _, ok := st.domains[5]; ok {
		t.Errorf("a failed lookup stored %+v", st.domains[5])
	}
	if st.monitors[0].DomainExpiresAt != "2027-03-14" || st.monitors[0].DomainCheckedAt != dayOld {
		t.Errorf("monitor after a failed lookup %+v", st.monitors[0])
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
	if d := st.domains[5]; d.ExpiresAt != "2028-05-05" {
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
			CertValid: true, CertCheckedAt: dayOld, DomainCheckedAt: fresh},
		{ProjectID: 6, Enabled: true, Origin: origin, DomainCheckedAt: fresh}, // no certificate read yet
	}}
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
	if _, ok := st.domains[6]; ok {
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
			if e.Attributes["expires"] != "2027-03-14" || e.Value == nil || *e.Value != 133 {
				t.Errorf("domain row %+v value %v", e.Attributes, e.Value)
			}
		}
		if e.EventName == "uptime" && e.Attributes["up"] != "1" {
			t.Errorf("uptime row %+v", e.Attributes)
		}
	}
	if len(names) != 2 {
		t.Errorf("rows %v, want uptime and domain (no cert for http)", names)
	}
	if d := st.domains[4]; d.ExpiresAt != "2027-03-14" {
		t.Errorf("stored domain %+v", d)
	}
	if c := st.certs[4]; c.CheckedAt == "" || c.ExpiresAt != "" {
		t.Errorf("stored cert for http origin %+v", c)
	}
}
