# The client declares its environment: `$os`, `$platform`, `$browser`, `$device`

(The file keeps its `os-and-platform` name; the other two specs and the
sequencing note link to it.)

Status: proposed
Date: 2026-09-20

## Sequencing

Second of three specs that land in order. It is written against the schema
`2026-09-19-project-ids-design.md` leaves behind, so it assumes
`project_id INTEGER` on every table and the raw product table renamed to
`events`, and it owns migration `015`:

1. `2026-09-19-project-ids-design.md` — `014_project_ids.sql`
2. **this spec** — `015_environment.sql`
3. `2026-09-20-sdk-consent-and-instances-design.md` — no migration

That third spec inherits this one's SDK work: `batchAttributes()` is
computed per flush and applied to every event in the batch, so detection
here reaches programmatic product events and views alike, with no
configuration on its side.

## Problem

`$platform` is currently a deprecated alias for `$os` (`internal/server/ingest.go:120`),
folded in `resolveAttributes` when `$os` is absent. That collapses two facts
that are not the same fact:

- **`$os`** — the operating system the client runs on.
- **`$platform`** — the surface the product is used through: web, ios,
  android, electron.

They coincide for a native app and diverge everywhere else. Safari on an
iPhone is `platform=web, os=ios`. An Electron build on a Mac is
`platform=electron, os=macos`. Today the second dimension does not exist, so
"how many of my users are on the web build versus the native one" and "which
OS versions do I still have to support" are the same column, and neither can
be answered.

`$kind` (`web`/`app`/`cli`) is adjacent but not a substitute. It gates
enrichment and is deliberately coarse. It stays exactly as it is.

**Browser and device class have the same problem one step further on.**
Neither is declarable: `internal/server/ingest.go:115` has no `$browser`,
`$browser_version` or `$device` key, so both exist only as server output
from `ParseUserAgent`. That was fine while the User-Agent was a reliable
witness. It is becoming less so — Chromium's reduced User-Agent erodes the
browser string deliberately, and Brave ships an unmodified Chrome
User-Agent as a privacy feature, so the server counts every Brave user as
Chrome and always will. Moving OS to the client and leaving browser on the
server would also split one question, "what is this client", across two
mechanisms with different failure modes.

So all of it moves. `ParseUserAgent` is deleted; the User-Agent stays only
for bot filtering, where self-declaration is worthless by definition.

## Decisions

1. `$platform` becomes an independent reserved key with its own column.
   `$kind` is untouched.
2. **Clean break.** `$platform` never fills `os`.
3. **`$os` closes to a lower-case vocabulary**; `$platform` stays open,
   lower-cased, bounded by `$kind`'s pattern.
4. **Neither column is ever empty.** Both fall back to `unknown`, and `$os`
   additionally distinguishes `other` — a real OS outside the vocabulary —
   from `unknown`, meaning no OS information at all.
5. **OS detection moves to the JS SDK. The server only validates.** The
   User-Agent is no longer a source of OS.
6. Platform gets its own aggregate, view and breakdown dimension, and
   `agg_views_app_versions` rekeys from `(os, app_version)` to
   `(platform, app_version)`.
7. `$platform` lands on product events too, alongside `$os`.
8. A new free-form **`$os_name`** preserves the OS's full self-reported name
   with version, so closing the vocabulary stops being lossy.
9. The SDK detects **`$os_version`** as well, using high-entropy hints where
   they are available.
10. **Browser and device class move to the client too**, on exactly the
    terms `$os` gets: new reserved keys `$browser`, `$browser_version` and
    `$device`, closed lower-case vocabularies with the same `other` /
    `unknown` floors, and the same clean break — an absent value is
    `unknown`, never a User-Agent parse.
11. `ParseUserAgent` is **deleted**, not reduced. `internal/enrich` keeps
    `IsBot` and gains the three validators beside `NormalizeOS`.
12. The SDK exposes detection as public API — `detectOS()`,
    `detectBrowser()`, `detectDevice()` — each backed by one internal
    resolve, each overridable by the matching option.
13. Bot filtering stays on the server and on the raw User-Agent. It is the
    one thing a client must not be asked to declare about itself.

## Wire contract

### `$os` — closed, lower-case, never empty

```
// desktop
'windows' | 'macos' | 'linux' | 'bsd' | 'chromeos'
// mobile / tablet
| 'ios' | 'ipados' | 'android' | 'fireos' | 'harmonyos' | 'kaios'
// TV, wearable, XR
| 'tvos' | 'watchos' | 'visionos' | 'tizen' | 'webos'
// console
| 'playstation' | 'xbox' | 'nintendo'
// floors
| 'other' | 'unknown'
```

**`other` and `unknown` are different answers and must stay that way.**
`other` means there is an OS and it is outside the vocabulary; `unknown`
means there is no OS information at all. Collapsing them would make a
server-relayed event indistinguishable from a genuine FreeBSD, and `unknown`
is precisely the value that makes undeclared traffic measurable: a backend
relay or a custom client that declares no OS becomes visible in a breakdown
instead of silently inflating a real bucket.

