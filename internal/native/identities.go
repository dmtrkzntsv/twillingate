package native

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// identityColumns are the identities table's columns, as archive's
// identitiesSelect makes them: project_id, views and events BIGINT,
// first_seen and last_seen TIMESTAMP, kind ('user' or 'group'), id and
// name VARCHAR.
var identityColumns = []string{"project_id", "kind", "id", "name", "first_seen", "last_seen", "views", "events"}

// identityEventColumns are the columns of the events identities are
// derived from.
var identityEventColumns = []string{"family", "user_id", "user_name", "group_id", "group_name", "ts"}

// statusColumns are the computed columns user_status and group_status:
// whether a row's user (group) is new that day, by the id column they
// read and the kind of identity they look it up as.
var statusColumns = map[string]struct {
	id    string
	group bool
}{
	"user_status":  {"user_id", false},
	"group_status": {"group_id", true},
}

// everything is a range holding every day an event can have.
var everything = func() Range {
	from, _ := civil.Parse("0001-01-01")
	to, _ := civil.Parse("9999-12-31")
	return Range{From: from, To: to}
}()

// identityKey is one identity: a user's id, or a group's.
type identityKey struct {
	group bool
	id    string
}

// identity is one identity's row: its name and when that was sent (within
// one part, the name's event; across parts, the part's last_seen), its
// first and last event (unix microseconds), its views and product events.
type identity struct {
	name          string
	named         bool
	nameAt        int64
	first, last   int64
	views, events int64
}

// identityBytes is about what an identity costs in a map, its strings
// aside.
const identityBytes = 112

// identityMap is a set of identities by key.
type identityMap map[identityKey]identity

// see adds one event of identity k to m, as identitiesSelect aggregates
// one part: the name of the latest event that carried one
// (arg_max(name, ts) FILTER (WHERE name IS NOT NULL)), the first and last
// event, the views and product events counted. It returns the bytes m
// grew by.
func (m identityMap) see(k identityKey, name string, named bool, ts int64, view bool) int64 {
	cur, ok := m[k]
	var grew int64
	if !ok {
		cur = identity{first: ts, last: ts}
		grew = identityBytes + int64(len(k.id))
	}
	if named && (!cur.named || ts > cur.nameAt) {
		grew += int64(len(name) - len(cur.name))
		cur.name, cur.named, cur.nameAt = name, true, ts
	}
	cur.first, cur.last = min(cur.first, ts), max(cur.last, ts)
	if view {
		cur.views++
	} else {
		cur.events++
	}
	m[k] = cur
	return grew
}

// fold adds o's identities to m within one part: the name of the later
// event, as see takes it.
func (m identityMap) fold(o identityMap) {
	for k, x := range o {
		cur, ok := m[k]
		if !ok {
			m[k] = x
			continue
		}
		if x.named && (!cur.named || x.nameAt > cur.nameAt) {
			cur.name, cur.named, cur.nameAt = x.name, true, x.nameAt
		}
		cur.first, cur.last = min(cur.first, x.first), max(cur.last, x.last)
		cur.views += x.views
		cur.events += x.events
		m[k] = cur
	}
}

// combineIdentities folds parts (a stored file's rows, those derived on
// read) into one row per identity, as identitiesCombine does: the name of
// the part with the latest last_seen that has one, the earliest
// first_seen, the latest last_seen, the counts added.
func combineIdentities(parts ...identityMap) identityMap {
	out := identityMap{}
	for _, p := range parts {
		for k, x := range p {
			x.nameAt = x.last
			cur, ok := out[k]
			if !ok {
				out[k] = x
				continue
			}
			if x.named && (!cur.named || x.nameAt > cur.nameAt) {
				cur.name, cur.named, cur.nameAt = x.name, true, x.nameAt
			}
			cur.first, cur.last = min(cur.first, x.first), max(cur.last, x.last)
			cur.views += x.views
			cur.events += x.events
			out[k] = cur
		}
	}
	return out
}

