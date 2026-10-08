package sqlite

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

func TestMigration036SubmissionColumns(t *testing.T) {
	db := newTestDBAt(t, 36)
	want := []string{"project_id", "id", "form", "received_at", "fields", "attribution"}
	sort.Strings(want)
	if got := columnsOf(t, db, "submissions"); !reflect.DeepEqual(got, want) {
		t.Errorf("submissions columns = %v, want %v", got, want)
	}
}

// TestMigration036MovesAttribution checks a visit keeps only its non-empty
// referrer and UTMs as the submission's attribution, which also land on
// its $form_submit event, and that a submission without a visit, and every
// other event, are left alone.
func TestMigration036MovesAttribution(t *testing.T) {
	db := newTestDBAt(t, 35)
	execAll(t, db,
		`INSERT INTO submissions (project_id, id, form, received_at, fields, actor_kind, actor_id, host, path, via, visit) VALUES
		 (1, 's1', 'contact', '2026-10-06T10:00:00Z', '{}', 'user', 'u', 'x.com', '/c', 'form',
		  '{"landing_path":"/p","referrer":"google","utm_source":"nl","utm_medium":"email","utm_campaign":"oct","views":3}'),
		 (1, 's2', 'contact', '2026-10-06T10:00:00Z', '{}', 'user', 'u', 'x.com', '/c', 'form', NULL),
		 (1, 's3', 'contact', '2026-10-06T10:00:00Z', '{}', 'user', 'u', 'x.com', '/c', 'form',
		  '{"landing_path":"/p","referrer":"","utm_source":"nl","utm_medium":"","utm_campaign":"","views":1}'),
		 (1, 's4', 'contact', '2026-10-06T10:00:00Z', '{}', 'user', 'u', 'x.com', '/c', 'form',
		  '{"landing_path":"/p","referrer":"","utm_source":"","utm_medium":"","utm_campaign":"","views":1}')`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, actor_id, attributes) VALUES
		 ('product', 1, '2026-10-06', 's1', '$form_submit', '2026-10-06T10:00:00Z', 'u', '{"form":"contact"}'),
		 ('product', 1, '2026-10-06', 's2', '$form_submit', '2026-10-06T10:00:00Z', 'u', '{"form":"contact"}'),
		 ('product', 2, '2026-10-06', 's1', '$form_submit', '2026-10-06T10:00:00Z', 'u', '{"form":"contact"}'),
		 ('views',   1, '2026-10-06', 's1', '$page_view',   '2026-10-06T10:00:00Z', 'u', '{}')`)
	if err := db.migrateThrough(context.Background(), 36); err != nil {
		t.Fatal(err)
	}
	row := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := db.db.QueryRow(q, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return s
	}
	const sub = `SELECT COALESCE(attribution, 'NULL') FROM submissions WHERE id=?`
	for id, want := range map[string]string{
		"s1": `{"referrer":"google","utm_source":"nl","utm_medium":"email","utm_campaign":"oct"}`,
		"s2": "NULL",
		"s3": `{"utm_source":"nl"}`,
		"s4": "NULL",
	} {
		if got := row(sub, id); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
	const ev = `SELECT referrer_source || '|' || utm_source || '|' || utm_medium || '|' || utm_campaign
		FROM events WHERE family=? AND project_id=? AND id=?`
	for _, c := range []struct {
		family  string
		project int64
		id      string
		want    string
	}{
		{"product", 1, "s1", "google|nl|email|oct"},
		{"product", 1, "s2", "|||"},
		{"product", 2, "s1", "|||"},
		{"views", 1, "s1", "|||"},
	} {
		if got := row(ev, c.family, c.project, c.id); got != c.want {
			t.Errorf("%s %d %s = %q, want %q", c.family, c.project, c.id, got, c.want)
		}
	}
}
