package alerts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// evalNow is the evaluations' clock: yesterday is 2026-11-24.
var (
	evalNow   = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	today     = civil.DateOf(evalNow)
	yesterday = today.AddDays(-1)
)

// answerKey is what the fake engine answers by: a query's kind and days.
type answerKey struct {
	kind     string
	from, to civil.Date
}

// fakeEngine answers canned results by answerKey, and records the
// requests it was given. A request it has no answer for fails.
type fakeEngine struct {
	answers map[answerKey]native.Result
	err     error // every request's, when set
	block   bool  // wait for the request's context to end
	slow    bool  // wait for the request's context to end, then answer
	reqs    []native.Request
}

func (f *fakeEngine) AnswerAll(ctx context.Context, reqs []native.Request) []native.Answered {
	f.reqs = append(f.reqs, reqs...)
	out := make([]native.Answered, len(reqs))
	for i, r := range reqs {
		if f.block {
			<-ctx.Done()
			out[i].Err = ctx.Err()
			continue
		}
		if f.slow {
			<-ctx.Done()
		}
		if f.err != nil {
			out[i].Err = f.err
			continue
		}
		res, ok := f.answers[answerKey{r.Query.Kind, r.Params.From, r.Params.To}]
		if !ok {
			out[i].Err = fmt.Errorf("no canned answer for %s %s…%s", r.Query.Kind, r.Params.From, r.Params.To)
		}
		out[i].Result = res
	}
	return out
}

func (f *fakeEngine) answer(kind string, from, to civil.Date, res native.Result) {
	if f.answers == nil {
		f.answers = map[answerKey]native.Result{}
	}
	f.answers[answerKey{kind, from, to}] = res
}

// statResult is a stat's one row: columns and values in pairs.
func statResult(kv ...string) native.Result {
	var res native.Result
	var row []string
	for i := 0; i < len(kv); i += 2 {
		res.Columns = append(res.Columns, kv[i])
		row = append(row, kv[i+1])
	}
	res.Rows = [][]string{row}
	return res
}

// funnelResult is a funnel's rows, a value per step.
func funnelResult(values ...int) native.Result {
	res := native.Result{Columns: []string{"step", "value"}}
	for i, v := range values {
		res.Rows = append(res.Rows, []string{"s" + strconv.Itoa(i), strconv.Itoa(v)})
	}
	return res
}

// silenceResult is a silence alert's stat: the newest received_at, "" for none.
func silenceResult(last time.Time) native.Result {
	v := ""
	if !last.IsZero() {
		v = last.UTC().Format(time.RFC3339Nano)
	}
	return statResult("max_received_at", v)
}

// countingPresence is fakePresence counting the questions it is asked.
type countingPresence struct {
	fakePresence
	calls int
}

func (c *countingPresence) HasEvents(ctx context.Context, p int64, f store.Family, name string) (bool, error) {
	c.calls++
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return c.fakePresence.HasEvents(ctx, p, f, name)
}

func (c *countingPresence) HasAttribute(ctx context.Context, p int64, key string) (bool, error) {
	c.calls++
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return c.fakePresence.HasAttribute(ctx, p, key)
}

// evalHarness is an Evaluator over fakes; every event is sent.
type evalHarness struct {
	e        *Evaluator
	eng      *fakeEngine
	projects *fakeProjects
	presence *countingPresence
}

func newEvalHarness() *evalHarness {
	h := &evalHarness{
		eng:      &fakeEngine{},
		projects: &fakeProjects{archived: map[int64]bool{7: false}, renames: map[string]string{}, blocked: map[string]bool{}},
		presence: &countingPresence{fakePresence: fakePresence{sent: map[string]bool{"signup": true, "paid": true, "$page_view": true}}},
	}
	h.e = NewEvaluator(h.eng, h.projects, h.presence, time.Second)
	return h
}

// alertOf is in built and stored as project 7's alert.
func alertOf(t *testing.T, in Input) store.Alert {
	t.Helper()
	return store.Alert{ID: 1, ProjectID: 7, Name: "a", Def: mustBuild(t, in)}
}

func (h *evalHarness) evaluate(a store.Alert) Outcome {
	return h.e.Evaluate(context.Background(), a, evalNow)
}

func countAlert(t *testing.T, cond store.AlertCondition, window int) store.Alert {
	t.Helper()
	in := statIn(store.AlertRead{}, cond, native.Metric{Op: "count"})
	in.WindowDays = window
	return alertOf(t, in)
}

