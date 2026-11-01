package native

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
)

// answerRec is one Answer call a recorder saw.
type answerRec struct {
	q     Query
	p     Params
	rules archive.Rules
	res   Result
	err   error
}

// engineCalls are the Answer calls of one engine, in order.
type engineCalls struct {
	e     *Engine
	calls []answerRec
}

// recordAnswers runs run with every Answer call recorded, by engine in
// the order the engines first answered.
func recordAnswers(run func()) []*engineCalls {
	var mu sync.Mutex
	var out []*engineCalls
	byEngine := map[*Engine]*engineCalls{}
	answered = func(e *Engine, q Query, p Params, rules archive.Rules, res Result, err error) {
		mu.Lock()
		defer mu.Unlock()
		ec := byEngine[e]
		if ec == nil {
			ec = &engineCalls{e: e}
			byEngine[e] = ec
			out = append(out, ec)
		}
		ec.calls = append(ec.calls, answerRec{q: q, p: p, rules: rules, res: res, err: err})
	}
	defer func() { answered = nil }()
	run()
	return out
}

// errClasses are the errors an answer's error is matched by; one in none
// of them is matched by its text.
var errClasses = []error{ErrInvalid, ErrTooLarge, fs.ErrNotExist, context.Canceled, context.DeadlineExceeded}

// diffAnswer is how got differs from Answer's res and err: "" when it
// does not. Numbers compare as sameCell compares them.
func diffAnswer(got Answered, res Result, err error) string {
	if got.Err != nil || err != nil {
		if (got.Err == nil) != (err == nil) {
			return fmt.Sprintf("error %v, want %v", got.Err, err)
		}
		classed := false
		for _, c := range errClasses {
			if errors.Is(got.Err, c) != errors.Is(err, c) {
				return fmt.Sprintf("error %v, want %v", got.Err, err)
			}
			classed = classed || errors.Is(err, c)
		}
		if !classed && got.Err.Error() != err.Error() {
			return fmt.Sprintf("error %q, want %q", got.Err, err)
		}
		return ""
	}
	g := got.Result
	switch {
	case !slices.Equal(g.Columns, res.Columns):
		return fmt.Sprintf("columns %q, want %q", g.Columns, res.Columns)
	case g.Truncated != res.Truncated:
		return fmt.Sprintf("truncated %v, want %v", g.Truncated, res.Truncated)
	case !slices.Equal(g.Approximate, res.Approximate):
		return fmt.Sprintf("approximate %q, want %q", g.Approximate, res.Approximate)
	case len(g.Rows) != len(res.Rows):
		return fmt.Sprintf("%d rows, want %d:\n got %q\nwant %q", len(g.Rows), len(res.Rows), g.Rows, res.Rows)
	}
	for i := range res.Rows {
		ok := len(g.Rows[i]) == len(res.Rows[i])
		for j := 0; ok && j < len(res.Rows[i]); j++ {
			ok = sameCell(g.Rows[i][j], res.Rows[i][j])
		}
		if !ok {
			return fmt.Sprintf("row %d:\n got %q\nwant %q", i, g.Rows[i], res.Rows[i])
		}
	}
	return ""
}

// newRand is a random source whose seed the test logs.
func newRand(t *testing.T) *rand.Rand {
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	return rand.New(rand.NewPCG(seed, 1))
}

// checkBatched asks e calls' queries through AnswerAll, in rounds: the
// first all in one call, the others in a random order cut into random
// groups; each answer must be the one recorded.
func checkBatched(t *testing.T, name string, e *Engine, calls []answerRec, rng *rand.Rand, rounds int) {
	t.Helper()
	for round := range rounds {
		perm := rng.Perm(len(calls))
		for len(perm) > 0 {
			n := len(perm)
			if round > 0 {
				n = 1 + rng.IntN(len(perm))
			}
			chunk := perm[:n]
			perm = perm[n:]
			reqs := make([]Request, len(chunk))
			for k, i := range chunk {
				reqs[k] = Request{Query: calls[i].q, Params: calls[i].p, Rules: calls[i].rules}
			}
			got := e.AnswerAll(context.Background(), reqs)
			for k, i := range chunk {
				if d := diffAnswer(got[k], calls[i].res, calls[i].err); d != "" {
					t.Errorf("%s round %d (%d of %d together): %+v %v: %s", name, round, k, len(chunk), calls[i].q, calls[i].p, d)
				}
			}
		}
	}
}

