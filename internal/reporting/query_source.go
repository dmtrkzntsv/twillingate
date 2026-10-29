package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Native answers a structured query over one project's event files: the
// native engine (native.Engine), which app passes in.
type Native interface {
	Answer(ctx context.Context, q native.Query, p native.Params, rules archive.Rules) (native.Result, error)
}

// RulesFunc is the event and attribute rules a query of project reads
// with: the registry's, every project's (the engine applies the
// project's own).
type RulesFunc func(ctx context.Context, project int64) archive.Rules

// queryContent is a query source's content: a structured query, the
// new names of the output columns the component reads under others,
// labels: per column (its name after rename), values shown as others,
// columns: the ones kept (after rename), in their order (nil keeps
// every column), and project: the one it always reads, whatever the
// project switcher says (0: the switcher's).
type queryContent struct {
	Project int64                        `json:"project_id,omitempty"`
	Query   native.Query                 `json:"query"`
	Rename  map[string]string            `json:"rename,omitempty"`
	Labels  map[string]map[string]string `json:"labels,omitempty"`
	Columns []string                     `json:"columns,omitempty"`
}

// querySource is the query SourceType: a structured query the native
// engine answers over the project and range the dashboard has chosen,
// its columns renamed to the component's inputs.
type querySource struct {
	native  Native
	rules   RulesFunc
	timeout time.Duration // CONSOLE_QUERY_TIMEOUT; 0: none
	maxRows int           // CONSOLE_QUERY_ROWS_MAX; 0: none
}

func (*querySource) Name() string    { return "query" }
func (*querySource) Cacheable() bool { return true }

// Follows: a query reads one project's rows over the range, the
// switcher's project unless its content names one. Content that does
// not parse follows both, as before it names any.
func (*querySource) Follows(content string) (project, rng bool) {
	var qc struct {
		Project int64 `json:"project_id"`
	}
	if json.Unmarshal([]byte(content), &qc) == nil && qc.Project > 0 {
		return false, true
	}
	return true, true
}

// parseQuery reads and checks content: one JSON object with no unknown
// field, a valid query, a rename of its output columns onto distinct
// names, labels of text columns only, and columns naming distinct ones
// of the renamed columns. It returns the content and the columns the
// source returns, renamed and picked, with their types.
func parseQuery(content string) (queryContent, []string, []string, error) {
	if strings.TrimSpace(content) == "" {
		return queryContent{}, nil, nil, store.Refuse(store.ErrInvalid, "query source is empty")
	}
	var qc queryContent
	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&qc); err != nil {
		return queryContent{}, nil, nil, store.Refuse(store.ErrInvalid, "query source: %s", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return queryContent{}, nil, nil, store.Refuse(store.ErrInvalid, "query source: more than one JSON value")
	}
	if qc.Project < 0 {
		return queryContent{}, nil, nil, store.Refuse(store.ErrInvalid, "project_id: a project's id is 1 or more; omit it to follow the project switcher")
	}
	if err := qc.Query.Validate(); err != nil {
		return queryContent{}, nil, nil, queryErr(err)
	}
	cols, err := renamed(qc.Query.Columns(), qc.Rename)
	if err != nil {
		return queryContent{}, nil, nil, err
	}
	types := qc.Query.ColumnTypes()
	if err := checkLabels(qc.Labels, cols, types); err != nil {
		return queryContent{}, nil, nil, err
	}
	if qc.Columns == nil {
		return qc, cols, types, nil
	}
	picked, err := pick(qc.Columns, cols)
	if err != nil {
		return queryContent{}, nil, nil, err
	}
	outCols, outTypes := make([]string, len(picked)), make([]string, len(picked))
	for i, j := range picked {
		outCols[i], outTypes[i] = cols[j], types[j]
	}
	return qc, outCols, outTypes, nil
}

