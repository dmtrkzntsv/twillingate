package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// formPositions is project 1's forms as name:position, by position.
func formPositions(t *testing.T, db *DB) []string {
	t.Helper()
	return queryStrings(t, db, `SELECT name || ':' || position FROM forms WHERE project_id=1 ORDER BY position, name`)
}

func activeFormNames(t *testing.T, db *DB) []string {
	t.Helper()
	fs, err := db.ListForms(context.Background(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, f := range fs {
		names = append(names, f.Name)
	}
	return names
}

func newApprovedForm(name string) store.Form {
	at := formsNow
	return store.Form{ProjectID: 1, Name: name, Status: store.FormApproved, Purpose: "p",
		Fields: []string{"email"}, ExpectedFields: []string{"email"}, CreatedAt: formsNow, ApprovedAt: &at}
}

// Migration 046 numbers every project's forms in the order list_forms
// showed them: live before archived, each drafts first, then by name.
func TestMigration046KeepsTheListOrder(t *testing.T) {
	db := newTestDBAt(t, 45)
	ctx := context.Background()
	execAll(t, db,
		`INSERT INTO forms (project_id, name, status, created_at, archived_at) VALUES
			(1, 'zeta', 'approved', '2026-10-01T00:00:00Z', NULL),
			(1, 'beta', 'draft', '2026-10-02T00:00:00Z', NULL),
			(1, 'alpha', 'approved', '2026-10-03T00:00:00Z', NULL),
			(1, 'yank', 'draft', '2026-10-04T00:00:00Z', NULL),
			(1, 'old', 'approved', '2026-09-01T00:00:00Z', '2026-10-05T00:00:00Z'),
			(1, 'gone', 'draft', '2026-09-02T00:00:00Z', '2026-10-05T00:00:00Z'),
			(2, 'other', 'approved', '2026-10-01T00:00:00Z', NULL)`)
	if err := db.migrateThrough(ctx, 46); err != nil {
		t.Fatal(err)
	}
	want := []string{"beta:1", "yank:2", "alpha:3", "zeta:4", "gone:5", "old:6"}
	if got := formPositions(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("positions = %q, want %q", got, want)
	}
	if got := queryStrings(t, db, `SELECT name || ':' || position FROM forms WHERE project_id=2`); !reflect.DeepEqual(got, []string{"other:1"}) {
		t.Errorf("project 2 = %q", got)
	}
	if got := activeFormNames(t, db); !reflect.DeepEqual(got, []string{"beta", "yank", "alpha", "zeta"}) {
		t.Errorf("ListForms = %q", got)
	}
}

// A draft a first submission creates comes after every form of its
// project, archived ones included.
func TestWriteSubmissionAppendsTheDraftLast(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.CreateForm(ctx, newApprovedForm("first"), formAudit); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateForm(ctx, newApprovedForm("aside"), formAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFormArchived(ctx, 1, "aside", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, db, newSub("s1", map[string]string{"email": "a@x.io"}))
	if got := formPositions(t, db); !reflect.DeepEqual(got, []string{"first:1", "aside:2", "contact:3"}) {
		t.Errorf("positions = %q", got)
	}
	// Another submission leaves the position alone.
	n := newSub("s2", map[string]string{"email": "b@x.io"})
	mustWrite(t, db, n)
	if got := activeFormNames(t, db); !reflect.DeepEqual(got, []string{"first", "contact"}) {
		t.Errorf("ListForms = %q", got)
	}
	// Another project's forms do not count.
	other := newSub("o1", map[string]string{"k": "v"})
	other.Submission.ProjectID, other.Event.ProjectID = 2, 2
	mustWrite(t, db, other)
	if got := queryStrings(t, db, `SELECT position FROM forms WHERE project_id=2`); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("project 2 position = %q", got)
	}
}

func TestCreateFormStoresAndAudits(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	in := newApprovedForm("signup")
	in.ReturnURL = "https://example.com/thanks"
	in.ExpectedFields = []string{"name", "email"}
	in.Fields = []string{"email", "name"}
	got, err := db.CreateForm(ctx, in, store.AuditEntry{Actor: "t", Action: "form.create", Subject: "form/1/signup"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FormApproved || got.DraftUntil != nil || got.ApprovedAt == nil || !got.ApprovedAt.Equal(formsNow) ||
		!got.CreatedAt.Equal(formsNow) || got.ReturnURL != in.ReturnURL || got.Purpose != "p" ||
		!reflect.DeepEqual(got.ExpectedFields, []string{"name", "email"}) || !reflect.DeepEqual(got.Fields, []string{"email", "name"}) {
		t.Fatalf("created = %+v", got)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='form.create' AND subject='form/1/signup'`); n != 1 {
		t.Errorf("audit rows = %d", n)
	}
	// The name is taken, archived or not; a refusal audits nothing.
	if _, err := db.CreateForm(ctx, in, formAudit); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
	if err := db.SetFormArchived(ctx, 1, "signup", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateForm(ctx, in, formAudit); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate of an archived form: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='form.test'`); n != 1 {
		t.Errorf("audit rows after refusals = %d, want only the archive's", n)
	}
}

func TestMoveFormRenumbers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b", "c", "d"} {
		if _, err := db.CreateForm(ctx, newApprovedForm(name), formAudit); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetFormArchived(ctx, 1, "b", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}
	move := store.AuditEntry{Actor: "t", Action: "form.move", Subject: "form/1/d"}
	if err := db.MoveForm(ctx, 1, "d", "", move); err != nil {
		t.Fatal(err)
	}
	// The live forms in their new order, then the archived one.
	if got := formPositions(t, db); !reflect.DeepEqual(got, []string{"d:1", "a:2", "c:3", "b:4"}) {
		t.Errorf("d first: %q", got)
	}
	if err := db.MoveForm(ctx, 1, "d", "c", move); err != nil {
		t.Fatal(err)
	}
	if got := formPositions(t, db); !reflect.DeepEqual(got, []string{"a:1", "c:2", "d:3", "b:4"}) {
		t.Errorf("d after c: %q", got)
	}
	if err := db.MoveForm(ctx, 1, "a", "a", move); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='form.move'`); n != 2 {
		t.Errorf("audit rows = %d, want 2 (a move after itself writes none)", n)
	}
	for _, c := range []struct {
		name, after string
		want        error
	}{
		{"nope", "", store.ErrNotFound},
		{"a", "nope", store.ErrNotFound},
		{"b", "", store.ErrInvalid},
		{"a", "b", store.ErrInvalid},
	} {
		if err := db.MoveForm(ctx, 1, c.name, c.after, move); !errors.Is(err, c.want) {
			t.Errorf("move %s after %q: %v, want %v", c.name, c.after, err, c.want)
		}
	}
	if err := db.MoveForm(ctx, 2, "a", "", move); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another project's form: %v", err)
	}
	// A restore keeps the position.
	if err := db.SetFormArchived(ctx, 1, "b", false, formsNow, formAudit); err != nil {
		t.Fatal(err)
	}
	if got := activeFormNames(t, db); !reflect.DeepEqual(got, []string{"a", "c", "d", "b"}) {
		t.Errorf("after restore: %q", got)
	}
}