// answersOf is Answer's answer to each of reqs, one at a time.
func answersOf(e *Engine, reqs []Request) []answerRec {
	out := make([]answerRec, len(reqs))
	for i, r := range reqs {
		res, err := e.Answer(context.Background(), r.Query, r.Params, r.Rules)
		out[i] = answerRec{q: r.Query, p: r.Params, rules: r.Rules, res: res, err: err}
	}
	return out
}

// manyRequests are queries of the engine fixture for one AnswerMany: of
// every table and the alone kinds, over mixed ranges (the open log in
// most), a stat compared with its previous range beside trends of the
// same range, metric filters, rules (and project 1 without them),
// identity columns, project 2 and refused ones.
func manyRequests(t *testing.T) []Request {
	r := func(q Query, from, to string) Request {
		return Request{Query: q, Params: Params{Project: 1, From: date(t, from), To: date(t, to)}, Rules: engineRules}
	}
	const m = "2026-08-26"
	product := []Filter{{Column: "family", Op: "eq", Values: []string{"product"}}}
	views := []Filter{{Column: "family", Op: "eq", Values: []string{"views"}}}
	reqs := []Request{
		r(Query{Kind: "stat", Table: "views", Compare: "previous", Metrics: []Metric{{Op: "count"}, {Op: "actors"},
			{Op: "daily_avg", Of: "actors"}, {Op: "sum", Column: "display_width"}}}, m, today),
		r(Query{Kind: "trend", Table: "views", Metrics: count()}, m, today),
		r(Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "actors"}}, Series: "country", SeriesLimit: 2}, m, today),
		r(Query{Kind: "trend", Table: "views", Fill: "zero", Metrics: []Metric{{Op: "count"}, {Op: "actors", Where: []Filter{
			{Column: "country", Op: "eq", Values: []string{"DE"}}}}}}, "2026-09-01", today),
		r(Query{Kind: "breakdown", Table: "views", Metrics: []Metric{{Op: "actors"}, {Op: "count"}},
			Dimensions: []string{"path"}, Limit: 3, Other: true}, m, today),
		r(Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "actors"}},
			Dimensions: []string{"family", "consent"}}, "2026-08-15", "2026-09-04"),
		r(Query{Kind: "breakdown", Table: "product", Metrics: []Metric{{Op: "count"}, {Op: "users"}},
			Dimensions: []string{"event_name", "attr_plan"}}, engineFrom, today),
		r(Query{Kind: "breakdown", Table: "product", Metrics: []Metric{{Op: "count"}, {Op: "groups"}},
			Dimensions: []string{"attr_color", "country", "display_width"}, Limit: 50}, engineFrom, today),
		r(Query{Kind: "stat", Table: "measures", Compare: "previous", Metrics: []Metric{{Op: "min", Column: "value"},
			{Op: "avg", Column: "value", Round: ptr(2)}, {Op: "percentile", Q: 0.75}, {Op: "weight"}, {Op: "mean"}}}, "2026-09-01", "2026-09-07"),
		r(Query{Kind: "breakdown", Table: "measures", Metrics: []Metric{{Op: "avg", Column: "value"}, {Op: "count"},
			{Op: "share", Of: "weight"}}, Dimensions: []string{"event_name"}}, engineFrom, today),
		r(Query{Kind: "breakdown", Table: "events", Dimensions: []string{"path"}, Limit: 3, Other: true, Metrics: []Metric{
			{Op: "count", As: "views", Where: views}, {Op: "count", As: "events", Where: product},
			{Op: "actors", As: "buyers", Where: product},
			{Op: "ratio", As: "per_buyer", Round: ptr(3), Num: &Metric{Op: "count", Where: product}, Den: &Metric{Op: "actors", Where: product}}}},
			engineFrom, today),
		r(Query{Kind: "stat", Table: "views", Metrics: count()}, "2026-07-01", "2026-07-05"),
		r(Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "users"}}}, today, today),
		r(Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "actors"}}, Series: "family"}, engineFrom, today),
		r(Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "users"}, {Op: "daily_sum", Of: "actors"}},
			Dimensions: []string{"day", "family"}, Limit: 100}, "2026-09-04", "2026-09-07"),
		r(Query{Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"event_name"}}, engineFrom, today),
		// A group per path and day: past a small MaxBytes, truncated.
		r(Query{Kind: "breakdown", Table: "views", Metrics: []Metric{{Op: "actors"}, {Op: "daily_max", Of: "users"}},
			Dimensions: []string{"path", "day"}, Limit: 1000}, engineFrom, today),
		r(Query{Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"path", "day"}, Limit: 1000}, "2026-09-01", today),
		r(Query{Kind: "breakdown", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "share", Of: "count"}},
			Dimensions: []string{"country"}}, m, today),
		r(Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "users"}},
			Dimensions: []string{"user_status"}, Where: []Filter{{Column: "user_id", Op: "is_set"}}}, m, today),
		r(Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "users"}}, Series: "user_label", SeriesLimit: 3}, "2026-09-01", today),
		r(Query{Kind: "stat", Table: "product", Metrics: []Metric{{Op: "days"}, {Op: "count", Where: []Filter{
			{Column: "user_first_seen", Op: "gte", Values: []string{"2026-09-01"}}}}}}, engineFrom, today),
		// Sessions.
		r(Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "count"}, {Op: "bounces"}, {Op: "bounce_rate"}, {Op: "actors"},
			{Op: "avg", Column: "duration_sec"}}}, m, today),
		r(Query{Kind: "stat", Table: "sessions", Compare: "previous", Metrics: []Metric{{Op: "count"}, {Op: "bounce_rate"}}}, "2026-09-03", "2026-09-06"),
		r(Query{Kind: "trend", Table: "sessions", Metrics: count()}, "2026-09-01", today),
		r(Query{Kind: "breakdown", Table: "sessions", Metrics: []Metric{{Op: "count"}, {Op: "bounce_rate"}},
			Dimensions: []string{"entry_path"}, Limit: 5, Other: true}, engineFrom, today),
		r(Query{Kind: "stat", Table: "sessions", Metrics: count()}, "2026-07-01", "2026-07-05"),
		// Alone.
		r(Query{Kind: "retention", Output: "grid"}, "2026-09-01", today),
		r(Query{Kind: "attributes", Output: "keys"}, engineFrom, today),
		r(Query{Kind: "stat", Table: "identities", Metrics: count()}, engineFrom, today),
		// Refused.
		r(Query{Kind: "stat", Table: "views"}, m, today),
		r(Query{Kind: "stat", Table: "views", Metrics: count()}, today, m),
		r(Query{Kind: "stat", Table: "measures", Metrics: count(), Where: []Filter{{Column: "day", Op: "eq", Values: []string{"x"}}}}, m, today),
	}
	// Project 1 without its rules, and project 2.
	noRules := r(Query{Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"event_name"}}, engineFrom, today)
	noRules.Rules = archive.Rules{}
	p2 := r(Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"family", "event_name"}}, engineFrom, today)
	p2.Params.Project = 2
	p2s := r(Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "actors"}}}, m, today)
	p2s.Params.Project = 2
	return append(reqs, noRules, p2, p2s)
}

