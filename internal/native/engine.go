package native

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Options configure an Engine. Zero fields take their defaults.
type Options struct {
	// Dir is the events directory (raw/).
	Dir string
	// Workers is how many goroutines one query scans with (default 2).
	Workers int
	// MaxScans is how many queries scan at once (default 2); the others
	// wait for a turn, or give up when their context is done.
	MaxScans int
	// MaxGroups is how many distinct groups a breakdown tracks (default
	// 100,000); a trend tracks as many series as fit 10 × MaxGroups cells
	// (series × days). Rows of further groups are left out and the result
	// says it is truncated.
	MaxGroups int
	// MaxBytes bounds one scan's aggregation (default 128 MiB), counted
	// approximately: the groups with their strings, the per-cell arrays,
	// the whole-range distinct sets (exact keys, at most about one 16 KiB
	// sketch's worth each, or that sketch) and the per-day distinct keys
	// (16 bytes per distinct id and day, up to twice that per worker
	// between compactions). It is checked after each batch, so a batch's
	// growth may pass it. A stat or a trend without series, which has no
	// group to leave out, is refused with ErrTooLarge once past MaxBytes.
	// A breakdown or a split trend past it admits no new group (its rows
	// are left out and the result says it is truncated); what the groups
	// admitted add then (their sets and per-day keys) may take it to twice
	// MaxBytes, past which it is refused with ErrTooLarge too. Merging the
	// workers' partials holds the per-day keys twice for a moment.
	MaxBytes int64
}

// Engine answers structured queries over the event files of Dir.
type Engine struct {
	dir                string
	workers, maxGroups int
	maxBytes           int64
	sem                chan struct{}

	logKeys  *archive.KeyScanner // each open log's keys, read as far as it grew
	colMu    sync.Mutex          // guards colFiles, never held over a read
	colFiles map[string]fileCols // each Parquet file's columns, by path
}

// New returns an engine over o.Dir.
func New(o Options) *Engine {
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.MaxScans <= 0 {
		o.MaxScans = 2
	}
	if o.MaxGroups <= 0 {
		o.MaxGroups = 100_000
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 128 << 20
	}
	return &Engine{dir: o.Dir, workers: o.Workers, maxGroups: o.MaxGroups, maxBytes: o.MaxBytes, sem: make(chan struct{}, o.MaxScans),
		logKeys: archive.NewKeyScanner()}
}

// Params are what a query runs on: one project, the days From to To.
type Params struct {
	Project  int64
	From, To civil.Date
}

// Result is a query's answer, every value as text: integers in decimal,
// floats in their shortest form, days as YYYY-MM-DD, NULL as "".
type Result struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
	// Approximate names the columns holding any value a sketch estimated
	// (a whole-range distinct count past hll.ExactLimit ids).
	Approximate []string `json:"approximate,omitempty"`
}

// The retry bounds of Answer when a file vanishes under it; variables so a
// test can shorten them.
var (
	maxAttempts     = 10
	retryBackoff    = 50 * time.Millisecond
	retryBackoffMax = time.Second
)

