package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// spec describes one operation on both transports. Method "" keeps it
// MCP-only; the parity test makes that an explicit choice.
type spec struct {
	Name        string // MCP tool name
	Description string
	Annotations *mcp.ToolAnnotations
	Method      string // HTTP method; "" = MCP only
	Path        string // ServeMux pattern path, e.g. "/api/projects/{project}/web/overview"
	Status      int    // REST success status; 0 = 200
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
	mcp.AddTool(r.mcp, &mcp.Tool{Name: s.Name, Description: s.Description, Annotations: s.Annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			out, err := fn(withActor(ctx, "mcp"), in)
			return nil, out, err
		})
	if s.Method != "" && r.rest != nil {
		r.rest.HandleFunc(s.Method+" "+s.Path, restHandler(r, s, fn))
	}
}
