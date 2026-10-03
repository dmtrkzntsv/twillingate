package reporting

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// rawAsObject infers json.RawMessage as an object, as the api package's
// schemaOptions does; inferred from its Go type it is an array of bytes.
var rawAsObject = &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[json.RawMessage](): {Type: "object"},
}}

// updateShape mirrors update_widget's input: every field optional and
// nullable, so the schema cannot know the component when it is omitted.
type updateShape struct {
	Component *string         `json:"component,omitempty"`
	Props     json.RawMessage `json:"props,omitempty"`
	Source    *Source         `json:"source,omitempty"`
	Width     *int            `json:"width,omitempty"`
	Height    *int            `json:"height,omitempty"`
}

// constrained infers T's schema, tightens it with testdata's components
// and resolves it.
func constrained[T any](t *testing.T) *jsonschema.Resolved {
	t.Helper()
	b, err := os.ReadFile("testdata/components.json")
	if err != nil {
		t.Fatal(err)
	}
	comps, err := ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jsonschema.For[T](rawAsObject)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConstrainWidget(s, comps, []string{"md", "sql"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func validate(t *testing.T, r *jsonschema.Resolved, in string) error {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatal(err)
	}
	return r.Validate(v)
}

func TestConstrainWidgetSpec(t *testing.T) {
	r := constrained[WidgetSpec](t)
	const sql = `"source":{"type":"sql","content":"SELECT 1 AS value"}`
	const md = `"source":{"type":"md","content":"# Hi"}`
	for _, in := range []string{
		`{"component":"stat",` + sql + `}`,
		`{"component":"stat","props":{"format":"percent","aggregate":"avg"},` + sql + `,"width":12,"height":1}`,
		`{"component":"table","props":{"formats":{"rate":"percent"},"mode":"remote"},` + sql + `}`,
		`{"component":"markdown",` + md + `}`,
		`{"component":"markdown","props":{},` + md + `}`,
	} {
		if err := validate(t, r, in); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
	for _, c := range []struct{ in, want string }{
		{`{"component":"nope",` + sql + `}`, "nope"},
		{`{"component":"stat","props":{"curve":"step"},` + sql + `}`, "curve"},
		{`{"component":"stat","props":{"format":"pct"},` + sql + `}`, "pct"},
		{`{"component":"table","props":{"formats":{"rate":"pct"}},` + sql + `}`, "pct"},
		{`{"component":"markdown",` + sql + `}`, "sql"},
		{`{"component":"stat",` + md + `}`, "md"},
		{`{"component":"stat","source":{"type":"csv","content":"x"}}`, "csv"},
		{`{"component":"stat",` + sql + `,"width":13}`, "width"},
		{`{"component":"stat",` + sql + `,"width":0}`, "width"},
		{`{"component":"stat",` + sql + `,"height":0}`, "height"},
	} {
		err := validate(t, r, c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", c.in, err, c.want)
		}
	}
}

// TestConstrainWidgetUpdate: without a component the schema cannot pick
// the props schema, so any props object passes and the server checks it.
func TestConstrainWidgetUpdate(t *testing.T) {
	r := constrained[updateShape](t)
	for _, in := range []string{
		`{}`,
		`{"props":{"anything":1}}`,
		`{"component":null,"source":null,"width":null,"height":null}`,
		`{"component":"line","props":{"curve":"step"}}`,
	} {
		if err := validate(t, r, in); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
	for _, c := range []struct{ in, want string }{
		{`{"component":"stat","props":{"curve":"step"}}`, "curve"},
		{`{"component":"nope"}`, "nope"},
		{`{"width":13}`, "width"},
	} {
		err := validate(t, r, c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", c.in, err, c.want)
		}
	}
}

func TestConstrainWidgetRefusesOtherShapes(t *testing.T) {
	s, err := jsonschema.For[Source](nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConstrainWidget(s, nil, []string{"sql"}); err == nil {
		t.Error("a schema without component, props, width and height was tightened")
	}
}
