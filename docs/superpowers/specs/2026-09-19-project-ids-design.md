# Integer project ids: remove the project alias — design

Date: 2026-09-19
Status: draft

## Sequencing

This is the first of three specs that land in order, and it owns migration
`014`:

1. **this spec** — `014_project_ids.sql`
2. `2026-09-20-os-and-platform-design.md` — `015_environment.sql`
3. `2026-09-20-sdk-consent-and-instances-design.md` — no migration

It goes first because it is the widest and the most mechanical. Every later
spec is then written against `project_id` and `events` from the start, and
neither has to be rewritten for a rename that was always going to happen.

Nothing here depends on the other two, and §4.1's table list is complete as
written: `agg_views_platforms` does not exist yet, and the `platform` and
`os_name` columns arrive in 015, on tables this migration has already
rebuilt. 015 also folds `views.browser` and `views.device` in place, which
needs no schema change at all.

## 1. Purpose

A project has three identifiers today: a UUIDv7 `projects.id` that is
written once and never read, an `alias` that does all the work, and a
display `name`. The alias is the `project` column on every stored row,
the argument of every MCP tool, REST route and CLI command, the salt input
of the actor hashes, and the path segment of every dashboard URL. Because
it is the physical key, renaming one rewrites every table
(`project rename`), and it carries a charset rule, an immutability rule
and a legacy exemption from that rule.

After this change a project has an integer id and a name. The id is the
only key; the name is what people read. The UUID and the alias are gone.

Two features that exist mainly to edit or move alias-keyed configuration
go with it: `config import` / `config export`, and per-project retention
overrides (whose only edit path was import).

## 2. Non-goals

- Changing event ids. `views.id` and `product_events.id` stay UUIDv7:
  clients generate them, and a retried batch dedupes on them.
- An id for ingest keys. Keys stay addressed by project + label.
- Looking projects up by name. Names are display-only and need not be
  unique.
- Carrying the registry between instances. The database file (Litestream
  restore, or a copy) is how an instance moves; it carries ids as-is.
- Rewriting history in `audit_log`. Rows written before the change keep
  the alias in `subject`.

## 3. Decisions

| Decision | Choice |
|---|---|
| Project key | `projects.id INTEGER PRIMARY KEY AUTOINCREMENT`. AUTOINCREMENT so a deleted project's id is never reissued: stale references (audit rows, bookmarks, agent memory) must not silently land on a new project |
| Alias | Removed, with its charset rule, immutability rule and legacy exemption |
| Name | Required on create, free text, not unique, editable with `project update` |
| Reference column | `project TEXT` → `project_id INTEGER` on every table that has it, including `ingest_keys` |
| Ingest key address | `(project_id, label)`, enforced by `UNIQUE (project_id, label)` rather than the current count-then-insert check |
| Hash input | `VisitorHash` / `ActorHash` take the id as a decimal string |
| Surfaces | MCP, REST and CLI take and return `project_id`; REST paths use `{project_id}` |
| Rename | Removed as an operation. Changing the name is `project update`; nothing is keyed by it |
| Import / export | Removed, including the legacy `projects.json` path and the `config` subcommand |
| Retention | Global only (`RETENTION_*`). The `projects.retention` column and every per-project override path are removed |
| Clearing origins | `project update -clear-origins`; on `update_project`, an explicit `"allowed_origins": []` clears and an omitted field keeps |
| Existing ids | Assigned by the migration in `created_at` order, alias breaking ties, starting at 1 |
| Raw product table | Renamed `product_events` → `events`, taken while 014 rebuilds it anyway. The product *family* keeps its prefix: `agg_product_*`, `v_product_*` and the `product_events` / `product_attributes` tools are unchanged |

## 4. Schema — migration 014

`014_project_ids.sql`, one transaction like every migration. It is
irreversible, and the upgrade takes the same backup-first path as 012.