// Answer answers q over project p.Project's rows of p.From to p.To, with
// rules (every project's; the project's own apply) applied as the
// reader's events view applies them. A malformed query, or a filter value
// that does not parse for its column, is refused with an error wrapping
// ErrInvalid; one whose aggregation needs more than MaxBytes allows, with
// one wrapping ErrTooLarge. Answer waits for one of MaxScans turns first,
// and stops when ctx is done.
//
// A file the daily pass removes while the query reads (a dump deletes its
// log, a merge its days) fails the attempt; the whole query is run again,
// on the files as they are then, up to maxAttempts times.
//
// The kinds' rows:
//   - stat: one row, a column per metric, then with compare "previous" a
//     previous_<name> column per metric over the range of as many days
//     ending the day before From;
//   - trend: day, series when split, the metric; a row per day (and
//     series) with rows, by day then series; only the series_limit (5)
//     series ranking first by the metric over the whole range, ties by
//     their value (a distinct count past hll.ExactLimit ids ranks by its
//     estimate; the per-day values stay exact);
//   - breakdown: a column per dimension, then per metric; the limit (20)
//     groups ranking first by the order metric (the first), ties by the
//     dimensions, or with order naming a dimension sorted by it ascending,
//     ties by the other dimensions; then with other an "Other" row (every
//     dimension "Other") of the rest, when there is any.
//
// A truncated result (MaxGroups, MaxBytes) leaves the untracked groups
// out of a breakdown's Other row too, and ranks a trend's tracked series
// only. Groups and series sort NULLs last, numbers by value. Metrics are SQL's:
// count is COUNT(*), actors, users and groups COUNT(DISTINCT actor_id),
// user_id and group_id (NULL left out), sum, avg, min and max NULL over no
// value, daily_* the per-day distinct counts' SUM, MAX or AVG over the
// days with rows (NULL over none).
func (e *Engine) Answer(ctx context.Context, q Query, p Params, rules archive.Rules) (Result, error) {
	if err := q.Validate(); err != nil {
		return Result{}, err
	}
	if p.To.Before(p.From) {
		return Result{}, invalid("to", "%s is before from %s", p.To, p.From)
	}
	filters, err := compileFilters(q.Where)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	defer func() { <-e.sem }()
	backoff := retryBackoff
	for attempt := 1; ; attempt++ {
		res, err := e.answerOnce(ctx, q, filters, p, rules)
		if err == nil || !errors.Is(err, fs.ErrNotExist) || attempt == maxAttempts {
			return res, err
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, retryBackoffMax)
	}
}

func (e *Engine) answerOnce(ctx context.Context, q Query, filters []filter, p Params, rules archive.Rules) (Result, error) {
	rs, err := e.ruleSet(p.Project, rules)
	if err != nil {
		return Result{}, err
	}
	s := scan{e: e, q: q, filters: filters, project: p.Project, rules: rs}
	switch q.Kind {
	case "stat":
		return s.stat(ctx, p.From, p.To)
	case "trend":
		return s.trend(ctx, p.From, p.To)
	}
	return s.breakdown(ctx, p.From, p.To)
}

// ruleSet is project's rules compiled. The files' columns, which only
// attribute rules need, are read only when the project has one.
func (e *Engine) ruleSet(project int64, rules archive.Rules) (ruleSet, error) {
	var have []string
	if slices.ContainsFunc(rules.Attributes, func(r store.AttributeRule) bool { return r.ProjectID == project }) {
		var err error
		if have, err = e.columns(); err != nil {
			return ruleSet{}, err
		}
	}
	return compileRules(project, rules, have), nil
}

