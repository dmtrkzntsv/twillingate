package native

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// sessionColumns are the sessions table's columns, in the order of
// archive's sessionsSelect: project_id, duration_sec and views BIGINT, day
// DATE, started_at and ended_at TIMESTAMP, the others VARCHAR.
var sessionColumns = []string{
	"project_id", "day", "kind", "actor_id", "actor_kind", "session_id", "started_at", "ended_at",
	"duration_sec", "views", "entry_host", "entry_path", "exit_path", "referrer_source",
	"utm_source", "utm_medium", "utm_campaign",
}

// sessionGapSeconds is archive's: the silence after which an actor's next
// undeclared view starts a session.
const sessionGapSeconds = 1800

// viewColumns are the columns of the views a gap day's sessions are
// derived from.
var viewColumns = []string{
	"kind", "actor_id", "actor_kind", "session_id", "id", "ts", "host", "path",
	"referrer_source", "utm_source", "utm_medium", "utm_campaign",
}

// gapIndex marks a work item that derives one gap day's sessions (its day
// in group.day) rather than reading a row group.
const gapIndex = -2

// scanSessions reads project's sessions of r, as the reader's sessions
// view holds them (rules never apply): the rows of the current session
// files (archive.Files.Sessions) whose day is in r, with the columns cols
// (a session file lacking one reads it all null); and, for each day of r
// no current file covers (SessionGaps), the sessions derived from that
// day's views in the events files and logs, with every column. add gets
// each batch from worker w's goroutine, as scanParallel calls it.
//
// A gap day is derived whole in one worker, holding only that day's views;
// charge is told the bytes they take as they are read (and may refuse
// them with an error), and given them back, as a negative count, once the
// day is done.
func (e *Engine) scanSessions(ctx context.Context, project int64, r Range, cols []string,
	charge func(int64) error, add func(w int, b *Batch) error) error {
	files, gaps, events, err := sessionSources(e.dir, project, r)
	if err != nil {
		return err
	}
	groups, err := parquetGroups(files, "", r)
	if err != nil {
		return err
	}
	// The gap days, derived from their views, are the heavier items: they
	// go first, so the row groups fill in beside them.
	work := make([]group, 0, len(gaps)+len(groups))
	for _, d := range gaps {
		work = append(work, group{index: gapIndex, day: epochDay(d)})
	}
	work = append(work, groups...)
	readers := make([]groupReader, e.workers)
	err = scanParallel(ctx, work, e.workers, func(w int, g group) error {
		var b *Batch
		var err error
		if g.index == gapIndex {
			b, err = deriveSessions(project, g.day, events, &readers[w], charge)
		} else {
			b, err = readers[w].read(g, cols, "", r)
		}
		if err != nil {
			return err
		}
		return add(w, b)
	})
	for i := range readers {
		if cerr := readers[i].close(); err == nil {
			err = cerr
		}
	}
	return err
}

// viewRow is one view of a gap day, as sessionizing orders it: by kind and
// actor (the partition), then ts and id (NULL ids last, as DuckDB sorts
// them ascending). b and i find the row's other columns.
type viewRow struct {
	kind, actor, sid, id        string
	kindNull, actorNull, idNull bool
	ts                          int64 // unix microseconds
	b, i                        int32
}

// viewRowBytes is about what a viewRow costs.
const viewRowBytes = 96

func cmpViewRow(a, b viewRow) int {
	if c := cmpPartition(a, b); c != 0 {
		return c
	}
	if c := cmp.Compare(a.ts, b.ts); c != 0 {
		return c
	}
	if c := cmp.Compare(boolInt(a.idNull), boolInt(b.idNull)); c != 0 {
		return c
	}
	return cmp.Compare(a.id, b.id)
}

func cmpPartition(a, b viewRow) int {
	if c := cmp.Compare(boolInt(a.kindNull), boolInt(b.kindNull)); c != 0 {
		return c
	}
	if c := cmp.Compare(a.kind, b.kind); c != 0 {
		return c
	}
	if c := cmp.Compare(boolInt(a.actorNull), boolInt(b.actorNull)); c != 0 {
		return c
	}
	return cmp.Compare(a.actor, b.actor)
}

