package app

import (
	"flag"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
)

// -update-golden rewrites testdata/*.golden.json from the oracle's
// answers: DuckDB's until step 6b removes it, native's after, and then
// only for a deliberate change a review reads in the diff.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from the oracle's answers")

// frozen returns the golden kept under key in t's test's file, keeping
// duck there first with -update-golden, and fails t unless duck equals it.
func frozen[T any](t testing.TB, key string, duck T) T {
	t.Helper()
	want := golden.Get(t, golden.For(t, *updateGolden), key, duck)
	if d := golden.Diff(duck, want); d != "" {
		t.Errorf("%s: DuckDB's answer differs from its golden at %s", key, d)
	}
	return want
}
