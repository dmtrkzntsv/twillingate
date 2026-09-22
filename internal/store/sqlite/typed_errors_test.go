package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Every registry write that rejects a call because of what the caller
// named — a missing id or key, a label already taken on that project —
// must be classifiable with errors.Is, so the MCP and CLI edges can map
// it without matching message text.
func TestRegistryWritesReturnTypedOutcomes(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "test", Action: "test", Subject: "test"}
	blog := store.RegistryProject{Name: "blog", Identity: "anonymous", AllowedOrigins: "[]", Attributes: "[]"}
	id, err := d.CreateProject(ctx, blog, audit)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_1", ProjectID: id, Label: "web"}, audit); err != nil {
		t.Fatal(err)
	}
	const ghost int64 = 7 // never assigned

	notFound := map[string]error{
		"update unknown id":      d.UpdateProject(ctx, store.RegistryProject{ID: ghost, AllowedOrigins: "[]", Attributes: "[]"}, audit),
		"archive unknown id":     d.SetProjectArchived(ctx, ghost, true, audit),
		"disable unknown key":    d.SetIngestKeyDisabled(ctx, id, "ghost", true, audit),
		"disable key of unknown": d.SetIngestKeyDisabled(ctx, ghost, "web", true, audit),
		"delete unknown id":      d.DeleteProjectData(ctx, ghost, audit),
	}
	for name, err := range notFound {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want errors.Is(err, store.ErrNotFound)", name, err)
		}
	}

	err = d.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_2", ProjectID: id, Label: "web"}, audit)
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("issue taken label: err = %v, want errors.Is(err, store.ErrConflict)", err)
	}

	// A repeated name is not a conflict: the id is the identity and the
	// name is a label, so nothing collides.
	if _, err := d.CreateProject(ctx, blog, audit); err != nil {
		t.Errorf("create with a repeated name: err = %v, want nil", err)
	}
}
