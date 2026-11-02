# Availability Monitoring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One HTTP availability monitor per project, plus daily certificate and domain-expiry checks, recorded in a new quota-free `checks` event family and shown on an "Availability" system dashboard.

**Architecture:** A new surface package `internal/monitor` (rank 2, beside `jobs`) runs its own loop, reads enabled monitors from SQLite and the project origins from the `manage` registry, and appends `checks` rows through the archive log (`WriteEvents`, the forms-attribution path). Settings live in a new `monitors` table edited through audited `manage` ops exposed over MCP, REST, CLI and a web settings section. The dashboard is ordinary `.query` widgets over a new native `checks` table.

**Tech Stack:** Go 1.25 (stdlib `net/http`, `crypto/tls`, `net`), `golang.org/x/net/publicsuffix` (new, Go team), modernc SQLite, React + TypeScript (web/).

**Spec:** `docs/superpowers/specs/2026-10-31-availability-monitoring-design.md` (read it whole, including "Amended during planning").

## Global Constraints

- Follow `CLAUDE.md`: layering (archtest), typed refusals via `errors.Is`, docs updated in the same commit per its table, Conventional Commits with scopes from the tree, `make check` green before each commit that touches Go.
- Family name `checks`; row names `uptime`, `cert`, `domain`; attributes `up`, `status`, `reason`, `error`, `expires`, `issuer`, `valid` (no `$`).
- `reason` vocabulary, closed: `dns`, `connect`, `timeout`, `tls`, `http_4xx`, `http_5xx`, `redirect`, `content`, `blocked`.
- `MONITOR_INTERVAL`: integer seconds, default 120, valid 10..3600; a fixed interval, not a floor.
- Check timeout: 10 s, or `interval - 1s` when the interval is shorter than 11 s. Body read limit 1 MB. Max 10 redirect hops. `contains` is a case-sensitive substring.
- Ingest refuses `family: "checks"`. `checks` never counts in `usage`, `limits`, `list_attributes`, event-name lists, or event/attribute rules.
- The web follows the CLAUDE.md Web UI rules (pointer cursor via real buttons/switches, nothing scrolls sideways at 360px).
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. A redirect from `https://example.com` to `https://www.example.com` when only the apex is listed → `reason=redirect`, `error` names the target and says to add it; the hop is never requested (Task 6 test `TestRedirectOffOriginsNotFollowed`).
2. An origin whose DNS answer is `127.0.0.1`, `10.x`, `169.254.169.254`, `100.64.x` or `::1`, including on a redirect hop and after a DNS change between checks → `reason=blocked`, no connection made (Task 6 `TestPrivateAddressBlocked`, dial-time control).
3. Removing from the project the origin the monitor uses → the monitor is disabled in the same transaction and audited (Task 4 `TestUpdateProjectDisablesMonitorOnOriginRemoval`).
4. A site that stops sending events while the monitor runs → `usage.last_received_at` and `limits.events_30d` do not move (Task 1 `TestEventsTableLeavesChecksOut`, Task 5 `TestUsageIgnoresChecks`).
5. Hundreds of enabled monitors at 30 s → checks spread over the interval, bounded concurrency, a slow site never delays others or overlaps its own previous check (Task 6 `TestSchedulerSpreadsAndSkipsOverlap`).

---

### Task 1: The `checks` family in store, ingest and native queries

**Files:**
- Modify: `internal/store/store.go:11-21` (add `FamilyChecks Family = "checks"`; fix comments).
- Modify: `internal/server/ingest.go:366-410` (`resolveFamily`: explicit `case string(store.FamilyChecks)` refusal, message `family "checks" is written by the server only`, styled like the `$form_submit` refusal).
- Modify: `internal/native/query.go:343-346` (`Tables` += `"checks"`, `AttributeTables` += `"checks"`), `internal/native/attributes.go:219-222` (the `events` expansion stays `{"product","measures"}`), `internal/native/columns.go:41-42` (`hour`, `weekday` += `"checks"`), `internal/native/engine.go:552-558` and the scan paths (`logs.go:82`, `scan.go` family filtering) so table `events` reads every family **except** `checks`.
- Modify: `internal/api/eventcount.go:102-103` comment ("every family but checks").
- Docs: `docs/twillingate.md` families table (~893-907, 1141-1160: `checks` row, "written by the server only", unknown-family bullet), structured-queries `table` row (~1751, bound by `docs_sync_test.go:676`), computed-column lists (~1800). Do **not** add `checks` to `schemaFiles` (`internal/api/resources.go`) — the SQL engine lacks it (spec, Amended).
- Test: `internal/server/ingest_test.go` (or where family tests live), `internal/native/*_test.go`.

