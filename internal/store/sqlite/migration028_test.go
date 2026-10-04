package sqlite

import "testing"

// Migration 028 adds an empty received_attributes table keyed by project,
// day and key.
func TestMigration028AddsReceivedAttributes(t *testing.T) {
	db := newTestDBAt(t, 28)
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('received_attributes') WHERE pk > 0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("received_attributes primary key columns = %d, want 3", n)
	}
}
