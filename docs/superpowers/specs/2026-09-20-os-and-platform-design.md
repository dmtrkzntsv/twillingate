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
3. **`$os` closes to a lower-case vocabulary and is never empty**;
   `$platform` stays open, lower-cased, bounded by `$kind`'s pattern.
4. **OS detection moves to the JS SDK. The server only validates.** The
   User-Agent is no longer a source of OS.
5. Platform gets its own aggregate, view and breakdown dimension, and
   `agg_views_app_versions` rekeys from `(os, app_version)` to
   `(platform, app_version)`.
6. `$platform` lands on product events too, alongside `$os`.

## Wire contract

### `$os` — closed, lower-case, never empty

```
// desktop
'windows' | 'macos' | 'linux' | 'chromeos'
// BSD
| 'freebsd' | 'openbsd' | 'netbsd'
// mobile / tablet
| 'ios' | 'ipados' | 'android' | 'fireos' | 'harmonyos' | 'kaios'
// TV, wearable, XR
| 'tvos' | 'watchos' | 'visionos' | 'tizen' | 'webos'
// console
| 'playstation' | 'xbox' | 'nintendo'
// floor
| 'other'
```

The cut: an OS earns a value when it is a distinct product target — something
a team would ship, test or drop support for separately — and is either
detectable from a browser or declarable by a native client. Distributions are
not OSes (`ubuntu` is `linux`), and dead platforms are not worth a value
(`windows_phone`, `blackberry`). `other` remains the floor, so nothing is ever
lost by the list being incomplete.

