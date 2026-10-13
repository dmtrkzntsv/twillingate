package archive

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const childEnv = "ARCHIVE_TEST_ENGINE_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		if err := engine.ServeChild(os.Args[1:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fixture struct {
	log *Log
	eng *engine.Engine
	a   *Archive
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	data := t.TempDir()
	l, err := OpenLog(filepath.Join(data, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{childEnv + "=1"},
		AllowedDirs: []string{l.Dir()}, SpillRoot: filepath.Join(data, "spill"), SlotDir: filepath.Join(data, "slots"),
		MaxChildren: 1, MemoryLimit: "256MB", Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close(); l.Close() })
	return fixture{l, eng, New(l, eng, slog.Default())}
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

// files lists project 1's directory.
func (f fixture) files(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.log.Dir(), "1"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// rows counts the rows and distinct ids DuckDB reads from a Parquet file.
func (f fixture) rows(t *testing.T, name string) (int, int) {
	t.Helper()
	res, err := f.eng.Query(context.Background(), "SELECT count(*), count(DISTINCT id) FROM read_parquet('"+filepath.Join(f.log.Dir(), "1", name)+"')", 0)
	if err != nil {
		t.Fatal(err)
	}
	var n, ids int
	fmt.Sscan(fmt.Sprint(res.Rows[0][0]), &n)
	fmt.Sscan(fmt.Sprint(res.Rows[0][1]), &ids)
	return n, ids
}

func eq(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

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
	if err := f.eng.Exec(context.Background(), "COPY (SELECT * FROM read_parquet(['"+filepath.Join(dir, "2026-09-01.parquet")+"','"+filepath.Join(dir, "2026-09-02.parquet")+"'], union_by_name=true)) TO '"+filepath.Join(dir, "2026-09.parquet")+"' (FORMAT parquet)"); err != nil {
		t.Fatal(err)
	}
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

// TestOneFailingDumpStopsNothingElse: a log DuckDB cannot read stays pending
// and holds its month back; the project's other dumps and its retention
// still run, and the error is returned.
func TestOneFailingDumpStopsNothingElse(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "old", "2025-08-15T10:00:00Z"), evOf(1, "a", "2026-08-30T10:00:00Z"), evOf(1, "c", "2026-10-11T10:00:00Z"))
	f.writeFile(t, "2026-08-31.1.closed", "[1]\n") // valid JSON, not an object
	d, _ := civil.Parse("2026-10-12")
	if err := f.a.maintainProject(context.Background(), 1, d, 1, 400); err == nil {
		t.Fatal("maintainProject returned nil with a log it cannot read")
	}
	want := []string{"2026-08-30.parquet", "2026-08-31.parquet.dumping", "2026-10-11.parquet"}
	if got := f.files(t); !eq(got, want) {
		t.Fatalf("files = %v, want %v: the good days dumped, August 2025 deleted, August 2026 not merged", got, want)
	}
}
