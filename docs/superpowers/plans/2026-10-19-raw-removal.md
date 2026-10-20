# The Removal Implementation Plan (PR 6)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Events live only in the event files. SQLite holds metadata, rules, forms and dashboards, nothing else. A fresh install never creates an `events` table, and every tool, page and pass that read SQLite's events now reads the files or is gone.

**Architecture:**
- **Ingest writes one sink.** The pipeline's buffer writes the archive log alone, and `pipeline.Sinks` goes. A form submission's event goes from its outbox (`submissions.unlogged_event`) to the log, never into `events`.
- **The SQLite-only tools move to the files or go.** `project_activity`, `event_names`, `list_attributes`, `usage` and `limits` read the files through the reader or the file names. `cap_usage` is removed, and the `query` tool's `sqlite` source is refused.
- **Rules check the files.** Renaming onto a name that has events, or a key that has values, asks the files through a `manage.Presence` that `internal/app` implements over the reader. SQLite keeps only the `rules` rows, which the reader already applies on read.
- **Declared breakdowns go**, with `project_attributes`, the projects' `attributes` field, the budget and the CLI's `-attr`.
- **Migration 042 drops the event schema.** That is `events`, `actors`, `identities`, every `agg_*`, the `raw_*` and `v_*` views, and `usage_attributes`. A pre-check refuses it while SQLite holds raw rows the import has not copied, and `twillingate events import` migrates only through 041.
- **The daily pass keeps:** shares, drafts, the purge, the orphan sweep, the incremental vacuum, `usage_history` (the database's size and each project's file size) and the archive's maintenance.
- **Removed variables are reported at boot and ignored for one release.**

**Tech Stack:** Go 1.25, modernc SQLite (metadata only), DuckDB through `internal/engine`, `internal/archive` (log, files, reader), React/TypeScript in `web/`, Python for `scripts/seed-demo.py`.

**Spec:** `docs/superpowers/specs/2026-10-12-raw-first-design.md`:
- D11, what goes away;
- D12, superseded work;
- D20, the allowance;
- D23, configuration;
- D19, the migration (amended by 5c: the import is a command).

