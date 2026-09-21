# SDK consent, storage and multiple instances

Status: proposed
Date: 2026-09-20

## Sequencing

Last of three specs that land in order, and the only one with no migration:

1. `2026-09-19-project-ids-design.md` — `014_project_ids.sql`
2. `2026-09-20-os-and-platform-design.md` — `015_platform.sql`
3. **this spec** — SDK and docs only

It is written against what those two leave behind: projects are addressed
by integer id, the raw product table is `events`, and the SDK already
detects OS on the client. §4 depends on all three.

The SDK work rebases onto the os/platform spec's. Both edit
`sdk/src/twillingate.ts`, and both regenerate the committed bundle
`internal/server/twillingate.js` — which is built output, so it is rebuilt
with `npm run build` and never merged by hand. The bundle assertions in
`internal/server/twillingate_script_test.go` are trimmed by that spec too;
check what is left before removing the `analytics_*` and `webdriver` lines.

## Goal

Econumo should drive its product analytics through the JS SDK,
programmatically, instead of constructing ingest requests by hand.

That is what the change is for. Hand-built requests mean Econumo maintains
its own copy of things the SDK already does: batching with a flush
interval, `sendBeacon` on `pagehide` and `visibilitychange` so events
survive a navigation, retrying 5xx and network failures but never a 4xx,
per-event ids so a retry cannot double-count, `$locale` and display size,
batch attributes applied to every event, and one place that tracks the wire
format as it changes.

Econumo runs two projects and can only use the SDK for one of them today:

- the web project (anonymous) takes web views. The tag is injected by
  liltag, a tag manager.
- `econumo` (identified) takes product events and a custom `page_view`
  event, from the cloud and from self-hosted instances. It posts to the
  ingest API by hand, because the SDK cannot be used here.

Three things block that:

1. **It writes to the device.** In identified mode every visitor gets a
   persistent `twillingate_visitor` id, and `identify()` saves the user id
   and name (`sdk/src/twillingate.ts:377`, `:447`). Under ePrivacy Art. 5(3)
   that needs consent, which means a banner. Anonymous mode is not clean
   either: `group()` always writes `twillingate_group` (`:387`), and the
   retry queue is written to `twillingate_queue` in every mode whenever a
   send fails (`:498`).
2. **Two tags on one page collide.** There is one global. A second tag with
   a different key takes over `window.twillingate` while the first copy
   keeps running (`supersededBy`, `:614`), and both copies share the same
   unprefixed storage keys.
3. **It silences itself on loopback.** `ignored()` (`:146`) drops everything
   on `localhost`, `127.0.0.0/8`, `[::1]`, `file://` URLs and when
   `navigator.webdriver` is set. Hand-built requests have no such rule, so
   self-hosted installs on loopback report today and would stop.

Econumo needs both tags on the same page: liltag's anonymous tag for web
analytics, and its own identified tag for product analytics, with no
consent banner in the default configuration.

## What does not change

The server is untouched. Identity stays a per-project mode, applied where
it always was (`internal/server/handlers.go:211`): an identified project
keeps `$user_id` as given, and any batch with no user or install id falls
back to the daily-rotating connection hash. That fallback is what makes a
single identified project cover signed-out visitors without a cookie, and
it already works.

No storage migration, no new project field, no change to the two projects'
identity modes.

## Design

### 1. Consent decides where records live, separately from identity

New option `consent`, settable as `data-consent` on the tag or `consent` in
`init`. **It defaults to `false` in every identity mode.**

Consent here means one thing: may this instance keep anything on the
device. Without it, records live in memory; with it, in localStorage. The
SDK is not a consent manager — it does not ask, does not record proof, and
does not remember the answer. Consent is also per visitor, not per page, so
whatever the tag declares has to reflect the answer the site holds for that
person.

**What `consent` accepts.** It mirrors `maskUrl`: the attribute can only
carry a string, `init` also takes the native values, and both resolve
through one `resolveConsent` function so they cannot drift apart
(`sdk/src/mask.ts:34` is the model).

| Value | Meaning |
| --- | --- |
| absent, `""`, `false`, `"false"` | no consent — the default |
| `true`, `"true"` | consent given |
| a function, or a string naming one on `globalThis` | called whenever consent is consulted; its return value is coerced to a boolean |
| a string naming a non-function global | read whenever consent is consulted, so a variable a consent manager flips is picked up live |

Anything else — a name that resolves to nothing, a function that throws —
**fails closed to no consent**, with a console warning. Masking fails closed
by dropping pageviews; consent fails closed by writing nothing.