func silenceAlert(t *testing.T, hours int) store.Alert {
	t.Helper()
	return alertOf(t, Input{Where: []native.Filter{evt("signup")}, WhereSet: true,
		Condition: store.AlertCondition{Type: "silence", Hours: hours}})
}

func wantOutcome(t *testing.T, o Outcome, outcome, reason string) {
	t.Helper()
	if o.Outcome != outcome || o.Reason != reason {
		t.Fatalf("outcome = %q (%q), want %q (%q); %+v", o.Outcome, o.Reason, outcome, reason, o)
	}
}

func wantValue(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, deref(got), want)
	}
}

func TestEvaluateThresholdBelow(t *testing.T) {
	h := newEvalHarness()
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "3"))
	o := h.evaluate(countAlert(t, below5, 1))
	wantOutcome(t, o, store.AlertBreached, "")
	wantValue(t, "Value", o.Value, 3)
	if o.From != yesterday || o.To != yesterday || !o.At.Equal(evalNow) {
		t.Errorf("window %s…%s at %v, want %s at %v", o.From, o.To, o.At, yesterday, evalNow)
	}
	if o.Compare != nil || o.Volume != nil {
		t.Errorf("compare %v, volume %v, want none", deref(o.Compare), deref(o.Volume))
	}
	if len(h.eng.reqs) != 1 || h.eng.reqs[0].Params.Project != 7 {
		t.Errorf("requests = %+v", h.eng.reqs)
	}

	h.eng.answer("stat", yesterday, yesterday, statResult("count", "5"))
	wantOutcome(t, h.evaluate(countAlert(t, below5, 1)), store.AlertOK, "")

	// A seven-day window ends yesterday.
	h.eng.answer("stat", yesterday.AddDays(-6), yesterday, statResult("count", "9"))
	above := store.AlertCondition{Type: "threshold", Op: "above", Value: ptr(5.0)}
	o = h.evaluate(countAlert(t, above, 7))
	wantOutcome(t, o, store.AlertBreached, "")
	if o.From != yesterday.AddDays(-6) || o.To != yesterday {
		t.Errorf("window %s…%s", o.From, o.To)
	}
}

func TestEvaluateNeverSentIsNoData(t *testing.T) {
	h := newEvalHarness()
	h.presence.sent["signup"] = false
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "0"))
	wantOutcome(t, h.evaluate(countAlert(t, below5, 1)), store.AlertNoData, "never sent: signup")

	h.eng.answer("stat", yesterday, today, silenceResult(time.Time{}))
	o := h.evaluate(silenceAlert(t, 6))
	wantOutcome(t, o, store.AlertNoData, "never sent: signup")
	if o.Value != nil || !o.Last.IsZero() {
		t.Errorf("value %v, last %v, want none", deref(o.Value), o.Last)
	}

	// An attribute is asked about too, as attr_<key>.
	in := statIn(store.AlertRead{}, below5, native.Metric{Op: "count"})
	in.Query.Where = append(in.Query.Where, native.Filter{Column: "attr_plan", Op: "eq", Values: []string{"pro"}})
	h.presence.sent["signup"] = true
	wantOutcome(t, h.evaluate(alertOf(t, in)), store.AlertNoData, "never sent: attr_plan")

}

// TestEvaluatePresenceFailureIsError: a value of 0 or none whose
// presence question fails is an error (retried, then broken), never
// judged: an event never sent must not read as a breach.
func TestEvaluatePresenceFailureIsError(t *testing.T) {
	const reason = "could not check whether signup was ever sent: disk on fire"
	h := newEvalHarness()
	h.presence.err = errors.New("disk on fire")
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "0"))
	o := h.evaluate(countAlert(t, below5, 1))
	wantOutcome(t, o, store.AlertError, reason)
	wantValue(t, "Value", o.Value, 0)

	h.eng.answer("stat", yesterday, today, silenceResult(time.Time{}))
	wantOutcome(t, h.evaluate(silenceAlert(t, 6)), store.AlertError, reason)

	// A value above 0 asks nothing, so a failing presence does not matter.
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "3"))
	wantOutcome(t, h.evaluate(countAlert(t, below5, 1)), store.AlertBreached, "")
}

// TestEvaluatePresenceHasItsOwnTimeout: queries that use up the timeout
// leave presence its own, as long.
func TestEvaluatePresenceHasItsOwnTimeout(t *testing.T) {
	h := newEvalHarness()
	h.e = NewEvaluator(h.eng, h.projects, h.presence, 20*time.Millisecond)
	h.eng.slow = true
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "0"))
	wantOutcome(t, h.evaluate(countAlert(t, below5, 1)), store.AlertBreached, "")
	if h.presence.calls != 1 {
		t.Errorf("presence asked %d times, want 1", h.presence.calls)
	}
}