**Interfaces:**
- Produces: `store.FamilyChecks`; native table `checks` with columns as `product`/`measures` share (`value`, `attr_<key>`, `event_name`, `ts`, `day`, `hour`, `weekday`).

- [ ] **Step 1: Failing tests.** `TestIngestRefusesChecksFamily`: an ingest batch with one `{"family":"checks","name":"uptime"}` event is refused for that event with the message above. `TestEventsTableLeavesChecksOut`: archive with 2 product + 3 checks rows; native `stat count` on `events` = 2, on `checks` = 3; `breakdown` on `checks` by `attr_reason` with `where attr_up eq "0"` returns the reasons; `ratio` metric on `checks` works. `TestChecksNotInAttributeKeys`: `list_attributes`-style `Output:"keys"` on `events` never lists `up`/`reason`.
- [ ] **Step 2: Run** `go test ./internal/native/ ./internal/server/ -run 'Checks|ChecksFamily'` → FAIL.
- [ ] **Step 3: Implement** the edits above. For `events`, the cleanest place is the family predicate of the scan (`scan.family()` returns `""` for events today): add an exclusion so both the log and Parquet paths (row-group stats too) skip `checks`.
- [ ] **Step 4: Run** the same tests → PASS; then `make check`.
- [ ] **Step 5: Commit** `feat(native): add the checks family, written by the server only`.

### Task 2: `MONITOR_INTERVAL`

**Files:** `internal/config/config.go` (new `MonitorConfig{Interval time.Duration}` field `Config.Monitor`, read with `e.num("MONITOR_INTERVAL", 120)` as seconds, validated 10..3600 in `validate()` with `config: MONITOR_INTERVAL must be between 10 and 3600 seconds`), `internal/config/config_test.go`, `.env.example`, `deploy/ship/twillingate.env.example`, `docs/deployment.md` env table (~151-162): "seconds between availability checks of each enabled monitor; fixed, 10 to 3600, default 120").

- [ ] Failing test: bad values `"9"`, `"3601"`, `"abc"` refused; unset gives `120*time.Second`; `"30"` gives 30 s. `TestDeploymentDocumentsEveryEnvVar` and `TestExamplesLoad` must pass.
- [ ] Implement, run `go test ./internal/config/ ./internal/api/ -run 'Config|Deployment|Examples'`, commit `feat(config): add MONITOR_INTERVAL`.

### Task 3: `monitors` table and store methods

**Files:**
- Create: `internal/store/sqlite/migrations/044_monitors.sql` (copy the style of `040_project_attributes.sql`):
  `monitors(project_id INTEGER PRIMARY KEY REFERENCES projects(id), enabled INTEGER NOT NULL DEFAULT 0, origin TEXT NOT NULL DEFAULT '', contains TEXT NOT NULL DEFAULT '', cert_expires_at TEXT NOT NULL DEFAULT '', cert_issuer TEXT NOT NULL DEFAULT '', cert_valid INTEGER NOT NULL DEFAULT 0, cert_error TEXT NOT NULL DEFAULT '', cert_checked_at TEXT NOT NULL DEFAULT '', domain_expires_at TEXT NOT NULL DEFAULT '', domain_checked_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))) WITHOUT ROWID`.
- Create: `internal/store/monitors.go` (types), `internal/store/sqlite/monitors.go`, `internal/store/sqlite/monitors_test.go`.
- Modify: `internal/store/store.go` Store interface; `internal/store/sqlite/registry.go:266` `projectTables` += `"monitors"`; `deploy/UPGRADES.md` only if needed (plain CREATE TABLE: a one-line note is enough).