**Consent is consulted lazily**, each time the instance is about to read or
write storage, never cached at init. A site whose consent manager answers
after page load therefore needs no extra call: the next decision point sees
the new value. Two consequences worth stating:

- Records already written stay written until consent reads false at a
  decision point, at which time the instance wipes the keys it owns. A site
  that needs erasure the instant someone withdraws calls `consent(false)`
  rather than waiting for that.
- The function is called often enough that it must be cheap and must not
  throw. A throw is treated as a withdrawal.

**Programmatic override.** `consent(granted)` pins a value, overriding
whatever the tag declared, and from then on the declared function is no
longer consulted. `consent(null)` hands control back to it. `consent()` with
no argument returns the current effective value.

Without consent (the default):

- Nothing is read from or written to the device. The one exception is the
  `twillingate_ignore` opt-out check, which reads a key the person set
  themselves.
- Identity comes from the host application on each load, through `user` and
  `group` in `init` or through `identify()`.
- A failed batch is retried from memory: with backoff, again when the
  `online` event fires, and once more through `sendBeacon` on `pagehide`.
  The existing cap of 50 batches applies to the in-memory queue. Records are
  lost if the tab closes while delivery is failing, which is the price of
  writing nothing to the device. Retry stays safe because every event
  carries an `id` and the server ignores duplicate ids.

With consent:

- Today's behaviour. An identified instance persists the visitor id, user
  and group in localStorage. Any instance may persist the retry queue so it
  survives a reload.
- An anonymous instance still never persists a visitor id: the server salts
  it daily, so it would buy nothing. There, consent only unlocks the queue.

However the value arrives — a pinned `consent(true)`, a declared function
that starts returning true — the transition is the same:

- **Granted:** anything waiting in the in-memory retry queue is written to
  localStorage, and an identified instance mints and stores a visitor id
  from that point on. Events already sent stay as they were sent; nothing is
  stitched retroactively.
- **Withdrawn:** every storage key this instance owns is deleted and records
  go back to memory.

The site owns the answer — for Econumo, the user setting behind its existing
`user_update_analytics` event. Passing it to `init` (or naming a global that
holds it) is what makes the entry pageview carry the stored identity, which
a runtime call cannot do, because by then the pageview has been sent.

`reset()` additionally clears the retry queue, in every mode. A queued
record carries the batch attributes it was built with, including
`$user_id`, so without this a logout leaves the previous user's data on a
shared browser.

The `identity` option keeps only one job: with consent, it decides whether
the visitor id, user and group persist. It no longer gates anything without
consent. The server remains the enforcement point for what is actually
stored.

**The environment guard goes away.** `ignored()` keeps only the
`twillingate_ignore` check and drops the `localhost` / loopback / `file://`
/ `navigator.webdriver` rules (`sdk/src/twillingate.ts:146`). Deciding
whether analytics should run at all belongs to the product — it can skip
`init()` on a condition it knows — not to the library. Two consequences to
accept knowingly: a snippet-mode tag no longer ignores a developer's own
`localhost` browsing, so keeping dev traffic out of a project becomes the
site's job; and `twillingate_ignore` becomes the only way to silence an
automated browser.

**The legacy `analytics_*` keys go away.** The migration that copied
`analytics_visitor`, `analytics_user` and `analytics_group` forward is
deleted, along with the `analytics_ignore` fallback in `ignored()`
(`sdk/src/twillingate.ts:115`, `:147`). They date from a snippet that no
longer exists. One consequence to accept knowingly: a device still opted out
under `analytics_ignore` alone stops being ignored, and would have to set
`twillingate_ignore`.

**Breaking change.** An identified tag that relies on today's implicit
localStorage must now set `data-consent="true"`, and the legacy keys above
are gone. Econumo has no SDK-based identified clients, so nothing in this
deployment breaks. Commit as `feat(sdk)!:` with a release note.

### 2. `data-instance` for a second tag on one page

`data-instance="et"` registers the instance at `window.et` and leaves
`window.twillingate` alone. It works with or without `data-key`, so a tag
can auto-init from its attributes or load dormant for `et.init({...})` in
code. Without the attribute, behaviour is exactly as today.

- The duplicate-tag guard looks at `window[name]` instead of
  `window.twillingate`, so a doubled liltag tag still stands down and
  Econumo's tag is never mistaken for a duplicate of it.
- Storage keys are prefixed with the instance name: `et_visitor`,
  `et_user`, `et_user_name`, `et_group`, `et_group_name`, `et_queue`. The
  default name keeps the current `twillingate_*` names.
- `twillingate_ignore` stays global and unprefixed. Opting out is a decision
  about the person, not about one tag.
- Each instance hooks `history.pushState` independently. The patches chain,
  so both fire, each into its own project.

