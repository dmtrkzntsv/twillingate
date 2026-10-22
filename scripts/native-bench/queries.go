package main

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Workers is how many goroutines a query scans row groups with; the query
// command sets it from -workers. The rows a query returns do not depend on it.
var Workers = 2

// nullID is the id of a null (or skipped) value in a remapped dictionary.
const nullID = ^uint32(0)

// interner maps strings to ids that are stable across batches, whose
// dictionaries differ, and across workers: the mutex is held while one
// batch's dictionary is remapped, never per row. Each column has its own
// interner, so each has its own id space and limit.
type interner struct {
	mu   sync.Mutex
	what string
	max  int // most distinct strings it may hold
	ids  map[string]uint32
	strs []string
}

func newInterner(what string, max int) *interner {
	return &interner{what: what, max: max, ids: map[string]uint32{}}
}

// remap returns in dst, per dictionary entry, its global id; an entry equal
// to skip (when skip is non-nil) maps to nullID and is not interned.
func (in *interner) remap(dict []string, skip *string, dst []uint32) ([]uint32, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	dst = dst[:0]
	for _, s := range dict {
		if skip != nil && s == *skip {
			dst = append(dst, nullID)
			continue
		}
		id, ok := in.ids[s]
		if !ok {
			if len(in.strs) >= in.max {
				return nil, fmt.Errorf("more than %d distinct %s", in.max, in.what)
			}
			id = uint32(len(in.strs))
			in.ids[s] = id
			in.strs = append(in.strs, s)
		}
		dst = append(dst, id)
	}
	return dst, nil
}

// remapCol remaps a batch column's dictionary; a column the batch lacks
// reads as all null (ids nil).
func (in *interner) remapCol(c *Column, skip *string, dst []uint32) ([]uint32, error) {
	if c == nil {
		return dst[:0], nil
	}
	return in.remap(c.Dict, skip, dst)
}

// idAt is row i's global id in a remapped column, nullID for null.
func idAt(c *Column, ids []uint32, i int) uint32 {
	if c == nil {
		return nullID
	}
	if x := c.Idx[i]; x >= 0 {
		return ids[x]
	}
	return nullID
}

func intAt(c *Column, i int) (int64, bool) {
	if c.Null != nil && c.Null[i] {
		return 0, false
	}
	return c.Ints[i], true
}

var noString = ""

// keySink is one worker's packed keys. Keys arrive one row group at a time,
// deduplicated within the group; once the slice is twice its size after the
// last compaction (and past compactAt keys), it is sorted and compacted in
// place, so memory follows the distinct keys rather than the rows.
type keySink struct {
	keys      []uint64
	compacted int
}

// compactAt is a variable so a test can lower it.
var compactAt = 1 << 20

// addGroup sorts and dedupes one group's keys (in place) and appends them.
func (k *keySink) addGroup(group []uint64) {
	k.keys = append(k.keys, sortedUnique(group)...)
	if len(k.keys) > compactAt && len(k.keys) > 2*k.compacted {
		k.keys = sortedUnique(k.keys)
		k.compacted = len(k.keys)
	}
}

func sortedUnique(s []uint64) []uint64 {
	slices.Sort(s)
	return slices.Compact(s)
}

// mergeKeys concatenates the workers' keys and returns them sorted, each once.
func mergeKeys(sinks []keySink) []uint64 {
	n := 0
	for _, s := range sinks {
		n += len(s.keys)
	}
	all := make([]uint64, 0, n)
	for i := range sinks {
		all = append(all, sinks[i].keys...)
		sinks[i].keys = nil
	}
	return sortedUnique(all)
}

func workers() int { return max(1, Workers) }

