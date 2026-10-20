package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSizesSumsEveryListedFile: a project's size is the bytes of every
// file ListFiles lists for it, its Parquet, its logs and its current
// derived files; a file it leaves out (merged away, a write in progress,
// any other name) and a project with neither events file nor log count
// nothing.
func TestSizesSumsEveryListedFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int, at time.Time) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Repeat("x", n)), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	old, later := time.Now().Add(-time.Hour), time.Now()
	write("1/2026-09.parquet", 1000, old)
	write("1/2026-09-30.parquet", 7, old) // merged into 2026-09.parquet: left out
	write("1/2026-10-01.parquet", 200, old)
	write("1/2026-10-02.log", 30, old)
	write("1/2026-10-02.parquet.tmp", 9, old) // a write in progress: left out
	write("1/notes.txt", 11, old)
	write("1/sessions/2026-09.parquet", 40, later)
	write("1/sessions/2026-10-01.parquet", 5, later)
	write("1/"+identitiesFile, 60, later)
	write("1/"+aliasesFile, 3, later)
	write("2/2026-10-02.log", 17, old)
	if err := os.MkdirAll(filepath.Join(dir, "3"), 0o750); err != nil { // nothing yet
		t.Fatal(err)
	}
	got, err := Sizes(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]int64{1: 1000 + 200 + 30 + 40 + 5 + 60 + 3, 2: 17}
	if len(got) != len(want) || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("sizes = %v, want %v", got, want)
	}

	none, err := Sizes(filepath.Join(t.TempDir(), "raw"))
	if err != nil || len(none) != 0 {
		t.Errorf("without raw/: %v, %v; want none", none, err)
	}
}