// TestAnswerManyEqualsAnswer holds every AnswerMany answer to Answer's:
// the parity tests' queries, batched in random orders and groupings on
// their own engines, and the engine fixture's mixed batch, with the
// groups and the bytes capped too. The parity tests, rerun, compare
// Answer with their own goldens as they do alone.
func TestAnswerManyEqualsAnswer(t *testing.T) {
	rng := newRand(t)
	t.Run("parity", func(t *testing.T) {
		// It reruns the parity tests, which the race suite runs already;
		// make check (no race) runs it.
		if raceEnabled {
			t.Skip("the parity rerun is too slow under the race detector; make check runs it")
		}
		parity := []struct {
			name, test string // test: the parity test's name, whose goldens it reads
			run        func(*testing.T)
		}{
			{"kinds", "TestKindsMatchGolden", TestKindsMatchGolden},
			{"share", "TestShareMatchesGolden", TestShareMatchesGolden},
			{"ratings", "TestRatingsMatchWidget", TestRatingsMatchWidget},
			{"ratio", "TestRatioMatchesGolden", TestRatioMatchesGolden},
			{"ratio trends", "TestRatioTrends", TestRatioTrends},
			{"trend", "TestTrendMatchesGolden", TestTrendMatchesGolden},
			{"days", "TestDaysMatchesGolden", TestDaysMatchesGolden},
			{"labels", "TestIdentityLabelsMatchGolden", TestIdentityLabelsMatchGolden},
			{"percentiles", "TestPercentilesMatchWidgets", TestPercentilesMatchWidgets},
			{"sessions", "TestSessionsQueriesMatchGolden", TestSessionsQueriesMatchGolden},
			{"retention", "TestRetentionMatchesSQL", TestRetentionMatchesSQL},
			{"attributes", "TestAttributesMatchGolden", TestAttributesMatchGolden},
			{"attribute keys", "TestAttributeKeysMatchGolden", TestAttributeKeysMatchGolden},
			{"identities", "TestIdentitiesQueriesMatchGolden", TestIdentitiesQueriesMatchGolden},
		}
		for _, p := range parity {
			t.Run(p.name, func(t *testing.T) {
				// The parity test compares Answer with its own goldens.
				parityOf.Store(t.Name(), p.test)
				// Run with this t, so the fixtures live until the batches
				// are checked.
				engines := recordAnswers(func() { p.run(t) })
				if len(engines) == 0 {
					t.Fatal("no answers recorded")
				}
				// Answered again now: a test may change its files between
				// its queries.
				for k, ec := range engines {
					reqs := make([]Request, len(ec.calls))
					for i, c := range ec.calls {
						reqs[i] = Request{Query: c.q, Params: c.p, Rules: c.rules}
					}
					checkBatched(t, fmt.Sprintf("engine %d", k), ec.e, answersOf(ec.e, reqs), rng, 2)
				}
			})
		}
	})
	f := engineFixture(t)
	reqs := manyRequests(t)
	for _, c := range []struct {
		name string
		o    Options
	}{
		{"one worker", Options{Workers: 1}},
		{"three workers", Options{Workers: 3, MaxScans: 3}},
		// One slot: a compared stat's two heavy aggregations take it whole.
		{"one scan at a time", Options{Workers: 2, MaxScans: 1}},
		// Groups past MaxGroups left out: one worker, so Answer and
		// AnswerMany meet the rows in one order and admit the same groups.
		{"max groups", Options{Workers: 1, MaxGroups: 3}},
		// Budgets each query nearly fits in; the second so small that
		// queries are evicted from their scans, and some are truncated
		// or refused alone too.
		{"max bytes", Options{Workers: 1, MaxBytes: 48 << 10}},
		{"tiny max bytes", Options{Workers: 1, MaxBytes: 12 << 10}},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := c.o
			o.Dir = f.Dir
			e := New(o)
			want := answersOf(e, reqs)
			truncated, refused := 0, 0
			for _, w := range want {
				if w.res.Truncated {
					truncated++
				}
				if errors.Is(w.err, ErrTooLarge) {
					refused++
				}
			}
			var evicted atomic.Int64
			defer func(f func()) { onEvict = f }(onEvict)
			onEvict = func() { evicted.Add(1) }
			checkBatched(t, c.name, e, want, rng, 4)
			t.Logf("%d truncated, %d refused, %d evicted", truncated, refused, evicted.Load())
			switch c.name {
			case "max groups":
				if truncated == 0 {
					t.Error("no answer truncated")
				}
			case "tiny max bytes":
				if evicted.Load() == 0 || truncated == 0 || refused == 0 {
					t.Error("a weak case: none evicted, truncated or refused")
				}
			}
		})
	}
}

