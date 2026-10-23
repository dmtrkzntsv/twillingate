package archive

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parquet-go/parquet-go"
)

// The derived files written in Go, without the engine: each reads the
// events files it comes from row group by row group, only the columns it
// needs, as eventsOf read them through DuckDB (a column a file lacks reads
// NULL, a repeated id counts each time), and writes exactly the rows of
// sessionsSelect, identitiesSelect or aliasesSelect, in their order.

var (
	sessionsColumns = []pqColumn{
		{"project_id", colBigint}, {"day", colDate}, {"kind", colVarchar}, {"actor_id", colVarchar},
		{"actor_kind", colVarchar}, {"session_id", colVarchar}, {"started_at", colTimestamp}, {"ended_at", colTimestamp},
		{"duration_sec", colBigint}, {"views", colBigint}, {"entry_host", colVarchar}, {"entry_path", colVarchar},
		{"exit_path", colVarchar}, {"referrer_source", colVarchar}, {"utm_source", colVarchar}, {"utm_medium", colVarchar},
		{"utm_campaign", colVarchar},
	}
	identitiesColumns = []pqColumn{
		{"project_id", colBigint}, {"kind", colVarchar}, {"id", colVarchar}, {"name", colVarchar},
		{"first_seen", colTimestamp}, {"last_seen", colTimestamp}, {"views", colBigint}, {"events", colBigint},
	}
	aliasesColumns = []pqColumn{
		{"project_id", colBigint}, {"user_id", colVarchar}, {"install_id", colVarchar},
		{"first_seen", colTimestamp}, {"last_seen", colTimestamp}, {"events", colBigint},
	}
)

// eventsFile is an events file opened for a deriver: the fixed columns it
// reads, by name, those the file has.
type eventsFile struct {
	path string
	f    *os.File
	pf   *parquet.File
	cols map[string]inColumn
}

// openEvents opens path for the columns names (fixed ones, lower case). A
// column of another type than fields gives it fails, naming it, as the
// merge fails.
func openEvents(path string, names []string) (*eventsFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("archive: %w", err)
	}
	pf, err := parquet.OpenFile(f, st.Size(), parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true),
		parquet.ReadBufferSize(256<<10))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("archive: %s: %w", filepath.Base(path), err)
	}
	e := &eventsFile{path: path, f: f, pf: pf, cols: map[string]inColumn{}}
	for leaf, fl := range pf.Schema().Fields() {
		if !fl.Leaf() || fl.Repeated() {
			f.Close()
			return nil, fmt.Errorf("archive: %s: column %s is not a flat column", filepath.Base(path), visible(fl.Name()))
		}
		lower := strings.ToLower(fl.Name())
		if _, dup := e.cols[lower]; dup || !slices.Contains(names, lower) {
			continue
		}
		want, _ := colTypeOf(fields[fieldOf(lower)].typ)
		got, scale, ok := leafType(fl.Type())
		if !ok || got != want {
			f.Close()
			return nil, fmt.Errorf("archive: %s: column %s is %s, want %s", filepath.Base(path), visible(fl.Name()), fl.Type(), typeNames[want])
		}
		e.cols[lower] = inColumn{leaf: leaf, typ: want, scale: scale}
	}
	return e, nil
}

// openAll opens every path for names; on a failure it closes what it
// opened.
func openAll(paths, names []string) ([]*eventsFile, error) {
	var files []*eventsFile
	for _, p := range paths {
		e, err := openEvents(p, names)
		if err != nil {
			closeAll(files)
			return nil, err
		}
		files = append(files, e)
	}
	return files, nil
}

func closeAll(files []*eventsFile) {
	for _, e := range files {
		e.f.Close()
	}
}

// read is rows from to to of column name in row group g: nil (every row
// NULL) when the file lacks it.
func (e *eventsFile) read(g int, name string, from, to int) (*vector, error) {
	ic, ok := e.cols[name]
	if !ok {
		return nil, nil
	}
	rg := e.pf.RowGroups()[g]
	v, err := readVector(rg.ColumnChunks()[ic.leaf], ic, int(rg.NumRows()), from, to, false)
	if err != nil {
		return nil, fmt.Errorf("archive: %s row group %d column %s: %w", filepath.Base(e.path), g, name, err)
	}
	return v, nil
}

