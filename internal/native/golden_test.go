package native

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// -update-golden rewrites testdata/*.golden.json from the oracle's
// answers: DuckDB's until step 6b removes it, the native engine's after,
// and then only for a deliberate change a review reads in the diff.
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
		t.Fatalf("%s: DuckDB's answer differs from its golden at %s", key, d)
	}
	return want
}

// asked counts, per (sub)test, the times each query was asked.
var asked struct {
	sync.Mutex
	n map[testing.TB]map[string]int
}

// queryKey is the key of an answer to q under rules in t's (sub)test: the
// subtest's name and a hash of the SQL, which the answer keeps beside it
// to be read, then "#n" when the test asks the same again (over files it
// has written to since, say), counted in the order it asks.
func queryKey(t testing.TB, q string, rules archive.Rules) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%+v\x00%s", rules, q)))
	key := hex.EncodeToString(sum[:6])
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

// tableAnswer is a table as its golden keeps it: the SQL that asked
// (and its rules, when any), the columns when they are part of the
// answer, and each row as one JSON array of its cells, a NULL as null.
// Past maxGoldenRows bytes of rows only their count and their SHA-256
// are kept (rowsDigest): what DuckDB answers is checked against it, and
// the rows returned to compare native's with are then DuckDB's, which the
// digest proves the golden's. Without DuckDB (step 6b), native's rows are
// to be digested and compared instead.
type tableAnswer struct {
	SQL      string   `json:"sql"`
	Rules    string   `json:"rules,omitempty"`
	Columns  []string `json:"columns,omitempty"`
	Rows     [][]any  `json:"rows,omitempty"`
	RowCount int      `json:"row_count,omitempty"`
	SHA256   string   `json:"rows_sha256,omitempty"`
}

const maxGoldenRows = 64 << 10

// tableOf is rows as a tableAnswer's rows, or their count and digest.
func tableOf(t testing.TB, a tableAnswer, rows [][]string) tableAnswer {
	t.Helper()
	size := 0
	for _, r := range rows {
		row := make([]any, len(r))
		for j, c := range r {
			if c != nullCell {
				row[j] = c
			}
			size += len(c) + 3
		}
		a.Rows = append(a.Rows, row)
	}
	if size > maxGoldenRows {
		a.Rows, a.RowCount, a.SHA256 = nil, len(rows), rowsDigest(t, rows)
	}
	return a
}

// rowsDigest is the SHA-256 of rows, each its golden.Compact JSON, joined
// by newlines.
func rowsDigest(t testing.TB, rows [][]string) string {
	t.Helper()
	h := sha256.New()
	for i, r := range rows {
		b, err := golden.Compact(r)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			h.Write([]byte("\n"))
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// rowsOf is a's rows as cells, NULL as nullCell; for a digest, duck's
// once their digest is a's.
func rowsOf(t testing.TB, a tableAnswer, duck [][]string) [][]string {
	t.Helper()
	if a.SHA256 != "" {
		if got := rowsDigest(t, duck); got != a.SHA256 || len(duck) != a.RowCount {
			t.Fatalf("%s: DuckDB's %d rows digest to %s, the golden's %d to %s", a.SQL, len(duck), got, a.RowCount, a.SHA256)
		}
		return duck
	}
	out := make([][]string, len(a.Rows))
	for i, r := range a.Rows {
		out[i] = make([]string, len(r))
		for j, c := range r {
			s, ok := c.(string)
			if !ok {
				s = nullCell
			}
			out[i][j] = s
		}
	}
	return out
}

func rulesText(rules archive.Rules) string {
	if len(rules.Events) == 0 && len(rules.Attributes) == 0 {
		return ""
	}
	return fmt.Sprintf("%+v", rules)
}

// frozenRows freezes duck's columns and rows for q under rules, exactly,
// and returns the golden's.
func frozenRows(t *testing.T, q string, rules archive.Rules, cols []string, rows [][]string) ([]string, [][]string) {
	t.Helper()
	if rerun(t) {
		return cols, rows
	}
	a := frozen(t, queryKey(t, q, rules), tableOf(t, tableAnswer{SQL: q, Rules: rulesText(rules), Columns: cols}, rows))
	return a.Columns, rowsOf(t, a, rows)
}

// rerun says whether t is TestAnswerManyEqualsAnswer running a parity
// test again, to record its native calls: its oracle's answers are the
// parity test's own goldens, so the rerun keeps none of its own.
func rerun(t testing.TB) bool {
	return strings.HasPrefix(t.Name(), "TestAnswerManyEqualsAnswer/")
}

// foundAnswer is an archive.Found as its golden keeps it: each event's
// fields that are set, by name.
type foundAnswer struct {
	ProjectID      int64
	ID             string
	Source, Newest map[string]any `json:",omitempty"`
}

// setFields is e's fields that are not their zero value.
func setFields(t testing.TB, e store.Event) map[string]any {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		switch v {
		case nil, "", float64(0), false, "0001-01-01T00:00:00Z":
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func eventOf(t testing.TB, m map[string]any) store.Event {
	t.Helper()
	var e store.Event
	if m == nil {
		return e
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// frozenFound freezes the oracle's answer to ls (sorted) and returns the
// golden's, keyed by what was asked.
func frozenFound(t *testing.T, days int, rules archive.Rules, ls []archive.Lookup, duck []archive.Found) []archive.Found {
	t.Helper()
	if rerun(t) {
		return duck
	}
	asked := make([]string, len(ls))
	for i, l := range ls {
		asked[i] = fmt.Sprintf("%d/%q %s:%q install %q connection %q at %s", l.ProjectID, l.ID, l.ActorKind, l.ActorID,
			l.InstallID, l.ConnectionID, l.At.UTC().Format("2006-01-02T15:04:05.999999999Z"))
	}
	key := fmt.Sprintf("%d days: %s", days, strings.Join(asked, "; "))
	if r := rulesText(rules); r != "" {
		key = fmt.Sprintf("%d days, rules %s: %s", days, r, strings.Join(asked, "; "))
	}
	answers := make([]foundAnswer, len(duck))
	for i, f := range duck {
		answers[i] = foundAnswer{ProjectID: f.ProjectID, ID: f.ID, Source: setFields(t, f.Source), Newest: setFields(t, f.Newest)}
	}
	answers = frozen(t, key, answers)
	out := make([]archive.Found, len(answers))
	for i, a := range answers {
		out[i] = archive.Found{ProjectID: a.ProjectID, ID: a.ID, Source: eventOf(t, a.Source), Newest: eventOf(t, a.Newest)}
	}
	return slices.Clip(out)
}

// frozenTable freezes duck, the oracle's table for q under rules, within
// sameTable's rounding, and returns the golden's.
func frozenTable(t *testing.T, q string, rules archive.Rules, duck [][]string) [][]string {
	t.Helper()
	if rerun(t) {
		return duck
	}
	a := oracle(t, queryKey(t, q, rules), tableOf(t, tableAnswer{SQL: q, Rules: rulesText(rules)}, duck))
	want := rowsOf(t, a, duck)
	sameTable(t, duck, want)
	return want
}