// Countries is views/countries.sql: visitors distinct per day and country,
// added up per country; empty and null countries out; top 20.
//
// Key: (day-from)<<48 | country<<32 | actor. A distinct key with a non-null
// actor is one visitor of that country on that day; a (day, country) with
// only null actors still makes the country, with 0.
func Countries(src Source, files []string, from, to int64) ([]Row, error) {
	if to-from > 0xFFFF {
		return nil, fmt.Errorf("countries: %d days do not fit the key's 16 bits", to-from+1)
	}
	countries := newInterner("countries", 0xFFFF)
	actors := newInterner("actors", int(nullID))
	type state struct {
		sink   keySink
		buf    []uint64
		cm, am []uint32
	}
	ws := make([]state, workers())
	err := ScanParallel(src, files, []string{"day", "country", "actor_id"}, "views", from, to, len(ws),
		func(w int, b *Batch) error {
			s := &ws[w]
			day, country, actor := b.Cols["day"], b.Cols["country"], b.Cols["actor_id"]
			var err error
			if s.cm, err = countries.remapCol(country, &noString, s.cm); err != nil {
				return err
			}
			if s.am, err = actors.remapCol(actor, nil, s.am); err != nil {
				return err
			}
			buf := s.buf[:0]
			for i := 0; i < b.N; i++ {
				c := idAt(country, s.cm, i)
				if c == nullID {
					continue
				}
				d, ok := intAt(day, i)
				if !ok {
					continue
				}
				off := d - from
				if off < 0 || off > 0xFFFF {
					return fmt.Errorf("countries: day %d outside [%d, %d]", d, from, to)
				}
				buf = append(buf, uint64(off)<<48|uint64(c)<<32|uint64(idAt(actor, s.am, i)))
			}
			s.sink.addGroup(buf)
			s.buf = buf
			return nil
		})
	if err != nil {
		return nil, err
	}
	sinks := make([]keySink, len(ws))
	for i := range ws {
		sinks[i] = ws[i].sink
	}
	keys := mergeKeys(sinks)
	sums := make([]int64, len(countries.strs))
	seen := make([]bool, len(countries.strs))
	for _, k := range keys {
		c := uint32(k>>32) & 0xFFFF
		seen[c] = true
		if uint32(k) != nullID {
			sums[c]++
		}
	}
	type lv struct {
		label string
		value int64
	}
	var all []lv
	for c, ok := range seen {
		if ok {
			all = append(all, lv{countries.strs[c], sums[c]})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].value != all[j].value {
			return all[i].value > all[j].value
		}
		return all[i].label < all[j].label
	})
	if len(all) > 20 {
		all = all[:20]
	}
	rows := make([]Row, len(all))
	for i, e := range all {
		rows[i] = Row{e.label, strconv.FormatInt(e.value, 10)}
	}
	return rows, nil
}

// DAU is product/daily-active-users.sql: distinct actors per day.
//
// Key: (day-from)<<32 | actor; a day with only null actors still has a row,
// with 0.
func DAU(src Source, files []string, from, to int64) ([]Row, error) {
	actors := newInterner("actors", int(nullID))
	type state struct {
		sink keySink
		buf  []uint64
		am   []uint32
	}
	ws := make([]state, workers())
	err := ScanParallel(src, files, []string{"day", "actor_id"}, "product", from, to, len(ws),
		func(w int, b *Batch) error {
			s := &ws[w]
			day, actor := b.Cols["day"], b.Cols["actor_id"]
			var err error
			if s.am, err = actors.remapCol(actor, nil, s.am); err != nil {
				return err
			}
			buf := s.buf[:0]
			for i := 0; i < b.N; i++ {
				d, ok := intAt(day, i)
				if !ok {
					continue
				}
				off := d - from
				if off < 0 || off > 0xFFFFFFFF {
					return fmt.Errorf("dau: day %d outside [%d, %d]", d, from, to)
				}
				buf = append(buf, uint64(off)<<32|uint64(idAt(actor, s.am, i)))
			}
			s.sink.addGroup(buf)
			s.buf = buf
			return nil
		})
	if err != nil {
		return nil, err
	}
	sinks := make([]keySink, len(ws))
	for i := range ws {
		sinks[i] = ws[i].sink
	}
	var rows []Row
	var cur, n int64 = -1, 0
	flush := func() {
		if cur >= 0 {
			rows = append(rows, Row{formatDay(from + cur), strconv.FormatInt(n, 10)})
		}
	}
	for _, k := range mergeKeys(sinks) { // sorted, so days ascend
		if d := int64(k >> 32); d != cur {
			flush()
			cur, n = d, 0
		}
		if uint32(k) != nullID {
			n++
		}
	}
	flush()
	return rows, nil
}

func formatDay(d int64) string {
	return time.Unix(d*86400, 0).UTC().Format("2006-01-02")
}

// pairs gives each (attribute id, value id) a dense id, shared by workers
// and filled once per dictionary entry under its mutex.
type pairs struct {
	mu   sync.Mutex
	ids  map[uint64]uint32
	list []uint64 // pair id -> attr<<32 | value
}

// remap returns, per value id in vids, its pair id with attr, and how many
// pairs exist (so a worker can size its per-pair counts).
func (p *pairs) remap(attr uint32, vids []uint32, dst []uint32) ([]uint32, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	dst = dst[:0]
	for _, v := range vids {
		k := uint64(attr)<<32 | uint64(v)
		id, ok := p.ids[k]
		if !ok {
			if len(p.list) >= int(nullID) {
				return nil, 0, fmt.Errorf("more than %d attribute values", nullID)
			}
			id = uint32(len(p.list))
			p.ids[k] = id
			p.list = append(p.list, k)
		}
		dst = append(dst, id)
	}
	return dst, len(p.list), nil
}

