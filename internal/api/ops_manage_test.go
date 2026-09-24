package api

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateProjectToolReturnsSnippet(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "create_project", map[string]any{
		"name": "My shop", "allowed_origins": []string{"https://shop.example.com"}})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"twillingate.js", "ak_", "data-key"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "data-identity") {
		t.Errorf("snippet still prints data-identity: %s", out)
	}
	// create_project without skip_key mints a first key; verify the key
	// is listed under the id the create returned
	list := callTool(t, cs, "list_ingest_keys", map[string]any{"project_id": projectIDOf(t, res)})
	if !strings.Contains(textOf(list), "default") {
		t.Errorf("no default key listed: %s", textOf(list))
	}
}

// TestMCPWriteRecordsActor: a management call over MCP is audited as
// actor "mcp", taken from the context the MCP edge sets.
func TestMCPWriteRecordsActor(t *testing.T) {
	h, cs := newTestHost(t)
	res := callTool(t, cs, "create_project", map[string]any{"name": "Audited", "skip_key": true})
	if res.IsError {
		t.Fatalf("create: %s", textOf(res))
	}
	var actor string
	if err := h.db.QueryRowContext(context.Background(),
		"SELECT actor FROM audit_log WHERE action='project.create' AND subject=?",
		strconv.FormatInt(projectIDOf(t, res), 10)).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != "mcp" {
		t.Errorf("audit actor = %q, want mcp", actor)
	}
}

func TestArchiveRestoreTools(t *testing.T) {
	_, cs := newTestHost(t)
	if res := callTool(t, cs, "archive_project", map[string]any{"project_id": 2}); res.IsError {
		t.Fatalf("archive: %s", textOf(res))
	}
	res := callTool(t, cs, "list_projects", nil)
	if !strings.Contains(textOf(res), "archived") {
		t.Errorf("archive not visible: %s", textOf(res))
	}
	if res := callTool(t, cs, "restore_project", map[string]any{"project_id": 2}); res.IsError {
		t.Fatalf("restore: %s", textOf(res))
	}
}

func TestKeyToolsLifecycle(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "issue_ingest_key", map[string]any{
		"project_id": 1, "label": "ios"})
	if res.IsError {
		t.Fatalf("issue: %s", textOf(res))
	}
	if res := callTool(t, cs, "disable_ingest_key", map[string]any{
		"project_id": 1, "label": "ios"}); res.IsError {
		t.Fatalf("disable: %s", textOf(res))
	}
	if res := callTool(t, cs, "enable_ingest_key", map[string]any{
		"project_id": 1, "label": "ios"}); res.IsError {
		t.Fatalf("enable: %s", textOf(res))
	}
}

func TestNoDeleteToolExists(t *testing.T) {
	_, cs := newTestHost(t)
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "delete") {
			t.Errorf("irreversible tool exposed over MCP: %s", tool.Name)
		}
		if tool.Annotations == nil {
			t.Errorf("tool %s has no annotations", tool.Name)
		}
	}
}

// TestManagementToolsAnnotatedNonReadOnly checks the annotation contract:
// writers must not be marked ReadOnlyHint, and every other tool must be.
// "query" is included in the read-only side (not exempted): it already
// carries ReadOnlyHint: true (Task 18), so there is no reason to carve it
// out — doing so would hide a regression if it ever lost the hint.
//
// It also checks the two guardrails the write annotations exist to
// encode (spec §6): every writer explicitly asserts DestructiveHint ==
// false (never left at the SDK's true default, which would tell a client
// this tool might destroy data), and the reversible-but-not-idempotent-
// by-nature ones (archive/restore/disable/enable) assert IdempotentHint
// == true, since calling them twice with the same arguments is a no-op.
func TestManagementToolsAnnotatedNonReadOnly(t *testing.T) {
	_, cs := newTestHost(t)
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	writers := map[string]bool{"create_project": true, "update_project": true,
		"archive_project": true, "restore_project": true, "issue_ingest_key": true,
		"disable_ingest_key": true, "enable_ingest_key": true}
	idempotent := map[string]bool{"archive_project": true, "restore_project": true,
		"disable_ingest_key": true, "enable_ingest_key": true}
	for _, tool := range tools.Tools {
		if writers[tool.Name] && tool.Annotations.ReadOnlyHint {
			t.Errorf("%s marked read-only", tool.Name)
		}
		if !writers[tool.Name] && !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s not marked read-only", tool.Name)
		}
		if writers[tool.Name] {
			d := tool.Annotations.DestructiveHint
			if d == nil || *d != false {
				t.Errorf("%s: DestructiveHint must be explicitly false, got %v", tool.Name, d)
			}
		}
		if idempotent[tool.Name] && !tool.Annotations.IdempotentHint {
			t.Errorf("%s: IdempotentHint must be true", tool.Name)
		}
	}
}

