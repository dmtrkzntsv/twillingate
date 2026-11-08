// Package monitor runs a project's availability monitors, one per origin.
// A project gets one availability check per interval, however many monitors it
// has: its enabled monitors take turns, in origin order, so n of them are each
// checked every n intervals. The turn's slot GETs the origin and writes the
// result to the project's event log as a checks/uptime row. Once a day (and right
// after a monitor is enabled) it reads each origin's certificate, and once a day
// each main domain's registration expiry, shared by the project's origins on it;
// it keeps them in the store and writes them as checks/cert and checks/domain
// rows. Those reads run on a small pool of their own, so that they never hold an
// availability check back. A read that fails says nothing about either: the last
// reading stands, unrecorded, and is tried again after an hour.
//
// Each result is also reported to the alerts (inbox's Tracker), which decides
// when to notify: every availability check, every certificate reading and
// every domain lookup, failed ones included. A certificate that reads as
// invalid is read again hourly, not daily, so its recovery shows soon.
//
// Every availability check, scheduled or manual, keeps its result on the monitor's
// row, which Settings shows. A manual check is a request on that row, since the API
// that takes it may be another process: the next tick runs all three for that
// origin, outside the slot.
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

	"github.com/dmtrkzntsv/twillingate/internal/inbox"
	"github.com/dmtrkzntsv/twillingate/internal/shared/maindomain"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Store is the slice of the store the monitor reads and writes.
type Store interface {
	ListEnabledMonitors(ctx context.Context) ([]store.Monitor, error)
	SetMonitorCert(ctx context.Context, projectID int64, origin string, c store.CertState) error
	SetMonitorCertError(ctx context.Context, projectID int64, origin, msg string) error
	ListMonitorDomains(ctx context.Context, projectID int64) ([]store.MonitorDomain, error)
	SetMonitorDomain(ctx context.Context, projectID int64, domain string, d store.DomainState) error
	SetMonitorRedirects(ctx context.Context, projectID int64, origin string, n int) error
	SetMonitorLast(ctx context.Context, projectID int64, origin string, r store.MonitorResult) error
	FinishMonitorCheck(ctx context.Context, projectID int64, origin, requestedAt, finishedAt string) error
}

// EventLog is where check rows go (archive.Log).
type EventLog interface {
	WriteEvents(ctx context.Context, evs []store.Event) error
}

// Alerts takes what the monitor observes (inbox.Tracker). It is called only
// for results shutdown did not cut short.
type Alerts interface {
	Uptime(ctx context.Context, projectID int64, origin string, up bool, reason, errText string, at time.Time)
	Cert(ctx context.Context, projectID int64, origin string, c inbox.CertReading, at time.Time)
	Domain(ctx context.Context, projectID int64, domain string, ok bool, errText, expiresAt string, at time.Time)
}

// noAlerts is the default: nobody is told.
type noAlerts struct{}

func (noAlerts) Uptime(context.Context, int64, string, bool, string, string, time.Time) {}
func (noAlerts) Cert(context.Context, int64, string, inbox.CertReading, time.Time)      {}
func (noAlerts) Domain(context.Context, int64, string, bool, string, string, time.Time) {}

// Origins says whether a redirect target is one of the project's origins
// (manage's snapshot, MonitorRedirectAllowed; a bare "*" counts only for
// reqOrigin, which is the monitor's own origin).
type Origins interface {
	RedirectAllowed(projectID int64, target, reqOrigin string) bool
}

const (
	workers        = 16              // availability checks running at once; a slot that finds none free is skipped
	readWorkers    = 4               // certificate and domain reads running at once, across projects
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
)

// Runner schedules the checks. Each project has a fixed offset within the
// interval, so checks spread across it, and each slot goes to one of its
// enabled monitors in turn. A monitor whose last check is still running skips
// its turn, and so does one that finds no free worker: slots are never queued.
// The daily reads keep a slot per monitor, so a long rotation never delays them.
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
	alerts Alerts

	// job runs one slot's availability check and returns its row; a seam for
	// tests, as are the two below it.
	job func(ctx context.Context, m store.Monitor) []store.Event
	// check runs one manual check and returns its rows.
	check func(ctx context.Context, m store.Monitor) []store.Event
	// readCert and readDomain read and store one reading and return its row.
	readCert   func(ctx context.Context, m store.Monitor, now time.Time) []store.Event
	readDomain func(ctx context.Context, projectID int64, domain string, now time.Time) []store.Event

	sem     chan struct{} // availability checks
	readSem chan struct{} // certificate and domain reads
	wg      sync.WaitGroup
	mu      sync.Mutex
	slots   map[monitorKey]bool // monitors whose slot check is running
	manuals map[monitorKey]bool // monitors whose manual check is running
	reads   map[readKey]bool    // daily reads queued or running
	// readLocks orders the reads of one reading, scheduled or manual: one at a
	// time, so the one that ran last holds the newest result. Kept for good (one
	// small channel per origin and main domain).
	readLocks map[readKey]chan struct{}
	pending   []store.Event
	// tried is when each daily read last was, so one that read nothing is not
	// retried every slot. It is not kept for a monitor switched off, so
	// enabling it again reads at once.
	tried   map[readKey]time.Time
	domains map[string]string // each enabled origin's main domain
	paused  bool
}

