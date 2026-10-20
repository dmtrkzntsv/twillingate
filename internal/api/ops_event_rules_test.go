package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

type ruleRow struct {
	Family    string `json:"family"`
	Name      string `json:"name"`
	Action    string `json:"action"`
	To        string `json:"to"`
	CreatedAt string `json:"created_at"`
}

func listRulesREST(t *testing.T, r *registrar, project string) []ruleRow {
	t.Helper()
	rec := serveREST(t, r, "GET", "/api/projects/"+project+"/event-rules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET event-rules = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Rules []ruleRow `json:"rules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	if out.Rules == nil {
		t.Fatalf("rules is not an array: %s", rec.Body)
	}
	return out.Rules
}

// A name with a slash, a space and a non-ASCII letter goes through rename
// and remove as the body carries it: remove takes a body because a path
// segment could not.
func TestEventRulesRenameAndRemoveOverREST(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)

	const name = "checkout/v2 é"
	rec := serveREST(t, r, "POST", "/api/projects/1/event-rules/rename",
		`{"family":"product","from":`+jsonString(name)+`,"to":"checkout"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body)
	}
	var renamed struct {
		Rule      ruleRow          `json:"rule"`
		Rewritten map[string]int64 `json:"rewritten"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &renamed); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	if renamed.Rule.Name != name || renamed.Rule.To != "checkout" || renamed.Rule.Action != "rename" || renamed.Rewritten == nil {
		t.Errorf("rename = %s", rec.Body)
	}

	got := listRulesREST(t, r, "1")
	if len(got) != 1 || got[0].Family != "product" || got[0].Name != name ||
		got[0].Action != "rename" || got[0].To != "checkout" || got[0].CreatedAt == "" {
		t.Fatalf("rules = %+v", got)
	}

	rec = serveREST(t, r, "POST", "/api/projects/1/event-rules/remove",
		`{"family":"product","name":`+jsonString(name)+`}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"removed"`) {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	if got := listRulesREST(t, r, "1"); len(got) != 0 {
		t.Errorf("rules after remove = %+v", got)
	}
	// Removing what is no longer there is a 404 that names the rule, not
	// an unknown project.
	rec = serveREST(t, r, "POST", "/api/projects/1/event-rules/remove",
		`{"family":"product","name":`+jsonString(name)+`}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("second remove = %d %s, want 404", rec.Code, rec.Body)
	}
	if msg := rec.Body.String(); !strings.Contains(msg, "event rule product/") || strings.Contains(msg, "unknown project") {
		t.Errorf("missing rule message = %s, want it to name the rule, not the project", msg)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A block carries no "to" key, not an empty one.
func TestEventRulesBlockOverMCP(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "block_event", map[string]any{"project_id": 1, "family": "measures", "name": "noisy"})
	if res.IsError {
		t.Fatalf("block: %s", textOf(res))
	}
	res = callTool(t, cs, "list_event_rules", map[string]any{"project_id": 1})
	if res.IsError {
		t.Fatalf("list: %s", textOf(res))
	}
	var out struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rules) != 1 {
		t.Fatalf("rules = %s", textOf(res))
	}
	rule := out.Rules[0]
	if rule["action"] != "block" || rule["family"] != "measures" || rule["name"] != "noisy" {
		t.Errorf("rule = %v", rule)
	}
	if _, has := rule["to"]; has {
		t.Errorf("a block carries a to key: %v", rule)
	}
}

// The tools are audited as actor mcp, like every project setting.
func TestEventRulesAuditedByTransport(t *testing.T) {
	h, cs := newTestHost(t)
	if res := callTool(t, cs, "rename_event", map[string]any{
		"project_id": 1, "family": "product", "from": "a", "to": "b"}); res.IsError {
		t.Fatalf("rename: %s", textOf(res))
	}
	if res := callTool(t, cs, "merge_event", map[string]any{
		"project_id": 1, "family": "product", "from": "c", "to": "b"}); res.IsError {
		t.Fatalf("merge: %s", textOf(res))
	}
	out, err := h.db.Run(t.Context(), "SELECT action, actor FROM audit_log WHERE action LIKE 'event.%' ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 2 || out.Rows[0][0] != "event.rename" || out.Rows[0][1] != "mcp" ||
		out.Rows[1][0] != "event.merge" || out.Rows[1][1] != "mcp" {
		t.Errorf("audit rows = %v, want a rename and a merge by mcp", out.Rows)
	}
}

func TestEventRulesRefusalsOverREST(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	for _, c := range []struct {
		name, path, body string
		want             int
	}{
		{"views family", "/api/projects/1/event-rules/block", `{"family":"views","name":"x"}`, http.StatusBadRequest},
		{"reserved name", "/api/projects/1/event-rules/rename", `{"family":"product","from":"$page_view","to":"x"}`, http.StatusBadRequest},
		{"unknown project", "/api/projects/99/event-rules/block", `{"family":"product","name":"x"}`, http.StatusNotFound},
		{"remove without a rule", "/api/projects/1/event-rules/remove", `{"family":"product","name":"never"}`, http.StatusNotFound},
	} {
		if rec := serveREST(t, r, "POST", c.path, c.body); rec.Code != c.want {
			t.Errorf("%s = %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}
	}
	if rec := serveREST(t, r, "GET", "/api/projects/99/event-rules", ""); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "unknown project 99; valid projects") {
		t.Errorf("list of an unknown project = %d %s, want 404 naming the valid projects", rec.Code, rec.Body)
	}

	// Renaming onto a name that has events is refused, merging is not:
	// "signup" has aggregated rows in project 1.
	body := `{"family":"product","from":"old","to":"signup"}`
	rec := serveREST(t, r, "POST", "/api/projects/1/event-rules/rename", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rename onto an existing name = %d %s, want 409", rec.Code, rec.Body)
	}
	if _, err := rawExec(h.ops.St, `INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users)
		VALUES (1,'2026-08-19','old',2,1)`); err != nil {
		t.Fatal(err)
	}
	rec = serveREST(t, r, "POST", "/api/projects/1/event-rules/merge", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Rewritten map[string]int64 `json:"rewritten"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, n := range out.Rewritten {
		sum += n
	}
	if out.Rewritten["agg_product_daily"] != 1 || sum < 1 {
		t.Errorf("rewritten = %v, want agg_product_daily 1", out.Rewritten)
	}

	// Nothing merges into a blocked name.
	if rec := serveREST(t, r, "POST", "/api/projects/1/event-rules/block", `{"family":"product","name":"gone"}`); rec.Code != http.StatusOK {
		t.Fatalf("block = %d %s", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "POST", "/api/projects/1/event-rules/merge", `{"family":"product","from":"signup","to":"gone"}`); rec.Code != http.StatusConflict {
		t.Errorf("merge onto a blocked name = %d %s, want 409", rec.Code, rec.Body)
	}
}

type nameRow struct {
	Family string `json:"family"`
	Name   string `json:"name"`
	Events int64  `json:"events"`
}

// TestEventNamesReadTheFiles: the product and measures names of the
// range in the event files, busiest first, rules applied: a blocked name
// is left out and a renamed one counts under its new name. SQLite's
// events are not read.
func TestEventNamesReadTheFiles(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	ctx := t.Context()
	if _, err := h.ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: "events"}); err != nil {
		t.Fatal(err)
	}
	const pid = "3"
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	product := func(name string) store.Event {
		return store.Event{ProjectID: 3, Family: store.FamilyProduct, EventName: name, Kind: "web", TS: at}
	}
	measure := func(name string) store.Event {
		v := 120.0
		return store.Event{ProjectID: 3, Family: store.FamilyMeasures, EventName: name, Kind: "web", TS: at,
			Value: &v, Measure: store.MeasureTime}
	}
	writeHostEvents(t, h,
		product("a"), product("a"), product("b"),
		measure("m"), measure("m"), measure("m"),
		// $ names belong to the server and take no rule: left out.
		product("$form_submit"), measure("$lcp"),
		// Blocked: left out. Renamed to "c": counted as "c".
		product("spam"), product("old"), product("old"), product("old"), product("old"))
	if _, err := h.ops.BlockEvent(ctx, "test", 3, "product", "spam"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.ops.RenameEvent(ctx, "test", 3, "product", "old", "c"); err != nil {
		t.Fatal(err)
	}
	// A row in SQLite alone is not read.
	if _, err := rawExec(h.ops.St, `INSERT INTO events (id, project_id, ts, day, received_at, kind, actor_id, actor_kind, path, family, event_name, sample_rate)
		VALUES ('n9', 3, '2026-09-10T10:00:00Z', '2026-09-10', '2026-09-10T10:00:00Z', 'web', 'a1', 'user', '/', 'product', 'sqlite_only', 1)`); err != nil {
		t.Fatal(err)
	}

	rec := serveREST(t, r, "GET", "/api/projects/"+pid+"/event-names?from=2026-09-01&to=2026-09-30", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("event-names = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Events []nameRow `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := []nameRow{{"product", "c", 4}, {"measures", "m", 3}, {"product", "a", 2}, {"product", "b", 1}}
	if len(out.Events) != len(want) {
		t.Fatalf("events = %+v, want %+v", out.Events, want)
	}
	for i := range want {
		if out.Events[i] != want[i] {
			t.Errorf("events[%d] = %+v, want %+v", i, out.Events[i], want[i])
		}
	}

	// A range outside the data is an empty array, not null.
	rec = serveREST(t, r, "GET", "/api/projects/"+pid+"/event-names?from=2026-01-01&to=2026-01-02", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Errorf("empty range = %d %s", rec.Code, rec.Body)
	}

	// The range is checked like product_events checks it.
	rec = serveREST(t, r, "GET", "/api/projects/"+pid+"/event-names?from=yesterday&to=2026-09-30", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad range = %d %s, want 400", rec.Code, rec.Body)
	}
	rec = serveREST(t, r, "GET", "/api/projects/99/event-names?from=2026-09-01&to=2026-09-30", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d %s, want 404", rec.Code, rec.Body)
	}
}

// A project sending generated names must not turn the Settings page into a
// 400 at the row cap: the busiest 200 come back, the rest are left out.
func TestEventNamesKeepsTheBusiest200(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	if _, err := h.ops.CreateProject(t.Context(), "test", manage.ProjectSpec{Name: "events"}); err != nil {
		t.Fatal(err)
	}
	const pid = "3"
	// 201 names with one event each, plus a second event for "n000" so the
	// order has a head; "n200" sorts last and is the one left out.
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	var evs []store.Event
	for i := 0; i <= 200; i++ {
		evs = append(evs, store.Event{ProjectID: 3, Family: store.FamilyProduct, EventName: fmt.Sprintf("n%03d", i), Kind: "web", TS: at})
	}
	evs = append(evs, store.Event{ProjectID: 3, Family: store.FamilyProduct, EventName: "n000", Kind: "web", TS: at})
	writeHostEvents(t, h, evs...)
	rec := serveREST(t, r, "GET", "/api/projects/"+pid+"/event-names?from=2026-09-01&to=2026-09-30", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("event-names = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Events []nameRow `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 200 {
		t.Fatalf("rows = %d, want 200", len(out.Events))
	}
	if out.Events[0].Name != "n000" || out.Events[0].Events != 2 {
		t.Errorf("first = %+v, want n000 with 2", out.Events[0])
	}
	if last := out.Events[199]; last.Name != "n199" {
		t.Errorf("last = %+v, want n199 (n200 is the one left out)", last)
	}
}

// event_names is for the console: no MCP tool carries it.
func TestEventNamesIsNotAnMCPTool(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tool := range res.Tools {
		have[tool.Name] = true
	}
	for _, name := range []string{"list_event_rules", "rename_event", "merge_event", "block_event", "remove_event_rule"} {
		if !have[name] {
			t.Errorf("tool %s missing", name)
		}
	}
	if have["event_names"] {
		t.Error("event_names is an MCP tool; it is REST only")
	}
}
