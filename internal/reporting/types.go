package reporting

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// Source is a widget's content: what kind of source produced it, and the
// text (SQL or Markdown) that produced it. It is the edge shape the API
// carries in and out — the type name a widget declares, paired with the
// text a person or an agent wrote.
type Source struct {
	Type    string `json:"type" jsonschema:"sql or md"`
	Content string `json:"content" jsonschema:"the SQL query, or the Markdown text"`
}

// Column is one column a component's input accepts: a name, the types a
// value in it may take (any of number, text, day), and whether the column
// may be absent from the query's result set.
type Column struct {
	Name     string   `json:"name"`
	Types    []string `json:"types"` // any of number, text, day
	Optional bool     `json:"optional,omitempty"`
}

// Inputs is what a component reads from its rows: either a fixed,
// declared set of columns (Open false), or — for a component like table
// that draws whatever a query returns — none declared at all (Open true).
type Inputs struct {
	Open    bool     `json:"open"` // any columns (table)
	Columns []Column `json:"columns"`
}

// Component is a row of components, shaped the way the API returns it: a
// React component's contract (which source types it accepts, the columns
// its rows must carry, the JSON schema its props must satisfy) plus the
// resolved form of that schema, built once and reused by every widget
// that names this component rather than re-parsed on every check.
type Component struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Accepts       []string        `json:"accepts"`
	Inputs        Inputs          `json:"inputs"`
	Props         json.RawMessage `json:"props"`
	DefaultWidth  int             `json:"default_width"`
	DefaultHeight int             `json:"default_height"`

	schema *jsonschema.Resolved
}
