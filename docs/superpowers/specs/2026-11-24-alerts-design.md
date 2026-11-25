# Alerts: notifications when a metric changes — design

Date: 2026-11-24. Status: draft, awaiting approval.

## Intent

Notifications (#268) tell the owner when a site goes down or a certificate
is about to expire, but nothing watches the numbers. A funnel's conversion can
halve, signups can stop, a checkout can break so that `purchase` events never
arrive, and the owner finds out only by opening a dashboard. The notifications
design named this as its later stage ("signups below 5") and kept the model
open for it.

An **alert** watches one number of one project and raises a notification when
that number crosses a line the owner chose. Agents create most alerts over MCP;
the owner needs one place to see them, check they are healthy, and adjust them.
Creating one by hand goes through a wizard with predefined templates.

## Goals

- An alert is its own object, on its own page, not attached to a widget:
  editing or archiving a widget never changes or breaks an alert.
- Three kinds of condition:
  - **threshold**: the value is above or below a number;
  - **change**: the value moved by at least N% against a comparison window;
  - **silence**: no matching event for N hours.
- A minimum-volume guard, so a day with a handful of visitors does not page.
- Agents create, read, edit, pause, archive and restore alerts over MCP (and
  REST), with any query that reduces to one number (below).
- A per-project **Alerts** tab in the console: the list with each alert's state
  and recent values; an editor; a history of firings; a backtest ("would have
  fired on these days").
- A wizard for creating an alert by hand from four templates.
- Firings go through the existing inbox and `NOTIFY_URL` channel, as failing
  states: breached, a reminder while it stays breached, recovered.
- An alert whose query stops running is shown as broken on the page and raises
  one inbox item. It never fails silently.

## Non-goals

- "Different from normal" (anomaly against a seasonal baseline, standard
  deviations). A later stage; the change condition against the window a week
  earlier covers weekly patterns for now.
- Alerts on trends, breakdowns ("any country's conversion drops"),
  retention, paths or `sql` sources.
- Editing an agent-made query in the console. The owner edits the name,
  condition, minimum volume, pause and archive; the query is read-only unless a
  template made it. A different query means asking an agent.
- A cross-project list of alerts. Firings land in the global Inbox, which
  already shows every project.
- Per-alert channels, recipients, quiet hours or severity.

Compared with PostHog and Amplitude (2026-11-25), the owner kept the scope
simple: anomaly detection, breakdown alerts, SQL alerts and per-alert
destinations, which both products offer in some form, stay out of this
version.
- A weekly digest of metrics that moved.
- Intraday evaluation of metric alerts (today's partial day). Only silence is
  checked within the day.

## Anti-goals

- **A false alarm.** A metric alert reads complete days only; change and
  conversion conditions respect the minimum volume. A silence alert on an event
  that legitimately happens twice a day is the owner's threshold choice, and
  the backtest shows it before they save.
- **An alert that quietly stops working, or one that fires because it
  stopped working.** The native engine does not error on an unknown event or
  attribute: it matches no rows, which reads as 0 and would breach "below",
  "drop" and silence. So an alert checks what it references before each
  evaluation (see [Broken](#broken)), and turns broken, visible on the page and
  in the inbox, rather than breached or "ok".
- **A number that disagrees with the dashboard.** An alert's value comes from
  the same native engine, with the same event rules, as a widget's `query`
  source with the same query and dates.
- **Reminder spam.** A daily alert does not remind every four hours with the
  same number.

## Constraints

- Layers: `internal/archtest` ranks. Surfaces (`api`, `jobs`, `monitor`, …)
  never import each other; `app` wires implementations into interfaces.
- `internal/sqlcheck`: every SQL string the new store code hands
  `database/sql` is prepared against a migrated database.
- A migration in `internal/store/sqlite` for the new tables; tests copy a
  migrated database from `storetest`, never migrate their own.
- Refusals are typed (`manage.ErrInvalid`, `ErrNotFound`, `ErrConflict`) and
  matched with `errors.Is`; REST answers 400/404/409.
- Docs in the same commit: `docs/twillingate.md` (the MCP tools, REST routes,
  notification types, the Alerts tab); `internal/api/docs_sync_test.go`
  binds tool names, routes and the closed vocabularies.
- Web: the pointer cursor via real buttons and links
  (`web/e2e/cursor.spec.ts`); nothing scrolls sideways at 360px
  (`web/e2e/phone.spec.ts` visits every page, so the new tab goes in it).
- Every write is audited with its edge (`mcp`, `api`), as project and form
  writes are.

## Approaches

Three ways to place alerts were considered:

1. **On a widget**: an "Alert…" item in a widget card's menu, reading the
   widget's own source. Easy to find, but each component needs its own rule for
   which number is watched, `sql` sources have arbitrary columns, and an alert
   breaks or silently changes meaning when the widget is edited or archived.
2. **Standalone, agent-only**: an agent defines every alert over MCP; the page
   lists and edits them. Least UI, but nobody can create an alert without an
   agent.
3. **Standalone, agents plus a template wizard** (chosen): alerts are their own
   objects on a per-project page; the MCP tools take any query that reduces to
   one number, and the wizard covers the common cases from templates.

The owner picked 3: agents create most alerts, the owner needs a place to see
and edit them, and manual creation goes through a wizard with predefined
templates. Of the two edit models (simple fields only, or also a raw JSON query
editor), the owner picked simple edits.

## The model

### What an alert watches

An alert's **query** is a structured query (the object `query`'s `query`
argument takes, `docs/twillingate.md#structured-queries`) of one of two kinds,
plus a **read** that names the one number:

| Query kind | Read | The number |
| --- | --- | --- |
| `stat` | `{"metric": "<name>"}` | that metric's value; required only when the stat has several metrics |
| `funnel` | `{"step": "<label>", "from": "start" \| "previous"}` | that step's value divided by the first step's (`start`, the default) or by the step just before it (`previous`): a conversion, 0–1; the step is required and must not be the first |

Any other kind is refused when the alert is saved, as `invalid`, with a message
saying an alert reads a `stat` (one metric) or a `funnel` (one step over the
first). The query's own `from` and `to` are not stored: the alert decides the
dates (below). `compare` on a stat is ignored for the same reason. A stat
metric whose value is not a number (a `latest` of a text column, a `min` of a
day) is refused, except `max` of `received_at` for silence.

A stat whose read is a rate or an average (`ratio`, `share`, `bounce_rate`,
`avg`, `mean`, `percentile`) carries a **volume metric**: a second metric of
the same stat, a count (`count`, `actors`, `users`, `groups`, `weight`) over
the same `where`, which `min_volume` measures. An alert on such a read without
one is refused, so "bounce rate above 70%" over three sessions cannot page.

### Conditions

```json
{"type": "threshold", "op": "below" | "above", "value": 0.03}
{"type": "change", "direction": "drop" | "rise" | "either", "percent": 30,
 "against": "previous" | "week_ago"}
{"type": "silence", "hours": 6}
```

- **window_days** (1–30, default 1) applies to threshold and change: the
  alert reads the last `window_days` complete days, ending yesterday (UTC).
  A funnel counts only the steps taken inside the window, so a conversion
  that takes longer than the window reads low (a trial that pays a week later
  is not in a one-day window). The Conversion template defaults to 7 days and
  says so; Amplitude handles this by waiting for the conversion window to
  close, which our day windows make unnecessary as long as the window is long
  enough.
- **change** compares that window with the one just before it (`previous`)
  or with the same days a week earlier (`week_ago`, which matches weekdays;
  with `window_days` ≥ 7 it overlaps or equals `previous`, and is still
  allowed). The percentage is relative: from 4.0% to 2.8% is a 30% drop.
  A comparison value of 0 or empty makes the evaluation "not enough data",
  never a breach.
- **silence** applies to a stat whose metric is
  `{"op": "max", "column": "received_at"}`: the server's clock, so an offline
  batch arriving now counts as now, and a device clock running fast cannot
  hide a real silence. Callers do not write that stat: `create_alert` and
  `update_alert` take a silence alert as `{"where": […]}` in place of
  `query`/`read` (the event filter), and the tool builds the stat. (`ts` is
  clamped to at most `INGEST_LATE_DAYS`, 1 day by default, before
  `received_at`, so the days window below always holds the events received in
  the last `hours`.) The alert reads the newest matching
  event over today and the `ceil(hours / 24) + INGEST_LATE_DAYS` days before
  it and is breached when it is
  older than `hours`, or when there is none in that window (an event the
  project has never sent is `no_data`, not breached; see
  [Broken](#broken)). `window_days` does not apply.
- **min_volume** (optional, ≥ 1): below it, the evaluation is "not enough
  data" and leaves the state as it was. What it measures:
  - a funnel: the denominator step's value (the first step, or with
    `from: previous` the step before the one read);
  - a stat with a rate or average read: its volume metric (required, above);
  - a change on a count: the comparison value (so 1 → 4 is not "+300%");
  - a threshold on a count: not allowed (the value itself is what is watched).
- **An empty value** (a rate or average over no rows, a funnel whose
  denominator step is 0) is "not enough data", never a breach. A count's 0 is a real
  value.

### States

An alert is in one of: `pending` (never evaluated), `ok`, `breached`,
`no_data` (the last evaluation fell under `min_volume`, or a change had a
zero comparison; the previous ok/breached state is kept for notifications),
`broken` (below), `paused`, `snoozed`. The page shows these; notifications
follow only ok ↔ breached and broken.

- **Pause** stops an alert until it is resumed. **Snooze** (`snoozed_until`, a
  time) stops it until then: no evaluation, no notification; it resumes on its
  own, from `pending`. A known outage or a launch day is a snooze, not a
  pause. Both end a breached state without a recovery.
- **Editing** the query, read, condition, window or minimum volume ends the
  state without a notification, sets it `pending` and requests a check, so the
  page shows the new definition's outcome within a minute and the next
  firing is about the new line. Renaming does not.

### Broken

The engine answers an unknown event or attribute with no rows, not an error,
so "broken" is decided by checks of what the alert references, made at save
and before every evaluation:

| Check | At save | At evaluation |
| --- | --- | --- |
| The query fails validation (`native` refuses a column, metric or kind) | refused, `invalid` | broken |
| An `event_name` the query filters on is renamed, merged or blocked by an event rule (`Snapshot.ResolveEvent`) | refused, `invalid`, naming the new name | broken, "renamed to X" / "blocked" |
| An `event_name` or `attr_<key>` the query filters on, other than under a `never` filter, has never been sent by the project (`Presence.HasEvents` / `HasAttribute`, over the files the project holds) | allowed, with a warning in the answer (an agent may set an alert up before a launch) | `no_data`, "never sent": not broken, not breached; it starts evaluating once the event is seen |
| A runtime error (timeout, too large, busy, a file gone mid-read) | — | retried; broken after 3 failed tries in a row (a daily alert retries hourly that day; a silence alert at its next hourly run) |

An alert of an archived project is not evaluated, and its state ends without
a recovery, as archiving a project does to its monitors; it is not broken.
Restoring the project resumes it from `pending`.

`alert_broken` is raised once when an alert turns broken; recovering from
broken is silent (the page shows it), and ok/breached tracking resumes from
the state before it broke. The page and `list_alerts` show the reason.

## Evaluation

- **Metric alerts** (threshold, change) are evaluated once a day, after the
  daily pass (03:00 UTC), over the days ending yesterday. A web SDK's events
  have landed by then; an app's offline batch can still land for yesterday up
  to `INGEST_LATE_DAYS` (1 day) later, so on an app-heavy project yesterday
  may read slightly short against a full comparison window, a bias towards
  "drop" and "below". Accepted for timeliness; the backtest, which reads
  settled days, will look calmer than live firings on such a project. One
  evaluation per alert per day; a missed day (process down) is caught up once,
  for yesterday only.
- **Silence alerts** are evaluated hourly.
- Evaluations, and so every firing, run only in the process that writes the
  event files, beside the monitor and the inbox tracker (`app.go`, the
  `archiveLog != nil` branch): the tracker holds `notify_states` in memory,
  and a second one in a console-only process would double or drop firings.
- "Check now" from the page or `check_alert` is a request, as `check_monitor`
  is: it records `check_requested_at` on the alert and returns at once; the
  loop serves it within a minute, as a scheduled evaluation (it can fire), and
  the client polls until `check_pending` is false (the request clears
  `last_evaluated_at`, which the check then sets again). A request
  while one is pending is a no-op, not an error.
- The backtest (`preview_alert`) is different: it runs in whichever process
  answers the API, reads only, and never touches notification state.
- Each evaluation is stored (alert, time, value, comparison value, volume,
  outcome, error), for the page's history and sparkline. Evaluations are kept
  for 400 days, pruned by the daily pass.

### Notifications

New types in `store.NotificationTypes`, rendered in `internal/inbox/render.go`
and shown in the type table of `docs/twillingate.md`:

| Type | Raised when | Emoji |
| --- | --- | --- |
| `alert_breached` | an ok or pending alert evaluates breached | 🔥 |
| `alert_still_breached` | a reminder while it stays breached | 🔥 |
| `alert_recovered` | a breached alert evaluates ok | ✅ |
| `alert_broken` | an alert's query errors, once until it runs again | ⚠️ |

The subject is the alert's name. The body says the value, the line, what it
was compared with and the dates, e.g. title `Signup conversion is 2.1%`, body
`Below 3% on 2026-11-23 (1,240 visitors at the first step). Was 3.4% the day
before.` The link is the alert on the project's Alerts tab.

An alert fires on the first breached evaluation and recovers on the first ok
one; there is no "two bad periods in a row" and no margin, so an alert whose
value sits on its line can alternate 🔥 and ✅ day to day. Daily evaluation of
whole days already smooths most noise; a debounce can come later if this
proves noisy.

The failing-state logic reuses the inbox tracker's pattern (a `notify_states`
row per `(project, "alert", alert id)`), with its own reminder rule: a daily
alert reminds at most once per evaluation (so once a day) while breached; a
silence alert reminds every `NOTIFY_REMIND_HOURS`. Pausing or archiving an
alert, or archiving its project, ends the state without a recovery, as
switching a monitor off does. Recovery from broken is silent (the page shows
it); breached/ok tracking resumes from the state before it broke.

## Templates and the wizard

The wizard is a few steps: pick a template, fill its fields, pick the
condition, see the backtest, name it and save. Each template builds a query
and read; the alert stores the template name and its fields, so the editor can
reopen a template-made alert with every field editable.

| Template | Fields | Query and read | Conditions offered |
| --- | --- | --- | --- |
| Event count | event (name, from the project's events), optional page path for `$page_view` | stat, `count`, `where` on `event_name` (and `path`) | threshold, change |
| Unique people | event (optional: all events), who: visitors (`actors`) or signed-in users (`users`) | stat, `actors` or `users` | threshold, change |
| Conversion | from: event (and path), to: event (and path), people (`actor`/`user`) | funnel of two steps, not sequential, read the second step | threshold (as a %), change |
| No events | event (and path), hours | stat, `max` of `received_at` | silence |

The event pickers read the project's event names (as `product_events` and the
views do), so a template never offers an event the project does not send.

### Backtest

The wizard, the editor (before saving a change) and `preview_alert` run the
alert over the past days and return, per day, the value, the comparison, the
volume and the outcome it would have had: 90 days for a stat alert, 30 for a
funnel (one funnel query per day). For silence, the backtest lists the gaps
longer than `hours` over the last 30 days. The page draws it as a strip of
days with the breached ones marked, and says "would have fired N times".

## Surfaces

### MCP tools and REST routes

| Tool | Arguments | Returns |
| --- | --- | --- |
| `list_alerts` | `{project_id, archived}` | the alerts, each with its definition, state, last value and last evaluated time |
| `create_alert` | `{project_id, name, condition, window_days, min_volume}` plus one of: `{query, read}`; `{where}` for a silence condition; `{template, fields}` | the alert, and `warnings` (an event or attribute never sent) |
| `update_alert` | `{id, …}`: `name`, `condition`, `window_days`, `min_volume`, `paused`, `snoozed_until`; `query`/`read`, `where` or `template`/`fields` | the alert |
| `archive_alert`, `restore_alert` | `{id}` | the alert |
| `check_alert` | `{id}` | the alert with `check_requested_at`; poll `list_alerts` or `alert_history` for the evaluation |
| `preview_alert` | an alert definition (as `create_alert`, without saving) or `{id}` | the backtest |
| `alert_history` | `{id, limit, cursor}` | the stored evaluations, newest first |

REST mirrors them under `/api/projects/{id}/alerts…`, as the forms routes do.
Archived alerts are purged `RETENTION_ARCHIVED_DAYS` after archiving, with
their evaluations. Names are unique among a project's live alerts (`conflict`).
A project holds at most 100 live alerts (`ALERTS_MAX` style limit, reported
by `limits`), so a runaway agent cannot make the daily run unbounded.

### The Alerts tab

A built-in project tab, `/projects/:id/alerts`, beside Settings and Forms,
shown on every project.

```
Alerts                                               [+ New alert]
──────────────────────────────────────────────────────────────────
Signup conversion < 3%        4.1%    ▁▂▃▂▄▃▅   ok
No purchases for 6h           2h ago             ok
Pageviews −30% vs last week   −34%    ▅▄▃▁      🔥 since Mon
Trial → paid (agent)          —                  ⚠ broken: trial_start renamed
```

- Each row: name, the last value (formatted for the read: a count, a %, "2h
  ago"), a sparkline of recent evaluations, the state. Archived alerts behind
  an Archived toggle, as the inbox does.
- Opening an alert: its condition, window, minimum volume and pause, editable;
  the query, read-only (a short summary in words and the JSON) unless a
  template made it, in which case the template's fields; the backtest; the
  history of evaluations and firings; Check now; Archive.
- On a phone, the row folds the sparkline away below `sm`.

## Code placement (proposed; the builder may adjust within the rank rules)

- **`internal/store`**: `Alert` and `AlertEvaluation` row types and the store
  methods; `internal/store/sqlite` the migration (`alerts`,
  `alert_evaluations`) and queries.
- **A new rank-1 package, `internal/alerts`**: validation (query kinds, reads,
  conditions, templates → queries), the audited operations, evaluation (one
  alert, given the native engine and a date, to an outcome), the backtest, and
  the scheduler loop. It imports leaves only (`store`, `native`, `inbox`,
  `shared/civil`) so that `api` can call operations and previews and `app` can
  run the loop. Add it to the rank table. The event rules and presence checks
  of [Broken](#broken) live in `manage` (rank 1, not importable from here), so
  the package declares small interfaces for them (resolve an event, has
  events, has attribute) and `app` passes `manage`'s registry and presence.
- **`internal/inbox`**: the four types' rendering and an `Alert` method on the
  tracker taking an outcome, with the per-kind reminder rule.
- **`internal/api`**: the tools and routes.
- **`internal/app`**: wire the loop with the engine, the tracker, the
  registry and presence, in the event-writing process only.
- **`web/`**: the tab, the editor, the wizard, and the API client.

## Build order

1. Store, migration, validation and operations, evaluation, the loop and the
   notifications; the MCP tools and REST routes; docs. Agents can use alerts.
2. The Alerts tab: list, editor (simple edits), history, backtest, Check now.
3. The wizard with the four templates.

Each step is a pull request of its own.

## Left to the builder

- Table and column names, the exact JSON field names of the tool arguments
  (keeping the shapes above), and how a template's fields are stored.
- How evaluations are scheduled inside the loop (one ticker, as `jobs` and
  `monitor` do), and how "after the daily pass" is signalled.
- Value formatting in titles and on the page (counts with separators,
  conversions as percentages with one decimal).
- Whether the funnel backtest batches its per-day queries through
  `AnswerMany`.
