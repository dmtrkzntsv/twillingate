package native

import (
	"cmp"
	"math/bits"
	"slices"

	"github.com/dmtrkzntsv/twillingate/internal/shared/hll"
)

// The per-day keys of one day: an id's 64-bit hash (idKey) or nullKey,
// the key that only marks a group as having rows that day (a NULL id),
// and, when the aggregation has groups, the group. 0 is an empty slot.
const nullKey = 1

// idKey is the per-day key of an id hashed to h: h, but for the two
// values the table keeps for itself (odds 2/2^64 that one comes up, far
// below the hash collisions the keys already allow).
func idKey(h uint64) uint64 { return max(h, 2) }

// daySet is one worker's per-day keys of one day, each once: an
// open-addressing table probed linearly, of 8-byte keys, with a 4-byte
// group beside each when the aggregation has groups (wide). It is grown by
// half past 85% full, so it holds a key in 9 to 14 bytes (13 to 21 wide),
// against the 16 bytes a key took in sorted arrays. So memory follows the
// distinct keys rather than the rows, no batch is sorted, and a batch,
// mostly of one day, probes a table of that day's ids alone.
type daySet struct {
	hs   []uint64
	gs   []uint32 // per slot its group, when wide
	n    int
	wide bool
}

// daySetMin is a set's first size, in slots; daySetBytes a set's header.
const (
	daySetMin   = 16
	daySetBytes = 64
)

// slotBytes is what a slot of s takes.
func (s *daySet) slotBytes() int64 {
	if s.wide {
		return 12
	}
	return 8
}

func (s *daySet) slot(g uint32, h uint64) int {
	x := (h ^ uint64(g)*0x9E3779B97F4A7C15) * 0xBF58476D1CE4E5B9
	hi, _ := bits.Mul64(x, uint64(len(s.hs)))
	return int(hi)
}

// full says whether s must grow to take n keys.
func full(n, size int) bool { return 20*n > 17*size }

// add adds key h of group g (0 unless wide) and returns how many bytes
// the set's table grew by.
func (s *daySet) add(g uint32, h uint64) int64 {
	var grew int64
	if full(s.n+1, len(s.hs)) {
		old := len(s.hs)
		size := max(daySetMin, old+old/2)
		s.resize(size)
		grew = int64(size-old) * s.slotBytes()
	}
	s.insert(g, h)
	return grew
}

// insert puts key h of group g in its slot, unless there.
func (s *daySet) insert(g uint32, h uint64) {
	for i := s.slot(g, h); ; {
		switch e := s.hs[i]; {
		case e == 0:
			s.hs[i] = h
			if s.wide {
				s.gs[i] = g
			}
			s.n++
			return
		case e == h && (!s.wide || s.gs[i] == g):
			return
		}
		if i++; i == len(s.hs) {
			i = 0
		}
	}
}

// reserve grows s's table, unless large enough, to hold n keys.
func (s *daySet) reserve(n int) {
	size := max(daySetMin, len(s.hs))
	for full(n, size) {
		size += size / 2
	}
	if size > len(s.hs) {
		s.resize(size)
	}
}

func (s *daySet) resize(size int) {
	hs, gs := s.hs, s.gs
	s.hs, s.n = make([]uint64, size), 0
	if s.wide {
		s.gs = make([]uint32, size)
	}
	for i, h := range hs {
		if h != 0 {
			var g uint32
			if s.wide {
				g = gs[i]
			}
			s.insert(g, h)
		}
	}
}

// each calls f with every key of s and its group, in no order.
func (s *daySet) each(f func(g uint32, h uint64)) {
	for i, h := range s.hs {
		if h == 0 {
			continue
		}
		var g uint32
		if s.wide {
			g = s.gs[i]
		}
		f(g, h)
	}
}

func sortedUnique(s []uint64) []uint64 {
	slices.Sort(s)
	return slices.Compact(s)
}

// mergeSets returns the sets' keys in one set, releasing the others: the
// largest takes theirs, its table sized once for all of them. What it
// grows by at the merge is not charged, as the merge's arrays are not.
func mergeSets(sets []*daySet) daySet {
	var into *daySet
	total := 0
	for _, s := range sets {
		total += s.n
		if into == nil || s.n > into.n {
			into = s
		}
	}
	if into == nil || total == 0 {
		return daySet{}
	}
	into.reserve(total)
	for _, s := range sets {
		if s != into {
			s.each(func(g uint32, h uint64) { into.insert(g, h) })
			*s = daySet{}
		}
	}
	out := *into
	*into = daySet{}
	return out
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

// run is one (group, day) cell's distinct non-NULL ids.
type run struct {
	cell uint64 // group*ndays + day
	n    int64
}

// runsOf counts each cell's distinct non-NULL ids, in cell order: a
// day's keys' cells are sorted, as plain integers, then the cells.
func runsOf(days []daySet) []run {
	var out []run
	var cs []uint64
	for d := range days {
		cs = cs[:0]
		// Each key as its group shifted left by one, the low bit set for
		// the NULL key.
		days[d].each(func(g uint32, h uint64) {
			c := uint64(g) << 1
			if h == nullKey {
				c |= 1
			}
			cs = append(cs, c)
		})
		slices.Sort(cs)
		for x, c := range cs {
			if x == 0 || cs[x-1]>>1 != c>>1 {
				out = append(out, run{cell: (c>>1)*uint64(len(days)) + uint64(d)})
			}
			if c&1 == 0 {
				out[len(out)-1].n++
			}
		}
	}
	// A cell is group*ndays + day: by day, the cells of each group come
	// in order but the groups interleave.
	slices.SortFunc(out, func(a, b run) int { return cmp.Compare(a.cell, b.cell) })
	return out
}
