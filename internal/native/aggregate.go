package native

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"hash/maphash"
	"math"
	"slices"
	"strconv"
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
//   - per-day distinct ids are packed uint64 keys (group*days + day)<<32 |
//     id, sorted and deduplicated per batch, compacted as they grow and
//     merged at the end: exact, and a (group, day) with rows but only NULL
//     ids still has a key (id nullID), so it counts as a day with rows;
//   - whole-range distinct ids are a dset per group: exact while it holds
//     at most hll.ExactLimit ids, then a sketch;
//   - strings become ids through interners shared by the workers, whose
//     mutex is held once per batch dictionary, never per row.

// trendCells is how many cells (series × days) a trend may track per
// group a breakdown may: a trend over many days tracks fewer series.
const trendCells = 10

// nullID is the id of a NULL value.
const nullID = ^uint32(0)

// interner maps strings to ids stable across batches and workers.
type interner struct {
	mu  sync.Mutex
	ids map[string]uint32
}

func newInterner() *interner { return &interner{ids: map[string]uint32{}} }

// remap returns in dst the id of each dictionary entry used (nullID for
// the others, which are not interned), and the bytes the new strings take.
func (in *interner) remap(dict []string, used []bool, dst []uint32) ([]uint32, int64, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	dst = dst[:0]
	var grew int64
	for j, s := range dict {
		if !used[j] {
			dst = append(dst, nullID)
			continue
		}
		id, ok := in.ids[s]
		if !ok {
			if len(in.ids) >= int(nullID) {
				return nil, 0, fmt.Errorf("native: more than %d distinct ids", nullID)
			}
			id = uint32(len(in.ids))
			in.ids[s] = id
			grew += int64(len(s)) + mapEntryBytes
		}
		dst = append(dst, id)
	}
	return dst, grew, nil
}

// mapEntryBytes is about what a map entry of a string key costs beside
// the string's bytes; sketchBytes is a sketch's registers.
const (
	mapEntryBytes = 48
	sketchBytes   = 1 << 14
)

// keySink is one worker's packed keys: each batch's sorted and
// deduplicated, appended, and the whole compacted once it is twice its
// size after the last compaction (and past compactAt keys), so memory
// follows the distinct keys rather than the rows.
type keySink struct {
	keys      []uint64
	compacted int
}

// compactAt is a variable so a test can lower it.
var compactAt = 1 << 20

// addGroup appends a batch's keys and returns how many bytes the sink's
// array grew by.
func (k *keySink) addGroup(group []uint64) int64 {
	before := cap(k.keys)
	k.keys = append(k.keys, sortedUnique(group)...)
	if len(k.keys) > compactAt && len(k.keys) > 2*k.compacted {
		k.keys = sortedUnique(k.keys)
		k.compacted = len(k.keys)
	}
	return int64(cap(k.keys)-before) * 8
}

func sortedUnique(s []uint64) []uint64 {
	slices.Sort(s)
	return slices.Compact(s)
}

// mergeKeys returns the sinks' keys sorted, each once, releasing each
// sink's array once copied.
func mergeKeys(sinks []*keySink) []uint64 {
	n := 0
	for _, s := range sinks {
		n += len(s.keys)
	}
	all := make([]uint64, 0, n)
	for _, s := range sinks {
		all = append(all, s.keys...)
		s.keys, s.compacted = nil, 0
	}
	return sortedUnique(all)
}

// dset is one group's distinct ids over the whole range: exact, as keys
// id<<32 | fingerprint, while it holds at most hll.ExactLimit ids, then a
// sketch of the fingerprints, whose count is approximate.
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

// memory is an aggregation's approximate size in bytes: the interned
// strings, the groups, the per-cell arrays, the exact distinct keys'
// arrays and the sketches, and the per-day keys. Workers add what they
// grow by once per batch.
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
	_, c := b.col(readColumn(name))
	if c == nil && typ == typeBucket {
		c = nullColumn("value", b.N) // NULL values: bucket -1000
	}
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
	num      int // index into agg.nums, for sum/avg/min/max
	distinct int // index into agg.distinct, for actors/users/groups and daily_*
}

// distinctOf is the id column a distinct op counts.
var distinctOf = map[string]string{"actors": "actor_id", "users": "user_id", "groups": "group_id"}

// distinctCol is one id column counted distinct: set says whole-range
// sets per group are kept, day that per-day keys are.
type distinctCol struct {
	name     string
	in       *interner
	set, day bool
}

// numCol is one numeric column summed, averaged or bounded.
type numCol struct {
	name  string
	float bool
}

