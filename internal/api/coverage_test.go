package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/reporting/sqlbind"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// ---- filesTable (over runFiles and sqlError) and checkRange() branches
// not reached via the happy-path *Overview/*Breakdown tests ----

// TestFilesTableTruncatesErrorsAndTimesOut: what the engine answers
// reaches the caller as the query tool words it, kind by kind, and a
// truncated answer carries its note.
func TestFilesTableTruncatesErrorsAndTimesOut(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	blog := rangeIn{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"}
	eng := archivetest.NewFakeSQL(t)
	h.sql = eng.Client(t)

	// truncation: blog's view days, capped to 1.
	eng.On(archivetest.SQLHas("SELECT DISTINCT day"), archivetest.Text([]string{"day"}, []string{"2026-08-01"}).Truncated())
	setGuards(t, h, h.queryTimeout, 1)
	out, err := h.filesTable(ctx, blog, `SELECT DISTINCT day FROM views WHERE project_id = :project AND day BETWEEN :from AND :to`, nil)
	if err != nil {
		t.Fatalf("truncation case: %v", err)
	}
	if !out.Truncated || out.Note == "" || len(out.Rows) != 1 {
		t.Errorf("out = %+v, want one row, Truncated, with a Note", out)
	}
	reqs := eng.Requests()
	if r := reqs[len(reqs)-1]; r.Mode != sqlengine.ModeText || r.MaxRows != 1 || r.Scope == nil || *r.Scope != 1 ||
		r.Args["tw_project"] != "1" || r.Args["tw_from"] != "2026-08-01" || r.Args["tw_to"] != "2026-08-31" || len(r.Tables.Projects) != 1 {
		t.Errorf("request = %+v, want mode text, max_rows 1, blog's scope, args and files", r)
	}
	setGuards(t, h, h.queryTimeout, 1000)

	binder := `Binder Error: Referenced column "no_such_column" not found in FROM clause!`
	for _, c := range []struct {
		name   string
		answer archivetest.Answer
		want   string
	}{
		{"sql", archivetest.Fail(sqlengine.KindSQL, binder), "SQL error (DuckDB): " + binder},
		{"sql on an old table", archivetest.Fail(sqlengine.KindSQL, "Catalog Error: Table with name v_events does not exist!\nLINE 1"),
			"Catalog Error: Table with name v_events does not exist!; the v_*, raw_* and agg_* tables went with the database's events. The event files hold"},
		{"refused", archivetest.Fail(sqlengine.KindRefused, `"agg_views" is not a table here`),
			`"agg_views" is not a table here; the v_*, raw_* and agg_* tables went with the database's events (docs://twillingate)`},
		{"timeout", archivetest.Fail(sqlengine.KindTimeout, "the query exceeded X-Timeout-Ms"), sqlbind.Timeout(h.queryTimeout)},
		{"out of memory", archivetest.Fail(sqlengine.KindOutOfMemory, "Out of Memory Error"), sqlbind.OutOfMemory},
		{"logs out of memory", archivetest.Fail(sqlengine.KindOutOfMemoryLoadingLog, "Out of Memory Error"), sqlbind.LogsOutOfMemory},
		{"exited", archivetest.Fail(sqlengine.KindExited, "the worker exited"), sqlbind.ChildExited},
		{"busy", archivetest.Fail(sqlengine.KindBusy, "no free slot"), sqlbind.Busy},
		{"stale", archivetest.Fail(sqlengine.KindStale, "a file in the table spec is gone"), sqlbind.Stale},
		{"internal", archivetest.Fail(sqlengine.KindInternal, "engine failed"), sqlbind.EngineFailed},
	} {
		eng.On(archivetest.SQLHas("no_such_column"), c.answer)
		_, err = h.filesTable(ctx, blog, `SELECT no_such_column FROM views WHERE project_id = :project`, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if c.name != "timeout" && err != nil && strings.Contains(err.Error(), "exceeded") {
			t.Errorf("%s: worded as a timeout: %v", c.name, err)
		}
	}

	// timeout: a runaway query against a host with a tiny deadline.
	eng.On(archivetest.SQLHas("range(100000000000)"), archivetest.Hang())
	setGuards(t, h, 20*time.Millisecond, h.queryRows)
	_, err = h.filesTable(ctx, blog, `SELECT count(*) FROM range(100000000000) a WHERE a.range % 7 = :project`, nil)
	if err == nil {
		t.Fatal("runaway query did not time out")
	}
	if !strings.Contains(err.Error(), "exceeded") || !strings.Contains(err.Error(), "narrow the date range") {
		t.Errorf("timeout error not worded as expected: %v", err)
	}
}

func TestCheckRangeRejectsImpossibleDay(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "views_overview", map[string]any{"project_id": 1, "from": "2026-02-30", "to": "2026-03-01"})
	if !res.IsError || !strings.Contains(textOf(res), "YYYY-MM-DD") {
		t.Errorf("2026-02-30 = %v %s, want the YYYY-MM-DD refusal", res.IsError, textOf(res))
	}
}

func TestCheckRangeRejectsBadDateFormat(t *testing.T) {
	_, cs := newTestHost(t)
	for _, bad := range []map[string]any{
		{"project_id": 1, "from": "not-a-date", "to": "2026-08-31"},
		{"project_id": 1, "from": "2026-08-01", "to": "8/31/2026"},
	} {
		res := callTool(t, cs, "views_overview", bad)
		if !res.IsError {
			t.Errorf("bad range %v accepted", bad)
		}
	}
}

// ---- ops_manage.go: error branches and the skip_key path ----

func TestCreateProjectSkipKey(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "create_project", map[string]any{
		"name": "No Key", "skip_key": true})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if strings.Contains(out, `"key"`) || strings.Contains(out, "script.js") {
		t.Errorf("skip_key:true still returned a key/snippet: %s", out)
	}
	list := callTool(t, cs, "list_ingest_keys", map[string]any{"project_id": projectIDOf(t, res)})
	if strings.Contains(textOf(list), "default") {
		t.Errorf("skip_key:true still issued a key: %s", textOf(list))
	}
}

