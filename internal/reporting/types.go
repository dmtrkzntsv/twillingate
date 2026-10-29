package reporting

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// Source is a widget's content: what kind of source produced it, and the
// text (SQL or Markdown) that produced it. It is the edge shape the API
// carries in and out — the type name a widget declares, paired with the
// text a person or an agent wrote.
type Source struct {
	Type    string `json:"type" jsonschema:"query (a structured query; prefer it), sql (the escape hatch) or md"`
	Content string `json:"content" jsonschema:"the structured query as JSON ({query, rename, labels, columns}; query is the query tool's query argument), the SQL query, or the Markdown text"`
}

// UnmarshalJSON reads content as text or, for a query source, as the
// JSON object itself, kept as its compact text: an agent writes the
// structured query as the query tool takes it, not as a string of JSON.
func (s *Source) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // as the field-by-field decode of the callers that ask for it
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	s.Type, s.Content = raw.Type, ""
	c := bytes.TrimSpace(raw.Content)
	switch {
	case len(c) == 0 || bytes.Equal(c, []byte("null")):
	case c[0] == '{':
		if raw.Type != "query" {
			return fmt.Errorf("source.content: an object is a query source's content; a %s source's is text", raw.Type)
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, c); err != nil {
			return err
		}
		s.Content = buf.String()
	default:
		if err := json.Unmarshal(c, &s.Content); err != nil {
			return fmt.Errorf("source.content: text, or a query source's JSON object: %w", err)
		}
	}
	return nil
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
