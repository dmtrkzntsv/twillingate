package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

type fakeAttributor struct {
	asked [][]archive.Lookup
	days  int
	found []archive.Found
	err   error
}

func (f *fakeAttributor) Attribute(_ context.Context, ls []archive.Lookup, days int, _ archive.Rules) ([]archive.Found, error) {
	f.asked, f.days = append(f.asked, ls), days
	return f.found, f.err
}

// loggedEvents is an event log that records each call; while fail is
// set, a call fails and writes nothing.
type loggedEvents struct {
	evs   []store.Event
	calls []int // events per call, failed ones included
	fail  error
}

func (l *loggedEvents) WriteEvents(_ context.Context, evs []store.Event) error {
	l.calls = append(l.calls, len(evs))
	if l.fail != nil {
		return l.fail
	}
	l.evs = append(l.evs, evs...)
	return nil
}

// pendingStore is a real store with the attribution methods over a list
// of pending submissions and an outbox the written events wait in.
type pendingStore struct {
	store.Store
	pending    []store.PendingSubmission
	before     time.Time
	attributed map[string]store.Event
	asWritten  map[string]bool // p.WriteEvent as AttributeSubmission got it
	outbox     []store.UnloggedEvent
	outboxRead int // UnloggedEvents calls
}

func (s *pendingStore) UnloggedEvents(_ context.Context, limit int) ([]store.UnloggedEvent, error) {
	s.outboxRead++
	return slices.Clone(s.outbox[:min(limit, len(s.outbox))]), nil
}

func (s *pendingStore) MarkLogged(_ context.Context, keys []store.UnloggedEvent) error {
	s.outbox = slices.DeleteFunc(s.outbox, func(u store.UnloggedEvent) bool {
		return slices.ContainsFunc(keys, func(k store.UnloggedEvent) bool { return k.ProjectID == u.ProjectID && k.ID == u.ID })
	})
	return nil
}

