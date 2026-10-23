package native

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// The attributes kind reads the rows of product or measures unpivoted:
// each row is one pseudo-row per attribute it carries (an attr_* column
// not NULL, after the rules, an empty value kept) and, with reserved, per
// reserved key whose column is not NULL or empty, named as the key
// ($platform). The pseudo-rows go to the breakdowns' aggregation, grouped
// by the query's dimensions, then attribute and value, as SQL's UNPIVOT
// then GROUP BY has them.

// ReservedKeyColumns are the reserved keys read from a typed column of
// the event files, by key: an attributes query's with reserved, and
// internal/api's breakdowns' and product_attributes' (the nine
// v_product_attrs and v_measures_attrs always had, and the nine a project
// could declare there, migration 040's mapping). On the files every one
// breaks down, declared or not; any other key reads its attr_<key>
// column.
var ReservedKeyColumns = map[string]string{
	"$platform": "platform", "$os": "os", "$app_version": "app_version", "$app_locale": "app_locale",
	"$kind": "kind", "$browser": "browser", "$device": "device", "$browser_locale": "browser_locale",
	"$country": "country", "$host": "host", "$path": "path", "$referrer": "referrer_source",
	"$utm_source": "utm_source", "$utm_medium": "utm_medium", "$utm_campaign": "utm_campaign",
	"$os_version": "os_version", "$browser_version": "browser_version", "$device_model": "device_model",
}

// reservedKeys are ReservedKeyColumns' keys, sorted.
var reservedKeys = slices.Sorted(maps.Keys(ReservedKeyColumns))

// The pseudo-rows' own columns, beside day, event_name, actor_id, user_id
// and group_id (group_id's empty value read as NULL).
const (
	attrKeyCol   = "attribute"
	attrValueCol = "value"
	nullActorCol = "actor_null" // "1" where actor_id is NULL, else NULL
)

// unpivotChunk is how many pseudo-rows one batch's unpivot hands to the
// aggregation at a time, so a wide batch's pseudo-rows are never all held
// at once; a variable so a test can lower it.
var unpivotChunk = 1 << 16

// attrMetric is one attributes metric resolved to the aggregation's: the
// one it reads (count, actors, users, groups) and, for actors_or_null, the count
// of the pseudo-rows without an actor, which adds one when there is any.
type attrMetric struct {
	Metric
	inner, nulls int // indexes into the aggregation's metrics; nulls -1 but for actors_or_null
}