**Interfaces (Produces):**
```go
type Monitor struct {
    ProjectID       int64
    Enabled         bool
    Origin, Contains string
    CertExpiresAt   string // RFC 3339 UTC, "" unknown
    CertIssuer      string
    CertValid       bool
    CertError       string
    CertCheckedAt   string
    DomainExpiresAt string // YYYY-MM-DD, "" unknown
    DomainCheckedAt string
}
type MonitorSettings struct{ Enabled bool; Origin, Contains string }
type CertState struct{ ExpiresAt, Issuer, Error, CheckedAt string; Valid bool }
type DomainState struct{ ExpiresAt, CheckedAt string }

GetMonitor(ctx, projectID int64) (Monitor, error)              // no row → Monitor{ProjectID: id}, nil; no project → ErrNotFound
SetMonitor(ctx, projectID int64, s MonitorSettings, a AuditEntry) (Monitor, error) // upsert; an origin change clears cert_* and domain_*
ListEnabledMonitors(ctx) ([]Monitor, error)                   // enabled rows of live (not archived) projects, by project_id
SetMonitorCert(ctx, projectID int64, origin string, c CertState) error     // no-op when the row's origin != origin (raced an edit)
SetMonitorDomain(ctx, projectID int64, origin string, d DomainState) error // same guard
```
Use `d.tx` + `audit(ctx, tx, a)` (not `auditAndBump`). State writers are not audited.

- [ ] Failing tests: round trip; origin change clears state; `ListEnabledMonitors` skips disabled and archived projects; state write with a stale origin is ignored; unknown project → `errors.Is(err, store.ErrNotFound)`; `TestProjectTablesMatchesSchema` passes; delete project removes the row.
- [ ] Implement; `go test ./internal/store/... ./internal/sqlcheck/` → PASS; commit `feat(store): add the monitors table`.

### Task 4: `manage` monitor operations

**Files:** Create `internal/manage/monitor.go`, `internal/manage/monitor_test.go`; modify `internal/manage/store.go` (add the Task 3 methods to `manage.Store`), `internal/manage/ops.go:188-211` (`UpdateProject`), fakes in manage tests.

**Interfaces:**
```go
// MonitorOrigins is the subset of origins a monitor may use: exact http:// or https:// origins, no "*" anywhere.
func MonitorOrigins(origins []string) []string
type MonitorSpec struct{ Enabled *bool; Origin, Contains *string } // nil keeps the current value
func (o *Ops) Monitor(ctx context.Context, projectID int64) (store.Monitor, error)
func (o *Ops) SetMonitor(ctx context.Context, actor string, projectID int64, spec MonitorSpec) (store.Monitor, error)
```
Rules: enabling with no origin set picks the only eligible origin, else `ErrInvalid` "pick one of the project's origins"; an origin not in `MonitorOrigins(project origins)` → `ErrInvalid`; `contains` trimmed, ≤ 200 chars (my call), else `ErrInvalid`; archived project → `ErrInvalid` like other edits; audit action `monitor.update`, subject `idSubject(id)`, detail = changed fields.
`UpdateProject`: when the new origins drop the monitor's origin, set `Enabled=false` (keep origin text) in the same call, audited `monitor.disable` with detail `origin removed`. Prefer one transaction (extend the store's `UpdateProject` or add a combined method); if two calls, disable first.

- [ ] Failing tests: `TestSetMonitorPicksTheOnlyOrigin`, `TestSetMonitorRefusesWildcardAndForeignOrigin`, `TestMonitorOriginsFilters` (`*`, `https://*.x.com`, `tauri://localhost`, `null`, `http://a.com:8080` kept, `https://b.com` kept), `TestUpdateProjectDisablesMonitorOnOriginRemoval`.
- [ ] Implement; `go test ./internal/manage/`; commit `feat(manage): add monitor settings`.

### Task 5: MCP tools, REST routes, CLI, docs

**Files:** Create `internal/api/ops_monitor.go` (+ `_test.go`), register from `ops_read.go:391` `register`; create `cmd/twillingate/monitor.go` (+ `_test.go`); docs `docs/twillingate.md` (tool table, Managing list ~1505, tool-count sentence ~1442, HTTP API rows ~1521, a short "Monitoring" section: what is checked, the `checks` table, the reason vocabulary, quota note); `internal/api/ops_usage.go` comment only.

**Interfaces (Produces, used by web Task 8):**
- `get_monitor` — `GET /api/projects/{project_id}/monitor`, `ro` → `monitorOut`.
- `update_monitor` — `PATCH /api/projects/{project_id}/monitor`, `idem`, body `{enabled?: bool, origin?: string, contains?: string}` → `monitorOut`.
- `monitorOut` JSON: `{project_id, enabled, origin, contains, eligible_origins: [string], interval_seconds, cert: {expires_at, issuer, valid, error, checked_at}, domain: {expires_at, checked_at}}` (empty strings for unknown).
- CLI: `twillingate monitor get -project-id N` and `twillingate monitor set -project-id N [-enable|-disable] [-origin URL] [-contains TEXT]`, actor `"cli"`, exit codes as `project update`.

