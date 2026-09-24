package sqlite

import (
	"context"
	"testing"
)

// 017 drops projects.identity. The row's other fields survive, a fresh
// database never has the column, and an anonymous project's raw rows that
// were hashed to actor_kind user/install (a client sent $user_id or
// $install_id under the old anonymous mode) are re-kinded to connection so
// the daily pass never turns a rotating hash into a cohort; an identified
// project's rows are left alone.
func TestMigration017DropsIdentity(t *testing.T) {
	db := newTestDBAt(t, 16)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO projects (id, name, identity, allowed_origins, attributes)
		 VALUES (1, 'Blog', 'identified', '["https://blog.example.com"]', '["plan"]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO projects (id, name, identity, allowed_origins, attributes)
		 VALUES (2, 'Site', 'anonymous', '[]', '[]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `
INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind, user_id, path) VALUES
 ('v1',1,'2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','web','u1','user','u1','/'),
 ('v2',2,'2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','web','a1b2c3d4e5f60718','user','a1b2c3d4e5f60718','/'),
 ('v3',2,'2026-09-10T10:01:00Z','2026-09-10T10:01:00Z','app','0f1e2d3c4b5a6978','install','','/home'),
 ('v4',2,'2026-09-10T10:02:00Z','2026-09-10T10:02:00Z','web','9988776655443322','connection','','/')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `
INSERT INTO events (id, project_id, event_name, actor_id, actor_kind, user_id, ts) VALUES
 ('e1',1,'signup','u1','user','u1','2026-09-10T10:00:00Z'),
 ('e2',2,'signup','a1b2c3d4e5f60718','user','a1b2c3d4e5f60718','2026-09-10T10:00:00Z')`); err != nil {
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
	if len(ps) != 2 || ps[0].ID != 1 || ps[0].Name != "Blog" ||
		ps[0].AllowedOrigins != `["https://blog.example.com"]` || ps[0].Attributes != `["plan"]` {
		t.Fatalf("registry after 017 = %+v", ps)
	}

	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM views WHERE project_id=1 AND actor_kind='user'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("project 1 views with actor_kind='user' = %d, want 1", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM events WHERE project_id=1 AND actor_kind='user'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("project 1 events with actor_kind='user' = %d, want 1", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM views WHERE project_id=2 AND actor_kind='connection'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("project 2 views with actor_kind='connection' = %d, want 3", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM events WHERE project_id=2 AND actor_kind='connection'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("project 2 events with actor_kind='connection' = %d, want 1", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM views WHERE project_id=2 AND actor_kind IN ('user','install')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("project 2 views still have user/install actor_kind: %d", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM events WHERE project_id=2 AND actor_kind IN ('user','install')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("project 2 events still have user/install actor_kind: %d", n)
	}
	var userID string
	if err := db.db.QueryRow(`SELECT user_id FROM views WHERE id='v2'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if userID != "a1b2c3d4e5f60718" {
		t.Fatalf("v2 user_id changed: %q", userID)
	}
}

func TestFreshDatabaseHasNoIdentityColumn(t *testing.T) {
	db := newTestDB(t)
	if hasColumn(t, db, "projects", "identity") {
		t.Fatal("a fresh database has projects.identity")
	}
}
