package alerts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Outcome is one evaluation of an alert.
type Outcome struct {
	Outcome  string     // store.AlertOK, AlertBreached, AlertNoData, AlertBroken, AlertError
	From, To civil.Date // the window; zero for silence
	// Value, Compare and Volume are nil where the evaluation had none.
	// Silence: Value is the newest matching event's age in hours.
	Value, Compare, Volume *float64
	Reason                 string    // no_data or broken reason; the error text for AlertError
	Last                   time.Time // silence: the newest matching received_at; zero when none
	At                     time.Time // when it was evaluated (now, UTC)
}

// Evaluator evaluates alerts over the event files.
type Evaluator struct {
	// LateDays is INGEST_LATE_DAYS: how many days before it was received
	// an event can be filed (its clamped ts), which a silence alert's
	// window reaches back too. Below 1 is 1, the default.
	LateDays int

	eng      Engine
	projects Projects
	presence Presence      // nil: every event counts as sent
	timeout  time.Duration // bounds an evaluation's queries; 0 none

	mu   sync.Mutex
	seen map[seenKey]bool // the references presence said were sent
}

// seenKey is a reference of one project's.
type seenKey struct {
	project int64
	ref     Reference
}

// NewEvaluator returns an Evaluator answering through eng, with each
// evaluation's queries bounded by timeout (CONSOLE_QUERY_TIMEOUT).
func NewEvaluator(eng Engine, projects Projects, presence Presence, timeout time.Duration) *Evaluator {
	return &Evaluator{eng: eng, projects: projects, presence: presence, timeout: timeout, seen: map[seenKey]bool{}}
}

// Evaluate evaluates a at now: a metric alert over its window_days
// complete days ending yesterday (UTC), and with change over its
// comparison window too; a silence alert over the ceil(hours/24) +
// LateDays days before today and today, which hold every event received
// in its last hours. A definition that no longer parses, or names an event an
// event rule renames or blocks, or a query the engine refuses, is
// broken; any other failure of the engine is AlertError. A value of 0
// or none, while an event or attribute it names was never sent, is
// no_data, and one whose presence question fails is AlertError (asked
// under its own timeout, as long as the queries'); otherwise judge
// decides.
func (e *Evaluator) Evaluate(ctx context.Context, a store.Alert, now time.Time) Outcome {
	now = now.UTC()
	o := Outcome{At: now}
	today := civil.DateOf(now)
	silence := a.Def.Condition.Type == condSilence
	if !silence {
		o.To = today.AddDays(-1)
		o.From = windowFrom(a.Def, o.To)
	}
	p, err := Parse(a.Def)
	if err != nil {
		return o.broken(err.Error())
	}
	if reason := e.ruleBreak(ctx, a.ProjectID, p); reason != "" {
		return o.broken(reason)
	}

	rules := e.projects.Rules(ctx)
	request := func(from, to civil.Date) native.Request {
		return native.Request{Query: p.Query, Params: native.Params{Project: a.ProjectID, From: from, To: to}, Rules: rules}
	}
	reqs := []native.Request{request(o.From, o.To)}
	if silence {
		reqs[0] = request(today.AddDays(-(silenceDays(p.Def.Condition.Hours) + max(e.LateDays, 1))), today)
	} else if p.Def.Condition.Type == condChange {
		reqs = append(reqs, request(comparison(p, o.From, o.To)))
	}
	qctx, cancel := e.bounded(ctx)
	answers := e.eng.AnswerAll(qctx, reqs)
	cancel()
	if len(answers) != len(reqs) {
		return o.failed(fmt.Errorf("the engine gave %d answers to %d queries", len(answers), len(reqs)))
	}
	for _, an := range answers {
		if errors.Is(an.Err, native.ErrInvalid) {
			return o.broken("query: " + strings.TrimPrefix(an.Err.Error(), native.ErrInvalid.Error()+": "))
		}
	}
	for _, an := range answers {
		if an.Err != nil {
			return o.failed(an.Err)
		}
	}

	var r reading
	if r, err = read(p, answers[0].Result, now); err != nil {
		return o.failed(err)
	}
	o.Value, o.Last = r.value, r.last
	if len(answers) > 1 {
		cmp, err := read(p, answers[1].Result, now)
		if err != nil {
			return o.failed(err)
		}
		o.Compare = cmp.value
	}
	o.Volume = volumeOf(p, r.volume, o.Compare)

	if o.Value == nil || (!silence && *o.Value == 0) {
		// Presence has its own timeout: the queries may have used theirs.
		pctx, cancel := e.bounded(ctx)
		name, err := e.neverSent(pctx, a.ProjectID, p)
		cancel()
		switch {
		case err != nil:
			return o.failed(err)
		case name != "":
			o.Outcome, o.Reason = store.AlertNoData, "never sent: "+name
			return o
		}
	}
	o.Outcome, o.Reason = judge(p, o.Value, o.Compare, o.Volume)
	return o
}

