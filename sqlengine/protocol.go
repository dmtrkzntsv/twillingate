// Package sqlengine defines the protocol twillingate speaks to an external
// SQL engine: HTTP and JSON over a Unix socket, version 1. It holds the
// request, response, error and table-spec types with their wire shapes
// and a Client that sends them, and imports only the standard library so
// an engine written in Go can import it too. A value in a Result is JSON
// null, a boolean, a JSON number or a string: numbers are JSON numbers
// (decode with json.Decoder.UseNumber to keep them exact), while dates,
// times, timestamps, UUIDs, NaN and the infinities are strings. An engine
// ignores any request field it does not know, so version 1 can grow by
// adding fields; what else it must do is in docs/deployment.md, "Engine
// requirements".
//
// Two modes answer a shape of their own (Request.Mode):
//   - page: the page's rows as text, its columns the query's with the two
//     counts appended, __tw_matched (the rows past the filters) and
//     __tw_total (the query's rows), on every row; testdata/response_page.json;
//   - count: columns ["total","matched"] and one row, for a page that came
//     back with no row to carry its counts; matched is 0, not run, when
//     the offset is 0; testdata/response_count.json.
//
// An error is a status and a kind (Kind.Status): 400 refused or sql, 409
// stale (a file the table spec names is gone: build the spec afresh and
// send it again, QueryFresh), 503 busy, 504 timeout (X-Timeout-Ms
// passed), 500 out_of_memory,
// out_of_memory_loading_logs, exited (the worker died mid-query) or
// internal.
package sqlengine

// Protocol is the version GET /v1/health reports and this package speaks.
const Protocol = 1

// TablesVersion is the version of the table spec in a Request.
const TablesVersion = 1

// Health is the body of GET /v1/health.
type Health struct {
	Protocol int    `json:"protocol"`
	Engine   string `json:"engine"` // "<name> <version>"
}

// Mode says how the engine wraps and answers the SQL.
type Mode string

const (
	ModeRows  Mode = "rows"  // the query's own rows
	ModeText  Mode = "text"  // every cell as text
	ModeShape Mode = "shape" // the result columns only, no rows
	ModePage  Mode = "page"  // one filtered, sorted page with its counts
	ModeCount Mode = "count" // the row counts a page would report
)

// Request is the body of POST /v1/query. The deadline travels in the
// X-Timeout-Ms header and cancellation as connection close, not here.
type Request struct {
	SQL  string            `json:"sql"`
	Args map[string]string `json:"args"` // the $tw_* names, without the dollar sign
	// MaxRows caps the rows of modes rows and text (0: all); in mode page
	// it is the largest page (a Page.Limit of 0 means MaxRows).
	MaxRows int `json:"max_rows"`
	// Mode is how the SQL is wrapped and what the result holds:
	//   - rows: the query's own rows;
	//   - text: the same, every cell as text (NULL as "");
	//   - shape: the query's columns and no rows;
	//   - page: the page's rows as text, its columns the query's with
	//     __tw_matched and __tw_total appended (each with the first free
	//     suffix _1, _2, … when the query has a column of that name; a
	//     distinct page: value,
	//     rows, __tw_matched, __tw_total), both counts on every row; a page
	//     with no row carries no counts;
	//   - count: columns ["total","matched"] and one row, the counts a page
	//     with no row would carry; matched is 0 without running when
	//     Page.Offset is 0, where an empty page means nothing matched.
	Mode Mode `json:"mode"`
	// Page is the page of modes page and count.
	Page    *Page  `json:"page,omitempty"`
	Scope   *int64 `json:"scope"` // a project id, or null for every project
	Confine bool   `json:"confine"`
	Tables  Tables `json:"tables"`
}

// Page is a table's view of its rows: filters combine with AND, Sort is
// nil to keep the query's order, Distinct asks for a column's values and
// their row counts instead of rows.
type Page struct {
	Filters  []Filter `json:"filters"`
	Sort     *Sort    `json:"sort"`
	Distinct string   `json:"distinct"`
	Limit    int      `json:"limit"`
	Offset   int      `json:"offset"`
}

// Filter keeps the rows whose Column satisfies Op against Values. Op is
// "=", "!=", "<", ">", "in" or "not in".
type Filter struct {
	Column string   `json:"column"`
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

// Sort orders a page by one column.
type Sort struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

// Tables names the files the engine reads. Every path is relative to the
// events directory and never absolute; the engine resolves it against its
// own read-only mount and refuses one that escapes it.
type Tables struct {
	Version  int       `json:"version"`
	Projects []Project `json:"projects"`
	Rules    Rules     `json:"rules"`
}

// Project is one project's files.
type Project struct {
	ID          int64    `json:"id"`
	Parquet     []string `json:"parquet"`
	Logs        []string `json:"logs"`
	Sessions    []string `json:"sessions"`
	SessionGaps []Gap    `json:"session_gaps"`
	Identities  string   `json:"identities"` // "" when there is none
	Aliases     string   `json:"aliases"`    // "" when there is none
}

// Gap is a span of days, both ends inclusive (YYYY-MM-DD), that the
// session files do not cover yet.
type Gap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Rules are the event and attribute rules the engine applies when it
// reads the files.
type Rules struct {
	Events     []EventRule     `json:"events"`
	Attributes []AttributeRule `json:"attributes"`
}

// EventRule renames or blocks one event name in one project and family.
type EventRule struct {
	ProjectID int64  `json:"project_id"`
	Family    string `json:"family"`
	Name      string `json:"name"`
	Action    string `json:"action"` // "rename" or "block"
	To        string `json:"to"`     // "" for a block
}

// AttributeRule renames or blocks one attribute key in one project.
type AttributeRule struct {
	ProjectID int64  `json:"project_id"`
	Key       string `json:"key"`
	Action    string `json:"action"` // "rename" or "block"
	To        string `json:"to"`     // "" for a block
}

// Result is a success body. A cell is nil, bool, string or json.Number
// when decoded with UseNumber.
type Result struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
}

// Kind says what went wrong.
type Kind string

const (
	KindRefused               Kind = "refused"                    // the SQL or request is not allowed
	KindSQL                   Kind = "sql"                        // the SQL failed to parse or run
	KindOutOfMemory           Kind = "out_of_memory"              // the query exceeded the memory limit
	KindOutOfMemoryLoadingLog Kind = "out_of_memory_loading_logs" // reading the logs exceeded it
	KindStale                 Kind = "stale"                      // a file in the table spec is gone; send a fresh spec
	KindBusy                  Kind = "busy"                       // no free slot; retry later
	KindTimeout               Kind = "timeout"                    // the X-Timeout-Ms deadline passed
	KindExited                Kind = "exited"                     // the worker running the query died mid-query
	KindInternal              Kind = "internal"                   // the engine failed otherwise
)

// Status is the HTTP status an engine answers k with: 400 for refused and
// sql, 409 for stale, 503 (with Retry-After) for busy, 504 for timeout,
// 500 for the rest.
func (k Kind) Status() int {
	switch k {
	case KindRefused, KindSQL:
		return 400
	case KindStale:
		return 409
	case KindBusy:
		return 503
	case KindTimeout:
		return 504
	}
	return 500
}

// ErrorBody is the body of every non-2xx response.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the error inside an ErrorBody.
type ErrorDetail struct {
	Kind    Kind   `json:"kind"`
	Message string `json:"message"`
}
