package native

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
)

// Request is one query of an AnswerMany: Query over Params, with Rules
// as Answer takes them. Ctx, when set, is the query's own context: once
// it is done the query alone leaves (nil: the call's context).
type Request struct {
	Query  Query
	Params Params
	Rules  archive.Rules
	Ctx    context.Context
}

// Answered is one request's answer, or why it has none.
type Answered struct {
	Result Result
	Err    error
}

// maxConsumers is how many aggregations one shared scan feeds; more are
// split over several scans.
const maxConsumers = 32

// AnswerMany answers reqs as Answer answers each, each answer the same as
// Answer's, and calls done once per request (i its index in reqs) as it
// is answered: from any goroutine, never two at once. It returns after
// the last call. done should return quickly: the answers of one scan are
// handed to it in turn.
//
// The stats, trends and breakdowns of one project read in one scan, in
// one turn of MaxScans, when they read the same table class (the events
// files' tables, events, views, product and measures, or sessions) and,
// for the events files, the same rules: each Parquet file's footer is
// read once and each row group once, with every column one of them needs,
// and each gap day's sessions are derived once. Each query keeps its own
// aggregation (a stat with compare "previous" two), with its own
// MaxGroups and MaxBytes, fed only the rows of its table and range, so
// its truncation and refusals are its own. A retention, an attributes and
// a query of identities are answered alone, by Answer.
//
// The call holds no more aggregations at once than MaxScans queries
// answered one by one would: an aggregation that keeps distinct ids or
// histograms (heavy: its memory follows the ids, not the groups) counts
// as one query, a scan of light ones alone as one, and so does a query
// answered alone; a scan takes at most MaxScans heavy aggregations (and
// maxConsumers in all), and the call's scans and alone queries run while
// their count stays within MaxScans. Independent scans so run beside each
// other as separate queries would.
//
// A shared scan also holds at most twice MaxBytes over its aggregations:
// once past it, the query holding the most leaves the scan and is
// answered alone, by Answer, after it. A query whose context is done
// leaves at once, answered with its error; the scan stops when none is
// left. A query that fails (refused, a value that does not parse, a batch
// it cannot read) fails alone; an error reading the files fails every
// query of the scan, and a file the daily pass removes meanwhile runs the
// whole scan again, every query from scratch, as Answer runs again.
func (e *Engine) AnswerMany(ctx context.Context, reqs []Request, done func(i int, a Answered)) {
	var mu sync.Mutex
	answer := func(i int, a Answered) {
		mu.Lock()
		defer mu.Unlock()
		done(i, a)
	}
	type scanKey struct {
		project  int64
		sessions bool
		rules    string
	}
	var (
		wg    sync.WaitGroup
		keys  []scanKey
		plans = map[scanKey][]*manyReq{}
		slots = newWeighted(cap(e.sem))
	)
	for i, r := range reqs {
		rctx := r.Ctx
		if rctx == nil {
			rctx = ctx
		}
		if alone(r.Query) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				answer(i, e.answerAlone(rctx, slots, r))
			}()
			continue
		}
		mr, err := e.prepare(rctx, i, r)
		if err != nil {
			answer(i, Answered{Err: err})
			continue
		}
		k := scanKey{project: r.Params.Project, sessions: mr.sessions}
		if !mr.sessions {
			k.rules = rulesKey(r.Params.Project, r.Rules)
		}
		if _, ok := plans[k]; !ok {
			keys = append(keys, k)
		}
		plans[k] = append(plans[k], mr)
	}
	for _, k := range keys {
		var chunk []*manyReq
		n, heavy := 0, 0
		flush := func() {
			if len(chunk) == 0 {
				return
			}
			// A request heavier than the slots (a compared stat with
			// MaxScans 1) takes them all.
			c, w := chunk, min(max(1, heavy), slots.size)
			wg.Add(1)
			go func() {
				defer wg.Done()
				e.shared(ctx, k.project, k.sessions, c, w, slots, answer)
			}()
			chunk, n, heavy = nil, 0, 0
		}
		for _, mr := range plans[k] {
			if n+len(mr.cons) > maxConsumers || heavy+mr.heavy > slots.size {
				flush()
			}
			chunk = append(chunk, mr)
			n += len(mr.cons)
			heavy += mr.heavy
		}
		flush()
	}
	wg.Wait()
}

