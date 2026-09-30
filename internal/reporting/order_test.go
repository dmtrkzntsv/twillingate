package reporting

import (
	"errors"
	"sort"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func row(id, group int64, key string) store.Dashboard {
	return store.Dashboard{ID: id, Owner: store.OwnerUser, GroupID: group, SortKey: key}
}

func archivedRow(id, group int64, key string) store.Dashboard {
	d := row(id, group, key)
	d.ArchivedAt = "2026-01-01T00:00:00Z"
	return d
}

// contiguous reports whether every group's rows sit together in o, with
// no other group's row landing between two of a group's rows.
func contiguous(o order) bool {
	closed := map[int64]bool{}
	for i, d := range o {
		if closed[d.GroupID] {
			return false
		}
		if i+1 == len(o) || o[i+1].GroupID != d.GroupID {
			closed[d.GroupID] = true
		}
	}
	return true
}

func TestOrderUserOrderFiltersToUserOwner(t *testing.T) {
	ds := []store.Dashboard{
		{ID: 1, Owner: store.OwnerSystem, GroupID: 1, SortKey: "a0"},
		{ID: 1001, Owner: store.OwnerUser, GroupID: 1001, SortKey: "a1"},
		{ID: 1002, Owner: store.OwnerUser, GroupID: 1001, SortKey: "a2"},
	}
	got := userOrder(ds)
	if len(got) != 2 || got[0].ID != 1001 || got[1].ID != 1002 {
		t.Fatalf("userOrder = %+v", got)
	}
}

func TestOrderGroupAndFind(t *testing.T) {
	o := order{
		row(1, 1, "a0"),
		row(2, 1, "a1"),
		row(3, 3, "a2"),
	}
	if g := o.group(1); len(g) != 2 || g[0].ID != 1 || g[1].ID != 2 {
		t.Fatalf("group(1) = %+v", g)
	}
	if g := o.group(9); g != nil {
		t.Fatalf("group(9) = %+v, want nil", g)
	}
	if d, ok := o.find(3); !ok || d.SortKey != "a2" {
		t.Fatalf("find(3) = %+v, %v", d, ok)
	}
	if _, ok := o.find(99); ok {
		t.Fatalf("find(99) found a row")
	}
}

func TestOrderKeyInGroupLastFirstAfter(t *testing.T) {
	// group 1: rows 1,2,3; group 4: row 4 alone.
	o := order{
		row(1, 1, "a0"),
		row(2, 1, "a1"),
		row(3, 1, "a2"),
		row(4, 4, "a3"),
	}

	t.Run("last", func(t *testing.T) {
		key, err := o.keyInGroup(0, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !(o[2].SortKey < key && key < o[3].SortKey) {
			t.Fatalf("key %q not between %q and %q", key, o[2].SortKey, o[3].SortKey)
		}
	})

	t.Run("first", func(t *testing.T) {
		zero := int64(0)
		key, err := o.keyInGroup(0, 1, &zero)
		if err != nil {
			t.Fatal(err)
		}
		if !(key < o[0].SortKey) {
			t.Fatalf("key %q not before %q", key, o[0].SortKey)
		}
	})

	t.Run("after a member", func(t *testing.T) {
		after := int64(1)
		key, err := o.keyInGroup(0, 1, &after)
		if err != nil {
			t.Fatal(err)
		}
		if !(o[0].SortKey < key && key < o[1].SortKey) {
			t.Fatalf("key %q not between %q and %q", key, o[0].SortKey, o[1].SortKey)
		}
	})
}

func TestOrderKeyInGroupAfterNotMemberRefuses(t *testing.T) {
	o := order{
		row(1, 1, "a0"),
		row(4, 4, "a1"),
	}
	after := int64(4)
	if _, err := o.keyInGroup(0, 1, &after); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestOrderKeyInGroupSoleMemberKeepsOwnNeighbours(t *testing.T) {
	// self (2) is the only member of group 2, between groups 1 and 3.
	o := order{
		row(1, 1, "a0"),
		row(2, 2, "a1"),
		row(3, 3, "a2"),
	}
	key, err := o.keyInGroup(2, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !(o[0].SortKey < key && key < o[2].SortKey) {
		t.Fatalf("key %q not between %q and %q", key, o[0].SortKey, o[2].SortKey)
	}

	t.Run("after 0 also keeps own neighbours", func(t *testing.T) {
		zero := int64(0)
		key, err := o.keyInGroup(2, 2, &zero)
		if err != nil {
			t.Fatal(err)
		}
		if !(o[0].SortKey < key && key < o[2].SortKey) {
			t.Fatalf("key %q not between %q and %q", key, o[0].SortKey, o[2].SortKey)
		}
	})
}

// TestOrderKeyInGroupSoleMemberRefusesForeignAfter covers the "self is the
// sole member of g" branch: spec decision 8 refuses an after that is not
// a member of the target group even when that group is empty after
// removing self, so {group_id: <own group>, after: 999} must not silently
// succeed.
func TestOrderKeyInGroupSoleMemberRefusesForeignAfter(t *testing.T) {
	// self (2) is the only member of group 2, between groups 1 and 3.
	o := order{
		row(1, 1, "a0"),
		row(2, 2, "a1"),
		row(3, 3, "a2"),
	}

	t.Run("after names a member of another group", func(t *testing.T) {
		after := int64(1) // a member of group 1, not group 2
		if _, err := o.keyInGroup(2, 2, &after); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
	})

	t.Run("after names an id not in the order", func(t *testing.T) {
		after := int64(999)
		if _, err := o.keyInGroup(2, 2, &after); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
	})
}

func TestOrderKeyInGroupIntoOtherGroupStaysContiguous(t *testing.T) {
	o := order{
		row(1, 1, "a0"),
		row(2, 1, "a1"),
		row(3, 1, "a2"),
		row(4, 4, "a3"),
		row(5, 4, "a4"),
	}
	after := int64(4)
	key, err := o.keyInGroup(2, 4, &after) // move dashboard 2 into group 4, after dashboard 4
	if err != nil {
		t.Fatal(err)
	}
	if !(o[3].SortKey < key && key < o[4].SortKey) {
		t.Fatalf("key %q not inside group 4's block (%q, %q)", key, o[3].SortKey, o[4].SortKey)
	}

	// Re-key self and re-sort as a caller would before writing: both
	// groups must stay contiguous.
	next := make(order, 0, len(o))
	for _, d := range o {
		if d.ID != 2 {
			next = append(next, d)
		}
	}
	next = append(next, row(2, 4, key))
	sort.Slice(next, func(i, j int) bool { return next[i].SortKey < next[j].SortKey })
	if !contiguous(next) {
		t.Fatalf("not contiguous: %+v", next)
	}
}

func TestOrderMoveGroupToTop(t *testing.T) {
	o := order{
		row(1, 1, "a0"),
		row(2, 1, "a1"),
		row(3, 3, "a2"),
	}
	keys, err := o.moveGroup(3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].ID != 3 || keys[0].GroupID != 3 {
		t.Fatalf("keys = %+v", keys)
	}
	if !(keys[0].SortKey < o[0].SortKey) {
		t.Fatalf("key %q not before %q", keys[0].SortKey, o[0].SortKey)
	}
}

func TestOrderMoveGroupAfterAnotherGroup(t *testing.T) {
	o := order{
		row(1, 1, "a0"),
		row(2, 2, "a1"),
		row(3, 3, "a2"),
	}
	keys, err := o.moveGroup(3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %+v", keys)
	}
	if !(o[0].SortKey < keys[0].SortKey && keys[0].SortKey < o[1].SortKey) {
		t.Fatalf("key %q not between group 1 and group 2", keys[0].SortKey)
	}
}

func TestOrderMoveGroupAfterOwnMemberIsNoop(t *testing.T) {
	o := order{
		row(1, 1, "a0"),
		row(2, 1, "a1"),
		row(3, 3, "a2"),
	}
	keys, err := o.moveGroup(1, 2)
	if err != nil || keys != nil {
		t.Fatalf("keys, err = %+v, %v; want nil, nil", keys, err)
	}
}

func TestOrderMoveGroupAlreadyThereIsNoop(t *testing.T) {
	o := order{
		row(1, 1, "a0"),
		row(2, 1, "a1"),
		row(3, 3, "a2"),
	}
	// group 3 already sits right after group 1 (there is nothing else).
	keys, err := o.moveGroup(3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if keys != nil {
		t.Fatalf("keys = %+v, want nil (already there)", keys)
	}
}

func TestOrderMoveGroupCarriesArchivedMembers(t *testing.T) {
	o := order{
		row(1, 1, "a0"),
		row(2, 2, "a1"),
		archivedRow(3, 2, "a2"),
	}
	keys, err := o.moveGroup(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, k := range keys {
		ids[k.ID] = true
	}
	if len(keys) != 2 || !ids[2] || !ids[3] {
		t.Fatalf("keys = %+v, want both 2 (live) and 3 (archived)", keys)
	}
}

func TestOrderMoveGroupRefusesAfterOutsideOrder(t *testing.T) {
	o := order{row(1, 1, "a0")}
	if _, err := o.moveGroup(1, 99); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestOrderKeyAfterGroup(t *testing.T) {
	o := order{
		row(1, 1, "a0"),
		row(2, 1, "a1"),
		row(3, 1, "a2"),
		row(4, 4, "a3"),
	}

	t.Run("nil - after the last row", func(t *testing.T) {
		key, err := o.keyAfterGroup(0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !(key > o[3].SortKey) {
			t.Fatalf("key %q not after %q", key, o[3].SortKey)
		}
	})

	t.Run("0 - before the first row", func(t *testing.T) {
		zero := int64(0)
		key, err := o.keyAfterGroup(0, &zero)
		if err != nil {
			t.Fatal(err)
		}
		if !(key < o[0].SortKey) {
			t.Fatalf("key %q not before %q", key, o[0].SortKey)
		}
	})

	t.Run("after a member of a 3-row group lands after the whole group", func(t *testing.T) {
		after := int64(2) // a member of the 3-row group (1,2,3), not its last row
		key, err := o.keyAfterGroup(0, &after)
		if err != nil {
			t.Fatal(err)
		}
		if !(o[2].SortKey < key && key < o[3].SortKey) {
			t.Fatalf("key %q not right after group 1's block (%q..%q)", key, o[2].SortKey, o[3].SortKey)
		}
	})
}

func TestOrderKeyAfterGroupRefusesUnknownAfter(t *testing.T) {
	o := order{row(1, 1, "a0")}
	after := int64(99)
	if _, err := o.keyAfterGroup(0, &after); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// An after naming an archived dashboard is refused by every placement
// (spec decision 8), whether it names a tab, another group or the
// group being moved; the archived rows still count for the keys.
func TestOrderRefusesArchivedAfter(t *testing.T) {
	o := order{
		row(1, 1, "a0"), archivedRow(2, 1, "a1"),
		row(3, 3, "b0"), archivedRow(4, 3, "b1"),
	}
	archivedTab, archivedOther := int64(2), int64(4)
	wantArchived := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
	}
	t.Run("keyInGroup", func(t *testing.T) {
		_, err := o.keyInGroup(0, 1, &archivedTab)
		wantArchived(t, err)
	})
	t.Run("moveGroup after another group's archived row", func(t *testing.T) {
		_, err := o.moveGroup(1, archivedOther)
		wantArchived(t, err)
	})
	t.Run("moveGroup after its own archived row", func(t *testing.T) {
		_, err := o.moveGroup(1, archivedTab)
		wantArchived(t, err)
	})
	t.Run("keyAfterGroup", func(t *testing.T) {
		_, err := o.keyAfterGroup(0, &archivedOther)
		wantArchived(t, err)
	})
}
