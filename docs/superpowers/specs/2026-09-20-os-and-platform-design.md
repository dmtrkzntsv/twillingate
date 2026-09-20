# `$os` and `$platform` as two dimensions

Status: proposed
Date: 2026-09-20

## Problem

`$platform` is currently a deprecated alias for `$os` (`internal/server/ingest.go:120`),
folded in `resolveAttributes` when `$os` is absent. That collapses two facts
that are not the same fact:

- **`$os`** — the operating system the client runs on: iOS, Android, macOS,
  Windows, Linux, ChromeOS.
- **`$platform`** — the surface the product is being used through: web, ios,
  android, electron, and whatever else someone ships on.

They coincide for a native app and diverge everywhere else. Safari on an
iPhone is `platform=web, os=iOS`. An Electron build on a Mac is
`platform=electron, os=macOS`. Today the second dimension does not exist, so
neither question can be answered: "how many of my users are on the web build
versus the native one" and "which OS versions do I still have to support" are
the same column.

`$kind` (`web`/`app`/`cli`) is adjacent but not a substitute. It gates
enrichment — only `web` rows are parsed from the connection — and it is
deliberately coarse. It stays exactly as it is.

## Decisions

1. `$platform` becomes an independent reserved key with its own column.
   `$kind` is untouched.
2. **Clean break.** `$platform` never fills `os`. A pre-split client sending
   `$platform: "ios"` alone records a platform and no OS.
3. **`$os` closes; `$platform` stays open.** The server already owns the OS
   vocabulary because it parses it from the User-Agent, so closing it keeps
   declared and parsed values in one row. Platform is client-owned and
   open-ended, so a closed list would need a redeploy every time someone
   ships on a new surface.
4. Platform gets its own aggregate, view and breakdown dimension.
   `agg_views_app_versions` rekeys from `(os, app_version)` to
   `(platform, app_version)`.
5. `$platform` lands on product events too, alongside `$os`.
6. The JS SDK supports both keys and detects `$os` automatically.

## Wire contract

### `$os` — closed vocabulary

`enrich.NormalizeOS` becomes an allowlist. Its current contract is the
opposite ("the vocabulary is a convenience, not an allowlist"), and that
comment flips with it.

| Input | Stored |
| --- | --- |
| a token in `osNames` (case-insensitive) | its canonical name: `iOS`, `Android`, `macOS`, `Windows`, `Linux`, `ChromeOS` |
| any other non-empty value | `other` |
| empty or whitespace | `''` |

`''` and `other` are different answers and must stay distinguishable: `''`
means the OS is not known, `other` means it is known and outside the
vocabulary. Callers are `internal/server/handlers.go:124` and `:175`.

The User-Agent parser already emits only canonical names or `''`, so it needs
no change — only declared values are folded.

### `$platform` — open, bounded, lower-cased

Trim, **lower-case**, then validate against `^[a-z][a-z0-9_]{0,15}$` — the
same shape as `kindPattern`, applied after case folding so a client sending
`iOS` records `ios` rather than being dropped. A value that still fails the
pattern is warned about and left empty, matching how an invalid `$kind` is
handled.

No server-side list. `docs/twillingate.md` names a conventional set — `web`,
`ios`, `android`, `macos`, `windows`, `linux` — without enforcing it, so
`electron`, `tvos` or `quest` need no redeploy.

Note the asymmetry with `$kind`, which validates without case folding. Left
alone deliberately: changing it is a separate behavioural change to a key
that is not otherwise in scope here.

## Storage — migration `014_platform.sql`

012 is already applied on prod, so this is a new migration, not an amendment
to it.

### Columns

```sql
ALTER TABLE views          ADD COLUMN platform TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN platform TEXT NOT NULL DEFAULT '';
```

### Views backfill — faithful

**Order matters: the platform backfill runs before the OS fold below.** It
derives platform from `os`, so folding `os` to `other` first would destroy
the very token it reads — an app row that declared a nonstandard platform
pre-merge would record `platform='other'` instead of what the client sent.

- `kind='web'` → `'web'`.
- `kind='app'` → `lower(os)`. This recovers the original `app_views.platform`
  token exactly: 012 folded that column into `os` through the same
  vocabulary, so lower-casing inverts it.
- Any other kind → `''`.
- A derived value that does not match the platform pattern → `''`, not
  `other`. The value is genuinely unknown for that row, and `other` has no
  defined meaning in an open vocabulary.

