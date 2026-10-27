package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/reporting/sqlbind"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
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
	// Today is the engine's clock's UTC day.
	Today() civil.Date
}

// metricSchema is native.Metric's schema, which the inference cannot
// reach itself: its num and den are Metrics. They are described as
// native.Metric's fields but as, round, num and den, the fields a
// ratio's side does not take. As and Round are left out by their Go
// names (renaming them breaks the side's schema, which
// TestQuerySchemaRatioSides pins), and the copy is built with
// reflect.StructOf, which panics, at init through expose.go, should
// native.Metric ever gain an unexported field: keep its fields exported.
func metricSchema() *jsonschema.Schema {
	mt := reflect.TypeFor[native.Metric]()
	fields := func(side reflect.Type) []reflect.StructField {
		var out []reflect.StructField
		for i := range mt.NumField() {
			f := mt.Field(i)
			switch {
			case f.Type == reflect.PointerTo(mt) && side == nil:
				continue
			case f.Type == reflect.PointerTo(mt):
				f.Type = reflect.PointerTo(side)
			case side == nil && (f.Name == "As" || f.Name == "Round"):
				continue
			}
			out = append(out, f)
		}
		return out
	}
	s, err := jsonschema.ForType(reflect.StructOf(fields(reflect.StructOf(fields(nil)))), filterOptions)
	if err != nil {
		panic(fmt.Sprintf("api: schema for native.Metric: %v", err))
	}
	return s
}

// filterOptions describes native.Filter, for the schemas that hold one.
var filterOptions = &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[native.Filter](): filterSchema(),
}}

