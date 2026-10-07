package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryToolSelects(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "query", map[string]any{
		"sql": "SELECT project_id, day, visitors FROM v_views_daily WHERE project_id=1 ORDER BY day"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "2026-08-20") {
		t.Errorf("missing data: %s", out)
	}
}

func TestQueryToolBlocksWrites(t *testing.T) {
	_, cs := newTestHost(t)
	// A target for the VACUUM INTO escape below: if the wrap's paren
	// guard ever regresses, a file appears here.
	vacuumTarget := filepath.Join(t.TempDir(), "escaped-copy.db")
	for _, q := range []string{
		"DELETE FROM web_hits",
		// projects is a real, readable table (other tools read it): this
		// proves the read-only connection itself refuses the write, not
		// just that Check recognizes a refused name.
		"INSERT INTO projects (name) VALUES ('pwned')",
		"UPDATE projects SET name='pwned'",
		"PRAGMA journal_mode=DELETE",
		"PRAGMA query_only=0",
		"SELECT 1; DELETE FROM web_hits",
		"ATTACH DATABASE '/etc/passwd' AS pwn",
		"attach database '/tmp/x' as pwn",
		"WITH x AS (SELECT 1) ATTACH DATABASE '/tmp/x' AS pwn",
		// The three below escape the subquery wrap itself: a stray ')'
		// with no matching '(' earlier in the text closes the wrapper's
		// own paren early, so the driver — which runs every statement it
		// is handed — executes whatever follows as a separate statement.
		"select 1); select 2; select * from (select 3",
		fmt.Sprintf("select 1 where 0); PRAGMA query_only=0; VACUUM INTO '%s'; select * from (select 1", vacuumTarget),
		"select 1); CREATE TEMP VIEW v_retention AS SELECT 'poisoned' AS cohort_day; select * from (select 1",
	} {
		res := callTool(t, cs, "query", map[string]any{"sql": q})
		if !res.IsError {
			t.Errorf("accepted: %s", q)
		}
	}
	if _, err := os.Stat(vacuumTarget); !os.IsNotExist(err) {
		t.Errorf("VACUUM INTO escape wrote %s", vacuumTarget)
	}
}

// TestQueryToolRefusesMeta pins the wording a model sees when it asks for
// meta directly: the refusal text, not a generic SQL error, and no
// "refused: " sentinel prefix leaking through.
func TestQueryToolRefusesMeta(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "query", map[string]any{"sql": "select * from meta"})
	if !res.IsError {
		t.Fatal("select * from meta was accepted")
	}
	msg := textOf(res)
	if !strings.Contains(msg, "meta") || strings.Contains(msg, "refused:") {
		t.Errorf("error = %q, want the identifier named and no sentinel prefix", msg)
	}
}

// TestQueryToolRefusesSubmissions pins that custom SQL never reaches the
// submissions table. Check refuses the name before SQLite sees the text,
// so this holds whether or not the table exists in the test database; the
// same refusal covers every spelling, a join and a subquery.
func TestQueryToolRefusesSubmissions(t *testing.T) {
	_, cs := newTestHost(t)
	for _, q := range []string{
		"select count(*) from submissions",
		`select * from "Submissions"`,
		"select * from main.submissions",
		"select 1 from events e join submissions s on s.id = e.id",
		"select * from (select * from submissions)",
	} {
		res := callTool(t, cs, "query", map[string]any{"sql": q})
		if !res.IsError {
			t.Errorf("%q was accepted", q)
			continue
		}
		msg := textOf(res)
		if !strings.Contains(strings.ToLower(msg), "may not read") || strings.Contains(msg, "refused:") {
			t.Errorf("%q: error = %q, want the refusal text and no sentinel prefix", q, msg)
		}
	}
}

func TestQueryToolCapsRows(t *testing.T) {
	h, cs := newTestHost(t)
	setGuards(t, h, h.db.Timeout(), 1)
	res := callTool(t, cs, "query", map[string]any{
		"sql": "WITH n(i) AS (VALUES (1),(2),(3)) SELECT i FROM n"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "PARTIAL") {
		t.Errorf("truncation not flagged: %s", out)
	}
}
