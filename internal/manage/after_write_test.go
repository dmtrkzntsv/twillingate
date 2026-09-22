package manage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// flakyReads fails LoadRegistry while broken is set; writes go through, so
// it models a read that fails right after a write committed.
type flakyReads struct {
	Store
	broken atomic.Bool
}

func (f *flakyReads) LoadRegistry(ctx context.Context) ([]store.RegistryProject, []store.RegistryKey, error) {
	if f.broken.Load() {
		return nil, nil, errors.New("disk hiccup")
	}
	return f.Store.LoadRegistry(ctx)
}

// TestCommittedWriteSucceedsWhenReloadFails: once the store has committed,
// the operation reports success even if refreshing the snapshot fails, and
// the registry catches up on its next read instead of serving stale data
// until the poll interval happens to elapse.
func TestCommittedWriteSucceedsWhenReloadFails(t *testing.T) {
	ctx := context.Background()
	st := &flakyReads{Store: testStore(t)}
	var logs strings.Builder
	reg := New(st, loggerTo(&logs))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	seed, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "seed"})
	if err != nil {
		t.Fatal(err)
	}

	st.broken.Store(true)
	p, key, err := ops.CreateProjectWithKey(ctx, "api", ProjectSpec{
		Name: "blog", AllowedOrigins: []string{"https://blog.example.com"}}, "default")
	if err != nil {
		t.Fatalf("create with key after commit = %v, want success", err)
	}
	if p == nil || p.ID != seed.ID+1 || p.Identity != "anonymous" || p.Name != "blog" ||
		len(p.AllowedOrigins) != 1 || !strings.HasPrefix(key, "ak_") {
		t.Fatalf("returned %+v, key %q; want the committed project", p, key)
	}
	// Ordered: later steps act on what earlier ones created.
	var docs *Project
	for _, step := range []struct {
		name string
		op   func() error
	}{
		{"CreateProject", func() error {
			var err error
			docs, err = ops.CreateProject(ctx, "cli", ProjectSpec{Name: "docs"})
			return err
		}},
		{"UpdateProject", func() error {
			p, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: seed.ID, Name: "Seed"})
			if err == nil && (p == nil || p.Name != "Seed") {
				return errors.New("update returned the pre-write project")
			}
			return err
		}},
		{"IssueIngestKey", func() error { _, err := ops.IssueIngestKey(ctx, "cli", seed.ID, "web"); return err }},
		{"DisableIngestKey", func() error { return ops.DisableIngestKey(ctx, "cli", seed.ID, "web") }},
		{"EnableIngestKey", func() error { return ops.EnableIngestKey(ctx, "cli", seed.ID, "web") }},
		{"ArchiveProject", func() error { return ops.ArchiveProject(ctx, "cli", seed.ID) }},
		{"RestoreProject", func() error { return ops.RestoreProject(ctx, "cli", seed.ID) }},
		{"DeleteProject", func() error { return ops.DeleteProject(ctx, "cli", docs.ID) }},
	} {
		if err := step.op(); err != nil {
			t.Errorf("%s after commit with a failing reload = %v, want success", step.name, err)
		}
	}
	if !strings.Contains(logs.String(), "registry reload after write failed") {
		t.Errorf("reload failure not logged: %s", logs.String())
	}

	// Reads recover on the very next snapshot, not a poll interval later.
	st.broken.Store(false)
	s := reg.Snapshot(ctx)
	if s.Project(p.ID) == nil || s.Project(seed.ID) == nil || s.Project(seed.ID).Name != "Seed" {
		t.Fatalf("snapshot after recovery missing committed writes: %+v", s.Projects())
	}
	if s.Project(docs.ID) != nil {
		t.Error("deleted project still in snapshot")
	}
}

func loggerTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// TestFailedReloadWithholdsTouchedProjects: after a committed write whose
// reload failed, the held snapshot may still grant what the write took
// away. Ingestion must fail closed for the projects the write touched until
// a reload succeeds, and only for those.
func TestFailedReloadWithholdsTouchedProjects(t *testing.T) {
	ctx := context.Background()
	st := &flakyReads{Store: testStore(t)}
	reg := New(st, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	blog, blogKey, err := ops.CreateProjectWithKey(ctx, "cli", ProjectSpec{Name: "blog"}, "web")
	if err != nil {
		t.Fatal(err)
	}
	_, shopKey, err := ops.CreateProjectWithKey(ctx, "cli", ProjectSpec{Name: "shop"}, "web")
	if err != nil {
		t.Fatal(err)
	}
	authorized := func(key string) bool {
		_, _, ok := reg.Snapshot(ctx).ProjectByKey(key)
		return ok
	}

	for _, tc := range []struct {
		name   string
		revoke func() error
		grant  func() error
	}{
		{"disable key",
			func() error { return ops.DisableIngestKey(ctx, "cli", blog.ID, "web") },
			func() error { return ops.EnableIngestKey(ctx, "cli", blog.ID, "web") }},
		{"archive project",
			func() error { return ops.ArchiveProject(ctx, "cli", blog.ID) },
			func() error { return ops.RestoreProject(ctx, "cli", blog.ID) }},
	} {
		st.broken.Store(true)
		if err := tc.revoke(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if authorized(blogKey) {
			t.Errorf("%s: revoked key still authorized while the reload keeps failing", tc.name)
		}
		if !authorized(shopKey) {
			t.Errorf("%s: an untouched project's key was withheld too", tc.name)
		}
		st.broken.Store(false)
		if authorized(blogKey) {
			t.Errorf("%s: revoked key authorized after recovery", tc.name)
		}
		if err := tc.grant(); err != nil {
			t.Fatal(err)
		}
		if !authorized(blogKey) {
			t.Fatalf("%s: key not authorized after undoing the revocation", tc.name)
		}
	}
}

// TestWithholdNeverOutlivesAReload: withholding must not stick when the
// snapshot it lands on is already current, or a project would stay locked
// out with nothing left to trigger a reload.
func TestWithholdNeverOutlivesAReload(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	reg := New(st, discard())
	ops := NewOps(reg, st)
	p, key, err := ops.CreateProjectWithKey(ctx, "cli", ProjectSpec{Name: "blog"}, "web")
	if err != nil {
		t.Fatal(err)
	}
	reg.withhold(p.ID) // lands on an up-to-date snapshot
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey(key); !ok {
		t.Fatal("withheld project stayed locked out after the next read")
	}
}