// answerAlone is Answer's answer to r, in one of the call's slots.
func (e *Engine) answerAlone(ctx context.Context, slots *weighted, r Request) Answered {
	if err := slots.acquire(ctx, 1); err != nil {
		return Answered{Err: err}
	}
	defer slots.release(1)
	res, err := e.Answer(ctx, r.Query, r.Params, r.Rules)
	return Answered{Result: res, Err: err}
}

// AnswerAll is AnswerMany's answers, by request.
func (e *Engine) AnswerAll(ctx context.Context, reqs []Request) []Answered {
	out := make([]Answered, len(reqs))
	e.AnswerMany(ctx, reqs, func(i int, a Answered) { out[i] = a })
	return out
}

// alone says whether q is answered alone rather than in a shared scan: a
// retention, an attributes (each with its own scan) or a query of
// identities (one cached batch, nothing to share).
func alone(q Query) bool {
	return q.Kind == "retention" || q.Kind == "attributes" || q.Table == "identities"
}

// rulesKey is what of rules a project's scan reads with: the project's
// own rules, in order (compileRules sorts them too), so queries with the
// same rules share a scan.
func rulesKey(project int64, rules archive.Rules) string {
	var ev []string
	for _, r := range rules.Events {
		if r.ProjectID == project {
			ev = append(ev, fmt.Sprintf("%q %q %q %q", r.Family, r.Name, r.Action, r.To))
		}
	}
	var at []string
	for _, r := range rules.Attributes {
		if r.ProjectID == project {
			at = append(at, fmt.Sprintf("%q %q %q", r.Key, r.Action, r.To))
		}
	}
	slices.Sort(ev)
	slices.Sort(at)
	return strings.Join(ev, ";") + "|" + strings.Join(at, ";")
}

// The states of a shared scan's request.
const (
	reqLive      = iota // in the scan
	reqDone             // answered (failed, or its context done)
	reqEvicted          // left the scan for the memory budget: answered alone after it
	reqFinishing        // the scan is done: its result is ready
)

// manyReq is one request of a shared scan.
type manyReq struct {
	i        int
	ctx      context.Context
	req      Request
	s        scan
	sessions bool
	cons     []*consumer // an aggregation each, as s.aggSpecs lists them
	heavy    int         // how many of them keep distinct ids or histograms
	// guarded by sharedScan.mu
	state int
	res   Result
}

// consumer is one aggregation a shared scan feeds.
type consumer struct {
	r      *manyReq
	sp     aggSpec
	rng    Range
	lo, hi int64  // rng's days since 1970-01-01
	fam    string // the family its rows are of ("" for all)
	idCols []string

	mu      sync.RWMutex // a and ms, written under Lock; add under RLock
	a       *agg         // nil once its request left the scan
	ms      []metricSpec
	read    []string // the columns its batches hold
	ids     *firstSet
	derived atomic.Int64 // bytes of gap days' views charged to a, being derived
}

// prepare checks and compiles r, request i, for a shared scan, with its
// aggregations.
func (e *Engine) prepare(ctx context.Context, i int, r Request) (*manyReq, error) {
	filters, metrics, err := compileQuery(r.Query, r.Params)
	if err != nil {
		return nil, err
	}
	mr := &manyReq{i: i, ctx: ctx, req: r, sessions: r.Query.Table == "sessions",
		s: scan{e: e, q: r.Query, filters: filters, metrics: metrics, project: r.Params.Project}}
	for _, sp := range mr.s.aggSpecs(r.Params.From, r.Params.To) {
		c := &consumer{r: mr, sp: sp, rng: Range{From: sp.from, To: sp.to}, lo: epochDay(sp.from), hi: epochDay(sp.to)}
		if !mr.sessions {
			c.fam = mr.s.family()
			c.idCols = mr.s.identityColumnsOf(sp.dims)
		}
		mr.cons = append(mr.cons, c)
		if a, _ := mr.s.newAgg(sp); a.heavy() {
			mr.heavy++
		}
	}
	return mr, nil
}

// sharedScan is one scan feeding several requests' aggregations.
type sharedScan struct {
	e        *Engine
	project  int64
	sessions bool
	reqs     []*manyReq
	answer   func(int, Answered)
	cancel   context.CancelFunc

	mu   sync.Mutex // the requests' states, live
	live int

	budget  sync.Mutex   // one budget check at a time
	derived atomic.Int64 // bytes of gap days' views held, once (each consumer is charged them)
}

// errNoConsumer stops a work item no request of the scan reads any more.
var errNoConsumer = errors.New("native: no query reads this item")

