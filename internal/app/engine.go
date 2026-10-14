package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// EngineOptions lays the engine out: EVENTS_DIR (raw/ beside the database
// unless set) is the one directory its SQL reaches, engine/ beside the
// database holds the spill directories and the slot locks, which every
// process on the data directory shares. Config refuses an EVENTS_DIR that
// holds the SQLite file, so widget SQL never has a path to it (raw-first
// spec D8). The child's SQL can write and delete inside EVENTS_DIR
// (DuckDB's allowed directory is read-write): only the archive's Exec
// writes; Query refuses anything but one SELECT.
func EngineOptions(cfg *config.Config) (engine.Options, error) {
	// The running binary itself, even after an upgrade replaced the file
	// on disk: os.Executable would name the new one, and the old server
	// would spawn a child of another version.
	self := "/proc/self/exe"
	if runtime.GOOS != "linux" {
		var err error
		if self, err = os.Executable(); err != nil {
			return engine.Options{}, fmt.Errorf("engine: %w", err)
		}
	}
	data := filepath.Dir(DatabasePath(cfg.Database))
	return engine.Options{
		Path:        self,
		Args:        []string{"engine"},
		AllowedDirs: []string{cfg.EventsDir},
		SpillRoot:   filepath.Join(data, "engine", "spill"),
		SlotDir:     filepath.Join(data, "engine", "slots"),
		MaxChildren: cfg.Engine.MaxChildren,
		MemoryLimit: cfg.Engine.MemoryLimit,
	}, nil
}
