# Forms

Status: implemented
Date: 2026-10-05

## Problem

- **A landing page has nowhere to send its form.** Contact forms,
  waitlists, feedback and RSVPs end up in a third-party form service or
  a spreadsheet, apart from the analytics that already see the visit.
- **The visit and the submission never meet.** twillingate knows that a
  visitor came from a search engine to `/pricing`; the form service knows
  their email. Nobody holds both, so "which source brings signups" is a
  manual join, if anyone does it at all.
- **twillingate stores no personal data today, and a form is personal
  data.** It must stay out of the analytics surfaces (the `query` tool,
  aggregates, dashboards) and leave only by deletion: one at a time, by
  search for an erasure request, or with its form or project.

## Decisions

- **D1. Each submission is a record; an approved form's is also a
  conversion, deleted with it.** An accepted submission writes a row in
  a new `submissions` table (the fields, kept until deleted). On an
  approved form (D2) it also writes a `$form_submit` product event in
  `events` (the form name as its one attribute, `form`; the page and the
  actor; no field values), so
  conversions show in dashboards, funnels and retention like any product
  event; the row is what the owner reads and exports. A draft's
  submissions write no event, even once the form is approved: spam stays
  out of the analytics, at the cost of a new form's first days. The
  event carries the submission's `id` and `day`, so `events`' key
  `(family, project_id, day, id)` finds it exactly.

  Deleting a submission (by id, filters or search, or with its purged
  form) deletes its event in the same transaction while the event is
  still raw. A day rolled up past `RETENTION_EVENTS_RAW_DAYS` keeps its
  aggregates as they are: they hold counts only, never an actor or a
  field value, so nothing personal survives, but a conversion total
  stays one higher. The aggregates are not decremented: a count could
  be, the day's distinct actors could not, and two numbers that
  disagree are harder to explain than "older totals are final".

- **D2. A form is captured as a draft and must be approved.** The first
  submission naming an unknown form creates its row as a `draft` and is
  stored. A draft accepts submissions until `draft_until` (its creation
  plus `FORMS_DRAFT_DAYS`, default 7); after that ingest refuses them, the
  next daily pass archives the form, and the archived-items purge deletes
  it with its submissions (D11). This is the spam protection: a form name
  nobody approves expires with everything sent to it.

  | State | Reached by | Accepts | Keeps |
  | --- | --- | --- | --- |
  | `draft` | the first submission to an unknown name | until `draft_until` | every field |
  | `approved` | `approve_form`, which **requires** `expected_fields` | until `closes_at` | the expected fields |
  | archived | archiving by hand, or a draft past `draft_until` | nothing | (purged later) |

  Approving sets `expected_fields` (one or more), `approved_at`, and
  clears `draft_until`. An approved form never returns to draft; its
  `expected_fields` can change (`update_form`) but never become empty.
  Restoring an archived draft makes it a draft again with a fresh
  `draft_until`, or the next pass would archive it at once; restoring an
  approved form keeps it approved. There is no project-level setting and
  no cap on drafts.

- **D2a. `closes_at` stops submissions, now or at a set time.** On a
  draft or an approved form, `closes_at` NULL is open; a time in the
  future closes the form then ("the waitlist closes Friday"); setting it
  to the present stops it now; clearing it or moving it later reopens
  it. A closed form refuses new submissions (D5) and keeps its own.

- **D3. Fields are free-form until approval; then expected fields narrow
  what is kept.** A draft stores every field a submission sends, so the
  owner can see what the form really sends before approving. An approved
  form drops fields outside `expected_fields` from the row (their names
  are still recorded in `forms.fields`, so the console can show
  "arriving, not kept"). Expected never means required: a submission
  missing an expected field is stored as it is. The approval picker
  preselects the fields the draft's submissions actually sent.

- **D3a. A submission is flat text: one string per field name, no
  attachments.** `fields` is a JSON object of string to string, never
  nested. A field repeated in the form (a checkbox group, a multi-select)
  is joined into one string with `", "`. Over JSON, numbers and booleans
  become their string form; an array, object or `null` value drops that
  field. Files are never stored: multipart file parts are discarded
  unread, and they still count toward the body limit (D10), so a form
  carrying a large file is refused as too large. The docs say not to put
  file inputs in a twillingate form.

- **D4. Migration 034 adds two tables.**

  ```sql
  CREATE TABLE forms (
      project_id        INTEGER NOT NULL,
      name              TEXT    NOT NULL,   -- [a-z0-9_-]{1,64}
      purpose           TEXT    NOT NULL DEFAULT '',
      return_url        TEXT    NOT NULL DEFAULT '',
      fields            TEXT    NOT NULL DEFAULT '[]',  -- every field name seen, sorted
      status            TEXT    NOT NULL DEFAULT 'draft',  -- draft | approved
      expected_fields   TEXT,                           -- JSON list; NULL only while draft
      created_at        TEXT    NOT NULL,
      draft_until       TEXT,                           -- NULL once approved
      approved_at       TEXT,
      closes_at         TEXT,                           -- NULL = open
      last_submitted_at TEXT,
      archived_at       TEXT,                           -- NULL = active
      PRIMARY KEY (project_id, name)
  ) WITHOUT ROWID;

  CREATE TABLE submissions (
      project_id  INTEGER NOT NULL,
      id          TEXT    NOT NULL,   -- client UUID when sent, else server-made
      form        TEXT    NOT NULL,
      received_at TEXT    NOT NULL,
      fields      TEXT    NOT NULL,   -- flat JSON object, string to string (D3a)
      actor_kind  TEXT    NOT NULL,   -- as events: user | install | connection
      actor_id    TEXT    NOT NULL,
      host        TEXT    NOT NULL DEFAULT '',
      path        TEXT    NOT NULL DEFAULT '',
      via         TEXT    NOT NULL,   -- form | json
      visit       TEXT,               -- JSON snapshot (D9); NULL when unmatched
      PRIMARY KEY (project_id, id)
  );
  CREATE INDEX submissions_form ON submissions (project_id, form, received_at);
  ```

  Both tables join `projectTables`, so a purged project takes them with
  it (`TestProjectTablesMatchesSchema` enforces the listing). The
  migration is additive: no data step, no pre-check.

- **D5. One endpoint, `POST /ingest/forms/{name}`, two body styles.**
  `{name}` outside `[a-z0-9_-]{1,64}` is a plain `400`.

  - **Plain HTML form** (`application/x-www-form-urlencoded`,
    `multipart/form-data`). The key comes from `?key=` in the action URL
    (the `X-Analytics-Key` header also works). Every field not starting
    with `$` is a submission field; multipart file parts are discarded
    (D3a). The answer is a redirect (D6).

    ```html
    <form method="post" action="https://t.example.com/ingest/forms/contact?key=tw_…">
      <input name="email"> <textarea name="message"></textarea>
      <input type="hidden" name="$redirect" value="https://site.com/thanks">
    </form>
    ```

  - **JSON** (any other content type, so the SDK's `text/plain` body is
    a simple request with no preflight). The key comes from the header,
    `?key=`, or `key` in the body. Values are strings, numbers or
    booleans, stored as strings; anything else drops the field (D3a).
    The answer is `201 {"id": "…"}`.

    ```json
    { "id": "uuid",
      "fields": { "email": "a@b.c", "plan": "pro", "seats": 5 },
      "attributes": { "$install_id": "…", "$host": "site.com", "$path": "/pricing" } }
    ```

  Context keys, as `$` fields on the form path or in `attributes` on
  JSON: `$id`, `$user_id`, `$install_id`, `$host`, `$path`, and on the
  form path `$redirect`. Their meanings match events; any other `$` key
  is dropped.
  The actor follows the events rules: `$user_id`, else `$install_id`,
  else the daily connection hash, so a no-JS form POST from the browser
  that sent the visit's views gets the same actor as those views.

  Checks, in order, on both styles:

  1. The key resolves to an active project, else a plain `401` (no
     redirect: without a project nothing can vouch for a target).
  2. A present `Origin` passes `allowed_origins`, else a plain `403`.
     Browsers send `Origin` on a cross-origin form POST.
  3. The body is within the limits (D10), else `-error` / `413`.
  4. The form row is read, or created as a draft. An archived form, a
     draft past `draft_until`, or a form past `closes_at` is `-error` /
     `409`, and nothing is written.
  5. The submission is written: the form's `fields` merged, then the row
     (`INSERT OR IGNORE` on `id`, so a retried `$id` stores once), in one
     transaction, with its `$form_submit` event on an approved form,
     **written directly, not through `pipeline.Buffer`**, which drops
     its oldest entries when full and flushes later: a submission
     deleted seconds after it arrived must find its event already
     there.

