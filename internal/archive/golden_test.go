package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"slices"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
)

// -update-golden rewrites testdata/*.golden.json from the oracle's
// answers: DuckDB's until step 6b removes it, the Go code's after, and
// then only for a deliberate change a review reads in the diff.
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

// fileAnswer is a Parquet file as its golden keeps it: DuckDB's DESCRIBE
// of it ("[name TYPE YES]") and its rows, each one JSON array in column
// order as the engine sends a row (a TIMESTAMP as RFC 3339 text, a DATE
// as YYYY-MM-DD, a number as a number), sorted by its text unless the
// file's order is the answer. Past maxGoldenRows bytes of rows only their
// count and the SHA-256 of the rows are kept: each golden.Compact, in
// that order, joined by newlines.
type fileAnswer struct {
	Columns    []string          `json:"columns"`
	RowCount   int               `json:"row_count"`
	Rows       []json.RawMessage `json:"rows,omitempty"`
	RowsSHA256 string            `json:"rows_sha256,omitempty"`
}

const maxGoldenRows = 64 << 10

// answerOf reads path through DuckDB as a fileAnswer: sel is the select
// list (*, or * with EXCLUDE or REPLACE); ordered (with sel *) keeps the
// file's order.
func (f fixture) answerOf(t *testing.T, path, sel string, ordered bool) fileAnswer {
	t.Helper()
	q := "SELECT " + sel + " FROM read_parquet(" + quote(path) + ")"
	if ordered {
		q = "SELECT * EXCLUDE (file_row_number) FROM read_parquet(" + quote(path) + ", file_row_number=true) ORDER BY file_row_number"
	}
	res, err := f.eng.Query(context.Background(), q, 0)
	if err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	a := fileAnswer{Columns: f.describe(t, path), RowCount: len(res.Rows)}
	rows := make([]string, len(res.Rows))
	size := 0
	for i, r := range res.Rows {
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
