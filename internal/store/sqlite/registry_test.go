package sqlite

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// openRegistryDB creates a migrated store in t.TempDir. Mirrors the
// helper style used across this package's tests.
func openRegistryDB(t *testing.T) *DB {
	t.Helper()
	d, err := openAt(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCreateProjectWritesAuditAndBumpsVersion(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	v0, err := d.ConfigVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := store.RegistryProject{Name: "My blog",
		AllowedOrigins: `["https://blog.example.com"]`}
	id, err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	ps, _, err := d.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].ID != id || ps[0].Name != "My blog" {
		t.Fatalf("LoadRegistry = %+v", ps)
	}
	v1, _ := d.ConfigVersion(ctx)
	if v1 != v0+1 {
		t.Errorf("config_version = %d, want %d", v1, v0+1)
	}
	var actor, action string
	if err := d.db.QueryRow(
		`SELECT actor, action FROM audit_log WHERE action='project.create'`).
		Scan(&actor, &action); err != nil {
		t.Fatal(err)
	}
	if actor != "cli" || action != "project.create" {
		t.Errorf("audit = %s %s", actor, action)
	}
}

func TestUpdateProjectAppliesFieldsAndAudits(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Name: "My blog",
		AllowedOrigins: `["https://blog.example.com"]`}
	id, err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	v0, _ := d.ConfigVersion(ctx)

	updated := store.RegistryProject{ID: id, Name: "Renamed blog",
		AllowedOrigins: `["https://blog.example.com","https://www.blog.example.com"]`,
		Attributes:     `["plan"]`}
	if err := d.UpdateProject(ctx, updated, store.AuditEntry{
		Actor: "cli", Action: "project.update", Subject: "1"}); err != nil {
		t.Fatal(err)
	}

	ps, _, err := d.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("LoadRegistry = %+v", ps)
	}
	got := ps[0]
	if got.Name != "Renamed blog" ||
		got.AllowedOrigins != updated.AllowedOrigins || got.Attributes != updated.Attributes {
		t.Fatalf("LoadRegistry after update = %+v", got)
	}

	v1, _ := d.ConfigVersion(ctx)
	if v1 != v0+1 {
		t.Errorf("config_version = %d, want %d", v1, v0+1)
	}
	var actor, action string
	if err := d.db.QueryRow(
		`SELECT actor, action FROM audit_log WHERE subject='1' AND action='project.update'`).
		Scan(&actor, &action); err != nil {
		t.Fatal(err)
	}
	if actor != "cli" || action != "project.update" {
		t.Errorf("audit = %s %s", actor, action)
	}
}

func TestUpdateProjectUnknownIDFails(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	p := store.RegistryProject{ID: 7, Name: "n", AllowedOrigins: "[]"}
	err := d.UpdateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.update", Subject: "7"})
	if err == nil {
		t.Fatal("update of an unknown id did not fail")
	}
}

