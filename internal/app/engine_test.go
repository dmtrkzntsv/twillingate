package app

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
)

// TestEngineOptionsKeepTheDatabaseOutOfReach is Review Focus 1: the child
// may write where it may read, so no allowed directory may be, or hold,
// the database's directory.
func TestEngineOptionsKeepTheDatabaseOutOfReach(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "twillingate.db")
	cfg := configtest.Load(t, map[string]string{"DATABASE_DSN": "sqlite://" + db,
		"ENGINE_MEMORY_LIMIT": "768MB", "ENGINE_MAX_CHILDREN": "3"})
	o, err := EngineOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.AllowedDirs) != 1 || o.AllowedDirs[0] != filepath.Join(dir, "raw") {
		t.Fatalf("AllowedDirs = %v, want only %s", o.AllowedDirs, filepath.Join(dir, "raw"))
	}
	for _, a := range o.AllowedDirs {
		if rel, err := filepath.Rel(a, db); err == nil && !strings.HasPrefix(rel, "..") {
			t.Errorf("allowed %s holds the database %s", a, db)
		}
	}
	if runtime.GOOS == "linux" && o.Path != "/proc/self/exe" {
		t.Errorf("Path = %q, want /proc/self/exe: os.Executable names the new binary after an in-place upgrade", o.Path)
	}
	if o.MemoryLimit != "768MB" || o.MaxChildren != 3 || o.Path == "" ||
		len(o.Args) != 1 || o.Args[0] != "engine" ||
		o.SlotDir != filepath.Join(dir, "engine", "slots") || o.SpillRoot != filepath.Join(dir, "engine", "spill") {
		t.Errorf("options = %+v", o)
	}
}

// TestEngineOptionsAllowEventsDir: the child reaches the event files
// wherever EVENTS_DIR puts them, and nothing else; its spill and slots stay
// beside the database, where every process on the data directory finds the
// same slots.
func TestEngineOptionsAllowEventsDir(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(t.TempDir(), "events")
	cfg := configtest.Load(t, map[string]string{"DATABASE_DSN": "sqlite://" + filepath.Join(dir, "twillingate.db"), "EVENTS_DIR": events})
	o, err := EngineOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.AllowedDirs) != 1 || o.AllowedDirs[0] != events || o.SlotDir != filepath.Join(dir, "engine", "slots") {
		t.Fatalf("AllowedDirs = %v, SlotDir = %s; want only %s, slots beside the database", o.AllowedDirs, o.SlotDir, events)
	}
}
