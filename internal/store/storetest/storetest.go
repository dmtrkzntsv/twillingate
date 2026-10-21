// Package storetest hands tests a database at the current schema without
// migrating one per test. Under -race every migration costs seconds
// (modernc SQLite is C translated to Go, so the detector instruments it
// too), and a package whose tests each migrated their own database ran
// past go test's 10-minute default. This migrates once per test binary
// and copies the resulting file.
package storetest

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
)

var (
	once     sync.Once
	image    []byte
	imageErr error

	legacyOnce     sync.Once
	legacyImage    []byte
	legacyImageErr error
)

// Copy writes a database migrated to the current schema to path. The
// first call migrates one in its test's temp dir and keeps the bytes, so
// nothing outlives the test binary.
func Copy(t testing.TB, path string) {
	t.Helper()
	once.Do(func() { image, imageErr = migrated(filepath.Join(t.TempDir(), "template.db")) })
	if imageErr != nil {
		t.Fatal(imageErr)
	}
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Open writes a migrated database to path, opens it, and closes it when
// the test ends.
func Open(t testing.TB, path string) store.Store {
	t.Helper()
	Copy(t, path)
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// OpenLegacy is Open at sqlite.LastEventsVersion, the last schema with
// the events table: what `twillingate events import` reads.
func OpenLegacy(t testing.TB, path string) store.Store {
	t.Helper()
	legacyOnce.Do(func() {
		legacyImage, legacyImageErr = migratedThrough(filepath.Join(t.TempDir(), "legacy.db"), sqlite.LastEventsVersion)
	})
	if legacyImageErr != nil {
		t.Fatal(legacyImageErr)
	}
	if err := os.WriteFile(path, legacyImage, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// migrated migrates a database at path and returns its bytes.
func migrated(path string) ([]byte, error) {
	return migratedThrough(path, math.MaxInt)
}

// migratedThrough migrates a database at path through version v and
// returns its bytes. Closing checkpoints the WAL into the main file, so
// that one file carries everything.
func migratedThrough(path string, v int) ([]byte, error) {
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		return nil, err
	}
	db := st.(*sqlite.DB)
	if err := errors.Join(db.MigrateThrough(context.Background(), v), st.Close()); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
