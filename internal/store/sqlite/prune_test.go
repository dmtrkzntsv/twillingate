package sqlite

import (
	"context"
	"testing"
)

func TestIncrementalVacuumRuns(t *testing.T) {
	db := newTestDB(t)
	// The pragma is a silent no-op unless the database was created with
	// auto_vacuum=INCREMENTAL, so assert the mode alongside the call.
	var mode int
	if err := db.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Errorf("auto_vacuum=%d, want 2 (INCREMENTAL); incremental_vacuum would be a no-op", mode)
	}
	if err := db.IncrementalVacuum(context.Background()); err != nil {
		t.Fatal(err)
	}
}
