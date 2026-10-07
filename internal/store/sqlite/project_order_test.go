package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
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

// A new project takes the key after the largest, so it comes last;
// SetProjectSortKeys reorders, audits and bumps the version once.
func TestProjectSortKeys(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	ids := createProjects(t, d, "a", "b", "c")
	if got := registryOrder(t, d); !reflect.DeepEqual(got, ids) {
		t.Fatalf("order after create = %v, want %v", got, ids)
	}
	a, b, c := ids[0], ids[1], ids[2]
	ps, _, err := d.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].SortKey != "a0" || ps[1].SortKey != "a1" || ps[2].SortKey != "a2" {
		t.Fatalf("keys after create = %q %q %q, want a0 a1 a2", ps[0].SortKey, ps[1].SortKey, ps[2].SortKey)
	}
	v0, _ := d.ConfigVersion(ctx)
	if err := d.SetProjectSortKeys(ctx, []store.ProjectSortKey{{ID: c, SortKey: "Zz"}},
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
	// After a2, the largest key: last.
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
	err := d.SetProjectSortKeys(ctx, []store.ProjectSortKey{{ID: ids[1], SortKey: "Zz"}, {ID: 999, SortKey: "a1"}}, store.AuditEntry{})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: %v, want ErrNotFound", err)
	}
	if got := registryOrder(t, d); !reflect.DeepEqual(got, ids) {
		t.Errorf("order = %v, want %v untouched", got, ids)
	}
}

// 034 keys the projects in id order with the keys sortkey hands out when
// appending, across both length changes (after 62 and after 62 + 3,844
// projects): each key is valid, the order is by id, and a project created
// afterwards still comes last.
func TestMigration034KeysProjectsInIDOrder(t *testing.T) {
	db := newTestDBAt(t, 33)
	const n = 3910
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Ids descending against insertion order, so the ranking must read id.
	for i := n; i >= 1; i-- {
		if _, err := tx.Exec(`INSERT INTO projects (id, name, allowed_origins) VALUES (?, 'p', '[]')`, i*2); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateThrough(context.Background(), 34); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT id, sort_key FROM projects ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for rows.Next() {
		var id int64
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	rows.Close()
	want := ""
	for i, k := range keys {
		next, err := sortkey.Between(want, "")
		if err != nil {
			t.Fatal(err)
		}
		if k != next {
			t.Fatalf("project %d of %d: key %q, want %q (the next sortkey appends)", i+1, n, k, next)
		}
		want = k
	}
	if keys[61] != "az" || keys[62] != "b00" || keys[3905] != "bzz" || keys[3906] != "c000" {
		t.Errorf("boundary keys = %q %q %q %q", keys[61], keys[62], keys[3905], keys[3906])
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateProject(context.Background(), store.RegistryProject{Name: "new", AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	if got := registryOrder(t, db); got[len(got)-1] != id {
		t.Errorf("new project %d not last: ... %v", id, got[len(got)-3:])
	}
}
