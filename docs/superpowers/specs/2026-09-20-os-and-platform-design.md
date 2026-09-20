# `$os` and `$platform` as two dimensions

Status: proposed
Date: 2026-09-20

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

### `$os` detection leaves the server

`ParseUserAgent` loses its `os` return value and its leading OS switch
(`internal/enrich/ua.go:30-43`). It keeps device class, browser and browser
version. Bot filtering and country lookup are untouched.

This deletes the "declared overrides parsed" rule for `$os` entirely — there
is nothing left to override. Detection belongs on the client, which can see
things a User-Agent cannot, and keeping two sources meant two vocabularies to
hold in agreement.

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
field on the raw row and `v_events_flat`. That is what keeps its unbounded
cardinality harmless.

Two consequences of that choice, both accepted:

- It is **not carried into aggregates**, so it disappears when raw rows roll
  up. Investigating `other` is something you do on recent data, before the
  retention window closes.
- It is **views-only**. `product_events` does not get the column: its OS
  surfaces through `product_attributes`, where a free-form key would blow up
  the attribute cardinality the `top_n` cap exists to bound.

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

## Storage — migration `014_platform.sql`

012 is already applied on prod, so this is a new migration.

```sql
ALTER TABLE views          ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE product_events ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE views          ADD COLUMN os_name  TEXT NOT NULL DEFAULT '';
```

The default does the whole `product_events` backfill on its own, and gives
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

### 2. `product_events` backfill — none

Deliberate. `product_events` has no `kind` column, so `lower(os)` would label
a web SDK's custom events `macos` rather than `web`. Every row keeps the
column default of `unknown`, which is the honest answer; the dimension fills
going forward.

### 3. OS fold — raw rows

**First**, every `views` row whose `os` is outside the vocabulary copies that
value into `os_name`:

```sql
UPDATE views SET os_name = os WHERE lower(os) NOT IN (<vocabulary>) AND os <> '';
```

**Then** `views.os` and `product_events.os` are lower-cased; `''` becomes
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

### 5. `agg_views_platforms`

Modelled on `agg_views_countries`, the existing single-key dimension. That
table is a shape to copy, not a thing to change.

```sql
CREATE TABLE agg_views_platforms (
    project TEXT NOT NULL, day TEXT NOT NULL, platform TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, platform)
) WITHOUT ROWID;
```

Seeded from `agg_views_daily`, which is keyed `(project, day, kind)` and
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

### 6. `agg_views_app_versions` rekey

From `(project, day, os, app_version)` to `(project, day, platform,
app_version)`, matching the key's own stated rationale: `2.4.1` means
unrelated things across the iOS and Android builds of one product.

Rebuilt with `platform = lower(os)`, or `unknown` when that does not match the
platform pattern. For rows whose `os` is canonical — effectively all of them —
this is value-preserving and merges nothing. The exception is the tail:
aggregate history is deliberately not OS-folded, so if two out-of-vocabulary
values coexist for one `(project, day, app_version)` their rows merge into one
`unknown` row whose `visitors` is a sum that can overcount. Accepted only
because the alternative is dropping app-version history for those projects.

**Known transitional cost.** The aggregator rebuilds this table daily from raw
`views.platform`, which is `unknown` for any client that has not shipped
`$platform`. Those rows collapse from `(ios, 2.4.1)` and `(android, 2.4.1)`
into `(unknown, 2.4.1)`. Counts stay correct — aggregation recomputes
`COUNT(DISTINCT actor_id)` from raw — but the split is lost until clients
update, which for native apps is an app-store timeline.

### 7. Views rebuilt

Exactly three views are touched, and no other view is dropped or recreated.
Unlike 012, which had to rebuild all of them because `RENAME COLUMN` would
otherwise leave stale definitions behind, this migration only adds columns.

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
`(project, day, os, os_version)`, `v_views_os` returns both columns, and
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
| `internal/server/ingest.go` | `$platform` → `r.Platform`; `$os_name` → `r.OSName`; `platformRaw` and the alias fold deleted; lower-case + pattern validation |
| `internal/server/handlers.go` | carry `Platform`; stop taking `os` from `ParseUserAgent` |
| `internal/enrich/ua.go` | `NormalizeOS` becomes the validator; `ParseUserAgent` loses its OS switch and return value |

`product_attributes` then exposes `$platform` as a third always-available
system dimension beside `$os` and `$app_version`.

## JS SDK

### Options

