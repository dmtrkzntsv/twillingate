package sqlite

import (
	"context"
	"testing"
)

// 017 drops projects.identity. The row's other fields survive, and a
// fresh database never has the column.
func TestMigration017DropsIdentity(t *testing.T) {
	db := newTestDBAt(t, 16)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO projects (id, name, identity, allowed_origins, attributes)
		 VALUES (1, 'Blog', 'identified', '["https://blog.example.com"]', '["plan"]')`); err != nil {
		t.Fatal(err)
	}
	if !hasColumn(t, db, "projects", "identity") {
		t.Fatal("schema 016 must still have projects.identity")
	}
	if err := db.migrateThrough(ctx, 17); err != nil {
		t.Fatalf("migration 017: %v", err)
	}
	if hasColumn(t, db, "projects", "identity") {
		t.Fatal("projects.identity survived migration 017")
	}
	ps, _, err := db.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].ID != 1 || ps[0].Name != "Blog" ||
		ps[0].AllowedOrigins != `["https://blog.example.com"]` || ps[0].Attributes != `["plan"]` {
		t.Fatalf("registry after 017 = %+v", ps)
	}
}

func TestFreshDatabaseHasNoIdentityColumn(t *testing.T) {
	db := newTestDB(t)
	if hasColumn(t, db, "projects", "identity") {
		t.Fatal("a fresh database has projects.identity")
	}
}