// agg is one scan's aggregation.
type agg struct {
	filters  []filter
	dims     []string // the group's columns: a breakdown's dimensions, a trend's series
	dimTypes []colType
	groups   *groupTable // nil for the one group of a stat or a trend without series
	from     int64       // the range's first day, days since 1970-01-01
	ndays    int
	nd       int // cells per group: ndays in a trend, else 1
	nums     []numCol
	distinct []*distinctCol
	parts    []part
	mem      memory
}

// part is one worker's partials.
type part struct {
	count []int64
	nums  []numPart
	sets  [][]dset // per distinct column, per group; nil without set
	days  []keySink
	// scratch, reused across batches
	sel, gids  []int32
	ids        []uint32
	keys, buf  []uint64
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
	a := &agg{filters: filters, dims: dims, from: from, ndays: ndays, nd: 1, parts: make([]part, o.workers)}
	a.mem.max = o.maxBytes
	if perDay {
		a.nd = ndays
	}
	for _, d := range dims {
		a.dimTypes = append(a.dimTypes, typeOf(d))
	}
	if len(dims) > 0 {
		// Per-day keys pack group*ndays + day into 32 bits.
		limit := min(maxGroups, int((uint64(nullID)-1)/uint64(ndays)))
		if perDay {
			// A trend's arrays hold a cell per series and day: they get
			// trendCells per group the cap allows, however many days.
			limit = min(limit, trendCells*maxGroups/ndays)
		}
		a.groups = &groupTable{ids: map[string]int32{}, max: max(1, limit)}
	}
	numIdx := map[string]int{}
	distIdx := map[string]int{}
	dist := func(col string) int {
		i, ok := distIdx[col]
		if !ok {
			i = len(a.distinct)
			distIdx[col] = i
			a.distinct = append(a.distinct, &distinctCol{name: col, in: newInterner()})
		}
		return i
	}
	out := make([]metricSpec, len(metrics))
	for k, m := range metrics {
		m.num, m.distinct = -1, -1
		switch m.Op {
		case "sum", "avg", "min", "max":
			i, ok := numIdx[m.Column]
			if !ok {
				i = len(a.nums)
				numIdx[m.Column] = i
				a.nums = append(a.nums, numCol{name: m.Column, float: m.Column == "value" || m.Column == "sample_rate"})
			}
			m.num = i
		case "actors", "users", "groups":
			m.distinct = dist(distinctOf[m.Op])
			if perDay {
				a.distinct[m.distinct].day = true
			}
			if !perDay || distinctSets {
				a.distinct[m.distinct].set = true
			}
		case "daily_sum", "daily_max", "daily_avg":
			m.distinct = dist(distinctOf[m.Of])
			a.distinct[m.distinct].day = true
		}
		out[k] = m
	}
	return a, out
}

// columns is what a scan must read for the aggregation.
func (a *agg) columns() []string {
	cols := []string{"day"}
	add := func(c string) {
		if !slices.Contains(cols, c) {
			cols = append(cols, c)
		}
	}
	for _, f := range a.filters {
		add(readColumn(f.column))
	}
	for _, d := range a.dims {
		add(readColumn(d))
	}
	for _, n := range a.nums {
		add(n.name)
	}
	for _, d := range a.distinct {
		add(d.name)
	}
	return cols
}