// bytes is about what m takes.
func (m identityMap) bytes() int64 {
	var n int64
	for k, x := range m {
		n += identityBytes + int64(len(k.id)+len(x.name))
	}
	return n
}

// identitySet is one project's identities, as the reader's identities
// view holds them.
type identitySet struct {
	rows  identityMap
	bytes int64
}

// firstDay is the day (days since 1970-01-01) identity k was first seen;
// ok false for an identity the set lacks.
func (s *identitySet) firstDay(group bool, id string) (day int64, ok bool) {
	x, ok := s.rows[identityKey{group, id}]
	if !ok {
		return 0, false
	}
	return microsDay(x.first), true
}

// microsDay is the UTC day of ts (unix microseconds), as ts::DATE.
func microsDay(ts int64) int64 {
	const perDay = 86400 * 1_000_000
	d := ts / perDay
	if ts%perDay < 0 {
		d--
	}
	return d
}

// batch is the identities of project first seen in r (every one, when r
// is nil) as a batch of identityColumns, and day: first_seen's day.
func (s *identitySet) batch(project int64, r *Range) *Batch {
	var lo, hi int64
	if r != nil {
		lo, hi = r.days()
	}
	var keys []identityKey
	for k, x := range s.rows {
		if d := microsDay(x.first); r == nil || (d >= lo && d <= hi) {
			keys = append(keys, k)
		}
	}
	n := len(keys)
	ints := func(v func(x identity) int64) *Column {
		c := &Column{Ints: make([]int64, n)}
		for i, k := range keys {
			c.Ints[i] = v(s.rows[k])
		}
		return c
	}
	times := func(v func(x identity) int64) *Column {
		c := &Column{Times: make([]int64, n)}
		for i, k := range keys {
			c.Times[i] = v(s.rows[k])
		}
		return c
	}
	b := &Batch{N: n, Cols: make(map[string]*Column, len(identityColumns)+1)}
	b.Cols["project_id"] = ints(func(identity) int64 { return project })
	b.Cols["kind"] = textColumn(n, func(i int) (string, bool) {
		if keys[i].group {
			return "group", true
		}
		return "user", true
	})
	b.Cols["id"] = textColumn(n, func(i int) (string, bool) { return keys[i].id, true })
	b.Cols["name"] = textColumn(n, func(i int) (string, bool) {
		x := s.rows[keys[i]]
		return x.name, x.named
	})
	b.Cols["first_seen"] = times(func(x identity) int64 { return x.first })
	b.Cols["last_seen"] = times(func(x identity) int64 { return x.last })
	b.Cols["views"] = ints(func(x identity) int64 { return x.views })
	b.Cols["events"] = ints(func(x identity) int64 { return x.events })
	b.Cols["day"] = ints(func(x identity) int64 { return microsDay(x.first) })
	return b
}

// maxCachedIdentities bounds how many projects' identities an engine
// keeps between queries; the least recently used go first.
const maxCachedIdentities = 8

// identityCache is one project's identities as last loaded: the stored
// part (the derived file, or, with none current, what the events Parquet
// files derive) by the signature of the files it was read from, and the
// whole set by that and the logs' signature.
type identityCache struct {
	base, logs string
	baseRows   identityMap
	set        *identitySet
	used       uint64
}