func TestIngestKeyLifecycle(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Name: "a", AllowedOrigins: "[]"}
	id, err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	k := store.RegistryKey{Key: "ak_test1", ProjectID: id, Label: "web"}
	if err := d.InsertIngestKey(ctx, k, store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetIngestKeyDisabled(ctx, id, "web", true, store.AuditEntry{Actor: "cli", Action: "key.disable", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	_, ks, err := d.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 1 || !ks[0].Disabled {
		t.Fatalf("keys = %+v, want one disabled", ks)
	}
	if err := d.SetIngestKeyDisabled(ctx, id, "web", false, store.AuditEntry{Actor: "cli", Action: "key.enable", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	_, ks, _ = d.LoadRegistry(ctx)
	if ks[0].Disabled {
		t.Fatal("key still disabled after enable")
	}
	// Unknown project+label is an error, not a silent no-op.
	if err := d.SetIngestKeyDisabled(ctx, id, "nope", true, store.AuditEntry{Actor: "cli", Action: "key.disable", Subject: "nope"}); err == nil {
		t.Fatal("unknown label did not fail")
	}
}

func TestArchiveRestoreProject(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Name: "a", AllowedOrigins: "[]"}
	id, err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetProjectArchived(ctx, id, true, store.AuditEntry{Actor: "mcp", Action: "project.archive", Subject: "1"}); err != nil {
		t.Fatal(err)
	}
	ps, _, _ := d.LoadRegistry(ctx)
	if !ps[0].Archived {
		t.Fatal("not archived")
	}
	if err := d.SetProjectArchived(ctx, id, false, store.AuditEntry{Actor: "mcp", Action: "project.restore", Subject: "1"}); err != nil {
		t.Fatal(err)
	}
	ps, _, _ = d.LoadRegistry(ctx)
	if ps[0].Archived {
		t.Fatal("still archived")
	}
}

func TestSetProjectArchivedErrors(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()

	// Archiving an unknown id should fail.
	if err := d.SetProjectArchived(ctx, 7, true, store.AuditEntry{Actor: "cli", Action: "project.archive", Subject: "7"}); err == nil {
		t.Fatal("archiving unknown id did not fail")
	}

	// Create a project but don't archive it.
	p := store.RegistryProject{Name: "a", AllowedOrigins: "[]"}
	id, err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}

	// Restore (archive=false) of non-archived project should be a no-op (nil error).
	if err := d.SetProjectArchived(ctx, id, false, store.AuditEntry{Actor: "mcp", Action: "project.restore", Subject: "1"}); err != nil {
		t.Fatal(err)
	}

	// Project should still be unarchived.
	ps, _, _ := d.LoadRegistry(ctx)
	if ps[0].Archived {
		t.Fatal("project was archived by restore no-op")
	}
}

// TestMigrationUpgradeFrom004 ensures a database populated before 005
// (with migrations 001-004 only) migrates cleanly and reads new columns
// with their defaults.
func TestMigrationUpgradeFrom004(t *testing.T) {
	d, err := openAt(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()

	// Manually apply migrations 001-004 only.
	if err := applyMigrationsUpTo(ctx, d, 4); err != nil {
		t.Fatal(err)
	}

	// Insert a project row using the old schema (before 005).
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO projects (id, alias, name) VALUES (?, ?, ?)`,
		"test-id", "old_project", "Old Project"); err != nil {
		t.Fatal(err)
	}

	// Apply through 005, the migration that adds allowed_origins.
	if err := d.migrateThrough(ctx, 5); err != nil {
		t.Fatal(err)
	}

	// Verify the new column exists with its default.
	var allowedOrigins string
	if err := d.db.QueryRowContext(ctx,
		`SELECT allowed_origins FROM projects WHERE name='Old Project'`).
		Scan(&allowedOrigins); err != nil {
		t.Fatal(err)
	}

	if allowedOrigins != "[]" {
		t.Errorf("allowed_origins = %q, want '[]'", allowedOrigins)
	}
}

// TestProjectsAttributesDefaultsToEmptyArray asserts the new column's
// NOT NULL DEFAULT '[]', for a row inserted (via ExecForTest, bypassing the
// application write path) without ever mentioning it.
func TestProjectsAttributesDefaultsToEmptyArray(t *testing.T) {
	db := newTestDB(t) // existing helper; applies all migrations
	ctx := context.Background()
	if _, err := db.ExecForTest(
		`INSERT INTO projects (name, allowed_origins)
		 VALUES ('Blog','[]')`); err != nil {
		t.Fatal(err)
	}
	ps, _, err := db.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Attributes != "[]" {
		t.Fatalf("Attributes = %q, want \"[]\"", ps[0].Attributes)
	}
}

// TestMigrationBackfillsAttributesFromLegacyMap is the risky part of 006:
// it reconstructs pre-006 state (product_aggregation's old event-keyed
// map), runs the REAL embedded migration via d.Migrate (not a copy of its
// SQL — a copy previously drifted from the shipped migration when the
// json_valid/json_type/m.type/v.type guards were hardened in, leaving the
// real UPDATE's union path with no in-repo coverage), and asserts the
// backfill produced the sorted DISTINCT union of every array in the map —
// {"*":["plan"],"subscribed":["tier","plan"]} -> ["plan","tier"]. This
// also exercises whether the correlated
// json_each(json_extract(projects.product_aggregation, '$.attributes'))
// reference actually works on this SQLite build.
func TestMigrationBackfillsAttributesFromLegacyMap(t *testing.T) {
	d, err := openAt(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	// Reconstruct pre-006 state: migrations 001-005 applied, so
	// product_aggregation exists as a plain TEXT column.
	if err := applyMigrationsUpTo(ctx, d, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO projects (id, alias, name, identity, allowed_origins, product_aggregation)
		 VALUES (?,?,?,?,?,?)`,
		"id1", "blog", "Blog", "anonymous", "[]",
		`{"enabled":true,"attributes":{"*":["plan"],"subscribed":["tier","plan"]},"top_n":50}`); err != nil {
		t.Fatal(err)
	}

	// Run every remaining migration (006 and 007) for real.
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var attrs string
	if err := d.db.QueryRowContext(ctx,
		`SELECT attributes FROM projects WHERE name='Blog'`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if attrs != `["plan","tier"]` {
		t.Fatalf("attributes = %q, want [\"plan\",\"tier\"] (sorted DISTINCT union)", attrs)
	}
}

// TestMigrationBackfillSkipsMalformedProductAggregation is the corruption
// case: migrations are forward-only with no scripted way back, so a
// hand-edited or otherwise malformed product_aggregation value must not
// abort the migration transaction (which would leave the server unable to
// boot). Runs the real embedded 006 migration via d.Migrate, not a copy of
// its SQL, so it also proves the DROP COLUMN step still completes.
func TestMigrationBackfillSkipsMalformedProductAggregation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"not JSON at all", "not json"},
		{"scalar where an array is expected", `{"enabled":true,"attributes":{"*":"plan"},"top_n":50}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := openAt(t.TempDir() + "/test.db")
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			ctx := context.Background()

			if err := applyMigrationsUpTo(ctx, d, 5); err != nil {
				t.Fatal(err)
			}
			if _, err := d.db.ExecContext(ctx,
				`INSERT INTO projects (id, alias, name, identity, allowed_origins, product_aggregation)
				 VALUES (?,?,?,?,?,?)`,
				"id1", "blog", "Blog", "anonymous", "[]", tc.value); err != nil {
				t.Fatal(err)
			}

			if err := d.Migrate(ctx); err != nil {
				t.Fatalf("migration aborted on malformed product_aggregation %q: %v", tc.value, err)
			}

			var attrs string
			if err := d.db.QueryRowContext(ctx,
				`SELECT attributes FROM projects WHERE name='Blog'`).Scan(&attrs); err != nil {
				t.Fatal(err)
			}
			if attrs != "[]" {
				t.Fatalf("attributes = %q, want \"[]\" (malformed input must backfill to empty, not abort)", attrs)
			}
		})
	}
}

// applyMigrationsUpTo applies migrations 001 through maxVersion to the database.
// It mimics the Migrate logic but stops at a specific version.
func applyMigrationsUpTo(ctx context.Context, d *DB, maxVersion int) error {
	if _, err := d.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
		   version INTEGER PRIMARY KEY,
		   applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return err
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}

	// Read migration files.
	migrations := make(map[int][]byte)
	for _, e := range entries {
		var version int
		_, err := parseVersion(e.Name(), &version)
		if err != nil {
			continue
		}
		if version > maxVersion {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		migrations[version] = body
	}

	// Apply in sorted order.
	for v := 1; v <= maxVersion; v++ {
		body, ok := migrations[v]
		if !ok {
			continue
		}

		var done int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, v).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			continue
		}

		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func TestDeleteProjectDataCascades(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Name: "a", AllowedOrigins: "[]"}
	id, err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_d", ProjectID: id, Label: "web"},
		store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "1/web"}); err != nil {
		t.Fatal(err)
	}
	// one row in a raw table and one in an aggregate table
	if _, err := d.db.Exec(`INSERT INTO events (id, project_id, ts, received_at, kind, actor_id, actor_kind, path, family, event_name)
		VALUES ('h1',?,'2026-08-01T10:00:00Z','2026-08-01T10:00:00Z','web','a','connection','/x','views','$page_view')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`INSERT INTO agg_views_daily (project_id, day, kind, visitors, views,
		sessions, bounces, duration_sec) VALUES (?,'2026-07-01','web',1,1,1,0,0)`, id); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteProjectData(ctx, id, store.AuditEntry{
		Actor: "cli", Action: "project.delete", Subject: "1"}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"projects", "ingest_keys", "events", "agg_views_daily"} {
		var c int
		if err := d.db.QueryRow(
			`SELECT COUNT(*) FROM `+table+` WHERE `+projectCol(table)+`=?`, id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Errorf("%s still has %d rows", table, c)
		}
	}
	// the audit row survives the deletion — that is the point of it
	var c int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='project.delete'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Error("no audit row for the delete")
	}
}

