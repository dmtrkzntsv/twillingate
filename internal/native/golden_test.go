package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// -update-golden rewrites testdata/*.golden.json from native's answers,
// only for a deliberate change a review reads in the diff. The goldens
// were frozen from the old query engine's answers (step 6a); they are the
// only reference now.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from native's answers")

// parityOf maps a TestAnswerManyEqualsAnswer subtest that reruns a parity
// test to that test's name: the rerun reads the parity test's goldens.
var parityOf sync.Map // "TestAnswerManyEqualsAnswer/parity/<name>" → "Test…"

// goldenSet is the Set t's answers are kept in, and t's name as the keys
// spell it: the top-level test's file, or under the parity rerun the
// parity test's, read only.
func goldenSet(t testing.TB) (*golden.Set, string) {
	t.Helper()
	name := t.Name()
	var found *golden.Set
	parityOf.Range(func(k, v any) bool {
		prefix := k.(string)
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			name = v.(string) + strings.TrimPrefix(name, prefix)
			found = golden.Open(t, filepath.Join("testdata", v.(string)+".golden.json"), false)
			return false
		}
		return true
	})
	if found != nil {
		return found, name
	}
	return golden.For(t, *updateGolden), name
}

// expect returns the golden kept under key, keeping got (native's answer)
// there first with -update-golden. The caller compares got with it. The
// parity rerun with -update-golden keeps nothing and returns got.
func expect[T any](t testing.TB, key string, got T) T {
	t.Helper()
	if rerun(t) && *updateGolden {
		return got
	}
	s, _ := goldenSet(t)
	return golden.Get(t, s, key, got)
}

