package native

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// The paths kind's bounds and defaults.
const (
	minDepth, maxDepth, defaultDepth = 2, 6, 3
	defaultPathsLimit                = 50
)

func (q Query) validatePaths() error {
	if q.Table != "" && q.Table != "views" && q.Table != "product" {
		return invalid("table", "paths reads views or product, not %q", q.Table)
	}
	switch {
	case len(q.Metrics) > 0:
		return invalid("metrics", "paths takes none; it counts the sessions along each step")
	case q.Series != "" || q.SeriesLimit != 0 || q.Compare != "" || q.Other || q.Order != "":
		return invalid("kind", "paths takes dimensions, depth, limit and where, nothing else of a stat, trend or breakdown")
	case q.Output != "" || q.IdentifiedBy != "" || q.Offsets != 0 || len(q.Milestones) > 0 || len(q.Steps) > 0 || q.By != "" || q.Sequence:
		return invalid("kind", "paths takes none of a retention's or a funnel's fields")
	case len(q.Dimensions) > 1:
		return invalid("dimensions", "paths takes 1, the column a step reads, got %d", len(q.Dimensions))
	case q.Depth != 0 && (q.Depth < minDepth || q.Depth > maxDepth):
		return invalid("depth", "%d is not between %d and %d", q.Depth, minDepth, maxDepth)
	case q.Limit < 0 || q.Limit > maxLimit:
		return invalid("limit", "%d is not between 1 and %d", q.Limit, maxLimit)
	}
	table := q.pathsTable()
	if len(q.Dimensions) == 1 {
		if d := q.Dimensions[0]; !dimensionOf(table, d) || d == "day" || isDefined(d) {
			return invalid("dimensions[0]", "%q is not a column of %s a step may read", d, table)
		}
	}
	for i, f := range q.Where {
		if err := f.validate(table, fmt.Sprintf("where[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

func (q Query) pathsTable() string {
	if q.Table == "" {
		return "views"
	}
	return q.Table
}

// pathsColumn is the column a step reads: path on views, event_name on
// product, unless the query names one.
func (q Query) pathsColumn() string {
	switch {
	case len(q.Dimensions) == 1:
		return q.Dimensions[0]
	case q.pathsTable() == "product":
		return "event_name"
	}
	return "path"
}

// paths answers a paths query over [from, to]: each session's rows (as
// the sessions table cuts them: a declared id, else 30 minutes of the
// actor's silence, and midnight UTC, over every row, where and an empty
// value aside; rows without an actor are no one's) in time order, a run of one value its one step, its first depth steps;
// a row per flow from a step to the next, source and target the step's
// number and value ("1 /pricing"), value the sessions that took it, the
// limit busiest, by sessions, then source and target.
func (s scan) paths(ctx context.Context, from, to civil.Date) (Result, error) {
	col, depth := s.q.pathsColumn(), cmp.Or(s.q.Depth, defaultDepth)
	cols := []string{"day", "ts", "id", "kind", "actor_id", "session_id"}
	for _, c := range readColumns(col) {
		if !slices.Contains(cols, c) {
			cols = append(cols, c)
		}
	}
	for _, f := range s.filters {
		for _, fc := range f.columns() {
			for _, c := range readColumns(fc) {
				if !slices.Contains(cols, c) {
					cols = append(cols, c)
				}
			}
		}
	}
	all, err := archive.ListFiles(s.e.dir)
	if err != nil {
		return Result{}, err
	}
	ps := s
	ps.q.Table = s.q.pathsTable()
	charge := s.e.charge()
	type hit struct {
		ts         int64
		v, sid, id string
		idNull     bool
		skip       bool // cuts sessions, takes no step: where refused it, or its value is empty
	}
	var mu sync.Mutex
	days := map[string][]hit{} // per kind, actor and day: its rows
	// The step column goes as a dimension, so an identity one is added.
	err = ps.scanEvents(ctx, all[s.project], dayRange{From: from, To: to}, cols, []string{col}, charge, func(_ int, b *batch) error {
		actor, kind, sid, vc, evID := batchColumn(b, "actor_id"), batchColumn(b, "kind"), batchColumn(b, "session_id"), batchColumn(b, col), batchColumn(b, "id")
		_, ts := b.col("ts")
		_, day := b.col("day")
		if actor == nil || ts == nil || ts.Times == nil || day == nil || day.Ints == nil {
			return nil
		}
		keep, err := passRows(b, s.filters, nil)
		if err != nil {
			return err
		}
		typ := typeOf(col)
		mu.Lock()
		defer mu.Unlock()
		for i := range b.N {
			a, ok := textAt(actor, i)
			if !ok || a == "" {
				continue // an actorless view starts and ends nobody's path
			}
			var t int64 // a NULL ts reads 0, as the sessions table reads it
			if ts.Null == nil || !ts.Null[i] {
				t = ts.Times[i]
			}
			v, ok := latestText(vc, i, typ)
			skip := !ok || v == "" || !keep[i]
			if skip {
				v = ""
			}
			id, _ := textAt(sid, i)
			eid, hasID := textAt(evID, i)
			k, hasKind := textAt(kind, i)
			// NULL kind and empty kind are two partitions, as Sessionize keeps them.
			key := strconv.FormatBool(hasKind) + k + "\x00" + a + "\x00" + strconv.FormatInt(day.Ints[i], 10)
			if err := charge(int64(64 + len(v) + len(id) + len(eid) + len(key)*boolInt(days[key] == nil))); err != nil {
				return err
			}
			days[key] = append(days[key], hit{t, strings.Clone(v), strings.Clone(id), strings.Clone(eid), !hasID, skip})
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	// Each kind and actor's day cut into sessions as archive.Sessionize
	// cuts them: a declared id its own, else a new one after 30 minutes
	// (in whole seconds) of silence since the view before, whichever
	// session that was in.
	const sessionGap = 1800 // seconds
	var sessions [][]hit
	for key, hs := range days {
		delete(days, key) // its hits move into sessions
		// By ts, then event id (none last), as Sessionize orders a day.
		slices.SortStableFunc(hs, func(a, b hit) int {
			return cmp.Or(cmp.Compare(a.ts, b.ts), cmp.Compare(boolInt(a.idNull), boolInt(b.idNull)), cmp.Compare(a.id, b.id))
		})
		byKey := map[string]int{}
		gap := 0
		for j, h := range hs {
			key := "id:" + h.sid
			if h.sid == "" {
				if j == 0 || archive.Seconds(h.ts)-archive.Seconds(hs[j-1].ts) > sessionGap {
					gap++
				}
				key = "gap:" + strconv.Itoa(gap)
			}
			k, ok := byKey[key]
			if !ok {
				if err := charge(64); err != nil { // the session's slot and key
					return Result{}, err
				}
				k = len(sessions)
				byKey[key] = k
				sessions = append(sessions, nil)
			}
			sessions[k] = append(sessions[k], h)
		}
	}
	type flow struct{ source, target string }
	flows := map[flow]int64{}
	for _, hs := range sessions {
		var steps []string
		for _, h := range hs {
			if h.skip {
				continue // it cut the session; it is no step of it
			}
			if len(steps) > 0 && steps[len(steps)-1] == h.v {
				continue // a run of one value is one step
			}
			if steps = append(steps, h.v); len(steps) == depth {
				break
			}
		}
		for k := 1; k < len(steps); k++ {
			fl := flow{strconv.Itoa(k) + " " + steps[k-1], strconv.Itoa(k+1) + " " + steps[k]}
			if _, seen := flows[fl]; !seen {
				if err := charge(int64(96 + len(fl.source) + len(fl.target))); err != nil { // the flow, its key and its row
					return Result{}, err
				}
			}
			flows[fl]++
		}
	}
	type row struct {
		f flow
		n int64
	}
	rows := make([]row, 0, len(flows))
	for f, n := range flows {
		rows = append(rows, row{f, n})
	}
	slices.SortFunc(rows, func(a, b row) int {
		return cmp.Or(cmp.Compare(b.n, a.n), cmp.Compare(a.f.source, b.f.source), cmp.Compare(a.f.target, b.f.target))
	})
	limit := cmp.Or(s.q.Limit, defaultPathsLimit)
	truncated := len(rows) > limit
	rows = rows[:min(limit, len(rows))]
	out := Result{Columns: []string{"source", "target", "value"}, Rows: make([][]string, len(rows)), Truncated: truncated}
	for i, r := range rows {
		out.Rows[i] = []string{r.f.source, r.f.target, strconv.FormatInt(r.n, 10)}
	}
	return out, nil
}
