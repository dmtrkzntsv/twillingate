package alerts

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/inbox"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The loop's timing: an alert is broken after maxFails failed tries in a
// row, a failed metric evaluation is tried again retryEvery, and a
// silence alert is evaluated every silenceEvery. A metric alert waits for
// the day's pass when it signals at or after passHour, and runs without
// it from fallbackHour.
const (
	maxFails     = 3
	retryEvery   = time.Hour
	silenceEvery = time.Hour
	passHour     = 3
	fallbackHour = 4
)

// RunnerStore is the slice of the store the loop uses.
type RunnerStore interface {
	ListEvaluableAlerts(ctx context.Context) ([]store.Alert, error)
	ListCheckPendingAlerts(ctx context.Context) ([]store.Alert, error)
	RecordAlertEvaluation(ctx context.Context, r store.AlertResult) (bool, error)
}

// Notifier is what the loop tells of each evaluation: *inbox.Tracker.
type Notifier interface {
	Alert(ctx context.Context, o inbox.AlertObservation)
	KeepAlerts(ctx context.Context, live map[inbox.AlertKey]int64)
}

// Runner is the loop that evaluates alerts: a metric alert once a day
// after the daily pass (one catch-up for yesterday after downtime), a
// silence alert hourly, a check request within a minute, and a failed
// evaluation again an hour later until it is broken. Each tick it also
// tells the notifier which alerts are live, so it ends the states of the
// others.
type Runner struct {
	st     RunnerStore
	n      Notifier
	logger *slog.Logger

	evaluate func(context.Context, store.Alert, time.Time) Outcome
	now      func() time.Time
	step     time.Duration // between ticks

	mu     sync.Mutex
	passOn civil.Date // the day of the last pass signalled
	passAt time.Time  // when it was signalled
}

// NewRunner returns the loop over st, evaluating with ev and telling n.
func NewRunner(st RunnerStore, ev *Evaluator, n Notifier, logger *slog.Logger) *Runner {
	return &Runner{st: st, n: n, logger: logger, evaluate: ev.Evaluate, now: time.Now, step: time.Minute}
}

// PassDone records that the daily pass of day ended, now. It never
// blocks, so a pass ending after Run has stopped is harmless.
func (r *Runner) PassDone(day civil.Date) {
	at := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.passOn, r.passAt = day, at
}

// afterPass reports whether today's days are settled at now: today's
// pass signalled at or after passHour, or it is fallbackHour or later. A
// pass at boot, before passHour, does not count.
func (r *Runner) afterPass(now time.Time) bool {
	day := civil.DateOf(now)
	if now.Hour() >= fallbackHour {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.passOn == day && !r.passAt.Before(day.Time().Add(passHour*time.Hour))
}

// Run ticks every minute until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(r.step)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Tick(ctx, r.now())
		}
	}
}

// Tick tells the notifier which alerts are live and evaluates the ones
// due at now, those with a check request first, and before each
// scheduled or silence evaluation the checks requested since: a long
// drain must not hold one up. An alert is evaluated at most once a tick.
// A failed list ends the tick before the notifier hears of it, which
// would otherwise end every state.
func (r *Runner) Tick(ctx context.Context, now time.Time) {
	now = now.UTC()
	list, err := r.st.ListEvaluableAlerts(ctx)
	if err != nil {
		r.logger.Error("alerts: list", "error", err)
		return
	}
	stamp := stampOf(now)
	afterPass := r.afterPass(now)
	live := map[inbox.AlertKey]int64{}
	var due []store.Alert
	for _, a := range list {
		if a.Paused || a.SnoozedUntil > stamp {
			continue
		}
		live[inbox.AlertKey{ProjectID: a.ProjectID, ID: a.ID}] = a.Revision
		if a.CheckPending() || scheduled(a, now, afterPass) || retryDue(a, now) || silenceDue(a, now) {
			due = append(due, a)
		}
	}
	r.n.KeepAlerts(ctx, live)
	// Check requests first: one is promised within a minute, while the
	// scheduled and silence evaluations can wait for the next tick.
	slices.SortStableFunc(due, func(a, b store.Alert) int {
		switch ac, bc := a.CheckPending(), b.CheckPending(); {
		case ac == bc:
			return 0
		case ac:
			return -1
		}
		return 1
	})
	done := map[int64]bool{} // the alerts evaluated this tick
	for _, a := range due {
		if !a.CheckPending() {
			r.serveChecks(ctx, afterPass, done)
		}
		if ctx.Err() != nil {
			return
		}
		if done[a.ID] {
			continue
		}
		done[a.ID] = true
		r.evaluateOne(ctx, a, now, daysDue(a, now, afterPass))
	}
}

