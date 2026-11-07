# Notifications (inbox and delivery) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every notification twillingate produces (site down/still down/up, certificate and domain-lookup failures, expiry warnings, form drafts and submissions) lands in an inbox and is sent, identically, to one optional channel configured by URL.

**Architecture:** The store owns the rows: `notifications` (the inbox and its delivery outcome), `notify_states` (opaque per-key state for failing states and expiry steps) and `form_bursts`. Form notifications are inserted inside the store's own form transactions. A new domain leaf `internal/inbox` renders a stored notification into one `notify.Message` (title, body, link) used by both the API and the channel, tracks failing states and expiry steps for the monitor (`Tracker`), and delivers pending rows (`Dispatcher`). A new generic leaf `internal/shared/notify` parses shoutrrr-style URLs and sends.

**Tech Stack:** Go (stdlib only for the new packages), SQLite via modernc, React + TypeScript web app, Playwright e2e.

**Spec:** `docs/superpowers/specs/2026-11-07-notifications-design.md`

## Global Constraints

- No new third-party Go dependency; `internal/shared/notify` imports the standard library only.
- One static binary, no cgo.
- New packages go into the rank table in `internal/archtest/archtest_test.go`: `internal/shared/notify` rank 0, `internal/inbox` rank 0.
- Refusals typed and matched with `errors.Is` (`store.ErrNotFound`, `store.ErrInvalid` / `manage.ErrInvalid`).
- Every SQL string is a constant or local the `internal/sqlcheck` test can read; a query built at run time goes in its `dynamic` list with the reason.
- Docs change in the same commit as the code, per CLAUDE.md's table (`docs/twillingate.md`, `docs/deployment.md`, `deploy/UPGRADES.md` when upgrade day shows something).
- Commits: Conventional Commits, scopes from CLAUDE.md, imperative, lower case, no trailing period; end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- `make check` passes before every push; Web UI rules (pointer cursor, no sideways scroll at 360px) hold for the new page.
- Inbox and channel messages are identical: one notification, one rendered `notify.Message`, every send its own inbox item.
- Never send form field values anywhere; never log or return the secret parts of `NOTIFY_URL`.

## Review Focus

1. A restart mid-outage: no second "down", and the reminder clock continues from the stored state (Task 4 test `TestTrackerSurvivesRestart`).
2. `NOTIFY_URL` secrets (Telegram token, SMTP password, webhook path tokens) never appear in parse errors, send errors or logs (Task 1 test `TestErrorsHideSecrets`).
3. Project or form names with Markdown/HTML characters (`_`, `*`, `<`, `&`) arrive verbatim: senders send plain text, never a parse mode (Task 1 test `TestTelegramPlainText`).
4. Deleting a project removes its notifications, states and bursts (Task 2 test `TestDeleteProjectDataNotifications`).
5. A channel down for hours: inbox unaffected, each item ends `failed` after the give-up time, newer items are still attempted, the dispatcher never blocks the monitor (Task 5 test `TestDispatcherGivesUp`).

---

### Task 1: `internal/shared/notify` — URL parsing and senders

**Files:**
- Create: `internal/shared/notify/notify.go` (types, `Parse`, errors), `telegram.go`, `ntfy.go`, `slack.go`, `discord.go`, `generic.go`, `smtp.go`
- Test: `internal/shared/notify/notify_test.go` (+ one `_test.go` per sender if it grows)
- Modify: `internal/archtest/archtest_test.go` (rank 0 entry)

**Interfaces:**
- Produces:
  - `type Message struct { Title, Body, Link string }`
  - `type Sender interface { Send(ctx context.Context, m Message) error; Service() string }` — `Service()` is the scheme name for display ("telegram").
  - `func Parse(raw string) (Sender, error)` — error wraps `ErrInvalidURL`, names the bad field, never contains the secret.
  - `var ErrInvalidURL, ErrPermanent, ErrTemporary = errors.New(...)`; a send error wraps exactly one of `ErrPermanent` (4xx except 408/429, auth failure, SMTP 5xx) or `ErrTemporary` (network, timeout, 5xx, 408, 429, SMTP 4xx).
  - `func ParseWith(raw string, c *http.Client) (Sender, error)` — test seam; `Parse` uses a client with a 15s timeout.