// identity was a project field until migration 017; sending it now is an
// unknown field, refused like any other.
func TestCreateProjectToolRejectsIdentity(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "create_project", map[string]any{
		"name": "bad", "identity": "identified"})
	if !res.IsError {
		t.Fatal("create_project accepted the removed identity field")
	}
	res = callTool(t, cs, "update_project", map[string]any{
		"project_id": 1, "identity": "identified"})
	if !res.IsError {
		t.Fatal("update_project accepted the removed identity field")
	}
}

// Project-scoped tools answer an unknown id with the valid ones listed
// (the recoverable form a model can act on), whichever layer noticed the
// miss: archive learns it from the store, issue_ingest_key from manage.
func TestArchiveToolUnknownId(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "archive_project", map[string]any{"project_id": 99})
	if !res.IsError {
		t.Fatal("archive of unknown id accepted")
	}
	if msg := textOf(res); !strings.Contains(msg, "unknown project 99; valid projects: 1 (blog), 2 (docs)") {
		t.Errorf("error = %q, want the valid ids and names listed", msg)
	}
}

// restore of an unknown id is refused like archive, not reported as done.
func TestRestoreToolUnknownIdIsRefused(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "restore_project", map[string]any{"project_id": 99})
	if !res.IsError {
		t.Fatal("restore of unknown id accepted")
	}
	if msg := textOf(res); !strings.Contains(msg, "unknown project 99; valid projects: 1 (blog), 2 (docs)") {
		t.Errorf("error = %q, want the valid ids and names listed", msg)
	}
}

func TestIssueKeyToolUnknownProject(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "issue_ingest_key", map[string]any{
		"project_id": 99, "label": "web"})
	if !res.IsError {
		t.Fatal("issue_ingest_key for unknown project accepted")
	}
	if msg := textOf(res); !strings.Contains(msg, "unknown project 99; valid projects: 1 (blog), 2 (docs)") {
		t.Errorf("error = %q, want the valid ids and names listed", msg)
	}
}

func TestDisableEnableKeyToolsUnknownKey(t *testing.T) {
	_, cs := newTestHost(t)
	if res := callTool(t, cs, "disable_ingest_key", map[string]any{
		"project_id": 1, "label": "ghost"}); !res.IsError {
		t.Fatal("disable of unknown key accepted")
	}
	if res := callTool(t, cs, "enable_ingest_key", map[string]any{
		"project_id": 1, "label": "ghost"}); !res.IsError {
		t.Fatal("enable of unknown key accepted")
	}
}

func TestListKeysFilterAndUnfiltered(t *testing.T) {
	_, cs := newTestHost(t)
	if res := callTool(t, cs, "issue_ingest_key", map[string]any{
		"project_id": 1, "label": "web"}); res.IsError {
		t.Fatalf("seed key: %s", textOf(res))
	}
	if res := callTool(t, cs, "issue_ingest_key", map[string]any{
		"project_id": 2, "label": "web"}); res.IsError {
		t.Fatalf("seed key: %s", textOf(res))
	}

	filtered := textOf(callTool(t, cs, "list_ingest_keys", map[string]any{"project_id": 1}))
	if !strings.Contains(filtered, `"project_id":1`) || strings.Contains(filtered, `"project_id":2`) {
		t.Errorf("project filter not applied: %s", filtered)
	}

	all := textOf(callTool(t, cs, "list_ingest_keys", nil))
	if !strings.Contains(all, `"project_id":1`) || !strings.Contains(all, `"project_id":2`) {
		t.Errorf("unfiltered list missing a project: %s", all)
	}
}

func TestListKeysPropagatesStoreError(t *testing.T) {
	h, cs := newTestHost(t)
	// manage.Store has no Close; the test reaches the real store's.
	if err := h.ops.St.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, cs, "list_ingest_keys", nil)
	if !res.IsError {
		t.Fatal("list_ingest_keys on a closed store returned no error")
	}
}

// ---- ops_product.go: kind/surface validation and the event filter ----

func TestIdentitiesRejectsBadKind(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "identities", map[string]any{
		"project_id": 1, "kind": "robot", "from": "2026-08-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("bad kind accepted")
	}
}

func TestIdentitiesGroupKind(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "identities", map[string]any{
		"project_id": 1, "kind": "group", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "g1") || !strings.Contains(out, "Acme Inc") {
		t.Errorf("group identity missing: %s", out)
	}
	// A group's users are its distinct members: u1 for g1.
	if !strings.Contains(out, `["g1","Acme Inc","1","1","0","1"]`) {
		t.Errorf("g1 row: %s", out)
	}
}

func TestRetentionRejectsBadActor(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project_id": 1, "actor": "mobile", "from": "2026-07-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("bad actor accepted")
	}
}

func TestProductEventsFiltersByEventName(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_events", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31", "event": "signup"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "signup") {
		t.Errorf("event filter dropped the row: %s", out)
	}
	// a name with no matching rows must succeed with an empty table, not error
	res = callTool(t, cs, "product_events", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31", "event": "no-such-event"})
	if res.IsError {
		t.Fatalf("unmatched event name errored: %s", textOf(res))
	}
}
