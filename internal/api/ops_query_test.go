package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// source "sqlite" was removed with the database's events: refused as
// invalid, over MCP and REST, before anything is read.
func TestQuerySourceSQLiteIsRefused(t *testing.T) {
	h, cs := newTestHost(t)
	h.files = &fakeFiles{err: errors.New("must not be called")}
	const want = `source "sqlite" was removed: the database no longer holds events; query the event files (docs://twillingate)`
	res := callTool(t, cs, "query", map[string]any{"sql": "SELECT 1", "source": "sqlite"})
	if !res.IsError || !strings.Contains(textOf(res), want) {
		t.Errorf("MCP source sqlite = %s, want refused with %q", textOf(res), want)
	}
	if _, err := h.runQuery(t.Context(), queryIn{SQL: "SELECT 1", Source: "sqlite"}); !errors.Is(err, manage.ErrInvalid) {
		t.Errorf("err = %v, want invalid", err)
	}
	rec := serveREST(t, newTestRegistrar(t, h), "POST", "/api/query", `{"sql":"SELECT 1","source":"sqlite"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "docs://twillingate") {
		t.Errorf("REST source sqlite = %d %s, want 400", rec.Code, rec.Body)
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
