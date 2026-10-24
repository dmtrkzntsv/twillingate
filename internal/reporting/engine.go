package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// SQL is the external SQL engine sql widgets run on, over a Unix socket
// (SQL_ENGINE_SOCKET); *sqlengine.Client is one. A nil SQL means no engine
// is configured: sql is then no source type, and every SQL path answers
// ErrSQLEngine.
type SQL interface {
	Query(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error)
}

// ErrSQLEngine refuses SQL in a process with no external SQL engine
// configured: saving or previewing a sql widget, loading a stored one,
// and the query tool's sql mode. It is a refusal (store.ErrInvalid), so a
// stored widget's card shows it while the rest of its dashboard loads;
// match it with errors.Is.
var ErrSQLEngine error = &store.Refusal{Kind: store.ErrInvalid, Msg: "SQL needs an external SQL engine (SQL_ENGINE_SOCKET)"}

// sqlEngine runs a widget's SQL on the external engine: each query under
// the console's timeout, over the files under dir (archive.TableSpec)
// with the rules, scoped to the widget's project when it reads :project.
type sqlEngine struct {
	client  SQL
	dir     string    // EVENTS_DIR, which the table spec's paths are relative to
	rules   RulesFunc // nil applies none
	live    LiveFunc  // nil counts every project with files as live
	timeout time.Duration
	maxRows int
}

// newSQLEngine is client with opt's files, rules and guards; nil without
// a client.
func newSQLEngine(client SQL, opt Options) *sqlEngine {
	if client == nil {
		return nil
	}
	return &sqlEngine{client: client, dir: opt.EventsDir, rules: opt.Rules, live: opt.Live,
		timeout: opt.QueryTimeout, maxRows: opt.QueryMaxRows}
}

// boundSQL is a widget's SQL with its parameters bound (bindSQL): the
// statement, its $tw_* arguments, the project it is scoped to (nil: every
// project), and whether it reads only the seven tables.
type boundSQL struct {
	sql     string
	args    map[string]string
	scope   *int64
	confine bool
}

// ask answers q in mode on the engine, at most maxRows rows (0: all), page
// being the page of modes page and count. The table spec names the files
// of q's project (every project's when unscoped) with its rules; an
// engine that finds one of them gone (the daily pass moved it) gets a
// fresh spec, within the timeout (sqlengine.QueryFresh). An error is
// classified by e.err.
func (e *sqlEngine) ask(ctx context.Context, q boundSQL, mode sqlengine.Mode, maxRows int, page *sqlengine.Page) (sqlengine.Result, error) {
	if e == nil {
		return sqlengine.Result{}, ErrSQLEngine
	}
	var projects []int64
	var project int64
	if q.scope != nil {
		project = *q.scope
		projects = []int64{project}
	}
	var rules archive.Rules
	if e.rules != nil {
		rules = e.rules(ctx, project)
	}
	if e.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}
	var specErr error
	res, err := sqlengine.QueryFresh(ctx, e.client, sqlengine.Request{SQL: q.sql, Args: q.args, MaxRows: maxRows, Mode: mode,
		Page: page, Scope: q.scope, Confine: q.confine}, func() (sqlengine.Tables, error) {
		t, err := archive.TableSpec(e.dir, projects, rules)
		specErr = err
		return t, err
	})
	if specErr != nil {
		return sqlengine.Result{}, specErr
	}
	if err != nil {
		return sqlengine.Result{}, e.err(ctx, err)
	}
	return res, nil
}

// sampleProject is the live project whose files hold the newest day (a
// tie goes to the lowest id), from the file names alone, so it costs no
// query; 0 when no live project has any.
func (e *sqlEngine) sampleProject(ctx context.Context) (int64, error) {
	days, err := archive.LatestDays(e.dir)
	if err != nil {
		return 0, err
	}
	var id int64
	var newest string
	for pid, day := range days {
		if (e.live == nil || e.live(ctx, pid)) && (day > newest || day == newest && pid < id) {
			id, newest = pid, day
		}
	}
	return id, nil
}