func (s *pendingStore) PendingSubmissions(_ context.Context, before time.Time, limit int) ([]store.PendingSubmission, error) {
	s.before = before
	var out []store.PendingSubmission
	for _, p := range s.pending {
		if _, done := s.attributed[p.ID]; !done && !p.ReceivedAt.After(before) && len(out) < limit {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *pendingStore) AttributeSubmission(_ context.Context, p store.PendingSubmission, ev store.Event) (bool, error) {
	if s.attributed == nil {
		s.attributed, s.asWritten = map[string]store.Event{}, map[string]bool{}
	}
	s.attributed[p.ID], s.asWritten[p.ID] = ev, p.WriteEvent
	if p.WriteEvent {
		s.outbox = append(s.outbox, store.UnloggedEvent{ProjectID: p.ProjectID, ID: p.ID, Event: ev})
	}
	return p.WriteEvent, nil
}

// attributionRunner is a Runner over st (backed by setup's store and
// registry) at a fixed now.
func attributionRunner(t *testing.T, st *pendingStore, now time.Time) *Runner {
	t.Helper()
	base, reg, _ := setup(t, nil, nil)
	st.Store = base
	clock := func() time.Time { return now }
	return New(st, configtest.Load(t, nil), reg, identity.NewSalter(base, clock), slog.Default(), clock)
}

func pendingAt(id string, at time.Time, f func(*store.Event)) store.PendingSubmission {
	ev := store.Event{ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: store.FormSubmitEvent,
		TS: at, ReceivedAt: at, ActorKind: store.ActorUser, ActorID: "u1", InstallID: "i1", Host: "app.com"}
	if f != nil {
		f(&ev)
	}
	return store.PendingSubmission{ProjectID: 1, ID: id, ReceivedAt: at, Event: ev, WriteEvent: true, ConnectionID: "h1"}
}

// Submissions a minute old are looked up with their actor and install id
// over the configured window; the event gets the source unless the
// client sent one, the newest view's details either way, and reaches the
// log once written.
func TestAttributeSubmissions(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{pending: []store.PendingSubmission{
		pendingAt("plain", now.Add(-2*time.Minute), nil),
		pendingAt("sent", now.Add(-2*time.Minute), func(e *store.Event) { e.UTMSource = "nl" }),
	}}
	a := &fakeAttributor{found: []archive.Found{
		{ProjectID: 1, ID: "plain", Source: store.Event{ReferrerSource: "google"}, Newest: store.Event{Kind: "web", OS: "macos"}},
		{ProjectID: 1, ID: "sent", Source: store.Event{ReferrerSource: "google"}, Newest: store.Event{Kind: "web", OS: "ios"}},
	}}
	log := &loggedEvents{}
	r := attributionRunner(t, st, now).WithAttribution(a, log)
	r.attributeSubmissions(context.Background())
	if !st.before.Equal(now.Add(-attributionDelay)) {
		t.Errorf("asked for submissions before %v", st.before)
	}
	if len(a.asked) != 1 || len(a.asked[0]) != 2 || a.days != 7 || a.asked[0][0].ActorID != "u1" ||
		a.asked[0][0].InstallID != "i1" || a.asked[0][0].ConnectionID != "h1" || !a.asked[0][0].At.Equal(now.Add(-2*time.Minute)) {
		t.Fatalf("asked = %+v over %d days", a.asked, a.days)
	}
	if ev := st.attributed["plain"]; ev.ReferrerSource != "google" || ev.OS != "macos" || ev.Kind != "web" || ev.Host != "app.com" {
		t.Errorf("plain = %+v", ev)
	}
	if ev := st.attributed["sent"]; ev.ReferrerSource != "" || ev.UTMSource != "nl" || ev.OS != "ios" {
		t.Errorf("sent = %+v: the client's source wins, the newest view still applies", ev)
	}
	if len(log.evs) != 2 || len(st.outbox) != 0 {
		t.Errorf("%d events logged, %d left in the outbox; want 2 and none", len(log.evs), len(st.outbox))
	}
}

// A failed lookup leaves submissions pending; past an hour they are
// attributed with what the client sent, so the conversion is not lost,
// and one error line counts them.
func TestAttributeSubmissionsGivesUpAfterAnHour(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{pending: []store.PendingSubmission{
		pendingAt("young", now.Add(-2*time.Minute), nil),
		pendingAt("old", now.Add(-attributionGiveUp), func(e *store.Event) { e.UTMSource = "nl" }),
		pendingAt("older", now.Add(-2*attributionGiveUp), nil),
	}}
	r := attributionRunner(t, st, now).WithAttribution(&fakeAttributor{err: errors.New("engine down")}, &loggedEvents{})
	var buf bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&buf, nil))
	r.attributeSubmissions(context.Background())
	if lines := strings.Count(buf.String(), "level=ERROR msg=\"form attribution: 2 submissions given up"); lines != 1 {
		t.Errorf("%d give-up error lines, want 1:\n%s", lines, buf.String())
	}
	if _, ok := st.attributed["young"]; ok {
		t.Error("a young submission was attributed despite the failed lookup")
	}
	if ev, ok := st.attributed["old"]; !ok || ev.UTMSource != "nl" {
		t.Errorf("old = %+v, %v; want attributed with what it sent", ev, ok)
	}
}

func TestAttributeSubmissionsWithoutAnAttributorDoesNothing(t *testing.T) {
	st := &pendingStore{}
	attributionRunner(t, st, time.Now()).attributeSubmissions(context.Background())
	if !st.before.IsZero() {
		t.Error("a runner without an attributor read the pending submissions")
	}
}

// blockingAttributor waits for its context, as a hung engine would.
type blockingAttributor struct{}

