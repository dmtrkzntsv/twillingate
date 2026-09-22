package sqlite

import (
	"context"
	"strings"
	"testing"
)

// seed013 builds a database at schema 013 with two projects -- blog created
// first, shop second, listed in the opposite order so the id map has to
// sort -- their keys (one disabled), a retention override, and one row in
// every table 014 rebuilds.
func seed013(t *testing.T) *DB {
	t.Helper()
	db := newTestDBAt(t, 13)
	for _, q := range []string{
		`INSERT INTO projects (id, alias, name, identity, allowed_origins, retention, attributes, created_at) VALUES
		 ('u1','shop','Shop','anonymous','[]',NULL,'[]','2026-01-02 00:00:00'),
		 ('u2','blog','Blog','identified','["https://blog.example.com"]','{"views":{"raw_days":90}}','["plan"]','2026-01-01 00:00:00')`,
		`INSERT INTO ingest_keys (key, project, label) VALUES ('ak_blog_web','blog','web'),('ak_blog_ios','blog','ios'),('ak_shop_web','shop','web')`,
		`UPDATE ingest_keys SET disabled_at='2026-02-01 00:00:00' WHERE key='ak_blog_ios'`,
		`INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind, user_id, path) VALUES
		 ('v1','blog','2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','web','a1','user','u1','/a'),
		 ('v2','shop','2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','web','a2','connection','','/b')`,
		`INSERT INTO product_events (id, project, event_name, actor_id, ts, attributes, user_id, os, app_version, received_at, actor_kind) VALUES
		 ('e1','blog','signup','a1','2026-09-10T11:00:00Z','{"plan":"pro"}','u1','iOS','2.4.1','2026-09-10T11:00:00Z','user'),
		 ('e2','shop','buy','a2','2026-09-10T11:00:00Z','{}','','','','2026-09-10T11:00:00Z','connection')`,
		`INSERT INTO agg_views_daily VALUES ('blog','2026-09-01','web',10,25,12,3,600),('shop','2026-09-01','web',1,2,1,0,0)`,
		`INSERT INTO agg_views_paths VALUES ('blog','2026-09-01','/home',8,15)`,
		`INSERT INTO agg_views_hosts VALUES ('blog','2026-09-01','x.com',9,20)`,
		`INSERT INTO agg_views_referrers VALUES ('blog','2026-09-01','google',3,4)`,
		`INSERT INTO agg_views_utm VALUES ('blog','2026-09-01','nl','email','aug',6,9)`,
		`INSERT INTO agg_views_countries VALUES ('blog','2026-09-01','DE',7,18)`,
		`INSERT INTO agg_views_os VALUES ('blog','2026-09-01','iOS','17.4',3,6)`,
		`INSERT INTO agg_views_browsers VALUES ('blog','2026-09-01','Chrome','126',7,14)`,
		`INSERT INTO agg_views_app_versions VALUES ('blog','2026-09-01','iOS','2.4.1',5,12)`,
		`INSERT INTO agg_views_devices VALUES ('blog','2026-09-01','mobile','',5,9)`,
		`INSERT INTO agg_views_displays VALUES ('blog','2026-09-01','1920x1080',6,11)`,
		`INSERT INTO agg_product_daily VALUES ('blog','2026-09-01','signup',5,4)`,
		`INSERT INTO agg_product_totals VALUES ('blog','2026-09-01',5,4)`,
		`INSERT INTO agg_product_attrs VALUES ('blog','2026-09-01','signup','plan','pro',3,3)`,
		`INSERT INTO agg_identity_daily VALUES ('blog','2026-09-01','user','u1',1,1,5,2)`,
		`INSERT INTO agg_retention VALUES ('blog','user','2026-08-01',0,10),('blog','user','2026-08-01',7,4)`,
		`INSERT INTO actors VALUES ('blog','a1','user','2026-08-01','2026-09-10')`,
		`INSERT INTO identities (project, kind, id, name) VALUES ('blog','user','u1','Jane')`,
	} {
		if _, err := db.db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return db
}

// rebuilt014 is every table 014 gives a project_id column, with the number
// of rows seed013 puts in it.
var rebuilt014 = map[string]int{
	"views": 2, "events": 2,
	"agg_views_daily": 2, "agg_views_paths": 1, "agg_views_hosts": 1, "agg_views_referrers": 1,
	"agg_views_utm": 1, "agg_views_countries": 1, "agg_views_os": 1, "agg_views_browsers": 1,
	"agg_views_app_versions": 1, "agg_views_devices": 1, "agg_views_displays": 1,
	"agg_product_daily": 1, "agg_product_totals": 1, "agg_product_attrs": 1,
	"agg_identity_daily": 1, "agg_retention": 2, "actors": 1, "identities": 1,
	"ingest_keys": 3,
}

func TestMigration014AssignsIdsAndRekeys(t *testing.T) {
	db := seed013(t)
	ctx := context.Background()
	// Stop at 014: this test asserts the shape 014 leaves behind, so a
	// later migration must not be able to change what it sees.
	if err := db.migrateThrough(ctx, 14); err != nil {
		t.Fatalf("migration 014: %v", err)
	}
	row := func(q string, dst ...any) {
		t.Helper()
		if err := db.db.QueryRowContext(ctx, q).Scan(dst...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var s string
	var n int

	// ids follow created_at: blog (2026-01-01) is 1, shop is 2
	row(`SELECT id || ':' || name FROM projects ORDER BY id LIMIT 1`, &s)
	if s != "1:Blog" {
		t.Errorf("first project = %q, want 1:Blog", s)
	}
	row(`SELECT id || ':' || name FROM projects WHERE name='Shop'`, &s)
	if s != "2:Shop" {
		t.Errorf("shop = %q, want 2:Shop", s)
	}
	// alias and retention are gone
	row(`SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name IN ('alias','retention')`, &n)
	if n != 0 {
		t.Errorf("projects still has %d of alias/retention", n)
	}
	// every rebuilt table: same row count, every row on the right id, no
	// project column left, a project_id column present
	for table, want := range rebuilt014 {
		row(`SELECT COUNT(*) FROM `+table, &n)
		if n != want {
			t.Errorf("%s: %d rows, want %d", table, n, want)
		}
		row(`SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name='project'`, &n)
		if n != 0 {
			t.Errorf("%s still has a project column", table)
		}
		row(`SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name='project_id' AND type='INTEGER'`, &n)
		if n != 1 {
			t.Errorf("%s has no INTEGER project_id column", table)
		}
	}
	row(`SELECT project_id FROM views WHERE id='v2'`, &n)
	if n != 2 {
		t.Errorf("v2 project_id = %d, want 2 (shop)", n)
	}
	row(`SELECT project_id FROM events WHERE id='e1'`, &n)
	if n != 1 {
		t.Errorf("e1 project_id = %d, want 1 (blog)", n)
	}
	row(`SELECT project_id || '/' || label || '/' || (disabled_at IS NOT NULL) FROM ingest_keys WHERE key='ak_blog_ios'`, &s)
	if s != "1/ios/1" {
		t.Errorf("ak_blog_ios = %q, want 1/ios/1 (disabled state kept)", s)
	}
	// product_events is gone, no _new leftovers, the temp map is dropped
	row(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND (name='product_events' OR name LIKE '%\_new' ESCAPE '\')`, &n)
	if n != 0 {
		t.Errorf("%d stale tables survived", n)
	}
	row(`SELECT COUNT(*) FROM sqlite_temp_master WHERE name='project_map'`, &n)
	if n != 0 {
		t.Error("temp project_map was not dropped")
	}
	// indexes recreated on project_id
	for _, idx := range []string{"idx_views_project_ts", "idx_views_actor", "idx_views_session",
		"idx_views_project_day", "idx_events_project_name_ts", "idx_events_project_user_ts", "idx_actors_last_seen"} {
		row(`SELECT COALESCE((SELECT sql FROM sqlite_master WHERE type='index' AND name='`+idx+`'), '')`, &s)
		if !strings.Contains(s, "(project_id") {
			t.Errorf("index %s = %q, want it to lead with project_id", idx, s)
		}
	}
	row(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_ingest_keys_project'`, &n)
	if n != 0 {
		t.Error("idx_ingest_keys_project survived; UNIQUE (project_id, label) replaces it")
	}
	// every view is back and readable on project_id: 16 of them at 014.
	// 015 adds v_views_platforms, asserted in migration015_test.go.
	row(`SELECT COUNT(*) FROM sqlite_master WHERE type='view'`, &n)
	if n != 16 {
		t.Errorf("%d views, want 16", n)
	}
	row(`SELECT COUNT(*) FROM v_product_attrs WHERE project_id=1 AND attr_key='plan'`, &n)
	if n != 2 {
		t.Errorf("v_product_attrs plan rows for blog = %d, want 2 (one aggregated, one live)", n)
	}
	row(`SELECT SUM(visitors) FROM v_views_daily WHERE project_id=1`, &n)
	if n != 11 {
		t.Errorf("v_views_daily blog visitors = %d, want 11 (10 aggregated + 1 live)", n)
	}
	row(`SELECT cohort_size FROM v_retention WHERE project_id=1 AND day_offset=7`, &n)
	if n != 10 {
		t.Errorf("v_retention cohort_size = %d, want 10", n)
	}
}

func TestMigration014AutoincrementNeverReissues(t *testing.T) {
	db := seed013(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var id int64
	exec := func(q string) {
		t.Helper()
		if _, err := db.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO projects (name) VALUES ('Third')`)
	if err := db.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='Third'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 3 {
		t.Fatalf("next id = %d, want 3 (max + 1)", id)
	}
	exec(`DELETE FROM projects WHERE id=3`)
	exec(`INSERT INTO projects (name) VALUES ('Fourth')`)
	if err := db.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='Fourth'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 4 {
		t.Fatalf("id after a delete = %d, want 4 (a deleted id is never reissued)", id)
	}
}

// assertStillAt013 checks a failed 014 left the database exactly where it
// was: the old table, text ids, no version row, and no temp leftovers that
// would break a retry on the same connection.
func assertStillAt013(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	var n int
	var s string
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_events`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("product_events after failed migration: n=%d err=%v", n, err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT typeof(id) FROM projects LIMIT 1`).Scan(&s); err != nil || s != "text" {
		t.Fatalf("projects.id after failed migration: %q %v", s, err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=14`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("schema_migrations has version 14 after a failure: n=%d err=%v", n, err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_temp_master`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("temp objects survived the rollback: n=%d err=%v", n, err)
	}
}

func TestMigration014AbortsOnOrphanRow(t *testing.T) {
	db := seed013(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind, path)
		VALUES ('vx','ghost','2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','web','a9','connection','/x')`); err != nil {
		t.Fatal(err)
	}
	err := db.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "014") || !strings.Contains(err.Error(), "NOT NULL") {
		t.Fatalf("err = %v, want migration 014 to fail on NOT NULL project_id", err)
	}
	assertStillAt013(t, db)
	// The retry path: fix the data, migrate again on the same connection.
	if _, err := db.db.ExecContext(ctx, `DELETE FROM views WHERE id='vx'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("retry after fixing the orphan: %v", err)
	}
}

func TestMigration014AbortsOnDuplicateLabel(t *testing.T) {
	db := seed013(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO ingest_keys (key, project, label) VALUES ('ak_blog_web2','blog','web')`); err != nil {
		t.Fatal(err)
	}
	err := db.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "014") || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("err = %v, want migration 014 to fail on UNIQUE (project_id, label)", err)
	}
	assertStillAt013(t, db)
}
