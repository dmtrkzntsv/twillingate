package alerts

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/inbox"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// fakeNotifier records what the runner tells the tracker.
type fakeNotifier struct {
	mu    sync.Mutex
	obs   []inbox.AlertObservation
	keeps []map[inbox.AlertKey]int64
}

func (f *fakeNotifier) Alert(_ context.Context, o inbox.AlertObservation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
}

func (f *fakeNotifier) KeepAlerts(_ context.Context, live map[inbox.AlertKey]int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keeps = append(f.keeps, maps.Clone(live))
}

// lastKeep is the live map of the newest KeepAlerts call.
func (f *fakeNotifier) lastKeep(t *testing.T) map[inbox.AlertKey]int64 {
	t.Helper()
	if len(f.keeps) == 0 {
		t.Fatal("KeepAlerts was never called")
	}
	return f.keeps[len(f.keeps)-1]
}

// runHarness is a Runner over a migrated store with one live project, a
// fake evaluate and a fake notifier, on a clock the test moves.
type runHarness struct {
	r       *Runner
	st      store.Store
	n       *fakeNotifier
	project int64
	clock   time.Time
	calls   []int64 // the alerts evaluated, in order
	// next is the outcome of an evaluation; nil is ok over yesterday (none
	// for silence).
	next func(a store.Alert, now time.Time) Outcome
}

func newRunHarness(t *testing.T) *runHarness {
	t.Helper()
	h := &runHarness{st: storetest.Open(t, t.TempDir()+"/runner.db"), n: &fakeNotifier{}}
	h.project = h.newProject(t, "live")
	h.r = h.runner(h.st)
	return h
}

// runner is a Runner over st wired to the harness's clock, evaluate and
// notifier.
func (h *runHarness) runner(st RunnerStore) *Runner {
	r := NewRunner(st, nil, h.n, slog.New(slog.DiscardHandler))
	r.now = func() time.Time { return h.clock }
	r.evaluate = func(_ context.Context, a store.Alert, now time.Time) Outcome {
		h.calls = append(h.calls, a.ID)
		if h.next != nil {
			return h.next(a, now)
		}
		return outcomeAt(a, now, store.AlertOK, "")
	}
	return r
}

