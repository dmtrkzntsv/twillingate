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
	reg := New(st, defaults, loggerTo(&logs))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "seed"}); err != nil {
		t.Fatal(err)
	}

	st.broken.Store(true)
	p, key, err := ops.CreateProjectWithKey(ctx, "api", ProjectSpec{
		Alias: "blog", AllowedOrigins: []string{"https://blog.example.com"}}, "default")
	if err != nil {
		t.Fatalf("create with key after commit = %v, want success", err)
	}
	if p == nil || p.Alias != "blog" || p.Identity != "anonymous" || p.Name != "blog" ||
		len(p.AllowedOrigins) != 1 || !strings.HasPrefix(key, "ak_") {
		t.Fatalf("returned %+v, key %q; want the committed project", p, key)
	}
	// Ordered: later steps act on what earlier ones created.
	for _, step := range []struct {
		name string
		op   func() error
	}{
		{"CreateProject", func() error { _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "docs"}); return err }},
		{"UpdateProject", func() error {
			p, err := ops.UpdateProject(ctx, "cli", ProjectSpec{Alias: "seed", Name: "Seed"})
			if err == nil && (p == nil || p.Name != "Seed") {
				return errors.New("update returned the pre-write project")
			}
			return err
		}},
		{"IssueIngestKey", func() error { _, err := ops.IssueIngestKey(ctx, "cli", "seed", "web"); return err }},
		{"DisableIngestKey", func() error { return ops.DisableIngestKey(ctx, "cli", "seed", "web") }},
		{"EnableIngestKey", func() error { return ops.EnableIngestKey(ctx, "cli", "seed", "web") }},
		{"ArchiveProject", func() error { return ops.ArchiveProject(ctx, "cli", "seed") }},
		{"RestoreProject", func() error { return ops.RestoreProject(ctx, "cli", "seed") }},
		{"RenameProject", func() error { return ops.RenameProject(ctx, "cli", "docs", "handbook") }},
		{"DeleteProject", func() error { return ops.DeleteProject(ctx, "cli", "handbook") }},
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
	if s.Project("blog") == nil || s.Project("seed") == nil || s.Project("seed").Name != "Seed" {
		t.Fatalf("snapshot after recovery missing committed writes: %+v", s.Projects())
	}
	if s.Project("docs") != nil {
		t.Error("renamed-then-deleted project still in snapshot")
	}
}

func loggerTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }
