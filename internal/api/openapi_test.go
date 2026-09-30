package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type openAPIDoc struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		License struct {
			Name       string `json:"name"`
			Identifier string `json:"identifier"`
		} `json:"license"`
	} `json:"info"`
	Paths map[string]map[string]struct {
		OperationID string `json:"operationId"`
		Parameters  []struct {
			Name     string          `json:"name"`
			In       string          `json:"in"`
			Required bool            `json:"required"`
			Schema   json.RawMessage `json:"schema"`
		} `json:"parameters"`
		RequestBody *struct {
			Content map[string]struct {
				Schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
		Responses map[string]json.RawMessage `json:"responses"`
	} `json:"paths"`
}

// TestOpenAPIDescribesEveryRoute binds the document to the registered
// routes: each is there under its method with its tool name and success
// status, every path wildcard is a required path parameter, a GET reads
// the rest of its input from the query and nothing else does, and every
// $ref resolves inside the document.
func TestOpenAPIDescribesEveryRoute(t *testing.T) {
	h, _ := newTestHost(t)
	specs := newTestRegistrar(t, h).specs
	raw, err := openAPI(specs)
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q", doc.OpenAPI)
	}
	if l := doc.Info.License; l.Identifier != "AGPL-3.0-only" || l.Name == "" {
		t.Errorf("info.license = %+v", l)
	}
	routes := 0
	for _, s := range specs {
		if s.Method == "" {
			continue
		}
		routes++
		op, ok := doc.Paths[s.Path][strings.ToLower(s.Method)]
		if !ok {
			t.Errorf("%s %s is registered but not in the document", s.Method, s.Path)
			continue
		}
		if op.OperationID != s.Name {
			t.Errorf("%s %s: operationId %q, want %q", s.Method, s.Path, op.OperationID, s.Name)
		}
		status := "200"
		if s.Status != 0 {
			status = strconv.Itoa(s.Status)
		}
		if _, ok := op.Responses[status]; !ok {
			t.Errorf("%s %s: no %s response", s.Method, s.Path, status)
		}
		want := map[string]bool{}
		for _, m := range pathParam.FindAllStringSubmatch(s.Path, -1) {
			want[m[1]] = true
		}
		for _, p := range op.Parameters {
			switch {
			case p.In == "path" && want[p.Name] && p.Required:
				delete(want, p.Name)
			case p.In == "query" && s.Method == http.MethodGet:
			default:
				t.Errorf("%s %s: unexpected parameter %+v", s.Method, s.Path, p)
			}
		}
		if len(want) > 0 {
			t.Errorf("%s %s: path parameters %v undeclared", s.Method, s.Path, want)
		}
		if op.RequestBody != nil && s.Method == http.MethodGet {
			t.Errorf("GET %s has a request body", s.Path)
		}
	}
	if n := len(doc.Paths["/api/schema/views"]); n != 1 {
		t.Errorf("/api/schema/views: %d operations", n)
	}
	if routes < 30 {
		t.Errorf("only %d routes: is the registrar wired?", routes)
	}
	for _, ref := range regexp.MustCompile(`"\$ref":\s*"([^"]*)"`).FindAllStringSubmatch(string(raw), -1) {
		if ref[1] != "#/components/schemas/Error" {
			t.Errorf("$ref %q does not resolve in the document", ref[1])
		}
	}
}

// A PATCH's body carries its fields, not the path's, and a field named by
// a wildcard is never read from the body.
func TestOpenAPISplitsPathFromBody(t *testing.T) {
	h, _ := newTestHost(t)
	raw, err := openAPI(newTestRegistrar(t, h).specs)
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	op := doc.Paths["/api/projects/{project_id}"]["patch"]
	if op.RequestBody == nil {
		t.Fatal("update_project has no request body")
	}
	props := op.RequestBody.Content["application/json"].Schema.Properties
	if _, ok := props["project_id"]; ok {
		t.Error("project_id is in the body as well as the path")
	}
	if _, ok := props["name"]; !ok {
		t.Errorf("body properties %v lack name", props)
	}
	if op := doc.Paths["/api/projects/{project_id}/archive"]["post"]; op.RequestBody != nil {
		t.Error("archive_project takes only the path, yet has a request body")
	}
}

func TestOpenAPIRefusesUndeclaredWildcard(t *testing.T) {
	h, _ := newTestHost(t)
	specs := newTestRegistrar(t, h).specs
	for i := range specs {
		if specs[i].Path == "/api/projects/{project_id}/retention" {
			specs[i].Path = "/api/projects/{project}/retention"
		}
	}
	if _, err := openAPI(specs); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("err = %v, want the wildcard named", err)
	}
}

// The document and its Swagger UI page answer without a token; everything
// else under /api/ still needs one.
func TestOpenAPIServedUnauthenticated(t *testing.T) {
	h := newHandlerFixture(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/openapi.json", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("openapi.json: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var doc openAPIDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc.Paths["/api/projects"] == nil {
		t.Fatalf("openapi.json body: %v", err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/docs", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>Twillingate API</title>") {
		t.Fatalf("/api/docs: %d %.200s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/openapi.json", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/openapi.json without a token: %d", rec.Code)
	}
}

func TestSummaryOf(t *testing.T) {
	for in, want := range map[string]string{
		"List projects. Call this first.":                        "List projects.",
		"Call reporting_guide first. Create a user dashboard. x": "Create a user dashboard.",
		"Unhide an archived widget, in its old place.":           "Unhide an archived widget, in its old place.",
	} {
		if got := summaryOf(in); got != want {
			t.Errorf("summaryOf(%q) = %q, want %q", in, got, want)
		}
	}
}