func (h *runHarness) newProject(t *testing.T, name string) int64 {
	t.Helper()
	id, err := h.st.CreateProject(context.Background(), store.RegistryProject{Name: name, AllowedOrigins: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// outcomeAt is an evaluation of a at now with a value of 3: over
// yesterday for a metric alert, the newest event 3h before now for
// silence.
func outcomeAt(a store.Alert, now time.Time, outcome, reason string) Outcome {
	o := Outcome{Outcome: outcome, Reason: reason, At: now, Value: ptr(3.0)}
	if a.Def.Condition.Type == condSilence {
		o.Last = now.Add(-3 * time.Hour)
	} else {
		o.From, o.To = yesterday, yesterday
	}
	return o
}

// runAt is today at h:m UTC.
func runAt(h, m int) time.Time {
	return today.Time().Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

// create stores an alert of project named name at the clock's time, so
// its creation check is pending.
func (h *runHarness) create(t *testing.T, project int64, name string, def store.AlertDef, paused bool) store.Alert {
	t.Helper()
	a, err := h.st.CreateAlert(context.Background(), store.Alert{ProjectID: project, Name: name, Def: def, Paused: paused},
		MaxLive, h.clock, store.AuditEntry{Actor: "test", Action: "alert.create"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (h *runHarness) metric(t *testing.T, name string) store.Alert {
	t.Helper()
	return h.create(t, h.project, name, mustBuild(t, countIn(name)), false)
}

func (h *runHarness) silence(t *testing.T, name string) store.Alert {
	t.Helper()
	return h.create(t, h.project, name, mustBuild(t, Input{Where: []native.Filter{evt("signup")}, WhereSet: true, Condition: silent6}), false)
}

// exec runs raw SQL against the harness's store, for seeding.
func (h *runHarness) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := h.st.(interface {
		ExecForTest(string, ...any) (sql.Result, error)
	}).ExecForTest(q, args...); err != nil {
		t.Fatal(err)
	}
}

// clearChecks ends every alert's check request.
func (h *runHarness) clearChecks(t *testing.T) { h.exec(t, `UPDATE alerts SET check_requested_at=''`) }

// tick ticks at at and returns the alerts it evaluated.
func (h *runHarness) tick(at time.Time) []int64 {
	h.clock = at
	before := len(h.calls)
	h.r.Tick(context.Background(), at)
	return h.calls[before:]
}

// passDone signals today's pass at at.
func (h *runHarness) passDone(at time.Time) {
	h.clock = at
	h.r.PassDone(today)
}

func (h *runHarness) get(t *testing.T, a store.Alert) store.Alert {
	t.Helper()
	got, err := h.st.GetAlert(context.Background(), a.ProjectID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func wantCalls(t *testing.T, when string, got []int64, want ...int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: evaluated %v, want %v", when, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: evaluated %v, want %v", when, got, want)
		}
	}
}

func TestRunnerMetricWaitsForThePass(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	h.clearChecks(t)

	h.passDone(runAt(2, 30))
	wantCalls(t, "02:30", h.tick(runAt(2, 30)))

	h.passDone(runAt(3, 4))
	wantCalls(t, "03:05", h.tick(runAt(3, 5)), a.ID)
	if got := h.get(t, a); got.LastDay != yesterday.String() || got.State != store.AlertOK {
		t.Fatalf("after the pass: last day %q, state %q", got.LastDay, got.State)
	}
	wantCalls(t, "03:06", h.tick(runAt(3, 6)))
}

func TestRunnerFallbackAtFour(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	h.clearChecks(t)

	wantCalls(t, "03:30", h.tick(runAt(3, 30)))
	wantCalls(t, "04:00", h.tick(runAt(4, 0)), a.ID)
	wantCalls(t, "04:01", h.tick(runAt(4, 1)))
}

func TestRunnerCatchesUpOnceAfterDowntime(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0).Add(-48 * time.Hour)
	a := h.metric(t, "signups")
	h.clearChecks(t)
	h.exec(t, `UPDATE alerts SET last_day=?`, today.AddDays(-2).String())

	h.passDone(runAt(2, 0)) // the boot's pass
	wantCalls(t, "02:00", h.tick(runAt(2, 0)))
	wantCalls(t, "03:10", h.tick(runAt(3, 10)))

	h.passDone(runAt(3, 20))
	wantCalls(t, "03:20", h.tick(runAt(3, 20)), a.ID)
	ev, err := h.st.ListAlertEvaluations(context.Background(), a.ID, 10, 0)
	if err != nil || len(ev) != 1 || ev[0].To != yesterday.String() || ev[0].At != "2026-11-25T03:20:00Z" {
		t.Fatalf("evaluations %+v, %v; want one to yesterday", ev, err)
	}
	if got := h.get(t, a); got.LastDay != yesterday.String() {
		t.Fatalf("last day %q", got.LastDay)
	}

	h.passDone(runAt(3, 25))
	wantCalls(t, "03:25", h.tick(runAt(3, 25)))
}

func TestRunnerServesCheckRequest(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	now := runAt(10, 0)
	h.exec(t, `UPDATE alerts SET last_day=?, check_requested_at=?`, yesterday.String(), now.Add(-30*time.Second).Format(time.RFC3339))

	wantCalls(t, "10:00", h.tick(now), a.ID)
	if got := h.get(t, a); got.CheckPending() {
		t.Fatalf("check still pending: %+v", got)
	}
	wantCalls(t, "10:01", h.tick(runAt(10, 1)))
}

// TestRunnerServesChecksFirst: a tick serves its check requests before
// the scheduled and silence evaluations, so a check is answered within
// the minute however many alerts the pass made due.
func TestRunnerServesChecksFirst(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	scheduled := h.metric(t, "scheduled")
	quiet := h.silence(t, "quiet")
	checked := h.metric(t, "checked")
	h.clearChecks(t)
	now := runAt(4, 0)
	h.exec(t, `UPDATE alerts SET last_day=?, check_requested_at=? WHERE id=?`,
		yesterday.String(), now.Add(-10*time.Second).Format(time.RFC3339), checked.ID)

	wantCalls(t, "04:00", h.tick(now), checked.ID, scheduled.ID, quiet.ID)
	if got := h.get(t, checked); got.CheckPending() || got.LastDay != yesterday.String() {
		t.Fatalf("checked: %+v", got)
	}
	if got := h.get(t, scheduled); got.LastDay != yesterday.String() {
		t.Fatalf("scheduled: last day %q", got.LastDay)
	}
}

func TestRunnerCheckBeforeThePassKeepsLastDay(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(1, 0)
	a := h.metric(t, "signups")

	wantCalls(t, "01:01", h.tick(runAt(1, 1)), a.ID)
	if got := h.get(t, a); got.LastDay != "" || got.CheckPending() || got.State != store.AlertOK {
		t.Fatalf("after the check: last day %q, pending %t, state %q", got.LastDay, got.CheckPending(), got.State)
	}
	if len(h.n.obs) != 1 || h.n.obs[0].Outcome != store.AlertOK || h.n.obs[0].Day != yesterday.String() {
		t.Fatalf("observations %+v", h.n.obs)
	}
	wantCalls(t, "01:02", h.tick(runAt(1, 2)))

	h.passDone(runAt(3, 4))
	wantCalls(t, "03:05", h.tick(runAt(3, 5)), a.ID)
	if got := h.get(t, a); got.LastDay != yesterday.String() {
		t.Fatalf("after the pass: last day %q", got.LastDay)
	}
}

// failingList fails ListEvaluableAlerts.
type failingList struct{ RunnerStore }

func (failingList) ListEvaluableAlerts(context.Context) ([]store.Alert, error) {
	return nil, errors.New("disk on fire")
}

func TestRunnerKeepsStatesWhenTheListFails(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	h.metric(t, "signups")
	h.r = h.runner(failingList{h.st})

	wantCalls(t, "04:00", h.tick(runAt(4, 0)))
	if len(h.n.keeps) != 0 || len(h.n.obs) != 0 {
		t.Fatalf("keeps %v, observations %v; want none", h.n.keeps, h.n.obs)
	}
}

func TestRunnerSilenceHourly(t *testing.T) {
	h := newRunHarness(t)
	t0 := runAt(10, 0)
	h.clock = t0
	a := h.silence(t, "quiet")
	h.clearChecks(t)
	h.next = func(a store.Alert, now time.Time) Outcome { return outcomeAt(a, now, store.AlertBreached, "") }

	wantCalls(t, "t0", h.tick(t0), a.ID)
	wantCalls(t, "t0+59m", h.tick(t0.Add(59*time.Minute)))
	wantCalls(t, "t0+60m", h.tick(t0.Add(time.Hour)), a.ID)

	got := h.get(t, a)
	if got.LastDay != "" || got.State != store.AlertBreached {
		t.Fatalf("silence: last day %q, state %q", got.LastDay, got.State)
	}
	if len(h.n.obs) != 2 || h.n.obs[0].Daily || h.n.obs[0].Day != "" || h.n.obs[0].Outcome != store.AlertBreached ||
		h.n.obs[0].Fields["value"] != "last event 3h ago" || h.n.obs[0].Fields["detail"] == "" {
		t.Fatalf("observations %+v", h.n.obs)
	}
	ev, err := h.st.ListAlertEvaluations(context.Background(), a.ID, 10, 0)
	if err != nil || len(ev) != 2 || ev[0].From != "" || ev[0].To != "" {
		t.Fatalf("evaluations %+v, %v", ev, err)
	}
}

func TestRunnerRetriesThenBroken(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	// The creation check makes it ok before the pass.
	wantCalls(t, "00:01", h.tick(runAt(0, 1)), a.ID)
	h.n.obs = nil

	h.next = func(a store.Alert, now time.Time) Outcome { return outcomeAt(a, now, store.AlertError, "engine busy.") }
	wantCalls(t, "04:00", h.tick(runAt(4, 0)), a.ID)
	got := h.get(t, a)
	if got.Fails != 1 || got.State != store.AlertOK || got.RetryAt != "2026-11-25T05:00:00Z" || got.LastDay != "" {
		t.Fatalf("first failure: %+v", got)
	}
	wantCalls(t, "04:30", h.tick(runAt(4, 30)))

	wantCalls(t, "05:00", h.tick(runAt(5, 0)), a.ID)
	if got = h.get(t, a); got.Fails != 2 || got.State != store.AlertOK {
		t.Fatalf("second failure: %+v", got)
	}
	if len(h.n.obs) != 0 {
		t.Fatalf("observations before broken: %+v", h.n.obs)
	}

	wantCalls(t, "06:00", h.tick(runAt(6, 0)), a.ID)
	got = h.get(t, a)
	const reason = "Failed 3 times: engine busy"
	if got.Fails != 3 || got.State != store.AlertBroken || got.Reason != reason || got.LastDay != yesterday.String() {
		t.Fatalf("third failure: %+v", got)
	}
	if len(h.n.obs) != 1 || h.n.obs[0].Outcome != store.AlertBroken || h.n.obs[0].Fields["reason"] != reason ||
		h.n.obs[0].Day != yesterday.String() || !h.n.obs[0].Daily || h.n.obs[0].Revision != a.Revision {
		t.Fatalf("observations %+v", h.n.obs)
	}
	ev, err := h.st.ListAlertEvaluations(context.Background(), a.ID, 1, 0)
	if err != nil || len(ev) != 1 || ev[0].Outcome != store.AlertError || ev[0].Error != "engine busy." {
		t.Fatalf("evaluation %+v, %v", ev, err)
	}
	wantCalls(t, "07:00", h.tick(runAt(7, 0)))
}

// TestRunnerRetriesAFailedCheck: a check that fails on an alert already
// evaluated through yesterday is tried again hourly, and is broken on the
// third failure, its last day kept.
func TestRunnerRetriesAFailedCheck(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	h.clearChecks(t)
	wantCalls(t, "04:00", h.tick(runAt(4, 0)), a.ID)
	if got := h.get(t, a); got.LastDay != yesterday.String() || got.State != store.AlertOK {
		t.Fatalf("scheduled: %+v", got)
	}

	h.exec(t, `UPDATE alerts SET check_requested_at=?`, runAt(10, 0).Format(time.RFC3339))
	h.next = func(a store.Alert, now time.Time) Outcome { return outcomeAt(a, now, store.AlertError, "engine busy.") }
	wantCalls(t, "10:00", h.tick(runAt(10, 0)), a.ID)
	got := h.get(t, a)
	if got.Fails != 1 || got.RetryAt != "2026-11-25T11:00:00Z" || got.LastDay != yesterday.String() || got.CheckPending() {
		t.Fatalf("failed check: %+v", got)
	}
	wantCalls(t, "10:30", h.tick(runAt(10, 30)))
	wantCalls(t, "11:00", h.tick(runAt(11, 0)), a.ID)
	if got = h.get(t, a); got.Fails != 2 || got.State != store.AlertOK || got.LastDay != yesterday.String() {
		t.Fatalf("second failure: %+v", got)
	}
	wantCalls(t, "12:00", h.tick(runAt(12, 0)), a.ID)
	if got = h.get(t, a); got.Fails != 3 || got.State != store.AlertBroken || got.LastDay != yesterday.String() {
		t.Fatalf("third failure: %+v", got)
	}
	wantCalls(t, "13:00", h.tick(runAt(13, 0)))
}

// TestRunnerCheckDuringARetryCountsAsScheduled: a check served while a
// failed scheduled evaluation waits to retry evaluates the same days, so
// it writes last_day and the retry does not evaluate them again.
func TestRunnerCheckDuringARetryCountsAsScheduled(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	h.clearChecks(t)

	h.next = func(a store.Alert, now time.Time) Outcome { return outcomeAt(a, now, store.AlertError, "engine busy.") }
	wantCalls(t, "04:00", h.tick(runAt(4, 0)), a.ID)
	if got := h.get(t, a); got.Fails != 1 || got.RetryAt != "2026-11-25T05:00:00Z" || got.LastDay != "" {
		t.Fatalf("failure: %+v", got)
	}

	h.next = nil
	h.exec(t, `UPDATE alerts SET check_requested_at=?`, runAt(4, 30).Format(time.RFC3339))
	wantCalls(t, "04:30", h.tick(runAt(4, 30)), a.ID)
	if got := h.get(t, a); got.LastDay != yesterday.String() || got.Fails != 0 || got.RetryAt != "" {
		t.Fatalf("after the check: %+v", got)
	}
	wantCalls(t, "05:00", h.tick(runAt(5, 0)))
	ev, err := h.st.ListAlertEvaluations(context.Background(), a.ID, 10, 0)
	if err != nil || len(ev) != 2 {
		t.Fatalf("evaluations %+v, %v; want the failure and the check", ev, err)
	}
}

func TestRunnerPassesNoData(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	h.clearChecks(t)

	h.next = func(a store.Alert, now time.Time) Outcome {
		return outcomeAt(a, now, store.AlertNoData, "never sent: signup")
	}
	wantCalls(t, "04:00", h.tick(runAt(4, 0)), a.ID)
	if got := h.get(t, a); got.State != store.AlertNoData || got.Reason != "never sent: signup" {
		t.Fatalf("no data: %+v", got)
	}
	if len(h.n.obs) != 1 || h.n.obs[0].Outcome != store.AlertNoData {
		t.Fatalf("observations %+v", h.n.obs)
	}

	// A check that fails once tells the tracker nothing.
	h.exec(t, `UPDATE alerts SET check_requested_at=?`, runAt(5, 0).Format(time.RFC3339))
	h.next = func(a store.Alert, now time.Time) Outcome { return outcomeAt(a, now, store.AlertError, "timeout") }
	wantCalls(t, "05:00", h.tick(runAt(5, 0)), a.ID)
	if got := h.get(t, a); got.Fails != 1 || got.State != store.AlertNoData {
		t.Fatalf("after a failure: %+v", got)
	}
	if len(h.n.obs) != 1 {
		t.Fatalf("observations %+v", h.n.obs)
	}
}

func TestRunnerEndsStatesOfEditedAlerts(t *testing.T) {
	ctx := context.Background()
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	h.clearChecks(t)
	key := inbox.AlertKey{ProjectID: h.project, ID: a.ID}
	audit := store.AuditEntry{Actor: "test", Action: "alert.update"}

	h.next = func(a store.Alert, now time.Time) Outcome { return outcomeAt(a, now, store.AlertBreached, "") }
	wantCalls(t, "04:00", h.tick(runAt(4, 0)), a.ID)
	if len(h.n.obs) != 1 || h.n.obs[0].Outcome != store.AlertBreached || h.n.obs[0].Revision != 1 {
		t.Fatalf("observations %+v", h.n.obs)
	}
	if keep := h.n.lastKeep(t); len(keep) != 1 || keep[key] != 1 {
		t.Fatalf("keep %v", keep)
	}

	def := mustBuild(t, countIn("signups"))
	def.WindowDays = 7
	edited, err := h.st.UpdateAlert(ctx, h.project, a.ID, store.AlertEdit{Def: &def}, runAt(4, 5), audit)
	if err != nil || edited.Revision != 2 || edited.State != store.AlertPending {
		t.Fatalf("edit: %+v, %v", edited, err)
	}
	h.tick(runAt(4, 6))
	if keep := h.n.lastKeep(t); len(keep) != 1 || keep[key] != 2 {
		t.Fatalf("keep after the edit %v", keep)
	}

	paused := true
	if _, err := h.st.UpdateAlert(ctx, h.project, a.ID, store.AlertEdit{Paused: &paused}, runAt(4, 10), audit); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, "paused", h.tick(runAt(4, 11)))
	if keep := h.n.lastKeep(t); len(keep) != 0 {
		t.Fatalf("keep while paused %v", keep)
	}

	// An edit landing while it is evaluated drops the evaluation.
	paused = false
	if _, err := h.st.UpdateAlert(ctx, h.project, a.ID, store.AlertEdit{Paused: &paused}, runAt(4, 15), audit); err != nil {
		t.Fatal(err)
	}
	h.n.obs = nil
	h.next = func(a store.Alert, now time.Time) Outcome {
		def := a.Def
		def.WindowDays = 3
		if _, err := h.st.UpdateAlert(ctx, a.ProjectID, a.ID, store.AlertEdit{Def: &def}, now, audit); err != nil {
			t.Error(err)
		}
		return outcomeAt(a, now, store.AlertBreached, "")
	}
	wantCalls(t, "edited meanwhile", h.tick(runAt(4, 16)), a.ID)
	if len(h.n.obs) != 0 {
		t.Fatalf("a stale evaluation was observed: %+v", h.n.obs)
	}
	if got := h.get(t, a); got.State != store.AlertPending || got.LastEvaluatedAt == runAt(4, 16).Format(time.RFC3339) {
		t.Fatalf("a stale evaluation was recorded: %+v", got)
	}
}

// TestRunnerArchivedProjectAlerts: an archived project's alerts are not
// evaluated, and archiving and restoring the project between two ticks
// still ends their states: the restored alert is pending at a new
// revision, which the tracker is told.
func TestRunnerArchivedProjectAlerts(t *testing.T) {
	ctx := context.Background()
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	old := h.newProject(t, "old")
	a := h.create(t, old, "signups", mustBuild(t, countIn("signups")), false)
	live := h.metric(t, "live")
	h.clearChecks(t)
	h.next = func(a store.Alert, now time.Time) Outcome { return outcomeAt(a, now, store.AlertBreached, "") }
	wantCalls(t, "04:00", h.tick(runAt(4, 0)), a.ID, live.ID)
	key := inbox.AlertKey{ProjectID: old, ID: a.ID}
	if keep := h.n.lastKeep(t); keep[key] != a.Revision {
		t.Fatalf("keep %v", keep)
	}

	if err := h.st.SetProjectArchived(ctx, old, true, store.AuditEntry{Actor: "test", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, "archived", h.tick(runAt(4, 1)))
	if keep := h.n.lastKeep(t); len(keep) != 1 || keep[inbox.AlertKey{ProjectID: h.project, ID: live.ID}] != live.Revision {
		t.Fatalf("keep while archived %v", keep)
	}

	// Archived and restored between two ticks.
	if err := h.st.SetProjectArchived(ctx, old, false, store.AuditEntry{Actor: "test", Action: "project.restore"}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetProjectArchived(ctx, old, true, store.AuditEntry{Actor: "test", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetProjectArchived(ctx, old, false, store.AuditEntry{Actor: "test", Action: "project.restore"}); err != nil {
		t.Fatal(err)
	}
	got := h.get(t, a)
	if got.State != store.AlertPending || got.Revision <= a.Revision || got.LastDay != "" {
		t.Fatalf("restored project's alert: %+v", got)
	}
	h.tick(runAt(4, 2))
	if keep := h.n.lastKeep(t); keep[key] != got.Revision {
		t.Fatalf("keep after the restore %v, want revision %d", keep, got.Revision)
	}
}

func TestRunnerSkipsPausedAndSnoozed(t *testing.T) {
	ctx := context.Background()
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	plain := h.metric(t, "plain")
	paused := h.create(t, h.project, "paused", mustBuild(t, countIn("paused")), true)
	snoozed := h.metric(t, "snoozed")
	until := runAt(12, 0).Format(time.RFC3339)
	snoozed, err := h.st.UpdateAlert(ctx, h.project, snoozed.ID, store.AlertEdit{SnoozedUntil: &until}, runAt(0, 1),
		store.AuditEntry{Actor: "test", Action: "alert.update"})
	if err != nil {
		t.Fatal(err)
	}
	h.clearChecks(t)

	wantCalls(t, "04:00", h.tick(runAt(4, 0)), plain.ID)
	keep := h.n.lastKeep(t)
	if len(keep) != 1 || keep[inbox.AlertKey{ProjectID: h.project, ID: plain.ID}] != plain.Revision {
		t.Fatalf("keep %v", keep)
	}

	// At its end the snooze no longer holds the alert back.
	wantCalls(t, "12:00", h.tick(runAt(12, 0)), snoozed.ID)
	keep = h.n.lastKeep(t)
	if _, ok := keep[inbox.AlertKey{ProjectID: h.project, ID: paused.ID}]; ok || len(keep) != 2 ||
		keep[inbox.AlertKey{ProjectID: h.project, ID: snoozed.ID}] != snoozed.Revision {
		t.Fatalf("keep after the snooze %v", keep)
	}
}

// TestRunnerPassDoneFromAnotherGoroutine: the pass signals from the jobs
// goroutine while the loop ticks (run under -race), and the metric alert
// it settles is evaluated once, by whichever tick first sees the signal.
func TestRunnerPassDoneFromAnotherGoroutine(t *testing.T) {
	ctx := context.Background()
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	h.clearChecks(t)
	var evaluated atomic.Int32
	r := NewRunner(h.st, nil, h.n, slog.New(slog.DiscardHandler))
	r.evaluate = func(_ context.Context, a store.Alert, now time.Time) Outcome {
		evaluated.Add(1)
		return outcomeAt(a, now, store.AlertOK, "")
	}
	r.now = func() time.Time { return runAt(3, 30) }
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			r.PassDone(today)
		}
	}()
	for range 20 {
		r.Tick(ctx, runAt(3, 31))
	}
	wg.Wait()
	r.Tick(ctx, runAt(3, 31)) // the pass has surely signalled by now
	if n := evaluated.Load(); n != 1 {
		t.Fatalf("evaluated %d times, want once", n)
	}
	if got := h.get(t, a); got.LastDay != yesterday.String() {
		t.Fatalf("last day %q, want %s", got.LastDay, yesterday)
	}
}

func TestRunnerRunTicksUntilCancelled(t *testing.T) {
	h := newRunHarness(t)
	h.clock = runAt(0, 0)
	a := h.metric(t, "signups")
	h.r.step = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.next = func(a store.Alert, now time.Time) Outcome {
		cancel()
		return outcomeAt(a, now, store.AlertOK, "")
	}
	go func() { h.r.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if len(h.calls) != 1 || h.calls[0] != a.ID {
		t.Fatalf("evaluated %v", h.calls)
	}
}
