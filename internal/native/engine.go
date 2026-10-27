package native

import (
	"context"
	"errors"
	"fmt"
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
	// MaxScans is how many queries scan at once (DefaultMaxScans); the others
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
	// sketch's worth each, or that sketch), the per-day distinct keys
	// (16 bytes per distinct id and day, up to twice that per worker
	// between compactions) and the measures' bucket histograms (an entry
	// per cell and bucket with rows). It is checked after each batch, so
	// a batch's growth may pass it. A stat or a trend without series,
	// which has no group to leave out, is refused with ErrTooLarge once
	// past MaxBytes.
	// A breakdown or a split trend past it admits no new group (its rows
	// are left out and the result says it is truncated); what the groups
	// admitted add then (their sets and per-day keys) may take it to twice
	// MaxBytes, past which it is refused with ErrTooLarge too. Merging the
	// workers' partials holds the per-day keys and the histograms twice
	// for a moment, uncounted. A retention query charges its per-actor
	// state and is refused with ErrTooLarge past MaxBytes.
	//
	// The engine as a whole holds at most about MaxBytes of identities
	// (its cache), MaxBytes of open logs' decoded rows (its log cache,
	// emptied after five idle minutes) and MaxScans × MaxBytes of
	// aggregations (AnswerMany).
	MaxBytes int64
	// Now is the clock (default time.Now): its UTC day is today, which a
	// retention query counts up to, leaving it out.
	Now func() time.Time
}

// Engine answers structured queries over the event files of Dir.
type Engine struct {
	dir                string
	workers, maxGroups int
	maxBytes           int64
	// slots are the MaxScans turns, taken several at once by a shared
	// scan with more than one heavy aggregation (turnFor).
	slots *weighted

	logKeys  *archive.KeyScanner // each open log's keys, read as far as it grew
	colMu    sync.Mutex          // guards colFiles, never held over a read
	colFiles map[string]fileCols // each Parquet file's columns, by path

	idCache identityCache // each project's identities, up to MaxBytes of them
	logs    *logCache     // each open log's decoded rows, up to MaxBytes of them
	retMemo retentionMemo // recent retention tallies, so a dashboard's widgets scan once

	now func() time.Time
}

// DefaultMaxScans is Options.MaxScans when it is not set.
const DefaultMaxScans = 2

