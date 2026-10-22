package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
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

// queryContent is a query source's content: a structured query, and the
// new names of the output columns the component reads under others.
type queryContent struct {
	Query  native.Query      `json:"query"`
	Rename map[string]string `json:"rename,omitempty"`
}

// querySource is the query SourceType: a structured query the native
// engine answers over the project and range the dashboard has chosen,
// its columns renamed to the component's inputs.
type querySource struct {
	files  Files // the timeout and row cap; nil: neither
	native Native
	rules  RulesFunc
}

func (*querySource) Name() string    { return "query" }
func (*querySource) Cacheable() bool { return true }

// Follows: a query always reads one project's rows over the range.
func (*querySource) Follows(string) (project, rng bool) { return true, true }

// parseQuery reads and checks content: one JSON object with no unknown
// field, a valid query, and a rename of its output columns onto distinct
// names. It returns the query and its columns as renamed.
func parseQuery(content string) (native.Query, []string, error) {
	if strings.TrimSpace(content) == "" {
		return native.Query{}, nil, store.Refuse(store.ErrInvalid, "query source is empty")
	}
	var qc queryContent
	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&qc); err != nil {
		return native.Query{}, nil, store.Refuse(store.ErrInvalid, "query source: %s", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return native.Query{}, nil, store.Refuse(store.ErrInvalid, "query source: more than one JSON value")
	}
	if err := qc.Query.Validate(); err != nil {
		return native.Query{}, nil, queryErr(err)
	}
	cols, err := renamed(qc.Query.Columns(), qc.Rename)
	if err != nil {
		return native.Query{}, nil, err
	}
	return qc.Query, cols, nil
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

// Validate checks content and that its columns, renamed, fit c: their
// names, and their types against the ones c declares. It reads no data:
// the columns and their types come from the query alone.
func (*querySource) Validate(_ context.Context, content string, c Component) error {
	q, cols, err := parseQuery(content)
	if err != nil {
		return err
	}
	if err := c.checkColumns(cols); err != nil {
		return err
	}
	return c.checkColumnTypes(cols, q.ColumnTypes())
}

func (s *querySource) Load(ctx context.Context, content string, p Params) (any, error) {
	return s.load(ctx, content, p)
}

// load answers content over p's project and range: at most
// Files.MaxRows rows, under Files.Timeout, its columns renamed.
func (s *querySource) load(ctx context.Context, content string, p Params) (readsql.Result, error) {
	q, cols, err := parseQuery(content)
	if err != nil {
		return readsql.Result{}, err
	}
	if s.native == nil {
		return readsql.Result{}, store.Refuse(store.ErrInvalid, "structured queries are not available in this process")
	}
	from, err := civil.Parse(p.From)
	if err != nil {
		return readsql.Result{}, store.Refuse(store.ErrInvalid, "from %q is not a YYYY-MM-DD date", p.From)
	}
	to, err := civil.Parse(p.To)
	if err != nil {
		return readsql.Result{}, store.Refuse(store.ErrInvalid, "to %q is not a YYYY-MM-DD date", p.To)
	}
	var rules archive.Rules
	if s.rules != nil {
		rules = s.rules(ctx, p.ProjectID)
	}
	maxRows := 0
	if s.files != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.files.Timeout())
		defer cancel()
		maxRows = s.files.MaxRows()
	}
	res, err := s.native.Answer(ctx, q, native.Params{Project: p.ProjectID, From: from, To: to}, rules)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && s.files != nil {
			return readsql.Result{}, &resourceErr{store.Refuse(store.ErrInvalid, "%s", enginesql.Timeout(s.files.Timeout())), err}
		}
		return readsql.Result{}, queryErr(err)
	}
	// The engine names the columns as Columns does; renamed by name, so a
	// column it returned and Columns did not name keeps its own.
	names := make(map[string]string, len(cols))
	for i, c := range q.Columns() {
		names[c] = cols[i]
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
	if maxRows > 0 && len(out.Rows) > maxRows {
		out.Rows, out.Truncated = out.Rows[:maxRows], true
	}
	return out, nil
}