// monitorKey names a monitor.
type monitorKey struct {
	projectID int64
	origin    string
}

// readKey names a daily reading: an origin's certificate, or a main domain's
// expiry.
type readKey struct {
	projectID int64
	kind      string // "cert" or "domain"
	name      string // the origin or the domain
}

func keyOf(m store.Monitor) monitorKey { return monitorKey{m.ProjectID, m.Origin} }

func certKey(m store.Monitor) readKey { return readKey{m.ProjectID, "cert", m.Origin} }

// options are the test seams.
type options struct {
	resolver      resolver
	allowLoopback bool
	roots         *x509.CertPool
	bootstrapURL  string
	timeout       time.Duration // MONITOR_TIMEOUT; 0: defaultTimeout
	now           func() time.Time
}

// New builds a Runner giving each project one check per interval, each check
// (the availability request and the certificate read) given timeout (config's MONITOR_TIMEOUT,
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
		alerts: noAlerts{},
		guard:  g, roots: o.roots, client: client,
		rdap:    &rdap{client: newClient(&guard{resolver: o.resolver, allowLoopback: o.allowLoopback, budget: rdapTimeout}, o.roots), bootstrapURL: o.bootstrapURL, now: o.now},
		sem:     make(chan struct{}, workers),
		readSem: make(chan struct{}, readWorkers),
		slots:   map[monitorKey]bool{}, manuals: map[monitorKey]bool{}, reads: map[readKey]bool{}, readLocks: map[readKey]chan struct{}{},
		tried: map[readKey]time.Time{}, domains: map[string]string{},
	}
	r.job = r.runSlot
	r.check = r.runCheck
	r.readCert = r.cert
	r.readDomain = r.domain
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

// tick writes the rows finished since the last tick in one batch, then reloads
// the enabled monitors, starts each with a manual check pending, starts the
// daily reads of each monitor whose slot falls in (prev, now], and checks the
// monitor whose turn it is in each project whose slot does.
func (r *Runner) tick(ctx context.Context, prev, now time.Time) {
	r.flush(ctx)
	// Taken before the list: a check that finishes while the list is read
	// reads as pending in it, and must not run twice. One that finished before
	// this has recorded so, which the list shows.
	r.mu.Lock()
	wasBusy := maps.Clone(r.manuals)
	r.mu.Unlock()
	ms, err := r.st.ListEnabledMonitors(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("monitor: list monitors", "error", err)
		}
		return
	}
	r.forgetDisabled(ms)
	if r.paused {
		r.finishPending(ctx, ms)
		return
	}
	for _, m := range ms {
		// A busy monitor's request waits, however old: it may be that request
		// which is running.
		if m.CheckPending() && !wasBusy[keyOf(m)] && ctx.Err() == nil {
			r.startManual(ctx, m, now)
		}
	}
	for _, m := range ms {
		if due(prev, now, r.interval, offset(m.ProjectID, m.Origin, r.interval)) {
			r.startReads(ctx, m)
		}
	}
	// The list is ordered by project, then origin.
	for start, end := 0, 0; start < len(ms); start = end {
		for end < len(ms) && ms[end].ProjectID == ms[start].ProjectID {
			end++
		}
		if due(prev, now, r.interval, offset(ms[start].ProjectID, "", r.interval)) {
			r.startSlot(ctx, turnOf(ms[start:end], now, r.interval))
		}
	}
}

// WithAlerts has the runner report every result to a. Call it before Run.
func (r *Runner) WithAlerts(a Alerts) {
	if a == nil {
		a = noAlerts{}
	}
	r.alerts = a
}

