// Package monitor runs each project's availability monitor: every
// interval it GETs the monitor's origin and writes the result to the
// project's event log as a checks/uptime row; once a day (and right after
// the monitor is enabled or its origin changes) it reads the origin's
// certificate and its domain's registration expiry, keeps them on the
// monitor row and writes them as checks/cert and checks/domain rows. A read
// that fails says nothing about either: the last reading stands, unrecorded,
// and is tried again after an hour.
//
// Every connection it makes, redirect hops and RDAP lookups included, goes
// through a dialer that refuses private addresses.
package monitor

import (
	"context"
	"crypto/x509"
	"errors"
	"hash/fnv"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Store is the slice of the store the monitor reads and writes.
type Store interface {
	ListEnabledMonitors(ctx context.Context) ([]store.Monitor, error)
	SetMonitorCert(ctx context.Context, projectID int64, origin string, c store.CertState) error
	SetMonitorCertError(ctx context.Context, projectID int64, origin, msg string) error
	SetMonitorDomain(ctx context.Context, projectID int64, origin string, d store.DomainState) error
	SetMonitorRedirects(ctx context.Context, projectID int64, origin string, n int) error
}

// EventLog is where check rows go (archive.Log).
type EventLog interface {
	WriteEvents(ctx context.Context, evs []store.Event) error
}

// Origins says whether a redirect target is one of the project's origins
// (manage's snapshot, MonitorRedirectAllowed; a bare "*" counts only for
// reqOrigin, which is the monitor's own origin).
type Origins interface {
	RedirectAllowed(projectID int64, target, reqOrigin string) bool
}

const (
	workers        = 16              // checks running at once, across projects
	defaultTimeout = 5 * time.Second // a check's limit when none is configured
	rdapTimeout    = 15 * time.Second
	dailyEvery     = 24 * time.Hour
	dailyRetry     = time.Hour // after a daily check whose result did not stick
	tickStep       = time.Second
)

// Runner schedules the checks. Each monitor has a fixed offset within the
// interval, from its project id, so checks spread across it; a monitor
// whose previous check is still running skips its slot.
type Runner struct {
	st       Store
	origins  Origins
	log      EventLog
	logger   *slog.Logger
	interval time.Duration
	timeout  time.Duration
	step     time.Duration
	now      func() time.Time

	guard  *guard
	roots  *x509.CertPool
	client *http.Client
	rdap   *rdap

	// job runs one monitor's slot and returns its rows; a seam for tests.
	job func(ctx context.Context, m store.Monitor) []store.Event

	sem     chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	running map[int64]bool
	pending []store.Event
	tried   map[int64]attempt
}

// attempt is the last daily check started for an enabled monitor, so one
// that read nothing is not retried every slot. A monitor switched off
// loses it, so enabling it again checks at once.
type attempt struct {
	origin string
	at     time.Time
}

// options are the test seams.
type options struct {
	resolver      resolver
	allowLoopback bool
	roots         *x509.CertPool
	bootstrapURL  string
	timeout       time.Duration // MONITOR_TIMEOUT; 0: defaultTimeout
	now           func() time.Time
}

// New builds a Runner checking every interval, each check (the availability
// request and the certificate read) given timeout (config's MONITOR_TIMEOUT,
// below interval).
func New(st Store, origins Origins, log EventLog, interval, timeout time.Duration, logger *slog.Logger) *Runner {
	return newRunner(st, origins, log, interval, logger, options{timeout: timeout})
}

func newRunner(st Store, origins Origins, log EventLog, interval time.Duration, logger *slog.Logger, o options) *Runner {
	if o.resolver == nil {
		o.resolver = net.DefaultResolver
	}
	if o.bootstrapURL == "" {
		o.bootstrapURL = ianaBootstrap
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.timeout <= 0 {
		o.timeout = defaultTimeout
	}
	g := &guard{resolver: o.resolver, allowLoopback: o.allowLoopback, budget: o.timeout}
	client := newClient(g, o.roots)
	r := &Runner{
		st: st, origins: origins, log: log, logger: logger,
		interval: interval, timeout: o.timeout, step: tickStep, now: o.now,
		guard: g, roots: o.roots, client: client,
		rdap:    &rdap{client: newClient(&guard{resolver: o.resolver, allowLoopback: o.allowLoopback, budget: rdapTimeout}, o.roots), bootstrapURL: o.bootstrapURL, now: o.now},
		sem:     make(chan struct{}, workers),
		running: map[int64]bool{},
		tried:   map[int64]attempt{},
	}
	r.job = r.runMonitor
	return r
}

// Run checks until ctx is cancelled, then waits for the checks in flight
// (cut short by the cancel, so their rows are dropped) and writes what
// finished before.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(r.step)
	defer t.Stop()
	prev := r.now()
	for {
		select {
		case <-ctx.Done():
			r.wg.Wait()
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			r.flush(flushCtx)
			cancel()
			return
		case <-t.C:
			now := r.now()
			r.tick(ctx, prev, now)
			prev = now
		}
	}
}

// tick writes the rows finished since the last tick in one batch, then
// reloads the enabled monitors and starts each whose slot falls in
// (prev, now].
func (r *Runner) tick(ctx context.Context, prev, now time.Time) {
	r.flush(ctx)
	ms, err := r.st.ListEnabledMonitors(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("monitor: list monitors", "error", err)
		}
		return
	}
	r.forgetDisabled(ms)
	for _, m := range ms {
		if !due(prev, now, r.interval, offset(m.ProjectID, r.interval)) {
			continue
		}
		r.mu.Lock()
		busy := r.running[m.ProjectID]
		if !busy {
			r.running[m.ProjectID] = true
		}
		r.mu.Unlock()
		if busy {
			continue
		}
		r.wg.Add(1)
		go r.dispatch(ctx, m)
	}
}