// One request's context done mid-scan answers it with its error, the
// others as Answer does; every request's done stops the scan.
func TestAnswerManyCancelsOne(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 1})
	all := manyRequests(t)
	var reqs []Request
	for _, r := range all {
		if _, err := compileQuery(r.Query, r.Params); err == nil && !alone(r.Query) && r.Query.Table != "sessions" {
			reqs = append(reqs, r)
		}
	}
	want := answersOf(e, reqs)
	defer func(f func()) { onScanItem = f }(onScanItem)
	defer func(f func(Request)) { onAdd = f }(onAdd)

	// Cancelled as its aggregation takes a batch: it is live then, so it
	// leaves before its scan finishes, however the goroutines run.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reqs[0].Ctx = ctx
	onAdd = func(r Request) {
		if r.Ctx == ctx {
			cancel()
		}
	}
	got := e.AnswerAll(context.Background(), reqs)
	onAdd = func(Request) {}
	if !errors.Is(got[0].Err, context.Canceled) {
		t.Errorf("the cancelled query: %v, want context.Canceled", got[0].Err)
	}
	for i := 1; i < len(reqs); i++ {
		if d := diffAnswer(got[i], want[i].res, want[i].err); d != "" {
			t.Errorf("%+v: %s", reqs[i].Query, d)
		}
	}

	// Every one cancelled at the first item, one scan at a time on one
	// worker: the scan reading it stops there (its worker may take one
	// more item), and no later scan reads any.
	e = New(Options{Dir: f.Dir, Workers: 1, MaxScans: 1})
	var items atomic.Int64
	ctxs := make([]context.CancelFunc, len(reqs))
	for i := range reqs {
		reqs[i].Ctx, ctxs[i] = context.WithCancel(context.Background())
	}
	onScanItem = func() {
		if items.Add(1) == 1 {
			for _, c := range ctxs {
				c()
			}
		}
	}
	got = e.AnswerAll(context.Background(), reqs)
	for i, a := range got {
		if !errors.Is(a.Err, context.Canceled) {
			t.Errorf("query %d: %v, want context.Canceled", i, a.Err)
		}
	}
	if n := items.Load() - 1; n > 1 {
		t.Errorf("%d items read after every query left, want at most 1", n)
	}
}