// Pause makes the runner run nothing and write no row: each tick only ends the
// manual checks found pending as ones that did not run, so no client waits for
// them. Call it before Run (MONITOR_DISABLED).
func (r *Runner) Pause() { r.paused = true }

// finishPending ends every pending manual check in ms as one that did not run.
// A paused install refuses new requests (manage.CheckMonitor), so these are
// from before the pause.
func (r *Runner) finishPending(ctx context.Context, ms []store.Monitor) {
	for _, m := range ms {
		if m.CheckPending() && ctx.Err() == nil {
			r.finishCheck(ctx, m)
		}
	}
}

// startManual starts m's manual check, unless it is too old to be worth serving.
// While it runs, m's slot is skipped: it is the check.
func (r *Runner) startManual(ctx context.Context, m store.Monitor, now time.Time) {
	if tooOld(m.CheckRequestedAt, now) {
		r.finishCheck(ctx, m)
		return
	}
	k := keyOf(m)
	r.mu.Lock()
	busy := r.manuals[k]
	if !busy {
		r.manuals[k] = true
	}
	r.mu.Unlock()
	if !busy {
		r.wg.Add(1)
		go r.dispatchManual(ctx, m)
	}
}

// startSlot starts m's slot check. It is skipped while m's last check or manual
// check is still running, and when all workers are busy.
func (r *Runner) startSlot(ctx context.Context, m store.Monitor) {
	k := keyOf(m)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.slots[k] || r.manuals[k] {
		return
	}
	select {
	case r.sem <- struct{}{}:
	default:
		r.logger.Debug("monitor: no free worker, slot skipped", "project_id", m.ProjectID, "origin", m.Origin)
		return
	}
	r.slots[k] = true
	r.wg.Add(1)
	go r.dispatchSlot(ctx, m)
}

// startReads queues the daily reads due for m: its certificate and its main
// domain's expiry, which the project's monitors on that domain share (one read
// at a time, one attempt per retry period). They wait for the read pool, not
// the slot.
func (r *Runner) startReads(ctx context.Context, m store.Monitor) {
	id := m.ProjectID
	now := r.now().UTC()
	if certDue(m, now) {
		r.startRead(ctx, certKey(m), now, func(ctx context.Context, at time.Time) []store.Event {
			return r.readCert(ctx, m, at)
		})
	}
	domain := r.domainOf(m.Origin)
	if domain == "" {
		return
	}
	readings, err := r.st.ListMonitorDomains(ctx, id)
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("monitor: list domains", "project_id", id, "error", err)
		}
		return
	}
	checked := ""
	for _, d := range readings {
		if d.Domain == domain {
			checked = d.CheckedAt
		}
	}
	if stale(checked, now) {
		r.startRead(ctx, readKey{id, "domain", domain}, now, func(ctx context.Context, at time.Time) []store.Event {
			return r.readDomain(ctx, id, domain, at)
		})
	}
}

// startRead queues one daily read, unless one is queued or running, or the last
// attempt was within dailyRetry.
func (r *Runner) startRead(ctx context.Context, k readKey, now time.Time, read func(context.Context, time.Time) []store.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.tried[k]; r.reads[k] || (ok && now.Sub(last) < dailyRetry) || ctx.Err() != nil {
		return
	}
	r.reads[k] = true
	r.tried[k] = now
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.reads, k)
			// From when it ended, for a list read before it did.
			if _, ok := r.tried[k]; ok {
				r.tried[k] = r.now().UTC()
			}
			r.mu.Unlock()
		}()
		unlock, ok := r.lockRead(ctx, k)
		if !ok {
			return
		}
		defer unlock()
		if !acquire(ctx, r.readSem) {
			return
		}
		evs := read(ctx, r.now().UTC())
		<-r.readSem
		r.keep(ctx, evs)
	}()
}

// live reports whether (projectID, origin) is still an enabled monitor. A
// read that started while it was enabled may end after it was disabled, removed
// or its project archived; reporting then would leave a notification no later
// prune takes back. When the list cannot be read, the monitor counts as live.
func (r *Runner) live(ctx context.Context, projectID int64, origin string) bool {
	ms, err := r.st.ListEnabledMonitors(ctx)
	if err != nil {
		return true
	}
	return slices.ContainsFunc(ms, func(m store.Monitor) bool { return m.ProjectID == projectID && m.Origin == origin })
}

