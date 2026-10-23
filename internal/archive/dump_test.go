package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// dumpQuery is the dump's SELECT of a log as DuckDB ran it before the dump
// moved to Go (dumpLog), kept as the parity tests' oracle: logSelect,
// strict, over every key of the log, every column's type spelled out.
func dumpQuery(path string) (string, error) {
	keys, err := logKeys(path)
	if err != nil {
		return "", err
	}
	return logSelect(path, columnsOf(keys), false), nil
}

// logKeys returns every key of a log's lines, for the oracle (dumpLog
// collects them as it reads). A line that is not a JSON object fails it,
// as DuckDB would.
func logKeys(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	defer f.Close()
	keys := map[string]bool{}
	dec := json.NewDecoder(bufio.NewReader(f))
	for {
		var m map[string]json.RawMessage
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			return keys, nil
		} else if err != nil {
			return nil, fmt.Errorf("archive: %s: %w", filepath.Base(path), err)
		}
		if m == nil {
			return nil, fmt.Errorf("archive: %s: a line is not an object", filepath.Base(path))
		}
		for k := range m {
			keys[k] = true
		}
	}
}

// logOf is evs as a log's content, one line each.
func logOf(t *testing.T, evs ...store.Event) string {
	t.Helper()
	var b strings.Builder
	for _, e := range evs {
		l, err := line(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(l)
	}
	return b.String()
}

// dumpView is evOf as a page view.
func dumpView(id, at string) store.Event {
	e := evOf(1, id, at)
	e.Family, e.EventName = store.FamilyViews, "$page_view"
	return e
}

// measureOf is evOf as a measure of value.
func measureOf(id, at string, value float64) store.Event {
	e := evOf(1, id, at)
	e.Family, e.EventName, e.Measure, e.SampleRate = store.FamilyMeasures, "$web_vitals", "lcp", 0.5
	e.Value = &value
	return e
}

// dumpCase is one log both dumps read. allowed names the ids whose
// column attr_n holds a JSON number, the parity's one allowed difference:
// the Go dump keeps its text as written (as native's live log read does),
// DuckDB gave its own form (1.50 → "1.5"). Ingest writes every VARCHAR
// as a string, so no real line has one.
type dumpCase struct {
	name, log string
	allowed   map[string]string // id → the Go dump's attr_n
}

// dumpCases are the logs of every dump test in maintain_test.go, and
// lines the ingest never writes that the decoder must still read as
// DuckDB did.
func dumpCases(t *testing.T) []dumpCase {
	view := dumpView("v", "2026-10-11T09:00:00Z")
	torn := logOf(t, evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z")) + `{"v":1,"id":"torn","project_id":1,"fam`
	const uuid = "ABCDEF01-2345-6789-ABCD-EF0123456789"
	cases := []dumpCase{
		{name: "closed days", log: logOf(t, evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z"))},
		{name: "family and ts order", log: logOf(t, evOf(1, "late", "2026-10-11T23:00:00Z"), view,
			evOf(1, "early", "2026-10-11T01:00:00Z"), evOf(1, "mid", "2026-10-11T12:00:00Z"))},
		{name: "duplicate ids", log: logOf(t, evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "a", "2026-10-11T10:00:00Z"))},
		{name: "torn last line", log: torn},
		{name: "one line", log: logOf(t, evOf(1, "a", "2026-10-11T10:00:00Z"))},
		{name: "types day 1", log: logOf(t,
			withAttrs(evOf(1, "a", "2026-09-01T10:00:00Z"), uuid, map[string]string{"d": "2026-10-12", "t": "12:30"}),
			withAttrs(evOf(1, "b", "2026-09-01T11:00:00Z"), uuid, map[string]string{"d": "2026-10-13", "t": "12:31"}))},
		{name: "types day 2", log: logOf(t, withAttrs(evOf(1, "c", "2026-09-02T10:00:00Z"), "hello", map[string]string{"d": "word", "t": "word"}))},
		{name: "keys equal but for case", log: logOf(t,
			withAttrs(evOf(1, "a", "2026-10-11T10:00:00Z"), "", map[string]string{"Plan": "A", "plan": "b"}),
			withAttrs(evOf(1, "b", "2026-10-11T11:00:00Z"), "", map[string]string{"plan": "c", "plan_1": "z"}))},
		{name: "any key", log: logOf(t, withAttrs(evOf(1, "a", "2026-10-11T10:00:00Z"), "", map[string]string{"a-b c": "1", `q"u'o`: "2",
			"x/y~z": "3", "a$.[0]": "4", "nul\x00k": "5", `nul\u0000k`: "6", "t\tb\nc\x01\x7f": "7"}))},
		{name: "merge copies", log: logOf(t, withAttrs(evOf(1, "a", "2026-09-30T10:00:00Z"), "", map[string]string{"copy": "1"}),
			withAttrs(evOf(1, "a", "2026-09-30T10:00:00Z"), "", map[string]string{"copy": "2"}))},
		{name: "families and measures", log: logOf(t, measureOf("m2", "2026-10-11T12:00:00Z", 2512.25), dumpView("v2", "2026-10-11T08:00:00Z"),
			evOf(1, "p", "2026-10-11T08:00:00Z"), measureOf("m1", "2026-10-11T07:00:00Z", -0.1), dumpView("v1", "2026-10-11T08:00:00Z"),
			dumpView("v3", "2026-10-11T08:00:00Z"))},
		{name: "hand-written", log: strings.Join([]string{
			`{"v":1,"family":"product","ts":"2026-10-11T10:00:00Z","attr_x":"first null id"}`,
			`{"v":1,"family":"product","ts":"2026-10-11T09:00:00Z","attr_x":"second null id"}`,
			`{"v":1,"id":null,"family":"views","attr_x":"third null id"}`,
			`{"v":1,"id":"no-family","ts":"2026-10-11T08:00:00Z"}`,
			`{"v":1,"id":"no-ts","family":"product","day":"2026-10-11"}`,
			`{"v":1,"id":"neither"}`,
			``,
			`{"v":1.0,"id":"casts","family":"product","ts":"2026-10-11 10:00:00.5","received_at":"2026-10-11T12:00:00.123456Z",` +
				`"display_width":"1280.5","display_height":-7,"consent":"1","value":"0.25","sample_rate":1e-3,"day":"2026-10-11"}`,
			`{"v":1,"id":"empty","family":"","ts":"2026-10-11T10:00:00Z","attr_x":"","user_id":""}`,
			`{"v":1,"id":"é ","family":"product","ts":"2026-10-11T10:00:00Z","attr_x":"é \"\\"}`,
			`{"v":1,"id":"rounded","family":"product","ts":"2026-10-11T10:00:00Z","display_width":"-2.5","consent":"1280.6"}`,
			``}, "\n")},
		{name: "a JSON number in a VARCHAR", log: strings.Join([]string{
			`{"v":1,"id":"n1","family":"product","ts":"2026-10-11T10:00:00Z","attr_n":1.50}`,
			`{"v":1,"id":"n2","family":"product","ts":"2026-10-11T10:00:01Z","attr_n":1e3}`,
			`{"v":1,"id":"n3","family":"product","ts":"2026-10-11T10:00:02Z","attr_n":true}`,
			`{"v":1,"id":"n4","family":"product","ts":"2026-10-11T10:00:03Z","attr_n":"1.50"}`,
			""}, "\n"),
			allowed: map[string]string{"n1": "1.50", "n2": "1e3"}},
	}
	return cases
}