// TestUpdateProjectMerges: update_project merges — fields the caller
// omits are preserved from the current project, not zeroed out. Lists are
// replaced wholesale when provided, never merged element-wise.
func TestUpdateProjectMerges(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	// blog is seeded identified with empty allowed_origins; give it an
	// origin first so we can prove it survives a name-only update.
	if res := callTool(t, cs, "update_project", map[string]any{
		"project_id": 1, "allowed_origins": []string{"https://blog.example.com"}}); res.IsError {
		t.Fatalf("seed origin: %s", textOf(res))
	}
	res := callTool(t, cs, "update_project", map[string]any{
		"project_id": 1, "name": "Blog Renamed"})
	if res.IsError {
		t.Fatalf("update: %s", textOf(res))
	}
	// list_projects doesn't surface origins, so check the registry
	// directly: a name-only update must not wipe allowed_origins.
	p := h.reg.Snapshot(ctx).Project(1)
	if p == nil {
		t.Fatalf("blog vanished from registry")
	}
	if len(p.AllowedOrigins) != 1 || p.AllowedOrigins[0] != "https://blog.example.com" {
		t.Errorf("allowed_origins not preserved by name-only update, got %v", p.AllowedOrigins)
	}
	list := callTool(t, cs, "list_projects", nil)
	out := textOf(list)
	type row struct {
		ProjectID      int64    `json:"project_id"`
		Name           string   `json:"name"`
		AllowedOrigins []string `json:"allowed_origins"`
	}
	var parsed struct {
		Projects []row `json:"projects"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("decode list_projects: %v\n%s", err, out)
	}
	var blog *row
	for i := range parsed.Projects {
		if parsed.Projects[i].ProjectID == 1 {
			blog = &parsed.Projects[i]
		}
	}
	if blog == nil {
		t.Fatalf("blog missing from list_projects: %s", out)
	}
	// list_projects also lists "docs"; asserting on the decoded blog
	// entry specifically proves this was a merge, not a blind overwrite
	// that would have reset the origins to nil and the name to "".
	if len(blog.AllowedOrigins) != 1 || blog.AllowedOrigins[0] != "https://blog.example.com" {
		t.Errorf("blog origins not preserved by merge, got %v", blog.AllowedOrigins)
	}
	if blog.Name != "Blog Renamed" {
		t.Errorf("name update did not apply, got %q", blog.Name)
	}
}

// TestUpdateProjectEmptyOriginsClears: an explicit [] clears the list and
// an omitted field keeps it — JSON can tell the two apart, and the tool
// passes that distinction through.
func TestUpdateProjectEmptyOriginsClears(t *testing.T) {
	h, cs := newTestHost(t)
	if res := callTool(t, cs, "update_project", map[string]any{
		"project_id": 1, "allowed_origins": []string{"https://blog.example.com"}}); res.IsError {
		t.Fatal(textOf(res))
	}
	if res := callTool(t, cs, "update_project", map[string]any{"project_id": 1, "name": "Blog"}); res.IsError {
		t.Fatal(textOf(res))
	}
	if p := h.reg.Snapshot(context.Background()).Project(1); len(p.AllowedOrigins) != 1 {
		t.Fatalf("omitted allowed_origins changed the list: %v", p.AllowedOrigins)
	}
	if res := callTool(t, cs, "update_project", map[string]any{"project_id": 1, "allowed_origins": []string{}}); res.IsError {
		t.Fatal(textOf(res))
	}
	if p := h.reg.Snapshot(context.Background()).Project(1); len(p.AllowedOrigins) != 0 {
		t.Fatalf("explicit [] did not clear: %v", p.AllowedOrigins)
	}
}

// TestCreateProjectSetsAttributes proves create_project's Attributes field
// reaches manage.ProjectSpec and is stored, not just accepted and dropped.
func TestCreateProjectSetsAttributes(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	res := callTool(t, cs, "create_project", map[string]any{
		"name": "shop", "attributes": []string{"plan", "tier"}})
	if res.IsError {
		t.Fatalf("create: %s", textOf(res))
	}
	p := h.reg.Snapshot(ctx).Project(projectIDOf(t, res))
	if p == nil {
		t.Fatalf("shop missing from registry")
	}
	if len(p.Attributes) != 2 || p.Attributes[0] != "plan" || p.Attributes[1] != "tier" {
		t.Fatalf("Attributes = %v, want [plan tier]", p.Attributes)
	}
}

// TestUpdateProjectAttributesMergeSemantics is the merge rule for
// Attributes, matching AllowedOrigins: omitting the field on update_project
// leaves the current declared attributes untouched, and supplying a
// non-nil list replaces the whole thing (never merges element-wise).
func TestUpdateProjectAttributesMergeSemantics(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	if res := callTool(t, cs, "update_project", map[string]any{
		"project_id": 1, "attributes": []string{"plan", "tier"}}); res.IsError {
		t.Fatalf("seed attributes: %s", textOf(res))
	}

	// name-only update must not wipe attributes
	if res := callTool(t, cs, "update_project", map[string]any{
		"project_id": 1, "name": "Blog Renamed"}); res.IsError {
		t.Fatalf("update: %s", textOf(res))
	}
	p := h.reg.Snapshot(ctx).Project(1)
	if p == nil {
		t.Fatalf("blog vanished from registry")
	}
	if len(p.Attributes) != 2 || p.Attributes[0] != "plan" || p.Attributes[1] != "tier" {
		t.Fatalf("attributes not preserved by name-only update, got %v", p.Attributes)
	}

	// supplying attributes replaces the whole list
	if res := callTool(t, cs, "update_project", map[string]any{
		"project_id": 1, "attributes": []string{"solo"}}); res.IsError {
		t.Fatalf("update attributes: %s", textOf(res))
	}
	p = h.reg.Snapshot(ctx).Project(1)
	if p == nil {
		t.Fatalf("blog vanished from registry")
	}
	if len(p.Attributes) != 1 || p.Attributes[0] != "solo" {
		t.Fatalf("attributes not replaced wholesale, got %v", p.Attributes)
	}
}

// TestUpdateProjectUnknownId checks the tool names the bad id and lists
// the valid ones rather than surfacing a nil-pointer or an opaque error.
func TestUpdateProjectUnknownId(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "update_project", map[string]any{
		"project_id": 42, "name": "x"})
	if !res.IsError {
		t.Fatalf("expected error for unknown id")
	}
	if msg := textOf(res); !strings.Contains(msg, "unknown project 42; valid projects: 1 (blog), 2 (docs)") {
		t.Errorf("error does not name the id and the valid ones: %s", msg)
	}
}
