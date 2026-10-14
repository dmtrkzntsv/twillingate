package jobs

import (
	"context"
	"errors"
	"log/slog"
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

type loggedEvents struct{ evs []store.Event }

func (l *loggedEvents) WriteEvents(_ context.Context, evs []store.Event) error {
	l.evs = append(l.evs, evs...)
	return nil
}

// pendingStore is a real store with the two attribution methods over a
// list of pending submissions.
type pendingStore struct {
	store.Store
	pending    []store.PendingSubmission
	before     time.Time
	attributed map[string]store.Event
}

func (s *pendingStore) PendingSubmissions(_ context.Context, before time.Time, limit int) ([]store.PendingSubmission, error) {
	s.before = before
	var out []store.PendingSubmission
	for _, p := range s.pending {
		if _, done := s.attributed[p.ID]; !done && !p.ReceivedAt.After(before) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *pendingStore) AttributeSubmission(_ context.Context, p store.PendingSubmission, ev store.Event) (bool, error) {
	if s.attributed == nil {
		s.attributed = map[string]store.Event{}
	}
	s.attributed[p.ID] = ev
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
	return store.PendingSubmission{ProjectID: 1, ID: id, ReceivedAt: at, Event: ev, WriteEvent: true}
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
		a.asked[0][0].InstallID != "i1" || !a.asked[0][0].At.Equal(now.Add(-2*time.Minute)) {
		t.Fatalf("asked = %+v over %d days", a.asked, a.days)
	}
	if ev := st.attributed["plain"]; ev.ReferrerSource != "google" || ev.OS != "macos" || ev.Kind != "web" || ev.Host != "app.com" {
		t.Errorf("plain = %+v", ev)
	}
	if ev := st.attributed["sent"]; ev.ReferrerSource != "" || ev.UTMSource != "nl" || ev.OS != "ios" {
		t.Errorf("sent = %+v: the client's source wins, the newest view still applies", ev)
	}
	if len(log.evs) != 2 {
		t.Errorf("%d events loggedEvents, want 2", len(log.evs))
	}
}

// A failed lookup leaves submissions pending; past an hour they are
// attributed with what the client sent, so the conversion is not lost.
func TestAttributeSubmissionsGivesUpAfterAnHour(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st := &pendingStore{pending: []store.PendingSubmission{
		pendingAt("young", now.Add(-2*time.Minute), nil),
		pendingAt("old", now.Add(-attributionGiveUp), func(e *store.Event) { e.UTMSource = "nl" }),
	}}
	r := attributionRunner(t, st, now).WithAttribution(&fakeAttributor{err: errors.New("engine down")}, &loggedEvents{})
	r.attributeSubmissions(context.Background())
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