### `product_events` backfill — none

Deliberate. `product_events` has no `kind` column, so `lower(os)` would label
a web SDK's custom events `macos` rather than `web`. Empty is the honest
value; the dimension fills going forward.

### OS fold

Raw `views.os` and `product_events.os` values outside the vocabulary become
`other`.

Aggregate history (`agg_views_os`) is **left alone**. Folding it would merge
rows and `SUM` their `visitors`, inventing distinct-visitor counts that were
never measured. The consequence is that days aggregated before this migration
can still show OS names outside the vocabulary; that is real data, and the
daily pass keeps every future aggregate clean because it rebuilds from raw.

This fold is lossy: the original token for an out-of-vocabulary value is gone
once 014 runs.

### `agg_views_platforms`

Modelled on `agg_views_countries` — a single-key dimension.

```sql
CREATE TABLE agg_views_platforms (
    project TEXT NOT NULL, day TEXT NOT NULL, platform TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, platform)
) WITHOUT ROWID;
```

Seeded from `agg_views_daily WHERE kind='web'` → `platform='web'`. That table
is already keyed by `(project, day, kind)` and carries `visitors` and `views`,
so the seed is exact.

App days aggregated before the split get **no** platform row. `agg_views_os`
is keyed `(os, os_version)`, so recovering a per-platform visitor count from
it would mean summing across `os_version` — which double-counts distinct
visitors. A missing row is better than a wrong one.

### `agg_views_app_versions` rekey

From `(project, day, os, app_version)` to `(project, day, platform,
app_version)`. This matches the key's own stated rationale, which was always a
platform argument rather than an OS one: `2.4.1` means unrelated things across
the iOS and Android builds of one product.

The table is rebuilt with `platform = lower(os)`, or `other` when that does
not match the platform pattern.

For rows whose `os` is in the canonical vocabulary — effectively all of them —
`lower()` is injective, so no rows merge and no counts change. The exception
is the tail: aggregate history is deliberately **not** OS-folded (see above),
so a project that declared a nonstandard platform token before the views
merge can still hold out-of-vocabulary `os` values here. If two such values
coexist for one `(project, day, app_version)`, their rows merge into a single
`other` row whose `visitors` is a sum, which can overcount distinct visitors.

That is the same defect that stops `agg_views_os` from being folded, and it is
accepted here only because the alternative is dropping app-version history for
those projects entirely. The tail is bounded to one `other` row per
`(project, day, app_version)`, and `views`/`visitors` totals for in-vocabulary
platforms are unaffected.

### Views rebuilt

`v_views_platforms` is created (the `agg` ∪ live-half shape of
`v_views_countries`, including the 500-value `(other)` cap).
`v_views_app_versions` and `v_product_attrs` are dropped and recreated — the
first for the rekey, the second to gain a `$platform` arm in its live half.

## Reporting

| Touchpoint | Change |
| --- | --- |
| `internal/store/sqlite/aggregate_views.go` | one row: `{table: "agg_views_platforms", keys: []string{"platform"}}`; `agg_views_app_versions` keys become `{"platform", "app_version"}` |
| `internal/store/sqlite/prune.go`, `registry.go` | add `agg_views_platforms` to the table lists |
| `internal/store/sqlite/aggregate_product.go` | add `{"platform", "$platform"}` to the system dimensions |
| `internal/api/ops_read.go` | `"platforms": {"v_views_platforms", []string{"platform"}}`; `app_versions` keys become `platform, app_version`; dimension enum, tool description |
| `internal/api/resources.go` | `schemaViews` gains `v_views_platforms`; the `v_views_app_versions` line changes key |
| `internal/store/store.go` | `View.Platform`, `ProductEvent.Platform` |
| `internal/store/sqlite` | insert and scan for both tables |
| `internal/server/ingest.go` | `$platform` → `r.Platform`; `platformRaw` and the alias fold deleted; lower-case + pattern validation |
| `internal/server/handlers.go` | carry `Platform` onto `store.View` and `store.ProductEvent` |
| `internal/enrich/ua.go` | `NormalizeOS` becomes an allowlist returning `other` |

`product_attributes` then exposes `$platform` as a third always-available
system dimension beside `$os` and `$app_version`.

## JS SDK

### Options

