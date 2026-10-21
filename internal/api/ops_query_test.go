package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// source was removed one release after the database's events: it is an
// unknown field, refused over MCP and REST before anything is read, "files"
// included.
func TestQuerySourceIsRefused(t *testing.T) {
	h, cs := newTestHost(t)
	h.files = &fakeFiles{err: errors.New("must not be called")}
	for _, source := range []string{"files", "sqlite"} {
		if res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1", "source": source}); !res.IsError ||
			strings.Contains(textOf(res), "must not be called") {
			t.Errorf("MCP source %s = %s, want refused before the query", source, textOf(res))
		}
		rec := serveREST(t, newTestRegistrar(t, h), "POST", "/api/query", `{"sql":"SELECT 1","source":"`+source+`"}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `unknown field \"source\"`) {
			t.Errorf("REST source %s = %d %s, want 400 for an unknown field", source, rec.Code, rec.Body)
		}
	}
}

func TestQueryToolCapsRows(t *testing.T) {
	h, cs := newTestHost(t)
	setGuards(t, h, h.queryTimeout, 1)
	res := callTool(t, cs, "query", map[string]any{"sql": "SELECT * FROM range(3)"})
	if res.IsError {
		t.Fatalf("files: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "truncated") || !strings.Contains(out, "PARTIAL") {
		t.Errorf("files: truncation not flagged: %s", out)
	}
}
