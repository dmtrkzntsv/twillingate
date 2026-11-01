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
		"update to an empty origin": func() error {
			_, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: blog.ID, AllowedOrigins: []string{""}})
			return err
		}(),
	}
	for name, err := range invalid {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want errors.Is(err, ErrInvalid)", name, err)
		}
		// The sentinel leads so the CLI line reads as one sentence.
		if err != nil && !strings.HasPrefix(err.Error(), "invalid: ") {
			t.Errorf("%s: err = %q, want the 'invalid: ' prefix", name, err)
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

	// Every operation on a project names an unknown one the same way.
	unknown := map[string]error{
		"update project": func() error {
			_, err := ops.UpdateProject(ctx, "cli", ProjectSpec{ID: 404})
			return err
		}(),
		"issue key": func() error {
			_, err := ops.IssueIngestKey(ctx, "cli", 404, "web")
			return err
		}(),
		"list event rules": func() error {
			_, err := ops.ListEventRules(ctx, 404)
			return err
		}(),
		"block event": func() error {
			_, err := ops.BlockEvent(ctx, "cli", 404, "product", "signup")
			return err
		}(),
		"block attribute": func() error {
			_, err := ops.BlockAttribute(ctx, "cli", 404, "plan")
			return err
		}(),
		"list forms": func() error {
			_, err := ops.ListForms(ctx, 404, false)
			return err
		}(),
		"update form": ops.UpdateForm(ctx, "cli", 404, "contact", FormSpec{}),
	}
	for name, err := range unknown {
		if !errors.Is(err, ErrNotFound) || err.Error() != "unknown project 404: not found" {
			t.Errorf("%s: err = %v, want ErrNotFound reading %q", name, err, "unknown project 404: not found")
		}
	}

	// Names are not unique, so the only conflict left is a key label
	// already taken for that project.
	if _, err := ops.IssueIngestKey(ctx, "cli", blog.ID, "web"); !errors.Is(err, ErrConflict) {
		t.Errorf("issue taken label: err = %v, want errors.Is(err, ErrConflict)", err)
	}
}
