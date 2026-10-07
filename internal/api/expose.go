package api

import (
	"context"
	"encoding/csv"
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
	RESTOnly    bool   // set by restOnly and restRaw: a route with no MCP tool
	Multipart   bool   // set by restRaw: the body is multipart/form-data
	Image       bool   // set by restImage: the response is image/png, not JSON
	CSV         bool   // set by restCSV: the response is text/csv, not JSON

	// constrain, when set, tightens the inferred input schema before the
	// tool is registered and the OpenAPI document reads it: what a Go
	// type cannot say, such as the widget contract.
	constrain func(in *jsonschema.Schema)

	in, out *jsonschema.Schema // inferred from the handler's types; the OpenAPI document reads them
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
	s.in, s.out = schemaFor[In](), schemaFor[Out]()
	if s.constrain != nil {
		s.constrain(s.in)
	}
	r.specs = append(r.specs, s)
	mcp.AddTool(r.mcp, &mcp.Tool{Name: s.Name, Description: s.Description, Annotations: s.Annotations,
		InputSchema: s.in, OutputSchema: s.out},
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
	s.in, s.out = schemaFor[In](), schemaFor[Out]()
	if s.constrain != nil {
		s.constrain(s.in)
	}
	r.specs = append(r.specs, s)
	if r.rest != nil {
		r.rest.HandleFunc(s.Method+" "+s.Path, restHandler(r, s, fn))
	}
}

// restRaw registers a REST-only route whose handler reads the request
// itself (a multipart upload). In and Out still describe it, for the
// OpenAPI document and the docs tests.
func restRaw[In, Out any](r *registrar, s spec, h http.HandlerFunc) {
	s.RESTOnly, s.Multipart = true, true
	s.in, s.out = schemaFor[In](), schemaFor[Out]()
	r.specs = append(r.specs, s)
	if r.rest != nil {
		r.rest.HandleFunc(s.Method+" "+s.Path, h)
	}
}

// restImage registers a REST-only GET route that answers a PNG: `fn` gets
// the path wildcards as In, and its bytes are written as image/png, kept
// by the browser (not by a shared cache: the route is authenticated) for
// an hour. A refusal is the usual JSON error.
func restImage[In any](r *registrar, s spec, fn func(context.Context, In) ([]byte, error)) {
	s.RESTOnly, s.Image = true, true
	s.in = schemaFor[In]()
	r.specs = append(r.specs, s)
	if r.rest != nil {
		r.rest.HandleFunc(s.Method+" "+s.Path, func(w http.ResponseWriter, req *http.Request) {
			var in In
			if err := decodeRequest(req, &in); err != nil {
				writeError(w, r.logger, req, err)
				return
			}
			b, err := fn(withActor(req.Context(), "api"), in)
			if err != nil {
				writeError(w, r.logger, req, err)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "private, max-age=3600")
			_, _ = w.Write(b)
		})
	}
}

// csvFile is a restCSV route's answer: the download's file name, the
// header row and the records.
type csvFile struct {
	Name   string
	Header []string
	Rows   [][]string
}

// restCSV registers a REST-only GET route that answers a CSV download:
// `fn` gets the path wildcards and query parameters as In, and its file is
// written as text/csv with a Content-Disposition naming it. A refusal is
// the usual JSON error, sent before any byte of the file.
func restCSV[In any](r *registrar, s spec, fn func(context.Context, In) (csvFile, error)) {
	s.RESTOnly, s.CSV = true, true
	s.in = schemaFor[In]()
	r.specs = append(r.specs, s)
	if r.rest != nil {
		r.rest.HandleFunc(s.Method+" "+s.Path, func(w http.ResponseWriter, req *http.Request) {
			var in In
			if err := decodeRequest(req, &in); err != nil {
				writeError(w, r.logger, req, err)
				return
			}
			f, err := fn(withActor(req.Context(), "api"), in)
			if err != nil {
				writeError(w, r.logger, req, err)
				return
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
			cw := csv.NewWriter(w)
			if err := cw.Write(f.Header); err == nil {
				err = cw.WriteAll(f.Rows)
			}
			if err := cw.Error(); err != nil {
				r.logger.Warn("csv download cut short", "path", req.URL.Path, "error", err)
			}
		})
	}
}

// schemaOptions describes json.RawMessage as a JSON object: inferred from
// its Go type it would be an array of bytes, and the SDK would refuse every
// value passed in or returned. Every RawMessage the tools carry is an
// object (widget props, a component's props schema).
var schemaOptions = &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[json.RawMessage](): {Type: "object"},
	// update_form's closes_at: a time, or null to reopen.
	reflect.TypeFor[optionalTime](): {Types: []string{"string", "null"}},
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
