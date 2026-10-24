package reporting

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
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

// newSources builds the registered source types against files and clock
// now, with nat answering query sources under rules. sampleRows controls
// whether sql's Validate also runs a sample query and checks its rows'
// types (true for a widget actually being saved; false where only the
// query's shape matters). files and now may be nil when the result is
// only used to enumerate or check registered names (validSourceType,
// sortedSourceNames) — no method that touches either is called in that
// case. nat nil fails a query source's Load, not its Validate, which
// reads no data; rules nil applies none.
func newSources(files Files, sampleRows bool, now func() time.Time, nat Native, rules RulesFunc) map[string]SourceType {
	return map[string]SourceType{
		"sql":   &sqlSource{files: files, sampleRows: sampleRows, now: now},
		"query": &querySource{files: files, native: nat, rules: rules},
		"md":    mdSource{},
	}
}

// validSourceType reports whether name is a registered source type. It
// asks the same registry newSources builds, rather than keeping a second
// list of names that could drift from it.
func validSourceType(name string) bool {
	_, ok := newSources(nil, false, nil, nil, nil)[name]
	return ok
}

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
// event files through Files, parameterized on at most :project, :from
// and :to.
type sqlSource struct {
	files      Files
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

func (s *sqlSource) Follows(content string) (project, rng bool) {
	params, err := enginesql.Params(content)
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

// checkSQLParams refuses content enginesql's scanner refuses, empty
// content, and any named parameter outside allowedParams, returning the
// ones it uses. Shared by a widget's Validate, Load and Preview, so they
// refuse the same SQL the same way.
func checkSQLParams(content string) ([]string, error) {
	params, err := enginesql.Params(content)
	if err != nil {
		return nil, refuseText(err)
	}
	if strings.TrimSpace(enginesql.Trim(content)) == "" {
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
// the seven tables (archive.Scoped.Confine): the save, the preview and
// every run of a widget refuse the same SQL.
func bindSQL(content string, params []string, p Params) (archive.Scoped, error) {
	bound, args, err := enginesql.Bind(content, map[string]enginesql.Value{
		":project": {Type: "BIGINT", Text: strconv.FormatInt(p.ProjectID, 10)},
		":from":    {Type: "DATE", Text: p.From},
		":to":      {Type: "DATE", Text: p.To},
	})
	if err != nil {
		return archive.Scoped{}, refuseText(err)
	}
	q := archive.Scoped{SQL: bound, Args: args, Confine: true}
	for _, name := range params {
		if name == ":project" {
			q.Scope, q.Project = true, p.ProjectID
		}
	}
	return q, nil
}

// rerun is a query on q's scope: sql with its args, at most maxRows rows.
func rerun(q archive.Scoped, sql string, args []engine.Arg, maxRows int) archive.Scoped {
	return archive.Scoped{SQL: sql, Args: args, MaxRows: maxRows, Scope: q.Scope, Project: q.Project, Confine: q.Confine}
}

func (s *sqlSource) Validate(ctx context.Context, content string, c Component) error {
	params, err := checkSQLParams(content)
	if err != nil {
		return err
	}

	// The engine refuses a parameter with no argument, so even the shape
	// check needs the sample values whenever content uses :project,
	// :from or :to, not only the sampleRows run below.
	needsProject, needsRange := s.Follows(content)
	var p Params
	if needsProject {
		if s.files == nil {
			return errNoFiles
		}
		p.ProjectID, err = s.files.SampleProject(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return filesErr(s.files, err)
			}
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

	res, err := runFiles(ctx, s.files, rerun(q, enginesql.Shape(q.SQL), q.Args, 0))
	if err != nil {
		return err
	}
	if err := c.checkColumns(res.Columns); err != nil {
		return err
	}
	if !s.sampleRows {
		return nil
	}

	sample, err := runFiles(ctx, s.files, rerun(q, enginesql.Text(q.SQL), q.Args, 5))
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
	return loadRows(ctx, s.files, content, params, p)
}

// loadRows runs content, its params bound to p's values, for its rows as
// text: at most Files.MaxRows of them, Truncated when more follow.
// Shared by Load and Preview.
func loadRows(ctx context.Context, files Files, content string, params []string, p Params) (readsql.Result, error) {
	q, err := bindSQL(content, params, p)
	if err != nil {
		return readsql.Result{}, err
	}
	if files == nil {
		return readsql.Result{}, errNoFiles
	}
	// One row past the cap tells a full result from a cut one.
	maxRows := files.MaxRows()
	res, err := runFiles(ctx, files, rerun(q, enginesql.Text(q.SQL), q.Args, maxRows+1))
	if err != nil {
		return readsql.Result{}, err
	}
	out := textResult(res)
	if len(out.Rows) > maxRows {
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
// QueryPage does on SQLite: the shape for the columns, the page, and the
// two counts when the page has no row to carry them.
func (s *sqlSource) LoadPage(ctx context.Context, content string, p Params, pg readsql.Page) (readsql.PageResult, error) {
	params, err := checkSQLParams(content)
	if err != nil {
		return readsql.PageResult{}, err
	}
	q, err := bindSQL(content, params, p)
	if err != nil {
		return readsql.PageResult{}, err
	}
	if s.files == nil {
		return readsql.PageResult{}, errNoFiles
	}
	shape, err := runFiles(ctx, s.files, rerun(q, enginesql.Shape(q.SQL), q.Args, 0))
	if err != nil {
		return readsql.PageResult{}, err
	}
	cols := shape.Columns
	stmt, err := enginesql.Page(q.SQL, q.Args, cols, pg, s.files.MaxRows())
	if err != nil {
		// content passed the scanner above, the only refusal of the text
		// itself, so a refusal here is of pg: most often a column the
		// query does not return.
		return readsql.PageResult{}, viewRefusal{refuseText(err)}
	}
	res, err := runFiles(ctx, s.files, rerun(q, stmt.SQL, stmt.Args, 0))
	if err != nil {
		return readsql.PageResult{}, err
	}

	out := readsql.PageResult{Result: readsql.Result{Columns: cols, Rows: make([][]string, len(res.Rows))}}
	if pg.Distinct != "" {
		out.Columns = []string{"value", "rows"}
	}
	keep := len(out.Columns)
	if len(res.Rows) > 0 {
		if out.Matched, err = count(res.Rows[0][keep]); err != nil {
			return readsql.PageResult{}, err
		}
		if out.Total, err = count(res.Rows[0][keep+1]); err != nil {
			return readsql.PageResult{}, err
		}
		for i, row := range res.Rows {
			out.Rows[i] = textRow(row[:keep])
		}
	} else if err := s.countEmpty(ctx, &out, q, cols, pg); err != nil {
		return readsql.PageResult{}, err
	}
	out.Truncated = out.Matched > pg.Offset+len(out.Rows)
	return out, nil
}

// countEmpty fills Matched and Total for a page with no rows, which has
// no row to carry them. Offset 0 with no rows means nothing matched; a
// page past the end still has to count what it skipped. q is the bound
// query, bindSQL's.
func (s *sqlSource) countEmpty(ctx context.Context, out *readsql.PageResult, q archive.Scoped, cols []string, pg readsql.Page) error {
	total, matched, err := enginesql.Count(q.SQL, q.Args, cols, pg)
	if err != nil {
		return viewRefusal{refuseText(err)}
	}
	for _, c := range []struct {
		stmt enginesql.Stmt
		n    *int
	}{{total, &out.Total}, {matched, &out.Matched}} {
		if c.stmt.SQL == "" {
			continue
		}
		res, err := runFiles(ctx, s.files, rerun(q, c.stmt.SQL, c.stmt.Args, 0))
		if err != nil {
			return err
		}
		if len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
			return errors.New("reporting: a page count returned no single cell")
		}
		if *c.n, err = count(res.Rows[0][0]); err != nil {
			return err
		}
	}
	return nil
}
