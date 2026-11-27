package alerts

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The days a backtest covers: a stat's and a funnel's, ending yesterday
// (a funnel answers each day alone, so fewer), and a silence alert's,
// ending today. silenceBuckets bounds a silence breakdown, an hour a
// row (native's most): 31 days of hours fit in one, and the late days
// before them are asked silenceChunkDays at a time.
const (
	backtestStatDays    = 90
	backtestFunnelDays  = 30
	backtestSilenceDays = 30
	silenceBuckets      = 1000
	silenceChunkDays    = silenceBuckets / 24
)

// BacktestDay is how an alert would have evaluated the window ending
// Day.
type BacktestDay struct {
	Day                    civil.Date
	Value, Compare, Volume *float64 // nil where it had none, as Outcome's
	Outcome                string   // store.AlertOK, AlertBreached or AlertNoData
}

// Gap is a stretch longer than a silence alert's hours with no matching
// event received.
type Gap struct {
	From, To time.Time
	Hours    float64
}

// Backtest is an alert run over past days: a metric alert's Days, oldest
// first, or a silence alert's Gaps, oldest first. Fired is how often it
// would have fired: the runs of breached days, or the gaps.
type Backtest struct {
	Days  []BacktestDay
	Gaps  []Gap
	Fired int
}

// Backtest runs p over past days at now, reading only: a stat over the
// 90 days ending yesterday, a funnel over 30, each day judged as
// Evaluate judges it but without asking whether its events were ever
// sent; a silence alert's gaps from the start of the day 30 days back to now. A query the
// engine refuses is store.ErrInvalid, one past the time limit
// store.ErrTimeout.
func (e *Evaluator) Backtest(ctx context.Context, projectID int64, p Parsed, now time.Time) (Backtest, error) {
	now = now.UTC()
	if e.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}
	if p.Kind == condSilence {
		return e.backtestSilence(ctx, projectID, p, now)
	}
	return e.backtestDays(ctx, projectID, p, now)
}

// backtestDays judges each day's window of the metric alert p, asking
// every window once: the days' own, and a change's comparisons, most of
// which are other days' windows.
func (e *Evaluator) backtestDays(ctx context.Context, projectID int64, p Parsed, now time.Time) (Backtest, error) {
	n := backtestStatDays
	if p.Kind == "funnel" {
		n = backtestFunnelDays
	}
	rules := e.projects.Rules(ctx)
	var reqs []native.Request
	asked := map[[2]civil.Date]int{} // a window's index in reqs
	ask := func(from, to civil.Date) int {
		k := [2]civil.Date{from, to}
		i, ok := asked[k]
		if !ok {
			i = len(reqs)
			asked[k] = i
			reqs = append(reqs, native.Request{Query: p.Query, Params: native.Params{Project: projectID, From: from, To: to}, Rules: rules})
		}
		return i
	}
	type windows struct{ value, compare int } // compare -1: none
	days := make([]windows, n)
	last := civil.DateOf(now).AddDays(-1)
	for i := range days {
		to := last.AddDays(i - (n - 1))
		from := windowFrom(p.Def, to)
		days[i] = windows{ask(from, to), -1}
		if p.Def.Condition.Type == condChange {
			days[i].compare = ask(comparison(p, from, to))
		}
	}
	results, err := e.answer(ctx, reqs)
	if err != nil {
		return Backtest{}, err
	}

	bt := Backtest{Days: make([]BacktestDay, n)}
	for i, w := range days {
		r, err := read(p, results[w.value], now)
		if err != nil {
			return Backtest{}, err
		}
		d := BacktestDay{Day: last.AddDays(i - (n - 1)), Value: r.value}
		if w.compare >= 0 {
			cmp, err := read(p, results[w.compare], now)
			if err != nil {
				return Backtest{}, err
			}
			d.Compare = cmp.value
		}
		d.Volume = volumeOf(p, r.volume, d.Compare)
		d.Outcome, _ = judge(p, d.Value, d.Compare, d.Volume)
		bt.Days[i] = d
	}
	bt.Fired = firings(bt.Days)
	return bt, nil
}

// firings counts the runs of breached days: an ok day ends a run, a
// no_data day neither starts nor ends one.
func firings(days []BacktestDay) int {
	n, breached := 0, false
	for _, d := range days {
		switch d.Outcome {
		case store.AlertBreached:
			if !breached {
				n++
			}
			breached = true
		case store.AlertOK:
			breached = false
		}
	}
	return n
}

