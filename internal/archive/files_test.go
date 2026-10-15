package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// touch creates each name under dir (directories as needed), empty.
func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

// TestListFilesSkipsWhatAMergeOrDumpLeftBehind is Review Focus 2: a merge
// that stopped before deleting its days left the month file and the days;
// a dump that stopped before deleting its log left the day file and the
// claimed log. Each row is listed once.
func TestListFilesSkipsWhatAMergeOrDumpLeftBehind(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir,
		"1/2026-09.parquet",
		"1/2026-09-30.parquet",   // merged into 2026-09.parquet: left out
		"1/2026-09-30.1.parquet", // likewise
		"1/2026-10-01.parquet",
		"1/2026-10-01.1.parquet",
		"1/2026-10-02.parquet",
		"1/2026-10-02.parquet.dumping", // its target exists: left out
		"1/2026-10-03.1.parquet.dumping",
		"1/2026-10-04.1.closed",
		"1/2026-10-05.log",
		"1/2026-10-05.parquet.tmp", // a write in progress: left out
		"1/notes.txt",
		"2/2026-10-05.log",
		"x/2026-10-05.log", // not a project directory
	)
	if err := os.MkdirAll(filepath.Join(dir, "3"), 0o750); err != nil { // a project with nothing yet
		t.Fatal(err)
	}
	got, err := ListFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := func(names ...string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = filepath.Join(dir, n)
		}
		return out
	}
	want := map[int64]Files{
		1: {
			Parquet: in("1/2026-09.parquet", "1/2026-10-01.1.parquet", "1/2026-10-01.parquet", "1/2026-10-02.parquet"),
			Logs:    in("1/2026-10-03.1.parquet.dumping", "1/2026-10-04.1.closed", "1/2026-10-05.log"),
		},
		2: {Logs: in("2/2026-10-05.log")},
	}
	if len(got) != len(want) {
		t.Fatalf("projects = %v, want %v", got, want)
	}
	for id, w := range want {
		if !eq(got[id].Parquet, w.Parquet) || !eq(got[id].Logs, w.Logs) {
			t.Errorf("project %d = %+v, want %+v", id, got[id], w)
		}
	}
}

func TestListFilesWithoutRaw(t *testing.T) {
	got, err := ListFiles(filepath.Join(t.TempDir(), "raw"))
	if err != nil || len(got) != 0 {
		t.Fatalf("ListFiles of a missing directory = %v, %v; want nothing and no error", got, err)
	}
}

// age sets the modification time of each name under dir to base plus its
// offset in minutes.
func age(t *testing.T, dir string, base time.Time, names map[string]int) {
	t.Helper()
	for n, m := range names {
		at := base.Add(time.Duration(m) * time.Minute)
		if err := os.Chtimes(filepath.Join(dir, n), at, at); err != nil {
			t.Fatal(err)
		}
	}
}

// TestListFilesKnowsWhichDerivedFilesAreCurrent: a derived file is read
// only while no events file it comes from is newer; every day it does not
// cover is a gap, adjacent gaps joined; a session file no events file
// backs is an orphan; identities and aliases are due when missing or
// older than any events file.
func TestListFilesKnowsWhichDerivedFilesAreCurrent(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir,
		"1/2026-08.parquet", "1/sessions/2026-08.parquet", // current
		"1/2026-09.parquet", "1/sessions/2026-09.parquet", // the month file is newer: stale
		"1/sessions/2026-09-30.parquet",                         // a day of a merged month: orphan
		"1/2026-10-01.parquet", "1/sessions/2026-10-01.parquet", // current
		"1/2026-10-02.parquet", "1/2026-10-02.1.parquet", "1/sessions/2026-10-02.parquet", // a segment is newer: stale
		"1/2026-10-03.parquet",                                                      // never derived
		"1/2026-10-04.log",                                                          // a log: computed on read, never derived
		"1/2026-10-05.parquet", "1/2026-10-05.log", "1/sessions/2026-10-05.parquet", // a late log: the day is a gap
		"1/sessions/2026-07.parquet", // its month is gone: orphan
		"1/identities.parquet", "1/aliases.parquet",
	)
	p := filepath.Join(dir, "1")
	base := time.Now().Add(-24 * time.Hour) // older than every file touch left unaged
	age(t, p, base, map[string]int{
		"2026-08.parquet": 0, "sessions/2026-08.parquet": 1,
		"2026-09.parquet": 5, "sessions/2026-09.parquet": 1,
		"2026-10-01.parquet": 0, "sessions/2026-10-01.parquet": 1,
		"2026-10-02.parquet": 0, "sessions/2026-10-02.parquet": 1, "2026-10-02.1.parquet": 2,
		"2026-10-05.parquet": 0, "sessions/2026-10-05.parquet": 1,
		"identities.parquet": 3, "aliases.parquet": 10,
		"2026-10-03.parquet": 4,
	})
	names, err := list(p)
	if err != nil {
		t.Fatal(err)
	}
	pf, err := projectOf(p, names)
	if err != nil {
		t.Fatal(err)
	}
	in := func(names ...string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = filepath.Join(p, n)
		}
		return out
	}
	if want := in("sessions/2026-08.parquet", "sessions/2026-10-01.parquet"); !eq(pf.Sessions, want) {
		t.Errorf("Sessions = %v, want %v", pf.Sessions, want)
	}
	if want := []Span{{"2026-09-01", "2026-09-30"}, {"2026-10-02", "2026-10-05"}}; fmt.Sprint(pf.SessionGaps) != fmt.Sprint(want) {
		t.Errorf("SessionGaps = %v, want %v", pf.SessionGaps, want)
	}
	if fmt.Sprint(pf.deriveMonths) != fmt.Sprint(map[string]string{"2026-09": filepath.Join(p, "2026-09.parquet")}) {
		t.Errorf("deriveMonths = %v, want 2026-09 only", pf.deriveMonths)
	}
	wantDays := map[string][]string{
		"2026-10-02": in("2026-10-02.1.parquet", "2026-10-02.parquet"),
		"2026-10-03": in("2026-10-03.parquet"),
	}
	if fmt.Sprint(pf.deriveDays) != fmt.Sprint(wantDays) {
		t.Errorf("deriveDays = %v, want %v", pf.deriveDays, wantDays)
	}
	if want := in("sessions/2026-07.parquet", "sessions/2026-09-30.parquet"); !eq(pf.orphans, want) {
		t.Errorf("orphans = %v, want %v", pf.orphans, want)
	}
	// identities.parquet is older than 2026-10-03.parquet; aliases.parquet is newer than every events file.
	if pf.Identities != "" || pf.Aliases != filepath.Join(p, "aliases.parquet") || !pf.identitiesDue {
		t.Errorf("Identities %q, Aliases %q, due %v; want stale, current, due", pf.Identities, pf.Aliases, pf.identitiesDue)
	}
}