// err classifies an error from running a widget's own SQL, by the
// engine's kind, as store.ErrInvalid where a person can act on it: SQL
// the engine refused (it read past the seven tables) says what it read,
// the timeout names its environment variable, running out of memory says
// what to change, and the engine's own message on the SQL passes
// through, with the hint when it names SQLite's. The timeout (the
// engine's or ctx's), running out of memory, a busy engine, a worker that
// died, files that went stale on every attempt and an engine that failed
// or could not be reached are a resourceErr: refused all the same, but no
// verdict on the SQL. A caller
// that went away keeps its own error. api's sqlError is the query tool's
// own wording of the same.
func (e *sqlEngine) err(ctx context.Context, err error) error {
	resource := func(msg string, cause error) error {
		return &resourceErr{store.Refuse(store.ErrInvalid, "%s", msg), cause}
	}
	var se *sqlengine.Error
	isKind := func(k sqlengine.Kind) bool { return errors.As(err, &se) && se.Kind == k }
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return resource(enginesql.Timeout(e.timeout), err)
	case ctx.Err() != nil: // the caller went away: whatever the engine said last
		return err
	case isKind(sqlengine.KindTimeout):
		// A timeout's cause says so (stopsCheck, Migrate's reason), as ctx's.
		return resource(enginesql.Timeout(e.timeout), fmt.Errorf("%w: %w", context.DeadlineExceeded, err))
	case isKind(sqlengine.KindOutOfMemoryLoadingLog):
		return resource(enginesql.LogsOutOfMemory, err)
	case isKind(sqlengine.KindOutOfMemory):
		return resource(enginesql.OutOfMemory, err)
	case isKind(sqlengine.KindRefused):
		return engineRefused{store.Refuse(store.ErrInvalid, "%s", withHint(se.Message))}
	case isKind(sqlengine.KindSQL):
		return store.Refuse(store.ErrInvalid, "%s", withHint(oneLine(se.Message)))
	case isKind(sqlengine.KindExited):
		return resource(enginesql.ChildExited, err)
	case isKind(sqlengine.KindBusy):
		return resource(enginesql.Busy, err)
	case isKind(sqlengine.KindStale):
		return resource(enginesql.Stale, err)
	case errors.As(err, &se), errors.Is(err, sqlengine.ErrUnavailable):
		return resource(enginesql.EngineFailed, err)
	}
	return err
}

// engineRefused is the engine's refusal of a request (KindRefused): the
// SQL reads past the seven tables, or a page it cannot build. LoadPage
// tells a refusal of its page from one of the widget by it.
type engineRefused struct{ error }

func (e engineRefused) Unwrap() error { return e.error }

// sqliteHint follows an error that names what only SQLite had (Decision
// 10): a widget written before the switch fails on the first such name,
// and the error alone would not say that the whole dialect changed.
const sqliteHint = "widget SQL is DuckDB SQL over events, views, product, measures, sessions, identities and aliases; see docs://reporting"

// sqliteNames matches DuckDB's errors, as it words them, for SQLite's
// tables (v_*, agg_*, raw_*), the SQLite functions DuckDB lacks
// (julianday) or reads otherwise (strftime has no %s, date takes no
// modifiers), the identities columns the files' table does not have, and
// events' JSON attributes column, which the files split into attr_<key>
// columns (json_extract itself runs: DuckDB's takes SQLite's arguments).
// SQLite's two-argument MAX and MIN (GREATEST and LEAST here) and any
// syntax error get it too: after the switch, SQL that no longer parses
// is most often SQLite's (a JOIN without ON, for one).
var sqliteNames = regexp.MustCompile(`(?i)` +
	`Table with name (v|agg|raw)_\w+` +
	`|"(v|agg|raw)_\w+" is not a table here` +
	`|Function with name julianday\b` +
	`|format for strftime` +
	`|arguments provided to DATE function` +
	`|column "(last_seen_day|updated_at|attributes)"` +
	`|argument types '(max|min)\(\w+, ` +
	`|syntax error at or near`)

// withHint is msg, with sqliteHint when it names what only SQLite had.
func withHint(msg string) string {
	if sqliteNames.MatchString(msg) {
		return msg + "; " + sqliteHint
	}
	return msg
}

// resourceErr is the refusal of a query that ran out of time or memory,
// whose worker died under it, or that the engine was too busy or broken
// to answer: what a person reads, and an
// errors.Is(store.ErrInvalid) like any refusal, but no failure of the SQL
// itself. It keeps the engine's error as cause, so a check of the SQL
// (CheckUserWidgets, Migrate) can tell the two apart (resourceCause).
type resourceErr struct {
	refusal error
	cause   error
}

func (e *resourceErr) Error() string { return e.refusal.Error() }
func (e *resourceErr) Unwrap() error { return e.refusal }

// resourceCause is the engine's error behind err when err is a
// resourceErr, nil otherwise.
func resourceCause(err error) error {
	var re *resourceErr
	if errors.As(err, &re) {
		return re.cause
	}
	return nil
}

