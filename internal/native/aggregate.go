package native

import (
	"encoding/binary"
	"fmt"
	"hash/maphash"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dmtrkzntsv/twillingate/internal/shared/hll"
)

// The aggregation runs per worker into partials, merged once the scan is
// done (the design of scripts/native-bench, step 1b):
//
//   - a cell is a group (a breakdown's tuple of dimension values, a
//     trend's series value, the one group of a stat), and in a trend a day
//     of it: index group*nd + day, nd being 1 or the range's days;
//   - counts, sums, minima and maxima are dense arrays per cell;
//   - per-day distinct ids are the id's 64-bit hash (and its group, when
//     there are groups), each once in a hash set per worker and day
//     (daySet), merged at the end: exact but for a hash collision (odds
//     about n²/2^65 among a day's n ids), and a (group, day) with rows but
//     only NULL ids still has a key, so it counts as a day with rows;
//   - whole-range distinct ids are a dset per group, keyed by 32 bits of
//     the same hash above the fingerprint a sketch takes: exact while it
//     holds at most hll.ExactLimit ids, then a sketch;
//   - no id string is kept: each batch dictionary's ids are hashed once;
//   - measures' weights (1 / sample_rate) and weighted values are dense
//     arrays per cell, and a percentile's histogram one map per worker of
//     (cell, bucket) to the bucket's weight, sorted by cell then bucket
//     once merged.
//
// The aggregation's memory is counted as it grows (memory) and bounded by
// Options.MaxBytes: see there.

// trendCells is how many cells (series × days) a trend may track per
// group a breakdown may: a trend over many days tracks fewer series.
const trendCells = 10

// mapEntryBytes is about what a map entry of a string key costs beside
// the string's bytes; sketchBytes is a sketch's registers.
const (
	mapEntryBytes = 48
	sketchBytes   = 1 << 14
	histKeyBytes  = mapEntryBytes + 24
)

// dimVal is one dimension value of a group: NULL, or text (and, for a
// numeric column, its number, to sort by).
type dimVal struct {
	s    string
	n    int64
	null bool
}

// groupTable gives each tuple of dimension values met a dense group id,
// shared by the workers, up to max groups and while the aggregation's
// memory is within its budget; a tuple met past either is not tracked
// (its rows are left out) and dropped says so.
type groupTable struct {
	mu      sync.Mutex
	ids     map[string]int32
	vals    [][]dimVal
	max     int
	dropped bool
}

// resolve returns each tuple's group id (-1 past the caps); keys are the
// tuples' encodings. Each new group's cost is added to mem, and none is
// admitted once mem is past budget.
func (t *groupTable) resolve(keys []string, tuples [][]dimVal, out []int32, mem *memory) []int32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out = out[:0]
	for i, k := range keys {
		id, ok := t.ids[k]
		if !ok {
			if len(t.vals) >= t.max || mem.over() {
				t.dropped = true
				out = append(out, -1)
				continue
			}
			id = int32(len(t.vals))
			t.ids[k] = id
			// A tuple's strings are a batch dictionary's, which may share
			// one chunk-sized string: a kept group copies its own.
			for j := range tuples[i] {
				tuples[i][j].s = strings.Clone(tuples[i][j].s)
			}
			t.vals = append(t.vals, tuples[i])
			mem.add(2*int64(len(k)) + mapEntryBytes + int64(len(tuples[i]))*32)
		}
		out = append(out, id)
	}
	return out
}

// memory is an aggregation's approximate size in bytes: the groups with
// their strings, the per-cell arrays, the exact distinct keys' arrays and
// the sketches, and the per-day keys. Workers add what they grow by once
// per batch, then check it against the bounds (agg.checkMemory).
type memory struct {
	n   atomic.Int64
	max int64
}

func (m *memory) add(n int64) { m.n.Add(n) }
func (m *memory) over() bool  { return m.n.Load() > m.max }

func (t *groupTable) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.vals)
}

// dimView is one batch's values of a dimension, dictionary-encoded: idx
// per row (-1 NULL) into dict; nums holds each entry's number for a
// numeric column.
type dimView struct {
	dict []string
	nums []int64
	idx  []int32
}

