package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// withWriteTables creates, on a database pinned below migration 040, the
// tables today's WriteEvents names that the schema lacks: rules (the
// rename lookup), usage_attributes (the keys received) and
// project_attributes (the catalog ingest adds to), as 040 leaves them. For a test that writes events through WriteEvents before migrating
// further, never through 40, which would create them again.
func withWriteTables(t *testing.T, db *DB) {
	t.Helper()
	execAll(t, db, `CREATE TABLE rules (
		project_id INTEGER NOT NULL REFERENCES projects(id),
		kind       TEXT    NOT NULL CHECK (kind IN ('event', 'attribute')),
		family     TEXT    NOT NULL,
		name       TEXT    NOT NULL,
		action     TEXT    NOT NULL CHECK (action IN ('rename', 'block')),
		to_name    TEXT,
		created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
		PRIMARY KEY (project_id, kind, family, name),
		CHECK ((action = 'rename') = (to_name IS NOT NULL)),
		CHECK ((kind = 'event' AND family IN ('product', 'measures'))
		    OR (kind = 'attribute' AND family = 'event'))
	) WITHOUT ROWID`, `CREATE TABLE usage_attributes (
		project_id INTEGER NOT NULL,
		day        TEXT    NOT NULL,
		attr_key   TEXT    NOT NULL,
		events     INTEGER NOT NULL,
		max_values INTEGER,
		PRIMARY KEY (project_id, day, attr_key)
	) WITHOUT ROWID`, `CREATE TABLE project_attributes (
		project_id INTEGER NOT NULL REFERENCES projects(id),
		key        TEXT    NOT NULL,
		breakdown  INTEGER NOT NULL DEFAULT 0 CHECK (breakdown IN (0, 1)),
		created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
		PRIMARY KEY (project_id, key)
	) WITHOUT ROWID`)
}

// Migration 040 moves event_rules to rules (kind 'event'), renames
// received_attributes to usage_attributes, builds the project_attributes
// catalog from both the received keys and each project's list, and drops
// projects.attributes; v_product_attrs answers the same rows from the
// catalog as it did from the list. A usage row of a project already gone
// (usage_attributes has no foreign key) stays, but enters no catalog.
func TestMigration040ProjectAttributes(t *testing.T) {
	db := newTestDBAt(t, 39)
	ctx := context.Background()
	execAll(t, db,
		`INSERT INTO projects (id, name, sort_key, attributes) VALUES
			(1, 'p1', 'a1', '["plan","$host"]'), (2, 'p2', 'a2', 'not json')`,
		`INSERT INTO event_rules (project_id, family, name, action, to_name, created_at) VALUES
			(1, 'product', 'old', 'rename', 'new', '2026-10-01T00:00:00Z'),
			(1, 'measures', 'spam', 'block', NULL, '2026-10-02T00:00:00Z')`,
		`INSERT INTO received_attributes (project_id, day, attr_key, events, max_values) VALUES
			(1, '2026-10-01', 'plan', 3, 2), (1, '2026-10-01', 'tier', 1, 1), (2, '2026-10-01', 'x', 0, NULL),
			(9, '2026-10-01', 'gone', 1, 1)`,
		`INSERT INTO events (id, project_id, family, event_name, ts, day, received_at, actor_id, host, attributes) VALUES
			('e1', 1, 'product', 'signup', '2026-10-01T10:00:00Z', '2026-10-01', '2026-10-01T10:00:00Z', 'a', 'x.com', '{"plan":"pro"}'),
			('e2', 1, 'product', 'signup', '2026-10-01T11:00:00Z', '2026-10-01', '2026-10-01T11:00:00Z', 'b', 'x.com', '{"plan":"free"}'),
			('e3', 2, 'product', 'signup', '2026-10-01T12:00:00Z', '2026-10-01', '2026-10-01T12:00:00Z', 'c', 'y.com', '{"x":"1"}')`,
	)
	const attrs = `SELECT * FROM v_product_attrs ORDER BY 1,2,3,4,5`
	before := queryStrings(t, db, attrs)
	if len(before) == 0 {
		t.Fatal("v_product_attrs is empty before the migration; the fixture does not exercise it")
	}
	if err := db.migrateThrough(ctx, 40); err != nil {
		t.Fatal(err)
	}

	if got, want := queryStrings(t, db, `SELECT project_id, kind, family, name, action, COALESCE(to_name, '-'), created_at
		FROM rules ORDER BY 1,2,3,4`), []string{
		"1|event|measures|spam|block|-|2026-10-02T00:00:00Z",
		"1|event|product|old|rename|new|2026-10-01T00:00:00Z",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %q, want %q", got, want)
	}
	if got, want := queryStrings(t, db, `SELECT project_id, day, attr_key, events, COALESCE(max_values, -1)
		FROM usage_attributes ORDER BY 1,2,3`), []string{
		"1|2026-10-01|plan|3|2", "1|2026-10-01|tier|1|1", "2|2026-10-01|x|0|-1", "9|2026-10-01|gone|1|1",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("usage_attributes = %q, want %q", got, want)
	}
	for _, gone := range []string{"event_rules", "received_attributes"} {
		var n int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name = ?`, gone).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s is still in the schema", gone)
		}
	}
	if got, want := queryStrings(t, db, `SELECT project_id, key, breakdown FROM project_attributes ORDER BY 1,2`),
		[]string{"1|$host|1", "1|plan|1", "1|tier|0", "2|x|0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("project_attributes = %q, want %q", got, want)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name = 'attributes'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("projects still has an attributes column")
	}
	if after := queryStrings(t, db, attrs); !reflect.DeepEqual(after, before) {
		t.Errorf("v_product_attrs changed across 040:\nbefore %q\nafter  %q", before, after)
	}

	if _, err := db.db.Exec(`INSERT INTO rules (project_id, kind, family, name, action) VALUES (1, 'attribute', 'product', 'a', 'block')`); err == nil {
		t.Error("an attribute rule with family product was accepted, want a CHECK failure")
	}
	if _, err := db.db.Exec(`INSERT INTO rules (project_id, kind, family, name, action) VALUES (1, 'attribute', 'event', 'a', 'block')`); err != nil {
		t.Errorf("an attribute rule with family event was refused: %v", err)
	}
}

// queryStrings reads every row of q as its columns joined by "|".
func queryStrings(t *testing.T, db *DB, q string) []string {
	t.Helper()
	rows, err := db.db.Query(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		line := ""
		for i, v := range vals {
			if i > 0 {
				line += "|"
			}
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			line += fmt.Sprint(v)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
