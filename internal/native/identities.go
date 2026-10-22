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

// bytes is about what m takes.
func (m identityMap) bytes() int64 {
	var n int64
	for k, x := range m {
		n += identityBytes + int64(len(k.id)+len(x.name))
	}
	return n
}

// firstMap is the day (days since 1970-01-01) each identity was first
// seen, users and groups apart: all a status lookup needs, without the
// names and counts an identityMap keeps.
type firstMap struct{ users, groups map[string]int32 }

// firstBytes is about what an entry costs in a firstMap, its id aside.
const firstBytes = 32

func newFirstMap() firstMap {
	return firstMap{users: map[string]int32{}, groups: map[string]int32{}}
}

func (m firstMap) of(group bool) map[string]int32 {
	if group {
		return m.groups
	}
	return m.users
}

// see notes identity (group, id) seen on day; it returns the bytes m grew
// by. A new id is copied, so m holds no batch's memory.
func (m firstMap) see(group bool, id string, day int32) int64 {
	ids := m.of(group)
	cur, ok := ids[id]
	switch {
	case !ok:
		ids[strings.Clone(id)] = day
		return firstBytes + int64(len(id))
	case day < cur:
		ids[id] = day
	}
	return 0
}

// fold adds o's identities to m: the earlier day of each.
func (m firstMap) fold(o firstMap) {
	for _, group := range []bool{false, true} {
		ids := m.of(group)
		for id, d := range o.of(group) {
			if cur, ok := ids[id]; !ok || d < cur {
				ids[id] = d
			}
		}
	}
}

// bytes is about what m takes.
func (m firstMap) bytes() int64 {
	var n int64
	for _, ids := range []map[string]int32{m.users, m.groups} {
		for id := range ids {
			n += firstBytes + int64(len(id))
		}
	}
	return n
}

// identitySet is one project's identities, as the reader's identities
// view holds them, kept as its two parts: base (the derived file's rows
// when current, else what the events Parquet files derive) and live
// (what the open logs derive). They are put together only as read
// (rowsIn), and only the identities a query asks for.
type identitySet struct {
	base, live identityMap
	current    bool // base is a current derived file's rows
}

// rowsIn is the identities first seen in r (every one, r nil), put
// together one row per identity: with a current file, as
// identitiesCombine folds its rows and the logs' (each part's name
// dated by its last_seen); else as one part, the files' events and the
// logs' together (each name dated by its event).
func (s *identitySet) rowsIn(r *Range) identityMap {
	lo, hi := everything.days()
	if r != nil {
		lo, hi = r.days()
	}
	in := func(first int64) bool { d := microsDay(first); return d >= lo && d <= hi }
	out := identityMap{}
	for k, b := range s.base {
		l, okl := s.live[k]
		first := b.first
		if okl {
			first = min(first, l.first)
		}
		if in(first) {
			out[k] = s.merge(b, true, l, okl)
		}
	}
	for k, l := range s.live {
		if _, ok := s.base[k]; !ok && in(l.first) {
			out[k] = s.merge(identity{}, false, l, true)
		}
	}
	return out
}

// merge is one identity's row from its base part b and live part l, those
// of them ok.
func (s *identitySet) merge(b identity, okb bool, l identity, okl bool) identity {
	if s.current {
		b.nameAt, l.nameAt = b.last, l.last
	}
	switch {
	case !okb:
		return l
	case !okl:
		return b
	}
	if l.named && (!b.named || l.nameAt > b.nameAt) {
		b.name, b.named, b.nameAt = l.name, true, l.nameAt
	}
	b.first, b.last = min(b.first, l.first), max(b.last, l.last)
	b.views += l.views
	b.events += l.events
	return b
}

// firstSet is one project's first-seen days, in the two parts an
// identitySet has.
type firstSet struct{ base, live firstMap }