// filterSchema is native.Filter's schema, three levels deep where the
// type would recurse, a cycle the inference refuses: an any_of's
// filters hold neither any nor where (Validate refuses an any_of or an
// ever in one), and an ever's where holds filters with an any_of's any
// but no where (no ever in an ever).
func filterSchema() *jsonschema.Schema {
	ft := reflect.TypeFor[native.Filter]()
	fields := func(anyOf, where reflect.Type) []reflect.StructField {
		var out []reflect.StructField
		for i := range ft.NumField() {
			f := ft.Field(i)
			switch {
			case f.Name == "Any" && anyOf == nil, f.Name == "Where" && where == nil:
				continue
			case f.Name == "Any":
				f.Type = reflect.SliceOf(anyOf)
			case f.Name == "Where":
				f.Type = reflect.SliceOf(where)
			}
			out = append(out, f)
		}
		return out
	}
	plain := reflect.StructOf(fields(nil, nil))
	inEver := reflect.StructOf(fields(plain, nil))
	s, err := jsonschema.ForType(reflect.StructOf(fields(plain, inEver)), nil)
	if err != nil {
		panic(fmt.Sprintf("api: schema for native.Filter: %v", err))
	}
	return s
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
	q.Properties["fill"].Enum = enum([]string{"", "zero"})
	q.Properties["interval"].Enum = enum(append([]string{""}, native.Intervals...))
	q.Properties["output"].Enum = enum(slices.Concat(native.RetentionOutputs, native.AttributeOutputs))
	q.Properties["identified_by"].Enum = enum(native.IdentifiedBy)
	minOffsets, maxOffsets := 1.0, 90.0
	q.Properties["offsets"].Minimum, q.Properties["offsets"].Maximum = &minOffsets, &maxOffsets
	minMilestone := 0.0
	q.Properties["milestones"].Items.Minimum, q.Properties["milestones"].Items.Maximum = &minMilestone, &maxOffsets
	maxMilestones := 6
	q.Properties["milestones"].MaxItems = &maxMilestones
	maxKeep := native.MaxKeep
	q.Properties["keep"].MaxItems = &maxKeep
	m := q.Properties["metrics"].Items
	m.Properties["op"].Enum = enum(native.MetricOps)
	once := func(lists ...[]string) []string { // each list's, in order, each once
		var out []string
		for _, c := range slices.Concat(lists...) {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
		return out
	}
	numbers := once(native.ValueColumns, native.SessionValueColumns, native.IdentityValueColumns)
	// latest reads any column a filter names, attr_<key> and def_ ones
	// included; the other ops a number or a bound.
	mc := m.Properties["column"]
	m.Properties["column"] = &jsonschema.Schema{Description: mc.Description, AnyOf: []*jsonschema.Schema{
		{Type: "string", Enum: enum(once(numbers, native.BoundColumns, native.SessionBoundColumns, native.LatestColumns))},
		{Type: "string", Pattern: "^(attr|def)_.+"},
	}}
	m.Properties["of"].Enum = enum(slices.Concat(native.OfColumns, native.ShareOfColumns))
	minRound, maxRound := 0.0, 6.0
	m.Properties["round"].Minimum, m.Properties["round"].Maximum = &minRound, &maxRound
	minQ, maxQ := 0.0, 1.0
	m.Properties["q"].ExclusiveMinimum, m.Properties["q"].ExclusiveMaximum = &minQ, &maxQ
	filterEnums := func(f *jsonschema.Schema) {
		f.Properties["op"].Enum = enum(native.FilterOps)
		f.Properties["by"].Enum = enum(append([]string{""}, native.EverBy...))
		f.Properties["over"].Enum = enum(append([]string{""}, native.EverOver...))
		plain := slices.DeleteFunc(slices.Clone(native.FilterOps), func(op string) bool { return op == "any_of" || op == "ever" || op == "never" })
		f.Properties["any"].Items.Properties["op"].Enum = enum(plain)
		w := f.Properties["where"].Items
		w.Properties["op"].Enum = enum(append(plain, "any_of"))
		w.Properties["any"].Items.Properties["op"].Enum = enum(plain)
	}
	filterEnums(m.Properties["where"].Items)
	filterEnums(q.Properties["where"].Items)
	filterEnums(q.Properties["define"].Items.Properties["cases"].Items.Properties["where"].Items)
	filterEnums(q.Properties["steps"].Items.Properties["where"].Items)
	q.Properties["by"].Enum = enum(append([]string{""}, native.EverBy...))
	q.Properties["shape"].Enum = enum(append([]string{""}, native.Shapes...))
	sideOps := slices.DeleteFunc(slices.Clone(native.MetricOps), func(op string) bool { return op == "ratio" || op == "share" })
	for _, side := range []string{"num", "den"} {
		s := m.Properties[side]
		s.Properties["op"].Enum = enum(sideOps)
		s.Properties["column"].Enum = enum(numbers) // a side is a number: no day or time
		s.Properties["of"].Enum = enum(native.OfColumns)
		s.Properties["q"].ExclusiveMinimum, s.Properties["q"].ExclusiveMaximum = &minQ, &maxQ
		filterEnums(s.Properties["where"].Items)
	}
}

// maxQueryDays bounds a structured query's range, from and to included.
const maxQueryDays = 1100

// SQL is the external SQL engine query's sql runs on, over a Unix socket
// (SQL_ENGINE_SOCKET); *sqlengine.Client is one. nil means none is
// configured, and sql is refused (reporting.ErrSQLEngine).
type SQL interface {
	Query(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error)
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
	out, err := h.runSQL(ctx, in.SQL)
	return queryOut{tableOut: out}, err
}

// runStructured answers in.Query over in.ProjectID's days in.From to
// in.To (default: the 30 days ending today, UTC) with the native engine,
// under CONSOLE_QUERY_TIMEOUT and CONSOLE_QUERY_ROWS_MAX, with the
// registry's rules as runSQL applies them.
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
	res, err := h.answer(ctx, *in.Query, native.Params{Project: in.ProjectID, From: from, To: to})
	if err != nil {
		return queryOut{}, err
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

// answer answers q natively under CONSOLE_QUERY_TIMEOUT, with the
// registry's rules as runSQL applies them: the query tool's
// structured queries and the analytics tools' own. A refusal or a timeout
// reads as invalid (nativeError).
func (h *host) answer(ctx context.Context, q native.Query, p native.Params) (native.Result, error) {
	if h.native == nil {
		return native.Result{}, invalidf("the event files cannot be queried by this process")
	}
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	events, attrs := h.reg.Snapshot(ctx).RuleLists()
	res, err := h.native.Answer(ctx, q, p, archive.Rules{Events: events, Attributes: attrs})
	if err != nil {
		return native.Result{}, h.answerErr(err)
	}
	return res, nil
}

// answerErr words an engine's refusal as answer does: a timeout as one,
// in CONSOLE_QUERY_TIMEOUT's words, a refusal as invalid (nativeError).
func (h *host) answerErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return timeoutf("%s", sqlbind.Timeout(h.queryTimeout))
	}
	return nativeError(err)
}