- [ ] Failing tests: REST and MCP parity for both tools; 400 on a foreign origin; 404 on unknown project; CLI round trip; `TestDocumentNamesEveryTool`, `TestDocumentMatchesRoutes`; `TestUsageIgnoresChecks` (seed checks rows: usage samples and `last_received_at` unchanged; `limits.events_30d` unchanged).
- [ ] Implement; `make check`; commit `feat(api): expose monitor settings over MCP, REST and the CLI`.

### Task 6: `internal/monitor` — the checker and its loop

**Files:** Create `internal/monitor/{monitor.go,check.go,dial.go,cert.go,rdap.go}` with tests; modify `internal/app/app.go` (build and run next to jobs inside the `archiveLog != nil` block, stop before the log closes, include in `stopBackground`), `internal/archtest/archtest_test.go` (rank 2), `CLAUDE.md` layout line, `go.mod` (`golang.org/x/net` for `publicsuffix`).

**Interfaces:**
```go
type Store interface {
    ListEnabledMonitors(ctx) ([]store.Monitor, error)
    SetMonitorCert(ctx, int64, string, store.CertState) error
    SetMonitorDomain(ctx, int64, string, store.DomainState) error
}
type EventLog interface{ WriteEvents(ctx context.Context, evs []store.Event) error }
type Origins interface{ RedirectAllowed(projectID int64, target, reqOrigin string) bool } // satisfied by the manage snapshot; app adapts
type Result struct{ Up bool; Status int; Reason, Error string; Elapsed time.Duration }
func Check(ctx context.Context, c *http.Client, origin, contains string, allowed func(target string) bool) Result
func New(st Store, origins Origins, log EventLog, interval time.Duration, logger *slog.Logger) *Runner
func (r *Runner) Run(ctx context.Context)
```
Decisions:
- **Dialer:** `net.Dialer.Control` refuses loopback, private (RFC 1918, fc00::/7), link-local (169.254/16, fe80::/10), CGNAT 100.64/10, unspecified, multicast → `Reason "blocked"`. Tests inject an allow-loopback seam (unexported field) to reach `httptest` servers.
- **Redirects:** `CheckRedirect` refuses any hop where `allowed(target)` is false (`error`: `redirected off-site to <origin> — add it to the project's origins if it is yours`), and the 11th hop (`too many redirects`); both `redirect`.
- **Classification:** DNS errors (`*net.DNSError`) → `dns`; `context.DeadlineExceeded`/timeouts → `timeout`; x509/TLS errors → `tls`; other dial errors → `connect`; 4xx → `http_4xx`; any other non-2xx → `http_5xx`; missing text → `content`. `error` ≤ 200 chars.
- **Request:** `GET origin + "/"`, `User-Agent: twillingate-monitor/<version> (+https://github.com/dmtrkzntsv/twillingate)`, no cookies, keep-alives off.
- **Rows:** `store.Event{ID: uuid.NewV7, ProjectID, Family: FamilyChecks, EventName: "uptime", TS = ReceivedAt = check start UTC, Value: ms, Attributes: up/status/reason/error}`. Daily: `cert` (value = whole days left, attrs `expires`, `issuer`, `valid`, `error`) and `domain` (value nil when unknown, attr `expires`). Batch writes per tick.
- **Schedule:** each monitor's offset = `hash(project_id) % interval`; a worker pool of 16 (my call); a monitor still running skips its next slot. Reload the enabled list every tick (cheap SQLite read). Cert and domain run on enable/origin change (detected as "no `cert_checked_at`") and when the last check is ≥ 24 h old.
- **RDAP:** registrable domain via `publicsuffix.EffectiveTLDPlusOne`; IANA bootstrap `https://data.iana.org/rdap/dns.json` cached 24 h in memory; `GET <base>domain/<name>`, take `events[eventAction=="expiration"].eventDate`; any failure stores `""` (unknown) and is logged at debug. The RDAP and bootstrap fetches use the same private-address dialer.

