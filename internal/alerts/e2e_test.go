package alerts

import (
	"context"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/inbox"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestAlertEndToEnd composes the real pieces (a migrated store, the
// operations, the Evaluator over a fake engine, the Runner and a real
// inbox.Tracker) and follows one alert: created, its check served by a
// tick, its breach recorded and announced as an alert_breached row; then
// edited, which ends the breach silently, so the next ok evaluation
// sends no alert_recovered.
func TestAlertEndToEnd(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t) // the store, Ops and a live project, at 10:00
	logger := slog.New(slog.DiscardHandler)
	eng := &fakeEngine{}
	ev := NewEvaluator(eng, h.projects, h.presence, time.Second)
	tracker := inbox.NewTracker(h.st, 6*time.Hour, logger)
	r := NewRunner(h.st, ev, tracker, logger)
	r.now = func() time.Time { return h.at }
	day := yesterday // h.at is evalNow

	// Created: pending with a check, which the next tick serves.
	v := h.create(t, countIn("signups"))
	if v.State != store.AlertPending || !v.CheckPending() {
		t.Fatalf("created: state %q, check pending %t", v.State, v.CheckPending())
	}
	eng.answer("stat", day, day, statResult("count", "3"))
	h.at = h.at.Add(time.Minute)
	r.Tick(ctx, h.at)

	a, err := h.st.GetAlert(ctx, h.live, v.ID)
	if err != nil || a.State != store.AlertBreached || a.CheckPending() || a.LastValue == nil || *a.LastValue != 3 {
		t.Fatalf("after the check: %+v, %v", a, err)
	}
	ns := notifications(t, h.st)
	if len(ns) != 1 || ns[0].Type != "alert_breached" || ns[0].ProjectID != h.live || ns[0].Subject != "signups" ||
		ns[0].Fields["alert_id"] != strconv.FormatInt(v.ID, 10) {
		t.Fatalf("notifications after the breach: %+v", ns)
	}

	// Edited in the same second: a new revision, pending with a check; the
	// tick drops the breach before it evaluates, so the ok it reads
	// recovers nothing.
	e := h.update(t, v.ID, Patch{WindowDays: ptr(3)})
	if e.Revision != a.Revision+1 || e.State != store.AlertPending || !e.CheckPending() {
		t.Fatalf("edited: %+v", e.Alert)
	}
	eng.answer("stat", day.AddDays(-2), day, statResult("count", "9"))
	h.at = h.at.Add(time.Minute)
	r.Tick(ctx, h.at)

	a, err = h.st.GetAlert(ctx, h.live, v.ID)
	if err != nil || a.State != store.AlertOK || a.CheckPending() {
		t.Fatalf("after the edit's check: %+v, %v", a, err)
	}
	evals, err := h.st.ListAlertEvaluations(ctx, v.ID, 10, 0)
	if err != nil || len(evals) != 2 || evals[0].Outcome != store.AlertOK || evals[0].From != day.AddDays(-2).String() ||
		evals[1].Outcome != store.AlertBreached {
		t.Fatalf("evaluations %+v, %v; want the breach, then the edit's ok", evals, err)
	}
	if ns := notifications(t, h.st); len(ns) != 1 {
		t.Fatalf("notifications after the edit: %+v; want only the breach", ns)
	}
}

// notifications are the store's live notifications.
func notifications(t *testing.T, st store.Store) []store.Notification {
	t.Helper()
	ns, err := st.ListNotifications(context.Background(), store.NotificationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return ns
}
