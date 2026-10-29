package native

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
)

// ever and never keep a row by what else its actor (user, group) did:
// whether one of the actor's events, over all days up to the query's to
// or over its range, passes the filter's where. The ids are collected by
// a scan of the project's events before the query's own.

// Ever's vocabularies and bounds.
var (
	EverBy   = []string{"actor", "user", "group"}
	EverOver = []string{"all", "range"}
)

const maxEverWhere = 10

// everColumn is the id column by names.
func everColumn(by string) string {
	switch by {
	case "user":
		return "user_id"
	case "group":
		return "group_id"
	}
	return "actor_id"
}

func (f Filter) validateEver(table, p string) error {
	switch n := len(f.Where); {
	case f.Column != "":
		return invalid(p+".column", "%s takes where, not column", f.Op)
	case len(f.Columns) != 0 || len(f.Any) != 0:
		return invalid(p+".where", "%s takes where, not columns or any", f.Op)
	case len(f.Values) != 0:
		return invalid(p+".values", "%s takes none", f.Op)
	case n < 1 || n > maxEverWhere:
		return invalid(p+".where", "%s takes 1 to %d filters, got %d", f.Op, maxEverWhere, n)
	case f.By != "" && !slices.Contains(EverBy, f.By):
		return invalid(p+".by", "%q is not actor, user or group", f.By)
	case f.Over != "" && !slices.Contains(EverOver, f.Over):
		return invalid(p+".over", "%q is not all or range", f.Over)
	case table == "identities":
		return invalid(p+".op", "identities has no actors; %s is for the event tables and sessions", f.Op)
	case table == "sessions" && f.By != "" && f.By != "actor":
		return invalid(p+".by", "a session has an actor only")
	}
	for j, g := range f.Where {
		q := fmt.Sprintf("%s.where[%d]", p, j)
		if g.Op == "ever" || g.Op == "never" {
			return invalid(q+".op", "a %s holds no %s", f.Op, g.Op)
		}
		if c, ok := namesDefined(g); ok {
			return invalid(q, "%s is a defined column; a %s's filters name the events'", c, f.Op)
		}
		if err := g.validate("events", q); err != nil {
			return err
		}
	}
	return nil
}

// idSet is the ids of an ever or never filter, filled before the scan.
type idSet struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (s *idSet) has(id string) bool {
	_, ok := s.ids[id]
	return ok
}

// hasEver says whether a filter of q, its metrics' included, is an
// ever or never: the query is answered alone, its ids collected first.
func hasEver(q Query) bool {
	var in func(fs []Filter) bool
	in = func(fs []Filter) bool {
		return slices.ContainsFunc(fs, func(f Filter) bool { return f.Op == "ever" || f.Op == "never" })
	}
	if in(q.Where) {
		return true
	}
	var metric func(m Metric) bool
	metric = func(m Metric) bool {
		return in(m.Where) || (m.Num != nil && metric(*m.Num)) || (m.Den != nil && metric(*m.Den))
	}
	return slices.ContainsFunc(q.Metrics, metric)
}

// everFilters are the compiled ever and never filters of filters and of
// metrics' (ratio sides included).
func everFilters(filters []filter, metrics []metricSpec) []*filter {
	var out []*filter
	for i := range filters {
		if filters[i].ids != nil {
			out = append(out, &filters[i])
		}
	}
	var metric func(mt *metricSpec)
	metric = func(mt *metricSpec) {
		for i := range mt.where {
			if mt.where[i].ids != nil {
				out = append(out, &mt.where[i])
			}
		}
		if mt.ratioNum != nil {
			metric(mt.ratioNum)
			metric(mt.ratioDen)
		}
	}
	for i := range metrics {
		metric(&metrics[i])
	}
	return out
}

// collectEver fills each ever and never filter's ids: those of the
// project's events (rules applied) passing its where, over the days it
// says, up to p.To.
func (e *Engine) collectEver(ctx context.Context, fs []*filter, p Params, rs ruleSet) error {
	if len(fs) == 0 {
		return nil
	}
	all, err := archive.ListFiles(e.dir)
	if err != nil {
		return err
	}
	files := all[p.Project]
	charge := e.charge()
	for _, f := range fs {
		r := Range{From: everything.From, To: p.To}
		if f.over == "range" {
			r.From = p.From
		}
		s := scan{e: e, q: Query{Table: "events"}, filters: f.inner, project: p.Project, rules: rs}
		cols := []string{"day", f.column}
		for _, g := range f.inner {
			for _, col := range g.columns() {
				for _, c := range readColumns(col) {
					if !slices.Contains(cols, c) {
						cols = append(cols, c)
					}
				}
			}
		}
		f.ids.ids = map[string]struct{}{}
		var sel []int32
		var mu sync.Mutex
		err := s.scanEvents(ctx, files, r, cols, nil, charge, func(_ int, b *Batch) error {
			c := batchColumn(b, f.column)
			if c == nil {
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			var err error
			if sel, err = selectRows(b, f.inner, sel); err != nil {
				return err
			}
			f.ids.mu.Lock()
			defer f.ids.mu.Unlock()
			for _, i := range sel {
				if id, ok := textAt(c, int(i)); ok && id != "" {
					if _, seen := f.ids.ids[id]; !seen {
						if err := charge(int64(len(id)) + 48); err != nil {
							return err
						}
						f.ids.ids[strings.Clone(id)] = struct{}{} // id may share a file dictionary's backing string
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// cloneEver is filters and metrics with each ever and never filter's
// ids a set of its own, for another range; the rest shared.
func cloneEver(filters []filter, metrics []metricSpec) ([]filter, []metricSpec) {
	fs := slices.Clone(filters)
	for i := range fs {
		if fs[i].ids != nil {
			fs[i].ids = &idSet{}
		}
	}
	var metric func(mt metricSpec) metricSpec
	metric = func(mt metricSpec) metricSpec {
		mt.where, _ = cloneEver(mt.where, nil)
		if mt.ratioNum != nil {
			num, den := metric(*mt.ratioNum), metric(*mt.ratioDen)
			mt.ratioNum, mt.ratioDen = &num, &den
		}
		return mt
	}
	ms := make([]metricSpec, len(metrics))
	for i, mt := range metrics {
		ms[i] = metric(mt)
	}
	return fs, ms
}
