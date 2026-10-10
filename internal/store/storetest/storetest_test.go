package storetest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Two copies are independent databases, each already migrated: both take
// a project without a Migrate call, and a write to one never shows in the
// other.
func TestOpenGivesIndependentMigratedCopies(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a := Open(t, filepath.Join(dir, "a.db"))
	b := Open(t, filepath.Join(dir, "b.db"))
	create := func(st store.Store, name string) {
		t.Helper()
		if _, err := st.CreateProject(ctx, store.RegistryProject{
			Name: name, AllowedOrigins: "[]", Attributes: "[]"},
			store.AuditEntry{Actor: "test", Action: "project.create"}); err != nil {
			t.Fatal(err)
		}
	}
	create(a, "blog")
	create(a, "docs")
	create(b, "shop")
	for name, want := range map[string]struct {
		st store.Store
		n  int
	}{"a": {a, 2}, "b": {b, 1}} {
		projects, _, err := want.st.LoadRegistry(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(projects) != want.n {
			t.Errorf("%s holds %d projects, want %d", name, len(projects), want.n)
		}
	}
}

// fatalTB records a Fatal and stops the calling goroutine, as testing.T
// does, so a test can watch a helper fail without failing itself.
type fatalTB struct {
	testing.TB
	msg string
}

func (f *fatalTB) Fatal(args ...any) {
	f.msg = fmt.Sprint(args...)
	runtime.Goexit()
}

func fatalOf(t *testing.T, fn func(testing.TB)) string {
	t.Helper()
	f := &fatalTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(f)
	}()
	<-done
	return f.msg
}

func TestCopyFailsTheTestOnAnUnwritablePath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "x.db")
	if msg := fatalOf(t, func(tb testing.TB) { Copy(tb, missing) }); msg == "" {
		t.Error("Copy into a missing directory did not fail the test")
	}
}

// A template that failed to migrate fails every test that asks for a
// copy, not only the first.
func TestCopyFailsEveryTestWhenMigrationFailed(t *testing.T) {
	Copy(t, filepath.Join(t.TempDir(), "ok.db"))
	saved := imageErr
	imageErr = errors.New("migration failed")
	t.Cleanup(func() { imageErr = saved })
	if msg := fatalOf(t, func(tb testing.TB) { Copy(tb, filepath.Join(t.TempDir(), "x.db")) }); msg != "migration failed" {
		t.Errorf("Copy failed with %q, want the migration's error", msg)
	}
}

func TestMigratedReportsAnUnopenablePath(t *testing.T) {
	if _, err := migrated(filepath.Join(t.TempDir(), "missing", "x.db")); err == nil {
		t.Error("migrated in a missing directory returned no error")
	}
}
