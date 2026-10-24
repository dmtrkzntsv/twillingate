package archive

import (
	"cmp"
	"slices"
)

// One project's identities and aliases, folded from its events one at a
// time as derive.go defines them. A map is one
// part (one worker's events, one file's); Fold adds another part to it.

// IdentityKey is one identity: a user's id, or a group's.
type IdentityKey struct {
	Group bool
	ID    string
}

// Identity is one identity's row: its name and when that was sent (within
// one part, the name's event; across parts, the part's last_seen), its
// first and last event (unix microseconds), its views and product events.
type Identity struct {
	Name          string
	Named         bool
	NameAt        int64
	First, Last   int64
	Views, Events int64
}

// IdentityBytes is about what an identity costs in a map, its strings
// aside.
const IdentityBytes = 112

// IdentityMap is a set of identities by key.
type IdentityMap map[IdentityKey]Identity

// See adds one event of identity k to m, as the identities aggregate
// one part (derive.go): the name of the latest event that carried one
// (arg_max(name, ts) FILTER (WHERE name IS NOT NULL)), the first and last
// event, the views and product events counted. The caller passes only the
// views and product events with a non-empty id and a ts. It returns the
// bytes m grew by. k's id is kept, not copied.
func (m IdentityMap) See(k IdentityKey, name string, named bool, ts int64, view bool) int64 {
	cur, ok := m[k]
	var grew int64
	if !ok {
		cur = Identity{First: ts, Last: ts}
		grew = IdentityBytes + int64(len(k.ID))
	}
	if named && (!cur.Named || ts > cur.NameAt) {
		grew += int64(len(name) - len(cur.Name))
		cur.Name, cur.Named, cur.NameAt = name, true, ts
	}
	cur.First, cur.Last = min(cur.First, ts), max(cur.Last, ts)
	if view {
		cur.Views++
	} else {
		cur.Events++
	}
	m[k] = cur
	return grew
}

// Fold adds o's identities to m within one part: the name of the later
// event, as See takes it.
func (m IdentityMap) Fold(o IdentityMap) {
	for k, x := range o {
		cur, ok := m[k]
		if !ok {
			m[k] = x
			continue
		}
		if x.Named && (!cur.Named || x.NameAt > cur.NameAt) {
			cur.Name, cur.Named, cur.NameAt = x.Name, true, x.NameAt
		}
		cur.First, cur.Last = min(cur.First, x.First), max(cur.Last, x.Last)
		cur.Views += x.Views
		cur.Events += x.Events
		m[k] = cur
	}
}

// Bytes is about what m takes.
func (m IdentityMap) Bytes() int64 {
	var n int64
	for k, x := range m {
		n += IdentityBytes + int64(len(k.ID)+len(x.Name))
	}
	return n
}

// AliasKey is one (user_id, install_id) pair.
type AliasKey struct {
	UserID, InstallID string
}

// Alias is one pair's row: its first and last event (unix microseconds;
// Timed false while no event of it had a ts, both then NULL) and the
// events that carried it.
type Alias struct {
	First, Last int64
	Timed       bool
	Events      int64
}

// AliasBytes is about what an alias costs in a map, its strings aside.
const AliasBytes = 96

// AliasMap is a set of aliases by pair.
type AliasMap map[AliasKey]Alias

// See adds one event, of any family, to m, as the aliases aggregate it
// (derive.go): an event whose user_id and install_id are both non-empty (NULL is
// "") counts for that pair, its ts (tsOK false for NULL) widening the
// pair's first and last seen. It returns the bytes m grew by. The ids are
// kept, not copied.
func (m AliasMap) See(userID, installID string, ts int64, tsOK bool) int64 {
	if userID == "" || installID == "" {
		return 0
	}
	k := AliasKey{userID, installID}
	cur, ok := m[k]
	var grew int64
	if !ok {
		grew = AliasBytes + int64(len(userID)+len(installID))
	}
	cur.see(ts, tsOK)
	cur.Events++
	m[k] = cur
	return grew
}

func (a *Alias) see(ts int64, ok bool) {
	switch {
	case !ok:
	case !a.Timed:
		a.First, a.Last, a.Timed = ts, ts, true
	default:
		a.First, a.Last = min(a.First, ts), max(a.Last, ts)
	}
}

// Fold adds o's aliases to m.
func (m AliasMap) Fold(o AliasMap) {
	for k, x := range o {
		cur, ok := m[k]
		if !ok {
			m[k] = x
			continue
		}
		if x.Timed {
			cur.see(x.First, true)
			cur.see(x.Last, true)
		}
		cur.Events += x.Events
		m[k] = cur
	}
}

// Keys is m's pairs in the aliases' order (derive.go): by user_id, then
// install_id.
func (m AliasMap) Keys() []AliasKey {
	keys := make([]AliasKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b AliasKey) int {
		if c := cmp.Compare(a.UserID, b.UserID); c != 0 {
			return c
		}
		return cmp.Compare(a.InstallID, b.InstallID)
	})
	return keys
}
