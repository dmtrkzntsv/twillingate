package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Migration 029 points the views breakdowns at the attribute cap: at 28
// v_views_paths folds past views_dimensions_top_n (1 here), after 029 past
// attributes_top_n (2 here), and the views_dimensions_top_n row is gone.
// No view reads the old key any more.
func TestMigration029ViewsReadTheAttributeCap(t *testing.T) {
	db := newTestDBAt(t, 28)
	ctx := context.Background()
	for key, value := range map[string]string{"views_dimensions_top_n": "1", "attributes_top_n": "2"} {
		if err := db.SetMeta(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	id := seedDeclaredProject(t, db, nil)
	var evs []store.Event
	for i, path := range []string{"/a", "/a", "/a", "/b", "/b", "/c"} {
		ts := time.Date(2026, 8, 2, 10, i, 0, 0, time.UTC)
		evs = append(evs, store.Event{Family: store.FamilyViews, ID: fmt.Sprintf("v%d", i),
			ProjectID: id, TS: ts, ReceivedAt: ts, Kind: "web",
			ActorID: fmt.Sprintf("a%d", i), ActorKind: store.ActorConnection,
			Host: "a.example", Path: path})
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	paths := func() []string {
		t.Helper()
		rows, err := db.db.Query(`SELECT path, views FROM v_views_paths WHERE project_id = ? ORDER BY path`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var p string
			var n int
			if err := rows.Scan(&p, &n); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s=%d", p, n))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got, want := paths(), []string{"(other)=3", "/a=3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("at 28 v_views_paths = %v, want %v (views_dimensions_top_n = 1)", got, want)
	}

	if err := db.migrateThrough(ctx, 29); err != nil {
		t.Fatal(err)
	}
	if got, want := paths(), []string{"(other)=1", "/a=3", "/b=2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after 029 v_views_paths = %v, want %v (attributes_top_n = 2)", got, want)
	}
	if v, err := db.GetMeta(ctx, "views_dimensions_top_n"); err != nil || v != "" {
		t.Errorf("views_dimensions_top_n = %q (%v), want it gone", v, err)
	}
	var stale []string
	rows, err := db.db.Query(`SELECT name FROM sqlite_master WHERE type = 'view' AND sql LIKE '%views_dimensions_top_n%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		stale = append(stale, n)
	}
	if len(stale) > 0 {
		t.Errorf("views still reading views_dimensions_top_n after 029: %v", stale)
	}
}
