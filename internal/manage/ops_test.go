package manage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCreateProjectValidatesAndReloads(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	ctx := context.Background()

	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Name:           "My blog",
		AllowedOrigins: []string{"https://blog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 1 {
		t.Fatalf("created = %+v", p)
	}
	// snapshot rebuilt synchronously after an in-process write
	if reg.Snapshot(ctx).Project(p.ID) == nil {
		t.Fatal("snapshot not reloaded")
	}
	// validation
	for _, bad := range []ProjectSpec{
		{Name: ""},
		{Name: "x", AllowedOrigins: []string{""}},
	} {
		if _, err := ops.CreateProject(ctx, "cli", bad); err == nil {
			t.Errorf("spec %+v did not fail", bad)
		}
	}
}

func TestCreateRequiresName(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	_, err := ops.CreateProject(ctx, "test", ProjectSpec{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create without a name: err = %v, want ErrInvalid", err)
	}
	p, err := ops.CreateProject(ctx, "test", ProjectSpec{Name: "My App"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 1 || p.Name != "My App" {
		t.Fatalf("created = %+v, want id 1 and the name", p)
	}
	// Names are not unique: a second project with the same name gets id 2.
	q, err := ops.CreateProject(ctx, "test", ProjectSpec{Name: "My App"})
	if err != nil || q.ID != 2 {
		t.Fatalf("second create = %+v, %v; want id 2", q, err)
	}
}

func TestUpdateMergesAndClearsOriginsOnlyWhenAsked(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	p, err := ops.CreateProject(ctx, "test", ProjectSpec{Name: "Blog",
		AllowedOrigins: []string{"https://blog.example.com"}, Attributes: []string{"plan"}})
	if err != nil {
		t.Fatal(err)
	}
	// A name-only update keeps origins and attributes.
	u, err := ops.UpdateProject(ctx, "test", ProjectSpec{ID: p.ID, Name: "Renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "Renamed" || len(u.AllowedOrigins) != 1 || len(u.Attributes) != 1 {
		t.Fatalf("name-only update changed more than the name: %+v", u)
	}
	// A non-nil empty origins list clears; nil keeps.
	u, err = ops.UpdateProject(ctx, "test", ProjectSpec{ID: p.ID, AllowedOrigins: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(u.AllowedOrigins) != 0 {
		t.Fatalf("explicit empty origins did not clear: %v", u.AllowedOrigins)
	}
	if u.Name != "Renamed" {
		t.Fatalf("clearing origins changed the name: %q", u.Name)
	}
	if _, err := ops.UpdateProject(ctx, "test", ProjectSpec{ID: 99, Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestUnknownIdsAreNotFound(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	for name, err := range map[string]error{
		"archive": ops.ArchiveProject(ctx, "test", 42),
		"delete":  ops.DeleteProject(ctx, "test", 42),
		"key":     func() error { _, e := ops.IssueIngestKey(ctx, "test", 42, "web"); return e }(),
		"disable": ops.DisableIngestKey(ctx, "test", 42, "web"),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s on id 42: err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestIssueKeyMintsAndResolves(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := ops.IssueIngestKey(ctx, "mcp", p.ID, "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "ak_") || len(key) != 3+32 {
		t.Fatalf("key = %q", key)
	}
	if got, label, ok := reg.Snapshot(ctx).ProjectByKey(key); !ok || got.ID != p.ID || label != "web" {
		t.Fatalf("minted key does not resolve: %v %q %v", got, label, ok)
	}
	if err := ops.DisableIngestKey(ctx, "mcp", p.ID, "web"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey(key); ok {
		t.Fatal("disabled key still resolves")
	}
	if err := ops.EnableIngestKey(ctx, "mcp", p.ID, "web"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey(key); !ok {
		t.Fatal("re-enabled key does not resolve")
	}
	// duplicate label on the same project is rejected (retire-by-label
	// depends on label uniqueness within a project)
	if _, err := ops.IssueIngestKey(ctx, "mcp", p.ID, "web"); err == nil {
		t.Fatal("duplicate label did not fail")
	}
}

func TestMintersAndSnippet(t *testing.T) {
	tok, err := MintAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "ar_") || len(tok) != 3+64 {
		t.Fatalf("token = %q", tok)
	}
	snip := Snippet("https://blog.example.com", "ak_x")
	for _, want := range []string{"twillingate.js", `data-key="ak_x"`} {
		if !strings.Contains(snip, want) {
			t.Errorf("snippet missing %q:\n%s", want, snip)
		}
	}
	if strings.Contains(snip, "data-identity") {
		t.Error("snippet must not print data-identity")
	}
}

func TestCreateProjectWithKey(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	ctx := context.Background()

	p, key, err := ops.CreateProjectWithKey(ctx, "api", ProjectSpec{
		Name: "Blog"}, "default")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.ID != 1 || !strings.HasPrefix(key, "ak_") {
		t.Fatalf("created %+v with key %q", p, key)
	}
	if got := reg.Snapshot(ctx).KeylessProjects(); len(got) != 0 {
		t.Errorf("keyless projects after create-with-key = %v", got)
	}

	// A refused spec creates nothing.
	if _, _, err := ops.CreateProjectWithKey(ctx, "api", ProjectSpec{}, "default"); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing name err = %v, want ErrInvalid", err)
	}
	if n := len(reg.Snapshot(ctx).Projects()); n != 1 {
		t.Errorf("projects = %d, want 1", n)
	}
}