// grow makes p's arrays hold groups groups, adding what they grow by to
// p.grew.
func (p *part) grow(a *agg, groups int) {
	cells := groups * a.nd
	if len(p.count) >= cells {
		return
	}
	cells = max(cells, 2*len(p.count))
	// Per cell: the count, and per numeric column 4 numbers; per group a
	// set header per distinct column.
	perCell := int64(8 + 32*len(a.nums))
	perGroup := int64(0)
	for _, d := range a.distinct {
		if d.set {
			perGroup += 48
		}
	}
	p.grew += int64(cells-len(p.count))*perCell + int64(cells-len(p.count))/int64(a.nd)*perGroup
	p.count = growTo(p.count, cells)
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

// add aggregates batch b into worker w's partials.
func (a *agg) add(w int, b *Batch) error {
	p := &a.parts[w]
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
	p.grew = 0
	defer func() { a.mem.add(p.grew) }()
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
	for k, i := range sel {
		if gids[k] >= 0 {
			p.count[cell(k, i)]++
		}
	}
	for j, n := range a.nums {
		_, c := b.col(n.name)
		if c == nil {
			continue
		}
		np := &p.nums[j]
		for k, i := range sel {
			if gids[k] < 0 || (c.Null != nil && c.Null[i]) {
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
	for j, d := range a.distinct {
		if err := a.addDistinct(p, j, d, b, sel, gids, off); err != nil {
			return err
		}
	}
	return nil
}

func (a *agg) addDistinct(p *part, j int, d *distinctCol, b *Batch, sel, gids []int32, off func(int32) int) error {
	_, c := b.col(d.name)
	// The dictionary entries the tracked rows use: only those are
	// interned or hashed.
	var used []bool
	if c != nil {
		if c.Idx == nil {
			return fmt.Errorf("native: column %s is not text", d.name)
		}
		used = make([]bool, len(c.Dict))
		for k, i := range sel {
			if x := c.Idx[i]; gids[k] >= 0 && x >= 0 {
				used[x] = true
			}
		}
	}
	// ids are the per-day keys' ids, from the interner (exact); sets are
	// the whole-range keys: the interned id, or without per-day keys a
	// second 32-bit hash, above the fingerprint a sketch takes.
	ids, keys := p.ids[:0], p.keys[:0]
	if c != nil {
		if d.day {
			var err error
			var grew int64
			if ids, grew, err = d.in.remap(c.Dict, used, ids); err != nil {
				return err
			}
			p.grew += grew
		}
		if d.set {
			for x, s := range c.Dict {
				var k uint64
				switch {
				case !used[x]:
				case d.day:
					k = uint64(ids[x])<<32 | uint64(hll.Fingerprint(s))
				default:
					k = uint64(uint32(maphash.String(hashSeed, s)))<<32 | uint64(hll.Fingerprint(s))
				}
				keys = append(keys, k)
			}
		}
	}
	p.ids, p.keys = ids, keys
	buf := p.buf[:0]
	for k, i := range sel {
		g := gids[k]
		if g < 0 {
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
			id := nullID
			if x >= 0 {
				id = ids[x]
			}
			k := (uint64(g)*uint64(a.ndays)+uint64(off(i)))<<32 | uint64(id)
			if n := len(buf); n == 0 || buf[n-1] != k {
				buf = append(buf, k)
			}
		}
	}
	if d.day {
		p.grew += p.days[j].addGroup(buf)
	}
	p.buf = buf
	return nil
}

// hashSeed seeds the hash that, with a fingerprint, keys a whole-range
// distinct id without interning it: two ids share a key with odds about
// n²/2^65 among n, far below the sketch's error.
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
	a      *agg
	groups int
	count  []int64
	nums   []numPart
	sets   [][]dset
	keys   [][]uint64 // per distinct column with day keys: all of them
	runs   [][]run    // and their counts per (group, day)
}

// merge puts the workers' partials together, releasing them.
func (a *agg) merge() *merged {
	m := &merged{a: a, groups: 1}
	if a.groups != nil {
		m.groups = a.groups.len()
	}
	cells := m.groups * a.nd
	m.count = make([]int64, cells)
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
		for x, c := range p.count[:min(len(p.count), cells)] {
			m.count[x] += c
		}
		for j := range p.nums {
			mergeNum(&m.nums[j], &p.nums[j], a.nums[j].float, cells)
		}
		for j := range p.sets {
			for g := range p.sets[j][:min(len(p.sets[j]), m.groups)] {
				m.sets[j][g].merge(&p.sets[j][g])
			}
			p.sets[j] = nil
		}
		p.count, p.nums = nil, nil
	}
	m.keys = make([][]uint64, len(a.distinct))
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
func runsOf(keys []uint64) []run {
	var out []run
	for _, k := range keys {
		c := k >> 32
		if len(out) == 0 || out[len(out)-1].cell != c {
			out = append(out, run{cell: c})
		}
		if uint32(k) != nullID {
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
func (m *merged) rows(g int32, day int) int64 {
	var n int64
	m.cellsOf(g, day, func(x int) { n += m.count[x] })
	return n
}

// compute is metric mt over the rows of the groups gs: on one day of the
// range (a trend's cell; gs one group then) or, when day < 0, over all.
func (m *merged) compute(mt metricSpec, gs []int32, day int) value {
	switch mt.Op {
	case "count":
		var n int64
		for _, g := range gs {
			n += m.rows(g, day)
		}
		return intValue(n)
	case "sum", "avg", "min", "max":
		return m.evalNum(mt, gs, day)
	case "actors", "users", "groups":
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
	var keys []uint64
	for _, k := range m.keys[j] {
		cell := k >> 32
		if g := cell / nd; in[g] {
			keys = append(keys, (cell%nd)<<32|k&0xFFFFFFFF)
		}
	}
	runs := runsOf(sortedUnique(keys))
	out := make([]int64, len(runs))
	for i, r := range runs {
		out[i] = r.n
	}
	return out
}
