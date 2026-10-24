package hll

import (
	"math/rand/v2"
	"strconv"
	"testing"
)

// distinctIDs returns n distinct pseudo-random ids from a fixed seed.
func distinctIDs(seed uint64, n int) []uint32 {
	r := rand.New(rand.NewPCG(seed, 0x5eed))
	seen := make(map[uint32]struct{}, n)
	ids := make([]uint32, 0, n)
	for len(ids) < n {
		id := r.Uint32()
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func relErr(got, want uint64) float64 {
	d := float64(got) - float64(want)
	if d < 0 {
		d = -d
	}
	return d / float64(want)
}

func TestApproxSmallSetsExact(t *testing.T) {
	var s Sketch
	ids := distinctIDs(1, 1000)
	for round := 0; round < 3; round++ { // every id three times, in two orders
		for i := range ids {
			if round == 1 {
				s.Add(ids[len(ids)-1-i])
			} else {
				s.Add(ids[i])
			}
		}
		s.Add(ids[0])
	}
	if got := s.Count(); got != 1000 {
		t.Fatalf("count %d, want exactly 1000", got)
	}
	var e Sketch
	if e.Count() != 0 {
		t.Fatal("an empty sketch counts more than 0")
	}
	var full Sketch
	for _, id := range distinctIDs(2, ExactLimit) {
		full.Add(id)
	}
	if got := full.Count(); got != ExactLimit {
		t.Fatalf("count %d at the exact limit, want %d", got, ExactLimit)
	}
}

func TestApproxErrorBound(t *testing.T) {
	for _, n := range []int{10_000, 50_000, 100_000, 1_000_000} {
		var s Sketch
		ids := distinctIDs(uint64(n), n)
		for _, id := range ids {
			s.Add(id)
		}
		for _, id := range ids[:n/2] { // repeats change nothing
			s.Add(id)
		}
		got := s.Count()
		if rel := relErr(got, uint64(n)); rel > 0.02 {
			t.Fatalf("%d distinct: counted %d, %.2f%% off", n, got, 100*rel)
		}
	}
}

func TestSketchMergeEqualsUnion(t *testing.T) {
	cases := []struct{ a, b, overlap int }{
		{300, 400, 100},       // both exact, union exact
		{800, 700, 200},       // both exact, union past the limit
		{5000, 600, 300},      // sketch + exact set
		{600, 5000, 300},      // exact set + sketch
		{20000, 30000, 10000}, // both sketches
	}
	for _, c := range cases {
		ids := distinctIDs(uint64(c.a*7+c.b), c.a+c.b-c.overlap)
		as, bs := ids[:c.a], ids[c.a-c.overlap:]
		var a, b, one Sketch
		for _, id := range as {
			a.Add(id)
			one.Add(id)
		}
		for _, id := range bs {
			b.Add(id)
			one.Add(id)
		}
		a.Merge(&b)
		if got, want := a.Count(), one.Count(); got != want {
			t.Fatalf("%+v: merged %d, one sketch %d", c, got, want)
		}
		if n := len(ids); n <= ExactLimit && a.Count() != uint64(n) {
			t.Fatalf("%+v: merged %d, want exactly %d", c, a.Count(), n)
		}
	}
}

func TestMergeLeavesOtherAlone(t *testing.T) {
	var a, b Sketch
	for _, id := range distinctIDs(3, 5000) {
		b.Add(id)
	}
	before := b.Count()
	a.Merge(&b)
	for _, id := range distinctIDs(4, 5000) {
		a.Add(id)
	}
	if b.Count() != before {
		t.Fatal("adding to the merged sketch changed the source")
	}
}

// The estimate depends on the set of ids, not on the order they arrive in
// or how they are split across sketches.
func TestCountIsDeterministic(t *testing.T) {
	ids := distinctIDs(5, 40_000)
	var fwd, rev, left, right Sketch
	for _, id := range ids {
		fwd.Add(id)
	}
	for i := len(ids) - 1; i >= 0; i-- {
		rev.Add(ids[i])
	}
	for i, id := range ids {
		if i%2 == 0 {
			left.Add(id)
		} else {
			right.Add(id)
		}
	}
	right.Merge(&left)
	if fwd.Count() != rev.Count() || fwd.Count() != right.Count() {
		t.Fatalf("forward %d, reverse %d, split %d", fwd.Count(), rev.Count(), right.Count())
	}
}

func TestExactReportsPhase(t *testing.T) {
	ids := distinctIDs(6, ExactLimit+1)
	var s Sketch
	if !s.Exact() {
		t.Fatal("an empty sketch is not exact")
	}
	for _, id := range ids[:ExactLimit] {
		s.Add(id)
		s.Add(id)
	}
	if !s.Exact() {
		t.Fatalf("not exact at %d distinct ids", ExactLimit)
	}
	s.Add(ids[ExactLimit])
	if s.Exact() {
		t.Fatalf("still exact at %d distinct ids", ExactLimit+1)
	}
}

func TestFingerprint(t *testing.T) {
	if Fingerprint("a") == Fingerprint("b") {
		t.Fatal("distinct strings share a fingerprint")
	}
	if Fingerprint("actor") != Fingerprint("actor") {
		t.Fatal("fingerprint is not stable")
	}
	seen := map[uint32]bool{}
	for i := 0; i < 1000; i++ {
		f := Fingerprint(strconv.Itoa(i))
		if f == 0xFFFFFFFF {
			t.Fatal("fingerprint is the reserved value")
		}
		seen[f] = true
	}
	if len(seen) != 1000 {
		t.Fatalf("%d fingerprints for 1000 strings", len(seen))
	}
}