// attributes answers an attributes query, output values: a row per
// (dimensions, attribute, value) of the pseudo-rows, its metrics: count
// the pseudo-rows, actors the distinct actor_id (NULL left out, as
// count(DISTINCT) has it), actors_or_null the same with NULL counting as
// one (as product/attribute-values.sql's GROUPING SETS has it), users the
// distinct user_id and groups the distinct group_id (NULL and empty left
// out). An attribute's name is its
// column's without attr_, spelled as the reader's events view spells it
// (the first spelling of the files' columns, unionColumns), so keys equal
// but for case in different files are one. The rows are ordered by order
// (a metric descending, a dimension ascending, ties by the dimensions in
// turn), else by day, the first metric descending, then event_name,
// attribute and value with day among the dimensions, and without by
// event_name, attribute, the first metric descending and value; limit
// keeps the first. Past MaxGroups or MaxBytes the groups not tracked are
// left out and the result is truncated, as a breakdown's.
func (s scan) attributes(ctx context.Context, from, to civil.Date) (Result, error) {
	r := Range{From: from, To: to}
	if s.q.Output == "keys" {
		return s.attributeKeys(ctx, r)
	}
	nullWhere := []Filter{{Column: nullActorCol, Op: "is_set"}}
	nullFilters, err := compileFilters(nullWhere, "where")
	if err != nil {
		return Result{}, err
	}
	var inner []metricSpec
	add := func(m Metric, where []filter) int {
		inner = append(inner, metricSpec{Metric: m, where: where})
		return len(inner) - 1
	}
	ams := make([]attrMetric, len(s.q.Metrics))
	for i, m := range s.q.Metrics {
		am := attrMetric{Metric: m, nulls: -1}
		switch m.Op {
		case "count", "actors", "users", "groups":
			am.inner = add(Metric{Op: m.Op}, nil)
		case "actors_or_null":
			am.inner = add(Metric{Op: "actors"}, nil)
			am.nulls = add(Metric{Op: "count", Where: nullWhere}, nullFilters)
		}
		ams[i] = am
	}
	dims := append(slices.Clone(s.q.Dimensions), attrKeyCol, attrValueCol)
	m, ms, err := s.unpivoted(ctx, r, []string{s.q.Table}, inner, dims)
	if err != nil {
		return Result{}, err
	}

	var out columnsOut
	for _, d := range dims {
		out.add(d)
	}
	for _, am := range ams {
		out.add(am.Name())
	}
	values := make([][]value, m.groups)
	for g := range values {
		row := make([]value, len(ams))
		for i, am := range ams {
			gs := []int32{int32(g)}
			v := m.compute(ms[am.inner], gs, -1)
			if am.nulls >= 0 && m.compute(ms[am.nulls], gs, -1).i > 0 {
				v.i++
			}
			row[i] = v
		}
		values[g] = row
	}
	keys := s.attributeOrder(dims, ams)
	vals, types := m.a.groups.vals, m.a.dimTypes
	gs := make([]int32, m.groups)
	for g := range gs {
		gs[g] = int32(g)
	}
	slices.SortFunc(gs, func(x, y int32) int {
		for _, k := range keys {
			var c int
			if k.metric >= 0 {
				c = cmpDesc(values[x][k.metric], values[y][k.metric])
			} else {
				c = cmpDim(types[k.dim], vals[x][k.dim], vals[y][k.dim])
			}
			if c != 0 {
				return c
			}
		}
		return 0
	})
	if s.q.Limit > 0 && len(gs) > s.q.Limit {
		gs = gs[:s.q.Limit]
	}
	rows := make([][]string, 0, len(gs))
	for _, g := range gs {
		row := make([]string, 0, len(out.names))
		for _, v := range vals[g] {
			row = append(row, label(v))
		}
		for i, v := range values[g] {
			if v.approx {
				out.approx[len(dims)+i] = true
			}
			row = append(row, v.String())
		}
		rows = append(rows, row)
	}
	return out.result(rows, m.a.groups.dropped), nil
}