- **D6. The form path redirects; twillingate renders no page.** The
  target is the first one allowed of:

  1. `$redirect` from the submission;
  2. the form's `return_url`;
  3. the `Referer`.

  A target is allowed when it is an absolute `http(s)` URL whose origin
  passes the project's `allowed_origins`; when it passes only through a
  bare `*` entry, it must also equal the request's `Origin`, so `*`
  never makes an open redirect. The answer is `303` with
  `#twillingate-form-success-{name}` or `#twillingate-form-error-{name}`
  in place of any fragment the target had. When no target is allowed,
  the submission is still stored (if it got that far) and the answer is
  a plain `400` saying the form has no return URL.

  The `Referer` usually carries only the origin on a cross-origin POST
  (the default `strict-origin-when-cross-origin` policy), so "back to the
  page" lands on the site's root. The docs tell JS-free pages to set
  `$redirect` or the form's return URL; the SDK path (D7) does not need
  either.

- **D7. The SDK: `data-twillingate-form` and `submitForm`.**

  ```html
  <form data-twillingate-form="contact">
    <input name="email"> <textarea name="message"></textarea>
  </form>
  <p id="twillingate-form-success-contact">Thanks, we'll be in touch.</p>
  <style>#twillingate-form-success-contact:not(:target){display:none}</style>
  ```

  The runtime's capture-phase `submit` listener (the one
  `data-twillingate-event` uses) handles a tagged form: it calls
  `preventDefault()`, reads `FormData` (file entries and `$` fields
  other than `$redirect` skipped, repeated names joined as in D3a), and posts the JSON body of D5 with a fresh
  `$id`, `$host`, `$path` and the identity keys the identity mode allows.
  While in flight the form has `aria-busy="true"` and a second submit is
  ignored. On the outcome:

  - with a `$redirect` field, it navigates there with the D6 fragment;
  - without one, it resets the form on success and sets `location.hash`
    to the fragment, so the same `:target` element serves both paths;
  - either way it dispatches a `twillingate:form` `CustomEvent` on the
    form, `detail: {name, status: "success" | "error", id}`.

  An `action` pointing at twillingate on the same form keeps it working
  where the SDK did not load.

  `twillingate.submitForm(name, fields)` takes a flat object (string,
  number or boolean values), a `FormData` or an `HTMLFormElement` and
  returns `Promise<{id}>`, rejecting on a `4xx` or once retries run out.
  It neither navigates nor touches the hash.

  Delivery is `fetch` with `keepalive` and three in-memory retries (1 s,
  5 s, 25 s) reusing the same `$id`. **A submission never enters the
  retry queue's storage driver**: personal data is not written to the
  visitor's device, whatever the consent. An opted-out visitor's
  submission is still sent (it is the one thing they asked to send),
  without identity keys. A tagged form fires no `data-twillingate-event`;
  the server's `$form_submit` is the one conversion.