Five of these are **declare-only** — no browser reaches them, or none reports
distinguishably: `watchos`, `visionos` (Vision Pro's Safari reports as a Mac),
`openbsd` and `netbsd` in practice, and `harmonyos` on devices that ship an
Android-compatible User-Agent. They exist so a native client has a correct
value to send.

**`ipados` splits from `ios`.** It is a genuinely separate target, but note the
consequence: iPad traffic that previously counted as `ios` now counts
separately, so an existing `ios` series will step down on the day this ships.
Pre-iPadOS-13 iPads (iOS 12 and earlier) are reported as `ipados` by the
`iPad` substring, which is wrong but affects a population that is effectively
gone.

Server-side this is **validation only**. Trim, lower-case, strip spaces and
dashes (`"Chrome OS"` → `chromeos`, which is what
`navigator.userAgentData.platform` returns), then match the vocabulary.
Anything else — including an absent `$os` — stores `other`.

`enrich.NormalizeOS` becomes that validator. Its current contract is the
opposite ("the vocabulary is a convenience, not an allowlist"), and its
comment flips with it.

**`other` therefore means two things** — "a real OS outside the list" and "no
information at all" — and nothing downstream can tell them apart. That is the
accepted cost of never being empty. It is what a server-relayed product event
and a `cli` client that declares nothing will record.

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
`iOS` records `ios` rather than being dropped. A value that still fails is
warned about and left **empty**.

Platform is empty when undeclared, while `$os` falls back to `other`. The
asymmetry is deliberate: `other` is a defined member of a closed vocabulary,
whereas in an open one it would be indistinguishable from a real value a
client chose.

No server-side list. `docs/twillingate.md` names a conventional set — `web`,
`ios`, `android`, `macos`, `windows`, `linux` — without enforcing it, so
`electron`, `tvos` or `quest` need no redeploy.

Note the asymmetry with `$kind`, which validates without case folding. Left
alone deliberately: it is a separate behavioural change to a key not
otherwise in scope.

## Storage — migration `014_platform.sql`

012 is already applied on prod, so this is a new migration.

```sql
ALTER TABLE views          ADD COLUMN platform TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN platform TEXT NOT NULL DEFAULT '';
```

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
- Any other kind → `''`.
- A derived value that does not match the platform pattern → `''`. It is
  genuinely unknown for that row, and `other` has no meaning in an open
  vocabulary.

### 2. `product_events` backfill — none

Deliberate. `product_events` has no `kind` column, so `lower(os)` would label
a web SDK's custom events `macos` rather than `web`. Empty is honest; the
dimension fills going forward.

### 3. OS fold — raw rows

`views.os` and `product_events.os` are lower-cased, and anything outside the
vocabulary — including `''` — becomes `other`.

### 4. OS fold — aggregate history

`agg_views_os` and `agg_product_attrs`' `$os` rows are **lower-cased only**.
Out-of-vocabulary values and `''` survive there.

Folding them would merge rows and `SUM` their `visitors`, producing
distinct-visitor counts that were never measured. So the closed, never-empty
guarantee holds **for raw rows and for every aggregate built after 014** —
days aggregated before it can still show `''` or a long tail. That is real
data; the daily pass rebuilds from raw, so it is self-correcting for
everything that has not yet rolled up.

Lower-casing is injective over the canonical vocabulary, so this step merges
nothing in practice.

### 5. `agg_views_platforms`

Modelled on `agg_views_countries` — a single-key dimension.

```sql
CREATE TABLE agg_views_platforms (
    project TEXT NOT NULL, day TEXT NOT NULL, platform TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, platform)
) WITHOUT ROWID;
```

Seeded from `agg_views_daily WHERE kind='web'` → `platform='web'`. That table
is keyed `(project, day, kind)` and carries `visitors` and `views`, so the
seed is exact.

App days aggregated before the split get **no** platform row. `agg_views_os`
is keyed `(os, os_version)`, so a per-platform visitor count would mean
summing across `os_version` and double-counting. A missing row beats a wrong
one.

### 6. `agg_views_app_versions` rekey

From `(project, day, os, app_version)` to `(project, day, platform,
app_version)`, matching the key's own stated rationale: `2.4.1` means
unrelated things across the iOS and Android builds of one product.

Rebuilt with `platform = lower(os)`, or `other` when that does not match the
platform pattern. For rows whose `os` is canonical — effectively all of them —
this is value-preserving and merges nothing. The exception is the tail:
aggregate history is deliberately not OS-folded, so if two out-of-vocabulary
values coexist for one `(project, day, app_version)` their rows merge into one
`other` row whose `visitors` is a sum that can overcount. Accepted only
because the alternative is dropping app-version history for those projects.

**Known transitional cost.** The aggregator rebuilds this table daily from raw
`views.platform`, which is `''` for any client that has not shipped
`$platform`. Those rows collapse from `(ios, 2.4.1)` and `(android, 2.4.1)`
into `('', 2.4.1)`. Counts stay correct — aggregation recomputes
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
   `Mac OS X`/`Macintosh` → `macos`; `FreeBSD` → `freebsd`;
   `OpenBSD` → `openbsd`; `NetBSD` → `netbsd`; `X11`/`Linux` → `linux`.
7. Otherwise `other`.

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

- `internal/enrich`: the validator — canonical, spaced `Chrome OS`, unknown →
  `other`, empty → `other`; `ParseUserAgent` no longer returns an OS.
- `internal/server`: `$platform` lower-cased and pattern-checked, invalid
  warns and leaves it empty, `$platform` no longer fills `os`; a batch with no
  `$os` stores `other`.
- `internal/store/sqlite/migration014_test.go`: the views backfill per kind,
  the raw fold, aggregate history lower-cased but not folded, the
  `agg_views_platforms` seed, and the `agg_views_app_versions` rekey asserting
  no row merged and no count changed.
- `views_test.go`: `v_views_platforms` across the aggregate ∪ live boundary,
  including the `(other)` cap.
- `aggregate_views_test.go`: both dimension changes.
- `internal/api`: the `platforms` dimension and the rekeyed `app_versions`.
- SDK: a table-driven case per vocabulary value with a real User-Agent
  string, plus the orderings that a naive implementation gets wrong — Fire OS
  not reported as `android`, Android not as `linux`, iPadOS desktop mode not
  as `macos`, and `userAgentData` not overriding a specific UA hit. Also
  `other` as the floor, and an explicit `os` option beating detection.

## Breaking changes and rollout

1. **`$platform` changes meaning for the second time in two days.** It became
   an alias for `$os` in v26.919.37 (2026-09-19) and stops being one here.
   Deployed app clients sending `$platform: "ios"` lose their OS dimension
   until they ship an update.
2. **`$os` closes and lower-cases.** Every stored value changes case, so any
   Evidence SQL or saved query comparing `os = 'iOS'` silently returns
   nothing. A project declaring an unlisted OS starts recording `other`.
3. **`v_views_app_versions` rekeys.** Saved SQL selecting its `os` column
   breaks.
4. **The server stops deriving OS from the User-Agent.** Any ingest path that
   is not the current JS SDK — a backend relay, a custom client — records
   `other` unless it declares `$os`.

Commit as `feat(server)!` / `feat(store)!`.

Prod (`prod-us-3`, systemd) needs a DB copy before the upgrade, same as 012:
the OS fold is not reversible.

## Open item: the plausible shim

`/js/plausible-shim.js` is served verbatim from `docs.PlausibleShim` and bound
to those exact bytes by `internal/server/script_test.go`. It does not send
`$os`, so once the server stops parsing User-Agents **every shim-tagged site
records `other` for OS**.

Options: add the same detection to the shim (it is server-served, so it
self-upgrades like the SDK), or accept the loss. This needs a decision before
implementation — it is a documented contract with a test holding it in place.

## Out of scope

- Case-folding `$kind` for symmetry with `$platform`.
- An Evidence dashboard page for the platform dimension; the data lands first.
- Moving browser/device detection to the client. Only OS moves here; the
  User-Agent remains the source for device class, browser and bot filtering.
