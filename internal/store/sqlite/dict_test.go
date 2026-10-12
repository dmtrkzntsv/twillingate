package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestWriteEventsReadsBackAsSent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	ts := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	ev := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "signup", TS: ts, ReceivedAt: ts, Kind: "app", ActorID: "0192f1a27c3b7d4e8f90a1b2c3d4e5f6",
		ActorKind: "user", UserID: "user@example.com", Platform: "ios",
		Attributes: map[string]string{"plan": "pro", "seats": "3"}}
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil { // retry: ignored
		t.Fatal(err)
	}
	var n int
	var id, actor, user, name, tsText, attrs string
	db.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	db.db.QueryRow(`SELECT id, actor_id, user_id, event_name, ts, attributes FROM raw_product`).
		Scan(&id, &actor, &user, &name, &tsText, &attrs)
	// Key order in the decoded attributes is not contract (spec D6).
	var parsed map[string]string
	if err := json.Unmarshal([]byte(attrs), &parsed); err != nil {
		t.Fatalf("attributes %q: %v", attrs, err)
	}
	if n != 1 || id != ev.ID || actor != ev.ActorID || user != ev.UserID || name != "signup" ||
		tsText != "2026-10-11T09:00:00Z" || !reflect.DeepEqual(parsed, ev.Attributes) {
		t.Errorf("got n=%d id=%s actor=%s user=%s name=%s ts=%s attrs=%s", n, id, actor, user, name, tsText, attrs)
	}
	// Each text got its dictionary row with the row's day, and the key and
	// value theirs.
	var seen string
	db.db.QueryRow(`SELECT last_seen_day FROM strings WHERE value = 'signup'`).Scan(&seen)
	if seen != "2026-10-11" {
		t.Errorf("strings.last_seen_day = %q", seen)
	}
	var keySeen, valSeen string
	var valN int
	db.db.QueryRow(`SELECT k.last_seen_day, v.last_seen_day, v.n FROM project_attributes k
		JOIN attribute_values v ON v.key_id = k.id WHERE k.project_id = 1 AND k.key = 'plan' AND v.value = 'pro'`).
		Scan(&keySeen, &valSeen, &valN)
	if keySeen != "2026-10-11" || valSeen != "2026-10-11" || valN != 1 {
		t.Errorf("plan=pro: key seen %q, value seen %q, n %d", keySeen, valSeen, valN)
	}
}

// A later day raises last_seen_day once, and a new value of a known key
// takes the key's next number.
func TestWriteEventsRaisesLastSeenDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	d1 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	ev := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e501", ProjectID: 1, Family: store.FamilyViews,
		EventName: "$page_view", TS: d1, ReceivedAt: d1, ActorID: "a", Attributes: map[string]string{"k": "v1"}}
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	d2 := d1.AddDate(0, 0, 1)
	ev.ID, ev.TS, ev.ReceivedAt, ev.Attributes = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e502", d2, d2, map[string]string{"k": "v2"}
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	var seen, keySeen string
	db.db.QueryRow(`SELECT last_seen_day FROM strings WHERE value = '$page_view'`).Scan(&seen)
	db.db.QueryRow(`SELECT last_seen_day FROM project_attributes WHERE key = 'k'`).Scan(&keySeen)
	if seen != "2026-10-11" || keySeen != "2026-10-11" {
		t.Errorf("last_seen_day: string %q, key %q, want 2026-10-11", seen, keySeen)
	}
	var n int
	db.db.QueryRow(`SELECT n FROM attribute_values WHERE value = 'v2'`).Scan(&n)
	if n != 2 {
		t.Errorf("v2 numbered %d, want 2", n)
	}
	// An older day leaves last_seen_day where it is.
	ev.ID, ev.TS, ev.ReceivedAt = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e503", d1, d1
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	db.db.QueryRow(`SELECT last_seen_day FROM attribute_values WHERE value = 'v2'`).Scan(&seen)
	if seen != "2026-10-11" {
		t.Errorf("v2 last_seen_day lowered to %q", seen)
	}
}