// viewOf is the dimension column of b for the rows sel.
func viewOf(b *batch, name string, typ colType, sel []int32) (dimView, error) {
	c := batchColumn(b, name)
	if c == nil {
		return dimView{}, nil
	}
	if typ == typeString {
		if c.Idx == nil {
			return dimView{}, fmt.Errorf("native: column %s is not text", name)
		}
		return dimView{dict: c.Dict, idx: c.Idx}, nil
	}
	if (typ == typeBucket && c.Floats == nil) || (typ != typeBucket && c.Ints == nil) {
		return dimView{}, fmt.Errorf("native: column %s is not a number", name)
	}
	v := dimView{idx: make([]int32, b.N)}
	local := map[int64]int32{}
	for _, i := range sel {
		null := c.Null != nil && c.Null[i]
		var n int64
		switch {
		case typ == typeBucket:
			n, null = bucketOf(c.Floats[i], null), false
		case null:
			v.idx[i] = -1
			continue
		default:
			n = c.Ints[i]
		}
		x, ok := local[n]
		if !ok {
			x = int32(len(v.dict))
			local[n] = x
			v.nums = append(v.nums, n)
			if typ == typeDay {
				v.dict = append(v.dict, formatDay(n))
			} else {
				v.dict = append(v.dict, strconv.FormatInt(n, 10))
			}
		}
		v.idx[i] = x
	}
	return v, nil
}

// metricSpec is one metric, resolved against the aggregation's columns.
type metricSpec struct {
	Metric
	where    []filter // the metric's own filters, compiled
	mask     int      // index into agg.masks: the rows the metric counts
	num      int      // index into agg.nums, for sum/avg/min/max
	distinct int      // index into agg.distinct, for actors/users/groups and daily_*
	msr      int      // index into agg.msrs, for percentile, mean and weight
	latest   int      // index into agg.latests, for latest
	// ratioNum and ratioDen are a ratio's sides, resolved as hidden
	// metrics: they share the masks and partials the others use.
	ratioNum, ratioDen *metricSpec
}

// walk calls f with mt, then with a ratio's sides, each walked in turn.
func (mt *metricSpec) walk(f func(*metricSpec)) {
	f(mt)
	if mt.ratioNum != nil {
		mt.ratioNum.walk(f)
		mt.ratioDen.walk(f)
	}
}

// countedOps are the metrics that count rows, ids or weights: over no
// rows each is 0, or the daily_* and weight NULL, which a ratio's
// numerator reads as 0.
var countedOps = setOf([]string{"count", "actors", "users", "groups", "bounces", "weight", "daily_sum", "daily_max", "daily_avg", "days"})

// distinctOf is the id column a distinct op counts: the metrics' own, and
// the attributes keys' count of an attribute's values (opValues, which
// no query names).
var distinctOf = map[string]string{"actors": "actor_id", "users": "user_id", "groups": "group_id", opValues: attrValueCol}

// opValues is the distinct count of the unpivoted values.
const opValues = "attr_values"

// daysCol is the column the days metric counts per day: none, so only
// the keys marking a day with rows are kept.
const daysCol = ""

// distinctCol is one id column counted distinct over the rows of a mask:
// set says whole-range sets per group are kept, day that per-day keys are.
type distinctCol struct {
	name     string
	mask     int
	set, day bool
}

// numCol is one numeric column summed, averaged or bounded over the rows
// of a mask; a day or time (form) only bounded, read as an integer.
type numCol struct {
	name  string
	mask  int
	float bool
	form  int
}

// msrCol is the measures' partials over the rows of a mask: per cell the
// weights (1 / sample_rate) and weighted values (value / sample_rate),
// and with hist the weight per bucket.
type msrCol struct {
	mask int
	hist bool
}

// histKey is a histogram entry's key: a cell and a bucket of it.
type histKey struct {
	cell   int
	bucket int64
}

// histEntry is a merged histogram's entry: the weight of a cell's bucket.
type histEntry struct {
	histKey
	w float64
}

// msrPart is one mask's measure partials per cell: rows with a weight
// (wn) and their weights summed (w); rows with a weight and a value (sn)
// and their weighted values summed (s); and the weight per (cell, bucket).
type msrPart struct {
	wn, sn []int64
	w, s   []float64
	hist   map[histKey]float64
}

// colMask keys a column's partials by the mask of rows they read.
type colMask struct {
	col  string
	mask int
}

