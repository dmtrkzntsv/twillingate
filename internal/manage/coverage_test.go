package manage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// rawExec reaches the underlying *sql.DB of the sqlite store for tests
// that need to write rows the exported API cannot produce (malformed JSON
// in a registry column, in particular). Mirrors internal/api's
// seed_test.go helper of the same shape.
func rawExec(t *testing.T, st store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.(interface {
		ExecForTest(string, ...any) (sql.Result, error)
	}).ExecForTest(q, args...); err != nil {
		t.Fatal(err)
	}
}

// ---- ops.go: zero/low-coverage operations ----

func TestArchiveAndRestoreProjectRoundTrip(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "cli", p.ID); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).Project(p.ID); got == nil || !got.Archived {
		t.Fatalf("after archive, Project = %+v", got)
	}
	if err := ops.RestoreProject(ctx, "cli", p.ID); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).Project(p.ID); got == nil || got.Archived {
		t.Fatalf("after restore, Project = %+v", got)
	}
}

func TestDeleteProjectRemovesEverything(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.IssueIngestKey(ctx, "cli", p.ID, "web"); err != nil {
		t.Fatal(err)
	}
	if err := ops.DeleteProject(ctx, "cli", p.ID); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).Project(p.ID); got != nil {
		t.Fatalf("deleted project still present: %+v", got)
	}
	// deleting an unknown id is an error, not a silent no-op.
	if err := ops.DeleteProject(ctx, "cli", 404); err == nil {
		t.Fatal("delete of unknown id = nil, want error")
	}
}

func TestIssueIngestKeyUnknownProject(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.IssueIngestKey(ctx, "cli", 404, "web"); err == nil {
		t.Fatal("want error for unknown project")
	}
}

func TestUpdateProjectAppliesAttributes(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := ops.UpdateProject(ctx, "cli", ProjectSpec{
		ID: p.ID, Attributes: []string{"plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "b" || len(u.Attributes) != 1 || u.Attributes[0] != "plan" {
		t.Fatalf("updated project = %+v", u)
	}
}

// Closing the underlying store forces every downstream write to fail,
// exercising the error-propagation branch of each Ops method the same
// way internal/store/sqlite's own TestOperationsOnClosedDB does.
func TestOpsPropagateStoreErrorsOnClosedDB(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.IssueIngestKey(ctx, "cli", p.ID, "web"); err != nil {
		t.Fatal(err)
	}
	// snapshot already has the project cached; closing st only breaks
	// writes and the LoadRegistry/ConfigVersion calls Reload makes
	// afterwards.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	for name, op := range map[string]func() error{
		"CreateProject": func() error {
			_, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "o"})
			return err
		},
		"UpdateProject": func() error {
			_, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: p.ID, Name: "b"})
			return err
		},
		"ArchiveProject":   func() error { return ops.ArchiveProject(ctx, "cli", p.ID) },
		"RestoreProject":   func() error { return ops.RestoreProject(ctx, "cli", p.ID) },
		"DisableIngestKey": func() error { return ops.DisableIngestKey(ctx, "cli", p.ID, "web") },
		"EnableIngestKey":  func() error { return ops.EnableIngestKey(ctx, "cli", p.ID, "web") },
		"DeleteProject":    func() error { return ops.DeleteProject(ctx, "cli", p.ID) },
		"IssueIngestKey": func() error {
			_, err := ops.IssueIngestKey(ctx, "cli", p.ID, "ios")
			return err
		},
	} {
		if err := op(); err == nil {
			t.Errorf("%s on a closed store returned nil, want error", name)
		}
	}
}

// ---- ops.go: ProjectSpec.validate / .row, unexported but same package ----

// A name is required and whitespace does not count.
func TestValidateRequiresAName(t *testing.T) {
	for _, name := range []string{"", "   ", "\t\n"} {
		sp := &ProjectSpec{Name: name}
		if err := sp.validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("validate(Name: %q) = %v, want ErrInvalid", name, err)
		}
	}
}

