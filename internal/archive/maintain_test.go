package archive

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// fixture is a Log and the Archive over it. The tests read the files back
// in Go (pqread_test.go).
type fixture struct {
	log *Log
	a   *Archive
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	l, err := OpenLog(filepath.Join(t.TempDir(), "raw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return fixture{log: l, a: New(l, slog.Default())}
}

// evOf is log_test.go's ev with the project first and ReceivedAt set.
func evOf(project int64, id, at string) store.Event {
	return store.Event{ID: id, ProjectID: project, Family: store.FamilyProduct, EventName: "x", TS: ts(at), ReceivedAt: ts(at)}
}

func (f fixture) write(t *testing.T, evs ...store.Event) {
	t.Helper()
	if err := f.log.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) maintain(t *testing.T, today string) {
	t.Helper()
	d, err := civil.Parse(today)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Maintain(context.Background(), d, 1, 400); err != nil {
		t.Fatal(err)
	}
}

// files lists project 1's events files and logs: the derived ones
// (derive.go) are derived's.
func (f fixture) files(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.log.Dir(), "1"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); n != sessionsDir && n != identitiesFile && n != aliasesFile {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// derived lists project 1's derived files, sessions/ ones prefixed.
func (f fixture) derived(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, n := range []string{aliasesFile, identitiesFile} {
		if exists(f.project1(n)) {
			names = append(names, n)
		}
	}
	entries, err := os.ReadDir(f.project1(sessionsDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		names = append(names, sessionsDir+"/"+e.Name())
	}
	return names
}

// rows counts the rows and distinct ids of a Parquet file of project 1.
func (f fixture) rows(t *testing.T, name string) (int, int) {
	t.Helper()
	ids := map[any]bool{}
	p := readTable(t, f.project1(name), "id")
	for _, r := range p.pick(t, "id") {
		if r[0] != nil {
			ids[r[0]] = true
		}
	}
	return len(p.rows), len(ids)
}

func eq(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// TestMaintainLogsWhatThePassDid: a pass that succeeds says so, with what
// it did, so the journal shows it ran (it logged only failures before).
func TestMaintainLogsWhatThePassDid(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-29T10:00:00Z"), evOf(1, "b", "2026-09-30T10:00:00Z"), evOf(2, "c", "2026-10-02T10:00:00Z"))
	var buf strings.Builder
	a := New(f.log, slog.New(slog.NewTextHandler(&buf, nil)))
	today, _ := civil.Parse("2026-10-02")
	if err := a.Maintain(context.Background(), today, 1, 400); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	for _, want := range []string{"archive: pass", "projects=2", "dumped=2", "merged=1", "expired_months=0", "failed_projects=0"} {
		if !strings.Contains(line, want) {
			t.Errorf("log = %q; missing %s", line, want)
		}
	}
}

// TestMaintainNeverTouchesAnEngine: the daily pass runs in Go in the
// calling process (the Archive holds no engine). A pass dumps, merges,
// derives and deletes an expired month, and its files read back in Go.
func TestMaintainNeverTouchesAnEngine(t *testing.T) {
	f := newFixture(t)
	pass := func(today string, retention int) {
		t.Helper()
		d, _ := civil.Parse(today)
		if err := f.a.Maintain(context.Background(), d, 1, retention); err != nil {
			t.Fatal(err)
		}
	}
	pass("2026-10-12", 400) // no project at all
	f.write(t, named(pageView("old", "o", "2026-06-10T10:00:00Z", "/"), "u0", "", "", ""),
		named(pageView("a", "a", "2026-09-29T10:00:00Z", "/"), "u1", "Ada", "", ""),
		evOf(1, "b", "2026-10-11T10:00:00Z"), evOf(1, "c", "2026-10-13T10:00:00Z"))
	pass("2026-10-13", 100) // dumps, merges June and September, deletes June, derives
	pass("2026-10-13", 100)
	if got := f.files(t); !eq(got, []string{"2026-09.parquet", "2026-10-11.parquet", "2026-10-13.log"}) {
		t.Fatalf("files = %v", got)
	}
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-09.parquet", "sessions/2026-10-11.parquet"}) {
		t.Fatalf("derived = %v", got)
	}
	if got := texts(readTable(t, f.project1(identitiesFile), "id", "name", "views").pick(t, "id", "name", "views")); !eq(got, []string{"[u1 Ada 1]"}) {
		t.Fatalf("identities = %v", got)
	}
}

// TestMaintainWritesFilesOnlyTheServiceReads: every Parquet file the pass
// writes (day, month and derived files) is 0640 like the logs, whatever
// the process's umask.
func TestMaintainWritesFilesOnlyTheServiceReads(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-29T10:00:00Z"), evOf(1, "b", "2026-10-01T10:00:00Z"))
	f.maintain(t, "2026-10-02")
	n := 0
	err := filepath.WalkDir(filepath.Join(f.log.Dir(), "1"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".parquet") {
			return err
		}
		n++
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o640 {
			t.Errorf("%s: mode %v, %v; want 0640", p, st.Mode().Perm(), err)
		}
		return nil
	})
	if err != nil || n < 2 {
		t.Fatalf("walked %d Parquet files, %v; want the month file and derived files", n, err)
	}
}

func TestMaintainDumpsClosedDaysOnly(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z"), evOf(1, "c", "2026-10-12T09:00:00Z"))
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet", "2026-10-12.log"}) {
		t.Fatalf("files = %v, want yesterday dumped and today's log left open", got)
	}
	if n, ids := f.rows(t, "2026-10-11.parquet"); n != 2 || ids != 2 {
		t.Fatalf("day file: %d rows, %d ids; want 2 and 2", n, ids)
	}
}

// TestDumpOrdersByFamilyAndTS: the day file's rows are in (family, ts)
// order, whatever order the log holds them in.
func TestDumpOrdersByFamilyAndTS(t *testing.T) {
	f := newFixture(t)
	view := evOf(1, "v", "2026-10-11T09:00:00Z")
	view.Family = store.FamilyViews
	f.write(t, evOf(1, "late", "2026-10-11T23:00:00Z"), view, evOf(1, "early", "2026-10-11T01:00:00Z"), evOf(1, "mid", "2026-10-11T12:00:00Z"))
	f.maintain(t, "2026-10-12")
	got := texts(readTable(t, f.project1("2026-10-11.parquet"), "id").pick(t, "id"))
	if want := []string{"[early]", "[mid]", "[late]", "[v]"}; !eq(got, want) {
		t.Fatalf("rows = %v, want %v (product before views, each by ts)", got, want)
	}
}

func TestDumpDropsDuplicateIDs(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"))
	f.log.now = func() time.Time { return time.Now().Add(recentIDs + time.Minute) } // past the recent-ids window
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"))
	f.maintain(t, "2026-10-12")
	if n, ids := f.rows(t, "2026-10-11.parquet"); n != 1 || ids != 1 {
		t.Fatalf("day file: %d rows, %d ids; want the duplicate dropped", n, ids)
	}
}

// TestDumpCutsATornLastLine: a log rotated after a failed cut-back can keep
// a torn last line, which read_ndjson would fail on every pass.
func TestDumpCutsATornLastLine(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z"))
	if _, err := f.log.Rotate(1, "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	closed := filepath.Join(f.log.Dir(), "1", "2026-10-11.1.closed")
	fh, err := os.OpenFile(closed, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(`{"v":1,"id":"torn","project_id":1,"fam`); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet"}) {
		t.Fatalf("files = %v", got)
	}
	if n, ids := f.rows(t, "2026-10-11.parquet"); n != 2 || ids != 2 {
		t.Fatalf("day file: %d rows, %d ids; want the complete lines only", n, ids)
	}
}

// TestLateLogBecomesASegmentAndMerges is Review Focus 2.
func TestLateLogBecomesASegmentAndMerges(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-30T10:00:00Z"))
	f.maintain(t, "2026-10-01")
	f.write(t, evOf(1, "late", "2026-09-30T23:00:00Z"))
	f.maintain(t, "2026-10-01") // the same day's pass again (a restart): the late log becomes a segment
	if got := f.files(t); !eq(got, []string{"2026-09-30.1.parquet", "2026-09-30.parquet"}) {
		t.Fatalf("files = %v, want the day file and a segment", got)
	}
	f.maintain(t, "2026-10-02") // September's last day + 1 late day = 10-01 < 10-02: final
	if got := f.files(t); !eq(got, []string{"2026-09.parquet"}) {
		t.Fatalf("files = %v, want the month file alone", got)
	}
	if n, ids := f.rows(t, "2026-09.parquet"); n != 2 || ids != 2 {
		t.Fatalf("month file: %d rows, %d ids; want 2 and 2", n, ids)
	}
}

func TestMonthWaitsForTheLateWindow(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-30T10:00:00Z"))
	f.maintain(t, "2026-10-01")
	if got := f.files(t); !eq(got, []string{"2026-09-30.parquet"}) {
		t.Fatalf("files = %v: September merged while its last day could still take late events", got)
	}
}

// TestMergeIncludesAnExistingMonthFile is Review Focus 3: a crash between
// the merge's rename and the deletes leaves the month file and its inputs;
// the next merge reads both and keeps each id once.
func TestMergeIncludesAnExistingMonthFile(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-01T10:00:00Z"), evOf(1, "b", "2026-09-02T10:00:00Z"))
	f.maintain(t, "2026-09-03")
	dir := filepath.Join(f.log.Dir(), "1")
	// Simulate the crash: a month file holding a, b, with the day files still there.
	month := readTable(t, filepath.Join(dir, "2026-09-01.parquet"))
	month.rows = append(month.rows, readTable(t, filepath.Join(dir, "2026-09-02.parquet")).rows...) // the same columns
	writeTable(t, filepath.Join(dir, "2026-09.parquet"), month)
	f.maintain(t, "2026-10-05")
	if got := f.files(t); !eq(got, []string{"2026-09.parquet"}) {
		t.Fatalf("files = %v", got)
	}
	if n, ids := f.rows(t, "2026-09.parquet"); n != 2 || ids != 2 {
		t.Fatalf("month file: %d rows, %d ids; want 2 and 2", n, ids)
	}
}

func TestRetentionDeletesWholeOldMonths(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "old", "2025-08-15T10:00:00Z"), evOf(1, "kept", "2025-09-15T10:00:00Z"))
	f.maintain(t, "2025-10-05") // both months merge
	d, _ := civil.Parse("2026-10-05")
	// 2026-10-05 - 400 days = 2025-08-31: August 2025 is entirely older, September is not.
	if err := f.a.Maintain(context.Background(), d, 1, 400); err != nil {
		t.Fatal(err)
	}
	if got := f.files(t); !eq(got, []string{"2025-09.parquet"}) {
		t.Fatalf("files = %v, want August deleted and September kept", got)
	}
}

func TestRemoveProjectDeletesItsDirectory(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-12T10:00:00Z"), evOf(2, "b", "2026-10-12T10:00:00Z"))
	if err := f.a.RemoveProject(1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.log.Dir(), "1")); !os.IsNotExist(err) {
		t.Fatalf("project 1's directory: %v, want it gone", err)
	}
	if _, err := os.Stat(filepath.Join(f.log.Dir(), "2", "2026-10-12.log")); err != nil {
		t.Fatalf("project 2's log: %v", err)
	}
	f.write(t, evOf(1, "c", "2026-10-12T11:00:00Z")) // an event queued before the purge must not fail the pipeline
}