// A file the daily pass removes mid-scan runs the whole scan again, on
// the files as they are then, every query from scratch; one that is gone
// on every attempt fails every query of the scan with ErrNotExist.
func TestAnswerManyRetriesVanishedFiles(t *testing.T) {
	defer func(n int, b time.Duration) { maxAttempts, retryBackoff = n, b }(maxAttempts, retryBackoff)
	maxAttempts, retryBackoff = 3, time.Millisecond
	defer func(f func()) { onScanItem = f }(onScanItem)

	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 1})
	var reqs []Request
	for _, r := range manyRequests(t) {
		if _, err := compileQuery(r.Query, r.Params); err == nil && !alone(r.Query) && r.Params.Project == 1 {
			reqs = append(reqs, r)
		}
	}
	before := answersOf(e, reqs)
	logs, err := filepath.Glob(filepath.Join(f.Dir, "1", "*.log"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no logs: %v", err)
	}
	var items atomic.Int64
	onScanItem = func() {
		if items.Add(1) == 1 { // the first item, a log: the pass dumps them
			for _, l := range logs {
				if err := os.Remove(l); err != nil {
					t.Error(err)
				}
			}
		}
	}
	got := e.AnswerAll(context.Background(), reqs)
	onScanItem = func() {}
	after := answersOf(e, reqs)
	changed := 0
	for i := range reqs {
		if d := diffAnswer(got[i], after[i].res, after[i].err); d != "" {
			t.Errorf("%+v: %s", reqs[i].Query, d)
		}
		if diffAnswer(Answered{Result: before[i].res, Err: before[i].err}, after[i].res, after[i].err) != "" {
			changed++
		}
	}
	if changed == 0 {
		t.Error("no answer changed without the logs: a weak case")
	}

	// Gone on every attempt.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "1", today+".log")); err != nil {
		t.Fatal(err)
	}
	e = New(Options{Dir: dir})
	p := Params{Project: 1, From: date(t, today), To: date(t, today)}
	for i, a := range e.AnswerAll(context.Background(), []Request{
		{Query: Query{Kind: "stat", Table: "views", Metrics: count()}, Params: p},
		{Query: Query{Kind: "breakdown", Table: "events", Metrics: count(), Dimensions: []string{"path"}}, Params: p},
	}) {
		if !errors.Is(a.Err, fs.ErrNotExist) {
			t.Errorf("query %d: %v, want ErrNotExist after the retries", i, a.Err)
		}
	}
}

