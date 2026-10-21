package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// ProjectIDs returns every registry row including archived ones: the
// import (internal/app) copies an archived project's events too.
func TestProjectIDsIncludesArchived(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "test", Action: "project.create"}
	a, err := db.CreateProject(ctx, store.RegistryProject{Name: "A", AllowedOrigins: "[]"}, audit)
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateProject(ctx, store.RegistryProject{Name: "B", AllowedOrigins: "[]"}, audit)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetProjectArchived(ctx, b, true, store.AuditEntry{Actor: "test", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}

	ids, err := db.ProjectIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != a || ids[1] != b {
		t.Fatalf("ProjectIDs = %v, want [%d %d] (archived rows included)", ids, a, b)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if v, err := db.GetMeta(ctx, "missing"); err != nil || v != "" {
		t.Fatalf("missing key: %q %v", v, err)
	}
	if err := db.SetMeta(ctx, "salt", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta(ctx, "salt", "s2"); err != nil {
		t.Fatal(err) // overwrite
	}
	if v, _ := db.GetMeta(ctx, "salt"); v != "s2" {
		t.Fatalf("got %q", v)
	}
}
