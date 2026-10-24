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

// -update-golden rewrites testdata/*.golden.json from the oracle's
// answers: DuckDB's until step 6b removes it, native's after, and then
// only for a deliberate change a review reads in the diff.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from the oracle's answers")

// oracle returns the golden kept under key in t's test's file, and keeps
// duck there first with -update-golden. The caller compares duck with it.
func oracle[T any](t testing.TB, key string, duck T) T {
	t.Helper()
	return golden.Get(t, golden.For(t, *updateGolden), key, duck)
}

// frozen is oracle, failing t unless duck equals the golden exactly.
func frozen[T any](t testing.TB, key string, duck T) T {
	t.Helper()
	want := oracle(t, key, duck)
	if d := golden.Diff(duck, want); d != "" {
		t.Errorf("%s: DuckDB's answer differs from its golden at %s", key, d)
	}
	return want
}

// frozenResult is an old SQL's answer (duck) frozen under key: DuckDB's
// must be the golden's, numbers compared as numbers (sameNumber), with no
// allowance; the golden's is returned to compare native's with.
func frozenResult(t *testing.T, key string, duck readsql.Result) readsql.Result {
	t.Helper()
	want := oracle(t, key, duck)
	if diffs, _ := compareParity(want, duck, nil); len(diffs) > 0 {
		t.Errorf("%s: DuckDB's answer (below as native's) differs from its golden (as the SQL's):\n%s", key, strings.Join(diffs, "\n"))
	}
	return want
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