// identities is project's identities, as the reader's identities view
// (archive's derivedView) holds them, rules never applied:
//   - with a current derived file (archive.Files.Identities), its rows
//     combined with those derived from the project's open logs;
//   - without, those derived from all the project's events, the Parquet
//     files and the logs.
//
// The stored part is cached between queries while its files (paths,
// sizes, modification times) stay as they were; the logs, which are small,
// are derived again whenever any of them changed (grew, came or went).
// charge is told the bytes the set takes, and the maps a derivation
// builds as they grow, and may refuse them with an error.
func (e *Engine) identities(ctx context.Context, project int64, charge func(int64) error) (*identitySet, error) {
	all, err := archive.ListFiles(e.dir)
	if err != nil {
		return nil, err
	}
	f := all[project]
	stored, current := f.Parquet, false
	if f.Identities != "" {
		stored, current = []string{f.Identities}, true
	}
	base, err := filesSignature(stored)
	if err != nil {
		return nil, err
	}
	base = fmt.Sprintf("%t\n%s", current, base)
	logs, err := filesSignature(f.Logs)
	if err != nil {
		return nil, err
	}

	e.idMu.Lock()
	c := e.idCache[project]
	var baseRows identityMap
	var set *identitySet
	if c != nil && c.base == base {
		baseRows = c.baseRows
		if c.logs == logs {
			set = c.set
		}
	}
	e.idMu.Unlock()
	if set != nil {
		if err := charge(set.bytes); err != nil {
			return nil, err
		}
		e.touchIdentities(project, c)
		return set, nil
	}

	if baseRows == nil {
		if current {
			baseRows, err = e.readIdentities(f.Identities, charge)
		} else {
			baseRows, err = e.deriveIdentities(ctx, f.Parquet, nil, charge)
		}
		if err != nil {
			return nil, err
		}
	}
	live, err := e.deriveIdentities(ctx, nil, f.Logs, charge)
	if err != nil {
		return nil, err
	}
	var rows identityMap
	if current {
		rows = combineIdentities(baseRows, live)
	} else {
		// One part: the stored files' events and the logs' together.
		rows = make(identityMap, len(baseRows)+len(live))
		rows.fold(baseRows)
		rows.fold(live)
		rows = combineIdentities(rows)
	}
	set = &identitySet{rows: rows, bytes: rows.bytes()}
	if err := charge(set.bytes); err != nil {
		return nil, err
	}
	e.touchIdentities(project, &identityCache{base: base, logs: logs, baseRows: baseRows, set: set})
	return set, nil
}

// touchIdentities puts c in the cache as project's and marks it used,
// dropping the least recently used projects past maxCachedIdentities.
func (e *Engine) touchIdentities(project int64, c *identityCache) {
	e.idMu.Lock()
	defer e.idMu.Unlock()
	if e.idCache == nil {
		e.idCache = map[int64]*identityCache{}
	}
	e.idTick++
	c.used = e.idTick
	e.idCache[project] = c
	for len(e.idCache) > maxCachedIdentities {
		var oldest int64
		var at uint64
		first := true
		for p, x := range e.idCache {
			if first || x.used < at {
				oldest, at, first = p, x.used, false
			}
		}
		delete(e.idCache, oldest)
	}
}

// filesSignature identifies paths by their names, sizes and modification
// times.
func filesSignature(paths []string) (string, error) {
	var sb strings.Builder
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "%s\x00%d\x00%d\n", p, st.Size(), st.ModTime().UnixNano())
	}
	return sb.String(), nil
}