// bounded is ctx bounded by the evaluator's timeout, when it has one.
func (e *Evaluator) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.timeout > 0 {
		return context.WithTimeout(ctx, e.timeout)
	}
	return ctx, func() {}
}

// broken is o broken for reason, worded to end the alert_broken
// notification's sentence: capitalised, no closing period.
func (o Outcome) broken(reason string) Outcome {
	o.Outcome, o.Reason = store.AlertBroken, sentence(reason)
	return o
}

// failed is o failed with err, which is tried again.
func (o Outcome) failed(err error) Outcome {
	o.Outcome, o.Reason = store.AlertError, err.Error()
	return o
}

// sentence is s with its first letter upper case and no closing period.
func sentence(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), ".")
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// silenceDays is how many days before today the last hours began on:
// ceil(hours/24). A silence alert reads LateDays days more, since an
// event received then can be filed (by its clamped ts) that much
// earlier.
func silenceDays(hours int) int { return (hours + 23) / 24 }

// windowFrom is the first day of def's window ending to: its window_days
// complete days.
func windowFrom(def store.AlertDef, to civil.Date) civil.Date {
	return to.AddDays(-(max(def.WindowDays, 1) - 1))
}

// comparison is the window a change compares from…to with: the as many
// days just before it (previous), or the same days a week earlier
// (week_ago).
func comparison(p Parsed, from, to civil.Date) (civil.Date, civil.Date) {
	if p.Def.Condition.Against == "week_ago" {
		return from.AddDays(-7), to.AddDays(-7)
	}
	days := p.Def.WindowDays
	return from.AddDays(-days), to.AddDays(-days)
}

// ruleBreak is why an event p's query names is broken by an event rule,
// or an attribute by an attribute rule (renamed, merged or blocked), ""
// when none is: ruleHit, which Ops.checkEvents refuses at save.
func (e *Evaluator) ruleBreak(ctx context.Context, projectID int64, p Parsed) string {
	h, ok := ruleHit(ctx, e.projects, projectID, p)
	switch {
	case !ok:
		return ""
	case h.to != h.name:
		return "A rule renamed " + h.name + " to " + h.to
	}
	return "A rule blocked " + h.name
}

// neverSent is the first event or attribute p's query names that the
// project has never sent (an attribute as attr_<key>), "" when every one
// was sent; what only a never filter names is not asked. A failed question is an error, which the caller retries
// rather than judge a value that may only mean "never sent"; a sent
// reference is remembered, never asked again (a rule renaming or
// blocking it later breaks the alert before presence is asked).
func (e *Evaluator) neverSent(ctx context.Context, projectID int64, p Parsed) (string, error) {
	if e.presence == nil {
		return "", nil
	}
	for _, r := range p.References() {
		if r.Never {
			continue
		}
		k := seenKey{projectID, r}
		e.mu.Lock()
		seen := e.seen[k]
		e.mu.Unlock()
		if seen {
			continue
		}
		name, sent, err := askPresence(ctx, e.presence, projectID, r)
		switch {
		case err != nil:
			return "", fmt.Errorf("could not check whether %s was ever sent: %w", name, err)
		case !sent:
			return name, nil
		}
		e.mu.Lock()
		e.seen[k] = true
		e.mu.Unlock()
	}
	return "", nil
}

// reading is the number an answer gives an alert.
type reading struct {
	value, volume *float64  // volume: a stat's volume metric, a funnel's base step
	last          time.Time // silence: the newest received_at
}

