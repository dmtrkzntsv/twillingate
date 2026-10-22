package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// interner maps strings to ids that are stable across batches, whose
// dictionaries differ.
type interner struct {
	ids  map[string]int32
	strs []string
}

func newInterner() *interner { return &interner{ids: map[string]int32{}} }

func (in *interner) intern(s string) int32 {
	if id, ok := in.ids[s]; ok {
		return id
	}
	id := int32(len(in.strs))
	in.ids[s] = id
	in.strs = append(in.strs, s)
	return id
}

// remap returns, per dictionary entry, its global id.
func (in *interner) remap(dict []string) []int32 {
	m := make([]int32, len(dict))
	for i, s := range dict {
		m[i] = in.intern(s)
	}
	return m
}

// gid is a row's global id for a dictionary column, -1 for null.
func gid(remap []int32, idx int32) int32 {
	if idx < 0 {
		return -1
	}
	return remap[idx]
}

func intAt(c *Column, i int) (int64, bool) {
	if c.Null != nil && c.Null[i] {
		return 0, false
	}
	return c.Ints[i], true
}

// Countries is views/countries.sql: visitors distinct per day and country,
// added up per country; empty and null countries out; top 20.
func Countries(src Source, files []string, from, to int64) ([]Row, error) {
	in := newInterner()
	empty := in.intern("")
	type key struct {
		day     int64
		country int32
		actor   int32
	}
	seen := map[key]struct{}{}
	err := src.Scan(files, []string{"day", "country", "actor_id"}, "views", from, to, func(b *Batch) error {
		day, country, actor := b.Cols["day"], b.Cols["country"], b.Cols["actor_id"]
		cm, am := in.remap(country.Dict), in.remap(actor.Dict)
		for i := 0; i < b.N; i++ {
			d, ok := intAt(day, i)
			c, a := gid(cm, country.Idx[i]), gid(am, actor.Idx[i])
			if !ok || c < 0 || c == empty || a < 0 {
				continue
			}
			seen[key{d, c, a}] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	perDay := map[[2]int64]int64{} // (day, country) -> visitors
	for k := range seen {
		perDay[[2]int64{k.day, int64(k.country)}]++
	}
	sums := map[int32]int64{}
	for k, n := range perDay {
		sums[int32(k[1])] += n
	}
	type lv struct {
		label string
		value int64
	}
	all := make([]lv, 0, len(sums))
	for c, v := range sums {
		all = append(all, lv{in.strs[c], v})
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
func DAU(src Source, files []string, from, to int64) ([]Row, error) {
	in := newInterner()
	type key struct {
		day   int64
		actor int32
	}
	seen := map[key]struct{}{}
	err := src.Scan(files, []string{"day", "actor_id"}, "product", from, to, func(b *Batch) error {
		day, actor := b.Cols["day"], b.Cols["actor_id"]
		am := in.remap(actor.Dict)
		for i := 0; i < b.N; i++ {
			d, ok := intAt(day, i)
			a := gid(am, actor.Idx[i])
			if !ok {
				continue
			}
			seen[key{d, a}] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	perDay := map[int64]int64{}
	for k := range seen {
		if k.actor >= 0 {
			perDay[k.day]++
		} else if _, ok := perDay[k.day]; !ok {
			perDay[k.day] = 0 // a day with only null actors still has a group
		}
	}
	days := make([]int64, 0, len(perDay))
	for d := range perDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	rows := make([]Row, len(days))
	for i, d := range days {
		rows[i] = Row{formatDay(d), strconv.FormatInt(perDay[d], 10)}
	}
	return rows, nil
}

func formatDay(d int64) string {
	return time.Unix(d*86400, 0).UTC().Format("2006-01-02")
}

// AttributeValues is product/attribute-values.sql: per (attribute, value)
// the events, distinct actors and distinct non-empty groups.
func AttributeValues(src Source, files []string, from, to int64) ([]Row, error) {
	in := newInterner() // values, actors and groups share one id space
	type key struct {
		attr  string
		value int32
	}
	type agg struct {
		count  int64
		actors map[int32]struct{}
		groups map[int32]struct{}
	}
	aggs := map[key]*agg{}
	err := src.Scan(files, []string{AttrCols, "actor_id", "group_id"}, "product", from, to, func(b *Batch) error {
		actor, group := b.Cols["actor_id"], b.Cols["group_id"]
		am := in.remap(actor.Dict)
		var gm []int32
		if group != nil {
			gm = in.remap(group.Dict)
		}
		noGroup := in.intern("")
		for name, col := range b.Cols {
			if !strings.HasPrefix(name, "attr_") {
				continue
			}
			vm := in.remap(col.Dict)
			attr := strings.TrimPrefix(name, "attr_")
			for i := 0; i < b.N; i++ {
				v := gid(vm, col.Idx[i])
				if v < 0 {
					continue
				}
				k := key{attr, v}
				a := aggs[k]
				if a == nil {
					a = &agg{actors: map[int32]struct{}{}, groups: map[int32]struct{}{}}
					aggs[k] = a
				}
				a.count++
				if id := gid(am, actor.Idx[i]); id >= 0 {
					a.actors[id] = struct{}{}
				}
				if group != nil {
					if id := gid(gm, group.Idx[i]); id >= 0 && id != noGroup {
						a.groups[id] = struct{}{}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	keys := make([]key, 0, len(aggs))
	for k := range aggs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.attr != b.attr {
			return a.attr < b.attr
		}
		if ca, cb := aggs[a].count, aggs[b].count; ca != cb {
			return ca > cb
		}
		return in.strs[a.value] < in.strs[b.value]
	})
	rows := make([]Row, len(keys))
	for i, k := range keys {
		a := aggs[k]
		rows[i] = Row{k.attr, in.strs[k.value], strconv.FormatInt(a.count, 10),
			strconv.Itoa(len(a.actors)), strconv.Itoa(len(a.groups))}
	}
	return rows, nil
}
