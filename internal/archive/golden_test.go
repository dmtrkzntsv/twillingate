package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"slices"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
)

// -update-golden rewrites testdata/*.golden.json from the Go code's
// answers, only for a deliberate change a review reads in the diff. The
// goldens were frozen from the old query engine's answers (step 6a); they
// are the only reference now.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from the Go code's answers")

// frozen returns the golden kept under key in t's test's file, keeping
// got there first with -update-golden, and fails t unless got equals it
// exactly.
func frozen[T any](t testing.TB, key string, got T) T {
	t.Helper()
	want := golden.Get(t, golden.For(t, *updateGolden), key, got)
	if d := golden.Diff(got, want); d != "" {
		t.Errorf("%s: the answer differs from its golden at %s", key, d)
	}
	return want
}

// fileAnswer is a Parquet file as its golden keeps it: the DESCRIBE of it
// ("[name TYPE YES]") and its rows, each one JSON array in column order
// as the engine sent a row (pqTable's values), sorted by its text unless
// the file's order is the answer. Past maxGoldenRows bytes of rows only
// their count and the SHA-256 of the rows are kept: each golden.Compact,
// in that order, joined by newlines.
type fileAnswer struct {
	Columns    []string          `json:"columns"`
	RowCount   int               `json:"row_count"`
	Rows       []json.RawMessage `json:"rows,omitempty"`
	RowsSHA256 string            `json:"rows_sha256,omitempty"`
}

const maxGoldenRows = 64 << 10

// answerOf is p as a fileAnswer; ordered keeps the file's order.
func answerOf(t testing.TB, p pqTable, ordered bool) fileAnswer {
	t.Helper()
	a := fileAnswer{Columns: p.describe(), RowCount: len(p.rows)}
	rows := make([]string, len(p.rows))
	size := 0
	for i, r := range p.rows {
		b, err := golden.Compact(r)
		if err != nil {
			t.Fatal(err)
		}
		rows[i] = string(b)
		size += len(b)
	}
	if !ordered {
		slices.Sort(rows)
	}
	if size > maxGoldenRows {
		sum := sha256.Sum256([]byte(strings.Join(rows, "\n")))
		a.RowsSHA256 = hex.EncodeToString(sum[:])
		return a
	}
	for _, r := range rows {
		a.Rows = append(a.Rows, json.RawMessage(r))
	}
	return a
}

// fileFrozen is the Go file at path read back (answerOf), failing t
// unless it is the golden kept under key, what differs named.
func fileFrozen(t *testing.T, key, name string, p pqTable, ordered bool) {
	t.Helper()
	got := answerOf(t, p, ordered)
	sameAnswer(t, name, got, golden.Get(t, golden.For(t, *updateGolden), key, got))
}

// sameAnswer fails t unless got (the Go file's) is want (the golden's),
// what differs named.
func sameAnswer(t *testing.T, name string, got, want fileAnswer) {
	t.Helper()
	if !eq(got.Columns, want.Columns) {
		t.Errorf("%s: columns\n%v\nwant the golden's\n%v", name, got.Columns, want.Columns)
	}
	if got.RowCount != want.RowCount || got.RowsSHA256 != want.RowsSHA256 {
		t.Errorf("%s: %d rows (sha %q), want the golden's %d (sha %q)", name, got.RowCount, got.RowsSHA256, want.RowCount, want.RowsSHA256)
	}
	for i := range max(len(got.Rows), len(want.Rows)) {
		if i >= len(got.Rows) || i >= len(want.Rows) || string(got.Rows[i]) != string(want.Rows[i]) {
			g, w := "(none)", "(none)"
			if i < len(got.Rows) {
				g = string(got.Rows[i])
			}
			if i < len(want.Rows) {
				w = string(want.Rows[i])
			}
			t.Errorf("%s: row %d\n%s\nwant the golden's\n%s", name, i, g, w)
			return
		}
	}
}