// shared runs reqs, of project's table class, in one scan, in weight of
// the call's slots, then the ones the budget evicted alone.
func (e *Engine) shared(ctx context.Context, project int64, sessions bool, reqs []*manyReq, weight int, slots *weighted,
	answer func(int, Answered)) {
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	sh := &sharedScan{e: e, project: project, sessions: sessions, reqs: reqs, answer: answer, cancel: cancel, live: len(reqs)}
	// A request whose context is done leaves at once; each such call has
	// ended before shared returns.
	stops := make([]func(), len(reqs))
	for k, r := range reqs {
		left := make(chan struct{})
		stop := context.AfterFunc(r.ctx, func() {
			defer close(left)
			sh.leave(r, reqDone, Answered{Err: r.ctx.Err()})
		})
		stops[k] = func() {
			if !stop() {
				<-left
			}
		}
	}
	err := slots.acquire(sctx, weight)
	if err == nil {
		err = e.turn(sctx, func() error { return sh.attempt(sctx) })
		slots.release(weight)
	}
	for _, stop := range stops {
		stop()
	}
	var evicted []*manyReq
	for _, r := range reqs {
		sh.mu.Lock()
		state, res := r.state, r.res
		if state == reqLive || state == reqFinishing {
			r.state = reqDone
		}
		sh.mu.Unlock()
		switch {
		case state == reqEvicted:
			evicted = append(evicted, r)
		case state == reqFinishing && err == nil:
			answer(r.i, Answered{Result: res})
		case state == reqLive || state == reqFinishing:
			if err == nil {
				err = errors.New("native: a query left unanswered") // never: each live query is answered
			}
			answer(r.i, Answered{Err: err})
		}
	}
	var wg sync.WaitGroup
	for _, r := range evicted {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answer(r.i, e.answerAlone(r.ctx, slots, r.req))
		}()
	}
	wg.Wait()
}

// leave takes r out of the scan, in state (reqDone or reqEvicted), its
// aggregations dropped; done, a is r's answer. A request already out
// stays as it is. The scan stops once no request is live.
func (sh *sharedScan) leave(r *manyReq, state int, a Answered) {
	sh.mu.Lock()
	if r.state != reqLive {
		sh.mu.Unlock()
		return
	}
	r.state = state
	sh.live--
	if sh.live == 0 {
		sh.cancel()
	}
	sh.mu.Unlock()
	for _, c := range r.cons {
		c.mu.Lock()
		c.a, c.ms, c.ids = nil, nil, nil
		c.mu.Unlock()
	}
	if state == reqEvicted {
		onEvict()
		return
	}
	sh.answer(r.i, a)
}

// fail answers r with err, out of the scan.
func (sh *sharedScan) fail(r *manyReq, err error) { sh.leave(r, reqDone, Answered{Err: err}) }

// onEvict is called as the budget evicts a query; a variable so a test
// can count them.
var onEvict = func() {}

// onScanItem is called as a shared scan's worker takes a work item; a
// variable so a test can act mid-scan.
var onScanItem = func() {}

// attempt runs the scan once, every live request's aggregations fresh,
// and readies their results.
func (sh *sharedScan) attempt(ctx context.Context) error {
	e := sh.e
	var rs ruleSet
	if !sh.sessions {
		var rules archive.Rules
		sh.mu.Lock()
		for _, r := range sh.reqs {
			if r.state == reqLive {
				rules = r.req.Rules
				break
			}
		}
		sh.mu.Unlock()
		var err error
		if rs, err = e.ruleSet(sh.project, rules); err != nil {
			return err
		}
	}
	var cons []*consumer
	sh.mu.Lock()
	for _, r := range sh.reqs {
		if r.state != reqLive {
			continue
		}
		r.s.rules = rs
		for _, c := range r.cons {
			c.mu.Lock()
			c.a, c.ms = r.s.newAgg(c.sp)
			c.read = rs.columns(c.a.columns())
			c.ids = nil
			c.derived.Store(0)
			c.mu.Unlock()
			cons = append(cons, c)
		}
	}
	sh.mu.Unlock()
	sh.derived.Store(0)
	if len(cons) == 0 {
		return nil
	}
	var err error
	if sh.sessions {
		err = sh.scanSessions(ctx, cons)
	} else {
		err = sh.scanEvents(ctx, cons)
	}
	if err != nil {
		return err
	}
	for _, r := range sh.reqs {
		sh.mu.Lock()
		if r.state != reqLive {
			sh.mu.Unlock()
			continue
		}
		r.state = reqFinishing
		sh.mu.Unlock()
		as := make([]aggregated, len(r.cons))
		for k, c := range r.cons {
			c.mu.Lock()
			as[k] = aggregated{m: c.a.merge(), ms: c.ms}
			c.a, c.ids = nil, nil
			c.mu.Unlock()
		}
		res := r.s.result(as)
		sh.mu.Lock()
		r.res = res
		sh.mu.Unlock()
	}
	return nil
}