// agg is one scan's aggregation.
type agg struct {
	filters []filter
	// masks are the rows each metric counts, beside the filters: masks[0]
	// every row (the groups' rows), the others each a distinct set of a
	// metric's own filters.
	masks    [][]filter
	dims     []string // the group's columns: a breakdown's dimensions, a trend's series
	dimTypes []colType
	groups   *groupTable // nil for the one group of a stat or a trend without series
	from     int64       // the range's first day, days since 1970-01-01
	ndays    int
	nd       int // cells per group: ndays in a trend, else 1
	// buckets and starts, for a trend by week or month: aggOptions'.
	buckets  []int
	starts   []int64
	defs     []defined // the query's defined columns, added to each batch
	nums     []numCol
	distinct []*distinctCol
	bounces  bool // count sessions of one view per cell and mask (bounces, bounce_rate)
	msrs     []msrCol
	latests  []latestCol
	parts    []part
	mem      memory
}

// part is one worker's partials.
type part struct {
	count   [][]int64 // per mask, per cell
	bounces [][]int64 // per mask, per cell, rows with views 1 (when agg.bounces)
	nums    []numPart
	msrs    []msrPart
	latest  []latestPart
	sets    [][]dset   // per distinct column, per group; nil without set
	days    [][]daySet // per distinct column with day keys, per day of the range
	// scratch, reused across batches
	sel, gids  []int32
	pass       [][]bool // per mask but 0, per row of the batch: whether it passes
	hashes     []uint64
	keys       []uint64
	seen       []uint64 // per dictionary entry: the cell+1 its id was last added to the per-day keys for, in this batch
	resolveIDs []int32
	grew       int64 // bytes the partials grew by in this batch
}

// numPart is one numeric column's partials per cell: rows with a value,
// their sum, minimum and maximum, as floats or integers.
type numPart struct {
	n                []int64
	fsum, fmin, fmax []float64
	isum, imin, imax []int64
}

// aggOptions shape an aggregation: metrics grouped by dims (none: one
// group) over the days [from, from+ndays), by day when perDay; sets keeps
// whole-range distinct sets beside a trend's per-day keys.
type aggOptions struct {
	metrics        []metricSpec
	filters        []filter
	dims           []string
	perDay, sets   bool
	from           int64
	ndays, workers int
	maxGroups      int
	maxBytes       int64
	// buckets, for a trend by week or month: each day's bucket, by its
	// offset from from, and each bucket's first day; ndays is then the
	// buckets' count. nil: one cell per day.
	buckets []int
	starts  []int64
	// defs are the query's defined columns, added to each batch.
	defs []defined
}

// cellDay is the day cell of a row of epoch day d: its offset from
// a.from, or its bucket's index; out of range (-1 or ndays and past)
// for a day outside the aggregation's.
func (a *agg) cellDay(d int64) int {
	off := int(d - a.from)
	if a.buckets == nil {
		return off
	}
	if off < 0 || off >= len(a.buckets) {
		return -1
	}
	return a.buckets[off]
}

// dayOf is the epoch day cell d stands for: the day, or its bucket's
// first.
func (a *agg) dayOf(d int) int64 {
	if a.starts != nil {
		return a.starts[d]
	}
	return a.from + int64(d)
}

