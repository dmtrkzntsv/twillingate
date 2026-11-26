package reporting

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const (
	// maxSelectorValues bounds the values selector_values returns, and
	// maxValueProjects the projects it reads.
	maxSelectorValues = 100
	maxValueProjects  = 10
	// valuesPerProject is how many of a column's values one project's
	// breakdown reads: the native engine's largest limit.
	valuesPerProject = 1000
)

// SelectorValuesIn is a read of a selector's values: dashboard
// DashboardID's selector Name, over ProjectIDs and From to To. Search keeps
// the values that contain it, in any case. Picked values stay in the answer
// even when the range has none of them, so a viewer can untick them.
type SelectorValuesIn struct {
	DashboardID int64
	Name        string
	ProjectIDs  []int64
	From, To    string
	Search      string
	Picked      []string
}

// SelectorValue is one value of a selector's column and the events that
// carry it.
type SelectorValue struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// SelectorValues lists the values of a selector's column over the range
// in the given projects, merged across them, the busiest first (ties by
// value), up to 100, and then each picked value the list lacks with a
// count of 0. One native breakdown on events per project.
func (s *Service) SelectorValues(ctx context.Context, in SelectorValuesIn) ([]SelectorValue, error) {
	d, err := s.st.GetDashboard(ctx, in.DashboardID)
	if err != nil {
		return nil, err
	}
	sels := storedSelectors(d.Selectors)
	i := slices.IndexFunc(sels, func(x Selector) bool { return x.Name == in.Name })
	if i < 0 {
		return nil, store.Refuse(store.ErrNotFound, "dashboard %d has no selector %s", in.DashboardID, in.Name)
	}
	projects := slices.Compact(slices.Sorted(slices.Values(in.ProjectIDs)))
	switch n := len(projects); {
	case n == 0:
		return nil, store.Refuse(store.ErrInvalid, "project_id: name at least one project")
	case n > maxValueProjects:
		return nil, store.Refuse(store.ErrInvalid, "project_id: %d projects; at most %d", n, maxValueProjects)
	}
	for _, p := range projects {
		if p <= 0 {
			return nil, store.Refuse(store.ErrInvalid, "project_id: %d is not a project id", p)
		}
	}
	from, to, err := checkDates(in.From, in.To)
	if err != nil {
		return nil, err
	}
	qs, _ := s.sources["query"].(*querySource)
	if qs == nil || qs.native == nil {
		return nil, store.Refuse(store.ErrInvalid, "structured queries are not available in this process")
	}

	q := native.Query{Kind: "breakdown", Table: "events", Dimensions: []string{sels[i].Column},
		Metrics: []native.Metric{{Op: "count", As: "n"}}, Limit: valuesPerProject}
	counts := map[string]int64{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, len(projects))
	for j, p := range projects {
		wg.Go(func() {
			var rules archive.Rules
			if qs.rules != nil {
				rules = qs.rules(ctx, p)
			}
			cctx, cancel := qs.withTimeout(ctx)
			defer cancel()
			res, err := qs.native.Answer(cctx, q, native.Params{Project: p, From: from, To: to}, rules)
			if err != nil {
				errs[j] = queryErr(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, row := range res.Rows {
				if len(row) < 2 {
					errs[j] = fmt.Errorf("reporting: selector values of project %d: a row of %d cells, want 2", p, len(row))
					return
				}
				if row[0] == "" {
					continue
				}
				n, err := strconv.ParseInt(row[1], 10, 64)
				if err != nil {
					errs[j] = fmt.Errorf("reporting: selector values of project %d: count %q: %w", p, row[1], err)
					return
				}
				counts[row[0]] += n
			}
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return selectorValueList(counts, in.Search, in.Picked), nil
}

// selectorValueList is counts as the answer: the values containing search
// (in any case), by count descending then value, cut to 100, then each of
// picked that is missing with its count (0 when absent), in picked's order.
func selectorValueList(counts map[string]int64, search string, picked []string) []SelectorValue {
	search = strings.ToLower(search)
	out := []SelectorValue{}
	for v, n := range counts {
		if search == "" || strings.Contains(strings.ToLower(v), search) {
			out = append(out, SelectorValue{Value: v, Count: n})
		}
	}
	slices.SortFunc(out, func(a, b SelectorValue) int {
		if a.Count != b.Count {
			return int(min(max(b.Count-a.Count, -1), 1))
		}
		return strings.Compare(a.Value, b.Value)
	})
	if len(out) > maxSelectorValues {
		out = out[:maxSelectorValues]
	}
	listed := make(map[string]bool, len(out))
	for _, v := range out {
		listed[v.Value] = true
	}
	for _, v := range picked {
		if v != "" && !listed[v] {
			listed[v] = true
			out = append(out, SelectorValue{Value: v, Count: counts[v]})
		}
	}
	return out
}