// alertDomain reports a domain lookup unless no enabled monitor of the
// project has an origin on that main domain any more.
func (r *Runner) alertDomain(ctx context.Context, projectID int64, domain string, ok bool, errText, expiresAt string, at time.Time) {
	ms, err := r.st.ListEnabledMonitors(ctx)
	if err == nil && !slices.ContainsFunc(ms, func(m store.Monitor) bool {
		return m.ProjectID == projectID && r.domainOf(m.Origin) == domain
	}) {
		return
	}
	r.alerts.Domain(ctx, projectID, domain, ok, errText, expiresAt, at)
}

// domainOf is origin's main domain, worked out once while it is enabled.
func (r *Runner) domainOf(origin string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.domains[origin]
	if !ok {
		d = maindomain.OfOrigin(origin)
		r.domains[origin] = d
	}
	return d
}

// acquire takes a worker of sem, unless shutdown comes first.
func acquire(ctx context.Context, sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// keep holds a check's rows for the next flush, unless shutdown cut it short,
// which makes them no real result.
func (r *Runner) keep(ctx context.Context, evs []store.Event) {
	if ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	r.pending = append(r.pending, evs...)
	r.mu.Unlock()
}

// tooOld reports whether a manual check requested at (RFC 3339) is no longer
// worth serving; one that cannot be read is not.
func tooOld(requestedAt string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, requestedAt)
	return err != nil || now.Sub(t) > checkMaxAge
}

// forgetDisabled drops what is remembered of monitors no longer in enabled, and
// of main domains none of them uses any more.
func (r *Runner) forgetDisabled(enabled []store.Monitor) {
	monitors := make(map[monitorKey]bool, len(enabled))
	reads := make(map[readKey]bool, 2*len(enabled))
	origins := make(map[string]bool, len(enabled))
	for _, m := range enabled {
		monitors[keyOf(m)] = true
		origins[m.Origin] = true
		reads[certKey(m)] = true
		reads[readKey{m.ProjectID, "domain", r.domainOf(m.Origin)}] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.tried {
		if !reads[k] {
			delete(r.tried, k)
		}
	}
	for o := range r.domains {
		if !origins[o] {
			delete(r.domains, o)
		}
	}
}

// dispatchSlot runs a slot's check on the worker startSlot took; the monitor is free again after.
func (r *Runner) dispatchSlot(ctx context.Context, m store.Monitor) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		delete(r.slots, keyOf(m))
		r.mu.Unlock()
	}()
	evs := r.job(ctx, m)
	<-r.sem
	r.keep(ctx, evs)
}

// dispatchManual runs m's manual check. It always records how it ended, before
// the monitor counts as free, whatever shutdown does to it.
func (r *Runner) dispatchManual(ctx context.Context, m store.Monitor) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		delete(r.manuals, keyOf(m))
		r.mu.Unlock()
	}()
	evs := r.check(ctx, m)
	r.keep(ctx, evs)
	r.finishCheck(ctx, m)
}

// finishCheck ends m's manual check, on a context shutdown does not cancel; its
// result, if it got that far, is on the row already.
func (r *Runner) finishCheck(ctx context.Context, m store.Monitor) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := r.st.FinishMonitorCheck(fctx, m.ProjectID, m.Origin, m.CheckRequestedAt, r.stamp(m)); err != nil {
		r.logger.Warn("monitor: store manual check", "project_id", m.ProjectID, "error", err)
	}
}

// stamp is now as a row's time. Never before m's request: the page waits for
// finished_at to reach requested_at and reads a result older than it as a check
// that did not run, and a clock that stepped back must not make it so.
func (r *Runner) stamp(m store.Monitor) string {
	at := r.now().UTC().Format(time.RFC3339)
	if at < m.CheckRequestedAt {
		return m.CheckRequestedAt
	}
	return at
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
	return len(r.slots) + len(r.manuals) + len(r.reads)
}

// offset is a fixed place within the interval, in whole ticks: a monitor's for
// its daily reads, a project's (no origin) for its availability check.
func offset(projectID int64, origin string, interval time.Duration) time.Duration {
	h := fnv.New64a()
	h.Write([]byte(strconv.FormatInt(projectID, 10) + "|" + origin))
	slots := max(uint64(interval/tickStep), 1)
	return time.Duration(h.Sum64()%slots) * tickStep
}

// due reports whether a slot (a time ≡ off modulo interval, counted from
// the Unix epoch) falls in (prev, now].
func due(prev, now time.Time, interval, off time.Duration) bool {
	return slotAt(now, interval, off)*int64(interval)+int64(off) > prev.UnixNano()
}

