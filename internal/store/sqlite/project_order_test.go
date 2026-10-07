package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// registryOrder is the project ids in LoadRegistry's order.
func registryOrder(t *testing.T, d *DB) []int64 {
	t.Helper()
	ps, _, err := d.LoadRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return ids
}

func createProjects(t *testing.T, d *DB, names ...string) []int64 {
	t.Helper()
	var ids []int64
	for _, n := range names {
		id, err := d.CreateProject(context.Background(), store.RegistryProject{Name: n, AllowedOrigins: "[]", Attributes: "[]"},
			store.AuditEntry{Actor: "cli", Action: "project.create"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// A new project comes last; SetProjectOrder puts the listed projects
// first in that order, audits and bumps the version, and a project it
// does not list (created after the order was read) keeps its place after
// them.
func TestSetProjectOrder(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	ids := createProjects(t, d, "a", "b", "c")
	if got := registryOrder(t, d); !reflect.DeepEqual(got, ids) {
		t.Fatalf("order after create = %v, want %v", got, ids)
	}
	v0, _ := d.ConfigVersion(ctx)
	a, b, c := ids[0], ids[1], ids[2]
	if err := d.SetProjectOrder(ctx, []int64{c, a, b}, store.AuditEntry{Actor: "api", Action: "project.move", Subject: "3"}); err != nil {
		t.Fatal(err)
	}
	if got := registryOrder(t, d); !reflect.DeepEqual(got, []int64{c, a, b}) {
		t.Errorf("order = %v, want %v", got, []int64{c, a, b})
	}
	if v1, _ := d.ConfigVersion(ctx); v1 != v0+1 {
		t.Errorf("config_version %d -> %d, want one bump", v0, v1)
	}
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'project.move'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows = %d, %v; want 1", n, err)
	}

	// d arrives after the order was read: the stale order moves b first
	// and d stays after the three listed.
	dd := createProjects(t, d, "d")[0]
	if got := registryOrder(t, d); !reflect.DeepEqual(got, []int64{c, a, b, dd}) {
		t.Fatalf("order after create = %v, want d last", got)
	}
	if err := d.SetProjectOrder(ctx, []int64{b, c, a}, store.AuditEntry{Actor: "api", Action: "project.move"}); err != nil {
		t.Fatal(err)
	}
	if got := registryOrder(t, d); !reflect.DeepEqual(got, []int64{b, c, a, dd}) {
		t.Errorf("order = %v, want %v", got, []int64{b, c, a, dd})
	}
}

// An unknown id is not_found and a repeated one invalid; neither moves
// anything.
func TestSetProjectOrderRefusals(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	ids := createProjects(t, d, "a", "b")
	if err := d.SetProjectOrder(ctx, []int64{ids[1], 999}, store.AuditEntry{}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: %v, want ErrNotFound", err)
	}
	if err := d.SetProjectOrder(ctx, []int64{ids[1], ids[1]}, store.AuditEntry{}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("repeated id: %v, want ErrInvalid", err)
	}
	if got := registryOrder(t, d); !reflect.DeepEqual(got, ids) {
		t.Errorf("order = %v, want %v untouched", got, ids)
	}
}

// 034 keeps today's order, projects by id, and its trigger puts a
// project inserted afterwards last, whoever inserts it.
func TestMigration034ProjectOrder(t *testing.T) {
	db := newTestDBAt(t, 33)
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (7, 'x', '[]'), (3, 'y', '[]'), (5, 'z', '[]')`)
	if err := db.migrateThrough(context.Background(), 34); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, `UPDATE projects SET position = 100 WHERE id = 3`,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (4, 'w', '[]')`)
	rows, err := db.db.Query(`SELECT id FROM projects ORDER BY position, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if !reflect.DeepEqual(got, []int64{5, 7, 3, 4}) {
		t.Errorf("order = %v, want 5 7 (by id), 3 (moved to 100), then the new 4", got)
	}
}