// newAgg sets up an aggregation and returns its metrics resolved.
func newAgg(o aggOptions) (*agg, []metricSpec) {
	metrics, filters, dims, perDay, from, ndays, maxGroups, distinctSets := o.metrics, o.filters, o.dims, o.perDay, o.from, o.ndays, o.maxGroups, o.sets
	a := &agg{filters: filters, masks: [][]filter{nil}, dims: dims, from: from, ndays: ndays, nd: 1, parts: make([]part, o.workers),
		buckets: o.buckets, starts: o.starts, defs: o.defs}
	a.mem.max = o.maxBytes
	if perDay {
		a.nd = ndays
	}
	for _, d := range dims {
		a.dimTypes = append(a.dimTypes, typeOf(d))
	}
	if len(dims) > 0 {
		limit := maxGroups
		if perDay {
			// A trend's arrays hold a cell per series and day: they get
			// trendCells per group the cap allows, however many days.
			limit = min(limit, trendCells*maxGroups/ndays)
		}
		a.groups = &groupTable{ids: map[string]int32{}, max: max(1, limit)}
	}
	maskIdx := map[string]int{"": 0}
	numIdx := map[colMask]int{}
	distIdx := map[colMask]int{}
	dist := func(col string, mask int) int {
		i, ok := distIdx[colMask{col, mask}]
		if !ok {
			i = len(a.distinct)
			distIdx[colMask{col, mask}] = i
			a.distinct = append(a.distinct, &distinctCol{name: col, mask: mask})
		}
		return i
	}
	var resolve func(m metricSpec) metricSpec
	resolve = func(m metricSpec) metricSpec {
		m.num, m.distinct, m.msr, m.latest = -1, -1, -1, -1
		if m.Op == "ratio" {
			num, den := resolve(*m.ratioNum), resolve(*m.ratioDen)
			m.ratioNum, m.ratioDen = &num, &den
			return m
		}
		key := filtersKey(m.Where)
		mask, ok := maskIdx[key]
		if !ok {
			mask = len(a.masks)
			maskIdx[key] = mask
			a.masks = append(a.masks, m.where)
		}
		m.mask = mask
		switch m.Op {
		case "latest":
			i := slices.Index(a.latests, latestCol{m.Column, mask})
			if i < 0 {
				i = len(a.latests)
				a.latests = append(a.latests, latestCol{m.Column, mask})
			}
			m.latest = i
		case "sum", "avg", "min", "max":
			i, ok := numIdx[colMask{m.Column, mask}]
			if !ok {
				i = len(a.nums)
				numIdx[colMask{m.Column, mask}] = i
				a.nums = append(a.nums, numCol{name: m.Column, mask: mask, float: m.Column == "value" || m.Column == "sample_rate", form: boundForms[m.Column]})
			}
			m.num = i
		case "actors", "users", "groups", opValues:
			m.distinct = dist(distinctOf[m.Op], mask)
			if perDay {
				a.distinct[m.distinct].day = true
			}
			if !perDay || distinctSets {
				a.distinct[m.distinct].set = true
			}
		case "daily_sum", "daily_max", "daily_avg":
			m.distinct = dist(distinctOf[m.Of], mask)
			a.distinct[m.distinct].day = true
		case "days":
			// No id: each (group, day) with rows has its one NULL key.
			m.distinct = dist(daysCol, mask)
			a.distinct[m.distinct].day = true
		case "bounces", "bounce_rate":
			a.bounces = true
		case "percentile", "mean", "weight", "share":
			if m.Op == "share" && m.Of != "weight" {
				break // a share of count reads the mask's counts
			}
			i := slices.IndexFunc(a.msrs, func(c msrCol) bool { return c.mask == mask })
			if i < 0 {
				i = len(a.msrs)
				a.msrs = append(a.msrs, msrCol{mask: mask})
			}
			if m.Op == "percentile" {
				a.msrs[i].hist = true
			}
			m.msr = i
		}
		return m
	}
	out := make([]metricSpec, len(metrics))
	for k, m := range metrics {
		out[k] = resolve(m)
	}
	return a, out
}

// filtersKey is a metric's filters as a key: metrics with the same
// filters share a mask ("" for none).
func filtersKey(where []Filter) string {
	var b strings.Builder
	for _, f := range where {
		b.WriteString(strconv.Quote(f.Column))
		b.WriteString(strconv.Quote(f.Op))
		for _, c := range f.Columns {
			b.WriteString(strconv.Quote(c))
		}
		if len(f.Any) > 0 {
			b.WriteString("(" + filtersKey(f.Any) + ")")
		}
		if len(f.Where) > 0 {
			b.WriteString(strconv.Quote(f.By) + strconv.Quote(f.Over) + "{" + filtersKey(f.Where) + "}")
		}
		b.WriteByte('|')
		for _, v := range f.Values {
			b.WriteString(strconv.Quote(v))
		}
		b.WriteByte(';')
	}
	return b.String()
}