// readIdentities reads a derived identities file (one project's).
func (e *Engine) readIdentities(path string, charge func(int64) error) (identityMap, error) {
	groups, err := parquetGroups([]string{path}, "", everything)
	if err != nil {
		return nil, err
	}
	var gr groupReader
	defer gr.close() //nolint:errcheck // read only
	out := identityMap{}
	for _, g := range groups {
		g.all = true // no family or day to filter by
		b, err := gr.read(g, identityColumns, "", everything)
		if err != nil {
			return nil, err
		}
		_, kind := b.col("kind")
		_, id := b.col("id")
		_, name := b.col("name")
		_, first := b.col("first_seen")
		_, last := b.col("last_seen")
		_, views := b.col("views")
		_, events := b.col("events")
		before := len(out)
		var grew int64
		for i := range b.N {
			k, _ := textAt(kind, i)
			s, _ := textAt(id, i)
			x := identity{}
			x.name, x.named = textAt(name, i)
			x.first, _ = timeAt(first, i)
			x.last, _ = timeAt(last, i)
			x.nameAt = x.last
			x.views, _ = intAt(views, i)
			x.events, _ = intAt(events, i)
			out[identityKey{group: k == "group", id: s}] = x
			grew += int64(len(s) + len(x.name))
		}
		if err := charge(grew + int64(len(out)-before)*identityBytes); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// timeAt is c's TIMESTAMP at row i; ok false for NULL.
func timeAt(c *Column, i int) (int64, bool) {
	if c == nil || c.Times == nil || (c.Null != nil && c.Null[i]) {
		return 0, false
	}
	return c.Times[i], true
}

// deriveIdentities derives one part of identities from the views and
// product events of the Parquet files parquet and the logs logs (every
// day, no rule), as identitiesSelect does: a user_id that is not empty
// is a user, a group_id a group, each named by its user_name or
// group_name.
func (e *Engine) deriveIdentities(ctx context.Context, parquet, logs []string, charge func(int64) error) (identityMap, error) {
	groups, err := parquetGroups(parquet, "", everything)
	if err != nil {
		return nil, err
	}
	work := make([]group, 0, len(logs)+len(groups))
	for _, path := range logs {
		work = append(work, group{file: path, index: -1})
	}
	work = append(work, groups...)
	parts := make([]identityMap, e.workers)
	for i := range parts {
		parts[i] = identityMap{}
	}
	readers := make([]groupReader, e.workers)
	var mu sync.Mutex // charge from one worker at a time
	err = scanParallel(ctx, work, e.workers, func(w int, g group) error {
		var b *Batch
		var err error
		if g.index < 0 {
			b, err = readLog(g.file, identityEventColumns, "", everything)
		} else {
			b, err = readers[w].read(g, identityEventColumns, "", everything)
		}
		if err != nil {
			return err
		}
		grew := seeIdentities(parts[w], b)
		mu.Lock()
		defer mu.Unlock()
		return charge(grew)
	})
	for i := range readers {
		if cerr := readers[i].close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return nil, err
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out.fold(p)
	}
	return out, nil
}

// seeIdentities adds b's views and product events to m; it returns the
// bytes m grew by.
func seeIdentities(m identityMap, b *Batch) int64 {
	_, fam := b.col("family")
	_, ts := b.col("ts")
	var grew int64
	for _, who := range []struct {
		id, name string
		group    bool
	}{{"user_id", "user_name", false}, {"group_id", "group_name", true}} {
		_, id := b.col(who.id)
		_, name := b.col(who.name)
		for i := range b.N {
			f, _ := textAt(fam, i)
			if f != "views" && f != "product" {
				continue
			}
			s, ok := textAt(id, i)
			if !ok || s == "" {
				continue
			}
			t, ok := timeAt(ts, i)
			if !ok {
				continue
			}
			n, named := textAt(name, i)
			grew += m.see(identityKey{who.group, s}, n, named, t, f == "views")
		}
	}
	return grew
}

// addStatus sets b's user_status and group_status, those of them cols
// names, from ids: new on the day its user (group) was first seen,
// returning on a later day, NULL when the id is NULL or empty, when ids
// lack it (an id only measures carried) or on a day before it was first
// seen.
func addStatus(b *Batch, cols []string, ids *identitySet) {
	for _, name := range cols {
		s, ok := statusColumns[name]
		if !ok {
			continue
		}
		b.Cols[name] = statusColumn(b, s.id, s.group, ids)
	}
}

func statusColumn(b *Batch, idCol string, group bool, ids *identitySet) *Column {
	_, ic := b.col(idCol)
	_, dc := b.col("day")
	out := &Column{Dict: []string{"new", "returning"}, Idx: make([]int32, b.N)}
	var first []int64
	if ic != nil && ic.Idx != nil {
		first = make([]int64, len(ic.Dict))
		for j, s := range ic.Dict {
			first[j] = -1 << 62
			if s == "" {
				continue
			}
			if d, ok := ids.firstDay(group, s); ok {
				first[j] = d
			}
		}
	}
	for i := range b.N {
		out.Idx[i] = -1
		if first == nil {
			continue
		}
		j := ic.Idx[i]
		if j < 0 || first[j] == -1<<62 {
			continue
		}
		switch d, ok := intAt(dc, i); {
		case !ok:
		case d == first[j]:
			out.Idx[i] = 0
		case d > first[j]:
			out.Idx[i] = 1
		}
	}
	return out
}
