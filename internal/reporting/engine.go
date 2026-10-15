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
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Files is where sql widgets run: the event files through the engine.
type Files interface {
	// Query runs q's SELECT, scoped to its project when q.Scope, under the
	// console's timeout, returning at most q.MaxRows rows (0: all).
	Query(ctx context.Context, q archive.Scoped) (engine.Result, error)
	// SampleProject is the live project with the newest events, 0 when none.
	SampleProject(ctx context.Context) (int64, error)
	Timeout() time.Duration
	MaxRows() int
}

// errNoFiles fails a sql widget on a Service given no Files, rather than
// a nil dereference. app wires Files everywhere; api's tests of the
// handler's routing and auth build one without, and run no widget.
var errNoFiles = errors.New("reporting: sql widgets run on the event files, and this process has none")

// sqliteHint follows an error that names what only SQLite had (Decision
// 10): a widget written before the switch fails on the first such name,
// and the error alone would not say that the whole dialect changed.
const sqliteHint = "widget SQL is DuckDB SQL over events, views, product, measures, sessions, identities and aliases; see docs://reporting"

// sqliteNames matches DuckDB's errors, as it words them, for SQLite's
// tables (v_*, agg_*, raw_*), the SQLite functions DuckDB lacks
// (julianday) or reads otherwise (strftime has no %s, date takes no
// modifiers), and the identities columns the files' table does not have.
// json_extract needs no entry: DuckDB's takes SQLite's arguments.
var sqliteNames = regexp.MustCompile(`(?i)` +
	`Table with name (v|agg|raw)_\w+` +
	`|Function with name julianday\b` +
	`|format for strftime` +
	`|arguments provided to DATE function` +
	`|column "(last_seen_day|updated_at)"`)

// runFiles runs q on files, an error classified by filesErr.
func runFiles(ctx context.Context, files Files, q archive.Scoped) (engine.Result, error) {
	if files == nil {
		return engine.Result{}, errNoFiles
	}
	res, err := files.Query(ctx, q)
	if err != nil {
		return engine.Result{}, filesErr(files, err)
	}
	return res, nil
}

// filesErr classifies an error from running a widget's own SQL as
// store.ErrInvalid where a person can act on it: the timeout names its
// environment variable, running out of memory says what to change, and
// DuckDB's own message passes through, with the hint when it names
// SQLite's. Anything else (the child could not start) stays an internal
// error. api's filesError is the query tool's own wording of the same.
func filesErr(files Files, err error) error {
	var qe *engine.QueryError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return store.Refuse(store.ErrInvalid, "%s", enginesql.Timeout(files.Timeout()))
	case errors.Is(err, archive.ErrLoadingLogs) && errors.As(err, &qe) && enginesql.IsOutOfMemory(qe.Msg):
		return store.Refuse(store.ErrInvalid, "%s", enginesql.LogsOutOfMemory)
	case errors.As(err, &qe) && enginesql.IsOutOfMemory(qe.Msg):
		return store.Refuse(store.ErrInvalid, "%s", enginesql.OutOfMemory)
	case errors.As(err, &qe):
		msg := oneLine(qe.Msg)
		if sqliteNames.MatchString(msg) {
			msg += "; " + sqliteHint
		}
		return store.Refuse(store.ErrInvalid, "%s", msg)
	case errors.Is(err, engine.ErrChildExited):
		return store.Refuse(store.ErrInvalid, "%s", enginesql.ChildExited)
	}
	return err
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
func textResult(res engine.Result) readsql.Result {
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

// WidgetFailure is a user's sql widget whose SQL the event files refuse.
type WidgetFailure struct {
	WidgetID, DashboardID int64
	Title                 string
	Err                   error
}

// CheckUserWidgets runs the check Migrate runs on the system widgets (the
// sample project and week bound, no rows, the columns the component
// needs) on every live sql widget of every live user dashboard, and
// returns those refused, in dashboard and widget order: most often SQL
// written for SQLite before the switch. A widget whose component was
// removed is left out; its dashboard says so already. An error that is
// no refusal of a widget (the engine did not start) stops the check and
// is returned with the failures found so far.
func CheckUserWidgets(ctx context.Context, st Store, files Files) ([]WidgetFailure, error) {
	svc := &Service{st: st, files: files, sources: newSources(files, false, time.Now), now: time.Now}
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
		case errors.Is(err, store.ErrInvalid):
			out = append(out, WidgetFailure{WidgetID: w.ID, DashboardID: w.DashboardID, Title: w.Title, Err: err})
		default:
			return out, err
		}
	}
	return out, nil
}
