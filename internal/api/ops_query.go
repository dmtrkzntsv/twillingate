package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/google/jsonschema-go/jsonschema"
)

type queryIn struct {
	SQL       string        `json:"sql,omitempty" jsonschema:"a single read-only SELECT or WITH query in DuckDB SQL over the event files' tables (read schema://views first); give this or query, not both"`
	ProjectID int64         `json:"project_id,omitempty" jsonschema:"with query: the project it reads (required); call list_projects first"`
	From      string        `json:"from,omitempty" jsonschema:"with query: start day inclusive, YYYY-MM-DD (default: 29 days before to)"`
	To        string        `json:"to,omitempty" jsonschema:"with query: end day inclusive, YYYY-MM-DD (default: today, UTC); at most 1100 days from from"`
	Query     *native.Query `json:"query,omitempty" jsonschema:"a structured query (stat, trend, breakdown, retention or attributes) over one project's events, answered natively; give this or sql, not both"`
}

// queryOut is query's answer: a table, and with a structured query the
// columns holding a sketch's estimate.
type queryOut struct {
	tableOut
	Approximate []string `json:"approximate,omitempty"`
}

// Native answers structured queries over the event files (native.Engine).
type Native interface {
	Answer(ctx context.Context, q native.Query, p native.Params, rules archive.Rules) (native.Result, error)
}

// queryEnums lists the structured query's closed vocabularies in query's
// input schema, which a Go type cannot say.
func queryEnums(in *jsonschema.Schema) {
	enum := func(xs []string) []any {
		out := make([]any, len(xs))
		for i, x := range xs {
			out[i] = x
		}
		return out
	}
	q := in.Properties["query"]
	q.Properties["kind"].Enum = enum(native.Kinds)
	q.Properties["table"].Enum = enum(native.Tables)
	q.Properties["compare"].Enum = enum([]string{"", "previous"})
	q.Properties["output"].Enum = enum(slices.Concat(native.RetentionOutputs, native.AttributeOutputs))
	q.Properties["identified_by"].Enum = enum(native.IdentifiedBy)
	minOffsets, maxOffsets := 1.0, 90.0
	q.Properties["offsets"].Minimum, q.Properties["offsets"].Maximum = &minOffsets, &maxOffsets
	minMilestone := 0.0
	q.Properties["milestones"].Items.Minimum, q.Properties["milestones"].Items.Maximum = &minMilestone, &maxOffsets
	maxMilestones := 6
	q.Properties["milestones"].MaxItems = &maxMilestones
	m := q.Properties["metrics"].Items
	m.Properties["op"].Enum = enum(native.MetricOps)
	var columns []string // each table's, in order, each once
	for _, c := range slices.Concat(native.ValueColumns, native.SessionValueColumns, native.IdentityValueColumns) {
		if !slices.Contains(columns, c) {
			columns = append(columns, c)
		}
	}
	m.Properties["column"].Enum = enum(columns)
	m.Properties["of"].Enum = enum(slices.Concat(native.OfColumns, native.ShareOfColumns))
	minRound, maxRound := 0.0, 6.0
	m.Properties["round"].Minimum, m.Properties["round"].Maximum = &minRound, &maxRound
	minQ, maxQ := 0.0, 1.0
	m.Properties["q"].ExclusiveMinimum, m.Properties["q"].ExclusiveMaximum = &minQ, &maxQ
	m.Properties["where"].Items.Properties["op"].Enum = enum(native.FilterOps)
	q.Properties["where"].Items.Properties["op"].Enum = enum(native.FilterOps)
}

// maxQueryDays bounds a structured query's range, from and to included.
const maxQueryDays = 1100

// Files runs SQL over the raw event files (archive.Reader): query's SQL
// over every project, the analytics tools' own SQL scoped to one.
type Files interface {
	QueryScoped(ctx context.Context, q archive.Scoped, rules archive.Rules) (engine.Result, error)
}

// runQuery runs in.SQL on the event files, or in.Query natively: exactly
// one of the two. The source field went one release after the database's
// events did: a client still sending it is refused like any unknown field.
func (h *host) runQuery(ctx context.Context, in queryIn) (queryOut, error) {
	switch {
	case in.SQL != "" && in.Query != nil:
		return queryOut{}, invalidf("give sql or query, not both")
	case in.Query != nil:
		return h.runStructured(ctx, in)
	case in.SQL == "":
		return queryOut{}, invalidf("give sql (DuckDB SQL) or query (a structured query)")
	case in.ProjectID != 0 || in.From != "" || in.To != "":
		return queryOut{}, invalidf("project_id, from and to go with query; sql filters project_id and day itself")
	}
	out, err := h.runFilesQuery(ctx, in.SQL)
	return queryOut{tableOut: out}, err
}

