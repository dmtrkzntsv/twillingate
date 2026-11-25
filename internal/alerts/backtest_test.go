package alerts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func (h *evalHarness) backtest(t *testing.T, p Parsed, now time.Time) Backtest {
	t.Helper()
	bt, err := h.e.Backtest(context.Background(), 7, p, now)
	if err != nil {
		t.Fatalf("Backtest: %v", err)
	}
	return bt
}

// answerDays answers the stat window of window days ending each of the
// days days ending yesterday with count v(day).
func answerDays(eng *fakeEngine, days, window int, v func(to civil.Date) string) {
	for i := range days {
		to := yesterday.AddDays(-i)
		eng.answer("stat", to.AddDays(-(window - 1)), to, statResult("count", v(to)))
	}
}

func TestBacktestStatDays(t *testing.T) {
	h := newEvalHarness()
	// Every window counts 10 but the one ending 3 days ago, 6: a drop of 40%.
	dropped := yesterday.AddDays(-3)
	answerDays(h.eng, 90+7, 7, func(to civil.Date) string {
		if to == dropped {
			return "6"
		}
		return "10"
	})
	in := statIn(store.AlertRead{}, drop30, native.Metric{Op: "count"})
	in.WindowDays = 7
	bt := h.backtest(t, mustParse(t, in), evalNow)

	if len(bt.Days) != 90 || bt.Days[89].Day != yesterday || bt.Days[0].Day != yesterday.AddDays(-89) {
		t.Fatalf("%d days, %v…%v, want 90 ending %s", len(bt.Days), bt.Days[0].Day, bt.Days[len(bt.Days)-1].Day, yesterday)
	}
	if len(h.eng.reqs) != 90+7 {
		t.Errorf("%d requests, want %d", len(h.eng.reqs), 90+7)
	}
	seen := map[[2]civil.Date]bool{}
	for _, r := range h.eng.reqs {
		k := [2]civil.Date{r.Params.From, r.Params.To}
		if seen[k] {
			t.Errorf("window %s…%s asked twice", k[0], k[1])
		}
		seen[k] = true
		if r.Params.Project != 7 || r.Params.To.AddDays(-6) != r.Params.From {
			t.Errorf("request %+v, want project 7 over 7 days", r.Params)
		}
	}

	d := bt.Days[86]
	if d.Day != dropped || d.Outcome != store.AlertBreached {
		t.Fatalf("day %s is %s, want %s breached", d.Day, d.Outcome, dropped)
	}
	wantValue(t, "Value", d.Value, 6)
	wantValue(t, "Compare", d.Compare, 10)
	wantValue(t, "Volume", d.Volume, 10) // a change on a count measures the comparison
	if bt.Days[85].Outcome != store.AlertOK || bt.Days[87].Outcome != store.AlertOK {
		t.Errorf("days around the drop: %s, %s, want ok", bt.Days[85].Outcome, bt.Days[87].Outcome)
	}
	if bt.Fired != 1 || bt.Gaps != nil {
		t.Errorf("Fired %d, gaps %v, want 1 and none", bt.Fired, bt.Gaps)
	}

	// week_ago compares with the window ending 7 days earlier, also 90 + 7.
	h.eng.reqs = nil
	in.Condition = store.AlertCondition{Type: "change", Direction: "drop", Percent: 30, Against: "week_ago"}
	h.backtest(t, mustParse(t, in), evalNow)
	if len(h.eng.reqs) != 90+7 {
		t.Errorf("week_ago: %d requests, want %d", len(h.eng.reqs), 90+7)
	}
}

