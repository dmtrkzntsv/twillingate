package reporting

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// columnTypes is the closed set of types a component's input column may
// declare (types.go's Column.Types), matched against fitsType.
var columnTypes = map[string]bool{"number": true, "text": true, "day": true}

// manifest is the shape the UI build writes to components.json: one
// entry per React component it ships.
type manifest struct {
	Components []manifestComponent `json:"components"`
}

// manifestComponent mirrors Component field for field; kept distinct so
// Component's unexported schema never has to round-trip through JSON.
type manifestComponent struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Accepts       []string        `json:"accepts"`
	Inputs        Inputs          `json:"inputs"`
	Props         json.RawMessage `json:"props"`
	DefaultWidth  int             `json:"default_width"`
	DefaultHeight int             `json:"default_height"`
}

// ParseManifest reads the UI's component manifest ({"components": [...]})
// and resolves every entry's props schema up front, so a later widget
// check never has to parse JSON Schema on the request path. It refuses an
// entry whose accepts names a source type this build does not register,
// whose default size falls outside the grid, whose props is not itself
// a valid JSON Schema object, whose input column names a type other than
// number/text/day, or whose name repeats an earlier entry's (components
// is a name-keyed table; a second entry silently overwriting the first
// would leave whichever the loop reached last, unnoticed).
func ParseManifest(b []byte) ([]Component, error) {
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("reporting: parse component manifest: %w", err)
	}
	seen := make(map[string]bool, len(m.Components))
	out := make([]Component, 0, len(m.Components))
	for _, mc := range m.Components {
		if seen[mc.Name] {
			return nil, store.Refuse(store.ErrInvalid, "%s: a component with this name is already in the manifest", mc.Name)
		}
		seen[mc.Name] = true
		for _, a := range mc.Accepts {
			if !validSourceType(a) {
				return nil, store.Refuse(store.ErrInvalid,
					"%s: accepts names %s, which is not a registered source type", mc.Name, a)
			}
		}
		for _, col := range mc.Inputs.Columns {
			for _, t := range col.Types {
				if !columnTypes[t] {
					return nil, store.Refuse(store.ErrInvalid,
						"%s: column %s names %s, which is not number, text or day", mc.Name, col.Name, t)
				}
			}
		}
		if err := checkSize(mc.DefaultWidth, mc.DefaultHeight); err != nil {
			return nil, store.Refuse(store.ErrInvalid, "%s: %s", mc.Name, err)
		}
		resolved, err := resolveSchema(mc.Props)
		if err != nil {
			return nil, store.Refuse(store.ErrInvalid, "%s: props is not a JSON schema: %s", mc.Name, err)
		}
		out = append(out, Component{
			Name:          mc.Name,
			Description:   mc.Description,
			Accepts:       mc.Accepts,
			Inputs:        mc.Inputs,
			Props:         mc.Props,
			DefaultWidth:  mc.DefaultWidth,
			DefaultHeight: mc.DefaultHeight,
			schema:        resolved,
		})
	}
	return out, nil
}

