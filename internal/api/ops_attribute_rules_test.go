package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type attrRuleRow struct {
	Key       string `json:"key"`
	Action    string `json:"action"`
	To        string `json:"to"`
	CreatedAt string `json:"created_at"`
}

// aliasesREST answers the aliases list_attributes shows on key over
// REST, and whether key is listed at all.
func aliasesREST(t *testing.T, r *registrar, key string) ([]string, bool) {
	t.Helper()
	rec := serveREST(t, r, "GET", "/api/projects/1/attributes?from=2026-08-01&to=2026-08-30", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET attributes = %d %s", rec.Code, rec.Body)
	}
	var out listAttributesOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	for _, a := range out.Attributes {
		if a.Key == key {
			return a.Aliases, true
		}
	}
	return nil, false
}

// A key with a slash, a space and a non-ASCII letter goes through rename
// and remove as the body carries it: remove takes a body because a path
// segment could not.
func TestAttributeRulesRenameAndRemoveOverREST(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)

	const key = "checkout/v2 é"
	rec := serveREST(t, r, "POST", "/api/projects/1/attribute-rules/rename",
		`{"from":`+jsonString(key)+`,"to":"checkout"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body)
	}
	var renamed struct {
		Rule      attrRuleRow      `json:"rule"`
		Rewritten map[string]int64 `json:"rewritten"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &renamed); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	if renamed.Rule.Key != key || renamed.Rule.To != "checkout" || renamed.Rule.Action != "rename" ||
		renamed.Rule.CreatedAt == "" || renamed.Rewritten == nil {
		t.Errorf("rename = %s", rec.Body)
	}
	if aliases, _ := aliasesREST(t, r, "checkout"); len(aliases) != 1 || aliases[0] != key {
		t.Fatalf("checkout's aliases = %v, want [%s]", aliases, key)
	}
	if _, listed := aliasesREST(t, r, key); listed {
		t.Errorf("%s is renamed away but listed", key)
	}

	rec = serveREST(t, r, "POST", "/api/projects/1/attribute-rules/remove", `{"key":`+jsonString(key)+`}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"removed"`) {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	if _, listed := aliasesREST(t, r, "checkout"); listed {
		t.Errorf("checkout still listed after its only rule went")
	}
	// Removing what is no longer there is a 404 that names the rule, not
	// an unknown project.
	rec = serveREST(t, r, "POST", "/api/projects/1/attribute-rules/remove", `{"key":`+jsonString(key)+`}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("second remove = %d %s, want 404", rec.Code, rec.Body)
	}
	if msg := rec.Body.String(); !strings.Contains(msg, "attribute rule checkout/v2") || strings.Contains(msg, "unknown project") {
		t.Errorf("missing rule message = %s, want it to name the rule, not the project", msg)
	}
}

// A block carries no "to" key, not an empty one, and the blocked key is
// listed with nothing received.
func TestAttributeRulesBlockOverMCP(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "block_attribute", map[string]any{"project_id": 1, "key": "session_id"})
	if res.IsError {
		t.Fatalf("block: %s", textOf(res))
	}
	var out struct {
		Rule map[string]any `json:"rule"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Rule["key"] != "session_id" || out.Rule["action"] != "block" || out.Rule["created_at"] == "" {
		t.Errorf("rule = %v", out.Rule)
	}
	if _, has := out.Rule["to"]; has {
		t.Errorf("a block carries a to key: %v", out.Rule)
	}
	res = callTool(t, cs, "list_attributes", map[string]any{"project_id": 1, "from": "2026-08-01", "to": "2026-08-30"})
	if res.IsError {
		t.Fatalf("list: %s", textOf(res))
	}
	var list listAttributesOut
	if err := json.Unmarshal([]byte(textOf(res)), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range list.Attributes {
		if a.Key == "session_id" {
			found = true
			if !a.Blocked || a.Received || a.Breakdown {
				t.Errorf("session_id = %+v, want blocked, not received", a)
			}
		}
	}
	if !found {
		t.Errorf("blocked key not listed: %+v", list.Attributes)
	}
}

// The tools are audited as actor mcp, like every project setting.
func TestAttributeRulesAuditedByTransport(t *testing.T) {
	h, cs := newTestHost(t)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"rename_attribute", map[string]any{"project_id": 1, "from": "a", "to": "b"}},
		{"merge_attribute", map[string]any{"project_id": 1, "from": "c", "to": "b"}},
		{"block_attribute", map[string]any{"project_id": 1, "key": "d"}},
		{"remove_attribute_rule", map[string]any{"project_id": 1, "key": "d"}},
	} {
		if res := callTool(t, cs, c.tool, c.args); res.IsError {
			t.Fatalf("%s: %s", c.tool, textOf(res))
		}
	}
	out, err := h.db.Run(t.Context(), "SELECT action, actor, subject FROM audit_log WHERE action LIKE 'attribute.%' ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"attribute.rename", "mcp", "1/a"},
		{"attribute.merge", "mcp", "1/c"},
		{"attribute.block", "mcp", "1/d"},
		{"attribute.rule_remove", "mcp", "1/d"},
	}
	if len(out.Rows) != len(want) {
		t.Fatalf("audit rows = %v, want %v", out.Rows, want)
	}
	for i, w := range want {
		if strings.Join(out.Rows[i], " ") != strings.Join(w, " ") {
			t.Errorf("audit row %d = %v, want %v", i, out.Rows[i], w)
		}
	}
}

