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
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

type queryIn struct {
	SQL    string `json:"sql" jsonschema:"a single read-only SELECT or WITH query; for source sqlite read schema://views first, for source files docs://twillingate (Writing SQL against the event files)"`
	Source string `json:"source,omitempty" jsonschema:"optional: sqlite (the default) for the v_* views and agg_* tables; files for the raw events through DuckDB (events, views, product, measures)"`
}

// Files runs SQL over the raw event files (archive.Reader).
type Files interface {
	Query(ctx context.Context, sql string, maxRows int, rules archive.Rules) (engine.Result, error)
}

// runQuery runs in.SQL on SQLite (source sqlite, the default) or on the
// event files (source files). On SQLite it applies the four guard layers
// of endpoint spec §8, all inside readsql.Query: the pool is mode=ro +
// query_only + _defensive; the text is checked and refused (ATTACH; meta
// and SQLite internals; a second statement — readsql.Check tracks paren
// depth and statement boundaries itself, since the driver runs every
// statement it is given and a stray ')' or ';' could otherwise ride a
// trailing statement in on the same call); it is then wrapped as a
// subquery with the row cap in the same clause, so a checked single
// statement that is not itself a query becomes a syntax error; and it
// runs against a deadline. This tool keeps only the messages: readsql's
// sentinel errors map back to the wording this tool has always used.
func (h *host) runQuery(ctx context.Context, in queryIn) (tableOut, error) {
	switch in.Source {
	case "", "sqlite":
	case "files":
		return h.runFilesQuery(ctx, in.SQL)
	default:
		return tableOut{}, invalidf(`source must be "sqlite" or "files", got %q`, in.Source)
	}
	h.logger.Debug("mcp query", "sql", in.SQL) // debug only, never info (spec §8)
	res, err := h.db.Query(ctx, in.SQL)
	if err != nil {
		switch {
		case errors.Is(err, readsql.ErrTimeout):
			return tableOut{}, invalidf("query exceeded %s; narrow the date range or query agg_* tables directly", h.db.Timeout())
		case errors.Is(err, readsql.ErrRefused):
			return tableOut{}, invalidf("%s", strings.TrimPrefix(err.Error(), readsql.ErrRefused.Error()+": "))
		default:
			return tableOut{}, invalidf("SQL error (the query runs wrapped as a subquery; only single SELECT/WITH statements parse): %v", err)
		}
	}
	out := tableOut{Columns: res.Columns, Rows: res.Rows}
	if res.Truncated {
		out.Truncated = true
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — add a WHERE or aggregate", h.db.MaxRows())
	}
	return out, nil
}

// runFilesQuery runs sql over the event files in the engine's child,
// under SQLite's row cap and timeout (CONSOLE_QUERY_ROWS_MAX,
// CONSOLE_QUERY_TIMEOUT). The child accepts one SELECT and reaches no
// file outside raw/, which stands in for readsql's guards (raw-first
// D21); the registry's rules reach the tables, which apply them (D6).
func (h *host) runFilesQuery(ctx context.Context, sql string) (tableOut, error) {
	h.logger.Debug("mcp query", "source", "files", "sql", sql) // debug only, never info (spec §8)
	if h.files == nil {
		return tableOut{}, invalidf("the event files cannot be queried by this process")
	}
	ctx, cancel := context.WithTimeout(ctx, h.db.Timeout())
	defer cancel()
	events, attrs := h.reg.Snapshot(ctx).RuleLists()
	res, err := h.files.Query(ctx, sql, h.db.MaxRows(), archive.Rules{Events: events, Attributes: attrs})
	if err != nil {
		return tableOut{}, filesError(err, h.db.Timeout())
	}
	out := tableOut{Columns: res.Columns, Rows: make([][]string, len(res.Rows))}
	for i, row := range res.Rows {
		out.Rows[i] = make([]string, len(row))
		for j, v := range row {
			out.Rows[i][j] = cellText(v)
		}
	}
	if res.Truncated {
		out.Truncated = true
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — add a WHERE or aggregate", h.db.MaxRows())
	}
	return out, nil
}

// sqliteTable matches DuckDB's missing-table message for a name that is
// SQLite's: the files have none of these.
var sqliteTable = regexp.MustCompile(`Table with name (v_|raw_|agg_)\w* does not exist`)

// filesError turns a failed files query into a message a model can act
// on (raw-first D21); anything else (the child could not start) passes
// through as an internal error.
func filesError(err error, timeout time.Duration) error {
	var qe *engine.QueryError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return invalidf("query exceeded %s (CONSOLE_QUERY_TIMEOUT); narrow it with a WHERE on project_id and day, or aggregate", timeout)
	case errors.As(err, &qe) && strings.HasPrefix(qe.Msg, "Out of Memory Error"):
		return invalidf("the query ran out of the engine's memory (ENGINE_MEMORY_LIMIT). A COUNT(DISTINCT …) over a large group is the usual cause: " +
			"select the distinct values with a GROUP BY in a subquery, then count its rows; or narrow the range")
	case errors.As(err, &qe) && sqliteTable.MatchString(qe.Msg):
		return invalidf(`%s; the v_*, raw_* and agg_* tables are SQLite's: pass source "sqlite" for them. The files hold events, views, product and measures`,
			strings.SplitN(qe.Msg, "\n", 2)[0])
	case errors.As(err, &qe):
		return invalidf("SQL error (DuckDB): %s", qe.Msg)
	case errors.Is(err, engine.ErrChildExited):
		return invalidf("the query engine stopped while running the query, most likely out of memory (ENGINE_MEMORY_LIMIT); narrow the query")
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
