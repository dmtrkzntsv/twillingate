package reporting

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// ConstrainWidget tightens widget, an inferred schema for a widget's
// fields (WidgetSpec, or update_widget's input with every field
// optional), so a client sees the widget contract before calling:
// component is one of comps' names, source.type one of sourceTypes,
// width and height 1–12, and one allOf rule per component that, when
// component names it, holds props to its props schema and source.type to
// the types it accepts. Without component (update_widget keeping the
// stored one) props stays any object, and the server checks it as
// before. Props schemas are inlined rather than put in $defs: the OpenAPI
// document copies this schema into a request body, where a "#/$defs/..."
// reference would resolve against the document, not the schema. A nil
// widget (a caller whose property lookup came back empty) is an error, not
// a panic.
func ConstrainWidget(widget *jsonschema.Schema, comps []Component, sourceTypes []string) error {
	if widget == nil {
		return fmt.Errorf("reporting: no widget schema")
	}
	props := widget.Properties
	for _, name := range []string{"component", "props", "source", "width", "height"} {
		if props[name] == nil {
			return fmt.Errorf("reporting: widget schema has no %s property", name)
		}
	}
	srcType := props["source"].Properties["type"]
	if srcType == nil {
		return fmt.Errorf("reporting: widget schema's source has no type property")
	}

	names := make([]any, 0, len(comps)+1)
	for _, c := range comps {
		names = append(names, c.Name)
	}
	// A nullable component (update_widget's *string) must still accept
	// null: enum applies whatever the type says.
	if slices.Contains(props["component"].Types, "null") {
		names = append(names, nil)
	}
	props["component"].Enum = names
	srcType.Enum = anys(sourceTypes)
	one, twelve := 1.0, 12.0
	for _, name := range []string{"width", "height"} {
		props[name].Minimum, props[name].Maximum = &one, &twelve
	}

	for _, c := range comps {
		var ps jsonschema.Schema
		if err := json.Unmarshal(c.Props, &ps); err != nil {
			return fmt.Errorf("reporting: component %s: props: %w", c.Name, err)
		}
		name := any(c.Name)
		widget.AllOf = append(widget.AllOf, &jsonschema.Schema{
			If: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"component": {Const: &name}},
				Required:   []string{"component"},
			},
			Then: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{
				"props":  &ps,
				"source": {Properties: map[string]*jsonschema.Schema{"type": {Enum: anys(c.Accepts)}}},
			}},
		})
	}
	return nil
}

// anys converts ss to the []any a schema's enum holds.
func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