### 3. Programmatic views, and suppressing derived attributes

Econumo's product tag tracks programmatically: `autoPageviews` off, with
`et.page(path, attrs)` called on each route change.

Two behaviours of `page()` matter here and stay as they are:

- An explicit path is resolved against `location.href`, so `$host` is the
  real hostname. A caller who needs something else — on self-hosted, the
  `selfhosted_<hash>` value Econumo already computes — passes `$host` in the
  call's attributes, which win over everything the SDK derives.
- Campaign parameters are read from the URL passed in, not from
  `location`, so a clean path carries none.

`$referrer` is different: it is attached on every pageview (`:317`) whatever
the caller passes. To suppress it per call, **an attribute whose value is
`null` or `undefined` is dropped at emit time** rather than sent. So
`et.page("/budget", { $host: "selfhosted_ab12", $referrer: null })` sends
neither the real host nor the referrer. The rule is general and applies to
any attribute, reserved or custom.

This matters beyond tidiness. The server drops a referrer as a self-referral
only when its host matches `$host`. Once `$host` is rewritten to a hashed
value, that check stops matching and the operator's real domain would land
in the referrer data — the thing hashing the host was meant to prevent.

### 4. How Econumo is wired afterwards

Both projects stay as they are. Two tags, two globals, two projects:

| | web analytics | product analytics |
| --- | --- | --- |
| Project | the web project, anonymous | the product project, identified |
| Addressed by | `project_id`, from `list_projects` | `project_id`, from `list_projects` |
| Loaded by | liltag | the app |
| Global | `window.twillingate` | `window.et` via `data-instance` |
| Pageviews | automatic, full acquisition data | programmatic `et.page(...)` |
| Consent | `false` (nothing stored) | `false` (nothing stored) |
| Scope | cloud only | cloud and self-hosted |

The app calls `et.init({ key, identity: "identified", user, group })` with
the user id it already hashes, then `et.page(path, {...})` per route change
and `et.track(name, attrs)` for actions. Signed-out visitors send no user
id, so the server attributes them to the daily-rotating connection hash.

Browser, device class and country are derived on the server from the
User-Agent and IP of the ingest request and then discarded, for `web`-kind
events. The product tag therefore keeps the default `kind: "web"`. This
applies to self-hosted users' connections too.

OS is no longer among them, and does not have to be declared either — see
below.

### 4.1 OS detection is inherited, not configured

The os/platform spec moves OS detection off the User-Agent and into the
SDK. This instance gets it for nothing, in both directions that matter
here: `batchAttributes()` is computed once per flush and applied to every
event in the batch, so `$os`, `$os_version`, `$os_name` and `$platform`
ride along on everything the instance emits — `et.track(...)` product
events and `et.page(...)` / `$page_view` views alike. Econumo declares
none of them and configures nothing. A self-hosted browser is detected the
same way a cloud one is, which is the part the server could no longer do.

What is *stored* differs by table, and the asymmetry is deliberate:

| key | `views` | `events` |
| --- | --- | --- |
| `$os` | column | column |
| `$platform` | column | column, added by 015 |
| `$os_version` | column | not stored |
| `$os_name` | column | not stored |

A key with no destination on the product side is dropped rather than kept
as an ordinary attribute, because `resolveAttributes` splits the whole `$`
namespace into typed fields and `handlers.go:125` stores only `rv.Custom`.
Nothing reserved reaches `events.attributes`. That is what keeps a
free-form `$os_name` out of `agg_product_attrs` and away from the `top_n`
cap, and it means the tag can send all four keys on every batch without
either configuring per-event attributes or risking attribute cardinality.

So the product project gains `$os` and `$platform` as breakdown dimensions
through `product_attributes`, and the `$page_view` rows gain the full set
through the views family. Neither needs a line of Econumo code.

Self-hosted sends `$page_view` as well, with `$host` hashed and `$referrer`
suppressed per call, and with clean paths so no campaign data is collected.

Econumo stops sending its custom `page_view` product event and sends
`$page_view` instead, which moves that traffic into the views family:
`views_overview`, `views_breakdown`, paths, hosts, countries and retention
all start covering the app.

If Econumo's routes carry ids, `maskUrl` should be configured at the same
time. The mask resolves before the entry pageview, so turning it on later
is more work than turning it on now.

### 5. Dropping the historical `page_view` rows

Done by hand in SQL, with the service stopped and a backup taken. No CLI
operation is added for it.

The product project's data starts on 2026-09-07. That was inside the raw
window when this was written, so `agg_product_daily` and `agg_product_attrs`
held nothing for those days — product aggregation only runs for days older
than the raw window (`internal/jobs/jobs.go:134`).

