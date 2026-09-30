package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/version"
	"github.com/google/jsonschema-go/jsonschema"
)

// The OpenAPI document's path, and that of the Swagger UI page reading it
// (built with the dashboards app, served by reporting.APIDocs).
const (
	openAPIPath = "/api/openapi.json"
	docsPath    = "/api/docs"
)

// pathParam matches a ServeMux wildcard; OpenAPI writes path parameters the
// same way.
var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

// openAPI renders the REST routes as an OpenAPI 3.1 document, from the same
// specs and inferred schemas that register them, so it cannot drift from
// what the routes accept. 3.1 because its schemas are JSON Schema 2020-12,
// which is what jsonschema.For infers: they go in unchanged. A GET's input
// becomes query parameters and any other method's a JSON body, as
// decodeRequest reads them; a field named by a path wildcard is a path
// parameter on both.
func openAPI(specs []spec) ([]byte, error) {
	paths := map[string]map[string]any{}
	for _, s := range specs {
		if s.Method == "" {
			continue
		}
		op, err := operation(s)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", s.Method, s.Path, err)
		}
		if paths[s.Path] == nil {
			paths[s.Path] = map[string]any{}
		}
		paths[s.Path][strings.ToLower(s.Method)] = op
	}
	paths["/api/schema/views"] = map[string]any{"get": map[string]any{
		"operationId": "schema_views",
		"tags":        []string{"schema"},
		"summary":     "The views' column reference: schema://views as plain text.",
		"responses": map[string]any{
			"200":     map[string]any{"description": "OK", "content": map[string]any{"text/plain": map[string]any{"schema": map[string]any{"type": "string"}}}},
			"401":     unauthorized,
			"default": errorResponse,
		},
	}}
	return json.MarshalIndent(map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "Twillingate API",
			"version":     version.Version,
			"license":     map[string]string{"name": "GNU Affero General Public License v3.0 only", "identifier": "AGPL-3.0-only"},
			"description": "Every route but PUT /api/dashboards/{dashboard_id}/view mirrors the MCP tool its operationId names. Reference: docs://twillingate and docs://reporting.",
		},
		// Relative: the routes are wherever this document was fetched from.
		"servers":  []map[string]string{{"url": "/"}},
		"security": []map[string][]string{{"bearer": {}}},
		"components": map[string]any{
			"securitySchemes": map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer"}},
			"schemas": map[string]any{"Error": map[string]any{
				"type":     "object",
				"required": []string{"error"},
				"properties": map[string]any{"error": map[string]any{
					"type":     "object",
					"required": []string{"code", "message"},
					"properties": map[string]any{
						"code":    map[string]any{"type": "string", "enum": []string{"invalid", "not_found", "conflict", "internal"}},
						"message": map[string]any{"type": "string"},
					},
				}},
			}},
		},
		"paths": paths,
	}, "", "  ")
}

var (
	errorResponse = map[string]any{
		"description": "Refused: invalid is 400, not_found 404, conflict 409, internal 500.",
		"content":     map[string]any{"application/json": map[string]any{"schema": map[string]string{"$ref": "#/components/schemas/Error"}}},
	}
	unauthorized = map[string]any{"description": "Missing or bad bearer token."}
)

func operation(s spec) (map[string]any, error) {
	status := s.Status
	if status == 0 {
		status = http.StatusOK
	}
	op := map[string]any{
		"operationId": s.Name,
		"tags":        []string{tagOf(s.Path)},
		"summary":     summaryOf(s.Description),
		"description": s.Description,
		"responses": map[string]any{
			strconv.Itoa(status): map[string]any{"description": http.StatusText(status),
				"content": map[string]any{"application/json": map[string]any{"schema": s.out}}},
			"401":     unauthorized,
			"default": errorResponse,
		},
	}
	in := s.in
	if in.Type != "object" {
		return nil, fmt.Errorf("input schema is %q, not an object", in.Type)
	}
	inPath := map[string]bool{}
	for _, m := range pathParam.FindAllStringSubmatch(s.Path, -1) {
		inPath[m[1]] = true
	}
	var params []map[string]any
	body := *in
	body.Properties = map[string]*jsonschema.Schema{}
	body.Required = nil
	for _, name := range slices.Sorted(maps.Keys(in.Properties)) {
		prop := in.Properties[name]
		switch {
		case inPath[name]:
			params = append(params, parameter(name, "path", true, prop))
			delete(inPath, name)
		case s.Method == http.MethodGet:
			params = append(params, parameter(name, "query", slices.Contains(in.Required, name), prop))
		default:
			body.Properties[name] = prop
			if slices.Contains(in.Required, name) {
				body.Required = append(body.Required, name)
			}
		}
	}
	if len(inPath) > 0 {
		return nil, fmt.Errorf("path wildcards %v name no input field", slices.Sorted(maps.Keys(inPath)))
	}
	if params != nil {
		op["parameters"] = params
	}
	if len(body.Properties) > 0 {
		op["requestBody"] = map[string]any{
			"required": len(body.Required) > 0,
			"content":  map[string]any{"application/json": map[string]any{"schema": &body}},
		}
	}
	return op, nil
}

// parameter describes one input field read from the URL. The description
// moves up to the parameter, where Swagger UI shows it.
func parameter(name, in string, required bool, prop *jsonschema.Schema) map[string]any {
	schema := *prop
	schema.Description = ""
	p := map[string]any{"name": name, "in": in, "required": required, "schema": &schema}
	if prop.Description != "" {
		p["description"] = prop.Description
	}
	return p
}

// summaryOf is a description's first sentence, the line Swagger UI shows
// beside a collapsed route, past a leading "Call reporting_guide first.":
// that is for agents, and says nothing of what the route does.
func summaryOf(description string) string {
	first, rest, ok := strings.Cut(description, ". ")
	if ok && strings.HasPrefix(first, "Call ") {
		return summaryOf(rest)
	}
	if ok {
		return first + "."
	}
	return description
}

// tagOf groups a route by the collection it is under: /api/projects/1/keys
// is "keys", /api/projects/1/archive is "projects".
func tagOf(path string) string {
	segs := strings.Split(strings.TrimPrefix(path, "/api/"), "/")
	if len(segs) > 2 {
		switch segs[2] {
		case "keys", "widgets":
			return segs[2]
		case "views", "product", "retention", "identities":
			return "analytics"
		}
	}
	return segs[0]
}
