# Alerts wizard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create an alert in the console: a New alert wizard with the four templates (pick a template, fill its fields, pick the condition, see the backtest, name it and save).

**Architecture:** Build-order step 3 of the alerts design, stacked on the Alerts tab (PR #285, branch `feat/alerts-tab`; this branch is `feat/alerts-wizard`). Web only, plus docs: the backend's `create_alert` and `preview_alert` already take `{template, fields, condition, window_days, min_volume}`. The wizard reuses the editor's pieces by building a *blank alert* — an `Alert`-shaped value for the chosen template that has not been saved — so `ConditionFields`, `TemplateFields`, the draft helpers and the backtest's result view work unchanged.

**Tech Stack:** React 19 + TypeScript, TanStack Query, react-router, shadcn/ui (Dialog), Tailwind, Vitest + Testing Library, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-24-alerts-design.md` ("Templates and the wizard", "Backtest", "The Alerts tab" — its `[+ New alert]`). Backend contract: `docs/twillingate.md` § Alerts and § Templates.

## Global Constraints

- The wizard is a dialog titled `New alert`, opened by a `New alert` button (with `PlusIcon`) on the Alerts list (Live view, beside the list; in the empty state too). Steps: **1 Template** → **2 What and when** (the template's fields, then the condition, window and minimum volume) → **3 Backtest and name** → `Create alert`. `Back` returns a step keeping what was entered; closing the dialog discards it.
- Templates, in this order, with this copy (title — description):
  - `event_count`: `Event count` — `How many times an event happens a day, or over a few days.`
  - `unique_people`: `Unique people` — `How many visitors or signed-in users do something.`
  - `conversion`: `Conversion` — `The share of people who do one event and then another.`
  - `no_events`: `No events` — `An event stops arriving for some hours.`
- Blank alert per template: `kind` `stat` (event_count, unique_people), `funnel` (conversion), `silence` (no_events); `unit` `count`, `count`, `fraction`, `hours`; `read` `{metric:'count'}` for event_count, `{metric: who}` for unique_people (`actors` default), `{step:'2', from:'start'}` for conversion, `{}` for no_events; `window_days` 1, or 7 for conversion; `min_volume` 0; default condition: threshold `below` with an empty value for the metric templates, silence `hours: 6` for no_events; `template` and `fields: {}`; `id` 0, empty strings elsewhere, `recent: []`. Only fields the draft helpers read matter; nothing reads `id` 0 as a saved alert (the backtest must not run its saved-alert query for it).
- Required fields per step 2: event_count `event`; conversion `from_event` and `to_event`; no_events `event`; unique_people none. Step 2's `Next` is disabled until the fields and the condition have no errors (`draftErrors`, ignoring `name`).
- Step 3 runs `preview_alert` with the draft's body once on entering the step (and again if the user goes back, changes the definition and returns), shows the backtest result (the same strip/gaps and "Would have fired …" sentence as the alert page), and a `Name` input prefilled with a suggested name the user can change. Suggested names: for the metric templates `describeSource` of the blank alert with the draft's fields + ` ` + `describeCondition` (e.g. `Count of signup below 5 over 1 day`); for no_events `No <event> for <hours> hour(s)` (with a path: `No <event> on <path> for 6 hours`); at most 100 characters (the server's limit), cut at a word boundary.
- `Create alert` sends `create_alert` with `{name, template, fields, condition, window_days, min_volume}` (window not for silence, min volume only where taken, empty fields dropped). On success: close, toast `Alert created`, put it in the live list's cache, navigate to its page (`alertPath`). `warnings` in the answer: toast `Never sent yet: <names>` (the editor's parsing of the server's sentence). A 409 (name taken, or at the live-alert limit) shows the server's message under the Name input and stays on step 3; other refusals the same, under the step.
- At `max_live` live alerts the `New alert` button is disabled with the tooltip `A project holds at most <n> live alerts`. An archived project shows no `New alert` button.
- Everything clickable is a button/link (template choices are buttons, `aria-pressed` on the chosen one, or a radio group). Nothing scrolls sideways at 360px: the dialog is full width on a phone, the template cards are `grid-cols-1 sm:grid-cols-2`.
- Docs in the same commit: `docs/twillingate.md` § Alerts, the console paragraph ("Alerts are created by an agent for now" becomes: created by an agent or with the console's New alert wizard from the four templates).
- Commits: Conventional Commits, `feat(web)` for the wizard, `refactor(web)`/`test(web)` otherwise; trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; no model names elsewhere.
- Locally only focused vitest + typecheck, and the one new e2e spec once; CI runs the rest.

## Review Focus

1. Going Back from step 3 to step 2, changing the event, then Next again must show a backtest of the new definition, not the cached old one. Test in Task 3.
2. A conversion threshold typed as `3` must create `value: 0.03` (percent input for fraction). Test in Task 2.
3. A project with `$page_view` only (no product events): event_count must still be creatable on `$page_view` with a path. Test in Task 3 (TemplateFields already offers `$page_view` first).
4. Double-clicking `Create alert` must create one alert. Test in Task 3.
5. A name the server refuses (taken) keeps the dialog open on step 3 with every entered value. Test in Task 3.

---

### Task 1: Shared form pieces and the backtest result view

**Files:**
- Create: `web/src/components/alerts/Field.tsx` (move `Field` and `errorProps` out of `ConditionFields.tsx`); `web/src/components/alerts/BacktestResult.tsx` (move `Result`, `Answer`, `firedText`, `local`, `OUTCOME_COLOURS` out of `AlertBacktest.tsx`, exported as `BacktestResult({ alert, preview })` and `BacktestAnswer({ alert, query })`)
- Modify: `ConditionFields.tsx`, `AlertEditor.tsx`, `AlertBacktest.tsx`, `TemplateFields.tsx` (use `Field` for its labelled controls; show a required-field error under the field it belongs to: `errors?: Partial<Record<string, string>>` keyed by field name replaces the single `error` string, and the editor passes its template errors keyed that way — `draftErrors` gains per-field keys `fields.<name>` or a separate `templateFieldErrors(template, fields)` helper in `alert-draft.ts` returning `Record<string,string>` with copy `Pick an event`).
- Test: existing alert tests stay green; add `templateFieldErrors` tests; TemplateFields test that the error shows under the empty event field.

- [ ] Write the failing tests (helper + per-field error placement), run, implement the moves and the helper, run `npx vitest run src/components/alerts src/lib/alert-draft.test.ts` and the typecheck, commit `refactor(web): share the alert form's field and backtest pieces`.

### Task 2: Blank alerts, suggested names and the create body

**Files:**
- Create: `web/src/lib/alert-wizard.ts`, `web/src/lib/alert-wizard.test.ts`
- Modify: `web/src/lib/api.ts` (`endpoints.createAlert(projectId, body: AlertSource & { name: string; condition: AlertCondition; window_days?: number; min_volume?: number }) => Promise<Alert>` — POST `/api/projects/{id}/alerts`), `web/src/test/alerts.ts` if a fixture helps

**Interfaces (produced):**
- `TEMPLATES: { id: Template; title: string; description: string }[]` — Global Constraints' order and copy.
- `blankAlert(projectId: number, template: Template): Alert` — Global Constraints' values.
- `wizardAlert(blank: Alert, draft: AlertDraft): Alert` — the blank with the draft's fields (and, for unique_people, `read.metric` following `who`), for `describeSource`/`takesMinVolume`.
- `suggestName(blank: Alert, draft: AlertDraft): string` — Global Constraints' rule, capped at 100 characters.
- `createBody(blank: Alert, draft: AlertDraft)` — `{ name: draft.name.trim(), template, fields (empty and default-valued keys dropped, as the editor's `sentFields` does for a new alert), condition, window_days?, min_volume? }`, built on `previewBody`'s rules so preview and create agree.
- `stepTwoErrors(blank, draft)` — `draftErrors` without `name`, plus `templateFieldErrors`.

- [ ] Failing tests: `blankAlert` per template (kind, unit, window, condition type, read); `suggestName` for each template (`Count of signup below 5 over 1 day`, `Conversion from signup_start to signup_done below 3% over 7 days`, `No heartbeat for 6 hours`, `Visitors doing signup above 100 over 1 day`, a long event name capped at 100); `createBody` for conversion with `3` typed → `condition.value === 0.03`, `window_days: 7`; for no_events → no `window_days`, no `min_volume`; for event_count threshold → no `min_volume`; for a change condition → `min_volume` when set; `who: 'actors'` dropped. Run, implement, run, commit `feat(web): build a new alert from a template` — or fold into Task 3's commit if nothing user-facing ships yet (prefer `refactor(web):` here then).

### Task 3: The New alert dialog

**Files:**
- Create: `web/src/components/alerts/NewAlertDialog.tsx`, `NewAlertDialog.test.tsx`
- Modify: `web/src/components/alerts/AlertsList.tsx` (the `New alert` button, disabled at `max_live`, hidden on an archived project and in the Archived view; the empty state's copy becomes `Create one from a template, or ask an agent connected over MCP: for example, to alert you when signups drop 30% against the week before.` with the button), `web/src/hooks/use-alert-actions.ts` only if a `create` belongs there (it writes the live list cache and invalidates `['alerts', projectId]`, as the other actions do)
- Modify: `docs/twillingate.md` § Alerts console paragraph

**Behaviour:** as Global Constraints. Step indicator text `Step <n> of 3`. Step 1: four template buttons (title + description); choosing one goes to step 2 (re-choosing a different template after going back resets the fields and condition to that template's blank). Step 2: `TemplateFields` then `ConditionFields` for the blank alert; `Back`, `Next`. Step 3: `BacktestAnswer` of `preview_alert(createBody without name)` keyed on the body (so a changed definition re-runs, an unchanged one reuses), `Name` input (prefilled with `suggestName` when step 3 is first reached and the name is still the previous suggestion or empty), `Back`, `Create alert` (disabled while pending or the name is empty).

- [ ] Failing tests: opens from the list's button; the four templates in order; event_count path: pick event `signup`, threshold below `5`, Next → preview called with `{template:'event_count', fields:{event:'signup'}, condition:{type:'threshold', op:'below', value:5}, window_days:1}`, name prefilled `Count of signup below 5 over 1 day`, Create → `createAlert` called once even on a double click, navigates to `/projects/1/alerts/<new id>`, toast; Back from step 3, change the event, Next → a second preview with the new event; a 409 on create shows the message under Name and stays on step 3 with the values; button disabled at `max_live` with the tooltip; no button on an archived project; no_events shows only Hours in step 2 and no Window. Run, implement, run (`npx vitest run src/components/alerts src/lib`) and the typecheck, commit `feat(web): create an alert from a template in the console` (docs in the same commit).

### Task 4: End to end, phone, cursor

**Files:**
- Modify: `web/e2e/alerts.spec.ts` (a new test: open Alerts, New alert, Event count on `$page_view`, threshold below 1, Next, the backtest sentence appears, keep the suggested name, Create alert → the alert's page with `Watches` `Count of $page_view`; then the list has its row), `web/e2e/phone.spec.ts` (the dialog's three steps at 360px have no sideways scroll), `web/e2e/cursor.spec.ts` (the dialog's controls: template buttons, Back/Next/Create)

- [ ] Write them; run `cd web && npx playwright test e2e/alerts.spec.ts --trace off` once (Go at `/usr/local/go/bin`); leave phone/cursor to CI unless a `-g` run is cheap; clean `/tmp/twillingate-e2e-*`; commit `test(web): create an alert through the wizard end to end`.
