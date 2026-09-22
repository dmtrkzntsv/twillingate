# Integer project ids Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the project alias with an integer `projects.id` as the only project key, rename the raw product table to `events`, and remove the alias-bound features (`project rename`, `config import`/`export`, per-project retention).

**Architecture:** One irreversible migration (`014_project_ids.sql`) rebuilds every project-keyed table with `project_id INTEGER` and recreates the views. The Go change then ripples bottom-up through the layers CLAUDE.md fixes: `store` → `store/sqlite` → `manage` → `config` → `server`/`pipeline` → `jobs` → `api` → `app`/`cmd`, then Evidence, scripts and docs. Every surface takes and returns `project_id`; the hash input becomes the id as a decimal string; retention comes only from `config.Retention`.

**Tech Stack:** Go 1.2x (stdlib, `modernc.org/sqlite`, `github.com/modelcontextprotocol/go-sdk`), SQLite, Evidence (DuckDB WASM) dashboards, bash scripts.

**Spec:** `docs/superpowers/specs/2026-09-19-project-ids-design.md` (PR #35). Read it first; every task below cites the section it implements.

## Global Constraints

- Migration file is `internal/store/sqlite/migrations/014_project_ids.sql`, one transaction, irreversible. `013_identity_daily_cap.sql` is the last migration on `main`; do not touch it.
- `projects.id INTEGER PRIMARY KEY AUTOINCREMENT`. Existing ids are assigned by `ROW_NUMBER() OVER (ORDER BY created_at, alias)`, starting at 1. A deleted id is never reissued.
- Every `project TEXT NOT NULL` column becomes `project_id INTEGER NOT NULL` **in the same position**, on all 20 data tables plus `ingest_keys`. Table shapes (`WITHOUT ROWID`, primary keys, indexes) are otherwise unchanged.
- The raw product table is renamed `product_events` → `events`. `agg_product_*`, `v_product_*`, the `product_events`/`product_attributes` tools and `RETENTION_PRODUCT_*` keep their names.
- Ingest key address is `(project_id, label)`, enforced by `UNIQUE (project_id, label)`.
- Hash input: callers pass `strconv.FormatInt(p.ID, 10)` to `identity.VisitorHash` / `identity.ActorHash`; the identity package is unchanged.
- MCP, REST and CLI take and return `project_id` (JSON integer). REST paths use `{project_id}`. CLI flags: `-id` on `project …`, `-project-id` on `key …`, `-name` required on `project create`, `-clear-origins` on `project update`.
- Unknown project error text: `unknown project 7; valid projects: 1 (Blog), 2 (Shop)`.
- Retention is global only (`config.Retention` from `RETENTION_*`). No per-project override anywhere.
- Removed outright: `project rename`, `config import`/`export`, `internal/manage/importexport.go`, the legacy `projects.json` types in `config`, `projects.alias`, `projects.retention`, the UUID project id.
- Non-goals (do not do): event ids stay UUIDv7; keys get no id; no lookup by name; `audit_log` history is not rewritten.
- CLAUDE.md: `docs/twillingate.md` changes ride in the same commit as the surface they describe; `schemaViews` in `internal/api/resources.go` changes with the migration.
- Commit messages: Conventional Commits. Per-task commits are `feat(...)`/`refactor(...)`/`docs(...)`; the PR title (the squash subject) is `feat!: identify projects by integer id instead of alias` with the `BREAKING CHANGE:` footer from Task 10.
- Go is at `/usr/local/go/bin/go` on the dev box and may not be on `PATH`.

## Build order and the broken window

`store.View.Project string` becomes `ProjectID int64` in Task 2, and every package above `store` stops compiling until its own task lands. That is expected: **from Task 2 through Task 7, `go build ./...` fails and each task verifies only the packages it leaves green** (the test command in every step names them). Task 8 restores the whole build; Task 10 runs `make check`. Do not "fix" a package ahead of its task to make the build pass — the fix is that task.

Work on a branch off `main` (`git checkout -b feat/project-ids main`). Never commit on `main`.

---

### Task 1: Migration 014 and its test

Implements spec §4 and §10 (migration test). Only SQL and one Go test file; the Go code that reads the new schema comes in Task 2, so after this task every other test in `internal/store/sqlite` fails at runtime (no such column `project`). That is expected until Task 2.

**Files:**
- Create: `internal/store/sqlite/migrations/014_project_ids.sql`
- Create: `internal/store/sqlite/migration014_test.go`

**Interfaces:**
- Consumes: `newTestDBAt(t, v)` from `migration012_test.go`; `(*DB).migrateThrough`.
- Produces: the schema every later task is written against. Tables listed in §4.1 step 5 carry `project_id INTEGER NOT NULL`; `events` replaces `product_events`; `projects(id INTEGER PRIMARY KEY AUTOINCREMENT, name, identity, allowed_origins, attributes, created_at, archived_at)`; `ingest_keys(key, project_id, label, created_at, disabled_at, UNIQUE(project_id,label))`; the 16 views recreated on `project_id`.

- [ ] **Step 1: Write the failing migration test**

Create `internal/store/sqlite/migration014_test.go`:

```go
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
	if err := db.Migrate(ctx); err != nil {
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
	// every view is back and readable on project_id
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd internal/store/sqlite && go test -run 'TestMigration014' -v .`
Expected: FAIL — `TestMigration014AssignsIdsAndRekeys` reports `no such column: project_id` (or migration 014 missing: `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name IN ('alias','retention')` returns 2).

- [ ] **Step 3: Write the migration**

Create `internal/store/sqlite/migrations/014_project_ids.sql` with exactly this content (it was validated against a seeded schema-013 database with sqlite3: happy path, orphan abort, duplicate-label abort, `max + 1`, no reissue after delete, retry on the same connection):

```sql
-- Projects are identified by an integer id, not an alias
-- (docs/superpowers/specs/2026-09-19-project-ids-design.md).
--
-- Every table keyed by the alias is rebuilt with `project_id INTEGER` in
-- the same position: SQLite cannot drop or retype a column that leads a
-- composite key, so each is CREATE _new / INSERT SELECT / DROP / RENAME,
-- the shape 012 used for actors. The raw product table takes its new name,
-- `events`, in the same rebuild. Views are dropped first (a rename with a
-- view still pointing at the old table fails) and recreated last.
--
-- Every copy LEFT JOINs the id map. A row whose project has no registry
-- row yields a NULL project_id, which fails NOT NULL and aborts the whole
-- migration -- an inner join would drop that row silently. An ingest key
-- label repeated within one project trips UNIQUE (project_id, label) the
-- same way. docs/deployment.md lists the pre-upgrade checks that find
-- both before the service is stopped.
--
-- Irreversible. Take a backup first.

-- ===== 1. views off =====
DROP VIEW IF EXISTS v_views_daily;
DROP VIEW IF EXISTS v_views_paths;
DROP VIEW IF EXISTS v_views_hosts;
DROP VIEW IF EXISTS v_views_referrers;
DROP VIEW IF EXISTS v_views_countries;
DROP VIEW IF EXISTS v_views_displays;
DROP VIEW IF EXISTS v_views_os;
DROP VIEW IF EXISTS v_views_browsers;
DROP VIEW IF EXISTS v_views_app_versions;
DROP VIEW IF EXISTS v_views_devices;
DROP VIEW IF EXISTS v_views_utm;
DROP VIEW IF EXISTS v_product_daily;
DROP VIEW IF EXISTS v_product_totals;
DROP VIEW IF EXISTS v_product_attrs;
DROP VIEW IF EXISTS v_retention;
DROP VIEW IF EXISTS v_identity_daily;
DROP VIEW IF EXISTS v_events_flat;

-- ===== 2. the id map =====
-- Ids follow creation order, alias breaking ties, starting at 1.
CREATE TEMP TABLE project_map (alias TEXT PRIMARY KEY, id INTEGER NOT NULL);
INSERT INTO project_map (alias, id)
SELECT alias, ROW_NUMBER() OVER (ORDER BY created_at, alias) FROM projects;

-- ===== 3. projects =====
-- AUTOINCREMENT so a deleted project's id is never reissued: stale
-- references (audit rows, bookmarks, agent memory) must not silently land
-- on a new project. Explicit ids advance sqlite_sequence, so the next
-- project gets max + 1. alias and retention are not carried over.
CREATE TABLE projects_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    identity        TEXT NOT NULL DEFAULT 'anonymous',
    allowed_origins TEXT NOT NULL DEFAULT '[]',
    attributes      TEXT NOT NULL DEFAULT '[]',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    archived_at     TEXT                                -- NULL = active
);
INSERT INTO projects_new (id, name, identity, allowed_origins, attributes, created_at, archived_at)
SELECT m.id, p.name, p.identity, p.allowed_origins, p.attributes, p.created_at, p.archived_at
FROM projects p JOIN project_map m ON m.alias = p.alias;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;

-- ===== 4. ingest_keys =====
-- UNIQUE (project_id, label) replaces the count-then-insert check in Go and
-- the idx_ingest_keys_project index, which the DROP takes with it.
CREATE TABLE ingest_keys_new (
    key         TEXT PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id),
    label       TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    disabled_at TEXT,                                   -- NULL = active
    UNIQUE (project_id, label)
);
INSERT INTO ingest_keys_new (key, project_id, label, created_at, disabled_at)
SELECT k.key, m.id, k.label, k.created_at, k.disabled_at
FROM ingest_keys k LEFT JOIN project_map m ON m.alias = k.project;
DROP TABLE ingest_keys;
ALTER TABLE ingest_keys_new RENAME TO ingest_keys;

-- ===== 5. raw tables =====
CREATE TABLE views_new (
    id              TEXT PRIMARY KEY,
    project_id      INTEGER NOT NULL,
    ts              TEXT NOT NULL,
    -- See 012 for why day is a stored generated column.
    day             TEXT GENERATED ALWAYS AS (substr(ts,1,10)) STORED,
    received_at     TEXT NOT NULL,
    kind            TEXT NOT NULL,
    actor_id        TEXT NOT NULL,
    actor_kind      TEXT NOT NULL,
    user_id         TEXT NOT NULL DEFAULT '',
    group_id        TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    host            TEXT NOT NULL DEFAULT '',
    path            TEXT NOT NULL,
    referrer_source TEXT NOT NULL DEFAULT '',
    utm_source      TEXT NOT NULL DEFAULT '',
    utm_medium      TEXT NOT NULL DEFAULT '',
    utm_campaign    TEXT NOT NULL DEFAULT '',
    os              TEXT NOT NULL DEFAULT '',
    os_version      TEXT NOT NULL DEFAULT '',
    browser         TEXT NOT NULL DEFAULT '',
    browser_version TEXT NOT NULL DEFAULT '',
    app_version     TEXT NOT NULL DEFAULT '',
    device          TEXT NOT NULL DEFAULT '',
    device_model    TEXT NOT NULL DEFAULT '',
    locale          TEXT NOT NULL DEFAULT '',
    display_width   INTEGER NOT NULL DEFAULT 0,
    display_height  INTEGER NOT NULL DEFAULT 0,
    country         TEXT NOT NULL DEFAULT ''
);
INSERT INTO views_new (id, project_id, ts, received_at, kind, actor_id, actor_kind, user_id, group_id,
    session_id, host, path, referrer_source, utm_source, utm_medium, utm_campaign, os, os_version,
    browser, browser_version, app_version, device, device_model, locale, display_width, display_height, country)
SELECT v.id, m.id, v.ts, v.received_at, v.kind, v.actor_id, v.actor_kind, v.user_id, v.group_id,
    v.session_id, v.host, v.path, v.referrer_source, v.utm_source, v.utm_medium, v.utm_campaign, v.os, v.os_version,
    v.browser, v.browser_version, v.app_version, v.device, v.device_model, v.locale, v.display_width, v.display_height, v.country
FROM views v LEFT JOIN project_map m ON m.alias = v.project;
DROP TABLE views;
ALTER TABLE views_new RENAME TO views;
CREATE INDEX idx_views_project_ts  ON views(project_id, ts);
CREATE INDEX idx_views_actor       ON views(project_id, actor_id, ts);
CREATE INDEX idx_views_session     ON views(project_id, session_id, ts);
CREATE INDEX idx_views_project_day ON views(project_id, day);

-- product_events is rebuilt under its new name. The product *family*
-- (agg_product_*, v_product_*, the product_events tool) keeps its prefix;
-- only the raw table is renamed, and its indexes already carried the name.
CREATE TABLE events (
    id          TEXT PRIMARY KEY,                       -- UUIDv7
    project_id  INTEGER NOT NULL,
    event_name  TEXT NOT NULL,
    actor_id    TEXT NOT NULL,
    ts          TEXT NOT NULL,
    attributes  TEXT NOT NULL DEFAULT '{}',             -- JSON
    user_id     TEXT NOT NULL DEFAULT '',
    group_id    TEXT NOT NULL DEFAULT '',
    os          TEXT NOT NULL DEFAULT '',
    app_version TEXT NOT NULL DEFAULT '',
    received_at TEXT NOT NULL DEFAULT '',
    actor_kind  TEXT NOT NULL DEFAULT ''
);
INSERT INTO events (id, project_id, event_name, actor_id, ts, attributes, user_id, group_id, os, app_version, received_at, actor_kind)
SELECT e.id, m.id, e.event_name, e.actor_id, e.ts, e.attributes, e.user_id, e.group_id, e.os, e.app_version, e.received_at, e.actor_kind
FROM product_events e LEFT JOIN project_map m ON m.alias = e.project;
DROP TABLE product_events;
CREATE INDEX idx_events_project_name_ts ON events(project_id, event_name, ts);
CREATE INDEX idx_events_project_user_ts ON events(project_id, actor_id, ts);

-- ===== 6. views aggregates =====
CREATE TABLE agg_views_daily_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, kind TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    sessions INTEGER NOT NULL, bounces INTEGER NOT NULL, duration_sec INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, kind)
) WITHOUT ROWID;
INSERT INTO agg_views_daily_new SELECT m.id, a.day, a.kind, a.visitors, a.views, a.sessions, a.bounces, a.duration_sec
FROM agg_views_daily a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_daily;
ALTER TABLE agg_views_daily_new RENAME TO agg_views_daily;

CREATE TABLE agg_views_paths_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, path TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, path)
) WITHOUT ROWID;
INSERT INTO agg_views_paths_new SELECT m.id, a.day, a.path, a.visitors, a.views
FROM agg_views_paths a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_paths;
ALTER TABLE agg_views_paths_new RENAME TO agg_views_paths;

CREATE TABLE agg_views_hosts_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, host TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, host)
) WITHOUT ROWID;
INSERT INTO agg_views_hosts_new SELECT m.id, a.day, a.host, a.visitors, a.views
FROM agg_views_hosts a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_hosts;
ALTER TABLE agg_views_hosts_new RENAME TO agg_views_hosts;

CREATE TABLE agg_views_referrers_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, source TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, source)
) WITHOUT ROWID;
INSERT INTO agg_views_referrers_new SELECT m.id, a.day, a.source, a.visitors, a.views
FROM agg_views_referrers a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_referrers;
ALTER TABLE agg_views_referrers_new RENAME TO agg_views_referrers;

CREATE TABLE agg_views_utm_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    utm_source TEXT NOT NULL, utm_medium TEXT NOT NULL, utm_campaign TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, utm_source, utm_medium, utm_campaign)
) WITHOUT ROWID;
INSERT INTO agg_views_utm_new SELECT m.id, a.day, a.utm_source, a.utm_medium, a.utm_campaign, a.visitors, a.views
FROM agg_views_utm a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_utm;
ALTER TABLE agg_views_utm_new RENAME TO agg_views_utm;

CREATE TABLE agg_views_countries_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, country TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, country)
) WITHOUT ROWID;
INSERT INTO agg_views_countries_new SELECT m.id, a.day, a.country, a.visitors, a.views
FROM agg_views_countries a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_countries;
ALTER TABLE agg_views_countries_new RENAME TO agg_views_countries;

CREATE TABLE agg_views_os_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, os TEXT NOT NULL, os_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, os, os_version)
) WITHOUT ROWID;
INSERT INTO agg_views_os_new SELECT m.id, a.day, a.os, a.os_version, a.visitors, a.views
FROM agg_views_os a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_os;
ALTER TABLE agg_views_os_new RENAME TO agg_views_os;

CREATE TABLE agg_views_browsers_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, browser TEXT NOT NULL, browser_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, browser, browser_version)
) WITHOUT ROWID;
INSERT INTO agg_views_browsers_new SELECT m.id, a.day, a.browser, a.browser_version, a.visitors, a.views
FROM agg_views_browsers a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_browsers;
ALTER TABLE agg_views_browsers_new RENAME TO agg_views_browsers;

CREATE TABLE agg_views_app_versions_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, os TEXT NOT NULL, app_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, os, app_version)
) WITHOUT ROWID;
INSERT INTO agg_views_app_versions_new SELECT m.id, a.day, a.os, a.app_version, a.visitors, a.views
FROM agg_views_app_versions a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_app_versions;
ALTER TABLE agg_views_app_versions_new RENAME TO agg_views_app_versions;

CREATE TABLE agg_views_devices_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, device TEXT NOT NULL, device_model TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, device, device_model)
) WITHOUT ROWID;
INSERT INTO agg_views_devices_new SELECT m.id, a.day, a.device, a.device_model, a.visitors, a.views
FROM agg_views_devices a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_devices;
ALTER TABLE agg_views_devices_new RENAME TO agg_views_devices;

CREATE TABLE agg_views_displays_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, display TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, display)
) WITHOUT ROWID;
INSERT INTO agg_views_displays_new SELECT m.id, a.day, a.display, a.visitors, a.views
FROM agg_views_displays a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_views_displays;
ALTER TABLE agg_views_displays_new RENAME TO agg_views_displays;

-- ===== 7. product aggregates =====
CREATE TABLE agg_product_daily_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, event_name TEXT NOT NULL,
    count INTEGER NOT NULL, unique_users INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, event_name)
) WITHOUT ROWID;
INSERT INTO agg_product_daily_new SELECT m.id, a.day, a.event_name, a.count, a.unique_users
FROM agg_product_daily a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_product_daily;
ALTER TABLE agg_product_daily_new RENAME TO agg_product_daily;

CREATE TABLE agg_product_totals_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    total_events INTEGER NOT NULL, active_users INTEGER NOT NULL,
    PRIMARY KEY (project_id, day)
) WITHOUT ROWID;
INSERT INTO agg_product_totals_new SELECT m.id, a.day, a.total_events, a.active_users
FROM agg_product_totals a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_product_totals;
ALTER TABLE agg_product_totals_new RENAME TO agg_product_totals;

CREATE TABLE agg_product_attrs_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, event_name TEXT NOT NULL,
    attr_key TEXT NOT NULL, attr_value TEXT NOT NULL,
    count INTEGER NOT NULL, unique_users INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, event_name, attr_key, attr_value)
) WITHOUT ROWID;
INSERT INTO agg_product_attrs_new SELECT m.id, a.day, a.event_name, a.attr_key, a.attr_value, a.count, a.unique_users
FROM agg_product_attrs a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_product_attrs;
ALTER TABLE agg_product_attrs_new RENAME TO agg_product_attrs;

-- ===== 8. identity and retention =====
CREATE TABLE agg_identity_daily_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    kind TEXT NOT NULL, id TEXT NOT NULL,
    actors INTEGER NOT NULL, users INTEGER NOT NULL,
    views INTEGER NOT NULL, events INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, kind, id)
) WITHOUT ROWID;
INSERT INTO agg_identity_daily_new SELECT m.id, a.day, a.kind, a.id, a.actors, a.users, a.views, a.events
FROM agg_identity_daily a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_identity_daily;
ALTER TABLE agg_identity_daily_new RENAME TO agg_identity_daily;

CREATE TABLE agg_retention_new (
    project_id INTEGER NOT NULL, actor_kind TEXT NOT NULL,
    cohort_day TEXT NOT NULL, day_offset INTEGER NOT NULL,
    actors INTEGER NOT NULL,
    PRIMARY KEY (project_id, actor_kind, cohort_day, day_offset)
) WITHOUT ROWID;
INSERT INTO agg_retention_new SELECT m.id, a.actor_kind, a.cohort_day, a.day_offset, a.actors
FROM agg_retention a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE agg_retention;
ALTER TABLE agg_retention_new RENAME TO agg_retention;

CREATE TABLE actors_new (
    project_id INTEGER NOT NULL, actor_id TEXT NOT NULL,
    actor_kind TEXT NOT NULL,
    first_seen_day TEXT NOT NULL,
    last_seen_day  TEXT NOT NULL,
    PRIMARY KEY (project_id, actor_id)
) WITHOUT ROWID;
INSERT INTO actors_new SELECT m.id, a.actor_id, a.actor_kind, a.first_seen_day, a.last_seen_day
FROM actors a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE actors;
ALTER TABLE actors_new RENAME TO actors;
CREATE INDEX idx_actors_last_seen ON actors(project_id, last_seen_day);

CREATE TABLE identities_new (
    project_id    INTEGER NOT NULL,
    kind          TEXT NOT NULL,
    id            TEXT NOT NULL,
    name          TEXT NOT NULL,
    last_seen_day TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (project_id, kind, id)
) WITHOUT ROWID;
INSERT INTO identities_new SELECT m.id, a.kind, a.id, a.name, a.last_seen_day, a.updated_at
FROM identities a LEFT JOIN project_map m ON m.alias = a.project;
DROP TABLE identities;
ALTER TABLE identities_new RENAME TO identities;

DROP TABLE project_map;

-- ===== 9. views back, on project_id =====
-- Identical to 012 and 013 apart from the key column, the raw product
-- table's name, and v_product_attrs joining projects on id. v_events_flat
-- is not recreated here: the boot-time RebuildFlatView builds it.
CREATE VIEW v_views_daily AS
SELECT project_id, day, kind, visitors, views, sessions, bounces, duration_sec FROM agg_views_daily
UNION ALL
SELECT c.project_id, c.day, c.kind, c.visitors, c.views, p.sessions, p.bounces, p.duration_sec
FROM (
  -- visitors and views per bucketed kind
  SELECT b.project_id, b.day, b.kind, COUNT(DISTINCT b.actor_id) AS visitors, COUNT(*) AS views
  FROM (
    SELECT v.project_id, v.day,
           CASE WHEN k.rn <= 500 THEN v.kind ELSE '(other)' END AS kind, v.actor_id
    FROM views v
    JOIN (
      SELECT project_id, day, kind,
             ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, kind) AS rn
      FROM views GROUP BY project_id, day, kind
    ) k ON k.project_id = v.project_id AND k.day = v.day AND k.kind = v.kind
  ) b
  GROUP BY b.project_id, b.day, b.kind
) c
JOIN (
  -- sessions, bounces and duration per bucketed kind
  WITH src AS (
    SELECT v.project_id, v.day,
           CASE WHEN k.rn <= 500 THEN v.kind ELSE '(other)' END AS kind,
           v.actor_id, v.session_id, CAST(strftime('%s', v.ts) AS INTEGER) AS t
    FROM views v
    JOIN (
      SELECT project_id, day, kind,
             ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, kind) AS rn
      FROM views GROUP BY project_id, day, kind
    ) k ON k.project_id = v.project_id AND k.day = v.day AND k.kind = v.kind
  ),
  marked AS (
    SELECT project_id, day, kind, actor_id, session_id, t,
           CASE WHEN session_id <> '' THEN 0
                WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
                ELSE 0 END AS new_session
    FROM src WINDOW w AS (PARTITION BY project_id, day, kind, actor_id ORDER BY t)
  ),
  keyed AS (
    SELECT project_id, day, kind, actor_id, t,
           CASE WHEN session_id <> '' THEN session_id
                ELSE CAST(SUM(new_session) OVER (PARTITION BY project_id, day, kind, actor_id ORDER BY t) AS TEXT)
           END AS skey
    FROM marked
  ),
  spans AS (
    SELECT project_id, day, kind, actor_id, skey, COUNT(*) AS view_count, MAX(t) - MIN(t) AS dur
    FROM keyed GROUP BY project_id, day, kind, actor_id, skey
  )
  SELECT project_id, day, kind, COUNT(*) AS sessions,
         SUM(CASE WHEN view_count = 1 THEN 1 ELSE 0 END) AS bounces,
         COALESCE(SUM(dur), 0) AS duration_sec
  FROM spans GROUP BY project_id, day, kind
) p ON p.project_id = c.project_id AND p.day = c.day AND p.kind = c.kind;

CREATE VIEW v_views_paths AS
SELECT project_id, day, path, visitors, views FROM agg_views_paths
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.path, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, path,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, path) AS rn
    FROM views GROUP BY project_id, day, path
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.path = v.path
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END;

CREATE VIEW v_views_hosts AS
SELECT project_id, day, host, visitors, views FROM agg_views_hosts
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.host, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, host,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, host) AS rn
    FROM views GROUP BY project_id, day, host
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.host = v.host
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END;

CREATE VIEW v_views_referrers AS
SELECT project_id, day, source, visitors, views FROM agg_views_referrers
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.referrer_source AS source, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, referrer_source AS source,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, referrer_source) AS rn
    FROM views GROUP BY project_id, day, referrer_source
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.source = v.referrer_source
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END;

CREATE VIEW v_views_countries AS
SELECT project_id, day, country, visitors, views FROM agg_views_countries
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.country, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, country,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, country) AS rn
    FROM views GROUP BY project_id, day, country
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.country = v.country
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END;

CREATE VIEW v_views_displays AS
SELECT project_id, day, display, visitors, views FROM agg_views_displays
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.display_width || 'x' || v.display_height AS display, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, display_width || 'x' || display_height AS display,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, display_width || 'x' || display_height) AS rn
    FROM views WHERE display_width > 0 AND display_height > 0
    GROUP BY project_id, day, display_width || 'x' || display_height
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.display = v.display_width || 'x' || v.display_height
  WHERE v.display_width > 0 AND v.display_height > 0
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END;

CREATE VIEW v_views_os AS
SELECT project_id, day, os, os_version, visitors, views FROM agg_views_os
UNION ALL
SELECT project_id, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.os, v.os_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, os, os_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, os, os_version) AS rn
    FROM views GROUP BY project_id, day, os, os_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.os = v.os AND r.os_version = v.os_version
)
GROUP BY project_id, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END;

CREATE VIEW v_views_browsers AS
SELECT project_id, day, browser, browser_version, visitors, views FROM agg_views_browsers
UNION ALL
SELECT project_id, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.browser, v.browser_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, browser, browser_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, browser, browser_version) AS rn
    FROM views GROUP BY project_id, day, browser, browser_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.browser = v.browser AND r.browser_version = v.browser_version
)
GROUP BY project_id, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END;

CREATE VIEW v_views_app_versions AS
SELECT project_id, day, os, app_version, visitors, views FROM agg_views_app_versions
UNION ALL
SELECT project_id, day, os, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.os, v.app_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, os, app_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, os, app_version) AS rn
    FROM views WHERE app_version <> '' GROUP BY project_id, day, os, app_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.os = v.os AND r.app_version = v.app_version
  WHERE v.app_version <> ''
)
GROUP BY project_id, day, os, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END;

CREATE VIEW v_views_devices AS
SELECT project_id, day, device, device_model, visitors, views FROM agg_views_devices
UNION ALL
SELECT project_id, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.device, v.device_model, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, device, device_model,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, device, device_model) AS rn
    FROM views GROUP BY project_id, day, device, device_model
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.device = v.device AND r.device_model = v.device_model
)
GROUP BY project_id, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END;

CREATE VIEW v_views_utm AS
SELECT project_id, day, utm_source, utm_medium, utm_campaign, visitors, views FROM agg_views_utm
UNION ALL
SELECT project_id, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.utm_source, v.utm_medium, v.utm_campaign, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, utm_source, utm_medium, utm_campaign,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, utm_source, utm_medium, utm_campaign) AS rn
    FROM views WHERE NOT (utm_source='' AND utm_medium='' AND utm_campaign='')
    GROUP BY project_id, day, utm_source, utm_medium, utm_campaign
  ) r ON r.project_id = v.project_id AND r.day = v.day
     AND r.utm_source = v.utm_source AND r.utm_medium = v.utm_medium AND r.utm_campaign = v.utm_campaign
  WHERE NOT (v.utm_source='' AND v.utm_medium='' AND v.utm_campaign='')
)
GROUP BY project_id, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END;

CREATE VIEW v_product_daily AS
SELECT project_id, day, event_name, count, unique_users FROM agg_product_daily
UNION ALL
SELECT project_id, substr(ts,1,10), event_name, COUNT(*), COUNT(DISTINCT actor_id)
FROM events GROUP BY project_id, substr(ts,1,10), event_name;

CREATE VIEW v_product_totals AS
SELECT project_id, day, total_events, active_users FROM agg_product_totals
UNION ALL
SELECT project_id, substr(ts,1,10), COUNT(*), COUNT(DISTINCT actor_id)
FROM events GROUP BY project_id, substr(ts,1,10);

CREATE VIEW v_product_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta
                   WHERE key='product_attributes_top_n'
                     AND CAST(value AS INTEGER) > 0), 50) AS n
),
declared AS (
  SELECT DISTINCT p.id AS project_id, j.value AS attr_key
  FROM projects p,
       json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
  WHERE j.type = 'text'
),
vals AS (
  SELECT e.project_id AS project_id, substr(e.ts,1,10) AS day,
         e.event_name AS event_name, d.attr_key AS attr_key,
         json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') AS attr_value,
         e.actor_id AS actor_id
  FROM events e
  JOIN declared d ON d.project_id = e.project_id
  WHERE json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') IS NOT NULL
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$os', os, actor_id
  FROM events WHERE os <> ''
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$app_version', app_version, actor_id
  FROM events WHERE app_version <> ''
),
counted AS (
  SELECT project_id, day, event_name, attr_key, attr_value,
         COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u
  FROM vals
  GROUP BY project_id, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT project_id, day, event_name, attr_key, attr_value, c, u,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, attr_key
                            ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT project_id, day, event_name, attr_key, attr_value, count, unique_users
FROM agg_product_attrs
UNION ALL
SELECT project_id, day, event_name, attr_key, attr_value, c, u
FROM ranked
WHERE rn <= (SELECT n FROM cap)
UNION ALL
SELECT v.project_id, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id)
FROM vals v
WHERE NOT EXISTS (
  SELECT 1 FROM ranked r
  WHERE r.project_id = v.project_id AND r.day = v.day
    AND r.event_name = v.event_name AND r.attr_key = v.attr_key
    AND r.attr_value = v.attr_value
    AND r.rn <= (SELECT n FROM cap))
GROUP BY v.project_id, v.day, v.event_name, v.attr_key;

CREATE VIEW v_retention AS
SELECT r.project_id, r.actor_kind, r.cohort_day, r.day_offset, r.actors,
       c.actors AS cohort_size
FROM agg_retention r
JOIN agg_retention c
  ON c.project_id = r.project_id AND c.actor_kind = r.actor_kind
 AND c.cohort_day = r.cohort_day AND c.day_offset = 0;

CREATE VIEW v_identity_daily AS
SELECT project_id, day, kind, id, actors, users, views, events
FROM agg_identity_daily
UNION ALL
SELECT project_id, day, kind, id, actors, users, views, events
FROM (
  SELECT project_id, day, kind, id,
         COUNT(DISTINCT actor_id) AS actors,
         CASE WHEN kind = 'user' THEN 1 ELSE COUNT(DISTINCT NULLIF(user_id, '')) END AS users,
         SUM(is_view) AS views, SUM(is_event) AS events,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, kind ORDER BY COUNT(*) DESC, id) AS rn
  FROM (
    SELECT project_id, day, 'user' AS kind, user_id AS id,
           actor_id, user_id, 1 AS is_view, 0 AS is_event
    FROM views WHERE user_id <> ''
    UNION ALL
    SELECT project_id, substr(ts,1,10), 'user', user_id, actor_id, user_id, 0, 1
    FROM events WHERE user_id <> ''
    UNION ALL
    SELECT project_id, day, 'group', group_id, actor_id, user_id, 1, 0
    FROM views WHERE group_id <> ''
    UNION ALL
    SELECT project_id, substr(ts,1,10), 'group', group_id, actor_id, user_id, 0, 1
    FROM events WHERE group_id <> ''
  )
  GROUP BY project_id, day, kind, id
) live
WHERE rn <= 500
  AND NOT EXISTS (
    SELECT 1 FROM agg_identity_daily g
    WHERE g.project_id = live.project_id AND g.day = live.day);
```

The file is 683 lines. Sections 1–8 are the tables; section 9 is the 16 `CREATE VIEW` statements from `012_views.sql` and `013_identity_daily_cap.sql` with `project` → `project_id`, `product_events` → `events` (alias `pe` → `e`), and `p.alias AS project` → `p.id AS project_id` in `v_product_attrs`' `declared` CTE. Nothing else in a view changes.

- [ ] **Step 4: Run the migration tests to verify they pass**

Run: `cd internal/store/sqlite && go test -run 'TestMigration014' -v .`
Expected: PASS for all four. (`go test .` without `-run` fails at runtime in other tests — `no such column: project` — until Task 2.)

Also confirm 012's test still targets the schema it tests: in `migration012_test.go` change `db.Migrate(ctx)` to `db.migrateThrough(ctx, 13)` — it asserts on `product_events`, which 014 renames. Run: `go test -run 'TestMigration012' .` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlite/migrations/014_project_ids.sql internal/store/sqlite/migration014_test.go internal/store/sqlite/migration012_test.go
git commit -m "feat(store): migrate projects to integer ids and rename product_events to events"
```

---

### Task 2: Store types and the SQLite implementation

Implements spec §6.1 and §4.3 (every `FROM product_events` in the store). After this task `internal/store/...` builds and passes; `manage`, `server`, `pipeline`, `jobs`, `api`, `app`, `cmd` do not compile until their tasks.

**Files:**
- Modify: `internal/store/store.go`, `internal/store/errors.go`
- Modify: `internal/store/sqlite/registry.go` (rewrite), `write.go`, `flatview.go`, `aggregate_views.go`, `aggregate_product.go`, `retention.go`, `identities.go`, `prune.go`
- Modify tests: `registry_test.go`, `write_test.go`, `views_test.go`, `aggregate_views_test.go`, `aggregate_product_test.go`, `retention_test.go`, `identities_test.go`, `prune_test.go`, `flatview_test.go`, `coverage_test.go`, `errors_test.go`, `typed_errors_test.go`, `bench_test.go`, `zz_seed_test.go`

**Interfaces:**
- Produces (the `store` package every later task builds on):

```go
type View struct {
	ID                                             string
	ProjectID                                      int64
	TS, ReceivedAt                                 time.Time
	Kind                                           string
	ActorID, ActorKind, UserID, GroupID, SessionID string
	Host, Path, ReferrerSource                     string
	UTMSource, UTMMedium, UTMCampaign              string
	OS, OSVersion, Browser, BrowserVersion         string
	AppVersion, Device, DeviceModel, Locale        string
	DisplayWidth, DisplayHeight                    int
	Country                                        string
}

type ProductEvent struct {
	ID                 string
	ProjectID          int64
	EventName          string
	TS, ReceivedAt     time.Time
	ActorID, ActorKind string
	UserID, GroupID    string
	OS, AppVersion     string
	Attributes         map[string]string
}

type Identity struct {
	ProjectID     int64
	Kind, ID, Name string
}

type RegistryProject struct {
	ID             int64  // 0 on create; assigned by the store
	Name, Identity string
	AllowedOrigins string // JSON array, "[]" if none
	Attributes     string // JSON array, "[]" if none declared
	Archived       bool
}

type RegistryKey struct {
	Key       string
	ProjectID int64
	Label     string
	Disabled  bool
}

type Store interface {
	Migrate(ctx context.Context) error
	WriteViews(ctx context.Context, views []View) error
	WriteProductEvents(ctx context.Context, evs []ProductEvent) error
	UpsertIdentities(ctx context.Context, ids []Identity) error
	ViewDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	AggregateViewDay(ctx context.Context, projectID int64, day civil.Date) error
	AggregateProductDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error
	UpsertActors(ctx context.Context, projectID int64, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, projectID int64, day civil.Date) error
	PruneActors(ctx context.Context, projectID int64, before civil.Date) error
	AggregateIdentityDay(ctx context.Context, projectID int64, day civil.Date) error
	PruneIdentities(ctx context.Context, projectID int64, before civil.Date) error
	PruneAggregates(ctx context.Context, projectID int64, viewsBefore, productBefore civil.Date) error
	IncrementalVacuum(ctx context.Context) error
	ProjectIDs(ctx context.Context) ([]int64, error) // all rows incl. archived, ascending
	RebuildFlatView(ctx context.Context, keys []string) error
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
	LoadRegistry(ctx context.Context) ([]RegistryProject, []RegistryKey, error)
	ConfigVersion(ctx context.Context) (int64, error)
	// CreateProject and CreateProjectWithKey return the id SQLite assigned.
	// The store fills the audit subjects itself (the id, and id/label),
	// because the caller cannot know the id before the insert.
	CreateProject(ctx context.Context, p RegistryProject, a AuditEntry) (int64, error)
	CreateProjectWithKey(ctx context.Context, p RegistryProject, k RegistryKey, projectAudit, keyAudit AuditEntry) (int64, error)
	UpdateProject(ctx context.Context, p RegistryProject, a AuditEntry) error // p.ID selects the row
	SetProjectArchived(ctx context.Context, id int64, archived bool, a AuditEntry) error
	InsertIngestKey(ctx context.Context, k RegistryKey, a AuditEntry) error
	SetIngestKeyDisabled(ctx context.Context, projectID int64, label string, disabled bool, a AuditEntry) error
	DeleteProjectData(ctx context.Context, id int64, a AuditEntry) error
	Close() error
}
```

`RenameProject` is gone from the interface. `ErrConflict`'s comment becomes "the key label is already taken for that project".

- [ ] **Step 1: Write the failing store tests**

Add to `internal/store/sqlite/registry_test.go` (keep the existing helpers; the rest of the file is adapted in Step 3):

```go
func TestInsertProjectReturnsIncreasingIds(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "test", Action: "project.create"}
	first, err := db.CreateProject(ctx, store.RegistryProject{Name: "Blog", Identity: "anonymous", AllowedOrigins: "[]", Attributes: "[]"}, audit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateProject(ctx, store.RegistryProject{Name: "Blog", Identity: "anonymous", AllowedOrigins: "[]", Attributes: "[]"}, audit)
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
	id, err := db.CreateProject(ctx, store.RegistryProject{Name: "Blog", Identity: "anonymous", AllowedOrigins: "[]", Attributes: "[]"},
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
	other, _ := db.CreateProject(ctx, store.RegistryProject{Name: "Shop", Identity: "anonymous", AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err := db.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_3", ProjectID: other, Label: "web"}, audit); err != nil {
		t.Fatalf("same label on another project: %v", err)
	}
}

func TestProjectIDsAscending(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, n := range []string{"c", "a", "b"} {
		if _, err := db.CreateProject(ctx, store.RegistryProject{Name: n, Identity: "anonymous", AllowedOrigins: "[]", Attributes: "[]"},
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
```

`errors` and `store` are already imported in that file.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd internal/store/sqlite && go test -run 'TestInsertProjectReturnsIncreasingIds|TestInsertKeyConflictIsTyped|TestProjectIDsAscending' .`
Expected: compile error (`db.CreateProject` returns one value; `store.RegistryKey` has no field `ProjectID`).

- [ ] **Step 3: Change the store types and the SQLite code**

`internal/store/store.go`: replace the types and interface with the block under **Interfaces** above (keep the `ActorUser…`, `KindUser…` constants, `AuditEntry`, `Register`/`Open` unchanged).

`internal/store/errors.go`: the `ErrConflict` comment becomes `// ErrConflict: the key label is already taken for that project.` and the package comment's example becomes `("update project: unknown id 7: not found")`.

`internal/store/sqlite/registry.go` — replace the whole file:

```go
// Package sqlite provides SQLite backend for the store interface.
// Registry row access (managed-config spec §3). Every write bumps
// meta.config_version and inserts its audit row in the same transaction,
// which is what lets other processes notice changes by polling one row.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func auditAndBump(ctx context.Context, tx *sql.Tx, a store.AuditEntry) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (actor, action, subject, detail) VALUES (?,?,?,?)`,
		a.Actor, a.Action, a.Subject, a.Detail); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)
		 WHERE key = 'config_version'`)
	return err
}

func (d *DB) ConfigVersion(ctx context.Context) (int64, error) {
	var v int64
	err := d.db.QueryRowContext(ctx,
		`SELECT CAST(value AS INTEGER) FROM meta WHERE key='config_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

func (d *DB) LoadRegistry(ctx context.Context) ([]store.RegistryProject, []store.RegistryKey, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, identity,
		allowed_origins, attributes, archived_at IS NOT NULL FROM projects ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ps []store.RegistryProject
	for rows.Next() {
		var p store.RegistryProject
		if err := rows.Scan(&p.ID, &p.Name, &p.Identity,
			&p.AllowedOrigins, &p.Attributes, &p.Archived); err != nil {
			return nil, nil, err
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	krows, err := d.db.QueryContext(ctx, `SELECT key, project_id, label,
		disabled_at IS NOT NULL FROM ingest_keys ORDER BY project_id, label`)
	if err != nil {
		return nil, nil, err
	}
	defer krows.Close()
	var ks []store.RegistryKey
	for krows.Next() {
		var k store.RegistryKey
		if err := krows.Scan(&k.Key, &k.ProjectID, &k.Label, &k.Disabled); err != nil {
			return nil, nil, err
		}
		ks = append(ks, k)
	}
	return ks2(ps, ks, krows.Err())
}

// ks2 keeps the happy-path return on one line above.
func ks2(ps []store.RegistryProject, ks []store.RegistryKey, err error) ([]store.RegistryProject, []store.RegistryKey, error) {
	if err != nil {
		return nil, nil, err
	}
	return ps, ks, nil
}

// CreateProject lets SQLite assign the id and returns it. The audit subject
// is the new id, written here because only the store knows it.
func (d *DB) CreateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) (int64, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = insertProject(ctx, tx, p); err != nil {
			return err
		}
		a.Subject = strconv.FormatInt(id, 10)
		return auditAndBump(ctx, tx, a)
	})
	return id, err
}

// CreateProjectWithKey creates a project and its first ingest key in one
// transaction: if the key cannot be inserted, the project is not created
// either, so a retry does not collide with a keyless leftover. k.ProjectID
// and both audit subjects are filled from the id SQLite assigns.
func (d *DB) CreateProjectWithKey(ctx context.Context, p store.RegistryProject, k store.RegistryKey, projectAudit, keyAudit store.AuditEntry) (int64, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = insertProject(ctx, tx, p); err != nil {
			return err
		}
		projectAudit.Subject = strconv.FormatInt(id, 10)
		if err := auditAndBump(ctx, tx, projectAudit); err != nil {
			return err
		}
		k.ProjectID = id
		if err := insertKey(ctx, tx, k); err != nil {
			return err
		}
		keyAudit.Subject = keySubject(id, k.Label)
		return auditAndBump(ctx, tx, keyAudit)
	})
	return id, err
}

// keySubject is the audit_log subject for a key: "<project id>/<label>".
func keySubject(projectID int64, label string) string {
	return strconv.FormatInt(projectID, 10) + "/" + label
}

func insertProject(ctx context.Context, tx *sql.Tx, p store.RegistryProject) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO projects
		(name, identity, allowed_origins, attributes) VALUES (?,?,?,?)`,
		p.Name, p.Identity, p.AllowedOrigins, p.Attributes)
	if err != nil {
		return 0, fmt.Errorf("create project %q: %w", p.Name, err)
	}
	return res.LastInsertId()
}

func (d *DB) UpdateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE projects SET name=?, identity=?,
			allowed_origins=?, attributes=? WHERE id=?`,
			p.Name, p.Identity, p.AllowedOrigins, p.Attributes, p.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("update project: unknown id %d: %w", p.ID, store.ErrNotFound)
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) SetProjectArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE projects SET archived_at=datetime('now') WHERE id=? AND archived_at IS NULL`
		if !archived {
			q = `UPDATE projects SET archived_at=NULL WHERE id=?`
		}
		res, err := tx.ExecContext(ctx, q, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 && archived {
			// restore of a non-archived project is a no-op, archive of an
			// unknown id is an error; check existence to distinguish.
			var c int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM projects WHERE id=?`, id).Scan(&c); err != nil {
				return err
			}
			if c == 0 {
				return fmt.Errorf("archive: unknown id %d: %w", id, store.ErrNotFound)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) InsertIngestKey(ctx context.Context, k store.RegistryKey, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if err := insertKey(ctx, tx, k); err != nil {
			return err
		}
		return auditAndBump(ctx, tx, a)
	})
}