// seconds is ts's whole seconds: floor(epoch(ts)).
func seconds(ts int64) int64 {
	s := ts / 1e6
	if ts%1e6 < 0 {
		s--
	}
	return s
}

// batchBytes is about what b's columns take.
func batchBytes(b *Batch) int64 {
	var n int64
	for _, c := range b.Cols {
		n += int64(len(c.Idx))*4 + int64(len(c.Ints)+len(c.Floats)+len(c.Times))*8 + int64(len(c.Null))
		for _, s := range c.Dict {
			n += int64(len(s)) + 16
		}
	}
	return n
}

// session is one session being derived: its first and last view (indexes
// into the day's sorted rows) and its views.
type session struct {
	first, last int
	views       int64
	declared    bool
}

// sessionKey is a session's key within its partition: a declared session
// id ('id:' in archive), or the run of undeclared views ('gap:').
type sessionKey struct {
	declared bool
	sid      string
	run      int
}

// deriveSessions derives project's sessions of day (days since 1970-01-01)
// from its views in events (Parquet files and logs, read without rules),
// as archive's sessionsSelect does:
//   - views are partitioned by (kind, actor_id), NULL one value, and
//     ordered by whole second t, then ts and id;
//   - a view with a session id belongs to that id's session; any other
//     starts a session when it is the partition's first view or comes more
//     than sessionGapSeconds after the view before it, whichever session
//     that was in. Views in one second are one peer group, as the window's
//     running sum counts them together: the group's first view decides;
//   - a session's entry columns are its first view's by (ts, id), its exit
//     path its last view's, its duration the whole seconds between them,
//     its session_id the declared id or NULL.
//
// gr reads the Parquet row groups (the worker's reader); charge as
// scanSessions describes.
func deriveSessions(project, day int64, events archive.Files, gr *groupReader, charge func(int64) error) (*Batch, error) {
	d := civil.DateOf(time.Unix(day*86400, 0).UTC())
	dr := Range{From: d, To: d}
	var held int64
	defer func() {
		if held > 0 {
			charge(-held) //nolint:errcheck // giving back never fails
		}
	}()
	hold := func(n int64) error {
		held += n
		return charge(n)
	}
	pq, logs := filesIn(events, dr)
	groups, err := parquetGroups(pq, "views", dr)
	if err != nil {
		return nil, err
	}
	var batches []*Batch
	keep := func(b *Batch) error {
		if b.N == 0 {
			return nil
		}
		batches = append(batches, b)
		return hold(batchBytes(b))
	}
	for _, path := range logs {
		b, err := readLog(path, viewColumns, "views", dr)
		if err != nil {
			return nil, err
		}
		if err := keep(b); err != nil {
			return nil, err
		}
	}
	for _, g := range groups {
		b, err := gr.read(g, viewColumns, "views", dr)
		if err != nil {
			return nil, err
		}
		if err := keep(b); err != nil {
			return nil, err
		}
	}

	// The columns of each batch, by viewColumns' index.
	cols := make([][]*Column, len(batches))
	n := 0
	for k, b := range batches {
		cols[k] = make([]*Column, len(viewColumns))
		for j, name := range viewColumns {
			_, cols[k][j] = b.col(name)
		}
		n += b.N
	}
	if err := hold(int64(n) * viewRowBytes); err != nil {
		return nil, err
	}
	const (
		cKind = iota
		cActor
		cActorKind
		cSession
		cID
		cTS
		cHost
		cPath
		cReferrer
		cUTMSource
		cUTMMedium
		cUTMCampaign
	)
	rows := make([]viewRow, 0, n)
	for k, b := range batches {
		c := cols[k]
		for i := range b.N {
			r := viewRow{b: int32(k), i: int32(i)}
			var ok bool
			r.kind, ok = textAt(c[cKind], i)
			r.kindNull = !ok
			r.actor, ok = textAt(c[cActor], i)
			r.actorNull = !ok
			r.sid, _ = textAt(c[cSession], i)
			r.id, ok = textAt(c[cID], i)
			r.idNull = !ok
			if ts := c[cTS]; ts != nil && ts.Times != nil {
				r.ts = ts.Times[i]
			}
			rows = append(rows, r)
		}
	}
	slices.SortFunc(rows, cmpViewRow)

	var (
		sessions  []session
		partition []int // each session's first row, for its kind and actor
	)
	for start := 0; start < len(rows); {
		end := start + 1
		for end < len(rows) && cmpPartition(rows[start], rows[end]) == 0 {
			end++
		}
		keys := map[sessionKey]int{}
		run := 0
		var prev int64
		for j := start; j < end; {
			t := seconds(rows[j].ts)
			k := j + 1
			for k < end && seconds(rows[k].ts) == t {
				k++
			}
			if rows[j].sid == "" && (j == start || t-prev > sessionGapSeconds) {
				run++
			}
			for x := j; x < k; x++ {
				key := sessionKey{run: run}
				if sid := rows[x].sid; sid != "" {
					key = sessionKey{declared: true, sid: sid}
				}
				s, ok := keys[key]
				if !ok {
					s = len(sessions)
					keys[key] = s
					sessions = append(sessions, session{first: x, declared: key.declared})
					partition = append(partition, start)
				}
				sessions[s].last = x
				sessions[s].views++
			}
			prev, j = t, k
		}
		start = end
	}

	m := len(sessions)
	text := func(of func(s session) viewRow, col int) *Column {
		return textColumn(m, func(i int) (string, bool) {
			r := of(sessions[i])
			return textAt(cols[r.b][col], int(r.i))
		})
	}
	first := func(s session) viewRow { return rows[s.first] }
	last := func(s session) viewRow { return rows[s.last] }
	out := &Batch{N: m, Cols: make(map[string]*Column, len(sessionColumns))}
	ints := func(v func(s session) int64) *Column {
		c := &Column{Ints: make([]int64, m)}
		for i, s := range sessions {
			c.Ints[i] = v(s)
		}
		return c
	}
	out.Cols["project_id"] = ints(func(session) int64 { return project })
	out.Cols["day"] = ints(func(session) int64 { return day })
	out.Cols["kind"] = textColumn(m, func(i int) (string, bool) {
		r := rows[partition[i]]
		return r.kind, !r.kindNull
	})
	out.Cols["actor_id"] = textColumn(m, func(i int) (string, bool) {
		r := rows[partition[i]]
		return r.actor, !r.actorNull
	})
	out.Cols["actor_kind"] = text(first, cActorKind)
	out.Cols["session_id"] = textColumn(m, func(i int) (string, bool) {
		s := sessions[i]
		return rows[s.first].sid, s.declared
	})
	started := &Column{Times: make([]int64, m)}
	ended := &Column{Times: make([]int64, m)}
	for i, s := range sessions {
		started.Times[i], ended.Times[i] = rows[s.first].ts, rows[s.last].ts
	}
	out.Cols["started_at"], out.Cols["ended_at"] = started, ended
	out.Cols["duration_sec"] = ints(func(s session) int64 { return seconds(rows[s.last].ts) - seconds(rows[s.first].ts) })
	out.Cols["views"] = ints(func(s session) int64 { return s.views })
	out.Cols["entry_host"] = text(first, cHost)
	out.Cols["entry_path"] = text(first, cPath)
	out.Cols["exit_path"] = text(last, cPath)
	out.Cols["referrer_source"] = text(first, cReferrer)
	out.Cols["utm_source"] = text(first, cUTMSource)
	out.Cols["utm_medium"] = text(first, cUTMMedium)
	out.Cols["utm_campaign"] = text(first, cUTMCampaign)
	return out, nil
}
