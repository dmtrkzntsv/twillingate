package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// groupsOf reads one row's unique_groups. Valid=false is a NULL, which
// after 016 means exactly one thing: the day was rolled up before the
// column existed.
func groupsOf(t *testing.T, db *DB, q string, args ...any) sql.NullInt64 {
	t.Helper()
	var g sql.NullInt64
	if err := db.db.QueryRowContext(context.Background(), q, args...).Scan(&g); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return g
}

// A day rolled up before 016 has no raw rows left to count groups from, so
// its unique_groups is NULL in the table and NULL through the view. A day
// still raw when the migration runs is measured by the live half and, once
// rolled up, keeps that measurement as an integer.
func TestMigration016LeavesHistoryUnmeasured(t *testing.T) {
	db := newTestDBAt(t, 15)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, name, attributes) VALUES (1, 'App', '["plan"]')`,
		`INSERT INTO agg_product_attrs VALUES (1,'2026-09-01','signup','plan','pro',3,2)`,
	} {
		if _, err := db.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migration 016: %v", err)
	}
	if !hasColumn(t, db, "agg_product_attrs", "unique_groups") {
		t.Fatal("agg_product_attrs has no unique_groups column")
	}
	if g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs WHERE day='2026-09-01'`); g.Valid {
		t.Fatalf("pre-016 row unique_groups = %d, want NULL (unmeasured)", g.Int64)
	}
	if g := groupsOf(t, db, `SELECT unique_groups FROM v_product_attrs
		WHERE project_id=1 AND day='2026-09-01' AND attr_key='plan'`); g.Valid {
		t.Fatalf("view shows %d for a pre-016 day, want NULL", g.Int64)
	}

	// A raw day: one event with a group, one without.
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", ProjectID: 1, EventName: "signup", ActorID: "u1", GroupID: "org1",
			TS: ts("2026-09-10T10:00:00Z"), Attributes: map[string]string{"plan": "pro"}},
		{ID: "e2", ProjectID: 1, EventName: "signup", ActorID: "u2",
			TS: ts("2026-09-10T11:00:00Z"), Attributes: map[string]string{"plan": "pro"}},
	}); err != nil {
		t.Fatal(err)
	}
	live := `SELECT unique_groups FROM v_product_attrs
		WHERE project_id=1 AND day='2026-09-10' AND attr_key='plan' AND attr_value='pro'`
	if g := groupsOf(t, db, live); !g.Valid || g.Int64 != 1 {
		t.Fatalf("live unique_groups = %+v, want 1", g)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-09-10"), []string{"plan"}, 50); err != nil {
		t.Fatal(err)
	}
	if g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs
		WHERE day='2026-09-10' AND attr_key='plan' AND attr_value='pro'`); !g.Valid || g.Int64 != 1 {
		t.Fatalf("rolled-up unique_groups = %+v, want 1", g)
	}
	if g := groupsOf(t, db, live); !g.Valid || g.Int64 != 1 {
		t.Fatalf("view after rollup unique_groups = %+v, want 1", g)
	}
}