func TestRowMarshalsOriginsAndAttributes(t *testing.T) {
	// nil origins/attributes -> "[]"
	sp := &ProjectSpec{Name: "a"}
	row, err := sp.row()
	if err != nil {
		t.Fatal(err)
	}
	if row.AllowedOrigins != "[]" || row.Attributes != "[]" {
		t.Fatalf("row = %+v", row)
	}

	// non-nil origins and attributes set, id carried through
	sp2 := &ProjectSpec{ID: 7, Name: "b",
		AllowedOrigins: []string{"https://b.example.com"},
		Attributes:     []string{"plan", "tier"}}
	row2, err := sp2.row()
	if err != nil {
		t.Fatal(err)
	}
	if row2.ID != 7 {
		t.Errorf("ID = %d, want 7", row2.ID)
	}
	if row2.AllowedOrigins != `["https://b.example.com"]` {
		t.Errorf("AllowedOrigins = %q", row2.AllowedOrigins)
	}
	if row2.Attributes != `["plan","tier"]` {
		t.Errorf("Attributes = %q", row2.Attributes)
	}
}

func TestSnippetDefaultsOriginWhenEmpty(t *testing.T) {
	snip := Snippet("", "ak_x")
	if !strings.Contains(snip, "https://twillingate.example.com/js/twillingate.js") {
		t.Errorf("Snippet with empty origin did not fall back to the placeholder host: %s", snip)
	}
}

// ---- registry.go: previously-zero accessors and error branches ----

func TestProjectsAnyOriginAllowedAndAttributesFor(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	blog, err := st.CreateProject(ctx, store.RegistryProject{
		Name:           "blog",
		AllowedOrigins: `["https://blog.example.com/"]`, // trailing slash on this side
		Attributes:     `["plan"]`,
	}, store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := st.CreateProject(ctx, store.RegistryProject{
		Name: "docs", AllowedOrigins: "[]",
	}, store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	reg := New(st, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	s := reg.Snapshot(ctx)

	if got := s.Projects(); len(got) != 2 || got[0].ID != blog || got[1].ID != docs {
		t.Fatalf("Projects() = %+v, want blog then docs by ascending id", got)
	}

	// origin allowed without the trailing slash the caller sends, matching
	// the origin stored with one — trimSlash must run on both sides.
	if !s.AnyOriginAllowed("https://blog.example.com") {
		t.Error("AnyOriginAllowed did not match across a trailing-slash difference")
	}
	if s.AnyOriginAllowed("https://evil.example.com") {
		t.Error("AnyOriginAllowed matched an origin nobody allows")
	}

	attrs := s.AttributesFor(blog)
	if len(attrs) != 1 || attrs[0] != "plan" {
		t.Fatalf("AttributesFor(blog) = %+v", attrs)
	}
	if got := s.AttributesFor(docs); len(got) != 0 {
		t.Fatalf("AttributesFor(docs) = %+v, want empty (no attributes declared)", got)
	}
	if got := s.AttributesFor(404); len(got) != 0 {
		t.Fatalf("AttributesFor(404) = %+v, want nil (unknown id)", got)
	}
}

func TestReloadPropagatesStoreErrors(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(context.Background()); err == nil {
		t.Fatal("Reload on a closed store returned nil, want error")
	}
}

// After a successful Reload, a poll whose ConfigVersion call fails must
// not propagate the error to the caller — the previous snapshot keeps
// serving (spec §3.3: a transient read error must not take down
// ingestion). This exercises the log-and-continue branch in Snapshot.
func TestSnapshotPollErrorKeepsServingPreviousSnapshot(t *testing.T) {
	st := testStore(t)
	id := seedProject(t, st, "blog")
	reg := New(st, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	before := reg.Snapshot(ctx)
	if before.Project(id) == nil {
		t.Fatal("setup: blog missing before closing the store")
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// force the next Snapshot call past the poll interval
	reg.lastCheck.Store(0)
	after := reg.Snapshot(ctx)
	if after.Project(id) == nil {
		t.Fatal("poll error dropped the previous snapshot instead of keeping it")
	}
}

// A malformed JSON blob in a registry column (which the exported API can
// never write, but a hand-edited database or a future bug could produce)
// must surface as an error from Reload rather than a panic or a silently
// empty field.
func TestReloadRejectsCorruptRegistryJSON(t *testing.T) {
	for _, column := range []string{"allowed_origins", "attributes"} {
		t.Run(column, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			id, err := st.CreateProject(ctx, store.RegistryProject{
				Name: "blog", AllowedOrigins: "[]",
			}, store.AuditEntry{Actor: "test", Action: "project.create"})
			if err != nil {
				t.Fatal(err)
			}
			rawExec(t, st, `UPDATE projects SET `+column+` = 'not json' WHERE id = ?`, id)

			reg := New(st, discard())
			if err := reg.Reload(ctx); err == nil {
				t.Error("Reload did not reject corrupt JSON")
			}
		})
	}
}
