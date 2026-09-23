package manage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOpsRefusalsAreTyped(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	blog, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "blog"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.IssueIngestKey(ctx, "cli", blog.ID, "web"); err != nil {
		t.Fatal(err)
	}

	invalid := map[string]error{
		"empty name": func() error {
			_, err := ops.CreateProject(ctx, "cli", ProjectSpec{})
			return err
		}(),
		"empty origin": func() error {
			_, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "x", AllowedOrigins: []string{""}})
			return err
		}(),
	}
	for name, err := range invalid {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want errors.Is(err, ErrInvalid)", name, err)
		}
		// The sentinel leads so the CLI line reads as one sentence.
		if err != nil && !strings.HasPrefix(err.Error(), "invalid project spec: ") {
			t.Errorf("%s: err = %q, want the 'invalid project spec: ' prefix", name, err)
		}
	}

	notFound := map[string]error{
		"issue key for unknown project": func() error {
			_, err := ops.IssueIngestKey(ctx, "cli", 404, "web")
			return err
		}(),
		"update unknown project": func() error {
			_, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: 404})
			return err
		}(),
		"archive unknown project": ops.ArchiveProject(ctx, "cli", 404),
		"disable unknown key":     ops.DisableIngestKey(ctx, "cli", blog.ID, "ghost"),
		"delete unknown project":  ops.DeleteProject(ctx, "cli", 404),
	}
	for name, err := range notFound {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want errors.Is(err, ErrNotFound)", name, err)
		}
	}

	// Names are not unique, so the only conflict left is a key label
	// already taken for that project.
	if _, err := ops.IssueIngestKey(ctx, "cli", blog.ID, "web"); !errors.Is(err, ErrConflict) {
		t.Errorf("issue taken label: err = %v, want errors.Is(err, ErrConflict)", err)
	}
}
