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

// Keyed projects list by key, then unkeyed ones by id: a new project,
// created unkeyed, comes last. SetProjectSortKeys audits and bumps the
// version once.
func TestProjectSortKeys(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	ids := createProjects(t, d, "a", "b", "c")
	if got := registryOrder(t, d); !reflect.DeepEqual(got, ids) {
		t.Fatalf("order after create = %v, want %v", got, ids)
	}
	a, b, c := ids[0], ids[1], ids[2]
	v0, _ := d.ConfigVersion(ctx)
	if err := d.SetProjectSortKeys(ctx, []store.ProjectSortKey{{ID: c, SortKey: "a0"}, {ID: a, SortKey: "a1"}, {ID: b, SortKey: "a2"}},
		store.AuditEntry{Actor: "api", Action: "project.move"}); err != nil {
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
	ps, _, err := d.LoadRegistry(ctx)
	if err != nil || ps[0].SortKey != "a0" {
		t.Errorf("first project's key = %q, %v; want a0", ps[0].SortKey, err)
	}

	// A new project is unkeyed: last, after every keyed one.
	dd := createProjects(t, d, "d")[0]
	if got := registryOrder(t, d); !reflect.DeepEqual(got, []int64{c, a, b, dd}) {
		t.Errorf("order after create = %v, want d last", got)
	}
}

// An unknown id is not_found and writes no key, the known ones included.
func TestProjectSortKeysUnknown(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	ids := createProjects(t, d, "a", "b")
	err := d.SetProjectSortKeys(ctx, []store.ProjectSortKey{{ID: ids[1], SortKey: "a0"}, {ID: 999, SortKey: "a1"}}, store.AuditEntry{})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: %v, want ErrNotFound", err)
	}
	if got := registryOrder(t, d); !reflect.DeepEqual(got, ids) {
		t.Errorf("order = %v, want %v untouched", got, ids)
	}
}

// After 034 every project is unkeyed, so the order stays by id.
func TestMigration034ProjectOrder(t *testing.T) {
	db := newTestDBAt(t, 33)
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (7, 'x', '[]'), (3, 'y', '[]'), (5, 'z', '[]')`)
	if err := db.migrateThrough(context.Background(), 34); err != nil {
		t.Fatal(err)
	}
	if got := registryOrder(t, db); !reflect.DeepEqual(got, []int64{3, 5, 7}) {
		t.Errorf("order = %v, want by id", got)
	}
}