SQLite cannot turn `project` into `project_id` in place: `DROP COLUMN`
refuses a column that is part of a primary key or an index, and `project`
leads every composite key. Every affected table is therefore rebuilt
(`CREATE x_new`, `INSERT … SELECT`, `DROP x`, `ALTER TABLE x_new RENAME TO
x`, recreate indexes), the pattern 012 uses for `actors`.

### 4.1 Order

1. Drop every view (`v_views_*`, `v_product_*`, `v_identity_daily`,
   `v_retention`) and `v_events_flat`. A table rename with views still
   pointing at the old table fails or rewrites them wrongly.
2. Build the id map:
   `CREATE TEMP TABLE project_map (alias TEXT PRIMARY KEY, id INTEGER NOT NULL)`,
   filled with `ROW_NUMBER() OVER (ORDER BY created_at, alias)`.
3. Rebuild `projects` as

   ```sql
   CREATE TABLE projects (
       id              INTEGER PRIMARY KEY AUTOINCREMENT,
       name            TEXT NOT NULL,
       identity        TEXT NOT NULL DEFAULT 'anonymous',
       allowed_origins TEXT NOT NULL DEFAULT '[]',
       attributes      TEXT NOT NULL DEFAULT '[]',
       created_at      TEXT NOT NULL DEFAULT (datetime('now')),
       archived_at     TEXT                                -- NULL = active
   );
   ```

   inserting explicit ids from `project_map`. Explicit ids into an
   AUTOINCREMENT column advance `sqlite_sequence`, so the next project
   gets `max + 1`. `alias` and `retention` are not carried over.
4. Rebuild `ingest_keys` as

   ```sql
   CREATE TABLE ingest_keys (
       key         TEXT PRIMARY KEY,
       project_id  INTEGER NOT NULL REFERENCES projects(id),
       label       TEXT NOT NULL,
       created_at  TEXT NOT NULL DEFAULT (datetime('now')),
       disabled_at TEXT,                                  -- NULL = active
       UNIQUE (project_id, label)
   );
   ```

   The unique index replaces `idx_ingest_keys_project`. The copy uses
   the same `LEFT JOIN project_map` as step 5, so a key whose project has
   no registry row aborts the migration, and a duplicate label trips
   the `UNIQUE` constraint.
5. Rebuild the 20 data tables. Each keeps its columns, types, `WITHOUT
   ROWID` and key shape, with `project TEXT NOT NULL` replaced by
   `project_id INTEGER NOT NULL` in the same position:

   - raw: `views`, `product_events` (rebuilt under its new name, `events`)
   - aggregates: `agg_views_daily`, `agg_views_paths`, `agg_views_hosts`,
     `agg_views_referrers`, `agg_views_utm`, `agg_views_countries`,
     `agg_views_displays`, `agg_views_os`, `agg_views_browsers`,
     `agg_views_app_versions`, `agg_views_devices`, `agg_product_daily`,
     `agg_product_totals`, `agg_product_attrs`, `agg_identity_daily`,
     `agg_retention`
   - people: `actors`, `identities`

   Each copy is
   `INSERT INTO x_new (project_id, …) SELECT m.id, … FROM x LEFT JOIN project_map m ON m.alias = x.project`.
   The `LEFT JOIN` is deliberate. A row whose `project` has no projects
   row yields a NULL `project_id`, which fails `NOT NULL` and aborts the
   whole migration. An inner join would drop that row silently. `views.day` is a
   generated column and is left out of the column list.
6. Recreate the indexes on `project_id`: `idx_views_actor`,
   `idx_views_project_day`, `idx_views_project_ts`, `idx_views_session`,
   `idx_events_project_name_ts`, `idx_events_project_user_ts`,
   `idx_actors_last_seen`.
7. Recreate every view from step 1 on `project_id`, identical otherwise.
   `v_events_flat` is not recreated here; the boot-time
   `RebuildFlatView` (`internal/app/app.go`) builds it with `project_id`
   among its base columns.

`schemaViews` in `internal/api/resources.go` changes in the same commit.