// One scan per project, table class and rules: a turn each, the alone
// kinds a turn each, and a class past maxConsumers aggregations split.
func TestAnswerManyOneTurn(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir})
	var turns atomic.Int64
	defer func(f func(int)) { onTurn = f }(onTurn)
	onTurn = func(w int) {
		if w > 0 {
			turns.Add(1)
		}
	}
	p := func(project int64) Params {
		return Params{Project: project, From: date(t, "2026-09-01"), To: date(t, today)}
	}
	q := func(table string) Query { return Query{Kind: "stat", Table: table, Metrics: count()} }
	reqs := []Request{
		{Query: q("views"), Params: p(1), Rules: engineRules},
		{Query: q("product"), Params: p(1), Rules: engineRules},
		{Query: q("events"), Params: p(1), Rules: engineRules},
		{Query: Query{Kind: "trend", Table: "measures", Metrics: count()}, Params: p(1), Rules: engineRules},
		{Query: Query{Kind: "stat", Table: "views", Compare: "previous", Metrics: count()}, Params: p(1), Rules: engineRules}, // 1: events files, rules
		{Query: q("sessions"), Params: p(1), Rules: engineRules},
		{Query: q("sessions"), Params: p(1)}, // 2: sessions take no rules
		{Query: q("views"), Params: p(1)},    // 3: project 1 without rules
		{Query: q("views"), Params: p(2)},    // 4: project 2
		{Query: Query{Kind: "retention", Output: "grid"}, Params: p(1)},
		{Query: Query{Kind: "attributes", Output: "keys"}, Params: p(1)},                                 // 5, 6: alone
		{Query: q("views"), Params: Params{Project: 1, From: date(t, today), To: date(t, "2026-09-01")}}, // refused: no turn
	}
	for i, a := range e.AnswerAll(context.Background(), reqs) {
		if (a.Err != nil) != (i == len(reqs)-1) {
			t.Errorf("query %d: %v", i, a.Err)
		}
	}
	if n := turns.Load(); n != 6 {
		t.Errorf("%d turns, want 6", n)
	}
	// 40 aggregations of one class: two scans.
	turns.Store(0)
	var many []Request
	for range 20 {
		many = append(many, Request{Query: Query{Kind: "stat", Table: "views", Compare: "previous", Metrics: count()}, Params: p(1)})
	}
	for i, a := range e.AnswerAll(context.Background(), many) {
		if a.Err != nil {
			t.Errorf("query %d: %v", i, a.Err)
		}
	}
	if n := turns.Load(); n != 2 {
		t.Errorf("%d turns for 40 aggregations, want 2", n)
	}
}