func (blockingAttributor) Attribute(ctx context.Context, _ []archive.Lookup, _ int, _ archive.Rules) ([]archive.Found, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A lookup that never answers is cut off at the timeout, so the minute tick
// (which also rotates the salt and runs the daily pass) goes on; the
// submission stays pending for the next minute.
func TestAttributeSubmissionsBoundsTheLookup(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{pending: []store.PendingSubmission{pendingAt("young", now.Add(-2*time.Minute), nil)}}
	r := attributionRunner(t, st, now).WithAttribution(blockingAttributor{}, &loggedEvents{})
	r.attributionTimeout = 20 * time.Millisecond
	done := make(chan struct{})
	go func() { r.attributeSubmissions(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("attributeSubmissions did not return: the lookup is not bounded")
	}
	if _, ok := st.attributed["young"]; ok {
		t.Error("a young submission was attributed despite the timed-out lookup")
	}
}

// echoAttributor finds a source for every lookup it is asked about.
type echoAttributor struct{ asked []int }

func (e *echoAttributor) Attribute(_ context.Context, ls []archive.Lookup, _ int, _ archive.Rules) ([]archive.Found, error) {
	e.asked = append(e.asked, len(ls))
	out := make([]archive.Found, len(ls))
	for i, l := range ls {
		out[i] = archive.Found{ProjectID: l.ProjectID, ID: l.ID, Source: store.Event{ReferrerSource: "google"}}
	}
	return out, nil
}

func manyPending(n int, at time.Time) []store.PendingSubmission {
	out := make([]store.PendingSubmission, n)
	for i := range out {
		out[i] = pendingAt(fmt.Sprintf("s%03d", i), at, nil)
	}
	return out
}

// A backlog drains in one tick, a batch at a time, while full batches
// keep coming and the tick's budget lasts.
func TestAttributeSubmissionsDrainsABacklogInOneTick(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{pending: manyPending(250, now.Add(-2*time.Minute))}
	a := &echoAttributor{}
	log := &loggedEvents{}
	attributionRunner(t, st, now).WithAttribution(a, log).attributeSubmissions(context.Background())
	if len(st.attributed) != 250 || !slices.Equal(a.asked, []int{100, 100, 50}) {
		t.Fatalf("attributed %d in lookups of %v, want 250 in 100, 100, 50", len(st.attributed), a.asked)
	}
	if len(log.evs) != 250 || !slices.Equal(log.calls, []int{250}) || len(st.outbox) != 0 {
		t.Errorf("%d events logged in calls of %v, %d left; want 250 in one call, none left", len(log.evs), log.calls, len(st.outbox))
	}
}

// A spent budget ends the tick after the batch it is in; the rest wait.
func TestAttributeSubmissionsStopsAtTheTickBudget(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{pending: manyPending(250, now.Add(-2*time.Minute))}
	a := &echoAttributor{}
	r := attributionRunner(t, st, now).WithAttribution(a, &loggedEvents{})
	r.attributionTickBudget = time.Nanosecond
	r.attributeSubmissions(context.Background())
	if len(st.attributed) != attributionBatch || len(a.asked) != 1 {
		t.Fatalf("attributed %d in %d lookups, want one batch", len(st.attributed), len(a.asked))
	}
}

// A failed lookup ends the tick: the next batch would be the same one.
func TestAttributeSubmissionsFailedLookupEndsTheTick(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{pending: manyPending(250, now.Add(-2*time.Minute))}
	a := &fakeAttributor{err: errors.New("engine down")}
	attributionRunner(t, st, now).WithAttribution(a, &loggedEvents{}).attributeSubmissions(context.Background())
	if len(a.asked) != 1 || len(st.attributed) != 0 {
		t.Fatalf("%d lookups, %d attributed; want one failed lookup", len(a.asked), len(st.attributed))
	}
}

func unreadable(id string, at time.Time) store.PendingSubmission {
	return store.PendingSubmission{ProjectID: 1, ID: id, ReceivedAt: at, ConnectionID: "h1", WriteEvent: true,
		Err: errors.New("submission " + id + " pending event: unexpected end of JSON input")}
}

// An unreadable pending row is logged as an error once, by its project
// and id, and cleared: attributed with nothing and no event (whatever
// WriteEvent says), so it leaves the queue. It is not looked up; the
// rows read beside it are attributed all the same.
func TestAttributeSubmissionsClearsUnreadableRows(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{pending: []store.PendingSubmission{
		unreadable("bad", now.Add(-2*time.Minute)), pendingAt("good", now.Add(-2*time.Minute), nil)}}
	a := &echoAttributor{}
	log := &loggedEvents{}
	r := attributionRunner(t, st, now).WithAttribution(a, log)
	var buf bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&buf, nil))
	r.attributeSubmissions(context.Background())
	r.attributeSubmissions(context.Background())
	if _, ok := st.attributed["good"]; !ok {
		t.Error("the readable submission was not attributed")
	}
	if ev, ok := st.attributed["bad"]; !ok || !reflect.DeepEqual(ev, store.Event{}) || st.asWritten["bad"] {
		t.Errorf("bad = %+v, %v, write %v; want cleared with a zero event, not written", ev, ok, st.asWritten["bad"])
	}
	if !slices.Equal(a.asked, []int{1}) {
		t.Errorf("lookups of %v, want one of the readable row only", a.asked)
	}
	var lines int
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "level=ERROR") && strings.Contains(l, "submission=bad") &&
			strings.Contains(l, "project=1") && strings.Contains(l, "unexpected end of JSON input") {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("%d error lines for the unreadable submission, want 1:\n%s", lines, buf.String())
	}
	for _, ev := range log.evs {
		if ev.ID == "bad" || ev.ID == "" {
			t.Errorf("logged %+v for the unreadable submission", ev)
		}
	}
}

