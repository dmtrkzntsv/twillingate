package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// statID names one usage_history row: a stat, a project and a day.
type statID struct {
	key     string
	project int64
	day     string
}

// readStats answers every usage_history row's value by its key.
func readStats(t *testing.T, db *DB) map[statID]int64 {
	t.Helper()
	rows, err := db.db.Query(`SELECT key, project_id, measured_at, value FROM usage_history`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[statID]int64{}
	for rows.Next() {
		var id statID
		var v int64
		if err := rows.Scan(&id.key, &id.project, &id.day, &v); err != nil {
			t.Fatal(err)
		}
		out[id] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestRecordUsageHistoryWritesFileBytes: the run writes the database's
// size and each project's file bytes on now's UTC day. A second run the
// same day replaces the day (a project left out has no files that day);
// a run with no file sizes (a process with no event files) writes the
// database's size alone and leaves the day's file bytes as they were; a
// later day keeps the earlier days.
func TestRecordUsageHistoryWritesFileBytes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	day := "2026-08-22"
	if err := db.RecordUsageHistory(ctx, now, map[int64]int64{1: 1000, 2: 20}); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	if v := stats[statID{store.StatDatabaseBytes, 0, day}]; v <= 0 {
		t.Errorf("database_bytes = %d, want the file's size", v)
	}
	if stats[statID{store.StatFileBytes, 1, day}] != 1000 || stats[statID{store.StatFileBytes, 2, day}] != 20 || len(stats) != 3 {
		t.Errorf("stats = %v, want the database's size and both projects' file bytes", stats)
	}

	if err := db.RecordUsageHistory(ctx, now.Add(time.Hour), map[int64]int64{1: 1500}); err != nil {
		t.Fatal(err)
	}
	stats = readStats(t, db)
	if _, ok := stats[statID{store.StatFileBytes, 2, day}]; ok || stats[statID{store.StatFileBytes, 1, day}] != 1500 || len(stats) != 2 {
		t.Errorf("after a second run the same day: %v, want project 1's 1500 and no row for project 2", stats)
	}

	if err := db.RecordUsageHistory(ctx, now.Add(2*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if got := readStats(t, db); got[statID{store.StatFileBytes, 1, day}] != 1500 || len(got) != 2 {
		t.Errorf("after a run with no file sizes: %v, want project 1's file bytes kept", got)
	}

	if err := db.RecordUsageHistory(ctx, now.Add(24*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	stats = readStats(t, db)
	if stats[statID{store.StatFileBytes, 1, day}] != 1500 || stats[statID{store.StatDatabaseBytes, 0, "2026-08-23"}] <= 0 || len(stats) != 3 {
		t.Errorf("the next day: %v, want the earlier day kept and the database measured again", stats)
	}
}

// DeleteProjectData takes the project's sizes with it.
func TestDeleteProjectDataRemovesUsageHistory(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id, err := db.CreateProject(ctx, store.RegistryProject{Name: "a", AllowedOrigins: "[]"},
		store.AuditEntry{Actor: "t", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []int64{id, id + 1} {
		for _, day := range []string{"2026-08-21", "2026-08-22"} {
			if _, err := db.db.Exec(`INSERT INTO usage_history (key, project_id, measured_at, value) VALUES (?, ?, ?, 5)`, store.StatFileBytes, p, day); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.DeleteProjectData(ctx, id, store.AuditEntry{Actor: "t", Action: "project.delete"}); err != nil {
		t.Fatal(err)
	}
	others := 0
	for row := range readStats(t, db) {
		if row.project == id {
			t.Errorf("%v survived the project's deletion", row)
		} else {
			others++
		}
	}
	if others != 2 {
		t.Errorf("%d rows of another project left, want its 2", others)
	}
}