### 4.2 Why the id and not the name in rows

Rows keep a reference, not a label. Renaming a project touches one row,
and a query that needs the name joins `projects`. No view carries the name.

### 4.3 Renaming `product_events` to `events`

014 rebuilds the table regardless, so the rename is the name on
`CREATE TABLE … _new` and costs nothing extra. Scope is the raw table
only:

- renamed: the table, and `FROM product_events` wherever it is read —
  `aggregate_product.go`, `aggregate_views.go`, `retention.go`,
  `identities.go`, `flatview.go`, `write.go`, `registry.go`,
  `internal/pipeline`, `internal/server/handlers.go`,
  `internal/api/ops_product.go`, `ops_read.go`, `scripts/smokecheck`
- unchanged: `agg_product_daily`, `agg_product_totals`,
  `agg_product_attrs`, `v_product_daily`, `v_product_totals`,
  `v_product_attrs`, the `product_events` and `product_attributes` tools,
  and `RETENTION_PRODUCT_*`. Those name the product family, not the table
- its indexes are already called `idx_events_project_name_ts` and
  `idx_events_project_user_ts`, and now match
- `v_events_flat` reads `FROM events`, which its name finally matches

The name says what the rows are, but only these rows: views are events
too and live in `views`. If the two raw tables are ever merged into one
stream keyed by `kind`, that merged table inherits this name.

## 5. Identity hashes

`identity.VisitorHash(salt, ip, ua, project)` and
`identity.ActorHash(salt, id, project)` keep their signatures; callers pass
`strconv.FormatInt(p.ID, 10)` instead of the alias.

The salt rotates daily, so anonymous actor ids already change at
midnight. The switch therefore breaks continuity only within the deploy
day. On that day an anonymous actor seen before and after the upgrade
counts twice in that day's uniques, and a session spanning the upgrade
splits. Identified projects store `actor_id` raw and are unaffected,
except for the hashed fallback used when no client id is sent. The
release notes say so.

From then on a project's hash input never changes. That was not true
under `project rename`.

## 6. Go changes by package

### 6.1 `store` and `store/sqlite`

- Row types: `Project string` → `ProjectID int64` on the view, event,
  identity and key types; `RegistryProject` gains `ID int64` and loses
  `Alias` and `Retention`.
- Every query's `project` → `project_id`.
- `insertProject` lets SQLite assign the id and returns it. The alias
  conflict check goes; there is nothing unique to conflict on.
- `insertKey` relies on `UNIQUE (project_id, label)` and maps the
  constraint error to `store.ErrConflict`.
- `RenameProject` is deleted. `ProjectAliases` becomes `ProjectIDs`.
- The `github.com/google/uuid` import stays for event ids only.

### 6.2 `manage`

- `Project` gets `ID int64` and loses `Alias` and `Retention`. `Snapshot` is keyed by
  id: `Project(id int64)`, `ProjectByKey`, `OriginAllowed(id, origin)`.
- `ProjectSpec` loses `Alias` and `Retention`. `validate` requires a
  non-empty name. `validateNew`, `validAlias` and the legacy exemption are
  deleted.
- `CreateProject` returns the new id. `UpdateProject`, `ArchiveProject`,
  `RestoreProject`, `DeleteProject`, `IssueKey`, `SetKeyDisabled` take an
  id.
- `RenameProject`, `Import`, `Export` and `importexport.go` are deleted.
- The registry no longer carries retention. `Snapshot.RetentionFor` and
  the `defaults` it wraps are deleted.
- Clearing origins: `ProjectSpec.AllowedOrigins` on update means nil =
  keep, non-nil = replace, so a non-nil empty slice clears. Today an empty
  list counts as "not supplied". The CLI's `-clear-origins` sends an
  empty non-nil slice. On MCP/REST, JSON `[]` decodes to one and an
  omitted field decodes to nil.

### 6.3 `config`

- `RetentionOverride`, `RetentionClassOverride` and the legacy
  `web`/`app` unmarshalling are deleted.