URL formats (shoutrrr's):
- `telegram://<token>@telegram?chats=<chat>[,<chat>...]` → POST `https://api.telegram.org/bot<token>/sendMessage`, JSON `{chat_id, text, disable_web_page_preview:true}`, no `parse_mode`.
- `ntfy://[<user>:<pass>@]<host>/<topic>[?scheme=http]` → POST `https://<host>/<topic>`, body = Body, headers `Title`, `Click: <Link>`.
- `slack://hook:<a>-<b>-<c>@webhook` → POST `https://hooks.slack.com/services/<a>/<b>/<c>`, JSON `{text}`.
- `discord://<token>@<webhookid>` → POST `https://discord.com/api/webhooks/<webhookid>/<token>`, JSON `{content}`.
- `generic://<host>[:port]/<path>[?<query>]` (HTTPS; `generic+http://` for plain HTTP) → POST JSON `{"title","body","link"}` to that URL with the query passed through.
- `smtp://[<user>:<pass>@]<host>:<port>/?from=<addr>&to=<addr>[,<addr>]` [`&starttls=no`] → `net/smtp`, STARTTLS required unless `starttls=no`, port 465 means implicit TLS. Subject = Title; body = Body + "\n\n" + Link.

Text-only services render one text: `Title + "\n" + Body + "\n" + Link`, empty parts and their newline left out. That function is `func (m Message) Text() string`.

- [ ] **Step 1: Write failing tests** in `notify_test.go`, one `httptest.Server` per service with `ParseWith(url, srv.Client())` and a test hook to point the base URL at the server (an unexported `base string` field each sender has, set by a `withBase` helper in the test file):
  - `TestParse` table: each scheme above parses; `Service()` returns `"telegram"`, `"ntfy"`, `"slack"`, `"discord"`, `"generic"`, `"smtp"`; bad inputs fail with `errors.Is(err, ErrInvalidURL)`: `""`, `"foo://x"`, `"telegram://@telegram?chats=1"` (no token), `"telegram://t@telegram"` (no chats), `"slack://hook:a-b@webhook"` (two parts), `"smtp://h:25/?from=a@b"` (no to).
  - `TestTelegramPlainText`: sends `Message{Title: "a_b *c*", Body: "<x> & y", Link: "https://c/x"}`; server sees JSON `text == "a_b *c*\n<x> & y\nhttps://c/x"` and no `parse_mode` key; one request per chat.
  - `TestSendErrorsClassified`: server answers 401 → `errors.Is(err, ErrPermanent)`; 429 → `ErrTemporary`; 503 → `ErrTemporary`; closed server → `ErrTemporary`.
  - `TestErrorsHideSecrets`: for `telegram://SECRET123@telegram?chats=1` with a 401 server and with an unreachable server, and for a parse failure of `smtp://u:SECRET123@h:25/?from=a@b`, `strings.Contains(err.Error(), "SECRET123") == false`.
  - `TestNtfyHeaders`, `TestGenericJSON`, `TestSlackAndDiscordBodies`: exact request path and body per the formats above.
  - `TestSMTP`: a minimal fake SMTP server on a `net.Listener` (`starttls=no`) receives `MAIL FROM`, `RCPT TO` for each `to`, and a DATA block whose `Subject:` header equals the title.
- [ ] **Step 2: Run** `go test ./internal/shared/notify/` — FAIL (package missing).
- [ ] **Step 3: Implement** the package. Redact by building errors from the scheme and status, never `url.String()`; wrap `*url.Error` with the host only.
- [ ] **Step 4: Add** `"internal/shared/notify": 0,` to the rank table; run `go test ./internal/shared/notify/ ./internal/archtest/` — PASS.
- [ ] **Step 5: Commit** `feat(shared): add a notify package sending to telegram, ntfy, slack, discord, webhooks and smtp`

### Task 2: Store — notifications, states, bursts; form hooks; migration 046

**Files:**
- Create: `internal/store/sqlite/migrations/046_notifications.sql`, `internal/store/notifications.go`, `internal/store/sqlite/notifications.go`, `internal/store/sqlite/notifications_test.go`
- Modify: `internal/store/forms.go` (`Form.Notify`), `internal/store/store.go` (interface), `internal/store/sqlite/forms.go` (`formColumns`, `scanForm`, `WriteSubmission`, `ApproveForm`, `UpdateForm`, `ExpireDrafts`), `internal/store/sqlite/registry.go:279` (tables list), `internal/store/sqlite/forms_test.go`, `deploy/UPGRADES.md` (046: new tables, existing forms get notify off)

**Interfaces:**
- Produces (package `store`):
  ```go
  // Notification types: the closed vocabulary.
  const (
      NotifyUptimeDown = "uptime_down"; NotifyUptimeStillDown = "uptime_still_down"; NotifyUptimeUp = "uptime_up"
      NotifyCertFailing = "cert_failing"; NotifyCertStillFailing = "cert_still_failing"; NotifyCertRecovered = "cert_recovered"
      NotifyDomainFailing = "domain_failing"; NotifyDomainStillFailing = "domain_still_failing"; NotifyDomainRecovered = "domain_recovered"
      NotifyCertExpiring = "cert_expiring"; NotifyDomainExpiring = "domain_expiring"
      NotifyFormDraftCreated = "form_draft_created"; NotifyFormDraftDiscarded = "form_draft_discarded"; NotifyFormSubmission = "form_submission"
  )
  var NotificationTypes = []string{ /* all 14, in the order above */ }
  const ( DeliveryPending = "pending"; DeliverySent = "sent"; DeliveryFailed = "failed"; DeliveryNone = "none"; DeliverySkipped = "skipped" )
  const FormBurstWindow = 15 * time.Minute

  type Notification struct {
      ID int64; Type string; ProjectID int64; Subject string
      Fields map[string]string // rendering facts; always has "project" (the name at creation) when ProjectID != 0
      CreatedAt time.Time; ArchivedAt *time.Time
      Delivery, DeliveryError string; Attempts int; NextAttemptAt *time.Time
  }
  type NewNotification struct { Type string; ProjectID int64; Subject string; Fields map[string]string; At time.Time }
  type NotifyState struct { ProjectID int64; Kind, Subject, Data string } // Data: JSON owned by internal/inbox
  type NotificationFilter struct { Archived bool; ProjectID int64; Limit int; Before int64 } // Before: id cursor, 0 = newest
  ```
  Store methods (on `store.Store` and `*sqlite.DB`):
  - `AddNotification(ctx, n NewNotification) (int64, error)`
  - `ListNotifyStates(ctx) ([]NotifyState, error)`
  - `PutNotifyState(ctx, s NotifyState, n *NewNotification) error` — upsert state and, if n != nil, insert n, one transaction
  - `DeleteNotifyState(ctx, projectID int64, kind, subject string) error`
  - `ListNotifications(ctx, f NotificationFilter) ([]Notification, error)` — newest first (id desc); archived=false lists active
  - `CountActiveNotifications(ctx) (int, error)`
  - `SetNotificationsArchived(ctx, ids []int64, all, archived bool, at time.Time) (int, error)` — `all` (archive only) archives every active one; ids that match nothing or are already in the target state are skipped; returns the count changed; `all` with `archived=false` is `ErrInvalid`
  - `PendingDeliveries(ctx, now time.Time, limit int) ([]Notification, error)` — `delivery='pending' AND (next_attempt_at IS NULL OR next_attempt_at <= now)`, oldest first
  - `SetDelivery(ctx, id int64, delivery, errText string, attempts int, next *time.Time) error`
  - `PurgeArchivedNotifications(ctx, now time.Time, days int) (int, error)` — deletes archived before now − days
  - `FlushFormBursts(ctx, now time.Time) (int, error)` — see below
  - `UpdateForm` honours `f.Notify` (always written; manage reads before writing)

Migration 046 (constant SQL file, style of 044 with a header comment):
```sql
CREATE TABLE notifications (
    id INTEGER PRIMARY KEY,
    type TEXT NOT NULL,
    project_id INTEGER REFERENCES projects(id),   -- NULL: no project
    subject TEXT NOT NULL DEFAULT '',
    fields TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    archived_at TEXT,
    delivery TEXT NOT NULL DEFAULT 'pending',
    delivery_error TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT
);
CREATE INDEX notifications_active ON notifications(archived_at, id);
CREATE INDEX notifications_pending ON notifications(delivery, id) WHERE delivery = 'pending';
CREATE TABLE notify_states (
    project_id INTEGER NOT NULL REFERENCES projects(id), kind TEXT NOT NULL, subject TEXT NOT NULL,
    data TEXT NOT NULL DEFAULT '{}', PRIMARY KEY (project_id, kind, subject)) WITHOUT ROWID;
CREATE TABLE form_bursts (
    project_id INTEGER NOT NULL REFERENCES projects(id), form TEXT NOT NULL,
    window_start TEXT NOT NULL, pending INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (project_id, form)) WITHOUT ROWID;
ALTER TABLE forms ADD COLUMN notify INTEGER NOT NULL DEFAULT 0;
```
Every insert goes through one helper `insertNotification(ctx, tx, n NewNotification) error` that sets `fields["project"]` from `projects.name` when ProjectID != 0 and not already set.

Form hooks, inside the existing transactions:
- `WriteSubmission`: when the `INSERT INTO forms … ON CONFLICT DO NOTHING` affected one row (a new draft) and the submission is inserted → `form_draft_created` (subject = form, fields `form`). When the submission is inserted, the form is approved, not archived, and `notify=1` → burst logic: no `form_bursts` row, or `window_start <= received − FormBurstWindow` and `pending = 0` → insert `form_submission` with `fields{"form", "count":"1"}` and upsert the row `(window_start=received, pending=0)`; otherwise `pending = pending + 1`.
- `FlushFormBursts(now)`: for rows with `pending > 0 AND window_start <= now − FormBurstWindow`: insert `form_submission` with `count = pending` (subject form, `At = now`), set `window_start = now, pending = 0`. Rows with pending 0 older than one day are deleted.
- `ExpireDrafts`: one `form_draft_discarded` per expired draft (fields `form`). `SetFormArchived` adds none.
- `ApproveForm`: also sets `notify = 1`.

- [ ] **Step 1: Write failing tests** in `notifications_test.go` (copy a migrated DB as the package's other tests do, `newTestDB`):
  - `TestAddAndListNotifications`: add three for project "blog"; `ListNotifications(Archived:false)` returns them newest first with `Fields["project"] == "blog"`, `Delivery == "pending"`; `Limit:2` then `Before: <last id>` pages.
  - `TestArchiveRestoreAll`: archive one by id → 1; `all:true` → archives the rest, `CountActiveNotifications == 0`; restore by id → 1; `SetNotificationsArchived(nil, true, false, …)` → `errors.Is(err, store.ErrInvalid)`.
  - `TestPutNotifyStateWithNotification`: put state + notification; `ListNotifyStates` returns Data verbatim; one notification exists; put again with nil n updates data only.
  - `TestPendingDeliveriesAndSetDelivery`: next_attempt in the future is not listed; SetDelivery sent removes it from pending.
  - `TestPurgeArchivedNotifications`: archived 31 days ago purged with days 30; archived 29 days ago and active ones kept.
  - `TestDeleteProjectDataNotifications`: rows in all three tables for a project are gone after `DeleteProjectData`.
  - In `forms_test.go`: `TestWriteSubmissionDraftCreatedNotification` (first submission to a new form → one `form_draft_created`; second → none); `TestSubmissionNotificationBurst` (approved form, notify on: submissions at t, t+1m, t+2m → one `form_submission` count 1; `FlushFormBursts(t+16m)` → one more with `count == "2"`; a submission at t+40m → a new immediate one); `TestSubmissionNotifyOff` (approved, `UpdateForm` notify false → none); `TestApproveSetsNotify`; `TestExpireDraftsNotifies` (one `form_draft_discarded` per expired draft); `TestMigration046ExistingFormsNotifyOff` (a form present before 046 reads `Notify == false`) — follow the `migrationNNN_test.go` pattern with `snapshotAt`.
- [ ] **Step 2: Run** `go test ./internal/store/... ./internal/sqlcheck/` — FAIL.
- [ ] **Step 3: Implement** migration, types, methods and hooks; add the three tables to the `registry.go` delete list (before `projects`).
- [ ] **Step 4: Run** `go test ./internal/store/... ./internal/sqlcheck/ ./internal/archtest/` — PASS. Fix any fake store elsewhere that must implement the widened `store.Store` (`go build ./... && go vet ./...`).
- [ ] **Step 5: Commit** `feat(store): store notifications, their delivery and form notification hooks`

### Task 3: `internal/inbox` — rendering

**Files:**
- Create: `internal/inbox/render.go`, `internal/inbox/render_test.go`, `internal/inbox/testdata/` (golden file via `internal/shared/golden`)
- Modify: `internal/archtest/archtest_test.go` (`"internal/inbox": 0`)

**Interfaces:**
- Consumes: `store.Notification`, `store.Notify*` constants, `notify.Message`.
- Produces:
  - `func Render(n store.Notification) notify.Message` — `Link` is the console path (relative), e.g. `/projects/3/settings/domains`.
  - `func Absolute(m notify.Message, consoleURL string) notify.Message` — prefixes `Link` with `consoleURL` (no double slash); unchanged when `consoleURL` is "".
  - `func Path(n store.Notification) string`

Copy (fields: `project`, `origin`/`domain`/`form` = subject, `since`, `until`, `reason`, `error`, `days`, `expires`, `count`; times RFC 3339 in fields, rendered `2006-01-02 15:04 UTC`; durations rendered `4h 12m`, `35m`, `2d 3h`):

| Type | Title | Body |
| --- | --- | --- |
| uptime_down | `<origin> is down` | `<Project>: down since <since> (<reason>)` |
| uptime_still_down | `<origin> is still down` | `<Project>: down since <since>, <dur until now>` |
| uptime_up | `<origin> is up` | `<Project>: was down since <since> (<dur>)` |
| cert_failing | `Certificate problem on <origin>` | `<Project>: <error> (since <since>)` |
| cert_still_failing | `Certificate still failing on <origin>` | `<Project>: <error>, since <since>, <dur>` |
| cert_recovered | `Certificate fixed on <origin>` | `<Project>: failing since <since> (<dur>)` |
| domain_failing | `Domain lookup failing for <domain>` | `<Project>: <error> (since <since>)` |
| domain_still_failing | `Domain lookup still failing for <domain>` | `<Project>: since <since>, <dur>` |
| domain_recovered | `Domain lookup works again for <domain>` | `<Project>: failing since <since> (<dur>)` |
| cert_expiring | `Certificate for <origin> expires in <days> days` | `<Project>: expires <expires>` (`expires today`/`expired` when days ≤ 0) |
| domain_expiring | `Domain <domain> expires in <days> days` | `<Project>: expires <expires>` |
| form_draft_created | `New form <form> is waiting for approval` | `<Project>` |
| form_draft_discarded | `Form <form> was discarded unapproved` | `<Project>` |
| form_submission | `A new form submission in <Project> / <form>` (count 1) or `<count> new form submissions in <Project> / <form>` | `` |

"still" types use `n.CreatedAt` as "now" for the duration. Paths: monitor types → `/projects/<id>/settings/domains`; form types → `/projects/<id>/forms/<url.PathEscape(form)>`; unknown type → title = type, path `/inbox`.

- [ ] **Step 1: Write** `TestRenderGolden`: one notification per type (fixed times, project "Blog <&>", form "contact us"), rendered through `Render` and `Absolute(…, "https://c.example")`, compared to `testdata/render.golden` (generate with the package's usual `-update` flag). Plus `TestRenderCoversVocabulary`: every `store.NotificationTypes` entry renders a title that is not the raw type.
- [ ] **Step 2: Run** `go test ./internal/inbox/` — FAIL.
- [ ] **Step 3: Implement**; generate and read the golden file, check it against the table.
- [ ] **Step 4: Run** `go test ./internal/inbox/ ./internal/archtest/` — PASS.
- [ ] **Step 5: Commit** `feat(inbox): render notifications into one message for every channel`

### Task 4: `internal/inbox` — Tracker (failing states, reminders, expiry steps, bursts)

**Files:**
- Create: `internal/inbox/tracker.go`, `internal/inbox/tracker_test.go`

**Interfaces:**
- Consumes: Task 2 store methods.
- Produces:
  ```go
  type TrackerStore interface {
      ListNotifyStates(ctx) ([]store.NotifyState, error)
      PutNotifyState(ctx, s store.NotifyState, n *store.NewNotification) error
      DeleteNotifyState(ctx, projectID int64, kind, subject string) error
      FlushFormBursts(ctx, now time.Time) (int, error)
      ListEnabledMonitors(ctx) ([]store.Monitor, error)
  }
  type CertReading struct { Read, Valid bool; Error, ExpiresAt string } // Read: a certificate was obtained
  func NewTracker(st TrackerStore, remind time.Duration, logger *slog.Logger) *Tracker
  func (t *Tracker) Uptime(ctx, projectID int64, origin string, up bool, reason, errText string, at time.Time)
  func (t *Tracker) Cert(ctx, projectID int64, origin string, c CertReading, at time.Time)
  func (t *Tracker) Domain(ctx, projectID int64, domain string, ok bool, errText, expiresAt string, at time.Time)
  func (t *Tracker) Tick(ctx, now time.Time)
  func (t *Tracker) Run(ctx context.Context) // Tick every minute until ctx ends
  ```
  State kinds: `uptime`, `cert` (subject origin), `domain` (subject main domain), `cert_expiry`, `domain_expiry`. States are loaded once (lazily, under a mutex) and written through; methods are safe for concurrent use; store errors are logged, never returned.

Rules (from the spec):
- uptime: 2 failed observations in a row → `uptime_down` (since = first failed observation, reason). Success while failing → `uptime_up` (since, until = at) and the state deleted; success while not failing resets the count.
- cert: ignored entirely while the origin's uptime state is failing. `Read && !Valid` → `cert_failing` at once. `!Read` → `cert_failing` only once `at − first failed observation ≥ 24h`. `Read && Valid` while failing → `cert_recovered`. `Read && Valid && ExpiresAt != ""` → expiry steps 14, 7, 1.
- domain: `ok` while failing → `domain_recovered`; `ok && expiresAt != ""` → steps 30, 7, 1. `!ok` → `domain_failing` once failing continuously for 24h.
- Expiry steps: state `{expires_at, step}`; a different `expires_at` than stored resets the step; when `days_left ≤ s` for the smallest step `s ≥ days_left` not yet sent (smaller than the stored step), send one `*_expiring` with fields `days` (floor, may be ≤ 0) and `expires`, store that step.
- Tick: every failing state with `now − reminded_at ≥ remind` sends its `*_still_*` and sets `reminded_at = now` (cert skipped while its origin's uptime fails); `FlushFormBursts(now)`; deletes states whose key no enabled monitor has (origin for uptime/cert/cert_expiry, `maindomain.OfOrigin` for domain/domain_expiry), without a message.

- [ ] **Step 1: Write failing tests** with an in-memory fake `TrackerStore` (map + list of notifications):
  - `TestUptimeDebounce`: one failure → no notification; second → one `uptime_down` with `since` = first failure time.
  - `TestUptimeReminderAndRecovery`: down at t0/t0+1m; `Tick(t0+4h)` → `uptime_still_down`; `Tick(t0+5h)` → none; `Tick(t0+8h1m)` → another; `Uptime(up)` at t0+9h → `uptime_up` with `since` t0 and `until` t0+9h; state gone.
  - `TestTrackerSurvivesRestart`: down announced; new `Tracker` over the same fake; another failure → no second `uptime_down`; `Tick` at reminded_at+4h → `uptime_still_down`.
  - `TestCertInvalidImmediate`, `TestCertUnreadableAfter24h` (failures at t, t+1h … t+23h → none; t+24h → one), `TestCertSilentWhileDown` (uptime failing → invalid cert produces nothing, and `Tick` sends no cert reminder).
  - `TestDomainLookupFailingAfter24h` and recovery.
  - `TestExpirySteps`: cert expiring in 20d → none; 13.5d → `cert_expiring` days 13; 12d → none; 6d → days 6; renewal (new expires 90d) → none, then 13d → again. First seen at 3d → exactly one (step 7).
  - `TestTickPrunesDisabled`: a failing uptime state for an origin missing from `ListEnabledMonitors` is deleted with no notification.
  - `TestTickFlushesBursts`: fake records `FlushFormBursts` called with now.
- [ ] **Step 2: Run** `go test ./internal/inbox/` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/inbox/` — PASS.
- [ ] **Step 5: Commit** `feat(inbox): track failing states, reminders and expiry warnings`

### Task 5: `internal/inbox` — Dispatcher; config; app wiring

**Files:**
- Create: `internal/inbox/dispatch.go`, `internal/inbox/dispatch_test.go`
- Modify: `internal/config/config.go` (+ `config_test.go`), `docs/deployment.md`, `internal/app/app.go`

**Interfaces:**
- Produces:
  ```go
  type DeliveryStore interface {
      PendingDeliveries(ctx, now time.Time, limit int) ([]store.Notification, error)
      SetDelivery(ctx, id int64, delivery, errText string, attempts int, next *time.Time) error
  }
  func NewDispatcher(st DeliveryStore, s notify.Sender /* nil: inbox only */, consoleURL string, logger *slog.Logger) *Dispatcher
  func (d *Dispatcher) Deliver(ctx, now time.Time) // one pass, up to 50 rows
  func (d *Dispatcher) Run(ctx context.Context)    // Deliver every 5s until ctx ends
  ```
  Config: `type NotifyConfig struct { URL string; Remind time.Duration }` on `Config.Notify`. `NOTIFY_URL` (optional; validated with `notify.Parse`, the error — already secret-free — prefixed `config: NOTIFY_URL`); `NOTIFY_REMIND_HOURS` whole hours, default 4, 1 to 168.

Rules: sender nil → every pending row → `none`. Attempts 0 and `created_at` older than 1h → `skipped`. Success → `sent`. `ErrPermanent` → `failed` with the error text. `ErrTemporary` → attempts+1, next attempt after 1m, 5m, 15m, then every 1h; once `created_at` is older than 6h → `failed`. Each send uses `Absolute(Render(n), consoleURL)` and a 30s context.

App wiring (`internal/app/app.go`, where the monitor starts, only when `archiveLog != nil`): build `tracker := inbox.NewTracker(st, cfg.Notify.Remind, logger)`, the sender from `cfg.Notify.URL` (already validated), `disp := inbox.NewDispatcher(st, sender, cfg.Console.URL, logger)`; `mon.WithAlerts(tracker)` (Task 6); run `tracker.Run` and `disp.Run` on the monitor's context and wait for both in the shutdown path the monitor uses. Under `MONITOR_DISABLED` the tracker and dispatcher still run (forms still notify).

- [ ] **Step 1: Write failing tests**: `TestDispatcherSends` (fake sender records the identical rendered message with absolute link; row → sent); `TestDispatcherNoSender` (→ none); `TestDispatcherSkipsOld`; `TestDispatcherRetriesTemporary` (attempts and next times 1m, 5m); `TestDispatcherGivesUp` (temporary failures on a row created 6h1m ago → failed; a newer row in the same pass still attempted); `TestDispatcherPermanent`. Config: `TestNotifyConfig` (unset → URL ""; bad URL fails with `config: NOTIFY_URL`; `NOTIFY_REMIND_HOURS` 0 and 169 fail, default 4h).
- [ ] **Step 2: Run** `go test ./internal/inbox/ ./internal/config/` — FAIL.
- [ ] **Step 3: Implement**; document both variables in `docs/deployment.md` (env table and a short "Notifications" section listing the URL formats from Task 1).
- [ ] **Step 4: Run** `go test ./internal/inbox/ ./internal/config/ ./internal/api/ -run 'Docs|Sync|Notify|Dispatch'` and `go build ./...` — PASS (the docs sync test checks env vars).
- [ ] **Step 5: Commit** `feat(inbox): deliver notifications to the NOTIFY_URL channel`

### Task 6: Monitor reports to the tracker

**Files:**
- Modify: `internal/monitor/monitor.go`, `internal/monitor/monitor_test.go`

**Interfaces:**
- Consumes: `inbox.CertReading` (monitor, rank 2, may import the rank-0 `inbox`).
- Produces in `monitor`:
  ```go
  type Alerts interface {
      Uptime(ctx, projectID int64, origin string, up bool, reason, errText string, at time.Time)
      Cert(ctx, projectID int64, origin string, c inbox.CertReading, at time.Time)
      Domain(ctx, projectID int64, domain string, ok bool, errText, expiresAt string, at time.Time)
  }
  func (r *Runner) WithAlerts(a Alerts) // before Run; nil-safe default does nothing
  ```

Calls (only when `ctx.Err() == nil`):
- `availability`: after `SetMonitorLast` → `Uptime(…, res.Up, res.Reason, res.Error, start)`; scheduled and manual checks alike.
- `cert`: https with no certificate → `Cert(Read:false, Error: st.Error)`; https with one → `Cert(Read:true, Valid, Error, ExpiresAt)`; http → no call.
- `domain`: `errUnknown` → `Domain(ok=true, "", "")`; other error → `Domain(ok=false, err.Error(), "")`; success → `Domain(ok=true, "", exp)`.
- Hourly re-read of an invalid certificate: in `startReads`, a certificate whose last reading has an expiry but `CertValid == false` is due after `dailyRetry` instead of `dailyEvery` (new helper `certDue(m store.Monitor, now time.Time) bool`).

- [ ] **Step 1: Write failing tests** with a recording fake `Alerts` using the package's existing test seams: `TestAlertsUptime` (a down result and an up result each produce one `Uptime` call with the right `up`/`reason`); `TestAlertsCert` (no certificate → `Read:false`; invalid → `Read:true, Valid:false`); `TestAlertsDomain` (lookup failure → ok false; `errUnknown` → ok true); `TestCertDueHourlyWhenInvalid` (`certDue` true at 1h for an invalid reading, false at 1h for a valid one, true at 24h).
- [ ] **Step 2: Run** `go test ./internal/monitor/` — FAIL.
- [ ] **Step 3: Implement**; wire `mon.WithAlerts(tracker)` in app if Task 5 left a placeholder.
- [ ] **Step 4: Run** `go test ./internal/monitor/ ./internal/app/ ./internal/archtest/` — PASS.
- [ ] **Step 5: Commit** `feat(monitor): notify when a site, certificate or domain lookup fails and recovers`

### Task 7: Jobs purge; manage, API, MCP and CLI surface; docs

**Files:**
- Modify: `internal/jobs/jobs.go` (+ test), `internal/manage/store.go`, create `internal/manage/notifications.go` (+ test), `internal/manage/forms.go` (`FormSpec.Notify *bool`), create `internal/api/ops_notifications.go` (+ test), `internal/api/ops_forms.go` (`notify_submissions` in `update_form` input and `formOut`), the API registration list that calls `registerForms`, `cmd/twillingate/form.go` (if it has an update subcommand: `-notify true|false`), `docs/twillingate.md`, `internal/api/openapi.go` if routes are listed there

**Interfaces:**
- manage (`Ops`), not audited (console state, like `mark_form_seen`):
  - `ListNotifications(ctx, f store.NotificationFilter) ([]store.Notification, int /*active count*/, error)` — Limit default 50, max 200; ProjectID != 0 must exist (`requireProject`)
  - `ArchiveNotifications(ctx, ids []int64, all bool) (int, error)` — exactly one of ids / all, else `ErrInvalid`
  - `RestoreNotifications(ctx, ids []int64) (int, error)` — ids required
- API/MCP (`expose` specs, descriptions written in the style of `archive_submissions`):
  - `list_notifications` GET `/api/notifications` — in `{archived?, project_id?, limit?, cursor?}`; out `{notifications: [{id, type, project_id?, project?, subject, title, body, path, fields, created_at, archived_at?, delivery, delivery_error?}], active, next_cursor?}`; title/body/path from `inbox.Render`.
  - `archive_notifications` POST `/api/notifications/archive` — `{ids?: [int], all?: bool}` → `{archived}`
  - `restore_notifications` POST `/api/notifications/restore` — `{ids: [int]}` → `{restored}`
  - `update_form` gains `notify_submissions` (bool, optional); `list_forms` items gain `notify_submissions`.
- jobs: `Store` slice gains `PurgeArchivedNotifications`; `RunDailyPass` calls it with `cfg.Retention.ArchivedDays`, logging the count as the submissions purge does.

- [ ] **Step 1: Write failing tests**: manage `TestArchiveNotificationsSelector` (neither / both → `ErrInvalid`); API test in the package's existing REST style: list returns rendered title for a seeded `uptime_down`, `active` count; archive all → active 0; restore by id; `update_form` with `notify_submissions:false` then `list_forms` shows false; jobs `TestDailyPassPurgesNotifications`.
- [ ] **Step 2: Run** `go test ./internal/manage/ ./internal/api/ ./internal/jobs/` — FAIL.
- [ ] **Step 3: Implement**; document in `docs/twillingate.md`: the three tools and routes in the tool/route tables, the notification type vocabulary as a closed vocabulary table (all 14 with one line each), the delivery values, `notify_submissions` on forms, and a short "Notifications" section (what fires when, the inbox, retention, `NOTIFY_URL` pointer to `docs://deployment`).
- [ ] **Step 4: Run** `go test ./internal/manage/ ./internal/api/ ./internal/jobs/ ./cmd/...` — PASS (incl. `docs_sync_test.go`).
- [ ] **Step 5: Commit** `feat(api): list, archive and restore notifications; opt forms into submission notifications`

### Task 8: Web — inbox page, sidebar badge, form toggle

**Files:**
- Create: `web/src/pages/Inbox.tsx`, `web/src/pages/Inbox.test.tsx`, `web/e2e/inbox.spec.ts`
- Modify: `web/src/App.tsx` (route `/inbox`), `web/src/components/AppSidebar.tsx` (Inbox entry above Shares, `BellIcon`, active count badge), `web/src/lib/api.ts` (`listNotifications`, `archiveNotifications`, `restoreNotifications`, `notify_submissions` on form types and update), the form settings UI in `web/src/components/forms/` (a "Notify on each submission" switch), `web/e2e/phone.spec.ts` (visit `/inbox`)

**Interfaces:**
- Consumes: Task 7 routes.
- UI: model `web/src/pages/Shares.tsx` for page structure. Two tabs, Active and Archived. Each item: a type icon and tone (failing types red, recovered/up green, expiring amber, form types neutral), title, body, project name, relative time with the absolute time as the title attribute, an "Open" link to `path`, an Archive button (Restore in Archived), and when `delivery === "failed"` a muted line `Not delivered: <delivery_error>`. An "Archive all" button on Active, behind `ConfirmDialog`, disabled when empty. Empty state: "Nothing in your inbox". The sidebar badge shows the active count (hidden at 0), refreshed every 60s and after any archive/restore on the page. "Load more" uses `next_cursor`.

- [ ] **Step 1: Write failing tests**: `Inbox.test.tsx` (renders items from a mocked list; Archive calls the API with the id and removes the row; Archive all confirms then calls `{all:true}`; failed delivery line shows); `inbox.spec.ts` e2e against the built binary with a seeded notification (use the e2e suite's existing seeding helpers; if none can insert a notification, seed via a form submission to a new form, which creates `form_draft_created`): page lists it, Archive all empties it, badge disappears.
- [ ] **Step 2: Run** `cd web && npm test -- Inbox` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run typecheck && npm test && npm run build && npm run e2e -- --trace off inbox cursor phone` — PASS.
- [ ] **Step 5: Commit** `feat(web): add an inbox page and a per-form submission notification switch`

### Task 9: Whole-branch verification

- [ ] **Step 1:** `make check` — PASS; `go test -race ./internal/inbox/ ./internal/shared/notify/ ./internal/monitor/` — PASS.
- [ ] **Step 2:** Re-read the spec's goals and the docs table in CLAUDE.md; confirm `docs/twillingate.md`, `docs/deployment.md`, `deploy/UPGRADES.md` and `internal/archtest` ranks are updated; update CLAUDE.md's Layout block with `internal/inbox/` and `internal/shared/notify` (in the shared line).
- [ ] **Step 3: Commit** any doc fixes: `docs: describe the inbox and notify packages in the layout`