// New returns an engine over o.Dir.
func New(o Options) *Engine {
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.MaxScans <= 0 {
		o.MaxScans = DefaultMaxScans
	}
	if o.MaxGroups <= 0 {
		o.MaxGroups = 100_000
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 128 << 20
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Engine{dir: o.Dir, workers: o.Workers, maxGroups: o.MaxGroups, maxBytes: o.MaxBytes, slots: newWeighted(o.MaxScans),
		logKeys: archive.NewKeyScanner(), idCache: identityCache{max: o.MaxBytes}, logs: newLogCache(o.MaxBytes), now: o.Now}
}

// readLog is readLog's rows, from the engine's log cache: a batch per
// family, each read-only.
func (e *Engine) readLog(ctx context.Context, path string, cols []string, family string, r Range) ([]*Batch, error) {
	return e.logs.read(ctx, path, cols, family, r)
}

// today is the clock's UTC day.
func (e *Engine) today() civil.Date { return civil.DateOf(e.now()) }

// Today is the UTC day of the engine's clock, the day a retention counts
// up to, leaving it out.
func (e *Engine) Today() civil.Date { return e.today() }

// charge is a fresh budget of MaxBytes: each call adds n bytes (n < 0
// gives them back) and, once past MaxBytes, refuses with ErrTooLarge. It
// is safe from several goroutines at once.
func (e *Engine) charge() func(n int64) error {
	m := &memory{max: e.maxBytes}
	return func(n int64) error {
		m.add(n)
		if n > 0 && m.over() {
			return fmt.Errorf("%w (about %d MiB counted, %d MiB allowed)", ErrTooLarge, m.n.Load()>>20, m.max>>20)
		}
		return nil
	}
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
// events table applies them; the sessions and identities tables,
// derived from the events as stored, take none (scanSessions, identities).
// identities holds the project's users and groups first seen in the
// range, its day their first_seen's day; the computed identity columns
// (user_status, user_label, user_first_seen and their group ones) look
// each row's id up in their first-seen days alone (firstSeen), with the
// names for a label, over the whole history. Identities are cached
// between queries (at most MaxBytes of them), their stored and live
// parts apart, so a log's flush derives only the logs' part again; so
// are the open logs' decoded rows (at most MaxBytes of them, logCache),
// so a log is decoded once while it grows rather than once a query; while
// the derived identities file is missing or stale (older than an events
// file), the next query that needs them derives them again from every
// event the project's files hold. A malformed query, or a filter value
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
//     estimate; the per-day values stay exact). With several metrics
//     (unsplit), day, series (the metric's name) and value: a row per
//     day and metric with rows passing the metric's where (a ratio's,
//     either side's), by day then the metrics' order. With fill zero,
//     every day of the range has its row per kept series or metric, a
//     counted metric (countedOps) reading 0 where it is empty;
//   - breakdown: a column per dimension, then per metric; the limit (20)
//     groups ranking first by the order metric (the first), ties by the
//     dimensions, or with order naming a dimension sorted by it ascending,
//     ties by the other dimensions; then with other an "Other" row (every
//     dimension "Other") of the rest, when there are two or more groups
//     left (a single one is listed under its own name, so limit 4 gives
//     at most 5 rows).
//   - retention: its output's rows (scan.retention), over the cohorts of
//     From to To, the days back up to the day before the clock's today;
//   - attributes: its dimensions, attribute and value, then its metrics,
//     a row per attribute value of the rows unpivoted (scan.attributes);
//     output keys attribute, events, values and keys_total, a row per
//     attribute (scan.attributeKeys).
//
// A truncated result (MaxGroups, MaxBytes) leaves the untracked groups
// out of a breakdown's Other row too, and ranks a trend's tracked series
// only. Groups and series sort NULLs last, numbers by value. Metrics are SQL's:
// count is COUNT(*), actors, users and groups COUNT(DISTINCT actor_id),
// user_id and group_id (NULL left out), sum, avg, min and max NULL over no
// value, daily_* the per-day distinct counts' SUM, MAX or AVG over the
// days with rows (NULL over none), days count(DISTINCT day) (0 over
// none); on sessions bounces is COUNT(*) FILTER
// (WHERE views = 1) and bounce_rate that over COUNT(*) as a DOUBLE (NULL
// over no session; an Other row's over its folded groups' sessions). On
// measures, a sample weighs 1 / sample_rate: weight is SUM(1 /
// sample_rate), mean SUM(value / sample_rate) over that, and percentile
// the widgets' recipe over the log-scale buckets (bucket 2 * 1.04^bucket /
// 2.04, 0 for -1000): the value of the first bucket, ascending, whose
// running weight reaches q of the total (NULL over no row; an Other row's
// over its folded groups' buckets put together); not approximate, the
// buckets being the answer's resolution.
// A metric with a where counts only the rows that pass it too (SQL's
// FILTER): its groups are still those of the query's where, so a group
// none of whose rows pass reads 0 for count, a distinct count and
// bounces, NULL for the others. A metric with round is rounded as
// DuckDB's ROUND rounds a DOUBLE, after Other is put together and
// before groups and series are ranked by it, as SQL's ORDER BY of the
// rounded column ranks them.
// ratio is SQL's num::DOUBLE / NULLIF(den, 0) of its two metrics, each
// with its own where (NULL when either is NULL, but a counted num, a
// daily_* or weight over no rows, reads 0 when den is above 0, as the
// widgets' COALESCE(num, 0) has it), computed from the same
// partials as the other metrics: over a breakdown's Other row both sides
// are put together over its folded groups first, over a stat's previous
// range both are the previous range's; approximate when either side is,
// and rounded as the ratio says.
// share is a group's of (count: its rows, of the metric's where when it
// has one; weight: their SUM(1 / sample_rate)) over its partition's, as
// SQL's x / SUM(x) OVER (PARTITION BY …) reads it before the limit: in a
// breakdown the groups of its first dimension's value (every group with
// one dimension), in a trend the day's groups, every series (over the
// range when the series are ranked); an Other row's partition is every
// group. It is NULL when the partition's total is 0, and never
// approximate.
func (e *Engine) Answer(ctx context.Context, q Query, p Params, rules archive.Rules) (Result, error) {
	filters, metrics, err := compileQuery(q, p)
	if err != nil {
		return Result{}, err
	}
	var res Result
	err = e.turn(ctx, func() error {
		var err error
		res, err = e.answerOnce(ctx, q, filters, metrics, p, rules)
		return err
	})
	if answered != nil {
		answered(e, q, p, rules, res, err)
	}
	return res, err
}

// answered, when set, is told each Answer's query and answer (after the
// checks compileQuery makes); nil in production, set only by a test's
// recorder.
var answered func(e *Engine, q Query, p Params, rules archive.Rules, res Result, err error)

// compileQuery checks q over p and compiles its filters and metrics'.
func compileQuery(q Query, p Params) ([]filter, []metricSpec, error) {
	if err := q.Validate(); err != nil {
		return nil, nil, err
	}
	if p.To.Before(p.From) {
		return nil, nil, invalid("to", "%s is before from %s", p.To, p.From)
	}
	filters, err := compileFilters(q.Where, "where")
	if err != nil {
		return nil, nil, err
	}
	if _, err := compileDefines(q.Define); err != nil { // newAgg compiles them again, unable to fail then
		return nil, nil, err
	}
	metrics := make([]metricSpec, len(q.Metrics))
	for i, m := range q.Metrics {
		if metrics[i], err = compileMetric(m, fmt.Sprintf("metrics[%d]", i)); err != nil {
			return nil, nil, err
		}
	}
	return filters, metrics, nil
}

// turn runs once in one of MaxScans turns, waiting for it first and
// giving up when ctx is done. A file the daily pass removes while once
// reads (an error wrapping fs.ErrNotExist) fails the attempt; once runs
// again, on the files as they are then, up to maxAttempts times.
func (e *Engine) turn(ctx context.Context, once func() error) error { return e.turnFor(ctx, 1, once) }

// turnFor is turn taking weight of the MaxScans slots at once (at most
// all of them): a shared scan takes one per heavy aggregation it feeds,
// so the engine never holds more of them than MaxScans queries would.
func (e *Engine) turnFor(ctx context.Context, weight int, once func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	weight = min(max(1, weight), e.slots.size)
	if err := e.slots.acquire(ctx, weight); err != nil {
		return err
	}
	defer e.slots.release(weight)
	onTurn(weight)
	defer onTurn(-weight)
	backoff := retryBackoff
	for attempt := 1; ; attempt++ {
		err := once()
		if err == nil || !errors.Is(err, fs.ErrNotExist) || attempt == maxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, retryBackoffMax)
	}
}

// onTurn is called with a turn's weight as a scan takes it, and with
// minus that as it ends; a no-op in production, a variable only so a test
// can count them.
var onTurn = func(weight int) {}

// compileMetric is m with its filters compiled, and a ratio's sides
// with theirs; p is its path (metrics[0]).
func compileMetric(m Metric, p string) (metricSpec, error) {
	where, err := compileFilters(m.Where, p+".where")
	if err != nil {
		return metricSpec{}, err
	}
	mt := metricSpec{Metric: m, where: where}
	if m.Op == "ratio" {
		num, err := compileMetric(*m.Num, p+".num")
		if err != nil {
			return metricSpec{}, err
		}
		den, err := compileMetric(*m.Den, p+".den")
		if err != nil {
			return metricSpec{}, err
		}
		mt.ratioNum, mt.ratioDen = &num, &den
	}
	return mt, nil
}

func (e *Engine) answerOnce(ctx context.Context, q Query, filters []filter, metrics []metricSpec, p Params, rules archive.Rules) (Result, error) {
	var rs ruleSet
	if q.Table != "sessions" && q.Table != "identities" { // derived from the events as stored
		var err error
		if rs, err = e.ruleSet(p.Project, rules); err != nil {
			return Result{}, err
		}
	}
	if err := e.collectEver(ctx, everFilters(filters, metrics), p, rs); err != nil {
		return Result{}, err
	}
	s := scan{e: e, q: q, filters: filters, metrics: metrics, project: p.Project, rules: rs}
	switch q.Kind {
	case "retention":
		return s.retention(ctx, p.From, p.To)
	case "attributes":
		return s.attributes(ctx, p.From, p.To)
	}
	specs := s.aggSpecs(p.From, p.To)
	as := make([]aggregated, len(specs))
	for i, sp := range specs {
		s := s
		if i > 0 && hasEver(q) { // a stat's previous period: its own ids, over its days
			s.filters, s.metrics = cloneEver(s.filters, s.metrics)
			if err := e.collectEver(ctx, everFilters(s.filters, s.metrics), Params{Project: p.Project, From: sp.from, To: sp.to}, rs); err != nil {
				return Result{}, err
			}
		}
		m, ms, err := s.aggregate(ctx, sp.from, sp.to, sp.dims, sp.perDay, sp.sets)
		if err != nil {
			return Result{}, err
		}
		as[i] = aggregated{m: m, ms: ms}
	}
	return s.result(as), nil
}

// aggSpec is one aggregation a stat, trend or breakdown is answered from:
// the query's metrics grouped by dims over [from, to], by day when
// perDay; sets keeps whole-range distinct sets beside a trend's per-day
// keys (scan.aggregate).
type aggSpec struct {
	from, to     civil.Date
	dims         []string
	perDay, sets bool
}

// aggregated is one aggSpec's aggregation merged, with its metrics
// resolved.
type aggregated struct {
	m  *merged
	ms []metricSpec
}

// aggSpecs are the aggregations s's stat, trend or breakdown of [from,
// to] is answered from, in result's order: a stat's range, then with
// compare "previous" as many days ending the day before from; a trend's
// range by day, split by its series; a breakdown's range by its
// dimensions.
func (s scan) aggSpecs(from, to civil.Date) []aggSpec {
	switch s.q.Kind {
	case "stat":
		specs := []aggSpec{{from: from, to: to}}
		if s.q.Compare == "previous" {
			days := int(epochDay(to)-epochDay(from)) + 1
			specs = append(specs, aggSpec{from: from.AddDays(-days), to: from.AddDays(-1)})
		}
		return specs
	case "trend":
		var dims []string
		if s.q.Series != "" {
			dims = []string{s.q.Series}
		}
		return []aggSpec{{from: from, to: to, dims: dims, perDay: true, sets: len(dims) > 0}}
	}
	return []aggSpec{{from: from, to: to, dims: s.q.Dimensions}}
}

// result is s's stat, trend or breakdown answered from its aggregations,
// as aggSpecs lists them.
func (s scan) result(as []aggregated) Result {
	switch s.q.Kind {
	case "stat":
		return s.stat(as)
	case "trend":
		return s.trend(as[0])
	}
	return s.breakdown(as[0])
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
	metrics []metricSpec // the query's metrics, their filters compiled (not resolved: newAgg resolves them)
	project int64
	rules   ruleSet
	logs    logReader // reads the logs; nil: the engine's readLog
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
	a, ms := s.newAgg(aggSpec{from: from, to: to, dims: dims, perDay: perDay, sets: sets})
	charge := a.charge
	if s.q.Table == "sessions" {
		if err := s.e.scanSessions(ctx, s.project, r, a.columns(), charge, a.add); err != nil {
			return nil, nil, err
		}
		return a.merge(), ms, nil
	}
	// One listing serves the scan and the identities.
	all, err := archive.ListFiles(s.e.dir)
	if err != nil {
		return nil, nil, err
	}
	files := all[s.project]
	if s.q.Table == "identities" {
		ids, err := s.e.identities(ctx, s.project, files, charge)
		if err != nil {
			return nil, nil, err
		}
		rows := ids.rowsIn(&r) // only those first seen in r put together
		if err := charge(rows.Bytes()); err != nil {
			return nil, nil, err
		}
		b := identityBatch(rows, s.project)
		if err := charge(batchBytes(b)); err != nil {
			return nil, nil, err
		}
		if err := a.add(0, b); err != nil {
			return nil, nil, err
		}
		return a.merge(), ms, nil
	}
	if err := s.scanEvents(ctx, files, r, a.columns(), dims, charge, a.add); err != nil {
		return nil, nil, err
	}
	return a.merge(), ms, nil
}

// newAgg is the aggregation of sp, with the query's metrics resolved.
func (s scan) newAgg(sp aggSpec) (*agg, []metricSpec) {
	lo, hi := epochDay(sp.from), epochDay(sp.to)
	o := aggOptions{metrics: s.metrics, filters: s.filters, dims: sp.dims, perDay: sp.perDay, sets: sp.sets,
		from: lo, ndays: int(hi - lo + 1), workers: s.e.workers, maxGroups: s.e.maxGroups, maxBytes: s.e.maxBytes}
	defs, err := compileDefines(s.q.Define)
	if err != nil {
		panic("native: defined columns compileQuery passed: " + err.Error())
	}
	o.defs = defs
	if sp.perDay && s.q.bucketed() {
		o.buckets, o.starts = buckets(sp.from, sp.to, s.q.Interval)
		o.ndays = len(o.starts)
	}
	return newAgg(o)
}

// buckets maps each day of [from, to], by its offset from from, to the
// index of its week (from Monday) or month, and lists each bucket's
// first day (epoch days), which may precede from.
func buckets(from, to civil.Date, interval string) (index []int, starts []int64) {
	for d := from; !to.Before(d); d = d.AddDays(1) {
		start := d
		if interval == "month" {
			start = civil.Date{Year: d.Year, Month: d.Month, Day: 1}
		} else {
			start = d.AddDays(-((int(d.Time().Weekday()) + 6) % 7)) // Monday
		}
		e := epochDay(start)
		if len(starts) == 0 || starts[len(starts)-1] != e {
			starts = append(starts, e)
		}
		index = append(index, len(starts)-1)
	}
	return index, starts
}

// scanEvents reads cols of the project's rows of the query's table in r
// from files, with the rules applied and the identity columns the query's
// filters or dims name added, and hands each batch to add, from the
// workers (w the worker's index).
func (s scan) scanEvents(ctx context.Context, files archive.Files, r Range, cols, dims []string, charge func(int64) error, add func(w int, b *Batch) error) error {
	fam := s.family()
	read := s.rules.columns(cols)
	idCols := s.identityColumnsOf(dims)
	var ids *firstSet
	var err error
	if len(idCols) > 0 {
		if ids, err = s.e.firstSeen(ctx, s.project, files, needsNames(idCols), charge); err != nil {
			return err
		}
	}
	pq, logs := filesIn(files, r)
	groups, err := parquetGroups(pq, fam, r)
	if err != nil {
		return err
	}
	// The logs, read whole, go first, so the row groups fill in beside them.
	work := make([]group, 0, len(logs)+len(groups))
	for _, path := range logs {
		work = append(work, group{file: path, index: -1})
	}
	work = append(work, groups...)
	readers := make([]groupReader, s.e.workers)
	err = scanParallel(ctx, work, s.e.workers, func(w int, g group) error {
		var bs []*Batch
		if g.index < 0 {
			logs := s.logs
			if logs == nil {
				logs = s.e.readLog
			}
			var err error
			if bs, err = logs(ctx, g.file, read, fam, r); err != nil {
				return err
			}
		} else {
			b, err := readers[w].read(g, read, fam, r)
			if err != nil {
				return err
			}
			bs = []*Batch{b}
		}
		for _, b := range bs {
			b = s.rules.apply(b, fam)
			if ids != nil {
				addIdentityColumns(b, idCols, ids)
			}
			if err := add(w, b); err != nil {
				return err
			}
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

// identityColumnsOf are the computed identity columns (idColumns: the
// status, label and first-seen ones) the query's filters, its metrics'
// filters and dims name.
func (s scan) identityColumnsOf(dims []string) []string {
	var out []string
	fss := [][]filter{s.filters}
	var add func(mt metricSpec)
	add = func(mt metricSpec) {
		fss = append(fss, mt.where)
		if mt.ratioNum != nil {
			add(*mt.ratioNum)
			add(*mt.ratioDen)
		}
	}
	for _, mt := range s.metrics {
		add(mt)
	}
	for _, fs := range fss {
		for _, f := range fs {
			for _, col := range f.columns() {
				if _, ok := idColumns[col]; ok && !slices.Contains(out, col) {
					out = append(out, col)
				}
			}
		}
	}
	for _, d := range dims {
		if _, ok := idColumns[d]; ok && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	defs, _ := compileDefines(s.q.Define) // compileQuery has compiled them
	for _, col := range definedColumns(defs) {
		if _, ok := idColumns[col]; ok && !slices.Contains(out, col) {
			out = append(out, col)
		}
	}
	return out
}

// needsNames says whether any of cols, computed identity columns, is a
// label, which looks names up.
func needsNames(cols []string) bool {
	return slices.ContainsFunc(cols, func(c string) bool { return idColumns[c].gives == idLabel })
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

func (s scan) stat(as []aggregated) Result {
	var cols columnsOut
	var row []string
	one := []int32{0}
	for k, x := range as {
		prefix := ""
		if k > 0 {
			prefix = "previous_"
		}
		for _, mt := range x.ms {
			i := cols.add(prefix + mt.Name())
			v := x.m.compute(mt, one, -1)
			cols.approx[i] = v.approx
			row = append(row, v.String())
		}
	}
	return cols.result([][]string{row}, false)
}

func label(v dimVal) string {
	if v.null {
		return ""
	}
	return v.s
}

func (s scan) trend(x aggregated) Result {
	m, ms, dims := x.m, x.ms, x.m.a.dims
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
	long, fill := len(ms) > 1, s.q.Fill == "zero"
	if long {
		cols.add("series")
		cols.add("value")
	} else {
		cols.add(mt.Name())
	}
	value := func(mt metricSpec, g int32, d int) string {
		v := m.compute(mt, []int32{g}, d)
		if fill && v.null && countedOps[mt.Op] {
			return "0"
		}
		return v.String()
	}
	var rows [][]string
	for d := range m.a.ndays {
		day := formatDay(m.a.dayOf(d))
		if long {
			for _, mt := range ms {
				if fill || m.metricRows(mt, 0, d) > 0 {
					rows = append(rows, []string{day, mt.Name(), value(mt, 0, d)})
				}
			}
			continue
		}
		for _, g := range series {
			if !fill && m.rows(g, d) == 0 {
				continue
			}
			row := []string{day}
			if len(dims) > 0 {
				row = append(row, label(m.a.groups.vals[g][0]))
			}
			rows = append(rows, append(row, value(mt, g, d)))
		}
	}
	return cols.result(rows, truncated)
}

func (s scan) breakdown(x aggregated) Result {
	m, ms := x.m, x.ms
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
	if len(types) > 1 && slices.ContainsFunc(ms, func(mt metricSpec) bool { return mt.Op == "share" }) {
		m.partitionBy()
	}
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
	if s.q.Other && len(gs) == limit+1 {
		limit++ // a single group past the limit is listed, not folded
	}
	top, rest := gs[:min(limit, len(gs))], gs[min(limit, len(gs)):]
	var rows [][]string
	metrics := func(row []string, groups []int32, other bool) []string {
		for i, mt := range ms {
			var v value
			if other {
				v = m.computeOther(mt, groups)
			} else {
				v = m.compute(mt, groups, -1)
			}
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
		rows = append(rows, metrics(row, []int32{g}, false))
	}
	if s.q.Other && len(rest) > 0 {
		row := make([]string, 0, len(cols.names))
		for range types {
			row = append(row, "Other")
		}
		rows = append(rows, metrics(row, rest, true))
	}
	return cols.result(rows, m.a.groups.dropped)
}
