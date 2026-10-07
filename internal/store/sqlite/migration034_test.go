package sqlite

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

func columnsOf(t *testing.T, db *DB, table string) []string {
	t.Helper()
	rows, err := db.db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info('`+table+`')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	return got
}

func TestMigration034FormsColumns(t *testing.T) {
	db := newTestDBAt(t, 34)
	for table, want := range map[string][]string{
		"forms": {"project_id", "name", "purpose", "return_url", "fields", "status", "expected_fields",
			"created_at", "draft_until", "approved_at", "closes_at", "last_submitted_at", "archived_at"},
		"submissions": {"project_id", "id", "form", "received_at", "fields", "actor_kind", "actor_id",
			"host", "path", "via", "visit"},
	} {
		sort.Strings(want)
		if got := columnsOf(t, db, table); !reflect.DeepEqual(got, want) {
			t.Errorf("%s columns = %v, want %v", table, got, want)
		}
	}
}

// TestMigration034FormDefaults checks a form inserted with only its key and
// creation time is a draft with no fields recorded and no purpose.
func TestMigration034FormDefaults(t *testing.T) {
	db := newTestDBAt(t, 34)
	execAll(t, db, `INSERT INTO forms (project_id, name, created_at) VALUES (1, 'contact', '2026-10-06T10:00:00Z')`)
	var status, fields, purpose, returnURL string
	var expected, draftUntil *string
	if err := db.db.QueryRow(`SELECT status, fields, purpose, return_url, expected_fields, draft_until
		FROM forms WHERE project_id=1 AND name='contact'`).Scan(&status, &fields, &purpose, &returnURL, &expected, &draftUntil); err != nil {
		t.Fatal(err)
	}
	if status != "draft" || fields != "[]" || purpose != "" || returnURL != "" || expected != nil || draftUntil != nil {
		t.Fatalf("defaults = %q %q %q %q %v %v", status, fields, purpose, returnURL, expected, draftUntil)
	}
}

func TestMigration034SubmissionKeyIsPerProject(t *testing.T) {
	db := newTestDBAt(t, 34)
	ins := `INSERT INTO submissions (project_id, id, form, received_at, fields, actor_kind, actor_id, via)
		VALUES (?, 'a', 'contact', '2026-10-06T10:00:00Z', '{}', 'user', 'u', 'form')`
	if _, err := db.db.Exec(ins, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(ins, 2); err != nil {
		t.Fatalf("same id in another project: %v", err)
	}
	if _, err := db.db.Exec(ins, 1); err == nil {
		t.Fatal("same id twice in one project was accepted")
	}
}