// columns is what a scan must read for the aggregation.
func (a *agg) columns() []string {
	cols := []string{"day"}
	add := func(cs ...string) { cols = appendNew(cols, cs...) }
	for _, fs := range append([][]filter{a.filters}, a.masks...) {
		for _, f := range fs {
			for _, col := range f.columns() {
				if !isDefined(col) { // a defined one is added to each batch
					add(readColumns(col)...)
				}
			}
		}
	}
	for _, d := range a.dims {
		if !isDefined(d) { // a defined one is added to each batch, from the columns below
			add(readColumns(d)...)
		}
	}
	for _, col := range definedColumns(a.defs) {
		add(readColumns(col)...)
	}
	for _, n := range a.nums {
		add(n.name)
	}
	for _, d := range a.distinct {
		if d.name != daysCol {
			add(d.name)
		}
	}
	if a.bounces {
		add("views")
	}
	if len(a.latests) > 0 {
		add("ts")
		for _, l := range a.latests {
			if !isDefined(l.name) { // a defined one is added to each batch
				add(readColumns(l.name)...)
			}
		}
	}
	if len(a.msrs) > 0 {
		add("value", "sample_rate")
	}
	return cols
}

// grow makes p's arrays hold groups groups, adding what they grow by to
// p.grew.
func (p *part) grow(a *agg, groups int) {
	cells := groups * a.nd
	have := 0
	if p.count != nil {
		have = len(p.count[0])
	}
	if have >= cells {
		return
	}
	cells = max(cells, 2*have)
	// Per cell: the count per mask (and bounces), and per numeric column 4
	// numbers; per group a set header per distinct column.
	perCell := int64(8*len(a.masks)*(1+boolInt(a.bounces)) + 32*len(a.nums) + 32*len(a.msrs) + 48*len(a.latests))
	perGroup := int64(0)
	for _, d := range a.distinct {
		if d.set {
			perGroup += 48
		}
	}
	p.grew += int64(cells-have)*perCell + int64(cells-have)/int64(a.nd)*perGroup
	if p.count == nil {
		p.count = make([][]int64, len(a.masks))
		if a.bounces {
			p.bounces = make([][]int64, len(a.masks))
		}
	}
	for k := range a.masks {
		p.count[k] = growTo(p.count[k], cells)
		if a.bounces {
			p.bounces[k] = growTo(p.bounces[k], cells)
		}
	}
	if p.nums == nil {
		p.nums = make([]numPart, len(a.nums))
	}
	if p.latest == nil {
		p.latest = make([]latestPart, len(a.latests))
	}
	for j := range p.latest {
		p.latest[j].grow(cells)
		p.latest[j].num = a.latests[j].numeric()
	}
	for j, n := range a.nums {
		np := &p.nums[j]
		np.n = growTo(np.n, cells)
		if n.float {
			np.fsum, np.fmin, np.fmax = growTo(np.fsum, cells), growTo(np.fmin, cells), growTo(np.fmax, cells)
		} else {
			np.isum, np.imin, np.imax = growTo(np.isum, cells), growTo(np.imin, cells), growTo(np.imax, cells)
		}
	}
	if p.msrs == nil {
		p.msrs = make([]msrPart, len(a.msrs))
		for j, c := range a.msrs {
			if c.hist {
				p.msrs[j].hist = map[histKey]float64{}
			}
		}
	}
	for j := range a.msrs {
		mp := &p.msrs[j]
		mp.wn, mp.sn, mp.w, mp.s = growTo(mp.wn, cells), growTo(mp.sn, cells), growTo(mp.w, cells), growTo(mp.s, cells)
	}
	if p.sets == nil {
		p.sets = make([][]dset, len(a.distinct))
		p.days = make([][]daySet, len(a.distinct))
		for j, d := range a.distinct {
			if d.day {
				p.days[j] = make([]daySet, a.ndays)
				for d := range p.days[j] {
					p.days[j][d].wide = a.groups != nil
				}
				p.grew += int64(a.ndays) * daySetBytes
			}
		}
	}
	for j, d := range a.distinct {
		if d.set {
			p.sets[j] = growTo(p.sets[j], cells/a.nd)
		}
	}
}

func growTo[T any](s []T, n int) []T {
	if len(s) >= n {
		return s
	}
	return append(s, make([]T, n-len(s))...)
}

// add aggregates batch b into worker w's partials, then counts what they
// grew by and checks the memory bounds.
func (a *agg) add(w int, b *batch) error {
	if len(a.defs) > 0 {
		var err error
		if b, err = withDefined(b, a.defs); err != nil {
			return err
		}
	}
	p := &a.parts[w]
	p.grew = 0
	err := a.addBatch(p, b)
	a.mem.add(p.grew)
	if err != nil {
		return err
	}
	return a.checkMemory()
}