func TestEvaluateStartsOnceSent(t *testing.T) {
	h := newEvalHarness()
	h.presence.sent["signup"] = false
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "0"))
	a := countAlert(t, below5, 1)
	wantOutcome(t, h.evaluate(a), store.AlertNoData, "never sent: signup")
	if h.presence.calls != 1 {
		t.Fatalf("presence asked %d times, want 1", h.presence.calls)
	}

	h.presence.sent["signup"] = true
	wantOutcome(t, h.evaluate(a), store.AlertBreached, "")
	wantOutcome(t, h.evaluate(a), store.AlertBreached, "")
	if h.presence.calls != 2 {
		t.Errorf("presence asked %d times, want 2 (a sent event is remembered)", h.presence.calls)
	}

	// A presence failure is not remembered: it is asked again.
	h2 := newEvalHarness()
	h2.presence.err = errors.New("busy")
	h2.eng.answer("stat", yesterday, yesterday, statResult("count", "0"))
	h2.evaluate(a)
	h2.evaluate(a)
	if h2.presence.calls != 2 {
		t.Errorf("presence asked %d times after failures, want 2", h2.presence.calls)
	}

	// A value above 0 needs no presence.
	h3 := newEvalHarness()
	h3.eng.answer("stat", yesterday, yesterday, statResult("count", "3"))
	h3.evaluate(a)
	if h3.presence.calls != 0 {
		t.Errorf("presence asked %d times over a count of 3, want 0", h3.presence.calls)
	}
}

func TestEvaluateCountZeroIsReal(t *testing.T) {
	h := newEvalHarness()
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "0"))
	below1 := store.AlertCondition{Type: "threshold", Op: "below", Value: ptr(1.0)}
	o := h.evaluate(countAlert(t, below1, 1))
	wantOutcome(t, o, store.AlertBreached, "")
	wantValue(t, "Value", o.Value, 0)
}

func TestEvaluateEmptyRateIsNoData(t *testing.T) {
	h := newEvalHarness()
	h.eng.answer("stat", yesterday, yesterday, statResult("avg_value", "", "count", "0"))
	a := alertOf(t, statIn(store.AlertRead{Metric: "avg_value", Volume: "count"}, below5,
		native.Metric{Op: "avg", Column: "value"}, native.Metric{Op: "count"}))
	o := h.evaluate(a)
	wantOutcome(t, o, store.AlertNoData, "no value")
	if o.Value != nil {
		t.Errorf("value = %v, want none", deref(o.Value))
	}
	wantValue(t, "Volume", o.Volume, 0)

	// A funnel whose first step is 0.
	h.eng.answer("funnel", yesterday, yesterday, funnelResult(0, 0, 0))
	o = h.evaluate(alertOf(t, funnelIn(store.AlertRead{Step: "signup"})))
	wantOutcome(t, o, store.AlertNoData, "no value")
}

func TestEvaluateMinVolume(t *testing.T) {
	h := newEvalHarness()
	h.eng.answer("funnel", yesterday, yesterday, funnelResult(10, 3, 1))
	in := funnelIn(store.AlertRead{Step: "signup"})
	in.MinVolume = 50
	o := h.evaluate(alertOf(t, in))
	wantOutcome(t, o, store.AlertNoData, "below min_volume")
	wantValue(t, "Value", o.Value, 0.3)
	wantValue(t, "Volume", o.Volume, 10)

	in.MinVolume = 10
	wantOutcome(t, h.evaluate(alertOf(t, in)), store.AlertBreached, "")

	// A stat's volume metric.
	h.eng.answer("stat", yesterday, yesterday, statResult("avg_value", "2", "count", "4"))
	rate := statIn(store.AlertRead{Metric: "avg_value", Volume: "count"}, below5,
		native.Metric{Op: "avg", Column: "value"}, native.Metric{Op: "count"})
	rate.MinVolume = 5
	o = h.evaluate(alertOf(t, rate))
	wantOutcome(t, o, store.AlertNoData, "below min_volume")
	wantValue(t, "Volume", o.Volume, 4)

	// A change on a count measures its comparison.
	h.eng.answer("stat", yesterday.AddDays(-1), yesterday.AddDays(-1), statResult("count", "1"))
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "4"))
	rise := store.AlertCondition{Type: "change", Direction: "rise", Percent: 50, Against: "previous"}
	change := statIn(store.AlertRead{}, rise, native.Metric{Op: "count"})
	change.MinVolume = 2
	o = h.evaluate(alertOf(t, change))
	wantOutcome(t, o, store.AlertNoData, "below min_volume")
	wantValue(t, "Volume", o.Volume, 1)
	change.MinVolume = 0
	wantOutcome(t, h.evaluate(alertOf(t, change)), store.AlertBreached, "")
}