- The legacy `projects.json` types (`Project`, `IngestKey`,
  `LegacyAggregation`, `ParseProjects`) are deleted.
- `config.Retention` (the global windows from `RETENTION_*`) is
  unchanged.

### 6.4 `server`

- The handler resolves the project from the key as today and writes
  `p.ID`.
- The event-age clamp reads the global raw window. `app` passes
  `config.Retention` into `server.New` rather than the server reading it
  off a registry snapshot.

### 6.5 `jobs`

The daily pass keeps its per-project loop (the aggregation, actor and
identity work is per project). The windows come from the global
`config.Retention` passed in by `app`, the same for every project.
`PruneAggregates` and the other prune calls take `projectID int64`.

### 6.6 `app`

It passes `cfg.Retention` to `server`, `jobs` and nowhere else. The
keyless-project warning logs the id and the name.

## 7. Surfaces

### 7.1 MCP and REST

| Tool | Change |
|---|---|
| `list_projects` | Each project is `{project_id, name, identity, archived, first_view_day, last_view_day, allowed_origins, attributes}`. `alias` and `retention` are gone |
| `create_project` | Input: `name` (required), `identity`, `allowed_origins`, `attributes`, `skip_key`. Output: `project_id`, `identity`, `key`, `snippet`, `note` |
| `update_project` | `PATCH /api/projects/{project_id}`. Adds `name`. `allowed_origins: []` clears; omitted keeps |
| `archive_project`, `restore_project` | `POST /api/projects/{project_id}/archive` and `/restore` |
| `views_overview`, `views_breakdown`, `product_events`, `product_attributes`, `retention`, `identities` | Path prefix `/api/projects/{project_id}`; MCP input `project_id` |
| `issue_ingest_key`, `disable_ingest_key`, `enable_ingest_key` | `/api/projects/{project_id}/keys[/{label}/…]`; MCP input `project_id` + `label` |
| `list_ingest_keys` | Filter and output field `project_id` |
| `integration_guide` | Input `project_id`; the heading names the project by name and id |
| `query` | Unchanged; the views it reads now have `project_id` |

`project_id` is a JSON integer, described as "project id; call
list_projects first". REST path values bind to it through `setField`
(`internal/api/rest.go`), which today converts only `reflect.Int`; it
gains `reflect.Int64` so the input structs can use the same `int64` as the
rest of the code.

The unknown-project error lists the valid choices as `id (name)` pairs,
e.g. `unknown project 7; valid projects: 1 (Blog), 2 (Shop)`, so a model
can still recover.

### 7.2 CLI

```
twillingate project create -name "Blog" [-identity …] [-origin …] [-attr …]
twillingate project update -id 1 [-name …] [-identity …] [-origin …] [-clear-origins] [-attr …]
twillingate project list
twillingate project archive|restore -id 1
twillingate project delete -id 1
twillingate key issue|disable|enable -project-id 1 -label web
twillingate key list [-project-id 1]
```

- `project create` prints the new id first, e.g. `project 1 ("Blog")
  created`, then the `key issue -project-id 1 -label web` hint.
- `project list` prints `id  identity  name  [archived]`.
- `project rename` and `config` (`import`, `export`) are gone. An unknown
  subcommand already prints usage.
- `-origin` and `-clear-origins` together are refused.

### 7.3 Evidence

- `sources/twillingate/projects.sql` selects `id, name, identity, archived`.
  The placeholder row for an empty database uses `id = 0`, which
  AUTOINCREMENT never issues, and the pages filter on `id != 0`
  instead of `alias != ''`.
- Page links become `/views/{p.id}`, `/product/{p.id}`, etc. The route
  segment keeps its name, `[project]`.
- Page queries filter `where project_id = '${params.project}'`. The
  value stays quoted: SQLite compares the text `'3'` to an INTEGER column
  as 3, and quoting keeps a URL segment from becoming SQL.
- The users and retention pages' hint becomes
  `twillingate project update -id {params.project} -identity identified`.