// resolveSchema unmarshals props as a JSON Schema and resolves it, ready
// for Component.checkProps to validate an instance against.
func resolveSchema(props json.RawMessage) (*jsonschema.Resolved, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(props, &schema); err != nil {
		return nil, err
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

// fromRow converts a components row (accepts/inputs stored as JSON text)
// into the typed, schema-resolved shape the rest of the package works
// with.
func fromRow(r store.Component) (Component, error) {
	var accepts []string
	if err := json.Unmarshal([]byte(r.Accepts), &accepts); err != nil {
		return Component{}, fmt.Errorf("reporting: component %s: accepts: %w", r.Name, err)
	}
	var inputs Inputs
	if err := json.Unmarshal([]byte(r.Inputs), &inputs); err != nil {
		return Component{}, fmt.Errorf("reporting: component %s: inputs: %w", r.Name, err)
	}
	resolved, err := resolveSchema(json.RawMessage(r.Props))
	if err != nil {
		return Component{}, fmt.Errorf("reporting: component %s: props: %w", r.Name, err)
	}
	return Component{
		Name:          r.Name,
		Description:   r.Description,
		Accepts:       accepts,
		Inputs:        inputs,
		Props:         json.RawMessage(r.Props),
		DefaultWidth:  r.DefaultWidth,
		DefaultHeight: r.DefaultHeight,
		schema:        resolved,
	}, nil
}

// row converts c back to the store's JSON-text shape, for SyncReporting
// and InsertWidget's component-side writes.
func (c Component) row() store.Component {
	accepts, _ := json.Marshal(c.Accepts) // c.Accepts always marshals: []string
	inputs, _ := json.Marshal(c.Inputs)   // c.Inputs always marshals: plain struct
	return store.Component{
		Name:          c.Name,
		Description:   c.Description,
		Accepts:       string(accepts),
		Inputs:        string(inputs),
		Props:         string(c.Props),
		DefaultWidth:  c.DefaultWidth,
		DefaultHeight: c.DefaultHeight,
	}
}

// checkProps validates a widget's props against c's resolved schema.
// rs.Validate already reports which property failed (its path includes
// the property name) and additionalProperties:false already names an
// unknown key, so the schema's own error text is what a caller sees for
// those; null and anything that isn't a JSON object (an array, a string,
// a bare number) are refused up front with a message of our own, since
// jsonschema-go's own "type: ... has type ..., want object" is not what
// a caller should have to parse and reflect.ValueOf(nil) would otherwise
// reach validate as an invalid Value.
func (c Component) checkProps(props json.RawMessage) error {
	var v any
	if err := json.Unmarshal(props, &v); err != nil {
		return store.Refuse(store.ErrInvalid, "%s: props must be a JSON object: %s", c.Name, err)
	}
	if _, ok := v.(map[string]any); !ok {
		return store.Refuse(store.ErrInvalid, "%s: props must be a JSON object", c.Name)
	}
	if err := c.schema.Validate(v); err != nil {
		return store.Refuse(store.ErrInvalid, "%s: %s", c.Name, err)
	}
	return nil
}

// checkColumns checks a query's column names against c's declared
// inputs: every required (non-optional) column must be present, and —
// unless c.Inputs.Open (table draws whatever it is given) — no column
// outside the declared set may appear either.
func (c Component) checkColumns(cols []string) error {
	if c.Inputs.Open {
		return nil
	}
	have := make(map[string]bool, len(cols))
	for _, col := range cols {
		have[col] = true
	}
	for _, decl := range c.Inputs.Columns {
		if decl.Optional {
			continue
		}
		if !have[decl.Name] {
			return store.Refuse(store.ErrInvalid, "%s needs %s (%s); columns are %s",
				c.Name, decl.Name, strings.Join(decl.Types, " or "), strings.Join(cols, ", "))
		}
	}
	declared := make(map[string]bool, len(c.Inputs.Columns))
	for _, decl := range c.Inputs.Columns {
		declared[decl.Name] = true
	}
	for _, col := range cols {
		if !declared[col] {
			return store.Refuse(store.ErrInvalid, "%s is not an input of %s", col, c.Name)
		}
	}
	return nil
}

// checkRows checks that every row's value in each of c's declared
// columns fits at least one of that column's types; an empty string
// (SQL NULL, per readsql.Result) always fits. Columns res does not
// carry (an optional column the query left out) are skipped.
func (c Component) checkRows(res readsql.Result) error {
	idx := make(map[string]int, len(res.Columns))
	for i, name := range res.Columns {
		idx[name] = i
	}
	for _, col := range c.Inputs.Columns {
		i, ok := idx[col.Name]
		if !ok {
			continue
		}
		for _, row := range res.Rows {
			v := row[i]
			if v == "" {
				continue
			}
			if !fitsAnyType(v, col.Types) {
				return store.Refuse(store.ErrInvalid, "%s.%s: %q is not %s",
					c.Name, col.Name, v, strings.Join(col.Types, " or "))
			}
		}
	}
	return nil
}

// fitsAnyType reports whether v is a valid value of at least one of types.
func fitsAnyType(v string, types []string) bool {
	for _, t := range types {
		if fitsType(v, t) {
			return true
		}
	}
	return false
}

// fitsType reports whether v is a valid value of the single type t.
func fitsType(v, t string) bool {
	switch t {
	case "number":
		// strconv.ParseFloat also accepts "nan"/"inf"/"infinity" (any
		// case, with an optional sign) — valid float text, but not a
		// value a number column's reader (a chart, a stat tile) can do
		// anything with, so those are refused too.
		f, err := strconv.ParseFloat(v, 64)
		return err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case "day":
		_, err := time.Parse("2006-01-02", v)
		return err == nil
	case "text":
		return true
	default:
		return false
	}
}
