package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// EngineOptions lays the engine out beside the database: raw/ is the one
// directory its SQL reaches (PR 3 keeps the events there), engine/ holds
// the spill directories and the slot locks. The SQLite file stays outside
// raw/, so widget SQL never has a path to it (raw-first spec D8).
func EngineOptions(cfg *config.Config) (engine.Options, error) {
	self, err := os.Executable()
	if err != nil {
		return engine.Options{}, fmt.Errorf("engine: %w", err)
	}
	data := filepath.Dir(DatabasePath(cfg.Database))
	return engine.Options{
		Path:        self,
		Args:        []string{"engine"},
		AllowedDirs: []string{filepath.Join(data, "raw")},
		SpillRoot:   filepath.Join(data, "engine", "spill"),
		SlotDir:     filepath.Join(data, "engine", "slots"),
		MaxChildren: cfg.Engine.MaxChildren,
		MemoryLimit: cfg.Engine.MemoryLimit,
	}, nil
}
