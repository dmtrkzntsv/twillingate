package manage

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func snapshotOrder(ctx context.Context, reg *Registry) []int64 {
	var ids []int64
	for _, p := range reg.Snapshot(ctx).Projects() {
		ids = append(ids, p.ID)
	}
	return ids
}

// MoveProject places a project after another (0: first), archived
// projects keeping their place among the rest, and the snapshot shows the
// order at once.
func TestMoveProject(t *testing.T) {
	ops, _, reg := newOps(t)
	ctx := context.Background()
	var ids []int64
	for _, n := range []string{"a", "b", "c", "d"} {
		p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: n})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]
	if err := ops.ArchiveProject(ctx, "cli", b); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		id, after int64
		want      []int64
	}{
		{d, 0, []int64{d, a, b, c}},
		{a, c, []int64{d, b, c, a}},
		{d, a, []int64{b, c, a, d}},
		{c, c, []int64{b, c, a, d}}, // after itself: unchanged
		{b, a, []int64{c, a, b, d}}, // an archived project moves too
	} {
		if err := ops.MoveProject(ctx, "api", step.id, step.after); err != nil {
			t.Fatalf("move %d after %d: %v", step.id, step.after, err)
		}
		if got := snapshotOrder(ctx, reg); !reflect.DeepEqual(got, step.want) {
			t.Errorf("move %d after %d: order = %v, want %v", step.id, step.after, got, step.want)
		}
	}
	if err := ops.MoveProject(ctx, "api", 99, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: %v, want ErrNotFound", err)
	}
	if err := ops.MoveProject(ctx, "api", a, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown after: %v, want ErrNotFound", err)
	}
}