// slotAt numbers the latest slot at or before t, counted from the Unix epoch.
func slotAt(t time.Time, interval, off time.Duration) int64 {
	n, iv := t.UnixNano()-int64(off), int64(interval)
	k := n / iv
	if n < 0 && n%iv != 0 {
		k--
	}
	return k
}

// turnOf is the monitor of one project whose turn it is at now, from the
// project's monitors in origin order. It is worked out from the clock, so a
// restart keeps the rotation; adding or removing a monitor reshuffles it once.
func turnOf(ms []store.Monitor, now time.Time, interval time.Duration) store.Monitor {
	n := int64(len(ms))
	i := slotAt(now, interval, offset(ms[0].ProjectID, "", interval)) % n
	if i < 0 {
		i += n
	}
	return ms[i]
}

// runSlot is one slot's check: the availability of its monitor.
func (r *Runner) runSlot(ctx context.Context, m store.Monitor) []store.Event {
	return []store.Event{r.availability(ctx, m)}
}

// runCheck is one manual check: the availability check on the availability pool,
// then both daily reads on theirs, due or not. They count as the day's attempts,
// so a read that failed is not tried again by the next slot. Shutdown cuts it
// short, which drops its rows.
func (r *Runner) runCheck(ctx context.Context, m store.Monitor) []store.Event {
	if !acquire(ctx, r.sem) {
		return nil
	}
	up := r.availability(ctx, m)
	<-r.sem
	evs := []store.Event{up}
	domain := r.domainOf(m.Origin)
	for _, read := range []struct {
		key readKey
		run func(time.Time) []store.Event
	}{
		{certKey(m), func(at time.Time) []store.Event { return r.readCert(ctx, m, at) }},
		{readKey{m.ProjectID, "domain", domain}, func(at time.Time) []store.Event { return r.readDomain(ctx, m.ProjectID, domain, at) }},
	} {
		// Waits for a scheduled read of the same reading in flight, so this
		// one, the newer, is the one stored last.
		unlock, ok := r.lockRead(ctx, read.key)
		if !ok {
			return evs
		}
		if !acquire(ctx, r.readSem) {
			unlock()
			return evs
		}
		now := r.now().UTC()
		r.mu.Lock()
		r.tried[read.key] = now
		r.mu.Unlock()
		evs = append(evs, read.run(now)...)
		<-r.readSem
		unlock()
	}
	return evs
}

// lockRead waits for its turn to read k, one reading at a time whoever asks. It
// is taken before a pool slot and the clock, so a read that waited is dated
// after the one it waited for. ok is false when ctx ended first.
func (r *Runner) lockRead(ctx context.Context, k readKey) (unlock func(), ok bool) {
	r.mu.Lock()
	ch := r.readLocks[k]
	if ch == nil {
		ch = make(chan struct{}, 1)
		r.readLocks[k] = ch
	}
	r.mu.Unlock()
	if !acquire(ctx, ch) {
		return nil, false
	}
	return func() { <-ch }, true
}

// availability GETs m's origin, keeps the result on its row and returns its check row.
func (r *Runner) availability(ctx context.Context, m store.Monitor) store.Event {
	start := r.now().UTC()
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	res := Check(cctx, r.client, m.Origin, m.Contains, func(target string) bool {
		return r.origins.RedirectAllowed(m.ProjectID, target, m.Origin)
	})
	cancel()
	// The alerts get the time the check ended: a manual check and a slot of
	// the same origin can overlap, and the one that ends later saw later.
	done := r.now().UTC()
	// Keep the count Settings shows, from every check that got an answer (a
	// blip must not hide the warning). m is a snapshot an overlapping check
	// may have outdated, so no comparison is made here: the store writes
	// only when the stored value differs.
	if res.Answered && ctx.Err() == nil {
		if err := r.st.SetMonitorRedirects(ctx, m.ProjectID, m.Origin, res.Redirects); err != nil {
			r.logger.Warn("monitor: store redirect count", "project_id", m.ProjectID, "error", err)
		}
	}
	// A check shutdown cut short says nothing of the site. The store's write
	// leaves out a row an origins edit removed meanwhile.
	if ctx.Err() == nil {
		if err := r.st.SetMonitorLast(ctx, m.ProjectID, m.Origin, store.MonitorResult{
			CheckedAt: r.stamp(m), Up: res.Up, Status: res.Status, Reason: res.Reason, Error: res.Error, MS: elapsedMS(res),
		}); err != nil {
			r.logger.Warn("monitor: store last result", "project_id", m.ProjectID, "error", err)
		}
		if r.live(ctx, m.ProjectID, m.Origin) {
			r.alerts.Uptime(ctx, m.ProjectID, m.Origin, res.Up, res.Reason, res.Error, done)
		}
	}
	return uptimeRow(m.ProjectID, m.Origin, start, res)
}

