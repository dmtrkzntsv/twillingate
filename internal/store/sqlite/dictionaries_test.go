package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func civilDate(t *testing.T, s string) civil.Date {
	t.Helper()
	d, err := civil.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func dayAt(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d.Add(9 * time.Hour)
}

func TestCleanDictionariesKeepsWhatRowsUse(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	write := func(id, d, name, val string) {
		t.Helper()
		if err := db.WriteEvents(ctx, []store.Event{{ID: id, ProjectID: 1, Family: store.FamilyViews, EventName: name,
			TS: dayAt(t, d), ReceivedAt: dayAt(t, d), ActorID: "a", Attributes: map[string]string{"k": val}}}); err != nil {
			t.Fatal(err)
		}
	}
	write("0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e501", "2026-09-01", "old-name", "old-value")
	write("0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e502", "2026-10-10", "new-name", "new-value")
	execAll(t, db, `DELETE FROM events WHERE day = '2026-09-01'`) // the rollup took the old day
	if err := db.CleanDictionaries(ctx, civilDate(t, "2026-10-01")); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT COUNT(*) FROM strings WHERE value = 'old-name'`) != 0 {
		t.Error("old-name kept")
	}
	if count(t, db, `SELECT COUNT(*) FROM strings WHERE value = 'new-name'`) != 1 {
		t.Error("new-name deleted while a row uses it")
	}
	if count(t, db, `SELECT COUNT(*) FROM attribute_values WHERE value = 'old-value'`) != 0 ||
		count(t, db, `SELECT COUNT(*) FROM attribute_values WHERE value = 'new-value'`) != 1 {
		t.Error("values not cleaned by last_seen_day")
	}
	if count(t, db, `SELECT COUNT(*) FROM project_attributes WHERE key = 'k'`) != 1 {
		t.Error("a key carried only by view rows was pruned while those rows exist")
	}
	// What the surviving row decodes to is intact.
	var name, attrs string
	db.db.QueryRow(`SELECT event_name, attributes FROM raw_views`).Scan(&name, &attrs)
	if name != "new-name" || attrs != `{"k":"new-value"}` {
		t.Errorf("surviving row decodes to %q %s", name, attrs)
	}
}

// Review Focus 1: a string cached for a day before the oldest held row but
// inside the raw window survives, so a late event cannot carry a stale id.
func TestCleanDictionariesSparesTheRawWindow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	ts := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	ev := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e503", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "late", TS: ts, ReceivedAt: ts, ActorID: "a", Attributes: map[string]string{"k": "v"}}
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, `DELETE FROM events`, `DELETE FROM usage_attributes`) // a form deletion took the only row
	// raw window starts 2026-10-02: before = 2026-10-01
	if err := db.CleanDictionaries(ctx, civilDate(t, "2026-10-01")); err != nil {
		t.Fatal(err)
	}
	ev.ID = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e504" // same day, cached ids
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	var name, attrs string
	db.db.QueryRow(`SELECT event_name, attributes FROM raw_product WHERE id = ?`, ev.ID).Scan(&name, &attrs)
	if name != "late" || attrs != `{"k":"v"}` {
		t.Errorf("row decodes to %q %s, want late and k=v (a stale id was written)", name, attrs)
	}
}

func TestDeletedStringComesBackWithANewID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	d1 := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	ev := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e505", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "again", TS: d1, ReceivedAt: d1, ActorID: "a", Attributes: map[string]string{"k": "v"}}
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	// The rollup took the day and the usage recount dropped its usage rows.
	execAll(t, db, `DELETE FROM events`, `DELETE FROM usage_attributes`)
	if err := db.CleanDictionaries(ctx, civilDate(t, "2026-10-01")); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT COUNT(*) FROM strings`)+count(t, db, `SELECT COUNT(*) FROM project_attributes`) != 0 {
		t.Fatal("the old day's rows survived the cleanup")
	}
	d2 := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	ev.ID, ev.TS, ev.ReceivedAt = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e506", d2, d2
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	var name, attrs string
	db.db.QueryRow(`SELECT event_name, attributes FROM raw_product WHERE id = ?`, ev.ID).Scan(&name, &attrs)
	if name != "again" || attrs != `{"k":"v"}` {
		t.Errorf("row decodes to %q %s", name, attrs)
	}
}

// The catalog keeps a breakdown, a key some usage row holds, and a key seen
// on or after the project's cutoff; each project's cutoff is its own oldest
// raw day (never later than before), and its usage rows are its own. A
// dropped key takes its values with it.
func TestCleanDictionariesCatalogRules(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]'), (2, 'q', '[]')`,
		`INSERT INTO project_attributes (project_id, key, breakdown, last_seen_day) VALUES
			(1, 'gone', 0, '2026-08-01'),
			(1, 'breakdown', 1, '2026-08-01'),
			(1, 'used', 0, '2026-08-01'),
			(1, 'fresh', 0, '2026-10-01'),
			(2, 'used', 0, '2026-08-01'),
			(2, 'held', 0, '2026-08-10'),
			(2, 'older', 0, '2026-08-04')`,
		// Project 1 received a 'used'; project 2's 'used' has no usage row of its own.
		`INSERT INTO usage_attributes (project_id, day, attr_key, events) VALUES (1, '2026-08-04', 'used', 1)`,
		`INSERT INTO attribute_values (key_id, n, value, last_seen_day)
			SELECT id, 1, 'v', '2026-08-01' FROM project_attributes WHERE project_id = 1 AND key IN ('gone', 'breakdown')`,
	)
	// Project 2 still holds a row from 2026-08-05: its cutoff is that day.
	if err := db.WriteEvents(ctx, []store.Event{{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e507", ProjectID: 2,
		Family: store.FamilyProduct, EventName: "e", TS: dayAt(t, "2026-08-05"), ReceivedAt: dayAt(t, "2026-08-05"), ActorID: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.CleanDictionaries(ctx, civilDate(t, "2026-10-01")); err != nil {
		t.Fatal(err)
	}
	if got, want := catalogRows(t, db), []string{"1/breakdown=1", "1/fresh=0", "1/used=0", "2/held=0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
	}
	// 'gone' went with its value; the breakdown's old value is aged out by its own day.
	if n := count(t, db, `SELECT COUNT(*) FROM attribute_values`); n != 0 {
		t.Errorf("attribute_values holds %d rows, want 0", n)
	}
}

// Two passes in a row change nothing the first did not.
func TestCleanDictionariesIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	if err := db.WriteEvents(ctx, []store.Event{{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e508", ProjectID: 1,
		Family: store.FamilyViews, EventName: "e", Kind: "web", TS: dayAt(t, "2026-10-10"), ActorID: "a",
		Attributes: map[string]string{"k": "v"}}}); err != nil {
		t.Fatal(err)
	}
	before := civilDate(t, "2026-10-01")
	for i := 0; i < 2; i++ {
		if err := db.CleanDictionaries(ctx, before); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM strings`); n != 2 { // e, web
		t.Errorf("strings = %d, want 2", n)
	}
}
