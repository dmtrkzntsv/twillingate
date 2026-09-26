package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

type queryIn struct {
	SQL string `json:"sql" jsonschema:"a single read-only SELECT or WITH query against the v_* views; read schema://views first"`
}

// runQuery applies the four guard layers of endpoint spec §8, all inside
// readsql.Query: the pool is mode=ro + query_only + _defensive, the text
// is checked and refused (ATTACH; meta and SQLite internals), wrapped as
// a subquery with the row cap in the same clause, and run against a
// deadline. This tool keeps only the messages: readsql's sentinel errors
// map back to the wording this tool has always used.
func (h *host) runQuery(ctx context.Context, in queryIn) (tableOut, error) {
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
