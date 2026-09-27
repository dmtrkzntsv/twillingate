package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// spec describes one operation on both transports. Method "" keeps it
// MCP-only and RESTOnly keeps it off MCP; the parity test makes either an
// explicit choice.
type spec struct {
	Name        string // MCP tool name
	Description string
	Annotations *mcp.ToolAnnotations
	Method      string // HTTP method; "" = MCP only
	Path        string // ServeMux pattern path, e.g. "/api/projects/{project_id}/views/overview"
	Status      int    // REST success status; 0 = 200
	RESTOnly    bool   // set by restOnly: a route with no MCP tool
}

// registrar collects the operations for both transports. specs is what
// the parity and docs tests enumerate.
type registrar struct {
	mcp    *mcp.Server
	rest   *http.ServeMux // nil registers MCP tools only
	logger *slog.Logger
	specs  []spec
}

type actorKey struct{}

// withActor names the edge an operation arrived through; management
// operations record it in audit_log.
func withActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// actorFrom returns the edge set by withActor, or "unknown" when unset.
func actorFrom(ctx context.Context) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok {
		return a
	}
	return "unknown"
}

// expose registers fn as an MCP tool and, when s.Method is set, as a REST
// route. One call per operation, so neither transport can drift.
func expose[In, Out any](r *registrar, s spec, fn func(context.Context, In) (Out, error)) {
	r.specs = append(r.specs, s)
	mcp.AddTool(r.mcp, &mcp.Tool{Name: s.Name, Description: s.Description, Annotations: s.Annotations,
		InputSchema: schemaFor[In](), OutputSchema: schemaFor[Out]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			out, err := fn(withActor(ctx, "mcp"), in)
			return nil, out, err
		})
	if s.Method != "" && r.rest != nil {
		r.rest.HandleFunc(s.Method+" "+s.Path, restHandler(r, s, fn))
	}
}

// restOnly registers fn as a REST route with no MCP tool: state only the
// web app writes, such as a dashboard's remembered selection.
func restOnly[In, Out any](r *registrar, s spec, fn func(context.Context, In) (Out, error)) {
	s.RESTOnly = true
	r.specs = append(r.specs, s)
	if r.rest != nil {
		r.rest.HandleFunc(s.Method+" "+s.Path, restHandler(r, s, fn))
	}
}

// schemaOptions describes json.RawMessage as a JSON object: inferred from
// its Go type it would be an array of bytes, and the SDK would refuse every
// value passed in or returned. Every RawMessage the tools carry is an
// object (widget props, a component's props schema).
var schemaOptions = &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[json.RawMessage](): {Type: "object"},
}}

// schemaFor infers T's schema with schemaOptions. A type it cannot
// describe is a programming error, found at startup the way
// mcp.AddTool's own inference would.
func schemaFor[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](schemaOptions)
	if err != nil {
		panic(fmt.Sprintf("api: schema for %T: %v", *new(T), err))
	}
	return s
}
