package sqlbind

import (
	"fmt"
	"time"
)

// What to do when the engine runs out of memory or time, worded once for
// the callers that run a person's SQL on it: the query tool, the
// analytics tools and widgets.
const (
	// OutOfMemory follows the engine's out-of-memory error on a query.
	OutOfMemory = "the query ran out of the SQL engine's memory limit. A COUNT(DISTINCT …) over a large group is the usual cause: " +
		"select the distinct values with a GROUP BY in a subquery, then count its rows; or narrow the range"
	// LogsOutOfMemory follows the same error while the open logs load,
	// which happens before any query and whatever the query is.
	LogsOutOfMemory = "the open logs do not fit the SQL engine's memory limit while they are loaded for a query, whatever the query is: raise the SQL engine's memory limit"
	// ChildExited follows a child that died with the query in flight,
	// which the kernel's out-of-memory killer is the usual cause of.
	ChildExited = "the SQL engine stopped while running the query, most likely past the SQL engine's memory limit; narrow the query"
	// Busy follows an engine that answered busy: every slot it runs
	// queries in was taken.
	Busy = "the SQL engine is busy: all of its slots are running other queries; retry in a moment"
	// Stale follows an engine that found a file of the table spec gone on
	// every attempt: the daily pass was moving the files under the query.
	Stale = "the event files changed under the query on every attempt (the daily pass dumps and merges them); retry in a moment"
	// TooLarge follows an answer past the SQL engine client's 256 MiB cap.
	TooLarge = "the SQL engine's answer is larger than 256 MiB; return fewer rows or columns, or aggregate"
	// EngineFailed follows an engine that could not be reached or failed
	// with no word on the SQL: down, restarting, or broken.
	EngineFailed = "the SQL engine failed (SQL_ENGINE_SOCKET); retry, and if it goes on, check the engine"
)

// Timeout follows a query that ran past CONSOLE_QUERY_TIMEOUT, d.
func Timeout(d time.Duration) string {
	return fmt.Sprintf("query exceeded CONSOLE_QUERY_TIMEOUT (%s); narrow the date range, or aggregate", d)
}