- Page titles and headings show the project name, looked up by id.

### 7.4 Scripts and deploy

`scripts/smoke.sh`, `scripts/test-compose.sh`,
`scripts/test-install-inner.sh`, the `install.sh` next-steps text and the
`docker-compose.yml` comment switch to `project create -name myapp` and
`-project-id 1`. On a fresh database the first project is id 1, so the
scripts can rely on it.

## 8. Documentation

Same commit, per CLAUDE.md:

- `docs/twillingate.md`
  - the project fields table: `id` replaces `alias`, and `retention` is gone
  - every CLI and tool example: `-id`, `-project-id`, `project_id`
  - the tool table and the REST route table
  - the queryable-views section: `project_id`. The three `product_events`
    mentions there are the tool, not the table, and stay
  - the origins paragraph: `-clear-origins` replaces the export/import advice
  - the `config export`/`import` paragraphs and the per-project retention example are deleted
- `docs/deployment.md`
  - the two `config` rows in the operations table are deleted
  - a new upgrade note gives the pre-upgrade checks (§9), the backup step, the hash seam, and states
    that retention is global only
- `docs/plausible/README.md` names the raw table twice (the routing
  diagram and `product_events.attributes`); both become `events`.
- `docs_sync_test` enforces the tool names, the route table and the
  views. Its fixtures move to `project_id`.

## 9. Upgrade

### 9.1 Checks before upgrading, on the live database

```sql
-- 1. Per-project retention overrides. Anything returned is data the first
--    daily pass after the upgrade will prune to the global window. Raise the
--    matching RETENTION_* variable first if that data must be kept.
SELECT alias, retention FROM projects WHERE retention IS NOT NULL;

-- 2. Rows whose project has no registry row. Any hit aborts migration 014.
--    Repeat for each table in §4.1 step 5.
SELECT DISTINCT project FROM views
WHERE project NOT IN (SELECT alias FROM projects);

-- 3. Duplicate key labels. Any hit aborts migration 014.
SELECT project, label, COUNT(*) FROM ingest_keys
GROUP BY project, label HAVING COUNT(*) > 1;
```

### 9.2 Procedure

The same as 012:
1. Stop the service and copy the database, or take a Litestream snapshot.
2. Run the installer.
3. Check `journalctl` for migration 014.
4. Check `list_projects` for ids and names, and a dashboard page per project.

Agents and scripts that stored aliases need the new ids from
`list_projects`.

## 10. Testing

- **Migration test**, next to `migration012_test.go`:
  - build a database at 013 with two projects, rows in every rebuilt table, keys and an override
  - `events` holds what `product_events` held, row for row
  - migrate to 014, then assert:
    - ids follow `created_at`
    - every row count is unchanged and every row is on the right id
    - `retention` is gone
    - the next created project gets `max + 1`, and a deleted project's id is not reissued
  - an orphan row aborts the migration and leaves the database at 013
  - a duplicate label aborts the migration and leaves the database at 013
- **Store**:
  - the `(project_id, label)` conflict maps to `ErrConflict`
  - `insertProject` returns increasing ids
- **Manage**:
  - create requires a name
  - update clears origins only when asked
  - unknown ids return `ErrNotFound`
- **API**:
  - the tool and route inputs use `project_id`
  - the unknown-project error lists `id (name)`
  - `update_project` with `[]` versus an omitted origins field
- **Jobs**: pruning uses the global windows for every project.
- **Server**: the hash input is the id; the clamp uses the global raw window.
- **Evidence**: the site builds against a migrated local database.
- `make check`, including the architecture test and `docs_sync_test`.

## 11. Release

One commit, `feat!: identify projects by integer id instead of alias`,
with a `BREAKING CHANGE:` footer listing:
- the changed tool inputs, outputs and routes
- the removed `config` subcommand and `project rename`
- global-only retention
- the one-day hash seam
- `product_events` renamed to `events`, which changes saved SQL
