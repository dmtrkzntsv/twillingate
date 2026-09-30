package reporting

import (
	"slices"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// order is one owner's dashboards in sort_key order, archived included.
// A group's rows (same GroupID) are always contiguous in an order: every
// write that places a dashboard keeps them so. Removing a row first
// (without, withoutGroup) before computing a new placement keeps the
// remaining rows contiguous too.
type order []store.Dashboard

// userOrder filters ds (ListDashboards' answer) to owner user.
func userOrder(ds []store.Dashboard) order {
	out := make(order, 0, len(ds))
	for _, d := range ds {
		if d.Owner == store.OwnerUser {
			out = append(out, d)
		}
	}
	return out
}

// group returns the members of group g in order (archived included).
func (o order) group(g int64) []store.Dashboard {
	var out []store.Dashboard
	for _, d := range o {
		if d.GroupID == g {
			out = append(out, d)
		}
	}
	return out
}

// find returns the row with id, or false.
func (o order) find(id int64) (store.Dashboard, bool) {
	for _, d := range o {
		if d.ID == id {
			return d, true
		}
	}
	return store.Dashboard{}, false
}

// without returns o with the row of id removed, if present.
func (o order) without(id int64) order {
	out := make(order, 0, len(o))
	for _, d := range o {
		if d.ID != id {
			out = append(out, d)
		}
	}
	return out
}

// withoutGroup returns o with every row of group g removed.
func (o order) withoutGroup(g int64) order {
	out := make(order, 0, len(o))
	for _, d := range o {
		if d.GroupID != g {
			out = append(out, d)
		}
	}
	return out
}

// keyInGroup returns a sort key that puts a dashboard (self; 0 for a new
// one) into group g: after member `after` (0 = first tab, nil = last).
// Refuses (ErrInvalid) an after that is not a member of g.
//
// members is o with self removed, filtered to g: removing self first
// keeps the group's remaining rows contiguous. after == nil: prev is the
// last member's key, next is the key of the row right after it in
// o-without-self (or "" if it was already last there). after == 0: next
// is the first member's key, prev is the row right before it. Otherwise:
// prev is the named member's key, next is the row right after it. If g
// turns out to have no member besides self (self is the sole occupant of
// its own group and is being placed back into it), self is put back at
// its own current neighbours in o instead.
func (o order) keyInGroup(self, g int64, after *int64) (string, error) {
	rest := o.without(self)
	members := rest.group(g)

	if len(members) == 0 {
		i := slices.IndexFunc(o, func(d store.Dashboard) bool { return d.ID == self })
		var prev, next string
		if i > 0 {
			prev = o[i-1].SortKey
		}
		if i >= 0 && i+1 < len(o) {
			next = o[i+1].SortKey
		}
		return sortkey.Between(prev, next)
	}

	var prev, next string
	switch {
	case after == nil:
		last := members[len(members)-1]
		prev = last.SortKey
		if i := slices.IndexFunc(rest, func(d store.Dashboard) bool { return d.ID == last.ID }); i+1 < len(rest) {
			next = rest[i+1].SortKey
		}
	case *after == 0:
		first := members[0]
		next = first.SortKey
		if i := slices.IndexFunc(rest, func(d store.Dashboard) bool { return d.ID == first.ID }); i > 0 {
			prev = rest[i-1].SortKey
		}
	default:
		idx := slices.IndexFunc(members, func(d store.Dashboard) bool { return d.ID == *after })
		if idx < 0 {
			return "", store.Refuse(store.ErrInvalid, "after %d is not a member of group %d", *after, g)
		}
		m := members[idx]
		prev = m.SortKey
		if i := slices.IndexFunc(rest, func(d store.Dashboard) bool { return d.ID == m.ID }); i+1 < len(rest) {
			next = rest[i+1].SortKey
		}
	}
	return sortkey.Between(prev, next)
}

// moveGroup returns the new keys for every member of group g (archived
// included) placed right after the group of dashboard `after` (0 = top).
// nil keys when the group already sits there. Refuses an after outside
// the order (ErrInvalid "after N is not a user dashboard").
//
// rest is o with g's own rows removed. The target sits at index 0 for
// after == 0, or one past the last row of after's group in rest (an
// after found inside g itself is a no-op: g cannot move relative to its
// own members). If g's rows already have those exact neighbours in o,
// nothing moves. Otherwise every member gets a fresh key from
// sortkey.Spread between the target's neighbours, in its current order;
// GroupID is unchanged.
func (o order) moveGroup(g int64, after int64) ([]store.DashboardKey, error) {
	members := o.group(g)
	if len(members) == 0 {
		return nil, nil
	}
	rest := o.withoutGroup(g)

	target := 0
	if after != 0 {
		row, ok := o.find(after)
		if !ok {
			return nil, store.Refuse(store.ErrInvalid, "after %d is not a user dashboard", after)
		}
		if row.GroupID == g {
			return nil, nil
		}
		afterMembers := rest.group(row.GroupID)
		last := afterMembers[len(afterMembers)-1]
		i := slices.IndexFunc(rest, func(d store.Dashboard) bool { return d.ID == last.ID })
		target = i + 1
	}

	var prevID, nextID int64
	var prevKey, nextKey string
	if target > 0 {
		prevID, prevKey = rest[target-1].ID, rest[target-1].SortKey
	}
	if target < len(rest) {
		nextID, nextKey = rest[target].ID, rest[target].SortKey
	}

	// Where g's block actually sits in o right now: it is contiguous, so
	// its end follows straight from its start and its length.
	idx0 := slices.IndexFunc(o, func(d store.Dashboard) bool { return d.ID == members[0].ID })
	idxLast := idx0 + len(members) - 1
	var actualPrevID, actualNextID int64
	if idx0 > 0 {
		actualPrevID = o[idx0-1].ID
	}
	if idxLast+1 < len(o) {
		actualNextID = o[idxLast+1].ID
	}
	if actualPrevID == prevID && actualNextID == nextID {
		return nil, nil
	}

	keys, err := sortkey.Spread(prevKey, nextKey, len(members))
	if err != nil {
		return nil, err
	}
	out := make([]store.DashboardKey, len(members))
	for i, m := range members {
		out[i] = store.DashboardKey{ID: m.ID, GroupID: g, SortKey: keys[i]}
	}
	return out, nil
}

// keyAfterGroup returns a key for a group of one placed right after the
// group of dashboard `after` (0 = top, nil = last), leaving out self.
//
// rest is o with self removed. nil sits after the last row; 0 sits
// before the first; a dashboard id sits after the last row of its whole
// group, not just after that one dashboard, since a group always moves
// together.
func (o order) keyAfterGroup(self int64, after *int64) (string, error) {
	rest := o.without(self)

	var prev, next string
	switch {
	case after == nil:
		if len(rest) > 0 {
			prev = rest[len(rest)-1].SortKey
		}
	case *after == 0:
		if len(rest) > 0 {
			next = rest[0].SortKey
		}
	default:
		row, ok := rest.find(*after)
		if !ok {
			return "", store.Refuse(store.ErrInvalid, "after %d is not a user dashboard", *after)
		}
		members := rest.group(row.GroupID)
		last := members[len(members)-1]
		i := slices.IndexFunc(rest, func(d store.Dashboard) bool { return d.ID == last.ID })
		prev = rest[i].SortKey
		if i+1 < len(rest) {
			next = rest[i+1].SortKey
		}
	}
	return sortkey.Between(prev, next)
}