// bigDumpLog is TestDumpKeepsAnIDsFirstLine's log, about 45 MB: three
// copies of 6,000 ids, each line padded with 2,400 bytes.
func bigDumpLog(t *testing.T) string {
	t.Helper()
	pad := strings.Repeat("x", 2400)
	var b strings.Builder
	for copy := 1; copy <= 3; copy++ {
		for i := 0; i < 6000; i++ {
			b.WriteString(logOf(t, withAttrs(evOf(1, fmt.Sprintf("id-%05d", i), "2026-10-11T10:00:00Z"), "",
				map[string]string{"copy": strconv.Itoa(copy), "pad": pad})))
		}
	}
	return b.String()
}

// duckDump writes src as out through the oracle, as the dump did before.
func (f fixture) duckDump(t *testing.T, src, out string) error {
	t.Helper()
	q, err := dumpQuery(src)
	if err != nil {
		return err
	}
	return f.eng.Exec(context.Background(), "COPY ("+q+") TO "+quote(out)+" (FORMAT parquet, COMPRESSION zstd)")
}

// TestGoDumpMatchesDuckDB: for every log of the dump tests, and lines the
// ingest never writes, the Go dump's file equals the one DuckDB's COPY of
// dumpQuery wrote, read through DuckDB: the same columns of the same types
// in the same order, and the same rows. DuckDB left the order of rows
// equal in (family, ts) unspecified, so the rows are compared as a
// multiset and the Go file's own order is checked: (family, ts)
// non-decreasing, NULLs last. The one allowed difference is dumpCase's:
// a JSON number in a VARCHAR column keeps its text.
func TestGoDumpMatchesDuckDB(t *testing.T) {
	f := newFixtureWith(t, 4, "512MB")
	cases := append(dumpCases(t), dumpCase{name: "45 MB of repeated ids", log: bigDumpLog(t)})
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := f.project1(fmt.Sprintf("2026-10-%02d.parquet.dumping", i+1))
			f.writeFile(t, filepath.Base(src), c.log)
			if err := cutTornLine(src); err != nil { // as claim does
				t.Fatal(err)
			}
			duck, goFile := f.project1(fmt.Sprintf("duck-%d.parquet", i)), f.project1(fmt.Sprintf("go-%d.parquet", i))
			if err := f.duckDump(t, src, duck); err != nil {
				t.Fatal(err)
			}
			if err := dumpLog(src, goFile); err != nil {
				t.Fatal(err)
			}
			if got, want := f.describe(t, goFile), f.describe(t, duck); !eq(got, want) {
				t.Fatalf("Go file's columns\n%v\nwant DuckDB's\n%v", got, want)
			}
			rel := func(p string) string { return "read_parquet(" + quote(p) + ")" }
			var allowed []string
			for id := range c.allowed {
				allowed = append(allowed, quote(id))
			}
			if len(allowed) == 0 {
				f.sameRows(t, rel(goFile), rel(duck))
			} else {
				in := "id IN (" + strings.Join(allowed, ", ") + ")"
				f.sameRows(t, "(SELECT * FROM "+rel(goFile)+" WHERE NOT "+in+")", "(SELECT * FROM "+rel(duck)+" WHERE NOT "+in+")")
				f.sameRows(t, "(SELECT * EXCLUDE (attr_n) FROM "+rel(goFile)+" WHERE "+in+")",
					"(SELECT * EXCLUDE (attr_n) FROM "+rel(duck)+" WHERE "+in+")")
				for id, want := range c.allowed {
					got := f.values(t, "SELECT attr_n FROM "+rel(goFile)+" WHERE id = "+quote(id))
					if !eq(got, []string{"[" + want + "]"}) {
						t.Errorf("%s: attr_n = %v, want the JSON text %s", id, got, want)
					}
					if duck := f.values(t, "SELECT attr_n FROM "+rel(duck)+" WHERE id = "+quote(id)); eq(duck, got) {
						t.Errorf("%s: DuckDB's attr_n is %v too: the allowance is not needed", id, duck)
					}
				}
			}
			f.orderedByFamilyAndTS(t, goFile)
		})
	}
}