- [ ] Failing tests (httptest + resolver/dial seams): one per `reason`; `TestRedirectOffOriginsNotFollowed` (the off-site server records zero requests); `TestRedirectWithinOriginsFollowed`; `TestPrivateAddressBlocked` (127.0.0.1, 10.0.0.1, 169.254.169.254, 100.64.0.1, ::1 via a fake resolver); `TestContainsCaseSensitiveWithin1MB`; `TestCertState` (self-signed httptest TLS → valid false, error set; expiry parsed); `TestRDAPExpiry` and `TestRDAPUnknownTLD` with a fake bootstrap; `TestSchedulerSpreadsAndSkipsOverlap` (fake clock); `TestRunnerWritesChecksRows` (fake EventLog gets `checks/uptime` rows with the attributes).
- [ ] Implement; `go test -race ./internal/monitor/`; `make check`; commit `feat(monitor): check availability, certificates and domain expiry`.

### Task 7: Availability system dashboard

**Files:** Create `internal/reporting/system/availability/` (`dashboard.json` id 8, title `Availability`, range `7d`, `sidebar: true`, `project_tab: true`, `group: 1`); widgets as `.json` + `.query`:
- `uptime` stat percent: `ratio` of `count` where `event_name eq uptime` and `attr_up eq "1"` over `count` where `event_name eq uptime`.
- `response` stat number: `avg` of `value`, `uptime` rows, rounded.
- `cert-days` stat number: `latest` of `value` on `cert` rows (title "Certificate expires in (days)").
- `domain-days` stat number: `latest` of `value` on `domain` rows (title "Domain renews in (days)").
- `response-by-day` line: trend `avg(value)` by day, uptime rows.
- `reasons` pie: breakdown by `attr_reason`, `attr_up eq "0"`.
- `failures` table: breakdown by `day`, `attr_reason`, `attr_status` with `count`, `attr_up eq "0"`, newest first.
- `about` markdown (`.md`): what is checked, the interval, that it is set up in Settings → Monitoring.
Modify `internal/reporting/system_test.go` (line 76 list `…, 8 Availability 7d`; group maps at ~155/178; seed `checks` rows in `systemEvents`), `internal/reporting/system_parity_test.go` (`newSystemWidgets` entries `availability/<widget>` with reason "new in availability monitoring"), `web/e2e/app.spec.ts:134` tab count, golden files as regenerated.

- [ ] Failing: `TestSystemDashboards` with the new list; implement; `go test ./internal/reporting/`; commit `feat(reporting): add the Availability system dashboard`.

### Task 8: Web settings section

**Files:** Create `web/src/components/projects/MonitoringSection.tsx` (+ `.test.tsx`); modify `SetupTab.tsx` (after `OriginsSection`, ~line 57), `web/src/lib/api.ts` (`Monitor` type mirroring `monitorOut`; `endpoints.monitor(id)`, `endpoints.updateMonitor(id, body)`), `web/src/lib/queries.ts` (`monitorQuery(id)`), `web/src/hooks/use-project-actions.ts` (`updateMonitor`, invalidating the monitor key; a project update also invalidates it).

Behaviour: a section "Monitoring" with a `Switch` (enabled), a `Select` of `eligible_origins` (hidden when one; a hint when none: "Add an http(s) origin above to monitor it"), an `Input` "Page must contain (optional)", Save. Below, when enabled: "Checked every N s", "Certificate: expires <date> (N days)" or "not checked yet"/error, "Domain: renews <date> (N days)" or "unknown", and a link to the Availability tab. Long origins wrap (`break-all`), single column `grid-cols-1`.

- [ ] Failing vitest: renders state; Save sends `{enabled:true, origin, contains}`; select hidden with one origin; none-eligible hint; days-left text. Run `cd web && npx vitest run src/components/projects/MonitoringSection.test.tsx`.
- [ ] Implement; `cd web && npm run typecheck && npx vitest run`; `make build`; `cd web && npx playwright test e2e/cursor.spec.ts e2e/phone.spec.ts e2e/app.spec.ts`; commit `feat(web): add the monitoring settings section`.

### Task 9: Whole-branch verification

- [ ] `make check` and `cd web && npm run e2e` (note the two pre-existing e2e failures on main from the handoff: "custom range stays open" on the 1st of a month, a flaky stream test; do not fix here).
- [ ] Re-read the spec's goals and anti-goals; confirm each has a test or a documented reason.
