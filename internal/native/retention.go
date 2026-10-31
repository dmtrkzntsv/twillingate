package native

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// retentionSpec is what a retention scan gathers: the actors whose cohort
// day is in [from, to], and their days back up to offsets days on, before
// today (the engine's clock when zero).
type retentionSpec struct {
	from, to, today civil.Date
	offsets         int
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

// retentionFamilies are the families a retention counts: a row group of
// measures alone is not read.
var retentionFamilies = []string{"product", "views"}

// retentionFiles is f's Parquet files and logs that may hold rows on any
// day up to hi: the cohort day is the first over all history.
func retentionFiles(f archive.Files, hi civil.Date) (parquet, logs []string) {
	return filesIn(f, dayRange{From: everything.From, To: hi})
}

// retentionActors gathers the retention state of the project's actors
// whose cohort day is in [spec.from, spec.to], as cohorts.sql's acts and
// firsts read the events: rows of families views and product (the rules
// applied as events applies them, then the scan's filters) whose
// actor_kind is user or install and actor_id not empty, on a day before
// spec.today and no later than to + offsets, over all
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
// files are the project's, as archive.ListFiles lists them.
func (s scan) retentionActors(ctx context.Context, spec retentionSpec, files archive.Files, charge func(int64) error) ([]retentionActor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.today == (civil.Date{}) {
		spec.today = s.e.Today()
	}
	hiDate := spec.to.AddDays(spec.offsets)
	if last := spec.today.AddDays(-1); last.Before(hiDate) {
		hiDate = last
	}
	if hiDate.Before(spec.from) || spec.to.Before(spec.from) {
		return nil, nil // no cohort day of the range has passed
	}
	lo, hi, toDay := epochDay(spec.from), epochDay(hiDate), epochDay(spec.to)
	words := int((hi - lo + 64) / 64)
	cols := slices.Clone(retentionColumns)
	for _, f := range s.filters {
		for _, col := range f.columns() {
			for _, c := range readColumns(col) {
				if !slices.Contains(cols, c) {
					cols = append(cols, c)
				}
			}
		}
	}
	read := s.rules.columns(cols)
	idCols := s.identityColumnsOf(nil)
	var ids *firstSet
	var err error
	if len(idCols) > 0 {
		if ids, err = s.e.firstSeen(ctx, s.project, files, needsNames(idCols), charge); err != nil {
			return nil, err
		}
	}
	r := dayRange{From: everything.From, To: hiDate}
	pq, logs := retentionFiles(files, hiDate)
	groups, err := parquetGroupsOf(pq, retentionFamilies, r)
	if err != nil {
		return nil, err
	}
	parts := make([]map[string]*retentionState, s.e.workers)
	for i := range parts {
		parts[i] = map[string]*retentionState{}
	}
	win := retentionWindow{lo: lo, hi: hi, words: words}
	err = s.e.scanFiles(ctx, logs, groups, s.e.logs.read, read, "", r, func(w int, b *batch) error {
		b = s.rules.apply(b, "")
		if ids != nil {
			addIdentityColumns(b, idCols, ids)
		}
		grew, err := win.add(parts[w], b, s.filters)
		if err != nil {
			return err
		}
		return charge(grew)
	})
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
func (w retentionWindow) add(m map[string]*retentionState, b *batch, filters []filter) (int64, error) {
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
		if y.first < w.lo {
			if y.window != nil {
				freed += w.windowBytes()
			}
			if x.window != nil {
				freed += w.windowBytes()
			}
			y.window = nil
			continue
		}
		// Neither first day is before lo, so both kept their windows (add
		// drops one only once its first day is).
		for j, v := range x.window {
			y.window[j] |= v
		}
		freed += w.windowBytes()
	}
	return -freed
}

// countRetention is the counts of actors, as retentionCounts has them.
func countRetention(actors []retentionActor, offsets int) map[retentionKey]int64 {
	counts := map[retentionKey]int64{}
	for _, a := range actors {
		for o := 0; o <= offsets; o++ {
			if a.activeOn(o) {
				counts[retentionKey{user: a.user <= a.cohort+int64(o), cohort: a.cohort, offset: o}]++
			}
		}
	}
	// A kind's rows of a cohort exist only beside its size.
	for k := range counts {
		if counts[retentionKey{user: k.user, cohort: k.cohort}] == 0 {
			delete(counts, k)
		}
	}
	return counts
}

// retentionKey is one count of the retention widgets' returned CTE: the
// actors of kind (install or user) of cohort day cohort back offset
// days later.
type retentionKey struct {
	user   bool
	cohort int64
	offset int
}

// retention answers a retention query over the cohorts of [from, to], as
// the built-in retention widgets' SQL (cohorts.sql and the curves and
// milestones beside it) has it. An actor back on day d of its cohort
// counts as a user when it was first a user on d or before, else as an
// install; a cohort's size, per kind, is its count at offset 0, and a
// kind's row at an offset exists only when that kind has a size in the
// cohort (the SQL's inner join on offset 0).
//   - grid: a row per cohort, kind and offset with any actor back, by
//     cohort descending, kind, offset; with identified_by, that kind's
//     rows only;
//   - curve: the kind's cohorts pooled per offset o, over those whose
//     cohort + o is before today: the actors back over the cohort sizes,
//     0 over none; a row per offset some cohort reached;
//   - milestones: one row, the curve at each milestone, empty when the
//     curve has no row there.
func (s scan) retention(ctx context.Context, from, to civil.Date) (Result, error) {
	offsets := s.q.offsets()
	got, err := s.retentionCounts(ctx, retentionSpec{from: from, to: to, today: s.e.Today(), offsets: offsets})
	if err != nil {
		return Result{}, err
	}
	counts := got.counts // shared with other queries: read only
	cols := columnsOut{}
	for _, c := range s.q.Columns() {
		cols.add(c)
	}
	ratio := func(n, d int64) string {
		if d == 0 {
			return "0"
		}
		return value{f: float64(n) / float64(d)}.String()
	}
	if s.q.Output == "grid" {
		keys := make([]retentionKey, 0, len(counts))
		for k := range counts {
			if s.q.IdentifiedBy == "" || k.user == (s.q.IdentifiedBy == "user") {
				keys = append(keys, k)
			}
		}
		slices.SortFunc(keys, func(a, b retentionKey) int {
			switch {
			case a.cohort != b.cohort:
				return cmp.Compare(b.cohort, a.cohort)
			case a.user != b.user:
				if b.user {
					return -1 // install before user
				}
				return 1
			}
			return cmp.Compare(a.offset, b.offset)
		})
		rows := make([][]string, 0, len(keys))
		for _, k := range keys {
			size, n := counts[retentionKey{user: k.user, cohort: k.cohort}], counts[k]
			kind := "install"
			if k.user {
				kind = "user"
			}
			rows = append(rows, []string{formatDay(k.cohort), kind, strconv.Itoa(k.offset),
				strconv.FormatInt(size, 10), strconv.FormatInt(n, 10), ratio(n, size)})
		}
		return cols.result(rows, false), nil
	}
	// The curve of one kind: per offset, the sizes and the actors back of
	// the cohorts that reached it before today.
	user, today := s.q.IdentifiedBy == "user", got.today
	sizes, back := make([]int64, offsets+1), make([]int64, offsets+1)
	reached := make([]bool, offsets+1)
	for k, n := range counts {
		if k.user != user {
			continue
		}
		if k.offset == 0 {
			for o := 0; o <= offsets && k.cohort+int64(o) < today; o++ {
				sizes[o] += n
				reached[o] = true
			}
		}
		if k.cohort+int64(k.offset) < today {
			back[k.offset] += n
		}
	}
	if s.q.Output == "curve" {
		var rows [][]string
		for o := range reached {
			if reached[o] {
				rows = append(rows, []string{strconv.Itoa(o), ratio(back[o], sizes[o])})
			}
		}
		return cols.result(rows, false), nil
	}
	row := make([]string, 0, len(cols.names))
	for _, m := range s.q.milestones() {
		v := ""
		if reached[m] {
			v = ratio(back[m], sizes[m])
		}
		row = append(row, v)
	}
	return cols.result([][]string{row}, false), nil
}