// serveChecks evaluates the alerts whose check is pending now, but those
// done this tick (whose request waits for the next) or paused or snoozed,
// each at the time it is served, so the evaluation is not older than the
// request. A failed read is logged and leaves them to the next tick.
func (r *Runner) serveChecks(ctx context.Context, afterPass bool, done map[int64]bool) {
	if ctx.Err() != nil {
		return
	}
	list, err := r.st.ListCheckPendingAlerts(ctx)
	if err != nil {
		r.logger.Error("alerts: list check requests", "error", err)
		return
	}
	for _, a := range list {
		now := r.now().UTC()
		if ctx.Err() != nil {
			return
		}
		if done[a.ID] || a.Paused || a.SnoozedUntil > stampOf(now) {
			continue
		}
		done[a.ID] = true
		r.evaluateOne(ctx, a, now, daysDue(a, now, afterPass))
	}
}

// daysDue reports whether metric alert a's days are due at now: not yet
// evaluated through yesterday, and the days settled (afterPass). Any
// evaluation of a then is its scheduled one, which writes last_day, a
// check served while a retry waits included.
func daysDue(a store.Alert, now time.Time, afterPass bool) bool {
	return a.Def.Condition.Type != condSilence && afterPass &&
		a.LastDay < civil.DateOf(now).AddDays(-1).String()
}

// scheduled reports whether metric alert a is due at now by its schedule:
// its days due and not waiting to retry.
func scheduled(a store.Alert, now time.Time, afterPass bool) bool {
	return daysDue(a, now, afterPass) && (a.RetryAt == "" || a.RetryAt <= stampOf(now))
}

// retryDue reports whether metric alert a failed fewer than maxFails times
// in a row and its retry is due at now, however far its days are
// evaluated: a failed check is tried again too.
func retryDue(a store.Alert, now time.Time) bool {
	return a.Def.Condition.Type != condSilence && a.Fails > 0 && a.Fails < maxFails &&
		a.RetryAt != "" && a.RetryAt <= stampOf(now)
}

// silenceDue reports whether silence alert a is due at now: not evaluated
// since it was created, reset to pending or asked for a check
// (last_evaluated_at empty), or last evaluated silenceEvery ago or more.
func silenceDue(a store.Alert, now time.Time) bool {
	if a.Def.Condition.Type != condSilence {
		return false
	}
	if a.LastEvaluatedAt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, a.LastEvaluatedAt)
	return err != nil || !now.Before(last.Add(silenceEvery))
}

// evaluateOne evaluates a at now, records it and, when the record applied, tells
// the notifier. onSchedule is whether this is a's scheduled evaluation
// of its days, which then counts as done (last_day).
func (r *Runner) evaluateOne(ctx context.Context, a store.Alert, now time.Time, onSchedule bool) {
	o := r.evaluate(ctx, a, now)
	if ctx.Err() != nil {
		return
	}
	if o.At.IsZero() {
		o.At = now
	}
	stamp := stampOf(now)
	res := store.AlertResult{
		AlertID:  a.ID,
		Revision: a.Revision,
		Eval: store.AlertEvaluation{AlertID: a.ID, At: stamp, From: dayOf(o.From), To: dayOf(o.To),
			Value: o.Value, Compare: o.Compare, Volume: o.Volume, Outcome: o.Outcome},
	}
	if o.Outcome != store.AlertOK && o.Outcome != store.AlertBreached {
		res.Eval.Error = o.Reason
	}
	if onSchedule {
		res.LastDay = dayOf(o.To)
	}
	outcome := o.Outcome
	if o.Outcome == store.AlertError {
		res.Fails = a.Fails + 1
		res.RetryAt = stampOf(now.Add(retryEvery))
		if res.Fails < maxFails {
			res.LastDay = "" // last_day kept: the evaluation is tried again at RetryAt
		} else {
			outcome = store.AlertBroken
			res.State = store.AlertBroken
			res.Reason = sentence("failed " + strconv.Itoa(res.Fails) + " times: " + o.Reason)
		}
	} else {
		res.State, res.Reason = o.Outcome, o.Reason
	}
	applied, err := r.st.RecordAlertEvaluation(ctx, res)
	if err != nil {
		r.logger.Error("alerts: record an evaluation", "alert", a.ID, "error", err)
		return
	}
	if !applied || res.State == "" {
		return
	}
	obs := inbox.AlertObservation{
		Key:      inbox.AlertKey{ProjectID: a.ProjectID, ID: a.ID},
		Revision: a.Revision,
		Name:     a.Name,
		Daily:    a.Def.Condition.Type != condSilence,
		Day:      dayOf(o.To),
		Outcome:  outcome,
		At:       now,
	}
	if outcome == store.AlertBroken {
		obs.Fields = map[string]string{"reason": res.Reason}
	} else if p, err := Parse(a.Def); err == nil {
		value, detail := message(p, o)
		obs.Fields = map[string]string{"value": value, "detail": detail}
	}
	r.n.Alert(ctx, obs)
}

// stampOf is t as the store keeps times: RFC 3339 in UTC, whole seconds,
// so that strings compare as times.
func stampOf(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// dayOf is d as YYYY-MM-DD, "" for the zero date (silence).
func dayOf(d civil.Date) string {
	if d == (civil.Date{}) {
		return ""
	}
	return d.String()
}