func TestAttributeRulesRefusalsOverREST(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	for _, c := range []struct {
		name, path, body string
		want             int
	}{
		{"reserved key", "/api/projects/1/attribute-rules/rename", `{"from":"$plan","to":"x"}`, http.StatusBadRequest},
		{"reserved target", "/api/projects/1/attribute-rules/merge", `{"from":"x","to":"$path"}`, http.StatusBadRequest},
		{"reserved block", "/api/projects/1/attribute-rules/block", `{"key":"$country"}`, http.StatusBadRequest},
		{"empty key", "/api/projects/1/attribute-rules/block", `{"key":""}`, http.StatusBadRequest},
		{"quoted key", "/api/projects/1/attribute-rules/block", `{"key":"a\"b"}`, http.StatusBadRequest},
		{"onto itself", "/api/projects/1/attribute-rules/rename", `{"from":"x","to":"x"}`, http.StatusBadRequest},
		{"unknown project", "/api/projects/99/attribute-rules/block", `{"key":"x"}`, http.StatusNotFound},
		{"remove without a rule", "/api/projects/1/attribute-rules/remove", `{"key":"never"}`, http.StatusNotFound},
	} {
		if rec := serveREST(t, r, "POST", c.path, c.body); rec.Code != c.want {
			t.Errorf("%s = %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}
	}
	if rec := serveREST(t, r, "POST", "/api/projects/99/attribute-rules/remove", `{"key":"x"}`); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "unknown project 99; valid projects") {
		t.Errorf("remove in an unknown project = %d %s, want 404 naming the valid projects", rec.Code, rec.Body)
	}

	// Renaming onto a key that has data is refused, merging is not:
	// "plan" has aggregated rows in project 1.
	body := `{"from":"planType","to":"plan"}`
	if rec := serveREST(t, r, "POST", "/api/projects/1/attribute-rules/rename", body); rec.Code != http.StatusConflict {
		t.Fatalf("rename onto a key with data = %d %s, want 409", rec.Code, rec.Body)
	}
	if _, err := rawExec(h.ops.St, `INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		VALUES (1,'2026-08-19','signup','planType','pro',2,1,0)`); err != nil {
		t.Fatal(err)
	}
	rec := serveREST(t, r, "POST", "/api/projects/1/attribute-rules/merge", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Rewritten map[string]int64 `json:"rewritten"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Rewritten["agg_product_attrs"] != 1 {
		t.Errorf("rewritten = %v, want agg_product_attrs 1", out.Rewritten)
	}

	// Nothing merges into a blocked key.
	if rec := serveREST(t, r, "POST", "/api/projects/1/attribute-rules/block", `{"key":"gone"}`); rec.Code != http.StatusOK {
		t.Fatalf("block = %d %s", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "POST", "/api/projects/1/attribute-rules/merge", `{"from":"tier","to":"gone"}`); rec.Code != http.StatusConflict {
		t.Errorf("merge onto a blocked key = %d %s, want 409", rec.Code, rec.Body)
	}
}

func TestAttributeRuleToolsListed(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tool := range res.Tools {
		have[tool.Name] = true
	}
	for _, name := range []string{"list_attributes", "rename_attribute", "merge_attribute", "block_attribute", "remove_attribute_rule"} {
		if !have[name] {
			t.Errorf("tool %s missing", name)
		}
	}
}