- **D8. Tools, routes, CLI.** Through `expose`, addressed by
  `{project_id, name}`:

  | Tool | Route | Does |
  | --- | --- | --- |
  | `list_forms` | `GET /api/projects/{project_id}/forms` | every form with `status`, `purpose`, `return_url`, `fields`, `expected_fields`, `draft_until`, `approved_at`, `closes_at`, submission count, `last_submitted_at`, `archived` |
  | `approve_form` | `POST …/forms/{name}/approve` | `expected_fields` (required, one or more); a draft becomes approved (D2); approving an approved form is `ErrConflict` |
  | `update_form` | `PATCH /api/projects/{project_id}/forms/{name}` | merges `purpose`, `return_url`, `closes_at` (`null` reopens), and on an approved form `expected_fields` (never empty); `return_url` must be an allowed target (D6), else `ErrInvalid`; `expected_fields` on a draft is `ErrInvalid` (approve it instead) |
  | `archive_form` / `restore_form` | `POST …/forms/{name}/archive` / `restore` | hides or restores the form and its submissions; restoring a draft gives it a fresh `draft_until` |
  | `list_submissions` | `GET /api/projects/{project_id}/forms/{name}/submissions` | one form's submissions as a table (D12a): `columns`, `rows`, `matched`, `total`; takes `filters`, `sort`, `offset`, `limit`, `distinct`, the arguments `widget_data` takes for a remote table; newest first without a sort |
  | `find_submissions` | `GET /api/projects/{project_id}/submissions` | `search` (required), `limit`, `cursor`: every active form's submissions with a field value containing `search`, case-insensitive (`json_each`), for an erasure request; each with its form and fields |
  | `delete_submissions` | `POST /api/projects/{project_id}/submissions/delete` | exactly one of `ids`; `form` with `filters` (the table's filters); or `search` (as `find_submissions`); destructive; the audit row holds the count and the selector, never the contents |
  | REST only | `GET …/forms/{name}/submissions.csv` | the D12a columns, with the same `filters` and `sort`, every matching row (no paging) |

  Filtering a form's table and then deleting with the same `filters`
  removes exactly what the table showed; `find_submissions` then
  `delete_submissions` with the same `search` does the same across forms.
  Submissions of archived forms are left out of all of them. There is no
  `create_form`. `integration_guide` gains a forms section.

  CLI, one noun: `twillingate form list | approve | update | archive | restore |
  export | erase`; `export` writes the CSV to stdout, `erase` deletes
  submissions by `-id` or `-search`.

- **D8a. Custom SQL cannot read submissions.** Today `readsql.Check`
  refuses only `meta` and SQLite's internals, so every table, `events`
  included, is readable from `query` and widget SQL; a new `submissions`
  table would be too. `readsql.Open` gains a list of further names to
  refuse (so `readsql` stays free of twillingate's domain), and the
  console's custom-SQL handle passes `submissions`: `query`, widgets and
  `schemaViews` never reach a submission, and a test pins that each of
  them refuses it. The submissions table (D12a) runs SQL the server
  builds, not user text, on a second handle opened without that name, so
  it reuses `QueryPage`'s filters, sort and paging unchanged.
  `$form_submit` events stay queryable through `raw_product` like any
  product event.

- **D9. The visit is snapshotted onto the submission.** At write time the
  handler reads the actor's current session from `events` (the 30-minute
  gap rule the session views use) and stores `visit` as JSON:
  `landing_path`, `referrer`, `utm_source`, `utm_medium`, `utm_campaign`,
  `views`. A join at read time would vanish after
  `RETENTION_EVENTS_RAW_DAYS` while the submission is kept forever.
  `visit` is NULL when nothing matches (a server-side API call, a page
  without the SDK). Views still in the pipeline buffer (up to the flush
  interval) are missed; a visitor who filled a form landed earlier.

- **D10. Fixed limits**, in `internal/wire` and listed by `limits` under
  `ingest`: body 64 KiB, 100 fields, field name 64 characters, value
  8 KiB (longer values are truncated, as event values are). No cap on
  forms per project or submissions per form.

- **D10a. `FORMS_DRAFT_DAYS`** (default 7, at least 1) sets a draft's
  window. It is read when a draft is
  created or restored, so changing it moves no existing `draft_until`.
  `limits` lists it under `retention`.

- **D11. Archiving and purging.** Archiving a form hides it and its
  submissions from lists, export and counts and refuses new submissions;
  restoring brings all back. The daily pass first archives every draft
  past `draft_until` (audit actor `retention`, `form.expire`), then
  purges a form archived longer than `RETENTION_ARCHIVED_DAYS` with its
  submissions and whatever of their events is still raw (in
  `PurgeArchived`, beside dashboards), one transaction and audit row per
  form. Submissions are otherwise kept until deleted. Archiving the
  project hides its forms; purging it deletes them.

- **D12. Console.** Since #138 a project page is a row of tabs: Setup,
  then the dashboards pinned to the project. Forms become a fixed tab
  after Setup, so the list sits on the project page and a form opens
  inside it:

  - **The Forms tab** (`/projects/:id/forms`): one row per form with its
    status (`Draft · expires in 5 days`, `Approved`, `Closes Oct 9`,
    `Closed`), purpose, submission count, last submission, and a menu
    (Approve…, Stop now, archive). Drafts sort first. Archived forms appear on the Archive page. The
    tab's header holds **Find a person**, a search across every form
    (`find_submissions`) with "delete all" and a confirm, for erasure
    requests. With no forms yet, the tab shows a short "add a form" hint
    with a snippet, as the Keys section shows its own.
  - **A form** (`/projects/:id/forms/:name`), opened by clicking its row,
    with the Forms tab still selected and a crumb back to the list: the
    submissions table (D12a), and settings:
    - on a draft, a banner (`Draft: accepting until Oct 12, then
      archived`) with **Approve**, which opens the expected-fields picker
      (one checkbox per field seen, preselected from what submissions
      sent) and approves with the chosen fields;
    - on an approved form, the same picker to change the fields, with
      "not kept" on fields arriving outside the list;
    - purpose and return URL;
    - **Closing**: `Open`, or a date and time it closes, with **Stop
      now** and **Reopen**.

  Like Setup, the Forms tab is not a dashboard: it cannot be moved,
  removed or duplicated, and `tabPath` gains its two paths. It keeps the
  URL's range for the other tabs, but the table ignores it: it lists
  every submission, and a `Received` filter narrows by date.

- **D12a. Submissions are a table with filters.** The form page shows
  the dashboards' `table` component in remote mode, the same filter bar
  (one chip per filter, `=`, `!=`, `<`, `>`, `in`, `not in`, values
  picked from `distinct`), sort cycle, paging footer and per-viewer state
  in localStorage, so filters mean the same here as on a widget. Its rows
  come from `list_submissions` rather than `widget_data`. Columns, in
  order:

  - `Received` (`received_at`);
  - one per field: on an approved form the expected fields in their
    order, on a draft every field in `forms.fields` (`json_extract(fields, '$."<name>"')`,
    the name quoted, so a field named like a column is safe);
  - `Page` (`host` + `path`), `Referrer`, `UTM source`, `UTM medium`,
    `UTM campaign` (from `visit`).

  A field name that collides with one of the fixed columns is shown as
  `<name> (field)`. The rows' ids come back beside them (`ids`, one per
  row), not as a column. A row opens a drawer with every stored field
  (including ones no longer expected), the full visit, and **Delete**;
  with filters set, **Delete all matching** sends the same `filters`.
  **CSV** downloads what the filters match.

