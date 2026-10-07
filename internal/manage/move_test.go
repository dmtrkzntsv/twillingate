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

func keysOf(ctx context.Context, reg *Registry) map[int64]string {
	out := map[int64]string{}
	for _, p := range reg.Snapshot(ctx).Projects() {
		out[p.ID] = p.SortKey
	}
	return out
}

// The first move keys every project; once all are keyed a move rewrites
// only the moved one. A project created later is unkeyed, last, and the
// next move keys everyone again in the order shown.
func TestMoveProjectKeys(t *testing.T) {
	ops, _, reg := newOps(t)
	ctx := context.Background()
	var ids []int64
	for _, n := range []string{"a", "b", "c"} {
		p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: n})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	a, b, c := ids[0], ids[1], ids[2]
	if err := ops.MoveProject(ctx, "api", c, a); err != nil {
		t.Fatal(err)
	}
	before := keysOf(ctx, reg)
	for id, k := range before {
		if k == "" {
			t.Fatalf("project %d unkeyed after the first move: %v", id, before)
		}
	}
	if err := ops.MoveProject(ctx, "api", b, 0); err != nil {
		t.Fatal(err)
	}
	after := keysOf(ctx, reg)
	if after[a] != before[a] || after[c] != before[c] || after[b] == before[b] {
		t.Errorf("keys %v -> %v; want only b's to change", before, after)
	}
	if got := snapshotOrder(ctx, reg); !reflect.DeepEqual(got, []int64{b, a, c}) {
		t.Errorf("order = %v, want %v", got, []int64{b, a, c})
	}

	d, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotOrder(ctx, reg); !reflect.DeepEqual(got, []int64{b, a, c, d.ID}) {
		t.Fatalf("order = %v, want the new project last", got)
	}
	if err := ops.MoveProject(ctx, "api", a, c); err != nil {
		t.Fatal(err)
	}
	if got := snapshotOrder(ctx, reg); !reflect.DeepEqual(got, []int64{b, c, a, d.ID}) {
		t.Errorf("order = %v, want %v", got, []int64{b, c, a, d.ID})
	}
	if k := keysOf(ctx, reg)[d.ID]; k == "" {
		t.Error("the new project is still unkeyed after a move")
	}
}

// Neighbours whose keys leave no key between them (equal keys, written by
// hand or by two processes at once) are keyed afresh rather than refused.
func TestMoveKeysRespreadsWhenNoKeyFits(t *testing.T) {
	rest := []*Project{{ID: 1, SortKey: "a0"}, {ID: 2, SortKey: "a1"}, {ID: 3, SortKey: "a1"}}
	keys, err := moveKeys(rest, 2, 4) // between 2 and 3, which share a1
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 4 {
		t.Fatalf("keys = %v, want all four keyed afresh", keys)
	}
	want := []int64{1, 2, 4, 3}
	for i, k := range keys {
		if k.ID != want[i] || (i > 0 && keys[i-1].SortKey >= k.SortKey) {
			t.Errorf("keys = %v, want ascending for %v", keys, want)
			break
		}
	}
	one, err := moveKeys(rest[:2], 1, 4)
	if err != nil || len(one) != 1 || one[0].SortKey <= "a0" || one[0].SortKey >= "a1" {
		t.Errorf("between a0 and a1 = %v, %v; want one key between them", one, err)
	}
}