// forgetDisabled drops the attempts of monitors no longer in enabled.
func (r *Runner) forgetDisabled(enabled []store.Monitor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.tried {
		if !slices.ContainsFunc(enabled, func(m store.Monitor) bool { return m.ProjectID == id }) {
			delete(r.tried, id)
		}
	}
}

func (r *Runner) dispatch(ctx context.Context, m store.Monitor) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		delete(r.running, m.ProjectID)
		r.mu.Unlock()
	}()
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	evs := r.job(ctx, m)
	<-r.sem
	if ctx.Err() != nil {
		return // cut short by shutdown: not a real result
	}
	r.mu.Lock()
	r.pending = append(r.pending, evs...)
	r.mu.Unlock()
}

// flush writes the pending rows. A failed write is logged and the rows
// dropped: a missed check is a gap, not a reason to stop checking.
func (r *Runner) flush(ctx context.Context) {
	r.mu.Lock()
	evs := r.pending
	r.pending = nil
	r.mu.Unlock()
	if len(evs) == 0 {
		return
	}
	if err := r.log.WriteEvents(ctx, evs); err != nil {
		r.logger.Error("monitor: write check rows", "error", err, "rows", len(evs))
	}
}

func (r *Runner) inflight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.running)
}

// offset is a project's fixed place within the interval, in whole ticks.
func offset(projectID int64, interval time.Duration) time.Duration {
	h := fnv.New64a()
	h.Write([]byte(strconv.FormatInt(projectID, 10)))
	slots := max(uint64(interval/tickStep), 1)
	return time.Duration(h.Sum64()%slots) * tickStep
}

// due reports whether a slot (a time ≡ off modulo interval, counted from
// the Unix epoch) falls in (prev, now].
func due(prev, now time.Time, interval, off time.Duration) bool {
	n, iv := now.UnixNano()-int64(off), int64(interval)
	k := n / iv
	if n < 0 && n%iv != 0 {
		k--
	}
	slot := k*iv + int64(off)
	return slot > prev.UnixNano()
}

// runMonitor is one slot: the availability check, then the daily checks
// when due.
func (r *Runner) runMonitor(ctx context.Context, m store.Monitor) []store.Event {
	start := r.now().UTC()
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	res := Check(cctx, r.client, m.Origin, m.Contains, func(target string) bool {
		return r.origins.RedirectAllowed(m.ProjectID, target, m.Origin)
	})
	cancel()
	// Keep the count Settings shows, written only when it moved and only
	// from a check that got an answer: a blip must not hide the warning.
	if res.Answered && res.Redirects != m.Redirects && ctx.Err() == nil {
		if err := r.st.SetMonitorRedirects(ctx, m.ProjectID, m.Origin, res.Redirects); err != nil {
			r.logger.Warn("monitor: store redirect count", "project_id", m.ProjectID, "error", err)
		}
	}
	evs := []store.Event{uptimeRow(m.ProjectID, start, res)}
	return append(evs, r.daily(ctx, m)...)
}

func uptimeRow(projectID int64, at time.Time, res Result) store.Event {
	attrs := map[string]string{"up": "0", "redirects": strconv.Itoa(res.Redirects)}
	if res.Up {
		attrs["up"] = "1"
	} else {
		attrs["reason"] = res.Reason
		attrs["error"] = res.Error
	}
	if res.Status > 0 {
		attrs["status"] = strconv.Itoa(res.Status)
	}
	ms := math.Round(float64(res.Elapsed) / float64(time.Millisecond))
	return row(projectID, "uptime", at, &ms, attrs)
}