// use is a consumer of one work item; all says every row of the item is
// of its family and range.
type use struct {
	c   *consumer
	all bool
}

// manyItem is one work item of a shared scan: a log (index -1), a gap
// day (gapIndex) or a row group, with the consumers that read it.
type manyItem struct {
	g    group
	uses []use
}

// charge is c's charge, as aggregate's: n more bytes held, refused past
// the bounds; nothing once c left the scan.
func (sh *sharedScan) charge(c *consumer) func(int64) error {
	return func(n int64) error {
		c.mu.RLock()
		var err error
		if c.a != nil {
			err = c.a.charge(n)
		}
		c.mu.RUnlock()
		if n > 0 && err == nil {
			sh.checkBudget()
		}
		return err
	}
}

// failsScan says whether err, met reading for one consumer, fails the
// whole scan rather than the consumer alone: a file gone (the scan runs
// again) or the scan stopped.
func failsScan(ctx context.Context, err error) bool {
	return errors.Is(err, fs.ErrNotExist) || ctx.Err() != nil
}

// scanEvents feeds cons, of the events files' tables, from one scan of
// the project's files.
func (sh *sharedScan) scanEvents(ctx context.Context, cons []*consumer) error {
	e := sh.e
	all, err := archive.ListFiles(e.dir)
	if err != nil {
		return err
	}
	files := all[sh.project]
	for _, c := range cons {
		if a, _, _ := c.attached(); a == nil || len(c.idCols) == 0 {
			continue
		}
		ids, err := e.firstSeen(ctx, sh.project, files, needsNames(c.idCols), sh.charge(c))
		if err != nil {
			if failsScan(ctx, err) {
				return err
			}
			sh.fail(c.r, err)
			continue
		}
		c.mu.Lock()
		if c.a != nil {
			c.ids = ids
		}
		c.mu.Unlock()
	}
	// The logs, read whole, go first, so the row groups fill in beside
	// them; each in name order, as filesIn lists them.
	var work []manyItem
	for _, path := range files.Logs {
		if uses := overlapping(cons, path); len(uses) > 0 {
			work = append(work, manyItem{g: group{file: path, index: -1}, uses: uses})
		}
	}
	groups, err := sharedGroups(files.Parquet, cons)
	if err != nil {
		return err
	}
	work = append(work, groups...)
	readers := make([]groupReader, e.workers)
	err = scanParallel(ctx, work, e.workers, func(w int, it manyItem) error {
		onScanItem()
		if it.g.index < 0 {
			return sh.feedLog(ctx, w, it)
		}
		return sh.feedGroup(w, &readers[w], it)
	})
	for i := range readers {
		if cerr := readers[i].close(); err == nil {
			err = cerr
		}
	}
	return err
}

// overlapping are the consumers of cons whose range a file named as path
// may hold rows of, as filesIn picks a query's files.
func overlapping(cons []*consumer, path string) []use {
	var uses []use
	name := filepath.Base(path)
	for _, c := range cons {
		if overlaps(name, c.rng) {
			uses = append(uses, use{c: c})
		}
	}
	return uses
}