// AttributeValues is product/attribute-values.sql: per (attribute, value)
// the events, distinct actors and distinct non-empty groups.
//
// Events are summed per pair id; users are distinct pairID<<32 | actor keys
// (a null actor is one key, so it counts as one user, as the SQL's bool_or
// does); groups are distinct pairID<<32 | group over non-empty groups.
func AttributeValues(src Source, files []string, from, to int64) ([]Row, error) {
	attrs := newInterner("attributes", int(nullID))
	values := newInterner("values", int(nullID))
	actors := newInterner("actors", int(nullID))
	groups := newInterner("groups", int(nullID))
	pm := &pairs{ids: map[uint64]uint32{}}
	type state struct {
		counts             []int64
		users, grps        keySink
		ubuf, gbuf         []uint64
		am, gm, vm, pidmap []uint32
		attr               []uint32
	}
	ws := make([]state, workers())
	err := ScanParallel(src, files, []string{AttrCols, "actor_id", "group_id"}, "product", from, to, len(ws),
		func(w int, b *Batch) error {
			s := &ws[w]
			actor, group := b.Cols["actor_id"], b.Cols["group_id"]
			var err error
			if s.am, err = actors.remapCol(actor, nil, s.am); err != nil {
				return err
			}
			if s.gm, err = groups.remapCol(group, &noString, s.gm); err != nil {
				return err
			}
			ubuf, gbuf := s.ubuf[:0], s.gbuf[:0]
			for name, col := range b.Cols {
				an, ok := strings.CutPrefix(name, "attr_")
				if !ok {
					continue
				}
				if s.attr, err = attrs.remap([]string{an}, nil, s.attr); err != nil {
					return err
				}
				if s.vm, err = values.remap(col.Dict, nil, s.vm); err != nil {
					return err
				}
				var n int
				if s.pidmap, n, err = pm.remap(s.attr[0], s.vm, s.pidmap); err != nil {
					return err
				}
				if len(s.counts) < n {
					s.counts = append(s.counts, make([]int64, n-len(s.counts))...)
				}
				for i := 0; i < b.N; i++ {
					x := col.Idx[i]
					if x < 0 {
						continue
					}
					p := uint64(s.pidmap[x])
					s.counts[p]++
					ubuf = append(ubuf, p<<32|uint64(idAt(actor, s.am, i)))
					if g := idAt(group, s.gm, i); g != nullID {
						gbuf = append(gbuf, p<<32|uint64(g))
					}
				}
			}
			s.users.addGroup(ubuf)
			s.grps.addGroup(gbuf)
			s.ubuf, s.gbuf = ubuf, gbuf
			return nil
		})
	if err != nil {
		return nil, err
	}
	n := len(pm.list)
	counts := make([]int64, n)
	users := make([]int64, n)
	grps := make([]int64, n)
	us := make([]keySink, len(ws))
	gs := make([]keySink, len(ws))
	for i := range ws {
		for p, c := range ws[i].counts {
			counts[p] += c
		}
		us[i], gs[i] = ws[i].users, ws[i].grps
	}
	for _, k := range mergeKeys(us) {
		users[k>>32]++
	}
	for _, k := range mergeKeys(gs) {
		grps[k>>32]++
	}
	var ids []uint32
	for p, c := range counts {
		if c > 0 { // a dictionary entry no kept row uses makes a pair too
			ids = append(ids, uint32(p))
		}
	}
	attrOf := func(p uint32) string { return attrs.strs[pm.list[p]>>32] }
	valueOf := func(p uint32) string { return values.strs[uint32(pm.list[p])] }
	sort.Slice(ids, func(i, j int) bool {
		a, b := ids[i], ids[j]
		if aa, ab := attrOf(a), attrOf(b); aa != ab {
			return aa < ab
		}
		if counts[a] != counts[b] {
			return counts[a] > counts[b]
		}
		return valueOf(a) < valueOf(b)
	})
	rows := make([]Row, len(ids))
	for i, p := range ids {
		rows[i] = Row{attrOf(p), valueOf(p), strconv.FormatInt(counts[p], 10),
			strconv.FormatInt(users[p], 10), strconv.FormatInt(grps[p], 10)}
	}
	return rows, nil
}
