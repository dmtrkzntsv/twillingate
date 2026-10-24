package reporting

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// -update-golden rewrites testdata/*.golden.json from native's answers,
// only for a deliberate change a review reads in the diff. The goldens
// were frozen from the old query engine's answers (step 6a); they are the
// only reference now.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from native's answers")

// keep returns the golden kept under key in t's test's file. With
// -update-golden it first keeps got (native's answer, as the golden reads)
// there, unless the golden held when the run began is alike (the test's
// own comparison passes on it: an old answer an allowance accepts, say),
// which then stays as it was.
func keep[T any](t testing.TB, key string, got T, alike func(prev T) bool) T {
	t.Helper()
	s := golden.For(t, *updateGolden)
	if *updateGolden {
		if prev, ok := golden.Previous[T](t, s, key); ok && alike(prev) {
			got = prev
		}
	}
	return golden.Get(t, s, key, got)
}

// frozenResult is the golden of an old SQL's answer under key, got being
// native's answer to compare with it under allows (compareParity): with
// -update-golden a golden held that compares clean stays, else got is
// kept.
func frozenResult(t *testing.T, key string, got readsql.Result, allows []allowance) readsql.Result {
	t.Helper()
	return keep(t, key, got, func(prev readsql.Result) bool {
		diffs, _ := compareParity(prev, got, allows)
		return len(diffs) == 0
	})
}

// asked counts, per (sub)test, the times each query was asked.
var asked struct {
	sync.Mutex
	n map[testing.TB]map[string]int
}

// queryKey is the key of an answer to q in t's (sub)test: the subtest's
// name and a hash of the SQL, then "#n" when it is asked again, counted
// in the order asked.
func queryKey(t testing.TB, q string) string {
	sum := sha256.Sum256([]byte(q))
	key := fmt.Sprintf("%x", sum[:6])
	if _, sub, ok := strings.Cut(t.Name(), "/"); ok {
		key = sub + " " + key
	}
	asked.Lock()
	defer asked.Unlock()
	if asked.n == nil {
		asked.n = map[testing.TB]map[string]int{}
	}
	m := asked.n[t]
	if m == nil {
		m = map[string]int{}
		asked.n[t] = m
		t.Cleanup(func() {
			asked.Lock()
			defer asked.Unlock()
			delete(asked.n, t)
		})
	}
	n := m[key]
	m[key]++
	if n > 0 {
		key += fmt.Sprintf(" #%d", n)
	}
	return key
}

// rowsAnswer is a query's rows as their golden keeps them, beside the SQL.
type rowsAnswer struct {
	SQL  string     `json:"sql"`
	Rows [][]string `json:"rows"`
}