**That claim decays, and this spec lands third.** By then those days may
have rolled up, in which case deleting the raw rows leaves aggregated
`page_view` counts behind with no source. Re-check it immediately before
running anything:

```sql
SELECT COUNT(*) FROM agg_product_daily WHERE project_id = :id;
SELECT COUNT(*) FROM agg_product_attrs WHERE project_id = :id;
```

A non-zero count means the deletes below are not sufficient on their own
and the matching aggregate rows have to go with them.

Identity and retention rollups are different: they run over every raw day on
each pass. `agg_identity_daily` is written with `INSERT OR REPLACE` keyed by
`(project_id, day, kind, id)` (`internal/store/sqlite/identities.go:31`), and
`actors` upserts `first_seen_day` with `MIN` (`retention.go:40`). Neither
removes a row that no longer has source data, so deleting raw rows alone
would leave stale identity and cohort rows behind. They have to be deleted
and rebuilt:

```sql
-- :id is the product project's integer id, from list_projects.
DELETE FROM events             WHERE project_id = :id AND event_name='page_view';
DELETE FROM agg_identity_daily WHERE project_id = :id;
DELETE FROM agg_retention      WHERE project_id = :id;
DELETE FROM actors             WHERE project_id = :id;
```

The next daily pass recomputes all three from the remaining raw rows.

## Alternatives rejected

- **A third identity mode** (`consent` / `hybrid`), where the server keeps
  `$user_id` but salts `$install_id` unless a batch declared consent. Needed
  only if one project takes both anonymous and identified traffic. With two
  projects kept, the existing modes cover it.
- **A `$identity` or `$consent` reserved attribute** on the wire, letting a
  batch declare its own treatment. Same reason: it exists to make one
  project mixed, and it would move enforcement from the server to the
  client.
- **One shared instance** for liltag and the app, with a command queue to
  absorb load order. `data-instance` is simpler and keeps the two projects
  genuinely separate.
- **An instance-level `acquisition: false` switch** instead of per-call
  `null`. Rejected in favour of the general "null drops the attribute" rule,
  which needs no new option.
- **A `project merge` operation.** Not needed while both projects stay.

## Testing

Everything lands in `sdk/` and `docs/`. No Go code changes.

Vitest, in `sdk/src/`:

- Without consent, nothing is written to localStorage in either identity
   mode, across a failed send, an `identify()`, a `group()` and a `reset()`.
- A failed batch retries from memory, replays on `online`, and is dropped
  into `sendBeacon` on `pagehide`.
- `consent(true)` moves pending records into storage and starts persisting
  the visitor id; `consent(false)` removes every key the instance owns.
- `data-consent` resolves a literal, a named global variable and a named
  global function; an unresolvable name and a function that throws both fail
  closed to no consent.
- A declared function that flips to true mid-session is picked up at the next
  storage decision, without any call; `consent(true)` pins a value over it
  and `consent(null)` hands control back.
- `reset()` clears the queue with and without consent.
- `data-instance` registers the named global, leaves `window.twillingate`
  untouched, and prefixes its storage keys.
- Existing tests covering the `analytics_*` migration are deleted rather
  than adapted (7 references in `sdk/src/identity.test.ts`, 2 in
  `twillingate.test.ts`), as is the `navigator.webdriver` case
  (`twillingate.test.ts:282`).
- Events are emitted from a `localhost` page, which is what jsdom serves, so
  the guard's removal is covered by every other test in the suite.
- Two instances on one page both emit a pageview on `pushState`, each with
  its own key.
- An attribute set to `null` is absent from the request body; `$host` and
  `$referrer` overrides reach the wire.

Docs, in the same commit as the code (required by CLAUDE.md):

- `docs/twillingate.md`: the `consent` option, `data-consent` and the
  `consent()` call, `data-instance`, the null-drops-an-attribute rule, and the
  privacy behaviour section, which currently states that identified
  projects persist a visitor id unconditionally and that the legacy keys are
  migrated (`docs/twillingate.md:455`).
  `docs/twillingate.md:458` states that the SDK is silent on localhost,
  `file://` URLs and automated browsers; that line goes.
- `internal/server/twillingate_script_test.go` asserts the built bundle
  still contains `analytics_ignore`, `analytics_visitor` (`:41`) and
  `webdriver` (`:48`); all three expectations are removed.
- `TestDocumentMatchesSDK` (`internal/api/docs_sync_test.go:123`) checks
  every documented SDK symbol against the source, so new symbols must exist
  in `sdk/src/twillingate.ts`.
- Rebuild the bundle: `npm run build` in `sdk/` writes the committed
  `internal/server/twillingate.js`.