// conversionChange is a two-step conversion alert under a change
// against against, over window days.
func conversionChange(t *testing.T, against string, percent float64, window int) store.Alert {
	t.Helper()
	in := funnelIn(store.AlertRead{Step: "signup"})
	in.Query.Steps = in.Query.Steps[:2]
	in.Condition = store.AlertCondition{Type: "change", Direction: "drop", Percent: percent, Against: against}
	in.WindowDays = window
	return alertOf(t, in)
}

func TestEvaluateChangeWeekAgo(t *testing.T) {
	h := newEvalHarness()
	from := yesterday.AddDays(-2)
	h.eng.answer("funnel", from, yesterday, funnelResult(1000, 28))
	h.eng.answer("funnel", from.AddDays(-7), yesterday.AddDays(-7), funnelResult(1000, 40))
	o := h.evaluate(conversionChange(t, "week_ago", 30, 3))
	wantOutcome(t, o, store.AlertBreached, "")
	wantValue(t, "Value", o.Value, 0.028)
	wantValue(t, "Compare", o.Compare, 0.040)
	wantValue(t, "Volume", o.Volume, 1000)
	if len(h.eng.reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(h.eng.reqs))
	}
	if p := h.eng.reqs[1].Params; p.From != from.AddDays(-7) || p.To != yesterday.AddDays(-7) {
		t.Errorf("comparison window %s…%s, want %s…%s", p.From, p.To, from.AddDays(-7), yesterday.AddDays(-7))
	}

	wantOutcome(t, h.evaluate(conversionChange(t, "week_ago", 31, 3)), store.AlertOK, "")
}

func TestEvaluateChangePrevious(t *testing.T) {
	h := newEvalHarness()
	from := yesterday.AddDays(-2)
	h.eng.answer("funnel", from, yesterday, funnelResult(1000, 28))
	h.eng.answer("funnel", from.AddDays(-3), from.AddDays(-1), funnelResult(1000, 40))
	wantOutcome(t, h.evaluate(conversionChange(t, "previous", 30, 3)), store.AlertBreached, "")
	if p := h.eng.reqs[1].Params; p.From != from.AddDays(-3) || p.To != from.AddDays(-1) {
		t.Errorf("comparison window %s…%s", p.From, p.To)
	}

	// A rise is never a drop; either takes both.
	h.eng.answer("funnel", from, yesterday, funnelResult(1000, 60))
	a := conversionChange(t, "previous", 30, 3)
	wantOutcome(t, h.evaluate(a), store.AlertOK, "")
	a.Def.Condition.Direction = "either"
	wantOutcome(t, h.evaluate(a), store.AlertBreached, "")
	a.Def.Condition.Direction = "rise"
	wantOutcome(t, h.evaluate(a), store.AlertBreached, "")
}

func TestEvaluateChangeZeroComparisonIsNoData(t *testing.T) {
	h := newEvalHarness()
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "10"))
	h.eng.answer("stat", yesterday.AddDays(-7), yesterday.AddDays(-7), statResult("count", "0"))
	cond := store.AlertCondition{Type: "change", Direction: "either", Percent: 10, Against: "week_ago"}
	o := h.evaluate(countAlert(t, cond, 1))
	wantOutcome(t, o, store.AlertNoData, "comparison is 0 or empty")
	wantValue(t, "Compare", o.Compare, 0)

	// An empty comparison: a funnel whose first step was 0.
	h.eng.answer("funnel", yesterday, yesterday, funnelResult(100, 20))
	h.eng.answer("funnel", yesterday.AddDays(-7), yesterday.AddDays(-7), funnelResult(0, 0))
	wantOutcome(t, h.evaluate(conversionChange(t, "week_ago", 10, 1)), store.AlertNoData, "comparison is 0 or empty")
}

