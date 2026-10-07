# Forms

Status: draft
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

- **D1. Each submission is a record and a conversion.** An accepted
  submission writes a row in a new `submissions` table (the fields, kept
  until deleted) and a `$form_submit` product event in `events` (the form
  name, the page and the actor, no field values). The event makes
  conversions show in dashboards, funnels and retention like any product
  event; the row is what the owner reads and exports.

- **D2. Forms are created by their first submission and are open.** A
  submission naming an unknown form creates its row and is stored. There
  is no approval step and no project-level setting. The owner restricts a
  form afterwards (D3), stops it (`accepting`), or archives it.

- **D3. Fields are free-form; expected fields only narrow what is kept.**
  Every field a submission sends is stored, as JSON, until the form has
  `expected_fields`. Then fields outside the list are dropped from the
  row (their names are still recorded in `forms.fields`, so the console
  can show "arriving, not kept"). Expected never means required: a
  submission missing an expected field is stored as it is. The console
  picks expected fields from the names submissions actually sent.

- **D3a. A submission is flat text: one string per field name, no
  attachments.** `fields` is a JSON object of string to string, never
  nested. A field repeated in the form (a checkbox group, a multi-select)
  is joined into one string with `", "`. Over JSON, numbers and booleans
  become their string form; an array, object or `null` value drops that
  field. Files are never stored: multipart file parts are discarded
  unread, and they still count toward the body limit (D10), so a form
  carrying a large file is refused as too large. The docs say not to put
  file inputs in a twillingate form.

- **D4. Migration 031 adds two tables.**

  ```sql
  CREATE TABLE forms (
      project_id        INTEGER NOT NULL,
      name              TEXT    NOT NULL,   -- [a-z0-9_-]{1,64}
      purpose           TEXT    NOT NULL DEFAULT '',
      return_url        TEXT    NOT NULL DEFAULT '',
      fields            TEXT    NOT NULL DEFAULT '[]',  -- every field name seen, sorted
      expected_fields   TEXT,                           -- JSON list; NULL keeps every field
      accepting         INTEGER NOT NULL DEFAULT 1,
      created_at        TEXT    NOT NULL,
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
  4. The form row is read or created. An archived form, or one with
     `accepting` off, is `-error` / `409`, and nothing is written.
  5. The submission is written: the form's `fields` merged, then the row
     (`INSERT OR IGNORE` on `id`, so a retried `$id` stores once), in one
     transaction **written directly, not through `pipeline.Buffer`**,
     which drops its oldest entries when full. `$form_submit` then goes
     through the buffer like any event.

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
  | `list_forms` | `GET /api/projects/{project_id}/forms` | every form with `purpose`, `return_url`, `fields`, `expected_fields`, `accepting`, submission count, `last_submitted_at`, `archived` |
  | `update_form` | `PATCH /api/projects/{project_id}/forms/{name}` | merges `purpose`, `return_url`, `expected_fields` (`null` keeps every field), `accepting`; `return_url` must be an allowed target (D6) or `ErrInvalid` |
  | `archive_form` / `restore_form` | `POST …/forms/{name}/archive` / `restore` | hides or restores the form and its submissions |
  | `list_submissions` | `GET /api/projects/{project_id}/forms/{name}/submissions` | one form's submissions as a table (D12a): `columns`, `rows`, `matched`, `total`; takes `filters`, `sort`, `offset`, `limit`, `distinct`, the arguments `widget_data` takes for a remote table; newest first without a sort |
  | `find_submissions` | `GET /api/projects/{project_id}/submissions` | `search` (required), `limit`, `cursor`: every active form's submissions with a field value containing `search`, case-insensitive (`json_each`), for an erasure request; each with its form and fields |
  | `delete_submissions` | `POST /api/projects/{project_id}/submissions/delete` | exactly one of `ids`; `form` with `filters` (the table's filters); or `search` (as `find_submissions`); destructive; the audit row holds the count and the selector, never the contents |
  | REST only | `GET …/forms/{name}/submissions.csv` | the D12a columns, with the same `filters` and `sort`, every matching row (no paging) |

  Filtering a form's table and then deleting with the same `filters`
  removes exactly what the table showed; `find_submissions` then
  `delete_submissions` with the same `search` does the same across forms.
  Submissions of archived forms are left out of all of them. There is no
  `create_form`. `integration_guide` gains a forms section.

  CLI, one noun: `twillingate form list | update | archive | restore |
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

- **D11. Archiving and purging.** Archiving a form hides it and its
  submissions from lists, export and counts and refuses new submissions;
  restoring brings all back. The daily pass purges a form archived longer
  than `RETENTION_ARCHIVED_DAYS` with its submissions (in
  `PurgeArchived`, beside dashboards), one transaction and audit row per
  form. Submissions are otherwise kept until deleted. Archiving the
  project hides its forms; purging it deletes them.