// heavy says whether the aggregation keeps distinct ids (days' keys, one
// per group and day, do not count) or histograms, whose memory follows
// the ids or the samples rather than the groups.
func (a *agg) heavy() bool {
	return slices.ContainsFunc(a.distinct, func(d *distinctCol) bool { return d.name != daysCol }) ||
		slices.ContainsFunc(a.msrs, func(c msrCol) bool { return c.hist })
}

// charge counts n more bytes the aggregation holds (n < 0 gives them
// back) and, for n > 0, checks the memory bounds as checkMemory does.
func (a *agg) charge(n int64) error {
	a.mem.add(n)
	if n > 0 {
		return a.checkMemory()
	}
	return nil
}

// checkMemory refuses the aggregation with ErrTooLarge once it holds more
// than it may: MaxBytes when it has nothing to leave out (one group: a
// stat, a trend without series), else twice that (past MaxBytes no group
// is admitted, so only the admitted ones' sets and per-day keys still grow).
func (a *agg) checkMemory() error {
	limit := a.mem.max
	if a.groups != nil {
		limit = 2 * a.mem.max
	}
	if n := a.mem.n.Load(); n > limit {
		return tooLarge(n, limit)
	}
	return nil
}

func (a *agg) addBatch(p *part, b *batch) error {
	sel, err := selectRows(b, a.filters, p.sel)
	if err != nil {
		return err
	}
	p.sel = sel
	if len(sel) == 0 {
		return nil
	}
	gids, gmax, err := a.groupsOf(b, sel, p)
	if err != nil {
		return err
	}
	p.gids = gids
	if gmax < 0 {
		return nil // every row's group is past the caps
	}
	p.grow(a, int(gmax)+1)
	_, dayCol := b.col("day")
	if dayCol == nil || dayCol.Ints == nil {
		return fmt.Errorf("native: a batch without day")
	}
	off := func(i int32) int { return a.cellDay(dayCol.Ints[i]) }
	cell := func(k int, i int32) int {
		if a.nd == 1 {
			return int(gids[k])
		}
		return int(gids[k])*a.nd + off(i)
	}
	// pass is each mask's rows (nil for mask 0: every row).
	if len(p.pass) < len(a.masks) {
		p.pass = make([][]bool, len(a.masks))
	}
	for k := 1; k < len(a.masks); k++ {
		if p.pass[k], err = passRows(b, a.masks[k], p.pass[k]); err != nil {
			return err
		}
	}
	pass := p.pass[:len(a.masks)]
	pass[0] = nil
	var views *column
	if a.bounces {
		if _, views = b.col("views"); views == nil || views.Ints == nil {
			return fmt.Errorf("native: a batch without views")
		}
	}
	for mask, keep := range pass {
		count := p.count[mask]
		for k, i := range sel {
			if gids[k] >= 0 && (keep == nil || keep[i]) {
				count[cell(k, i)]++
			}
		}
		if views == nil {
			continue
		}
		bounces := p.bounces[mask]
		for k, i := range sel {
			if gids[k] >= 0 && (keep == nil || keep[i]) && (views.Null == nil || !views.Null[i]) && views.Ints[i] == 1 {
				bounces[cell(k, i)]++
			}
		}
	}
	for j, n := range a.nums {
		_, c := b.col(n.name)
		if c == nil {
			continue
		}
		np := &p.nums[j]
		keep := pass[n.mask]
		for k, i := range sel {
			if gids[k] < 0 || (keep != nil && !keep[i]) || (c.Null != nil && c.Null[i]) {
				continue
			}
			x := cell(k, i)
			np.n[x]++
			first := np.n[x] == 1
			switch {
			case n.float && (c.Floats != nil || c.Ints != nil):
				var v float64
				if c.Floats != nil {
					v = c.Floats[i]
				} else {
					v = float64(c.Ints[i])
				}
				foldBounds(&np.fsum[x], &np.fmin[x], &np.fmax[x], v, v, v, first)
			case !n.float && (c.Ints != nil || c.Times != nil):
				var v int64
				if c.Ints != nil {
					v = c.Ints[i]
				} else {
					v = c.Times[i]
				}
				// A day or time is only bounded (no sum or avg takes one),
				// and a sum of microsecond times can overflow: it adds 0.
				sum := v
				if n.form != formNumber {
					sum = 0
				}
				foldBounds(&np.isum[x], &np.imin[x], &np.imax[x], sum, v, v, first)
			default:
				return fmt.Errorf("native: column %s is not a number", n.name)
			}
		}
	}
	if err := a.addMeasures(p, b, sel, gids, pass, cell); err != nil {
		return err
	}
	a.addLatest(p, b, sel, gids, pass, cell)
	for j, d := range a.distinct {
		if err := a.addDistinct(p, j, d, b, sel, gids, pass[d.mask], off); err != nil {
			return err
		}
	}
	return nil
}