// insertKey relies on UNIQUE (project_id, label): the constraint error is
// mapped to store.ErrConflict rather than pre-checked with a count.
func insertKey(ctx context.Context, tx *sql.Tx, k store.RegistryKey) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO ingest_keys (key, project_id, label) VALUES (?,?,?)`,
		k.Key, k.ProjectID, k.Label)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed: ingest_keys.project_id, ingest_keys.label") {
		return fmt.Errorf("key label %q for project %d: %w", k.Label, k.ProjectID, store.ErrConflict)
	}
	return fmt.Errorf("issue key for project %d: %w", k.ProjectID, err)
}

func (d *DB) SetIngestKeyDisabled(ctx context.Context, projectID int64, label string, disabled bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE ingest_keys SET disabled_at=datetime('now') WHERE project_id=? AND label=?`
		if !disabled {
			q = `UPDATE ingest_keys SET disabled_at=NULL WHERE project_id=? AND label=?`
		}
		res, err := tx.ExecContext(ctx, q, projectID, label)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("key %s: %w", keySubject(projectID, label), store.ErrNotFound)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// projectTables is every table carrying a per-project `project_id` column.
// Kept in one place so a future migration adding a table has one list to
// extend; TestProjectTablesMatchesSchema (registry_test.go) cross-checks
// this list against the live schema (sqlite_master + pragma_table_info) in
// both directions, so a forgotten addition or a stale entry fails loudly
// instead of silently orphaning rows on DeleteProjectData.
var projectTables = []string{
	"views", "events",
	"agg_views_daily", "agg_views_paths", "agg_views_hosts", "agg_views_referrers",
	"agg_views_utm", "agg_views_countries", "agg_views_os", "agg_views_browsers",
	"agg_views_app_versions", "agg_views_devices", "agg_views_displays",
	"agg_product_daily", "agg_product_totals", "agg_product_attrs",
	"actors", "agg_retention", "identities", "agg_identity_daily",
	"ingest_keys",
}