- `os?: string` — an **override** for detection.
- `osVersion?: string`, `osName?: string` — overrides for the two new
  detected values, with `data-os-version` and `data-os-name`.
- `platform?: string` — stops being `@deprecated use os` and becomes the real
  platform option, defaulting to `"web"`. `sdk/src/twillingate.ts:211`
  (`this.os = opts.os || opts.platform || null`) is deleted.
- `data-platform` joins `data-os` in the snippet reader.

`batchAttributes` sends `$platform` and `$os` on every batch — both always
resolve to a value — plus `$os_version` and `$os_name` when detection or an
override produced them.

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

## Documentation

Same commit, per CLAUDE.md:

- `docs/twillingate.md`: reserved attribute keys table, the declared-
  environment paragraph, the web-enrichment sentence (which must stop listing
  OS), the envelope example, the queryable views table (`v_views_platforms`,
  rekeyed `v_views_app_versions`), the `product_attributes` row, and the SDK's
  public API and `data-` attributes.
- `internal/api/resources.go` `schemaViews`.
- `docs_sync_test.go`: the `aliasKeys` entry for `$platform` is removed,
  `$platform` and `$os_name` are checked in both directions as real keys, and
  `v_views_platforms` joins the queryable-view check.

## Testing

TDD throughout.

- `internal/enrich`: the validator — canonical, spaced `Chrome OS`,
  unrecognised → `other`, empty → `unknown` (the two floors must not be
  conflated); `ParseUserAgent` no longer returns an OS.
- `internal/server`: `$platform` lower-cased and pattern-checked, invalid
  warns and stores `unknown`, `$platform` no longer fills `os`; a batch with
  no `$os` stores `unknown` and one with an unrecognised `$os` stores
  `other`, warns, and copies the raw value into `os_name` — but does not warn
  when the client sent `other` itself, and does not overwrite an explicit
  `$os_name`.
- `internal/store/sqlite/migration014_test.go`: the views backfill per kind,
  the raw fold, aggregate history lower-cased and `''`-relabelled but not
  folded, the
  `agg_views_platforms` seed, and the `agg_views_app_versions` rekey asserting
  no row merged and no count changed.
- `views_test.go`: `v_views_platforms` across the aggregate ∪ live boundary,
  including the `(other)` cap.
- `aggregate_views_test.go`: both dimension changes.
- `internal/api`: the `platforms` dimension and the rekeyed `app_versions`.
- SDK: `$os_version` parsed per OS family, the high-entropy branch resolving
  Windows 11 and true macOS versions, the synchronous fallback when
  `userAgentData` is absent, and that a batch flushed after the promise
  resolves carries the corrected value. Plus a table-driven case per
  vocabulary value with a real User-Agent
  string, plus the orderings that a naive implementation gets wrong — Fire OS
  not reported as `android`, Android not as `linux`, BSD not as `linux`,
  iPadOS desktop mode not as `macos`, and `userAgentData` not overriding a specific UA hit. Also
  `other` as the floor, and an explicit `os` option beating detection.

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
4. **The server stops deriving OS from the User-Agent.** Any ingest path that
   is not the current JS SDK — a backend relay, a custom client — records
   `unknown` unless it declares `$os`.

Commit as `feat(server)!` / `feat(store)!`.

Prod (`prod-us-3`, systemd) needs a DB copy before the upgrade, same as 012.
The fold is non-destructive for raw rows — the original name is copied into
`os_name` first — but aggregate history has no such column, so its
out-of-vocabulary tail is the part that cannot be reconstructed.

## The plausible shim needs no change

Recorded because it looks like a problem and is not. `/js/plausible-shim.js`
is served verbatim from `docs.PlausibleShim` and bound to those exact bytes by
`internal/server/script_test.go`, and it sends no `$os` — which suggests
shim-tagged sites would record `unknown` once the server stops parsing
User-Agents.

They will not. The shim is **not a tracker**: it is a class-based tagging
helper that calls `tg.track(...)` on `window.twillingate`, and it is loaded
*after* the tracking snippet. It never builds a batch, so its events carry
whatever `batchAttributes()` supplies — including detected `$os`. The
collector serves exactly one tracker, `/js/twillingate.js`, so a shim-tagged
site picks up detection with the server upgrade like any other snippet site.

## Out of scope

- Case-folding `$kind` for symmetry with `$platform`.
- An Evidence dashboard page for the platform dimension; the data lands first.
- Moving browser/device detection to the client. Only OS moves here; the
  User-Agent remains the source for device class, browser and bot filtering.
