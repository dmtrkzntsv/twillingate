package manage

import (
	"context"
	"errors"
	"testing"
)

func keys(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + string(rune('a'+i))
	}
	return out
}

// ATTRIBUTE_BREAKDOWNS_MAX counts the attributes declared across active
// projects: a save that adds past it is refused, one that keeps or
// removes keys passes even over the limit, archived projects don't count
// but adding to one is checked as if it were active, and restore is never
// refused.
func TestBreakdownsLimit(t *testing.T) {
	ops, _, reg := newOps(t)
	ctx := context.Background()
	ops.BreakdownsMax = 3

	a, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "a", Attributes: keys(2, "a")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "b", Attributes: keys(2, "b")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create past the limit: err = %v, want ErrInvalid", err)
	}
	b, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "b", Attributes: keys(1, "b")})
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).BreakdownsInUse(); got != 3 {
		t.Fatalf("in use = %d, want 3", got)
	}
	// Swapping one key for another adds one and removes one: still 3.
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: a.ID, Attributes: []string{"aa", "zz"}}); err != nil {
		t.Fatalf("swap within the limit: %v", err)
	}
	// Over the limit (lowered setting): saves that add nothing still pass.
	ops.BreakdownsMax = 1
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: a.ID, Name: "renamed"}); err != nil {
		t.Fatalf("rename over the limit: %v", err)
	}
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: a.ID, Attributes: []string{"aa"}}); err != nil {
		t.Fatalf("remove over the limit: %v", err)
	}
	// Archived projects don't count, and restore is never refused…
	if err := ops.ArchiveProject(ctx, "t", b.ID); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).BreakdownsInUse(); got != 1 {
		t.Fatalf("in use after archive = %d, want 1", got)
	}
	// …but adding to an archived project is checked as if it were active.
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: b.ID, Attributes: []string{"ba", "bb"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("add to archived past the limit: err = %v, want ErrInvalid", err)
	}
	if err := ops.RestoreProject(ctx, "t", b.ID); err != nil {
		t.Fatalf("restore over the limit: %v", err)
	}
	// 0 is no limit.
	ops.BreakdownsMax = 0
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: a.ID, Attributes: keys(10, "a")}); err != nil {
		t.Fatalf("no limit: %v", err)
	}
}

// Adding to an archived project counts that project's keys on top of the
// active total: its own current keys are not subtracted, because they were
// never in the total. Max 2, active total 1, archived project with one key
// adding a second: 1 + 2 = 3 is refused.
func TestBreakdownsArchivedAddCountsItsOwnKeys(t *testing.T) {
	ops, _, reg := newOps(t)
	ctx := context.Background()

	if _, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "a", Attributes: keys(1, "a")}); err != nil {
		t.Fatal(err)
	}
	b, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "b", Attributes: keys(1, "b")})
	if err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "t", b.ID); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).BreakdownsInUse(); got != 1 {
		t.Fatalf("in use = %d, want 1", got)
	}
	ops.BreakdownsMax = 2
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: b.ID, Attributes: []string{"ba", "bb"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("add to archived project: err = %v, want ErrInvalid", err)
	}
}

// Duplicate attribute keys collapse on save, first occurrence first, so the
// stored list, BreakdownsInUse and the limit all count each key once.
func TestAttributesDeduplicated(t *testing.T) {
	ops, _, reg := newOps(t)
	ctx := context.Background()
	ops.BreakdownsMax = 4

	if _, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "a", Attributes: keys(3, "a")}); err != nil {
		t.Fatal(err)
	}
	b, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "b", Attributes: []string{"$path", "$path", "$path"}})
	if err != nil {
		t.Fatalf("create with duplicates: %v", err)
	}
	if len(b.Attributes) != 1 || b.Attributes[0] != "$path" {
		t.Fatalf("returned attributes = %v, want [$path]", b.Attributes)
	}
	snap := reg.Snapshot(ctx)
	if got := snap.Project(b.ID).Attributes; len(got) != 1 {
		t.Fatalf("stored attributes = %v, want one", got)
	}
	if got := snap.BreakdownsInUse(); got != 4 {
		t.Fatalf("in use = %d, want 4", got)
	}
	ops.BreakdownsMax = 0
	b, err = ops.UpdateProject(ctx, "t", ProjectSpec{ID: b.ID, Attributes: []string{"y", "x", "y", "x", "z"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Attributes; len(got) != 3 || got[0] != "y" || got[1] != "x" || got[2] != "z" {
		t.Fatalf("attributes = %v, want [y x z]", got)
	}
}
