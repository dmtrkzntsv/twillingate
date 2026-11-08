package inbox

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/maindomain"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TrackerStore is the slice of the store the Tracker uses.
type TrackerStore interface {
	ListNotifyStates(ctx context.Context) ([]store.NotifyState, error)
	PutNotifyState(ctx context.Context, s store.NotifyState, n *store.NewNotification) error
	DeleteNotifyState(ctx context.Context, projectID int64, kind, subject string) error
	FlushFormBursts(ctx context.Context, now time.Time) (int, error)
	ListEnabledMonitors(ctx context.Context) ([]store.Monitor, error)
}

// CertReading is one certificate observation of an origin. Read: a
// certificate was obtained (then Valid says whether it verified, Error
// why not); !Read: none could be (timeout, DNS, handshake), Error why.
// ExpiresAt is RFC 3339, "" unknown.
type CertReading struct {
	Read, Valid bool
	Error       string
	ExpiresAt   string
}

// State kinds: a failing state's or an expiry's key is (project, kind,
// subject); the subject is the origin, or the main domain for the domain
// kinds.
const (
	kindUptime       = "uptime"
	kindCert         = "cert"
	kindDomain       = "domain"
	kindCertExpiry   = "cert_expiry"
	kindDomainExpiry = "domain_expiry"
)

const (
	// downAfter is how many failed availability checks in a row make an
	// origin down.
	downAfter = 2
	// graceUnread is how long a certificate that cannot be read, or a
	// domain whose lookups fail, waits before it is failing.
	graceUnread = 24 * time.Hour
	// A streak of failures not yet failing starts over after a gap
	// without observations longer than these: two failures that far
	// apart (a restart, a lost write) are not in a row. MONITOR_INTERVAL
	// is at most an hour; certificate and domain reads that fail are
	// retried hourly.
	uptimeGap = 2 * time.Hour
	unreadGap = 25 * time.Hour
	// alignSlack is how long after the remind multiple before it an
	// announcement may come and still have its first reminder there.
	alignSlack = time.Hour
)

// The days-left steps an expiry warns at, once each per expiry.
var (
	certSteps   = []int{14, 7, 1}
	domainSteps = []int{30, 7, 1}
)

type key struct {
	projectID     int64
	kind, subject string
}

// entry is a state's Data: a failing kind uses the first fields, an
// expiry kind the last two.
type entry struct {
	Fails      int       `json:"fails,omitempty"` // failed checks in a row (uptime)
	Since      time.Time `json:"since,omitzero"`  // first failed observation
	Last       time.Time `json:"last,omitzero"`   // latest failed observation
	Ended      time.Time `json:"ended,omitzero"`  // recovered at; the row awaits its delete
	Failing    bool      `json:"failing,omitempty"`
	RemindedAt time.Time `json:"reminded_at,omitzero"`
	Reason     string    `json:"reason,omitempty"`
	Error      string    `json:"error,omitempty"`
	ExpiresAt  string    `json:"expires_at,omitempty"`
	Step       int       `json:"step,omitempty"` // the last step sent for ExpiresAt; 0 none
}

// Tracker decides, from each check result and reading, when a failing,
// still failing, recovered or expiring notification is created. Its
// states live in the store, so a restart repeats nothing; they are read
// once, on first use, and written through. Safe for concurrent use;
// store errors are logged, and a failed write is retried by the next
// observation.
type Tracker struct {
	st     TrackerStore
	remind time.Duration
	logger *slog.Logger

	mu     sync.Mutex
	loaded bool
	paused bool
	states map[key]entry
	// lastUp is each origin's latest success, in memory only: a failure
	// dated before it is stale, so a check that reports late cannot start
	// a streak the recovery already ended.
	lastUp map[key]time.Time
}

// NewTracker is a Tracker over st that reminds of a failing state every
// remind (<= 0: never).
func NewTracker(st TrackerStore, remind time.Duration, logger *slog.Logger) *Tracker {
	return &Tracker{st: st, remind: remind, logger: logger, states: map[key]entry{}, lastUp: map[key]time.Time{}}
}

// PauseMonitoring makes Tick leave the monitor states alone, for an
// install whose monitoring is paused: no observation comes to end them, so
// it neither reminds of a failing one nor prunes any. They stay as they
// are, and a later run with monitoring on continues them. Form bursts
// still flush. Call it before Run.
func (t *Tracker) PauseMonitoring() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.paused = true
}

