package archivetest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func event(id string, project int64, at string) store.Event {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		panic(err)
	}
	return store.Event{ID: id, ProjectID: project, Family: store.FamilyProduct, EventName: "x", TS: t, ReceivedAt: t}
}

// TestWritesTwoDaysAndMaintains: yesterday is a day file after the pass,
// today an open log, for each project.
func TestWritesTwoDaysAndMaintains(t *testing.T) {
	f := New(t)
	f.Write(t, event("a", 1, "2026-10-11T10:00:00Z"), event("b", 1, "2026-10-12T10:00:00Z"), event("c", 2, "2026-10-12T11:00:00Z"))
	f.Maintain(t, "2026-10-12")
	days, err := archive.LatestDays(f.Dir)
	if err != nil || fmt.Sprint(days) != "map[1:2026-10-12 2:2026-10-12]" {
		t.Fatalf("latest days = %v, %v", days, err)
	}
	for _, p := range []string{"1/2026-10-11.parquet", "1/2026-10-12.log", "2/2026-10-12.log"} {
		if _, err := os.Stat(filepath.Join(f.Dir, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// TestCopyDirKeepsTimes: a copy reads as the template did, its derived
// files current, because every file keeps its modification time; the
// log's lock stays behind.
func TestCopyDirKeepsTimes(t *testing.T) {
	f := New(t)
	f.Write(t, event("a", 1, "2026-10-11T10:00:00Z"), event("b", 1, "2026-10-12T10:00:00Z"))
	f.Maintain(t, "2026-10-12")
	dst := filepath.Join(t.TempDir(), "raw")
	CopyDir(t, f.Dir, dst)
	n := 0
	err := filepath.WalkDir(f.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(f.Dir, path)
		got, err := os.Stat(filepath.Join(dst, rel))
		if d.Name() == ".lock" {
			if err == nil {
				t.Errorf("the lock was copied")
			}
			return nil
		}
		if err != nil {
			return err
		}
		want, err := d.Info()
		if err != nil {
			return err
		}
		if !got.ModTime().Equal(want.ModTime()) || got.Size() != want.Size() {
			t.Errorf("%s: %v %d bytes, want %v %d", rel, got.ModTime(), got.Size(), want.ModTime(), want.Size())
		}
		n++
		return nil
	})
	if err != nil || n == 0 {
		t.Fatalf("walked %d files: %v", n, err)
	}
}
