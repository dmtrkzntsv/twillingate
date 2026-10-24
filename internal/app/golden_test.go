package app

import (
	"flag"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
)

// -update-golden rewrites testdata/*.golden.json from native's answers,
// only for a deliberate change a review reads in the diff. The goldens
// were frozen from the old query engine's answers (step 6a); they are the
// only reference now.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from native's answers")

// frozen returns the golden kept under key in t's test's file, keeping
// got (native's answer) there first with -update-golden, and fails t
// unless got equals it.
func frozen[T any](t testing.TB, key string, got T) T {
	t.Helper()
	want := golden.Get(t, golden.For(t, *updateGolden), key, got)
	if d := golden.Diff(got, want); d != "" {
		t.Errorf("%s: the answer differs from its golden at %s", key, d)
	}
	return want
}