## Revised during implementation

- A retry of an already-stored submission `id` is idempotent success
  (`201`, same id, no second event) even after the form closed or was
  archived; the id is checked before the form's state.
- `visit` needs a view of the actor within 30 minutes of the submission;
  an older newest view gives no visit.
- A refusal with no allowed redirect target answers its own status
  (`409` closed, `413` too large, `400` bad body); the plain `400` of D6
  applies only after a stored submission has nowhere to go.
- The delete audit row holds the selector kind, the form (for filters)
  and the count, never search text or filter values: those are usually
  the erased person's email.
- CSV cells starting with `=`, `+`, `-`, `@`, tab or CR get a leading `'`.
- Delete by ids may reach an archived form's submissions; lists, find and
  filters stay active-only.
- Approving needs expected fields (`ErrInvalid` when empty) and refuses an
  archived form ("restore it first"); the store itself accepts both.
- The CLI flag is `-project-id`, as on `key`, not `-project`.
- `list_forms` carries `action_base` (`PUBLIC_URL` + `/ingest/forms`, empty
  when unset), which the console's snippet uses.
- A draft's submissions never write `$form_submit`, not even after the
  form is approved.
- The redirect check is `Snapshot.RedirectAllowed`; `return_url` is
  validated with it. The config field is `cfg.Forms.DraftDays`.
- The submissions query names its columns by position (`c0` is the id,
  `c1`… the display columns); a field name reaches the SQL only inside a
  quoted JSON path. `manage.QuerySubmissions` translates filter, sort and
  distinct columns from display names and the result's back, so a field
  called `meta`, `dbstat`, `sqlite_*` or `pragma_*` (names readsql refuses
  as identifiers) reads like any other.
- A field name holding a control character (U+0000 to U+001F, U+007F) is
  dropped at ingest and refused in `expected_fields`: a NUL would end the
  SQL that reads it.