// newID is a v7 UUID, a v4 one on entropy exhaustion: a worker must not
// panic for the sake of an id's order.
func newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func row(projectID int64, name string, at time.Time, value *float64, attrs map[string]string) store.Event {
	return store.Event{
		ID: newID(), ProjectID: projectID, Family: store.FamilyChecks,
		EventName: name, TS: at, ReceivedAt: at, Value: value, Attributes: attrs,
	}
}

// stale reports whether a reading taken at checkedAt (RFC 3339, "" never)
// is due again.
func stale(checkedAt string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, checkedAt)
	return err != nil || now.Sub(t) >= dailyEvery
}

// daily runs each of the certificate and domain checks when due: never
// read (an origin change or enabling the monitor clears its check time), or
// a day old.
func (r *Runner) daily(ctx context.Context, m store.Monitor) []store.Event {
	now := r.now().UTC()
	certDue := stale(m.CertCheckedAt, now)
	domainDue := stale(m.DomainCheckedAt, now)
	if !certDue && !domainDue {
		return nil
	}
	r.mu.Lock()
	last, ok := r.tried[m.ProjectID]
	if ok && last.origin == m.Origin && now.Sub(last.at) < dailyRetry {
		r.mu.Unlock()
		return nil
	}
	r.tried[m.ProjectID] = attempt{m.Origin, now}
	r.mu.Unlock()

	u, err := url.Parse(m.Origin)
	if err != nil {
		return nil
	}
	var evs []store.Event
	if certDue {
		evs = append(evs, r.cert(ctx, m, u.Scheme == "https", now)...)
	}
	if domainDue && ctx.Err() == nil {
		evs = append(evs, r.domain(ctx, m, u.Hostname(), now)...)
	}
	return evs
}

// cert reads and stores the certificate. An http:// origin has none: it
// stores only the check time and writes no row. A read that got no
// certificate keeps the last reading and its check time, stores only why,
// and writes no row; one that got a certificate is a reading, verified or
// not.
func (r *Runner) cert(ctx context.Context, m store.Monitor, https bool, now time.Time) []store.Event {
	st := store.CertState{CheckedAt: now.Format(time.RFC3339)}
	if https {
		cctx, cancel := context.WithTimeout(ctx, r.timeout)
		st = checkCert(cctx, r.guard, r.roots, m.Origin, now)
		cancel()
	}
	if ctx.Err() != nil {
		return nil
	}
	if https && st.ExpiresAt == "" {
		if err := r.st.SetMonitorCertError(ctx, m.ProjectID, m.Origin, st.Error); err != nil {
			r.logger.Warn("monitor: store certificate error", "project_id", m.ProjectID, "error", err)
		}
		return nil
	}
	if err := r.st.SetMonitorCert(ctx, m.ProjectID, m.Origin, st); err != nil {
		r.logger.Warn("monitor: store certificate", "project_id", m.ProjectID, "error", err)
	}
	if !https {
		return nil
	}
	attrs := map[string]string{"valid": "0", "issuer": st.Issuer, "error": st.Error}
	if st.Valid {
		attrs["valid"] = "1"
	}
	var value *float64
	if d, ok := daysLeft(st.ExpiresAt, now); ok {
		value = &d
		attrs["expires"] = st.ExpiresAt[:len(time.DateOnly)]
	}
	for k, v := range attrs {
		if v == "" {
			delete(attrs, k)
		}
	}
	return []store.Event{row(m.ProjectID, "cert", now, value, attrs)}
}

// domain reads and stores the domain's expiry. A domain RDAP cannot
// answer for is stored as unknown; a failed lookup keeps the last reading
// and writes no row.
func (r *Runner) domain(ctx context.Context, m store.Monitor, host string, now time.Time) []store.Event {
	dctx, cancel := context.WithTimeout(ctx, rdapTimeout)
	exp, err := r.rdap.expiry(dctx, host)
	cancel()
	if ctx.Err() != nil {
		return nil
	}
	switch {
	case errors.Is(err, errUnknown):
		r.logger.Debug("monitor: domain expiry unknown", "project_id", m.ProjectID, "host", host, "error", err)
	case err != nil:
		// The lookup failed, which says nothing about the domain: the last
		// reading stands, unrecorded, and is retried after dailyRetry.
		r.logger.Debug("monitor: domain expiry lookup failed", "project_id", m.ProjectID, "host", host, "error", err)
		return nil
	}
	if err := r.st.SetMonitorDomain(ctx, m.ProjectID, m.Origin, store.DomainState{ExpiresAt: exp, CheckedAt: now.Format(time.RFC3339)}); err != nil {
		r.logger.Warn("monitor: store domain expiry", "project_id", m.ProjectID, "error", err)
	}
	attrs := map[string]string{}
	var value *float64
	if d, ok := daysLeft(exp, now); ok {
		value = &d
		attrs["expires"] = exp
	}
	return []store.Event{row(m.ProjectID, "domain", now, value, attrs)}
}