// pick is the index in cols (renamed) of each of columns, refusing an
// empty list, a name that is no column and one named twice.
func pick(columns, cols []string) ([]int, error) {
	if len(columns) == 0 {
		return nil, store.Refuse(store.ErrInvalid, "columns: name at least one; omit it to keep every column")
	}
	out := make([]int, len(columns))
	for i, c := range columns {
		j := slices.Index(cols, c)
		if j < 0 {
			return nil, store.Refuse(store.ErrInvalid,
				"columns: %s is not a column of the query; its columns are %s", c, strings.Join(cols, ", "))
		}
		if slices.Contains(columns[:i], c) {
			return nil, store.Refuse(store.ErrInvalid, "columns: %s is named twice", c)
		}
		out[i] = j
	}
	return out, nil
}

// checkLabels refuses labels of a column that cols (renamed) lacks, and
// of a day or number column: a label is text, which neither reads as.
func checkLabels(labels map[string]map[string]string, cols, types []string) error {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		i := slices.Index(cols, k)
		if i < 0 {
			return store.Refuse(store.ErrInvalid,
				"labels.%s: not a column of the query; its columns are %s", k, strings.Join(cols, ", "))
		}
		if t := types[i]; t != "text" {
			return store.Refuse(store.ErrInvalid, "labels.%s: a %s column takes no labels", k, t)
		}
	}
	return nil
}

// relabel replaces, in place, each value of rows that labels lists for
// its column; the rest pass through.
func relabel(rows [][]string, cols []string, labels map[string]map[string]string) {
	for i, c := range cols {
		m := labels[c]
		if len(m) == 0 {
			continue
		}
		for _, row := range rows {
			if i < len(row) {
				if l, ok := m[row[i]]; ok {
					row[i] = l
				}
			}
		}
	}
}

// renamed is cols with rename applied, refusing a key that is no column,
// an empty new name, and two columns that would end up with one name.
func renamed(cols []string, rename map[string]string) ([]string, error) {
	has := make(map[string]bool, len(cols))
	for _, c := range cols {
		has[c] = true
	}
	keys := make([]string, 0, len(rename))
	for k := range rename {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !has[k] {
			return nil, store.Refuse(store.ErrInvalid,
				"rename: %s is not a column of the query; its columns are %s", k, strings.Join(cols, ", "))
		}
		if strings.TrimSpace(rename[k]) == "" {
			return nil, store.Refuse(store.ErrInvalid, "rename: %s is renamed to an empty name", k)
		}
	}
	out := make([]string, len(cols))
	from := make(map[string]string, len(cols)) // new name → the column it came from
	for i, c := range cols {
		name := c
		if n, ok := rename[c]; ok {
			name = n
		}
		if prev, taken := from[name]; taken {
			_, prevRenamed := rename[prev]
			_, cRenamed := rename[c]
			switch {
			case prevRenamed && cRenamed:
				a, b := min(prev, c), max(prev, c)
				return nil, store.Refuse(store.ErrInvalid, "rename: %s and %s are both renamed %s", a, b, name)
			case prevRenamed:
				return nil, store.Refuse(store.ErrInvalid, "rename: %s is renamed %s, which the query already returns", prev, name)
			default:
				return nil, store.Refuse(store.ErrInvalid, "rename: %s is renamed %s, which the query already returns", c, name)
			}
		}
		from[name] = c
		out[i] = name
	}
	return out, nil
}

// queryErr is native's refusal of a query as reporting's: a malformed
// query names its field under query.
func queryErr(err error) error {
	switch {
	case errors.Is(err, native.ErrInvalid):
		return store.Refuse(store.ErrInvalid, "query.%s", strings.TrimPrefix(err.Error(), native.ErrInvalid.Error()+": "))
	case errors.Is(err, native.ErrTooLarge):
		return &resourceErr{store.Refuse(store.ErrInvalid, "%s", strings.TrimPrefix(err.Error(), "native: ")), err}
	}
	return err
}

