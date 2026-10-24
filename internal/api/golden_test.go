package api

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
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

// oldAnswer is an old query's answer as its golden keeps it, beside the
// SQL and the values bound.
type oldAnswer struct {
	SQL  string   `json:"sql"`
	Vals string   `json:"vals,omitempty"`
	Out  tableOut `json:"out"`
}

// oldTable runs one of the tools' old queries over the files (filesTable)
// and returns its answer frozen in t's golden file, keyed by the range
// and a hash of the SQL and values: DuckDB's must be the golden's, its
// numbers within 1e-9 (sums of doubles depend on their order), its text,
// truncation and note exactly.
func oldTable(t *testing.T, h *host, in rangeIn, q string, vals map[string]enginesql.Value) tableOut {
	t.Helper()
	duck, err := h.filesTable(context.Background(), in, q, vals)
	if err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	v := ""
	if len(vals) > 0 {
		v = fmt.Sprint(vals)
	}
	sum := sha256.Sum256([]byte(q + "\x00" + v))
	key := fmt.Sprintf("%d %s..%s %x", in.ProjectID, in.From, in.To, sum[:6])
	if h.queryRows > 0 {
		key += fmt.Sprintf(" rows %d", h.queryRows)
	}
	want := oracle(t, key, oldAnswer{SQL: q, Vals: v, Out: duck}).Out
	sameNumbers(t, key+": DuckDB's answer against its golden", duck, want, 1e-9)
	if duck.Truncated != want.Truncated || duck.Note != want.Note {
		t.Errorf("%s: DuckDB's truncated %v, note %q; the golden's %v, %q", key, duck.Truncated, duck.Note, want.Truncated, want.Note)
	}
	return want
}