func TestEvaluateFunnelFromPrevious(t *testing.T) {
	h := newEvalHarness()
	h.eng.answer("funnel", yesterday, yesterday, funnelResult(100, 40, 10))
	o := h.evaluate(alertOf(t, funnelIn(store.AlertRead{Step: "paid", From: "previous"})))
	wantOutcome(t, o, store.AlertBreached, "")
	wantValue(t, "Value", o.Value, 0.25)
	wantValue(t, "Volume", o.Volume, 40)

	o = h.evaluate(alertOf(t, funnelIn(store.AlertRead{Step: "paid"})))
	wantValue(t, "Value from start", o.Value, 0.1)
	wantValue(t, "Volume from start", o.Volume, 100)
}

func TestEvaluateSilenceNoneInWindowIsBreached(t *testing.T) {
	h := newEvalHarness()
	h.eng.answer("stat", yesterday, today, silenceResult(time.Time{}))
	o := h.evaluate(silenceAlert(t, 6))
	wantOutcome(t, o, store.AlertBreached, "")
	if o.Value != nil || !o.Last.IsZero() {
		t.Errorf("value %v, last %v, want none", deref(o.Value), o.Last)
	}
	if o.From != (civil.Date{}) || o.To != (civil.Date{}) {
		t.Errorf("window %s…%s, want none", o.From, o.To)
	}
}

func TestEvaluateSilence(t *testing.T) {
	h := newEvalHarness()
	last := evalNow.Add(-7 * time.Hour)
	h.eng.answer("stat", yesterday, today, silenceResult(last))
	o := h.evaluate(silenceAlert(t, 6))
	wantOutcome(t, o, store.AlertBreached, "")
	wantValue(t, "Value", o.Value, 7)
	if !o.Last.Equal(last) {
		t.Errorf("Last = %v, want %v", o.Last, last)
	}
	if p := h.eng.reqs[0].Params; p.From != yesterday || p.To != today {
		t.Errorf("window %s…%s, want the 2 days %s…%s", p.From, p.To, yesterday, today)
	}

	h.eng.answer("stat", yesterday, today, silenceResult(evalNow.Add(-2*time.Hour)))
	wantOutcome(t, h.evaluate(silenceAlert(t, 6)), store.AlertOK, "")
	if h.presence.calls != 0 {
		t.Errorf("presence asked %d times, want 0", h.presence.calls)
	}

	// 30 hours read ceil(30/24) + 1 = 3 days.
	h.eng.answer("stat", today.AddDays(-2), today, silenceResult(evalNow.Add(-29*time.Hour)))
	wantOutcome(t, h.evaluate(silenceAlert(t, 30)), store.AlertOK, "")
}

func TestEvaluateRenamedIsBroken(t *testing.T) {
	h := newEvalHarness()
	h.projects.renames[ruleKey(store.FamilyProduct, "signup")] = "sign_up"
	o := h.evaluate(countAlert(t, below5, 3))
	wantOutcome(t, o, store.AlertBroken, "Renamed to sign_up: signup")
	if len(h.eng.reqs) != 0 {
		t.Errorf("%d requests, want none", len(h.eng.reqs))
	}
	if o.To != yesterday || o.From != yesterday.AddDays(-2) {
		t.Errorf("window %s…%s: a broken outcome keeps its window", o.From, o.To)
	}
}

func TestEvaluateBlockedIsBroken(t *testing.T) {
	h := newEvalHarness()
	h.projects.blocked[ruleKey(store.FamilyMeasures, "signup")] = true
	wantOutcome(t, h.evaluate(countAlert(t, below5, 1)), store.AlertBroken, "Blocked: signup")

	// A $ event takes no rules.
	h2 := newEvalHarness()
	h2.projects.blocked[ruleKey(store.FamilyProduct, "$page_view")] = true
	h2.eng.answer("funnel", yesterday, yesterday, funnelResult(10, 5, 1))
	wantOutcome(t, h2.evaluate(alertOf(t, funnelIn(store.AlertRead{Step: "signup"}))), store.AlertBreached, "")
}

// TestEvaluateAttributeRuleIsBroken: an attribute an attribute rule
// renames, merges or blocks breaks the alert, even one presence already
// saw (which is never asked again).
func TestEvaluateAttributeRuleIsBroken(t *testing.T) {
	h := newEvalHarness()
	in := statIn(store.AlertRead{}, below5, native.Metric{Op: "count"})
	in.Query.Where = append(in.Query.Where, native.Filter{Column: "attr_plan", Op: "eq", Values: []string{"pro"}})
	a := alertOf(t, in)
	h.presence.sent["attr_plan"] = true
	h.eng.answer("stat", yesterday, yesterday, statResult("count", "0"))
	wantOutcome(t, h.evaluate(a), store.AlertBreached, "")

	h.projects.attrRenames = map[string]string{"plan": "tier"}
	wantOutcome(t, h.evaluate(a), store.AlertBroken, "Renamed to attr_tier: attr_plan")

	h.projects.attrRenames = nil
	h.projects.attrBlocked = map[string]bool{"plan": true}
	wantOutcome(t, h.evaluate(a), store.AlertBroken, "Blocked: attr_plan")
	if n := len(h.eng.reqs); n != 1 {
		t.Errorf("%d requests, want only the first evaluation's", n)
	}
}

