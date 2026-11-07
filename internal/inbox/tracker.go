package inbox

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
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
	states map[key]entry
}

// NewTracker is a Tracker over st that reminds of a failing state every
// remind (<= 0: never).
func NewTracker(st TrackerStore, remind time.Duration, logger *slog.Logger) *Tracker {
	return &Tracker{st: st, remind: remind, logger: logger, states: map[key]entry{}}
}

// Uptime records one availability check of origin: down after two
// failures in a row (reason from the check's closed vocabulary), up
// again after one success. errText, the check's error, is kept in the
// state only.
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
		if ok {
			t.recover(ctx, k, e, store.NotifyUptimeUp, at)
		}
		return
	}
	if e.Failing {
		return
	}
	if !ok {
		e = entry{Since: at}
	}
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
// (expiresAt, RFC 3339, "" unknown) warns at 30, 7 and 1 days.
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

// Tick drops the states no enabled monitor has any more (without a
// message), reminds of every failing state not reminded of for the
// remind interval (a certificate not while its origin is down), and
// sends the form submission bursts whose window has ended.
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
	if !t.load(ctx) {
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
// monitor of their project has.
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
	for k := range t.states {
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
	e, ok := t.states[k]
	if e.Failing {
		if errText != e.Error { // so a reminder says what fails now
			e.Error = errText
			t.put(ctx, k, e, nil)
		}
		return
	}
	if !ok {
		e = entry{Since: at}
	}
	e.Error = errText
	if immediate || at.Sub(e.Since) >= graceUnread {
		t.announce(ctx, k, e, typ, at, map[string]string{"error": errText})
		return
	}
	if !ok {
		t.put(ctx, k, e, nil)
	}
}

// announce makes e failing and sends typ with since and fields. The
// first reminder falls on a multiple of the remind interval after since,
// so it reads "still down since 14:02 (4h)" and never comes at once.
func (t *Tracker) announce(ctx context.Context, k key, e entry, typ string, at time.Time, fields map[string]string) {
	e.Failing = true
	e.RemindedAt = at
	if t.remind > 0 {
		e.RemindedAt = e.Since.Add(at.Sub(e.Since) / t.remind * t.remind)
	}
	fields["since"] = stampRFC(e.Since)
	t.put(ctx, k, e, t.note(k, typ, fields, at))
}

// recover ends the state of k: with typ when it was failing, silently
// when it was only counting failures. The message and a cleared state
// are written together, then the state is deleted.
func (t *Tracker) recover(ctx context.Context, k key, e entry, typ string, at time.Time) {
	if e.Failing {
		n := t.note(k, typ, map[string]string{"since": stampRFC(e.Since), "until": stampRFC(at)}, at)
		if !t.put(ctx, k, entry{}, n) {
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
	exp, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		t.logger.Warn("inbox: bad expiry", "kind", k.kind, "subject", k.subject, "expires", expires, "error", err)
		return
	}
	e, ok := t.states[k]
	if !ok || e.ExpiresAt != expires {
		e = entry{ExpiresAt: expires}
	}
	left := daysLeft(exp, at)
	step := 0
	for _, s := range steps {
		if left <= s && (step == 0 || s < step) {
			step = s
		}
	}
	if step == 0 || (e.Step != 0 && step >= e.Step) {
		if !ok || e != t.states[k] {
			t.put(ctx, k, e, nil) // first sight, or a renewal: remember the expiry
		}
		return
	}
	e.Step = step
	t.put(ctx, k, e, t.note(k, typ, map[string]string{"days": strconv.Itoa(left), "expires": expires}, at))
}

// daysLeft is floor((exp − at) / 24h), negative once expired.
func daysLeft(exp, at time.Time) int {
	d := exp.Sub(at)
	days := d / (24 * time.Hour)
	if d < 0 && d%(24*time.Hour) != 0 {
		days--
	}
	return int(days)
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
