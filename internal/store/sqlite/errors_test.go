package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// This file drives the SQL error branches that the happy-path tests in the
// rest of the package never touch: a missing table, a blocking trigger, or a
// duplicate key that INSERT OR IGNORE does not silently swallow. Every
// assertion checks the error text names the failing operation, not just that
// an error came back.

// --- registry.go ---

func TestConfigVersionFailsOnMissingMetaTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE meta`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfigVersion(ctx); err == nil {
		t.Error("want error reading config_version from a missing meta table")
	}
}

func TestConfigVersionDefaultsToZeroWhenUnset(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DELETE FROM meta WHERE key='config_version'`); err != nil {
		t.Fatal(err)
	}
	v, err := db.ConfigVersion(ctx)
	if err != nil {
		t.Fatalf("ConfigVersion = %v, want nil error", err)
	}
	if v != 0 {
		t.Errorf("ConfigVersion = %d, want 0 when the row is absent", v)
	}
}

func TestAuditAndBumpFailsOnMissingAuditLogTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE audit_log`); err != nil {
		t.Fatal(err)
	}
	p := store.RegistryProject{Name: "a", AllowedOrigins: "[]"}
	_, err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err == nil {
		t.Error("want error writing the audit row to a missing audit_log table")
	}
}

func TestAuditAndBumpFailsWhenMetaMissing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE meta`); err != nil {
		t.Fatal(err)
	}
	p := store.RegistryProject{Name: "a", AllowedOrigins: "[]"}
	_, err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err == nil {
		t.Error("want error bumping config_version in a missing meta table")
	}
	// The audit_log insert (which happens before the bump) must have rolled
	// back along with everything else in the transaction.
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("audit_log rows = %d after a failed bump, want 0 (transaction not rolled back)", n)
	}
}

func TestLoadRegistryFailsOnMissingProjectsTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LoadRegistry(ctx); err == nil {
		t.Error("want error listing projects from a missing table")
	}
}

func TestLoadRegistryFailsOnMissingIngestKeysTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE ingest_keys`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LoadRegistry(ctx); err == nil {
		t.Error("want error listing ingest keys from a missing table")
	}
}

func TestUpdateProjectFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	p := store.RegistryProject{ID: 1, Name: "a", AllowedOrigins: "[]"}
	err := db.UpdateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.update", Subject: "1"})
	if err == nil {
		t.Error("want error updating a missing projects table")
	}
}

func TestSetProjectArchivedFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	err := db.SetProjectArchived(ctx, 1, true, store.AuditEntry{Actor: "cli", Action: "project.archive", Subject: "1"})
	if err == nil {
		t.Error("want error archiving against a missing projects table")
	}
}

func TestInsertIngestKeyFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE ingest_keys`); err != nil {
		t.Fatal(err)
	}
	k := store.RegistryKey{Key: "ak_x", ProjectID: 1, Label: "web"}
	err := db.InsertIngestKey(ctx, k, store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "1/web"})
	if err == nil {
		t.Error("want error inserting a key into a missing table")
	}
}

func TestInsertIngestKeyFailsOnDuplicateLabel(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Name: "a", AllowedOrigins: "[]"}
	id, err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	k := store.RegistryKey{Key: "ak_1", ProjectID: id, Label: "web"}
	if err := db.InsertIngestKey(ctx, k, store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "1/web"}); err != nil {
		t.Fatal(err)
	}
	dup := store.RegistryKey{Key: "ak_2", ProjectID: id, Label: "web"}
	err = db.InsertIngestKey(ctx, dup, store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "1/web"})
	if err == nil || !strings.Contains(err.Error(), `already exists`) {
		t.Errorf("err = %v, want mention that the label already exists", err)
	}
}