// Unreadable rows count as processed: a batch of them does not end the
// tick, so the rows behind them are attributed in the same minute.
func TestAttributeSubmissionsDrainsPastUnreadableRows(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	at := now.Add(-2 * time.Minute)
	var pending []store.PendingSubmission
	for i := 0; i < 100; i++ {
		pending = append(pending, unreadable(fmt.Sprintf("bad%03d", i), at))
	}
	pending = append(pending, manyPending(50, at)...)
	st := &pendingStore{pending: pending}
	a := &echoAttributor{}
	r := attributionRunner(t, st, now).WithAttribution(a, &loggedEvents{})
	r.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r.attributeSubmissions(context.Background())
	if len(st.attributed) != 150 || !slices.Equal(a.asked, []int{50}) {
		t.Fatalf("processed %d in lookups of %v, want 150 with one lookup of 50", len(st.attributed), a.asked)
	}
}

// A draft's submission (WriteEvent false) is attributed, and its event is
// neither written nor logged: it never reaches the outbox.
func TestAttributeSubmissionsLogsOnlyWrittenEvents(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	draft := pendingAt("draft", now.Add(-2*time.Minute), nil)
	draft.WriteEvent = false
	st := &pendingStore{pending: []store.PendingSubmission{draft, pendingAt("approved", now.Add(-2*time.Minute), nil)}}
	log := &loggedEvents{fail: errors.New("disk full")} // keeps the outbox as attribution left it
	r := attributionRunner(t, st, now).WithAttribution(&echoAttributor{}, log)
	r.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r.attributeSubmissions(context.Background())
	if ev, ok := st.attributed["draft"]; !ok || ev.ReferrerSource != "google" {
		t.Errorf("draft = %+v, %v; want attributed", ev, ok)
	}
	if len(st.outbox) != 1 || st.outbox[0].ID != "approved" {
		t.Errorf("outbox = %+v, want only the approved submission's event", st.outbox)
	}
	log.fail = nil
	r.attributeSubmissions(context.Background())
	if len(log.evs) != 1 || log.evs[0].ID != "approved" {
		t.Errorf("logged %+v, want only the approved submission's event", log.evs)
	}
}

