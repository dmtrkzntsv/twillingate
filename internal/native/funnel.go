package native

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// Step is one step of a funnel: its label and the events that take it.
type Step struct {
	Label string   `json:"label" jsonschema:"the step's name, the step column's value"`
	Where []Filter `json:"where" jsonschema:"1 or more filters, as the query's where (an any_of among them for an OR), all of which an event passes to take the step"`
}

// The funnel's bounds.
const (
	minSteps = 2
	maxSteps = 10
)

func (q Query) validateFunnel() error {
	if q.Table != "" && q.Table != "events" && q.Table != "views" && q.Table != "product" {
		return invalid("table", "a funnel reads events, views or product, not %q", q.Table)
	}
	switch {
	case len(q.Metrics) > 0:
		return invalid("metrics", "a funnel takes none; it counts the actors at each step")
	case len(q.Dimensions) > 0 || q.Series != "" || q.SeriesLimit != 0 || q.Compare != "" || q.Limit != 0 || q.Other || q.Order != "":
		return invalid("kind", "a funnel takes steps, by, sequence and where, nothing else of a stat, trend or breakdown")
	case q.Output != "" || q.IdentifiedBy != "" || q.Offsets != 0 || len(q.Milestones) > 0:
		return invalid("output", "a funnel takes none of a retention's fields")
	case q.By != "" && !slices.Contains(EverBy, q.By):
		return invalid("by", "%q is not actor, user or group", q.By)
	case len(q.Steps) < minSteps || len(q.Steps) > maxSteps:
		return invalid("steps", "a funnel takes %d to %d, got %d", minSteps, maxSteps, len(q.Steps))
	}
	table := q.funnelTable()
	for i, f := range q.Where {
		if err := f.validate(table, fmt.Sprintf("where[%d]", i)); err != nil {
			return err
		}
	}
	labels := map[string]bool{}
	for i, st := range q.Steps {
		p := fmt.Sprintf("steps[%d]", i)
		switch {
		case st.Label == "":
			return invalid(p+".label", "a step's label is not empty")
		case labels[st.Label]:
			return invalid(p+".label", "%q is the label of two steps", st.Label)
		case len(st.Where) == 0:
			return invalid(p+".where", "a step takes 1 or more filters")
		}
		labels[st.Label] = true
		for j, f := range st.Where {
			fp := fmt.Sprintf("%s.where[%d]", p, j)
			if f.Op == "ever" || f.Op == "never" {
				return invalid(fp+".op", "a step takes no %s; filter the funnel's where with it instead", f.Op)
			}
			if c, ok := namesDefined(f); ok {
				return invalid(fp, "%s is a defined column; a funnel defines none", c)
			}
			if err := f.validate(table, fp); err != nil {
				return err
			}
		}
	}
	return nil
}

// funnelTable is the table a funnel reads: events unless it names one.
func (q Query) funnelTable() string {
	if q.Table == "" {
		return "events"
	}
	return q.Table
}

// funnel answers a funnel over [from, to]: per step, in order, the
// actors (users, groups) that took it and every step before it, any
// time in the range, or with sequence one after another in time.
func (s scan) funnel(ctx context.Context, from, to civil.Date) (Result, error) {
	steps := make([][]filter, len(s.q.Steps))
	for i, st := range s.q.Steps {
		fs, err := compileFilters(st.Where, fmt.Sprintf("steps[%d].where", i))
		if err != nil {
			return Result{}, err
		}
		steps[i] = fs
	}
	idCol := everColumn(s.q.By)
	cols := []string{"day", "ts", idCol}
	add := func(fs []filter) {
		for _, f := range fs {
			for _, col := range f.columns() {
				for _, c := range readColumns(col) {
					if !slices.Contains(cols, c) {
						cols = append(cols, c)
					}
				}
			}
		}
	}
	add(s.filters)
	for _, fs := range steps {
		add(fs)
	}
	all, err := archive.ListFiles(s.e.dir)
	if err != nil {
		return Result{}, err
	}
	fs := s
	fs.q.Table = s.q.funnelTable()
	charge := s.e.charge()
	type hit struct {
		ts   int64
		mask uint16
	}
	var mu sync.Mutex
	masks := map[string]uint16{} // any order: the steps each id took
	hits := map[string][]hit{}   // sequence: each id's events taking a step
	pass := make([][]bool, len(steps))
	err = fs.scanEvents(ctx, all[s.project], Range{From: from, To: to}, cols, nil, charge, func(_ int, b *Batch) error {
		id := batchColumn(b, idCol)
		if id == nil {
			return nil
		}
		_, ts := b.col("ts")
		mu.Lock()
		defer mu.Unlock()
		keep, err := passRows(b, s.filters, nil)
		if err != nil {
			return err
		}
		for j, st := range steps {
			if pass[j], err = passRows(b, st, pass[j]); err != nil {
				return err
			}
		}
		for i := range b.N {
			v, ok := textAt(id, i)
			if !ok || v == "" || !keep[i] {
				continue
			}
			var mask uint16
			for j := range steps {
				if pass[j][i] {
					mask |= 1 << j
				}
			}
			if mask == 0 {
				continue
			}
			if !s.q.Sequence {
				if _, seen := masks[v]; !seen {
					if err := charge(int64(len(v)) + 48); err != nil {
						return err
					}
				}
				masks[v] |= mask
				continue
			}
			if ts == nil || ts.Times == nil || (ts.Null != nil && ts.Null[i]) {
				continue
			}
			if err := charge(24 + int64(len(v)*boolInt(hits[v] == nil))); err != nil {
				return err
			}
			hits[v] = append(hits[v], hit{ts.Times[i], mask})
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	reached := make([]int64, len(steps)) // reached[k]: the ids at step k or past it
	count := func(n int) {
		for k := range n {
			reached[k]++
		}
	}
	if s.q.Sequence {
		for _, hs := range hits {
			slices.SortFunc(hs, func(a, b hit) int { return cmp.Compare(a.ts, b.ts) })
			n := 0
			for _, h := range hs {
				for n < len(steps) && h.mask&(1<<n) != 0 {
					n++ // one event may take the next steps too
				}
			}
			count(n)
		}
	} else {
		for _, m := range masks {
			n := 0
			for n < len(steps) && m&(1<<n) != 0 {
				n++
			}
			count(n)
		}
	}
	rows := make([][]string, len(steps))
	for k, st := range s.q.Steps {
		rows[k] = []string{st.Label, strconv.FormatInt(reached[k], 10)}
	}
	return Result{Columns: []string{"step", "value"}, Rows: rows}, nil
}
