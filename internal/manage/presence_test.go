package manage

import (
	"context"
	"errors"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// filesPresence answers as the event files would: from the names and keys
// events were sent with, read through the registry's current rules.
type filesPresence struct {
	reg    *Registry
	events map[int64]map[store.Family][]string
	keys   map[int64][]string
	err    error
}

func (f *filesPresence) sendEvent(project int64, family store.Family, name string) {
	if f.events == nil {
		f.events = map[int64]map[store.Family][]string{}
	}
	if f.events[project] == nil {
		f.events[project] = map[store.Family][]string{}
	}
	f.events[project][family] = append(f.events[project][family], name)
}

func (f *filesPresence) sendKey(project int64, key string) {
	if f.keys == nil {
		f.keys = map[int64][]string{}
	}
	f.keys[project] = append(f.keys[project], key)
}

func (f *filesPresence) HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	snap := f.reg.Snapshot(ctx)
	for _, sent := range f.events[projectID][family] {
		if got, blocked := snap.ResolveEvent(projectID, family, sent); !blocked && got == name {
			return true, nil
		}
	}
	return false, nil
}

func (f *filesPresence) HasAttribute(ctx context.Context, projectID int64, key string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	rules := f.reg.Snapshot(ctx).AttributeRules(projectID)
	for _, sent := range f.keys[projectID] {
		got := sent
		if to, ok := rules.Renamed[sent]; ok {
			got = to
		}
		if !rules.Blocked[sent] && got == key {
			return true, nil
		}
	}
	return false, nil
}

// presence is the filesPresence ruleOps gave ops.
func presence(ops *Ops) *filesPresence { return ops.Presence.(*filesPresence) }

// Review focus: a name only a rule makes exist. a was renamed to b, so b
// reads as having events although none was sent as b: renaming c onto it
// is a conflict, merging c into it is what the caller meant.
func TestRenameOntoARuledNameIsAConflict(t *testing.T) {
	ops, _, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	presence(ops).sendEvent(p, store.FamilyProduct, "a")
	presence(ops).sendEvent(p, store.FamilyProduct, "c")
	if _, err := ops.RenameEvent(ctx, "t", p, "product", "a", "b"); err != nil {
		t.Fatalf("rename a > b: %v", err)
	}
	_, err := ops.RenameEvent(ctx, "t", p, "product", "c", "b")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("rename c > b: err = %v, want ErrConflict", err)
	}
	if want := "rename c to b: b already has events; merge c into it instead"; err.Error()[:len(want)] != want {
		t.Errorf("err = %q, want it to start %q", err, want)
	}
	if _, err := ops.MergeEvent(ctx, "t", p, "product", "c", "b"); err != nil {
		t.Fatalf("merge c > b: %v", err)
	}
	if got, _ := reg.Snapshot(ctx).ResolveEvent(p, store.FamilyProduct, "c"); got != "b" {
		t.Errorf("c resolves to %q, want b", got)
	}
	// Another family's b is no conflict.
	if _, err := ops.RenameEvent(ctx, "t", p, "measures", "c", "b"); err != nil {
		t.Errorf("rename a measure onto a product's ruled name: %v", err)
	}

	presence(ops).sendKey(p, "plan")
	presence(ops).sendKey(p, "tier")
	if _, err := ops.RenameAttribute(ctx, "t", p, "plan", "level"); err != nil {
		t.Fatalf("rename plan > level: %v", err)
	}
	if _, err := ops.RenameAttribute(ctx, "t", p, "tier", "level"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename tier > level: err = %v, want ErrConflict", err)
	}
	if _, err := ops.MergeAttribute(ctx, "t", p, "tier", "level"); err != nil {
		t.Fatalf("merge tier > level: %v", err)
	}
}

// A presence that cannot answer stops the rename with its error, which is
// no conflict, and nothing is written. A merge does not ask.
func TestRenameStopsWhenPresenceFails(t *testing.T) {
	ops, _, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	down := errors.New("engine down")
	presence(ops).err = down
	if _, err := ops.RenameEvent(ctx, "t", p, "product", "a", "b"); !errors.Is(err, down) || errors.Is(err, ErrConflict) {
		t.Errorf("rename event: err = %v, want the presence's error", err)
	}
	if _, err := ops.RenameAttribute(ctx, "t", p, "a", "b"); !errors.Is(err, down) || errors.Is(err, ErrConflict) {
		t.Errorf("rename attribute: err = %v, want the presence's error", err)
	}
	rules := reg.Snapshot(ctx).Rules()
	if events, attrs := rules.Events, rules.Attributes; len(events) != 0 || len(attrs) != 0 {
		t.Errorf("a failed check wrote rules: %v %v", events, attrs)
	}
	if _, err := ops.MergeEvent(ctx, "t", p, "product", "a", "b"); err != nil {
		t.Errorf("merge event: %v", err)
	}
	if _, err := ops.MergeAttribute(ctx, "t", p, "a", "b"); err != nil {
		t.Errorf("merge attribute: %v", err)
	}
}

// Without a presence (a process with no engine) nothing is checked.
func TestRenameWithoutPresence(t *testing.T) {
	ops, _, _, p, _ := ruleOps(t)
	ctx := context.Background()
	presence(ops).sendEvent(p, store.FamilyProduct, "b")
	ops.Presence = nil
	if _, err := ops.RenameEvent(ctx, "t", p, "product", "a", "b"); err != nil {
		t.Errorf("rename with no presence: %v", err)
	}
}

// A rename or a merge onto a name or key that is itself renamed to a third
// one is a conflict; nothing is written.
func TestMoveOntoARenamedTargetIsAConflict(t *testing.T) {
	ops, _, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	for _, step := range []func() error{
		func() error { _, err := ops.RenameEvent(ctx, "t", p, "product", "a", "b"); return err },
		func() error { _, err := ops.RenameEvent(ctx, "t", p, "product", "b", "x"); return err },
		func() error { _, err := ops.RenameAttribute(ctx, "t", p, "a", "b"); return err },
		func() error { _, err := ops.RenameAttribute(ctx, "t", p, "b", "x"); return err },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	before := reg.Snapshot(ctx).Rules()
	for name, move := range map[string]func() error{
		"rename event":     func() error { _, err := ops.RenameEvent(ctx, "t", p, "product", "c", "b"); return err },
		"merge event":      func() error { _, err := ops.MergeEvent(ctx, "t", p, "product", "c", "b"); return err },
		"rename attribute": func() error { _, err := ops.RenameAttribute(ctx, "t", p, "c", "b"); return err },
		"merge attribute":  func() error { _, err := ops.MergeAttribute(ctx, "t", p, "c", "b"); return err },
	} {
		if err := move(); !errors.Is(err, ErrConflict) {
			t.Errorf("%s: err = %v, want ErrConflict", name, err)
		}
	}
	if after := reg.Snapshot(ctx).Rules(); len(after.Events) != len(before.Events) || len(after.Attributes) != len(before.Attributes) {
		t.Errorf("refusals wrote rules: %v %v", after.Events, after.Attributes)
	}
}