// Uptime records one availability check of origin: down after two
// failures in a row (reason from the check's closed vocabulary), up
// again after one success. errText, the check's error, is kept in the
// state only. Overlapping checks (a manual one beside a slot) can report
// out of order: a success dated before the state's latest observation is
// stale and ignored, so a recovery never ends before its outage began, and
// so is a failure dated before the latest success, so a late failure from
// before a recovery starts no new streak. Other failures count in any
// order.
func (t *Tracker) Uptime(ctx context.Context, projectID int64, origin string, up bool, reason, errText string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.load(ctx) {
		return
	}
	at = at.UTC()
	k := key{projectID, kindUptime, origin}
	e, ok := t.states[k]
	if up {
		if ok && at.Before(latest(e)) {
			return
		}
		if at.After(t.lastUp[k]) {
			t.lastUp[k] = at
		}
		if !ok {
			return
		}
		t.recover(ctx, k, e, store.NotifyUptimeUp, at)
		// The outage kept the certificate's reminders back; its next one
		// is an interval from now, not at the next tick.
		ck := key{projectID, kindCert, origin}
		if c := t.states[ck]; e.Failing && c.Failing && c.RemindedAt.Before(at) {
			c.RemindedAt = at
			t.put(ctx, ck, c, nil)
		}
		return
	}
	if at.Before(t.lastUp[k]) {
		return
	}
	if e.Failing {
		// Kept in memory only, not worth a write per check: a stale success
		// from before this failure is then ignored too.
		if at.After(e.Last) {
			e.Last = at
			t.states[k] = e
		}
		return
	}
	e = t.streak(k, at, uptimeGap)
	e.Fails++
	e.Reason, e.Error = reason, errText
	if e.Fails < downAfter {
		t.put(ctx, k, e, nil)
		return
	}
	t.announce(ctx, k, e, store.NotifyUptimeDown, at, map[string]string{"reason": reason})
}

// Cert records one certificate reading of origin. While the origin is
// down it is ignored, so one outage pages once. A certificate that fails
// verification is failing at once, one that cannot be read after 24
// hours; a valid one recovers, and its expiry warns at 14, 7 and 1 days.
func (t *Tracker) Cert(ctx context.Context, projectID int64, origin string, c CertReading, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.load(ctx) || t.down(projectID, origin) {
		return
	}
	at = at.UTC()
	k := key{projectID, kindCert, origin}
	if c.Read && c.Valid {
		if e, ok := t.states[k]; ok {
			t.recover(ctx, k, e, store.NotifyCertRecovered, at)
		}
		if c.ExpiresAt != "" {
			t.expiry(ctx, key{projectID, kindCertExpiry, origin}, c.ExpiresAt, certSteps, store.NotifyCertExpiring, at)
		}
		return
	}
	t.failure(ctx, k, store.NotifyCertFailing, c.Error, c.Read, at)
}

// Domain records one RDAP lookup of a main domain: failing once lookups
// have failed for 24 hours, recovered by one that works, whose expiry
// (expiresAt, YYYY-MM-DD or RFC 3339, "" unknown) warns at 30, 7 and 1 days.
func (t *Tracker) Domain(ctx context.Context, projectID int64, domain string, ok bool, errText, expiresAt string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.load(ctx) {
		return
	}
	at = at.UTC()
	k := key{projectID, kindDomain, domain}
	if !ok {
		t.failure(ctx, k, store.NotifyDomainFailing, errText, false, at)
		return
	}
	if e, had := t.states[k]; had {
		t.recover(ctx, k, e, store.NotifyDomainRecovered, at)
	}
	if expiresAt != "" {
		t.expiry(ctx, key{projectID, kindDomainExpiry, domain}, expiresAt, domainSteps, store.NotifyDomainExpiring, at)
	}
}

// Tick drops the states no enabled monitor has any more, and ended ones
// (without a message), reminds of every failing state not reminded of for the
// remind interval (a certificate not while its origin is down), and
// sends the form submission bursts whose window has ended. After
// PauseMonitoring it only sends the bursts.
func (t *Tracker) Tick(ctx context.Context, now time.Time) {
	now = now.UTC()
	t.tick(ctx, now)
	if _, err := t.st.FlushFormBursts(ctx, now); err != nil {
		t.logger.Warn("inbox: flush form bursts", "error", err)
	}
}

// Run calls Tick every minute until ctx ends.
func (t *Tracker) Run(ctx context.Context) {
	tk := time.NewTicker(time.Minute)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tk.C:
			t.Tick(ctx, now)
		}
	}
}

