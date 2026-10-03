package api

import (
	"context"
	"testing"
)

func TestActorContext(t *testing.T) {
	if got := actorFrom(context.Background()); got != "unknown" {
		t.Errorf("unset actor = %q", got)
	}
	if got := actorFrom(withActor(context.Background(), "api")); got != "api" {
		t.Errorf("actor = %q", got)
	}
}

// TestEveryToolChoosesATransport: an operation is on both transports, or
// on the MCP-only or REST-only list, and routes are unique. A new
// operation must decide.
func TestEveryToolChoosesATransport(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	mcpOnly := map[string]bool{"integration_guide": true, "reporting_guide": true}
	restOnly := map[string]bool{"view": true} // the web app's own selection
	seen := map[string]string{}
	tools := 0
	for _, s := range r.specs {
		if s.RESTOnly != restOnly[s.Name] {
			t.Errorf("operation %s: REST-only is %v, the list says %v", s.Name, s.RESTOnly, restOnly[s.Name])
		}
		if !s.RESTOnly {
			tools++
		}
		if s.Method == "" {
			if !mcpOnly[s.Name] {
				t.Errorf("tool %s has no REST route and is not MCP-only", s.Name)
			}
			continue
		}
		if mcpOnly[s.Name] {
			t.Errorf("tool %s is MCP-only but has route %s %s", s.Name, s.Method, s.Path)
		}
		key := s.Method + " " + s.Path
		if other, dup := seen[key]; dup {
			t.Errorf("%s and %s share route %s", s.Name, other, key)
		}
		seen[key] = s.Name
	}
	if tools != 35 {
		t.Errorf("registered %d tools, want 35", tools)
	}
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != tools {
		t.Errorf("MCP lists %d tools, the registrar %d", len(listed.Tools), tools)
	}
	for _, tool := range listed.Tools {
		if restOnly[tool.Name] {
			t.Errorf("REST-only %s is listed as an MCP tool", tool.Name)
		}
	}
}
