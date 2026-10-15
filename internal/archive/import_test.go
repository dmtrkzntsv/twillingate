package archive

import (
	"context"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func (f fixture) importDay(t *testing.T, day string, evs ...store.Event) int {
	t.Helper()
	n, err := f.a.ImportDay(context.Background(), 1, day, evs)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestImportDayWritesWhatTheFilesLack: of a day's events, those the
// project's files and logs hold are skipped, the others written as a
// closed log; a project with no directory gets one.
func TestImportDayWritesWhatTheFilesLack(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"))
	if n := f.importDay(t, "2026-10-11", evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z")); n != 1 {
		t.Fatalf("wrote %d, want 1 (b)", n)
	}
	if got := f.files(t); !eq(got, []string{"2026-10-11.1.closed", "2026-10-11.log"}) {
		t.Fatalf("files = %v", got)
	}
	if got := lines(t, f.project1("2026-10-11.1.closed")); len(got) != 1 || got[0]["id"] != "b" {
		t.Fatalf("closed log = %v, want b alone", got)
	}
	n, err := f.a.ImportDay(context.Background(), 7, "2026-10-11", []store.Event{evOf(7, "c", "2026-10-11T09:00:00Z")})
	if err != nil || n != 1 {
		t.Fatalf("a project with no directory: %d, %v; want 1 written", n, err)
	}
}

// TestImportDaySkipsWhatTheFilesHold: an id in the day file, a segment, a
// closed log or the open log is held; one only another day holds is not.
func TestImportDaySkipsWhatTheFilesHold(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "day", "2026-10-11T10:00:00Z"), evOf(1, "other", "2026-10-12T10:00:00Z"))
	f.maintain(t, "2026-10-12") // 2026-10-11.parquet
	f.write(t, evOf(1, "seg", "2026-10-11T11:00:00Z"))
	f.maintain(t, "2026-10-12") // 2026-10-11.1.parquet
	f.write(t, evOf(1, "closed", "2026-10-11T12:00:00Z"))
	if _, err := f.log.Rotate(1, "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	f.write(t, evOf(1, "open", "2026-10-11T13:00:00Z"))
	n := f.importDay(t, "2026-10-11",
		evOf(1, "day", "2026-10-11T10:00:00Z"), evOf(1, "seg", "2026-10-11T11:00:00Z"),
		evOf(1, "closed", "2026-10-11T12:00:00Z"), evOf(1, "open", "2026-10-11T13:00:00Z"),
		evOf(1, "new", "2026-10-11T14:00:00Z"), evOf(1, "other", "2026-10-11T15:00:00Z"))
	if n != 2 {
		t.Fatalf("wrote %d, want 2 (new, and other: held for another day only)", n)
	}
}

// TestImportDayTwiceWritesOnce: the closed log an import wrote holds its
// ids, so the same import again writes nothing.
func TestImportDayTwiceWritesOnce(t *testing.T) {
	f := newFixture(t)
	evs := []store.Event{evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z")}
	if n := f.importDay(t, "2026-10-11", evs...); n != 2 {
		t.Fatalf("first import wrote %d, want 2", n)
	}
	if n := f.importDay(t, "2026-10-11", evs...); n != 0 {
		t.Fatalf("second import wrote %d, want 0", n)
	}
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet"}) {
		t.Fatalf("files after the pass = %v", got)
	}
	if n, ids := f.rows(t, "2026-10-11.parquet"); n != 2 || ids != 2 {
		t.Fatalf("day file: %d rows, %d ids; want 2 and 2", n, ids)
	}
}

// TestImportedDayOfAMergedMonthIsMergedIn: a day of a month already merged
// is checked against the month file, and what it lacks goes into the month
// file at the next pass: dumped as the day file, merged again, the day
// file gone.
func TestImportedDayOfAMergedMonthIsMergedIn(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "m1", "2026-09-29T10:00:00Z"))
	f.maintain(t, "2026-10-05")
	if got := f.files(t); !eq(got, []string{"2026-09.parquet"}) {
		t.Fatalf("files before = %v, want September merged", got)
	}
	if n := f.importDay(t, "2026-09-29", evOf(1, "m1", "2026-09-29T10:00:00Z"), evOf(1, "m2", "2026-09-29T11:00:00Z")); n != 1 {
		t.Fatalf("wrote %d, want 1 (m2)", n)
	}
	f.maintain(t, "2026-10-05")
	if got := f.files(t); !eq(got, []string{"2026-09.parquet"}) {
		t.Fatalf("files after = %v, want the month file alone", got)
	}
	if n, ids := f.rows(t, "2026-09.parquet"); n != 2 || ids != 2 {
		t.Fatalf("month file: %d rows, %d ids; want 2 and 2", n, ids)
	}
}
