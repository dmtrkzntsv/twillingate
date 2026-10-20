package enginesql

import (
	"fmt"
	"strings"
	"time"
)

// What to do when the engine runs out of memory or time, worded once for
// the callers that run a person's SQL on it: the query tool, the
// analytics tools and widgets.
const (
	// OutOfMemory follows DuckDB's own out-of-memory error on a query.
	OutOfMemory = "the query ran out of the engine's memory (ENGINE_MEMORY_LIMIT). A COUNT(DISTINCT …) over a large group is the usual cause: " +
		"select the distinct values with a GROUP BY in a subquery, then count its rows; or narrow the range"
	// LogsOutOfMemory follows the same error while the open logs load,
	// which happens before any query and whatever the query is.
	LogsOutOfMemory = "the open logs do not fit the engine's memory (ENGINE_MEMORY_LIMIT) while they are loaded for a query, whatever the query is: raise ENGINE_MEMORY_LIMIT"
	// ChildExited follows a child that died with the query in flight,
	// which the kernel's out-of-memory killer is the usual cause of.
	ChildExited = "the query engine stopped while running the query, most likely out of memory (ENGINE_MEMORY_LIMIT); narrow the query"
)

// Timeout follows a query that ran past CONSOLE_QUERY_TIMEOUT, d.
func Timeout(d time.Duration) string {
	return fmt.Sprintf("query exceeded CONSOLE_QUERY_TIMEOUT (%s); narrow the date range, or aggregate", d)
}

// IsOutOfMemory reports whether msg, an engine.QueryError's, is DuckDB's
// out-of-memory error.
func IsOutOfMemory(msg string) bool { return strings.HasPrefix(msg, "Out of Memory Error") }
