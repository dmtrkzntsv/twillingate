package api

import (
	"context"
	"fmt"
	"strings"
)

type queryIn struct {
	SQL string `json:"sql" jsonschema:"a single read-only SELECT or WITH query against the v_* views; read schema://views first"`
}

// runQuery applies the four guard layers of endpoint spec §8:
//  1. the pool is mode=ro + query_only (writes cannot succeed),
//  2. the query is wrapped as a subquery — DDL/DML/PRAGMA/multi-statement
//     become syntax errors, and the row cap rides in the same clause,
//  3. ATTACH is rejected by token scan (read-only does not prevent it),
//  4. queryRows enforces the deadline.
//
// database/sql with this driver already refuses multi-statement query
// strings, and wrapping as a subquery makes a second statement a syntax
// error besides — belt and braces, verified by TestQueryToolBlocksWrites.
func (h *host) runQuery(ctx context.Context, in queryIn) (tableOut, error) {
	if strings.TrimSpace(in.SQL) == "" {
		return tableOut{}, invalidf("sql must not be empty")
	}
	upper := strings.ToUpper(in.SQL)
	if strings.Contains(upper, "ATTACH") {
		return tableOut{}, invalidf("ATTACH is not allowed")
	}
	h.logger.Debug("mcp query", "sql", in.SQL) // debug only, never info (spec §8)
	wrapped := fmt.Sprintf("SELECT * FROM (%s\n) LIMIT %d",
		strings.TrimRight(strings.TrimSpace(in.SQL), ";"), h.maxRows+1)
	cols, rows, _, err := queryRows(ctx, h.db, h.timeout, h.maxRows, wrapped)
	if err != nil {
		if ctx.Err() != nil || strings.Contains(err.Error(), "context deadline") {
			return tableOut{}, invalidf("query exceeded %s; narrow the date range or query agg_* tables directly", h.timeout)
		}
		return tableOut{}, invalidf("SQL error (the query runs wrapped as a subquery; only single SELECT/WITH statements parse): %v", err)
	}
	out := tableOut{Columns: cols, Rows: rows}
	if len(rows) == h.maxRows {
		out.Truncated = true
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — add a WHERE or aggregate", h.maxRows)
	}
	return out, nil
}
