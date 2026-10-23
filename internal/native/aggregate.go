package native

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"hash/maphash"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/hll"
)

// The aggregation runs per worker into partials, merged once the scan is
// done (the design of scripts/native-bench, step 1b):
//
//   - a cell is a group (a breakdown's tuple of dimension values, a
//     trend's series value, the one group of a stat), and in a trend a day
//     of it: index group*nd + day, nd being 1 or the range's days;
//   - counts, sums, minima and maxima are dense arrays per cell;
//   - per-day distinct ids are 16-byte keys, the (group, day) and the id's
//     64-bit hash, sorted and deduplicated per batch, compacted as they
//     grow and merged at the end: exact but for a hash collision (odds
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
// the string's bytes; sketchBytes is a sketch's registers; dayKeyBytes a
// per-day key's.
const (
	mapEntryBytes = 48
	sketchBytes   = 1 << 14
	dayKeyBytes   = 16
	histKeyBytes  = mapEntryBytes + 24
)

// dayKey is one per-day distinct key: c is the cell (group*ndays + day)
// shifted left by one, its low bit set for the key that only marks the
// cell as having rows (a NULL id, h 0); h is the id's hash.
type dayKey struct{ c, h uint64 }

func (k dayKey) cell() uint64 { return k.c >> 1 }
func (k dayKey) null() bool   { return k.c&1 == 1 }

func cmpDayKey(a, b dayKey) int {
	if a.c != b.c {
		return cmp.Compare(a.c, b.c)
	}
	return cmp.Compare(a.h, b.h)
}

// keySink is one worker's per-day keys: each batch's sorted and
// deduplicated, appended, and the whole compacted once it is twice its
// size after the last compaction (and past compactAt keys), so memory
// follows the distinct keys rather than the rows.
type keySink struct {
	keys      []dayKey
	compacted int
}

// compactAt is a variable so a test can lower it.
var compactAt = 1 << 20

// addGroup appends a batch's keys and returns how many bytes the sink's
// array grew by.
func (k *keySink) addGroup(group []dayKey) int64 {
	before := cap(k.keys)
	k.keys = append(k.keys, sortedUniqueKeys(group)...)
	if len(k.keys) > compactAt && len(k.keys) > 2*k.compacted {
		k.keys = sortedUniqueKeys(k.keys)
		k.compacted = len(k.keys)
	}
	return int64(cap(k.keys)-before) * dayKeyBytes
}

func sortedUnique(s []uint64) []uint64 {
	slices.Sort(s)
	return slices.Compact(s)
}

func sortedUniqueKeys(s []dayKey) []dayKey {
	slices.SortFunc(s, cmpDayKey)
	return slices.Compact(s)
}

// mergeKeys returns the sinks' keys sorted, each once, releasing each
// sink's array once copied.
func mergeKeys(sinks []*keySink) []dayKey {
	n := 0
	for _, s := range sinks {
		n += len(s.keys)
	}
	all := make([]dayKey, 0, n)
	for _, s := range sinks {
		all = append(all, s.keys...)
		s.keys, s.compacted = nil, 0
	}
	return sortedUniqueKeys(all)
}

// dset is one group's distinct ids over the whole range: exact, as keys
// hash<<32 | fingerprint (32 bits of each), while it holds at most
// hll.ExactLimit ids, then a sketch of the fingerprints, whose count is
// approximate. Either way it never takes more than about one sketch's
// sketchBytes: the exact keys are compacted at 2 × hll.ExactLimit.
type dset struct {
	keys   []uint64
	sorted int // keys[:sorted] is sorted and unique
	sk     *hll.Sketch
}

// add adds key k and returns how many bytes s grew by (less than 0 when
// it turned into a sketch smaller than its keys).
func (s *dset) add(k uint64) int64 {
	if s.sk != nil {
		s.sk.Add(uint32(k))
		return 0
	}
	if n := len(s.keys); n > 0 && s.keys[n-1] == k {
		return 0
	}
	before := cap(s.keys)
	s.keys = append(s.keys, k)
	grew := int64(cap(s.keys)-before) * 8
	if len(s.keys) >= 2*hll.ExactLimit {
		s.compact()
		if s.sk != nil {
			grew += sketchBytes - int64(before)*8
		}
	}
	return grew
}

