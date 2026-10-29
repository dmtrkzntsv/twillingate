// Package archivetest is a data/ directory and its log, for the tests of
// the packages that read event files, and a scripted SQL engine (FakeSQL)
// for the tests of what sends SQL to one.
package archivetest

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Files is a data/ directory under a test's temp directory, its log and
// the archive that runs the daily pass over it.
type Files struct {
	Dir     string
	Log     *archive.Log
	Archive *archive.Archive
}

// New opens the log, closed with the test.
func New(t testing.TB) *Files {
	t.Helper()
	data := t.TempDir()
	l, err := archive.OpenLog(filepath.Join(data, "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return &Files{Dir: l.Dir(), Log: l, Archive: archive.New(l, slog.Default())}
}

// Write appends events to the logs of their days.
func (f *Files) Write(t testing.TB, evs ...store.Event) {
	t.Helper()
	if err := f.Log.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// Maintain runs the daily pass as of today (YYYY-MM-DD) with a late window
// of one day and 400 days of retention: every day before today is dumped to
// its Parquet file and derived, so the directory then holds day files,
// derived files and today's log.
func (f *Files) Maintain(t testing.TB, today string) {
	t.Helper()
	d, err := civil.Parse(today)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Archive.Maintain(context.Background(), d, 1, 400); err != nil {
		t.Fatal(err)
	}
}

// CopyDir copies every file under src (a data/ directory) into dst, made if
// missing, but the log's lock, keeping modification times: a derived file
// is current only while no events file it comes from is newer, so a copy
// that stamps every file anew would leave the derived files stale.
func CopyDir(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || d.Name() == ".lock" {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
	if err != nil {
		t.Fatal(err)
	}
}
