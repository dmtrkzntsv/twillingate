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