// elapsedMS is a check's time in whole milliseconds.
func elapsedMS(res Result) int {
	return int(math.Round(float64(res.Elapsed) / float64(time.Millisecond)))
}

func uptimeRow(projectID int64, origin string, at time.Time, res Result) store.Event {
	attrs := map[string]string{"origin": origin, "up": "0", "redirects": strconv.Itoa(res.Redirects)}
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

// certDue reports whether m's certificate is to be read again: daily, but
// hourly while the last reading has an expiry and is not valid, so that a
// renewed certificate is noticed soon.
func certDue(m store.Monitor, now time.Time) bool {
	if m.CertExpiresAt != "" && !m.CertValid {
		t, err := time.Parse(time.RFC3339, m.CertCheckedAt)
		return err != nil || now.Sub(t) >= dailyRetry
	}
	return stale(m.CertCheckedAt, now)
}

// cert reads and stores the certificate. An http:// origin has none: it
// stores only the check time and writes a row with no value, so a reading
// from before a move to it stops showing. A read that got no certificate
// keeps the last reading and its check time, stores only why, and writes no
// row; one that got a certificate is a reading, verified or not.
func (r *Runner) cert(ctx context.Context, m store.Monitor, now time.Time) []store.Event {
	u, err := url.Parse(m.Origin)
	if err != nil {
		return nil
	}
	https := u.Scheme == "https"
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
		if r.live(ctx, m.ProjectID, m.Origin) {
			r.alerts.Cert(ctx, m.ProjectID, m.Origin, inbox.CertReading{Error: st.Error}, now)
		}
		return nil
	}
	if err := r.st.SetMonitorCert(ctx, m.ProjectID, m.Origin, st); err != nil {
		r.logger.Warn("monitor: store certificate", "project_id", m.ProjectID, "error", err)
	}
	if !https {
		return []store.Event{row(m.ProjectID, "cert", now, nil, map[string]string{"origin": m.Origin})}
	}
	if r.live(ctx, m.ProjectID, m.Origin) {
		r.alerts.Cert(ctx, m.ProjectID, m.Origin, inbox.CertReading{Read: true, Valid: st.Valid, Error: st.Error, ExpiresAt: st.ExpiresAt}, now)
	}
	attrs := map[string]string{"origin": m.Origin, "valid": "0", "issuer": st.Issuer, "error": st.Error}
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

// domain reads and stores a main domain's expiry. One RDAP cannot answer for (an
// address, a private suffix, a TLD without RDAP) is stored as unknown; a failed
// lookup keeps the last reading and writes no row.
func (r *Runner) domain(ctx context.Context, projectID int64, domain string, now time.Time) []store.Event {
	dctx, cancel := context.WithTimeout(ctx, rdapTimeout)
	exp, err := r.rdap.expiry(dctx, domain)
	cancel()
	if ctx.Err() != nil {
		return nil
	}
	switch {
	case errors.Is(err, errUnknown):
		r.logger.Debug("monitor: domain expiry unknown", "project_id", projectID, "domain", domain, "error", err)
		r.alertDomain(ctx, projectID, domain, true, "", "", now)
	case err != nil:
		// The lookup failed, which says nothing about the domain: the last
		// reading stands, unrecorded, and is retried after dailyRetry.
		r.logger.Debug("monitor: domain expiry lookup failed", "project_id", projectID, "domain", domain, "error", err)
		r.alertDomain(ctx, projectID, domain, false, err.Error(), "", now)
		return nil
	}
	if err := r.st.SetMonitorDomain(ctx, projectID, domain, store.DomainState{ExpiresAt: exp, CheckedAt: now.Format(time.RFC3339)}); err != nil {
		r.logger.Warn("monitor: store domain expiry", "project_id", projectID, "error", err)
	}
	if err == nil {
		r.alertDomain(ctx, projectID, domain, true, "", exp, now)
	}
	attrs := map[string]string{"domain": domain}
	var value *float64
	if d, ok := daysLeft(exp, now); ok {
		value = &d
		attrs["expires"] = exp
	}
	return []store.Event{row(projectID, "domain", now, value, attrs)}
}