// readAll is read for several columns.
func (e *eventsFile) readAll(g int, from, to int, names ...string) ([]*vector, error) {
	out := make([]*vector, len(names))
	for i, n := range names {
		v, err := e.read(g, n, from, to)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// cmpNullsLast orders two values of one column, NULL after every value.
func cmpNullsLast(a, b value) int {
	switch {
	case a.null() && b.null():
		return 0
	case a.null():
		return 1
	case b.null():
		return -1
	case a.k == colVarchar:
		return strings.Compare(a.s, b.s)
	}
	return cmp.Compare(a.n, b.n)
}

// writeDerived writes rows as out, in their order, its row groups cut at
// maxGroupRows only; a failure leaves no file and no .tmp.
func writeDerived(out string, cols []pqColumn, each func(write func(row) error) error) error {
	w, err := newRowWriter(out, cols, nil)
	if err != nil {
		return err
	}
	if err := each(w.Write); err != nil {
		w.Abort()
		return err
	}
	return w.Close()
}

var sessionsReads = []string{"family", "day", "project_id", "kind", "actor_id", "actor_kind", "session_id", "id", "ts",
	"host", "path", "referrer_source", "utm_source", "utm_medium", "utm_campaign"}

// viewPart is the rows of one row group that hold views: lo to hi.
type viewPart struct {
	file, group, lo, hi int
}

// viewCols is the columns of a view sessionsSelect takes from its entry or
// exit view, beside what Sessionize reads.
type viewCols struct {
	actorKind, host, path, referrer, utmSource, utmMedium, utmCampaign value
}

// sessionsKey is the project and day a session belongs to, the first two
// keys of sessionsSelect's partition and of its order.
type sessionsKey struct{ project, day value }

func cmpSessionsKey(a, b sessionsKey) int {
	if c := cmpNullsLast(a.project, b.project); c != 0 {
		return c
	}
	return cmpNullsLast(a.day, b.day)
}

// deriveSessions writes the sessions of inputs' views as out:
// sessionsSelect over eventsOf(inputs). It works one project's day at a
// time: it reads each row group's family, project_id and day to find the
// days its views are in, then, for each (project, day) in order (NULL
// last), the groups holding it, keeping only those views. A Go-written
// file holds a day's views in its own groups, so each is read once; an
// older DuckDB file's group spans days and is read once per day it holds.
//
// A view without a ts is sessionized at the epoch, as native's
// deriveSessions does; sessionsSelect would read it otherwise. Ingest
// never writes one.
func deriveSessions(ctx context.Context, inputs []string, out string) error {
	files, err := openAll(inputs, sessionsReads)
	if err != nil {
		return err
	}
	defer closeAll(files)
	parts := map[sessionsKey][]viewPart{}
	for fi, e := range files {
		for g, rg := range e.pf.RowGroups() {
			if err := ctx.Err(); err != nil {
				return err
			}
			n := int(rg.NumRows())
			fam, err := e.read(g, "family", 0, n)
			if err != nil {
				return err
			}
			lo, hi := -1, -1
			for r := range n {
				if isViews(fam.at(r)) {
					if lo < 0 {
						lo = r
					}
					hi = r + 1
				}
			}
			if lo < 0 {
				continue
			}
			c, err := e.readAll(g, lo, hi, "project_id", "day")
			if err != nil {
				return err
			}
			seen := map[sessionsKey]bool{}
			for r := lo; r < hi; r++ {
				if k := (sessionsKey{c[0].at(r), c[1].at(r)}); isViews(fam.at(r)) && !seen[k] {
					seen[k] = true
					parts[k] = append(parts[k], viewPart{fi, g, lo, hi})
				}
			}
		}
	}
	keys := slices.SortedFunc(maps.Keys(parts), cmpSessionsKey)
	return writeDerived(out, sessionsColumns, func(write func(row) error) error {
		buf := make(row, len(sessionsColumns))
		for _, k := range keys {
			if err := ctx.Err(); err != nil {
				return err
			}
			views, extra, err := viewsOf(files, parts[k], k)
			if err != nil {
				return err
			}
			if err := writeSessions(write, buf, k, views, extra); err != nil {
				return err
			}
		}
		return nil
	})
}

func isViews(v value) bool { return v.ok && v.s == "views" }

// viewsOf reads the views of k from parts.
func viewsOf(files []*eventsFile, parts []viewPart, k sessionsKey) ([]SessionView, []viewCols, error) {
	var views []SessionView
	var extra []viewCols
	for _, p := range parts {
		c, err := files[p.file].readAll(p.group, p.lo, p.hi, sessionsReads...)
		if err != nil {
			return nil, nil, err
		}
		fam, day, project, kind, actor, session, id, ts := c[0], c[1], c[2], c[3], c[4], c[6], c[7], c[8]
		for r := p.lo; r < p.hi; r++ {
			if !isViews(fam.at(r)) || day.at(r) != k.day || project.at(r) != k.project {
				continue
			}
			kd, a, i := kind.at(r), actor.at(r), id.at(r)
			views = append(views, SessionView{Kind: kd.s, KindNull: !kd.ok, Actor: a.s, ActorNull: !a.ok, SessionID: session.at(r).s,
				ID: i.s, IDNull: !i.ok, TS: ts.at(r).n, Row: int32(len(extra))})
			extra = append(extra, viewCols{c[5].at(r), c[9].at(r), c[10].at(r), c[11].at(r), c[12].at(r), c[13].at(r), c[14].at(r)})
		}
	}
	return views, extra, nil
}

// writeSessions writes the sessions of k's views, in sessionsSelect's
// order: by started_at, then actor_id (NULL last).
func writeSessions(write func(row) error, buf row, k sessionsKey, views []SessionView, extra []viewCols) error {
	sessions := Sessionize(views)
	slices.SortStableFunc(sessions, func(a, b Session) int {
		if c := cmp.Compare(views[a.First].TS, views[b.First].TS); c != 0 {
			return c
		}
		pa, pb := views[a.Partition], views[b.Partition]
		return cmpNullsLast(value{s: pa.Actor, ok: !pa.ActorNull, k: colVarchar}, value{s: pb.Actor, ok: !pb.ActorNull, k: colVarchar})
	})
	text := func(s string, ok bool) value {
		if !ok {
			return value{}
		}
		return strValue(s)
	}
	for _, s := range sessions {
		first, last, part := views[s.First], views[s.Last], views[s.Partition]
		x, y := extra[first.Row], extra[last.Row]
		buf[0], buf[1] = k.project, k.day
		buf[2], buf[3] = text(part.Kind, !part.KindNull), text(part.Actor, !part.ActorNull)
		buf[4], buf[5] = x.actorKind, text(first.SessionID, s.Declared)
		buf[6], buf[7] = timestampValue(first.TS), timestampValue(last.TS)
		buf[8], buf[9] = intValue(Seconds(last.TS)-Seconds(first.TS)), intValue(s.Views)
		buf[10], buf[11], buf[12] = x.host, x.path, y.path
		buf[13], buf[14], buf[15], buf[16] = x.referrer, x.utmSource, x.utmMedium, x.utmCampaign
		if err := write(buf); err != nil {
			return err
		}
	}
	return nil
}

var identitiesReads = []string{"family", "project_id", "ts", "user_id", "user_name", "group_id", "group_name", "install_id"}

// identityFold is the identities and aliases of a project's events files,
// by project_id: identitiesSelect's and aliasesSelect's groups, folded
// one event at a time. An identity's events without a ts count in its
// views and events but name it and date it no more than in the SQL
// (arg_max, min and max skip them), so they are kept apart.
type identityFold struct {
	ids     map[value]IdentityMap
	untimed map[value]map[IdentityKey][2]int64 // views, product events
	aliases map[value]AliasMap
}

// foldIdentities reads every file's events, row group by row group: its
// state grows with the distinct users, groups and pairs, not the events.
func foldIdentities(ctx context.Context, inputs []string) (*identityFold, error) {
	files, err := openAll(inputs, identitiesReads)
	if err != nil {
		return nil, err
	}
	defer closeAll(files)
	fo := &identityFold{ids: map[value]IdentityMap{}, untimed: map[value]map[IdentityKey][2]int64{}, aliases: map[value]AliasMap{}}
	for _, e := range files {
		for g, rg := range e.pf.RowGroups() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n := int(rg.NumRows())
			c, err := e.readAll(g, 0, n, identitiesReads...)
			if err != nil {
				return nil, err
			}
			for r := range n {
				fo.see(c[0].at(r), c[1].at(r), c[2].at(r), c[3].at(r), c[4].at(r), c[5].at(r), c[6].at(r), c[7].at(r))
			}
		}
	}
	return fo, nil
}

// see folds one event.
func (fo *identityFold) see(fam, project, ts, user, userName, group, groupName, install value) {
	al := fo.aliases[project]
	if al == nil {
		al = AliasMap{}
		fo.aliases[project] = al
	}
	al.See(user.s, install.s, ts.n, ts.ok)
	if !fam.ok || (fam.s != "views" && fam.s != "product") {
		return
	}
	view := fam.s == "views"
	for _, id := range []struct {
		key  IdentityKey
		name value
	}{{IdentityKey{false, user.s}, userName}, {IdentityKey{true, group.s}, groupName}} {
		if id.key.ID == "" {
			continue
		}
		if !ts.ok {
			u := fo.untimed[project]
			if u == nil {
				u = map[IdentityKey][2]int64{}
				fo.untimed[project] = u
			}
			c := u[id.key]
			if view {
				c[0]++
			} else {
				c[1]++
			}
			u[id.key] = c
			continue
		}
		m := fo.ids[project]
		if m == nil {
			m = IdentityMap{}
			fo.ids[project] = m
		}
		m.See(id.key, id.name.s, id.name.ok, ts.n, view)
	}
}

// writeIdentities writes the identities as out, in identitiesSelect's
// order: project_id, kind ('group' before 'user'), id.
func (fo *identityFold) writeIdentities(out string) error {
	projects := map[value]bool{}
	for p := range fo.ids {
		projects[p] = true
	}
	for p := range fo.untimed {
		projects[p] = true
	}
	return writeDerived(out, identitiesColumns, func(write func(row) error) error {
		buf := make(row, len(identitiesColumns))
		for _, p := range slices.SortedFunc(maps.Keys(projects), cmpNullsLast) {
			ids, untimed := fo.ids[p], fo.untimed[p]
			keys := map[IdentityKey]bool{}
			for k := range ids {
				keys[k] = true
			}
			for k := range untimed {
				keys[k] = true
			}
			for _, k := range slices.SortedFunc(maps.Keys(keys), func(a, b IdentityKey) int {
				if a.Group != b.Group {
					return cmp.Compare(boolInt(!a.Group), boolInt(!b.Group))
				}
				return strings.Compare(a.ID, b.ID)
			}) {
				x, timed := ids[k]
				u := untimed[k]
				kind := "user"
				if k.Group {
					kind = "group"
				}
				buf[0], buf[1], buf[2] = p, strValue(kind), strValue(k.ID)
				buf[3], buf[4], buf[5] = value{}, value{}, value{}
				if x.Named {
					buf[3] = strValue(x.Name)
				}
				if timed {
					buf[4], buf[5] = timestampValue(x.First), timestampValue(x.Last)
				}
				buf[6], buf[7] = intValue(x.Views+u[0]), intValue(x.Events+u[1])
				if err := write(buf); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// writeAliases writes the aliases as out, in aliasesSelect's order:
// project_id, user_id, install_id.
func (fo *identityFold) writeAliases(out string) error {
	return writeDerived(out, aliasesColumns, func(write func(row) error) error {
		buf := make(row, len(aliasesColumns))
		for _, p := range slices.SortedFunc(maps.Keys(fo.aliases), cmpNullsLast) {
			m := fo.aliases[p]
			for _, k := range m.Keys() {
				a := m[k]
				buf[0], buf[1], buf[2] = p, strValue(k.UserID), strValue(k.InstallID)
				buf[3], buf[4] = value{}, value{}
				if a.Timed {
					buf[3], buf[4] = timestampValue(a.First), timestampValue(a.Last)
				}
				buf[5] = intValue(a.Events)
				if err := write(buf); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