func TestEvaluateInvalidQueryIsBroken(t *testing.T) {
	h := newEvalHarness()
	h.eng.err = fmt.Errorf("%w: where[0].values: \"x\" is not a day.", native.ErrInvalid)
	o := h.evaluate(countAlert(t, below5, 1))
	wantOutcome(t, o, store.AlertBroken, `Query: where[0].values: "x" is not a day`)
	if o.To != yesterday {
		t.Errorf("To = %s, want %s", o.To, yesterday)
	}

	// A stored definition that no longer parses.
	a := countAlert(t, below5, 1)
	a.Def.Query = []byte(`{"kind": "trend"}`)
	o = h.evaluate(a)
	if o.Outcome != store.AlertBroken || !strings.HasPrefix(o.Reason, "Query.kind: ") {
		t.Errorf("unparsable: %q (%q)", o.Outcome, o.Reason)
	}
	if o.To != yesterday || o.From != yesterday {
		t.Errorf("unparsable window %s…%s", o.From, o.To)
	}
}

func TestEvaluateRuntimeErrorIsError(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, fmt.Errorf("%w (about 9 MiB counted)", native.ErrTooLarge)} {
		h := newEvalHarness()
		h.eng.err = err
		o := h.evaluate(countAlert(t, below5, 1))
		wantOutcome(t, o, store.AlertError, err.Error())
		if o.To != yesterday {
			t.Errorf("To = %s, want %s", o.To, yesterday)
		}
	}

	// The timeout bounds the queries.
	h := newEvalHarness()
	h.e = NewEvaluator(h.eng, h.projects, h.presence, 10*time.Millisecond)
	h.eng.block = true
	wantOutcome(t, h.evaluate(countAlert(t, below5, 1)), store.AlertError, context.DeadlineExceeded.Error())

	// An answer that does not read as the query's is an error too.
	for name, res := range map[string]native.Result{
		"no rows":       {Columns: []string{"count"}},
		"no column":     statResult("other", "3"),
		"not a number":  statResult("count", "many"),
		"short funnel":  funnelResult(10),
		"bad timestamp": statResult("max_received_at", "yesterday"),
		"short silence": {Columns: []string{"max_received_at"}, Rows: [][]string{{}}},
	} {
		h := newEvalHarness()
		a := countAlert(t, below5, 1)
		switch name {
		case "short funnel":
			a = alertOf(t, funnelIn(store.AlertRead{Step: "signup"}))
			h.eng.answer("funnel", yesterday, yesterday, res)
		case "bad timestamp", "short silence":
			a = silenceAlert(t, 6)
			h.eng.answer("stat", yesterday, today, res)
		default:
			h.eng.answer("stat", yesterday, yesterday, res)
		}
		if o := h.evaluate(a); o.Outcome != store.AlertError || o.Reason == "" {
			t.Errorf("%s: %q (%q), want an error", name, o.Outcome, o.Reason)
		}
	}
}

func TestJudge(t *testing.T) {
	p := Parsed{Kind: "stat", Read: readCount, Volume: -1, Def: store.AlertDef{
		Condition: store.AlertCondition{Type: "change", Direction: "drop", Percent: 30, Against: "previous"}}}
	for _, c := range []struct {
		v, c    *float64
		outcome string
	}{
		{ptr(70.0), ptr(100.0), store.AlertBreached},
		{ptr(71.0), ptr(100.0), store.AlertOK},
		{ptr(140.0), ptr(100.0), store.AlertOK},
		{ptr(0.0), ptr(100.0), store.AlertBreached},
		{nil, ptr(100.0), store.AlertNoData},
		{ptr(5.0), nil, store.AlertNoData},
	} {
		if got, _ := judge(p, c.v, c.c, nil); got != c.outcome {
			t.Errorf("judge(%v, %v) = %q, want %q", deref(c.v), deref(c.c), got, c.outcome)
		}
	}
}