// backtestSilence finds the gaps longer than p's hours from the start of
// the day 30 days back to now (so at least 720 hours, the most an alert
// may name): a breakdown by day and hour reads each bucket's first and
// last received_at, and the gaps are the stretches between them, from the
// window's start and to now. The LateDays days before the
// window are read too, in breakdowns of their own, since an event
// received in it can be filed (by its clamped ts) that much earlier.
// Events received late put a bucket's span out of time order, so the
// spans are sorted, and a gap is measured from the latest end yet.
func (e *Evaluator) backtestSilence(ctx context.Context, projectID int64, p Parsed, now time.Time) (Backtest, error) {
	today := civil.DateOf(now)
	from := today.AddDays(-backtestSilenceDays) // a whole day back, so the span is over 720 hours at any hour
	q := native.Query{
		Kind: "breakdown", Table: p.Query.Table, Where: p.Query.Where,
		Dimensions: []string{"day", "hour"},
		Metrics:    []native.Metric{{Op: "min", Column: "received_at"}, {Op: "max", Column: "received_at"}},
		Limit:      silenceBuckets,
	}
	rules := e.projects.Rules(ctx)
	request := func(from, to civil.Date) native.Request {
		return native.Request{Query: q, Params: native.Params{Project: projectID, From: from, To: to}, Rules: rules}
	}
	reqs := []native.Request{request(from, today)}
	for d := from.AddDays(-max(e.LateDays, 1)); d.Before(from); d = d.AddDays(silenceChunkDays) {
		to := d.AddDays(silenceChunkDays - 1)
		if !to.Before(from) {
			to = from.AddDays(-1)
		}
		reqs = append(reqs, request(d, to))
	}
	results, err := e.answer(ctx, reqs)
	if err != nil {
		return Backtest{}, err
	}
	var spans [][2]time.Time
	for _, res := range results {
		s, err := receivedSpans(q, res)
		if err != nil {
			return Backtest{}, err
		}
		spans = append(spans, s...)
	}
	slices.SortFunc(spans, func(a, b [2]time.Time) int { return a[0].Compare(b[0]) })

	var bt Backtest
	limit := time.Duration(p.Def.Condition.Hours) * time.Hour
	gap := func(from, to time.Time) {
		if to.Sub(from) > limit {
			bt.Gaps = append(bt.Gaps, Gap{From: from, To: to, Hours: to.Sub(from).Hours()})
		}
	}
	at := from.Time() // the latest received_at yet
	for _, s := range spans {
		gap(at, s[0])
		if s[1].After(at) {
			at = s[1]
		}
	}
	gap(at, now)
	bt.Fired = len(bt.Gaps)
	return bt, nil
}

// receivedSpans are the first and last received_at of each of res's
// buckets, the answer to q.
func receivedSpans(q native.Query, res native.Result) ([][2]time.Time, error) {
	if res.Truncated {
		return nil, fmt.Errorf("the breakdown of received_at has more than %d buckets", q.Limit)
	}
	var cols [2]int
	for i, m := range q.Metrics {
		if cols[i] = slices.Index(res.Columns, m.Name()); cols[i] < 0 {
			return nil, fmt.Errorf("the breakdown's answer has no column %s", m.Name())
		}
	}
	spans := make([][2]time.Time, 0, len(res.Rows))
	for _, row := range res.Rows {
		var s [2]time.Time
		for i, c := range cols {
			if c >= len(row) {
				return nil, fmt.Errorf("the answer's row has %d values, not %d", len(row), c+1)
			}
			t, err := time.Parse(time.RFC3339Nano, row[c])
			if err != nil {
				return nil, fmt.Errorf("the received_at %q is not a time", row[c])
			}
			s[i] = t.UTC()
		}
		spans = append(spans, s)
	}
	return spans, nil
}

// answer answers reqs, in order. A query the engine refuses is
// store.ErrInvalid, before any other failure; a run past the time limit
// is store.ErrTimeout.
func (e *Evaluator) answer(ctx context.Context, reqs []native.Request) ([]native.Result, error) {
	answers := e.eng.AnswerAll(ctx, reqs)
	if len(answers) != len(reqs) {
		return nil, fmt.Errorf("the engine gave %d answers to %d queries", len(answers), len(reqs))
	}
	for _, an := range answers {
		if errors.Is(an.Err, native.ErrInvalid) {
			return nil, invalid("query: %s", strings.TrimPrefix(an.Err.Error(), native.ErrInvalid.Error()+": "))
		}
	}
	out := make([]native.Result, len(answers))
	for i, an := range answers {
		switch {
		case an.Err == nil:
			out[i] = an.Result
		case e.timeout > 0 && (errors.Is(an.Err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)):
			return nil, store.Refuse(store.ErrTimeout, "the backtest ran past its time limit of %s (CONSOLE_QUERY_TIMEOUT)", e.timeout)
		default:
			return nil, an.Err
		}
	}
	return out, nil
}

// Preview backtests in (validated by Build, and its events resolved as
// Create does) or, with in nil, the stored alert id. It reads only, in
// whichever process answers, bounded by Timeout; a process that cannot
// query the event files refuses it (store.ErrInvalid).
func (o *Ops) Preview(ctx context.Context, projectID int64, in *Input, id int64) (Backtest, error) {
	if _, err := o.project(ctx, projectID); err != nil {
		return Backtest{}, err
	}
	if o.eng == nil {
		return Backtest{}, invalid("the event files cannot be queried by this process")
	}
	var def store.AlertDef
	if in != nil {
		var err error
		if def, err = Build(*in); err != nil {
			return Backtest{}, err
		}
	} else {
		a, err := o.st.GetAlert(ctx, projectID, id)
		if err != nil {
			return Backtest{}, err
		}
		def = a.Def
	}
	p, err := Parse(def)
	if err != nil {
		return Backtest{}, err
	}
	if err := o.refuseRuleHit(ctx, projectID, p); err != nil {
		return Backtest{}, err
	}
	ev := NewEvaluator(o.eng, o.projects, nil, o.Timeout)
	ev.LateDays = o.LateDays
	return ev.Backtest(ctx, projectID, p, o.now())
}
