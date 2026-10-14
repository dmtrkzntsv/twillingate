package archive

import (
	"os"
	"path/filepath"
	"testing"
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
