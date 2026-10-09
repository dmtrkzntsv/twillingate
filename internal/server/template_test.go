package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestMain runs the package's tests, then removes the template
// database's temp dir (see templateDB) once every test has finished
// with it.
func TestMain(m *testing.M) {
	code := m.Run()
	if templateDir != "" {
		os.RemoveAll(templateDir)
	}
	os.Exit(code)
}

var (
	templateOnce sync.Once
	templateDir  string
	templatePath string
)

// templateDB migrates a database to the current schema version once per
// test binary and returns its path, as internal/store/sqlite's tests do.
// Under -race every migration costs seconds, and migrating a database
// per test brings this package close to go test's 10-minute default
// timeout; openMigratedStore copies this file instead.
func templateDB(t *testing.T) string {
	t.Helper()
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "twillingate-server-template-")
		if err != nil {
			t.Fatal(err)
		}
		templateDir = dir
		path := filepath.Join(dir, "template.db")
		st, err := store.Open("sqlite://" + path)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Close checkpoints the WAL into the main file, so the copy below
		// only ever needs the one file.
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		templatePath = path
	})
	if templatePath == "" {
		t.Fatal("template database was not built")
	}
	return templatePath
}

// openMigratedStore opens a migrated store at path, a fresh copy of
// templateDB's, and closes it when the test ends.
func openMigratedStore(t *testing.T, path string) store.Store {
	t.Helper()
	data, err := os.ReadFile(templateDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// A no-op at the version the template carries; kept so the test
	// path matches how production opens an existing database.
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}