// TestListFilesDerivedWithoutEvents: a project with only logs has every
// log's day as a gap and nothing to derive; derived files of a project
// with no events file left are orphans.
func TestListFilesDerivedWithoutEvents(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "1/2026-10-05.log", "1/2026-10-06.log", "1/identities.parquet", "1/aliases.parquet")
	p := filepath.Join(dir, "1")
	names, err := list(p)
	if err != nil {
		t.Fatal(err)
	}
	pf, err := projectOf(p, names)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(pf.SessionGaps) != fmt.Sprint([]Span{{"2026-10-05", "2026-10-06"}}) || len(pf.deriveDays)+len(pf.deriveMonths) != 0 {
		t.Errorf("gaps %v, derive %v %v; want one span and nothing to derive", pf.SessionGaps, pf.deriveDays, pf.deriveMonths)
	}
	if pf.identitiesDue || !eq(pf.orphans, []string{filepath.Join(p, "aliases.parquet"), filepath.Join(p, "identities.parquet")}) {
		t.Errorf("due %v, orphans %v; want not due, both files orphans", pf.identitiesDue, pf.orphans)
	}
}

// TestLatestDays: a month file counts as its last day, a day file or
// segment, a log, a closed log and a claimed one as their day; the newest
// wins per project, and derived files, temp files and other names count for
// nothing.
func TestLatestDays(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir,
		"1/2026-02.parquet", // a month: counts as the 28th
		"1/2026-02-10.parquet",
		"1/2026-03-01.parquet",
		"1/2026-03-02.1.parquet", // a segment
		"1/2026-03-02.parquet.tmp",
		"1/2026-04-01.parquet.tmp", // a write in progress: not a day
		"1/sessions/2026-05.parquet",
		"1/identities.parquet",
		"2/2026-12.parquet", // the last day of a December
		"2/2026-12-05.parquet",
		"3/2026-10-04.log",
		"3/2026-10-04.1.closed",
		"3/2026-10-03.parquet",
		"4/2026-10-05.1.parquet.dumping",
		"4/2026-10-01.parquet",
		"5/notes.txt",
		"x/2026-10-05.log", // not a project directory
	)
	if err := os.MkdirAll(filepath.Join(dir, "6"), 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := LatestDays(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{1: "2026-03-02", 2: "2026-12-31", 3: "2026-10-04", 4: "2026-10-05"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("LatestDays = %v, want %v", got, want)
	}
	touch(t, dir, "1/2024-02.parquet")
	if got, _ := LatestDays(dir); got[1] != "2026-03-02" {
		t.Errorf("an older month made it %s", got[1])
	}
	if got, err := LatestDays(filepath.Join(dir, "none")); err != nil || len(got) != 0 {
		t.Errorf("a missing raw/: %v, %v", got, err)
	}
}

// TestOldestDays: each project's oldest day by name, a month file from
// its first day; what is no events file is left out.
func TestOldestDays(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir,
		"1/2026-02.parquet", // a month: counts as the 1st
		"1/2026-02-10.parquet",
		"1/sessions/2025-05.parquet",
		"2/2026-12-05.parquet",
		"2/2026-12-04.parquet.tmp",
		"3/2026-10-04.log",
		"3/2026-10-05.1.closed",
		"4/notes.txt",
	)
	got, err := OldestDays(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{1: "2026-02-01", 2: "2026-12-05", 3: "2026-10-04"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("OldestDays = %v, want %v", got, want)
	}
	if got, err := OldestDays(filepath.Join(dir, "none")); err != nil || len(got) != 0 {
		t.Errorf("a missing raw/: %v, %v", got, err)
	}
}
