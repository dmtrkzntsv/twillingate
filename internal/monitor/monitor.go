// Package monitor runs each project's availability monitor: every
// interval it GETs the monitor's origin and writes the result to the
// project's event log as a checks/uptime row; once a day (and right after
// the monitor is enabled or its origin changes) it reads the origin's
// certificate and its domain's registration expiry, keeps them on the
// monitor row and writes them as checks/cert and checks/domain rows. A read
// that fails says nothing about either: the last reading stands, unrecorded,
// and is tried again after an hour.
//
// A manual check is a request on the monitor row, since the API that takes it
// may be another process: the next tick runs all three outside the slot and
// records the availability result on the row.
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
	"maps"
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
	FinishMonitorCheck(ctx context.Context, projectID int64, origin string, r store.MonitorCheckResult) error
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
	// checkMaxAge is how long a manual check's request stays worth serving;
	// past it the page has long stopped waiting.
	checkMaxAge = 2 * time.Minute
	// finishTimeout bounds recording a manual check's end, on a context of its
	// own so that a shutdown does not leave it pending.
	finishTimeout = 5 * time.Second
	checkNotRun   = "the check did not run"
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
	// check runs one manual check and returns its rows and result; likewise.
	check func(ctx context.Context, m store.Monitor) ([]store.Event, Result)

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
	r.check = r.runCheck
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
// reloads the enabled monitors and starts each with a manual check pending,
// or whose slot falls in (prev, now].
func (r *Runner) tick(ctx context.Context, prev, now time.Time) {
	r.flush(ctx)
	// Taken before the list: a check that finishes while the list is read
	// reads as pending in it, and must not run twice. One that finished before
	// this has recorded so, which the list shows.
	r.mu.Lock()
	wasBusy := maps.Clone(r.running)
	r.mu.Unlock()
	ms, err := r.st.ListEnabledMonitors(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("monitor: list monitors", "error", err)
		}
		return
	}
	r.forgetDisabled(ms)
	for _, m := range ms {
		// A busy monitor's request waits, however old: it may be that request
		// which is running.
		manual := m.CheckRequestedAt > m.CheckFinishedAt && !wasBusy[m.ProjectID] && ctx.Err() == nil
		if manual && tooOld(m.CheckRequestedAt, now) {
			r.finishCheck(ctx, m, Result{Error: checkNotRun})
			manual = false
		}
		if !manual && !due(prev, now, r.interval, offset(m.ProjectID, r.interval)) {
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
		go r.dispatch(ctx, m, manual)
	}
}

// tooOld reports whether a manual check requested at (RFC 3339) is no longer
// worth serving; one that cannot be read is not.
func tooOld(requestedAt string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, requestedAt)
	return err != nil || now.Sub(t) > checkMaxAge
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

// dispatch runs m's slot, or its manual check, on a worker. A manual check
// always records how it ended, before the monitor counts as free, whatever
// shutdown does to it.
func (r *Runner) dispatch(ctx context.Context, m store.Monitor, manual bool) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		delete(r.running, m.ProjectID)
		r.mu.Unlock()
	}()
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		if manual {
			r.finishCheck(ctx, m, Result{Error: checkNotRun})
		}
		return
	}
	var evs []store.Event
	var res Result
	switch {
	case !manual:
		evs = r.job(ctx, m)
	case ctx.Err() == nil:
		evs, res = r.check(ctx, m)
	}
	<-r.sem
	if ctx.Err() != nil {
		if manual {
			r.finishCheck(ctx, m, Result{Error: checkNotRun})
		}
		return // cut short by shutdown: not a real result
	}
	r.mu.Lock()
	r.pending = append(r.pending, evs...)
	r.mu.Unlock()
	if manual {
		r.finishCheck(ctx, m, res)
	}
}

// finishCheck records how m's manual check ended, on a context shutdown does
// not cancel. Never before its request: the page waits for finished_at to reach
// requested_at, and a clock that stepped back must not leave it waiting.
func (r *Runner) finishCheck(ctx context.Context, m store.Monitor, res Result) {
	at := r.now().UTC().Format(time.RFC3339)
	if at < m.CheckRequestedAt {
		at = m.CheckRequestedAt
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := r.st.FinishMonitorCheck(fctx, m.ProjectID, m.Origin, store.MonitorCheckResult{
		FinishedAt: at, Up: res.Up, Status: res.Status, Reason: res.Reason, Error: res.Error, MS: elapsedMS(res),
	}); err != nil {
		r.logger.Warn("monitor: store manual check", "project_id", m.ProjectID, "error", err)
	}
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
	up, _ := r.availability(ctx, m)
	return append([]store.Event{up}, r.daily(ctx, m)...)
}

// runCheck is one manual check: the availability check and both daily ones,
// due or not. It counts as the day's attempt, so a read that failed is not
// tried again by the next slot.
func (r *Runner) runCheck(ctx context.Context, m store.Monitor) ([]store.Event, Result) {
	up, res := r.availability(ctx, m)
	now := r.now().UTC()
	r.mu.Lock()
	r.tried[m.ProjectID] = attempt{m.Origin, now}
	r.mu.Unlock()
	return append([]store.Event{up}, r.readings(ctx, m, true, true, now)...), res
}

// availability GETs m's origin: its row and the result.
func (r *Runner) availability(ctx context.Context, m store.Monitor) (store.Event, Result) {
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
	return uptimeRow(m.ProjectID, start, res), res
}

// elapsedMS is a check's time in whole milliseconds.
func elapsedMS(res Result) int {
	return int(math.Round(float64(res.Elapsed) / float64(time.Millisecond)))
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
	ms := float64(elapsedMS(res))
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
	return r.readings(ctx, m, certDue, domainDue, now)
}

// readings runs the certificate check and the domain check, each when asked.
func (r *Runner) readings(ctx context.Context, m store.Monitor, certDue, domainDue bool, now time.Time) []store.Event {
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
// stores only the check time and writes a row with no value, so a reading
// from before a move to it stops showing. A read that got no certificate
// keeps the last reading and its check time, stores only why, and writes no
// row; one that got a certificate is a reading, verified or not.
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
		return []store.Event{row(m.ProjectID, "cert", now, nil, map[string]string{})}
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
