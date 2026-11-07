# Notifications: inbox and delivery — design

Date: 2026-11-07. Status: draft, awaiting approval.

## Intent

Monitoring (#254) records whether a site is up and when its certificate and
domain expire, but nothing tells the owner. They find out a site was down by
looking at a dashboard, after the fact. The first job of this work is the
3am case: the site goes down and the owner's phone buzzes.

Underneath that sits a general mechanism every later feature will reuse:

- **The inbox** is the one place every notification lands. It is the required
  channel for all current and future communication from twillingate, so
  nothing is ever only sent somewhere else.
- **Delivery** pushes every notification, of every kind, to one extra channel
  the operator configures (the owner prefers Telegram; others use other
  services). There is no split by kind: inbox and channel get the same set.

## Goals

- A notification model with two kinds:
  - **Failing states.** A watched thing has a regular and a failing state. On
    the first failure: a notification. While it keeps failing: a reminder
    every X hours. On recovery: a notification.
  - **Info.** One-off messages with no recovery: a certificate or domain that
    expires in N days, a new form submission.
- First sources: site availability, certificate errors, domain (RDAP) lookup
  failures, certificate and domain expiry warnings, form drafts created and
  discarded, new form submissions.
- An inbox with active and archived items. Archiving is the owner saying "seen,
  dealt with". Archived items are deleted under the retention rules that
  already apply to archived things.
- One optional external channel, configured by an environment variable as one
  URL in shoutrrr's URL format, sent by our own small set of senders.
- The inbox in the web console, and over MCP and REST.

## Non-goals

- Preferences of any kind at this stage: no per-type, per-project or
  per-channel choices, no quiet hours, no on/off switch per monitor. Everything
  is on, with defaults, except the one per-form submission setting below.
- More than one external channel.
- Alerts on arbitrary widgets ("signups below 5"). The word "per widget" in the
  original ask points there; it is a later stage. The model must not make it
  hard, but nothing is built for it now.
- shoutrrr as a dependency. Only its URL format is borrowed.
- Multiple users or per-user inboxes. The console has one operator; the inbox is
  theirs.

## Anti-goals

- A false alarm at 3am. One timed-out check or one flaky RDAP answer must not
  page anyone.
- A notification that exists only in Telegram (or wherever). If it was sent, it
  is in the inbox, even when the external send failed.
- A delivery failure that blocks or slows monitoring or ingest.

## The model

### Failing states

Each source has a key (project, kind, subject) and a state: regular or failing.
The state, the time it started failing and the time of the last reminder are
kept in SQLite, so a restart does not re-announce an outage or lose one.

| Source | Subject | Fails when | Recovers when |
| --- | --- | --- | --- |
| Availability | origin | 2 failed checks in a row | 1 successful check |
| Certificate | origin | a certificate was read and fails verification (expired, untrusted, wrong host); or no certificate could be read for 24 hours, as for RDAP | a reading with no error |
| Domain lookup | main domain | RDAP lookups have failed for 24 hours (see below) | one successful lookup |

Messages (wording is illustrative, the times in the operator's view of time are
the builder's call):

- First failure: `example.com is down since 14:02`. For the reason, the
  failure reason from the check's closed vocabulary.
- Reminder, every `NOTIFY_REMIND_HOURS` (default 4) while still failing:
  `example.com is still down since 14:02 (4h)`. One interval for all kinds.
- Recovery: `example.com is up, was down since 14:02 (4h 12m)`.

Domain lookup: today a failed RDAP read stores nothing and is retried after an
hour (`dailyRetry`), and the last good expiry stands. The domain reading must
now also record the failure (error text and since when). The owner asked to be
warned about RDAP failures; to keep the anti-goal, the failing state starts
only after lookups have failed continuously for 24 hours (in practice about 24
hourly attempts). This is my call; it replaces the "2 daily passes" said in
conversation, because a failed read is retried hourly, not daily.

Certificate details (my calls, after the builder check): while an origin's
availability is failing, its certificate state neither fails nor reminds, so
one outage pages once. While the certificate state is failing, its certificate
is read hourly instead of daily, so a renewal shows as recovered within the
hour and reminders never repeat a day-old reading. "No certificate could be
read" (timeout, DNS, handshake) gets the same 24-hour rule as RDAP, because
one timed-out daily read must not page.

A monitor that is switched off, or a project archived, ends its failing states
without a recovery message; any items already in the inbox stay.

### Info notifications

| Source | Fires |
| --- | --- |
| Certificate expiring | at 14, 7 and 1 days left, once per step; a later expiry (renewal) resets the steps |
| Domain expiring | at 30, 7 and 1 days left, once per step; a renewal resets the steps |
| Form draft created | the first submission to an unknown form creates a draft: `New form <Form> in <Project> is waiting for approval`, linking to the form |
| Form draft discarded | a draft dropped by retention (`RETENTION_FORMS_DRAFT_DAYS`) without being approved: `Form <Form> in <Project> was discarded unapproved`. My call: a draft the operator archives themselves does not notify, since they already know |
| Form submission | only for forms whose new **notify on submission** setting is on: `A new form submission in <Project> / <Form>`, linking to the form. Never the field values |

The steps sent are remembered in SQLite so a restart does not repeat them. An
expiry already past the 1-day step when first seen sends one notification for
the tightest step it falls in, not all of them.

Form submissions come from the ingest process; only a submission actually
inserted (`WriteSubmission` reports `inserted`) to an approved, active form
with the setting on notifies. Submissions to a draft are covered by the one
"draft created" item; approving a draft does not notify its earlier
submissions.

The **notify on submission** setting is a new per-form field, edited wherever
forms are edited today (console, `update_form`, REST, CLI if it edits forms),
documented in `docs/twillingate.md`. My call on defaults: off for forms that
exist at migration, on for a form approved after it.

Spam bursts (my call): the first submission to a form notifies at once; later
ones within 15 minutes fold into that item and into one follow-up send
(`12 new submissions in <Project> / <Form>`) at the end of the window, so a
spam run is at most a few sends per form per hour.

## The inbox

- An item has: project (or none), kind, title, body, link (into the console:
  the monitor's settings, the submission), created time, and archived time.
- A first failure, a recovery and each info notification create an item. My
  call: a reminder does not create a new item but bumps the open failure's item
  (updated time, reminder count, "still down, 8h") so a long outage is one item,
  and the recovery is a new item. A builder may decide otherwise if the
  timeline reads better as separate items, as long as a long outage does not
  flood the inbox.
- Active and Archived views, newest first. Archive and restore one item, and
  **Archive all** for every active item (the owner asked for this explicitly).
- Archived items are purged after `RETENTION_ARCHIVED_DAYS`, by the daily jobs
  pass, as archived submissions are.
- Active items are never purged automatically (my call).
- The web console shows an active count in the navigation and an inbox page.
  The page follows the Web UI rules in CLAUDE.md (pointer cursor, no sideways
  scroll at 360px). Layout is the builder's call.
- MCP tools and REST routes to list items (active or archived, by project) and
  to archive and restore them, documented in `docs/twillingate.md`. Names are
  the builder's call, in the style of the existing `archive_*` / `restore_*`
  pairs.

## Delivery

### The sender package

`internal/shared/notify/`, rank 0: standard library only, no twillingate code,
so it can later move to a top-level package (like `sqlengine/`) for the owner's
other projects with a rename. Parse a URL into a sender; send a message
(title, body, optional link). Parse errors name the bad field. Send errors are
typed so the caller can tell permanent (bad token, chat not found) from
temporary (network, 5xx, 429). No retries or queueing inside.

First list, URL format as shoutrrr's where shoutrrr has the service:

- `telegram://` (the owner's own channel)
- `ntfy://`
- `slack://`
- `discord://`
- `generic://` (JSON POST to any URL, the escape hatch)
- `smtp://` (email, `net/smtp`)

Every sender talks HTTPS (or SMTP with STARTTLS/TLS) and never logs the URL's
secrets.

### Configuration

`NOTIFY_URL`: one URL, optional. Unset means inbox only. An invalid URL fails
startup with the parse error. `NOTIFY_REMIND_HOURS` sets the reminder
interval. Both documented in `docs/deployment.md`. Names are my call.

### Sending

Creating an inbox item and recording that it needs sending happen in one
SQLite transaction. A dispatcher sends pending items to `NOTIFY_URL` in the
background, retries temporary failures with backoff for a bounded time, and
gives up on permanent ones. A send that finally fails is shown on the item in
the inbox ("not delivered to Telegram: chat not found") so the owner learns the
channel is broken. One dispatcher runs beside the monitor, in the process
that writes the event files (the ingest process; `app` starts the monitor only
where the archive log is). A console-only process reads, archives and
restores items but never sends. A reminder or
recovery is sent even if the original failed to send.

## Constraints

- One static binary, no cgo, no new third-party dependency.
- `internal/archtest` ranks: `notify` is a leaf; the state tracking and
  dispatcher live where the layout puts them (likely a new rank-1 package that
  `monitor`, `server` and `jobs` reach through interfaces passed by `app`),
  with each consumer declaring its store slice.
- New tables in a migration; refusals typed (`ErrNotFound` for an unknown
  item).
- Docs updated in the same commit, per the table in CLAUDE.md.

## Left to the builder

Package placement and names beyond `internal/shared/notify`, table layout,
message wording, the console page layout, retry schedule and give-up time,
how times are rendered in messages, and whether notifications are also written
to the event files.