func projectCol(table string) string {
	if table == "projects" {
		return "id"
	}
	return "project_id"
}

// TestProjectTablesMatchesSchema keeps the projectTables comment's claim
// honest: it independently enumerates every table in the live schema that
// carries a `project_id` column (via sqlite_master + pragma_table_info) and
// asserts the set is exactly projectTables, in both directions. A missing
// entry silently orphans rows on DeleteProjectData; a
// stale entry (a dropped or renamed table still listed) is dead weight
// that hides the day a real gap opens up. Either defect fails loudly here
// with the offending table names, rather than staying invisible until
// someone loses data.
func TestProjectTablesMatchesSchema(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Collect table names and close the cursor before running any further
	// query: the pool is capped at one connection (single-writer, spec
	// §7.2), so a nested query issued while these rows are still open
	// would deadlock waiting for a connection that never frees up.
	rows, err := db.db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()

	actual := map[string]bool{}
	for _, name := range tables {
		if hasColumn(t, db, name, "project_id") {
			actual[name] = true
		}
	}

	expected := map[string]bool{}
	for _, table := range projectTables {
		expected[table] = true
	}

	var missing, stale []string
	// missing: schema says this table has a project_id column, but
	// projectTables does not list it.
	for table := range actual {
		if !expected[table] {
			missing = append(missing, table)
		}
	}
	// stale: projectTables lists this table, but the schema says it does
	// not (or no longer) have a project_id column.
	for table := range expected {
		if !actual[table] {
			stale = append(stale, table)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 || len(stale) > 0 {
		t.Fatalf("projectTables (internal/store/sqlite/registry.go) is out of sync with the schema.\n"+
			"missing from projectTables (have a project_id column, not listed — DeleteProjectData will orphan their rows): %v\n"+
			"stale in projectTables (listed but table dropped or no longer has a project_id column): %v",
			missing, stale)
	}
}

// parseVersion extracts the version number from a migration filename.
func parseVersion(name string, version *int) (string, error) {
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			if i == 0 {
				return "", nil
			}
			*version = 0
			for j := 0; j < i; j++ {
				*version = *version*10 + int(name[j]-'0')
			}
			return name[i:], nil
		}
	}
	return "", nil
}