// Review Focus 2: ids resolved in a batch that rolls back must not stay
// cached, or a later batch would write an id with no row.
func TestWriteEventsRollbackForgetsIDs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	ts := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	good := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "fresh-name", TS: ts, ReceivedAt: ts, ActorID: "a"}
	bad := good
	bad.ID, bad.Family = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fb", "nope"
	// A batch refused before anything is written resolves nothing.
	if err := db.WriteEvents(ctx, []store.Event{good, bad}); err == nil {
		t.Fatal("want the bad family refused")
	}
	// Force a failure mid-transaction: a trigger refusing the second insert,
	// after the first resolved its name and the second "another-fresh".
	execAll(t, db, `CREATE TEMP TRIGGER refuse BEFORE INSERT ON events WHEN NEW.id = x'0192f1a27c3b7d4e8f90a1b2c3d4e5fc'
		BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	third := good
	third.ID, third.EventName = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fc", "another-fresh"
	if err := db.WriteEvents(ctx, []store.Event{good, third}); err == nil {
		t.Fatal("want the trigger to refuse")
	}
	execAll(t, db, `DROP TRIGGER refuse`)
	if err := db.WriteEvents(ctx, []store.Event{third}); err != nil {
		t.Fatal(err)
	}
	var name string
	db.db.QueryRow(`SELECT event_name FROM raw_product WHERE id = ?`, third.ID).Scan(&name)
	if name != "another-fresh" {
		t.Errorf("event_name = %q: a rolled-back id stayed cached", name)
	}
}

// Review Focus 5: a project purged while its events were buffered loses
// them; the batch still commits for the others.
func TestWriteEventsDropsPurgedProjectRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	ts := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	kept := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "e", TS: ts, ReceivedAt: ts, ActorID: "a", Attributes: map[string]string{"k": "v"}}
	gone := kept
	gone.ID, gone.ProjectID = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fb", 99
	if err := db.WriteEvents(ctx, []store.Event{kept, gone}); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	if n != 1 {
		t.Errorf("rows = %d, want only project 1's", n)
	}
}

// The event rename rules apply in Go now, read in the flush's transaction.
func TestWriteEventsAppliesEventRename(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`,
		`INSERT INTO rules (project_id, kind, family, name, action, to_name) VALUES (1, 'event', 'product', 'old', 'rename', 'new')`)
	ts := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	evs := []store.Event{
		{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa", ProjectID: 1, Family: store.FamilyProduct, EventName: "old", TS: ts, ActorID: "a"},
		{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fb", ProjectID: 1, Family: store.FamilyViews, EventName: "old", TS: ts, ActorID: "a"},
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	got := allRows(t, db, `SELECT family, event_name FROM (SELECT * FROM raw_product UNION ALL SELECT * FROM raw_views) ORDER BY family`)
	if len(got) != 2 || got[0][1] != "new" || got[1][1] != "old" {
		t.Errorf("rows = %v, want product renamed and views as sent", got)
	}
}

// A rename resolves its target with the newest day it rewrote as
// last_seen_day (spec D7.5), so the cleanup keeps the target while those
// rows exist; values get their numbers under the new key.
func TestRenamesRaiseTargetsLastSeenDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	var evs []store.Event
	for i, d := range []string{"2026-10-08", "2026-10-10"} {
		at, _ := time.Parse("2006-01-02", d)
		evs = append(evs, store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e50" + string(rune('1'+i)), ProjectID: 1,
			Family: store.FamilyProduct, EventName: "old", TS: at.Add(9 * time.Hour), ActorID: "a",
			Attributes: map[string]string{"from": "v" + d}})
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenameEvent(ctx, 1, store.FamilyProduct, "old", "new", false, ruleAudit); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenameAttribute(ctx, 1, "from", "to", false, attrAudit); err != nil {
		t.Fatal(err)
	}
	if got := allRows(t, db, `SELECT last_seen_day FROM strings WHERE value = 'new'`); len(got) != 1 || got[0][0] != "2026-10-10" {
		t.Errorf("new: %v, want last_seen_day 2026-10-10", got)
	}
	if got := allRows(t, db, `SELECT k.last_seen_day, v.value, v.last_seen_day FROM project_attributes k
		JOIN attribute_values v ON v.key_id = k.id WHERE k.key = 'to' ORDER BY v.value`); !reflect.DeepEqual(got, [][]string{
		{"2026-10-10", "v2026-10-08", "2026-10-08"}, {"2026-10-10", "v2026-10-10", "2026-10-10"}}) {
		t.Errorf("to: %v", got)
	}
	got := allRows(t, db, `SELECT event_name, attributes FROM raw_product ORDER BY id`)
	if len(got) != 2 || got[0][0] != "new" || got[0][1] != `{"to":"v2026-10-08"}` || got[1][1] != `{"to":"v2026-10-10"}` {
		t.Errorf("rows = %v", got)
	}
}
