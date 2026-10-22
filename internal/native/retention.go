package native

import (
	"context"
	"math"
	"slices"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// retentionSpec is what a retention scan gathers: the actors whose cohort
// day is in [from, to], and their days back up to offsets days on.
type retentionSpec struct {
	from, to civil.Date
	offsets  int
}

// noUserDay is a retentionActor's user day when it was never a user.
const noUserDay = math.MaxInt64

// retentionActor is one actor's retention state, as the retention
// widgets' acts and firsts CTEs (cohorts.sql) have it: cohort is its
// first day (days since 1970-01-01) with a views or product event, user
// its first day as a user (noUserDay when none), active its days back,
// bit o for cohort + o, o from 0 to the offsets.
type retentionActor struct {
	id     string
	cohort int64
	user   int64
	active []uint64
}

// activeOn says whether the actor was active o days after its cohort day.
func (a *retentionActor) activeOn(o int) bool {
	return o >= 0 && o/64 < len(a.active) && a.active[o/64]>>(o%64)&1 == 1
}

// retentionState is one actor's state while the scan runs: its first day
// and first day as a user so far, and the days it was active within the
// scan's window [lo, hi] (bit d − lo for day d), nil once its first day is
// before lo, since its cohort is then out of the range whatever comes.
type retentionState struct {
	first, user int64
	window      []uint64
}

// retentionEntryBytes is about what an actor costs in the scan's map, its
// id and window aside; a window costs 24 bytes and its words.
const retentionEntryBytes = 96

// retentionColumns are the columns the scan reads, beside its filters'.
var retentionColumns = []string{"day", "family", "actor_id", "actor_kind"}

// retentionFiles is f's Parquet files and logs that may hold rows on any
// day up to hi: the cohort day is the first over all history.
func retentionFiles(f archive.Files, hi civil.Date) (parquet, logs []string) {
	return filesIn(f, Range{From: everything.From, To: hi})
}

// retentionActors gathers the retention state of the project's actors
// whose cohort day is in [spec.from, spec.to], as cohorts.sql's acts and
// firsts read the events: rows of families views and product (the rules
// applied as events applies them, then the scan's filters) whose
// actor_kind is user or install and actor_id not empty, on a day before
// today (the engine's clock) and no later than to + offsets, over all
// history before that, so an actor first seen before from is in no
// cohort of the range.
//
// One pass reads it all. An actor's cohort day is known only once every
// file is read, so each keeps its days within [from, hi] (hi the last day
// read) as a bitset of that window, which holds every day a cohort of the
// range can count: from + offsets + the range's days at most, a few words
// an actor. Once its first day falls before from, its bitset is dropped
// and it keeps only its first days. Each worker keeps its own actors,
// folded together at the end. Every actor and bitset is charged as it
// comes (and given back as dropped); the result is charged in place of
// the map it is built from. A done ctx stops the scan with its error.
func (s scan) retentionActors(ctx context.Context, spec retentionSpec, charge func(int64) error) ([]retentionActor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hiDate := spec.to.AddDays(spec.offsets)
	if last := s.e.today().AddDays(-1); last.Before(hiDate) {
		hiDate = last
	}
	if hiDate.Before(spec.from) || spec.to.Before(spec.from) {
		return nil, nil // no cohort day of the range has passed
	}
	lo, hi, toDay := epochDay(spec.from), epochDay(hiDate), epochDay(spec.to)
	words := int((hi - lo + 64) / 64)
	all, err := archive.ListFiles(s.e.dir)
	if err != nil {
		return nil, err
	}
	files := all[s.project]
	cols := slices.Clone(retentionColumns)
	for _, f := range s.filters {
		for _, c := range readColumns(f.column) {
			if !slices.Contains(cols, c) {
				cols = append(cols, c)
			}
		}
	}
	read := s.rules.columns(cols)
	status := s.statusColumns(nil)
	var ids *firstSet
	if len(status) > 0 {
		if ids, err = s.e.firstSeen(ctx, s.project, files, charge); err != nil {
			return nil, err
		}
	}
	r := Range{From: everything.From, To: hiDate}
	pq, logs := retentionFiles(files, hiDate)
	groups, err := parquetGroups(pq, "", r)
	if err != nil {
		return nil, err
	}
	work := make([]group, 0, len(logs)+len(groups))
	for _, path := range logs {
		work = append(work, group{file: path, index: -1})
	}
	work = append(work, groups...)
	parts := make([]map[string]*retentionState, s.e.workers)
	for i := range parts {
		parts[i] = map[string]*retentionState{}
	}
	win := retentionWindow{lo: lo, hi: hi, words: words}
	readers := make([]groupReader, s.e.workers)
	err = scanParallel(ctx, work, s.e.workers, func(w int, g group) error {
		var b *Batch
		var err error
		if g.index < 0 {
			b, err = readLog(g.file, read, "", r)
		} else {
			b, err = readers[w].read(g, read, "", r)
		}
		if err != nil {
			return err
		}
		b = s.rules.apply(b, "")
		if ids != nil {
			addStatus(b, status, ids)
		}
		grew, err := win.add(parts[w], b, s.filters)
		if err != nil {
			return err
		}
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
	m := parts[0]
	for _, p := range parts[1:] {
		if err := charge(win.fold(m, p)); err != nil {
			return nil, err
		}
	}
	// The cohorts of the range, their windows cut to the offsets.
	actorWords := (spec.offsets + 64) / 64
	var out []retentionActor
	var held int64
	for id, st := range m {
		held += win.bytes(id, st)
		if st.window == nil || st.first > toDay {
			continue
		}
		a := retentionActor{id: id, cohort: st.first, user: st.user, active: make([]uint64, actorWords)}
		for o := 0; o <= spec.offsets && st.first+int64(o) <= hi; o++ {
			if x := st.first + int64(o) - lo; st.window[x/64]>>(x%64)&1 == 1 {
				a.active[o/64] |= 1 << (o % 64)
			}
		}
		out = append(out, a)
		if err := charge(int64(64 + len(id) + 8*actorWords)); err != nil {
			return nil, err
		}
	}
	return out, charge(-held)
}

// retentionWindow is a retention scan's window of days [lo, hi], words
// 64-bit words a bitset.
type retentionWindow struct {
	lo, hi int64
	words  int
}

func (w retentionWindow) windowBytes() int64 { return int64(24 + 8*w.words) }

// bytes is what actor id's state st is charged.
func (w retentionWindow) bytes(id string, st *retentionState) int64 {
	n := int64(retentionEntryBytes + len(id))
	if st.window != nil {
		n += w.windowBytes()
	}
	return n
}

// add notes b's rows that pass filters and that the retention acts hold
// in m, and returns the bytes m grew by (less what it gave back).
func (w retentionWindow) add(m map[string]*retentionState, b *Batch, filters []filter) (int64, error) {
	sel, err := selectRows(b, filters, nil)
	if err != nil || len(sel) == 0 {
		return 0, err
	}
	_, day := b.col("day")
	_, fam := b.col("family")
	_, actor := b.col("actor_id")
	_, kind := b.col("actor_kind")
	if day == nil || fam == nil || fam.Idx == nil || actor == nil || actor.Idx == nil || kind == nil || kind.Idx == nil {
		return 0, nil // a column all NULL: no row is an act
	}
	famOK := make([]bool, len(fam.Dict))
	for j, s := range fam.Dict {
		famOK[j] = s == "views" || s == "product"
	}
	const install, user = 1, 2
	kindOf := make([]int8, len(kind.Dict))
	for j, s := range kind.Dict {
		switch s {
		case "install":
			kindOf[j] = install
		case "user":
			kindOf[j] = user
		}
	}
	states := make([]*retentionState, len(actor.Dict)) // this batch's, by its dictionary
	var grew int64
	for _, i := range sel {
		f, k, a := fam.Idx[i], kind.Idx[i], actor.Idx[i]
		if f < 0 || !famOK[f] || k < 0 || kindOf[k] == 0 || a < 0 || actor.Dict[a] == "" {
			continue
		}
		d, ok := intAt(day, int(i))
		if !ok || d > w.hi {
			continue
		}
		st := states[a]
		if st == nil {
			id := actor.Dict[a]
			if st = m[id]; st == nil {
				st = &retentionState{first: d, user: noUserDay}
				m[strings.Clone(id)] = st
				grew += retentionEntryBytes + int64(len(id))
			}
			states[a] = st
		}
		st.first = min(st.first, d)
		if kindOf[k] == user {
			st.user = min(st.user, d)
		}
		if st.first < w.lo {
			if st.window != nil {
				st.window = nil
				grew -= w.windowBytes()
			}
			continue
		}
		if st.window == nil {
			st.window = make([]uint64, w.words)
			grew += w.windowBytes()
		}
		x := d - w.lo
		st.window[x/64] |= 1 << (x % 64)
	}
	return grew, nil
}

// fold adds src's actors to dst: the earlier first days, the days
// together. It returns the bytes dst grew by less src's, given back
// (never more than 0).
func (w retentionWindow) fold(dst, src map[string]*retentionState) int64 {
	var freed int64
	for id, x := range src {
		y := dst[id]
		if y == nil {
			dst[id] = x
			continue
		}
		freed += retentionEntryBytes + int64(len(id))
		y.first, y.user = min(y.first, x.first), min(y.user, x.user)
		switch {
		case y.first < w.lo:
			if y.window != nil {
				freed += w.windowBytes()
			}
			if x.window != nil {
				freed += w.windowBytes()
			}
			y.window = nil
		case y.window == nil:
			y.window = x.window
		case x.window != nil:
			for j, v := range x.window {
				y.window[j] |= v
			}
			freed += w.windowBytes()
		}
	}
	return -freed
}
