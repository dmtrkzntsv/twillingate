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

// TestEveryToolChoosesATransport: a tool either has a REST route or is on
// the MCP-only list, and routes are unique. A new tool must decide.
func TestEveryToolChoosesATransport(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	mcpOnly := map[string]bool{"integration_guide": true}
	seen := map[string]string{}
	for _, s := range r.specs {
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
	if len(r.specs) != 17 {
		t.Errorf("registered %d tools, want 17", len(r.specs))
	}
}