// describe is DuckDB's DESCRIBE of a Parquet file: name, type, nullable.
func (f fixture) describe(t *testing.T, path string) []string {
	t.Helper()
	return f.values(t, `SELECT column_name, column_type, "null" FROM (DESCRIBE SELECT * FROM read_parquet(`+quote(path)+"))")
}

// sameRows fails unless relations a and b hold the same rows, each as
// many times.
func (f fixture) sameRows(t *testing.T, a, b string) {
	t.Helper()
	q := "SELECT count(*) FROM ((SELECT * FROM " + a + " EXCEPT ALL SELECT * FROM " + b + ") UNION ALL (SELECT * FROM " + b + " EXCEPT ALL SELECT * FROM " + a + "))"
	if got := f.values(t, q); !eq(got, []string{"[0]"}) {
		t.Errorf("rows differing between %s and %s: %v\n%s\n%s", a, b, got,
			f.values(t, "SELECT * FROM "+a+" EXCEPT ALL SELECT * FROM "+b), f.values(t, "SELECT * FROM "+b+" EXCEPT ALL SELECT * FROM "+a))
	}
	if na, nb := f.values(t, "SELECT count(*) FROM "+a), f.values(t, "SELECT count(*) FROM "+b); !eq(na, nb) {
		t.Errorf("%s rows, want %s", na, nb)
	}
}

// orderedByFamilyAndTS fails unless the file's rows, in file order, are
// non-decreasing in family, then ts, NULLs last.
func (f fixture) orderedByFamilyAndTS(t *testing.T, path string) {
	t.Helper()
	res, err := f.eng.Query(context.Background(), "SELECT family, epoch_us(ts) FROM read_parquet("+quote(path)+", file_row_number=true) ORDER BY file_row_number", 0)
	if err != nil {
		t.Fatal(err)
	}
	key := func(r []any) (string, int64, bool, bool) {
		fam, famOK := r[0].(string)
		var ts int64 = math.MaxInt64
		tsOK := r[1] != nil
		if tsOK {
			fmt.Sscan(fmt.Sprint(r[1]), &ts)
		}
		return fam, ts, famOK, tsOK
	}
	for i := 1; i < len(res.Rows); i++ {
		pf, pt, pfOK, ptOK := key(res.Rows[i-1])
		nf, nt, nfOK, ntOK := key(res.Rows[i])
		famCmp := 0
		switch {
		case pfOK && nfOK:
			famCmp = strings.Compare(pf, nf)
		case pfOK:
			famCmp = -1
		case nfOK:
			famCmp = 1
		}
		if famCmp > 0 || (famCmp == 0 && ((!ptOK && ntOK) || (ptOK && ntOK && pt > nt))) {
			t.Fatalf("row %d (%v) after row %d (%v): not in (family, ts) order", i, res.Rows[i], i-1, res.Rows[i-1])
		}
	}
}