// addMeasures adds the rows sel (those of each mask) to the measure
// partials: a row without a sample_rate has no weight and counts toward
// none; one without a value is in bucket -1000 and adds to the weights,
// not the weighted values, as SUM(value / sample_rate) leaves it out.
func (a *agg) addMeasures(p *part, b *batch, sel, gids []int32, pass [][]bool, cell func(int, int32) int) error {
	if len(a.msrs) == 0 {
		return nil
	}
	_, val := b.col("value")
	_, rate := b.col("sample_rate")
	for _, c := range []struct {
		name string
		col  *column
	}{{"value", val}, {"sample_rate", rate}} {
		if c.col != nil && c.col.Floats == nil && c.col.Ints == nil {
			return fmt.Errorf("native: column %s is not a number", c.name)
		}
	}
	if rate == nil {
		return nil
	}
	num := func(c *column, i int32) (float64, bool) {
		switch {
		case c == nil || (c.Null != nil && c.Null[i]):
			return 0, false
		case c.Floats != nil:
			return c.Floats[i], true
		}
		return float64(c.Ints[i]), true
	}
	for j, mc := range a.msrs {
		mp := &p.msrs[j]
		keep := pass[mc.mask]
		for k, i := range sel {
			if gids[k] < 0 || (keep != nil && !keep[i]) {
				continue
			}
			r, ok := num(rate, i)
			if !ok {
				continue
			}
			x := cell(k, i)
			w := 1 / r
			mp.wn[x]++
			mp.w[x] += w
			v, hasValue := num(val, i)
			if hasValue {
				mp.sn[x]++
				mp.s[x] += v / r
			}
			if mc.hist {
				key := histKey{cell: x, bucket: bucketOf(v, !hasValue)}
				if _, ok := mp.hist[key]; !ok {
					p.grew += histKeyBytes
				}
				mp.hist[key] += w
			}
		}
	}
	return nil
}

// addDistinct adds the ids of column d of the rows sel (those keep, when
// not nil, passes) to their groups' sets and per-day keys.
func (a *agg) addDistinct(p *part, j int, d *distinctCol, b *batch, sel, gids []int32, keep []bool, off func(int32) int) error {
	_, c := b.col(d.name)
	// hashes is each dictionary entry's hash, of the entries the tracked
	// rows use only (the others are not hashed); keys the whole-range keys:
	// 32 bits of the hash above the fingerprint a sketch takes.
	hashes, keys := p.hashes[:0], p.keys[:0]
	if c != nil {
		if c.Idx == nil {
			return fmt.Errorf("native: column %s is not text", d.name)
		}
		used := make([]bool, len(c.Dict))
		for k, i := range sel {
			if x := c.Idx[i]; gids[k] >= 0 && x >= 0 && (keep == nil || keep[i]) {
				used[x] = true
			}
		}
		for x, s := range c.Dict {
			var h, k uint64
			if used[x] {
				h = maphash.String(hashSeed, s)
				k = h&^0xFFFFFFFF | uint64(hll.Fingerprint(s))
			}
			hashes, keys = append(hashes, h), append(keys, k)
		}
	}
	p.hashes, p.keys = hashes, keys
	// seen skips the id's rows after its first in a cell, as long as its
	// rows keep to one cell (a batch is mostly one day): only a key not
	// yet added since the id's last cell probes the set.
	seen := p.seen
	if d.day && c != nil {
		seen = growTo(seen[:0], len(c.Dict))
		clear(seen)
		p.seen = seen
	}
	last := ^uint64(0) // the cell whose NULL key was added last
	days := p.days[j]
	for k, i := range sel {
		g := gids[k]
		if g < 0 || (keep != nil && !keep[i]) {
			continue
		}
		x := int32(-1)
		if c != nil {
			x = c.Idx[i]
		}
		if d.set && x >= 0 {
			p.grew += p.sets[j][g].add(keys[x])
		}
		if !d.day {
			continue
		}
		day := off(i)
		if day < 0 || day >= len(days) {
			return fmt.Errorf("native: a row's day is outside the range")
		}
		cell := uint64(g)*uint64(a.ndays) + uint64(day)
		if x < 0 {
			if cell != last {
				p.grew += days[day].add(uint32(g), nullKey)
				last = cell
			}
			continue
		}
		if seen[x] != cell+1 {
			p.grew += days[day].add(uint32(g), idKey(hashes[x]))
			seen[x] = cell + 1
		}
	}
	return nil
}