// unpivoted aggregates the pseudo-rows of tables' rows in r (each table
// read in turn into the one aggregation) by dims, with the metrics inner.
func (s scan) unpivoted(ctx context.Context, r Range, tables []string, inner []metricSpec, dims []string) (*merged, []metricSpec, error) {
	lo, hi := r.days()
	a, ms := newAgg(aggOptions{metrics: inner, dims: dims, from: lo, ndays: int(hi - lo + 1),
		workers: s.e.workers, maxGroups: s.e.maxGroups, maxBytes: s.e.maxBytes})
	charge := func(n int64) error {
		a.mem.add(n)
		if n > 0 {
			return a.checkMemory()
		}
		return nil
	}
	have, err := s.e.columns()
	if err != nil {
		return nil, nil, err
	}
	spell := map[string]string{} // the view's spelling, by lower-case name
	for _, c := range have {
		if _, ok := spell[strings.ToLower(c)]; !ok {
			spell[strings.ToLower(c)] = c
		}
	}
	all, err := archive.ListFiles(s.e.dir)
	if err != nil {
		return nil, nil, err
	}
	cols := []string{"day", "event_name", "actor_id", "user_id", "group_id", AttrCols}
	more := func(cs ...string) {
		for _, c := range cs {
			if !slices.Contains(cols, c) {
				cols = append(cols, c)
			}
		}
	}
	for _, f := range s.filters {
		for _, col := range f.columns() {
			more(readColumns(col)...)
		}
	}
	if s.q.Reserved {
		for _, k := range reservedKeys {
			more(ReservedKeyColumns[k])
		}
	}
	us := make([]unpivoter, s.e.workers)
	for _, t := range tables {
		st := s
		st.q.Table = t
		err = st.scanEvents(ctx, all[s.project], r, cols, nil, charge, func(w int, b *Batch) error {
			return us[w].unpivot(b, s.filters, spell, s.q.Reserved, func(c *Batch) error { return a.add(w, c) })
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return a.merge(), ms, nil
}

// attributeKeys answers an attributes query, output keys, as
// list_attributes' SQL does: per attribute of the pseudo-rows of its
// table's rows (events: product's and measures'), events the
// pseudo-rows and values the distinct values (an empty one a value;
// exact up to hll.ExactLimit, then estimated), keys_total the number of
// attributes; the limit first by events descending, then attribute, and
// those keep names exactly (as SQL's IN) past them; in that order.
func (s scan) attributeKeys(ctx context.Context, r Range) (Result, error) {
	tables := []string{s.q.keysTable()}
	if tables[0] == "events" {
		tables = []string{"product", "measures"}
	}
	inner := []metricSpec{{Metric: Metric{Op: "count"}}, {Metric: Metric{Op: opValues}}}
	m, ms, err := s.unpivoted(ctx, r, tables, inner, []string{attrKeyCol})
	if err != nil {
		return Result{}, err
	}
	var out columnsOut
	for _, c := range s.q.Columns() {
		out.add(c)
	}
	type key struct {
		name           string
		events, values value
	}
	vals := m.a.groups.vals
	keys := make([]key, m.groups)
	for g := range keys {
		gs := []int32{int32(g)}
		keys[g] = key{name: label(vals[g][0]), events: m.compute(ms[0], gs, -1), values: m.compute(ms[1], gs, -1)}
	}
	slices.SortFunc(keys, func(x, y key) int {
		if c := cmpDesc(x.events, y.events); c != 0 {
			return c
		}
		return strings.Compare(x.name, y.name)
	})
	total := strconv.Itoa(len(keys))
	var rows [][]string
	for i, k := range keys {
		if i >= s.q.keysLimit() && !slices.Contains(s.q.Keep, k.name) {
			continue
		}
		out.approx[2] = out.approx[2] || k.values.approx
		rows = append(rows, []string{k.name, k.events.String(), k.values.String(), total})
	}
	return out.result(rows, m.a.groups.dropped), nil
}

// sortKey is one key an attributes' rows sort by: a metric, descending
// (metric its index), or else a dimension, ascending.
type sortKey struct{ dim, metric int }

// attributeOrder is the keys an attributes' rows sort by, its dims being
// the query's dimensions, then attribute and value.
func (s scan) attributeOrder(dims []string, ams []attrMetric) []sortKey {
	dimKeys := func(skip int) []sortKey {
		var out []sortKey
		for j := range dims {
			if j != skip {
				out = append(out, sortKey{dim: j, metric: -1})
			}
		}
		return out
	}
	if s.q.Order != "" {
		if i := slices.IndexFunc(ams, func(am attrMetric) bool { return am.Name() == s.q.Order }); i >= 0 {
			return append([]sortKey{{dim: -1, metric: i}}, dimKeys(-1)...)
		}
		j := slices.Index(dims, s.q.Order)
		return append([]sortKey{{dim: j, metric: -1}}, dimKeys(j)...)
	}
	first := sortKey{dim: -1, metric: 0}
	all := dimKeys(-1)
	if dims[0] == "day" {
		return slices.Concat(all[:1], []sortKey{first}, all[1:])
	}
	n := len(all)
	return slices.Concat(all[:n-1], []sortKey{first}, all[n-1:])
}

// unpivoter is one worker's unpivot of its batches into pseudo-rows,
// its arrays reused from chunk to chunk.
type unpivoter struct {
	sel                                       []int32
	day                                       []int64
	event, actor, user, null, group, key, val []int32
}

// unpivot hands the pseudo-rows of b's rows that pass filters to emit,
// unpivotChunk at a time. An attribute's name is its column's spelled as
// spell has it (by lower-case name), else as b spells it.
func (u *unpivoter) unpivot(b *Batch, filters []filter, spell map[string]string, reserved bool, emit func(*Batch) error) error {
	sel, err := selectRows(b, filters, u.sel)
	if err != nil {
		return err
	}
	u.sel = sel
	if len(sel) == 0 {
		return nil
	}
	_, day := b.col("day")
	if day == nil || day.Ints == nil {
		return fmt.Errorf("native: a batch without day")
	}
	text := func(name string) (*Column, error) {
		_, c := b.col(name)
		if c != nil && c.Idx == nil {
			return nil, fmt.Errorf("native: column %s is not text", name)
		}
		return c, nil
	}
	event, err := text("event_name")
	if err != nil {
		return err
	}
	actor, err := text("actor_id")
	if err != nil {
		return err
	}
	user, err := text("user_id")
	if err != nil {
		return err
	}
	group, err := text("group_id")
	if err != nil {
		return err
	}
	// The sources: each attr_* column, then the reserved keys' columns,
	// whose empty values are left out.
	var names []string
	var srcs []*Column
	var reservedFrom int
	for _, n := range slices.Sorted(maps.Keys(b.Cols)) {
		if !strings.HasPrefix(n, "attr_") {
			continue
		}
		c := b.Cols[n]
		if c.Idx == nil {
			return fmt.Errorf("native: column %s is not text", n)
		}
		if sp, ok := spell[strings.ToLower(n)]; ok {
			n = sp
		}
		names, srcs = append(names, strings.TrimPrefix(n, "attr_")), append(srcs, c)
	}
	reservedFrom = len(srcs)
	if reserved {
		for _, k := range reservedKeys {
			c, err := text(ReservedKeyColumns[k])
			if err != nil {
				return err
			}
			if c != nil {
				names, srcs = append(names, k), append(srcs, c)
			}
		}
	}
	if len(srcs) == 0 {
		return nil
	}
	// remap is each source's dictionary entries' index in the values'
	// dictionary, set as met: -1 not yet, -2 left out (a reserved key's
	// empty value).
	var values dictBuilder
	remap := make([][]int32, len(srcs))
	for j, c := range srcs {
		remap[j] = make([]int32, len(c.Dict))
		for x := range remap[j] {
			remap[j][x] = -1
		}
	}
	// noGroup marks group_id's empty entries, read as NULL as the SQL's
	// nullif has it; the files never hold one (an empty field is left
	// out), so this only guards the count of groups.
	var noGroup []bool
	if group != nil {
		noGroup = make([]bool, len(group.Dict))
		for x, v := range group.Dict {
			noGroup[x] = v == ""
		}
	}
	at := func(c *Column, i int32) int32 {
		if c == nil {
			return -1
		}
		return c.Idx[i]
	}
	reset := func() {
		u.day, u.event, u.actor, u.user, u.null, u.group, u.key, u.val = u.day[:0], u.event[:0], u.actor[:0], u.user[:0], u.null[:0], u.group[:0], u.key[:0], u.val[:0]
	}
	flush := func() error {
		c := &Batch{N: len(u.day), Cols: map[string]*Column{
			"day":        {Ints: u.day},
			"actor_id":   {Idx: u.actor},
			"user_id":    {Idx: u.user},
			nullActorCol: {Dict: []string{"1"}, Idx: u.null},
			"group_id":   {Idx: u.group},
			attrKeyCol:   {Dict: names, Idx: u.key},
			attrValueCol: {Dict: values.dict, Idx: u.val},
		}}
		if actor != nil {
			c.Cols["actor_id"].Dict = actor.Dict
		}
		if user != nil {
			c.Cols["user_id"].Dict = user.Dict
		}
		if group != nil {
			c.Cols["group_id"].Dict = group.Dict
		}
		if event != nil {
			c.Cols["event_name"] = &Column{Dict: event.Dict, Idx: u.event}
		}
		err := emit(c)
		reset()
		return err
	}
	reset()
	for _, i := range sel {
		ev, act, usr, grp := at(event, i), at(actor, i), at(user, i), at(group, i)
		if grp >= 0 && noGroup[grp] {
			grp = -1
		}
		nul := int32(-1)
		if act < 0 {
			nul = 0
		}
		for j, c := range srcs {
			x := c.Idx[i]
			if x < 0 {
				continue
			}
			v := remap[j][x]
			if v == -1 {
				if j >= reservedFrom && c.Dict[x] == "" {
					v = -2
				} else {
					v = values.id([]byte(c.Dict[x]))
				}
				remap[j][x] = v
			}
			if v == -2 {
				continue
			}
			u.day = append(u.day, day.Ints[i])
			u.event, u.actor, u.user = append(u.event, ev), append(u.actor, act), append(u.user, usr)
			u.null, u.group = append(u.null, nul), append(u.group, grp)
			u.key, u.val = append(u.key, int32(j)), append(u.val, v)
			if len(u.day) >= unpivotChunk {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	if len(u.day) > 0 {
		return flush()
	}
	return nil
}
