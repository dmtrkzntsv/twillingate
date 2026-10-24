package native

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ruleSet is one project's event and attribute rules, compiled to apply to
// its batches as the events table applies them (docs://twillingate):
//   - an event renamed or merged reads under its new name; a blocked one,
//     and one renamed onto a blocked name, is left out;
//   - an attribute key renamed or blocked reads NULL; a rename target reads
//     its own value, else the renamed keys' in key order; a target no file
//     has is added;
//   - attribute rules cover the product and measures rows only.
//
// Columns match case-insensitively (Batch.col), as DuckDB's do.
type ruleSet struct {
	events map[eventKey]eventAction
	// dropFams and dropNames are the families and the names of the dropped
	// events, for a row whose family or event name is null: a drop whose
	// condition the null leaves unknown drops it, as SQL's NOT (NULL)
	// leaves a row out.
	dropFams, dropNames map[string]bool
	attrs               []attrCol
}

type eventKey struct{ family, name string }

type eventAction struct {
	drop bool
	to   string
}

// attrCol is one attribute column a rule touches, by its name as the
// events view spells it: the first spelling of the files' columns (have),
// else, for a target no file holds, the first rule's ("attr_" and the key,
// a NUL as \u0000). apply adds a target a batch lacks under that name,
// an internal column found case-insensitively (Batch.col) like any other;
// a batch that has the column keeps its own spelling. The engine's output
// columns are named as the query wrote them, whatever the spelling.
type attrCol struct {
	name string
	// null: the column is a renamed or blocked key's and reads NULL in
	// scope, even when it is a rename target too (its NULL comes first).
	null bool
	// from: a rename target's sources, in key order, read after its own
	// value; nil for a column no rename targets.
	from []string
}

// compileRules compiles the rules of project p, ordered as archive's
// sortRules orders them (the view's result depends on the order), against
// have, the columns of every events file (unionColumns): as the events
// table does, an attribute rule whose source column no file holds is skipped, so a
// key that is only a rename target reads its sources even when a rule
// renames it too.
func compileRules(p int64, rules archive.Rules, have []string) ruleSet {
	spelled := map[string]string{} // have's first spelling, by lower-case name
	for _, c := range have {
		if _, ok := spelled[strings.ToLower(c)]; !ok {
			spelled[strings.ToLower(c)] = c
		}
	}
	ev := slices.Clone(rules.Events)
	slices.SortFunc(ev, func(a, b store.EventRule) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Family, b.Family), cmp.Compare(a.Name, b.Name))
	})
	at := slices.Clone(rules.Attributes)
	slices.SortFunc(at, func(a, b store.AttributeRule) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Key, b.Key))
	})
	var rs ruleSet

	blocked := map[eventKey]bool{}
	for _, r := range ev {
		if r.ProjectID == p && r.Action == store.EventRuleBlock {
			blocked[eventKey{string(r.Family), r.Name}] = true
		}
	}
	for _, r := range ev {
		if r.ProjectID != p {
			continue
		}
		k := eventKey{string(r.Family), r.Name}
		drop := r.Action == store.EventRuleBlock || blocked[eventKey{string(r.Family), r.To}]
		if a, ok := rs.events[k]; ok && (a.drop || !drop) {
			continue // a drop wins, then the CASE's first arm
		}
		if rs.events == nil {
			rs.events, rs.dropFams, rs.dropNames = map[eventKey]eventAction{}, map[string]bool{}, map[string]bool{}
		}
		if drop {
			rs.events[k] = eventAction{drop: true}
			rs.dropFams[k.family], rs.dropNames[k.name] = true, true
		} else {
			rs.events[k] = eventAction{to: r.To}
		}
	}

	keyBlocked := map[string]bool{}
	for _, r := range at {
		if r.ProjectID == p && r.Action == store.EventRuleBlock {
			keyBlocked[r.Key] = true
		}
	}
	byLow := map[string]int{} // an attrs entry, by its lower-case name
	col := func(name string) *attrCol {
		low := strings.ToLower(name)
		i, ok := byLow[low]
		if !ok {
			i = len(rs.attrs)
			byLow[low] = i
			rs.attrs = append(rs.attrs, attrCol{name: name})
		}
		return &rs.attrs[i]
	}
	sources := map[string][]string{} // by target key (as the rule spells it)
	var targets []string
	for _, r := range at {
		if r.ProjectID != p {
			continue
		}
		src, dst := visible("attr_"+r.Key), visible("attr_"+r.To)
		if r.Action == store.EventRuleRename && strings.EqualFold(src, dst) {
			continue // one column already: the values read there as they are
		}
		src, ok := spelled[strings.ToLower(src)]
		if !ok {
			continue // no file holds it: no column to read NULL or to fold
		}
		col(src).null = true
		if r.Action != store.EventRuleRename || keyBlocked[r.To] {
			continue
		}
		if sources[r.To] == nil {
			targets = append(targets, r.To)
		}
		sources[r.To] = append(sources[r.To], src)
	}
	for _, t := range targets {
		name := visible("attr_" + t)
		if n, ok := spelled[strings.ToLower(name)]; ok {
			name = n
		}
		c := col(name)
		if c.from == nil { // two targets equal but for case: the first's arm wins
			c.from = sources[t]
		}
	}
	return rs
}