// TestDumpFailsWhereDuckDBDid: a line that is not a JSON object, or a value
// its column cannot hold, fails both dumps, and the Go dump leaves no
// file and no .tmp.
func TestDumpFailsWhereDuckDBDid(t *testing.T) {
	f := newFixture(t)
	for i, log := range []string{
		"[1]\n",
		`{"id":"a"}` + "\n" + "garbage\n",
		`{"id":"a","ts":"yesterday"}` + "\n",
		`{"id":"a","display_width":"wide"}` + "\n",
		`{"id":"a","day":"10.11.2026"}` + "\n",
		`{"id":"a","value":"x"}` + "\n",
		`{"id":"a","consent":true}` + "\n",
	} {
		src := f.project1(fmt.Sprintf("2026-10-%02d.parquet.dumping", i+1))
		f.writeFile(t, filepath.Base(src), log)
		out := strings.TrimSuffix(src, ".dumping")
		if err := f.duckDump(t, src, f.project1("duck.parquet")); err == nil {
			t.Errorf("%q: DuckDB's dump went through", log)
		}
		if err := dumpLog(src, out); err == nil {
			t.Errorf("%q: the Go dump went through", log)
		}
		if exists(out) || exists(out+".tmp") {
			t.Errorf("%q: the failed dump left a file", log)
		}
	}
}

// TestDumpKeepsTiesInFileOrder: rows equal in family and ts keep the order
// of their lines (a stable sort), and a row group is cut where the family
// or the day changes.
func TestDumpKeepsTiesInFileOrder(t *testing.T) {
	f := newFixture(t)
	late := evOf(1, "p-late-day", "2026-10-11T10:00:00Z")
	late.TS = ts("2026-10-12T00:30:00Z") // a late event of the next day's file name: day follows ts
	f.writeFile(t, "2026-10-11.parquet.dumping", logOf(t,
		dumpView("v-b", "2026-10-11T10:00:00Z"), evOf(1, "p-z", "2026-10-11T10:00:00Z"), dumpView("v-a", "2026-10-11T10:00:00Z"),
		evOf(1, "p-a", "2026-10-11T10:00:00Z"), late, evOf(1, "p-m", "2026-10-11T10:00:00Z")))
	out := f.project1("2026-10-11.parquet")
	if err := dumpLog(out+".dumping", out); err != nil {
		t.Fatal(err)
	}
	got := f.values(t, "SELECT id FROM read_parquet("+quote(out)+", file_row_number=true) ORDER BY file_row_number")
	if want := []string{"[p-z]", "[p-a]", "[p-m]", "[p-late-day]", "[v-b]", "[v-a]"}; !eq(got, want) {
		t.Fatalf("rows = %v, want %v: ties in file order", got, want)
	}
	if got := groupRows(t, out); fmt.Sprint(got) != "[3 1 2]" {
		t.Fatalf("row groups = %v, want [3 1 2]: cut at the day, then at the family", got)
	}
}

// TestDumpSendsTheEngineNothing: claiming and dumping a log, the 45 MB one
// included, sends the engine no request, so a pass with only dumps to do
// starts no child for them.
func TestDumpSendsTheEngineNothing(t *testing.T) {
	f := newFixture(t)
	eng := &countingEngine{Engine: f.eng}
	a := New(f.log, eng, f.a.logger)
	f.writeFile(t, "2026-10-11.1.closed", bigDumpLog(t))
	dir := filepath.Join(f.log.Dir(), "1")
	if err := a.claim(dir, "2026-10-11.1.closed", "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := a.dump(dir, "2026-10-11.parquet.dumping"); err != nil {
		t.Fatal(err)
	}
	t.Logf("dumped 45 MB in %s", time.Since(start).Round(time.Millisecond))
	if eng.requests != 0 {
		t.Fatalf("%d engine requests from a dump, want none", eng.requests)
	}
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet"}) {
		t.Fatalf("files = %v, want the day file alone", got)
	}
	if st, err := os.Stat(f.project1("2026-10-11.parquet")); err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("day file: %v, %v; want 0640", st, err)
	}
	if n, ids := f.rows(t, "2026-10-11.parquet"); n != 6000 || ids != 6000 {
		t.Fatalf("day file: %d rows, %d ids; want 6000 and 6000", n, ids)
	}
}