// runStructured answers in.Query over in.ProjectID's days in.From to
// in.To (default: the 30 days ending today, UTC) with the native engine,
// under CONSOLE_QUERY_TIMEOUT and CONSOLE_QUERY_ROWS_MAX, with the
// registry's rules as runFilesQuery applies them.
func (h *host) runStructured(ctx context.Context, in queryIn) (queryOut, error) {
	if in.ProjectID == 0 {
		return queryOut{}, invalidf("query needs project_id; call list_projects first")
	}
	to, from := civil.Today(time.Now()), civil.Date{}
	var err error
	if in.To != "" {
		if to, err = parseDay("to", in.To); err != nil {
			return queryOut{}, err
		}
	}
	from = to.AddDays(-29)
	if in.From != "" {
		if from, err = parseDay("from", in.From); err != nil {
			return queryOut{}, err
		}
	}
	if to.Before(from) {
		return queryOut{}, invalidf("from %s is after to %s", from, to)
	}
	if days := int(to.Time().Sub(from.Time()).Hours()/24) + 1; days > maxQueryDays {
		return queryOut{}, invalidf("from %s to %s is %d days; at most %d a call", from, to, days, maxQueryDays)
	}
	if err := in.Query.Validate(); err != nil {
		return queryOut{}, nativeError(err)
	}
	if h.reg.Snapshot(ctx).Project(in.ProjectID) == nil {
		return queryOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	if h.native == nil {
		return queryOut{}, invalidf("the event files cannot be queried by this process")
	}
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	events, attrs := h.reg.Snapshot(ctx).RuleLists()
	res, err := h.native.Answer(ctx, *in.Query, native.Params{Project: in.ProjectID, From: from, To: to},
		archive.Rules{Events: events, Attributes: attrs})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return queryOut{}, invalidf("%s", enginesql.Timeout(h.queryTimeout))
		}
		return queryOut{}, nativeError(err)
	}
	rows := res.Rows
	if rows == nil {
		rows = [][]string{}
	}
	out := queryOut{tableOut: tableOut{Columns: res.Columns, Rows: rows, Truncated: res.Truncated}, Approximate: res.Approximate}
	if len(rows) > h.queryRows {
		out.Rows, out.Truncated = rows[:h.queryRows], true
	}
	if out.Truncated {
		out.Note = fmt.Sprintf("truncated (at most %d rows, and the groups one query tracks); results are PARTIAL — narrow the range, add a where or lower the limit", h.queryRows)
	}
	return out, nil
}

// parseDay reads a YYYY-MM-DD day for the field name.
func parseDay(name, s string) (civil.Date, error) {
	if !isDay(s) {
		return civil.Date{}, invalidf("%s must be YYYY-MM-DD, got %q", name, s)
	}
	return civil.Parse(s)
}

// tooLarge is the refusal of a structured query that needs more memory
// than the engine allows (native.ErrTooLarge).
const tooLarge = "the query needs more memory than allowed; narrow the range or add filters"

// nativeError words a refused structured query (native.ErrInvalid, whose
// message names the field, or native.ErrTooLarge) as invalid; anything
// else passes through.
func nativeError(err error) error {
	switch {
	case errors.Is(err, native.ErrInvalid):
		return invalidf("query: %s", strings.TrimPrefix(err.Error(), native.ErrInvalid.Error()+": "))
	case errors.Is(err, native.ErrTooLarge):
		return invalidf("%s", tooLarge)
	}
	return err
}

// runFilesQuery runs sql over the event files in the engine's child,
// under CONSOLE_QUERY_ROWS_MAX and CONSOLE_QUERY_TIMEOUT, over every
// project. The child accepts one SELECT and reaches no file outside raw/
// (raw-first D21); Confine keeps it on the seven tables, where the rules
// apply (D6), and off raw/'s files themselves, as widget SQL is (#192).
func (h *host) runFilesQuery(ctx context.Context, sql string) (tableOut, error) {
	h.logger.Debug("mcp query", "source", "files", "sql", sql) // debug only, never info (spec §8)
	if h.files == nil {
		return tableOut{}, invalidf("the event files cannot be queried by this process")
	}
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	events, attrs := h.reg.Snapshot(ctx).RuleLists()
	res, err := h.files.QueryScoped(ctx, archive.Scoped{SQL: sql, MaxRows: h.queryRows, Confine: true},
		archive.Rules{Events: events, Attributes: attrs})
	if err != nil {
		return tableOut{}, filesError(err, h.queryTimeout)
	}
	out := tableOut{Columns: res.Columns, Rows: cellRows(res.Rows)}
	if res.Truncated {
		out.Truncated = true
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — add a WHERE or aggregate", h.queryRows)
	}
	return out, nil
}

// sqliteTable matches DuckDB's missing-table message for a name that was
// SQLite's: the files have none of these.
var sqliteTable = regexp.MustCompile(`(?i)Table with name (v_|raw_|agg_)\w* does not exist|"(v_|raw_|agg_)\w*" is not a table here`)

// filesError turns a failed files query into a message a model can act
// on (raw-first D21); anything else (the child could not start) passes
// through as an internal error.
func filesError(err error, timeout time.Duration) error {
	var qe *engine.QueryError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return invalidf("%s", enginesql.Timeout(timeout))
	case errors.Is(err, archive.ErrLoadingLogs) && errors.As(err, &qe) && enginesql.IsOutOfMemory(qe.Msg):
		return invalidf("%s", enginesql.LogsOutOfMemory)
	case errors.As(err, &qe) && enginesql.IsOutOfMemory(qe.Msg):
		return invalidf("%s", enginesql.OutOfMemory)
	case errors.Is(err, enginesql.ErrRefused):
		msg := strings.TrimPrefix(err.Error(), enginesql.ErrRefused.Error()+": ")
		if sqliteTable.MatchString(msg) {
			msg += "; the v_*, raw_* and agg_* tables went with the database's events (docs://twillingate)"
		}
		return invalidf("%s", msg)
	case errors.As(err, &qe) && sqliteTable.MatchString(qe.Msg):
		return invalidf(`%s; the v_*, raw_* and agg_* tables went with the database's events. The event files hold events, views, product, measures, sessions, identities and aliases (docs://twillingate)`,
			strings.SplitN(qe.Msg, "\n", 2)[0])
	case errors.As(err, &qe):
		return invalidf("SQL error (DuckDB): %s", qe.Msg)
	case errors.Is(err, engine.ErrChildExited):
		return invalidf("%s", enginesql.ChildExited)
	}
	return err
}

// cellText is an engine value as query's text: NULL as "" (as SQLite's
// rows read), a number in its exact text, a list or struct as JSON.
func cellText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