Roadmap: `docs/superpowers/plans/2026-10-12-raw-first-roadmap.md`, PR 6. This plan branches from `main` after 5d (#188) and #190.

**Timing:** implement after prod has run v0.20.0 for 1–2 weeks. Migration 042 cannot be undone, and until it runs a downgrade to v0.20 still finds its data.

## Facts this plan rests on (read 2026-10-19 on main, 6ac6562)

- **Ingest's sink.** `app.go:145-160` builds `pipeline.Sinks{st, archiveLog}` when the process ingests, else `st`. `jobs.WithAttribution(files, archiveLog)` (`app.go:177`) gets the same log.
- **Forms.** `forms.go:AttributeSubmission` writes the event into `events` through `insertEvents` and `writeReceived`, and also into `submissions.unlogged_event`, the outbox that `jobs.flushOutbox` drains to the log. The outbox needs no `events` row.
  - `DeleteSubmissions` and `deleteFormData` (reached from `PurgeArchived`) delete the `$form_submit` row from `events`.
  - The event carries the form's name, the actor and the attribution, never the fields (`server/forms.go:414-420`).
- **Store methods that exist only for events** (`store.go:296-338`): `WriteEvents`, `UpsertIdentities` (no production caller), `ViewDaysBefore`, `ProductDaysBefore`, `MeasureDaysBefore`, `RawEvents`, `OldestRawDays`, `AggregateViewDay`, `AggregateProductDay`, `AggregateMeasureDay`, `DropBlockedAggregates`, `UpsertActors`, `AggregateRetentionDay`, `PruneActors`, `AggregateIdentityDay`, `PruneIdentities`, `PruneAggregates` and `RebuildFlatView`.
  - Files holding nothing else: `write.go`'s event half, `aggregate_views.go`, `aggregate_product.go`, `aggregate_measures.go`, `retention.go`, `identities.go`, `prune.go`'s table list, `raw_events.go`, `flatview.go`, `usage_attributes.go` and `migration015.go`'s helpers. Migration 015's step stays registered: old databases still run it.
- **The final schema after 041.**
  - Event tables: `events`, `actors`, `identities`.
  - 21 `agg_*` tables:
    - `agg_views_{daily,paths,hosts,referrers,utm,countries,platforms,os,browsers,app_versions,devices,displays,consent,locales}`;
    - `agg_product_{daily,totals,attrs}`;
    - `agg_measures_{daily,attrs}`;
    - `agg_retention`, `agg_identity_daily`.
  - Views: `raw_views`, `raw_product`, `raw_measures`, the 14 `v_views_*`, `v_product_daily`, `v_product_totals`, `v_product_attrs`, `v_measures_daily`, `v_measures_attrs`, `v_identity_daily`, `v_retention`, and the process-built `v_events_flat`.
  - Catalog and stats: `usage_attributes`, `project_attributes`, and `usage_history` (keys in `store.go:168-198`).
  - `meta` holds the caps as `attributes_top_n`, `attribute_breakdowns_max` and `identities_top_n`, written at every start (`app.go:110-121`).
  - No trigger touches event data.
- **The migrator** (`store/sqlite/migrate.go`):
  - `migrateThrough(ctx, maxVersion)` exists, unexported.
  - `dataSteps[v]` run after a migration's SQL, in its transaction. Nothing runs before the SQL.
  - `processOwned` (`v_events_flat`) is dropped before every pending migration. An upgrade from before 020 still needs that drop, because migrations up to 041 rebuild `events` under the view.
- **The import** (`app/import.go:ImportEvents`) runs `app.Migrate`, all of it, before reading. With 042 in the tree it would drop `events` before reading it.
- **What reads SQLite's events at request time:**
  - `ops_activity.go` (MAX(day) over `raw_*` and `agg_*`);
  - `ops_event_rules.go:eventNames` (`v_product_daily`, `v_measures_daily`);
  - `ops_attributes.go` (`usage_attributes`);
  - `ops_usage.go` (`raw_*`, `agg_*`, `v_*`, `usage_history`);
  - `ops_limits.go:capUsage` (`v_views_*`, `v_*_attrs`, `v_identity_daily`);
  - `rawcount.go` (`COUNT(*) FROM v_events_flat`);
  - `ops_query.go` with `source: "sqlite"`;
  - `resources.go:schemaSQLite`.
- **File-side helpers that exist:** `archive.LatestDays(dir)`, `archive.OldestDays(dir)`, `archive.ListFiles(dir)`, `(*archive.Reader).Query` and `QueryScoped`, and the api's `h.filesTable`, `h.runFiles` and `h.measureAttr` (the DESCRIBE lookup of an `attr_` column).
  - There is no Parquet-footer counter and no per-project size helper.
  - `host` has no events directory; `cfg.EventsDir` reaches `api.Build`.
- **What the web app reads:**
  - `rewritten` on rule writes is typed, never read.
  - `unused_attributes` is typed, never read.
  - `parseSchemaViews` stops at the "Deprecated: query with source sqlite" line, so dropping that half needs no web change.
- **Seeding writes SQLite.**
  - `scripts/seed-demo.py` INSERTs into `events` and `identities`, and `web/e2e/serve.sh` then runs `events import`.
  - `scripts/smokecheck` counts `raw_views` and `raw_product`.
  - A closed log (`raw/<project>/<day>.<n>.closed`, NDJSON in `archive.line`'s shape, `"v":1`) is dumped by the next pass. That is how `WriteClosed` gets rows in without ingest.
- **Docs bound by tests:**
  - `TestDocumentNamesEveryTool` spells "sixty-five tools".
  - `TestDocumentMatchesRoutes` binds the HTTP API tables.
  - `TestDeploymentDocumentsEveryEnvVar` binds config's `e.num("X")` names to `docs/deployment.md`'s table.
  - `TestDocumentMatchesVocabularies` reads migration 015's pre-check in `deploy/UPGRADES.md`, so that section stays.

## Global Constraints

- **One migration, 042, and it is the only schema change.** It cannot be undone. `deploy/UPGRADES.md` says so, says to run `twillingate events import` first, and says to `VACUUM` once afterwards.
- **No SQLite fallback anywhere.** Nothing reads `events` after this PR but `events import` (through 041) and migration 042's pre-check.
- **Layering** (CLAUDE.md):
  - `manage` declares `Presence` and `app` implements it.
  - `internal/archive` and `internal/shared/enginesql` stay leaves.
  - No surface imports another.
- **Refusals are typed:** `manage.ErrInvalid` for a removed `source`; `ErrConflict` for a taken rename target, as today.
- **Docs in the same commit as their change** (CLAUDE.md's table):
  - `docs/twillingate.md`: tools, routes, `schema://views`, attributes, rules;
  - `docs/reporting.md`: Settings and the dialect section;
  - `docs/deployment.md`: variables, routine operations, backup checks;
  - `docs/litestream.md`: backup checks and restore;
  - `deploy/UPGRADES.md`.
- **Commits and checks.**
  - Conventional Commits with CLAUDE.md's scopes.
  - PR title: `feat!: keep events only in the event files`.
  - Run `make check` before every push, and `cd web && npm run typecheck && npm test` after every web change.
  - Run the e2e suite (`cd web && npm run e2e`) before the last push, and `make test` (race) once at the end: the per-package budget is 600 s.
- **Toolchain.** Run `export PATH=$PATH:/usr/local/go/bin GOTMPDIR=$HOME/.cache/gotmp` first.

## Decisions this plan takes (review them)

1. **Migration 042 refuses while un-imported raw rows remain.**
   - A pre-check runs before the migration's SQL. When `events` has a row and `meta` has no `events_imported`, it returns an error and nothing is dropped:

     ```
     migration 042 drops the events table, which still holds raw events the event files may lack: stop the service, run `twillingate events import`, then start again
     ```

   - An install since v0.18 wrote both sides. Its import finds every id held and writes nothing (`ImportDay` skips held ids), so asking it to run the import costs seconds.
   - There is no flag to skip the check: an operator who wants the rows gone runs the import anyway.
2. **`events import` stays for one release** and migrates only through 041 (`sqlite.LastEventsVersion = 41`). It then imports and sets `events_imported`; the next start runs 042. On a database already past 041 it prints "the events table was dropped by migration 042; nothing to import" and exits 0. Its reads (`RawEvents`, the `*DaysBefore`) move off `store.Store` onto `*sqlite.DB` in `legacy_events.go`.
3. **`limits` reports the events of the last 30 days** (D20) as `events_30d {count, from, to}`, which replaces `raw_events {held, window_days}`.
   - The count is one engine query, `SELECT count(*) FROM events WHERE day >= $from`, kept in `rawcount.go`'s 5-minute cache (renamed `eventCount`). It does not sum the Parquet footers that D20 names: DuckDB answers a `day` filter from row-group statistics and the open logs are part of the `events` view, so one query covers both at the same cost and needs no new reader code.
   - The retention group keeps `RETENTION_DAYS`, `RETENTION_ARCHIVED_DAYS` and `RETENTION_FORMS_DRAFT_DAYS`.
   - The `caps` group goes.
   - `INGEST_LATE_DAYS`'s description loses "a day already rolled up".
4. **`cap_usage` is removed:** the tool, the route, the web section and its tests. That leaves sixty-four tools. Nothing is capped, so it has nothing to report.
5. **`list_attributes` reads the files.**
   - Range: default the last 30 days, at most `productAttributesMaxDays` (92), refused past it as `product_attributes` is.
   - Per `attr_*` key, over product and measures rows of the range: `events` (rows with the key) and `values` (distinct values). `values` replaces `max_values`.
   - It keeps `key`, `received`, `blocked`, `aliases` and `keys_total`, with keys known only from rules listed with `received: false`, and the top 500 by events.
   - It drops `breakdown`, `breakdowns_used`, `breakdowns_max` and `values_cap`.
6. **`usage` reads the files.**
   - `series` (views, events, measures per day), `totals`, `first_day` (`archive.OldestDays`) and `last_received_at` (`max(received_at)` of the newest day) come from one engine query over the range.
   - `size` is `{total_bytes, measured_at}`: the project's file bytes as the daily pass last measured them (`usage_history` key `file_bytes`, written from `archive.Sizes`). `series[].total_bytes` reads the same key per day.
   - Removed: `raw_days`, `rolled_up_days`, `unused_attributes`, `size.raw_bytes`, `size.aggregate_bytes`, `declared_attributes`, `attribute_keys`, `attribute_values` and `attribute_values_folded`.
   - `database_bytes` and `database_series` stay.
7. **Rename and merge check the files.**
   - `manage.Ops.Presence` answers whether `to` has events (or `attr_<to>` has a value) in the project, rules applied, over the whole history. A rename onto a name another rule already maps there is therefore a merge.
   - The store's `RenameEvent` and `RenameAttribute` write rules only and return `error`. The outputs lose `rewritten`.
   - A nil `Presence` (a process with no engine) skips the check; `app` always sets it where the console runs.
8. **Breakdowns go entirely.**
   - The projects' `attributes` field is gone from create, update and list, over MCP, REST and the CLI (`-attr`), as are `project_attributes` and `ATTRIBUTE_BREAKDOWNS_MAX`.
   - A request that still sends `attributes` gets what the decoder does with any unknown field.
   - `docs/twillingate.md` and UPGRADES say every key breaks down already (D4).
9. **`query`'s `source`:** `""` and `"files"` run on the files. `"sqlite"` is refused with `ErrInvalid`:

   ```
   source "sqlite" was removed: the database no longer holds events; query the event files (docs://twillingate)
   ```

   The field stays for one release, so a client that sends `"files"` keeps working. `schemaSQLite` and `CustomSQLRefused` go.
10. **Removed variables** are listed in `config.Removed` and logged at boot, one warning each:

    ```
    <NAME> is no longer used: events are kept in the event files for RETENTION_DAYS, and nothing is capped
    ```

    - The variables are `RETENTION_RAW_DAYS`, `RETENTION_AGGREGATE_DAYS`, `RETENTION_EVENTS_RAW_DAYS`, `RETENTION_EVENTS_AGGREGATE_DAYS`, `ATTRIBUTE_VALUES_TOP_N`, `ATTRIBUTE_BREAKDOWNS_MAX` and `IDENTITIES_TOP_N`.
    - The two `RETENTION_EVENTS_*` names leave `renamed`.
    - `INGEST_LATE_DAYS` must be at most `RETENTION_DAYS`.
11. **Deleting a submission leaves its `$form_submit` event in the files.**
    - The event holds no field. The count of conversions stays, which is what the files already did since PR 3.
    - Erasing an actor from the files is D6's rewrite, a separate change.
    - The docs stop saying the event is deleted.
12. **Tests whose subject is dropped are deleted:**
    - `reporting/parity_test.go`, `parity_race_test.go` and `testdata/system-sqlite/`;
    - `app/archive_parity_test.go`;
    - the store's aggregate, retention, identities, prune, raw-events, flat-view, caps and usage-attribute tests;
    - migration tests (`migration0NN_test.go`) whose assertions read only dropped objects.

    A migration test that also asserts kept tables keeps those assertions and seeds with SQL `INSERT`s instead of `WriteEvents`.
13. **`processOwned` keeps `v_events_flat`'s name only so the migrator drops it before the old migrations.** Nothing builds it any more: `RebuildFlatView`, `flatview.go` and the app's rebuild go. Migration 042 does not name it; the migrator has already dropped it.
14. **The widget dialect hint (`reporting/engine.go:sqliteHint`) stays.** Users' widgets written for SQLite are still out there.
15. **`seed-demo.py` writes closed logs.** It writes `raw/<project>/<day>.1.closed`, one NDJSON line per event in `archive.line`'s shape, with `user_name` and `group_name` on the events in place of the `identities` rows. The first pass dumps them, so seeding needs no `events import`.

## Review Focus

1. **An upgrade with un-imported events.** Migration 042 refuses, the error names the command, and nothing is dropped. Then `events import` migrates through 041, imports and marks, and the next start drops. Pinned by Task 7's `TestMigration042RefusesUnimportedEvents`, `TestMigration042DropsAfterTheImport` and `TestImportEventsStopsBefore042`.
2. **An upgrade from a database before 020, straight to 042.** `v_events_flat` exists and must be dropped before each old migration, and nothing may rebuild it after. Pinned by Task 7's `TestMigrateFromSnapshot19ThroughHead`, which uses `snapshotAt(19)` with a seeded view row.
3. **A split install.**
   - The console process has no archive log, and the ingest process's pass writes `file_bytes`.
   - A pass with no archive records `database_bytes` alone.
   - `usage` with no `file_bytes` yet returns `size: null`, not zero.

   Pinned by Task 4's `TestUsageSizeIsNullUntilMeasured` (kept, re-seeded) and Task 7's `TestPassWithoutArchiveRecordsTheDatabaseOnly`.
4. **Renaming onto a name that only a rule makes exist.** `a` was renamed to `b`; `rename c → b` must be a conflict (`b` reads as having events), and `merge c → b` must work. Pinned by Task 5's `TestRenameOntoARuledNameIsAConflict`.
5. **A removed variable set in an old env file.** The boot continues with one warning per name, `RETENTION_EVENTS_RAW_DAYS` included, and `INGEST_LATE_DAYS=45` with `RETENTION_DAYS=30` is refused. Pinned by Task 8's `TestRemovedVariablesAreReportedNotRefused` and `TestLateDaysWithinRetentionDays`.

---

### Task 1: Seed and smoke-check through the event files

**Files:**
- Modify: `scripts/seed-demo.py` (write closed logs; no `events`/`actors`/`identities` SQL)
- Modify: `web/e2e/serve.sh` (drop `events import`; `EVENTS_DIR` passed to the seed)
- Modify: `Makefile` (`seed-demo`: seed into `raw/` beside the database, then say the first start dumps it)
- Modify: `scripts/smokecheck/main.go` (count the families from `raw/*/*.log` lines), `scripts/smoke.sh` (pass the events directory)
- Test: `internal/archive/seed_format_test.go`

**Interfaces:**
- `seed-demo.py <db> [--events-dir DIR]`. DIR defaults to `raw/` beside the db. It still writes the SQLite rows it wrote that are not events (projects' metadata, if any), and nothing else.
- Each line is `archive.line`'s map, sorted keys optional:
  - `v`, `id`, `project_id`, `family`, `event_name`, `ts` and `day` (`YYYY-MM-DDTHH:MM:SSZ`, `YYYY-MM-DD`);
  - the non-empty string fields, `display_width`/`display_height`, `consent` 1/0, `value`, `measure`, `sample_rate` (default 1);
  - attributes as `attr_<key>`, `$`-keys left out.
- `smokecheck <events-dir>` prints `views=<n> events=<n>`, counting `family` `views` and `product` over every `*.log` and `*.closed` under the directory.

- [ ] **Step 1: Failing test.** `TestSeedDemoLinesDumpLikeIngestLines`:
  - runs `python3 scripts/seed-demo.py` with a temp db and events dir (skipped when `python3` is missing);
  - opens an `archivetest` archive on that dir and runs `Maintain` for today;
  - asserts that the reader's `SELECT family, count(*) FROM events GROUP BY ALL` has views, product and measures rows, and that `SELECT count(*) FROM identities WHERE name <> ''` is above 0.
- [ ] **Step 2: Run it.** `go test ./internal/archive -run SeedDemo` fails: the seed writes SQLite.
- [ ] **Step 3: Rewrite the seed's writers to emit lines,** grouped by project and day, one `<day>.1.closed` file each. Write `<day>.import` and rename it, as `WriteClosed` does. `serve.sh` drops `events import`; the boot pass dumps the logs before its wait loop ends.
- [ ] **Step 4: Rewrite `smokecheck`;** `make smoke` prints the same counts as before.
- [ ] **Step 5: Run the test, then `cd web && npm run e2e`.** Both are green, and the dashboards show the seeded data.
- [ ] **Step 6: Commit** `test: seed the demo and the end-to-end suite into the event files`

### Task 2: Ingest and form submissions stop writing SQLite events

**Files:**
- Modify: `internal/pipeline/pipeline.go` (remove `Sinks`), `internal/app/app.go:145-160` (the sink is the archive log; a process that does not ingest builds no buffer)
- Modify: `internal/store/sqlite/forms.go`:
  - `AttributeSubmission` writes the attribution and `unlogged_event` only;
  - remove `deleteEvent` and its calls in `DeleteSubmissions` and `deleteFormData`.
- Modify: `internal/jobs/attribution.go` (comments: the event goes to the log only)
- Docs: `docs/twillingate.md` (forms: "A submission and its event", `delete_submissions`: the event stays in the files and holds no field)
- Test: `internal/pipeline/pipeline_test.go`, `internal/store/sqlite/forms_test.go`, `internal/jobs/attribution_test.go`, `internal/app/app_test.go`

**Interfaces:**
- `pipeline.New(cfg, sink Sink, logger)` is unchanged; `app` passes `*archive.Log`.
- `store.Store.AttributeSubmission(ctx, p, ev) (wrote bool, err error)` keeps its signature. `wrote` now means the outbox was filled.

- [ ] **Step 1: Failing tests.**
  - `TestAttributeSubmissionWritesNoEventRow`: after `AttributeSubmission`, `SELECT count(*) FROM events` is 0 and `UnloggedEvents` returns the event.
  - `TestDeleteSubmissionsLeavesTheFiles`: delete returns 1, the submission is gone, and the archive log line written earlier is still there.
  - `TestIngestWritesTheLogOnly` (app): a served batch lands in `raw/` and `events` stays empty.
- [ ] **Step 2: Run them; they fail.**
- [ ] **Step 3: Implement;** delete the tests of `Sinks` and of the SQLite event row.
- [ ] **Step 4: Run `make check`.**
- [ ] **Step 5: Commit** `feat!: write ingested and attributed events only to the event files`

### Task 3: Activity, event names, limits and query on the files; cap_usage goes

**Files:**
- Modify: `internal/api/ops_activity.go` (`last_event_day` from `archive.LatestDays(h.eventsDir)`)
- Modify: `internal/api/ops_event_rules.go:eventNames` (files)
- Modify: `internal/api/ops_limits.go` (limits per Decision 3; remove `capUsage`, `capViews`, `summarize`, `daysOf`, `capOf` and the caps constants)
- Modify: `internal/api/rawcount.go` (rename to `eventcount.go`; count over the files)
- Modify: `internal/api/ops_query.go` (Decision 9; `filesError`'s hint points at docs://twillingate, not `source "sqlite"`)
- Modify: `internal/api/resources.go` (`schemaViews = schemaFiles`), `internal/api/server.go` (remove `CustomSQLRefused`; `host` gains `eventsDir`, `queryTimeout` and `queryRows` from config, so the files no longer borrow `h.db`'s), `internal/api/ops_read.go` (no `cap_usage` spec; `query`'s description)
- Modify: `web/src/lib/api.ts`, `queries.ts` (no `capUsage`; `LimitsResponse.events_30d`), `web/src/components/projects/LimitsPanel.tsx` ("Events in the last 30 days: N"; no caps group), `SetupTab.tsx` (no `CapImpactSection`; no `rawDays`), delete `CapImpactSection.tsx` and its test, `EventsSection.tsx`/`AttributesSection.tsx`/`rule-copy.ts` (block copy without `rawDays`: "hidden from every report at once, past events included; new ones are not stored")
- Modify: `web/e2e/projects.spec.ts` (no "Cap impact")
- Docs: `docs/twillingate.md` ("sixty-four tools", the tool table, HTTP API rows, `query`, Resources), `docs/reporting.md` (refusal rows naming `source "sqlite"`)
- Test: `ops_activity_test.go`, `ops_event_rules_test.go`, `ops_limits_test.go`, `eventcount_test.go`, `ops_query_test.go`, `ops_query_files_test.go`, `resources_test.go`, `docs_sync_test.go`, `openapi_test.go`, web unit tests

**Interfaces:**
- `limitsOut{Limits, Events30d *events30dOut "events_30d,omitempty", IngestDisabled}`, where `events30dOut{Count int64 "count", From string "from", To string "to"}`; `From` is today − 29, UTC.
- `eventNamesOut` keeps its shape. Its SQL is

  ```sql
  SELECT family, event_name, count(*) FROM events WHERE family IN ('product','measures') AND project_id = :project AND day BETWEEN :from AND :to AND event_name NOT LIKE '$%' GROUP BY ALL ORDER BY 3 DESC, 2 LIMIT 200
  ```

  It runs through `h.filesTable`, over the same default range as today.
- `projectActivityOut` keeps its shape. A month file counts as its last day (`LatestDays`); the docs say "the newest day file".

- [ ] **Step 1: Failing tests.** Seed files through `archivetest` (as `seed_files_test.go` does).
  - `TestProjectActivityReadsTheFiles`
  - `TestEventNamesReadTheFiles`, with a blocked and a renamed name read as the rules say
  - `TestLimitsCountsTheLast30Days`: an event 31 days old is not counted
  - `TestLimitsHasNoCaps`
  - `TestQuerySourceSQLiteIsRefused`
  - `TestSchemaViewsIsTheFilesOnly`
  - `TestCapUsageIsGone`: no tool, and the route is 404
- [ ] **Step 2: Run them; they fail.**
- [ ] **Step 3: Implement the Go side.** Delete `TestCapUsage*`, `TestRawEventsCountReadsTheFlatView` and the sqlite-source query tests.
- [ ] **Step 4: Web,** then `npm run typecheck && npm test`.
- [ ] **Step 5: Docs; `docs_sync_test` passes. Run `make check`.**
- [ ] **Step 6: Commit** `feat(api)!: read activity, event names and limits from the event files; remove cap_usage and the sqlite query source`

### Task 4: Usage and attributes on the files

**Files:**
- Create: `internal/archive/sizes.go`: `func Sizes(dir string) (map[int64]int64, error)`, the bytes of every file `ListFiles` lists per project (Parquet, logs, derived files)
- Modify: `internal/shared/enginesql/`, adding `columns.go`: `func AttrColumn(cols []string, key string) (string, bool)`, measureAttr's matching (NUL as `\u0000`, case-insensitive, quoted). `ops_measures.go:measureAttr` uses it.
- Modify: `internal/store/store.go`:
  - the `Stat*` constants become `StatDatabaseBytes` and `StatFileBytes = "file_bytes"`;
  - `RecordUsageHistory(ctx, now time.Time, fileBytes map[int64]int64) error`.
- Modify: `internal/store/sqlite/usage_history.go` (database bytes, plus `file_bytes` per project when the map is non-nil)
- Modify: `internal/jobs/jobs.go` (`Archive` gains `Sizes() (map[int64]int64, error)`; the pass passes them, or nil without an archive), `internal/archive/maintain.go` (`(*Archive).Sizes`)
- Modify: `internal/api/ops_usage.go` (Decision 6), `internal/api/ops_attributes.go` (Decision 5)
- Modify: `web/src/lib/api.ts` (`ProjectUsage`, `UsageDay`, the attributes types), `UsageSection.tsx` (no `AttributesTile`; size tile `total_bytes`; "Days kept" → "First day"), `ProjectCard.tsx`, `SizeChart.tsx`, `breakdowns.ts:describeKey` (`values`), `AttributesSection.tsx` (columns from the new fields; the breakdown column stays until Task 6)
- Docs: `docs/twillingate.md` (`usage` and `list_attributes` rows, HTTP API fields), `docs/reporting.md` (the Settings line)
- Test: `internal/archive/sizes_test.go`, `internal/shared/enginesql/columns_test.go`, `ops_usage_test.go`, `ops_attributes_test.go`, `usage_history_test.go`, `jobs_test.go`, web unit tests

**Interfaces:**
- `usageOut` keeps `From`, `To`, `DatabaseBytes`, `DatabaseSeries` and `Projects`.
- `projectUsage{ProjectID, Series []usageDay, Totals, LastReceivedAt *string, FirstDay *string, Size *usageSize}`.
- `usageDay{Day, Views, Events, Measures, TotalBytes *int64}`; `usageSize{TotalBytes int64 "total_bytes", MeasuredAt string "measured_at"}`.
- `listAttributesOut{ProjectID, From, To, Attributes []attributeOut, KeysTotal}`; `attributeOut{Key, Events int64, Values int64 "values", Received, Blocked, Aliases}`.
- The series query, over the range (all projects, or `project_id = :project` when given):

  ```sql
  SELECT project_id, day, family, count(*) FROM events WHERE day BETWEEN :from AND :to GROUP BY ALL
  ```

- The attributes query: unpivot `COLUMNS('^attr_')` of `events` where `family IN ('product','measures')`, as `productAttributes` does. Then `GROUP BY key` for `count(*)` and `count(DISTINCT value)`, ordered by events descending, key, `LIMIT 500`. `keys_total` is the same without the limit.

- [ ] **Step 1: Failing tests.**
  - `TestSizesSumsEveryListedFile`
  - `TestAttrColumnMatchesLikeTheFiles` (`Plan` matches `attr_plan`; a NUL; a `"`)
  - `TestUsageReadsTheFiles`
  - `TestUsageSizeIsNullUntilMeasured` (rewritten for `file_bytes`)
  - `TestUsageSeriesCarriesTheFileBytes`
  - `TestListAttributesReadsTheFiles` (counts, distinct values, a blocked key with no rows listed `received: false`)
  - `TestListAttributesRefusesPast92Days`
  - `TestRecordUsageHistoryWritesFileBytes`
  - `TestPassRecordsTheArchiveSizes`
- [ ] **Step 2: Run them; they fail.**
- [ ] **Step 3: Implement.** Delete the usage tests on raw/aggregate days, attribute stats and unused attributes.
- [ ] **Step 4: Web,** then `npm run typecheck && npm test`.
- [ ] **Step 5: Docs, then `make check`.**
- [ ] **Step 6: Commit** `feat(api)!: answer usage and attributes from the event files`

### Task 5: Rules check the files and write only rules

**Files:**
- Modify: `internal/manage/store.go`:
  - `RenameEvent(ctx, projectID int64, family store.Family, from, to string, a store.AuditEntry) error`;
  - `RenameAttribute(ctx, projectID int64, from, to string, a store.AuditEntry) error`.
- Create: `internal/manage/presence.go`
- Modify: `internal/manage/event_rules.go`, `attribute_rules.go` (the check moves here; return values lose the counts)
- Modify: `internal/store/sqlite/event_rules.go`:
  - remove `hasEventRows`, `aggregateMoves`, `renameAggregates`, the `UPDATE events`, `dropBlockedAggregates*`, `hasAttributeBlocks`, `DropBlockedAggregates` and `dropBlockedAttributeRows`;
  - keep the blocked-target conflict and the chain collapse.
- Modify: `internal/store/sqlite/attribute_rules.go`: remove the `events` rewrite, `attributeMoves`, the `usage_attributes` checks and `seen.forget`. `renameCatalog` stays until Task 6.
- Create: `internal/app/presence.go` (`filePresence` over `*archive.Reader` and the registry's rules), wired in `app.go`
- Modify: `internal/api/ops_event_rules.go`, `ops_attribute_rules.go` (outputs `{rule}`), `web/src/lib/api.ts` (types)
- Docs: `docs/twillingate.md` (Event rules, Attribute rules: a rename checks the event files; nothing is rewritten; no aggregates)
- Test: `internal/manage/event_rules_test.go`, `attribute_rules_test.go`, `internal/app/presence_test.go`, `internal/store/sqlite/event_rules_test.go`, `attribute_rules_test.go`, `internal/api/ops_*_rules_test.go`

**Interfaces:**
- Produces, in `manage`:

  ```go
  // Presence answers from the event files, rules applied.
  type Presence interface {
  	HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error)
  	HasAttribute(ctx context.Context, projectID int64, key string) (bool, error)
  }
  ```

  `Ops` gains `Presence Presence`, set by `app` like `BreakdownsMax`.
- `Ops.RenameEvent`/`MergeEvent`/`RenameAttribute`/`MergeAttribute` return `(store.EventRule, error)` / `(store.AttributeRule, error)`. A rename whose target `HasEvents`/`HasAttribute` is `ErrConflict` with today's message.
- `filePresence.HasEvents`:

  ```sql
  SELECT 1 FROM events WHERE project_id = $p AND family = $f AND event_name = $n LIMIT 1
  ```

  `HasAttribute` first DESCRIBEs `events` and resolves the column with `enginesql.AttrColumn`. No column means false; otherwise it runs `SELECT 1 FROM events WHERE project_id = $p AND <col> IS NOT NULL LIMIT 1`.

- [ ] **Step 1: Failing tests.**
  - `TestRenameOntoARuledNameIsAConflict` (manage, with a fake `Presence` that answers as the rules read)
  - `TestFilePresenceAppliesRules` (app, on `archivetest` files: `b` has events after `a → b`)
  - `TestFilePresenceAttributeColumnAbsent`
  - `TestRenameEventWritesOnlyRules` (store: no `events` statement runs)
  - `TestRuleOutputsHaveNoRewritten` (api)
- [ ] **Step 2: Run them; they fail.**
- [ ] **Step 3: Implement.** Delete the store tests of rewritten rows and aggregates.
- [ ] **Step 4: Docs, then `make check`.**
- [ ] **Step 5: Commit** `feat(manage)!: check renames against the event files and keep only the rules`

### Task 6: Declared breakdowns go

**Files:**
- Modify: `internal/manage/ops.go` (remove `BreakdownsMax`, the spec's `Attributes`, `checkBreakdowns`), `attribute_rules.go` (remove `checkRuledBreakdowns`), `registry.go` (remove `Project.Attributes`, `AttributesFor`, `BreakdownsInUse`, `DeclaredAttributeKeys`; keep `RuleLists`), and every `rebuildView`/`rebuildFlatView` path in `ops.go`
- Modify: `internal/store/store.go` (`RegistryProject.Attributes`, `KeepBreakdowns`), `internal/store/sqlite/registry.go` (`syncBreakdowns`, the `project_attributes` read in `LoadRegistry`), `attribute_rules.go` (`renameCatalog`), `registry.go:projectTables` (drop `project_attributes`)
- Modify: `internal/api/ops_manage.go`, `ops_read.go` (no `attributes` in project input or output), `internal/reporting/dev.go:400-425`, `cmd/twillingate/project.go` (no `-attr`; no `BreakdownsMax`), `internal/app/app.go:128`
- Modify: `web/src/components/projects/AttributesSection.tsx` (no switch, budget, remove dialog), `breakdowns.ts` (rename to `attributes.ts`; `describeKey` only), `ProjectFormDialog.tsx`, `SetupTab.tsx` (no `onSaveBreakdowns`), `web/src/hooks/use-project-actions.ts` (invalidations), `web/e2e/projects.spec.ts` (remove the breakdown test), `attribute-rules.spec.ts` (no breakdown steps), `cursor.spec.ts` (the blocked row's rules menu, not a switch), `serve.sh` (no `-attr`)
- Docs: `docs/twillingate.md` ("Attribute breakdowns" becomes "Attributes": every key breaks down in the files; `list_projects`; `create_project`/`update_project` fields; CLI table), `docs/reporting.md` (Settings line)
- Test: `internal/manage/*_test.go`, `internal/store/sqlite/registry_test.go`, `internal/api/ops_manage_test.go`, `cmd/twillingate/project_test.go`, web unit and e2e tests

- [ ] **Step 1: Failing tests.**
  - `TestCreateProjectHasNoAttributes`: the JSON output has no `attributes` key.
  - `TestProjectCLIHasNoAttrFlag`: `-attr` is an unknown flag.
- [ ] **Step 2: Remove the field top to bottom** (until Task 7 deletes them, `app.migrate` and the daily pass call `RebuildFlatView(ctx, nil)` and roll up with no declared keys) until `go build ./...` and `go vet ./...` pass. Delete `TestRuledBreakdownsAreRefused`, `TestBlockingABreakdownRemovesIt`, `TestMergingBreakdownsFreesBudget`, `TestUpdateWithoutAttributesLeavesTheCatalog` and the breakdown budget tests.
- [ ] **Step 3: Web,** then `npm run typecheck && npm test`.
- [ ] **Step 4: Docs, then `make check`.**
- [ ] **Step 5: Commit** `feat!: remove declared attribute breakdowns`

### Task 7: The daily pass and the store lose events; migration 042

**Files:**
- Create: `internal/store/sqlite/migrations/042_events_removed.sql`, `internal/store/sqlite/legacy_events.go`, `internal/store/sqlite/migration042_test.go`
- Modify: `internal/store/sqlite/migrate.go` (`preChecks map[int]func(ctx, *sql.Conn) error`, run before a migration's transaction; the 042 check; exported `MigrateThrough(ctx, v int) error`; `const LastEventsVersion = 41`; `processOwned`'s comment per Decision 13)
- Delete: `aggregate_views.go`, `aggregate_product.go`, `aggregate_measures.go`, `retention.go`, `identities.go`, `raw_events.go` (its reads move to `legacy_events.go`), `flatview.go`, `usage_attributes.go`, and their tests; `write.go`'s `WriteEvents`, `insertEvents`, `UpsertIdentities`; `prune.go`'s `PruneAggregates`; `sqlite.go`'s `seen`; `registry.go:projectTables`' dropped tables; `migration015.go` stays (old databases run it)
- Modify: `internal/store/store.go` (remove every method in Facts' list, the `Actor*`/`Identity` types that only they use, `Event` stays), `internal/jobs/jobs.go` (Store interface and `RunDailyPass` per Architecture; remove `topN`, `allRawDays`), `internal/app/import.go` (Decision 2: `importStore` gains `MigrateThrough`; it no longer calls `app.Migrate`), `internal/app/migrate.go` (no flat view), `internal/app/widgets.go` (remove `warnImport`, `importBehind`, `importMarks`), `internal/app/app.go` (no `warnImport`), `internal/store/storetest/storetest.go` (no `RebuildFlatView`), `internal/manage/store.go` (no `RebuildFlatView`), `internal/sqlcheck/sqlcheck_test.go` (remove the dynamic entries of deleted functions and `api/ops_limits.go:capUsage`; set `minSites` to the new count rounded down to ten)
- Delete: `internal/reporting/parity_test.go`, `parity_race_test.go`, `testdata/system-sqlite/`; `internal/app/archive_parity_test.go`; migration tests per Decision 12
- Docs: `deploy/UPGRADES.md` (new section: the pre-check, import first, irreversibility, `VACUUM` once with the service stopped, removed tools and fields, removed variables), `docs/deployment.md` (routine operations: the pass; `events import` is for upgrades from before this release), `docs/twillingate.md` (drop "Writing SQL against the views (deprecated)", the `agg_*` mentions in the files section, `events import` caveats kept as history), `docs/reporting.md` (Rules and Tables: no `v_*`/`agg_*`; the imported-history note kept)

**Interfaces:**
- `042_events_removed.sql`:
  - drops, in this order, the `v_*` views, `raw_views`, `raw_product`, `raw_measures`, the 21 `agg_*` tables, `actors`, `identities`, `events`, `usage_attributes` and `project_attributes`;
  - `DELETE FROM meta WHERE key IN ('attributes_top_n','attribute_breakdowns_max','identities_top_n')`;
  - `DELETE FROM usage_history WHERE key NOT IN ('database_bytes','file_bytes')`.
- The pre-check: `SELECT EXISTS(SELECT 1 FROM events)` and `meta` `events_imported`, with Decision 1's message. A database with no `events` table (a fresh install's) passes.
- `legacy_events.go` keeps `RawEvents`, `ViewDaysBefore`, `ProductDaysBefore`, `MeasureDaysBefore` and `OldestRawDays` as `*DB` methods, documented as the import's, valid only through 041.
- `jobs.Store`: `ProjectIDs`, `IncrementalVacuum`, `RecordUsageHistory`, `PurgeArchived`, `ArchiveDueWidgetShares`, `ExpireDrafts` and the attribution methods.

- [ ] **Step 1: Failing tests.**
  - `TestMigration042RefusesUnimportedEvents`: at 41, one `events` row, no `events_imported`; `Migrate` errors with the message and `events` still exists.
  - `TestMigration042DropsAfterTheImport`: with `events_imported` set, every listed object is gone, `submissions`/`rules`/`dashboards` are intact, and the meta and `usage_history` rows are pruned.
  - `TestFreshInstallHasNoEventsTable`: `storetest.Open` has no `events`, `agg_%`, `v_%` or `raw_%` in `sqlite_master`.
  - `TestMigrateFromSnapshot19ThroughHead`: `snapshotAt(19)`, `v_events_flat` created as 019's code built it, migrate to head, no error.
  - `TestImportEventsStopsBefore042` (app): import on a 41 database with rows leaves `schema_migrations` at 41, files written, mark set; then `app.Migrate` drops.
  - `TestImportEventsAfter042SaysNothingToImport`.
  - `TestPassWithoutArchiveRecordsTheDatabaseOnly`.
  - `TestPassRunsNoEventWork`: a fake store with only `jobs.Store`'s methods compiles, and the pass calls archive `Maintain`.
- [ ] **Step 2: Run them; they fail.**
- [ ] **Step 3: Write the migration, the pre-check and `MigrateThrough`.**
- [ ] **Step 4: Remove the store, jobs and app code top down** until `go build ./... && go vet ./...` pass, deleting the tests whose subject went (Decision 12).
- [ ] **Step 5: `sqlcheck` lists, `minSites`; run `make check`.**
- [ ] **Step 6: Docs and UPGRADES.**
- [ ] **Step 7: Commit** `feat(store)!: drop the event tables, rollups and views from SQLite (migration 042)`

### Task 8: Removed configuration is reported and ignored

**Files:**
- Modify: `internal/config/config.go`:
  - remove `RetentionClass`, `Retention.Events`, `AttributeValuesTopN`, `AttributeBreakdownsMax`, `IdentitiesTopN`, their defaults and validations;
  - add `Removed []string`;
  - move the two `RETENTION_EVENTS_*` entries out of `renamed`;
  - `INGEST_LATE_DAYS ≤ RETENTION_DAYS`;
  - fix `MaxEventAge`'s comment.
- Modify: `internal/app/app.go` (log Decision 10's warning per name; remove the cap `meta` writes at :110-121), `internal/api/server.go`/`ops_limits.go` (no `rawDays`), `.env.example`
- Docs: `docs/deployment.md` (remove the five rows; a "Removed in this release" note listing all seven names; the Raspberry Pi advice without caps or raw window, with the memory guidance from the spec's Measurements: 1 GB, the child's limit, `GOMEMLIMIT` not covering it), `docs/twillingate.md:1234` (`INGEST_LATE_DAYS` bound), `deploy/UPGRADES.md` (the variables line)
- Test: `internal/config/config_test.go`, `example_config_test.go`, `internal/app/app_test.go`, `internal/api/docs_sync_test.go`

**Interfaces:**
- `config.Removed` lists, in the order of `removedVars`, every removed name set to a non-empty value.

  ```go
  var removedVars = []string{"RETENTION_RAW_DAYS", "RETENTION_AGGREGATE_DAYS", "RETENTION_EVENTS_RAW_DAYS",
  	"RETENTION_EVENTS_AGGREGATE_DAYS", "ATTRIBUTE_VALUES_TOP_N", "ATTRIBUTE_BREAKDOWNS_MAX", "IDENTITIES_TOP_N"}
  ```

- [ ] **Step 1: Failing tests.**
  - `TestRemovedVariablesAreReportedNotRefused`: all seven set, `FromEnv` succeeds, `Removed` lists them.
  - `TestLateDaysWithinRetentionDays`: 45 against 30 is refused, naming both.
  - `TestBootWarnsOncePerRemovedVariable` (app, captured logger).
- [ ] **Step 2: Run them; they fail.**
- [ ] **Step 3: Implement;** `go build ./...`.
- [ ] **Step 4: Docs; `TestDeploymentDocumentsEveryEnvVar` passes. Run `make check`.**
- [ ] **Step 5: Commit** `feat(config)!: report and ignore the retired retention and cap variables`

### Task 9: The whole branch, measured

**Files:**
- Modify: `docs/litestream.md` (the monthly check reads `raw/` and the newest day file instead of `v_views_daily`; the restore steps' `events import -force` only for a backup taken before this release)
- Modify: `docs/deployment.md:453` (the same check)
- Modify: `docs/superpowers/plans/2026-10-12-raw-first-roadmap.md` (PR 6: link this plan, the decisions that changed it, the RSS measurement)

- [ ] **Step 1: Grep for leftovers.** `rg -n 'agg_|v_views|raw_views|raw_product|v_events_flat|RETENTION_RAW_DAYS|ATTRIBUTE_VALUES_TOP_N|IDENTITIES_TOP_N|ATTRIBUTE_BREAKDOWNS_MAX|cap_usage|source.*sqlite|breakdown' --glob '!deploy/UPGRADES.md' --glob '!docs/superpowers/**' --glob '!internal/store/sqlite/migrations/**' --glob '!**/migration0*_test.go'`. Every hit is either history the text says is history, the dialect hint (Decision 14), or a fix.
- [ ] **Step 2: Fix the backup docs.**
- [ ] **Step 3: Measure idle RSS of a fresh install.**
  - Start `twillingate serve` on an empty data directory, then wait 60 s.
  - Read `ps -o rss= -p <pid>`.
  - Target: about 17 MB (roadmap). Record the number and the child's absence (none spawned while idle).
- [ ] **Step 4: Run `make check`, `make test` (race, note each package's time against 600 s), and `cd web && npm run e2e`.**
- [ ] **Step 5: Commit** `docs: check backups against the event files and link the removal plan`
- [ ] **Step 6: Push and open the draft PR.**
  - The body leads with the problems and the reasoning, then the decisions above.
  - It lists what the user must do: CLAUDE.md (below), the test-instance skill's `start.sh` (drop `events import`; seed into `raw/`), and closing #173 and reducing #175 (D12).
  - `CLAUDE.md` text to propose, not edit:
    - the Layout line for `internal/store/` ("store/sqlite owns every migration; no events");
    - the `processOwned` paragraph, reduced to: the migrator drops `v_events_flat` before old migrations, and nothing builds it;
    - the docs-table row "queryable views" becomes "the event files' tables".