- `os?: string` — unchanged in spelling, now an **override** for detection.
- `platform?: string` — stops being `@deprecated use os` and becomes the real
  platform option, defaulting to `"web"`. `sdk/src/twillingate.ts:211`
  (`this.os = opts.os || opts.platform || null`) is deleted.
- `data-platform` is added beside `data-os` in the snippet reader.

`batchAttributes` sends `$platform` always (it has a default) and `$os` when
resolved.

### OS detection

Resolution order: explicit `os` option → detection → omit.

Detection is synchronous and cheap — no `getHighEntropyValues()`:

1. `navigator.userAgentData.platform` (Chromium): `Windows`, `macOS`,
   `Android`, `Linux`, `Chrome OS`/`Chromium OS` → `ChromeOS`.
2. Fallback UA/`navigator.platform` sniff: `iPhone`/`iPad`/`iPod` → `iOS`;
   `Android` → `Android`; `CrOS` → `ChromeOS`; `Windows` → `Windows`;
   `Mac OS X` → `macOS`; `Linux` → `Linux`.
3. iPadOS: `navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1`
   → `iOS`. iPadOS 13+ reports as a Mac, a blind spot the server's parser
   shares.

**When detection cannot place the OS it omits `$os` rather than sending
`other`.** Declared beats parsed on the server, so sending `other` would
overwrite a better answer with a worse one; omitting leaves the server's
User-Agent parser its chance.

This materially improves web OS accuracy — the server's parser has no
`Windows` case at all (`internal/enrich/ua.go:35-43`), so Windows browsers
currently record `os=''`. It also means **web OS breakdowns will shift after
an SDK upgrade**, with `Windows` and iPad `iOS` appearing where `''` was.

The parser's missing `Windows` case is left unfixed here on purpose: it is a
separate defect with its own blast radius on non-SDK traffic, and bundling it
would hide it inside a large change.

## Documentation

Same commit, per CLAUDE.md:

- `docs/twillingate.md`: reserved attribute keys table (Environment group),
  the declared-environment paragraph, the envelope example, the queryable
  views table (`v_views_platforms`, rekeyed `v_views_app_versions`), the
  `product_attributes` row, and the SDK's public API and `data-` attributes.
- `internal/api/resources.go` `schemaViews`, as above.
- `docs_sync_test.go`: the `aliasKeys` entry for `$platform` is removed, and
  `$platform` is checked in both directions as a real key; `v_views_platforms`
  joins the queryable-view check.

## Testing

TDD throughout.

- `internal/enrich`: `NormalizeOS` allowlist — canonical, unknown → `other`,
  empty → `''`.
- `internal/server`: `$platform` lower-cased and pattern-checked; an invalid
  value warns and leaves it empty; `$platform` no longer fills `os`; both keys
  resolve independently.
- `internal/store/sqlite/migration014_test.go`: the views backfill for each
  kind, the OS fold on raw rows, aggregate history left untouched, the
  `agg_views_platforms` seed, and the `agg_views_app_versions` rekey — the
  last asserting no row merged and no count changed.
- `views_test.go`: `v_views_platforms` across the aggregate ∪ live boundary,
  including the `(other)` cap.
- `aggregate_views_test.go`: both dimension changes.
- `internal/api`: the `platforms` breakdown dimension and the rekeyed
  `app_versions`.
- SDK: detection per branch, the omit-when-unknown rule, and that an explicit
  `os` option beats detection.

## Breaking changes and rollout

This breaks compatibility in three places, and the release note has to say so
plainly:

1. **`$platform` changes meaning for the second time in two days.** It became
   an alias for `$os` in v26.919.37 (2026-09-19) and stops being one here.
   Deployed app clients sending `$platform: "ios"` silently lose their OS
   dimension until they ship an update — on app-store timelines, not the
   operator's.
2. **`$os` closes.** A project declaring an unlisted OS (`FreeBSD`) starts
   recording `other`.
3. **`v_views_app_versions` rekeys.** Saved SQL selecting its `os` column
   breaks.

Commit as `feat(server)!` / `feat(store)!`.

Prod (`prod-us-3`, systemd) needs a DB copy before the upgrade, same as 012:
the OS fold is not reversible.

## Out of scope

- The User-Agent parser's missing `Windows` case — a separate fix.
- Case-folding `$kind` for symmetry with `$platform`.
- An Evidence dashboard page for the platform dimension; the data lands first.
