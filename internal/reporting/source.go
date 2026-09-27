package reporting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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

// SourceType is one kind of widget content: SQL against the store, or
// Markdown text. Validate is checked once, when a widget is saved;
// Load runs on every read, with the viewer's chosen project and range.
type SourceType interface {
	Name() string
	// Validate refuses content that does not fit c: a sql source checks
	// it against the database (params, shape, sample rows); refusals are
	// store.ErrInvalid.
	Validate(ctx context.Context, content string, c Component) error
	// Load runs content and returns the value a widget's component
	// renders: readsql.Result for sql, Markdown for md.
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

// newSources builds the registered source types against db and clock
// now. sampleRows controls whether sql's Validate also runs a sample
// query and checks its rows' types (true for a widget actually being
// saved; false where only the query's shape matters). db and now may be
// nil when the result is only used to enumerate or check registered
// names (validSourceType, sortedSourceNames) — no method that touches
// either is called in that case.
func newSources(db *readsql.DB, sampleRows bool, now func() time.Time) map[string]SourceType {
	return map[string]SourceType{
		"sql": &sqlSource{db: db, sampleRows: sampleRows, now: now},
		"md":  mdSource{},
	}
}

// validSourceType reports whether name is a registered source type. It
// asks the same registry newSources builds, rather than keeping a second
// list of names that could drift from it.
func validSourceType(name string) bool {
	_, ok := newSources(nil, false, nil)[name]
	return ok
}

// sortedSourceNames lists m's keys alphabetically, for a refusal that
// names every registered source type ("there are md and sql").
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

// sqlSource is the sql SourceType: a SELECT run read-only through
// readsql, parameterized on at most :project, :from and :to.
type sqlSource struct {
	db         *readsql.DB
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
	params, err := readsql.Check(content)
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

func (s *sqlSource) Validate(ctx context.Context, content string, c Component) error {
	params, err := readsql.Check(content)
	if err != nil {
		return store.Refuse(store.ErrInvalid, "%s", err)
	}
	for _, p := range params {
		if !allowedParams[p] {
			return store.Refuse(store.ErrInvalid,
				"sql uses %s; widgets get only :project, :from and :to", p)
		}
	}

	// The sample values must be computed — and bound — before the very
	// first run: SQLite errors "missing named argument" for a named
	// parameter the statement mentions but no argument was given for, so
	// even the LIMIT 0 shape check needs them whenever content uses
	// :project, :from or :to, not only the sampleRows path below.
	needsProject, needsRange := s.Follows(content)
	var projectID int64
	var from, to string
	if needsProject {
		projectID, err = sampleProject(ctx, s.db)
		if err != nil {
			if errors.Is(err, readsql.ErrTimeout) {
				return refuseSQLErr(s.db, err)
			}
			return err // an infrastructure error, not a refusal of content
		}
	}
	if needsRange {
		now := s.clock().UTC()
		from = now.AddDate(0, 0, -6).Format("2006-01-02")
		to = now.Format("2006-01-02")
	}
	args := bindArgs(params, projectID, from, to)

	res, err := s.db.QueryLimit(ctx, content, 0, args...)
	if err != nil {
		return refuseSQLErr(s.db, err)
	}
	if err := c.checkColumns(res.Columns); err != nil {
		return err
	}
	if !s.sampleRows {
		return nil
	}

	sample, err := s.db.QueryLimit(ctx, content, 5, args...)
	if err != nil {
		return refuseSQLErr(s.db, err)
	}
	if len(sample.Rows) == 0 {
		return nil
	}
	return c.checkRows(sample)
}

func (s *sqlSource) Load(ctx context.Context, content string, p Params) (any, error) {
	params, err := readsql.Check(content)
	if err != nil {
		return nil, store.Refuse(store.ErrInvalid, "%s", err)
	}
	res, err := s.db.Query(ctx, content, bindArgs(params, p.ProjectID, p.From, p.To)...)
	if err != nil {
		return nil, refuseSQLErr(s.db, err)
	}
	return res, nil
}

// bindArgs builds the sql.Named arguments for exactly the params content
// uses (params is Check's own list, already refused down to the
// project/from/to allow-list by the time this is called), so a widget
// that never mentions :project need not have one bound.
func bindArgs(params []string, projectID int64, from, to string) []any {
	var args []any
	for _, p := range params {
		switch p {
		case ":project":
			args = append(args, sql.Named("project", projectID))
		case ":from":
			args = append(args, sql.Named("from", from))
		case ":to":
			args = append(args, sql.Named("to", to))
		}
	}
	return args
}

// refuseSQLErr classifies a readsql error from running a widget's own
// content as store.ErrInvalid: a timeout names the environment variable
// a person can act on, everything else (Check's own refusal, or
// SQLite's syntax error text) passes through as the message.
func refuseSQLErr(db *readsql.DB, err error) error {
	if errors.Is(err, readsql.ErrTimeout) {
		return store.Refuse(store.ErrInvalid,
			"query exceeded API_QUERY_TIMEOUT (%s); narrow the range or group the query", db.Timeout())
	}
	return store.Refuse(store.ErrInvalid, "%s", err)
}

// sampleProject picks the project with the most recent data (views or
// product), for a sample run of a widget's SQL when no dashboard
// selection exists yet. It reads projects and events directly — trusted
// Go SQL, not checked custom SQL — which is why it may read a table
// widget SQL itself may not. Its own errors are not store refusals (the
// caller decides: a timeout still becomes one, anything else is this
// package's own infrastructure failing, not a problem with the widget).
func sampleProject(ctx context.Context, db *readsql.DB) (int64, error) {
	res, err := db.Run(ctx, `
SELECT p.id FROM projects p WHERE p.archived_at IS NULL
ORDER BY MAX(COALESCE((SELECT MAX(day) FROM events WHERE family='views' AND project_id=p.id),''),
             COALESCE((SELECT MAX(day) FROM events WHERE family='product' AND project_id=p.id),'')) DESC, p.id
LIMIT 1`)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 {
		return 0, nil
	}
	id, err := strconv.ParseInt(res.Rows[0][0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("reporting: sampleProject: %w", err)
	}
	return id, nil
}
