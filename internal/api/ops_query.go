package api

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
)

type queryIn struct {
	SQL string `json:"sql" jsonschema:"a single read-only SELECT or WITH query in DuckDB SQL over the event files' tables (read schema://views first)"`
}

// Files runs SQL over the raw event files (archive.Reader): query's SQL
// over every project, the analytics tools' own SQL scoped to one.
type Files interface {
	QueryScoped(ctx context.Context, q archive.Scoped, rules archive.Rules) (engine.Result, error)
}

// runQuery runs in.SQL on the event files. The source field went one
// release after the database's events did: a client still sending it is
// refused like any unknown field.
func (h *host) runQuery(ctx context.Context, in queryIn) (tableOut, error) {
	return h.runFilesQuery(ctx, in.SQL)
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
