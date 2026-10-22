package main

import (
	"fmt"
	"math/rand/v2"
	"reflect"
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

func relErr(got, want uint64) float64 {
	d := float64(got) - float64(want)
	if d < 0 {
		d = -d
	}
	return d / float64(want)
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

// approxClose is the Global Constraints' tolerance: within 2%, or within 1
// where the exact value is under 50.
func approxClose(got, want string) bool {
	g, err1 := strconv.ParseUint(got, 10, 64)
	w, err2 := strconv.ParseUint(want, 10, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	if w < 50 {
		return g+1 >= w && g <= w+1
	}
	return relErr(g, w) <= 0.02
}

func checkApprox(t *testing.T, name string, got, want []Row) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d rows, exact has %d", name, len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if len(g) != 5 || !reflect.DeepEqual(g[:3], w[:3]) {
			t.Fatalf("%s row %d: %v, exact %v", name, i, g, w)
		}
		if !approxClose(g[3], w[3]) || !approxClose(g[4], w[4]) {
			t.Fatalf("%s row %d: users/groups %v, exact %v", name, i, g[3:], w[3:])
		}
	}
}

func TestAttributeValuesApproxMatchesExact(t *testing.T) {
	saved := Workers
	t.Cleanup(func() { Workers = saved })
	db := duck(t)
	files := monthFiles(t, db)
	for _, src := range sources() {
		for _, r := range [][2]string{{"2026-08-01", "2026-09-30"}, {"2026-08-10", "2026-09-05"}, {"2026-09-03", "2026-09-03"}} {
			for _, w := range []int{1, 3} {
				Workers = w
				from, to := day(r[0]), day(r[1])
				want, err := AttributeValues(src, files, from, to)
				if err != nil {
					t.Fatal(err)
				}
				got, err := AttributeValuesApprox(src, files, from, to)
				if err != nil {
					t.Fatal(err)
				}
				checkApprox(t, fmt.Sprintf("%s %v workers %d", src.Name(), r, w), got, want)
			}
		}
	}
	// Past the exact limit: 30k actors over 40 groups, so per-worker
	// sketches promote and merge.
	var batches []*Batch
	for g := 0; g < 40; g++ {
		n := 2000
		var as, gs, plan []string
		for i := 0; i < n; i++ {
			k := g*n + i
			as = append(as, fmt.Sprintf("a%d", (k*7919)%30000))
			gs = append(gs, fmt.Sprintf("g%d", k%3000))
			plan = append(plan, []string{"pro", "free", "team", "\x00"}[k%4])
		}
		batches = append(batches, &Batch{N: n, Cols: map[string]*Column{
			"actor_id": strCol(as...), "group_id": strCol(gs...), "attr_plan": strCol(plan...),
		}})
	}
	// The estimate does not depend on which strings arrive first: a leading
	// batch that meets every actor in another order (under another
	// attribute) leaves the plan rows as they were.
	var pas, pzz []string
	for j := 0; j < 30000; j++ {
		pas = append(pas, fmt.Sprintf("a%d", (j*7907+13)%30000))
		pzz = append(pzz, "x")
	}
	primer := &Batch{N: len(pas), Cols: map[string]*Column{
		"actor_id": strCol(pas...), "group_id": strCol(pzz...), "attr_zz": strCol(pzz...),
	}}
	Workers = 1
	fwd, err := AttributeValuesApprox(memSource{batches}, nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	primed, err := AttributeValuesApprox(memSource{append([]*Batch{primer}, batches...)}, nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(primed) != len(fwd)+1 || !reflect.DeepEqual(primed[:len(fwd)], fwd) {
		t.Fatalf("the order actors arrive in changed the estimate:\n%v\n%v", fwd, primed)
	}
	for _, w := range []int{1, 4} {
		Workers = w
		want, err := AttributeValues(memSource{batches}, nil, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		got, err := AttributeValuesApprox(memSource{batches}, nil, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		checkApprox(t, fmt.Sprintf("mem workers %d", w), got, want)
		if !reflect.DeepEqual(got, fwd) {
			t.Fatalf("workers %d changed the estimate:\n%v\n%v", w, got, fwd)
		}
		t.Logf("workers %d: exact %v, approx %v", w, want, got)
	}
}
