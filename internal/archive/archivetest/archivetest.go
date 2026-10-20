// Package archivetest is a raw/ directory with an engine child and a
// reader over it, for the tests of the packages that read event files.
package archivetest

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ChildEnv names the variable that makes a test binary serve as the engine
// child: the engine starts the binary it runs in (os.Executable), so the
// binary's TestMain must call Child first.
const ChildEnv = "ARCHIVETEST_ENGINE_CHILD"

// Child serves engine.ServeChild on stdin and stdout, then exits, when
// ChildEnv is 1, and returns false otherwise. A test binary that uses New
// starts its TestMain with it:
//
//	func TestMain(m *testing.M) {
//		if archivetest.Child() {
//			return
//		}
//		os.Exit(m.Run())
//	}
func Child() bool {
	if os.Getenv(ChildEnv) != "1" {
		return false
	}
	if err := engine.ServeChild(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

// Files is a raw/ directory under a test's temp directory, its log, an
// engine on this test binary and a reader over them.
type Files struct {
	Dir     string
	Log     *archive.Log
	Eng     *engine.Engine
	Reader  *archive.Reader
	Archive *archive.Archive
}

// New opens the log and the engine, closed with the test.
func New(t testing.TB) *Files {
	t.Helper()
	data := t.TempDir()
	l, err := archive.OpenLog(filepath.Join(data, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(engine.Options{Path: self, Env: []string{ChildEnv + "=1"},
		AllowedDirs: []string{l.Dir()}, SpillRoot: filepath.Join(data, "spill"), SlotDir: filepath.Join(data, "slots"),
		MaxChildren: 1, MemoryLimit: "256MB", Threads: 1})
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close(); l.Close() })
	r, err := archive.NewReader(l.Dir(), eng)
	if err != nil {
		t.Fatal(err)
	}
	return &Files{Dir: l.Dir(), Log: l, Eng: eng, Reader: r, Archive: archive.New(l, eng, slog.Default())}
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
// its Parquet file and derived, so the reader then reads day files, derived
// files and today's log.
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

// CopyDir copies every file under src (a raw/ directory) into dst, made if
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