// A failed log write is logged and leaves the outbox as it was; the next
// tick, with nothing left to attribute, logs the events and marks them.
func TestAttributeSubmissionsRetriesAFailedLogWrite(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{pending: manyPending(2, now.Add(-2*time.Minute))}
	log := &loggedEvents{fail: errors.New("disk full")}
	r := attributionRunner(t, st, now).WithAttribution(&echoAttributor{}, log)
	var buf bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&buf, nil))
	r.attributeSubmissions(context.Background())
	if len(st.attributed) != 2 || len(st.outbox) != 2 || len(log.evs) != 0 {
		t.Fatalf("attributed %d, %d unlogged, %d logged; want 2, 2, 0", len(st.attributed), len(st.outbox), len(log.evs))
	}
	if !strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), "disk full") {
		t.Errorf("log %q does not report the failed write", buf.String())
	}
	log.fail = nil
	r.attributeSubmissions(context.Background())
	if len(log.evs) != 2 || len(st.outbox) != 0 || log.evs[0].ReferrerSource != "google" {
		t.Fatalf("%d logged (%+v), %d left unlogged; want both logged and marked", len(log.evs), log.evs, len(st.outbox))
	}
}

// The outbox is flushed a page at a time while full pages keep coming;
// an event that does not read is logged as an error and marked with
// the rest, never written.
func TestAttributeSubmissionsFlushesTheOutbox(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{}
	for i := 0; i < 2*outboxPage+500; i++ {
		id := fmt.Sprintf("e%04d", i)
		st.outbox = append(st.outbox, store.UnloggedEvent{ProjectID: 1, ID: id, Event: store.Event{ID: id, ProjectID: 1}})
	}
	st.outbox[3] = store.UnloggedEvent{ProjectID: 1, ID: "e0003", Err: errors.New("submission e0003 unlogged event: unexpected end of JSON input")}
	log := &loggedEvents{}
	r := attributionRunner(t, st, now).WithAttribution(&echoAttributor{}, log)
	var buf bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&buf, nil))
	r.attributeSubmissions(context.Background())
	if !slices.Equal(log.calls, []int{outboxPage - 1, outboxPage, 500}) || len(st.outbox) != 0 {
		t.Fatalf("logged in calls of %v, %d left; want %d, %d and 500, none left", log.calls, len(st.outbox), outboxPage-1, outboxPage)
	}
	if !strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), "submission=e0003") {
		t.Errorf("log %q does not name the unreadable event", buf.String())
	}
}

// A spent budget ends the flush after the page it is in; a runner
// without a log leaves the outbox alone.
func TestAttributeSubmissionsOutboxBudgetAndNoLog(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	fill := func() *pendingStore {
		st := &pendingStore{}
		for i := 0; i < outboxPage+1; i++ {
			st.outbox = append(st.outbox, store.UnloggedEvent{ProjectID: 1, ID: fmt.Sprintf("e%04d", i)})
		}
		return st
	}
	st := fill()
	log := &loggedEvents{}
	r := attributionRunner(t, st, now).WithAttribution(&echoAttributor{}, log)
	r.attributionTickBudget = time.Nanosecond
	r.attributeSubmissions(context.Background())
	if !slices.Equal(log.calls, []int{outboxPage}) || len(st.outbox) != 1 {
		t.Fatalf("logged in calls of %v, %d left; want one page, one left", log.calls, len(st.outbox))
	}
	st = fill()
	attributionRunner(t, st, now).WithAttribution(&echoAttributor{}, nil).attributeSubmissions(context.Background())
	if st.outboxRead != 0 || len(st.outbox) != outboxPage+1 {
		t.Fatalf("a runner without a log read the outbox %d times, left %d", st.outboxRead, len(st.outbox))
	}
}