// manyNative is a Native that answers several queries from shared scans
// (native.Engine.AnswerMany).
type manyNative interface {
	AnswerMany(ctx context.Context, reqs []native.Request, done func(i int, a native.Answered))
}

// The engine answers from shared scans: a drifted signature fails the
// build rather than falling back to one query at a time.
var _ manyNative = (*native.Engine)(nil)

// answerAll answers a tool's queries as answer answers one, under one
// CONSOLE_QUERY_TIMEOUT for them all and the registry's rules (each
// request's own Rules and Ctx are ignored): from shared scans when the
// engine has them (manyNative), else one by one in order, stopping at the
// first failure, whose error the answers after it repeat. Errors read as
// answer's. A caller reads the answers in order and stops at the first
// error, so a tool fails with the error it failed with one by one.
func (h *host) answerAll(ctx context.Context, reqs []native.Request) []native.Answered {
	return h.answerRequests(ctx, reqs, false)
}

// answerEach is answerAll with a CONSOLE_QUERY_TIMEOUT of its own for
// each request (its Ctx), derived from ctx, rather than one for them all:
// for a tool whose requests are many projects' each, which wait for the
// engine's turns in turn.
func (h *host) answerEach(ctx context.Context, reqs []native.Request) []native.Answered {
	return h.answerRequests(ctx, reqs, true)
}

// answerRequests is answerAll, with a timeout of its own for each request
// when each is set.
func (h *host) answerRequests(ctx context.Context, reqs []native.Request, each bool) []native.Answered {
	out := make([]native.Answered, len(reqs))
	if h.native == nil {
		err := invalidf("the event files cannot be queried by this process")
		for i := range out {
			out[i].Err = err
		}
		return out
	}
	if !each {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.queryTimeout)
		defer cancel()
	}
	// own is a request's context: its own timeout when each, else ctx.
	own := func() (context.Context, context.CancelFunc) {
		if each {
			return context.WithTimeout(ctx, h.queryTimeout)
		}
		return ctx, func() {}
	}
	events, attrs := h.reg.Snapshot(ctx).RuleLists()
	rules := archive.Rules{Events: events, Attributes: attrs}
	if m, ok := h.native.(manyNative); ok {
		all := make([]native.Request, len(reqs))
		for i, r := range reqs {
			rctx, cancel := own()
			defer cancel()
			all[i] = native.Request{Query: r.Query, Params: r.Params, Rules: rules, Ctx: rctx}
		}
		m.AnswerMany(ctx, all, func(i int, a native.Answered) {
			if a.Err != nil {
				a.Err = h.answerErr(a.Err)
			}
			out[i] = a
		})
		return out
	}
	var err error
	for i, r := range reqs {
		if err == nil {
			rctx, cancel := own()
			out[i].Result, err = h.native.Answer(rctx, r.Query, r.Params, rules)
			cancel()
			if err != nil {
				err = h.answerErr(err)
			}
		}
		out[i].Err = err
	}
	return out
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