func TestProjectsListsTheNumericDirectories(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(7, "a", "2026-10-12T10:00:00Z"), evOf(2, "b", "2026-10-12T10:00:00Z"))
	if err := os.WriteFile(filepath.Join(f.log.Dir(), "9"), nil, 0o640); err != nil { // a file, not a directory
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.log.Dir(), "scratch"), 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := f.a.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 7 {
		t.Fatalf("Projects = %v, want [2 7]", got)
	}
}

// project1 is the path of a file in project 1's directory.
func (f fixture) project1(name string) string { return filepath.Join(f.log.Dir(), "1", name) }

func (f fixture) writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(f.log.Dir(), "1"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.project1(name), []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

// TestInterruptedDumpNeverRepeats: a dump stopped after its target was
// written leaves <target>.dumping; the next pass removes it and makes no
// segment (an exact copy would double the day until its month merges).
func TestInterruptedDumpNeverRepeats(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"))
	f.maintain(t, "2026-10-12")
	b, err := line(evOf(1, "a", "2026-10-11T10:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	f.writeFile(t, "2026-10-11.parquet.dumping", string(b))
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet"}) {
		t.Fatalf("files = %v, want the pending log removed and no segment", got)
	}
	if n, ids := f.rows(t, "2026-10-11.parquet"); n != 1 || ids != 1 {
		t.Fatalf("day file: %d rows, %d ids; want 1 and 1", n, ids)
	}
}

// TestPendingDumpIsDumpedIntoItsTarget: a claimed log whose target was not
// written is dumped into exactly that target, and a closed log of the same
// day takes the next free segment around it.
func TestPendingDumpIsDumpedIntoItsTarget(t *testing.T) {
	f := newFixture(t)
	b, err := line(evOf(1, "a", "2026-10-11T10:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	f.writeFile(t, "2026-10-11.parquet.dumping", string(b))
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet"}) {
		t.Fatalf("files = %v, want the pending log dumped into the day file", got)
	}
	if n, ids := f.rows(t, "2026-10-11.parquet"); n != 1 || ids != 1 {
		t.Fatalf("day file: %d rows, %d ids; want 1 and 1", n, ids)
	}
	f.writeFile(t, "2026-10-13.parquet.dumping", string(b))
	b2, _ := line(evOf(1, "b", "2026-10-13T10:00:00Z"))
	f.writeFile(t, "2026-10-13.1.closed", string(b2))
	f.maintain(t, "2026-10-14")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet", "2026-10-13.1.parquet", "2026-10-13.parquet"}) {
		t.Fatalf("files = %v, want the closed log in the next free segment", got)
	}
}

// TestOneFailingDumpStopsNothingElse: a log the dump cannot read stays pending
// and holds its month back; the project's other dumps and its retention
// still run, and the error is returned.
func TestOneFailingDumpStopsNothingElse(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "old", "2025-08-15T10:00:00Z"), evOf(1, "a", "2026-08-30T10:00:00Z"), evOf(1, "c", "2026-10-11T10:00:00Z"))
	f.writeFile(t, "2026-08-31.1.closed", "[1]\n") // valid JSON, not an object
	d, _ := civil.Parse("2026-10-12")
	if err := f.a.maintainProject(context.Background(), 1, d, 1, 400, &passStats{}); err == nil {
		t.Fatal("maintainProject returned nil with a log it cannot read")
	}
	want := []string{"2026-08-30.parquet", "2026-08-31.parquet.dumping", "2026-10-11.parquet"}
	if got := f.files(t); !eq(got, want) {
		t.Fatalf("files = %v, want %v: the good days dumped, August 2025 deleted, August 2026 not merged", got, want)
	}
}

