package sqlite

import (
	"context"
	"testing"
)

// TestMigration039EventRulesChecks checks the table exists after the
// migration and its CHECKs refuse the rows the store never writes.
func TestMigration039EventRulesChecks(t *testing.T) {
	db := newTestDBAt(t, 38)
	if err := db.migrateThrough(context.Background(), 39); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM event_rules`).Scan(&n); err != nil {
		t.Fatalf("event_rules: %v", err)
	}
	// The project exists, so only the CHECKs can refuse the rows below.
	execAll(t, db, `INSERT OR IGNORE INTO projects (id, name, sort_key) VALUES (1, 'p1', 'a1')`)
	for name, stmt := range map[string]string{
		"rename without to_name": `INSERT INTO event_rules (project_id, family, name, action) VALUES (1, 'product', 'a', 'rename')`,
		"block with to_name":     `INSERT INTO event_rules (project_id, family, name, action, to_name) VALUES (1, 'product', 'a', 'block', 'b')`,
		"views family":           `INSERT INTO event_rules (project_id, family, name, action) VALUES (1, 'views', 'a', 'block')`,
		"unknown action":         `INSERT INTO event_rules (project_id, family, name, action) VALUES (1, 'product', 'a', 'drop')`,
	} {
		if _, err := db.db.Exec(stmt); err == nil {
			t.Errorf("%s: insert accepted, want a CHECK failure", name)
		}
	}
	if _, err := db.db.Exec(`INSERT INTO event_rules (project_id, family, name, action, to_name) VALUES (1, 'measures', 'a', 'rename', 'b')`); err != nil {
		t.Errorf("valid rename refused: %v", err)
	}
}
