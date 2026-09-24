package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// withDB points DATABASE_DSN at a fresh temp file for one test.
func withDB(t *testing.T) string {
	t.Helper()
	dsn := "sqlite://" + t.TempDir() + "/cli.db"
	t.Setenv("DATABASE_DSN", dsn)
	return dsn
}

func TestProjectCreateListArchiveDelete(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create"}, &out); code != 2 {
		t.Fatalf("create without -name: exit %d, want 2: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "create", "-name", "My blog",
		"-origin", "https://blog.example.com"}, &out); code != 0 {
		t.Fatalf("create: exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `project 1 ("My blog") created`) ||
		!strings.Contains(out.String(), "key issue -project-id 1 -label web") {
		t.Fatalf("create output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.HasPrefix(out.String(), "1\tMy blog") {
		t.Fatalf("list output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"project", "archive", "-id", "1"}, &out); code != 0 {
		t.Fatalf("archive: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "restore", "-id", "1"}, &out); code != 0 {
		t.Fatalf("restore: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "delete", "-id", "1"}, &out); code == 0 {
		t.Fatal("delete without -force succeeded")
	}
	out.Reset()
	if code := run([]string{"project", "delete", "-id", "1", "-force"}, &out); code != 0 {
		t.Fatalf("delete -force: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 || strings.Contains(out.String(), "My blog") {
		t.Fatalf("project survived delete: %s", out.String())
	}
}

func TestProjectUpdateClearsOriginsOnlyWhenAsked(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", "https://shop.example.com"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-name", "Shop UK"}, &out); code != 0 {
		t.Fatalf("update: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-origin", "https://a.com", "-clear-origins"}, &out); code != 2 {
		t.Fatalf("-origin with -clear-origins: exit %d, want 2", code)
	}
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-clear-origins"}, &out); code != 0 {
		t.Fatalf("clear: %s", out.String())
	}
	// The registry is the source of truth: reopen it and look.
	ops, _, closeStore, code := openOps(&out, "")
	if code != 0 {
		t.Fatal(out.String())
	}
	defer closeStore()
	p := ops.Reg.Snapshot(context.Background()).Project(1)
	if p == nil || p.Name != "Shop UK" || len(p.AllowedOrigins) != 0 {
		t.Fatalf("project = %+v, want name Shop UK and no origins", p)
	}
	out.Reset()
	if code := run([]string{"project", "update", "-id", "9"}, &out); code != 1 || !strings.Contains(out.String(), "unknown id 9") {
		t.Fatalf("unknown id: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "rename", "-id", "1"}, &out); code != 2 {
		t.Fatalf("rename must be an unknown subcommand: exit %d", code)
	}
	out.Reset()
	if code := run([]string{"config", "export"}, &out); code != 2 {
		t.Fatalf("config must be an unknown command: exit %d", code)
	}
}

func TestProjectCreateUnknownSubcommand(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "frobnicate"}, &out); code != 2 {
		t.Fatalf("exit = %d", code)
	}
}

func TestEnvFileFlag(t *testing.T) {
	dir := t.TempDir()
	envFile := dir + "/analytics.env"
	os.WriteFile(envFile, []byte("DATABASE_DSN=sqlite://"+dir+"/env.db\n"), 0o600)
	os.Unsetenv("DATABASE_DSN")
	var out bytes.Buffer
	if code := run([]string{"project", "-env-file", envFile, "create", "-name", "x"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
}

func TestProjectUpdateMergeSemantics(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	// Create a project with an origin
	if code := run([]string{"project", "create", "-name", "Original",
		"-origin", "https://example.com"}, &out); code != 0 {
		t.Fatalf("create: exit %d: %s", code, out.String())
	}

	// Update only the name; the origin should survive
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-name", "Updated"}, &out); code != 0 {
		t.Fatalf("update: exit %d: %s", code, out.String())
	}
	p := snapshotProject(t, 1)
	if p.Name != "Updated" || len(p.AllowedOrigins) != 1 {
		t.Fatalf("name-only update changed more than the name: %+v", p)
	}

	// The flag is gone: the parser refuses it.
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-identity", "anonymous"}, &out); code != 2 {
		t.Fatalf("-identity accepted: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 || !strings.Contains(out.String(), "1\tUpdated") {
		t.Fatalf("project list = exit %d: %q, want `1\\tUpdated`", code, out.String())
	}
}

// snapshotProject reopens the registry and returns project id: the CLI's
// own output is not the source of truth, the database is.
func snapshotProject(t *testing.T, id int64) *manage.Project {
	t.Helper()
	var out bytes.Buffer
	ops, _, closeStore, code := openOps(&out, "")
	if code != 0 {
		t.Fatal(out.String())
	}
	defer closeStore()
	p := ops.Reg.Snapshot(context.Background()).Project(id)
	if p == nil {
		t.Fatalf("project %d missing", id)
	}
	return p
}

// TestProjectAttrFlag proves the repeatable -attr flag on `project create`
// reaches storage, not merely the output.
func TestProjectAttrFlag(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "blog",
		"-attr", "plan", "-attr", "tier"}, &out); code != 0 {
		t.Fatalf("create = %d: %s", code, out.String())
	}
	attrs := snapshotProject(t, 1).Attributes
	if len(attrs) != 2 || attrs[0] != "plan" || attrs[1] != "tier" {
		t.Fatalf("Attributes = %v, want [plan tier]", attrs)
	}
}

// TestProjectAttrUpdateMergeSemantics is the CLI-level guard for the merge
// rule -attr must follow, matching -origin exactly (docs/twillingate.md):
// omitting -attr on `project update` leaves the current list untouched, and
// supplying it at all replaces the whole list wholesale.
func TestProjectAttrUpdateMergeSemantics(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "blog",
		"-attr", "plan", "-attr", "tier"}, &out); code != 0 {
		t.Fatalf("create: exit %d: %s", code, out.String())
	}

	// update without -attr must leave the declared attributes untouched
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-name", "Blog Renamed"}, &out); code != 0 {
		t.Fatalf("update: exit %d: %s", code, out.String())
	}
	if attrs := snapshotProject(t, 1).Attributes; len(attrs) != 2 {
		t.Fatalf("update without -attr changed declared attributes: %v", attrs)
	}

	// update with -attr must replace the whole list, not merge into it
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-attr", "solo"}, &out); code != 0 {
		t.Fatalf("update -attr: exit %d: %s", code, out.String())
	}
	if attrs := snapshotProject(t, 1).Attributes; len(attrs) != 1 || attrs[0] != "solo" {
		t.Fatalf("update -attr merged instead of replacing: %v", attrs)
	}
}