// compact sorts and dedupes the exact keys, and turns them into a sketch
// once they are more than hll.ExactLimit.
func (s *dset) compact() {
	if s.sk != nil || s.sorted == len(s.keys) {
		return
	}
	s.keys = sortedUnique(s.keys)
	s.sorted = len(s.keys)
	if len(s.keys) > hll.ExactLimit {
		s.promote()
	}
}

func (s *dset) promote() {
	s.sk = &hll.Sketch{}
	for _, k := range s.keys {
		s.sk.Add(uint32(k))
	}
	s.keys, s.sorted = nil, 0
}

// merge adds o's ids to s; o is left as it was.
func (s *dset) merge(o *dset) {
	if o.sk != nil {
		if s.sk == nil {
			s.promote()
		}
		s.sk.Merge(o.sk)
		return
	}
	for _, k := range o.keys {
		s.add(k)
	}
}

// count is how many distinct ids s holds, and whether that is a sketch's
// estimate.
func (s *dset) count() (int64, bool) {
	s.compact()
	if s.sk != nil {
		return int64(s.sk.Count()), true
	}
	return int64(len(s.keys)), false
}

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
func viewOf(b *Batch, name string, typ colType, sel []int32) (dimView, error) {
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

func formatDay(d int64) string { return time.Unix(d*86400, 0).UTC().Format(time.DateOnly) }

// value is one metric's result: NULL, an integer or a float, and whether
// a sketch estimated it.
type value struct {
	null, isInt, approx bool
	i                   int64
	f                   float64
}

func intValue(n int64) value { return value{isInt: true, i: n} }

func (v value) String() string {
	switch {
	case v.null:
		return ""
	case v.isInt:
		return strconv.FormatInt(v.i, 10)
	}
	return strconv.FormatFloat(v.f, 'f', -1, 64)
}

// round is v rounded to *places decimals, as DuckDB's ROUND rounds a
// DOUBLE: half away from zero, of v times 10^places; an integer, NULL, or
// places nil leave v as it is.
func (v value) round(places *int) value {
	if places == nil || v.null || v.isInt {
		return v
	}
	mod := math.Pow(10, float64(*places))
	if r := math.Round(v.f*mod) / mod; !math.IsInf(r, 0) && !math.IsNaN(r) {
		v.f = r
	}
	return v
}

func (v value) num() float64 {
	if v.isInt {
		return float64(v.i)
	}
	return v.f
}

// cmpDesc orders values descending, NULLs last.
func cmpDesc(a, b value) int {
	switch {
	case a.null || b.null:
		return cmp.Compare(boolInt(a.null), boolInt(b.null))
	case a.isInt && b.isInt:
		return cmp.Compare(b.i, a.i)
	}
	return cmp.Compare(b.num(), a.num())
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// cmpDim orders dimension values ascending, NULLs last, numbers by value.
func cmpDim(typ colType, a, b dimVal) int {
	switch {
	case a.null || b.null:
		return cmp.Compare(boolInt(a.null), boolInt(b.null))
	case typ == typeInt || typ == typeBucket:
		return cmp.Compare(a.n, b.n)
	}
	return cmp.Compare(a.s, b.s)
}

// metricSpec is one metric, resolved against the aggregation's columns.
type metricSpec struct {
	Metric
	where    []filter // the metric's own filters, compiled
	mask     int      // index into agg.masks: the rows the metric counts
	num      int      // index into agg.nums, for sum/avg/min/max
	distinct int      // index into agg.distinct, for actors/users/groups and daily_*
	msr      int      // index into agg.msrs, for percentile, mean and weight
	// ratioNum and ratioDen are a ratio's sides, resolved as hidden
	// metrics: they share the masks and partials the others use.
	ratioNum, ratioDen *metricSpec
}

// distinctOf is the id column a distinct op counts: the metrics' own, and
// the attributes keys' count of an attribute's values (opValues, which
// no query names).
var distinctOf = map[string]string{"actors": "actor_id", "users": "user_id", "groups": "group_id", opValues: attrValueCol}

// opValues is the distinct count of the unpivoted values.
const opValues = "attr_values"

// distinctCol is one id column counted distinct over the rows of a mask:
// set says whole-range sets per group are kept, day that per-day keys are.
type distinctCol struct {
	name     string
	mask     int
	set, day bool
}

// numCol is one numeric column summed, averaged or bounded over the rows
// of a mask.
type numCol struct {
	name  string
	mask  int
	float bool
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
	nums     []numCol
	distinct []*distinctCol
	bounces  bool // count sessions of one view per cell and mask (bounces, bounce_rate)
	msrs     []msrCol
	parts    []part
	mem      memory
}

// part is one worker's partials.
type part struct {
	count   [][]int64 // per mask, per cell
	bounces [][]int64 // per mask, per cell, rows with views 1 (when agg.bounces)
	nums    []numPart
	msrs    []msrPart
	sets    [][]dset // per distinct column, per group; nil without set
	days    []keySink
	// scratch, reused across batches
	sel, gids  []int32
	pass       [][]bool // per mask but 0, per row of the batch: whether it passes
	hashes     []uint64
	keys       []uint64
	buf        []dayKey
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
}

// newAgg sets up an aggregation and returns its metrics resolved.
func newAgg(o aggOptions) (*agg, []metricSpec) {
	metrics, filters, dims, perDay, from, ndays, maxGroups, distinctSets := o.metrics, o.filters, o.dims, o.perDay, o.from, o.ndays, o.maxGroups, o.sets
	a := &agg{filters: filters, masks: [][]filter{nil}, dims: dims, from: from, ndays: ndays, nd: 1, parts: make([]part, o.workers)}
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
		m.num, m.distinct, m.msr = -1, -1, -1
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
		case "sum", "avg", "min", "max":
			i, ok := numIdx[colMask{m.Column, mask}]
			if !ok {
				i = len(a.nums)
				numIdx[colMask{m.Column, mask}] = i
				a.nums = append(a.nums, numCol{name: m.Column, mask: mask, float: m.Column == "value" || m.Column == "sample_rate"})
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
	add := func(c string) {
		if !slices.Contains(cols, c) {
			cols = append(cols, c)
		}
	}
	for _, fs := range append([][]filter{a.filters}, a.masks...) {
		for _, f := range fs {
			for _, c := range readColumns(f.column) {
				add(c)
			}
		}
	}
	for _, d := range a.dims {
		for _, c := range readColumns(d) {
			add(c)
		}
	}
	for _, n := range a.nums {
		add(n.name)
	}
	for _, d := range a.distinct {
		add(d.name)
	}
	if a.bounces {
		add("views")
	}
	if len(a.msrs) > 0 {
		add("value")
		add("sample_rate")
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
	perCell := int64(8*len(a.masks)*(1+boolInt(a.bounces)) + 32*len(a.nums) + 32*len(a.msrs))
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
		p.days = make([]keySink, len(a.distinct))
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
func (a *agg) add(w int, b *Batch) error {
	p := &a.parts[w]
	p.grew = 0
	err := a.addBatch(p, b)
	a.mem.add(p.grew)
	if err != nil {
		return err
	}
	return a.checkMemory()
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
		return fmt.Errorf("%w (about %d MiB counted, %d MiB allowed)", ErrTooLarge, n>>20, limit>>20)
	}
	return nil
}

func (a *agg) addBatch(p *part, b *Batch) error {
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
	off := func(i int32) int { return int(dayCol.Ints[i] - a.from) }
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
	var views *Column
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
				np.fsum[x] += v
				if first || v < np.fmin[x] {
					np.fmin[x] = v
				}
				if first || v > np.fmax[x] {
					np.fmax[x] = v
				}
			case !n.float && c.Ints != nil:
				v := c.Ints[i]
				np.isum[x] += v
				if first || v < np.imin[x] {
					np.imin[x] = v
				}
				if first || v > np.imax[x] {
					np.imax[x] = v
				}
			default:
				return fmt.Errorf("native: column %s is not a number", n.name)
			}
		}
	}
	if err := a.addMeasures(p, b, sel, gids, pass, cell); err != nil {
		return err
	}
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
func (a *agg) addMeasures(p *part, b *Batch, sel, gids []int32, pass [][]bool, cell func(int, int32) int) error {
	if len(a.msrs) == 0 {
		return nil
	}
	_, val := b.col("value")
	_, rate := b.col("sample_rate")
	for _, c := range []struct {
		name string
		col  *Column
	}{{"value", val}, {"sample_rate", rate}} {
		if c.col != nil && c.col.Floats == nil && c.col.Ints == nil {
			return fmt.Errorf("native: column %s is not a number", c.name)
		}
	}
	if rate == nil {
		return nil
	}
	num := func(c *Column, i int32) (float64, bool) {
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
func (a *agg) addDistinct(p *part, j int, d *distinctCol, b *Batch, sel, gids []int32, keep []bool, off func(int32) int) error {
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
	buf := p.buf[:0]
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
		if d.day {
			cell := uint64(g)*uint64(a.ndays) + uint64(off(i))
			key := dayKey{c: cell<<1 | 1}
			if x >= 0 {
				key = dayKey{c: cell << 1, h: hashes[x]}
			}
			if n := len(buf); n == 0 || buf[n-1] != key {
				buf = append(buf, key)
			}
		}
	}
	if d.day {
		p.grew += p.days[j].addGroup(buf)
	}
	p.buf = buf
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
func (a *agg) groupsOf(b *Batch, sel []int32, p *part) ([]int32, int32, error) {
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

// run is one (group, day) cell's distinct non-NULL ids.
type run struct {
	cell uint64 // group*ndays + day
	n    int64
}

// merged is the workers' partials put together.
type merged struct {
	a       *agg
	groups  int
	count   [][]int64 // per mask
	bounces [][]int64 // per mask
	nums    []numPart
	msrs    []msrPart
	hists   [][]histEntry // per measure column with hist: sorted by cell, then bucket
	sets    [][]dset
	keys    [][]dayKey // per distinct column with day keys: all of them
	runs    [][]run    // and their counts per (group, day)
	// parts are the groups of each partition a share is taken over, by
	// partition (nil: one, every group); partOf is each group's.
	parts       [][]int32
	partOf      []int32
	shareTotals map[shareKey]float64
}

// shareKey is a cached share total: of a mask's rows (count) or a
// measure column's weights, over a partition (-1: every group), on a day
// (-1: all).
type shareKey struct {
	weight    bool
	idx, part int
	day       int
}

// partitionBy splits the groups into partitions by their first
// dimension's value, which a breakdown's share is taken over.
func (m *merged) partitionBy() {
	ids := map[dimVal]int32{}
	m.partOf = make([]int32, m.groups)
	for g := range m.groups {
		v := m.a.groups.vals[g][0]
		id, ok := ids[v]
		if !ok {
			id = int32(len(m.parts))
			ids[v] = id
			m.parts = append(m.parts, nil)
		}
		m.parts[id] = append(m.parts[id], int32(g))
		m.partOf[g] = id
	}
}

// merge puts the workers' partials together, releasing them.
func (a *agg) merge() *merged {
	m := &merged{a: a, groups: 1}
	if a.groups != nil {
		m.groups = a.groups.len()
	}
	cells := m.groups * a.nd
	m.count = make([][]int64, len(a.masks))
	if a.bounces {
		m.bounces = make([][]int64, len(a.masks))
	}
	for k := range a.masks {
		m.count[k] = make([]int64, cells)
		if a.bounces {
			m.bounces[k] = make([]int64, cells)
		}
	}
	m.nums = make([]numPart, len(a.nums))
	for j, n := range a.nums {
		np := &m.nums[j]
		np.n = make([]int64, cells)
		if n.float {
			np.fsum, np.fmin, np.fmax = make([]float64, cells), make([]float64, cells), make([]float64, cells)
		} else {
			np.isum, np.imin, np.imax = make([]int64, cells), make([]int64, cells), make([]int64, cells)
		}
	}
	m.msrs = make([]msrPart, len(a.msrs))
	m.hists = make([][]histEntry, len(a.msrs))
	hists := make([]map[histKey]float64, len(a.msrs))
	for j, c := range a.msrs {
		mp := &m.msrs[j]
		mp.wn, mp.sn, mp.w, mp.s = make([]int64, cells), make([]int64, cells), make([]float64, cells), make([]float64, cells)
		if c.hist {
			hists[j] = map[histKey]float64{}
		}
	}
	m.sets = make([][]dset, len(a.distinct))
	for j, d := range a.distinct {
		if d.set {
			m.sets[j] = make([]dset, m.groups)
		}
	}
	for w := range a.parts {
		p := &a.parts[w]
		// A worker's arrays may be longer than the groups (they grow
		// ahead); the cells past them hold nothing.
		for k, count := range p.count {
			for x, c := range count[:min(len(count), cells)] {
				m.count[k][x] += c
			}
		}
		for k, bounces := range p.bounces {
			for x, c := range bounces[:min(len(bounces), cells)] {
				m.bounces[k][x] += c
			}
		}
		for j := range p.nums {
			mergeNum(&m.nums[j], &p.nums[j], a.nums[j].float, cells)
		}
		for j := range p.msrs {
			mergeMsr(&m.msrs[j], &p.msrs[j], hists[j], cells)
		}
		for j := range p.sets {
			for g := range p.sets[j][:min(len(p.sets[j]), m.groups)] {
				m.sets[j][g].merge(&p.sets[j][g])
			}
			p.sets[j] = nil
		}
		p.count, p.bounces, p.nums, p.msrs = nil, nil, nil, nil
	}
	for j, h := range hists {
		if h == nil {
			continue
		}
		es := make([]histEntry, 0, len(h))
		for k, w := range h {
			es = append(es, histEntry{histKey: k, w: w})
		}
		slices.SortFunc(es, func(a, b histEntry) int { return cmpHistKey(a.histKey, b.histKey) })
		m.hists[j] = es
		hists[j] = nil
	}
	m.keys = make([][]dayKey, len(a.distinct))
	m.runs = make([][]run, len(a.distinct))
	for j, d := range a.distinct {
		if !d.day {
			continue
		}
		sinks := make([]*keySink, 0, len(a.parts))
		for w := range a.parts {
			if a.parts[w].days != nil {
				sinks = append(sinks, &a.parts[w].days[j])
			}
		}
		m.keys[j] = mergeKeys(sinks)
		m.runs[j] = runsOf(m.keys[j])
	}
	return m
}

// mergeMsr adds src's measure partials to dst's, its histogram to hist.
func mergeMsr(dst, src *msrPart, hist map[histKey]float64, cells int) {
	for x, n := range src.wn[:min(len(src.wn), cells)] {
		if n == 0 {
			continue
		}
		dst.wn[x] += n
		dst.w[x] += src.w[x]
		dst.sn[x] += src.sn[x]
		dst.s[x] += src.s[x]
	}
	for k, w := range src.hist {
		if k.cell < cells {
			hist[k] += w
		}
	}
}

func cmpHistKey(a, b histKey) int {
	if a.cell != b.cell {
		return cmp.Compare(a.cell, b.cell)
	}
	return cmp.Compare(a.bucket, b.bucket)
}

func mergeNum(dst, src *numPart, float bool, cells int) {
	for x, n := range src.n[:min(len(src.n), cells)] {
		if n == 0 {
			continue
		}
		first := dst.n[x] == 0
		dst.n[x] += n
		if float {
			dst.fsum[x] += src.fsum[x]
			if first || src.fmin[x] < dst.fmin[x] {
				dst.fmin[x] = src.fmin[x]
			}
			if first || src.fmax[x] > dst.fmax[x] {
				dst.fmax[x] = src.fmax[x]
			}
			continue
		}
		dst.isum[x] += src.isum[x]
		if first || src.imin[x] < dst.imin[x] {
			dst.imin[x] = src.imin[x]
		}
		if first || src.imax[x] > dst.imax[x] {
			dst.imax[x] = src.imax[x]
		}
	}
}

// runsOf counts sorted keys' non-NULL ids per cell.
func runsOf(keys []dayKey) []run {
	var out []run
	for _, k := range keys {
		c := k.cell()
		if len(out) == 0 || out[len(out)-1].cell != c {
			out = append(out, run{cell: c})
		}
		if !k.null() {
			out[len(out)-1].n++
		}
	}
	return out
}

// cellsOf calls f with each cell of group g: all its days when day < 0.
func (m *merged) cellsOf(g int32, day int, f func(x int)) {
	if int(g) >= m.groups {
		return
	}
	nd := m.a.nd
	if day >= 0 {
		f(int(g)*nd + day)
		return
	}
	for x := int(g) * nd; x < int(g+1)*nd; x++ {
		f(x)
	}
}

// rows is how many rows group g holds (on day, unless it is < 0).
func (m *merged) rows(g int32, day int) int64 { return m.maskRows(0, g, day) }

// maskRows is how many rows of mask group g holds (on day, unless it is
// < 0).
func (m *merged) maskRows(mask int, g int32, day int) int64 {
	var n int64
	m.cellsOf(g, day, func(x int) { n += m.count[mask][x] })
	return n
}

// compute is metric mt over the rows of the groups gs: on one day of the
// range (a trend's cell; gs one group then) or, when day < 0, over all;
// rounded as mt says.
func (m *merged) compute(mt metricSpec, gs []int32, day int) value {
	return m.computeRaw(mt, gs, day, false).round(mt.Round)
}

// computeOther is compute for a breakdown's Other row: its share is over
// every group.
func (m *merged) computeOther(mt metricSpec, gs []int32) value {
	return m.computeRaw(mt, gs, -1, true).round(mt.Round)
}

func (m *merged) computeRaw(mt metricSpec, gs []int32, day int, whole bool) value {
	switch mt.Op {
	case "ratio":
		// num / den as a DOUBLE, both over the same groups and day (an
		// Other row's both folded first): NULL when den is 0 or NULL, or
		// num NULL.
		num := m.computeRaw(*mt.ratioNum, gs, day, whole)
		den := m.computeRaw(*mt.ratioDen, gs, day, whole)
		if num.null || den.null || den.num() == 0 {
			return value{null: true}
		}
		return value{f: num.num() / den.num(), approx: num.approx || den.approx}
	case "share":
		part := -1
		if m.partOf != nil && !whole && len(gs) > 0 {
			part = int(m.partOf[gs[0]])
		}
		total := m.shareTotal(mt, part, day)
		if total == 0 {
			return value{null: true}
		}
		return value{f: m.shareOf(mt, gs, day) / total}
	case "count":
		var n int64
		for _, g := range gs {
			n += m.maskRows(mt.mask, g, day)
		}
		return intValue(n)
	case "sum", "avg", "min", "max":
		return m.evalNum(mt, gs, day)
	case "percentile":
		return m.percentile(mt, gs, day)
	case "mean", "weight":
		// SUM(value / sample_rate) / SUM(1 / sample_rate), and the latter
		// alone: NULL over no row (no value, for the mean).
		mp := &m.msrs[mt.msr]
		var wn, sn int64
		var w, s float64
		for _, g := range gs {
			m.cellsOf(g, day, func(x int) {
				wn += mp.wn[x]
				w += mp.w[x]
				sn += mp.sn[x]
				s += mp.s[x]
			})
		}
		switch {
		case wn == 0 || (mt.Op == "mean" && sn == 0):
			return value{null: true}
		case mt.Op == "weight":
			return value{f: w}
		}
		return value{f: s / w}
	case "bounces", "bounce_rate":
		// Sessions of one view, and their share of the sessions: DuckDB's
		// count(*) FILTER (WHERE views = 1)::DOUBLE / count(*), empty over
		// none.
		var b, n int64
		for _, g := range gs {
			n += m.maskRows(mt.mask, g, day)
			m.cellsOf(g, day, func(x int) { b += m.bounces[mt.mask][x] })
		}
		switch {
		case mt.Op == "bounces":
			return intValue(b)
		case n == 0:
			return value{null: true}
		}
		return value{f: float64(b) / float64(n)}
	case "actors", "users", "groups", opValues:
		if day >= 0 {
			return intValue(m.dayCount(mt.distinct, gs[0], day))
		}
		if len(gs) == 1 {
			if int(gs[0]) >= m.groups {
				return intValue(0)
			}
			n, approx := m.sets[mt.distinct][gs[0]].count()
			return value{isInt: true, i: n, approx: approx}
		}
		var u dset
		for _, g := range gs {
			u.merge(&m.sets[mt.distinct][g])
		}
		n, approx := u.count()
		return value{isInt: true, i: n, approx: approx}
	}
	// daily_*: the per-day distinct counts of the days with rows.
	var days []int64
	switch {
	case day >= 0:
		days = []int64{m.dayCount(mt.distinct, gs[0], day)}
	case len(gs) == 1:
		days = m.groupDays(mt.distinct, gs[0])
	default:
		days = m.unionDays(mt.distinct, gs)
	}
	if len(days) == 0 {
		return value{null: true}
	}
	var sum, top int64
	for i, n := range days {
		sum += n
		if i == 0 || n > top {
			top = n
		}
	}
	switch mt.Op {
	case "daily_sum":
		return intValue(sum)
	case "daily_max":
		return intValue(top)
	}
	return value{f: float64(sum) / float64(len(days))}
}

func (m *merged) evalNum(mt metricSpec, gs []int32, day int) value {
	np, float := &m.nums[mt.num], m.a.nums[mt.num].float
	var n, isum, imin, imax int64
	var fsum, fmin, fmax float64
	for _, g := range gs {
		m.cellsOf(g, day, func(x int) {
			c := np.n[x]
			if c == 0 {
				return
			}
			first := n == 0
			n += c
			if float {
				fsum += np.fsum[x]
				if first || np.fmin[x] < fmin {
					fmin = np.fmin[x]
				}
				if first || np.fmax[x] > fmax {
					fmax = np.fmax[x]
				}
				return
			}
			isum += np.isum[x]
			if first || np.imin[x] < imin {
				imin = np.imin[x]
			}
			if first || np.imax[x] > imax {
				imax = np.imax[x]
			}
		})
	}
	if n == 0 {
		return value{null: true}
	}
	switch {
	case mt.Op == "avg" && float:
		return value{f: fsum / float64(n)}
	case mt.Op == "avg":
		return value{f: float64(isum) / float64(n)}
	case float && mt.Op == "sum":
		return value{f: fsum}
	case float && mt.Op == "min":
		return value{f: fmin}
	case float:
		return value{f: fmax}
	case mt.Op == "sum":
		return intValue(isum)
	case mt.Op == "min":
		return intValue(imin)
	}
	return intValue(imax)
}

// shareOf is what share mt is taken of over the groups gs (on day, unless
// it is < 0): the rows of its mask, or their weights.
func (m *merged) shareOf(mt metricSpec, gs []int32, day int) float64 {
	if mt.Of != "weight" {
		var n int64
		for _, g := range gs {
			n += m.maskRows(mt.mask, g, day)
		}
		return float64(n)
	}
	var w float64
	mp := &m.msrs[mt.msr]
	for _, g := range gs {
		m.cellsOf(g, day, func(x int) { w += mp.w[x] })
	}
	return w
}

// shareTotal is shareOf over partition part's groups (every group when
// part < 0), cached.
func (m *merged) shareTotal(mt metricSpec, part, day int) float64 {
	k := shareKey{weight: mt.Of == "weight", idx: mt.mask, part: part, day: day}
	if k.weight {
		k.idx = mt.msr
	}
	if t, ok := m.shareTotals[k]; ok {
		return t
	}
	var gs []int32
	if part >= 0 {
		gs = m.parts[part]
	} else {
		gs = make([]int32, m.groups)
		for g := range gs {
			gs[g] = int32(g)
		}
	}
	t := m.shareOf(mt, gs, day)
	if m.shareTotals == nil {
		m.shareTotals = map[shareKey]float64{}
	}
	m.shareTotals[k] = t
	return t
}

// percentile is mt's quantile over the rows of the groups gs (on day,
// unless it is < 0), as the widgets' SQL reads it from the buckets: their
// weights summed per bucket, the running sum in ascending bucket order
// against the total, and the value of the first bucket whose running sum
// reaches q of the total; NULL over no row.
func (m *merged) percentile(mt metricSpec, gs []int32, day int) value {
	h := m.hists[mt.msr]
	var bs []histEntry
	cells := 0
	for _, g := range gs {
		m.cellsOf(g, day, func(x int) {
			i, _ := slices.BinarySearchFunc(h, x, func(e histEntry, c int) int { return cmp.Compare(e.cell, c) })
			if i < len(h) && h[i].cell == x {
				cells++
			}
			for ; i < len(h) && h[i].cell == x; i++ {
				bs = append(bs, h[i])
			}
		})
	}
	if len(bs) == 0 {
		return value{null: true}
	}
	if cells > 1 {
		// Several cells' histograms: one weight per bucket.
		slices.SortStableFunc(bs, func(a, b histEntry) int { return cmp.Compare(a.bucket, b.bucket) })
		n := 0
		for _, e := range bs {
			if n > 0 && bs[n-1].bucket == e.bucket {
				bs[n-1].w += e.w
				continue
			}
			bs[n] = e
			n++
		}
		bs = bs[:n]
	}
	var total float64
	for _, e := range bs {
		total += e.w
	}
	at := mt.Q * total
	var run float64
	for _, e := range bs {
		run += e.w
		if run >= at {
			return value{f: bucketValue(e.bucket)}
		}
	}
	return value{null: true}
}

// bucketValue is a bucket's representative value, as the widgets' SQL
// reads it: 0 for -1000, else 2 * 1.04^bucket / 2.04.
func bucketValue(b int64) float64 {
	if b == -1000 {
		return 0
	}
	return 2 * math.Pow(1.04, float64(b)) / 2.04
}

// dayCount is the distinct ids of group g on day.
func (m *merged) dayCount(j int, g int32, day int) int64 {
	cell := uint64(g)*uint64(m.a.ndays) + uint64(day)
	runs := m.runs[j]
	i, ok := slices.BinarySearchFunc(runs, cell, func(r run, c uint64) int { return cmp.Compare(r.cell, c) })
	if !ok {
		return 0
	}
	return runs[i].n
}

// groupDays is group g's distinct ids per day with rows.
func (m *merged) groupDays(j int, g int32) []int64 {
	nd := uint64(m.a.ndays)
	lo, hi := uint64(g)*nd, uint64(g+1)*nd
	runs := m.runs[j]
	i, _ := slices.BinarySearchFunc(runs, lo, func(r run, c uint64) int { return cmp.Compare(r.cell, c) })
	var out []int64
	for ; i < len(runs) && runs[i].cell < hi; i++ {
		out = append(out, runs[i].n)
	}
	return out
}

// unionDays is the distinct ids per day with rows of the groups gs
// together.
func (m *merged) unionDays(j int, gs []int32) []int64 {
	in := make([]bool, m.groups)
	for _, g := range gs {
		if int(g) < m.groups {
			in[g] = true
		}
	}
	nd := uint64(m.a.ndays)
	var keys []dayKey
	for _, k := range m.keys[j] {
		cell := k.cell()
		if g := cell / nd; in[g] {
			keys = append(keys, dayKey{c: (cell%nd)<<1 | k.c&1, h: k.h})
		}
	}
	runs := runsOf(sortedUniqueKeys(keys))
	out := make([]int64, len(runs))
	for i, r := range runs {
		out[i] = r.n
	}
	return out
}