// columns is unionColumns(e.dir): an open log's keys are read only as far
// as it grew since (archive.KeyScanner, under live ingest the lines
// appended), a Parquet file's footer only when the file is new or changed
// in size or time since it was last read. colMu guards the footer cache
// only: it is copied out, the files read without it, and the new cache
// put back (the gone files drop out).
func (e *Engine) columns() ([]string, error) {
	e.colMu.Lock()
	known := e.colFiles
	e.colMu.Unlock()
	next := make(map[string]fileCols, len(known))
	var logs []string
	cols, err := unionColumnsOf(e.dir, func(path string, log bool) ([]string, error) {
		if log {
			logs = append(logs, path)
			keys, err := e.logKeys.Keys(path)
			if err != nil {
				return nil, err
			}
			return logColumnNames(keys), nil
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		fc, ok := known[path]
		if !ok || fc.size != st.Size() || !fc.mtime.Equal(st.ModTime()) {
			names, err := readFileColumns(path, false)
			if err != nil {
				return nil, err
			}
			fc = fileCols{size: st.Size(), mtime: st.ModTime(), names: names}
		}
		next[path] = fc
		return fc.names, nil
	})
	if err != nil {
		return nil, err
	}
	e.colMu.Lock()
	e.colFiles = next
	e.colMu.Unlock()
	e.logKeys.Keep(logs)
	return cols, nil
}

// readFileColumns is fileColumns; a variable so a test can count the
// Parquet footers read.
var readFileColumns = fileColumns

// fileCols is one file's columns as read when it had size and mtime.
type fileCols struct {
	size  int64
	mtime time.Time
	names []string
}

// scan runs one query's scans.
type scan struct {
	e       *Engine
	q       Query
	filters []filter
	project int64
	rules   ruleSet
}

// family is the family the query's table holds ("" for events: all).
func (s scan) family() string {
	if s.q.Table == "events" {
		return ""
	}
	return s.q.Table
}

// aggregate reads the project's rows of [from, to] into an aggregation of
// the query's metrics grouped by dims, by day when perDay; sets keeps
// whole-range distinct sets beside a trend's per-day keys.
func (s scan) aggregate(ctx context.Context, from, to civil.Date, dims []string, perDay, sets bool) (*merged, []metricSpec, error) {
	r := Range{From: from, To: to}
	lo, hi := r.days()
	ms := make([]metricSpec, len(s.q.Metrics))
	for i, m := range s.q.Metrics {
		ms[i] = metricSpec{Metric: m}
	}
	a, ms := newAgg(aggOptions{metrics: ms, filters: s.filters, dims: dims, perDay: perDay, sets: sets,
		from: lo, ndays: int(hi - lo + 1), workers: s.e.workers, maxGroups: s.e.maxGroups, maxBytes: s.e.maxBytes})
	fam := s.family()
	read := s.rules.columns(a.columns())
	pq, logs, err := projectFiles(s.e.dir, s.project, r)
	if err != nil {
		return nil, nil, err
	}
	groups, err := parquetGroups(pq, fam, r)
	if err != nil {
		return nil, nil, err
	}
	// The logs, read whole, go first, so the row groups fill in beside them.
	work := make([]group, 0, len(logs)+len(groups))
	for _, path := range logs {
		work = append(work, group{file: path, index: -1})
	}
	work = append(work, groups...)
	readers := make([]groupReader, s.e.workers)
	err = scanParallel(ctx, work, s.e.workers, func(w int, g group) error {
		var b *Batch
		var err error
		if g.index < 0 {
			b, err = readLog(g.file, read, fam, r)
		} else {
			b, err = readers[w].read(g, read, fam, r)
		}
		if err != nil {
			return err
		}
		return a.add(w, s.rules.apply(b, fam))
	})
	for i := range readers {
		if cerr := readers[i].close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return nil, nil, err
	}
	return a.merge(), ms, nil
}

// columnsOut is a result's columns, and marks the approximate ones.
type columnsOut struct {
	names  []string
	approx []bool
}

func (c *columnsOut) add(name string) int {
	c.names = append(c.names, name)
	c.approx = append(c.approx, false)
	return len(c.names) - 1
}

func (c *columnsOut) result(rows [][]string, truncated bool) Result {
	res := Result{Columns: c.names, Rows: rows, Truncated: truncated}
	if rows == nil {
		res.Rows = [][]string{}
	}
	for i, a := range c.approx {
		if a {
			res.Approximate = append(res.Approximate, c.names[i])
		}
	}
	return res
}

func (s scan) stat(ctx context.Context, from, to civil.Date) (Result, error) {
	var cols columnsOut
	var row []string
	one := []int32{0}
	values := func(from, to civil.Date, prefix string) error {
		m, ms, err := s.aggregate(ctx, from, to, nil, false, false)
		if err != nil {
			return err
		}
		for _, mt := range ms {
			i := cols.add(prefix + mt.Name())
			v := m.compute(mt, one, -1)
			cols.approx[i] = v.approx
			row = append(row, v.String())
		}
		return nil
	}
	if err := values(from, to, ""); err != nil {
		return Result{}, err
	}
	if s.q.Compare == "previous" {
		days := int(epochDay(to)-epochDay(from)) + 1
		if err := values(from.AddDays(-days), from.AddDays(-1), "previous_"); err != nil {
			return Result{}, err
		}
	}
	return cols.result([][]string{row}, false), nil
}

func label(v dimVal) string {
	if v.null {
		return ""
	}
	return v.s
}

func (s scan) trend(ctx context.Context, from, to civil.Date) (Result, error) {
	var dims []string
	if s.q.Series != "" {
		dims = []string{s.q.Series}
	}
	m, ms, err := s.aggregate(ctx, from, to, dims, true, len(dims) > 0)
	if err != nil {
		return Result{}, err
	}
	mt := ms[0]
	var cols columnsOut
	cols.add("day")
	series := []int32{0}
	truncated := false
	if len(dims) > 0 {
		cols.add("series")
		typ := m.a.dimTypes[0]
		vals := m.a.groups.vals
		truncated = m.a.groups.dropped
		series = make([]int32, m.groups)
		rank := make([]value, m.groups)
		for g := range series {
			series[g] = int32(g)
			rank[g] = m.compute(mt, []int32{int32(g)}, -1)
		}
		slices.SortFunc(series, func(a, b int32) int {
			if c := cmpDesc(rank[a], rank[b]); c != 0 {
				return c
			}
			return cmpDim(typ, vals[a][0], vals[b][0])
		})
		limit := s.q.SeriesLimit
		if limit == 0 {
			limit = 5
		}
		series = series[:min(limit, len(series))]
		slices.SortFunc(series, func(a, b int32) int { return cmpDim(typ, vals[a][0], vals[b][0]) })
	}
	cols.add(mt.Name())
	var rows [][]string
	for d := range m.a.ndays {
		day := formatDay(m.a.from + int64(d))
		for _, g := range series {
			if m.rows(g, d) == 0 {
				continue
			}
			row := []string{day}
			if len(dims) > 0 {
				row = append(row, label(m.a.groups.vals[g][0]))
			}
			rows = append(rows, append(row, m.compute(mt, []int32{g}, d).String()))
		}
	}
	return cols.result(rows, truncated), nil
}

func (s scan) breakdown(ctx context.Context, from, to civil.Date) (Result, error) {
	m, ms, err := s.aggregate(ctx, from, to, s.q.Dimensions, false, false)
	if err != nil {
		return Result{}, err
	}
	var cols columnsOut
	for _, d := range s.q.Dimensions {
		cols.add(d)
	}
	order, byDim := 0, slices.Index(s.q.Dimensions, s.q.Order)
	for i, mt := range ms {
		cols.add(mt.Name())
		if mt.Name() == s.q.Order {
			order = i
		}
	}
	vals, types := m.a.groups.vals, m.a.dimTypes
	gs := make([]int32, m.groups)
	for g := range gs {
		gs[g] = int32(g)
	}
	byDims := func(a, b int32) int {
		for j, t := range types {
			if c := cmpDim(t, vals[a][j], vals[b][j]); c != 0 {
				return c
			}
		}
		return 0
	}
	if byDim >= 0 {
		// Ordered by a dimension: ascending, ties by the others.
		slices.SortFunc(gs, func(a, b int32) int {
			if c := cmpDim(types[byDim], vals[a][byDim], vals[b][byDim]); c != 0 {
				return c
			}
			return byDims(a, b)
		})
	} else {
		rank := make([]value, m.groups)
		for g := range gs {
			rank[g] = m.compute(ms[order], []int32{int32(g)}, -1)
		}
		slices.SortFunc(gs, func(a, b int32) int {
			if c := cmpDesc(rank[a], rank[b]); c != 0 {
				return c
			}
			return byDims(a, b)
		})
	}
	limit := s.q.Limit
	if limit == 0 {
		limit = 20
	}
	top, rest := gs[:min(limit, len(gs))], gs[min(limit, len(gs)):]
	var rows [][]string
	metrics := func(row []string, groups []int32) []string {
		for i, mt := range ms {
			v := m.compute(mt, groups, -1)
			if v.approx {
				cols.approx[len(types)+i] = true
			}
			row = append(row, v.String())
		}
		return row
	}
	for _, g := range top {
		row := make([]string, 0, len(cols.names))
		for _, v := range vals[g] {
			row = append(row, label(v))
		}
		rows = append(rows, metrics(row, []int32{g}))
	}
	if s.q.Other && len(rest) > 0 {
		row := make([]string, 0, len(cols.names))
		for range types {
			row = append(row, "Other")
		}
		rows = append(rows, metrics(row, rest))
	}
	return cols.result(rows, m.a.groups.dropped), nil
}
