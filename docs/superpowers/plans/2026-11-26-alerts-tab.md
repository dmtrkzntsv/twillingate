# Alerts tab Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The console's Alerts tab: a project's alerts listed with their last value, a sparkline and their state; one alert's page with simple edits, its template's fields, a backtest, its history, Check now, pause, snooze and archive.

**Architecture:** Build-order step 2 of the alerts design, on top of the backend of PR #281 (branch `feat/alerts`; this branch, `feat/alerts-tab`, is stacked on it). One small backend addition (`unit` on the alert JSON) so the page can format raw values; the rest is `web/`: API client and pure helpers, a fourth project page beside Settings and Forms (`/projects/:id/alerts` and `/projects/:id/alerts/:alertId`, the address PR1's notifications already link to), and Playwright coverage.

**Tech Stack:** Go (backend field), React 19 + TypeScript, TanStack Query, react-router, shadcn/ui, Tailwind, Vitest + Testing Library, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-24-alerts-design.md` (sections "The Alerts tab", "Backtest", "Templates and the wizard", "Build order"). The backend contract the page reads is `docs/twillingate.md` § Alerts.

## Global Constraints

- Scope is build-order step 2 only: list, editor (simple edits), history, backtest, Check now. No wizard and no "+ New alert" button (PR3). The list's empty state says agents create alerts.
- Simple edits: `name`, `condition` (within the alert's kind), `window_days`, `min_volume`, pause, snooze, archive/restore are editable. The query is read-only (a summary in words and its JSON) unless a template made it; then the template's fields are editable (sent as `fields` alone). The template itself is not switchable.
- A silence alert offers only the silence condition; a stat or funnel alert only threshold or change.
- `min_volume` is shown only where the server takes it: not for `kind === 'silence'`, and not for `kind === 'stat'` with a threshold condition and no `read.volume`.
- A preview or save body carries exactly one source: `{template, fields}`, or `{where}` (silence without a template), or `{query, read}`. Never two (`list_alerts` returns `query` alongside the others).
- No single-alert GET exists and none is added: the alert page finds the alert in `list_alerts`, then in `list_alerts?archived=true`, else a not-found notice (a purged alert, a wrong link).
- Check now is a request served within a minute (the runner ticks every minute): poll the list every 2 s while the alert's `check_pending` is true. Check now is disabled for a paused, snoozed or archived alert (the server answers 400 for those).
- Two different 409s on save (a live alert has the name; another edit landed first): show the server's message and refetch the list.
- Everything clickable is a `button`, a link, or `role="button"` (`web/e2e/cursor.spec.ts`). Rows of the list are links.
- Nothing scrolls sideways on a 360px phone (`web/e2e/phone.spec.ts`): the list row folds the sparkline away below `sm`; the history table hides its Days, Compared with and Volume columns below `sm`; a single-column grid says `grid-cols-1`.
- Values are formatted by `unit` (Task 1): `count` an integer with thousands separators; `fraction` a percent with one decimal (`0.034` → `3.4%`); `number` at most two decimals, trailing zeros trimmed; `hours` (silence) `7h` (whole hours) or `45m` under one hour; `null` → `—`. A threshold's value is entered in the read's unit: a percent for `fraction` (typing `3` saves `0.03`).
- Docs in the same commit as the change (`docs/twillingate.md`); Conventional Commits with scopes `api`/`web`; commit trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. No model identifiers in code, comments, commits or docs.
- Locally run only the unit tests and typecheck for what changed (`cd web && npx vitest run <files> && npx tsc -b --noEmit` or the repo's typecheck script; `go test ./internal/api/ ./internal/alerts/`); CI runs the full suites and e2e.

## Review Focus

1. A template-made alert whose template field names an event the project has stopped sending: the field editor must still show the saved value (not blank it because the picker's list lacks it). Test in Task 5.
2. A notification link to an archived alert (`/projects/3/alerts/12` after archive) must open the alert, archived, with Restore; a purged one a not-found notice, not a spinner forever. Test in Task 4.
3. A `fraction` threshold edited and saved without touching the value must send the same `0.03`, not `3` or `0.0003` (round-trip through the percent input). Test in Task 2 (helper) and Task 5 (editor).
4. Check now on an alert whose check is already pending, or saving a new definition (which requests a check), must keep polling until `check_pending` is false, then stop polling. Test in Task 4.
5. A broken alert (`kind === ''`, its stored definition no longer parses): the list row and page must render (state Broken, reason shown, query JSON shown) without crashing on an unknown kind, with the editor showing only name, pause, snooze and archive. Test in Tasks 3 and 4.

---

### Task 1: `unit` on the alert JSON, and a broken reason that reads forwards

**Files:**
- Modify: `internal/alerts/ops.go` (View gains `Unit string`), `internal/alerts/define.go` (a `func (p Parsed) Unit() string`)
- Modify: `internal/api/ops_alerts.go` (`alertOut.Unit`, `toAlertOut`, and the `list_alerts` description)
- Modify: `internal/alerts/evaluate.go:201-213` (`ruleBreak`)
- Modify: `docs/twillingate.md` § Alerts (the List row of the operations table names `unit`; the sentence "the console's Alerts tab comes later" becomes a short description of the tab — Task 7 finishes that paragraph)
- Test: `internal/alerts/define_test.go` (or the file holding Parse tests), `internal/alerts/evaluate_test.go`, `internal/api/ops_alerts_test.go`

**Interfaces:**
- Produces: alert JSON field `unit`: `"count" | "fraction" | "number" | "hours" | ""` (`""` when `kind` is `""`). Mapping: `readCount` → `count`, `readFraction` → `fraction`, `readNumber` → `number`, `readSilence` → `hours`.
- Produces: broken reasons `"<name> renamed to <to>"` (was `"renamed to <to>: <name>"`) and `"<name> blocked"` (was `"blocked: <name>"`). The save-time refusal in `Ops.checkEvents` keeps its own wording.

- [ ] **Step 1: Failing tests.** `TestParsedUnit`: a stat `count` → `count`; a stat `bounce_rate` with `volume: count` → `fraction`; a stat `avg_value` with volume → `number`; a funnel → `fraction`; a silence `where: []` → `hours`; zero `Parsed{}` → `""`. In the API tests, `list_alerts` for a funnel alert has `"unit":"fraction"`. Update the existing evaluate tests that assert the old reason strings to `"activated renamed to activation"` and `"signup blocked"` style (grep `renamed to` and `blocked:` under `internal/alerts` and `internal/inbox`).
- [ ] **Step 2: Run** `go test ./internal/alerts/ ./internal/api/ -run 'Unit|Broken|Rule|Alert'` — expect failures on the new assertions.
- [ ] **Step 3: Implement** `Parsed.Unit()`, `View.Unit` filled where `ValueText` is (`ops.go:473`), `alertOut.Unit` with jsonschema `"how value, last_value and a threshold's value read: count, fraction (0 to 1, shown as a percent), number, or hours (a silence alert's age); empty when kind is"`, and the two reason strings.
- [ ] **Step 4: Docs.** In the List row of the operations table add `unit` after `kind` with the same meaning. In the no-data/broken paragraph nothing names the reason text; leave it.
- [ ] **Step 5: Run** `go test ./internal/alerts/ ./internal/api/ ./internal/inbox/` — PASS.
- [ ] **Step 6: Commit** in two commits: `feat(api): say the unit an alert's value reads in` (unit + docs) and `fix(alerts): word a broken alert's reason as "<name> renamed to <to>"`.

---

### Task 2: API client, queries and alert helpers

**Files:**
- Modify: `web/src/lib/api.ts` (types and `endpoints` entries), `web/src/lib/queries.ts`
- Create: `web/src/lib/alerts.ts`, `web/src/lib/alerts.test.ts`

**Interfaces:**
- Produces (types in `api.ts`, field names exactly the server's JSON):
  - `AlertUnit = 'count' | 'fraction' | 'number' | 'hours' | ''`
  - `AlertState = 'pending' | 'ok' | 'breached' | 'no_data' | 'broken' | 'paused' | 'snoozed'`
  - `AlertOutcome = 'ok' | 'breached' | 'no_data' | 'broken' | 'error'`
  - `AlertCondition = { type: 'threshold'; op: 'below' | 'above'; value: number } | { type: 'change'; direction: 'drop' | 'rise' | 'either'; percent: number; against: 'previous' | 'week_ago' } | { type: 'silence'; hours: number }`
  - `AlertRead = { metric?: string; volume?: string; step?: number; from?: 'start' | 'previous' }`
  - `AlertFilter` = the structured query filter type already in `api.ts` if one exists, else `Record<string, unknown>`
  - `Alert` with every field `list_alerts` returns (`docs/twillingate.md` List row), including `unit`, `query: unknown`, `where?: AlertFilter[]`, `template?: 'event_count' | 'unique_people' | 'conversion' | 'no_events'`, `fields?: Record<string, string>`, `recent: { at: string; value: number | null; outcome: AlertOutcome }[]`
  - `AlertsResponse = { alerts: Alert[]; max_live: number }`
  - `AlertSource = { template: string; fields: Record<string, string> } | { where: AlertFilter[] } | { query: unknown; read: AlertRead }`
  - `AlertPatch = { name?: string; condition?: AlertCondition; window_days?: number; min_volume?: number; paused?: boolean; snoozed_until?: string | null; fields?: Record<string, string> }`
  - `AlertPreview = { days: { day: string; value: number | null; compare: number | null; volume: number | null; outcome: 'ok' | 'breached' | 'no_data' }[]; gaps: { from: string; to: string; hours: number }[]; fired: number }`
  - `AlertEvaluation = { id: number; at: string; from: string; to: string; value: number | null; compare: number | null; volume: number | null; outcome: AlertOutcome; error: string }`, `AlertHistory = { evaluations: AlertEvaluation[]; next_cursor?: string }`
- Produces (`endpoints` in `api.ts`):
  - `alerts(projectId, archived = false)` → GET `/api/projects/{id}/alerts` (`?archived=true` when archived)
  - `updateAlert(projectId, id, patch: AlertPatch)` → PATCH `/api/projects/{id}/alerts/{alertId}` → `Alert`
  - `archiveAlert`, `restoreAlert`, `checkAlert(projectId, id)` → POST `…/{alertId}/archive|restore|check` → `Alert`
  - `previewAlert(projectId, body: { id: number } | (AlertSource & { condition: AlertCondition; window_days?: number; min_volume?: number }))` → POST `…/alerts/preview` → `AlertPreview`
  - `alertHistory(projectId, id, cursor?: string, limit = 50)` → GET `…/{alertId}/history`
- Produces (`queries.ts`): `alertsQuery(projectId, archived = false)` key `['alerts', projectId, archived]`; `alertPreviewQuery(projectId, id)` key `['alert-preview', projectId, id]`, `staleTime: 5 * 60_000`; history is an infinite query built in Task 6 with key `['alert-history', projectId, id]`.
- Produces (`alerts.ts`, all pure):
  - `formatAlertValue(unit: AlertUnit, v: number | null): string` — the Global Constraints' formatting (`hours`: `7h`, `45m`; this is the bare age, unlike the server's `last event 7h ago`).
  - `thresholdToInput(unit, value: number): string` and `inputToThreshold(unit, text: string): number | null` — percent for `fraction` (`0.03` ↔ `"3"`), otherwise the number; `null` for empty or not a finite number. Round so `0.07` → `"7"` (no `7.000000000000001`).
  - `describeCondition(a: Pick<Alert, 'condition' | 'unit' | 'window_days'>): string` — exact copy: threshold `below 3% over 7 days` / `above 1,200 over 1 day`; change `drops 30% against the 7 days before`, `rises 30% against the same day a week earlier` (window 1 says "day", more says "the 7 days" / "same 7 days a week earlier"), `either` → `moves 30%`; silence `no event for 6 hours` (`1 hour`).
  - `describeSource(a: Alert): string` — template-made: `event_count` → `Count of signup`; with path `Count of $page_view on /pricing`; `unique_people` → `Visitors doing signup` / `Signed-in users doing signup` / `Visitors` (no event) ; `conversion` → `Conversion from signup_start to signup_done` (people `user`: append ` (signed-in users)`); `no_events` → `Events named heartbeat` (path appended as above). Otherwise: `kind === 'funnel'` → `Funnel step <step> over <the first step|the step before>`; `stat` → `<read.metric> of a stat query`; `silence` → `Events matching <n> filters` (`every event` for none); `''` → `A definition that no longer holds`.
  - `alertSource(a: Alert): AlertSource` — the exactly-one-source rule of Global Constraints.
  - `takesMinVolume(kind: string, condition: AlertCondition, read: AlertRead): boolean` — the Global Constraints rule.
  - `conditionTypes(kind: string): AlertCondition['type'][]` — `['silence']` for silence, `['threshold', 'change']` for stat and funnel, `[]` for `''`.
  - `breachedSince(a: Alert): string | null` — when the state is `breached`, the `at` of the oldest evaluation in the newest unbroken run of `breached` in `recent` (`recent` is newest first; `no_data` entries neither break nor extend the run); else null.
  - `snoozeUntil(now: Date, choice: '1h' | '1d' | '1w'): string` — RFC 3339 UTC.

- [ ] **Step 1: Failing tests** in `alerts.test.ts`, one `describe` per helper, with these cases at least: `formatAlertValue('count', 1240)` → `'1,240'`; `('fraction', 0.034)` → `'3.4%'`; `('number', 2.5)` → `'2.5'`; `('number', 2.456)` → `'2.46'`; `('hours', 7.9)` → `'7h'`; `('hours', 0.75)` → `'45m'`; `(any, null)` → `'—'`. `thresholdToInput('fraction', 0.07)` → `'7'`; `inputToThreshold('fraction', '3')` → `0.03`; `inputToThreshold('count', '')` → `null`; round-trip `inputToThreshold('fraction', thresholdToInput('fraction', 0.035)) === 0.035`. `alertSource` of a template alert carrying `query` too → only `{template, fields}`; of a silence alert with `where` and `query` → only `{where}`; of a stat → `{query, read}`. `takesMinVolume('stat', threshold, {metric:'count'})` → false; with `volume:'count'` → true; `('funnel', threshold, …)` → true; `('silence', …)` → false; `('stat', change, {metric:'count'})` → true. `breachedSince` with recent `[breached t3, no_data t2, breached t1, ok t0]` and state breached → `t1`. Every `describeCondition`/`describeSource` copy above.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/alerts.test.ts` — FAIL.
- [ ] **Step 3: Implement** the types, endpoints, queries and helpers.
- [ ] **Step 4: Run** the test and the typecheck — PASS.
- [ ] **Step 5: Commit** `feat(web): add the alerts API client and helpers`.