// runSQL runs sql on the external SQL engine over every project's event
// files, under CONSOLE_QUERY_ROWS_MAX and CONSOLE_QUERY_TIMEOUT, with the
// registry's rules. The engine runs one read-only SELECT and reaches no
// file outside the table spec (raw-first D21); confine keeps it on the
// seven tables, where the rules apply (D6), and off raw/'s files
// themselves, as widget SQL is (#192): always set from the tool. An
// engine that finds a file of the spec gone (the daily pass moved it)
// gets a fresh spec, within the timeout (sqlengine.QueryFresh).
func (h *host) runSQL(ctx context.Context, sql string) (tableOut, error) {
	h.logger.Debug("mcp query", "source", "files", "sql", sql) // debug only, never info (spec §8)
	if h.sql == nil {
		return tableOut{}, reporting.ErrSQLEngine
	}
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	events, attrs := h.reg.Snapshot(ctx).RuleLists()
	rules := archive.Rules{Events: events, Attributes: attrs}
	var specErr error
	res, err := sqlengine.QueryFresh(ctx, h.sql, sqlengine.Request{SQL: sql, Args: map[string]string{}, MaxRows: h.queryRows,
		Mode: sqlengine.ModeRows, Confine: true}, func() (sqlengine.Tables, error) {
		t, err := archive.TableSpec(h.eventsDir, nil, rules)
		specErr = err
		return t, err
	})
	if specErr != nil {
		return tableOut{}, specErr
	}
	if err != nil {
		return tableOut{}, h.sqlError(ctx, err)
	}
	out := tableOut{Columns: res.Columns, Rows: cellRows(res.Rows)}
	if out.Columns == nil {
		out.Columns = []string{}
	}
	if res.Truncated {
		out.Truncated = true
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — add a WHERE or aggregate", h.queryRows)
	}
	return out, nil
}

// sqliteTable matches DuckDB's missing-table message for a name that was
// SQLite's: the files have none of these.
var sqliteTable = regexp.MustCompile(`(?i)Table with name (v_|raw_|agg_)\w* does not exist|"(v_|raw_|agg_)\w*" is not a table here`)

// sqlError turns a failed query on the engine into a message a model can
// act on (raw-first D21), by the engine's kind: the timeout (the engine's
// or ctx's), memory, a busy engine, a lost worker, files stale on every
// attempt and an answer past the client's cap with what to change,
// a refusal with what the SQL read, the engine's own message on the SQL
// as it is. An engine that failed or could not be reached reads as such,
// its cause logged; a caller that went away keeps its own error.
func (h *host) sqlError(ctx context.Context, err error) error {
	var se *sqlengine.Error
	isKind := func(k sqlengine.Kind) bool { return errors.As(err, &se) && se.Kind == k }
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return timeoutf("%s", sqlbind.Timeout(h.queryTimeout))
	case ctx.Err() != nil: // the caller went away: whatever the engine said last
		return err
	case isKind(sqlengine.KindTimeout):
		return timeoutf("%s", sqlbind.Timeout(h.queryTimeout))
	case isKind(sqlengine.KindOutOfMemoryLoadingLog):
		return invalidf("%s", sqlbind.LogsOutOfMemory)
	case isKind(sqlengine.KindOutOfMemory):
		return invalidf("%s", sqlbind.OutOfMemory)
	case isKind(sqlengine.KindRefused):
		msg := se.Message
		if sqliteTable.MatchString(msg) {
			msg += "; the v_*, raw_* and agg_* tables went with the database's events (docs://twillingate)"
		}
		return invalidf("%s", msg)
	case isKind(sqlengine.KindSQL) && sqliteTable.MatchString(se.Message):
		return invalidf(`%s; the v_*, raw_* and agg_* tables went with the database's events. The event files hold events, views, product, measures, sessions, identities and aliases (docs://twillingate)`,
			strings.SplitN(se.Message, "\n", 2)[0])
	case isKind(sqlengine.KindSQL):
		return invalidf("SQL error (DuckDB): %s", se.Message)
	case isKind(sqlengine.KindExited):
		return invalidf("%s", sqlbind.EngineExited)
	case isKind(sqlengine.KindBusy):
		return store.Refuse(manage.ErrBusy, "%s", sqlbind.Busy)
	case isKind(sqlengine.KindStale):
		return invalidf("%s", sqlbind.Stale)
	case errors.Is(err, sqlengine.ErrTooLarge):
		return invalidf("%s", sqlbind.TooLarge)
	case errors.As(err, &se), errors.Is(err, sqlengine.ErrUnavailable):
		h.logger.Warn("the SQL engine failed", "error", err.Error())
		return invalidf("%s", sqlbind.EngineFailed)
	}
	return err
}

// cellRows is an engine result's rows as text (cellText), never nil.
func cellRows(rows [][]any) [][]string {
	out := make([][]string, len(rows))
	for i, row := range rows {
		out[i] = make([]string, len(row))
		for j, v := range row {
			out[i][j] = cellText(v)
		}
	}
	return out
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