The cut: an OS earns a value when it is a distinct product target — something
a team would ship, test or drop support for separately — and is either
detectable from a browser or declarable by a native client. Distributions are
not OSes (`ubuntu` is `linux`), and dead platforms are not worth a value
(`windows_phone`, `blackberry`). `other` remains the floor, so nothing is ever
lost by the list being incomplete.

The BSDs share one value rather than three. Merging them into `linux` was
considered and rejected: it would make `linux` mean "Linux or BSD", and
nothing downstream could separate them again. One `bsd` keeps the number
honest at a third of the vocabulary cost, which matters because OpenBSD and
NetBSD are close to undetectable in a browser anyway.

Three values are **declare-only** — no browser reaches them, or none reports
distinguishably: `watchos`, `visionos` (Vision Pro's Safari reports as a Mac),
and `harmonyos` on devices that ship an Android-compatible User-Agent. They
exist so a native client has a correct value to send.

**`ipados` splits from `ios`.** It is a genuinely separate target, but note the
consequence: iPad traffic that previously counted as `ios` now counts
separately, so an existing `ios` series will step down on the day this ships.
Pre-iPadOS-13 iPads (iOS 12 and earlier) are reported as `ipados` by the
`iPad` substring, which is wrong but affects a population that is effectively
gone.

Server-side this is **validation only**. Trim, lower-case, strip spaces and
dashes (`"Chrome OS"` → `chromeos`, which is what
`navigator.userAgentData.platform` returns), then match the vocabulary.
Anything present but unrecognised stores `other`; anything absent or empty
stores `unknown`.

An unrecognised value is never a rejection. The event is accepted, exactly as
an unrecognised `$` event name or an invalid `$kind` is: a client shipping
against a server that has not learned a value yet must not receive a `4xx`,
which the retry rules classify as a poison batch to drop.

Two rules make that fold safe rather than silent:

- **It warns.** `res.warn` records `$os "iOS 17" is not a known value, stored
  as other`, so the mistake surfaces in the response body during integration
  instead of becoming a quiet `other` months later. It does *not* warn when
  the client sent `other` deliberately — that is a legitimate value.
- **It preserves the original.** When `$os` is unrecognised and `$os_name` was
  not sent, the raw value is copied into `os_name`. This mirrors what the
  migration does for existing rows; without it, `other` would be
  investigable for history and mute for everything arriving afterwards. An
  explicit `$os_name` always wins.

A value that is well-formed but *semantically* wrong — `"windows"` declared
from an iPhone — is stored as declared. The server no longer has a
User-Agent to contradict it, which is the accepted cost of moving detection
to the client.

`enrich.NormalizeOS` becomes that validator. Its current contract is the
opposite ("the vocabulary is a convenience, not an allowlist"), and its
comment flips with it.

An **absent** `$os` stores `unknown`, not `other`. A modern SDK always sends a
value, so `unknown` in the data means "this traffic is not declaring OS" — a
live measurement rather than a silent gap, and a value the breakdown API can
filter on directly instead of reasoning about empty strings.

### Detection leaves the server entirely

`ParseUserAgent` is deleted, along with its `majorAfter` helper. It has one
production caller — `handlers.go:166`,
`v.Device, v.Browser, v.BrowserVersion, v.OS = enrich.ParseUserAgent(ua)` —
which goes with it, so the blast radius is that line and
`internal/enrich/ua_test.go`. `IsBot` and the country lookup are untouched.

This deletes the "declared overrides parsed" rule outright — there is
nothing left to override. Detection belongs on the client, which can see
things a User-Agent cannot, and keeping two sources meant two vocabularies
to hold in agreement.

Two of those "things a User-Agent cannot see" are the whole argument, one
per dimension:

- **iPadOS 13+ in desktop mode** sends `Macintosh; Intel Mac OS X` and is
  separable only by `maxTouchPoints`.
- **Brave** ships an unmodified Chrome User-Agent on purpose. Only
  `navigator.brave.isBrave()` answers, so a server parse cannot ever count
  Brave, and silently folds it into Chrome.

A note on what `kind` gating meant. Enrichment ran only for `kind='web'`
(`handlers.go:159`), so app and CLI rows have carried an empty `browser`
and `device` since 001. Declared attributes are not gated that way, so
those dimensions start working for native clients that declare them —
`device` especially, which is a real question for an app and has never been
answerable.

**The reason this is safe to do now**: `internal/server/twillingate.js` is the
built SDK, embedded and served at `/js/twillingate.js`, so every snippet-mode
site picks up detection the moment the server upgrades. Only bundled/npm
consumers lag, and they pin a version deliberately.

### `$os_name` — free-form, verbatim

The full name as the client reports it, **including the version**:
`"macOS 14.2"`, `"Windows 11"`, `"FreeBSD 14.1"`. Stored verbatim, truncated
at `maxAttrValue` like any other attribute.

It exists because closing `$os` makes `other` a bucket with no label. With
`os_name`, `other` stays investigable — you can see what is actually in it and
decide whether something in there deserves its own vocabulary value.

**It is deliberately not part of any aggregate key.** `$os` and `$os_version`
remain the structured, queryable pair; `os_name` is a forensic and display
field on the raw row, reachable through `query`. That is what keeps its
unbounded cardinality harmless. It is **not** on `v_events_flat`:
`RebuildFlatView` builds that view `FROM events`
(`internal/store/sqlite/flatview.go:82`), the product table, which by the
rule below has no `os_name` column.

Two consequences of that choice, both accepted:

- It is **not carried into aggregates**, so it disappears when raw rows roll
  up. Investigating `other` is something you do on recent data, before the
  retention window closes.
- It is **views-only**. `events` does not get the column: its OS surfaces
  through `product_attributes`, where a free-form key would blow up the
  attribute cardinality the `top_n` cap exists to bound.

  A `$os_name` arriving on a product-event batch is therefore **resolved and
  dropped**, not stored as an ordinary attribute. That is already how the
  reserved namespace works and needs no new rule: `resolveAttributes` splits
  every `$` key into a typed field, and `handlers.go:125` stores only
  `rv.Custom`, so nothing reserved can reach `events.attributes`.
  `$os_version` is dropped the same way and for the same reason — `events`
  has had no `os_version` column since 001. An SDK that sends all four keys
  on every batch is thus correct and cheap: the product side keeps `$os` and
  `$platform`, which are columns, and silently discards the other two.

Unlike `$os` and `$platform` it is **empty when absent**, not `unknown`. It is
not a breakdown dimension, so there is nothing to filter and a sentinel would
just be noise in a display string.

### `$platform` — open, bounded, lower-cased

Trim, **lower-case**, then validate against `^[a-z][a-z0-9_]{0,15}$` — the
same shape as `kindPattern`, applied after case folding so a client sending
`iOS` records `ios` rather than being dropped.

An absent `$platform`, or one that fails the pattern, stores **`unknown`** —
the same floor as `$os`, warned about in the invalid case. Platform has no
`other`: in an open vocabulary every well-formed token is already accepted, so
the only failure left is "no usable value", which is what `unknown` says.

A client could send `unknown` itself. That is harmless — it means the same
thing.

No server-side list. `docs/twillingate.md` names a conventional set — `web`,
`ios`, `android`, `macos`, `windows`, `linux` — without enforcing it, so
`electron`, `tvos` or `quest` need no redeploy.

Note the asymmetry with `$kind`, which validates without case folding. Left
alone deliberately: it is a separate behavioural change to a key not
otherwise in scope.

### `$browser` and `$browser_version` — closed, lower-case, never empty

```
'chrome' | 'safari' | 'firefox' | 'edge' | 'opera' | 'samsung_internet'
| 'brave' | 'vivaldi' | 'duckduckgo' | 'yandex'
| 'other' | 'unknown'
```

The first six are exactly what `ParseUserAgent` emits today, lower-cased
and underscored, so no existing row changes meaning. The four added are
ones the client can separate and the server never could: Brave via
`navigator.brave`, the other three via `userAgentData.brands` or a distinct
UA token. The same cut applies as for `$os` — a value earns a place when a
team would test or drop support for it separately — and `other` is the
floor for everything else.

Validation is the mirror of `$os`: trim, lower-case, fold spaces and dashes
to `_`, match the vocabulary; present but unrecognised is `other` with a
`res.warn`; absent or empty is `unknown`. There is no `browser_name`
counterpart to `$os_name`, and none is needed — see the fold below.

**`$browser_version` stays the major only**, as a string, which is what the
parser produced and what `agg_views_browsers` is already keyed on. Full
version strings are available through
`getHighEntropyValues(['fullVersionList'])` and are deliberately not taken:
majors are the unit support decisions are made in, and `$os_version` is
already growing that table's row count on its own.

### `$device` — closed, lower-case, never empty

```
'desktop' | 'mobile' | 'tablet' | 'wearable' | 'xr'
| 'other' | 'unknown'
```

The first three are what the parser emits. The cut for the rest is one
test: **a device value earns its place only when `$os` cannot imply it.**

- `wearable` and `xr` pass. Wear OS reports as `android` and Quest reports
  as `android`, so both are facts no other column carries.
- `tv` and `console` fail. `tizen`, `webos`, `tvos` and
  `playstation`/`xbox`/`nintendo` already name them, and a second column
  repeating it buys nothing.
- A purpose-shaped value like `iot` fails twice over: it is not a form
  factor — the others all describe screen size and how the thing is held —
  and nothing detects it, so it would mean "miscellaneous", which is what
  `other` means already.

`other` therefore carries consoles, TVs and embedded clients, with `$os`
naming the specific one. That is still a correction: today a PlayStation
reports `device = desktop`, because `desktop` is the parser's default.

`$device_model` is unchanged: it has always been declarable and stays free
text beside `$device`.

## Storage — migration `015_environment.sql`

013 and 014 are already applied on prod, so this is 015.

```sql
ALTER TABLE views          ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE events         ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE views          ADD COLUMN os_name  TEXT NOT NULL DEFAULT '';
```

`events.platform` is a new column that reuses an old name. 003 added a
`platform` column to `product_events` and 012 renamed it to `os` — that
rename is exactly the conflation this spec undoes. The column arriving here
is the second dimension, not the one 012 took away, and it starts empty for
every existing row.

The default does the whole `events` backfill on its own, and gives
`views` a correct value for every row the steps below do not touch.

### Statement order

**The platform backfill must run before the OS fold.** It derives platform
from `os`, and the fold rewrites out-of-vocabulary values to `other`, so
running the fold first would destroy the token the backfill reads.

This is a one-time inversion and only a one-time inversion: the *current*
`views.os` can produce a platform because 012 folded `app_views.platform` into
it. Going forward `os` is a genuine OS and cannot — `macos` could be web or
electron. **Nothing after this migration ever derives platform from os.**

### 1. Views backfill — faithful

- `kind='web'` → `'web'`.
- `kind='app'` → `lower(os)`, which inverts 012's fold exactly.
- Any other kind → `unknown` (the column default; no statement needed).
- A derived value that does not match the platform pattern → `unknown`.

### 2. `events` backfill — none

Deliberate. `events` has no `kind` column, so `lower(os)` would label
a web SDK's custom events `macos` rather than `web`. Every row keeps the
column default of `unknown`, which is the honest answer; the dimension fills
going forward.

### 3. OS fold — raw rows

**First**, every `views` row whose `os` is outside the vocabulary copies that
value into `os_name`:

```sql
UPDATE views SET os_name = os WHERE lower(os) NOT IN (<vocabulary>) AND os <> '';
```

**Then** `views.os` and `events.os` are lower-cased; `''` becomes
`unknown`, and anything else outside the vocabulary becomes `other`.

This ordering is what makes the fold **non-destructive for raw rows**: the
name that `other` would have erased is preserved beside it. Aggregate history
has no such column, so the fold's losses there stand — which is a further
reason it is left unfolded.

### 4. OS fold — aggregate history

`agg_views_os` and `agg_product_attrs`' `$os` rows are lower-cased, and `''`
is relabelled to `unknown`. Out-of-vocabulary values are **left alone**.

The split is about what can be done without inventing counts. Lower-casing is
injective over the canonical vocabulary and `'' → unknown` is a pure relabel
onto a value that did not previously exist — both merge nothing and change no
count. Folding the out-of-vocabulary tail into `other` would merge rows and
`SUM` their `visitors`, producing distinct-visitor counts that were never
measured, so it is not done.

The **never-empty** guarantee therefore holds everywhere, including aggregate
history. Only the **closed-vocabulary** guarantee is partial: days aggregated
before 014 can still show a tail of unlisted OS names. The daily pass rebuilds
from raw, so anything not yet rolled up is self-correcting.

### 5. Browser and device fold — loss-free

`views.browser` and `views.device` are lower-cased, `Samsung Internet`
becomes `samsung_internet`, and `''` becomes `unknown`. `agg_views_browsers`
and `agg_views_devices` get the same treatment.

**Unlike the OS fold, this one loses nothing and merges nothing, in raw
rows and aggregate history alike.** The reason is that no client could ever
write these columns: there is no `$browser` or `$device` reserved key, so
every value in them was produced by `ParseUserAgent`, which emits exactly
six browser names and three device classes or the empty string. All nine
are in the new vocabularies, so the fold is injective, no row collides with
another, and no `visitors` count is summed.

That is also why there is no `browser_name`. `$os_name` exists because a
declared `$os` could be any string and `other` would erase it; a stored
browser never can be, so there is nothing to preserve.

The `''` rows being relabelled to `unknown` are the app and CLI rows that
`kind` gating never enriched. `unknown` is the honest name for them, and
from here they are fillable by a client that declares.

### 6. `agg_views_platforms`

Modelled on `agg_views_countries`, the existing single-key dimension. That
table is a shape to copy, not a thing to change.

```sql
CREATE TABLE agg_views_platforms (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, platform TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, platform)
) WITHOUT ROWID;
```

Seeded from `agg_views_daily`, which is keyed `(project_id, day, kind)` and
carries `visitors` and `views`:

- `kind='web'` → `platform='web'`. Exact.
- every other kind → `platform='unknown'`.

`agg_views_os` cannot help here — it is keyed `(os, os_version)`, so a
per-platform count would mean summing across `os_version` and double-counting.

Seeding `unknown` rather than omitting the row keeps the platform breakdown's
totals consistent with the daily totals, and `unknown` states plainly what is
true of those days. `views` stays exact because views are additive. `visitors`
can overcount in one case only: a project running **two or more non-web kinds**
on the same pre-014 day, whose separate rows collapse into one `unknown` row.
Bounded to that row, on those days.

### 7. `agg_views_app_versions` rekey

From `(project_id, day, os, app_version)` to `(project_id, day, platform,
app_version)`, matching the key's own stated rationale: `2.4.1` means
unrelated things across the iOS and Android builds of one product.

Rebuilt with `platform = lower(os)`, or `unknown` when that does not match the
platform pattern. For rows whose `os` is canonical — effectively all of them —
this is value-preserving and merges nothing. The exception is the tail:
aggregate history is deliberately not OS-folded, so if two out-of-vocabulary
values coexist for one `(project_id, day, app_version)` their rows merge into one
`unknown` row whose `visitors` is a sum that can overcount. Accepted only
because the alternative is dropping app-version history for those projects.

**Known transitional cost.** The aggregator rebuilds this table daily from raw
`views.platform`, which is `unknown` for any client that has not shipped
`$platform`. Those rows collapse from `(ios, 2.4.1)` and `(android, 2.4.1)`
into `(unknown, 2.4.1)`. Counts stay correct — aggregation recomputes
`COUNT(DISTINCT actor_id)` from raw — but the split is lost until clients
update, which for native apps is an app-store timeline.

### 8. Views rebuilt

Exactly three views are touched, and no other view is dropped or recreated.
Unlike 012 and 014, which had to rebuild all of them, this migration only
adds columns.

- **`v_views_platforms`** — created. It copies the aggregate ∪ live shape of
  `v_views_countries`, the existing single-key dimension, including the
  500-value `(other)` cap. `v_views_countries` itself is not modified; it is
  the template, not a target.
- **`v_views_app_versions`** — dropped and recreated for the rekey.
- **`v_product_attrs`** — dropped and recreated to gain a `$platform` arm.

`v_views_os` is deliberately absent from that list: `os_version` is already
one of its keys, so populating it needs no schema change at all.

## Interaction with the existing OS aggregate

`os_version` already **is** part of the OS rollup: `agg_views_os` is keyed
`(project_id, day, os, os_version)`, `v_views_os` returns both columns, and
`aggregate_views.go` declares it as `keys: {"os", "os_version"}`. No change is
needed there — but populating `os_version` for web traffic for the first time
has two consequences.

**Cardinality is already handled, by design.** `viewDimension` collapses the
tail of its *last* key into `(other)` while a leading key stays intact, so a
collapsed row still says which OS it belongs to. `agg_views_os` therefore goes
from roughly one row per OS today — every web row has `os_version=''` — to one
row per OS-and-version, which is exactly the shape `agg_views_browsers`
already carries. No new risk, but it is a real growth in that table and worth
expecting.

**`other` and `(other)` are different things in adjacent columns.** `other` is
a vocabulary value of `os`, meaning a real OS outside the list. `(other)` is
the aggregator's cardinality-cap sentinel, and it can only ever appear in
`os_version`, because the cap never applies to a leading key. The parentheses
are the only thing distinguishing them, so neither should be "tidied" into the
other later. The same holds for `platform`: it has no `other` value, so
`(other)` in that column is unambiguously the cap.

## Reporting

| Touchpoint | Change |
| --- | --- |
| `internal/store/sqlite/aggregate_views.go` | add `{table: "agg_views_platforms", keys: []string{"platform"}}`; `agg_views_app_versions` keys become `{"platform", "app_version"}` |
| `internal/store/sqlite/prune.go`, `registry.go` | add `agg_views_platforms` |
| `internal/store/sqlite/aggregate_product.go` | add `{"platform", "$platform"}` |
| `internal/api/ops_read.go` | `"platforms": {"v_views_platforms", []string{"platform"}}`; `app_versions` keys; dimension enum and tool description |
| `internal/api/resources.go` | `schemaViews` gains `v_views_platforms`; `v_views_app_versions` line rekeyed |
| `internal/store/store.go` | `View.Platform`, `View.OSName`, `ProductEvent.Platform` |
| `internal/store/sqlite` | insert and scan for both tables |
| `internal/server/ingest.go` | `$platform` → `r.Platform`; `$os_name` → `r.OSName`; new `$browser`, `$browser_version`, `$device` keys and their fields on `resolved`; `platformRaw` and the alias fold deleted; lower-case + pattern validation |
| `internal/server/handlers.go` | carry `Platform`; delete the `ParseUserAgent` call at `:166` and take browser, browser version and device from the declared values. The `kind == "web"` block keeps only the bot drop and `CleanReferrer` |
| `internal/enrich/ua.go` | `ParseUserAgent`, `majorAfter` and `osNames` deleted. The package keeps `IsBot` and `CleanReferrer`, and holds the three validators: `NormalizeOS`, `NormalizeBrowser`, `NormalizeDevice` |

`views` needs no new column for any of this: `browser`, `browser_version`,
`device` and `device_model` have existed since 012. Only `platform` and
`os_name` are new.

`product_attributes` then exposes `$platform` as a third always-available
system dimension beside `$os` and `$app_version`.

## JS SDK

### Options

Every detected value has an override option and a matching `data-`
attribute, and the rule is uniform: **an explicit option always beats
detection.**

- `os?: string`, `osVersion?: string`, `osName?: string` — with `data-os`,
  `data-os-version`, `data-os-name`.
- `browser?: string`, `browserVersion?: string`, `device?: string` — with
  `data-browser`, `data-browser-version`, `data-device`.
- `platform?: string` — stops being `@deprecated use os` and becomes the real
  platform option, defaulting to `"web"`. `sdk/src/twillingate.ts:211`
  (`this.os = opts.os || opts.platform || null`) is deleted.
- `data-platform` joins `data-os` in the snippet reader.

`batchAttributes` sends `$platform`, `$os`, `$browser` and `$device` on
every batch — all four always resolve to a value — plus `$os_version`,
`$os_name` and `$browser_version` when detection or an override produced
them.

### Detection is public API

Three methods on the instance, so an application can read what the SDK
would send without waiting for a batch or re-implementing any of it:

```ts
detectOS():      { os: string; osVersion: string; osName: string }
detectBrowser(): { browser: string; browserVersion: string }
detectDevice():  { device: string }
```

Each returns the **effective** value — the override if one was set,
otherwise detection — so what they return is exactly what the next batch
carries. With named instances they are per-instance, `et.detectOS()`.

**All three are views onto one internal resolve, run once at init.** This
is not a tidiness point: OS and device class share their signals almost
entirely (`iPad`, `maxTouchPoints`, the console and TV markers), and three
independent passes would eventually disagree — reporting `os: ipados` with
`device: desktop`, which is not a state that exists.

One caveat to document: they are **synchronous**, and `$os_version` on
Chromium improves when the high-entropy promise resolves shortly after
init. `detectOS()` called immediately returns the User-Agent answer, and
the same call a tick later returns the corrected one. Batches are
unaffected — `batchAttributes()` runs in `flush()`, after the promise has
settled.

Because `TestDocumentMatchesSDK` checks every documented SDK symbol against
the source, these three must appear in both `docs/twillingate.md` and
`sdk/src/twillingate.ts`.

### OS detection

Detection is now load-bearing — it is the only source of OS. Resolution order:
explicit `os` option → detection → `other`.

Synchronous and cheap — no `getHighEntropyValues()`, which is async.

**Order is the whole design here.** Most of these User-Agents are supersets of
a more generic one: Fire OS contains `Android`, every Android UA contains
`Linux`, iPadOS in desktop mode contains `Macintosh`. Specific always beats
generic, so the checks run most-specific first and return on the first hit.

1. **Consoles and embedded**, which no other check would catch:
   `Xbox` → `xbox`; `PlayStation` → `playstation`; `Nintendo` → `nintendo`;
   `KAIOS` → `kaios`; `Tizen` → `tizen`; `Web0S`/`webOS`/`hpwOS` → `webos`;
   `AppleTV`/`tvOS` → `tvos`.
2. **Android derivatives, before Android**: `Silk` → `fireos`;
   `HarmonyOS` → `harmonyos`.
3. **Apple and Chrome OS**: `CrOS` → `chromeos`; `iPhone`/`iPod` → `ios`;
   `iPad` → `ipados`.
4. **iPadOS in desktop mode**: `navigator.platform === "MacIntel" &&
   navigator.maxTouchPoints > 1` → `ipados`. iPadOS 13+ sends
   `Macintosh; Intel Mac OS X`, so a substring match alone records `macos`.
   **This is the one case only the client can resolve** — `maxTouchPoints` is
   not in the User-Agent — and it is the clearest thing SDK detection buys.
5. `navigator.userAgentData.platform` (Chromium ≥90), which answers
   `Windows`, `macOS`, `Android`, `Linux` and `Chrome OS`/`Chromium OS`
   without sniffing. It runs *after* the specific checks because it reports a
   Fire tablet as `Android`.
6. **Generic UA fallback**: `Android` → `android`; `Windows` → `windows`;
   `Mac OS X`/`Macintosh` → `macos`;
   `FreeBSD`/`OpenBSD`/`NetBSD`/`DragonFly` → `bsd`; `X11`/`Linux` → `linux`.
7. Otherwise `other` — a browser is running on *something*, so `other` is the
   right floor for detection. `unknown` is never produced by the SDK; it only
   appears when no `$os` reaches the server at all.

`watchos` and `visionos` are never returned by detection; they are reachable
only through the explicit `os` option.

### `$os_version` and `$os_name` detection

`$os_version` has always been **declare-only** — `ParseUserAgent` returns
`browserVersion` but never an OS version, and `handlers.go:152` takes it
straight from the declared attribute. Every web view in the database has
`os_version = ''` today. This is the first chance to populate it.

Synchronous parse from the User-Agent: `CPU iPhone OS 17_2` → `17.2`;
`Android 14` → `14`; `Windows NT 10.0` → `10`; `Mac OS X 10_15_7` → `10.15.7`.

Two of those are knowingly wrong. **Windows 10 and 11 are indistinguishable in
a User-Agent** — both report `Windows NT 10.0` — and Safari freezes macOS at
`10_15_7`. Both are recoverable only through
`navigator.userAgentData.getHighEntropyValues(['platformVersion'])`, which is
async.

**That async call is affordable here.** `batchAttributes()` is computed in
`flush()`, not at `emit()`, and the default `flushInterval` is 1000ms — so a
promise kicked off once at init resolves well before the first batch is built,
with no added latency. On Chromium a `platformVersion` major of 13 or greater
means Windows 11; 1–12 means Windows 10.

It is a Chromium-only path, so the synchronous parse stays as the fallback and
both branches are tested. `os_name` is composed from the resolved name and
version (`macOS 14.2`, `Windows 11`), falling back to the raw UA-derived name
when high-entropy values are unavailable.

### Browser detection

Same principle as OS: most specific first, return on the first hit,
because every Chromium UA contains `Chrome` and every Chrome UA contains
`Safari`.

1. **`navigator.brave?.isBrave()`** → `brave`. First, because Brave's
   User-Agent is deliberately identical to Chrome's and nothing later in
   this list can recover it.
2. **`navigator.userAgentData.brands`** (Chromium ≥90) — match `Microsoft
   Edge`, `Opera`, `Samsung Internet`, `Vivaldi`, then `Google Chrome` /
   `Chromium`. This is the API built for the question, and it survives
   User-Agent reduction. Version comes from the matching brand entry.
3. **UA fallback**, derivatives before bases, the existing server order
   extended: `Edg/`, `Edge/`, `SamsungBrowser/`, `OPR/`, `Opera/`,
   `Vivaldi/`, `YaBrowser/`, `DuckDuckGo/`, `Firefox/`, `CriOS/`,
   `Chrome/`, then `Safari/`. The marker is also where the version starts,
   except Safari, which carries its under `Version/` — `Safari/` is the
   WebKit build number.
4. Otherwise `other`. A browser is running, so `other` is the right floor;
   the SDK never produces `unknown`.

### Device detection

Shares the OS pass, and resolves in this order:

1. `OculusBrowser` or `Quest` → `xr`. First, and the reason `xr` is a value
   at all: the OS pass reports these as `android`, so nothing downstream
   could recover it.
2. Console, TV and embedded markers — `Xbox`, `PlayStation`, `Nintendo`,
   `AppleTV`, `tvOS`, `Web0S`, `webOS`, `Tizen` → **`other`**. They get no
   value of their own because `$os` already names each one, but they must
   not fall through to the default, which is what makes a PlayStation a
   `desktop` today.
3. `iPad`, `Tablet`, or the iPadOS desktop-mode signal
   (`navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1`) →
   `tablet`. That is the same test that yields `ipados`, which is what
   keeps the two answers consistent.
4. `navigator.userAgentData.mobile === true` → `mobile`. It is a boolean
   and does not separate tablets, so it runs after the tablet tests.
5. `Mobile` or `iPhone` in the UA → `mobile`.
6. Otherwise `desktop`.

`wearable` is never returned by detection — watchOS has no browser and Wear
OS carries no reliable marker — so it is declare-only, like `watchos` on
`$os`. `xr` is the opposite: detectable, and detection is the only way to
get it, since a Quest browser is a web client that declares nothing.

## Documentation

Same commit, per CLAUDE.md:

- `docs/twillingate.md`: reserved attribute keys table, the declared-
  environment paragraph, the web-enrichment sentence (which now derives
  only country from the connection), the envelope example, the queryable
  views table (`v_views_platforms`, rekeyed `v_views_app_versions`), the
  `product_attributes` row, and the SDK's public API and `data-`
  attributes, including the three `detect*` methods.
- `internal/api/resources.go` `schemaViews`.
- `docs_sync_test.go`: the `aliasKeys` entry for `$platform` is removed;
  `$platform`, `$os_name`, `$browser`, `$browser_version` and `$device` are
  checked in both directions as real keys; `v_views_platforms` joins the
  queryable-view check; and `TestDocumentMatchesSDK` picks up `detectOS`,
  `detectBrowser` and `detectDevice`.

## Testing

TDD throughout.

- `internal/enrich`: each of the three validators — canonical, spaced
  (`Chrome OS`, `Samsung Internet`), unrecognised → `other`, empty →
  `unknown` (the two floors must not be conflated). `ua_test.go` loses
  `TestParseUserAgent` outright rather than adapting it; the function is
  gone.
- `internal/server`: `$platform` lower-cased and pattern-checked, invalid
  warns and stores `unknown`, `$platform` no longer fills `os`; a batch with
  no `$os` stores `unknown` and one with an unrecognised `$os` stores
  `other`, warns, and copies the raw value into `os_name` — but does not warn
  when the client sent `other` itself, and does not overwrite an explicit
  `$os_name`.
- `internal/store/sqlite/migration015_test.go`: the views backfill per kind,
  the raw fold, aggregate history lower-cased and `''`-relabelled but not
  folded, the
  `agg_views_platforms` seed, and the `agg_views_app_versions` rekey asserting
  no row merged and no count changed.
- `views_test.go`: `v_views_platforms` across the aggregate ∪ live boundary,
  including the `(other)` cap.
- `aggregate_views_test.go`: both dimension changes.
- `internal/api`: the `platforms` dimension and the rekeyed `app_versions`.
- `internal/server`: a web batch declaring nothing stores `unknown` for
  browser and device rather than a parsed value, and a bot User-Agent is
  still dropped — the one thing the server still reads the UA for.
- SDK: `$os_version` parsed per OS family, the high-entropy branch resolving
  Windows 11 and true macOS versions, the synchronous fallback when
  `userAgentData` is absent, and that a batch flushed after the promise
  resolves carries the corrected value. Plus a table-driven case per
  vocabulary value with a real User-Agent
  string, plus the orderings that a naive implementation gets wrong — Fire OS
  not reported as `android`, Android not as `linux`, BSD not as `linux`,
  iPadOS desktop mode not as `macos`, and `userAgentData` not overriding a specific UA hit. Also
  `other` as the floor, and an explicit `os` option beating detection.
- SDK detection API: `detectOS`, `detectBrowser` and `detectDevice` return
  what the next batch carries; each option overrides its detected value;
  and OS and device stay consistent — an iPadOS desktop-mode User-Agent
  gives `ipados` with `tablet`, never `macos` with `desktop`.
- SDK browser detection: a table-driven case per vocabulary value with a
  real User-Agent, plus the orderings a naive implementation gets wrong —
  Brave not reported as `chrome` (the `navigator.brave` stub), Edge not as
  `chrome`, Chrome not as `safari`, Samsung Internet not as `chrome`, and
  `brands` losing to nothing because it runs before the UA fallback but
  after `navigator.brave`. Safari's version read from `Version/` and not
  from `Safari/`.
- SDK device detection: the three legacy classes; a Quest User-Agent giving
  `xr` and not `mobile`, with `$os` still `android`; a PlayStation giving
  `other` rather than falling through to `desktop`; and
  `userAgentData.mobile` not overriding a tablet hit. `wearable` is
  reachable only through the `device` option.

## Breaking changes and rollout

1. **`$platform` changes meaning for the second time in two days.** It became
   an alias for `$os` in v26.919.37 (2026-09-19) and stops being one here.
   Deployed app clients sending `$platform: "ios"` lose their OS dimension
   until they ship an update.
2. **`$os` closes and lower-cases.** Every stored value changes case, so any
   Evidence SQL or saved query comparing `os = 'iOS'` silently returns
   nothing. A project declaring an unlisted OS starts recording `other`, and
   rows that were empty become `unknown`.
3. **`v_views_app_versions` rekeys.** Saved SQL selecting its `os` column
   breaks.
4. **The server stops deriving anything from the User-Agent except whether
   it is a bot.** Any ingest path that is not the current JS SDK — a
   backend relay, a custom client — records `unknown` for OS, browser and
   device class unless it declares them. This is the widest of the four:
   it silently empties three dimensions at once for such a client, leaving
   only country, which still comes from the connection IP.
5. **`$browser` and `$device` close and lower-case.** Saved SQL comparing
   `browser = 'Chrome'` or `'Samsung Internet'` returns nothing. The stored
   values themselves are preserved by the migration, so no count moves —
   only the spelling.

Commit as `feat(server)!` / `feat(store)!`.

Prod (`prod-us-3`, systemd) needs a DB copy before the upgrade, same as 012
and 014.
The OS fold is non-destructive for raw rows — the original name is copied
into `os_name` first — but aggregate history has no such column, so its
out-of-vocabulary tail is the part that cannot be reconstructed. The
browser and device folds lose nothing anywhere, for the reason in §5 of the
migration.

## The plausible shim needs no change

Recorded because it looks like a problem and is not. `/js/plausible-shim.js`
is served verbatim from `docs.PlausibleShim` and bound to those exact bytes by
`internal/server/script_test.go`, and it declares no environment at all —
which suggests shim-tagged sites would record `unknown` for OS, browser and
device once the server stops parsing User-Agents.

They will not. The shim is **not a tracker**: it is a class-based tagging
helper that calls `tg.track(...)` on `window.twillingate`, and it is loaded
*after* the tracking snippet. It never builds a batch, so its events carry
whatever `batchAttributes()` supplies — including every detected value. The
collector serves exactly one tracker, `/js/twillingate.js`, so a shim-tagged
site picks up detection with the server upgrade like any other snippet site.

## Out of scope

- Case-folding `$kind` for symmetry with `$platform`.
- An Evidence dashboard page for the platform dimension; the data lands first.
- Taking full browser versions from `fullVersionList`. Majors only, as
  today.
- Moving **bot filtering** to the client, which would be self-defeating.
  The server keeps the raw User-Agent for `IsBot` and nothing else.