// frozen is expect, failing t unless got equals the golden exactly.
func frozen[T any](t testing.TB, key string, got T) T {
	t.Helper()
	want := expect(t, key, got)
	if d := golden.Diff(got, want); d != "" {
		t.Fatalf("%s: the answer differs from its golden at %s", key, d)
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
	_, name := goldenSet(t)
	if _, sub, ok := strings.Cut(name, "/"); ok {
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
// are kept (rowsDigest): native's rows are digested and compared with it.
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

// rowsOf is a's rows as cells, NULL as nullCell; for a digest, got (the
// native rows asked about) once their digest is a's, failing t otherwise.
func rowsOf(t testing.TB, a tableAnswer, got [][]string) [][]string {
	t.Helper()
	if a.SHA256 != "" {
		if d := rowsDigest(t, got); d != a.SHA256 || len(got) != a.RowCount {
			t.Fatalf("%s: native's %d rows digest to %s, the golden's %d to %s", a.SQL, len(got), d, a.RowCount, a.SHA256)
		}
		return got
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

// goldenRows returns the golden columns and rows of q under rules, keeping
// cols and rows (native's, sorted by their first cell) with -update-golden,
// and fails t unless the golden's columns are cols. The caller compares
// rows with the golden's.
func goldenRows(t *testing.T, q string, rules archive.Rules, cols []string, rows [][]string) ([]string, [][]string) {
	t.Helper()
	a := expect(t, queryKey(t, q, rules), tableOf(t, tableAnswer{SQL: q, Rules: rulesText(rules), Columns: cols}, rows))
	if !slices.Equal(a.Columns, cols) {
		t.Fatalf("%s: columns %q, the golden's %q", q, cols, a.Columns)
	}
	return a.Columns, rowsOf(t, a, rows)
}

// rerun says whether t is TestAnswerManyEqualsAnswer running a parity
// test again, to record its native calls: it compares them with the
// parity test's goldens (goldenSet) and keeps none.
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

// frozenFound returns the golden answer to ls (sorted), keeping got
// (native's) there first with -update-golden, keyed by what was asked.
func frozenFound(t *testing.T, days int, rules archive.Rules, ls []archive.Lookup, got []archive.Found) []archive.Found {
	t.Helper()
	asked := make([]string, len(ls))
	for i, l := range ls {
		asked[i] = fmt.Sprintf("%d/%q %s:%q install %q connection %q at %s", l.ProjectID, l.ID, l.ActorKind, l.ActorID,
			l.InstallID, l.ConnectionID, l.At.UTC().Format("2006-01-02T15:04:05.999999999Z"))
	}
	key := fmt.Sprintf("%d days: %s", days, strings.Join(asked, "; "))
	if r := rulesText(rules); r != "" {
		key = fmt.Sprintf("%d days, rules %s: %s", days, r, strings.Join(asked, "; "))
	}
	answers := make([]foundAnswer, len(got))
	for i, f := range got {
		answers[i] = foundAnswer{ProjectID: f.ProjectID, ID: f.ID, Source: setFields(t, f.Source), Newest: setFields(t, f.Newest)}
	}
	answers = expect(t, key, answers)
	out := make([]archive.Found, len(answers))
	for i, a := range answers {
		out[i] = archive.Found{ProjectID: a.ProjectID, ID: a.ID, Source: eventOf(t, a.Source), Newest: eventOf(t, a.Newest)}
	}
	return slices.Clip(out)
}

// goldenTable is the golden table of sql (its ranges spelled as %[1]s and
// %[2]s, from and to) under engineRules, keeping got (native's rows, as
// cells) there first with -update-golden. The caller compares got with it
// (sameTable: numbers within a rounding error).
func goldenTable(t *testing.T, sql, from, to string, got [][]string) [][]string {
	t.Helper()
	return goldenTableWith(t, engineRules, sql, from, to, got)
}

// goldenTableWith is goldenTable under rules. With -update-golden, a
// golden native's rows are alike (tablesAlike) is kept as it was.
func goldenTableWith(t *testing.T, rules archive.Rules, sql, from, to string, got [][]string) [][]string {
	t.Helper()
	q := fmt.Sprintf(sql, from, to)
	key := queryKey(t, q, rules)
	a := tableOf(t, tableAnswer{SQL: q, Rules: rulesText(rules)}, got)
	if *updateGolden && !rerun(t) {
		s, _ := goldenSet(t)
		if prev, ok := golden.Previous[tableAnswer](t, s, key); ok && prev.SQL == a.SQL && prev.Rules == a.Rules {
			switch {
			case prev.SHA256 != "" || a.SHA256 != "":
				if prev.SHA256 == a.SHA256 && prev.RowCount == a.RowCount {
					a = prev
				}
			case tablesAlike(rowsOf(t, prev, nil), got):
				a = prev
			}
		}
	}
	return rowsOf(t, expect(t, key, a), got)
}

// tablesAlike says whether a and b hold the same rows, cells equal as
// sameCell has them, in order or once both are sorted (a test that reads
// rows in no order sorts them before it compares).
func tablesAlike(a, b [][]string) bool {
	alike := func(a, b [][]string) bool {
		return slices.EqualFunc(a, b, func(x, y []string) bool { return slices.EqualFunc(x, y, sameCell) })
	}
	if len(a) != len(b) || alike(a, b) {
		return len(a) == len(b)
	}
	a, b = slices.Clone(a), slices.Clone(b)
	sortRows(a)
	sortRows(b)
	return alike(a, b)
}

// goldenTables is the golden table of a case asked as one query of
// several SQLs, its range from and to: got's rows are the first SQL's,
// then a row of each other one (a stat's cells, or a breakdown's Other
// row); with prev, the first SQL over the range before of the same length
// too, its first row's cells appended to got's first row.
func goldenTables(t *testing.T, sqls []string, from, to string, prev bool, got [][]string) [][]string {
	t.Helper()
	w := -1 // the cells of got's first row that are the range's
	if prev && len(got) > 0 {
		w = len(got[0]) / 2
	}
	// The first SQL's rows, then one row of each other (a stat's cells,
	// or a breakdown's Other row).
	first := len(got) - (len(sqls) - 1)
	part := func(i int) [][]string { // the rows of got for sqls[i]
		lo, hi := 0, first
		if i > 0 {
			lo, hi = first+i-1, first+i
		}
		if lo < 0 || hi > len(got) {
			return nil
		}
		rows := got[lo:hi]
		if w >= 0 && len(rows) > 0 && i == 0 {
			rows = append([][]string{rows[0][:w]}, rows[1:]...)
		}
		return rows
	}
	var want [][]string
	for i, sql := range sqls {
		want = append(want, goldenTable(t, sql, from, to, part(i))...)
	}
	if prev {
		f, l := date(t, from), date(t, to)
		days := int(l.Time().Sub(f.Time()).Hours()/24) + 1
		pf := f.Time().AddDate(0, 0, -days).Format(time.DateOnly)
		pt := f.Time().AddDate(0, 0, -1).Format(time.DateOnly)
		var before [][]string
		if w >= 0 {
			before = [][]string{got[0][w:]}
		}
		if len(want) == 0 {
			t.Fatal("no rows to append the range before to")
		}
		want[0] = append(slices.Clone(want[0]), goldenTable(t, sqls[0], pf, pt, before)[0]...)
	}
	return want
}

// answerRows is e's answer to q for project over from..to under rules,
// its rows.
func answerRows(t *testing.T, e *Engine, project int64, q Query, from, to string, rules archive.Rules) [][]string {
	t.Helper()
	res, err := e.Answer(context.Background(), q, Params{Project: project, From: date(t, from), To: date(t, to)}, rules)
	if err != nil {
		t.Fatal(err)
	}
	return res.Rows
}

// goldenAnswer is goldenTableWith of sql over from..to, got being e's
// answer to q for project, which it fails t unless it matches (sameTable).
func goldenAnswer(t *testing.T, e *Engine, project int64, q Query, rules archive.Rules, sql, from, to string) [][]string {
	t.Helper()
	got := answerRows(t, e, project, q, from, to, rules)
	want := goldenTableWith(t, rules, sql, from, to, got)
	sameTable(t, got, want)
	return want
}

// eventsColumns is the columns SELECT * FROM events gave under project's
// rules, as the goldens keep them: id, the fixed columns, then every other
// column in the order a union by name first sees it, names matched
// regardless of case (the open logs, by project, then the Parquet files,
// each file's in its order), then each rename target no file holds, by
// project.
func eventsColumns(t *testing.T, dir string, project int64, rules archive.Rules) []string {
	t.Helper()
	out := []string{"id"}
	seen := map[string]bool{"id": true, "v": true}
	add := func(names []string) {
		for _, n := range names {
			if !seen[strings.ToLower(n)] {
				seen[strings.ToLower(n)] = true
				out = append(out, n)
			}
		}
	}
	add(logColumnNames(nil))
	all, err := archive.ListFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := slices.Sorted(maps.Keys(all))
	for _, log := range []bool{true, false} {
		for _, id := range ids {
			paths := all[id].Parquet
			if log {
				paths = all[id].Logs
			}
			for _, p := range paths {
				names, err := fileColumns(p, log)
				if err != nil {
					t.Fatal(err)
				}
				add(names)
			}
		}
	}
	have, err := unionColumns(dir)
	if err != nil {
		t.Fatal(err)
	}
	projects := map[int64]bool{project: true}
	for _, r := range rules.Attributes {
		projects[r.ProjectID] = true
	}
	for _, p := range slices.Sorted(maps.Keys(projects)) {
		for _, c := range compileRules(p, rules, have).attrs {
			if c.from != nil {
				add([]string{c.name})
			}
		}
	}
	return out
}