// empty says whether the rules leave every batch as it is.
func (rs ruleSet) empty() bool { return len(rs.events) == 0 && len(rs.attrs) == 0 }

// columns is cols and the columns a scan must also read for apply to build
// them: event_name and family for event rules, family for attribute rules,
// and the sources of each rename target cols names (a "prefix*" pattern
// names those with its prefix). cols' own names come first, as they are.
func (rs ruleSet) columns(cols []string) []string {
	out := slices.Clone(cols)
	add := func(name string) {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if len(rs.events) > 0 {
		add("event_name")
		add("family")
	}
	if len(rs.attrs) == 0 {
		return out
	}
	add("family")
	for _, c := range cols {
		prefix, pattern := strings.CutSuffix(c, "*")
		for _, a := range rs.attrs {
			if a.null || a.from == nil {
				continue
			}
			if pattern && strings.HasPrefix(a.name, prefix) || !pattern && strings.EqualFold(a.name, c) {
				for _, s := range a.from {
					add(s)
				}
			}
		}
	}
	return out
}

// apply returns b with the rules applied; b itself when there are none.
// The rows are all of the rules' project, and of family when it is not "";
// when it is "", b's family column says each row's (columns asks for it),
// and a batch without one has no row in an attribute rule's scope. Event
// rules need b's event_name column; without one, no event is renamed or
// left out. b's columns are not changed: a column the rules change is a
// new one, under b's own name for it (attrCol's name when b has none);
// either way only the batch's, not the name a result shows.
func (rs ruleSet) apply(b *Batch, family string) *Batch {
	if rs.empty() {
		return b
	}
	out := &Batch{N: b.N, Cols: maps.Clone(b.Cols)}
	if len(rs.attrs) > 0 {
		rs.foldAttrs(b, out, family)
	}
	if len(rs.events) > 0 {
		out = rs.applyEvents(out, family)
	}
	return out
}

// isAttrFamily says whether a row of family is in an attribute rule's
// scope.
func isAttrFamily(family string) bool {
	return family == string(store.FamilyProduct) || family == string(store.FamilyMeasures)
}

// scope marks b's rows in an attribute rule's scope.
func scope(b *Batch, family string) []bool {
	in := make([]bool, b.N)
	if family != "" {
		if isAttrFamily(family) {
			for i := range in {
				in[i] = true
			}
		}
		return in
	}
	_, fc := b.col("family")
	if fc == nil {
		return in
	}
	ok := make([]bool, len(fc.Dict))
	for j, s := range fc.Dict {
		ok[j] = isAttrFamily(s)
	}
	for i, f := range fc.Idx {
		in[i] = f >= 0 && ok[f]
	}
	return in
}

// foldAttrs sets out's attribute columns the rules change, read from b.
func (rs ruleSet) foldAttrs(b, out *Batch, family string) {
	in := scope(b, family)
	for _, a := range rs.attrs {
		key, own := b.col(a.name)
		var srcs []*Column
		if !a.null {
			for _, s := range a.from {
				if _, c := b.col(s); c != nil {
					srcs = append(srcs, c)
				}
			}
		}
		switch {
		case own == nil && len(srcs) == 0:
			continue // NULL either way; the column stays as b has it (or not)
		case key == "":
			key = a.name
		}
		if a.null {
			idx := slices.Clone(own.Idx)
			for i, ok := range in {
				if ok {
					idx[i] = -1
				}
			}
			out.Cols[key] = &Column{Dict: own.Dict, Idx: idx}
			continue
		}
		out.Cols[key] = coalesce(own, srcs, in, b.N)
	}
}

// coalesce is a column of n rows reading own's value, and in the rows in
// scope, when own's is null, the first source's that is not. own may be
// nil (all null). Each column's dictionary is mapped into the result's
// once.
func coalesce(own *Column, srcs []*Column, in []bool, n int) *Column {
	var d dictBuilder
	remap := func(c *Column) []int32 {
		r := make([]int32, len(c.Dict))
		for j, s := range c.Dict {
			r[j] = d.id([]byte(s))
		}
		return r
	}
	var ownMap []int32
	if own != nil {
		ownMap = remap(own)
	}
	srcMaps := make([][]int32, len(srcs))
	for k, c := range srcs {
		srcMaps[k] = remap(c)
	}
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = -1
		if own != nil && own.Idx[i] >= 0 {
			idx[i] = ownMap[own.Idx[i]]
			continue
		}
		if !in[i] {
			continue
		}
		for k, c := range srcs {
			if v := c.Idx[i]; v >= 0 {
				idx[i] = srcMaps[k][v]
				break
			}
		}
	}
	return &Column{Dict: d.dict, Idx: idx}
}