// oneLine is DuckDB's message on one line, as SQLite's were, without its
// LINE excerpt, which quotes the wraps around the widget's SQL (its line
// numbers count theirs) rather than what the widget's author wrote.
func oneLine(msg string) string {
	if i := strings.Index(msg, "\nLINE "); i >= 0 {
		msg = msg[:i]
	}
	var parts []string
	for _, l := range strings.Split(msg, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// refuseText is a refusal of the widget's text by enginesql, without the
// sentinel's own "refused: " (the kind says that).
func refuseText(err error) error {
	return store.Refuse(store.ErrInvalid, "%s", strings.TrimPrefix(err.Error(), enginesql.ErrRefused.Error()+": "))
}

// textResult is res as a widget's rows: Text and the page wrap make every
// cell tw_cell's text already, so a cell that is not a string is a count
// the page added, or a value from SQL run without the wrap.
func textResult(res sqlengine.Result) readsql.Result {
	out := readsql.Result{Columns: res.Columns, Rows: make([][]string, len(res.Rows)), Truncated: res.Truncated}
	for i, row := range res.Rows {
		out.Rows[i] = textRow(row)
	}
	return out
}

func textRow(row []any) []string {
	out := make([]string, len(row))
	for i, v := range row {
		out[i] = cellText(v)
	}
	return out
}

// cellText is one engine value as text, NULL as "".
func cellText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

// count reads a count the page wrap added.
func count(v any) (int, error) {
	n, err := strconv.Atoi(cellText(v))
	if err != nil {
		return 0, fmt.Errorf("reporting: page count %v: %w", v, err)
	}
	return n, nil
}

// WidgetFailure is a user's sql widget whose SQL the event files refuse,
// or, Unchecked, one whose check could not run.
type WidgetFailure struct {
	WidgetID, DashboardID int64
	Title                 string
	Err                   error
	// Unchecked: the check ran out of time or memory, or the engine was
	// busy, failed or lost its worker under it (resourceErr), which says
	// nothing of the SQL. A timeout is that widget's own: its SQL may just
	// be slow on the sample week, so the check goes on to the next.
	// Anything else says the engine itself is short or down, and the next
	// query would fare no better: the check stops there, and that failure
	// is the last returned (Stopped).
	Unchecked bool
	// Stopped: this Unchecked failure ended the check, and the widgets
	// after it went unchecked.
	Stopped bool
}

// stopsCheck reports whether cause, a resourceErr's, says the engine
// itself is short (WidgetFailure.Unchecked) rather than that one widget's
// query ran past the timeout.
func stopsCheck(cause error) bool {
	return !errors.Is(cause, context.DeadlineExceeded)
}

// CheckUserWidgets runs the check Migrate runs on the system widgets (the
// sample project and week bound, no rows, the columns the component
// needs) on every live sql widget of every live user dashboard, and
// returns those refused, in dashboard and widget order: most often SQL
// written for SQLite before the switch. A widget whose component was
// removed is left out; its dashboard says so already. A check that could
// not run is returned as WidgetFailure.Unchecked: a plain timeout skips
// that widget, and one the engine was short for (Stopped) ends the list
// and the check. An error that is no refusal of a widget (the engine did
// not start, ctx ended) stops the check and is returned with the failures
// found so far. Without an engine (sql nil) there is nothing to check
// them on, and it checks none.
func CheckUserWidgets(ctx context.Context, st Store, sql SQL, opt Options) ([]WidgetFailure, error) {
	if sql == nil {
		return nil, nil
	}
	svc := newService(st, sql, false, opt)
	comps, err := svc.components(ctx)
	if err != nil {
		return nil, err
	}
	ds, err := st.ListDashboards(ctx)
	if err != nil {
		return nil, err
	}
	live := map[int64]bool{}
	for _, d := range ds {
		live[d.ID] = d.Owner == store.OwnerUser && d.ArchivedAt == ""
	}
	ws, err := st.ListWidgets(ctx, 0)
	if err != nil {
		return nil, err
	}
	var out []WidgetFailure
	for _, w := range ws {
		comp, ok := comps[w.Component]
		if !live[w.DashboardID] || w.ArchivedAt != "" || w.SourceType != "sql" || !ok {
			continue
		}
		err := svc.sources["sql"].Validate(ctx, w.Source, comp)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return out, ctx.Err()
		case resourceCause(err) != nil:
			stop := stopsCheck(resourceCause(err))
			out = append(out, WidgetFailure{WidgetID: w.ID, DashboardID: w.DashboardID, Title: w.Title, Err: err, Unchecked: true, Stopped: stop})
			if stop {
				return out, nil
			}
		case errors.Is(err, store.ErrInvalid):
			out = append(out, WidgetFailure{WidgetID: w.ID, DashboardID: w.DashboardID, Title: w.Title, Err: err})
		default:
			return out, err
		}
	}
	return out, nil
}
