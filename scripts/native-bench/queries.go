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

// mergeKeys concatenates the workers' keys and returns them sorted, each
// once. It drops each sink's keys once copied, so the workers' arrays can be
// collected before the final sort.
func mergeKeys(sinks []*keySink) []uint64 {
	n := 0
	for _, s := range sinks {
		n += len(s.keys)
	}
	all := make([]uint64, 0, n)
	for i := range sinks {
		all = append(all, sinks[i].keys...)
		sinks[i].keys, sinks[i].compacted = nil, 0
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
				if k := uint64(off)<<48 | uint64(c)<<32 | uint64(idAt(actor, s.am, i)); len(buf) == 0 || buf[len(buf)-1] != k {
					buf = append(buf, k)
				}
			}
			s.sink.addGroup(buf)
			s.buf = buf
			return nil
		})
	if err != nil {
		return nil, err
	}
	sinks := make([]*keySink, len(ws))
	for i := range ws {
		ws[i].buf = nil
		sinks[i] = &ws[i].sink
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
				if k := uint64(off)<<32 | uint64(idAt(actor, s.am, i)); len(buf) == 0 || buf[len(buf)-1] != k {
					buf = append(buf, k)
				}
			}
			s.sink.addGroup(buf)
			s.buf = buf
			return nil
		})
	if err != nil {
		return nil, err
	}
	sinks := make([]*keySink, len(ws))
	for i := range ws {
		ws[i].buf = nil
		sinks[i] = &ws[i].sink
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

// attrScan is the scan AttributeValues and AttributeValuesApprox share:
// the interners, the pair ids, and per worker the events per pair. Each
// query passes per-column code that counts the distinct actors and groups.
type attrScan struct {
	attrs, values, actors, groups *interner
	pm                            *pairs
	counts                        [][]int64 // per worker, per pair id
}

// attrCol is one attribute column of a batch, remapped: pids[col.Idx[i]] is
// row i's pair id; actor and group ids come from idAt with am and gm.
type attrCol struct {
	col          *Column
	pids         []uint32
	actor, group *Column
	am, gm       []uint32
	counts       []int64 // this worker's, long enough for every pair id
	pairs        int     // pair ids that exist now
}

// scanAttrs scans the product rows and calls column(w, c) per attribute
// column of every batch, and batchDone(w) after each batch. With fp, actor
// and group ids are fingerprints of their strings rather than interned ids,
// so they do not depend on which worker saw a string first.
func scanAttrs(src Source, files []string, from, to int64, nw int, fp bool,
	column func(w int, c *attrCol), batchDone func(w int)) (*attrScan, error) {
	a := &attrScan{
		attrs:  newInterner("attributes", int(nullID)),
		values: newInterner("values", int(nullID)),
		actors: newInterner("actors", int(nullID)),
		groups: newInterner("groups", int(nullID)),
		pm:     &pairs{ids: map[uint64]uint32{}},
		counts: make([][]int64, nw),
	}
	type state struct {
		am, gm, vm, pidmap []uint32
		attr               []uint32
	}
	ws := make([]state, nw)
	err := ScanParallel(src, files, []string{AttrCols, "actor_id", "group_id"}, "product", from, to, nw,
		func(w int, b *Batch) error {
			s := &ws[w]
			actor, group := b.Cols["actor_id"], b.Cols["group_id"]
			var err error
			if fp {
				s.am = fingerprints(actor, nil, s.am)
				s.gm = fingerprints(group, &noString, s.gm)
			} else {
				if s.am, err = a.actors.remapCol(actor, nil, s.am); err != nil {
					return err
				}
				if s.gm, err = a.groups.remapCol(group, &noString, s.gm); err != nil {
					return err
				}
			}
			for name, col := range b.Cols {
				an, ok := strings.CutPrefix(name, "attr_")
				if !ok {
					continue
				}
				if s.attr, err = a.attrs.remap([]string{an}, nil, s.attr); err != nil {
					return err
				}
				if s.vm, err = a.values.remap(col.Dict, nil, s.vm); err != nil {
					return err
				}
				var n int
				if s.pidmap, n, err = a.pm.remap(s.attr[0], s.vm, s.pidmap); err != nil {
					return err
				}
				if len(a.counts[w]) < n {
					a.counts[w] = append(a.counts[w], make([]int64, n-len(a.counts[w]))...)
				}
				column(w, &attrCol{col: col, pids: s.pidmap, actor: actor, group: group,
					am: s.am, gm: s.gm, counts: a.counts[w], pairs: n})
			}
			batchDone(w)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return a, nil
}

// fingerprints returns per dictionary entry a 32-bit FNV-1a fingerprint of
// its string, never nullID; an entry equal to skip maps to nullID. Two
// strings share a fingerprint with odds 2^-32, which a Sketch's error
// swamps.
func fingerprints(c *Column, skip *string, dst []uint32) []uint32 {
	dst = dst[:0]
	if c == nil {
		return dst
	}
	for _, str := range c.Dict {
		if skip != nil && str == *skip {
			dst = append(dst, nullID)
			continue
		}
		h := uint64(14695981039346656037)
		for i := 0; i < len(str); i++ {
			h = (h ^ uint64(str[i])) * 1099511628211
		}
		f := uint32(h ^ h>>32)
		if f == nullID {
			f--
		}
		dst = append(dst, f)
	}
	return dst
}

// rows sums the workers' events per pair and returns the query's rows, in
// its order, with users and groups per pair id.
func (a *attrScan) rows(users, grps func(p uint32) uint64) []Row {
	counts := make([]int64, len(a.pm.list))
	for _, wc := range a.counts {
		for p, c := range wc {
			counts[p] += c
		}
	}
	var ids []uint32
	for p, c := range counts {
		if c > 0 { // a dictionary entry no kept row uses makes a pair too
			ids = append(ids, uint32(p))
		}
	}
	attrOf := func(p uint32) string { return a.attrs.strs[a.pm.list[p]>>32] }
	valueOf := func(p uint32) string { return a.values.strs[uint32(a.pm.list[p])] }
	sort.Slice(ids, func(i, j int) bool {
		x, y := ids[i], ids[j]
		if ax, ay := attrOf(x), attrOf(y); ax != ay {
			return ax < ay
		}
		if counts[x] != counts[y] {
			return counts[x] > counts[y]
		}
		return valueOf(x) < valueOf(y)
	})
	rows := make([]Row, len(ids))
	for i, p := range ids {
		rows[i] = Row{attrOf(p), valueOf(p), strconv.FormatInt(counts[p], 10),
			strconv.FormatUint(users(p), 10), strconv.FormatUint(grps(p), 10)}
	}
	return rows
}

// AttributeValues is product/attribute-values.sql: per (attribute, value)
// the events, distinct actors and distinct non-empty groups.
//
// Events are summed per pair id; users are distinct pairID<<32 | actor keys
// (a null actor is one key, so it counts as one user, as the SQL's bool_or
// does); groups are distinct pairID<<32 | group over non-empty groups.
func AttributeValues(src Source, files []string, from, to int64) ([]Row, error) {
	type state struct {
		users, grps keySink
		ubuf, gbuf  []uint64
	}
	ws := make([]state, workers())
	a, err := scanAttrs(src, files, from, to, len(ws), false,
		func(w int, c *attrCol) {
			s := &ws[w]
			ubuf, gbuf := s.ubuf, s.gbuf
			for i, x := range c.col.Idx {
				if x < 0 {
					continue
				}
				p := uint64(c.pids[x])
				c.counts[p]++
				if k := p<<32 | uint64(idAt(c.actor, c.am, i)); len(ubuf) == 0 || ubuf[len(ubuf)-1] != k {
					ubuf = append(ubuf, k)
				}
				if g := idAt(c.group, c.gm, i); g != nullID {
					if k := p<<32 | uint64(g); len(gbuf) == 0 || gbuf[len(gbuf)-1] != k {
						gbuf = append(gbuf, k)
					}
				}
			}
			s.ubuf, s.gbuf = ubuf, gbuf
		},
		func(w int) {
			s := &ws[w]
			s.users.addGroup(s.ubuf)
			s.grps.addGroup(s.gbuf)
			s.ubuf, s.gbuf = s.ubuf[:0], s.gbuf[:0]
		})
	if err != nil {
		return nil, err
	}
	n := len(a.pm.list)
	users := make([]uint64, n)
	grps := make([]uint64, n)
	us := make([]*keySink, len(ws))
	gs := make([]*keySink, len(ws))
	for i := range ws {
		ws[i].ubuf, ws[i].gbuf = nil, nil
		us[i], gs[i] = &ws[i].users, &ws[i].grps
	}
	for _, k := range mergeKeys(us) {
		users[k>>32]++
	}
	for _, k := range mergeKeys(gs) {
		grps[k>>32]++
	}
	return a.rows(func(p uint32) uint64 { return users[p] }, func(p uint32) uint64 { return grps[p] }), nil
}

// AttributeValuesApprox is AttributeValues with users and groups counted by
// a Sketch per pair per worker, merged at the end; events stay exact. The
// sketches take string fingerprints (a null actor is nullID, one user), so
// the rows are the same on every run and with any number of workers.
func AttributeValuesApprox(src Source, files []string, from, to int64) ([]Row, error) {
	type state struct{ users, grps []Sketch }
	ws := make([]state, workers())
	a, err := scanAttrs(src, files, from, to, len(ws), true,
		func(w int, c *attrCol) {
			s := &ws[w]
			if len(s.users) < c.pairs {
				s.users = append(s.users, make([]Sketch, c.pairs-len(s.users))...)
				s.grps = append(s.grps, make([]Sketch, c.pairs-len(s.grps))...)
			}
			for i, x := range c.col.Idx {
				if x < 0 {
					continue
				}
				p := c.pids[x]
				c.counts[p]++
				s.users[p].Add(idAt(c.actor, c.am, i))
				if g := idAt(c.group, c.gm, i); g != nullID {
					s.grps[p].Add(g)
				}
			}
		},
		func(int) {})
	if err != nil {
		return nil, err
	}
	n := len(a.pm.list)
	// The first worker's sketches become the totals; the others merge in.
	users := append(ws[0].users, make([]Sketch, n-len(ws[0].users))...)
	grps := append(ws[0].grps, make([]Sketch, n-len(ws[0].grps))...)
	ws[0] = state{}
	for i := 1; i < len(ws); i++ {
		for p := range ws[i].users {
			users[p].Merge(&ws[i].users[p])
			grps[p].Merge(&ws[i].grps[p])
		}
		ws[i] = state{}
	}
	return a.rows(func(p uint32) uint64 { return users[p].Count() }, func(p uint32) uint64 { return grps[p].Count() }), nil
}