## Where the code goes

No new package; the archtest rank table is unchanged.

| Package | Change |
| --- | --- |
| `store` | `Form`, `Submission` row types and their methods |
| `store/sqlite` | `034_forms.sql`; `projectTables`; `PurgeArchived` |
| `server` | `forms.go`: the endpoint, decoding, D6, field filtering; takes a `server.FormStore` (as `NameStore` today), passed by `app`, which writes the submission and, on an approved form, its `$form_submit` in one transaction |
| `wire` | the D10 limits |
| `config` | `FORMS_DRAFT_DAYS` (D10a) |
| `jobs` | expire drafts before `PurgeArchived` (D11) |
| `manage` | audited `approve_form`, `update_form`, archive, restore, erase; `manage.Store` grows by those. Forms stay out of the registry snapshot: ingest reads the row per submission |
| `api` | `ops_forms.go`: tools and the CSV route; the second `readsql` handle (D8a) |
| `shared/readsql` | `Open` takes further refused names (D8a) |
| `cmd` | `twillingate form` |
| `sdk`, `web` | D7, D12 |

`$form_submit` becomes a reserved event name.

## Out of scope

- Outbound delivery: email, webhooks. The `submissions` row is shaped so
  a webhook can be added later without a migration.
- Spam checks on a submission (honeypot, timing, rate limits, CAPTCHA).
  Drafts that expire unapproved (D2), `expected_fields`, `closes_at` and
  the limits (D10) are the controls in this version.
- Required fields and field types.
- File uploads and nested or list values (D3a).

## Docs

- `docs/twillingate.md`: the endpoint and both body styles, the redirect
  and fragment rules with the `Referer` caveat, `$form_submit` among the
  reserved event names, the SDK attribute, method and event, the tools,
  routes and CLI, the limits; that a draft's submissions never count as
  conversions, and that deleting a submission removes its conversion
  from the raw window only.
- `integration_guide`: a forms section.
- `deploy/UPGRADES.md`: 034, additive.
- `docs/deployment.md`: `FORMS_DRAFT_DAYS`.

## Tests

- `migration034_test.go`; store tests for idempotent ids, `fields`
  merging, project purge and archived-form purge.
- Jobs: a draft past `draft_until` is archived by the daily pass and
  purged `RETENTION_ARCHIVED_DAYS` later with its submissions; an
  approved form is never expired; restore gives a fresh window.
- Store: deleting a submission deletes its raw event; a rolled-up day's
  aggregates are untouched.
- `readsql`: a refused name passed to `Open` is refused by `Check` in
  every form `meta` is (bare, quoted, as a string); `query` and widget
  SQL reading `submissions` are refused.
- Server: both body styles and multipart; key from the query; the
  Origin check; the D6 order and every open-redirect case (foreign
  origin, `javascript:`, relative URL, bare `*`, empty
  `allowed_origins`); the fragments; a draft keeps every field and
  writes no `$form_submit`; a draft past `draft_until`, a form past
  `closes_at` and an archived form refused; approved-form field
  filtering; the limits; on an approved form `$form_submit` written with the
  submission, same `id`; the
  `visit` snapshot.
- API: tool tests, including `approve_form` without fields refused and
  `expected_fields` on a draft refused; `docs_sync_test`, `coverage_test`, `openapi_test`
  pick up the new tools.
- SDK (vitest): `preventDefault`, the hash and redirect, the
  `CustomEvent`, double submit ignored, retries reuse `$id`, nothing
  written to storage.
- Web: vitest; Playwright `forms.spec.ts` against the built binary
  (submit a plain form, see the draft in the console, approve it with
  expected fields, filter the table, stop it, export CSV, erase by search); the cursor and phone specs cover the
  new pages.

## Delivery

Four stacked pull requests:

1. `feat(server)`: migration, store, endpoint, `$form_submit`, `visit`,
   the `readsql` refusal (D8a, so the table is never queryable, not even
   for one release), docs. Works for plain HTML forms and the JSON API on its own.
2. `feat(sdk)`: `data-twillingate-form`, `submitForm`.
3. `feat(api)`: tools, CSV, CLI.
4. `feat(web)`: the Forms pages.