func TestBacktestFiredCountsRuns(t *testing.T) {
	h := newEvalHarness()
	last7 := []string{"9", "3", "3", "", "3", "9", "3"} // ok, breached, breached, no_data, breached, ok, breached
	answerDays(h.eng, 90, 1, func(to civil.Date) string {
		if back := int(yesterday.Time().Sub(to.Time()).Hours() / 24); back < 7 {
			return last7[6-back]
		}
		return "9"
	})
	h.presence.sent = map[string]bool{} // never sent: the backtest asks no one
	bt := h.backtest(t, mustParse(t, statIn(store.AlertRead{}, below5, native.Metric{Op: "count"})), evalNow)

	var got []string
	for _, d := range bt.Days[83:] {
		got = append(got, d.Outcome)
	}
	want := []string{store.AlertOK, store.AlertBreached, store.AlertBreached, store.AlertNoData, store.AlertBreached, store.AlertOK, store.AlertBreached}
	if !slices.Equal(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	if bt.Fired != 2 {
		t.Errorf("Fired = %d, want 2", bt.Fired)
	}
	if bt.Days[86].Value != nil || bt.Days[86].Compare != nil {
		t.Errorf("the no_data day: value %v, compare %v, want none", deref(bt.Days[86].Value), deref(bt.Days[86].Compare))
	}
	if h.presence.calls != 0 {
		t.Errorf("presence asked %d times, want 0", h.presence.calls)
	}
}

func TestBacktestFunnel30Days(t *testing.T) {
	h := newEvalHarness()
	for i := range 30 {
		d := yesterday.AddDays(-i)
		h.eng.answer("funnel", d, d, funnelResult(100, 50, 10))
	}
	in := funnelIn(store.AlertRead{Step: "paid"})
	in.Condition = store.AlertCondition{Type: "threshold", Op: "below", Value: ptr(0.2)}
	bt := h.backtest(t, mustParse(t, in), evalNow)
	if len(bt.Days) != 30 || bt.Days[29].Day != yesterday || len(h.eng.reqs) != 30 {
		t.Fatalf("%d days, %d requests, want 30 ending %s", len(bt.Days), len(h.eng.reqs), yesterday)
	}
	d := bt.Days[29]
	wantValue(t, "Value", d.Value, 0.1)
	wantValue(t, "Volume", d.Volume, 100)
	if d.Outcome != store.AlertBreached || bt.Fired != 1 {
		t.Errorf("outcome %s, fired %d, want breached once", d.Outcome, bt.Fired)
	}
}

// silenceNow is just after the last day's 23h bucket.
var silenceNow = time.Date(2026, 11, 25, 23, 50, 0, 0, time.UTC)

// bucketRows are the silence breakdown's rows, newest first: every hour
// of the 31 days ending silenceNow's, its events received from :15 to
// :45, but the last day's hours skip.
func bucketRows(skip func(h int) bool) native.Result {
	res := native.Result{Columns: []string{"day", "hour", "min_received_at", "max_received_at"}}
	last := civil.DateOf(silenceNow)
	for i := range 31 {
		d := last.AddDays(-i)
		for h := 23; h >= 0; h-- {
			if i == 0 && skip(h) {
				continue
			}
			at := d.Time().Add(time.Duration(h) * time.Hour)
			res.Rows = append(res.Rows, []string{d.String(), fmt.Sprintf("%02d", h),
				at.Add(15 * time.Minute).Format(time.RFC3339Nano), at.Add(45 * time.Minute).Format(time.RFC3339Nano)})
		}
	}
	return res
}

func TestBacktestSilenceGaps(t *testing.T) {
	h := newEvalHarness()
	last := civil.DateOf(silenceNow)
	from := last.AddDays(-30)
	res := bucketRows(func(h int) bool { return h >= 6 && h <= 13 })
	h.eng.answer("breakdown", from, last, res)
	// The day before the window, where an event received in it can be
	// filed (INGEST_LATE_DAYS, 1 by default): nothing.
	h.eng.answer("breakdown", from.AddDays(-1), from.AddDays(-1), native.Result{Columns: res.Columns})

	bt := h.backtest(t, mustParse(t, Input{Where: []native.Filter{evt("signup")}, WhereSet: true,
		Condition: store.AlertCondition{Type: "silence", Hours: 6}}), silenceNow)
	if len(bt.Gaps) != 1 || bt.Fired != 1 || bt.Days != nil {
		t.Fatalf("gaps %+v, fired %d, days %d, want one gap", bt.Gaps, bt.Fired, len(bt.Days))
	}
	g := bt.Gaps[0]
	gapFrom := last.Time().Add(5*time.Hour + 45*time.Minute)
	gapTo := last.Time().Add(14*time.Hour + 15*time.Minute)
	if !g.From.Equal(gapFrom) || !g.To.Equal(gapTo) || math.Abs(g.Hours-8.5) > 1e-9 {
		t.Errorf("gap %+v, want %v…%v, 8.5 hours", g, gapFrom, gapTo)
	}
	if len(h.eng.reqs) != 2 {
		t.Fatalf("%d requests, want the window's and the day before it", len(h.eng.reqs))
	}
	r := h.eng.reqs[0]
	if err := r.Query.Validate(); err != nil {
		t.Errorf("the breakdown does not validate: %v", err)
	}
	q := r.Query
	if q.Kind != "breakdown" || !slices.Equal(q.Dimensions, []string{"day", "hour"}) || q.Limit != 1000 ||
		!sameJSON(q.Metrics, []native.Metric{{Op: "min", Column: "received_at"}, {Op: "max", Column: "received_at"}}) ||
		len(q.Where) != 1 || q.Where[0].Values[0] != "signup" {
		t.Errorf("query %+v", q)
	}
	if r.Params.Project != 7 || r.Params.From != from || r.Params.To != last {
		t.Errorf("params %+v, want project 7 over %s…%s", r.Params, from, last)
	}

	silence := func(hours int) Parsed {
		return mustParse(t, Input{Where: []native.Filter{evt("signup")}, WhereSet: true,
			Condition: store.AlertCondition{Type: "silence", Hours: hours}})
	}
	if bt := h.backtest(t, silence(12), silenceNow); len(bt.Gaps) != 0 || bt.Fired != 0 {
		t.Errorf("hours 12: gaps %+v, fired %d, want none", bt.Gaps, bt.Fired)
	}

	// Events received late (a bucket of yesterday's 23h, received until
	// 14:20) close the gap, though their bucket is a day earlier.
	late := bucketRows(func(h int) bool { return h >= 6 && h <= 13 })
	for _, row := range late.Rows {
		if row[0] == last.AddDays(-1).String() && row[1] == "23" {
			row[3] = last.Time().Add(14*time.Hour + 20*time.Minute).Format(time.RFC3339Nano)
		}
	}
	h.eng.answer("breakdown", from, last, late)
	if bt := h.backtest(t, silence(6), silenceNow); len(bt.Gaps) != 0 {
		t.Errorf("late events: gaps %+v, want none", bt.Gaps)
	}

	// The last hours since the newest event are a gap too.
	h.eng.answer("breakdown", from, last, bucketRows(func(h int) bool { return h >= 14 }))
	if bt := h.backtest(t, silence(6), silenceNow); len(bt.Gaps) != 1 || !bt.Gaps[0].To.Equal(silenceNow) {
		t.Errorf("the end: gaps %+v, want one to now", bt.Gaps)
	}

	// With no events at all, one gap covers the whole window.
	h.eng.answer("breakdown", from, last, native.Result{Columns: res.Columns})
	bt = h.backtest(t, silence(6), silenceNow)
	if len(bt.Gaps) != 1 || bt.Fired != 1 || !bt.Gaps[0].From.Equal(from.Time()) || !bt.Gaps[0].To.Equal(silenceNow) {
		t.Fatalf("no events: gaps %+v, fired %d, want one from %v to %v", bt.Gaps, bt.Fired, from.Time(), silenceNow)
	}
	if want := silenceNow.Sub(from.Time()).Hours(); math.Abs(bt.Gaps[0].Hours-want) > 1e-9 {
		t.Errorf("no events: %v hours, want %v", bt.Gaps[0].Hours, want)
	}
}

// TestBacktestSilenceLateFiled: the backtest also reads the
// INGEST_LATE_DAYS days before its window, in requests of their own, so
// an event received inside the window but filed (its clamped ts) on a day
// before it closes the window's leading gap, which is still measured from
// the window's start.
func TestBacktestSilenceLateFiled(t *testing.T) {
	h := newEvalHarness()
	h.e.LateDays = 2
	last := civil.DateOf(silenceNow)
	from := last.AddDays(-30)
	// The window's first day has no bucket before 10h.
	res := bucketRows(func(int) bool { return false })
	rows := res.Rows[:0]
	for _, row := range res.Rows {
		if row[0] == from.String() && row[1] < "10" {
			continue
		}
		rows = append(rows, row)
	}
	res.Rows = rows
	h.eng.answer("breakdown", from, last, res)
	silence := mustParse(t, Input{Where: []native.Filter{evt("signup")}, WhereSet: true,
		Condition: store.AlertCondition{Type: "silence", Hours: 6}})

	// Nothing filed before the window: a gap from its start to 10:15.
	empty := native.Result{Columns: res.Columns}
	h.eng.answer("breakdown", from.AddDays(-2), from.AddDays(-1), empty)
	bt := h.backtest(t, silence, silenceNow)
	if len(bt.Gaps) != 1 || !bt.Gaps[0].From.Equal(from.Time()) || !bt.Gaps[0].To.Equal(from.Time().Add(10*time.Hour+15*time.Minute)) {
		t.Fatalf("nothing late: gaps %+v, want one from the window's start to 10:15", bt.Gaps)
	}
	pre := h.eng.reqs[len(h.eng.reqs)-1]
	if pre.Params.From != from.AddDays(-2) || pre.Params.To != from.AddDays(-1) || pre.Query.Kind != "breakdown" || pre.Query.Limit != silenceBuckets {
		t.Errorf("the days before the window: %+v over %s…%s", pre.Query, pre.Params.From, pre.Params.To)
	}

	// An event filed on the day before the window, received at its 5h,
	// closes the gap; one received before the window starts none.
	h.eng.answer("breakdown", from.AddDays(-2), from.AddDays(-1), native.Result{Columns: res.Columns, Rows: [][]string{
		{from.AddDays(-2).String(), "03", from.AddDays(-2).Time().Add(3 * time.Hour).Format(time.RFC3339Nano), from.AddDays(-2).Time().Add(3 * time.Hour).Format(time.RFC3339Nano)},
		{from.AddDays(-1).String(), "23", from.AddDays(-1).Time().Add(23 * time.Hour).Format(time.RFC3339Nano), from.Time().Add(5 * time.Hour).Format(time.RFC3339Nano)},
	}})
	if bt := h.backtest(t, silence, silenceNow); len(bt.Gaps) != 0 || bt.Fired != 0 {
		t.Errorf("filed the day before: gaps %+v, want none", bt.Gaps)
	}

	// Late days past what one breakdown holds are asked in several.
	h.e.LateDays = 50
	h.eng.reqs = nil
	h.eng.answer("breakdown", from.AddDays(-50), from.AddDays(-10), empty)
	h.eng.answer("breakdown", from.AddDays(-9), from.AddDays(-1), empty)
	h.backtest(t, silence, silenceNow)
	if len(h.eng.reqs) != 3 {
		t.Errorf("%d requests, want the window's and two before it", len(h.eng.reqs))
	}

	// A preview reads them too.
	oh := newHarness(t)
	eng := &fakeEngine{answers: h.eng.answers}
	oh.o.eng, oh.o.LateDays = eng, 2
	in := Input{Where: []native.Filter{evt("signup")}, WhereSet: true, Condition: store.AlertCondition{Type: "silence", Hours: 6}}
	oh.at = silenceNow
	if _, err := oh.o.Preview(context.Background(), oh.live, &in, 0); err != nil {
		t.Fatal(err)
	}
	if len(eng.reqs) != 2 || eng.reqs[1].Params.From != from.AddDays(-2) {
		t.Errorf("preview requests %+v, want the window's and the 2 days before it", eng.reqs)
	}
}

// shortEngine answers no request.
type shortEngine struct{}

func (shortEngine) AnswerAll(context.Context, []native.Request) []native.Answered { return nil }

func TestBacktestFailures(t *testing.T) {
	ctx := context.Background()
	count := func(t *testing.T) Parsed {
		return mustParse(t, statIn(store.AlertRead{}, below5, native.Metric{Op: "count"}))
	}
	silence := func(t *testing.T) Parsed {
		return mustParse(t, Input{Where: []native.Filter{evt("signup")}, WhereSet: true, Condition: silent6})
	}

	h := newEvalHarness()
	h.eng.err = fmt.Errorf("%w: unknown column", native.ErrInvalid)
	_, err := h.e.Backtest(ctx, 7, count(t), evalNow)
	mustInvalid(t, err, "query: unknown column")

	h.eng.err = errors.New("disk on fire")
	if _, err := h.e.Backtest(ctx, 7, silence(t), evalNow); err == nil || errors.Is(err, store.ErrInvalid) {
		t.Errorf("engine failure: err = %v, want it as it is", err)
	}

	h.eng.err, h.eng.block = nil, true
	h.e.timeout = time.Millisecond
	if _, err := h.e.Backtest(ctx, 7, count(t), evalNow); !errors.Is(err, store.ErrTimeout) {
		t.Errorf("slow engine: err = %v, want ErrTimeout", err)
	}

	h.e.eng = shortEngine{}
	if _, err := h.e.Backtest(ctx, 7, count(t), evalNow); err == nil {
		t.Error("no answers: no error")
	}

	h = newEvalHarness()
	answerDays(h.eng, 90, 1, func(civil.Date) string { return "many" })
	if _, err := h.e.Backtest(ctx, 7, count(t), evalNow); err == nil {
		t.Error("a count that is not a number: no error")
	}

	last := civil.DateOf(evalNow)
	h.eng.answer("breakdown", last.AddDays(-30), last.AddDays(-30), native.Result{Columns: []string{"day", "hour", "min_received_at", "max_received_at"}})
	h.eng.answer("breakdown", last.AddDays(-30), last, native.Result{Columns: []string{"day", "hour", "min_received_at", "max_received_at"},
		Rows: [][]string{{last.String(), "01", "yesterday", "today"}}})
	if _, err := h.e.Backtest(ctx, 7, silence(t), evalNow); err == nil {
		t.Error("a received_at that is not a time: no error")
	}
	h.eng.answer("breakdown", last.AddDays(-30), last, native.Result{Columns: []string{"day", "hour"}})
	if _, err := h.e.Backtest(ctx, 7, silence(t), evalNow); err == nil {
		t.Error("no received_at columns: no error")
	}
	h.eng.answer("breakdown", last.AddDays(-30), last, native.Result{Columns: []string{"day", "hour", "min_received_at", "max_received_at"}, Truncated: true})
	if _, err := h.e.Backtest(ctx, 7, silence(t), evalNow); err == nil {
		t.Error("a truncated breakdown: no error")
	}
}

func TestPreviewByDefinitionAndById(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	eng := &fakeEngine{}
	h.o.eng = eng
	answerDays(eng, 90, 1, func(civil.Date) string { return "3" })

	in := countIn("signups")
	bt, err := h.o.Preview(ctx, h.live, &in, 0)
	if err != nil || len(bt.Days) != 90 || bt.Days[89].Day != yesterday || bt.Fired != 1 {
		t.Fatalf("Preview by definition = %d days, fired %d, %v; want 90 breached days", len(bt.Days), bt.Fired, err)
	}
	v := h.create(t, in)
	if bt, err := h.o.Preview(ctx, h.live, nil, v.ID); err != nil || len(bt.Days) != 90 || bt.Fired != 1 {
		t.Fatalf("Preview by id = %d days, fired %d, %v", len(bt.Days), bt.Fired, err)
	}
	if es, _, _ := h.o.History(ctx, h.live, v.ID, 0, ""); len(es) != 0 {
		t.Errorf("the preview recorded %d evaluations", len(es))
	}

	bad := countIn("bad")
	bad.WindowDays = 31
	_, err = h.o.Preview(ctx, h.live, &bad, 0)
	mustInvalid(t, err, "window_days")

	h.projects.renames[ruleKey(store.FamilyProduct, "signup")] = "sign_up"
	_, err = h.o.Preview(ctx, h.live, &in, 0)
	mustInvalid(t, err, "sign_up")
	_, err = h.o.Preview(ctx, h.live, nil, v.ID)
	mustInvalid(t, err, "sign_up")
	delete(h.projects.renames, ruleKey(store.FamilyProduct, "signup"))

	other := h.project(t, "other")
	ov, err := h.o.Create(ctx, "tester", other, countIn("theirs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.o.Preview(ctx, h.live, nil, ov.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another project's alert: err = %v, want ErrNotFound", err)
	}
	if _, err := h.o.Preview(ctx, 999, &in, 0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown project: err = %v, want ErrNotFound", err)
	}

	h.o.Timeout, eng.block = time.Millisecond, true
	if _, err := h.o.Preview(ctx, h.live, &in, 0); !errors.Is(err, store.ErrTimeout) {
		t.Errorf("a slow engine: err = %v, want ErrTimeout", err)
	}

	h.o.eng = nil
	_, err = h.o.Preview(ctx, h.live, &in, 0)
	mustInvalid(t, err, "the event files cannot be queried by this process")
}

// TestBacktestSilenceMaxHours: the window spans at least 720 hours at any
// time of day, so a gap at the most hours an alert may name can show.
func TestBacktestSilenceMaxHours(t *testing.T) {
	h := newEvalHarness()
	last := civil.DateOf(silenceNow)
	from := last.AddDays(-30)
	cols := []string{"day", "hour", "min_received_at", "max_received_at"}
	h.eng.answer("breakdown", from, last, native.Result{Columns: cols})
	h.eng.answer("breakdown", from.AddDays(-1), from.AddDays(-1), native.Result{Columns: cols})
	bt := h.backtest(t, mustParse(t, Input{Where: []native.Filter{evt("signup")}, WhereSet: true,
		Condition: store.AlertCondition{Type: "silence", Hours: 720}}), silenceNow)
	if len(bt.Gaps) != 1 || bt.Fired != 1 {
		t.Fatalf("hours 720, no events: gaps %+v, fired %d, want one", bt.Gaps, bt.Fired)
	}
	if got := bt.Gaps[0].Hours; got < 720 {
		t.Errorf("gap of %v hours, want at least 720", got)
	}
}