func (t *Tracker) tick(ctx context.Context, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.paused || !t.load(ctx) {
		return
	}
	t.prune(ctx)
	if t.remind <= 0 {
		return
	}
	keys := make([]key, 0, len(t.states))
	for k, e := range t.states {
		if e.Failing && now.Sub(e.RemindedAt) >= t.remind {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b key) int {
		return cmp.Or(cmp.Compare(a.projectID, b.projectID), cmp.Compare(a.kind, b.kind), cmp.Compare(a.subject, b.subject))
	})
	for _, k := range keys {
		if k.kind == kindCert && t.down(k.projectID, k.subject) {
			continue
		}
		e := t.states[k]
		e.RemindedAt = now
		fields := map[string]string{"since": stampRFC(e.Since)}
		typ := store.NotifyUptimeStillDown
		switch k.kind {
		case kindCert:
			typ, fields["error"] = store.NotifyCertStillFailing, e.Error
		case kindDomain:
			typ, fields["error"] = store.NotifyDomainStillFailing, e.Error
		}
		t.put(ctx, k, e, t.note(k, typ, fields, now))
	}
}

// prune deletes the states whose origin, or main domain, no enabled
// monitor of their project has, and the ended rows a failed delete left.
func (t *Tracker) prune(ctx context.Context) {
	ms, err := t.st.ListEnabledMonitors(ctx)
	if err != nil {
		t.logger.Warn("inbox: list monitors", "error", err)
		return
	}
	origins, domains := map[key]bool{}, map[key]bool{}
	for _, m := range ms {
		origins[key{projectID: m.ProjectID, subject: m.Origin}] = true
		domains[key{projectID: m.ProjectID, subject: maindomain.OfOrigin(m.Origin)}] = true
	}
	for k := range t.lastUp {
		if !origins[key{projectID: k.projectID, subject: k.subject}] {
			delete(t.lastUp, k)
		}
	}
	for k, e := range t.states {
		failingKind := k.kind == kindUptime || k.kind == kindCert || k.kind == kindDomain
		if failingKind && !e.Failing && e.Since.IsZero() { // an ended or empty row
			t.del(ctx, k)
			continue
		}
		live := origins
		if k.kind == kindDomain || k.kind == kindDomainExpiry {
			live = domains
		}
		if !live[key{projectID: k.projectID, subject: k.subject}] {
			t.del(ctx, k)
		}
	}
}

// failure records one failed certificate or domain observation: failing
// at once when immediate, else once it has failed for graceUnread.
func (t *Tracker) failure(ctx context.Context, k key, typ, errText string, immediate bool, at time.Time) {
	if e := t.states[k]; e.Failing {
		if errText != e.Error { // so a reminder says what fails now
			e.Error = errText
			t.put(ctx, k, e, nil)
		}
		return
	}
	e := t.streak(k, at, unreadGap)
	e.Error = errText
	if immediate || at.Sub(e.Since) >= graceUnread {
		t.announce(ctx, k, e, typ, at, map[string]string{"error": errText})
		return
	}
	t.put(ctx, k, e, nil)
}

// streak is k's not-yet-failing state with at recorded among its
// failures (the first or latest when it is earlier or later than those):
// the stored one when it is a streak still going, else a new one from at. A row without a first failure (a recovery whose delete
// did not happen, an unreadable one) or one whose latest failure is more
// than gap ago does not continue.
func (t *Tracker) streak(k key, at time.Time, gap time.Duration) entry {
	e := t.states[k]
	last := e.Last
	if last.IsZero() {
		last = e.Since
	}
	if e.Since.IsZero() || at.Sub(last) > gap || e.Since.Sub(at) > gap {
		e = entry{Since: at}
	}
	if at.Before(e.Since) {
		e.Since = at
	}
	if at.After(e.Last) {
		e.Last = at
	}
	return e
}

// announce makes e failing and sends typ with since and fields. When
// the announcement comes soon after since (an outage, debounced for
// minutes), the first reminder falls on a multiple of the remind interval
// after since, so it reads "still down since 14:02 (4h)". When it comes
// later (a certificate or a lookup failing for a day), that multiple can
// be minutes after the announcement, so the first reminder comes a whole
// interval after it instead. Either way it never comes at once.
func (t *Tracker) announce(ctx context.Context, k key, e entry, typ string, at time.Time, fields map[string]string) {
	e.Failing = true
	e.RemindedAt = at
	if t.remind > 0 {
		if aligned := e.Since.Add(at.Sub(e.Since) / t.remind * t.remind); at.Sub(aligned) <= alignSlack {
			e.RemindedAt = aligned
		}
	}
	fields["since"] = stampRFC(e.Since)
	t.put(ctx, k, e, t.note(k, typ, fields, at))
}

// recover ends the state of k: with typ when it was failing, silently
// when it was only counting failures. The store cannot delete a state
// and insert a notification in one step, so the message goes with a row
// marked ended (which no failure continues, and Tick deletes), then the
// row is deleted.
func (t *Tracker) recover(ctx context.Context, k key, e entry, typ string, at time.Time) {
	if e.Failing {
		n := t.note(k, typ, map[string]string{"since": stampRFC(e.Since), "until": stampRFC(at)}, at)
		if !t.put(ctx, k, entry{Ended: at}, n) {
			return
		}
	}
	t.del(ctx, k)
}