- **D12. Console.** Since #138 a project page is a row of tabs: Setup,
  then the dashboards pinned to the project. Forms become a fixed tab
  after Setup, so the list sits on the project page and a form opens
  inside it:

  - **The Forms tab** (`/projects/:id/forms`): one row per form with its
    purpose, submission count, last submission, the `accepting` switch
    and a menu (archive). Archived forms appear on the Archive page. The
    tab's header holds **Find a person**, a search across every form
    (`find_submissions`) with "delete all" and a confirm, for erasure
    requests. With no forms yet, the tab shows a short "add a form" hint
    with a snippet, as the Keys section shows its own.
  - **A form** (`/projects/:id/forms/:name`), opened by clicking its row,
    with the Forms tab still selected and a crumb back to the list: the
    submissions table (D12a), and settings (purpose, return URL, and the
    expected-fields picker, one checkbox per field seen, with "not kept"
    on fields arriving outside the list).

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
  - one per field: the expected fields in their order, or every field in
    `forms.fields` when none are set (`json_extract(fields, '$."<name>"')`,
    the name quoted, so a field named like a column is safe);
  - `Page` (`host` + `path`), `Referrer`, `UTM source`, `UTM medium`,
    `UTM campaign` (from `visit`).

  A field name that collides with one of the fixed columns is shown as
  `<name> (field)`. Selecting rows enables **Delete**; with filters set,
  **Delete all matching** sends the same `filters`. **CSV** downloads
  what the filters match. A row opens a drawer with every stored field
  (including ones no longer expected) and the full visit.

## Where the code goes

No new package; the archtest rank table is unchanged.

| Package | Change |
| --- | --- |
| `store` | `Form`, `Submission` row types and their methods |
| `store/sqlite` | `031_forms.sql`; `projectTables`; `PurgeArchived` |
| `server` | `forms.go`: the endpoint, decoding, D6, field filtering; takes a `server.FormStore` (as `NameStore` today), passed by `app`; `$form_submit` through `Enqueuer` |
| `wire` | the D10 limits |
| `manage` | audited `update_form`, archive, restore, erase; `manage.Store` grows by those. Forms stay out of the registry snapshot: ingest reads the row per submission |
| `api` | `ops_forms.go`: tools and the CSV route; the second `readsql` handle (D8a) |
| `shared/readsql` | `Open` takes further refused names (D8a) |
| `cmd` | `twillingate form` |
| `sdk`, `web` | D7, D12 |

`$form_submit` becomes a reserved event name.

## Out of scope

- Outbound delivery: email, webhooks. The `submissions` row is shaped so
  a webhook can be added later without a migration.
- Spam checks (honeypot, timing, rate limits, CAPTCHA). The limits (D10),
  `expected_fields` and `accepting` are the controls in this version.
- Required fields and field types.
- File uploads and nested or list values (D3a).

## Docs

- `docs/twillingate.md`: the endpoint and both body styles, the redirect
  and fragment rules with the `Referer` caveat, `$form_submit` among the
  reserved event names, the SDK attribute, method and event, the tools,
  routes and CLI, the limits.
- `integration_guide`: a forms section.
- `deploy/UPGRADES.md`: 031, additive.
- No environment variables change.

## Tests

- `migration031_test.go`; store tests for idempotent ids, `fields`
  merging, project purge and archived-form purge.
- `readsql`: a refused name passed to `Open` is refused by `Check` in
  every form `meta` is (bare, quoted, as a string); `query` and widget
  SQL reading `submissions` are refused.
- Server: both body styles and multipart; key from the query; the
  Origin check; the D6 order and every open-redirect case (foreign
  origin, `javascript:`, relative URL, bare `*`, empty
  `allowed_origins`); the fragments; `accepting` off; archived form;
  expected-field filtering; the limits; `$form_submit` queued; the
  `visit` snapshot.
- API: tool tests; `docs_sync_test`, `coverage_test`, `openapi_test`
  pick up the new tools.
- SDK (vitest): `preventDefault`, the hash and redirect, the
  `CustomEvent`, double submit ignored, retries reuse `$id`, nothing
  written to storage.
- Web: vitest; Playwright `forms.spec.ts` against the built binary
  (submit a plain form, see it in the console, set expected fields,
  filter the table, export CSV, erase by search); the cursor and phone specs cover the
  new pages.

## Delivery

Four stacked pull requests:

1. `feat(server)`: migration, store, endpoint, `$form_submit`, `visit`,
   the `readsql` refusal (D8a, so the table is never queryable, not even
   for one release), docs. Works for plain HTML forms and the JSON API on its own.
2. `feat(sdk)`: `data-twillingate-form`, `submitForm`.
3. `feat(api)`: tools, CSV, CLI.
4. `feat(web)`: the Forms pages.