// done is called once per request, never two at once, and AnswerMany
// returns after the last.
func TestAnswerManyDoneOnce(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, MaxScans: 3})
	reqs := manyRequests(t)
	counts := make([]int, len(reqs))
	var in atomic.Int32
	returned := false
	e.AnswerMany(context.Background(), reqs, func(i int, a Answered) {
		if in.Add(1) != 1 {
			t.Error("done called twice at once")
		}
		time.Sleep(time.Millisecond)
		if returned {
			t.Error("done called after AnswerMany returned")
		}
		counts[i]++
		in.Add(-1)
	})
	returned = true
	for i, n := range counts {
		if n != 1 {
			t.Errorf("request %d answered %d times", i, n)
		}
	}
}

// A weighted semaphore serves its waiters in order, a large one too, and
// a waiter whose context is done leaves the queue.
func TestWeighted(t *testing.T) {
	w := newWeighted(2)
	ctx := context.Background()
	if err := w.acquire(ctx, 1); err != nil {
		t.Fatal(err)
	}
	two := make(chan struct{})
	go func() {
		if err := w.acquire(ctx, 2); err != nil {
			t.Error(err)
		}
		close(two)
	}()
	for {
		w.mu.Lock()
		n := len(w.waiters)
		w.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// One unit is free, but the waiter for two is first.
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := w.acquire(short, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("jumped the queue: %v", err)
	}
	w.release(1)
	select {
	case <-two:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter for two was not served")
	}
	w.release(2)
	if w.free != 2 || len(w.waiters) != 0 {
		t.Errorf("free %d, %d waiters", w.free, len(w.waiters))
	}
}

// Two calls at once never hold more heavy aggregations than MaxScans
// queries would: the turns' weights, summed over both, stay within
// MaxScans, and a scan of two heavy ones takes both.
func TestAnswerManyHeavyBoundAcrossCalls(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, MaxScans: 2})
	p := Params{Project: 1, From: date(t, "2026-08-26"), To: date(t, today)}
	heavy := func(table string) Request {
		return Request{Query: Query{Kind: "breakdown", Table: table, Dimensions: []string{"path"},
			Metrics: []Metric{{Op: "daily_sum", Of: "actors"}}}, Params: p, Rules: engineRules}
	}
	reqs := []Request{heavy("views"), heavy("events"), heavy("product"), heavy("views"),
		{Query: Query{Kind: "stat", Table: "views", Metrics: count()}, Params: p, Rules: engineRules},
		{Query: Query{Kind: "retention", Output: "grid"}, Params: p}}
	want := answersOf(e, reqs)
	var held, most, widest atomic.Int64
	defer func(f func(int)) { onTurn = f }(onTurn)
	onTurn = func(w int) {
		n := held.Add(int64(w))
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		for m := widest.Load(); int64(w) > m && !widest.CompareAndSwap(m, int64(w)); m = widest.Load() {
		}
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i, a := range e.AnswerAll(context.Background(), reqs) {
				if d := diffAnswer(a, want[i].res, want[i].err); d != "" {
					t.Errorf("%+v: %s", reqs[i].Query, d)
				}
			}
		}()
	}
	wg.Wait()
	if n := most.Load(); n > 2 {
		t.Errorf("%d turns' weight held at once, want at most MaxScans (2)", n)
	}
	if widest.Load() != 2 {
		t.Errorf("the widest turn weighed %d, want 2 (a scan of two heavy aggregations)", widest.Load())
	}
}