// hashSeed seeds the hash that keys a distinct id without keeping it: per
// day 64 bits of it, over the whole range 32 bits with a 32-bit
// fingerprint, so two ids share a key with odds about n²/2^65 among n, far
// below the sketch's error.
var hashSeed = maphash.MakeSeed()

// groupsOf returns each selected row's group id (-1: past the cap) and the
// largest one (-1 when none is tracked). The batch's distinct tuples are
// found first, then resolved under the table's lock at once, in the order
// of their first row.
func (a *agg) groupsOf(b *batch, sel []int32, p *part) ([]int32, int32, error) {
	gids := p.gids[:0]
	if a.groups == nil {
		for range sel {
			gids = append(gids, 0)
		}
		return gids, 0, nil
	}
	views := make([]dimView, len(a.dims))
	mult := make([]uint64, len(a.dims))
	radix := uint64(1)
	for j, d := range a.dims {
		v, err := viewOf(b, d, a.dimTypes[j], sel)
		if err != nil {
			return nil, -1, err
		}
		views[j] = v
		r := uint64(len(v.dict) + 1)
		if radix > math.MaxUint64/r {
			return nil, -1, fmt.Errorf("native: too many dimension values in one batch")
		}
		mult[j] = radix
		radix *= r
	}
	// local is a tuple's key within the batch: each value's index + 1 (0
	// for NULL), in mixed radix.
	local := func(i int32) uint64 {
		var k uint64
		for j, v := range views {
			x := int32(-1)
			if v.idx != nil {
				x = v.idx[i]
			}
			k += uint64(x+1) * mult[j]
		}
		return k
	}
	var dense []int32
	var sparse map[uint64]int32
	if radix <= uint64(4*len(sel)+1024) {
		dense = make([]int32, radix)
		for i := range dense {
			dense[i] = -1
		}
	} else {
		sparse = map[uint64]int32{}
	}
	var firsts []int32 // a row of each distinct tuple
	slot := make([]int32, len(sel))
	for k, i := range sel {
		lk := local(i)
		var s int32
		var ok bool
		if dense != nil {
			s = dense[lk]
			ok = s >= 0
		} else {
			s, ok = sparse[lk]
		}
		if !ok {
			s = int32(len(firsts))
			firsts = append(firsts, i)
			if dense != nil {
				dense[lk] = s
			} else {
				sparse[lk] = s
			}
		}
		slot[k] = s
	}
	keys := make([]string, len(firsts))
	tuples := make([][]dimVal, len(firsts))
	var enc []byte
	for s, i := range firsts {
		enc = enc[:0]
		t := make([]dimVal, len(views))
		for j, v := range views {
			x := int32(-1)
			if v.idx != nil {
				x = v.idx[i]
			}
			if x < 0 {
				t[j] = dimVal{null: true}
				enc = append(enc, 0)
				continue
			}
			t[j] = dimVal{s: v.dict[x]}
			if v.nums != nil {
				t[j].n = v.nums[x]
			}
			enc = append(enc, 1)
			enc = binary.AppendUvarint(enc, uint64(len(t[j].s)))
			enc = append(enc, t[j].s...)
		}
		keys[s], tuples[s] = string(enc), t
	}
	resolved := a.groups.resolve(keys, tuples, p.resolveIDs, &a.mem)
	p.resolveIDs = resolved
	gmax := int32(-1)
	for k := range sel {
		g := resolved[slot[k]]
		gids = append(gids, g)
		gmax = max(gmax, g)
	}
	return gids, gmax, nil
}