// applyEvents renames b's events and leaves out the dropped ones, the
// action looked up once per family and event name of b's dictionaries. b
// is apply's own batch: its event_name column may be replaced in place.
func (rs ruleSet) applyEvents(b *Batch, family string) *Batch {
	nameKey, nc := b.col("event_name")
	if nc == nil {
		return b
	}
	fams, famOf := []string{family}, func(int) int32 { return 0 }
	if family == "" {
		_, fc := b.col("family")
		if fc == nil {
			return b
		}
		fams, famOf = fc.Dict, func(i int) int32 { return fc.Idx[i] }
	}
	const drop = -2
	// The new names join a copy of the dictionary, each once: a merge's
	// target already in it keeps its index.
	dict := nc.Dict
	var ids map[string]int32
	newName := func(s string) int32 {
		if ids == nil {
			ids = make(map[string]int32, len(nc.Dict))
			for j, v := range nc.Dict {
				ids[v] = int32(j)
			}
			dict = slices.Clip(dict)
		}
		if id, ok := ids[s]; ok {
			return id
		}
		id := int32(len(dict))
		dict = append(dict, s)
		ids[s] = id
		return id
	}
	// act[f][v] is the new index of family f's name v, or drop; nil for
	// a family no rule names. nullName[f] is for a null name, nullFam[v]
	// for a null family.
	act := make([][]int32, len(fams))
	for f, fam := range fams {
		var row []int32
		for v, name := range nc.Dict {
			a, ok := rs.events[eventKey{fam, name}]
			if !ok {
				continue
			}
			if row == nil {
				row = make([]int32, len(nc.Dict))
				for j := range row {
					row[j] = int32(j)
				}
			}
			if a.drop {
				row[v] = drop
			} else {
				row[v] = newName(a.to)
			}
		}
		act[f] = row
	}
	nullFam := make([]bool, len(nc.Dict))
	for v, name := range nc.Dict {
		nullFam[v] = rs.dropNames[name]
	}
	idx := make([]int32, b.N)
	sel := make([]int32, 0, b.N)
	for i := range b.N {
		v, f := nc.Idx[i], famOf(i)
		switch {
		case f < 0 && v < 0:
			if len(rs.dropFams) > 0 {
				continue
			}
		case f < 0:
			if nullFam[v] {
				continue
			}
		case v < 0:
			if rs.dropFams[fams[f]] {
				continue
			}
		case act[f] != nil:
			if v = act[f][v]; v == drop {
				continue
			}
		}
		idx[len(sel)] = v
		sel = append(sel, int32(i))
	}
	col := &Column{Dict: dict, Idx: idx[:len(sel)]}
	if len(sel) == b.N {
		b.Cols[nameKey] = col
		return b
	}
	out := &Batch{N: len(sel), Cols: make(map[string]*Column, len(b.Cols))}
	for name, c := range b.Cols {
		if name != nameKey {
			out.Cols[name] = c.take(sel)
		}
	}
	out.Cols[nameKey] = col
	return out
}

// visible is an attribute column's name for key as archive spells it: a
// NUL as \u0000.
func visible(name string) string { return strings.ReplaceAll(name, "\x00", `\u0000`) }

// unionColumns is the column names of every events file under dir, of
// every project and day: the columns the events table is built over,
// which its rules are checked against (a rule whose source key no file
// holds is skipped). They come in the order a union by name first sees
// them: the open logs, by project, then the Parquet files, so the first
// spelling of a name equal to another but for case is the table's.
//
// It reads every Parquet file's footer and every open log's keys:
// Engine.columns keeps both, reading only what changed.
func unionColumns(dir string) ([]string, error) {
	return unionColumnsOf(dir, fileColumns)
}

// unionColumnsOf is unionColumns with each file's columns from of (log
// says the file is an open log).
func unionColumnsOf(dir string, of func(path string, log bool) ([]string, error)) ([]string, error) {
	all, err := archive.ListFiles(dir)
	if err != nil {
		return nil, err
	}
	ids := slices.Sorted(maps.Keys(all))
	var out []string
	seen := map[string]bool{}
	add := func(path string, log bool) error {
		names, err := of(path, log)
		if err != nil {
			return err
		}
		names = slices.Clone(names)
		slices.Sort(names) // one file holds no two names equal but for case
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		return nil
	}
	for _, id := range ids {
		for _, path := range all[id].Logs {
			if err := add(path, true); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range ids {
		for _, path := range all[id].Parquet {
			if err := add(path, false); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// fileColumns is the column names of one events file: an open log's (its
// complete lines' keys, as archive.LogColumns names them) or a Parquet
// file's (its footer's).
func fileColumns(path string, log bool) ([]string, error) {
	if log {
		keys, err := archive.NewKeyScanner().Keys(path)
		if err != nil {
			return nil, err
		}
		return logColumnNames(keys), nil
	}
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	names := slices.Clone(f.cols)
	if err := f.close(); err != nil {
		return nil, err
	}
	return names, nil
}

// logColumnNames is the names of the columns a log of keys reads into.
func logColumnNames(keys map[string]bool) []string {
	lc := archive.LogColumns(keys)
	names := make([]string, len(lc))
	for i, c := range lc {
		names[i] = c.Name
	}
	return names
}