func withAttrs(e store.Event, user string, attrs map[string]string) store.Event {
	e.UserID, e.Attributes = user, attrs
	return e
}

// TestMonthMergesWhenDaysDisagreeOnTypes: a value is read back exactly as
// written, whatever it looks like, and days whose values would infer
// different types (UUID, DATE, TIME against plain words) merge.
func TestMonthMergesWhenDaysDisagreeOnTypes(t *testing.T) {
	f := newFixture(t)
	const id = "ABCDEF01-2345-6789-ABCD-EF0123456789"
	f.write(t,
		withAttrs(evOf(1, "a", "2026-09-01T10:00:00Z"), id, map[string]string{"d": "2026-10-12", "t": "12:30"}),
		withAttrs(evOf(1, "b", "2026-09-01T11:00:00Z"), id, map[string]string{"d": "2026-10-13", "t": "12:31"}),
		withAttrs(evOf(1, "c", "2026-09-02T10:00:00Z"), "hello", map[string]string{"d": "word", "t": "word"}))
	f.maintain(t, "2026-09-03")
	f.maintain(t, "2026-10-05")
	if got := f.files(t); !eq(got, []string{"2026-09.parquet"}) {
		t.Fatalf("files = %v, want the month merged", got)
	}
	month := f.project1("2026-09.parquet")
	p := readTable(t, month)
	var got []string
	for _, r := range p.pick(t, "id", "user_id", "attr_d", "attr_t") {
		got = append(got, fmt.Sprint(append(r, p.types[p.col(t, "user_id")], p.types[p.col(t, "attr_d")], p.types[p.col(t, "attr_t")])))
	}
	slices.Sort(got)
	want := []string{
		"[a " + id + " 2026-10-12 12:30 VARCHAR VARCHAR VARCHAR]",
		"[b " + id + " 2026-10-13 12:31 VARCHAR VARCHAR VARCHAR]",
		"[c hello word word VARCHAR VARCHAR VARCHAR]",
	}
	if !eq(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	types := map[string]string{}
	for i, name := range p.names {
		types[name] = p.types[i]
	}
	for _, fd := range fields {
		if types[fd.name] != fd.typ {
			t.Errorf("%s: %q, want %q", fd.name, types[fd.name], fd.typ)
		}
	}
}

// TestDumpKeepsKeysEqualButForCase: DuckDB's names are case-insensitive, so
// attr_Plan and attr_plan cannot both be columns; the first of them in byte
// order keeps its name and the next takes the first free _<n> suffix. Which
// column holds which key is a known gap; no value is dropped.
func TestDumpKeepsKeysEqualButForCase(t *testing.T) {
	f := newFixture(t)
	f.write(t,
		withAttrs(evOf(1, "a", "2026-10-11T10:00:00Z"), "", map[string]string{"Plan": "A", "plan": "b"}),
		withAttrs(evOf(1, "b", "2026-10-11T11:00:00Z"), "", map[string]string{"plan": "c", "plan_1": "z"}))
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet"}) {
		t.Fatalf("files = %v, want the day dumped", got)
	}
	got := sortedTexts(readTable(t, f.project1("2026-10-11.parquet"), "id", "attr_Plan", "attr_plan_2", "attr_plan_1").pick(t, "id", "attr_Plan", "attr_plan_2", "attr_plan_1"))
	if want := []string{"[a A b <nil>]", "[b <nil> c z]"}; !eq(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestDumpReadsAnyKey: a key is a JSON pointer and a column name, whatever
// characters it holds. NUL cannot be in the SQL text (DuckDB reads it as a
// C string), so its column is named with the visible \u0000, which a key
// holding that text then collides with.
func TestDumpReadsAnyKey(t *testing.T) {
	f := newFixture(t)
	f.write(t, withAttrs(evOf(1, "a", "2026-10-11T10:00:00Z"), "", map[string]string{"a-b c": "1", `q"u'o`: "2",
		"x/y~z": "3", "a$.[0]": "4", "nul\x00k": "5", `nul\u0000k`: "6", "t\tb\nc\x01\x7f": "7"}))
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet"}) {
		t.Fatalf("files = %v, want the day dumped", got)
	}
	got := texts(readTable(t, f.project1("2026-10-11.parquet")).pick(t, "attr_a-b c", `attr_q"u'o`, "attr_x/y~z", "attr_a$.[0]",
		`attr_nul\u0000k`, `attr_nul\u0000k_1`, "attr_t\tb\nc\x01\x7f"))
	if want := []string{"[1 2 3 4 5 6 7]"}; !eq(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestDumpKeepsAnIDsFirstLine: of a repeated id the dump keeps the line
// appended first, as SQLite keeps the first row, at any thread count. The
// log is about 45 MB, so it is read in more than one piece.
func TestDumpKeepsAnIDsFirstLine(t *testing.T) {
	f := newFixture(t)
	const ids = 6000
	pad := strings.Repeat("x", 2400)
	now := time.Now()
	for copy := 1; copy <= 3; copy++ {
		f.log.now = func() time.Time { return now.Add(time.Duration(copy) * (recentIDs + time.Minute)) }
		var evs []store.Event
		for i := 0; i < ids; i++ {
			evs = append(evs, withAttrs(evOf(1, fmt.Sprintf("id-%05d", i), "2026-10-11T10:00:00Z"), "", map[string]string{"copy": strconv.Itoa(copy), "pad": pad}))
		}
		f.write(t, evs...)
	}
	f.maintain(t, "2026-10-12")
	rows := readTable(t, f.project1("2026-10-11.parquet"), "attr_copy").pick(t, "attr_copy")
	later := len(slices.DeleteFunc(slices.Clone(rows), func(r []any) bool { return r[0] == "1" }))
	got := []string{fmt.Sprintf("[%d %d]", len(rows), later)}
	if want := []string{fmt.Sprintf("[%d 0]", ids)}; !eq(got, want) {
		t.Fatalf("rows, later copies kept = %v, want %v", got, want)
	}
}

// TestMergeKeepsAnIDsFirstRow: an id in the day file and again in a later
// segment (a repeat past the recent-ids window, after the day's dump) keeps
// the day file's row, though the segment's name sorts first.
func TestMergeKeepsAnIDsFirstRow(t *testing.T) {
	f := newFixture(t)
	f.write(t, withAttrs(evOf(1, "a", "2026-09-30T10:00:00Z"), "", map[string]string{"copy": "1"}))
	f.maintain(t, "2026-10-01")
	f.log.now = func() time.Time { return time.Now().Add(recentIDs + time.Minute) }
	f.write(t, withAttrs(evOf(1, "a", "2026-09-30T10:00:00Z"), "", map[string]string{"copy": "2"}))
	f.maintain(t, "2026-10-01")
	if got := f.files(t); !eq(got, []string{"2026-09-30.1.parquet", "2026-09-30.parquet"}) {
		t.Fatalf("files = %v, want the day file and a segment", got)
	}
	f.maintain(t, "2026-10-02")
	got := texts(readTable(t, f.project1("2026-09.parquet"), "id", "attr_copy").pick(t, "id", "attr_copy"))
	if want := []string{"[a 1]"}; !eq(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}