// Validate checks content and that its columns, renamed and picked, fit c: their
// names, and their types against the ones c declares. It reads no data:
// the columns and their types come from the query alone.
func (*querySource) Validate(_ context.Context, content string, c Component) error {
	_, cols, types, err := parseQuery(content)
	if err != nil {
		return err
	}
	if err := c.checkColumns(cols); err != nil {
		return err
	}
	return c.checkColumnTypes(cols, types)
}

func (s *querySource) Load(ctx context.Context, content string, p Params) (any, error) {
	return s.load(ctx, content, p)
}

// load answers content over p's project and range: at most
// CONSOLE_QUERY_ROWS_MAX rows, under CONSOLE_QUERY_TIMEOUT, its columns renamed, its
// values relabelled and its columns picked.
func (s *querySource) load(ctx context.Context, content string, p Params) (readsql.Result, error) {
	pl, err := s.plan(ctx, content, p)
	if err != nil {
		return readsql.Result{}, err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	res, err := s.native.Answer(ctx, pl.q, pl.p, pl.rules)
	return s.answered(pl, res, err)
}

// queryPlan is one query source's load before the engine answers it:
// its content, its columns renamed, and the engine's query, params and
// rules.
type queryPlan struct {
	qc    queryContent
	cols  []string
	q     native.Query
	p     native.Params
	rules archive.Rules
}

// plan checks content and p for a load, and reads the rules it runs
// with.
func (s *querySource) plan(ctx context.Context, content string, p Params) (queryPlan, error) {
	qc, _, _, err := parseQuery(content)
	if err != nil {
		return queryPlan{}, err
	}
	cols, err := renamed(qc.Query.Columns(), qc.Rename)
	if err != nil {
		return queryPlan{}, err
	}
	if s.native == nil {
		return queryPlan{}, store.Refuse(store.ErrInvalid, "structured queries are not available in this process")
	}
	from, err := civil.Parse(p.From)
	if err != nil {
		return queryPlan{}, store.Refuse(store.ErrInvalid, "from %q is not a YYYY-MM-DD date", p.From)
	}
	to, err := civil.Parse(p.To)
	if err != nil {
		return queryPlan{}, store.Refuse(store.ErrInvalid, "to %q is not a YYYY-MM-DD date", p.To)
	}
	project := p.ProjectID
	if qc.Project > 0 {
		project = qc.Project
	}
	var rules archive.Rules
	if s.rules != nil {
		rules = s.rules(ctx, project)
	}
	return queryPlan{qc: qc, cols: cols, q: qc.Query, p: native.Params{Project: project, From: from, To: to}, rules: rules}, nil
}

// withTimeout is ctx under CONSOLE_QUERY_TIMEOUT; without one, ctx
// itself.
func (s *querySource) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.timeout)
}

// answered is pl's result from the engine's answer: an error as
// reporting's, or at most CONSOLE_QUERY_ROWS_MAX rows, its columns renamed, its
// values relabelled and its columns picked.
func (s *querySource) answered(pl queryPlan, res native.Result, err error) (readsql.Result, error) {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && s.timeout > 0 {
			return readsql.Result{}, timedOut(s.timeout, err)
		}
		return readsql.Result{}, queryErr(err)
	}
	maxRows := s.maxRows
	// The engine names the columns as Columns does; renamed by name, so a
	// column it returned and Columns did not name keeps its own.
	names := make(map[string]string, len(pl.cols))
	for i, c := range pl.q.Columns() {
		names[c] = pl.cols[i]
	}
	out := readsql.Result{Columns: make([]string, len(res.Columns)), Rows: res.Rows, Truncated: res.Truncated}
	for i, c := range res.Columns {
		if n, ok := names[c]; ok {
			c = n
		}
		out.Columns[i] = c
	}
	for _, c := range res.Approximate {
		if n, ok := names[c]; ok {
			c = n
		}
		out.Approximate = append(out.Approximate, c)
	}
	if out.Rows == nil {
		out.Rows = [][]string{}
	}
	relabel(out.Rows, out.Columns, pl.qc.Labels)
	if pl.qc.Columns != nil {
		if out, err = pickColumns(out, pl.qc.Columns); err != nil {
			return readsql.Result{}, err
		}
	}
	if maxRows > 0 && len(out.Rows) > maxRows {
		out.Rows, out.Truncated = out.Rows[:maxRows], true
	}
	return out, nil
}