// firstDay is the day (days since 1970-01-01) identity (group, id) was
// first seen; ok false for an identity the set lacks.
func (s *firstSet) firstDay(group bool, id string) (day int64, ok bool) {
	b, okb := s.base.of(group)[id]
	l, okl := s.live.of(group)[id]
	switch {
	case okb && okl:
		return int64(min(b, l)), true
	case okb:
		return int64(b), true
	case okl:
		return int64(l), true
	}
	return 0, false
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

// identityBatch is rows, project's identities, as a batch of
// identityColumns, and day: first_seen's day.
func identityBatch(rows identityMap, project int64) *Batch {
	keys := make([]identityKey, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	n := len(keys)
	ints := func(v func(x identity) int64) *Column {
		c := &Column{Ints: make([]int64, n)}
		for i, k := range keys {
			c.Ints[i] = v(rows[k])
		}
		return c
	}
	times := func(v func(x identity) int64) *Column {
		c := &Column{Times: make([]int64, n)}
		for i, k := range keys {
			c.Times[i] = v(rows[k])
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
		x := rows[keys[i]]
		return x.name, x.named
	})
	b.Cols["first_seen"] = times(func(x identity) int64 { return x.first })
	b.Cols["last_seen"] = times(func(x identity) int64 { return x.last })
	b.Cols["views"] = ints(func(x identity) int64 { return x.views })
	b.Cols["events"] = ints(func(x identity) int64 { return x.events })
	b.Cols["day"] = ints(func(x identity) int64 { return microsDay(x.first) })
	return b
}

// idPart names one cached part of a project's identities: the stored
// part or the live one (the open logs'), as whole rows (an identityMap)
// or first-seen days only (a firstMap).
type idPart struct {
	project int64
	live    bool
	first   bool
}

// identityCache is the engine's cache of identity parts, each under the
// signature of the files it was read from, at most MaxBytes of them all
// together, the least recently used dropped first. A project's stored and
// live parts are cached apart, so a log's flush derives the logs again
// and nothing else; a query puts the parts together itself.
type identityCache struct {
	mu      sync.Mutex // never held over a read
	max     int64
	bytes   int64
	tick    uint64
	entries map[idPart]*identityEntry
	flights map[idPart]chan struct{} // a load under way, closed when done
}

// identityEntry is one cached part: full or first, as its idPart says.
type identityEntry struct {
	sig   string
	full  identityMap
	first firstMap
	bytes int64
	used  uint64
}

// get is part's entry if cached under sig, marked used.
func (c *identityCache) get(part idPart, sig string) *identityEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	x := c.entries[part]
	if x == nil || x.sig != sig {
		return nil
	}
	c.tick++
	x.used = c.tick
	return x
}

// put caches x as part, replacing what it had, and drops the least
// recently used parts while the cache holds more than max. A part larger
// than max alone is not cached.
func (c *identityCache) put(part idPart, x *identityEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.entries[part]; old != nil {
		c.bytes -= old.bytes
		delete(c.entries, part)
	}
	if x.bytes > c.max {
		return
	}
	if c.entries == nil {
		c.entries = map[idPart]*identityEntry{}
	}
	c.tick++
	x.used = c.tick
	c.entries[part] = x
	c.bytes += x.bytes
	for c.bytes > c.max {
		var oldest idPart
		var at uint64
		first := true
		for p, y := range c.entries {
			if first || y.used < at {
				oldest, at, first = p, y.used, false
			}
		}
		c.bytes -= c.entries[oldest].bytes
		delete(c.entries, oldest)
	}
}

// size is the bytes the cached parts take.
func (c *identityCache) size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// flight waits while another query loads part, then returns nil; with
// none under way, it registers this one's and returns done, which the
// caller calls once it has loaded (or failed to).
func (c *identityCache) flight(ctx context.Context, part idPart) (done func(), err error) {
	for {
		c.mu.Lock()
		ch, busy := c.flights[part]
		if !busy {
			if c.flights == nil {
				c.flights = map[idPart]chan struct{}{}
			}
			ch = make(chan struct{})
			c.flights[part] = ch
			c.mu.Unlock()
			return func() {
				c.mu.Lock()
				delete(c.flights, part)
				c.mu.Unlock()
				close(ch)
			}, nil
		}
		c.mu.Unlock()
		select {
		case <-ch:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// identityParts is where the two parts of the identities of the project
// whose files f lists (archive.ListFiles) come from, as the reader's
// identities view (archive's derivedView) has them, rules never applied:
//   - with a current derived file (archive.Files.Identities), its rows;
//     without, those derived from the project's events Parquet files;
//   - and those derived from the project's open logs.
//
// Each part's signature names its files (paths, sizes, modification
// times), and the stored one whether it is the derived file.
type identityParts struct {
	current          bool
	baseSig, liveSig string
}

func newIdentityParts(f archive.Files) (identityParts, error) {
	p := identityParts{current: f.Identities != ""}
	stored := f.Parquet
	if p.current {
		stored = []string{f.Identities}
	}
	sig, err := filesSignature(stored)
	if err != nil {
		return p, err
	}
	p.baseSig = fmt.Sprintf("%t\n%s", p.current, sig)
	p.liveSig, err = filesSignature(f.Logs)
	return p, err
}

// identities is the identities of project, as identityParts says, whole
// rows. Each part is cached while its files stay as they were; when any
// changes, that part alone is read again. charge is told the bytes the
// parts take (as a load builds them, or at once from the cache), and may
// refuse them with an error.
func (e *Engine) identities(ctx context.Context, project int64, f archive.Files, charge func(int64) error) (*identitySet, error) {
	p, err := newIdentityParts(f)
	if err != nil {
		return nil, err
	}
	base, err := e.identityPart(ctx, idPart{project: project}, p.baseSig, charge, func() (*identityEntry, error) {
		var m identityMap
		var err error
		if p.current {
			m, err = e.readIdentities(f.Identities, charge)
		} else {
			m, err = e.deriveIdentities(ctx, f.Parquet, nil, charge)
		}
		return &identityEntry{full: m, bytes: m.bytes()}, err
	})
	if err != nil {
		return nil, err
	}
	live, err := e.identityPart(ctx, idPart{project: project, live: true}, p.liveSig, charge, func() (*identityEntry, error) {
		m, err := e.deriveIdentities(ctx, nil, f.Logs, charge)
		return &identityEntry{full: m, bytes: m.bytes()}, err
	})
	if err != nil {
		return nil, err
	}
	return &identitySet{base: base.full, live: live.full, current: p.current}, nil
}

// firstSeen is the first-seen days of project's identities, as
// identities has them, cached and charged as identities says: what the
// user_status and group_status columns look up, at a fraction of the
// whole rows' memory.
func (e *Engine) firstSeen(ctx context.Context, project int64, f archive.Files, charge func(int64) error) (*firstSet, error) {
	p, err := newIdentityParts(f)
	if err != nil {
		return nil, err
	}
	base, err := e.identityPart(ctx, idPart{project: project, first: true}, p.baseSig, charge, func() (*identityEntry, error) {
		var m firstMap
		var err error
		if p.current {
			m, err = e.readFirstSeen(f.Identities, charge)
		} else {
			m, err = e.deriveFirstSeen(ctx, f.Parquet, nil, charge)
		}
		return &identityEntry{first: m, bytes: m.bytes()}, err
	})
	if err != nil {
		return nil, err
	}
	live, err := e.identityPart(ctx, idPart{project: project, live: true, first: true}, p.liveSig, charge, func() (*identityEntry, error) {
		m, err := e.deriveFirstSeen(ctx, nil, f.Logs, charge)
		return &identityEntry{first: m, bytes: m.bytes()}, err
	})
	if err != nil {
		return nil, err
	}
	return &firstSet{base: base.first, live: live.first}, nil
}

// identityPart is part, from the cache when held there under sig, else
// as load builds it (charging as it goes), then cached. Concurrent
// queries missing the cache for one part load it once: the others wait,
// then look again. A cached part is charged at once.
func (e *Engine) identityPart(ctx context.Context, part idPart, sig string, charge func(int64) error,
	load func() (*identityEntry, error)) (*identityEntry, error) {
	for {
		if x := e.idCache.get(part, sig); x != nil {
			return x, charge(x.bytes)
		}
		done, err := e.idCache.flight(ctx, part)
		if err != nil {
			return nil, err
		}
		if done == nil {
			continue // another query loaded it: look again
		}
		defer done()
		if x := e.idCache.get(part, sig); x != nil { // loaded meanwhile
			return x, charge(x.bytes)
		}
		break
	}
	onIdentityLoad(part)
	x, err := load()
	if err != nil {
		return nil, err
	}
	x.sig = sig
	e.idCache.put(part, x)
	return x, nil
}

// onIdentityLoad is called as identityPart loads a part rather than
// taking it from the cache; a variable so a test can count the loads.
var onIdentityLoad = func(idPart) {}

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
	out := identityMap{}
	err := readIdentityFile(path, identityColumns, func(b *Batch) error {
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
		return charge(grew + int64(len(out)-before)*identityBytes)
	})
	return out, err
}

// readFirstSeen reads the first-seen days of a derived identities file
// (one project's), its kind, id and first_seen columns alone.
func (e *Engine) readFirstSeen(path string, charge func(int64) error) (firstMap, error) {
	out := newFirstMap()
	err := readIdentityFile(path, []string{"kind", "id", "first_seen"}, func(b *Batch) error {
		_, kind := b.col("kind")
		_, id := b.col("id")
		_, first := b.col("first_seen")
		var grew int64
		for i := range b.N {
			k, _ := textAt(kind, i)
			s, _ := textAt(id, i)
			ts, _ := timeAt(first, i)
			grew += out.see(k == "group", s, int32(microsDay(ts)))
		}
		return charge(grew)
	})
	return out, err
}

// readIdentityFile reads cols of a derived identities file, each row
// group's batch handed to each.
func readIdentityFile(path string, cols []string, each func(*Batch) error) error {
	groups, err := parquetGroups([]string{path}, "", everything)
	if err != nil {
		return err
	}
	var gr groupReader
	defer gr.close() //nolint:errcheck // read only
	for _, g := range groups {
		g.all = true // no family or day to filter by
		b, err := gr.read(g, cols, "", everything)
		if err != nil {
			return err
		}
		if err := each(b); err != nil {
			return err
		}
	}
	return nil
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
	parts := make([]identityMap, e.workers)
	for i := range parts {
		parts[i] = identityMap{}
	}
	err := e.scanIdentityEvents(ctx, parquet, logs, identityEventColumns, func(w int, b *Batch) error {
		var grew int64
		eachIdentity(b, func(k identityKey, name string, named bool, ts int64, view bool) {
			grew += parts[w].see(k, name, named, ts, view)
		})
		return charge(grew)
	})
	if err != nil {
		return nil, err
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out.fold(p)
	}
	return out, nil
}

// deriveFirstSeen derives one part of the first-seen days as
// deriveIdentities derives one of identities, reading no names.
func (e *Engine) deriveFirstSeen(ctx context.Context, parquet, logs []string, charge func(int64) error) (firstMap, error) {
	parts := make([]firstMap, e.workers)
	for i := range parts {
		parts[i] = newFirstMap()
	}
	err := e.scanIdentityEvents(ctx, parquet, logs, []string{"family", "user_id", "group_id", "ts"}, func(w int, b *Batch) error {
		var grew int64
		eachIdentity(b, func(k identityKey, _ string, _ bool, ts int64, _ bool) {
			grew += parts[w].see(k.group, k.id, int32(microsDay(ts)))
		})
		return charge(grew)
	})
	if err != nil {
		return firstMap{}, err
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out.fold(p)
	}
	return out, nil
}

// scanIdentityEvents reads cols of every event of the Parquet files
// parquet and the logs logs, each batch handed to each with its worker.
// charge, which each may call, is safe from several workers at once.
func (e *Engine) scanIdentityEvents(ctx context.Context, parquet, logs, cols []string, each func(w int, b *Batch) error) error {
	groups, err := parquetGroups(parquet, "", everything)
	if err != nil {
		return err
	}
	work := make([]group, 0, len(logs)+len(groups))
	for _, path := range logs {
		work = append(work, group{file: path, index: -1})
	}
	work = append(work, groups...)
	readers := make([]groupReader, e.workers)
	err = scanParallel(ctx, work, e.workers, func(w int, g group) error {
		var b *Batch
		var err error
		if g.index < 0 {
			b, err = readLog(g.file, cols, "", everything)
		} else {
			b, err = readers[w].read(g, cols, "", everything)
		}
		if err != nil {
			return err
		}
		return each(w, b)
	})
	for i := range readers {
		if cerr := readers[i].close(); err == nil {
			err = cerr
		}
	}
	return err
}

// eachIdentity calls fn for each identity on b's views and product
// events: a user_id that is not empty is a user, a group_id a group, each
// named by its user_name or group_name (when b has them).
func eachIdentity(b *Batch, fn func(k identityKey, name string, named bool, ts int64, view bool)) {
	_, fam := b.col("family")
	_, ts := b.col("ts")
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
			fn(identityKey{who.group, s}, n, named, t, f == "views")
		}
	}
}

// addStatus sets b's user_status and group_status, those of them cols
// names, from ids: new on the day its user (group) was first seen,
// returning on a later day, NULL when the id is NULL or empty, when ids
// lack it (an id only measures carried) or on a day before it was first
// seen.
func addStatus(b *Batch, cols []string, ids *firstSet) {
	for _, name := range cols {
		s, ok := statusColumns[name]
		if !ok {
			continue
		}
		b.Cols[name] = statusColumn(b, s.id, s.group, ids)
	}
}

func statusColumn(b *Batch, idCol string, group bool, ids *firstSet) *Column {
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