// read reads p's number from res: a stat's read metric (and volume
// metric), a funnel's step over its base (none when the base is 0), or
// a silence alert's newest received_at as its age at now, in hours.
func read(p Parsed, res native.Result, now time.Time) (reading, error) {
	var r reading
	if p.Kind == "funnel" {
		if len(res.Rows) <= max(p.Step, p.Base) {
			return r, fmt.Errorf("the funnel's answer has %d steps, not %d", len(res.Rows), len(p.Query.Steps))
		}
		var step, base *float64
		var err error
		if step, err = cell(res.Rows[p.Step], 1); err == nil {
			base, err = cell(res.Rows[p.Base], 1)
		}
		if err != nil {
			return r, err
		}
		r.volume = base
		if step != nil && base != nil && *base != 0 {
			r.value = ptrTo(*step / *base)
		}
		return r, nil
	}
	if len(res.Rows) != 1 {
		return r, fmt.Errorf("the stat's answer has %d rows, not 1", len(res.Rows))
	}
	column := func(i int) (int, error) {
		name := p.Query.Metrics[i].Name()
		if c := slices.Index(res.Columns, name); c >= 0 {
			return c, nil
		}
		return 0, fmt.Errorf("the stat's answer has no column %s", name)
	}
	c, err := column(p.Metric)
	if err != nil {
		return r, err
	}
	if p.Kind == condSilence {
		if c >= len(res.Rows[0]) {
			return r, fmt.Errorf("the answer's row has %d values, not %d", len(res.Rows[0]), c+1)
		}
		if res.Rows[0][c] == "" {
			return r, nil
		}
		last, err := time.Parse(time.RFC3339Nano, res.Rows[0][c])
		if err != nil {
			return r, fmt.Errorf("the newest received_at %q is not a time", res.Rows[0][c])
		}
		r.last = last.UTC()
		r.value = ptrTo(now.Sub(last).Hours())
		return r, nil
	}
	if r.value, err = cell(res.Rows[0], c); err != nil {
		return r, err
	}
	if p.Volume >= 0 {
		if c, err = column(p.Volume); err == nil {
			r.volume, err = cell(res.Rows[0], c)
		}
	}
	return r, err
}

// cell is row's i'th value as a number, nil when it is empty.
func cell(row []string, i int) (*float64, error) {
	if i >= len(row) {
		return nil, fmt.Errorf("the answer's row has %d values, not %d", len(row), i+1)
	}
	if row[i] == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(row[i], 64)
	if err != nil {
		return nil, fmt.Errorf("%q is not a number", row[i])
	}
	return &v, nil
}

func ptrTo(v float64) *float64 { return &v }

// volumeOf is the volume min_volume measures: the volume read (a stat's
// volume metric, a funnel's base step), or, for a change on a stat
// without a volume metric, the comparison.
func volumeOf(p Parsed, volume, compare *float64) *float64 {
	if volume == nil && p.Kind == "stat" && p.Volume < 0 && p.Def.Condition.Type == condChange {
		return compare
	}
	return volume
}

// pctTolerance absorbs the float error of a change's percentage, so a
// drop of exactly the condition's percent breaches.
const pctTolerance = 1e-9

// judge decides p's outcome from its value, the comparison (change) and
// the volume, with the reason for no_data. Silence is breached with no
// event in its window (value nil) or one older than its hours. Otherwise
// no value, a volume under min_volume, or a change's comparison of 0 or
// none is no_data; a change breaches by its percent in its direction, a
// threshold beyond its value.
func judge(p Parsed, value, compare, volume *float64) (outcome, reason string) {
	c := p.Def.Condition
	if c.Type == condSilence {
		if value == nil || *value > float64(c.Hours) {
			return store.AlertBreached, ""
		}
		return store.AlertOK, ""
	}
	if value == nil {
		return store.AlertNoData, "no value"
	}
	volume = volumeOf(p, volume, compare)
	if p.Def.MinVolume > 0 && (volume == nil || *volume < p.Def.MinVolume) {
		return store.AlertNoData, "below min_volume"
	}
	breached := false
	switch c.Type {
	case condChange:
		if compare == nil || *compare == 0 {
			return store.AlertNoData, "comparison is 0 or empty"
		}
		pct := (*value - *compare) / *compare * 100
		switch c.Direction {
		case "drop":
			breached = pct <= -c.Percent+pctTolerance
		case "rise":
			breached = pct >= c.Percent-pctTolerance
		case "either":
			breached = math.Abs(pct) >= c.Percent-pctTolerance
		}
	case condThreshold:
		if c.Value != nil {
			breached = (c.Op == "below" && *value < *c.Value) || (c.Op == "above" && *value > *c.Value)
		}
	}
	if breached {
		return store.AlertBreached, ""
	}
	return store.AlertOK, ""
}
