package native

import (
	"context"
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
	return e.scanSessionsWith(ctx, project, r, cols, e.readLog, charge, add)
}

// logReader reads a log's rows as readLog does, in one batch or several
// (each read-only: their columns may be shared with other queries),
// giving up when ctx is done.
//
// The rows need not come in the log's line order: the engine's cache
// hands a batch per family, a family's rows in line order. A reader
// that breaks ties by the order it meets rows (identities' latest name
// at equal timestamps, seeName and fold, keep the first met) may then
// break one differently than line order would; it already differs from
// any fixed order across files, which the workers read in parallel, and
// within one log it is the same for the same log.
type logReader func(ctx context.Context, path string, cols []string, family string, r Range) ([]*Batch, error)

// scanSessionsWith is scanSessions, a gap day's logs read through logs.
func (e *Engine) scanSessionsWith(ctx context.Context, project int64, r Range, cols []string, logs logReader,
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
			b, err = deriveSessions(ctx, project, g.day, events, &readers[w], logs, charge)
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

// deriveSessions derives project's sessions of day (days since 1970-01-01)
// from its views in events (Parquet files and logs, read without rules),
// as archive's sessionsSelect does (archive.Sessionize).
//
// gr reads the Parquet row groups (the worker's reader), readLogs the
// logs (readLog); charge as scanSessions describes.
func deriveSessions(ctx context.Context, project, day int64, events archive.Files, gr *groupReader, readLogs logReader, charge func(int64) error) (*Batch, error) {
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
		bs, err := readLogs(ctx, path, viewColumns, "views", dr)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			if err := keep(b); err != nil {
				return nil, err
			}
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
	if err := hold(int64(n) * archive.SessionViewBytes); err != nil {
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
	rows := make([]archive.SessionView, 0, n)
	for k, b := range batches {
		c := cols[k]
		for i := range b.N {
			r := archive.SessionView{Batch: int32(k), Row: int32(i)}
			var ok bool
			r.Kind, ok = textAt(c[cKind], i)
			r.KindNull = !ok
			r.Actor, ok = textAt(c[cActor], i)
			r.ActorNull = !ok
			r.SessionID, _ = textAt(c[cSession], i)
			r.ID, ok = textAt(c[cID], i)
			r.IDNull = !ok
			if ts := c[cTS]; ts != nil && ts.Times != nil {
				r.TS = ts.Times[i]
			}
			rows = append(rows, r)
		}
	}
	sessions := archive.Sessionize(rows)

	m := len(sessions)
	text := func(of func(s archive.Session) archive.SessionView, col int) *Column {
		return textColumn(m, func(i int) (string, bool) {
			r := of(sessions[i])
			return textAt(cols[r.Batch][col], int(r.Row))
		})
	}
	first := func(s archive.Session) archive.SessionView { return rows[s.First] }
	last := func(s archive.Session) archive.SessionView { return rows[s.Last] }
	out := &Batch{N: m, Cols: make(map[string]*Column, len(sessionColumns))}
	ints := func(v func(s archive.Session) int64) *Column {
		c := &Column{Ints: make([]int64, m)}
		for i, s := range sessions {
			c.Ints[i] = v(s)
		}
		return c
	}
	out.Cols["project_id"] = ints(func(archive.Session) int64 { return project })
	out.Cols["day"] = ints(func(archive.Session) int64 { return day })
	out.Cols["kind"] = textColumn(m, func(i int) (string, bool) {
		r := rows[sessions[i].Partition]
		return r.Kind, !r.KindNull
	})
	out.Cols["actor_id"] = textColumn(m, func(i int) (string, bool) {
		r := rows[sessions[i].Partition]
		return r.Actor, !r.ActorNull
	})
	out.Cols["actor_kind"] = text(first, cActorKind)
	out.Cols["session_id"] = textColumn(m, func(i int) (string, bool) {
		s := sessions[i]
		return rows[s.First].SessionID, s.Declared
	})
	started := &Column{Times: make([]int64, m)}
	ended := &Column{Times: make([]int64, m)}
	for i, s := range sessions {
		started.Times[i], ended.Times[i] = rows[s.First].TS, rows[s.Last].TS
	}
	out.Cols["started_at"], out.Cols["ended_at"] = started, ended
	out.Cols["duration_sec"] = ints(func(s archive.Session) int64 {
		return archive.Seconds(rows[s.Last].TS) - archive.Seconds(rows[s.First].TS)
	})
	out.Cols["views"] = ints(func(s archive.Session) int64 { return s.Views })
	out.Cols["entry_host"] = text(first, cHost)
	out.Cols["entry_path"] = text(first, cPath)
	out.Cols["exit_path"] = text(last, cPath)
	out.Cols["referrer_source"] = text(first, cReferrer)
	out.Cols["utm_source"] = text(first, cUTMSource)
	out.Cols["utm_medium"] = text(first, cUTMMedium)
	out.Cols["utm_campaign"] = text(first, cUTMCampaign)
	return out, nil
}
