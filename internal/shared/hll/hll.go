// Package hll counts distinct values in constant space: exactly while a set
// is small, then with a HyperLogLog sketch. Values are 32-bit fingerprints
// (see Fingerprint), so two sketches over the same ids merge into the
// sketch of their union, which lets a count be split across workers or
// days and put back together.
package hll

import (
	"math"
	"math/bits"
	"slices"
)

// ExactLimit is the most distinct ids a Sketch counts exactly.
const ExactLimit = 1024

const (
	hllP = 14
	hllM = 1 << hllP
)

// Sketch counts distinct uint32 ids: exact while it holds at most
// ExactLimit ids, then a HyperLogLog with 2^14 registers (standard error
// 1.04/sqrt(2^14), about 0.8%). The tests hold the estimate to 2%, which is
// statistical (about 2.5 sigma), not a guarantee for every input. Ids come
// from Fingerprint, whose collisions (about n^2/2^33 pairs among n distinct
// strings) are small next to that error. The zero value is an empty sketch.
// A Sketch is not safe for concurrent use; Count compacts it, so even
// reading needs the caller's lock.
type Sketch struct {
	ids  []uint32 // exact set, unsorted and with repeats past sorted
	sort int      // ids[:sort] is sorted and unique
	regs []uint8  // nil while exact
}

// Add counts id.
func (s *Sketch) Add(id uint32) {
	if s.regs != nil {
		s.addHash(id)
		return
	}
	if n := len(s.ids); n > 0 && s.ids[n-1] == id {
		return
	}
	s.ids = append(s.ids, id)
	if len(s.ids) >= 2*ExactLimit {
		s.compact()
	}
}

// compact sorts and dedupes the exact set, and turns it into registers
// once it holds more than ExactLimit ids.
func (s *Sketch) compact() {
	if s.sort == len(s.ids) {
		return
	}
	slices.Sort(s.ids)
	s.ids = slices.Compact(s.ids)
	s.sort = len(s.ids)
	if len(s.ids) > ExactLimit {
		s.promote()
	}
}

func (s *Sketch) promote() {
	s.regs = make([]uint8, hllM)
	for _, id := range s.ids {
		s.addHash(id)
	}
	s.ids, s.sort = nil, 0
}

func (s *Sketch) addHash(id uint32) {
	h := splitmix64(uint64(id))
	i := h >> (64 - hllP)
	// The rank of the first set bit after the index bits; the sentinel bit
	// caps it at 64-hllP+1.
	r := uint8(bits.LeadingZeros64(h<<hllP|1<<(hllP-1)) + 1)
	if r > s.regs[i] {
		s.regs[i] = r
	}
}

func splitmix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ x>>30) * 0xBF58476D1CE4E5B9
	x = (x ^ x>>27) * 0x94D049BB133111EB
	return x ^ x>>31
}

// Merge adds o's ids to s; o is left as it was.
func (s *Sketch) Merge(o *Sketch) {
	switch {
	case o.regs != nil && s.regs == nil:
		ids := s.ids
		s.regs = slices.Clone(o.regs)
		s.ids, s.sort = nil, 0
		for _, id := range ids {
			s.addHash(id)
		}
	case o.regs != nil:
		for i, r := range o.regs {
			if r > s.regs[i] {
				s.regs[i] = r
			}
		}
	default:
		for _, id := range o.ids {
			s.Add(id)
		}
	}
}

// Exact reports whether the sketch still holds the exact set of ids, so
// Count is not an estimate.
func (s *Sketch) Exact() bool {
	if s.regs == nil {
		s.compact()
	}
	return s.regs == nil
}

// Fingerprint returns the 32-bit id of a string: FNV-1a over its bytes,
// with the 64-bit hash folded to 32 bits, and never 0xFFFFFFFF, which
// callers may keep for "no value".
func Fingerprint(s string) uint32 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 1099511628211
	}
	f := uint32(h ^ h>>32)
	if f == math.MaxUint32 {
		f--
	}
	return f
}

// Count returns how many distinct ids were added: exact up to ExactLimit,
// then the HyperLogLog estimate.
//
// The estimate is Ertl's improved raw estimator ("New cardinality
// estimation algorithms for HyperLogLog sketches", 2017): the standard
// alpha*m^2/z harmonic mean, with z corrected for empty registers (sigma,
// which is linear counting in the small range) and for saturated ones (tau).
// The textbook switch from linear counting to the raw estimate at 2.5m
// leaves a bias of about +2% around 40k ids with 2^14 registers, past the
// tolerance; this has none, and needs no empirical tables.
func (s *Sketch) Count() uint64 {
	if s.regs == nil {
		s.compact()
		if s.regs == nil {
			return uint64(len(s.ids))
		}
	}
	const q = 64 - hllP // ranks run 0..q+1
	var c [q + 2]int
	for _, r := range s.regs {
		c[r]++
	}
	const m = float64(hllM)
	if c[0] == hllM {
		return 0
	}
	z := m * hllTau(1-float64(c[q+1])/m)
	for k := q; k >= 1; k-- {
		z = 0.5 * (z + float64(c[k]))
	}
	z += m * hllSigma(float64(c[0])/m)
	return uint64(math.Round(m * m / (2 * math.Ln2) / z))
}

func hllSigma(x float64) float64 {
	if x == 1 {
		return math.Inf(1)
	}
	y, z := 1.0, x
	for {
		x *= x
		prev := z
		z += x * y
		y += y
		if z == prev {
			return z
		}
	}
}

func hllTau(x float64) float64 {
	if x == 0 || x == 1 {
		return 0
	}
	y, z := 1.0, 1-x
	for {
		x = math.Sqrt(x)
		prev := z
		y *= 0.5
		z -= (1 - x) * (1 - x) * y
		if z == prev {
			return z / 3
		}
	}
}