// expiry warns once per step of steps (days left, descending) for the
// expiry expires: the tightest step the days left fall in, when it is
// tighter than the last one sent for this expiry. Another expiry
// (a renewal) starts over.
func (t *Tracker) expiry(ctx context.Context, k key, expires string, steps []int, typ string, at time.Time) {
	norm, left, ok := readExpiry(expires, at)
	if !ok {
		t.logger.Warn("inbox: bad expiry", "kind", k.kind, "subject", k.subject, "expires", expires)
		return
	}
	e, had := t.states[k]
	if !had || e.ExpiresAt != norm {
		e = entry{ExpiresAt: norm}
	}
	step := 0
	for _, s := range steps {
		if left <= s && (step == 0 || s < step) {
			step = s
		}
	}
	if step == 0 || (e.Step != 0 && step >= e.Step) {
		if !had || e != t.states[k] {
			t.put(ctx, k, e, nil) // first sight, or a renewal: remember the expiry
		}
		return
	}
	e.Step = step
	t.put(ctx, k, e, t.note(k, typ, map[string]string{"days": strconv.Itoa(left), "expires": expires}, at))
}

// readExpiry reads an expiry as the monitor writes it (RFC 3339 for a
// certificate, a bare YYYY-MM-DD for a domain) and returns it normalized
// (Z and +00:00 are one expiry) with the days left at at, counted as the
// monitor's daysLeft counts them: for RFC 3339 the whole days, rounded
// down (negative once expired); for a date the calendar days from at's UTC
// date, 0 on the day itself. False when expires is neither.
func readExpiry(expires string, at time.Time) (norm string, left int, ok bool) {
	if exp, err := time.Parse(time.RFC3339, expires); err == nil {
		return exp.UTC().Format(time.RFC3339), int(math.Floor(exp.Sub(at).Hours() / 24)), true
	}
	exp, err := time.Parse(time.DateOnly, expires)
	if err != nil {
		return "", 0, false
	}
	y, m, d := at.UTC().Date()
	return exp.Format(time.DateOnly), int(math.Round(exp.Sub(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)).Hours() / 24)), true
}

// latest is the time of e's latest observation: its latest failure, its
// first, or its recovery.
func latest(e entry) time.Time {
	l := e.Since
	for _, at := range []time.Time{e.Last, e.Ended} {
		if at.After(l) {
			l = at
		}
	}
	return l
}

// down reports whether origin's availability is failing in projectID.
func (t *Tracker) down(projectID int64, origin string) bool {
	return t.states[key{projectID, kindUptime, origin}].Failing
}

func (t *Tracker) note(k key, typ string, fields map[string]string, at time.Time) *store.NewNotification {
	return &store.NewNotification{Type: typ, ProjectID: k.projectID, Subject: k.subject, Fields: fields, At: at}
}

// load reads the states once; false (logged) when the store fails, so
// the next call tries again.
func (t *Tracker) load(ctx context.Context) bool {
	if t.loaded {
		return true
	}
	rows, err := t.st.ListNotifyStates(ctx)
	if err != nil {
		t.logger.Warn("inbox: list notify states", "error", err)
		return false
	}
	for _, r := range rows {
		var e entry
		if err := json.Unmarshal([]byte(r.Data), &e); err != nil {
			t.logger.Warn("inbox: bad notify state", "kind", r.Kind, "subject", r.Subject, "error", err)
			continue
		}
		t.states[key{r.ProjectID, r.Kind, r.Subject}] = e
	}
	t.loaded = true
	return true
}

// put writes e as k's state, with n when not nil, and keeps it on
// success; false (logged) on failure, leaving the old state in memory.
func (t *Tracker) put(ctx context.Context, k key, e entry, n *store.NewNotification) bool {
	data, err := json.Marshal(e)
	if err == nil {
		err = t.st.PutNotifyState(ctx, store.NotifyState{ProjectID: k.projectID, Kind: k.kind, Subject: k.subject, Data: string(data)}, n)
	}
	if err != nil {
		t.logger.Error("inbox: write notify state", "project_id", k.projectID, "kind", k.kind, "subject", k.subject, "error", err)
		return false
	}
	t.states[k] = e
	return true
}

func (t *Tracker) del(ctx context.Context, k key) {
	if err := t.st.DeleteNotifyState(ctx, k.projectID, k.kind, k.subject); err != nil {
		t.logger.Error("inbox: delete notify state", "project_id", k.projectID, "kind", k.kind, "subject", k.subject, "error", err)
		return
	}
	delete(t.states, k)
}

func stampRFC(at time.Time) string { return at.UTC().Format(time.RFC3339) }
