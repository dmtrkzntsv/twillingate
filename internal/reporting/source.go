package reporting

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/reporting/sqlbind"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// Params is what a widget's source may read at load time: the dashboard's
// selected project and date range. A source's content declares which of
// these it actually uses (Follows); Load binds only those.
type Params struct {
	ProjectID int64
	From, To  string
}

// SourceType is one kind of widget content: SQL over the event files, a
// structured query the native engine answers, or Markdown text. Validate
// is checked once, when a widget is saved; Load runs on every read, with
// the viewer's chosen project and range.
type SourceType interface {
	Name() string
	// Validate refuses content that does not fit c: a sql source checks
	// it against the event files (params, shape, sample rows), a query
	// source its columns, from the query alone; refusals are
	// store.ErrInvalid.
	Validate(ctx context.Context, content string, c Component) error
	// Load runs content and returns the value a widget's component
	// renders: readsql.Result for sql and query, Markdown for md.
	Load(ctx context.Context, content string, p Params) (any, error)
	// Cacheable reports whether Load's result may be kept across
	// widgets/viewers until the next refresh (sql: true; md: false — it
	// has nothing worth caching, being the content itself).
	Cacheable() bool
	// Follows reports which of Params the content actually reads, so a
	// dashboard need only re-load widgets whose source cares about the
	// part of Params that changed.
	Follows(content string) (project, rng bool)
}

// Markdown is a md source's Load result: the text itself, wrapped so the
// API returns it in the same {json} shape as any other widget value.
type Markdown struct {
	Markdown string `json:"markdown"`
}

// newSources builds the registered source types: sql on eng, with clock
// now, only when there is an engine (eng non-nil); query answered by nat
// under rules, each load under timeout (0: none) and at most maxRows rows
// (0: all); and md. sampleRows controls whether sql's Validate also runs
// a sample query and checks its rows' types (true for a widget actually
// being saved; false where only the query's shape matters). nat nil
// fails a query source's Load, not its Validate, which reads no data;
// rules nil applies none.
func newSources(eng *sqlEngine, sampleRows bool, now func() time.Time, nat Native, rules RulesFunc, timeout time.Duration, maxRows int) map[string]SourceType {
	m := map[string]SourceType{
		"query": &querySource{native: nat, rules: rules, timeout: timeout, maxRows: maxRows},
		"md":    mdSource{},
	}
	if eng != nil {
		m["sql"] = &sqlSource{eng: eng, sampleRows: sampleRows, now: now}
	}
	return m
}

// sourceTypes are the names of every source type this build has, sql
// among them whether or not an engine is configured: what a component's
// accepts may name.
var sourceTypes = map[string]bool{"sql": true, "query": true, "md": true}

// validSourceType reports whether name is a source type this build has
// (sourceTypes), registered in this process or not.
func validSourceType(name string) bool { return sourceTypes[name] }

// sortedSourceNames lists m's keys alphabetically, for a refusal that
// names every registered source type ("there are md, query and sql").
func sortedSourceNames(m map[string]SourceType) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// mdSource is the md SourceType: free-form Markdown text, checked only
// for being non-empty.
type mdSource struct{}

func (mdSource) Name() string                       { return "md" }
func (mdSource) Cacheable() bool                    { return false }
func (mdSource) Follows(string) (project, rng bool) { return false, false }

func (mdSource) Validate(_ context.Context, content string, _ Component) error {
	if strings.TrimSpace(content) == "" {
		return store.Refuse(store.ErrInvalid, "markdown text is empty")
	}
	return nil
}

func (mdSource) Load(_ context.Context, content string, _ Params) (any, error) {
	return Markdown{Markdown: content}, nil
}

// sqlSource is the sql SourceType: a SELECT in DuckDB SQL run on the
// event files by the external SQL engine, parameterized on at most
// :project, :from and :to.
type sqlSource struct {
	eng        *sqlEngine
	sampleRows bool
	now        func() time.Time // stands in for time.Now in tests; nil means time.Now
}

func (s *sqlSource) Name() string    { return "sql" }
func (s *sqlSource) Cacheable() bool { return true }

func (s *sqlSource) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// allowedParams is the closed set of named parameters a widget's SQL may
// use: the dashboard's selected project and date range. Anything else —
// including a positional ? — is refused, since a widget's SQL has no
// other input to bind.
var allowedParams = map[string]bool{":project": true, ":from": true, ":to": true}

func (s *sqlSource) Follows(content string) (project, rng bool) { return sqlFollows(content) }