// manyNative is a Native that answers several queries from one scan
// (native.Engine.AnswerMany).
type manyNative interface {
	AnswerMany(ctx context.Context, reqs []native.Request, done func(i int, a native.Answered))
}

// queryLoad is one load of a loadMany: a query source's content over
// its params.
type queryLoad struct {
	content string
	p       Params
}

// loadMany answers loads as load answers each, each answer the same, and
// calls done once per load (i its index), never two at once, as it is
// answered: a load refused before the engine runs at once, then the
// engine's answers as AnswerMany hands them over, a shared scan's when
// that scan finishes. Each load runs under its own CONSOLE_QUERY_TIMEOUT, in ctx:
// once ctx is done every load still running stops. A Native that cannot
// answer several at once answers each alone, concurrently. It returns
// after the last call to done.
func (s *querySource) loadMany(ctx context.Context, loads []queryLoad, done func(i int, res readsql.Result, err error)) {
	mn, ok := s.native.(manyNative)
	if !ok {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i, l := range loads {
			wg.Go(func() {
				var res readsql.Result
				err := errInternal
				defer func() {
					if p := recover(); p != nil {
						logPanic(p)
						mu.Lock()
						defer mu.Unlock()
						done(i, readsql.Result{}, err)
					}
				}()
				res, err = s.load(ctx, l.content, l.p)
				mu.Lock()
				defer mu.Unlock()
				done(i, res, err)
			})
		}
		wg.Wait()
		return
	}
	var (
		reqs    []native.Request
		plans   []queryPlan
		idx     []int
		cancels []context.CancelFunc
	)
	for i, l := range loads {
		pl, err := s.plan(ctx, l.content, l.p)
		if err != nil {
			done(i, readsql.Result{}, err)
			continue
		}
		// The timeout starts here, before the engine's turn, as load's does.
		rctx, cancel := s.withTimeout(ctx)
		reqs = append(reqs, native.Request{Query: pl.q, Params: pl.p, Rules: pl.rules, Ctx: rctx})
		plans, idx, cancels = append(plans, pl), append(idx, i), append(cancels, cancel)
	}
	if len(reqs) == 0 {
		return
	}
	mn.AnswerMany(ctx, reqs, func(k int, a native.Answered) {
		cancels[k]()
		res, err := s.answered(plans[k], a.Result, a.Err)
		done(idx[k], res, err)
	})
}

// pickColumns is res with only columns, in their order: its rows'
// values, and approximate marks, of those columns.
func pickColumns(res readsql.Result, columns []string) (readsql.Result, error) {
	idx := make([]int, len(columns))
	for i, c := range columns {
		j := slices.Index(res.Columns, c)
		if j < 0 {
			return readsql.Result{}, fmt.Errorf("reporting: the query returned no column %s", c)
		}
		idx[i] = j
	}
	out := readsql.Result{Columns: slices.Clone(columns), Rows: make([][]string, len(res.Rows)), Truncated: res.Truncated}
	for r, row := range res.Rows {
		picked := make([]string, len(idx))
		for i, j := range idx {
			if j < len(row) {
				picked[i] = row[j]
			}
		}
		out.Rows[r] = picked
	}
	for _, c := range res.Approximate {
		if slices.Contains(columns, c) {
			out.Approximate = append(out.Approximate, c)
		}
	}
	return out, nil
}