// sharedGroups lists, in file order, the row groups of files some of cons
// reads, as parquetGroups lists each one's: the files whose name overlaps
// its range, the groups its statistics allow, each footer read once.
func sharedGroups(files []string, cons []*consumer) ([]manyItem, error) {
	var out []manyItem
	for _, path := range files {
		fileUses := overlapping(cons, path)
		if len(fileUses) == 0 {
			continue
		}
		p, err := openFile(path)
		if err != nil {
			return nil, err
		}
		md := p.pf.Metadata()
		for i := range md.RowGroups {
			rg := &md.RowGroups[i]
			if rg.NumRows <= 0 {
				continue
			}
			st := statsOf(rg, p.famIdx, p.dayIdx)
			var uses []use
			for _, u := range fileUses {
				if keep, all := st.decide(u.c.fam, u.c.lo, u.c.hi); keep {
					uses = append(uses, use{c: u.c, all: all})
				}
			}
			if len(uses) > 0 {
				out = append(out, manyItem{g: group{file: path, index: i, rows: rg.NumRows}, uses: uses})
			}
		}
		if err := p.close(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return out, nil
}

// attached is c's aggregation and what it reads, under RLock; a nil when
// c left the scan.
func (c *consumer) attached() (a *agg, read []string, ids *firstSet) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.a, c.read, c.ids
}

// feedLog reads the log of it for each of its consumers, as Answer reads
// it for that query alone (the cache decodes it once), and feeds them.
func (sh *sharedScan) feedLog(ctx context.Context, w int, it manyItem) error {
	for _, u := range it.uses {
		c := u.c
		a, read, ids := c.attached()
		if a == nil {
			continue
		}
		bs, err := sh.e.readLog(ctx, it.g.file, read, c.fam, c.rng)
		if err != nil {
			if failsScan(ctx, err) {
				return err
			}
			sh.fail(c.r, err)
			continue
		}
		for _, b := range bs {
			b = c.r.s.rules.apply(b, c.fam)
			if ids != nil {
				addIdentityColumns(b, c.idCols, ids)
			}
			if !sh.feed(w, c, b) {
				break
			}
		}
	}
	return nil
}

// feedGroup reads the row group of it once, with every column its live
// consumers read (and day and family when one needs them to filter), and
// feeds each its rows, as Answer reads the group for that query alone.
func (sh *sharedScan) feedGroup(w int, gr *groupReader, it manyItem) error {
	type live struct {
		use
		read []string
		ids  *firstSet
	}
	var (
		lives      []live
		union      []string
		seen       = map[string]bool{}
		day, famly bool
	)
	for _, u := range it.uses {
		a, read, ids := u.c.attached()
		if a == nil {
			continue
		}
		lives = append(lives, live{use: u, read: read, ids: ids})
		for _, n := range read {
			if !seen[n] {
				seen[n] = true
				union = append(union, n)
			}
		}
		if !u.all {
			day = true
			famly = famly || u.c.fam != ""
		}
	}
	if len(lives) == 0 {
		return nil
	}
	p, err := gr.open(it.g.file)
	if err != nil {
		return err
	}
	read := expandCols(p.cols, union)
	if day {
		extra := []string{"day"}
		if famly {
			extra = append(extra, "family")
		}
		read = expandCols(nil, append(read, extra...))
	}
	b, err := p.readGroup(it.g, read)
	if err != nil {
		return err
	}
	for _, l := range lives {
		c := l.c
		requested := expandCols(p.cols, l.read)
		cb := pickCols(b, requested)
		if !l.all {
			cb.Cols["day"] = b.Cols["day"]
			if c.fam != "" {
				cb.Cols["family"] = b.Cols["family"]
			}
		}
		cb = finish(cb, requested, l.all, c.fam, c.lo, c.hi)
		if !sh.sessions {
			cb = c.r.s.rules.apply(cb, c.fam)
			if l.ids != nil {
				addIdentityColumns(cb, c.idCols, l.ids)
			}
		}
		sh.feed(w, c, cb)
	}
	return nil
}

// pickCols is a batch of b's columns names, in a map of its own.
func pickCols(b *Batch, names []string) *Batch {
	out := &Batch{N: b.N, Cols: make(map[string]*Column, len(names)+2)}
	for _, n := range names {
		out.Cols[n] = b.Cols[n]
	}
	return out
}

// feed adds b to c's aggregation from worker w, failing c's request on an
// error, then checks the scan's budget; false once c is out of the scan.
func (sh *sharedScan) feed(w int, c *consumer, b *Batch) bool {
	c.mu.RLock()
	a := c.a
	var err error
	if a != nil {
		err = a.add(w, b)
	}
	c.mu.RUnlock()
	if a == nil {
		return false
	}
	if err != nil {
		sh.fail(c.r, err)
		return false
	}
	sh.checkBudget()
	return true
}

// checkBudget evicts, while the scan's aggregations hold more than twice
// MaxBytes put together, the live request holding the most (a gap day's
// views counted once, though each consumer is charged them).
func (sh *sharedScan) checkBudget() {
	sh.budget.Lock()
	defer sh.budget.Unlock()
	limit := 2 * sh.e.maxBytes
	for {
		total := sh.derived.Load()
		var most *manyReq
		var mostN int64
		for _, r := range sh.reqs {
			var held int64
			for _, c := range r.cons {
				c.mu.RLock()
				if c.a != nil {
					held += c.a.mem.n.Load() - c.derived.Load()
				}
				c.mu.RUnlock()
			}
			total += held
			if most == nil || held > mostN {
				most, mostN = r, held
			}
		}
		if total <= limit || mostN <= 0 {
			return
		}
		sh.leave(most, reqEvicted, Answered{})
	}
}

// scanSessions feeds cons, of sessions, from one scan of the project's
// session files and one derivation of each gap day.
func (sh *sharedScan) scanSessions(ctx context.Context, cons []*consumer) error {
	e := sh.e
	hull := cons[0].rng
	for _, c := range cons[1:] {
		if c.rng.From.Before(hull.From) {
			hull.From = c.rng.From
		}
		if hull.To.Before(c.rng.To) {
			hull.To = c.rng.To
		}
	}
	files, gaps, events, err := sessionSources(e.dir, sh.project, hull)
	if err != nil {
		return err
	}
	// The gap days, derived from their views, are the heavier items: they
	// go first, so the row groups fill in beside them.
	var work []manyItem
	for _, d := range gaps {
		day := epochDay(d)
		var uses []use
		for _, c := range cons {
			if day >= c.lo && day <= c.hi {
				uses = append(uses, use{c: c, all: true})
			}
		}
		if len(uses) > 0 {
			work = append(work, manyItem{g: group{index: gapIndex, day: day}, uses: uses})
		}
	}
	groups, err := sharedGroups(files, cons)
	if err != nil {
		return err
	}
	work = append(work, groups...)
	readers := make([]groupReader, e.workers)
	err = scanParallel(ctx, work, e.workers, func(w int, it manyItem) error {
		onScanItem()
		if it.g.index != gapIndex {
			return sh.feedGroup(w, &readers[w], it)
		}
		b, err := deriveSessions(ctx, sh.project, it.g.day, events, &readers[w], e.readLog, sh.chargeAll(it.uses))
		if errors.Is(err, errNoConsumer) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, u := range it.uses {
			sh.feed(w, u.c, b)
		}
		return nil
	})
	for i := range readers {
		if cerr := readers[i].close(); err == nil {
			err = cerr
		}
	}
	return err
}

// chargeAll charges each of uses' consumers the bytes a gap day's views
// take, as each alone would be (deriveSessions), failing those it takes
// past their bounds; errNoConsumer once none of them is left.
func (sh *sharedScan) chargeAll(uses []use) func(int64) error {
	return func(n int64) error {
		sh.derived.Add(n)
		alive := 0
		for _, u := range uses {
			c := u.c
			c.mu.RLock()
			var err error
			if c.a != nil {
				c.derived.Add(n)
				err = c.a.charge(n)
				alive++
			}
			c.mu.RUnlock()
			if err != nil {
				sh.fail(c.r, err)
				alive--
			}
		}
		if n <= 0 {
			return nil
		}
		sh.checkBudget()
		if alive == 0 {
			return errNoConsumer
		}
		return nil
	}
}

// weighted is a counting semaphore of size units, taken several at once:
// the waiters are served in order, so a large one is not starved.
type weighted struct {
	size    int
	mu      sync.Mutex
	free    int
	waiters []*waiter
}

type waiter struct {
	n     int
	ready chan struct{}
}

func newWeighted(size int) *weighted { return &weighted{size: size, free: size} }

// acquire takes n units, waiting until they are free and every earlier
// waiter served, or until ctx is done.
func (w *weighted) acquire(ctx context.Context, n int) error {
	w.mu.Lock()
	if w.free >= n && len(w.waiters) == 0 {
		w.free -= n
		w.mu.Unlock()
		return nil
	}
	wt := &waiter{n: n, ready: make(chan struct{})}
	w.waiters = append(w.waiters, wt)
	w.mu.Unlock()
	select {
	case <-wt.ready:
		return nil
	case <-ctx.Done():
		w.mu.Lock()
		defer w.mu.Unlock()
		select {
		case <-wt.ready: // served meanwhile: give the units back
			w.free += n
			w.serve()
		default:
			w.waiters = slices.DeleteFunc(w.waiters, func(x *waiter) bool { return x == wt })
			w.serve()
		}
		return ctx.Err()
	}
}

// release gives n units back.
func (w *weighted) release(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.free += n
	w.serve()
}

// serve hands free units to the waiters in order, while the first fits.
func (w *weighted) serve() {
	for len(w.waiters) > 0 && w.waiters[0].n <= w.free {
		wt := w.waiters[0]
		w.free -= wt.n
		w.waiters = w.waiters[1:]
		close(wt.ready)
	}
}