// sqlFollows is which of Params a widget's SQL reads, from its text
// alone: a stored sql widget's, engine or none.
func sqlFollows(content string) (project, rng bool) {
	params, err := sqlbind.Params(content)
	if err != nil {
		return false, false
	}
	for _, p := range params {
		switch p {
		case ":project":
			project = true
		case ":from", ":to":
			rng = true
		}
	}
	return
}

// checkSQLParams refuses content sqlbind's scanner refuses, empty
// content, and any named parameter outside allowedParams, returning the
// ones it uses. Shared by a widget's Validate, Load and Preview, so they
// refuse the same SQL the same way.
func checkSQLParams(content string) ([]string, error) {
	params, err := sqlbind.Params(content)
	if err != nil {
		return nil, refuseText(err)
	}
	if strings.TrimSpace(sqlbind.Trim(content)) == "" {
		return nil, store.Refuse(store.ErrInvalid, "sql must not be empty")
	}
	for _, p := range params {
		if !allowedParams[p] {
			return nil, store.Refuse(store.ErrInvalid,
				"sql uses %s; widgets get only :project, :from and :to", p)
		}
	}
	return params, nil
}

// bindSQL binds p's values to the parameters content uses (params, from
// checkSQLParams) and returns the bound query: scoped to p's project when
// content reads :project, even to one with no rows (project 0 among them,
// which reads none), and otherwise over every project, as a widget that
// never names one read on SQLite (Decision 2). Either way it reads only
// the seven tables (confine): the save, the preview and every run of a
// widget refuse the same SQL.
func bindSQL(content string, params []string, p Params) (boundSQL, error) {
	bound, args, err := sqlbind.Bind(content, map[string]sqlbind.Value{
		":project": {Type: "BIGINT", Text: strconv.FormatInt(p.ProjectID, 10)},
		":from":    {Type: "DATE", Text: p.From},
		":to":      {Type: "DATE", Text: p.To},
	})
	if err != nil {
		return boundSQL{}, refuseText(err)
	}
	q := boundSQL{sql: bound, args: args, confine: true}
	for _, name := range params {
		if name == ":project" {
			project := p.ProjectID
			q.scope = &project
		}
	}
	return q, nil
}

func (s *sqlSource) Validate(ctx context.Context, content string, c Component) error {
	params, err := checkSQLParams(content)
	if err != nil {
		return err
	}
	if s.eng == nil {
		return ErrSQLEngine
	}

	// The engine refuses a parameter with no argument, so even the shape
	// check needs the sample values whenever content uses :project,
	// :from or :to, not only the sampleRows run below.
	needsProject, needsRange := s.Follows(content)
	var p Params
	if needsProject {
		if p.ProjectID, err = s.eng.sampleProject(ctx); err != nil {
			return err // an infrastructure error, not a refusal of content
		}
	}
	if needsRange {
		now := s.clock().UTC()
		p.From = now.AddDate(0, 0, -6).Format("2006-01-02")
		p.To = now.Format("2006-01-02")
	}
	q, err := bindSQL(content, params, p)
	if err != nil {
		return err
	}

	res, err := s.eng.ask(ctx, q, sqlengine.ModeShape, 0, nil)
	if err != nil {
		return err
	}
	if err := c.checkColumns(res.Columns); err != nil {
		return err
	}
	if !s.sampleRows {
		return nil
	}

	sample, err := s.eng.ask(ctx, q, sqlengine.ModeText, 5, nil)
	if err != nil {
		return err
	}
	if len(sample.Rows) == 0 {
		return nil
	}
	return c.checkRows(textResult(sample))
}

func (s *sqlSource) Load(ctx context.Context, content string, p Params) (any, error) {
	params, err := checkSQLParams(content)
	if err != nil {
		return nil, err
	}
	return loadRows(ctx, s.eng, content, params, p)
}

// loadRows runs content, its params bound to p's values, for its rows as
// text: at most CONSOLE_QUERY_ROWS_MAX of them, Truncated when more
// follow. Shared by Load and Preview.
func loadRows(ctx context.Context, eng *sqlEngine, content string, params []string, p Params) (readsql.Result, error) {
	q, err := bindSQL(content, params, p)
	if err != nil {
		return readsql.Result{}, err
	}
	if eng == nil {
		return readsql.Result{}, ErrSQLEngine
	}
	// One row past the cap tells a full result from a cut one.
	maxRows, limit := eng.maxRows, 0
	if maxRows > 0 {
		limit = maxRows + 1
	}
	res, err := eng.ask(ctx, q, sqlengine.ModeText, limit, nil)
	if err != nil {
		return readsql.Result{}, err
	}
	out := textResult(res)
	if maxRows > 0 && len(out.Rows) > maxRows {
		out.Rows, out.Truncated = out.Rows[:maxRows], true
	}
	return out, nil
}