// TestCreateProjectWithKeyIsAtomic: the project and its first key commit
// together or not at all, so a failed key never leaves a keyless project
// behind for the caller to trip over on retry.
func TestCreateProjectWithKeyIsAtomic(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	project := func(name string) store.RegistryProject {
		return store.RegistryProject{Name: name, AllowedOrigins: "[]"}
	}
	// Subjects are left empty: the store fills them from the assigned id.
	pa := store.AuditEntry{Actor: "api", Action: "project.create"}
	ka := store.AuditEntry{Actor: "api", Action: "key.issue"}

	id, err := d.CreateProjectWithKey(ctx, project("blog"),
		store.RegistryKey{Key: "ak_one", Label: "default"}, pa, ka)
	if err != nil {
		t.Fatal(err)
	}
	ps, ks, err := d.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].ID != id || len(ks) != 1 || ks[0].Key != "ak_one" || ks[0].ProjectID != id {
		t.Fatalf("registry = %+v / %+v", ps, ks)
	}
	var subjects string
	if err := d.db.QueryRow(`SELECT group_concat(subject, ',') FROM (SELECT subject FROM audit_log
		WHERE actor='api' AND action IN ('project.create','key.issue') ORDER BY rowid)`).Scan(&subjects); err != nil {
		t.Fatal(err)
	}
	if subjects != "1,1/default" {
		t.Fatalf("audit subjects = %q, want the id and id/label", subjects)
	}

	// A key that cannot be inserted (its value is already taken) must take
	// the new project down with it, and the id it would have used is not
	// reissued to the next successful create.
	v0, _ := d.ConfigVersion(ctx)
	if _, err := d.CreateProjectWithKey(ctx, project("shop"),
		store.RegistryKey{Key: "ak_one", Label: "default"}, pa, ka); err == nil {
		t.Fatal("duplicate key value accepted")
	}
	ps, _, _ = d.LoadRegistry(ctx)
	if len(ps) != 1 {
		t.Errorf("projects after failed create = %+v, want only blog", ps)
	}
	if v1, _ := d.ConfigVersion(ctx); v1 != v0 {
		t.Errorf("config_version moved %d → %d on a rolled-back create", v0, v1)
	}
}