---

### Task 3: The Alerts page in the project's tab row, and the list

**Files:**
- Modify: `web/src/lib/project-tabs.ts` (`ALERTS_ID = -3`; `tabPath` maps it to `/projects/{id}/alerts`; add `alertPath(projectId, alertId, search = '')`)
- Modify: `web/src/App.tsx` (routes `/projects/:id/alerts` and `/projects/:id/alerts/:alertId`, both `<Project tab="alerts" />`)
- Modify: `web/src/pages/Project.tsx` (the `tab` prop takes `'forms' | 'alerts'`; every `SETTINGS_ID || FORMS_ID` branch includes `ALERTS_ID`: the back header, the "No events received yet" banner's exclusion; the body renders `AlertsList` or `AlertPage` (Task 4; render a placeholder `null` for `:alertId` in this task))
- Modify: `web/src/components/projects/ProjectTabBar.tsx` (a `BellIcon` link button, label and tooltip `Alerts`, before Forms, `aria-current="page"` on the Alerts pages; on the Alerts pages the row's left holds `AlertTabs`)
- Create: `web/src/components/alerts/AlertTabs.tsx` (two tabs via `ReportTabs`: `Live` → `/alerts`, `Archived` → `/alerts?show=archived`, keeping the range params; prop `current: 'live' | 'archived'`)
- Create: `web/src/components/alerts/AlertsList.tsx`, `AlertStateBadge.tsx`, `AlertSparkline.tsx`
- Test: `web/src/lib/project-tabs.test.ts`, `web/src/components/alerts/AlertsList.test.tsx`, `AlertStateBadge.test.tsx`, `web/src/components/projects/ProjectTabBar.test.tsx`, `web/src/pages/Project.test.tsx` (route renders)

**Interfaces:**
- Consumes: Task 2's `alertsQuery`, `Alert`, `formatAlertValue`, `describeCondition`, `breachedSince`.
- Produces: `AlertStateBadge({ alert }: { alert: Alert })` (Task 4 reuses it); `AlertSparkline({ recent, unit })`; `AlertTabs({ projectId, current, search })`.

Copy and behaviour (exact):
- `AlertStateBadge`: `pending` → `Pending` (outline); `ok` → `OK` (green text); `breached` → `Breached` + ` since <Mon D>` when `breachedSince` (red, `BellRingIcon`); `no_data` → `No data` (muted); `broken` → `Broken` (amber, `TriangleAlertIcon`); `paused` → `Paused` (outline, `PauseIcon`); `snoozed` → `Snoozed until <Mon D, HH:MM>` in local time (outline, `BellOffIcon`). Same colours as the Inbox's alert notifications (red breached, green recovered/ok, amber broken).
- `AlertsList` (`?show=archived` picks the archived list): a `<ul aria-label="Alerts">` of rows; each row is a `Link` to `alertPath` whose accessible name starts with the alert's name; inside: the name (truncates on one line), under it `describeCondition` in muted text, then a broken alert's `reason`; on the right `last_value_text`, the sparkline (`hidden sm:block`, fixed width `w-24`), and the badge. Sorted as the server sends (by name).
- Live, none: a section titled `No alerts yet` with the text `Alerts are made by an agent connected over MCP. Ask it, for example, to alert you when signups drop 30% against the week before.` Archived, none: `Nothing archived`. Loading: a skeleton; error: `LoadError what="the alerts"`.
- `AlertSparkline`: the `recent` values oldest → newest as a small line (recharts, as `projects/Sparkline.tsx`), `aria-hidden`, with breached points dotted red; fewer than 2 values with a number draws nothing.

- [ ] **Step 1: Failing tests.** `tabPath(3, ALERTS_ID)` → `/projects/3/alerts`; `alertPath(3, 12, 'range=7d')` → `/projects/3/alerts/12?range=7d`. `AlertsList`: renders rows as links to `/projects/1/alerts/<id>` with names, value text and badges from a mocked `endpoints.alerts`; the empty-state copy; `?show=archived` calls `endpoints.alerts(1, true)` and shows `Nothing archived` when empty; a broken alert with `kind: ''`, `unit: ''`, `query: null` renders its reason without throwing. `AlertStateBadge`: each state's text, `Breached since Nov 24` from recent. `ProjectTabBar`: an `Alerts` link to `/projects/1/alerts` with `aria-current="page"` when `currentId` is `ALERTS_ID`. `Project.test`: `/projects/1/alerts` renders the back header and the Live/Archived tabs.
- [ ] **Step 2: Run** these tests — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the tests and the typecheck — PASS.
- [ ] **Step 5: Commit** `feat(web): add the Alerts tab with a project's alerts`.

---

### Task 4: One alert's page: state, actions, definition, Check now

**Files:**
- Create: `web/src/components/alerts/AlertPage.tsx`, `AlertActions.tsx`, `AlertDefinition.tsx`, `web/src/hooks/use-alert-actions.ts`
- Modify: `web/src/pages/Project.tsx` (render `AlertPage` for `:alertId`)
- Test: `web/src/components/alerts/AlertPage.test.tsx`, `AlertActions.test.tsx`

**Interfaces:**
- Consumes: Tasks 2–3.
- Produces: `AlertPage({ project, alertId }: { project: Project; alertId: string })`, which lays out (top to bottom, one column, `grid-cols-1`): heading and badge; facts; actions; `AlertDefinition`; the editor slot (Task 5: `AlertEditor`); the backtest slot (Task 5: `AlertBacktest`); the history slot (Task 6: `AlertHistory`). In this task the slots render nothing.
- Produces: `useAlertActions(projectId)` returning `{ check, pause, resume, snooze, wake, archive, restore, update, pending }`, each calling the endpoint, writing the answered `Alert` into both `alertsQuery` caches (move it between live and archived on archive/restore), invalidating `['alerts', projectId]`, and toasting errors with `toastError`; `update(id, patch)` returns `Promise<Alert | null>`.

Behaviour (exact):
- Finding the alert: read `alertsQuery(id, false)`; if loaded and the alert is not in it, read `alertsQuery(id, true)`; if not there either, `NotFound` titled `No alert at this address` with `It may have been purged after it was archived, or the link may be wrong.` and a link `Go to the alerts`. A non-numeric `alertId` is not found at once. The tab row's `AlertTabs` shows `Archived` current for an archived alert, else `Live`.
- Heading: the alert's name (`h2`, truncates), the `AlertStateBadge`, and `reason` beneath it for `broken` and `no_data`.
- Facts line (muted): `Last value <last_value_text>` · `checked <relative time of last_evaluated_at>` (omitted when empty) · `days <first> to <last_day>`, where first is `last_day − window_days + 1`, omitted for silence or an empty `last_day`; `Archived <date>` for an archived alert.
- Actions, in a row that wraps: `Check now` (disabled for paused, snoozed or archived, with a tooltip saying why: `Paused alerts are not checked` / `Snoozed alerts are not checked` / `Archived alerts are not checked`; while `check_pending` it reads `Checking…` and is disabled); `Pause` or `Resume`; a `Snooze` dropdown with `1 hour`, `1 day`, `1 week` (hidden while paused), or `Wake` while snoozed; `Archive`, or `Restore` when archived (no confirmation: both are reversible). An archived alert shows only Restore.
- Polling: while the shown alert has `check_pending`, refetch `alertsQuery` every 2000 ms (`refetchInterval` returning `false` once it is not pending). When it clears, toast `Checked: <state text>`, only if this page asked for the check.
- `AlertDefinition`: a section `Watches` with `describeSource(alert)`; for a non-template alert a `<details>` `Query` showing `JSON.stringify(query (or where for silence), null, 2)` in a `pre` that scrolls inside itself (`overflow-x-auto`, `max-w-full`). For a template alert, the source line only (Task 5 adds the editable fields).

- [ ] **Step 1: Failing tests.** `AlertPage`: finds a live alert; finds an archived one (live list lacks it) and shows Restore and no Check now; shows the not-found notice when neither list has it, and for `alertId="abc"`; a broken alert (`kind: ''`) renders name, Broken, reason and its query JSON. `AlertActions`: Check now disabled with each tooltip reason; clicking Check now calls `endpoints.checkAlert(1, 7)`, and while the mocked list answers `check_pending: true` the button reads `Checking…`, then after the list answers `check_pending: false` it re-enables and the toast fires (fake timers); Snooze `1 day` calls `updateAlert` with `snoozed_until` one day ahead; Wake sends `snoozed_until: null`; Pause sends `paused: true`; Archive calls `archiveAlert` and the page then shows Restore.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** tests and typecheck — PASS.
- [ ] **Step 5: Commit** `feat(web): open an alert with its state, Check now, pause, snooze and archive`.

---

### Task 5: The editor and the backtest

**Files:**
- Create: `web/src/components/alerts/AlertEditor.tsx`, `ConditionFields.tsx`, `TemplateFields.tsx`, `AlertBacktest.tsx`, `web/src/lib/alert-draft.ts`, `web/src/lib/alert-draft.test.ts`
- Modify: `web/src/components/alerts/AlertPage.tsx` (fill the editor and backtest slots; hide the editor for an archived alert and show the alert read-only)
- Test: `web/src/components/alerts/AlertEditor.test.tsx`, `AlertBacktest.test.tsx`

**Interfaces:**
- Consumes: Task 2 helpers and endpoints, Task 4's `useAlertActions().update`.
- Produces (`alert-draft.ts`, pure): `type AlertDraft = { name: string; conditionType: 'threshold' | 'change' | 'silence'; op: 'below' | 'above'; value: string; direction: 'drop' | 'rise' | 'either'; percent: string; against: 'previous' | 'week_ago'; hours: string; windowDays: string; minVolume: string; fields: Record<string, string> }`; `draftOf(a: Alert): AlertDraft` (value through `thresholdToInput`; a condition type absent from the alert takes defaults: `below`, `drop`, `30`, `previous`); `draftErrors(a: Alert, d: AlertDraft): Partial<Record<keyof AlertDraft | 'fields', string>>` (name non-empty; threshold value a number; percent above 0 and at most 1000; hours whole 1–720; window whole 1–30; min volume 0 or at least 1); `patchOf(a: Alert, d: AlertDraft): AlertPatch` (only what changed; `condition` whole when any part changed; `fields` whole when any changed; `min_volume` only where `takesMinVolume` for the draft's condition); `previewBody(a: Alert, d: AlertDraft)` = `alertSource(a)` with the draft's fields swapped into a template source, plus the draft's `condition`, `window_days` and (where taken) `min_volume`.
- `TemplateFields({ projectId, template, fields, onChange })` — built for reuse by PR3's wizard: per template, the fields of `docs/twillingate.md` § Templates: event pickers (`event`, `from_event`, `to_event`) as a combobox or `select` over `eventNamesQuery(projectId, { range of the last 30 days as the codebase's RangeQuery expresses it })`, always including the current value even when the list lacks it, `unique_people`'s `event` with an `Every event` empty choice; path inputs (`path`, `from_path`, `to_path`) shown only when the matching event is `$page_view`; `who` select `Visitors` (`actors`) / `Signed-in users` (`users`); `people` select `Visitors` (`actor`) / `Signed-in users` (`user`).

Behaviour (exact):
- `AlertEditor` is a form titled `Settings` with: `Name`; `TemplateFields` for a template alert; `Condition` — a select of `conditionTypes(kind)` labelled `Threshold` / `Change` (hidden when only `silence`); threshold: `op` select `below`/`above` and the value input with a `%` suffix for `fraction`; change: direction `drops`/`rises`/`moves`, percent input with `%` suffix, against `the days before` / `the same days a week earlier`; silence: `Hours without an event`; `Window` (days, 1–30, not for silence) ; `Minimum volume` (`0 for none`), shown per `takesMinVolume`. Errors show under their field; `Save` is disabled while the draft is unchanged or has errors; `Discard` resets to `draftOf(alert)`. A broken alert (`kind === ''`) shows only `Name`.
- Save sends `patchOf`; on success toast `Saved` (and, when a definition part changed, the page's Check-now polling starts because the answer has `check_pending: true`); on a 409 show the server's message under the form and refetch the alerts; `warnings` in the answer are shown under the form as `Never sent yet: <name>` lines.
- `AlertBacktest` titled `Backtest`: for the saved alert, `alertPreviewQuery(projectId, id)` runs when the page opens (not for an archived or broken alert); when the editor has unsaved definition changes a `Preview changes` button runs `previewAlert(previewBody(...))` and shows that result labelled `With your changes`, until saved or discarded. Metric result: a strip of one cell per day (`flex` of equal cells, `min-w-0`, wrapping or shrinking to fit 360px), coloured breached red, ok green-muted, no_data grey, each with `title` `<day>: <formatAlertValue> — <outcome>`; text `Would have fired <n> time(s) in the last <90|30> days` (`never` for 0: `Would not have fired in the last 90 days`). Silence result: the same sentence over 30 days and a list of gaps `<from> – <to> · <hours>h` in local time. Loading: a skeleton strip; 429/504 or other errors: `LoadError what="the backtest"` with retry.

- [ ] **Step 1: Failing tests.** `alert-draft.test.ts`: `draftOf` → `patchOf` with no edits is `{}`; editing only the name gives `{name}`; a `fraction` threshold untouched sends nothing, changed `3` → `4` sends `condition.value === 0.04`; switching threshold → change sends the whole change condition; `min_volume` omitted for a stat threshold without volume; a template field change sends `fields` whole and nothing else; `previewBody` for a template alert with an edited field carries `template` + the edited `fields`, and no `query`; `draftErrors` for percent `0`, hours `721`, window `31`, min volume `0.5`. `AlertEditor.test`: a silence alert shows only Hours (no Condition select, no Window, no Minimum volume); a stat-count threshold hides Minimum volume; Save disabled until a change; a 409 shows its message; `TemplateFields` keeps a saved `event` the event list lacks; path appears only for `$page_view`. `AlertBacktest.test`: the strip has 90 cells and `Would have fired 2 times in the last 90 days` for a mocked preview with two runs; a silence preview lists its gaps; an error shows the retry.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** tests and typecheck — PASS.
- [ ] **Step 5: Commit** `feat(web): edit an alert and backtest it before saving`.

---

### Task 6: History

**Files:**
- Create: `web/src/components/alerts/AlertHistory.tsx`, `AlertHistory.test.tsx`
- Modify: `web/src/components/alerts/AlertPage.tsx` (the history slot)

**Interfaces:**
- Consumes: `endpoints.alertHistory`, `formatAlertValue`.
- Produces: `AlertHistory({ projectId, alert }: { projectId: number; alert: Alert })` using `useInfiniteQuery` with key `['alert-history', projectId, alert.id]`, `getNextPageParam: (p) => p.next_cursor`. Invalidated when the alert's `last_evaluated_at` changes (e.g. a check is served).

Behaviour (exact):
- Section titled `History`; a table (`region` named `History`) with columns `When` (local date and time), `Days` (`<from> to <to>`, `—` for silence; `hidden sm:table-cell`), `Value`, `Compared with` (`hidden sm:table-cell`), `Volume` (`hidden sm:table-cell`), `Outcome` (`OK`, `Breached` red, `No data`, `Broken` amber, `Error`), with `error` text under the outcome in muted small text when present. `Load more` button while there is a next page. Empty: `Not evaluated yet`.

- [ ] **Step 1: Failing tests.** Renders rows from a mocked first page with formatted values per `unit`; `Load more` calls `alertHistory` with the `next_cursor` and appends; no `Load more` without `next_cursor`; empty copy; the three columns carry `hidden sm:table-cell`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `feat(web): show an alert's evaluations`.

---

### Task 7: End to end, phone, docs and the Inbox's intro

**Files:**
- Create: `web/e2e/alerts.spec.ts`, and an `createAlert(request, projectId, body)` helper in `web/e2e/forms.ts` (beside `createProject`, same bearer token)
- Modify: `web/e2e/phone.spec.ts` (visit `/app/projects/${id}/alerts`, `?show=archived` and one alert's page, with an alert whose name is too long for one line, e.g. 120 characters)
- Modify: `docs/twillingate.md` § Alerts (the tab, in a short paragraph), `web/src/pages/Inbox.tsx:174` (the intro mentions alerts)
- Test: the two specs; `web/src/pages/Inbox.test.tsx` if it asserts the intro

**Interfaces:**
- Consumes: everything above. `create_alert` over REST: `POST /api/projects/{id}/alerts` with `{name, template: 'event_count', fields: {event: '$page_view'}, condition: {type: 'threshold', op: 'below', value: 1}}` (a warning about a never-sent event is fine).

Behaviour:
- `alerts.spec.ts`, one test, `test.setTimeout(150_000)`: create a project and an alert via REST; log in; open the project's Settings and click the `Alerts` link; the list shows the alert's row with `Pending` or a later state; open it; the page shows `Watches` `Count of $page_view`; change the threshold to `5`, Save, toast `Saved`; the button reads `Checking…` then, within 90 s (the runner ticks each minute), re-enables (assert pending clears, not a particular outcome); the History table has at least one row; Snooze `1 day` shows `Snoozed until`; Wake; Archive shows Restore; the Archived tab lists it; Restore.
- Docs: replace "the console's Alerts tab comes later" with: the console's Alerts tab (`/projects/:id/alerts`, the bell in a project's tab row) lists a project's alerts with their last value, recent evaluations and state; an alert's page edits its name, condition, window, minimum volume and template fields, pauses, snoozes, archives and restores it, backtests a change before it is saved, shows its history, and asks for a check. Alerts are created by an agent for now.
- Inbox intro: `Sites down and back up, certificates and domains failing or about to expire, alerts crossing their line, and forms. Archive an item once it is handled.`

- [ ] **Step 1: Write** the spec and the phone additions; the docs paragraph; the Inbox copy (its own commit: `fix(web): mention alerts in the Inbox's intro`).
- [ ] **Step 2: Run** `cd web && npx playwright test e2e/alerts.spec.ts --trace off` once locally if the e2e server builds here; otherwise leave it to CI and say so.
- [ ] **Step 3: Commit** `test(web): cover the Alerts tab end to end` and `docs: describe the console's Alerts tab`.