// DeleteProjectData hard-deletes the project and every row keyed by its
// id, in one transaction (spec §7.3). The audit row is written in the
// same transaction and survives — audit_log has no project column.
// Page reclamation is the caller's job (IncrementalVacuum), because a
// vacuum inside the tx would deadlock the single connection. The id is
// never reissued (AUTOINCREMENT), so a stale reference to it stays dead.
func (d *DB) DeleteProjectData(ctx context.Context, id int64, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("delete: unknown id %d: %w", id, store.ErrNotFound)
		}
		for _, table := range projectTables {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM `+table+` WHERE project_id=?`, id); err != nil {
				return fmt.Errorf("delete %s: %w", table, err)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}
```

The `github.com/google/uuid` import leaves this file; it stays in `go.mod` for event ids (`internal/server/handlers.go`).

`internal/store/sqlite/write.go`:
- `WriteViews`: column list `(id, project_id, ts, …)`, bind `v.ID, v.ProjectID, …`.
- `WriteProductEvents`: `INSERT OR IGNORE INTO events (id, project_id, event_name, …)`, bind `e.ID, e.ProjectID, e.EventName, …`.
- `UpsertIdentities`: `INSERT INTO identities (project_id, kind, id, name, updated_at) … ON CONFLICT(project_id, kind, id)`, bind `i.ProjectID`.
- Replace `ProjectAliases` with:

```go
func (d *DB) ProjectIDs(ctx context.Context) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
```

`internal/store/sqlite/flatview.go`: `flatViewBaseColumns = []string{"id", "project_id", "event_name", "actor_id", "ts", "attributes"}` and the statement becomes `CREATE VIEW v_events_flat AS SELECT %s FROM events`.

`internal/store/sqlite/aggregate_views.go`:
- `ViewDaysBefore(ctx, projectID int64, before)` → `d.daysBefore(ctx, "views", projectID, before)`; `ProductDaysBefore` → `d.daysBefore(ctx, "events", projectID, before)`; `daysBefore(ctx, table string, projectID int64, before)` with `WHERE project_id=? AND ts < ?`.
- `viewSessionsCTE`: `FROM views WHERE project_id = :p AND day = :day`.
- `AggregateViewDay(ctx, projectID int64, day)`: `SELECT COUNT(*) FROM views WHERE project_id=? AND day=?`; `sql.Named("p", projectID)`; `INSERT OR REPLACE INTO agg_views_daily (project_id, day, kind, …)`; `DELETE FROM views WHERE project_id=? AND day=?`.
- `aggregateSQL`: `INSERT OR REPLACE INTO %s (project_id, day, %s, visitors, views)` and `WHERE project_id = :p AND day = :day %s`.

`internal/store/sqlite/aggregate_product.go`: every `product_events` → `events`, every `project=?`/`project=:p` → `project_id=?`/`project_id=:p`, `(project, day, …)` column lists → `(project_id, day, …)`, `project string` parameters → `projectID int64`, `sql.Named("p", projectID)`. The `agg_product_totals` insert's `SELECT project, ?, …` becomes `SELECT project_id, ?, …` and its `GROUP BY project` → `GROUP BY project_id`. Same for `agg_product_daily`'s `SELECT project, ?, event_name` → `SELECT project_id, ?, event_name`. Update the `systemDims` comment's "product_events column" → "events column".

`internal/store/sqlite/retention.go`: `actorSources = []string{"views", "events"}`; `INSERT INTO actors (project_id, …)`; `WHERE project_id=?`; `ON CONFLICT(project_id, actor_id)`; `INSERT OR REPLACE INTO agg_retention (project_id, …)`; `FROM events WHERE project_id=?`; `SELECT a.project_id, …`; `WHERE a.project_id=?`; `GROUP BY a.project_id, …`; `DELETE FROM actors WHERE project_id=?`; `DELETE FROM agg_retention WHERE project_id=?`. Parameters `projectID int64`.

`internal/store/sqlite/identities.go`: `INSERT OR REPLACE INTO agg_identity_daily (project_id, day, …)`; `FROM views WHERE project_id=?`; `FROM events WHERE project_id=?`; `UPDATE identities SET last_seen_day=? WHERE project_id=? …`; `SELECT id FROM agg_identity_daily WHERE project_id=? …`; `DELETE FROM identities WHERE project_id=?`; `DELETE FROM agg_identity_daily WHERE project_id=?`. Parameters `projectID int64`.

`internal/store/sqlite/prune.go`: `PruneAggregates(ctx, projectID int64, …)` with `DELETE FROM %s WHERE project_id=? AND day < ?`.

- [ ] **Step 4: Adapt the existing sqlite tests**

The rules, applied to every `*_test.go` in `internal/store/sqlite` (except the two migration tests):

1. Struct literals: `Project: "<x>"` → `ProjectID: <n>` on `store.View`, `store.ProductEvent`, `store.Identity`, `store.RegistryKey`; `store.RegistryProject{Alias: x, Name: x, …}` → `store.RegistryProject{Name: x, …}` and capture the returned id (`id, err := db.CreateProject(...)`). Drop every `Retention:` field.
2. Method calls: the `project string` argument becomes an `int64`. A test that never created a registry row keeps using a literal — `1` in place of `"p"`/`"app"`, `2` for a second project — because no data table has a foreign key to `projects`. A test whose assertions go through `v_product_attrs` with declared keys, or through `LoadRegistry`, creates the project first and uses the returned id.
3. Raw SQL in tests: `'p'`/`'app'`/`'blog'` in a project position → `1` (unquoted); `project=` / `(project, ` / `, project,` → `project_id`; `product_events` → `events`.
4. `RenameProject` tests (in `registry_test.go`, `errors_test.go`, `typed_errors_test.go`) are deleted, not adapted.
5. `TestProjectTablesMatchesSchema`: `hasColumn(t, db, name, "project_id")`.
6. `flatview_test.go`: expected base columns become `"id", "project_id", "event_name", "actor_id", "ts", "attributes"`; the injection test's `DROP TABLE product_events` → `DROP TABLE events` and the survival check `SELECT COUNT(*) FROM events`; the scanned `project` is now an `int64` compared to `1`.
7. `zz_seed_test.go`: create `app` then `blog` with `Name:` and use the returned ids; `ProjectID: appID` on the rows; aggregate calls take `appID`.
8. `bench_test.go`: `ProjectID: 1`.

A first pass that gets most of it (review every hunk afterwards — it cannot know which `'p'` is a project):

```bash
cd internal/store/sqlite
sed -i -E \
  -e 's/\bProject:\s*"[a-z0-9_]+"/ProjectID: 1/g' \
  -e 's/\bproduct_events\b/events/g' \
  -e 's/\(project, /(project_id, /g' \
  -e 's/\bproject=\?/project_id=?/g' \
  -e "s/'p','/1,'/g" -e "s/\('p',/(1,/g" -e "s/'p'\)/1)/g" -e "s/, 'p'/, 1/g" \
  $(ls *_test.go | grep -v migration01)
```

Then fix by hand what the sed cannot: multi-project tests (`'q'` → `2`), calls like `db.AggregateViewDay(ctx, "p", day)` → `1`, `CreateProject` return values, `Alias:` literals, and the deletions in rule 4. `go vet .` lists the leftovers.

- [ ] **Step 5: Run the package tests**

Run: `cd internal/store/sqlite && go vet . && go test -race .` and `cd ../ && go test ./...`
Expected: PASS, including `TestProjectTablesMatchesSchema`, `TestPruneAggregatesCoversAllAggTables`, and the views-family parity tests in `views_test.go`.

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -m "feat(store): key every row and registry operation by project id"
```

---

### Task 3: The `manage` package

Implements spec §6.2. Snapshot keyed by id, `ProjectSpec` without alias or retention, id-taking operations, merge semantics for update, and the deletion of rename and import/export. `manage` builds and passes after this; `server`, `jobs`, `api`, `app`, `cmd` still do not.

**Files:**
- Modify: `internal/manage/registry.go`, `internal/manage/ops.go`, `internal/manage/store.go`, `internal/manage/errors.go`
- Delete: `internal/manage/importexport.go`, `internal/manage/importexport_test.go`
- Modify tests: `registry_test.go`, `ops_test.go`, `coverage_test.go`, `after_write_test.go`, `typed_errors_test.go`

**Interfaces:**
- Consumes: the `store` package from Task 2.
- Produces:

```go
type Project struct {
	ID             int64
	Name, Identity string
	AllowedOrigins []string
	Attributes     []string
	Archived       bool
}

func New(st Store, logger *slog.Logger) *Registry          // no retention defaults
func (s *Snapshot) Project(id int64) *Project
func (s *Snapshot) Projects() []*Project                    // ascending id
func (s *Snapshot) ProjectByKey(key string) (*Project, string, bool)
func (s *Snapshot) OriginAllowed(id int64, origin string) bool
func (s *Snapshot) AnyOriginAllowed(origin string) bool
func (s *Snapshot) KeylessProjects() []*Project
func (s *Snapshot) AttributesFor(id int64) []string
func (s *Snapshot) DeclaredAttributeKeys() []string

type ProjectSpec struct {
	ID             int64    // update only: which row
	Name, Identity string
	AllowedOrigins []string // update: nil keeps, non-nil replaces ([] clears)
	Attributes     []string // update: nil keeps, non-nil replaces
}

func (o *Ops) CreateProject(ctx, actor string, spec ProjectSpec) (*Project, error)             // Name required
func (o *Ops) CreateProjectWithKey(ctx, actor string, spec ProjectSpec, label string) (*Project, string, error)
func (o *Ops) UpdateProject(ctx, actor string, spec ProjectSpec) (*Project, error)             // merge: "" and nil keep
func (o *Ops) ArchiveProject(ctx, actor string, id int64) error
func (o *Ops) RestoreProject(ctx, actor string, id int64) error
func (o *Ops) IssueIngestKey(ctx, actor string, projectID int64, label string) (string, error)
func (o *Ops) DisableIngestKey(ctx, actor string, projectID int64, label string) error
func (o *Ops) EnableIngestKey(ctx, actor string, projectID int64, label string) error
func (o *Ops) DeleteProject(ctx, actor string, id int64) error
```

Gone: `RenameProject`, `Import`, `Export`, `ImportResult`, `Snapshot.RetentionFor`, `validateNew`, `validAlias`, `Project.Alias`, `Project.Retention`.

- [ ] **Step 1: Write the failing tests**

Replace `TestCreateRejectsBadAlias`, `TestUpdateDoesNotCharsetCheck`, `TestRenameProjectAllowsLegacyAlias` and `TestRenameProjectRejectsInvalidNewAlias` in `internal/manage/ops_test.go` with:

```go
func TestCreateRequiresName(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	_, err := ops.CreateProject(ctx, "test", ProjectSpec{Identity: "anonymous"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create without a name: err = %v, want ErrInvalid", err)
	}
	p, err := ops.CreateProject(ctx, "test", ProjectSpec{Name: "My App"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 1 || p.Name != "My App" || p.Identity != "anonymous" {
		t.Fatalf("created = %+v, want id 1, the name, and the anonymous default", p)
	}
	// Names are not unique: a second project with the same name gets id 2.
	q, err := ops.CreateProject(ctx, "test", ProjectSpec{Name: "My App"})
	if err != nil || q.ID != 2 {
		t.Fatalf("second create = %+v, %v; want id 2", q, err)
	}
}

func TestUpdateMergesAndClearsOriginsOnlyWhenAsked(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	p, err := ops.CreateProject(ctx, "test", ProjectSpec{Name: "Blog", Identity: "identified",
		AllowedOrigins: []string{"https://blog.example.com"}, Attributes: []string{"plan"}})
	if err != nil {
		t.Fatal(err)
	}
	// A name-only update keeps identity, origins and attributes.
	u, err := ops.UpdateProject(ctx, "test", ProjectSpec{ID: p.ID, Name: "Renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "Renamed" || u.Identity != "identified" || len(u.AllowedOrigins) != 1 || len(u.Attributes) != 1 {
		t.Fatalf("name-only update changed more than the name: %+v", u)
	}
	// A non-nil empty origins list clears; nil keeps.
	u, err = ops.UpdateProject(ctx, "test", ProjectSpec{ID: p.ID, AllowedOrigins: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(u.AllowedOrigins) != 0 {
		t.Fatalf("explicit empty origins did not clear: %v", u.AllowedOrigins)
	}
	if u.Name != "Renamed" {
		t.Fatalf("clearing origins changed the name: %q", u.Name)
	}
	if _, err := ops.UpdateProject(ctx, "test", ProjectSpec{ID: 99, Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestUnknownIdsAreNotFound(t *testing.T) {
	st := testStore(t)
	reg := New(st, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	for name, err := range map[string]error{
		"archive": ops.ArchiveProject(ctx, "test", 42),
		"delete":  ops.DeleteProject(ctx, "test", 42),
		"key":     func() error { _, e := ops.IssueIngestKey(ctx, "test", 42, "web"); return e }(),
		"disable": ops.DisableIngestKey(ctx, "test", 42, "web"),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s on id 42: err = %v, want ErrNotFound", name, err)
		}
	}
}
```

Add `"errors"` to that file's imports if it is not there.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd internal/manage && go test -run 'TestCreateRequiresName|TestUpdateMerges|TestUnknownIds' .`
Expected: compile error (`New` takes three arguments, `ProjectSpec` has no field `ID`).

- [ ] **Step 3: Rewrite `registry.go`**

Replace the `Project` type, `Snapshot` fields, `Registry.defaults`, `New`, the body of `Reload`, `withhold`, and every alias-keyed accessor:

```go
type Project struct {
	ID             int64
	Name, Identity string
	AllowedOrigins []string
	Attributes     []string
	Archived       bool
}

type keyOwner struct {
	key     string
	project *Project
	label   string
}

// Snapshot is an immutable view of the registry. Readers pay one atomic
// load; every mutation builds a fresh one.
type Snapshot struct {
	byID    map[int64]*Project
	ordered []*Project // ascending id
	keys    []keyOwner // active keys of non-archived projects only
	origins map[int64]originSet
}

type Registry struct {
	st     Store
	logger *slog.Logger

	publish   sync.Mutex
	snap      atomic.Pointer[Snapshot]
	version   atomic.Int64
	lastCheck atomic.Int64
}

func New(st Store, logger *slog.Logger) *Registry {
	r := &Registry{st: st, logger: logger}
	r.snap.Store(&Snapshot{byID: map[int64]*Project{}, origins: map[int64]originSet{}})
	return r
}

func (r *Registry) Reload(ctx context.Context) error {
	ps, ks, err := r.st.LoadRegistry(ctx)
	if err != nil {
		return fmt.Errorf("manage: load registry: %w", err)
	}
	v, err := r.st.ConfigVersion(ctx)
	if err != nil {
		return fmt.Errorf("manage: config version: %w", err)
	}
	s := &Snapshot{
		byID:    make(map[int64]*Project, len(ps)),
		origins: make(map[int64]originSet, len(ps)),
	}
	for _, rp := range ps {
		p := &Project{ID: rp.ID, Name: rp.Name, Identity: rp.Identity, Archived: rp.Archived}
		if rp.AllowedOrigins != "" {
			if err := json.Unmarshal([]byte(rp.AllowedOrigins), &p.AllowedOrigins); err != nil {
				return fmt.Errorf("manage: project %d allowed_origins: %w", rp.ID, err)
			}
		}
		if rp.Attributes != "" {
			if err := json.Unmarshal([]byte(rp.Attributes), &p.Attributes); err != nil {
				return fmt.Errorf("manage: project %d attributes: %w", rp.ID, err)
			}
		}
		s.byID[p.ID] = p
		s.ordered = append(s.ordered, p)
		set := originSet{exact: map[string]bool{}}
		for _, o := range p.AllowedOrigins {
			if o = trimSlash(o); strings.ContainsRune(o, '*') {
				set.globs = append(set.globs, o)
				continue
			}
			set.exact[o] = true
		}
		s.origins[p.ID] = set
	}
	for _, k := range ks {
		p := s.byID[k.ProjectID]
		if p == nil || p.Archived || k.Disabled {
			continue // archived projects reject events (001_init.sql comment)
		}
		s.keys = append(s.keys, keyOwner{key: k.Key, project: p, label: k.Label})
	}
	r.publish.Lock()
	r.snap.Store(s)
	r.version.Store(v)
	r.publish.Unlock()
	r.lastCheck.Store(time.Now().UnixNano())
	return nil
}

func (r *Registry) withhold(ids ...int64) {
	drop := make(map[int64]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	r.publish.Lock()
	defer r.publish.Unlock()
	cur := r.snap.Load()
	s := *cur
	s.keys = make([]keyOwner, 0, len(cur.keys))
	for _, k := range cur.keys {
		if !drop[k.project.ID] {
			s.keys = append(s.keys, k)
		}
	}
	r.snap.Store(&s)
	r.version.Store(unknownVersion)
	r.lastCheck.Store(0)
}

func (s *Snapshot) Project(id int64) *Project { return s.byID[id] }

func (s *Snapshot) OriginAllowed(id int64, origin string) bool {
	set, ok := s.origins[id]
	return ok && set.match(trimSlash(origin))
}

// KeylessProjects lists active projects with no active key: a legitimate
// retired state, so callers warn rather than fail.
func (s *Snapshot) KeylessProjects() []*Project {
	withKey := map[int64]bool{}
	for _, k := range s.keys {
		withKey[k.project.ID] = true
	}
	var out []*Project
	for _, p := range s.ordered {
		if !p.Archived && !withKey[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// AttributesFor returns the project's declared attribute keys. Unknown ids
// return nil, matching the archived-project fallback.
func (s *Snapshot) AttributesFor(id int64) []string {
	p := s.byID[id]
	if p == nil {
		return nil
	}
	return p.Attributes
}
```

Delete `RetentionFor` and the `config` import (nothing else in the file uses it). `Snapshot()` (the polling getter), `ProjectByKey`, `AnyOriginAllowed`, `DeclaredAttributeKeys`, `originSet`, `matchOrigin`, `trimSlash` are unchanged. Update the package comment: "MCP tools and CLI subcommands are thin frontends over this package" (the importer is gone).

- [ ] **Step 4: Rewrite `ops.go`**

Replace `written`, `ProjectSpec`, `validate`, `validateNew`, `validAlias`, `row`, `create`, `CreateProject`, `CreateProjectWithKey`, `UpdateProject`, `ArchiveProject`, `RestoreProject`, `IssueIngestKey`, `DisableIngestKey`, `EnableIngestKey`, `RenameProject` and `DeleteProject` with:

```go
// afterWrite: signature becomes (ctx, rebuildView bool, ids ...int64);
// the log field "projects" carries ids. Body otherwise unchanged.

// written is the project a create or update just committed: the snapshot's
// copy when the reload succeeded, otherwise one built from the merged spec,
// keeping the archived flag the spec does not carry.
func (o *Ops) written(ctx context.Context, spec ProjectSpec, reloaded bool) *Project {
	if reloaded {
		if cur := o.Reg.Snapshot(ctx).Project(spec.ID); cur != nil {
			return cur
		}
	}
	cur := o.Reg.snap.Load().Project(spec.ID)
	p := &Project{ID: spec.ID, Name: spec.Name, Identity: spec.Identity,
		AllowedOrigins: spec.AllowedOrigins, Attributes: spec.Attributes}
	if cur != nil {
		p.Archived = cur.Archived
	}
	return p
}

// ProjectSpec is the caller's view of a project. On create, Name is
// required and ID is ignored. On update, ID selects the row and every
// other field merges: an empty Name or Identity keeps the current value,
// a nil slice keeps the current list, a non-nil slice replaces it — so an
// empty non-nil AllowedOrigins clears the origins. JSON `[]` decodes to a
// non-nil empty slice and an omitted field to nil, which is what lets the
// API express both without a second field.
type ProjectSpec struct {
	ID             int64
	Name, Identity string
	AllowedOrigins []string
	Attributes     []string
}

// validate checks a complete spec: the one a caller built for create, or
// the merged one UpdateProject built over the current row.
func (sp *ProjectSpec) validate() error {
	if strings.TrimSpace(sp.Name) == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalid)
	}
	if sp.Identity == "" {
		sp.Identity = config.IdentityAnonymous
	}
	switch sp.Identity {
	case config.IdentityAnonymous, config.IdentityIdentified:
	default:
		return fmt.Errorf("%w: identity must be %q or %q, got %q", ErrInvalid,
			config.IdentityAnonymous, config.IdentityIdentified, sp.Identity)
	}
	for _, o := range sp.AllowedOrigins {
		if o == "" {
			return fmt.Errorf("%w: allowed_origins must not contain an empty origin", ErrInvalid)
		}
	}
	return nil
}

func (sp *ProjectSpec) row() (store.RegistryProject, error) {
	origins, err := json.Marshal(sp.AllowedOrigins)
	if sp.AllowedOrigins == nil {
		origins, err = []byte("[]"), nil
	}
	if err != nil {
		return store.RegistryProject{}, err
	}
	attrs, err := json.Marshal(sp.Attributes)
	if sp.Attributes == nil {
		attrs, err = []byte("[]"), nil
	}
	if err != nil {
		return store.RegistryProject{}, err
	}
	return store.RegistryProject{ID: sp.ID, Name: sp.Name, Identity: sp.Identity,
		AllowedOrigins: string(origins), Attributes: string(attrs)}, nil
}

func (o *Ops) CreateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error) {
	return o.create(ctx, actor, spec, func(row store.RegistryProject, audit store.AuditEntry) (int64, error) {
		return o.St.CreateProject(ctx, row, audit)
	})
}

// CreateProjectWithKey is CreateProject plus a first ingest key under
// label, committed together: either both exist afterwards or neither does.
func (o *Ops) CreateProjectWithKey(ctx context.Context, actor string, spec ProjectSpec, label string) (*Project, string, error) {
	key, err := MintIngestKey()
	if err != nil {
		return nil, "", err
	}
	p, err := o.create(ctx, actor, spec, func(row store.RegistryProject, audit store.AuditEntry) (int64, error) {
		return o.St.CreateProjectWithKey(ctx, row,
			store.RegistryKey{Key: key, Label: label}, audit,
			store.AuditEntry{Actor: actor, Action: "key.issue"})
	})
	if err != nil {
		return nil, "", err
	}
	return p, key, nil
}

// create validates spec, hands its row and audit entry to write, and
// reloads the registry once the write has committed. The store fills the
// audit subject with the id it assigns.
func (o *Ops) create(ctx context.Context, actor string, spec ProjectSpec, write func(store.RegistryProject, store.AuditEntry) (int64, error)) (*Project, error) {
	spec.ID = 0
	if err := spec.validate(); err != nil {
		return nil, err
	}
	row, err := spec.row()
	if err != nil {
		return nil, err
	}
	id, err := write(row, store.AuditEntry{Actor: actor, Action: "project.create"})
	if err != nil {
		return nil, err
	}
	spec.ID = id
	return o.written(ctx, spec, o.afterWrite(ctx, true, id)), nil
}

// UpdateProject merges spec over the current row (see ProjectSpec) and
// writes the result whole.
func (o *Ops) UpdateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error) {
	cur := o.Reg.Snapshot(ctx).Project(spec.ID)
	if cur == nil {
		return nil, fmt.Errorf("update project: unknown id %d: %w", spec.ID, ErrNotFound)
	}
	if spec.Name == "" {
		spec.Name = cur.Name
	}
	if spec.Identity == "" {
		spec.Identity = cur.Identity
	}
	if spec.AllowedOrigins == nil {
		spec.AllowedOrigins = cur.AllowedOrigins
	}
	if spec.Attributes == nil {
		spec.Attributes = cur.Attributes
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	row, err := spec.row()
	if err != nil {
		return nil, err
	}
	if err := o.St.UpdateProject(ctx, row, store.AuditEntry{
		Actor: actor, Action: "project.update", Subject: strconv.FormatInt(spec.ID, 10)}); err != nil {
		return nil, err
	}
	return o.written(ctx, spec, o.afterWrite(ctx, true, spec.ID)), nil
}

func idSubject(id int64) string { return strconv.FormatInt(id, 10) }

func keySubject(id int64, label string) string { return strconv.FormatInt(id, 10) + "/" + label }

func (o *Ops) ArchiveProject(ctx context.Context, actor string, id int64) error {
	if err := o.St.SetProjectArchived(ctx, id, true, store.AuditEntry{
		Actor: actor, Action: "project.archive", Subject: idSubject(id)}); err != nil {
		return err
	}
	o.afterWrite(ctx, false, id)
	return nil
}

func (o *Ops) RestoreProject(ctx context.Context, actor string, id int64) error {
	if err := o.St.SetProjectArchived(ctx, id, false, store.AuditEntry{
		Actor: actor, Action: "project.restore", Subject: idSubject(id)}); err != nil {
		return err
	}
	o.afterWrite(ctx, false, id)
	return nil
}

func (o *Ops) IssueIngestKey(ctx context.Context, actor string, projectID int64, label string) (string, error) {
	if o.Reg.Snapshot(ctx).Project(projectID) == nil {
		return "", fmt.Errorf("unknown project %d: %w", projectID, ErrNotFound)
	}
	key, err := MintIngestKey()
	if err != nil {
		return "", err
	}
	if err := o.St.InsertIngestKey(ctx, store.RegistryKey{
		Key: key, ProjectID: projectID, Label: label}, store.AuditEntry{
		Actor: actor, Action: "key.issue", Subject: keySubject(projectID, label)}); err != nil {
		return "", err
	}
	o.afterWrite(ctx, false, projectID)
	return key, nil
}

func (o *Ops) DisableIngestKey(ctx context.Context, actor string, projectID int64, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, projectID, label, true, store.AuditEntry{
		Actor: actor, Action: "key.disable", Subject: keySubject(projectID, label)}); err != nil {
		return err
	}
	o.afterWrite(ctx, false, projectID)
	return nil
}

func (o *Ops) EnableIngestKey(ctx context.Context, actor string, projectID int64, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, projectID, label, false, store.AuditEntry{
		Actor: actor, Action: "key.enable", Subject: keySubject(projectID, label)}); err != nil {
		return err
	}
	o.afterWrite(ctx, false, projectID)
	return nil
}

// DeleteProject is exposed by the CLI only — never as an MCP tool
// (spec §7.3: irreversible operations require a shell). Reclaims pages
// afterwards; the tx cannot (single connection).
func (o *Ops) DeleteProject(ctx context.Context, actor string, id int64) error {
	if err := o.St.DeleteProjectData(ctx, id, store.AuditEntry{
		Actor: actor, Action: "project.delete", Subject: idSubject(id)}); err != nil {
		return err
	}
	if err := o.St.IncrementalVacuum(ctx); err != nil {
		o.Reg.logger.Warn("vacuum after project delete failed", "project_id", id, "error", err)
	}
	o.afterWrite(ctx, false, id)
	return nil
}
```

Imports become `context, crypto/rand, encoding/hex, encoding/json, fmt, strconv, strings` plus `config` and `store`. `rebuildFlatView`, `MintIngestKey`, `MintAPIToken`, `mint`, `Snippet`, `SnippetPlaceholderBase` are unchanged. Delete `RenameProject` and its comment.

`internal/manage/store.go`: the interface mirrors Task 2 — `CreateProject`/`CreateProjectWithKey` return `(int64, error)`, `SetProjectArchived(ctx, id int64, …)`, `SetIngestKeyDisabled(ctx, projectID int64, label string, …)`, `DeleteProjectData(ctx, id int64, …)`, and `RenameProject` is removed.

`internal/manage/errors.go`: `ErrConflict`'s comment becomes "a key label is already taken for that project".

Delete `internal/manage/importexport.go` and `internal/manage/importexport_test.go`.

- [ ] **Step 5: Adapt the remaining manage tests**

- `registry_test.go`: `seedProject(t, st, name string) int64` creates with `Name: name` (no `Retention`), inserts the key with `ProjectID: id`, returns `id`. `New(st, defaults, discard())` → `New(st, discard())` everywhere; delete `var defaults` and the `config` import if unused. Every `s.Project("blog")` → `s.Project(id)`, `p.Alias != "blog"` → `p.ID != id`, `OriginAllowed("blog", …)` → `OriginAllowed(id, …)`. Delete the `RetentionFor` assertions and any test whose only subject is `RetentionFor`. `KeylessProjects` now returns `[]*Project`: compare `[0].ID`.
- `ops_test.go`: `TestCreateProjectValidatesAndReloads` uses `ProjectSpec{Name: "My blog", …}` and asserts `p.ID == 1`; its invalid cases become `{Name: ""}`, `{Name: "x", Identity: "sometimes"}`, `{Name: "x", AllowedOrigins: []string{""}}`. `TestIssueKeyMintsAndResolves` and `TestCreateProjectWithKey` use the returned `p.ID` and compare `p.ID` where they compared `p.Alias`.
- `coverage_test.go`: drop the two `Retention:` fields (lines ~100 and ~194) and the four `Import` tests (`TestImportRejectsUnsupportedVersion`, `TestImportRejectsMalformedJSON`, `TestImportRejectsMalformedLegacyJSON`, `TestImportEmptyDocumentIsAnError`); ids everywhere else.
- `after_write_test.go`, `typed_errors_test.go`: the fake store's methods take ids; the rename case in `TestOpsRefusalsAreTyped` is deleted; `withhold` assertions key on ids.

- [ ] **Step 6: Run the package tests**

Run: `cd internal/manage && go vet . && go test -race .`
Expected: PASS, including the three new tests.

- [ ] **Step 7: Commit**

```bash
git add internal/manage
git commit -m "feat(manage): address projects by id and drop rename, import and export"
```

---

### Task 4: `config` loses the per-project and legacy types

Implements spec §6.3.

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `config.Retention` and `config.RetentionClass` unchanged; `RetentionOverride`, `RetentionClassOverride`, `LegacyAggregation`, `IngestKey`, `Project`, `(*Project).DeclaredAttributes`, `ParseProjects` no longer exist.

- [ ] **Step 1: Delete the tests for removed types**

In `internal/config/config_test.go` delete `TestParseProjectsIngestKeys`, `TestParseProjectsRejectsUnknownFields`, `TestParseProjectsRejectsNonArray`, `TestParseProjectsAcceptsLegacyProductAggregation`, the two `DeclaredAttributes` tests that construct `Project{…}` (around lines 236–260), `TestRetentionOverrideDecodesLegacyKeys` and the two tests after it that use `RetentionOverride` (around lines 291–315). Remove imports that become unused (`strings`, `encoding/json`) — `go vet` will tell you.

- [ ] **Step 2: Run to verify the package still compiles and nothing else referenced them**

Run: `cd internal/config && go vet . && go test .`
Expected: PASS (the types still exist; the deleted tests were their only in-package users).

- [ ] **Step 3: Delete the types**

In `internal/config/config.go` delete `RetentionClassOverride`, `RetentionOverride` and its `UnmarshalJSON`, `LegacyAggregation`, `IngestKey`, `Project`, `DeclaredAttributes`, and `ParseProjects`. Remove `encoding/json`, `io` and `sort` from the imports if nothing else uses them. Rewrite the package comment:

```go
// Package config loads infra settings from the process environment
// (12-factor; systemd/compose/make load the env *file*, the process reads
// real env vars). It reads no file: the project list lives in the
// registry (internal/manage).
// stdlib only: net/url for DSN scheme checks.
```

and the `MaxEventAge` comment:

```go
// MaxEventAge is derived from the views raw window rather than separately
// configurable: the two must agree or a clamped timestamp could land in
// an already-aggregated day. Retention is global, so ingest clamps against
// exactly this value.
```

- [ ] **Step 4: Verify**

Run: `cd internal/config && go vet . && go test -race ./...`
Expected: PASS. (`grep -rn "RetentionOverride\|ParseProjects\|LegacyAggregation" internal cmd` must list nothing outside `internal/api/ops_read.go`, which Task 7 fixes.)

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "refactor(config): remove per-project retention overrides and the legacy projects.json types"
```

---

### Task 5: Ingest server and pipeline

Implements spec §5 and §6.4. The handler writes `p.ID`, hashes with the id as a decimal string, checks origins by id, and clamps against the global raw window. `server` and `pipeline` build and pass after this.

**Files:**
- Modify: `internal/server/handlers.go`, `internal/server/server.go`, `internal/pipeline/pipeline.go`
- Modify tests: `internal/server/server_test.go`, `internal/server/ingest_test.go`, `internal/pipeline/pipeline_test.go`

**Interfaces:**
- Consumes: `manage.Project.ID`, `Snapshot.OriginAllowed(id, origin)`, `store.View.ProjectID`, `config.(*Config).MaxEventAge()`.
- Produces: `server.New(cfg, reg, q, g, salt, names, logger)` — signature unchanged (cfg already carries `Retention`, which is what §6.4 asks `app` to pass).

- [ ] **Step 1: Write the failing tests**

In `internal/server/server_test.go`, replace `newTestRegistry` so it creates projects by name and returns the ids, and add a hash-input test:

```go
// newTestRegistry seeds a temp-DB registry with the given projects, in
// order, so the first spec is project 1. keys maps a project's index in
// specs to {key, label}.
func newTestRegistry(t *testing.T, projects []manage.ProjectSpec, keys map[int][2]string) *manage.Registry {
	t.Helper()
	st, err := store.Open("sqlite://" + t.TempDir() + "/reg.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reg := manage.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	var ids []int64
	for _, spec := range projects {
		p, err := ops.CreateProject(ctx, "test", spec)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	for i, kl := range keys {
		if err := st.InsertIngestKey(ctx, store.RegistryKey{
			Key: kl[0], ProjectID: ids[i], Label: kl[1]},
			store.AuditEntry{Actor: "test", Action: "key.issue", Subject: kl[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestHashInputIsTheProjectId pins the salt seam: an anonymous actor is
// hashed with the id as a decimal string, never a name, so renaming a
// project cannot change its hashes.
func TestHashInputIsTheProjectId(t *testing.T) {
	q, h := testServer(t)
	rec := post(h, `{"events":[{"name":"$page_view","attributes":{"$path":"/"}}]}`,
		map[string]string{"Origin": testOrigin, "X-Analytics-Key": testKey})
	if rec.Code != 202 {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if len(q.views) != 1 {
		t.Fatalf("views = %d, want 1", len(q.views))
	}
	want := identity.VisitorHash("test-salt", "192.0.2.1", chromeUA, "1")
	if q.views[0].ProjectID != 1 || q.views[0].ActorID != want {
		t.Fatalf("view = project %d actor %q, want project 1 actor %q", q.views[0].ProjectID, q.views[0].ActorID, want)
	}
}
```

`httptest.NewRequest` sets `RemoteAddr` to `192.0.2.1:1234`; if `post` overrides the IP, use that value. Add `"github.com/dmtrkzntsv/twillingate/internal/identity"` to the imports. Update `newServerWithIdentity` to `newTestRegistry(t, []manage.ProjectSpec{{Name: "App", Identity: mode, AllowedOrigins: []string{testOrigin}}}, map[int][2]string{0: {testKey, "web"}})` and drop its `cfg` argument to the registry (cfg still goes to `New`).

- [ ] **Step 2: Run to verify it fails**

Run: `cd internal/server && go test -run TestHashInputIsTheProjectId .`
Expected: compile error (`q.views[0].ProjectID` undefined until handlers.go changes / `manage.New` arity).

- [ ] **Step 3: Change the handler and server**

`internal/server/handlers.go`:
- Add `"strconv"` to the imports.
- After `p, label, ok := snap.ProjectByKey(key)`: `hashKey := strconv.FormatInt(p.ID, 10)`.
- `s.originAllowed(w, r, p.Alias)` → `s.originAllowed(w, r, p.ID)`.
- Replace the `maxAge` line and its comment with:

```go
	// Retention is global, so the clamp is the configured raw window: a
	// clamped event can never target a day the daily pass already
	// aggregated and deleted.
	maxAge := s.cfg.MaxEventAge()
```

- `resolveIdentity(p, rv, salt, ip, ua)` → `resolveIdentity(p, rv, salt, ip, ua, hashKey)`; in its signature add `hashKey string` and replace every `p.Alias` in its body with `hashKey`. Its doc comment gains: "hashKey is the project id as a decimal string — the id never changes, so a project's hash input never changes."
- `store.ProductEvent{ID: id, Project: p.Alias, …}` → `ProjectID: p.ID`; `store.View{ID: id, Project: p.Alias, …}` → `ProjectID: p.ID`.
- `dedupeIdentities(names, p.Alias)` → `dedupeIdentities(names, p.ID)`; the function takes `projectID int64` and sets `i.ProjectID = projectID`.

`internal/server/server.go`: `originAllowed(w, r, projectID int64)` calls `OriginAllowed(projectID, origin)`.

`internal/pipeline/pipeline.go`: the flush label `"product_events"` → `"events"` (it is a log field naming the table).

- [ ] **Step 4: Adapt the remaining tests**

`server_test.go`: `ProjectSpec{Alias: "app", Name: "App", …}` → `{Name: "App", …}`; the two-project test around line 469 builds `[]manage.ProjectSpec{{Name: "Clamped", …}, {Name: "Normal", …}}` with `map[int][2]string{0: {…}, 1: {…}}`; `q.events[0].Project != "app"` → `q.events[0].ProjectID != 1`; `q.identities[0].Project != "app"` → `.ProjectID != 1`. Any test that asserted the clamp against a per-project override now sets `RETENTION_VIEWS_RAW_DAYS` through `configtest.Load(t, map[string]string{...})` instead. `ingest_test.go` has no project references beyond the two alias-named test functions, which are about `$platform` and stay. `pipeline_test.go`: `Project:` → `ProjectID: 1` if present (grep says none).

- [ ] **Step 5: Run the package tests**

Run: `cd internal/server && go vet . && go test -race . && cd ../pipeline && go vet . && go test -race .`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server internal/pipeline
git commit -m "feat(server): write project ids, hash with the id and clamp to the global raw window"
```

---

### Task 6: The daily pass

Implements spec §6.5. Per-project loop stays; windows come from `cfg.Retention`; ids everywhere.

**Files:**
- Modify: `internal/jobs/jobs.go`
- Modify tests: `internal/jobs/jobs_test.go`, `internal/jobs/errors_test.go`

**Interfaces:**
- Consumes: `store.ProjectIDs`, the id-taking store methods, `Snapshot.Project(id)`, `Snapshot.AttributesFor(id)`.
- Produces: `jobs.Store` (below); `jobs.New` unchanged.

- [ ] **Step 1: Write the failing test**

Add to `internal/jobs/jobs_test.go`:

```go
// TestPruneUsesGlobalWindowsForEveryProject: retention is global, so two
// projects with the same aged-out data lose it on the same pass.
func TestPruneUsesGlobalWindowsForEveryProject(t *testing.T) {
	st, _, r := setup(t, jobsVars, []manage.ProjectSpec{
		{Name: "App", AllowedOrigins: []string{"https://a.com"}},
		{Name: "Shop", AllowedOrigins: []string{"https://s.com"}},
	})
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		if _, err := rawExec(t, `INSERT INTO agg_views_daily (project_id, day, kind, visitors, views, sessions, bounces, duration_sec)
			VALUES (?, '2024-01-01', 'web', 1, 1, 1, 0, 0), (?, '2026-08-20', 'web', 1, 1, 1, 0, 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		days := queryDays(t, `SELECT day FROM agg_views_daily WHERE project_id=? ORDER BY day`, id)
		if len(days) != 1 || days[0] != "2026-08-20" {
			t.Errorf("project %d kept %v, want only 2026-08-20 (365-day aggregate window)", id, days)
		}
	}
	_ = st
}
```

Add these two helpers next to it (`database/sql`, `os` and the `modernc.org/sqlite` blank import are already in the file):

```go
// rawExec runs one statement against the store's file through a second
// connection, for seeding rows the Store interface has no writer for.
func rawExec(t *testing.T, q string, args ...any) (sql.Result, error) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+os.Getenv("JOBS_TEST_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	return db.Exec(q, args...)
}

func queryDays(t *testing.T, q string, args ...any) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+os.Getenv("JOBS_TEST_DB")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd internal/jobs && go test -run TestPruneUsesGlobalWindows .`
Expected: compile error (`ProjectSpec` has no `Name`-only form until the file's other specs are fixed / `Store` mismatch).

- [ ] **Step 3: Change `jobs.go`**

```go
type Store interface {
	ProjectIDs(ctx context.Context) ([]int64, error)
	ViewDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	AggregateViewDay(ctx context.Context, projectID int64, day civil.Date) error
	AggregateProductDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error
	UpsertActors(ctx context.Context, projectID int64, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, projectID int64, day civil.Date) error
	PruneActors(ctx context.Context, projectID int64, before civil.Date) error
	AggregateIdentityDay(ctx context.Context, projectID int64, day civil.Date) error
	PruneIdentities(ctx context.Context, projectID int64, before civil.Date) error
	PruneAggregates(ctx context.Context, projectID int64, viewsBefore, productBefore civil.Date) error
	RebuildFlatView(ctx context.Context, keys []string) error
	IncrementalVacuum(ctx context.Context) error
}
```

In `RunDailyPass`: `ids, err := r.store.ProjectIDs(ctx)`; replace the comment block and `ret := snap.RetentionFor(id)` with

```go
	// Retention is global: the same windows apply to every project, and
	// store.ProjectIDs (all rows, including archived) is the complete list.
	ret := r.cfg.Retention
	snap := r.reg.Snapshot(ctx)
	for _, id := range ids {
```

Everything else in the loop already uses `id` and now compiles with `int64` (`snap.Project(id)`, `snap.AttributesFor(id)`, the store calls). `allRawDays(ctx, projectID int64, before)` and its `func(context.Context, int64, civil.Date)` slice type.

- [ ] **Step 4: Adapt the tests**

`jobsProjectSpecs = []manage.ProjectSpec{{Name: "App", AllowedOrigins: …, Attributes: []string{"plan"}}}`; `newRegistry` → `manage.New(st, slog.Default())`; every `"app"` passed to a store method → `int64(1)`; `store.View{Project: "app"}` → `ProjectID: 1`; `ProjectAliases` → `ProjectIDs` in any fake store in `errors_test.go`, with the fake's methods taking `int64`. Delete the `intPtr` helper if its only use was a retention override.

- [ ] **Step 5: Run the package tests**

Run: `cd internal/jobs && go vet . && go test -race .`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/jobs
git commit -m "feat(jobs): run the daily pass by project id with global retention windows"
```

---

### Task 7: MCP tools, REST routes, guide, schema resources — and their docs tables

Implements spec §7.1 and the tool/route parts of §8. Every input takes `project_id` (JSON integer), every path is `/api/projects/{project_id}/…`, `setField` learns `int64`, and the unknown-project error lists `id (name)` pairs. `docs/twillingate.md`'s tool table and HTTP API table change in this commit because `docs_sync_test.go` binds them to the registrar. `api` builds and passes after this.

**Files:**
- Modify: `internal/api/ops_manage.go`, `ops_read.go`, `ops_product.go`, `guide.go`, `rest.go`, `resources.go`
- Modify: `docs/twillingate.md` (the "Reading" tool table, the "Managing" paragraph's `{alias, …}` line, the HTTP API table and its curl example, the `schema://views` sentence)
- Modify tests: `seed_test.go`, `ops_manage_test.go`, `ops_read_test.go`, `ops_product_test.go`, `rest_test.go`, `coverage_test.go`, `guide_test.go`, `resources_test.go`, `server_test.go`, `auth_test.go`, `oauth_e2e_test.go`, `readdb_test.go`, `docs_sync_test.go`

**Interfaces:**
- Consumes: `manage` from Task 3; `store` from Task 2.
- Produces (wire shapes an agent sees):

```go
type rangeIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	From      string `json:"from" jsonschema:"start day inclusive, YYYY-MM-DD"`
	To        string `json:"to" jsonschema:"end day inclusive, YYYY-MM-DD"`
}
type projectOut struct {           // list_projects rows
	ProjectID      int64    `json:"project_id"`
	Name           string   `json:"name"`
	Identity       string   `json:"identity"`
	Archived       bool     `json:"archived,omitempty"`
	FirstViewDay   string   `json:"first_view_day,omitempty"`
	LastViewDay    string   `json:"last_view_day,omitempty"`
	AllowedOrigins []string `json:"allowed_origins"`
	Attributes     []string `json:"attributes,omitempty"`
}
type createProjectIn struct {
	Name           string   `json:"name" jsonschema:"display name (required); need not be unique"`
	Identity       string   `json:"identity,omitempty" jsonschema:"…unchanged text…"`
	AllowedOrigins []string `json:"allowed_origins,omitempty" jsonschema:"…unchanged text…"`
	Attributes     []string `json:"attributes,omitempty" jsonschema:"…unchanged text…"`
	SkipKey        bool     `json:"skip_key,omitempty" jsonschema:"set true to NOT issue a first ingest key"`
}
type updateProjectIn struct {
	ProjectID      int64    `json:"project_id" jsonschema:"project id; call list_projects first"`
	Name           string   `json:"name,omitempty" jsonschema:"new display name; omit to keep"`
	Identity       string   `json:"identity,omitempty" jsonschema:"…unchanged text…; omit to keep"`
	AllowedOrigins []string `json:"allowed_origins,omitempty" jsonschema:"replaces the whole list; an explicit [] clears it; omit to keep"`
	Attributes     []string `json:"attributes,omitempty" jsonschema:"replaces the whole list; omit to keep"`
}
type projectToolOut struct {
	ProjectID int64  `json:"project_id"`
	Identity  string `json:"identity"`
	Key       string `json:"key,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
	Note      string `json:"note,omitempty"`
}
type idIn struct { ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"` }
type keyIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Label     string `json:"label" jsonschema:"key label, e.g. web, ios; unique per project"`
}
type listKeysIn struct { ProjectID int64 `json:"project_id,omitempty" jsonschema:"filter to one project"` }
type keyRow struct {
	ProjectID          int64 `json:"project_id"`
	Label, Key, State string
}
type guideIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Platform  string `json:"platform" jsonschema:"…unchanged…"`
}
```

Routes: `const p = "/api/projects/{project_id}"`; `update_project` is `PATCH /api/projects/{project_id}`; archive/restore are `POST /api/projects/{project_id}/archive|restore`. `list_ingest_keys` stays `GET /api/keys` with `?project_id=`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/api/ops_read_test.go`:

```go
func TestUnknownProjectErrorListsIdsAndNames(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "views_overview", map[string]any{
		"project_id": 7, "from": "2026-08-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("unknown project accepted")
	}
	if msg := textOf(res); !strings.Contains(msg, "unknown project 7; valid projects: 1 (blog), 2 (docs)") {
		t.Fatalf("error = %q, want the id (name) list", msg)
	}
}
```

Add to `internal/api/rest_test.go`:

```go
// TestPathProjectIdBindsAsInt64: the {project_id} wildcard must land in
// an int64 field, and a non-integer must be a 400, not a 404.
func TestPathProjectIdBindsAsInt64(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	if rec := serveREST(t, r, "GET", "/api/projects/1/views/overview?from=2026-08-20&to=2026-08-21", ""); rec.Code != 200 {
		t.Fatalf("id 1: %d %s", rec.Code, rec.Body.String())
	}
	rec := serveREST(t, r, "GET", "/api/projects/blog/views/overview?from=2026-08-20&to=2026-08-21", "")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "project_id must be an integer") {
		t.Fatalf("alias in the path: %d %s, want 400 project_id must be an integer", rec.Code, rec.Body.String())
	}
}
```

Rewrite `TestUpdateProjectEmptyOriginsPreservesExisting` in `ops_manage_test.go` into its inverse:

```go
// TestUpdateProjectEmptyOriginsClears: an explicit [] clears the list and
// an omitted field keeps it — JSON can tell the two apart, and the tool
// passes that distinction through.
func TestUpdateProjectEmptyOriginsClears(t *testing.T) {
	h, cs := newTestHost(t)
	if res := callTool(t, cs, "update_project", map[string]any{
		"project_id": 1, "allowed_origins": []string{"https://blog.example.com"}}); res.IsError {
		t.Fatal(textOf(res))
	}
	if res := callTool(t, cs, "update_project", map[string]any{"project_id": 1, "name": "Blog"}); res.IsError {
		t.Fatal(textOf(res))
	}
	if p := h.reg.Snapshot(context.Background()).Project(1); len(p.AllowedOrigins) != 1 {
		t.Fatalf("omitted allowed_origins changed the list: %v", p.AllowedOrigins)
	}
	if res := callTool(t, cs, "update_project", map[string]any{"project_id": 1, "allowed_origins": []string{}}); res.IsError {
		t.Fatal(textOf(res))
	}
	if p := h.reg.Snapshot(context.Background()).Project(1); len(p.AllowedOrigins) != 0 {
		t.Fatalf("explicit [] did not clear: %v", p.AllowedOrigins)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd internal/api && go test -run 'TestUnknownProjectErrorListsIdsAndNames|TestPathProjectIdBindsAsInt64|TestUpdateProjectEmptyOriginsClears' .`
Expected: compile errors (`manage.New` arity in `seed_test.go`, `store.RegistryProject` has no `Alias`).

- [ ] **Step 3: `rest.go` — bind int64 path and query values**

In `setField` add, after the `reflect.Int` case:

```go
	case reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return invalidf("%s must be an integer, got %q", name, raw)
		}
		f.SetInt(n)
```

- [ ] **Step 4: `ops_read.go`**

- `rangeIn` as under Interfaces. `checkRange`: `h.reg.Snapshot(ctx).Project(in.ProjectID) == nil` → `h.unknownProjectErr(ctx, in.ProjectID)`.
- Replace `unknownProjectErr`:

```go
// unknownProjectErr lists the valid ids with their names so a model can
// recover (endpoint spec §10) without a second list_projects call.
func (h *host) unknownProjectErr(ctx context.Context, id int64) error {
	var choices []string
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		choices = append(choices, fmt.Sprintf("%d (%s)", p.ID, p.Name))
	}
	return notFoundf("unknown project %d; valid projects: %s", id, strings.Join(choices, ", "))
}
```

Drop the `sort` import if nothing else uses it and the `config` import (the `Retention` field goes).
- `projectOut` as under Interfaces; `listProjects` fills `ProjectID: p.ID` and probes `… FROM v_views_daily WHERE project_id=?`, `p.ID`.
- `viewsOverview` / `viewsBreakdown`: `WHERE project_id=?` with `in.ProjectID`.
- `register`: `const p = "/api/projects/{project_id}"`; `update_project` path `"/api/projects/{project_id}"`; archive/restore `"/api/projects/{project_id}/archive"` / `/restore`. Descriptions: `list_projects` → "List projects with id, name, identity mode and data coverage. Call this first: every other tool takes a project_id from here. …"; `update_project` → "Update a project's name, identity mode, allowed origins and/or declared product-event attributes. Fields you omit are left unchanged; allowed_origins and attributes replace the whole list when given, and an explicit empty allowed_origins clears it. Switching to identity=identified …" (keep the privacy sentence).

- [ ] **Step 5: `ops_manage.go`**

Replace `projectIn`, `projectToolOut`, `createProject`, `updateProject`, `projectErr`, `aliasIn`, the archive/restore handlers, `keyIn`, `listKeysIn`, `keyRow`, `listKeys` with the shapes under Interfaces and these bodies:

```go
func (h *host) createProject(ctx context.Context, in createProjectIn) (projectToolOut, error) {
	spec := manage.ProjectSpec{Name: in.Name, Identity: in.Identity,
		AllowedOrigins: in.AllowedOrigins, Attributes: in.Attributes}
	var p *manage.Project
	var key string
	var err error
	if in.SkipKey {
		p, err = h.ops.CreateProject(ctx, actorFrom(ctx), spec)
	} else {
		p, key, err = h.ops.CreateProjectWithKey(ctx, actorFrom(ctx), spec, "default")
	}
	if err != nil {
		return projectToolOut{}, err
	}
	out := projectToolOut{ProjectID: p.ID, Identity: p.Identity, Key: key}
	if key != "" {
		out.Snippet = manage.Snippet(h.publicURL, key, p.Identity)
		if h.publicURL == "" {
			out.Note = "PUBLIC_URL is not configured; the snippet uses the placeholder " + manage.SnippetPlaceholderBase + " — ask the operator for the collector's public URL"
		}
	}
	return out, nil
}

// updateProject passes the merge to Ops.UpdateProject: "" and nil keep,
// a non-nil slice replaces, so an explicit [] clears the origins.
func (h *host) updateProject(ctx context.Context, in updateProjectIn) (projectToolOut, error) {
	p, err := h.ops.UpdateProject(ctx, actorFrom(ctx), manage.ProjectSpec{
		ID: in.ProjectID, Name: in.Name, Identity: in.Identity,
		AllowedOrigins: in.AllowedOrigins, Attributes: in.Attributes})
	if err != nil {
		return projectToolOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return projectToolOut{ProjectID: p.ID, Identity: p.Identity}, nil
}

func (h *host) projectErr(ctx context.Context, id int64, err error) error {
	if errors.Is(err, manage.ErrNotFound) {
		return h.unknownProjectErr(ctx, id)
	}
	return err
}

func (h *host) archiveProject(ctx context.Context, in idIn) (okOut, error) {
	if err := h.ops.ArchiveProject(ctx, actorFrom(ctx), in.ProjectID); err != nil {
		return okOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return okOut{Status: "archived; ingestion rejected, data kept, reversible with restore_project"}, nil
}

func (h *host) restoreProject(ctx context.Context, in idIn) (okOut, error) {
	if err := h.ops.RestoreProject(ctx, actorFrom(ctx), in.ProjectID); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "restored"}, nil
}

func (h *host) issueKey(ctx context.Context, in keyIn) (keyOut, error) {
	key, err := h.ops.IssueIngestKey(ctx, actorFrom(ctx), in.ProjectID, in.Label)
	if err != nil {
		return keyOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	out := keyOut{Key: key, Status: "issued"}
	if p := h.reg.Snapshot(ctx).Project(in.ProjectID); p != nil {
		out.Snippet = manage.Snippet(h.publicURL, key, p.Identity)
		if h.publicURL == "" {
			out.Note = "PUBLIC_URL is not configured; the snippet uses the placeholder " + manage.SnippetPlaceholderBase + " — ask the operator for the collector's public URL"
		}
	}
	return out, nil
}

// disableKey / enableKey: in.ProjectID instead of in.Project, otherwise unchanged.

func (h *host) listKeys(ctx context.Context, in listKeysIn) (listKeysOut, error) {
	_, ks, err := h.ops.St.LoadRegistry(ctx)
	if err != nil {
		return listKeysOut{}, err
	}
	var out listKeysOut
	for _, k := range ks {
		if in.ProjectID != 0 && k.ProjectID != in.ProjectID {
			continue
		}
		state := "active"
		if k.Disabled {
			state = "disabled"
		}
		out.Keys = append(out.Keys, keyRow{ProjectID: k.ProjectID, Label: k.Label, Key: k.Key, State: state})
	}
	return out, nil
}
```

- [ ] **Step 6: `ops_product.go`, `guide.go`, `resources.go`**

`ops_product.go`: every `WHERE project=?` → `WHERE project_id=?`, every `in.Project` → `in.ProjectID` (including the `agg_retention` freshness probe and the retention refusal message, which becomes `"project %d is anonymous: …"`). The identities join: `i.project_id=d.project_id` and `d.project_id=?`.

`guide.go`: `guideIn` as above; `p := s.Project(in.ProjectID)`; the key scan `k.ProjectID == p.ID`; the heading

```go
	fmt.Fprintf(&b, "# Integrating %s (project %d; %s, identity=%s)\n%s%s%s\n", p.Name, p.ID, in.Platform, p.Identity, baseNote, hostNote, keyLine)
```

and `origins := p.AllowedOrigins` in place of the second snapshot lookup.

`resources.go`: in `schemaViews` replace `project` with `project_id` in every view line and change the lead sentence to `Views (all carry a 'project_id' column — always filter on it; ids come from list_projects):`. In `schema://projects`:

```go
		type pj struct {
			ProjectID      int64    `json:"project_id"`
			Name, Identity string
			Archived       bool     `json:",omitempty"`
			AllowedOrigins []string `json:"allowed_origins"`
		}
		…
			out = append(out, pj{p.ID, p.Name, p.Identity, p.Archived, p.AllowedOrigins})
```

- [ ] **Step 7: Adapt the test fixtures**

`seed_test.go` `newTestHost`: create the projects in a fixed order so blog is 1 and docs is 2, and seed with integer ids:

```go
	for _, p := range []struct{ name, identity string }{{"blog", "identified"}, {"docs", "anonymous"}} {
		if _, err := st.CreateProject(ctx, store.RegistryProject{
			Name: p.name, Identity: p.identity, AllowedOrigins: "[]", Attributes: "[]"},
			store.AuditEntry{Actor: "test", Action: "project.create"}); err != nil {
			t.Fatal(err)
		}
	}
```

then in every `seed(…)` string: `'blog'` → `1`, `(project, ` → `(project_id, `, `WHERE alias='blog'` → `WHERE id=1`; `reg := manage.New(st, logger)`; delete `testRetention` if nothing else uses it. `readdb_test.go` `seedDB`: `RegistryProject{Name: "My blog", Identity: "identified", AllowedOrigins: "[]", Attributes: "[]"}` and drop the returned id.

Tool-call arguments across `*_test.go`: `"project": "blog"` → `"project_id": 1`, `"project": "docs"` → `"project_id": 2`, `"alias": "blog"` → `"project_id": 1`; create calls `{"alias": "nokey", …}` → `{"name": "nokey", …}` and then read the returned `project_id` (3) for the follow-up `list_ingest_keys` filter. `"valid aliases: blog"` → `"valid projects: 1 (blog)"`. REST paths `/api/projects/blog/` → `/api/projects/1/`, `/api/projects/shop/…` → the id the test's own create returned (`3`); the PATCH body test at `rest_test.go:90` becomes `PATCH /api/projects/1` with body `{"project_id":2,"name":"Blog"}` and asserts the path won (project 1 renamed, 2 untouched). `guide_test.go`: the heading assertion becomes `# Integrating blog (project 1; web, identity=identified)`. `TestUpdateProjectMerges` and `TestUpdateProjectAttributesMergeSemantics` keep their intent with `project_id`; `TestUpdateProjectUnknownAlias` becomes `TestUpdateProjectUnknownId` with `"project_id": 42`.

- [ ] **Step 8: Update `docs/twillingate.md` for this surface**

In the MCP section:
- `**Reading (all take `project`, `from`, `to` …)**` → `**Reading (all take `project_id`, `from`, `to` as `YYYY-MM-DD` unless noted):**`.
- `list_projects` row: `| `list_projects` | none | Every project with its `project_id`, name, identity mode and data coverage. Call this first — every other tool needs a `project_id` |`.
- The "Set up a project" paragraph: `` `create_project` takes `{name, identity, allowed_origins, attributes}` (`name` required) and returns the new `project_id`; `update_project` takes `{project_id, …}` and merges — an omitted field keeps its value, `allowed_origins: []` clears the list. `create_project` also takes `skip_key: true` … `issue_ingest_key` takes `{project_id, label}` … `integration_guide` takes `{project_id, platform}` … ``.
- The curl example path → `/api/projects/1/views/overview?…`.
- The HTTP API table:

```
| `GET` | `/api/projects` | `list_projects` | — |
| `POST` | `/api/projects` | `create_project` | body: `name`, `identity`, `allowed_origins`, `attributes`, `skip_key` → 201 |
| `PATCH` | `/api/projects/{project_id}` | `update_project` | body: fields to change (merge); `allowed_origins: []` clears |
| `POST` | `/api/projects/{project_id}/archive` | `archive_project` | — |
| `POST` | `/api/projects/{project_id}/restore` | `restore_project` | — |
| `GET` | `/api/keys` | `list_ingest_keys` | query: `project_id` |
| `POST` | `/api/projects/{project_id}/keys` | `issue_ingest_key` | body: `label` → 201 |
| `POST` | `/api/projects/{project_id}/keys/{label}/disable` | `disable_ingest_key` | — |
| `POST` | `/api/projects/{project_id}/keys/{label}/enable` | `enable_ingest_key` | — |
| `GET` | `/api/projects/{project_id}/views/overview` | `views_overview` | query: `from`, `to`, `kind` |
| `GET` | `/api/projects/{project_id}/views/breakdown` | `views_breakdown` | query: `from`, `to`, `dimension`, `limit` |
| `GET` | `/api/projects/{project_id}/product/events` | `product_events` | query: `from`, `to`, `event` |
| `GET` | `/api/projects/{project_id}/product/attributes` | `product_attributes` | query: `from`, `to`, `event` |
| `GET` | `/api/projects/{project_id}/retention` | `retention` | query: `from`, `to`, `actor` |
| `GET` | `/api/projects/{project_id}/identities` | `identities` | query: `from`, `to`, `kind`, `limit` |
| `POST` | `/api/query` | `query` | body: `sql` |
| `GET` | `/api/schema/views` | `schema://views` | — (text/plain) |
```

- In "Query the data": `Every view carries a `project` column` → `Every view carries a `project_id` column — always filter on it; the ids are the ones `list_projects` returns.`

- [ ] **Step 9: Run the package tests**

Run: `cd internal/api && go vet . && go test -race .`
Expected: PASS, including `TestDocumentMatchesRoutes`, `TestDocumentNamesEveryTool` and the three new tests.

- [ ] **Step 10: Commit**

```bash
git add internal/api docs/twillingate.md
git commit -m "feat(api): take and return project_id on every tool and route"
```

---

### Task 8: `app` wiring and the CLI — the whole tree builds again

Implements spec §6.6 and §7.2, plus the CLI parts of §8. Removes `config` (`import`/`export`), `project rename` and the boot warning that pointed at `config import`. After this task `go build ./...` and `go vet ./...` are green.

**Files:**
- Modify: `internal/app/app.go`, `cmd/twillingate/project.go` (rewrite), `cmd/twillingate/key.go` (rewrite), `cmd/twillingate/main.go`
- Delete: `cmd/twillingate/configcmd.go`, `cmd/twillingate/configcmd_test.go`
- Modify: `docs/twillingate.md` (operation table, CLI block, origins paragraph, project fields table, snippet-mode `key issue` line)
- Modify tests: `internal/app/app_test.go`, `internal/app/errors_test.go`, `cmd/twillingate/project_test.go`, `cmd/twillingate/key_test.go`, `cmd/twillingate/main_test.go`

**Interfaces:**
- Consumes: `manage.New(st, logger)`, `Snapshot.KeylessProjects() []*Project`, id-taking `Ops`.
- Produces: the CLI surface

```
twillingate project create -name "Blog" [-identity …] [-origin …]… [-attr …]…
twillingate project update -id 1 [-name …] [-identity …] [-origin …]… [-clear-origins] [-attr …]…
twillingate project list                      # id  identity  name  [archived]
twillingate project archive|restore -id 1
twillingate project delete -id 1 [-force]
twillingate key issue|disable|enable -project-id 1 -label web
twillingate key list [-project-id 1]          # project_id  label  key  state
```

- [ ] **Step 1: Write the failing CLI tests**

Replace `TestProjectCreateListArchiveDelete` in `cmd/twillingate/project_test.go`:

```go
func TestProjectCreateListArchiveDelete(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create"}, &out); code != 2 {
		t.Fatalf("create without -name: exit %d, want 2: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "create", "-name", "My blog",
		"-origin", "https://blog.example.com"}, &out); code != 0 {
		t.Fatalf("create: exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `project 1 ("My blog") created`) ||
		!strings.Contains(out.String(), "key issue -project-id 1 -label web") {
		t.Fatalf("create output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.HasPrefix(out.String(), "1\tanonymous\tMy blog") {
		t.Fatalf("list output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"project", "archive", "-id", "1"}, &out); code != 0 {
		t.Fatalf("archive: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "restore", "-id", "1"}, &out); code != 0 {
		t.Fatalf("restore: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "delete", "-id", "1"}, &out); code == 0 {
		t.Fatal("delete without -force succeeded")
	}
	out.Reset()
	if code := run([]string{"project", "delete", "-id", "1", "-force"}, &out); code != 0 {
		t.Fatalf("delete -force: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 || strings.Contains(out.String(), "My blog") {
		t.Fatalf("project survived delete: %s", out.String())
	}
}

func TestProjectUpdateClearsOriginsOnlyWhenAsked(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", "https://shop.example.com"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-name", "Shop UK"}, &out); code != 0 {
		t.Fatalf("update: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-origin", "https://a.com", "-clear-origins"}, &out); code != 2 {
		t.Fatalf("-origin with -clear-origins: exit %d, want 2", code)
	}
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-clear-origins"}, &out); code != 0 {
		t.Fatalf("clear: %s", out.String())
	}
	// The registry is the source of truth: reopen it and look.
	ops, _, closeStore, code := openOps(&out, "")
	if code != 0 {
		t.Fatal(out.String())
	}
	defer closeStore()
	p := ops.Reg.Snapshot(context.Background()).Project(1)
	if p == nil || p.Name != "Shop UK" || len(p.AllowedOrigins) != 0 {
		t.Fatalf("project = %+v, want name Shop UK and no origins", p)
	}
	out.Reset()
	if code := run([]string{"project", "update", "-id", "9"}, &out); code != 1 || !strings.Contains(out.String(), "unknown id 9") {
		t.Fatalf("unknown id: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "rename", "-id", "1"}, &out); code != 2 {
		t.Fatalf("rename must be an unknown subcommand: exit %d", code)
	}
	out.Reset()
	if code := run([]string{"config", "export"}, &out); code != 2 {
		t.Fatalf("config must be an unknown command: exit %d", code)
	}
}
```

Add `"context"` to the imports. In `key_test.go` change `"project", "create", "-alias", "blog"` → `"-name", "blog"` and every `"-project", "blog"` → `"-project-id", "1"`; add an assertion that `key list` output starts with `1\tweb\tak_`.

- [ ] **Step 2: Run to verify they fail**

Run: `cd cmd/twillingate && go test -run 'TestProject|TestKey' .`
Expected: compile error (`manage.New` arity in `openOps`).

- [ ] **Step 3: Rewrite `cmd/twillingate/project.go`**

Keep `envFileLookup`, `openOps` (change one line: `reg := manage.New(st, app.NewLogger(cfg.Log))`) and `multiFlag`. Replace `cmdProject`:

```go
const projectUsage = "usage: twillingate project <create|update|list|archive|restore|delete> [flags]"

func cmdProject(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	fs.SetOutput(stdout)
	envFile := fs.String("env-file", "", "load environment from this file (real env wins)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stdout, projectUsage)
		return 2
	}
	sub, subArgs := rest[0], rest[1:]
	switch sub {
	case "create", "update", "list", "archive", "restore", "delete":
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\n%s\n", sub, projectUsage)
		return 2
	}
	ops, _, closeStore, code := openOps(stdout, *envFile)
	if code != 0 {
		return code
	}
	defer closeStore()
	ctx := context.Background()
	switch sub {
	case "create":
		sf := flag.NewFlagSet("project create", flag.ContinueOnError)
		sf.SetOutput(stdout)
		name := sf.String("name", "", "display name (required)")
		identity := sf.String("identity", "anonymous", "anonymous|identified")
		var origins, attrs multiFlag
		sf.Var(&origins, "origin", "allowed origin, `*` wildcards accepted (repeatable)")
		sf.Var(&attrs, "attr", "attribute key to break down (repeatable)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if *name == "" {
			fmt.Fprintln(stdout, "usage: twillingate project create -name <name> [-identity anonymous|identified] [-origin ...] [-attr ...]")
			return 2
		}
		p, err := ops.CreateProject(ctx, "cli", manage.ProjectSpec{
			Name: *name, Identity: *identity, AllowedOrigins: origins, Attributes: attrs})
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %d (%q) created\n", p.ID, p.Name)
		fmt.Fprintf(stdout, "next: twillingate key issue -project-id %d -label web\n", p.ID)
		return 0
	case "update":
		sf := flag.NewFlagSet("project update", flag.ContinueOnError)
		sf.SetOutput(stdout)
		id := sf.Int64("id", 0, "project id (required)")
		name := sf.String("name", "", "new display name")
		identity := sf.String("identity", "", "anonymous|identified")
		var origins, attrs multiFlag
		sf.Var(&origins, "origin", "allowed origin, replaces the whole list (repeatable)")
		clearOrigins := sf.Bool("clear-origins", false, "remove every allowed origin")
		sf.Var(&attrs, "attr", "attribute key to break down, replaces the whole list (repeatable)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if *id == 0 {
			fmt.Fprintln(stdout, "usage: twillingate project update -id <id> [-name ...] [-identity ...] [-origin ... | -clear-origins] [-attr ...]")
			return 2
		}
		if *clearOrigins && len(origins) > 0 {
			fmt.Fprintln(stdout, "use -origin or -clear-origins, not both")
			return 2
		}
		// A flag left out is nil and keeps the current list; -clear-origins
		// sends an empty non-nil list, which clears it.
		spec := manage.ProjectSpec{ID: *id, Name: *name, Identity: *identity,
			AllowedOrigins: origins, Attributes: attrs}
		if *clearOrigins {
			spec.AllowedOrigins = []string{}
		}
		p, err := ops.UpdateProject(ctx, "cli", spec)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %d (%q) updated\n", p.ID, p.Name)
		return 0
	case "list":
		for _, p := range ops.Reg.Snapshot(ctx).Projects() {
			state := ""
			if p.Archived {
				state = "\t(archived)"
			}
			fmt.Fprintf(stdout, "%d\t%s\t%s%s\n", p.ID, p.Identity, p.Name, state)
		}
		return 0
	case "archive", "restore":
		sf := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
		sf.SetOutput(stdout)
		id := sf.Int64("id", 0, "project id (required)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if *id == 0 {
			fmt.Fprintf(stdout, "usage: twillingate project %s -id <id>\n", sub)
			return 2
		}
		var err error
		if sub == "archive" {
			err = ops.ArchiveProject(ctx, "cli", *id)
		} else {
			err = ops.RestoreProject(ctx, "cli", *id)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %d %sd\n", *id, sub)
		return 0
	default: // delete
		sf := flag.NewFlagSet("project delete", flag.ContinueOnError)
		sf.SetOutput(stdout)
		id := sf.Int64("id", 0, "project id (required)")
		force := sf.Bool("force", false, "skip confirmation")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if *id == 0 {
			fmt.Fprintln(stdout, "usage: twillingate project delete -id <id> [-force]")
			return 2
		}
		if !*force {
			fmt.Fprintf(stdout, "This permanently deletes project %d and ALL its data.\n", *id)
			fmt.Fprintln(stdout, "Re-run with -force to confirm.")
			return 1
		}
		if err := ops.DeleteProject(ctx, "cli", *id); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %d deleted\n", *id)
		return 0
	}
}
```

Note the unknown-subcommand check now runs *before* the store is opened, so `project rename` is refused without touching the database; `store` and `config` imports may become unused in this file — `openOps` still uses both, so they stay.

- [ ] **Step 4: Rewrite `cmd/twillingate/key.go`'s flag block and bodies**

```go
	sf := flag.NewFlagSet("key "+sub, flag.ContinueOnError)
	sf.SetOutput(stdout)
	projectID := sf.Int64("project-id", 0, "project id (required except for list)")
	label := sf.String("label", "", "key label")
	if err := sf.Parse(subArgs); err != nil {
		return 2
	}
	if sub != "list" && *projectID == 0 {
		fmt.Fprintf(stdout, "usage: twillingate key %s -project-id <id> -label <label>\n", sub)
		return 2
	}
	switch sub {
	case "issue":
		key, err := ops.IssueIngestKey(ctx, "cli", *projectID, *label)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		p := ops.Reg.Snapshot(ctx).Project(*projectID)
		if p == nil {
			fmt.Fprintf(stdout, "issued %s (label %q) but project %d vanished before the snippet could be built; run `twillingate key list` to confirm\n", key, *label, *projectID)
			return 1
		}
		fmt.Fprintf(stdout, "issued %s (label %q)\n\nWeb snippet:\n\n%s\n",
			key, *label, manage.Snippet(cfg.PublicURL, key, p.Identity))
		if cfg.PublicURL == "" {
			fmt.Fprintln(stdout, "\nnote: PUBLIC_URL is not set; replace "+manage.SnippetPlaceholderBase+" with your collector URL")
		}
		return 0
	case "list":
		_, ks, err := ops.St.LoadRegistry(ctx)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		for _, k := range ks {
			if *projectID != 0 && k.ProjectID != *projectID {
				continue
			}
			state := "active"
			if k.Disabled {
				state = "disabled"
			}
			fmt.Fprintf(stdout, "%d\t%s\t%s\t%s\n", k.ProjectID, k.Label, k.Key, state)
		}
		return 0
	case "disable", "enable":
		var err error
		if sub == "disable" {
			err = ops.DisableIngestKey(ctx, "cli", *projectID, *label)
		} else {
			err = ops.EnableIngestKey(ctx, "cli", *projectID, *label)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "key %d/%s %sd\n", *projectID, *label, sub)
		return 0
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\nusage: twillingate key <issue|list|disable|enable> [flags]\n", sub)
		return 2
	}
```

- [ ] **Step 5: `main.go`, `configcmd.go`, `app.go`**

`main.go`: both usage strings become `usage: twillingate <serve|dashboards|migrate|keygen|project|key|version> [flags]`; the package comment drops nothing (it never listed `config`).

Delete `cmd/twillingate/configcmd.go` and `configcmd_test.go`.

`internal/app/app.go`:
- `reg := manage.New(st, logger)`.
- Delete the `warnLegacyProjectsFile(cfg, logger)` call and the function (the command it recommends no longer exists).
- The keyless warning:

```go
	for _, p := range reg.Snapshot(ctx).KeylessProjects() {
		logger.Warn("project has no active ingest keys and can receive nothing", "project_id", p.ID, "name", p.Name)
	}
```

`app_test.go`: `seedProject` creates with `Name:` and passes `ProjectID: p.ID` to `InsertIngestKey`; its "already exists" guard becomes "if any project exists, return" (`len(reg.Snapshot(ctx).Projects()) > 0`) since names are not keys; `manage.New(st, slog.New(…))`; the `ProjectAliases` assertion at ~line 188 becomes `ids, err := st.ProjectIDs(bg)` with `ids[0] != 1`; delete the two `warnLegacyProjectsFile` tests (~lines 555–585) and the `os`/`filepath` imports if they become unused. `errors_test.go`: `ProjectSpec{Alias: "app", …}` → `{Name: "App", …}` and the `nokey` spec likewise.

- [ ] **Step 6: Update the CLI parts of `docs/twillingate.md`**

- Operation table: delete the `Rename` and `Export / import the registry` rows.
- The paragraph "Renaming and registry import/export have no MCP tool…" is deleted; in its place: "There is no rename: the id is the key and the name is free text, so `project update -name` is a rename. There is no delete over the API either — deletion needs the CLI."
- The CLI block:

```bash
twillingate project create -name "My App" -identity anonymous \
  -origin https://myapp.com -attr plan -attr tier
twillingate project list                                 # id  identity  name
twillingate project update -id 1 -origin https://myapp.com -origin https://www.myapp.com
twillingate project update -id 1 -clear-origins
twillingate project archive -id 1                        # reversible: `project restore`
twillingate key issue -project-id 1 -label web
twillingate key list -project-id 1
twillingate key disable -project-id 1 -label ios-2025
```

- The merge paragraph: "`project update` (and the `update_project` MCP tool) merge rather than replace: a field you omit keeps its current value. `-origin` and `-attr` are the exception — supplying either replaces the whole list. `-clear-origins` (or `allowed_origins: []` over the API) empties the origins."
- The project fields table: replace the `alias` row with `` | `project_id` | Integer key assigned on create, never reissued after a delete. The `project_id` column on every stored row, the argument of every tool, route and CLI command, and the segment of every dashboard URL. Never transmitted by clients. | `` and change `name` to `` | `name` | Display name. Required; free text, need not be unique; change it with `project update -name`. | ``. Delete the `retention` row. Delete the paragraph and JSON example that follow the table (``retention` is not a CLI flag …` through the fenced `"retention": …` block).
- Attribute breakdowns example: `twillingate project update -id 1 -attr plan -attr tier`.
- Snippet mode: `` `twillingate key issue -project-id <id> -label <label>` mints the key ``.

- [ ] **Step 7: Verify the whole tree**

Run: `go build ./... && go vet ./... && cd cmd/twillingate && go test -race . && cd ../../internal/app && go test -race . && cd ../api && go test -run 'TestDocument' .`
Expected: PASS everywhere; `go build ./...` is green for the first time since Task 2.

- [ ] **Step 8: Commit**

```bash
git add cmd/twillingate internal/app docs/twillingate.md
git commit -m "feat(cmd)!: address projects by id in the CLI and drop rename and config import/export"
```

---

### Task 9: Evidence dashboards, scripts and deploy files

Implements spec §7.3 and §7.4, plus two dev-only files the spec's list omits but that break on the new schema: `scripts/seed-demo.py` and the `make run` / `seed-demo` targets, which still speak alias and `projects.json`.

**Files:**
- Modify: `evidence/sources/twillingate/projects.sql`, `evidence/pages/index.md`, `evidence/pages/archived.md`, `evidence/pages/views/[project].md`, `evidence/pages/views/[project]/page.md`, `evidence/pages/product/[project].md`, `evidence/pages/users/[project].md`, `evidence/pages/groups/[project].md`, `evidence/pages/retention/[project].md`, `evidence/svelte.config.js`
- Modify: `scripts/smoke.sh`, `scripts/test-compose.sh`, `scripts/test-install-inner.sh`, `scripts/smokecheck/main.go`, `scripts/seed-demo.py`, `deploy/systemd/install.sh`, `deploy/compose/docker-compose.yml`, `Makefile`

**Interfaces:**
- Consumes: `projects(id, name, identity, archived_at)`; every `v_*` view's `project_id`; the CLI from Task 8.
- Produces: dashboard URLs `/views/<id>`, `/product/<id>`, `/users/<id>`, `/groups/<id>`, `/retention/<id>`; the `[project]` route segment keeps its name.

- [ ] **Step 1: Sources and index pages**

`evidence/sources/twillingate/projects.sql` (keep the existing comment block, change the sentinel sentence to "index.md filters the sentinel out on `id != 0`, an id AUTOINCREMENT never issues"):

```sql
select id, name, identity,
       case when archived_at is null then 0 else 1 end as archived
from projects
union all
select 0, '', 'anonymous', 0 where not exists (select 1 from projects)
```

`evidence/pages/index.md`: both queries select `id, name, identity` / `count(*)` with `and id != 0`; the list line becomes

```
- **{p.name}** ({p.identity}) — [views](/views/{p.id}) · [product](/product/{p.id}) · [users](/users/{p.id}) · [groups](/groups/{p.id}) · [retention](/retention/{p.id})
```

`evidence/pages/archived.md`: `select id, name … and id != 0`; links `/views/{p.id}` and `/product/{p.id}`.

- [ ] **Step 2: The six project pages**

In every `evidence/pages/**/[project].md` and `views/[project]/page.md`:

- `where project = '${params.project}'` → `where project_id = '${params.project}'` (also `d.project =` → `d.project_id =`, and the identities join `on i.project = d.project` → `on i.project_id = d.project_id`). The value stays quoted: DuckDB casts the literal to the column's integer type, and quoting keeps a URL segment from becoming SQL.
- Add a name lookup directly under the H1 of each page and use it in the heading:

```markdown
# {project_name[0].name} — Views

```sql project_name
select name from twillingate.projects where id = '${params.project}'
```
```

(Product, Users, Groups, Retention likewise; `page.md`'s back-link becomes `[← back to {project_name[0].name}](/views/{params.project})`.)
- `users` and `retention`: `select identity from twillingate.projects where alias = '${params.project}'` → `where id = '${params.project}'`; the hint lines become `twillingate project update -id {params.project} -identity identified`.
- `evidence/svelte.config.js`: rename `projectAliases` → `projectIds`, query `select id from projects order by id`, map `String(row.id)`, and `const PLACEHOLDER = '0';` with the comment "id 0 is never issued, so the placeholder page queries nothing and the string still casts to the integer key" (the old `__no_projects__` would fail DuckDB's cast against an integer column and break the empty-database build).

- [ ] **Step 3: Scripts and deploy**

- `scripts/smoke.sh`: `./twillingate project create -name "Smoke" -identity anonymous -origin "http://localhost"`; `key issue -project-id 1 -label smoke`. Add a comment: "a fresh database's first project is id 1".
- `scripts/test-compose.sh`: `project create -name Dev -origin "http://localhost:18080"`; `key issue -project-id 1 -label web`.
- `scripts/test-install-inner.sh`: `project create -name myapp` and `grep -q 'project 1 ("myapp") created'`.
- `scripts/smokecheck/main.go`: `count("events")` and print `views=%d events=%d`; update `scripts/smoke.sh` / `test-compose.sh` if they grep the old `product=` token.
- `deploy/systemd/install.sh` next steps: `project create -name myapp` and `key issue -project-id 1 -label web`, with a line "  (the first project on a fresh install is id 1; `project list` shows the rest)".
- `deploy/compose/docker-compose.yml` header comment: the same two commands.
- `Makefile`: `run` target creates a project only when none exists —

```make
run: build local/.env
	set -a; . ./local/.env; set +a; \
	./$(BIN) project list | grep -q . || ./$(BIN) project create -name dev; \
	./$(BIN) serve
```

  (update its comment: "projects live in the database; make sure one exists, since names are not unique and a blind create would add another each run"). `seed-demo` drops `local/projects.json` and `PROJECTS_FILE=`: `seed-demo: local/.env build` running `DATABASE_DSN=… ./$(BIN) migrate` then `python3 scripts/seed-demo.py local/twillingate.db`. Delete the `local/projects.json` target and the `LOCAL_PROJECTS` define.
- `scripts/seed-demo.py`: read projects from the database instead of a file —

```python
def main():
    db = sys.argv[1]
    con = sqlite3.connect(db)
    cur = con.cursor()
    projects = cur.execute("select id, name, identity from projects order by id").fetchall()
    unknown = [name for _, name, _ in projects if name not in PROFILES]
    if unknown:
        sys.exit(f"no traffic profile for {', '.join(unknown)}; add one to PROFILES (keyed by project name)")
    today = datetime.datetime.now(datetime.timezone.utc).date()
    random.seed(1337)
    for pid, name, identity in projects:
        views, events = seed(cur, pid, name, PROFILES[name], today, identity == "identified")
        print(f"  {pid:<3} {name:<10} views={views:<7} events={events:<6} ({identity})")
    con.commit()
    con.close()
```

  `seed`, `seed_app` and `seed_identities` take `pid` as the first argument and write it in place of `alias` in every INSERT/DELETE (`project_id` column, `events` table); `alias` stays as the label inside generated ids (`install-{name}-{n}`, `user-{name}-{n}`) so pass `name` through for that. Update the docstring's usage line to `python3 scripts/seed-demo.py local/twillingate.db`.

- [ ] **Step 4: Verify the site builds against a migrated database**

Run:

```bash
go build -o twillingate ./cmd/twillingate
rm -rf local && mkdir -p local
cd internal/store/sqlite && SEED_DB=$PWD/../../../local/twillingate.db go test -run TestSeedEvidenceFixture . && cd ../../..
cd evidence && npm install && EVIDENCE_SOURCE__twillingate__filename=../../../local/twillingate.db npm run sources && EVIDENCE_SOURCE__twillingate__filename=../../../local/twillingate.db npm run build
```

Expected: the build succeeds and `.evidence/template/build/views/1/index.html` exists (project `app` is id 1 in the seed). Then `rm local/twillingate.db` and rebuild: succeeds with the `0` placeholder routes.

Run: `make smoke` (boots the binary, creates the project, posts a batch) — expected `ok:` lines and `views=1 events=2` style output from smokecheck. `make test-compose` needs Docker; run it if available.

- [ ] **Step 5: Commit**

```bash
git add evidence scripts deploy Makefile
git commit -m "feat(dashboards): address projects by id in the dashboards, scripts and installer"
```

---

### Task 10: Remaining docs, the upgrade note, and the release gate

Implements the rest of spec §8, §9 and §11: `deployment.md`'s operations table and upgrade note, the plausible README, the `config`-related prose left in `twillingate.md`, then the full check and the PR description with the breaking-change list.

**Files:**
- Modify: `docs/twillingate.md`, `docs/deployment.md`, `docs/plausible/README.md`, `docs/superpowers/specs/2026-09-19-project-ids-design.md` (Status line)

- [ ] **Step 1: `docs/twillingate.md` leftovers**

Search the file for every remaining `alias`, `config export`, `config import`, `project rename`, `-project ` and `product_events` (the three tool mentions stay; a table mention becomes `events`):

```bash
grep -n "alias\|config export\|config import\|project rename\|-project \|product_events" docs/twillingate.md
```

Fix each: the "Managing" description sentence for `update_project`, the `integration_guide` sentence, and the queryable-views paragraph ("`v_events_flat` reads the `events` table with one column per declared attribute"). The tool count sentence is unchanged (no tool was added or removed).

- [ ] **Step 2: `docs/deployment.md`**

- Quick-start commands (two places): `project create -name myapp` and `key issue -project-id 1 -label web`.
- Operations table: delete the `Export the registry` and `Import the registry` rows. The "Upgrade across a schema change" row names 014 too: "migrations such as 012 (web and app folded into one views family) and 014 (integer project ids) are irreversible".
- Add, after the operations table, a subsection:

```markdown
### Upgrading to integer project ids (migration 014)

Projects are keyed by an integer id from this migration on; the alias is
gone, and so are `config import`/`export` and per-project retention.
Before upgrading, run these against the live database — each hit is
something the migration refuses or the first daily pass will prune:

```sql
-- 1. Per-project retention overrides. Anything returned is data the first
--    daily pass after the upgrade will prune to the global window. Raise
--    the matching RETENTION_* variable first if that data must be kept.
SELECT alias, retention FROM projects WHERE retention IS NOT NULL;

-- 2. Rows whose project has no registry row. Any hit aborts migration 014.
--    Repeat for every table that has a project column.
SELECT DISTINCT project FROM views
WHERE project NOT IN (SELECT alias FROM projects);

-- 3. Duplicate key labels. Any hit aborts migration 014.
SELECT project, label, COUNT(*) FROM ingest_keys
GROUP BY project, label HAVING COUNT(*) > 1;
```

Then stop the service, copy the database (or take a Litestream
snapshot), run the installer, and check `journalctl` for migration 014.
`list_projects` (or `twillingate project list`) shows the new ids: they
follow creation order, starting at 1. Agents and scripts that stored
aliases need those ids.

Two things change on the day: retention is global from now on
(`RETENTION_*`), and anonymous actor hashes are computed from the id
instead of the alias, so an anonymous visitor seen before and after the
upgrade counts twice in that day's uniques and a session spanning it
splits. The salt rotates at midnight anyway, so the seam is one day.
```

- [ ] **Step 3: `docs/plausible/README.md`**

The routing diagram line `→ product_events` becomes `→ events`, and "Class props are stored in `product_events.attributes`" becomes "in `events.attributes`".

- [ ] **Step 4: Spec status**

In `docs/superpowers/specs/2026-09-19-project-ids-design.md` change `Status: draft` to `Status: implemented`.

- [ ] **Step 5: The full gate**

Run: `make check`
Expected: vet clean, every package passes with race, coverage gates hold (`internal/manage` and `internal/api` lost code but their tests shrank with it; if a gate dips below 90%, add a test for an uncovered branch in that package rather than lowering the gate), `TestDocumentMatchesRoutes`, `TestDocumentNamesEveryTool`, `TestDeploymentDocumentsEveryEnvVar` and the restore test pass. Also `cd internal/archtest && go test .` (no package moved layers, so it passes unchanged).

Then `grep -rn "alias\|Alias" --include=*.go internal cmd | grep -v "_test.go" | grep -v "aliasKeys\|Alias for\|is an alias\|alias for\|expression alias\|aliasPageview\|silent alias"` must return nothing project-related (the remaining hits are `$platform`/`$pageview` aliases and SQL expression aliases).

- [ ] **Step 6: Commit and open the PR**

```bash
git add docs
git commit -m "docs: describe integer project ids, the upgrade checks and the removed commands"
git push -u origin feat/project-ids
```

PR title (the squash subject): `feat!: identify projects by integer id instead of alias`.

PR body, leading with the problems (memory `pr-problem-statements`), then:

```
BREAKING CHANGE: projects are addressed by an integer project_id everywhere.
- MCP tools and REST routes take and return project_id; routes are /api/projects/{project_id}/...; create_project requires name and returns project_id; update_project's allowed_origins: [] clears the list.
- The CLI takes -id (project ...) and -project-id (key ...); `project create` requires -name.
- `twillingate config import|export` and `project rename` are removed.
- Retention is global only: projects.retention and every per-project override are gone.
- Anonymous actor hashes use the id, so uniques and sessions split once, on the upgrade day.
- The raw product table is renamed product_events -> events; saved SQL against it changes. The product_events tool keeps its name.
- Migration 014 is irreversible; docs/deployment.md lists the pre-upgrade checks.
```

Update the memory file `project-ids-design.md` ("implementation not started" → the PR number and branch) once the PR exists.

---

## Self-review

**Spec coverage.** §3 decisions: id/AUTOINCREMENT (T1), alias removed (T1–T3, T8), name required (T3), `project_id` column (T1), key uniqueness (T1, T2), hash input (T5), surfaces (T7, T8), rename removed (T3, T8), import/export removed (T3, T4, T8), retention global (T3–T6, T8), clearing origins (T3, T7, T8), existing ids by `created_at` (T1), `events` rename (T1, T2, T9, T10). §4 migration order and views (T1). §5 hash seam (T5, T10 note). §6.1–§6.6 (T2, T3, T4, T5, T6, T8). §7.1 (T7), §7.2 (T8), §7.3 (T9), §7.4 (T9). §8 docs (T7, T8, T10). §9 upgrade checks (T10). §10 tests: migration (T1), store (T2), manage (T3), API (T7), jobs (T6), server (T5), Evidence build (T9), `make check` (T10). §11 release (T10). Two additions beyond the spec's lists, both flagged in their tasks: `warnLegacyProjectsFile` (recommends a removed command) and `scripts/seed-demo.py` / the `run` and `seed-demo` Makefile targets (write alias rows).

**Placeholder scan.** Every code step carries the code; the two mechanical test sweeps (T2 step 4, T3 step 5, T7 step 7) give the substitution rules, the command that does most of it, and the named non-mechanical edits, with `go vet` as the completeness check.

**Type consistency.** `ProjectID int64` on `store.View`, `store.ProductEvent`, `store.Identity`, `store.RegistryKey`; `ID int64` on `store.RegistryProject`, `manage.Project`, `manage.ProjectSpec`; `projectID int64` on every store method (T2 interface block, repeated verbatim in `jobs.Store` T6 and `manage.Store` T3); `int64` wire fields named `project_id` (T7); `strconv.FormatInt(p.ID, 10)` as the hash key (T5). `ProjectIDs` replaces `ProjectAliases` in T2, T6 and T8's tests. `manage.New(st, logger)` in T3 and every later fixture.