func TestInsertProjectReturnsIncreasingIds(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "test", Action: "project.create"}
	first, err := db.CreateProject(ctx, store.RegistryProject{Name: "Blog", AllowedOrigins: "[]", Attributes: "[]"}, audit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateProject(ctx, store.RegistryProject{Name: "Blog", AllowedOrigins: "[]", Attributes: "[]"}, audit)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("ids = %d, %d; want 1, 2 (names need not be unique)", first, second)
	}
	var subject string
	if err := db.db.QueryRowContext(ctx, `SELECT subject FROM audit_log WHERE action='project.create' ORDER BY rowid DESC LIMIT 1`).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if subject != "2" {
		t.Fatalf("audit subject = %q, want the new id", subject)
	}
}

func TestInsertKeyConflictIsTyped(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id, err := db.CreateProject(ctx, store.RegistryProject{Name: "Blog", AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	audit := store.AuditEntry{Actor: "test", Action: "key.issue"}
	if err := db.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_1", ProjectID: id, Label: "web"}, audit); err != nil {
		t.Fatal(err)
	}
	err = db.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_2", ProjectID: id, Label: "web"}, audit)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate label err = %v, want ErrConflict from UNIQUE (project_id, label)", err)
	}
	// Same label on another project is fine.
	other, _ := db.CreateProject(ctx, store.RegistryProject{Name: "Shop", AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err := db.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_3", ProjectID: other, Label: "web"}, audit); err != nil {
		t.Fatalf("same label on another project: %v", err)
	}
}

func TestProjectIDsAscending(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, n := range []string{"c", "a", "b"} {
		if _, err := db.CreateProject(ctx, store.RegistryProject{Name: n, AllowedOrigins: "[]", Attributes: "[]"},
			store.AuditEntry{Actor: "test", Action: "project.create"}); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := db.ProjectIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
		t.Fatalf("ids = %v, want [1 2 3]", ids)
	}
}