// A done that blocks holds up neither the call's scans nor another
// call's: the first answer is a query refused mid-scan (past a tiny
// MaxBytes), under the scan's turn; every turn is released while done
// waits on it, and another call answers meanwhile on the one turn.
func TestAnswerManySlowDone(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, MaxScans: 1, MaxBytes: 1 << 10})
	p := Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}
	light := []Request{
		{Query: Query{Kind: "stat", Table: "views", Metrics: count()}, Params: p, Rules: engineRules},
		{Query: Query{Kind: "stat", Table: "product", Metrics: count()}, Params: p, Rules: engineRules},
		{Query: Query{Kind: "trend", Table: "measures", Metrics: count()}, Params: p, Rules: engineRules},
	}
	refused := Request{Query: Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "daily_sum", Of: "actors"}}},
		Params: p, Rules: engineRules}
	if _, err := e.Answer(context.Background(), refused.Query, refused.Params, refused.Rules); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("the refused query: %v, want ErrTooLarge", err)
	}
	reqs := append([]Request{refused}, light...)
	var held atomic.Int64
	defer func(f func(int)) { onTurn = f }(onTurn)
	onTurn = func(w int) { held.Add(int64(w)) }

	release := make(chan struct{})
	first := make(chan struct{})
	var answered atomic.Int32
	slow := make(chan struct{})
	go func() {
		defer close(slow)
		e.AnswerMany(context.Background(), reqs, func(i int, a Answered) {
			if answered.Add(1) == 1 {
				if i != 0 || !errors.Is(a.Err, ErrTooLarge) {
					t.Errorf("first answer %d: %v, want the refusal", i, a.Err)
				}
				close(first)
				<-release // blocks while the scans go on
			}
		})
	}()
	<-first
	// Another call runs to the end meanwhile, on the one turn.
	other := make(chan []Answered, 1)
	go func() { other <- e.AnswerAll(context.Background(), light) }()
	select {
	case got := <-other:
		for i, a := range got {
			if a.Err != nil {
				t.Errorf("the other call's query %d: %v", i, a.Err)
			}
		}
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("the other call waits on the blocked done")
	}
	// The blocked call's scans end too: no turn is left held.
	deadline := time.Now().Add(10 * time.Second)
	for held.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := held.Load(); n != 0 {
		t.Errorf("%d turns held while done blocks", n)
	}
	select {
	case <-slow:
		t.Fatal("AnswerMany returned before its last done")
	default:
	}
	close(release)
	<-slow
	if n := answered.Load(); n != int32(len(reqs)) {
		t.Errorf("%d answers, want %d", n, len(reqs))
	}
}

// TestAnswerManyPanics: a panic answering one query (a bug) fails only
// that query's requests with an internal error, in a shared scan, in a
// query answered alone and in done, and the process goes on.
func TestAnswerManyPanics(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 2})
	all := manyRequests(t)
	var reqs []Request
	for _, r := range all {
		if _, err := compileQuery(r.Query, r.Params); err == nil && r.Query.Table != "sessions" {
			reqs = append(reqs, r)
		}
	}
	want := answersOf(e, reqs)
	var target, aloneQ Query
	for _, r := range reqs {
		if alone(r.Query) {
			if aloneQ.Kind == "" {
				aloneQ = r.Query
			}
		} else if target.Kind == "" {
			target = r.Query
		}
	}
	if target.Kind == "" || aloneQ.Kind == "" {
		t.Fatal("no shared or alone query to break")
	}
	defer func(f func(Request)) { onAdd = f }(onAdd)
	onAdd = func(r Request) {
		if reflect.DeepEqual(r.Query, target) {
			panic("boom in a consumer")
		}
	}
	defer func(f func(*Engine, Query, Params, archive.Rules, Result, error)) { answered = f }(answered)
	answered = func(_ *Engine, q Query, _ Params, _ archive.Rules, _ Result, _ error) {
		if reflect.DeepEqual(q, aloneQ) {
			panic("boom alone")
		}
	}
	broken := 0
	got := make([]Answered, len(reqs))
	e.AnswerMany(context.Background(), reqs, func(i int, a Answered) {
		got[i] = a
		if i == 0 {
			panic("boom in done")
		}
	})
	for i, r := range reqs {
		if reflect.DeepEqual(r.Query, target) || reflect.DeepEqual(r.Query, aloneQ) {
			broken++
			if !errors.Is(got[i].Err, ErrInternal) {
				t.Errorf("%+v: %v, want the internal error", r.Query, got[i].Err)
			}
			continue
		}
		if d := diffAnswer(got[i], want[i].res, want[i].err); d != "" {
			t.Errorf("%+v: %s", r.Query, d)
		}
	}
	if broken < 2 || broken >= len(reqs)/2 {
		t.Fatalf("%d of %d requests broken", broken, len(reqs))
	}
}