func TestInsertIngestKeyFailsOnDuplicateKeyValue(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Name: "a", AllowedOrigins: "[]"}
	id, err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_dup", ProjectID: id, Label: "web"},
		store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "1/web"}); err != nil {
		t.Fatal(err)
	}
	// Same key value, different label: UNIQUE (project_id, label) is
	// satisfied, so this must fail on the primary key instead, and that
	// failure must not be reported as a label conflict.
	err = db.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_dup", ProjectID: id, Label: "mobile"},
		store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "1/mobile"})
	if err == nil || !strings.Contains(err.Error(), "issue key for") {
		t.Errorf("err = %v, want mention of issue key for", err)
	}
}

func TestSetIngestKeyDisabledFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE ingest_keys`); err != nil {
		t.Fatal(err)
	}
	err := db.SetIngestKeyDisabled(ctx, 1, "web", true, store.AuditEntry{Actor: "cli", Action: "key.disable", Subject: "1/web"})
	if err == nil {
		t.Error("want error disabling a key in a missing table")
	}
}

func TestDeleteProjectDataFailsOnMissingProjectsTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	err := db.DeleteProjectData(ctx, 1, store.AuditEntry{Actor: "cli", Action: "project.delete", Subject: "1"})
	if err == nil {
		t.Error("want error deleting from a missing projects table")
	}
}

func TestDeleteProjectDataFailsOnUnknownID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	err := db.DeleteProjectData(ctx, 7, store.AuditEntry{Actor: "cli", Action: "project.delete", Subject: "7"})
	if err == nil || !strings.Contains(err.Error(), "unknown id 7") {
		t.Errorf("err = %v, want mention of unknown id 7", err)
	}
}

func TestDeleteProjectDataFailsOnMissingChildTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Name: "a", AllowedOrigins: "[]"}
	id, err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TABLE rules`); err != nil {
		t.Fatal(err)
	}
	err = db.DeleteProjectData(ctx, id, store.AuditEntry{Actor: "cli", Action: "project.delete", Subject: "1"})
	if err == nil || !strings.Contains(err.Error(), "delete rules") {
		t.Errorf("err = %v, want mention of delete rules", err)
	}
}

// --- sqlite.go ---

func TestOpenFailsOnInvalidURLEscape(t *testing.T) {
	if _, err := open("sqlite://%zz"); err == nil {
		t.Error("want error parsing an invalid percent-escape in the DSN")
	}
}

func TestOpenAtFailsOnReadonlyFile(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/ro.db"
	if err := os.WriteFile(p, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := openAt(p); err == nil {
		t.Error("want error setting auto_vacuum on a readonly file")
	}
}

// --- write.go ---

func TestProjectIDsFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ProjectIDs(ctx); err == nil {
		t.Error("want error listing ids from a missing table")
	}
}

// --- migrate.go ---

func TestMigrateFailsOnLookupQuery(t *testing.T) {
	db, err := openAt(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// A view named schema_migrations satisfies Migrate's own "IF NOT
	// EXISTS" guard (the name already exists), so the CREATE TABLE step
	// no-ops; but SQLite validates views lazily, so the very next SELECT
	// against it fails because the view's underlying table doesn't exist.
	if _, err := db.db.ExecContext(ctx,
		`CREATE VIEW schema_migrations AS SELECT * FROM nonexistent_table`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err == nil {
		t.Error("want error querying schema_migrations through a broken view")
	}
}

func TestMigrateFailsInsertingSchemaVersion(t *testing.T) {
	db, err := openAt(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// Pre-create schema_migrations with an extra NOT NULL column that has
	// no default. The lookup SELECT only reads version, so it still works;
	// Migrate's own CREATE TABLE IF NOT EXISTS then no-ops on the existing
	// table, and the migration body itself succeeds, but the final INSERT
	// (which supplies only version) then violates the extra column.
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now')),
		extra TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err == nil {
		t.Error("want error inserting into a schema_migrations missing a required column's value")
	}
	// The migration body's own tables must have rolled back with the
	// failed INSERT, or a retry would find them already there and error
	// on a duplicate CREATE TABLE.
	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("migration 001's tables survived a failed schema_migrations insert; rollback did not happen")
	}
}
