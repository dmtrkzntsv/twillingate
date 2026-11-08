package inbox_test

import (
	"context"

	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/inbox"
	"github.com/dmtrkzntsv/twillingate/internal/shared/notify"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// deliveryStore is an in-memory DeliveryStore over notifications by id.
type deliveryStore struct {
	mu   sync.Mutex
	rows []store.Notification
}

func (f *deliveryStore) PendingDeliveries(_ context.Context, now time.Time, limit int) ([]store.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Notification
	for _, n := range f.rows {
		if n.Delivery == store.DeliveryPending && (n.NextAttemptAt == nil || !n.NextAttemptAt.After(now)) && len(out) < limit {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *deliveryStore) SetDelivery(_ context.Context, id int64, delivery, errText string, attempts int, next *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rows {
		if f.rows[i].ID == id {
			f.rows[i].Delivery, f.rows[i].DeliveryError, f.rows[i].Attempts, f.rows[i].NextAttemptAt = delivery, errText, attempts, next
			return nil
		}
	}
	return fmt.Errorf("notification %d: %w", id, store.ErrNotFound)
}

func (f *deliveryStore) row(id int64) store.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.rows {
		if n.ID == id {
			return n
		}
	}
	return store.Notification{}
}

// fakeSender records what it is asked to send and answers with errs in
// turn (nil once they run out).
type fakeSender struct {
	mu        sync.Mutex
	sent      []notify.Message
	deadlines []time.Duration
	errs      []error
}

func (s *fakeSender) Service() string { return "fake" }

func (s *fakeSender) Send(ctx context.Context, m notify.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	if dl, ok := ctx.Deadline(); ok {
		s.deadlines = append(s.deadlines, time.Until(dl))
	}
	if len(s.errs) == 0 {
		return nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return err
}

var dispNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func pending(id int64, created time.Time, attempts int) store.Notification {
	return store.Notification{ID: id, Type: store.NotifyUptimeDown, ProjectID: 1, Subject: "https://example.com",
		Fields:    map[string]string{"project": "Example", "since": created.Format(time.RFC3339), "reason": "timeout"},
		CreatedAt: created, Delivery: store.DeliveryPending, Attempts: attempts}
}

func TestDispatcherSends(t *testing.T) {
	n := pending(1, dispNow.Add(-time.Minute), 0)
	st := &deliveryStore{rows: []store.Notification{n}}
	s := &fakeSender{}
	inbox.NewDispatcher(st, s, "https://console.example.com/", quiet()).Deliver(context.Background(), dispNow)

	want := inbox.Absolute(inbox.Render(n), "https://console.example.com/")
	if len(s.sent) != 1 || s.sent[0] != want {
		t.Fatalf("sent %+v, want [%+v]", s.sent, want)
	}
	if want.Link != "https://console.example.com/app/projects/1/settings/domains" {
		t.Errorf("link %q is not the console page under /app", want.Link)
	}
	if len(s.deadlines) != 1 || s.deadlines[0] > 30*time.Second || s.deadlines[0] < 25*time.Second {
		t.Errorf("send deadlines %v, want about 30s", s.deadlines)
	}
	if got := st.row(1); got.Delivery != store.DeliverySent || got.Attempts != 1 || got.NextAttemptAt != nil {
		t.Errorf("row = %s attempts %d next %v, want sent", got.Delivery, got.Attempts, got.NextAttemptAt)
	}
}

func TestDispatcherNoSender(t *testing.T) {
	st := &deliveryStore{rows: []store.Notification{pending(1, dispNow, 0), pending(2, dispNow.Add(-3*time.Hour), 0)}}
	inbox.NewDispatcher(st, nil, "", quiet()).Deliver(context.Background(), dispNow)
	for _, id := range []int64{1, 2} {
		if got := st.row(id).Delivery; got != store.DeliveryNone {
			t.Errorf("row %d = %s, want none", id, got)
		}
	}
}

func TestDispatcherSkipsOld(t *testing.T) {
	st := &deliveryStore{rows: []store.Notification{
		pending(1, dispNow.Add(-time.Hour-time.Second), 0), // never tried, too old
		pending(2, dispNow.Add(-59*time.Minute), 0),
	}}
	s := &fakeSender{}
	inbox.NewDispatcher(st, s, "", quiet()).Deliver(context.Background(), dispNow)
	if got := st.row(1).Delivery; got != store.DeliverySkipped {
		t.Errorf("old row = %s, want skipped", got)
	}
	if got := st.row(2).Delivery; got != store.DeliverySent {
		t.Errorf("recent row = %s, want sent", got)
	}
	if len(s.sent) != 1 {
		t.Errorf("sent %d messages, want 1", len(s.sent))
	}
}

func TestDispatcherRetriesTemporary(t *testing.T) {
	st := &deliveryStore{rows: []store.Notification{pending(1, dispNow, 0)}}
	tmp := fmt.Errorf("fake: host: status 503: %w", notify.ErrTemporary)
	s := &fakeSender{errs: []error{tmp, tmp, tmp, tmp, tmp}}
	d := inbox.NewDispatcher(st, s, "", quiet())

	now := dispNow
	for i, wait := range []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, time.Hour} {
		d.Deliver(context.Background(), now)
		got := st.row(1)
		if got.Delivery != store.DeliveryPending || got.Attempts != i+1 || got.NextAttemptAt == nil ||
			!got.NextAttemptAt.Equal(now.Add(wait)) || got.DeliveryError != tmp.Error() {
			t.Fatalf("after attempt %d: %s attempts %d next %v error %q, want pending, %d, %v",
				i+1, got.Delivery, got.Attempts, got.NextAttemptAt, got.DeliveryError, i+1, now.Add(wait))
		}
		// Not due yet: nothing is sent.
		d.Deliver(context.Background(), now.Add(wait-time.Second))
		if len(s.sent) != i+1 {
			t.Fatalf("sent %d times before the next attempt was due", len(s.sent))
		}
		now = now.Add(wait)
	}
	d.Deliver(context.Background(), now)
	if got := st.row(1); got.Delivery != store.DeliverySent || got.Attempts != 6 {
		t.Errorf("after recovery: %s attempts %d, want sent 6", got.Delivery, got.Attempts)
	}
}

func TestDispatcherGivesUp(t *testing.T) {
	st := &deliveryStore{rows: []store.Notification{
		pending(1, dispNow.Add(-6*time.Hour-time.Minute), 4),
		pending(2, dispNow.Add(-time.Minute), 0),
	}}
	tmp := fmt.Errorf("fake: dial: %w", notify.ErrTemporary)
	s := &fakeSender{errs: []error{tmp, tmp}}
	inbox.NewDispatcher(st, s, "", quiet()).Deliver(context.Background(), dispNow)
	if got := st.row(1); got.Delivery != store.DeliveryFailed || got.DeliveryError != tmp.Error() || got.NextAttemptAt != nil {
		t.Errorf("old row = %s %q next %v, want failed", got.Delivery, got.DeliveryError, got.NextAttemptAt)
	}
	if got := st.row(2); got.Delivery != store.DeliveryPending || got.Attempts != 1 {
		t.Errorf("new row = %s attempts %d, want pending 1", got.Delivery, got.Attempts)
	}
	if len(s.sent) != 2 {
		t.Errorf("sent %d, want both rows attempted", len(s.sent))
	}
}

func TestDispatcherPermanent(t *testing.T) {
	st := &deliveryStore{rows: []store.Notification{pending(1, dispNow, 0)}}
	perm := fmt.Errorf("fake: host: status 401: %w", notify.ErrPermanent)
	inbox.NewDispatcher(st, &fakeSender{errs: []error{perm}}, "", quiet()).Deliver(context.Background(), dispNow)
	if got := st.row(1); got.Delivery != store.DeliveryFailed || got.DeliveryError != perm.Error() ||
		got.Attempts != 1 || got.NextAttemptAt != nil {
		t.Errorf("row = %s %q attempts %d next %v, want failed", got.Delivery, got.DeliveryError, got.Attempts, got.NextAttemptAt)
	}
}

// TestDispatcherBatch: one pass takes at most 50 rows.
func TestDispatcherBatch(t *testing.T) {
	st := &deliveryStore{}
	for i := range 60 {
		st.rows = append(st.rows, pending(int64(i+1), dispNow, 0))
	}
	s := &fakeSender{}
	inbox.NewDispatcher(st, s, "", quiet()).Deliver(context.Background(), dispNow)
	if len(s.sent) != 50 {
		t.Errorf("one pass sent %d, want 50", len(s.sent))
	}
}

// failingOnce wraps a deliveryStore whose first SetDelivery to "sent" fails.
type failingOnce struct {
	*deliveryStore
	failed bool
}

func (f *failingOnce) SetDelivery(ctx context.Context, id int64, delivery, errText string, attempts int, next *time.Time) error {
	if delivery == store.DeliverySent && !f.failed {
		f.failed = true
		return fmt.Errorf("disk full")
	}
	return f.deliveryStore.SetDelivery(ctx, id, delivery, errText, attempts, next)
}

func TestDispatcherNeverResendsWhenRecordFails(t *testing.T) {
	st := &failingOnce{deliveryStore: &deliveryStore{rows: []store.Notification{pending(1, dispNow.Add(-time.Minute), 0)}}}
	s := &fakeSender{}
	d := inbox.NewDispatcher(st, s, "https://console.example.com/", quiet())
	d.Deliver(context.Background(), dispNow)
	if got := st.row(1); got.Delivery != store.DeliveryPending {
		t.Fatalf("after the failed write: %s, want pending", got.Delivery)
	}
	d.Deliver(context.Background(), dispNow.Add(5*time.Second))
	if len(s.sent) != 1 {
		t.Errorf("sender called %d times, want 1", len(s.sent))
	}
	if got := st.row(1); got.Delivery != store.DeliverySent || got.Attempts != 1 {
		t.Errorf("row = %s attempts %d, want sent 1", got.Delivery, got.Attempts)
	}
}

func TestDispatcherRecordsSentAfterCancel(t *testing.T) {
	st := &deliveryStore{rows: []store.Notification{pending(1, dispNow.Add(-time.Minute), 0)}}
	ctx, cancel := context.WithCancel(context.Background())
	s := &cancelingSender{cancel: cancel}
	inbox.NewDispatcher(st, s, "https://console.example.com/", quiet()).Deliver(ctx, dispNow)
	if got := st.row(1); got.Delivery != store.DeliverySent {
		t.Errorf("row = %s, want sent although ctx was cancelled during the send", got.Delivery)
	}
}

// cancelingSender cancels the pass's context and then reports success.
type cancelingSender struct{ cancel func() }

func (s *cancelingSender) Service() string { return "fake" }
func (s *cancelingSender) Send(context.Context, notify.Message) error {
	s.cancel()
	return nil
}
