# Alerts backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build order step 1 of the alerts design. It covers the store, migration 051, the new `internal/alerts` package (validation, templates, audited operations, evaluation, backtest, the loop), the four notification types and the tracker's alert rule. It adds the eight MCP tools and their REST routes, the cap in `limits`, the wiring in `app`, and the docs. Agents can use alerts when it is done. There is no UI in this PR.

**Architecture:** `internal/alerts` is a new rank-1 package beside `manage`. It imports only leaves (`store`, `native`, `archive`, `inbox`, `shared/civil`). It reaches the registry (rules, event resolution, project state) and presence through small interfaces. `app` implements them over `manage.Registry` and the existing `filePresence`. `alerts.Ops` (CRUD, check, history, preview) exists in every process and is handed to `api.Build`. `alerts.Runner` (the loop) runs only in the event-writing process, beside the monitor and the tracker. `jobs` signals it when the daily pass is done. Notifications go through `inbox.Tracker.Alert`, keyed by `(project, "alert", <alert id>)`. Each tracker state carries the alert's revision, so an edit or pause made in a console-only process ends the state without a recovery.

**Tech Stack:** Go (stdlib, modernc SQLite through `internal/store/sqlite`), the native engine (`internal/native`), MCP go-sdk via `internal/api/expose.go`.

**Spec:** `docs/superpowers/specs/2026-11-24-alerts-design.md` (read it first; this plan argues from it).

## Global Constraints

