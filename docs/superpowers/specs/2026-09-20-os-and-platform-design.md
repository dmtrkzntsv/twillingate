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
is precisely the value that makes undeclared traffic measurable — see the
plausible shim below.

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

`views.os` and `product_events.os` are lower-cased; `''` becomes `unknown`,
and anything else outside the vocabulary becomes `other`.

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

Modelled on `agg_views_countries` — a single-key dimension.

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

`v_views_platforms` is created (the aggregate ∪ live shape of
`v_views_countries`, including the 500-value `(other)` cap).
`v_views_app_versions` and `v_product_attrs` are dropped and recreated — the
first for the rekey, the second to gain a `$platform` arm.

## Reporting

| Touchpoint | Change |
| --- | --- |
| `internal/store/sqlite/aggregate_views.go` | add `{table: "agg_views_platforms", keys: []string{"platform"}}`; `agg_views_app_versions` keys become `{"platform", "app_version"}` |
| `internal/store/sqlite/prune.go`, `registry.go` | add `agg_views_platforms` |
| `internal/store/sqlite/aggregate_product.go` | add `{"platform", "$platform"}` |
| `internal/api/ops_read.go` | `"platforms": {"v_views_platforms", []string{"platform"}}`; `app_versions` keys; dimension enum and tool description |
| `internal/api/resources.go` | `schemaViews` gains `v_views_platforms`; `v_views_app_versions` line rekeyed |
| `internal/store/store.go` | `View.Platform`, `ProductEvent.Platform` |
| `internal/store/sqlite` | insert and scan for both tables |
| `internal/server/ingest.go` | `$platform` → `r.Platform`; `platformRaw` and the alias fold deleted; lower-case + pattern validation |
| `internal/server/handlers.go` | carry `Platform`; stop taking `os` from `ParseUserAgent` |
| `internal/enrich/ua.go` | `NormalizeOS` becomes the validator; `ParseUserAgent` loses its OS switch and return value |

`product_attributes` then exposes `$platform` as a third always-available
system dimension beside `$os` and `$app_version`.

## JS SDK

### Options

- `os?: string` — an **override** for detection.
- `platform?: string` — stops being `@deprecated use os` and becomes the real
  platform option, defaulting to `"web"`. `sdk/src/twillingate.ts:211`
  (`this.os = opts.os || opts.platform || null`) is deleted.
- `data-platform` joins `data-os` in the snippet reader.

`batchAttributes` sends `$platform` and `$os` on every batch; both always
resolve to a value.

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

## Documentation

Same commit, per CLAUDE.md:

- `docs/twillingate.md`: reserved attribute keys table, the declared-
  environment paragraph, the web-enrichment sentence (which must stop listing
  OS), the envelope example, the queryable views table (`v_views_platforms`,
  rekeyed `v_views_app_versions`), the `product_attributes` row, and the SDK's
  public API and `data-` attributes.
- `internal/api/resources.go` `schemaViews`.
- `docs_sync_test.go`: the `aliasKeys` entry for `$platform` is removed,
  `$platform` is checked in both directions as a real key, and
  `v_views_platforms` joins the queryable-view check.

## Testing

TDD throughout.

- `internal/enrich`: the validator — canonical, spaced `Chrome OS`,
  unrecognised → `other`, empty → `unknown` (the two floors must not be
  conflated); `ParseUserAgent` no longer returns an OS.
- `internal/server`: `$platform` lower-cased and pattern-checked, invalid
  warns and stores `unknown`, `$platform` no longer fills `os`; a batch with
  no `$os` stores `unknown` and one with an unrecognised `$os` stores
  `other`.
- `internal/store/sqlite/migration014_test.go`: the views backfill per kind,
  the raw fold, aggregate history lower-cased and `''`-relabelled but not
  folded, the
  `agg_views_platforms` seed, and the `agg_views_app_versions` rekey asserting
  no row merged and no count changed.
- `views_test.go`: `v_views_platforms` across the aggregate ∪ live boundary,
  including the `(other)` cap.
- `aggregate_views_test.go`: both dimension changes.
- `internal/api`: the `platforms` dimension and the rekeyed `app_versions`.
- SDK: a table-driven case per vocabulary value with a real User-Agent
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

Prod (`prod-us-3`, systemd) needs a DB copy before the upgrade, same as 012:
the OS fold is not reversible.

## Open item: the plausible shim

`/js/plausible-shim.js` is served verbatim from `docs.PlausibleShim` and bound
to those exact bytes by `internal/server/script_test.go`. It does not send
`$os`, so once the server stops parsing User-Agents **every shim-tagged site
records `unknown` for OS**. That at least makes the loss visible in a
breakdown rather than silent, which is an argument for `unknown` existing at
all.

Options: add the same detection to the shim (it is server-served, so it
self-upgrades like the SDK), or accept the loss. This needs a decision before
implementation — it is a documented contract with a test holding it in place.

## Out of scope

- Case-folding `$kind` for symmetry with `$platform`.
- An Evidence dashboard page for the platform dimension; the data lands first.
- Moving browser/device detection to the client. Only OS moves here; the
  User-Agent remains the source for device class, browser and bot filtering.