// pageLoader is a source type that can load one page of its content
// rather than the whole result: sql, for a remote table.
type pageLoader interface {
	LoadPage(ctx context.Context, content string, p Params, pg readsql.Page) (readsql.PageResult, error)
}

// viewRefusal marks a refusal of the paging arguments a caller sent
// rather than of the widget itself, so wrapLoadErr leaves out the release
// notes pointer: what to change is the filter, not the query.
type viewRefusal struct{ error }

func (e viewRefusal) Unwrap() error { return e.error }

// LoadPage is Load for a remote table: content filtered, sorted and paged
// by pg in the engine, with the matched and total counts, as readsql's
// QueryPage does on SQLite: the shape for the columns (and any refusal
// of the widget's SQL itself), the page, and the two counts when the
// page has no row to carry them.
func (s *sqlSource) LoadPage(ctx context.Context, content string, p Params, pg readsql.Page) (readsql.PageResult, error) {
	params, err := checkSQLParams(content)
	if err != nil {
		return readsql.PageResult{}, err
	}
	q, err := bindSQL(content, params, p)
	if err != nil {
		return readsql.PageResult{}, err
	}
	shape, err := s.eng.ask(ctx, q, sqlengine.ModeShape, 0, nil)
	if err != nil {
		return readsql.PageResult{}, err
	}
	page := enginePage(pg)
	res, err := s.eng.ask(ctx, q, sqlengine.ModePage, s.eng.maxRows, &page)
	if err != nil {
		return readsql.PageResult{}, pageErr(err)
	}

	out := readsql.PageResult{Result: readsql.Result{Columns: shape.Columns, Rows: make([][]string, len(res.Rows))}}
	if pg.Distinct != "" {
		out.Columns = []string{"value", "rows"}
	}
	if len(res.Rows) > 0 {
		// The page's own columns, then __tw_matched and __tw_total.
		for i, row := range res.Rows {
			if len(row) < 2 {
				return readsql.PageResult{}, errors.New("reporting: a page row carries no counts")
			}
			out.Rows[i] = textRow(row[:len(row)-2])
		}
		last := res.Rows[0]
		if out.Matched, err = count(last[len(last)-2]); err != nil {
			return readsql.PageResult{}, err
		}
		if out.Total, err = count(last[len(last)-1]); err != nil {
			return readsql.PageResult{}, err
		}
	} else if err := s.countEmpty(ctx, &out, q, page); err != nil {
		return readsql.PageResult{}, err
	}
	out.Truncated = out.Matched > pg.Offset+len(out.Rows)
	return out, nil
}

// pageErr is err from a page or count query: the engine's refusal there
// is of the page (content passed the shape query before it, where any
// refusal of the text itself comes), most often a column the query does
// not return.
func pageErr(err error) error {
	var er engineRefused
	if errors.As(err, &er) {
		return viewRefusal{er.error}
	}
	return err
}

// countEmpty fills Matched and Total for a page with no rows, which has
// no row to carry them: the engine's count of what the page skipped
// (matched is 0, not run, at offset 0, where no row means nothing
// matched). q is the bound query, bindSQL's.
func (s *sqlSource) countEmpty(ctx context.Context, out *readsql.PageResult, q boundSQL, page sqlengine.Page) error {
	res, err := s.eng.ask(ctx, q, sqlengine.ModeCount, 0, &page)
	if err != nil {
		return pageErr(err)
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 2 {
		return errors.New("reporting: a page count returned no single row of two counts")
	}
	if out.Total, err = count(res.Rows[0][0]); err != nil {
		return err
	}
	out.Matched, err = count(res.Rows[0][1])
	return err
}

// enginePage is pg as the engine's protocol carries it.
func enginePage(pg readsql.Page) sqlengine.Page {
	out := sqlengine.Page{Filters: []sqlengine.Filter{}, Distinct: pg.Distinct, Limit: pg.Limit, Offset: pg.Offset}
	for _, f := range pg.Filters {
		out.Filters = append(out.Filters, sqlengine.Filter{Column: f.Column, Op: f.Op, Values: f.Values})
	}
	if pg.Sort != nil {
		out.Sort = &sqlengine.Sort{Column: pg.Sort.Column, Desc: pg.Sort.Desc}
	}
	return out
}