- Layers: `internal/alerts` is rank 1 in `internal/archtest/archtest_test.go`. It imports leaves only, never `manage` (rank 1, sideways), `api`, `jobs` or `app`. `api` and `app` import it.
- Refusals are typed: `store.Refuse(store.ErrInvalid|ErrNotFound|ErrConflict, "…")` (manage's sentinels are the same values). REST answers 400/404/409 through the existing `refusals` table. Match with `errors.Is`, never on text.
- Every SQL string handed to `database/sql` is a constant, a local or uses `placeholders(n)`, so `internal/sqlcheck` resolves it. No new `dynamic` entries.
- Migration `051_alerts.sql` is a plain new-tables migration. Tests copy a migrated database (`newTestDB`/`newTestDBAt` in `store/sqlite`, `storetest.Open` elsewhere) and never migrate their own.
- Every write is audited with its edge (`actorFrom(ctx)`: `mcp` or `api`). Actions: `alert.create`, `alert.update`, `alert.archive`, `alert.restore`, `alert.check`, `alert.purge` (actor `retention`). Subject: `alert/<id>`.
- Values copied from the spec: `window_days` 1–30, default 1. `min_volume` ≥ 1. Live-alert cap 100 per project. Evaluations are kept 400 days. Broken after 3 failed tries in a row, a daily alert retrying hourly. Silence is evaluated hourly and reads the last `ceil(hours/24) + 1` days. The backtest covers 90 days for a stat and 30 for a funnel, and a silence alert's gaps over 30 days. Archived alerts are purged `RETENTION_ARCHIVED_DAYS` after archiving. Daily alerts remind once per evaluated day. Silence alerts remind every `NOTIFY_REMIND_HOURS`.
- Emoji: `alert_breached` 🔥, `alert_still_breached` 🔥, `alert_recovered` ✅, `alert_broken` ⚠️.
- Docs in the same commit as the code they describe (CLAUDE.md table). `internal/api/docs_sync_test.go` must pass at every commit that touches tools, routes or notification types.
- `make check` must pass (vet, coverage ≥ 90% total and for `internal/store`, `internal/manage`, `internal/api`). The new package must be tested well enough not to drag the total under 90%.
- Commits use Conventional Commits with the package as scope (`store`, `jobs`, `inbox`, `alerts`, `api`), lower case, imperative, and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Locally run only the touched packages' tests and `make check` at the end. CI runs the rest.

## Review Focus

1. **A check requested on an alert that then gets paused, snoozed or archived, or whose project gets archived.** The client polls `last_evaluated_at`, so the request must end and not stay pending forever. Tests: `TestPausingEndsTheCheckRequest` (Task 1) and `TestArchivingProjectEndsAlertChecks` (Task 1).
2. **A breached alert edited or paused from a console-only process.** The ingest process's tracker must end the state silently. It must send no reminder from the old definition and no `alert_recovered`. Test: `TestRunnerEndsStatesOfEditedAlerts` (Task 8).
3. **A restart after downtime at 02:00 UTC with `last_day` two days old.** Nothing may run before 03:00. After that, exactly one evaluation for yesterday, and no second one when the 03:00 pass signals again. Test: `TestRunnerCatchesUpOnceAfterDowntime` (Task 8).
4. **An alert on an event the project has never sent.** A count of 0, or a silence with nothing in its window, is `no_data` "never sent", never `breached`. It turns into a real evaluation once the event appears. Tests: `TestEvaluateNeverSentIsNoData` and `TestEvaluateStartsOnceSent` (Task 6).
5. **A name freed by archiving, then reused, then the archived alert restored.** The restore is a `conflict`, and so is a restore at 100 live alerts. Test: `TestArchiveRestoreAlert` (Task 1).

---

## Decisions and deviations from the design

- **Placement.** The operations, evaluation, backtest and loop live in `internal/alerts` (rank 1), as proposed. Event rules and presence come through interfaces (`alerts.Projects`, `alerts.Presence`). `app/alerts.go` implements `Projects` over `manage.Registry`. `Presence` has exactly `manage.Presence`'s method set, so `app`'s `filePresence` serves both. Audit rows are written by the store in the write's transaction, as forms do. `alerts` builds the `store.AuditEntry`.
- **Every tool takes `project_id` and `id`.** REST path wildcards must be input fields (`decodeRequest`), and routes live under `/api/projects/{project_id}/alerts/{id}`, as forms do. An id of another project is `not_found`. The design's tables show `{id}` alone.
- **Revision.** Changing the definition, pausing, snoozing, archiving or restoring bumps `alerts.revision` and sets the state to `pending`. Renaming does not. The tracker stores the revision in its entry. The loop calls `Tracker.KeepAlerts` every minute with the evaluable alerts' revisions, which ends stale or no-longer-evaluable states without a message. This is how "ends the state without a recovery" works across processes.
- **"After the daily pass".** `jobs.Runner.OnPassDone(fn)` is called at the end of every `RunDailyPass`, including the catch-up at boot. A metric alert is due when `last_day < yesterday` and either a pass signal arrived today at or after 03:00 UTC (a boot pass before 03:00 does not count), or the hour is ≥ 4 UTC (a fallback). `last_day` is persisted, so a restart never evaluates a day twice. Only yesterday is caught up.
- **Create requests a check**, as an edit does, so the new alert shows an outcome within a minute.
- **Volume metric.** `read.volume` names a second metric of the stat. Its op is `count`, `actors`, `users`, `groups` or `weight`. Its own `where` must deep-equal the read metric's `where`, or for a `ratio` read its `den`'s `where`. A rate or average read (`ratio`, `bounce_rate`, `avg`, `mean`, `percentile`) requires one. Any other read may carry one. `share` is left out because native refuses it in a stat, so the design's list overstates.
- **min_volume on a funnel** measures the denominator actually used: the first step for `from: start`, the step before for `from: previous`. The design says "the first step's value (the denominator)", and the intent is the denominator.
- **ResolveEvent families.** Event names starting with `$` are skipped. For the rest: table `product` → product. `measures` → measures. `events` or empty → product and measures. Other tables → none. Presence families: product → `FamilyProduct`, measures → `FamilyMeasures`, views → `FamilyViews`, checks → `FamilyChecks`, events/empty → `""` (every family).
- **Presence is checked only when the value reads 0 or empty**, or a silence finds no event. A miss scans the whole history. Positive answers are remembered per process.
- **Restore** refuses with `conflict` when the name is taken by a live alert or the project is at the cap. Create on an archived project is `invalid` ("restore the project first").
- **Cap in `limits`.** A new group `alerts` holds one fixed limit, "Live alerts per project", 100. There is no environment variable for it. (`deployment.md` still changes, for `RETENTION_ARCHIVED_DAYS` and `NOTIFY_REMIND_HOURS`: Tasks 2 and 3.) `TestLimitsHasNoCaps` and `TestLimitsReportsTheLimitsInForce` change, and so does the docs sentence "Nothing is capped". `web/src/lib/api.ts`'s `Limit.group` union gains `'alerts'` in PR 2. Today only `ingest_disabled` is read there.
- **Funnel conversion template labels** are `from` and `to`, so two steps on the same event with different paths still differ.
- **Silence backtest** reads a breakdown by `day` and `hour` with `min`/`max` of `received_at`. `hour` comes from `ts`, so a late batch can mask a gap inside one bucket. The tool description and the docs say this is an approximation.
- **Funnel batching.** Funnels are answered `alone()` by `AnswerMany`, so the funnel backtest issues one funnel per distinct window through `AnswerAll`, with no shared scan. Stats share scans.
- **`latest` is refused outright**, as are `min`/`max` of a day or time. The design refuses only reads whose value is not a number; a `latest` of `value` would be one, but its "latest row" semantics make a poor alert. Revisit if an agent needs it.
- **The threshold message has no "Was … the day before".** That would need a second query for every threshold alert. Change alerts name what they compared with.
- **`queryEnums` on the alert tools offers every query kind**, because the schema is shared with `query`. Only `stat` and `funnel` are accepted, and the refusal says so.
- **Notification link.** `inbox.Path` gives `/projects/<p>/alerts/<id>`. The web route arrives in PR 2, and until then that link opens the app's not-found page. Releases are manual, so cut none between the two PRs.

## REST shapes PR 2 (the Alerts tab) will rely on

`alertOut`: `id, project_id, name, kind (stat|funnel|silence), query (object), read {metric, volume, step, from}, where (silence only), condition {type, op, value, direction, percent, against, hours}, window_days, min_volume (0 none), template, fields, paused, snoozed_until, state, reason, last_value (number|null), last_value_text, last_evaluated_at, last_day, check_requested_at, check_pending, recent [{at, value, outcome}] (newest first, at most 14), created_at, updated_at, archived_at, warnings (create/update only)`. `list_alerts` → `{alerts, max_live}`. `preview_alert` → `{days [{day, value, compare, volume, outcome}], gaps [{from, to, hours}], fired}`. `alert_history` → `{evaluations [{id, at, from, to, value, compare, volume, outcome, error}], next_cursor}`.

## File structure

| File | Responsibility |
| --- | --- |
| `internal/store/alerts.go` (new) | `Alert`, `AlertDef`, `AlertRead`, `AlertCondition`, `AlertEdit`, `AlertEvaluation`, `AlertResult`, state and outcome vocabularies |
| `internal/store/store.go`, `internal/store/reporting.go` | Store interface gains the alert methods; `PurgeResult.Alerts` (in `reporting.go`) |
| `internal/store/sqlite/migrations/051_alerts.sql` (new) | `alerts`, `alert_evaluations` |
| `internal/store/sqlite/alerts.go` (new) | CRUD, check requests, evaluation records, history, prune |
| `internal/store/sqlite/{registry,purge}.go` | `projectTables`, project archive ends checks, purge |
| `internal/jobs/jobs.go` | prune evaluations, purge log, `OnPassDone` |
| `internal/inbox/{render,tracker}.go` | four types, `Tracker.Alert`, `Tracker.KeepAlerts` |
| `internal/alerts/deps.go` | `Projects`, `Presence`, `Engine`, `Store` interfaces, `MaxLive` |
| `internal/alerts/define.go`, `templates.go` | `Input` → `store.AlertDef` (validation), `Parse`, references |
| `internal/alerts/ops.go` | audited operations, `View` |
| `internal/alerts/evaluate.go`, `format.go` | one evaluation, `judge` (Task 6); value, line (Task 5) and message text (Task 6) |
| `internal/alerts/backtest.go` | `Backtest`, `Ops.Preview` |
| `internal/alerts/runner.go` | the loop |
| `internal/api/ops_alerts.go` (new), `ops_limits.go`, `ops_read.go`, `server.go`, `seed_test.go` | tools, routes, cap, host field |
| `internal/app/alerts.go` (new), `app.go` | `Projects` adapter, wiring |
| `docs/twillingate.md`, `CLAUDE.md`, `internal/archtest/archtest_test.go` | docs, layout, rank |

---

### Task 1: Store types, migration 051 and alert CRUD

**Files:**
- Create: `internal/store/alerts.go`, `internal/store/alerts_test.go`, `internal/store/sqlite/migrations/051_alerts.sql`, `internal/store/sqlite/alerts.go`, `internal/store/sqlite/alerts_test.go`
- Modify: `internal/store/store.go` (interface), `internal/store/sqlite/registry.go` (`projectTables`, `SetProjectArchived`)

**Interfaces:**
- Produces, in `internal/store/alerts.go`:

```go
const (
	AlertPending = "pending"; AlertOK = "ok"; AlertBreached = "breached"; AlertNoData = "no_data"
	AlertBroken = "broken"; AlertPaused = "paused"; AlertSnoozed = "snoozed"
	AlertError = "error" // an evaluation outcome only: a runtime failure that is retried
)
var AlertStates = []string{AlertPending, AlertOK, AlertBreached, AlertNoData, AlertBroken, AlertPaused, AlertSnoozed}
var AlertOutcomes = []string{AlertOK, AlertBreached, AlertNoData, AlertBroken, AlertError}
const AlertEvaluationDays = 400

type AlertRead struct {
	Metric string `json:"metric,omitempty"`; Volume string `json:"volume,omitempty"`
	Step string `json:"step,omitempty"`; From string `json:"from,omitempty"` // start | previous
}
type AlertCondition struct {
	Type string `json:"type"` // threshold | change | silence
	Op string `json:"op,omitempty"`; Value *float64 `json:"value,omitempty"`
	Direction string `json:"direction,omitempty"`; Percent float64 `json:"percent,omitempty"`
	Against string `json:"against,omitempty"`; Hours int `json:"hours,omitempty"`
}
// AlertDef is an alert's definition, stored as one JSON object (alerts.definition).
type AlertDef struct {
	Query json.RawMessage `json:"query"` // a native.Query, normalized by internal/alerts
	Read AlertRead `json:"read"`; Condition AlertCondition `json:"condition"`
	WindowDays int `json:"window_days"`; MinVolume float64 `json:"min_volume,omitempty"`
	Template string `json:"template,omitempty"`; Fields map[string]string `json:"fields,omitempty"`
}
type Alert struct {
	ID, ProjectID int64; Name string; Def AlertDef
	Paused bool; SnoozedUntil string // RFC 3339 UTC, "" none
	Revision int64; State string // one of AlertStates but paused/snoozed (those are derived)
	Reason string; LastValue *float64; LastEvaluatedAt, LastDay string
	Fails int; RetryAt, CheckRequestedAt, CreatedAt, UpdatedAt, ArchivedAt string
}
func (a Alert) CheckPending() bool // requested, and not evaluated at or after the request
type AlertEdit struct {
	Name *string; Def *AlertDef; Paused *bool
	SnoozedUntil *string // "" clears
}
```

- Produces the store methods (on `store.Store` and `*sqlite.DB`):

```go
CreateAlert(ctx context.Context, a Alert, max int, now time.Time, au AuditEntry) (Alert, error) // created_at, updated_at, check_requested_at = now
GetAlert(ctx context.Context, projectID, id int64) (Alert, error)
ListAlerts(ctx context.Context, projectID int64, archived bool) ([]Alert, error) // by name
UpdateAlert(ctx context.Context, projectID, id int64, e AlertEdit, now time.Time, au AuditEntry) (Alert, error)
SetAlertArchived(ctx context.Context, projectID, id int64, archived bool, max int, now time.Time, au AuditEntry) (Alert, error)
RequestAlertCheck(ctx context.Context, projectID, id int64, now time.Time, au AuditEntry) (Alert, bool, error)
```

- [ ] **Step 1: Write the migration test and the CRUD tests.** The names and key assertions:
  - `TestMigration051CreatesAlertTables`: `newTestDBAt(t, 50)`, then `migrateThrough(ctx, 51)`. The `alerts` and `alert_evaluations` tables and the indexes `alerts_live_name` and `alert_evaluations_alert` exist (`sqlite_master`).
  - `TestCreateAlertAuditsAndReadsBack`: the created alert has `ID > 0`, `State == "pending"`, `Revision == 1` and `CheckRequestedAt == CreatedAt`. `GetAlert` returns it equal, `Def` included. `auditRows(t, db, "alert.create")` has subject `alert/<id>` and detail `<name>`.
  - `TestCreateAlertRefusesTakenNameAndCap`: a live duplicate name gives `errors.Is(err, store.ErrConflict)`. After archiving, the same name succeeds. With `max = 2`, the third live alert is `ErrConflict`. An unknown project is `ErrNotFound`, checked in the transaction with `projectExists` (`monitors.go`), not left to the foreign key.
  - `TestUpdateAlertResetsOnDefinition`:
    - A rename alone keeps `Revision` and `State`, and its audit detail is `name`.
    - A new `Def` gives `Revision+1`, `State "pending"`, `LastValue == nil`, `LastDay == ""`, `Fails == 0`, `Reason == ""` and `CheckRequestedAt == now`.
    - An edit equal to the row writes nothing (no audit row).
    - An archived alert is `ErrInvalid`. An id of another project is `ErrNotFound`.
  - `TestPausingEndsTheCheckRequest`: a pause gives `Revision+1`, `State "pending"`, `CheckRequestedAt == ""` and `CheckPending() == false`. An unpause gives `CheckRequestedAt == now`. A snooze set clears the request. A snooze cleared requests a check. No request is left that nothing serves: a definition edit while paused, an unpause while snoozed (`SnoozedUntil` in the future) and a definition edit on an alert of an archived project all give `CheckRequestedAt == ""`.
  - `TestArchiveRestoreAlert`:
    - Archive sets `ArchivedAt`, clears the request and bumps `Revision`. Archiving again writes no audit row.
    - Restore refuses with `ErrConflict` when a live alert has the name, and when `max` live exist.
    - A restore gives `State "pending"` and `Revision+1`.
  - `TestRequestAlertCheck`: the first call returns `requested == true` and writes an `alert.check` audit row. A second call while pending returns `false` and writes no audit row. An archived alert is `ErrInvalid`. After `last_evaluated_at` is set (with `execAll`) at or after the request, a new request is accepted.
  - `TestArchivingProjectEndsAlertChecks`: after `SetProjectArchived(…, true, …)`, the project's alerts have `check_requested_at == ''`.
  - `TestAlertCheckPending` (in `internal/store/alerts_test.go`): table cases matching `Monitor.CheckPending`'s rule. A result in the request's own second counts.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/store/... -run 'Alert|Migration051'`
Expected: FAIL (undefined `store.Alert`, no migration 051).

- [ ] **Step 3: Write `051_alerts.sql`.** Lead with a comment in 044's style (spec name, what each table holds, "plain new tables").

```sql
CREATE TABLE alerts (
    id                 INTEGER PRIMARY KEY,
    project_id         INTEGER NOT NULL REFERENCES projects(id),
    name               TEXT    NOT NULL,
    definition         TEXT    NOT NULL,
    paused             INTEGER NOT NULL DEFAULT 0,
    snoozed_until      TEXT    NOT NULL DEFAULT '',
    revision           INTEGER NOT NULL DEFAULT 1,
    state              TEXT    NOT NULL DEFAULT 'pending',
    reason             TEXT    NOT NULL DEFAULT '',
    last_value         REAL,
    last_evaluated_at  TEXT    NOT NULL DEFAULT '',
    last_day           TEXT    NOT NULL DEFAULT '',
    fails              INTEGER NOT NULL DEFAULT 0,
    retry_at           TEXT    NOT NULL DEFAULT '',
    check_requested_at TEXT    NOT NULL DEFAULT '',
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL,
    archived_at        TEXT
);
CREATE UNIQUE INDEX alerts_live_name ON alerts(project_id, name) WHERE archived_at IS NULL;
CREATE TABLE alert_evaluations (
    id        INTEGER PRIMARY KEY,
    alert_id  INTEGER NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    at        TEXT    NOT NULL,
    day_from  TEXT    NOT NULL DEFAULT '',
    day_to    TEXT    NOT NULL DEFAULT '',
    value     REAL,
    compare   REAL,
    volume    REAL,
    outcome   TEXT    NOT NULL,
    error     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX alert_evaluations_alert ON alert_evaluations(alert_id, id);
CREATE INDEX alert_evaluations_at ON alert_evaluations(at);
```

- [ ] **Step 4: Implement the types, then the sqlite methods in `internal/store/sqlite/alerts.go`.**
  - One `alertCols` constant and one `scanAlert(rowScanner)`, as `monitors.go` does.
  - Every mutation runs in `d.tx` with `audit(ctx, tx, a)`. No config bump: alerts are not in the registry snapshot.
  - Name and cap checks run inside the transaction and refuse with `store.Refuse(store.ErrConflict, "project %d already has a live alert named %q", …)` and `store.Refuse(store.ErrConflict, "project %d holds %d live alerts, the most it may; archive one first", …)`. The unique index is the backstop.
  - `UpdateAlert` reads the row in the transaction, merges the edit in Go and writes every column with one constant `UPDATE`.
    - A changed `Def`, `Paused` or `SnoozedUntil` (compare `Def` by its JSON) resets: `state='pending', reason='', last_value=NULL, last_day='', fails=0, retry_at=''`, and `revision+1`.
    - `check_requested_at = now` when `Def` changed, `Paused` went false, or the snooze was cleared, and only if the result is evaluable: not paused, `snoozed_until` empty or not after `now`, and the project live (read in the transaction). Otherwise it is `''`.
    - The audit detail is the changed keys, space-separated: `name`, `definition`, `paused=true`, `snoozed_until=<v>`.
  - Times are `now.UTC().Format(time.RFC3339)`.
  - Append `"alerts"` to `projectTables`. `alert_evaluations` has no `project_id` and goes by cascade.
  - In `SetProjectArchived`'s archived branch, beside `finishPendingChecks`, add `UPDATE alerts SET check_requested_at='' WHERE project_id=?`.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/store/... -run 'Alert|Migration051|ProjectTables|DeleteProjectData'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -m "feat(store): add alerts with audited create, edit, archive and check requests" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Evaluation records, history, prune and purge; the daily pass

**Files:**
- Modify: `internal/store/alerts.go`, `internal/store/store.go` (the six methods below join `store.Store`, which `jobs.New`, `alerts.NewOps` and `alerts.NewRunner` receive), `internal/store/reporting.go` (`PurgeResult.Alerts []int64`), `internal/store/sqlite/alerts.go`, `internal/store/sqlite/purge.go`, `internal/jobs/jobs.go`, `internal/api/ops_limits.go` (the "Archived items" description), `docs/deployment.md` (the `RETENTION_ARCHIVED_DAYS` row and the daily-pass paragraph), `docs/twillingate.md` (the `RETENTION_ARCHIVED_DAYS` sentence near the top)
- Test: `internal/store/sqlite/alerts_test.go`, `internal/store/sqlite/purge_test.go`, `internal/jobs/jobs_test.go` (and `passOnlyStore`)

**Interfaces:**
- Consumes: Task 1's types.
- Produces:

```go
type AlertEvaluation struct {
	ID, AlertID int64; At string; From, To string // days; "" for silence
	Value, Compare, Volume *float64; Outcome, Error string
}
// AlertResult is one evaluation and what it does to the alert's row.
type AlertResult struct {
	AlertID, Revision int64 // written only while the alert is live at this revision
	Eval    AlertEvaluation // stored in the history; Eval.At becomes last_evaluated_at
	State   string          // the new state; "" keeps it (a failed try not yet broken)
	Reason  string          // written with State
	LastDay string          // "" keeps last_day
	Fails   int
	RetryAt string
}
RecordAlertEvaluation(ctx context.Context, r AlertResult) (bool, error)
ListAlertEvaluations(ctx context.Context, alertID int64, limit int, before int64) ([]AlertEvaluation, error) // newest first
RecentAlertEvaluations(ctx context.Context, ids []int64, n int) (map[int64][]AlertEvaluation, error)
ListEvaluableAlerts(ctx context.Context) ([]Alert, error) // live alerts of live projects, paused and snoozed included
ResetAlertsOfArchivedProjects(ctx context.Context) (int, error)
PruneAlertEvaluations(ctx context.Context, before time.Time) (int, error)
// jobs
func (r *Runner) OnPassDone(fn func(day civil.Date)) *Runner
```

- [ ] **Step 1: Write the tests.**
  - `TestRecordAlertEvaluation`:
    - At the current revision it returns `true`. One history row is stored, and the row's `state`, `reason`, `last_value` (`= Eval.Value`), `last_evaluated_at`, `last_day`, `fails` and `retry_at` are written.
    - `State: ""` keeps the state and `last_value` but writes `fails`, `retry_at` and `last_evaluated_at`.
    - A stale revision, an archived alert, or an alert whose project is archived returns `false` and writes nothing: no history row.
  - `TestListAlertEvaluationsPages`: five rows. `limit 2` gives ids 5 and 4. `before 4` gives 3 and 2.
  - `TestRecentAlertEvaluations`: three alerts and `n = 2`. Each maps to its two newest. An alert with none is absent.
  - `TestListEvaluableAlerts`: it leaves out archived alerts and alerts of an archived project. It keeps paused and snoozed ones.
  - `TestResetAlertsOfArchivedProjects`: a breached alert of an archived project becomes `pending` with `last_day ''`, and the call returns 1. A live project's alert is untouched. A second call returns 0.
  - `TestPruneAlertEvaluations`: rows with `at < before` go, and the call returns their count.
  - `TestPurgeArchivedTakesAlerts` (`purge_test.go`): an alert archived 40 days ago with `days = 30` goes with its evaluations. There is an `alert.purge` audit row with actor `retention`, and `PurgeResult.Alerts == []int64{id}`. An alert archived 10 days ago stays.
  - `TestDailyPassPrunesAlertEvaluations` (`jobs_test.go`): `passOnlyStore` records `PruneAlertEvaluations` with `before == now.AddDate(0, 0, -store.AlertEvaluationDays)`.
  - `TestRunDailyPassSignalsItsDay`: with `OnPassDone(fn)`, one `RunDailyPass` calls `fn` once with `civil.Today(now)`, after the purge and prune.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/store/... ./internal/jobs/... -run 'AlertEvaluation|Evaluable|ArchivedProjects|PurgeArchivedTakesAlerts|PrunesAlert|SignalsItsDay'`
Expected: FAIL (undefined methods).

- [ ] **Step 3: Implement.**
  - `RecordAlertEvaluation` does one transaction: an `UPDATE … WHERE id=? AND revision=? AND archived_at IS NULL AND project_id IN (SELECT id FROM projects WHERE archived_at IS NULL)` (when `RowsAffected` is 0, return `false` with no insert), then the `INSERT` into `alert_evaluations`.
  - `RecentAlertEvaluations` uses `ROW_NUMBER() OVER (PARTITION BY alert_id ORDER BY id DESC)` over `alert_id IN (`+placeholders(len(ids))+`)`.
  - `ResetAlertsOfArchivedProjects` is one `UPDATE … WHERE archived_at IS NULL AND state != 'pending' AND project_id IN (SELECT id FROM projects WHERE archived_at IS NOT NULL)`. It sets `state='pending', reason='', last_value=NULL, last_day='', fails=0, retry_at=''`. It is unaudited, like the runner's other writes.
  - `PurgeArchived` gains an alerts block after the forms block, in `purgeEach`'s style.
  - In `jobs`: add `PruneAlertEvaluations` to `jobs.Store`. Call it in `RunDailyPass` every pass, whatever `RETENTION_ARCHIVED_DAYS` is. Log failures and `"alert_evaluations", n`. Add `"alerts", len(purged.Alerts)` to the purge log. `OnPassDone` stores `fn`, and `RunDailyPass` calls it last.

  - Docs, in this commit: the `RETENTION_ARCHIVED_DAYS` row of `docs/deployment.md` and `docs/twillingate.md`'s sentence about it name an archived alert (with its evaluations). The daily-pass paragraph of `deployment.md` adds that it prunes alert evaluations older than 400 days. `limitsFrom`'s "Archived items" description adds "alert".

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/store/... ./internal/jobs/... && go test ./internal/api/ -run 'Limits|TestDeployment'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store internal/jobs internal/api/ops_limits.go docs/deployment.md docs/twillingate.md
git commit -m "feat(store): record alert evaluations, prune them after 400 days and purge archived alerts" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Notification types, rendering and the tracker's alert rule

**Files:**
- Modify: `internal/store/notifications.go`, `internal/inbox/render.go`, `internal/inbox/tracker.go`, `internal/api/ops_notifications.go` (the description's type list and "subject"), `docs/twillingate.md` (the Notifications intro, the `list_notifications` row, `### Notification types`), `docs/deployment.md` (the `NOTIFY_REMIND_HOURS` row)
- Test: `internal/inbox/render_test.go` (+ regenerated `testdata/TestRenderGolden.golden.json`), `internal/inbox/tracker_test.go`

**Interfaces:**
- Produces:

```go
// store
const (NotifyAlertBreached = "alert_breached"; NotifyAlertStillBreached = "alert_still_breached"
	NotifyAlertRecovered = "alert_recovered"; NotifyAlertBroken = "alert_broken")
// appended to NotificationTypes in this order, after NotifyFormSubmission

// inbox
type AlertKey struct{ ProjectID, ID int64 }
type AlertObservation struct {
	Key      AlertKey
	Revision int64
	Name     string            // the notification's Subject
	Daily    bool              // a metric alert: reminds once per Day; else every remind interval
	Day      string            // the window's last day, YYYY-MM-DD; "" for silence
	Outcome  string            // store.AlertOK, AlertBreached, AlertBroken, or AlertNoData (only clears Broken, silently)
	Fields   map[string]string // "value", "detail"; "reason" when broken
	At       time.Time
}
func (t *Tracker) Alert(ctx context.Context, o AlertObservation)
func (t *Tracker) KeepAlerts(ctx context.Context, live map[AlertKey]int64) // id -> revision
```

The notification's `Fields` are `o.Fields` plus `alert_id` (decimal), and `since` (and `until` on recovery) as RFC 3339. Rendering, where `<x>` is a field:

| Type | Title | Body |
| --- | --- | --- |
| `alert_breached` | `<alert>: <value>` (alert = `Subject`) | `<detail>` |
| `alert_still_breached` | `<alert>: still <value>` | `<detail> Breached for <elapsed>, since <since>.` |
| `alert_recovered` | `<alert>: back to <value>` | `<detail> Was breached for <span>, since <since>.` |
| `alert_broken` | `<alert> is broken` | `<reason>.` |

`Emoji`: breached and still breached 🔥, recovered ✅, broken ⚠️. `Path`: `/projects/<project_id>/alerts/<alert_id>` (`/inbox` when either is missing).

- [ ] **Step 1: Write the tests.**
  - `sample()` gains the four types with `Subject "Signup conversion"`, `alert_id "12"`, `value "2.1%"`, `detail "Below 3% on 2026-11-23 (1,240 at the first step)."`, and `since`/`until`/`reason` as the types need. `TestRenderGolden` covers them through `store.NotificationTypes`.
  - `TestAlertEmojiAndPath`: the emoji are `🔥 🔥 ✅ ⚠️`. `Path` is `/projects/3/alerts/12`.
  - `TestAlertBreachedThenRecovered`: `breached` sends one `alert_breached` with `Subject == "Signup conversion"`, `Fields["alert_id"] == "12"` and `Fields["since"]`. A second `breached` the same `Day` sends none. `ok` sends one `alert_recovered` with `until`, after which `f.has(1, "alert", "12")` is false.
  - `TestAlertDailyRemindsOncePerDay`: `breached` on `Day` D, then a `breached` at D the same day (a check request) sends nothing. A `breached` at D+1 sends one `alert_still_breached`.
  - `TestAlertSilenceRemindsEveryInterval` (`Daily false`, remind 4h): observations 1h apart send `alert_still_breached` only at 4h and 8h.
  - `TestAlertBrokenOnceAndSilentRecovery`: `broken` sends one `alert_broken` with `reason`. Another `broken` sends none. `ok` sends none, and the row is gone. Breached then broken then breached sends no second `alert_breached`, because tracking resumes from before the break. Breached then broken then ok sends `alert_recovered`.
  - `TestAlertNoDataClearsBroken`: broken, then `no_data` (nothing sent), then broken sends a second `alert_broken`. `no_data` while breached keeps the breach and sends nothing.
  - `TestAlertRevisionStartsOver`: breached at rev 1, then breached at rev 2, sends `alert_breached` again and no recovery.
  - `TestKeepAlertsEndsWithoutRecovery`: breached, then `KeepAlerts(map{})` sends nothing and the row is gone. `KeepAlerts` with a different revision also drops it. With the same revision it is kept.
  - `TestTickLeavesAlertStates`: with no monitors and a breached alert state, `Tick` at +5h sends nothing (no `uptime_still_down`) and keeps the state. Also under `PauseMonitoring`.
  - `TestAlertStateSurvivesRestart`: a new tracker over the same fake sends no second `alert_breached`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/inbox/... -run 'Alert|TickLeavesAlertStates|RenderGolden'`
Expected: FAIL.

- [ ] **Step 3: Implement.**
  - Add `kindAlert = "alert"`.
  - `entry` gains `Rev int64 \`json:"rev,omitempty"\``, `Day string \`json:"day,omitempty"\`` and `Broken bool \`json:"broken,omitempty"\``.
  - `tick`'s reminder loop and `prune` skip `kindAlert`. Alert reminders come only from `Alert`, which also keeps them going while monitoring is paused.
  - `Alert` builds its own `store.NewNotification` (the Subject is the name, not the key's subject).
  - Recovery uses the existing two-step: put an `entry{Ended}` with the message, then delete.
- [ ] **Step 4: Regenerate the golden file and run the tests**

Run: `go test ./internal/inbox/ -run TestRenderGolden -update-golden && go test ./internal/inbox/...`
Expected: PASS. Read the golden diff: four new entries, the rest unchanged.

- [ ] **Step 5: Update the docs and the `list_notifications` description.** Add four rows to `### Notification types` in `store.NotificationTypes` order (example titles as in the table above). The intro's emoji list gains ⚠️ for a broken alert and mentions alerts. In `ops_notifications.go`, the type list in the description gains the four, and "subject (the origin, domain or form)" becomes "subject (the origin, domain, form or alert name)", here and in `docs/twillingate.md`'s `list_notifications` row. `docs/deployment.md`'s `NOTIFY_REMIND_HOURS` row adds the reminders of a silence alert still breached.

Run: `go test ./internal/api/ -run 'TestDocumentMatchesNotificationVocabularies|TestDeployment'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/notifications.go internal/inbox internal/api/ops_notifications.go docs/twillingate.md docs/deployment.md
git commit -m "feat(inbox): render alert notifications and track breached alerts with their own reminder rule" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `internal/alerts`: definitions, validation and templates

**Files:**
- Create: `internal/alerts/deps.go`, `internal/alerts/define.go`, `internal/alerts/templates.go`, `internal/alerts/define_test.go`, `internal/alerts/templates_test.go`
- Modify: `internal/archtest/archtest_test.go` (`"internal/alerts": 1`, and the package comment), `CLAUDE.md` (a Layout line for `internal/alerts/`, and "`manage`, `alerts` and the leaves import only leaves")

**Interfaces:**
- Produces, in `deps.go`:

```go
const MaxLive = 100
type Projects interface {
	Project(ctx context.Context, id int64) (name string, archived, ok bool)
	// ResolveEvent is manage.Snapshot.ResolveEvent: the name the event is stored under, and whether it is blocked.
	ResolveEvent(ctx context.Context, projectID int64, family store.Family, name string) (to string, blocked bool)
	Rules(ctx context.Context) archive.Rules
}
type Presence interface { // manage.Presence's method set
	HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error)
	HasAttribute(ctx context.Context, projectID int64, key string) (bool, error)
}
type Engine interface { AnswerAll(ctx context.Context, reqs []native.Request) []native.Answered }
// Store (the operations' slice of store.Store) is declared here too, in Task 5.
```

- Produces, in `define.go`:

```go
// Input is a definition as create_alert and preview_alert take it: exactly one source, Query with Read,
// Where (silence; WhereSet tells an empty where from none) or Template with Fields.
type Input struct {
	Name string; Query *native.Query; Read *store.AlertRead
	Where []native.Filter; WhereSet bool; Template string; Fields map[string]string
	Condition store.AlertCondition; WindowDays int; MinVolume float64
}
func Build(in Input) (store.AlertDef, error)          // refusals wrap store.ErrInvalid
func Parse(def store.AlertDef) (Parsed, error)        // a stored definition back for evaluation
func InputOf(def store.AlertDef) (Input, error)       // what Build was given, for merging an edit
type Parsed struct {
	Def store.AlertDef; Query native.Query
	Kind string // stat | funnel | silence
	Read readKind // readCount, readFraction, readNumber, readSilence (unexported enum)
	Metric, Volume int // stat: indexes into Query.Metrics; Volume -1 when none
	Step, Base int    // funnel: indexes into Query.Steps; Base is the denominator
}
type Reference struct { Family store.Family; Event string; Attr string } // one of Event, Attr
func (p Parsed) References() []Reference // event_name eq/in values ($ names included) and attr_<key> columns, deduplicated
func CheckName(name string) error // 1–100 characters, no control characters
```

- Produces, in `templates.go`: `func fromTemplate(name string, f map[string]string) (native.Query, store.AlertRead, []native.Filter, bool /*silence*/, []string /*conditions offered*/, error)`.

| Template | Fields | Query and read | Conditions |
| --- | --- | --- | --- |
| `event_count` | `event` (required), `path` (only with `$page_view`) | stat, table `events`, `count`, where `event_name eq` (+ `path eq`); read `count` | threshold, change |
| `unique_people` | `event` (optional), `who` `actors` (default) or `users` | stat, `actors`/`users`, where on the event if given | threshold, change |
| `conversion` | `from_event`, `to_event` (required), `from_path`, `to_path`, `people` `actor` (default) or `user` | funnel, steps `from` and `to`, `by`, not sequential; read `{step: "to", from: "start"}` | threshold, change |
| `no_events` | `event` (required), `path` | silence where as `event_count`'s | silence |

**Rules `Build` pins.** Each line is a refusal unless stated otherwise:
- There must be exactly one source. A silence condition takes `where` or the `no_events` template. `where` takes only silence.
- A kind other than `stat` or `funnel` is refused with "an alert reads a `stat` (one metric) or a `funnel` (one step over the first)".
- `compare` and `shape` are cleared before storing. `q.Validate()` failing gives `invalid: query: <native's message>`.
- A stat read:
  - `metric` is needed with more than one metric and must name one (`Metric.Name()`).
  - An op of `latest`, or a `min`/`max` of `day`, `ts` or `received_at`, is refused, because it is not a number. The tool builds silence's stat itself.
  - `ratio`, `bounce_rate`, `avg`, `mean` and `percentile` need `volume`, following the volume rule in Decisions.
- A funnel read: `step` is required and is one of the labels, not the first. `from` is `start` (the default) or `previous`. `volume` is refused.
- Conditions:
  - threshold: `op` is `below` or `above`, and `value` is set and finite.
  - change: `direction` is `drop`, `rise` or `either`; `percent` is > 0 and ≤ 1000; `against` is `previous` or `week_ago`.
  - silence: `hours` is 1 to 720.
- `window_days`: 0 means 1, except the `conversion` template, where 0 means 7. 1–30 are taken. Silence takes 0 or 1 only.
- `min_volume`: 0 means none. Otherwise it is ≥ 1. It is refused on silence. A threshold on a count or number read without a volume metric refuses it.
- A silence query is `{kind: stat, table: events, metrics: [{op: max, column: received_at}], where}`.

- [ ] **Step 1: Write the tests.**
  - `TestBuildRefusesOtherKinds`: a trend and a breakdown are `ErrInvalid`, and the message contains "an alert reads a `stat` (one metric) or a `funnel`".
  - `TestBuildStatRead`: one metric defaults the read. Two without `metric` are invalid. An unknown metric is invalid. `latest`, `max day` and a caller-written `max received_at` with a threshold are invalid.
  - `TestBuildRateNeedsVolume`: `avg` without volume is invalid. A volume op of `sum` is invalid. A volume with another `where` is invalid. A ratio whose volume `where` equals the den's is OK.
  - `TestBuildFunnelRead`: no step, the first step, and an unknown label are invalid. `from: previous` is OK, with `Parsed.Base == Step-1`.
  - `TestBuildConditions`: a table of eight invalid conditions (each bound above), and one valid of each type.
  - `TestBuildMinVolume`: a threshold on a count without volume is invalid. A change on a count is OK. A silence with `min_volume` is invalid. `0.5` is invalid.
  - `TestBuildWindowDays`: 0 is stored as 1, but as 7 for the `conversion` template (an explicit 1 stays 1). 31 is invalid. Silence with 7 is invalid.
  - `TestBuildStripsCompareAndShape`: the stored `Query` JSON has no `compare` or `shape`.
  - `TestBuildSilenceFromWhere`: the stored query equals the silence stat above. An empty `where` (set) is OK. `query` together with `where` is invalid.
  - `TestTemplates`: each template's built `native.Query` and `AlertRead` equal an expected literal. `event_count` with a change condition is OK, and with silence it is invalid. `conversion` with no `to_event` is invalid. `conversion` without `window_days` stores 7. `path` on `event_count` for a non-`$page_view` event is invalid.
  - `TestParseRoundTrip`: `Parse(Build(in))` gives the same kind, read and indexes. `InputOf` gives back the template (or where, or query and read).
  - `TestReferences`: events from `where`, a metric's `where`, ratio `num`/`den`, funnel steps, `any_of` and `ever`, with families by table. `in` contributes each value. `ne` contributes none. `$page_view` is returned (presence applies to it); only the `ResolveEvent` call skips `$` names. `attr_plan` in a filter or metric column gives `Attr "plan"`.
  - `TestCheckName`: empty, 101 characters and a control character are invalid.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/alerts/... ./internal/archtest/...`
Expected: FAIL (no package, then not in the rank table).

- [ ] **Step 3: Implement** `deps.go`, `define.go` and `templates.go`. Add the rank entry and the CLAUDE.md Layout line: `internal/alerts/  metric alerts: definitions and templates, audited operations, evaluation, backtest and the loop`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/alerts/... ./internal/archtest/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/alerts internal/archtest CLAUDE.md
git commit -m "feat(alerts): validate alert definitions and build the four templates' queries" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Audited operations

**Files:**
- Create: `internal/alerts/ops.go`, `internal/alerts/ops_test.go` (a real store via `storetest.Open`; fake `Projects` and `Presence`), `internal/alerts/format.go` (`formatValue`, `formatLine`), `internal/alerts/format_test.go`
- Modify: `internal/alerts/deps.go` (`Store`)

**Interfaces:**
- Consumes: Task 1/2 store methods, Task 4's `Build`/`Parse`/`InputOf`/`References`.
- Produces:

```go
// in deps.go
type Store interface { CreateAlert; GetAlert; ListAlerts; UpdateAlert; SetAlertArchived; RequestAlertCheck;
	ListAlertEvaluations; RecentAlertEvaluations } // the Task 1/2 signatures; Create passes o.now() as CreateAlert's now
type Ops struct { Timeout time.Duration /* CONSOLE_QUERY_TIMEOUT for Preview */ ; /* unexported: st, projects, eng, presence, now */ }
func NewOps(st Store, projects Projects, eng Engine, presence Presence) *Ops // eng and presence may be nil
type View struct {
	store.Alert; Parsed Parsed; State string // derived: paused, snoozed, else the stored state
	ValueText string; Recent []store.AlertEvaluation; Warnings []string
}
type Patch struct {
	Name *string; Condition *store.AlertCondition; WindowDays *int; MinVolume *float64
	Paused *bool; SnoozedUntil **time.Time // non-nil pointer to nil clears
	Query *native.Query; Read *store.AlertRead; Where *[]native.Filter; Template *string; Fields map[string]string
}
func (o *Ops) List(ctx context.Context, projectID int64, archived bool) ([]View, error)
func (o *Ops) Create(ctx context.Context, actor string, projectID int64, in Input) (View, error)
func (o *Ops) Update(ctx context.Context, actor string, projectID, id int64, p Patch) (View, error)
func (o *Ops) Archive(ctx context.Context, actor string, projectID, id int64) (View, error)
func (o *Ops) Restore(ctx context.Context, actor string, projectID, id int64) (View, error)
func (o *Ops) Check(ctx context.Context, actor string, projectID, id int64) (View, error)
func (o *Ops) History(ctx context.Context, projectID, id int64, limit int, cursor string) ([]store.AlertEvaluation, string, error)
func formatValue(p Parsed, v *float64) string // in format.go; View.ValueText and the messages use it
func formatLine(p Parsed, v float64) string    // a threshold's value in the read's unit, trailing zeros trimmed
```

**Rules:**
- An unknown project is `ErrNotFound`. Create on an archived project is `ErrInvalid`.
- At save, each `Reference.Event` goes through `ResolveEvent`. A renamed event is refused: "`<name>` is now `<to>` (an event rule); alert on `<to>`". A blocked one is refused too.
- When `presence` is not nil, a never-sent event or attribute adds a warning: "`<name>` has never been sent by this project; the alert reads no_data until it is". A presence error adds "could not check whether `<name>` was sent: `<err>`". Neither refuses.
- Update merges `p` over `InputOf(current)`. A new source (`Query`/`Read`, `Where`, `Template`/`Fields`) replaces the old one whole. Only a changed `Build` result is passed as `AlertEdit.Def`.
- `SnoozedUntil` must be in the future.
- Check refuses `ErrInvalid` on an archived alert, a paused one, a snoozed one and an archived project. While a request is pending it returns the view unchanged, with no error.
- History: the default limit is 50, the most 400. The cursor is a decimal evaluation id; a bad one is `ErrInvalid`.
- `Recent` holds the 14 newest.

- [ ] **Step 1: Write the tests.**
  - `TestCreateAlertRefusesRenamedEvent`: renamed → `ErrInvalid` naming the new name. Blocked → `ErrInvalid` containing "blocked".
  - `TestCreateAlertWarnsNeverSent`: the alert is created and `Warnings` names `signup_done`.
  - `TestCreateOnUnknownOrArchivedProject`: `ErrNotFound`, `ErrInvalid`.
  - `TestUpdateMergesPatch`:
    - A condition change keeps the query and bumps `Revision`.
    - A rename alone keeps `Revision`.
    - A switch to silence needs `Where`: without it, `ErrInvalid`.
    - A template alert's `Fields` change rebuilds the query.
    - A past `SnoozedUntil` is `ErrInvalid`.
  - `TestViewState`: paused gives `"paused"`. A future snooze gives `"snoozed"`. A past one gives the stored state.
  - `TestCheckAlert`: paused is `ErrInvalid`. The first call has `CheckRequestedAt` set and an `alert.check` audit row. A second returns the same `CheckRequestedAt`.
  - `TestHistoryOfAnotherProjectIsNotFound`, `TestHistoryCursor` (pages, a bad cursor gives `ErrInvalid`).
  - `TestListRecent`: 20 evaluations give `len(View.Recent) == 14`, newest first.
  - `TestFormatValue` (`format_test.go`): `1240` count gives `1,240`. `0.0213` fraction gives `2.1%`, `0.41` gives `41.0%`. `1234.567` number gives `1,234.57`. `12.5` gives `12.5`. Nil gives `—`. `formatLine`: fraction `0.03` gives `3%`, `0.025` gives `2.5%`; count `1000` gives `1,000`. The silence forms are in Task 6.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/alerts/... -run 'Create|Update|ViewState|CheckAlert|History|ListRecent'`
Expected: FAIL.

- [ ] **Step 3: Implement `ops.go`, and `formatValue` and `formatLine` in `format.go`.** Audit entries are `store.AuditEntry{Actor: actor, Action: "alert.<op>", Subject: "alert/<id>"}`. Create's subject is filled by the store after the insert. The formatting rules are listed under Task 6.
- [ ] **Step 4: Run the tests**

Run: `go test ./internal/alerts/...`
Expected: PASS.

- [ ] **Step 5: Commit** — `git add internal/alerts && git commit -m "feat(alerts): audited alert operations with event checks at save" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`

---

### Task 6: Evaluating one alert, and its message

**Files:**
- Create: `internal/alerts/evaluate.go`, `internal/alerts/evaluate_test.go`
- Modify: `internal/alerts/format.go` (`message`), `internal/alerts/format_test.go` (`TestMessage`); both created in Task 5

**Interfaces:**
- Consumes: `Parsed`, `Engine`, `Projects`, `Presence`.
- Produces:

```go
type Outcome struct {
	Outcome  string     // store.AlertOK, AlertBreached, AlertNoData, AlertBroken, AlertError
	From, To civil.Date // the window; zero for silence
	Value, Compare, Volume *float64 // silence: Value is the newest matching event's age in hours
	Reason   string     // no_data or broken reason; the error text for AlertError
	Last     time.Time  // silence: the newest matching received_at; zero when none
	At       time.Time  // when it was evaluated (now)
}
type Evaluator struct { /* eng, projects, presence, timeout, seen (positive presence memo, mutex) */ }
func NewEvaluator(eng Engine, projects Projects, presence Presence, timeout time.Duration) *Evaluator
func (e *Evaluator) Evaluate(ctx context.Context, a store.Alert, now time.Time) Outcome
func judge(p Parsed, value, compare, volume *float64) (outcome, reason string) // shared with the backtest
func message(p Parsed, o Outcome) (value, detail string) // silence reads o.Last and o.At
```

**Evaluation rules:**
- Windows. Metric: `To = yesterday`, `From = To - (w-1)`. The comparison window is `previous` (the w days before `From`) or `week_ago` (`From-7 … To-7`). Silence: `From = today - ceil(h/24)`, `To = today`.
- Order of checks:
  1. `Parse` fails, or `ResolveEvent` gives a rename or a block: broken, with reason `renamed to <to>: <name>` or `blocked: <name>`.
  2. Answer the current window and the comparison together through `AnswerAll` under `timeout`, with `projects.Rules`. `native.ErrInvalid` gives broken with `query: …`. Any other error (deadline, `ErrTooLarge`, busy, a vanished file) gives `AlertError` with the error text.
  3. Read the number. A stat gives the read metric's column. A funnel gives step / base, empty when the base is 0. Silence gives `(now - max received_at)` in hours, or nil.
  4. When the value is 0 or nil (silence: nil), ask presence for each reference not yet seen. Never sent gives `no_data` with reason `never sent: <name>`. A presence error is treated as "sent", so the presence check never decides breached or broken.
  5. `judge`, in this order:
     - silence first: nil (no event in the window), or `age > hours`, is breached; otherwise ok. Only step 4's presence check turns a nil into `no_data`.
     - An empty value gives `no_data` ("no value").
     - `min_volume` above the volume gives `no_data` ("below min_volume"). The volume is the volume metric, the funnel base, or for a change on a count or number read without one, the comparison.
     - change: a comparison of 0 or empty gives `no_data`. `pct = (v-c)/c*100`. `drop` breaches when `pct <= -percent`, `rise` when `pct >= percent`, `either` when `|pct| >= percent`.
     - threshold: `below` is `v < value`, `above` is `v > value`.
     - Otherwise ok.

**Message examples** (pinned in `TestMessage`):

| Case | value | detail |
| --- | --- | --- |
| funnel threshold below 0.03, w1, breached | `2.1%` | `Below 3% on 2026-11-23 (1,240 at the first step).` |
| same, ok | `3.4%` | `Not below 3% on 2026-11-24 (1,310 at the first step).` |
| count threshold below 1000, w7, breached | `812` | `Below 1,000 from 2026-11-17 to 2026-11-23.` |
| count change drop 30 week_ago, w1, breached | `3,310` | `Down 34% on 2026-11-23 against 2026-11-16 (5,015).` |
| from previous funnel | `41.0%` | `… (220 at the step before).` |
| silence 6h, breached | `last event 7h ago` | `No matching event since 2026-11-24 06:10 UTC (alert at 6h).` |
| silence, none in window | `no event in 30h` | `No matching event in the last 30h (alert at 6h).` |

The value text is `formatValue` and the line (`Below 3%`, `Below 1,000`) is `formatLine`, both written in Task 5. `formatValue`: a count is an integer with thousands separators. A fraction (funnel or `bounce_rate`) is a percent with one decimal. A number has at most two decimals, trailing zeros trimmed, with separators. Nil is `—`.

- [ ] **Step 1: Write the tests.** The fake engine answers canned `native.Result`s keyed by `(kind, From, To)` and records requests.
  - `TestEvaluateThresholdBelow`: a count of 3 below 5 gives breached with `Value 3`, `From == To == yesterday`.
  - `TestEvaluateNeverSentIsNoData`: a count of 0 with presence false gives `no_data`, reason `never sent: signup`. A silence with nothing in its window and presence false gives `no_data`.
  - `TestEvaluateStartsOnceSent`: the same alert with presence true gives breached, and presence is not asked again (the memo).
  - `TestEvaluateCountZeroIsReal`: 0 with presence true is breached below 1.
  - `TestEvaluateEmptyRateIsNoData`.
  - `TestEvaluateMinVolume`: a funnel base of 10 with `min_volume 50` gives `no_data`.
  - `TestEvaluateChangeWeekAgo`: the second request's window is `From-7 … To-7`. 0.040 → 0.028 with `drop 30` is breached (exactly 30%). With `percent 31` it is ok.
  - `TestEvaluateChangeZeroComparisonIsNoData`.
  - `TestEvaluateFunnelFromPrevious`: value = step / previous step.
  - `TestEvaluateSilenceNoneInWindowIsBreached`: nothing in the window and presence true gives breached, `Value == nil`, `Last.IsZero()`.
  - `TestEvaluateSilence`: `hours 6`, newest 7h ago gives breached with `Value ≈ 7` and `Last` that event's `received_at`. Newest 2h ago gives ok. A request covers `ceil(6/24)+1 = 2` days.
  - `TestEvaluateRenamedIsBroken`, `TestEvaluateBlockedIsBroken`, `TestEvaluateInvalidQueryIsBroken` (the engine returns a `native.ErrInvalid`-wrapped error).
  - `TestEvaluateRuntimeErrorIsError` (`context.DeadlineExceeded`, `native.ErrTooLarge`).
  - `TestMessage` (the table above).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/alerts/... -run 'Evaluate|Message|FormatValue'`
Expected: FAIL.

- [ ] **Step 3: Implement `evaluate.go` and `message` in `format.go`.**
- [ ] **Step 4: Run the tests**

Run: `go test ./internal/alerts/...`
Expected: PASS.

- [ ] **Step 5: Commit** — `git add internal/alerts && git commit -m "feat(alerts): evaluate an alert over complete days, with broken and no-data rules" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`

---

### Task 7: Backtest and preview

**Files:**
- Create: `internal/alerts/backtest.go`, `internal/alerts/backtest_test.go`

**Interfaces:**
- Consumes: `Evaluator`, `judge`, `Ops`.
- Produces:

```go
type BacktestDay struct { Day civil.Date; Value, Compare, Volume *float64; Outcome string }
type Gap struct { From, To time.Time; Hours float64 }
type Backtest struct { Days []BacktestDay; Gaps []Gap; Fired int }
func (e *Evaluator) Backtest(ctx context.Context, projectID int64, p Parsed, now time.Time) (Backtest, error)
// Preview backtests in (validated by Build, and its events resolved as Create does) or, with in nil, the stored alert id.
func (o *Ops) Preview(ctx context.Context, projectID int64, in *Input, id int64) (Backtest, error)
```

**Rules:**
- A stat covers 90 days ending yesterday, a funnel 30.
- Each needed window is answered once: every end day's window, plus the comparison windows by offset (`previous` is the window ending `w` days earlier, `week_ago` the one ending 7 days earlier), deduplicated.
- Each day's outcome comes from `judge`. The backtest does no presence checks and touches no notification state.
- `Fired` counts the runs of breached days. A `no_data` day neither starts nor ends a run.
- Silence: one breakdown over the last 30 days, `dimensions [day, hour]`, metrics `min received_at`, `max received_at`, `limit 1000`. Merge the buckets' `[min, max]` intervals in time order. The gaps longer than `hours` between them, and from the window's start or to `now`, are the `Gaps`, and `Fired = len(Gaps)`.
- Preview needs an engine: `o.eng == nil` is `ErrInvalid` "the event files cannot be queried by this process". The time limit is `Ops.Timeout`.

- [ ] **Step 1: Write the tests.**
  - `TestBacktestStatDays`: `len(Days) == 90`, the last `Day == yesterday`. With `change previous, w = 7`, the requests number `90 + 7`, with no duplicate window.
  - `TestBacktestFiredCountsRuns`: outcomes `ok, breached, breached, no_data, breached, ok, breached` give `Fired == 2`.
  - `TestBacktestFunnel30Days`: `len(Days) == 30`.
  - `TestBacktestSilenceGaps`: the fixture fills every hour of the 30 days, except that the last day has events only at 00–05h and 14–23h, and `now` is just after its 23h bucket. `hours 6` gives one gap of about 8–9h. `hours 12` gives none. With no events at all, one gap covers the whole window.
  - `TestPreviewByDefinitionAndById`: both answer. An invalid definition is `ErrInvalid`. A renamed event is `ErrInvalid`. A nil engine is `ErrInvalid`. Another project's id is `ErrNotFound`.

- [ ] **Step 2: Run them to see them fail** — `go test ./internal/alerts/... -run 'Backtest|Preview'`. Expected: FAIL.
- [ ] **Step 3: Implement `backtest.go`.**
- [ ] **Step 4: Run the tests** — `go test ./internal/alerts/...`. Expected: PASS.
- [ ] **Step 5: Commit** — `git add internal/alerts && git commit -m "feat(alerts): backtest an alert over past days, and silence gaps" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`

---

### Task 8: The loop

**Files:**
- Create: `internal/alerts/runner.go`, `internal/alerts/runner_test.go` (a real store via `storetest.Open`; a fake evaluate function and a fake notifier)

**Interfaces:**
- Consumes: Task 2's `RecordAlertEvaluation`, `ListEvaluableAlerts`, `ResetAlertsOfArchivedProjects`; Task 3's `inbox.AlertObservation`, `AlertKey`; Task 6's `Evaluator`, `message`.
- Produces:

```go
type RunnerStore interface {
	ListEvaluableAlerts(ctx context.Context) ([]store.Alert, error)
	RecordAlertEvaluation(ctx context.Context, r store.AlertResult) (bool, error)
	ResetAlertsOfArchivedProjects(ctx context.Context) (int, error)
}
type Notifier interface { // *inbox.Tracker
	Alert(ctx context.Context, o inbox.AlertObservation)
	KeepAlerts(ctx context.Context, live map[inbox.AlertKey]int64)
}
func NewRunner(st RunnerStore, ev *Evaluator, n Notifier, logger *slog.Logger) *Runner
func (r *Runner) PassDone(day civil.Date) // records the day and the time it arrived (r.now()) under a mutex; never blocks, so a pass ending after Run has stopped is harmless
func (r *Runner) Tick(ctx context.Context, now time.Time)
func (r *Runner) Run(ctx context.Context) // Tick every minute until ctx ends
```

`Runner` has an unexported seam `evaluate func(context.Context, store.Alert, time.Time) Outcome` for tests. Constants: `maxFails = 3`, `retryEvery = time.Hour`, `silenceEvery = time.Hour`.

**Each tick:**
1. Call `ResetAlertsOfArchivedProjects`.
2. `ListEvaluableAlerts`. If it fails, log and end the tick: never call `KeepAlerts` with a map built from a failed list, which would drop every state.
3. The evaluable set is the alerts neither paused nor snoozed (`SnoozedUntil > now`). Pass it to `KeepAlerts(map[{ProjectID, ID}]Revision)`.
4. Each evaluable alert is due when:
   - a check is pending (`CheckPending()`), or
   - it is silence, and `LastEvaluatedAt` is empty or ≥ `silenceEvery` old, or
   - it is a metric alert, `LastDay < yesterday`, `RetryAt` is empty or ≤ now, and either a `PassDone` for today arrived at or after 03:00 UTC today, or `hour >= 4`.
5. Evaluate each due alert in turn and build the `store.AlertResult`:
   - Success (ok, breached, no_data, broken): `State = outcome`, `Reason`, `Fails 0`, `RetryAt ""`. A metric alert gets `LastDay = To` only when the schedule rule (the third bullet of step 4) holds. A check request served before the pass leaves `LastDay` alone, so the after-pass evaluation still runs; the tracker's once-per-`Day` rule absorbs the repeat.
   - `AlertError`: `Fails+1` and `RetryAt = now+1h`. When `Fails >= maxFails`, `State = broken`, `Reason = "failed 3 times: <err>"`, and for a metric alert `LastDay = To` when the schedule rule holds, so the next try is the next day's evaluation after the pass (a check request still tries at once). Otherwise `State ""`.
6. Only when the record applied (`true`) and the outcome is ok, breached, broken or no_data, call `Notifier.Alert` (no_data only clears a broken mark):
   - `Daily = kind != silence`, `Day = To` (or `""`).
   - Fields: `value` and `detail` from `message`, or `reason` for broken.
   - For an `AlertError` that turned broken, the observation's `Outcome` is broken.

Log a store or list failure and go on.

- [ ] **Step 1: Write the tests** (fake clock values, `storetest.Open`, a project created through the store). Every alert is created with `CreateAlert`'s `now` at the test's fake time, so its creation check is pending. Except in `TestRunnerCheckBeforeThePassKeepsLastDay`, which asserts that check, clear it before the asserted schedule (`UPDATE alerts SET check_requested_at=''` with `execAll`-style raw SQL through `ExecForTest`, or one tick that serves it).
  - `TestRunnerMetricWaitsForThePass`: at 02:30 with `PassDone(today)`, nothing is evaluated. `PassDone(today)` at 03:04, then a tick at 03:05, evaluates one alert with `LastDay == yesterday`. The next tick at 03:06 evaluates nothing.
  - `TestRunnerFallbackAtFour`: without `PassDone`, 03:30 evaluates nothing and 04:00 evaluates once.
  - `TestRunnerCatchesUpOnceAfterDowntime`: `LastDay` is two days old. Boot at 02:00 with `PassDone(today)` runs nothing, and neither does a tick at 03:10 (the boot signal came before 03:00). `PassDone(today)` at 03:20 followed by a tick runs exactly one evaluation with `To == yesterday`. Another `PassDone(today)` at 03:25 followed by a tick runs none.
  - `TestRunnerServesCheckRequest`: a metric alert already through yesterday, with `CheckRequestedAt = now-30s`, is evaluated at the next tick, after which `CheckPending() == false`.
  - `TestRunnerCheckBeforeThePassKeepsLastDay`: an alert created at 01:00 is evaluated at 01:01 (its check), with `LastDay` still `""`. It is evaluated again at 03:05 after a `PassDone(today)` at 03:04, which sets `LastDay = yesterday`.
  - `TestRunnerKeepsStatesWhenTheListFails`: a store wrapper whose `ListEvaluableAlerts` fails makes the tick call neither `KeepAlerts` nor `Alert`.
  - `TestRunnerSilenceHourly`: evaluated at t0, not at t0+59m, again at t0+60m.
  - `TestRunnerRetriesThenBroken`:
    - Three `AlertError`s an hour apart give `Fails` 1 and 2 with the state kept, then `broken` on the third.
    - The notifier gets exactly one observation, broken, on the third.
    - A tick at +30m after the first failure evaluates nothing.
    - After the third failure `LastDay == yesterday`, and a tick at +1h evaluates nothing (no hourly retries past that day).
  - `TestRunnerPassesNoData`: `no_data` stores the state and calls `Alert` with `Outcome no_data`. `AlertError` below `maxFails` calls nothing.
  - `TestRunnerEndsStatesOfEditedAlerts`:
    - After an alert is breached and notified at rev 1, `UpdateAlert` with a new `Def` (rev 2, pending) makes the next tick's `KeepAlerts` carry rev 2.
    - Pausing removes the alert from `KeepAlerts`.
    - An evaluation whose record returns `false` (an edit landed meanwhile) does not call `Alert`.
  - `TestRunnerResetsArchivedProjectAlerts`: an archived project's breached alert reads `pending` after a tick, is never evaluated, and is not in `KeepAlerts`.
  - `TestRunnerSkipsPausedAndSnoozed`.

- [ ] **Step 2: Run them to see them fail** — `go test ./internal/alerts/... -run Runner`. Expected: FAIL.
- [ ] **Step 3: Implement `runner.go`.**
- [ ] **Step 4: Run the tests** — `go test ./internal/alerts/...` (and once `go test -race ./internal/alerts/... -run Runner`). Expected: PASS.
- [ ] **Step 5: Commit** — `git add internal/alerts && git commit -m "feat(alerts): evaluate alerts daily after the pass and silence hourly, serving check requests" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`

---

### Task 9: MCP tools, REST routes, the cap in `limits`, and the docs

**Files:**
- Create: `internal/api/ops_alerts.go`, `internal/api/ops_alerts_test.go`
- Modify:
  - `internal/api/ops_read.go`: `host.alerts *alerts.Ops`; `h.registerAlerts(r)` after `registerNotifications`.
  - `internal/api/server.go`: `Build` and `NewHandler` gain a trailing `al *alerts.Ops`.
  - `internal/api/seed_test.go`: `newBareHost`, the one host constructor, sets `alerts: alerts.NewOps(st, testProjects{reg}, nat, hostPresence{files, nat})`. `testProjects` is a small `_test.go` adapter over `*manage.Registry` (it repeats `app`'s, which `api` cannot import).
  - `internal/api/server_test.go` and `oauth_e2e_test.go`: all six test calls get a trailing `nil`: `NewHandler` at `server_test.go:52`, `:216` and `oauth_e2e_test.go:269`; `Build` at `server_test.go:331`, `:386` and `oauth_e2e_test.go:276`.
  - `internal/api/ops_forms.go`: `optionalTime`'s two messages name no field ("must be an RFC 3339 time or null", "%q is not an RFC 3339 time such as 2026-10-09T17:00:00Z"), so `snoozed_until` reuses it. No test pins the old text (checked).
  - `internal/api/ops_limits.go` (+ `_test.go`), `internal/api/expose_test.go` (73 → 81), `internal/api/docs_sync_test.go`, `internal/app/app.go` (two call sites pass `nil` for now; Task 10 passes the real one), `docs/twillingate.md`.

**Interfaces:**
- Consumes: Task 5/7's `Ops` methods, `alerts.MaxLive`, `store.AlertStates`, `store.AlertOutcomes`.
- Produces: the routes below. Every one maps refusals through `writeError`. A nil `h.alerts` refuses `invalid` "alerts are not available in this process".

| Tool | Method and path | Annotations |
| --- | --- | --- |
| `list_alerts` | `GET /api/projects/{project_id}/alerts` (`archived`) | `ro` |
| `create_alert` | `POST /api/projects/{project_id}/alerts`, 201 | `write` |
| `preview_alert` | `POST /api/projects/{project_id}/alerts/preview` (a definition, or `id`) | `ro` |
| `update_alert` | `PATCH /api/projects/{project_id}/alerts/{id}` | `write` |
| `archive_alert` | `POST /api/projects/{project_id}/alerts/{id}/archive` | `idem` |
| `restore_alert` | `POST /api/projects/{project_id}/alerts/{id}/restore` | `idem` |
| `check_alert` | `POST /api/projects/{project_id}/alerts/{id}/check` | `write` |
| `alert_history` | `GET /api/projects/{project_id}/alerts/{id}/history` (`limit`, `cursor`) | `ro` |

- Inputs:
  - The input fields are the `alertOut` names in "REST shapes" above. `query` is `*native.Query`, and `where` is `*[]native.Filter`.
  - `read` and `condition` are api-side structs with `jsonschema` tags, converted to the store types.
  - `snoozed_until` is an `optionalTime`: absent keeps, `null` clears, an RFC 3339 time snoozes.
  - The `constrain` of `create_alert`, `update_alert` and `preview_alert` applies `queryEnums` to `query`, as `query` does. The descriptions say the read and condition rules in a sentence each, and name the four templates with their fields. `create_alert`'s also says `window_days` defaults to 1, and to 7 for `conversion`.
- Limits: `groupAlerts = "alerts"`. `limitsFrom` appends `limitOut{Group: groupAlerts, Name: "Live alerts per project", Value: alerts.MaxLive, Description: "create_alert and restore_alert refuse past it (conflict); archive one first"}`. The `Group` jsonschema text becomes "retention, ingest or alerts".

- [ ] **Step 1: Write the tests.**
  - `TestAlertToolsOverMCP`:
    - `create_alert` `{project_id: 1, name: "Signups", template: "event_count", fields: {event: "signup"}, condition: {type: "threshold", op: "below", value: 5}}` returns `state "pending"`, `kind "stat"`, `check_pending true` and `warnings` naming `signup`.
    - `list_alerts` has it with `max_live 100`.
    - `update_alert` `{name: "Signups/day"}` returns the new name and `check_pending` still true (a rename requests nothing).
    - `check_alert` keeps `check_requested_at`.
    - `archive_alert` makes `list_alerts` empty; `archived: true` lists it; `restore_alert` makes it live again.
    - `alert_history` returns `evaluations: []`.
  - `TestAlertRoutes`:
    - `POST …/alerts` answers 201.
    - A duplicate name answers 409 `conflict`.
    - `PATCH …/alerts/{id}` on another project's id answers 404.
    - An invalid condition answers 400 `invalid`.
    - `GET …/alerts/{id}/history?cursor=x` answers 400.
  - `TestPreviewAlertTool`: over the seeded files (`newTestHost`), a `event_count` on `$page_view` answers 90 `days` ending yesterday.
  - `TestLimitsReportsTheAlertCap`: one limit in group `alerts` with value 100 and no setting.
  - Updates:
    - `TestLimitsHasNoCaps` allows `groupAlerts`.
    - `TestLimitsReportsTheLimitsInForce`'s fixed-limit loop skips the alerts row and asserts it separately.
    - `TestEveryToolChoosesATransport` expects 81.
  - `TestDocumentMatchesAlertVocabularies` (`docs_sync_test.go`): the first cells of `### Alert states` equal `store.AlertStates`, and those of `### Evaluation outcomes` equal `store.AlertOutcomes`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/api/ -run 'Alert|Limits|EveryToolChoosesATransport|TestDocument'`
Expected: FAIL.

- [ ] **Step 3: Implement `ops_alerts.go`, the limits change and the `Build`/`NewHandler` parameter.**
- [ ] **Step 4: Write the docs, in the same commit.** In `docs/twillingate.md`:
  - A new `## Alerts` section after `## Notifications`. It covers:
    - what an alert watches (stat or funnel and its read, with the volume metric);
    - conditions and `window_days`, and `min_volume`;
    - the evaluation schedule (daily after the 03:00 pass for complete days through yesterday, one catch-up; silence hourly on `received_at`; check requests served within a minute);
    - broken and `no_data` (the reference checks);
    - pause, snooze and edit reset;
    - the four templates with their fields;
    - the backtest (silence gaps by sent hour, an approximation);
    - the cap of 100 live alerts, unique live names, and the purge after `RETENTION_ARCHIVED_DAYS`.
  - Put every table whose first cell is backticked (templates, states, outcomes) under its own `###` heading. `section()` ends `### Delivery` only at the next `### `, so such a table placed before the first `###` of `## Alerts` would join the delivery table and fail `TestDocumentMatchesNotificationVocabularies`. Use `### Templates` (its `conversion` row says the window defaults to 7 days), `### Alert states` and `### Evaluation outcomes`.
  - The section also has a tools table in the Notifications section's shape (Operation | CLI | MCP tool | Tool arguments) with all eight tools, `### Alert states` (`| \`pending\` | … |` rows in `store.AlertStates` order) and `### Evaluation outcomes`.
  - The count sentence becomes "eighty-one tools: the thirty-four below, the twelve in [Collect form submissions], the three in [Notifications], the eight in [Alerts](#alerts), and twenty-four …".
  - Add eight rows to `### HTTP API` (`| \`GET\` | \`/api/projects/{project_id}/alerts\` | \`list_alerts\` |` …).
  - The `limits` row: three groups, and "Nothing is capped" becomes "Nothing in ingest is capped; `alerts` holds the live alerts a project may have".
  - State that the Alerts tab comes later: do not document a UI that does not exist.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/api/... && go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit** — `git add internal/api internal/app/app.go docs/twillingate.md && git commit -m "feat(api): alert tools and routes, and the live-alert cap in limits" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`

---

### Task 10: Wiring in `app`

**Files:**
- Create: `internal/app/alerts.go`, `internal/app/alerts_test.go`
- Modify: `internal/app/app.go`

**Interfaces:**
- Consumes: `alerts.NewOps`, `alerts.NewEvaluator`, `alerts.NewRunner`, `jobs.Runner.OnPassDone`, `inbox.NewTracker`.
- Produces: `type alertProjects struct{ reg *manage.Registry }` implementing `alerts.Projects` through `reg.Snapshot(ctx)`:
  - `Project` gives name, `Archived` and ok.
  - `ResolveEvent` is the snapshot's.
  - `Rules` is `Snapshot(ctx).Rules()`.

**Wiring in `Serve`:**
- After `ops.Presence` is set: `al := alerts.NewOps(st, alertProjects{reg}, nat, ops.Presence)` with `al.Timeout = cfg.Console.QueryTimeout`. Pass `al` to both `api.Build` and `api.NewHandler`, replacing Task 9's `nil`s.
- Declare `var tracker *inbox.Tracker` and `var alertRunner *alerts.Runner`. In an `if archiveLog != nil` block placed before `jobs.New`:
  - Create the `tracker` (moved up from the monitor block, which reuses it).
  - Create `alertRunner = alerts.NewRunner(st, alerts.NewEvaluator(nat, alertProjects{reg}, ops.Presence, cfg.Console.QueryTimeout), tracker, logger)`.
- In the existing `if archiveLog != nil { runner.WithArchive(…); runner.WithAttribution(…) }` block after `jobs.New` (`app.go:202`), add `runner.OnPassDone(alertRunner.PassDone)`. It runs before `go runner.Run`, so the boot pass signals it. At shutdown `stopMonitor` stops the alert runner before `stopJobs`; `PassDone` never blocks, so a pass ending then is harmless.
- In the monitor block add `wg.Go(func() { alertRunner.Run(monCtx) })`, so the runner stops before the archive log closes, as the monitor does.
- A console-only process builds `al` and no runner.

- [ ] **Step 1: Write the tests.**
  - `TestAlertProjectsReadsTheRegistry`: over a store with a project, an archived project and a rename rule. `Project` returns the name and archived flag. `ResolveEvent` maps the renamed name. `Rules(ctx).Events` has the rule. An unknown id gives `ok == false`.
  - `var _ alerts.Presence = (*filePresence)(nil)`, a compile-time check in `alerts.go`.
- [ ] **Step 2: Run them to see them fail** — `go test ./internal/app/ -run AlertProjects`. Expected: FAIL.
- [ ] **Step 3: Implement the adapter and the wiring.**
- [ ] **Step 4: Run** `go test ./internal/app/... ./internal/archtest/... && go build ./...`. Expected: PASS.
- [ ] **Step 5: Commit** — `git add internal/app && git commit -m "feat: run alerts in the process that writes the event files" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`

---

### Task 11: Verify the whole branch

- [ ] **Step 1:** `make check`. Expected: vet clean, `sqlcheck` and `archtest` pass, total coverage ≥ 90% and `internal/store`, `internal/api` ≥ 90%. Below that, add tests to the package that dropped; do not lower the gate.
- [ ] **Step 2:** `gofmt -l ./internal` prints nothing. `git grep -n "TODO\|stub" internal/alerts` prints nothing.
- [ ] **Step 3:** Push `feat/alerts` and open the PR. Its title is `feat: alerts on a metric over MCP and REST, with breached, recovered and broken notifications` and its body ends with the Claude Code line. Wait for CI and fix until green. Then run the `agent-review` skill (the global CLAUDE.md requires it) and address what it finds.

---

## Self-review notes

- **Spec coverage.**
  - Model, reads and volume: T4.
  - Conditions, window and min_volume: T4, T6.
  - States: T1, T5, T8.
  - Broken: T5 (at save), T6 (at evaluation), T8 (retries).
  - Project archive: T1, T2, T8.
  - Evaluation schedule and catch-up: T2 (hook), T8.
  - Process placement: T10.
  - Check requests: T1, T5, T8.
  - Preview: T7.
  - History and its 400-day prune: T2, T5.
  - Notifications and reminders: T3, T8.
  - Templates: T4.
  - Tools, routes, cap, purge and unique names: T1, T2, T9.
  - Docs: T3, T9.
  - Rank: T4.
  - Out of scope here (PR 2/3): the Alerts tab, the wizard, `web/` types, e2e specs.
- **Type names used across tasks:** `store.AlertResult`, `store.AlertEdit`, `alerts.Input`, `Patch`, `Parsed`, `Outcome`, `View`, `Backtest`, `inbox.AlertObservation`, `inbox.AlertKey`, `alerts.MaxLive`, `OnPassDone`/`PassDone`.
