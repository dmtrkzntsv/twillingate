# Declared environment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the client the only source of its environment — `$os`, `$os_version`, `$os_name`, `$platform`, `$browser`, `$browser_version`, `$device` — with closed lower-case vocabularies validated on the server, `platform` as its own column, aggregate, view and breakdown dimension, and detection (`detectOS`, `detectBrowser`, `detectDevice`) living in the JS SDK instead of `ParseUserAgent`, which is deleted.

**Architecture:** One migration (`015_environment.sql`) adds `platform` and `os_name`, folds the stored OS, browser and device values into the new vocabularies, seeds `agg_views_platforms` and rekeys `agg_views_app_versions` to `(platform, app_version)`. `internal/enrich` keeps `IsBot` and `CleanReferrer` and gains three validators; the server validates and warns but never parses; the SDK detects from a flat `ClientSignals` list and sends every environment key on every batch. Ripple order follows the layers CLAUDE.md fixes: `enrich` → `store`/`store/sqlite` → `server` → `api` (+docs) → SDK → Evidence/scripts/deployment docs.

**Tech Stack:** Go 1.26 (stdlib, `modernc.org/sqlite`, `github.com/modelcontextprotocol/go-sdk`), SQLite, TypeScript SDK (esbuild, vitest + jsdom), Evidence (DuckDB WASM) dashboards.

**Spec:** `docs/superpowers/specs/2026-09-20-os-and-platform-design.md` (PR #38). Read it first; every task cites the section it implements. The spec was written against the schema PR #43 left behind (`project_id INTEGER`, raw product table `events`), which is what `main` has now.

## Global Constraints

- Migration file is `internal/store/sqlite/migrations/015_environment.sql`, one transaction, irreversible. `014_project_ids.sql` is the last migration on `main`; do not touch it.
- **Statement order in 015**: platform backfill (reads `views.os`) **before** the OS fold (rewrites it). Nothing after 015 ever derives platform from os.
- `$os` vocabulary (closed, lower-case): `windows macos linux bsd chromeos ios ipados android fireos harmonyos kaios tvos watchos visionos tizen webos playstation xbox nintendo other unknown`.
- `$browser` vocabulary: `chrome safari firefox edge opera samsung_internet brave vivaldi duckduckgo yandex other unknown`.
- `$device` vocabulary: `desktop mobile tablet wearable xr other unknown`.
- `other` ≠ `unknown`, everywhere: `other` = present but outside the vocabulary; `unknown` = no information. An absent or empty `$os`/`$browser`/`$device` stores `unknown`; a present unrecognised value stores `other` **and warns** (`$os "iOS 17" is not a known value, stored as other`) — but never warns when the client sent `other` itself.
- `$os_name` is free-form, verbatim, **empty when absent** (not `unknown`), views-only, never aggregated, never on `v_events_flat`. When `$os` is unrecognised and `$os_name` absent, the raw `$os` is copied into `os_name`; an explicit `$os_name` always wins.
- `$platform` is open: trim, **lower-case**, then validate against `^[a-z][a-z0-9_]{0,15}$` (the same pattern as `$kind`). Absent or invalid stores `unknown` (invalid warns). No `other` for platform. `$kind` itself is untouched (still no case folding).
- `$platform` never fills `os`. The alias is deleted outright.
- The server derives **nothing** from the User-Agent except `IsBot`. `ParseUserAgent`, `majorAfter` and `osNames` are deleted. The bot drop still applies to `kind == "web"` only.
- New reserved keys: `$platform`, `$os_name`, `$browser`, `$browser_version`, `$device`. On product events `$os` and `$platform` are columns; `$os_version`, `$os_name`, `$browser`, `$browser_version`, `$device` are resolved and dropped (never reach `events.attributes`).
- New table `agg_views_platforms(project_id, day, platform, visitors, views)`, seeded from `agg_views_daily` (`web` → `web`, every other kind → `unknown`). New view `v_views_platforms` copies the `v_views_countries` shape. `v_views_app_versions` is `(project_id, day, platform, app_version, visitors, views)`. `v_product_attrs` gains a `$platform` arm. No other view is dropped or recreated.
- Aggregate history OS fold: lower-case canonical values and relabel `''` → `unknown` only; out-of-vocabulary values are left as they are (folding them would sum distinct-visitor counts). Browser/device folds are loss-free everywhere.
- SDK: every detected value has an override option and a `data-` attribute, and **an explicit option always beats detection**. `platform` defaults to `"web"` only while `kind` is `web`; any other kind sends no `$platform` unless the option is set. `detectOS`/`detectBrowser`/`detectDevice` are exported functions and instance methods, take an optional `ClientSignals`, and when one is supplied consult **only** what it contains.
- Docs contract (CLAUDE.md): `docs/twillingate.md` changes ride in the same commit as the surface they describe; `schemaViews` in `internal/api/resources.go` changes with the migration; `docs/deployment.md` gains the 015 upgrade note.
- Commit messages: Conventional Commits, per-task `feat(...)`/`refactor(...)`/`docs(...)`. The PR title (squash subject) is `feat!: let the client declare its environment` with the `BREAKING CHANGE:` footer from Task 9.
- Go is at `/usr/local/go/bin/go` on the dev box and may not be on `PATH`. `go test -race ./internal/store/...` takes ~6 minutes: run it with `-timeout 900s` in the background or narrow with `-run`.
- Out of scope (do not do): case-folding `$kind`; an Evidence page for platforms; full browser versions; client-side bot filtering.

## Build order

Every change here is additive at the type level (`store.View` and `store.ProductEvent` gain fields), so `go build ./...` stays green after every task. Tests, however, are red inside a task until its last step: Task 2's migration rekeys `agg_views_app_versions` and the aggregator only follows in the same task. Each task ends with the packages it touched green.

Work on `feat/environment`, branched from `main` after PR #43 merged (`6a65d1c`). Never commit on `main`.

---

### Task 0: Branch, spec and plan

**Files:**
- Create: `docs/superpowers/specs/2026-09-20-os-and-platform-design.md` (copied from the `spec/os-and-platform` branch, PR #38)
- Create: `docs/superpowers/plans/2026-09-21-environment.md` (this file)

- [ ] **Step 1: Branch from main**

```bash
git fetch origin main
git checkout -B feat/environment origin/main
```

- [ ] **Step 2: Bring the spec onto the branch, status unchanged for now**

```bash
git show origin/spec/os-and-platform:docs/superpowers/specs/2026-09-20-os-and-platform-design.md \
  > docs/superpowers/specs/2026-09-20-os-and-platform-design.md
git add docs/superpowers/specs/2026-09-20-os-and-platform-design.md
git commit -m "docs: add the declared-environment design spec"
```

- [ ] **Step 3: Commit the plan**

```bash
git add docs/superpowers/plans/2026-09-21-environment.md
git commit -m "docs: add the declared-environment implementation plan"
```

---

### Task 1: `internal/enrich` — validators in, `ParseUserAgent` out

Implements spec "Wire contract" (`$os`, `$browser`, `$device` validation) and decision 11. Only this package changes; `handlers.go` still calls `ParseUserAgent` after this task, so **`go build ./...` fails until Task 3**. That is the one broken window in the plan; Task 3 removes the caller. Verify with `go build ./internal/enrich/` and `go test ./internal/enrich/` only.

**Files:**
- Modify: `internal/enrich/ua.go` (rewrite; keep `IsBot` and `botMarkers` exactly as they are)
- Modify: `internal/enrich/ua_test.go` (delete `TestParseUserAgent`, replace `TestNormalizeOS`, add the browser/device/vocabulary tests; keep `TestIsBot`)

**Interfaces:**
- Produces: `enrich.NormalizeOS(v string) (string, bool)`, `enrich.NormalizeBrowser(v string) (string, bool)`, `enrich.NormalizeDevice(v string) (string, bool)` — the canonical value and `ok`. `ok == false` means present-but-unrecognised (value is `other`); absent is `("unknown", true)`; a deliberate `other` is `("other", true)`.
- Produces: `enrich.OSValues`, `enrich.BrowserValues`, `enrich.DeviceValues []string` — the vocabularies, each ending `"other", "unknown"`.

- [ ] **Step 1: Replace the tests**

Delete `TestParseUserAgent` and the old `TestNormalizeOS` from `internal/enrich/ua_test.go` and add:

```go
func TestNormalizeOS(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"ios": {"ios", true}, "iOS": {"ios", true}, " macOS ": {"macos", true},
		"Chrome OS": {"chromeos", true}, "chrome-os": {"chromeos", true},
		"HarmonyOS": {"harmonyos", true}, "PlayStation": {"playstation", true},
		"other":   {"other", true},   // sent deliberately: legitimate, never warned
		"unknown": {"unknown", true}, // same
		"":        {"unknown", true}, // absent: nothing to validate
		"   ":     {"unknown", true},
		"haiku":   {"other", false}, // present but outside the vocabulary
		"iOS 17":  {"other", false},
		"ubuntu":  {"other", false}, // distributions are not OSes
	}
	for in, c := range cases {
		got, ok := NormalizeOS(in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeOS(%q) = (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizeBrowser(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"Chrome": {"chrome", true}, "safari": {"safari", true}, "Edge": {"edge", true},
		"Samsung Internet": {"samsung_internet", true}, "samsung-internet": {"samsung_internet", true},
		"brave": {"brave", true}, "DuckDuckGo": {"duckduckgo", true},
		"other": {"other", true}, "": {"unknown", true},
		"netscape": {"other", false}, "Chrome 126": {"other", false},
	}
	for in, c := range cases {
		got, ok := NormalizeBrowser(in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeBrowser(%q) = (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizeDevice(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"desktop": {"desktop", true}, "Mobile": {"mobile", true}, "tablet": {"tablet", true},
		"wearable": {"wearable", true}, "XR": {"xr", true},
		"other": {"other", true}, "": {"unknown", true},
		"phablet": {"other", false}, "tv": {"other", false},
	}
	for in, c := range cases {
		got, ok := NormalizeDevice(in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeDevice(%q) = (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}

// Every vocabulary is lower-case, closed under its own validator, and ends
// with the two floors — other and unknown are different answers and both
// must be sendable.
func TestVocabulariesAreLowerCaseAndClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		values    []string
		normalize func(string) (string, bool)
	}{
		"os":      {OSValues, NormalizeOS},
		"browser": {BrowserValues, NormalizeBrowser},
		"device":  {DeviceValues, NormalizeDevice},
	} {
		if n := len(tc.values); n < 2 || tc.values[n-2] != "other" || tc.values[n-1] != "unknown" {
			t.Errorf("%s: vocabulary must end with other, unknown: %v", name, tc.values)
		}
		for _, v := range tc.values {
			if v != strings.ToLower(v) || strings.ContainsAny(v, " -") {
				t.Errorf("%s: %q is not a lower-case token", name, v)
			}
			if got, ok := tc.normalize(v); got != v || !ok {
				t.Errorf("%s: %q does not round-trip: (%q, %v)", name, v, got, ok)
			}
		}
	}
}
```

Add `"strings"` to the test file's imports.

- [ ] **Step 2: Run the tests to see them fail**

Run: `cd internal/enrich && go test ./ 2>&1 | head`
Expected: compile errors — `NormalizeOS` returns one value; `NormalizeBrowser`, `NormalizeDevice`, `OSValues`… undefined.

- [ ] **Step 3: Rewrite `ua.go`**

Replace everything in `internal/enrich/ua.go` after `IsBot` (i.e. delete `ParseUserAgent`, `majorAfter`, `osNames`, the old `NormalizeOS`) and change the package comment. The file becomes:

```go
// Package enrich keeps the two things the server still derives from a
// request — whether the User-Agent belongs to a bot, and a referrer's
// source — and validates the environment a client declares about itself.
// The User-Agent is no longer a source of OS, browser or device class:
// the client detects those (sdk/src/detect.ts) and the server only checks
// a declared value against a closed vocabulary.
package enrich

import "strings"

var botMarkers = []string{
	"bot", "crawler", "spider", "crawling", "headless", "lighthouse",
	"slurp", "curl/", "wget/", "python-requests", "facebookexternalhit", "preview",
}

func IsBot(ua string) bool {
	if ua == "" {
		return true
	}
	l := strings.ToLower(ua)
	for _, m := range botMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// Vocabularies. A value earns a place when it is a distinct product target
// — something a team would ship, test or drop support for separately —
// and is either detectable from a browser or declarable by a native
// client. Each list ends with the two floors, which must stay distinct:
// other means a real value outside the list, unknown means no information
// at all. Collapsing them would make a server-relayed event
// indistinguishable from a genuine FreeBSD.
var (
	OSValues = []string{
		"windows", "macos", "linux", "bsd", "chromeos",
		"ios", "ipados", "android", "fireos", "harmonyos", "kaios",
		"tvos", "watchos", "visionos", "tizen", "webos",
		"playstation", "xbox", "nintendo",
		"other", "unknown",
	}
	BrowserValues = []string{
		"chrome", "safari", "firefox", "edge", "opera", "samsung_internet",
		"brave", "vivaldi", "duckduckgo", "yandex",
		"other", "unknown",
	}
	DeviceValues = []string{
		"desktop", "mobile", "tablet", "wearable", "xr",
		"other", "unknown",
	}
)

var (
	osSet      = set(OSValues)
	browserSet = set(BrowserValues)
	deviceSet  = set(DeviceValues)
)

func set(values []string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// NormalizeOS validates a declared $os against OSValues. Absent stores
// unknown; present but unrecognised stores other and reports ok=false so
// the caller can warn and preserve the raw value in os_name. A client
// sending other itself is recognised (ok=true): that is a legitimate
// value, not a mistake to warn about.
func NormalizeOS(v string) (string, bool) { return closed(v, osSet, "") }

// NormalizeBrowser is NormalizeOS for $browser. Spaces and dashes fold to
// underscores so "Samsung Internet" is samsung_internet.
func NormalizeBrowser(v string) (string, bool) { return closed(v, browserSet, "_") }

// NormalizeDevice is NormalizeOS for $device.
func NormalizeDevice(v string) (string, bool) { return closed(v, deviceSet, "") }

// closed trims, lower-cases, replaces spaces and dashes with sep ("Chrome
// OS" → chromeos, "Samsung Internet" → samsung_internet), then matches
// vocab. It never rejects: an unrecognised value is stored as other so a
// client shipping a value this server has not learned yet is not handed
// a 4xx, which the retry rules classify as a poison batch to drop.
func closed(v string, vocab map[string]bool, sep string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown", true
	}
	folded := strings.NewReplacer(" ", sep, "-", sep).Replace(strings.ToLower(v))
	if vocab[folded] {
		return folded, true
	}
	return "other", false
}
```

- [ ] **Step 4: Run the package tests**

Run: `cd internal/enrich && go test -cover ./`
Expected: PASS, coverage ≥ 90%.

- [ ] **Step 5: Commit**

```bash
git add internal/enrich/ua.go internal/enrich/ua_test.go
git commit -m "feat(enrich)!: validate declared os, browser and device against closed vocabularies and drop the User-Agent parser"
```

---

### Task 2: Migration 015, the store types, the aggregator and every store test

Implements spec "Storage — migration 015" (statements 1–8), "Reporting" rows for `internal/store/*`, and "Interaction with the existing OS aggregate". After Step 3 the migration exists but the aggregator still writes `os` into the rekeyed `agg_views_app_versions`, so several store tests fail until Step 8. Verify only what each step names; the whole package runs at Step 12.

**Files:**
- Create: `internal/store/sqlite/migrations/015_environment.sql`
- Create: `internal/store/sqlite/migration015_test.go`
- Modify: `internal/store/store.go:16-40` (`View`, `ProductEvent`)
- Modify: `internal/store/sqlite/write.go:22-40, 51-75`
- Modify: `internal/store/sqlite/aggregate_views.go:117-129` (`viewDimensions`)
- Modify: `internal/store/sqlite/aggregate_product.go` (`systemDims`)
- Modify: `internal/store/sqlite/prune.go:15-19` (`viewsAggTables`)
- Modify: `internal/store/sqlite/registry.go:226-233` (`projectTables`)
- Modify: `internal/store/sqlite/sqlite_test.go:137-160` (`TestMigrationViews`)
- Modify: `internal/store/sqlite/aggregate_views_test.go` (`seedViewDay`, `TestAggregateViewDayDimensions`)
- Modify: `internal/store/sqlite/views_test.go` (`TestStitchViewsInvariantAllViewsDimensions`, `TestProductAttrsViewSystemDimensionsWithoutDeclaredKeys`)
- Modify: `internal/store/sqlite/write_test.go` (`TestWriteViewsAppRoundTrip`, the product-event round trip)
- Modify: `internal/store/sqlite/aggregate_product_test.go` (`TestRollupWritesSystemDimensions`)

**Interfaces:**
- Produces: `store.View.Platform string`, `store.View.OSName string`, `store.ProductEvent.Platform string`. The store writes them as given; the never-empty guarantee is the server's (Task 3).
- Produces: tables `agg_views_platforms`, rekeyed `agg_views_app_versions(project_id, day, platform, app_version, …)`; views `v_views_platforms`, `v_views_app_versions(project_id, day, platform, app_version, visitors, views)`, `v_product_attrs` with `$platform` rows.

- [ ] **Step 1: Write the migration test**

Create `internal/store/sqlite/migration015_test.go`:

```go
package sqlite

import (
	"context"
	"strings"
	"testing"
)

// seed014 builds a database at schema 014 holding one row per case the
// 015 folds have to get right: canonical, out-of-vocabulary and empty
// values in every column that closes, an app row 012 folded a platform
// into, and aggregate history in each shape.
func seed014(t *testing.T) *DB {
	t.Helper()
	db := newTestDBAt(t, 14)
	for _, q := range []string{
		`INSERT INTO projects (id, name) VALUES (1, 'Blog'), (2, 'App')`,
		`INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind, path, os, browser, device, app_version) VALUES
		 ('w1',1,'2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','web','a1','connection','/','Windows','Chrome','desktop',''),
		 ('w2',1,'2026-09-10T10:01:00Z','2026-09-10T10:01:00Z','web','a2','connection','/','Haiku','Samsung Internet','mobile',''),
		 ('w3',1,'2026-09-10T10:02:00Z','2026-09-10T10:02:00Z','web','a3','connection','/','','','',''),
		 ('a1',2,'2026-09-10T11:00:00Z','2026-09-10T11:00:00Z','app','i1','install','/home','iOS','','','2.4.1'),
		 ('a2',2,'2026-09-10T11:01:00Z','2026-09-10T11:01:00Z','app','i2','install','/home','Not A Platform!','','','2.4.1'),
		 ('c1',2,'2026-09-10T12:00:00Z','2026-09-10T12:00:00Z','cli','i3','install','deploy','Linux','','','')`,
		`INSERT INTO events (id, project_id, event_name, actor_id, ts, os) VALUES
		 ('e1',2,'signup','i1','2026-09-10T11:00:00Z','iOS'),
		 ('e2',2,'signup','i2','2026-09-10T11:00:00Z',''),
		 ('e3',2,'signup','i3','2026-09-10T11:00:00Z','Haiku')`,
		`INSERT INTO agg_views_daily VALUES (1,'2026-09-01','web',10,25,12,3,600), (2,'2026-09-01','app',6,20,8,0,480), (2,'2026-09-01','cli',2,4,2,0,0)`,
		`INSERT INTO agg_views_os VALUES (1,'2026-09-01','iOS','17.4',3,6), (1,'2026-09-01','','',2,2), (1,'2026-09-01','Haiku','',1,1)`,
		`INSERT INTO agg_views_browsers VALUES (1,'2026-09-01','Chrome','126',7,14), (1,'2026-09-01','Samsung Internet','25',1,1), (1,'2026-09-01','','',2,2)`,
		`INSERT INTO agg_views_devices VALUES (1,'2026-09-01','desktop','',7,13), (1,'2026-09-01','','iPhone15,3',5,12)`,
		`INSERT INTO agg_views_app_versions VALUES
		 (2,'2026-09-01','iOS','2.4.1',5,12), (2,'2026-09-01','Android','2.4.1',4,9),
		 (2,'2026-09-01','Haiku','2.4.1',1,1), (2,'2026-09-01','Beta OS','2.4.1',1,1), (2,'2026-09-01','Not/OS','2.4.1',1,1)`,
		`INSERT INTO agg_product_attrs VALUES
		 (2,'2026-09-01','signup','$os','iOS',3,3), (2,'2026-09-01','signup','$os','Haiku',1,1), (2,'2026-09-01','signup','plan','Pro',2,2)`,
	} {
		if _, err := db.db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return db
}

func TestMigration015FoldsAndBackfills(t *testing.T) {
	db := seed014(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migration 015: %v", err)
	}
	row := func(q string, dst ...any) {
		t.Helper()
		if err := db.db.QueryRowContext(ctx, q).Scan(dst...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var s, s2 string
	var n, m int

	// 1. platform backfill: web is web, app inverts 012's fold, anything
	//    else (and a token outside the pattern) is unknown.
	for id, want := range map[string]string{"w1": "web", "w2": "web", "w3": "web", "a1": "ios", "a2": "unknown", "c1": "unknown"} {
		row(`SELECT platform FROM views WHERE id='`+id+`'`, &s)
		if s != want {
			t.Errorf("views %s platform = %q, want %q", id, s, want)
		}
	}
	// 2. events: no backfill, the default stands.
	row(`SELECT COUNT(*) FROM events WHERE platform <> 'unknown'`, &n)
	if n != 0 {
		t.Errorf("%d events rows have a backfilled platform; expected none", n)
	}
	// 3. OS fold on raw rows: lower-cased, '' -> unknown, unlisted -> other
	//    with the original preserved in os_name (and only then).
	for id, want := range map[string][2]string{
		"w1": {"windows", ""}, "w2": {"other", "Haiku"}, "w3": {"unknown", ""},
		"a1": {"ios", ""}, "a2": {"other", "Not A Platform!"}, "c1": {"linux", ""},
	} {
		row(`SELECT os, os_name FROM views WHERE id='`+id+`'`, &s, &s2)
		if s != want[0] || s2 != want[1] {
			t.Errorf("views %s os = (%q, %q), want %v", id, s, s2, want)
		}
	}
	for id, want := range map[string]string{"e1": "ios", "e2": "unknown", "e3": "other"} {
		row(`SELECT os FROM events WHERE id='`+id+`'`, &s)
		if s != want {
			t.Errorf("events %s os = %q, want %q", id, s, want)
		}
	}
	// 4. OS fold on aggregate history: canonical lower-cased, '' relabelled,
	//    the out-of-vocabulary tail left exactly as it was, no count moved.
	row(`SELECT visitors FROM agg_views_os WHERE os='ios' AND os_version='17.4'`, &n)
	if n != 3 {
		t.Errorf("agg_views_os ios/17.4 visitors = %d, want 3", n)
	}
	row(`SELECT visitors FROM agg_views_os WHERE os='unknown' AND os_version=''`, &n)
	if n != 2 {
		t.Errorf("agg_views_os unknown visitors = %d, want 2", n)
	}
	row(`SELECT COUNT(*) FROM agg_views_os WHERE os='Haiku'`, &n)
	if n != 1 {
		t.Errorf("agg_views_os Haiku rows = %d, want 1 (left unfolded)", n)
	}
	row(`SELECT COUNT(*) FROM agg_views_os WHERE os IN ('iOS','','other')`, &n)
	if n != 0 {
		t.Errorf("agg_views_os still has %d unfolded or over-folded rows", n)
	}
	row(`SELECT count FROM agg_product_attrs WHERE attr_key='$os' AND attr_value='ios'`, &n)
	row(`SELECT count FROM agg_product_attrs WHERE attr_key='$os' AND attr_value='Haiku'`, &m)
	if n != 3 || m != 1 {
		t.Errorf("agg_product_attrs $os = ios %d, Haiku %d; want 3, 1", n, m)
	}
	row(`SELECT attr_value FROM agg_product_attrs WHERE attr_key='plan'`, &s)
	if s != "Pro" {
		t.Errorf("a custom attribute value was case-folded: %q", s)
	}
	// 5. browser and device fold: loss-free, raw and aggregate alike.
	for id, want := range map[string][2]string{
		"w1": {"chrome", "desktop"}, "w2": {"samsung_internet", "mobile"}, "w3": {"unknown", "unknown"}, "a1": {"unknown", "unknown"},
	} {
		row(`SELECT browser, device FROM views WHERE id='`+id+`'`, &s, &s2)
		if s != want[0] || s2 != want[1] {
			t.Errorf("views %s browser/device = (%q, %q), want %v", id, s, s2, want)
		}
	}
	for _, c := range []struct {
		q    string
		want int
	}{
		{`SELECT visitors FROM agg_views_browsers WHERE browser='chrome' AND browser_version='126'`, 7},
		{`SELECT visitors FROM agg_views_browsers WHERE browser='samsung_internet' AND browser_version='25'`, 1},
		{`SELECT visitors FROM agg_views_browsers WHERE browser='unknown' AND browser_version=''`, 2},
		{`SELECT visitors FROM agg_views_devices WHERE device='desktop' AND device_model=''`, 7},
		{`SELECT visitors FROM agg_views_devices WHERE device='unknown' AND device_model='iPhone15,3'`, 5},
		{`SELECT COUNT(*) FROM agg_views_browsers`, 3},
		{`SELECT COUNT(*) FROM agg_views_devices`, 2},
	} {
		row(c.q, &n)
		if n != c.want {
			t.Errorf("%s = %d, want %d", c.q, n, c.want)
		}
	}
	// 6. agg_views_platforms seeded from agg_views_daily: web exact, every
	//    other kind summed into unknown.
	row(`SELECT visitors, views FROM agg_views_platforms WHERE project_id=1 AND day='2026-09-01' AND platform='web'`, &n, &m)
	if n != 10 || m != 25 {
		t.Errorf("platforms web = (%d,%d), want (10,25)", n, m)
	}
	row(`SELECT visitors, views FROM agg_views_platforms WHERE project_id=2 AND day='2026-09-01' AND platform='unknown'`, &n, &m)
	if n != 8 || m != 24 {
		t.Errorf("platforms unknown = (%d,%d), want (8,24): app and cli summed", n, m)
	}
	row(`SELECT COUNT(*) FROM agg_views_platforms`, &n)
	if n != 2 {
		t.Errorf("agg_views_platforms rows = %d, want 2", n)
	}
	// 7. app_versions rekey: canonical values lower-cased and unmerged, a
	//    pattern-conforming unlisted value kept as its own platform, only
	//    values outside the pattern merge into unknown.
	if hasColumn(t, db, "agg_views_app_versions", "os") || !hasColumn(t, db, "agg_views_app_versions", "platform") {
		t.Fatal("agg_views_app_versions was not rekeyed to platform")
	}
	for _, c := range []struct {
		platform string
		v, p     int
	}{{"ios", 5, 12}, {"android", 4, 9}, {"haiku", 1, 1}, {"unknown", 2, 2}} {
		row(`SELECT visitors, views FROM agg_views_app_versions WHERE platform='`+c.platform+`' AND app_version='2.4.1'`, &n, &m)
		if n != c.v || m != c.p {
			t.Errorf("app_versions %s = (%d,%d), want (%d,%d)", c.platform, n, m, c.v, c.p)
		}
	}
	row(`SELECT COUNT(*) FROM agg_views_app_versions`, &n)
	if n != 4 {
		t.Errorf("agg_views_app_versions rows = %d, want 4", n)
	}
	// 8. views: platforms created, app_versions rekeyed, product attrs
	//    gains $platform. The live halves see the folded raw rows.
	row(`SELECT visitors, views FROM v_views_platforms WHERE project_id=1 AND day='2026-09-10' AND platform='web'`, &n, &m)
	if n != 3 || m != 3 {
		t.Errorf("v_views_platforms live web = (%d,%d), want (3,3)", n, m)
	}
	row(`SELECT visitors FROM v_views_app_versions WHERE project_id=2 AND day='2026-09-10' AND platform='ios' AND app_version='2.4.1'`, &n)
	if n != 1 {
		t.Errorf("v_views_app_versions live ios = %d, want 1", n)
	}
	row(`SELECT count FROM v_product_attrs WHERE project_id=2 AND day='2026-09-10' AND attr_key='$platform' AND attr_value='unknown'`, &n)
	if n != 3 {
		t.Errorf("v_product_attrs $platform unknown = %d, want 3", n)
	}
	var version int
	row(`SELECT MAX(version) FROM schema_migrations`, &version)
	if version != 15 {
		t.Errorf("schema version = %d, want 15", version)
	}
}

// Lower-casing aggregate history is only safe because no two spellings of
// one canonical value can share a key. If they do, the migration must
// abort inside its transaction and leave the database at 014, rather than
// silently dropping or summing one of them. docs/deployment.md tells the
// operator how to find such rows before upgrading.
func TestMigration015AbortsOnCaseCollision(t *testing.T) {
	db := seed014(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO agg_views_os VALUES (1,'2026-09-01','ios','17.4',1,1)`); err != nil {
		t.Fatal(err)
	}
	err := db.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("Migrate = %v, want a constraint failure", err)
	}
	var version int
	if err := db.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 14 {
		t.Errorf("schema version after the aborted migration = %d, want 14", version)
	}
	if hasColumn(t, db, "views", "platform") {
		t.Error("views.platform exists after an aborted 015: the migration did not run in one transaction")
	}
}
```

`hasColumn` and `newTestDBAt` already exist in `sqlite_test.go` and `migration012_test.go`.

- [ ] **Step 2: Run it to see it fail**

Run: `cd internal/store/sqlite && go test -run 'Migration015' ./`
Expected: FAIL — `no such column: platform` (015 does not exist yet, `Migrate` stops at 14).

- [ ] **Step 3: Write the migration**

Create `internal/store/sqlite/migrations/015_environment.sql`. The vocabulary list appears five times; it is the exact `OSValues` from Task 1.

```sql
-- 015: the client declares its environment.
-- Spec: docs/superpowers/specs/2026-09-20-os-and-platform-design.md
--
-- Adds platform (views, events) and os_name (views), closes os, browser
-- and device to lower-case vocabularies, gives platform its own aggregate
-- and view, and rekeys agg_views_app_versions from (os, app_version) to
-- (platform, app_version). Statements are numbered as in the spec and the
-- order is load-bearing: the platform backfill (1) reads views.os and
-- must run before the OS fold (3) rewrites it. This is the only time
-- platform is ever derived from os — 012 folded app_views.platform into
-- os, and this inverts that fold. From here on os is a genuine OS and
-- says nothing about platform (macos could be web or electron).
--
-- Irreversible. The OS fold copies the original name into os_name for
-- raw rows, but aggregate history has no such column, so its
-- out-of-vocabulary tail is left unfolded rather than merged (4).

-- Only these views are touched. They are dropped first so the
-- app_versions rebuild below can rename its table: with a view still
-- naming the dropped table, ALTER TABLE ... RENAME fails.
DROP VIEW IF EXISTS v_views_app_versions;
DROP VIEW IF EXISTS v_product_attrs;

ALTER TABLE views  ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE events ADD COLUMN platform TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE views  ADD COLUMN os_name  TEXT NOT NULL DEFAULT '';

-- 1. views platform backfill, faithful: web is web; app inverts 012's
--    fold; every other kind keeps the default. A derived token outside
--    ^[a-z][a-z0-9_]{0,15}$ is unknown.
UPDATE views SET platform = 'web' WHERE kind = 'web';
UPDATE views SET platform = lower(os)
WHERE kind = 'app'
  AND lower(os) GLOB '[a-z]*'
  AND lower(os) NOT GLOB '*[^a-z0-9_]*'
  AND length(os) <= 16;

-- 2. events platform backfill: none. events has no kind column, so
--    lower(os) would label a web SDK's custom events macos rather than
--    web. Every row keeps the column default, unknown.

-- 3. OS fold, raw rows. The original is preserved first, so other stays
--    investigable; then lower-case, '' -> unknown, unlisted -> other.
UPDATE views SET os_name = os
WHERE os <> '' AND lower(os) NOT IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown');
UPDATE views SET os = CASE
  WHEN os = '' THEN 'unknown'
  WHEN lower(os) IN (
    'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
    'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown')
    THEN lower(os)
  ELSE 'other' END;
UPDATE events SET os = CASE
  WHEN os = '' THEN 'unknown'
  WHEN lower(os) IN (
    'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
    'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown')
    THEN lower(os)
  ELSE 'other' END;

-- 4. OS fold, aggregate history: only what merges nothing. Canonical
--    values are lower-cased (injective over the vocabulary) and '' is
--    relabelled to unknown (a value that did not exist before). An
--    unlisted value is left exactly as it was: folding it into other
--    would SUM visitors that were counted as distinct actors. Two
--    spellings of one canonical value on one key would collide here and
--    abort the migration; docs/deployment.md lists the query that finds
--    them beforehand.
UPDATE agg_views_os SET os = CASE WHEN os = '' THEN 'unknown' ELSE lower(os) END
WHERE os = '' OR lower(os) IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown');
UPDATE agg_product_attrs SET attr_value = CASE WHEN attr_value = '' THEN 'unknown' ELSE lower(attr_value) END
WHERE attr_key = '$os' AND (attr_value = '' OR lower(attr_value) IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown'));

-- 5. Browser and device fold: loss-free. No client could write these
--    columns before 015, so every value came from ParseUserAgent — six
--    browser names, three device classes, or '' — and all nine are in
--    the new vocabularies. The fold is injective and merges nothing. The
--    '' rows are the app and cli rows kind gating never enriched.
UPDATE views SET browser = CASE
  WHEN browser = '' THEN 'unknown'
  WHEN browser = 'Samsung Internet' THEN 'samsung_internet'
  ELSE lower(browser) END;
UPDATE views SET device = CASE WHEN device = '' THEN 'unknown' ELSE lower(device) END;
UPDATE agg_views_browsers SET browser = CASE
  WHEN browser = '' THEN 'unknown'
  WHEN browser = 'Samsung Internet' THEN 'samsung_internet'
  ELSE lower(browser) END;
UPDATE agg_views_devices SET device = CASE WHEN device = '' THEN 'unknown' ELSE lower(device) END;

-- 6. agg_views_platforms, modelled on agg_views_countries and seeded from
--    agg_views_daily: web is exact; every other kind becomes unknown, so
--    the platform totals stay consistent with the daily totals. views
--    are additive; visitors overcount only where two non-web kinds shared
--    one pre-015 day, and only in that row.
CREATE TABLE agg_views_platforms (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, platform TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, platform)
) WITHOUT ROWID;
INSERT INTO agg_views_platforms (project_id, day, platform, visitors, views)
SELECT project_id, day, CASE WHEN kind = 'web' THEN 'web' ELSE 'unknown' END,
       SUM(visitors), SUM(views)
FROM agg_views_daily
GROUP BY project_id, day, CASE WHEN kind = 'web' THEN 'web' ELSE 'unknown' END;

-- 7. agg_views_app_versions rekey: (os, app_version) -> (platform,
--    app_version), platform = lower(os), or unknown outside the platform
--    pattern. Value-preserving for canonical os; only values outside the
--    pattern merge, and only into unknown.
CREATE TABLE agg_views_app_versions_new (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, platform TEXT NOT NULL, app_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, platform, app_version)
) WITHOUT ROWID;
INSERT INTO agg_views_app_versions_new (project_id, day, platform, app_version, visitors, views)
SELECT project_id, day,
       CASE WHEN lower(os) GLOB '[a-z]*' AND lower(os) NOT GLOB '*[^a-z0-9_]*' AND length(os) <= 16
            THEN lower(os) ELSE 'unknown' END,
       app_version, SUM(visitors), SUM(views)
FROM agg_views_app_versions
GROUP BY 1, 2, 3, 4;
DROP TABLE agg_views_app_versions;
ALTER TABLE agg_views_app_versions_new RENAME TO agg_views_app_versions;

-- 8. Views. v_views_platforms copies the v_views_countries shape (the
--    single-key dimension, including the 500-value cap); v_views_countries
--    itself is the template, not a target. v_views_app_versions is rekeyed.
--    v_product_attrs gains a $platform arm beside $os and $app_version.
CREATE VIEW v_views_platforms AS
SELECT project_id, day, platform, visitors, views FROM agg_views_platforms
UNION ALL
SELECT project_id, day, CASE WHEN rn <= 500 THEN platform ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.platform, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, platform,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, platform) AS rn
    FROM views GROUP BY project_id, day, platform
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.platform = v.platform
)
GROUP BY project_id, day, CASE WHEN rn <= 500 THEN platform ELSE '(other)' END;

CREATE VIEW v_views_app_versions AS
SELECT project_id, day, platform, app_version, visitors, views FROM agg_views_app_versions
UNION ALL
SELECT project_id, day, platform, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project_id, v.day, v.platform, v.app_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project_id, day, platform, app_version,
           ROW_NUMBER() OVER (PARTITION BY project_id, day ORDER BY COUNT(*) DESC, platform, app_version) AS rn
    FROM views WHERE app_version <> '' GROUP BY project_id, day, platform, app_version
  ) r ON r.project_id = v.project_id AND r.day = v.day AND r.platform = v.platform AND r.app_version = v.app_version
  WHERE v.app_version <> ''
)
GROUP BY project_id, day, platform, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END;

CREATE VIEW v_product_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta
                   WHERE key='product_attributes_top_n'
                     AND CAST(value AS INTEGER) > 0), 50) AS n
),
declared AS (
  SELECT DISTINCT p.id AS project_id, j.value AS attr_key
  FROM projects p,
       json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
  WHERE j.type = 'text'
),
vals AS (
  SELECT e.project_id AS project_id, substr(e.ts,1,10) AS day,
         e.event_name AS event_name, d.attr_key AS attr_key,
         json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') AS attr_value,
         e.actor_id AS actor_id
  FROM events e
  JOIN declared d ON d.project_id = e.project_id
  WHERE json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') IS NOT NULL
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$os', os, actor_id
  FROM events WHERE os <> ''
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$platform', platform, actor_id
  FROM events WHERE platform <> ''
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$app_version', app_version, actor_id
  FROM events WHERE app_version <> ''
),
counted AS (
  SELECT project_id, day, event_name, attr_key, attr_value,
         COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u
  FROM vals
  GROUP BY project_id, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT project_id, day, event_name, attr_key, attr_value, c, u,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, attr_key
                            ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT project_id, day, event_name, attr_key, attr_value, count, unique_users
FROM agg_product_attrs
UNION ALL
SELECT project_id, day, event_name, attr_key, attr_value, c, u
FROM ranked
WHERE rn <= (SELECT n FROM cap)
UNION ALL
SELECT v.project_id, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id)
FROM vals v
WHERE NOT EXISTS (
  SELECT 1 FROM ranked r
  WHERE r.project_id = v.project_id AND r.day = v.day
    AND r.event_name = v.event_name AND r.attr_key = v.attr_key
    AND r.attr_value = v.attr_value
    AND r.rn <= (SELECT n FROM cap))
GROUP BY v.project_id, v.day, v.event_name, v.attr_key;
```

The `v_product_attrs` text is the 014 definition plus the `$platform` arm — copy the rest byte-for-byte from `014_project_ids.sql` rather than retyping it.

- [ ] **Step 4: Run the migration tests**

Run: `cd internal/store/sqlite && go test -run 'Migration015' ./`
Expected: PASS (both).

- [ ] **Step 5: Extend the table lists and the view list**

`internal/store/sqlite/prune.go` — add `"agg_views_platforms"` after `"agg_views_countries"` in `viewsAggTables`.

`internal/store/sqlite/registry.go` — add `"agg_views_platforms"` after `"agg_views_countries"` in `projectTables`.

`internal/store/sqlite/sqlite_test.go` `TestMigrationViews` — add `"v_views_platforms"` after `"v_views_countries"`.

Run: `cd internal/store/sqlite && go test -run 'ProjectTables|PruneAggregatesCovers|MigrationViews' ./`
Expected: PASS.

- [ ] **Step 6: Store types and writes**

`internal/store/store.go` — `View` gains `Platform, OSName` and `ProductEvent` gains `Platform`:

```go
// View is one page or screen shown to someone. Platform is the surface the
// product is used through (web, ios, electron, …); OS is the operating
// system it runs on. They coincide for a native app and diverge everywhere
// else. Both are never empty in the database — the server stores unknown
// for an undeclared value — and OSName is the free-form name the client
// reported, kept beside an OS of other so the bucket stays investigable.
type View struct {
	ID                                             string
	ProjectID                                      int64
	TS, ReceivedAt                                 time.Time
	Kind                                           string
	ActorID, ActorKind, UserID, GroupID, SessionID string
	Host, Path, ReferrerSource                     string
	UTMSource, UTMMedium, UTMCampaign              string
	Platform, OS, OSVersion, OSName                string
	Browser, BrowserVersion                        string
	AppVersion, Device, DeviceModel, Locale        string
	DisplayWidth, DisplayHeight                    int
	Country                                        string
}

// ProductEvent represents a custom event from any surface. Platform and OS
// are the two environment columns a product event carries; every other
// declared environment key is resolved and dropped at ingest.
type ProductEvent struct {
	ID                 string
	ProjectID          int64
	EventName          string
	TS, ReceivedAt     time.Time
	ActorID, ActorKind string
	UserID, GroupID    string
	Platform, OS       string
	AppVersion         string
	Attributes         map[string]string
}
```

`internal/store/sqlite/write.go` — `WriteViews` inserts the two new columns:

```go
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO views
			(id, project_id, ts, received_at, kind, actor_id, actor_kind, user_id, group_id, session_id,
			 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
			 platform, os, os_version, os_name, browser, browser_version, app_version,
			 device, device_model, locale, display_width, display_height, country)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
```

with the matching `ExecContext` arguments `… v.UTMCampaign, v.Platform, v.OS, v.OSVersion, v.OSName, v.Browser, v.BrowserVersion, v.AppVersion, v.Device, …` (29 placeholders). `WriteProductEvents`:

```go
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO events
			(id, project_id, event_name, ts, received_at, actor_id, actor_kind, user_id, group_id,
			 platform, os, app_version, attributes)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
```

with `… e.UserID, e.GroupID, e.Platform, e.OS, e.AppVersion, string(blob)`.

- [ ] **Step 7: Write tests for the round trip**

In `internal/store/sqlite/write_test.go`, `TestWriteViewsAppRoundTrip`: add `Platform: "ios", OSName: "iOS 17.2"` to the `View` literal and change `OS: "iOS"` to `OS: "ios"`. Extend the read-back:

```go
	var path, osCol, platform, osName, group, session, locale string
	if err := db.db.QueryRowContext(ctx,
		`SELECT path, os, platform, os_name, group_id, session_id, locale FROM views WHERE id=?`, "018f-a").
		Scan(&path, &osCol, &platform, &osName, &group, &session, &locale); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if path != "/settings" || osCol != "ios" || platform != "ios" || osName != "iOS 17.2" || group != "org9" ||
		session != "s1" || locale != "en-US" {
		t.Errorf("got %q %q %q %q %q %q %q", path, osCol, platform, osName, group, session, locale)
	}
```

In the product-event round trip (the test around `write_test.go:195-222` that writes event `e`): add `Platform: "electron"` to the `ProductEvent`, change `OS: "iOS"` to `OS: "macos"`, and read back `SELECT os, platform, app_version FROM events WHERE id='e'` asserting `"macos"`, `"electron"`, `"2.4.1"`.

Run: `cd internal/store/sqlite && go test -run 'WriteViewsAppRoundTrip|WriteProduct|RoundTrip' ./`
Expected: PASS.

- [ ] **Step 8: Aggregator dimensions**

`internal/store/sqlite/aggregate_views.go` `viewDimensions`:

```go
	{table: "agg_views_countries", keys: []string{"country"}},
	{table: "agg_views_platforms", keys: []string{"platform"}},
	{table: "agg_views_os", keys: []string{"os", "os_version"}},
	{table: "agg_views_browsers", keys: []string{"browser", "browser_version"}},
	// Keyed on platform, not os: 2.4.1 means unrelated things across the
	// iOS and Android builds of one product, and a web build has no os
	// of its own to key on.
	{table: "agg_views_app_versions", keys: []string{"platform", "app_version"}, where: "AND app_version <> ''"},
```

`internal/store/sqlite/aggregate_product.go` `systemDims`:

```go
var systemDims = []struct{ column, key string }{
	{"platform", "$platform"},
	{"os", "$os"},
	{"app_version", "$app_version"},
}
```

Also update the two comments above `systemDims` and in `rollupProduct` that say "os and app_version are typed columns" to "platform, os and app_version are typed columns".

- [ ] **Step 9: Update the view fixtures and assertions**

`internal/store/sqlite/aggregate_views_test.go`:

`seedViewDay` — fixtures use the new vocabularies and set `Platform`:

```go
	web := func(id, actor, path string, ts time.Time) store.View {
		return store.View{ID: id, TS: ts, ActorID: actor, Kind: "web", Platform: "web",
			Host: "shop.example.com", Path: path, Country: "US",
			Device: "desktop", Browser: "firefox", BrowserVersion: "127", OS: "linux"}
	}
	v2 := web("4", "v2", "/a", at(11, 0))
	v2.Country, v2.Device, v2.Browser, v2.BrowserVersion, v2.OS = "DE", "mobile", "chrome", "126", "android"
	…
	app := func(id, actor, path, session, os, osv, model, country string, ts time.Time) store.View {
		return store.View{ID: id, TS: ts, ActorID: actor, ActorKind: store.ActorInstall, Kind: "app",
			Platform: os, SessionID: session, Path: path, OS: os, OSVersion: osv, AppVersion: "2.4.1",
			Device: "unknown", Browser: "unknown",
			DeviceModel: model, Locale: "en-US", Country: country}
	}
	seedViews(t, db,
		web("1", "v1", "/a", at(10, 0)), web("2", "v1", "/b", at(10, 10)), web("3", "v1", "/a", at(12, 0)), v2,
		app("5", "i1", "/home", "s1", "ios", "17.2", "iPhone15,2", "DE", at(10, 0)),
		app("6", "i1", "/settings", "s1", "ios", "17.2", "iPhone15,2", "DE", at(10, 5)),
		app("7", "i2", "/home", "s2", "android", "14", "Pixel 8", "FR", at(11, 0)),
	)
```

Update the doc comment above it (`iOS` → `ios`, `Chrome` → `chrome`, etc., and note `platform=web` for web rows, `platform=os` for app rows).

`TestAggregateViewDayDimensions` — replace the os/browser/app_versions/devices checks:

```go
	check(`SELECT visitors, views FROM agg_views_platforms`+w+`platform=?`, []any{"web"}, 2, 4)
	check(`SELECT visitors, views FROM agg_views_platforms`+w+`platform=?`, []any{"ios"}, 1, 2)
	check(`SELECT visitors, views FROM agg_views_os`+w+`os=? AND os_version=?`, []any{"ios", "17.2"}, 1, 2)
	check(`SELECT visitors, views FROM agg_views_os`+w+`os=? AND os_version=?`, []any{"linux", ""}, 1, 3)
	check(`SELECT visitors, views FROM agg_views_browsers`+w+`browser=? AND browser_version=?`, []any{"chrome", "126"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_app_versions`+w+`platform=? AND app_version=?`, []any{"ios", "2.4.1"}, 1, 2)
	check(`SELECT visitors, views FROM agg_views_app_versions`+w+`platform=? AND app_version=?`, []any{"android", "2.4.1"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_devices`+w+`device=? AND device_model=?`, []any{"unknown", "Pixel 8"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_devices`+w+`device=? AND device_model=?`, []any{"desktop", ""}, 1, 3)
```

`internal/store/sqlite/views_test.go`:

`TestStitchViewsInvariantAllViewsDimensions` — the `extra` rows get `Platform: "web", OS: "linux", Browser: "firefox", Device: "desktop"` (lower-case), and the `dims` table gains a row and rekeys one:

```go
		{"v_views_countries", "country"},
		{"v_views_platforms", "platform"},
		{"v_views_os", "os || '|' || os_version"},
		{"v_views_browsers", "browser || '|' || browser_version"},
		{"v_views_app_versions", "platform || '|' || app_version"},
```

`TestProductAttrsViewSystemDimensionsWithoutDeclaredKeys` — the `switch r.Key` case becomes `case "$os", "$platform", "$app_version":`.

Grep the rest of the package for `"iOS"`, `"Linux"`, `"Chrome"`, `"Android"`, `"Firefox"` in fixtures (`retention_test.go:18`, `views_test.go` extras, any other) and lower-case them; none of those tests assert on the value, but the fixtures should speak the vocabulary.

- [ ] **Step 10: New tests — the platform dimension across the boundary, and the product `$platform` rollup**

Append to `internal/store/sqlite/views_test.go`:

```go
// v_views_platforms must agree with agg_views_platforms across the
// aggregate ∪ live boundary, including the (other) cap on a day with more
// than 500 distinct platforms — the shape it copies from countries.
func TestStitchViewPlatformsAcrossBoundaryWithCap(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	var extra []store.View
	for i := 0; i < topNDimension+5; i++ {
		extra = append(extra, store.View{ID: fmt.Sprintf("p-%d", i), TS: at(13, 0).Add(time.Duration(i) * time.Second),
			ActorID: "v3", Path: "/x", Platform: fmt.Sprintf("p%d", i), OS: "linux", Browser: "firefox", Device: "desktop"})
	}
	seedViews(t, db, extra...)
	snapshot := func() map[string][2]int {
		t.Helper()
		rows, err := db.db.Query(`SELECT platform, visitors, views FROM v_views_platforms WHERE project_id=1 AND day='2026-08-10'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][2]int{}
		for rows.Next() {
			var k string
			var v, pv int
			if err := rows.Scan(&k, &v, &pv); err != nil {
				t.Fatal(err)
			}
			out[k] = [2]int{v, pv}
		}
		return out
	}
	before := snapshot()
	if before["web"] != [2]int{2, 4} || before["ios"] != [2]int{1, 2} || before["android"] != [2]int{1, 1} {
		t.Fatalf("live half = %v", before)
	}
	if _, ok := before[otherBucket]; !ok {
		t.Fatal("platform fixture did not exceed the cap")
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); !reflect.DeepEqual(after, before) {
		t.Errorf("v_views_platforms changed across the boundary:\nbefore %v\nafter  %v", before, after)
	}
}
```

In `internal/store/sqlite/aggregate_product_test.go`, extend `seedProductEvent` to set `Platform` from a new trailing parameter is more churn than it is worth; instead add a new test:

```go
// $platform rolls up beside $os and $app_version without being declared.
func TestRollupWritesPlatformSystemDimension(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{{
		ID: uuid.NewString(), ProjectID: 1, EventName: "signup", ActorID: "u1",
		TS: ts("2026-08-01T10:00:00Z"), Platform: "electron", OS: "macos", AppVersion: "1.2.0",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateProductDay(ctx, 1, civil.DateOf(ts("2026-08-01T00:00:00Z")), nil, 50); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := db.db.QueryRow(`SELECT attr_value FROM agg_product_attrs
		WHERE project_id=1 AND attr_key='$platform'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "electron" {
		t.Fatalf("$platform = %q, want electron", v)
	}
}
```

- [ ] **Step 11: Run the targeted tests**

Run: `cd internal/store/sqlite && go test -run 'Aggregate|Stitch|ProductAttrs|Rollup|Write|Migration|ProjectTables|Prune' ./`
Expected: PASS.

- [ ] **Step 12: Run the whole store tree**

Run (long): `go test -timeout 900s ./internal/store/... 2>&1 | tail -5`
Expected: `ok` for both packages.

- [ ] **Step 13: Commit**

```bash
git add internal/store internal/store/sqlite
git commit -m "feat(store)!: add platform and os_name, close the os, browser and device vocabularies, and key app versions by platform"
```

---

### Task 3: `internal/server` — validate the declared environment, never parse

Implements spec "Wire contract" (server side), "Detection leaves the server entirely", decisions 2, 5, 10, 13, and the "Reporting" rows for `ingest.go` and `handlers.go`. Restores `go build ./...`. Includes the `docs/twillingate.md` paragraphs that describe the ingest contract, because `internal/api`'s `TestDocumentMatchesReservedKeys` binds the reserved-key table to `ingest.go` and would fail otherwise.

**Files:**
- Modify: `internal/server/ingest.go:98-186` (`resolved`, `reservedKeys`, `resolveAttributes`; add `normalizePlatform`, `ingestResult.declared`)
- Modify: `internal/server/handlers.go:117-190` (the per-event loop)
- Modify: `internal/server/ingest_test.go` (drop the two alias tests, extend the split test, add `TestNormalizePlatform`)
- Modify: `internal/server/server_test.go` (`TestRoutesViewsAndCustom`, `TestLegacyNamesAreSilentAliases`, `TestKindDeclaredValidatedAndDefaulted`; add three tests)
- Modify: `internal/api/docs_sync_test.go:62-66, 83-85` (delete `aliasKeys`)
- Modify: `docs/twillingate.md` — lines 30-32, 193-194, 486-497, 588-620 (envelope), 652-667 (reserved keys and the sentence after)

**Interfaces:**
- Consumes: `enrich.NormalizeOS/NormalizeBrowser/NormalizeDevice (string) (string, bool)` from Task 1; `store.View.Platform/OSName`, `store.ProductEvent.Platform` from Task 2.
- Produces: reserved keys `$platform`, `$os_name`, `$browser`, `$browser_version`, `$device`; warning texts `$os "iOS 17" is not a known value, stored as other` and `$platform "Bad!" is not a known value, stored as unknown`.

- [ ] **Step 1: Unit tests for attribute resolution**

In `internal/server/ingest_test.go`: delete `TestResolveAttributesPlatformIsAnOSAlias` and `TestResolveAttributesCanonicalOSBeatsAlias` outright. In `TestResolveAttributesSplitsReservedFromCustom` add the new keys to the input map — `"$platform": "iOS", "$os_name": "iOS 17.2", "$browser": "Safari", "$browser_version": "17", "$device": "mobile"` — and assert them:

```go
	if r.Platform != "iOS" || r.OSName != "iOS 17.2" || r.Browser != "Safari" || r.BrowserVersion != "17" || r.Device != "mobile" {
		t.Errorf("declared environment = %+v", r)
	}
```

(`resolveAttributes` stores what was sent; validation happens in the handler.) Then add:

```go
// $platform is open but bounded: the same shape as $kind, applied after
// lower-casing so a client sending iOS records ios rather than being
// dropped. There is no other: every well-formed token is a value, so the
// only failure is "no usable value", which unknown says.
func TestNormalizePlatform(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"web": {"web", true}, "iOS": {"ios", true}, " Electron ": {"electron", true},
		"quest_2": {"quest_2", true}, "unknown": {"unknown", true},
		"": {"unknown", true},
		"Bad Platform!": {"unknown", false}, "9lives": {"unknown", false},
		"averyveryverylongplatformname": {"unknown", false},
	}
	for in, c := range cases {
		got, ok := normalizePlatform(in)
		if got != c.want || ok != c.ok {
			t.Errorf("normalizePlatform(%q) = (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `cd internal/server && go test -run 'ResolveAttributes|NormalizePlatform' ./ 2>&1 | head`
Expected: compile errors (`r.Platform` undefined, `normalizePlatform` undefined).

- [ ] **Step 3: Rewrite the reserved half of `ingest.go`**

Replace the `resolved` type, `reservedKeys` and `resolveAttributes` in `internal/server/ingest.go` with:

```go
// resolved is the reserved half of an event's attributes split into typed
// fields, plus whatever ordinary attributes remain. Environment fields
// hold what the client sent; the handler validates them.
type resolved struct {
	InstallID, UserID, UserName    string
	GroupID, GroupName, SessionID  string
	Kind, Platform, OS, OSVersion  string
	OSName, AppVersion             string
	Browser, BrowserVersion        string
	Device, DeviceModel, Locale    string
	Host, Path, Referrer, Screen   string
	UTMSource, UTMMedium           string
	UTMCampaign                    string
	displayWidthRaw                string
	displayHeightRaw               string
	Custom                         map[string]string
}

// reservedKeys maps every system-defined attribute key to its destination.
// Location attributes are stored verbatim: the client owns normalization
// (masking, routing mode), so the server does no URL parsing at all. That
// is what lets a site report /account/[id]/edit without the raw path ever
// leaving the browser. $screen is a fallback for path, resolved in
// handleEvents when $path is absent. The environment keys are declared by
// the client and only validated here; the User-Agent is never a source
// for any of them.
var reservedKeys = map[string]func(*resolved, string){
	"$install_id":      func(r *resolved, v string) { r.InstallID = v },
	"$user_id":         func(r *resolved, v string) { r.UserID = v },
	"$user_name":       func(r *resolved, v string) { r.UserName = v },
	"$group_id":        func(r *resolved, v string) { r.GroupID = v },
	"$group_name":      func(r *resolved, v string) { r.GroupName = v },
	"$session_id":      func(r *resolved, v string) { r.SessionID = v },
	"$kind":            func(r *resolved, v string) { r.Kind = v },
	"$platform":        func(r *resolved, v string) { r.Platform = v },
	"$os":              func(r *resolved, v string) { r.OS = v },
	"$os_version":      func(r *resolved, v string) { r.OSVersion = v },
	"$os_name":         func(r *resolved, v string) { r.OSName = v },
	"$browser":         func(r *resolved, v string) { r.Browser = v },
	"$browser_version": func(r *resolved, v string) { r.BrowserVersion = v },
	"$device":          func(r *resolved, v string) { r.Device = v },
	"$app_version":     func(r *resolved, v string) { r.AppVersion = v },
	"$device_model":    func(r *resolved, v string) { r.DeviceModel = v },
	"$locale":          func(r *resolved, v string) { r.Locale = v },
	"$host":            func(r *resolved, v string) { r.Host = v },
	"$path":            func(r *resolved, v string) { r.Path = v },
	"$utm_source":      func(r *resolved, v string) { r.UTMSource = v },
	"$utm_medium":      func(r *resolved, v string) { r.UTMMedium = v },
	"$utm_campaign":    func(r *resolved, v string) { r.UTMCampaign = v },
	"$referrer":        func(r *resolved, v string) { r.Referrer = v },
	"$screen":          func(r *resolved, v string) { r.Screen = v },
	"$display_width":   func(r *resolved, v string) { r.displayWidthRaw = v },
	"$display_height":  func(r *resolved, v string) { r.displayHeightRaw = v },
}
```

In `resolveAttributes`, delete the trailing block (`// The canonical key wins over its alias …` through `r.OS = r.platformRaw }`) so the function ends with `return r, unknown`. Then add, after `parseDisplay`:

```go
// normalizePlatform validates a declared $platform: trim, lower-case, then
// the same shape as $kind. The vocabulary is open, so every well-formed
// token is accepted as sent; the only failure is "no usable value", which
// unknown says. Absent is unknown too, and is not a mistake to warn about.
func normalizePlatform(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "unknown", true
	}
	if kindPattern.MatchString(v) {
		return v, true
	}
	return "unknown", false
}

// declared runs one environment validator and warns when the value was
// present but unrecognised, so the mistake surfaces in the response body
// during integration instead of becoming a quiet other months later. A
// deliberate other or unknown is recognised by every validator, so a
// client that means it is never warned.
func (res *ingestResult) declared(i int, key, raw string, normalize func(string) (string, bool)) string {
	v, ok := normalize(raw)
	if !ok {
		res.warn(i, "%s %q is not a known value, stored as %s", key, raw, v)
	}
	return v
}
```

- [ ] **Step 4: Rewrite the per-event environment handling in `handlers.go`**

In `handleEvents`, replace everything from `defaultKind, isView := viewName(ev.Name)` down to (and including) the `// Declared environment overrides whatever was parsed.` block with:

```go
		// The environment is declared, validated and never parsed: the
		// User-Agent is read for nothing but the bot check below. $os and
		// $platform land on views and product events alike; the rest are
		// views-only and are resolved and dropped on a product event.
		osv, osKnown := enrich.NormalizeOS(rv.OS)
		if !osKnown {
			res.warn(i, "$os %q is not a known value, stored as other", rv.OS)
		}
		platform := res.declared(i, "$platform", rv.Platform, normalizePlatform)

		defaultKind, isView := viewName(ev.Name)
		if !isView {
			if strings.HasPrefix(ev.Name, "$") {
				res.warn(i, "unknown reserved name %s, stored as a custom event", ev.Name)
			}
			s.queue.EnqueueEvent(store.ProductEvent{
				ID: id, ProjectID: p.ID, EventName: ev.Name,
				TS: ts, ReceivedAt: received,
				ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group,
				Platform: platform, OS: osv, AppVersion: rv.AppVersion,
				Attributes: rv.Custom,
			})
			res.Accepted++
			continue
		}

		kind := defaultKind
		if rv.Kind != "" {
			if kindPattern.MatchString(rv.Kind) {
				kind = rv.Kind
			} else {
				res.warn(i, "invalid $kind %q, using %q", rv.Kind, defaultKind)
			}
		}
		path := rv.Path
		if path == "" {
			path = rv.Screen
		}
		if path == "" {
			res.reject(i, "view requires $path or $screen")
			continue
		}
		// An unrecognised $os becomes other; the name it would erase is
		// kept in os_name so the bucket stays investigable. An explicit
		// $os_name always wins.
		osName := rv.OSName
		if osName == "" && !osKnown {
			osName = rv.OS
		}
		v := store.View{
			ID: id, ProjectID: p.ID, TS: ts, ReceivedAt: received, Kind: kind,
			ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group, SessionID: rv.SessionID,
			Host: rv.Host, Path: path,
			UTMSource: rv.UTMSource, UTMMedium: rv.UTMMedium, UTMCampaign: rv.UTMCampaign,
			Platform: platform, OS: osv, OSVersion: rv.OSVersion, OSName: osName,
			Browser:        res.declared(i, "$browser", rv.Browser, enrich.NormalizeBrowser),
			BrowserVersion: rv.BrowserVersion,
			Device:         res.declared(i, "$device", rv.Device, enrich.NormalizeDevice),
			AppVersion: rv.AppVersion, DeviceModel: rv.DeviceModel, Locale: rv.Locale, Country: country,
		}
		// Bot filtering is the one thing still read off the User-Agent,
		// and it applies to web rows only: any other kind declares what
		// it is and is never filtered, whatever HTTP library it uses.
		if kind == "web" {
			if botUA {
				// Accepted and silently ignored: the client did nothing
				// wrong, so it must not retry.
				res.Accepted++
				continue
			}
			v.ReferrerSource = enrich.CleanReferrer(rv.Referrer, rv.Host)
		} else {
			// No host to compare against, so a referrer is taken at face
			// value — a deep link can still carry one.
			v.ReferrerSource = enrich.CleanReferrer(rv.Referrer, "")
		}
```

The `parseDisplay` block and `s.queue.EnqueueView(v)` that follow stay as they are.

- [ ] **Step 5: Build everything**

Run: `go build ./... && go vet ./internal/server/`
Expected: clean — `ParseUserAgent` has no caller left.

- [ ] **Step 6: Rewrite the server tests that asserted parsed values**

`internal/server/server_test.go`:

`TestRoutesViewsAndCustom` — the batch now declares `"$platform":"iOS"` alongside `$os` and `$app_version`, and the assertions become:

```go
	web, app := q.views[0], q.views[1]
	if web.Kind != "web" || web.Host != "app.com" || web.Path != "/pricing" || web.UTMSource != "hn" ||
		web.Country != "DE" || web.DisplayWidth != 1920 || web.DisplayHeight != 1080 || web.Locale != "de-DE" {
		t.Errorf("web view = %+v", web)
	}
	// The environment is declared, lower-cased and never parsed: a Chrome
	// User-Agent on this request names nothing.
	if web.Platform != "ios" || web.OS != "ios" || web.Browser != "unknown" || web.BrowserVersion != "" || web.Device != "unknown" {
		t.Errorf("web environment = platform %q os %q browser %q/%q device %q", web.Platform, web.OS, web.Browser, web.BrowserVersion, web.Device)
	}
	if app.Kind != "app" || app.Path != "/settings" || app.Platform != "ios" || app.OS != "ios" || app.OSVersion != "17.2" ||
		app.AppVersion != "2.4.1" || app.DeviceModel != "iPhone15,2" || app.Locale != "en-US" ||
		app.SessionID != "s1" || app.Country != "DE" || app.Browser != "unknown" || app.Device != "unknown" {
		t.Errorf("app view = %+v", app)
	}
	if len(q.events) != 1 || q.events[0].Platform != "ios" || q.events[0].OS != "ios" || q.events[0].AppVersion != "2.4.1" {
		t.Errorf("events = %+v", q.events)
	}
```

`TestLegacyNamesAreSilentAliases` — `$pageview` is still an alias; `$platform` no longer is. Rename to `TestPageviewNameIsASilentAlias` and assert the clean break:

```go
	if len(q.views) != 1 || q.views[0].Kind != "web" || q.views[0].Platform != "linux" || q.views[0].OS != "unknown" {
		t.Errorf("views = %+v ($platform must land on platform and never fill os)", q.views)
	}
```

`TestKindDeclaredValidatedAndDefaulted` — the final check becomes `q.views[0].Browser != "unknown"` with the message `(non-web kinds are never filtered; nothing is parsed on any kind)`.

Then add:

```go
// A web batch that declares nothing stores unknown for os, browser and
// device: the server no longer derives any of them from the User-Agent.
// The one thing it still reads the User-Agent for is the crawler drop.
func TestEnvironmentIsDeclaredNotParsed(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$page_view","attributes":{"$host":"app.com","$path":"/x"}}`), nil)
	if res := decodeResult(t, w); res.Accepted != 1 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v (an absent value is not a mistake to warn about)", res)
	}
	if len(q.views) != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	v := q.views[0]
	if v.Platform != "unknown" || v.OS != "unknown" || v.OSName != "" || v.Browser != "unknown" || v.BrowserVersion != "" || v.Device != "unknown" {
		t.Errorf("undeclared environment = %+v, want unknown everywhere and an empty os_name", v)
	}

	q, h = testServer(t)
	post(h, envelopeOf(`{"name":"$page_view","attributes":{"$host":"app.com","$path":"/x"}}`),
		map[string]string{"User-Agent": "Googlebot/2.1"})
	if len(q.views) != 0 {
		t.Errorf("a crawler User-Agent must still drop a web view: %+v", q.views)
	}
}

// $os closes to a lower-case vocabulary. Present but unrecognised is other
// with a warning and the raw value preserved in os_name; a deliberate
// other is not warned about; an explicit $os_name always wins.
func TestOSIsValidatedAndTheNamePreserved(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"$page_view","attributes":{"$path":"/","$os":"Chrome OS"}},
		{"name":"$page_view","attributes":{"$path":"/","$os":"Haiku R1"}},
		{"name":"$page_view","attributes":{"$path":"/","$os":"Haiku R1","$os_name":"Haiku R1 beta 5"}},
		{"name":"$page_view","attributes":{"$path":"/","$os":"other"}},
		{"name":"$page_view","attributes":{"$path":"/","$os":"macos","$os_name":"macOS 14.2"}},
		{"name":"signup","attributes":{"$os":"Haiku R1","$os_name":"dropped on product events"}}`)
	res := decodeResult(t, post(h, body, nil))
	if res.Accepted != 6 || res.Rejected != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Warnings) != 3 {
		t.Fatalf("warnings = %+v, want one per unrecognised $os and none for a deliberate other", res.Warnings)
	}
	if res.Warnings[0].Index != 1 || res.Warnings[0].Reason != `$os "Haiku R1" is not a known value, stored as other` {
		t.Errorf("warning = %+v", res.Warnings[0])
	}
	if res.Warnings[1].Index != 2 || res.Warnings[2].Index != 5 {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	want := []struct{ os, name string }{
		{"chromeos", ""}, {"other", "Haiku R1"}, {"other", "Haiku R1 beta 5"}, {"other", ""}, {"macos", "macOS 14.2"},
	}
	if len(q.views) != len(want) {
		t.Fatalf("views = %+v", q.views)
	}
	for i, w := range want {
		if q.views[i].OS != w.os || q.views[i].OSName != w.name {
			t.Errorf("view %d os = (%q, %q), want (%q, %q)", i, q.views[i].OS, q.views[i].OSName, w.os, w.name)
		}
	}
	if len(q.events) != 1 || q.events[0].OS != "other" {
		t.Errorf("product event os = %+v, want other", q.events)
	}
	if _, leaked := q.events[0].Attributes["$os_name"]; leaked {
		t.Error("$os_name reached the product event's attributes")
	}
}

// $platform is lower-cased and pattern-checked, never fills os, lands on
// product events too, and an invalid value warns and stores unknown.
func TestPlatformIsValidatedIndependentlyOfOS(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"$page_view","attributes":{"$path":"/","$platform":"Electron","$os":"macos"}},
		{"name":"$page_view","attributes":{"$path":"/","$platform":"Not A Platform!"}},
		{"name":"$page_view","attributes":{"$path":"/","$platform":"ios"}},
		{"name":"signup","attributes":{"$platform":"iOS"}}`)
	res := decodeResult(t, post(h, body, nil))
	if res.Accepted != 4 || len(res.Warnings) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.Warnings[0].Index != 1 || res.Warnings[0].Reason != `$platform "Not A Platform!" is not a known value, stored as unknown` {
		t.Errorf("warning = %+v", res.Warnings[0])
	}
	if len(q.views) != 3 {
		t.Fatalf("views = %+v", q.views)
	}
	if q.views[0].Platform != "electron" || q.views[0].OS != "macos" {
		t.Errorf("view 0 = platform %q os %q", q.views[0].Platform, q.views[0].OS)
	}
	if q.views[1].Platform != "unknown" || q.views[1].OS != "unknown" {
		t.Errorf("view 1 = platform %q os %q, want unknown for both", q.views[1].Platform, q.views[1].OS)
	}
	if q.views[2].Platform != "ios" || q.views[2].OS != "unknown" {
		t.Errorf("view 2 = platform %q os %q: $platform must never fill os", q.views[2].Platform, q.views[2].OS)
	}
	if len(q.events) != 1 || q.events[0].Platform != "ios" {
		t.Errorf("product event = %+v, want platform ios", q.events)
	}
}

// Browser and device close on the same terms as os. Both are views-only:
// a product event resolves and drops them without a warning about the
// drop, but an unrecognised value still warns.
func TestBrowserAndDeviceAreValidated(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"$page_view","attributes":{"$path":"/","$browser":"Samsung Internet","$browser_version":"25","$device":"Tablet"}},
		{"name":"$page_view","attributes":{"$path":"/","$browser":"netscape","$device":"phablet"}},
		{"name":"$page_view","attributes":{"$path":"/","$browser":"other","$device":"other"}},
		{"name":"signup","attributes":{"$browser":"safari","$device":"mobile"}}`)
	res := decodeResult(t, post(h, body, nil))
	if res.Accepted != 4 || len(res.Warnings) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.Warnings[0].Reason != `$browser "netscape" is not a known value, stored as other` ||
		res.Warnings[1].Reason != `$device "phablet" is not a known value, stored as other` {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	want := []struct{ browser, version, device string }{
		{"samsung_internet", "25", "tablet"}, {"other", "", "other"}, {"other", "", "other"},
	}
	for i, w := range want {
		v := q.views[i]
		if v.Browser != w.browser || v.BrowserVersion != w.version || v.Device != w.device {
			t.Errorf("view %d = %q/%q %q, want %+v", i, v.Browser, v.BrowserVersion, v.Device, w)
		}
	}
	if len(q.events) != 1 || len(q.events[0].Attributes) != 0 {
		t.Errorf("product event attributes = %+v, want the reserved keys dropped", q.events)
	}
}
```

- [ ] **Step 7: Run the server package**

Run: `go test ./internal/server/ 2>&1 | tail -20`
Expected: PASS. If `TestServeLogsIngestSummary` or other app-level tests in `internal/app` run this path, they only post `$pageview` and are unaffected.

- [ ] **Step 8: Docs — the ingest contract, and drop `aliasKeys`**

`internal/api/docs_sync_test.go`: delete the `aliasKeys` var and its comment (lines 62-66) and the `if aliasKeys[k] { continue }` in `TestDocumentMatchesReservedKeys`.

`docs/twillingate.md`:

Lines 30-32 (`Raw IP addresses and User-Agent strings are never stored — they are enriched into a country and a browser name at ingest and discarded.`) become:

```
midnight, so nothing links across days. Raw IP addresses and User-Agent
strings are never stored: the IP becomes a country at ingest, the
User-Agent is checked for crawlers, and both are discarded.
```

Line 193 (`data-kind` row) — replace the clause `and tells the server to trust the declared environment instead of parsing the User-Agent.` with `and exempts the client from the server's crawler filter, which applies to \`web\` only.`

Line 194 (`data-os` row) — replace the whole row with:

```
| `data-os` | `os` | Override the detected operating system (`$os`). See [Declaring the environment](#declaring-the-environment). |
```

Lines 486-497 (from `**Only `web` has server-side meaning.**` to `…whatever its kind.`) become:

```
**Only `web` has server-side meaning, and only in two ways.** A web view
is enriched from its connection for one thing — the country, from the
IP — and filtered for one thing — a crawler User-Agent is dropped. Every
other kind is never filtered, so a CLI or an Electron app is never dropped
as a crawler whatever HTTP library it uses. Nothing else is derived from
the User-Agent on any kind.

### Declaring the environment

**The client declares its environment; the server validates and never
parses.** `$platform`, `$os`, `$os_version`, `$os_name`, `$browser`,
`$browser_version`, `$device`, `$device_model`, `$app_version`, `$locale`,
`$display_width` and `$display_height` are stored on any kind exactly as
declared. The JS SDK detects and sends them on every batch (see
[Detection](#detection)); any other client — a backend relay, a native
app, a custom SDK — sends them itself or records `unknown`.

`$platform` is the surface the product is used through and `$os` the
operating system it runs on. They coincide for a native app and diverge
everywhere else: Safari on an iPhone is `platform=web, os=ios`; an
Electron build on a Mac is `platform=electron, os=macos`. `$kind` is
adjacent but coarser and stays as it is.

| Key | Values | Absent | Unrecognised |
| --- | --- | --- | --- |
| `$platform` | Open. Trimmed, lower-cased, then `^[a-z][a-z0-9_]{0,15}$`. Conventionally `web`, `ios`, `android`, `macos`, `windows`, `linux`, `electron`, … | `unknown` | `unknown`, with a warning |
| `$os` | `windows` `macos` `linux` `bsd` `chromeos` `ios` `ipados` `android` `fireos` `harmonyos` `kaios` `tvos` `watchos` `visionos` `tizen` `webos` `playstation` `xbox` `nintendo` `other` `unknown` | `unknown` | `other`, with a warning; the raw value is kept in `os_name` |
| `$browser` | `chrome` `safari` `firefox` `edge` `opera` `samsung_internet` `brave` `vivaldi` `duckduckgo` `yandex` `other` `unknown` | `unknown` | `other`, with a warning |
| `$device` | `desktop` `mobile` `tablet` `wearable` `xr` `other` `unknown` | `unknown` | `other`, with a warning |

Validation trims, lower-cases and folds spaces and dashes (`Chrome OS` →
`chromeos`, `Samsung Internet` → `samsung_internet`), so case is
forgiven; nothing is ever rejected, because a client shipping a value
this server has not learned yet must not receive a `4xx`. **`other` and
`unknown` are different answers**: `other` means there is a value and it
is outside the list, `unknown` means no information at all — which is
what makes undeclared traffic visible in a breakdown instead of silently
inflating a real bucket. A client may send either deliberately, and
neither is warned about.

`$os_name` is the OS's full self-reported name with version — `macOS 14.2`,
`Windows 11`, `FreeBSD 14.1` — stored verbatim on views, empty when
absent, never aggregated and never a breakdown dimension. It exists so an
`$os` of `other` stays investigable through `query` while the raw rows
last. `$os_version` and `$browser_version` are free text; the SDK sends
the major browser version and the OS version it can determine. `$device`
is the form factor; consoles and TVs are `other` there because `$os`
already names them. `$device_model` is free text beside it.

Product events keep `$platform` and `$os` as columns and resolve and drop
the rest (`$os_version`, `$os_name`, `$browser`, `$browser_version`,
`$device`), so an SDK that sends every environment key on every batch is
correct and cheap. `$app_version` is the version of whatever client sent
the event, whatever its kind.
```

In the envelope example (line ~600), replace the line `"$kind": "app", "$os": "ios",` with:

```
    "$kind": "app", "$platform": "ios", "$os": "ios",
    "$os_name": "iOS 17.2", "$browser": "safari", "$browser_version": "17",
    "$device": "mobile",
```

In the reserved-key table (line ~655), the Environment row becomes:

```
| Environment | `$kind` `$platform` `$os` `$os_version` `$os_name` `$browser` `$browser_version` `$device` `$device_model` `$app_version` `$locale` `$display_width` `$display_height` |
```

Lines 664-667 (`Location attributes are stored **verbatim**. … discarded.`) become:

```
Location attributes are stored **verbatim**. The server does no URL parsing,
no normalization, no case folding — the client owns normalization. The
client IP and User-Agent are never stored: the IP is enriched into a
country, the User-Agent is read only to drop crawlers, and both are
discarded.
```

- [ ] **Step 9: Run the docs binding tests**

Run: `cd internal/api && go test -run 'TestDocument' ./`
Expected: PASS (`TestDocumentMatchesReservedKeys` sees the five new keys in the table; `TestDocumentMatchesSDK` still passes because it only checks symbols that already exist).

- [ ] **Step 10: Commit**

```bash
git add internal/server internal/api/docs_sync_test.go docs/twillingate.md
git commit -m "feat(server)!: store the declared platform, os, browser and device and stop parsing the User-Agent"
```

---

### Task 4: `internal/api` — the platforms dimension, the rekeyed app versions, the schema resource

Implements spec "Reporting" rows for `ops_read.go`, `resources.go`, and the documentation of both.

**Files:**
- Modify: `internal/api/ops_read.go:160-172, 184-187, 226-235`
- Modify: `internal/api/resources.go:34-49` (`schemaViews`)
- Modify: `internal/api/guide.go:74`
- Modify: `internal/api/seed_test.go:57-62`
- Modify: `internal/api/ops_read_test.go:100-105`
- Modify: `docs/twillingate.md` — lines 145-147 (attribute breakdowns), 787-793 (tool table rows), 883-886 (views family)

**Interfaces:**
- Consumes: `v_views_platforms`, rekeyed `v_views_app_versions` from Task 2.
- Produces: `views_breakdown` dimension `platforms`; `app_versions` returns `platform, app_version`.

- [ ] **Step 1: Extend the dimension tests**

`internal/api/seed_test.go` — the two seeds change:

```go
	seed(`INSERT INTO agg_views_app_versions (project_id, day, platform, app_version, visitors, views)
	      VALUES (1,'2026-08-20','ios','2.4.1',5,12)`)
	seed(`INSERT INTO agg_views_platforms (project_id, day, platform, visitors, views)
	      VALUES (1,'2026-08-20','web',10,25), (1,'2026-08-20','ios',6,20)`)
```

Also lower-case the seeded `agg_views_os` values (`'ios'`, `'windows'`) and `agg_views_browsers` (`'chrome'`), and change the `agg_views_devices` row `(1,'2026-08-20','','iPhone15,3',5,12)` to `'unknown'` in place of `''` — the fixtures speak the post-015 vocabulary.

`internal/api/ops_read_test.go` `TestViewsBreakdownEveryDimension` — add `"platforms": "web"` to `want`, and change `"os": "17.4"` to `"os": "ios"`.

- [ ] **Step 2: Run to see the failures**

Run: `cd internal/api && go test -run 'ViewsBreakdown|BreakdownEnum' ./ 2>&1 | tail`
Expected: FAIL — `unknown dimension "platforms"`, and the seed of `agg_views_app_versions` fails on `platform` only if the migration is missing (it is not).

- [ ] **Step 3: The dimension table, the enum and the descriptions**

`internal/api/ops_read.go`:

```go
var viewsDimensions = map[string]viewsDimension{
	"kinds":        {"v_views_daily", []string{"kind"}},
	"paths":        {"v_views_paths", []string{"path"}},
	"hosts":        {"v_views_hosts", []string{"host"}},
	"referrers":    {"v_views_referrers", []string{"source"}},
	"utm":          {"v_views_utm", []string{"utm_source", "utm_medium", "utm_campaign"}},
	"countries":    {"v_views_countries", []string{"country"}},
	"platforms":    {"v_views_platforms", []string{"platform"}},
	"os":           {"v_views_os", []string{"os", "os_version"}},
	"browsers":     {"v_views_browsers", []string{"browser", "browser_version"}},
	"app_versions": {"v_views_app_versions", []string{"platform", "app_version"}},
	"devices":      {"v_views_devices", []string{"device", "device_model"}},
	"displays":     {"v_views_displays", []string{"display"}},
}
```

`breakdownIn.Dimension` tag: `jsonschema:"one of: app_versions, browsers, countries, devices, displays, hosts, kinds, os, paths, platforms, referrers, utm"` (sorted; `TestBreakdownEnumMatchesDimensions` checks it against `dimensionNames()`).

`views_breakdown` description: `"Top values for one dimension of a project's views over a date range: kinds, paths, hosts, referrers, utm, countries, platforms, os, browsers, app_versions, devices or displays. Two-key dimensions (os, browsers, app_versions, devices) return both columns. os, browser and device values are lower-case closed vocabularies with other (outside the list) and unknown (not declared) as distinct floors; platform is the surface (web, ios, electron, …) and never empty."`

`product_attributes` description: `"Attribute breakdowns for product events. The system dimensions $platform, $os and $app_version are always included; a custom key only appears once the project declares it in attributes (see update_project)."`

`internal/api/resources.go` `schemaViews` — replace the `v_views_countries` … `v_views_app_versions` lines with:

```
  v_views_countries(project_id, day, country, visitors, views)
  v_views_platforms(project_id, day, platform, visitors, views)  -- platform: 'web'|'ios'|'android'|'electron'|…|'unknown', never empty
  v_views_os(project_id, day, os, os_version, visitors, views)  -- os: lower-case closed vocabulary ('windows','macos','ios',…) plus 'other' (outside the list) and 'unknown' (not declared)
  v_views_browsers(project_id, day, browser, browser_version, visitors, views)  -- browser: 'chrome'|'safari'|'firefox'|'edge'|'opera'|'samsung_internet'|'brave'|'vivaldi'|'duckduckgo'|'yandex'|'other'|'unknown'
  v_views_app_versions(project_id, day, platform, app_version, visitors, views)  -- keyed by platform: 2.4.1 means different things per build
```

and the `v_views_devices` line gains `  -- device: 'desktop'|'mobile'|'tablet'|'wearable'|'xr'|'other'|'unknown'`. Add to the `v_product_attrs` line: `  -- attr_key '$platform', '$os', '$app_version' always present`.

`internal/api/guide.go:74` — replace `and data-os / data-app-version so its views are declared rather than parsed from the User-Agent.` with `, data-platform=\"electron\" and data-app-version, so its views are keyed by the build rather than counted as web; the OS, browser and device are detected by the SDK on every kind.`

- [ ] **Step 4: Docs — tool rows, attribute breakdowns, the views family**

`docs/twillingate.md`:

Line ~145 (`$os` and `$app_version` roll up automatically without being declared. …`) becomes:

```
`$platform`, `$os` and `$app_version` roll up automatically without being
declared. Do not add them to `attributes`: `$`-prefixed keys are reserved
and never reach the custom attribute blob, so `"attributes": ["$os"]`
extracts nothing.
```

`views_breakdown` tool row: the dimension list becomes `` `kinds`, `paths`, `hosts`, `referrers`, `utm`, `countries`, `platforms`, `os`, `browsers`, `app_versions`, `devices`, `displays` ``. `product_attributes` row: `` `$platform`, `$os` and `$app_version` are always available; a custom key only appears once the project declares it ``.

Lines ~883-886 (the views family sentence) become:

```
The views family is `v_views_daily` (per kind), `v_views_paths`,
`v_views_hosts`, `v_views_referrers`, `v_views_utm`, `v_views_countries`,
`v_views_platforms`, `v_views_os`, `v_views_browsers`,
`v_views_app_versions` (keyed by `platform` and `app_version`),
`v_views_devices` and `v_views_displays`. Every dimension is capped at 500
values per day; the tail is one `(other)` row whose visitors are distinct
actors, not a sum. `os`, `browser` and `device` are lower-case closed
vocabularies (see [Declaring the environment](#declaring-the-environment))
in which `other` and `(other)` are different things: `other` is a real
value outside the list, `(other)` is the cap.
```

- [ ] **Step 5: Run the api package**

Run: `go test ./internal/api/ 2>&1 | tail -5` (about 6 minutes; background it with `-timeout 900s` if needed)
Expected: PASS, including `TestDocumentCoversEveryViewsDimension`, `TestBreakdownEnumMatchesDimensions`, `TestViewsBreakdownEveryDimension`, the guide tests.

- [ ] **Step 6: Commit**

```bash
git add internal/api docs/twillingate.md
git commit -m "feat(api): add the platforms breakdown and key app versions by platform"
```

---

### Task 5: SDK — the detection module

Implements spec "JS SDK → Detection is public API", "OS detection", "`$os_version` and `$os_name` detection", "Browser detection", "Device detection". A new self-contained module with no dependency on the tracker; Task 6 wires it in. Vitest runs under jsdom (`sdk/vitest.config.ts`), but every case here passes an explicit `ClientSignals` and never touches `navigator`.

**Files:**
- Create: `sdk/src/detect.ts`
- Create: `sdk/src/detect.test.ts`

**Interfaces:**
- Produces: `ClientSignals`, `OSInfo {os, osVersion, osName}`, `BrowserInfo {browser, browserVersion}`, `DeviceInfo {device}`; `detectOS(s?: ClientSignals): OSInfo`, `detectBrowser(s?): BrowserInfo`, `detectDevice(s?): DeviceInfo`, `detectAll(s?): OSInfo & BrowserInfo & DeviceInfo`; `ambientSignals(): ClientSignals`; `primePlatformVersion(): void`; `resetPlatformVersion(): void` (test hook).

- [ ] **Step 1: Write the tests**

Create `sdk/src/detect.test.ts`:

```ts
// Detection is a table: every case is a ClientSignals literal, so no case
// depends on what jsdom happens to provide, and the type is the whole
// record of what the SDK reads off the device.
import { describe, expect, it } from "vitest";
import { detectBrowser, detectDevice, detectOS, type ClientSignals } from "./detect";

const UA = {
  chromeWin: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
  safariMac: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
  safariIphone: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
  safariIpad: "Mozilla/5.0 (iPad; CPU OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
  chromeIos: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/126.0.6478.54 Mobile/15E148 Safari/604.1",
  chromeAndroid: "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
  androidTablet: "Mozilla/5.0 (Linux; Android 13; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
  samsung: "Mozilla/5.0 (Linux; Android 13; SAMSUNG SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36",
  silk: "Mozilla/5.0 (Linux; Android 9; KFMAWI) AppleWebKit/537.36 (KHTML, like Gecko) Silk/120.5.1 like Chrome/120.0.6099.230 Safari/537.36",
  harmony: "Mozilla/5.0 (Linux; Android 10; HarmonyOS; NOH-AN00) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/92.0.4515.105 HuaweiBrowser/12.0.3.310 Mobile Safari/537.36",
  firefoxLinux: "Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0",
  freebsd: "Mozilla/5.0 (X11; FreeBSD amd64; rv:127.0) Gecko/20100101 Firefox/127.0",
  cros: "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
  edge: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.2592.87",
  opera: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 OPR/111.0.0.0",
  vivaldi: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Vivaldi/6.8.3381.46",
  yandex: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 YaBrowser/24.6.0.0 Safari/537.36",
  duckduckgo: "Mozilla/5.0 (Linux; Android 10; SM-G960F) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/126.0.0.0 Mobile DuckDuckGo/5 Safari/537.36",
  kaios: "Mozilla/5.0 (Mobile; Nokia 8110 4G; rv:48.0) Gecko/48.0 Firefox/48.0 KAIOS/2.5",
  quest: "Mozilla/5.0 (Linux; Android 12; Quest 3) AppleWebKit/537.36 (KHTML, like Gecko) OculusBrowser/33.0 Chrome/126.0.0.0 VR Safari/537.36",
  playstation: "Mozilla/5.0 (PlayStation; PlayStation 5/8.20) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
  xbox: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; Xbox; Xbox Series X) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edge/44.18363.8131",
  nintendo: "Mozilla/5.0 (Nintendo Switch; WebApplet) AppleWebKit/609.4 (KHTML, like Gecko) NF/6.0.2.22.4 NintendoBrowser/5.1.0.23519",
  tizen: "Mozilla/5.0 (SMART-TV; LINUX; Tizen 6.0) AppleWebKit/537.36 (KHTML, like Gecko) 76.0.3809.146/6.0 TV Safari/537.36",
  webos: "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.79 Safari/537.36 WebAppManager",
  appletv: "AppleTV11,1/11.1",
  unknownThing: "SomeNewRuntime/1.0",
};

describe("detectOS", () => {
  // One row per vocabulary value detection can reach, with a real
  // User-Agent, plus the version each one yields.
  const table: [string, ClientSignals, string, string][] = [
    ["windows", { userAgent: UA.chromeWin }, "windows", "10"],
    ["macos", { userAgent: UA.safariMac }, "macos", "10.15.7"],
    ["linux", { userAgent: UA.firefoxLinux }, "linux", ""],
    ["bsd", { userAgent: UA.freebsd }, "bsd", ""],
    ["chromeos", { userAgent: UA.cros }, "chromeos", "14541.0.0"],
    ["ios", { userAgent: UA.safariIphone }, "ios", "17.2"],
    ["ipados", { userAgent: UA.safariIpad }, "ipados", "17.2"],
    ["android", { userAgent: UA.chromeAndroid }, "android", "14"],
    ["fireos", { userAgent: UA.silk }, "fireos", "9"],
    ["harmonyos", { userAgent: UA.harmony }, "harmonyos", "10"],
    ["kaios", { userAgent: UA.kaios }, "kaios", ""],
    ["tvos", { userAgent: UA.appletv }, "tvos", ""],
    ["tizen", { userAgent: UA.tizen }, "tizen", ""],
    ["webos", { userAgent: UA.webos }, "webos", ""],
    ["playstation", { userAgent: UA.playstation }, "playstation", ""],
    ["xbox", { userAgent: UA.xbox }, "xbox", ""],
    ["nintendo", { userAgent: UA.nintendo }, "nintendo", ""],
  ];
  it.each(table)("%s", (_name, signals, os, osVersion) => {
    expect(detectOS(signals)).toMatchObject({ os, osVersion });
  });

  it("orders specific before generic", () => {
    expect(detectOS({ userAgent: UA.silk }).os).toBe("fireos"); // contains Android
    expect(detectOS({ userAgent: UA.chromeAndroid }).os).toBe("android"); // contains Linux
    expect(detectOS({ userAgent: UA.freebsd }).os).toBe("bsd"); // contains X11
    expect(detectOS({ userAgent: UA.xbox }).os).toBe("xbox"); // contains Windows NT
  });

  it("separates iPadOS in desktop mode from a Mac by maxTouchPoints", () => {
    const r = detectOS({ userAgent: UA.safariMac, platform: "MacIntel", maxTouchPoints: 5 });
    expect(r.os).toBe("ipados");
    expect(detectOS({ userAgent: UA.safariMac, platform: "MacIntel", maxTouchPoints: 0 }).os).toBe("macos");
  });

  it("uses userAgentData.platform after the specific checks, not before", () => {
    expect(detectOS({ userAgent: UA.silk, uaPlatform: "Android" }).os).toBe("fireos");
    expect(detectOS({ uaPlatform: "Chrome OS" }).os).toBe("chromeos");
    expect(detectOS({ uaPlatform: "Windows" }).os).toBe("windows");
  });

  it("resolves Windows 11 and a true macOS version from platformVersion", () => {
    expect(detectOS({ userAgent: UA.chromeWin, platformVersion: "15.0.0" })).toMatchObject({ osVersion: "11", osName: "Windows 11" });
    expect(detectOS({ userAgent: UA.chromeWin, platformVersion: "10.0.0" })).toMatchObject({ osVersion: "10", osName: "Windows 10" });
    expect(detectOS({ userAgent: UA.chromeWin, platformVersion: "0.0.0" }).osVersion).toBe("10"); // Windows 7/8 report 0.x: NT stands
    expect(detectOS({ userAgent: UA.safariMac, platformVersion: "14.2.1" })).toMatchObject({ osVersion: "14.2.1", osName: "macOS 14.2.1" });
  });

  it("falls back to the synchronous parse without platformVersion", () => {
    expect(detectOS({ userAgent: UA.chromeWin })).toMatchObject({ osVersion: "10", osName: "Windows 10" });
    expect(detectOS({ userAgent: UA.safariMac }).osName).toBe("macOS 10.15.7");
    expect(detectOS({ userAgent: UA.freebsd }).osName).toBe("FreeBSD");
  });

  it("composes osName from the display name and version", () => {
    expect(detectOS({ userAgent: UA.safariIphone }).osName).toBe("iOS 17.2");
    expect(detectOS({ userAgent: UA.firefoxLinux }).osName).toBe("Linux");
  });

  it("is other for an unrecognised User-Agent and unknown for none", () => {
    expect(detectOS({ userAgent: UA.unknownThing })).toEqual({ os: "other", osVersion: "", osName: "" });
    expect(detectOS({})).toEqual({ os: "unknown", osVersion: "", osName: "" });
    expect(detectOS({ userAgent: "" })).toEqual({ os: "unknown", osVersion: "", osName: "" });
  });

  it("never returns the declare-only values", () => {
    for (const ua of Object.values(UA)) {
      expect(["watchos", "visionos"]).not.toContain(detectOS({ userAgent: ua }).os);
    }
  });
});

describe("detectBrowser", () => {
  const table: [string, ClientSignals, string, string][] = [
    ["chrome", { userAgent: UA.chromeWin }, "chrome", "126"],
    ["chrome on iOS", { userAgent: UA.chromeIos }, "chrome", "126"],
    ["safari", { userAgent: UA.safariMac }, "safari", "17"],
    ["firefox", { userAgent: UA.firefoxLinux }, "firefox", "127"],
    ["edge", { userAgent: UA.edge }, "edge", "126"],
    ["legacy edge", { userAgent: UA.xbox }, "edge", "44"],
    ["opera", { userAgent: UA.opera }, "opera", "111"],
    ["samsung_internet", { userAgent: UA.samsung }, "samsung_internet", "25"],
    ["vivaldi", { userAgent: UA.vivaldi }, "vivaldi", "6"],
    ["yandex", { userAgent: UA.yandex }, "yandex", "24"],
    ["duckduckgo", { userAgent: UA.duckduckgo }, "duckduckgo", "5"],
  ];
  it.each(table)("%s", (_name, signals, browser, browserVersion) => {
    expect(detectBrowser(signals)).toEqual({ browser, browserVersion });
  });

  it("reports Brave from the presence of navigator.brave beside a verbatim Chrome User-Agent", () => {
    expect(detectBrowser({ userAgent: UA.chromeWin, brave: true })).toEqual({ browser: "brave", browserVersion: "126" });
    expect(detectBrowser({ userAgent: UA.chromeWin, brave: false }).browser).toBe("chrome");
  });

  it("orders derivatives before bases", () => {
    expect(detectBrowser({ userAgent: UA.edge }).browser).toBe("edge"); // contains Chrome
    expect(detectBrowser({ userAgent: UA.chromeWin }).browser).toBe("chrome"); // contains Safari
    expect(detectBrowser({ userAgent: UA.samsung }).browser).toBe("samsung_internet"); // contains Chrome
  });

  it("reads brands after brave and before the User-Agent", () => {
    const brands = [{ brand: "Microsoft Edge", version: "126.0.2592.87" }, { brand: "Chromium", version: "126.0.0.0" }];
    expect(detectBrowser({ userAgent: UA.chromeWin, brands })).toEqual({ browser: "edge", browserVersion: "126" });
    expect(detectBrowser({ userAgent: UA.chromeWin, brands, brave: true }).browser).toBe("brave");
    expect(detectBrowser({ brands: [{ brand: "Google Chrome", version: "126.0.0.0" }] })).toEqual({ browser: "chrome", browserVersion: "126" });
  });

  it("takes Safari's version from Version/, not Safari/", () => {
    expect(detectBrowser({ userAgent: UA.safariIphone }).browserVersion).toBe("17");
  });

  it("is other for an unrecognised User-Agent and unknown for none", () => {
    expect(detectBrowser({ userAgent: UA.unknownThing })).toEqual({ browser: "other", browserVersion: "" });
    expect(detectBrowser({})).toEqual({ browser: "unknown", browserVersion: "" });
  });
});

describe("detectDevice", () => {
  const table: [string, ClientSignals, string][] = [
    ["desktop", { userAgent: UA.chromeWin }, "desktop"],
    ["mobile", { userAgent: UA.safariIphone }, "mobile"],
    ["mobile android", { userAgent: UA.chromeAndroid }, "mobile"],
    ["tablet", { userAgent: UA.safariIpad }, "tablet"],
    ["xr", { userAgent: UA.quest }, "xr"],
    ["console is other", { userAgent: UA.playstation }, "other"],
    ["tv is other", { userAgent: UA.tizen }, "other"],
  ];
  it.each(table)("%s", (_name, signals, device) => {
    expect(detectDevice(signals)).toEqual({ device });
  });

  it("reports a Quest as xr while its OS is still android", () => {
    expect(detectDevice({ userAgent: UA.quest }).device).toBe("xr");
    expect(detectOS({ userAgent: UA.quest }).os).toBe("android");
  });

  it("does not let userAgentData.mobile override a tablet hit", () => {
    expect(detectDevice({ userAgent: UA.safariIpad, mobile: true }).device).toBe("tablet");
    expect(detectDevice({ userAgent: UA.androidTablet, mobile: true }).device).toBe("mobile");
    expect(detectDevice({ userAgent: UA.androidTablet }).device).toBe("desktop"); // no marker: the honest default
  });

  it("keeps OS and device consistent for iPadOS in desktop mode", () => {
    const s = { userAgent: UA.safariMac, platform: "MacIntel", maxTouchPoints: 5 };
    expect(detectOS(s).os).toBe("ipados");
    expect(detectDevice(s).device).toBe("tablet");
  });

  it("is desktop for an unrecognised User-Agent and unknown for none", () => {
    expect(detectDevice({ userAgent: UA.unknownThing }).device).toBe("desktop");
    expect(detectDevice({}).device).toBe("unknown");
  });

  it("never returns wearable", () => {
    for (const ua of Object.values(UA)) {
      expect(detectDevice({ userAgent: ua }).device).not.toBe("wearable");
    }
  });
});

describe("signals are the whole input", () => {
  it("consults only what was supplied", () => {
    // jsdom's navigator would say linux/other/desktop; a supplied object
    // with no userAgent must not be backfilled from it.
    expect(detectOS({ maxTouchPoints: 5 }).os).toBe("unknown");
    expect(detectBrowser({ brave: true }).browser).toBe("brave");
  });
});
```

- [ ] **Step 2: Run to see it fail**

Run: `cd sdk && npx vitest run src/detect.test.ts 2>&1 | tail -5`
Expected: FAIL — cannot resolve `./detect`.

- [ ] **Step 3: Write the module**

Create `sdk/src/detect.ts`:

```ts
/* Client environment detection: OS, browser and device class.
 *
 * Pure functions over a flat list of signals. Supply a ClientSignals and
 * only its fields are consulted — a field left out is absent, never read
 * from the real browser — so a test is a table of literals and the type
 * is the complete record of what the SDK reads off the device. Omit the
 * argument and every field comes from the ambient navigator.
 *
 * The three detect* functions are views onto one resolve: OS and device
 * share their signals (iPad, maxTouchPoints, the console markers), and
 * separate passes would eventually disagree — ipados with desktop is not
 * a state that exists.
 *
 * Vocabularies mirror internal/enrich/ua.go. other means a User-Agent was
 * present and named nothing on the list; unknown means there was nothing
 * to read — a non-browser runtime. In a browser unknown is unreachable.
 */

export interface ClientSignals {
  /** navigator.userAgent */
  userAgent?: string;
  /** navigator.platform — "MacIntel" is load-bearing for iPadOS */
  platform?: string;
  /** navigator.maxTouchPoints */
  maxTouchPoints?: number;
  /** whether navigator.brave is defined — presence, not isBrave() */
  brave?: boolean;
  /** navigator.userAgentData.brands */
  brands?: { brand: string; version: string }[];
  /** navigator.userAgentData.platform */
  uaPlatform?: string;
  /** navigator.userAgentData.mobile */
  mobile?: boolean;
  /** resolved getHighEntropyValues(["platformVersion"]) */
  platformVersion?: string;
}

export interface OSInfo {
  os: string;
  osVersion: string;
  osName: string;
}
export interface BrowserInfo {
  browser: string;
  browserVersion: string;
}
export interface DeviceInfo {
  device: string;
}

interface NavigatorUA extends Navigator {
  userAgentData?: {
    brands?: { brand: string; version: string }[];
    platform?: string;
    mobile?: boolean;
    getHighEntropyValues?: (hints: string[]) => Promise<{ platformVersion?: string }>;
  };
  brave?: unknown;
}

// The one async signal. Windows 10 and 11 are indistinguishable in a
// User-Agent (both NT 10.0) and Safari freezes macOS at 10_15_7; both are
// recoverable only from userAgentData.getHighEntropyValues, which returns
// a promise. The tracker kicks it off once at init and detection reads
// whatever has resolved, so detectOS() right after init answers from the
// User-Agent and the same call a tick later carries the corrected
// version. Batches are unaffected: batchAttributes() runs in flush(),
// after the default 1000ms flushInterval has let this settle.
let resolvedPlatformVersion: string | undefined;

export function primePlatformVersion(): void {
  if (typeof navigator === "undefined") return;
  const uad = (navigator as NavigatorUA).userAgentData;
  if (!uad || typeof uad.getHighEntropyValues !== "function") return;
  uad.getHighEntropyValues(["platformVersion"]).then(
    (v) => {
      if (v && typeof v.platformVersion === "string") resolvedPlatformVersion = v.platformVersion;
    },
    () => {
      /* refused or unsupported: the synchronous parse stands */
    },
  );
}

/** Test hook: forget a resolved platformVersion between cases. */
export function resetPlatformVersion(): void {
  resolvedPlatformVersion = undefined;
}

/** Every signal, read from the ambient navigator. */
export function ambientSignals(): ClientSignals {
  if (typeof navigator === "undefined") return {};
  const n = navigator as NavigatorUA;
  const s: ClientSignals = {
    userAgent: n.userAgent,
    platform: n.platform,
    maxTouchPoints: n.maxTouchPoints,
    brave: n.brave !== undefined,
  };
  const uad = n.userAgentData;
  if (uad) {
    s.brands = uad.brands;
    s.uaPlatform = uad.platform;
    s.mobile = uad.mobile;
  }
  if (resolvedPlatformVersion !== undefined) s.platformVersion = resolvedPlatformVersion;
  return s;
}

export function detectOS(s?: ClientSignals): OSInfo {
  const r = resolve(s || ambientSignals());
  return { os: r.os, osVersion: r.osVersion, osName: r.osName };
}

export function detectBrowser(s?: ClientSignals): BrowserInfo {
  const r = resolve(s || ambientSignals());
  return { browser: r.browser, browserVersion: r.browserVersion };
}

export function detectDevice(s?: ClientSignals): DeviceInfo {
  return { device: resolve(s || ambientSignals()).device };
}

/** Everything at once, for the tracker's batch attributes. */
export function detectAll(s?: ClientSignals): OSInfo & BrowserInfo & DeviceInfo {
  return resolve(s || ambientSignals());
}

const OS_NAMES: Record<string, string> = {
  windows: "Windows", macos: "macOS", linux: "Linux", chromeos: "Chrome OS",
  ios: "iOS", ipados: "iPadOS", android: "Android", fireos: "Fire OS",
  harmonyos: "HarmonyOS", kaios: "KaiOS", tvos: "tvOS", tizen: "Tizen", webos: "webOS",
  playstation: "PlayStation", xbox: "Xbox", nintendo: "Nintendo",
};

// userAgentData.platform (Chromium >= 90) answers these without sniffing.
// It runs after the specific User-Agent checks because it reports a Fire
// tablet as Android.
const UA_PLATFORMS: Record<string, string> = {
  Windows: "windows", macOS: "macos", Android: "android", Linux: "linux",
  "Chrome OS": "chromeos", "Chromium OS": "chromeos", iOS: "ios",
};

// User-Agent markers, derivatives before bases: every Chromium UA contains
// Chrome and every Chrome UA contains Safari. The marker is also where the
// version starts, except Safari, whose version follows Version/ (Safari/
// is the WebKit build number).
const UA_BROWSERS: [marker: string, browser: string, versionMarker?: string][] = [
  ["Edg/", "edge"], ["Edge/", "edge"], ["SamsungBrowser/", "samsung_internet"],
  ["OPR/", "opera"], ["Opera/", "opera"], ["Vivaldi/", "vivaldi"], ["YaBrowser/", "yandex"],
  ["DuckDuckGo/", "duckduckgo"], ["Firefox/", "firefox"], ["FxiOS/", "firefox"],
  ["CriOS/", "chrome"], ["Chrome/", "chrome"], ["Safari/", "safari", "Version/"],
];

// userAgentData.brands, most specific first. Google Chrome and Chromium
// both mean chrome; "Not A Brand" entries match nothing.
const BRANDS: [brand: string, browser: string][] = [
  ["Microsoft Edge", "edge"], ["Opera", "opera"], ["Samsung Internet", "samsung_internet"],
  ["Vivaldi", "vivaldi"], ["Google Chrome", "chrome"], ["Chromium", "chrome"],
];

const NT: Record<string, string> = { "10.0": "10", "6.3": "8.1", "6.2": "8", "6.1": "7", "6.0": "Vista", "5.1": "XP" };

function resolve(s: ClientSignals): OSInfo & BrowserInfo & DeviceInfo {
  const ua = s.userAgent || "";
  const has = (m: string): boolean => ua.indexOf(m) >= 0;
  // iPadOS 13+ in desktop mode sends "Macintosh; Intel Mac OS X"; only
  // maxTouchPoints separates it from a Mac. The one case a User-Agent
  // cannot resolve, and the clearest thing client detection buys.
  const ipadDesktop = s.platform === "MacIntel" && (s.maxTouchPoints || 0) > 1;

  // OS: most specific first, first hit wins. Most of these User-Agents
  // are supersets of a more generic one (Fire OS contains Android, every
  // Android UA contains Linux, iPadOS in desktop mode contains Macintosh).
  let os: string;
  if (has("Xbox")) os = "xbox";
  else if (has("PlayStation")) os = "playstation";
  else if (has("Nintendo")) os = "nintendo";
  else if (has("KAIOS")) os = "kaios";
  else if (has("Tizen")) os = "tizen";
  else if (has("Web0S") || has("webOS") || has("hpwOS")) os = "webos";
  else if (has("AppleTV") || has("tvOS")) os = "tvos";
  else if (has("Silk")) os = "fireos";
  else if (has("HarmonyOS")) os = "harmonyos";
  else if (has("CrOS")) os = "chromeos";
  else if (has("iPhone") || has("iPod")) os = "ios";
  else if (has("iPad") || ipadDesktop) os = "ipados";
  else if (s.uaPlatform && UA_PLATFORMS[s.uaPlatform]) os = UA_PLATFORMS[s.uaPlatform];
  else if (has("Android")) os = "android";
  else if (has("Windows")) os = "windows";
  else if (has("Mac OS X") || has("Macintosh")) os = "macos";
  else if (has("FreeBSD") || has("OpenBSD") || has("NetBSD") || has("DragonFly")) os = "bsd";
  else if (has("X11") || has("Linux")) os = "linux";
  else if (ua || s.uaPlatform) os = "other";
  else os = "unknown";

  const osVersion = versionOf(os, ua, s.platformVersion);
  let osName = OS_NAMES[os] || "";
  if (os === "bsd") osName = (/(FreeBSD|OpenBSD|NetBSD|DragonFly)/.exec(ua) || ["", "BSD"])[1];
  if (osName && osVersion) osName += " " + osVersion;

  // Browser: brave first, because its User-Agent is deliberately Chrome's
  // and nothing later can recover it; then brands, the API built for the
  // question, which survives User-Agent reduction; then the markers.
  let browser = "";
  let browserVersion = "";
  if (s.brave) browser = "brave";
  if (!browser && s.brands) {
    for (const [brand, name] of BRANDS) {
      const b = s.brands.find((x) => x.brand === brand);
      if (b) {
        browser = name;
        browserVersion = major(b.version);
        break;
      }
    }
  }
  if (!browser) {
    for (const [marker, name, versionMarker] of UA_BROWSERS) {
      if (has(marker)) {
        browser = name;
        browserVersion = majorAfter(ua, versionMarker || marker);
        break;
      }
    }
  }
  if (!browser) browser = ua || (s.brands && s.brands.length) ? "other" : "unknown";
  if (browser === "brave" && !browserVersion) browserVersion = majorAfter(ua, "Chrome/");

  // Device: shares the OS pass. xr first, because the OS pass reports a
  // Quest as android and nothing downstream could recover it. Consoles
  // and TVs are other: $os already names each one, and they must not
  // fall through to desktop.
  let device: string;
  if (has("OculusBrowser") || has("Quest")) device = "xr";
  else if (has("Xbox") || has("PlayStation") || has("Nintendo") || has("AppleTV") || has("tvOS") ||
           has("Web0S") || has("webOS") || has("Tizen")) device = "other";
  else if (has("iPad") || has("Tablet") || ipadDesktop) device = "tablet";
  else if (s.mobile === true) device = "mobile";
  else if (has("Mobile") || has("iPhone")) device = "mobile";
  else if (ua) device = "desktop";
  else device = "unknown";

  return { os, osVersion, osName, browser, browserVersion, device };
}

function versionOf(os: string, ua: string, platformVersion?: string): string {
  const m = (re: RegExp): string => {
    const r = re.exec(ua);
    return r ? r[1].replace(/_/g, ".") : "";
  };
  switch (os) {
    case "ios":
    case "ipados":
      return m(/OS (\d+[_.]\d+(?:[_.]\d+)?)/);
    case "android":
    case "fireos":
    case "harmonyos":
      return m(/Android (\d+(?:\.\d+)*)/);
    case "windows": {
      // platformVersion major >= 13 is Windows 11, 1-12 is Windows 10;
      // Windows 7 and 8 report 0.x, so NT stands for them.
      const hi = platformVersion ? parseInt(platformVersion, 10) : 0;
      if (hi >= 13) return "11";
      if (hi >= 1) return "10";
      const nt = m(/Windows NT (\d+\.\d+)/);
      return NT[nt] || nt;
    }
    case "macos":
      return platformVersion || m(/Mac OS X (\d+[_.]\d+(?:[_.]\d+)?)/);
    case "chromeos":
      return m(/CrOS \S+ (\d+(?:\.\d+)*)/);
    default:
      return "";
  }
}

function major(version: string): string {
  return (/^\d+/.exec(version) || [""])[0];
}

// The run of digits after marker, or "" when the marker is absent or not
// followed by a digit.
function majorAfter(ua: string, marker: string): string {
  const i = ua.indexOf(marker);
  return i < 0 ? "" : major(ua.slice(i + marker.length));
}
```

- [ ] **Step 4: Run the tests and the type check**

Run: `cd sdk && npx vitest run src/detect.test.ts && npm run typecheck`
Expected: all cases PASS; `tsc` clean. If a table row fails on a version string (regex reading a different token than expected), fix the regex, not the expectation — every expectation above was derived from the User-Agent literal beside it.

- [ ] **Step 5: Commit**

```bash
git add sdk/src/detect.ts sdk/src/detect.test.ts
git commit -m "feat(sdk): detect os, browser and device class from a declared list of client signals"
```

---

### Task 6: SDK — declare the environment on every batch

Implements spec "JS SDK → Options", the method mirrors of the `detect*` functions, the `$platform` default rule, and the SDK sections of `docs/twillingate.md`. Rebuilds the embedded bundle.

**Files:**
- Modify: `sdk/src/twillingate.ts` (`InitOptions`, imports, fields, `init`, `batchAttributes`, three methods, re-exports, `autoInit`)
- Modify: `sdk/src/twillingate.test.ts` (the "carries app context" test, delete the deprecated-alias test, the parity map; add an "environment" block)
- Modify: `internal/api/docs_sync_test.go:126-138` (`TestDocumentMatchesSDK` symbol list)
- Modify: `docs/twillingate.md` — the snippet table (lines ~184-198), the SDK-only example (~230-246), a new `### Detection` subsection after Runtime API
- Regenerate: `internal/server/twillingate.js` (`npm run build`)

**Interfaces:**
- Consumes: everything Task 5 exports.
- Produces: `InitOptions.platform`, `.os`, `.osVersion`, `.osName`, `.browser`, `.browserVersion`, `.device`; `data-platform`, `data-os`, `data-os-version`, `data-os-name`, `data-browser`, `data-browser-version`, `data-device`; instance methods `detectOS`, `detectBrowser`, `detectDevice`; module re-exports of the same plus `ClientSignals`.

- [ ] **Step 1: Write the tracker tests**

In `sdk/src/twillingate.test.ts`:

Delete the test `"accepts the deprecated platform alias for os"`.

Replace `"carries app context as batch attributes"` with:

```ts
  it("carries app context as batch attributes", async () => {
    const t = tg({ kind: "app", platform: "ios", os: "ios", osVersion: "17.2", appVersion: "2.4.1", installId: "018f-install" });
    t.screen("/settings");
    await drain();
    const { attributes, events } = sent[0].body;
    expect(attributes).toMatchObject({
      $kind: "app",
      $platform: "ios",
      $os: "ios",
      $os_version: "17.2",
      $app_version: "2.4.1",
      $install_id: "018f-install",
    });
    expect(events[0].name).toBe("$screen_view");
    expect(events[0].attributes).toEqual({ $screen: "/settings" });
  });
```

In `"maps every data attribute to an InitOptions field"`, the `optionFor` map becomes:

```ts
    const optionFor: Record<string, string> = {
      key: "key", identity: "identity", user: "user", group: "group",
      auto: "autoPageviews", "mask-url": "maskUrl", routing: "routing",
      kind: "kind", platform: "platform", os: "os", "os-version": "osVersion", "os-name": "osName",
      browser: "browser", "browser-version": "browserVersion", device: "device",
      "app-version": "appVersion",
    };
```

Add a new block after `describe("payload shape", …)`:

```ts
describe("environment", () => {
  const CHROME_WIN = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36";

  // Replace the whole navigator: the ignore rules read webdriver, the
  // batch reads language, send reads sendBeacon, detection reads the rest.
  function stubNavigator(extra: Record<string, unknown>): void {
    vi.stubGlobal("navigator", { language: "en-US", webdriver: false, userAgent: CHROME_WIN, platform: "Win32", maxTouchPoints: 0, ...extra });
  }

  afterEach(() => resetPlatformVersion());

  it("sends the detected os, browser and device on every batch", async () => {
    stubNavigator({});
    const t = tg();
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({
      $platform: "web", $os: "windows", $os_version: "10", $os_name: "Windows 10",
      $browser: "chrome", $browser_version: "126", $device: "desktop",
    });
  });

  it("defaults $platform to web only while kind is web", async () => {
    stubNavigator({});
    const web = tg();
    web.track("a");
    await drain();
    expect(sent[0].body.attributes.$platform).toBe("web");

    sent = [];
    const app = tg({ kind: "app" });
    app.track("b");
    await drain();
    expect(sent[0].body.attributes).not.toHaveProperty("$platform");
    expect(sent[0].body.attributes.$os).toBe("windows"); // detection still runs on any kind

    sent = [];
    const electron = tg({ kind: "app", platform: "electron" });
    electron.track("c");
    await drain();
    expect(sent[0].body.attributes.$platform).toBe("electron");
  });

  it("lets an explicit option beat detection, while detect* still answers for the signals", async () => {
    stubNavigator({});
    const t = tg({ os: "linux", browser: "firefox", device: "tablet" });
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $os: "linux", $browser: "firefox", $device: "tablet" });
    // Pure detection ignores the option: it has to answer for THIS
    // User-Agent, or it is useless for the debugging case it exists for.
    expect(t.detectOS().os).toBe("windows");
    expect(t.detectBrowser().browser).toBe("chrome");
    expect(t.detectDevice().device).toBe("desktop");
  });

  it("consults only a supplied ClientSignals, never the ambient navigator", () => {
    stubNavigator({});
    const t = tg();
    expect(t.detectOS({ userAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0" }).os).toBe("linux");
    expect(t.detectOS({}).os).toBe("unknown");
  });

  it("carries the high-entropy platformVersion once it has resolved", async () => {
    stubNavigator({
      userAgentData: {
        brands: [{ brand: "Google Chrome", version: "126.0.0.0" }, { brand: "Chromium", version: "126.0.0.0" }],
        platform: "Windows", mobile: false,
        getHighEntropyValues: () => Promise.resolve({ platformVersion: "15.0.0" }),
      },
    });
    const t = tg({ flushInterval: 50 });
    // Immediately after init the promise has not settled: the User-Agent answer stands.
    expect(t.detectOS().osVersion).toBe("10");
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $os: "windows", $os_version: "11", $os_name: "Windows 11", $browser: "chrome" });
    expect(t.detectOS().osVersion).toBe("11");
  });

  it("sends unknown values honestly when there is no browser to read", async () => {
    // A runtime with no User-Agent at all — the case a bundled SDK meets
    // in a worker or a test harness — must say unknown, not other.
    stubNavigator({ userAgent: "", platform: undefined });
    const t = tg();
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $os: "unknown", $browser: "unknown", $device: "unknown" });
    expect(sent[0].body.attributes).not.toHaveProperty("$os_version");
    expect(sent[0].body.attributes).not.toHaveProperty("$os_name");
  });
});
```

Add `resetPlatformVersion` to the import from `"./detect"` at the top of the test file (`import { resetPlatformVersion } from "./detect";`).

In the snippet block (`describe("snippet auto-init", …)`), add:

```ts
  it("reads every environment data attribute", async () => {
    const s = scriptTag({
      "data-key": "ak_snip", "data-auto": "off", "data-kind": "app", "data-platform": "electron",
      "data-os": "macos", "data-os-version": "14.2", "data-os-name": "macOS 14.2",
      "data-browser": "chrome", "data-browser-version": "126", "data-device": "desktop",
    });
    const t = new Twillingate();
    autoInit(t, s);
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({
      $kind: "app", $platform: "electron", $os: "macos", $os_version: "14.2", $os_name: "macOS 14.2",
      $browser: "chrome", $browser_version: "126", $device: "desktop",
    });
  });
```

(`scriptTag` already exists in that block: it creates a `<script>` with a `src` on `URL_BASE` and sets the given attributes.)

- [ ] **Step 2: Run to see the failures**

Run: `cd sdk && npx vitest run src/twillingate.test.ts 2>&1 | tail -20`
Expected: FAIL — `detectOS` is not a function, `$platform` undefined, parity map complains about unknown attributes.

- [ ] **Step 3: Wire the tracker**

`sdk/src/twillingate.ts`:

Imports and re-exports, after the existing imports:

```ts
import {
  ambientSignals, detectAll, detectBrowser as detectBrowserFrom, detectDevice as detectDeviceFrom,
  detectOS as detectOSFrom, primePlatformVersion, type BrowserInfo, type ClientSignals, type DeviceInfo, type OSInfo,
} from "./detect";

export { detectOS, detectBrowser, detectDevice } from "./detect";
export type { ClientSignals, OSInfo, BrowserInfo, DeviceInfo } from "./detect";
```

`InitOptions` — replace the `kind`, `os`, `platform` members with:

```ts
  /**
   * What this client is: "web" (default), "app", "cli", or any short
   * lower-case token. Anything but "web" makes automatic tracking emit
   * $screen_view with the route path as the screen, and exempts the
   * client from the server's crawler filter, which applies to web only.
   */
  kind?: string;
  /**
   * The surface the product is used through ($platform): "web", "ios",
   * "android", "electron", … Defaults to "web" while kind is "web". Any
   * other kind is a wrapper the SDK cannot identify, so it sends no
   * $platform and the server records unknown — set it beside kind.
   */
  platform?: string;
  /**
   * Overrides for detection. Every detected value has one, and an
   * explicit option always beats detection. os, browser and device are
   * closed lower-case vocabularies (docs/twillingate.md); osName is the
   * full self-reported name with version.
   */
  os?: string;
  osVersion?: string;
  osName?: string;
  browser?: string;
  browserVersion?: string;
  device?: string;
```

Class fields — replace `private os: string | null = null;` with:

```ts
  private platform: string | null = null;
  private env: Partial<OSInfo & BrowserInfo & DeviceInfo> = {};
```

In `init`, replace `this.os = opts.os || opts.platform || null;` with:

```ts
    this.platform = opts.platform || (this.kind === "web" ? "web" : null);
    this.env = {
      os: opts.os || undefined, osVersion: opts.osVersion || undefined, osName: opts.osName || undefined,
      browser: opts.browser || undefined, browserVersion: opts.browserVersion || undefined,
      device: opts.device || undefined,
    };
    // The one async detection input; read at flush time, not awaited.
    primePlatformVersion();
```

(`this.kind` is assigned on the line above, so the platform default sees the validated kind.)

`batchAttributes` — replace `if (this.os) a.$os = this.os;` with:

```ts
    // Detection runs per flush: an explicit option beats it, and the
    // three always-resolved values are sent on every batch so the server
    // can tell "declared unknown" from "sent nothing".
    const d = detectAll();
    const e = this.env;
    if (this.platform) a.$platform = this.platform;
    a.$os = e.os || d.os;
    const osVersion = e.osVersion || d.osVersion;
    if (osVersion) a.$os_version = osVersion;
    const osName = e.osName || d.osName;
    if (osName) a.$os_name = osName;
    a.$browser = e.browser || d.browser;
    const browserVersion = e.browserVersion || d.browserVersion;
    if (browserVersion) a.$browser_version = browserVersion;
    a.$device = e.device || d.device;
```

Methods, placed after `flush`:

```ts
  /**
   * Pure detection, exposed so a page can see what the SDK would send:
   * twillingate.detectOS() in the console. Omit the argument to read the
   * ambient navigator; pass a ClientSignals to answer for exactly those
   * signals and nothing else. Overrides given to init() are deliberately
   * ignored here — this answers for the signals, the batch carries the
   * option.
   */
  detectOS(signals?: ClientSignals): OSInfo {
    return detectOSFrom(signals || ambientSignals());
  }

  detectBrowser(signals?: ClientSignals): BrowserInfo {
    return detectBrowserFrom(signals || ambientSignals());
  }

  detectDevice(signals?: ClientSignals): DeviceInfo {
    return detectDeviceFrom(signals || ambientSignals());
  }
```

`autoInit` — after `kind:` add the seven attributes:

```ts
    kind: script.getAttribute("data-kind") || undefined,
    platform: script.getAttribute("data-platform") || undefined,
    os: script.getAttribute("data-os") || undefined,
    osVersion: script.getAttribute("data-os-version") || undefined,
    osName: script.getAttribute("data-os-name") || undefined,
    browser: script.getAttribute("data-browser") || undefined,
    browserVersion: script.getAttribute("data-browser-version") || undefined,
    device: script.getAttribute("data-device") || undefined,
    appVersion: script.getAttribute("data-app-version") || undefined,
```

Also fix the stale header comment in `twillingate.ts` if it still says the server parses the User-Agent, and the `kind` doc comment already rewritten above.

- [ ] **Step 4: Run the SDK suite, typecheck, build**

Run: `cd sdk && npm test && npm run typecheck && npm run build && git status --short internal/server/twillingate.js`
Expected: all green; `internal/server/twillingate.js` modified.

- [ ] **Step 5: Docs — the SDK contract**

`internal/api/docs_sync_test.go` `TestDocumentMatchesSDK` symbol list — extend:

```go
		"data-kind", "data-platform", "data-os", "data-os-version", "data-os-name",
		"data-browser", "data-browser-version", "data-device", "data-app-version",
		"init", "page", "screen", "track", "attrs", "identify", "group", "reset", "flush",
		"detectOS", "detectBrowser", "detectDevice", "ClientSignals",
		"twillingate_ignore", "analytics_ignore",
		"pushState", "popstate", "hashchange",
		"$page_view", "$screen_view", "$install_id", "$kind", "$platform", "$os", "$os_name",
		"$browser", "$browser_version", "$device",
		"$display_width", "$display_height",
```

`docs/twillingate.md` snippet table — replace the `data-kind`, `data-os`, `data-app-version` rows with:

```
| `data-kind` | `kind` | What this client is: `web` (default), `app`, `cli`, or any short lower-case token. Anything but `web` switches automatic tracking from `$page_view` to `$screen_view` (the route path becomes the screen) and exempts the client from the server's crawler filter, which applies to `web` only. |
| `data-platform` | `platform` | The surface the product is used through (`$platform`): `web`, `ios`, `android`, `electron`, … Defaults to `web` only while `kind` is `web`; any other kind is a wrapper the SDK cannot name, so set it beside `data-kind` or the server records `unknown`. |
| `data-os`, `data-os-version`, `data-os-name` | `os`, `osVersion`, `osName` | Override the detected operating system (`$os`, `$os_version`, `$os_name`). See [Detection](#detection); an explicit value always beats detection. |
| `data-browser`, `data-browser-version` | `browser`, `browserVersion` | Override the detected browser (`$browser`, `$browser_version`). |
| `data-device` | `device` | Override the detected form factor (`$device`). `wearable` is reachable only this way. |
| `data-app-version` | `appVersion` | The version of the client application — a site build, an app release, a CLI version. |
```

SDK-only example — replace the `kind:`, `os:` lines with:

```js
  // client context, sent as batch attributes:
  kind: "app",                 // → $kind ("app" for Electron/Tauri, "cli", …)
  platform: "electron",        // → $platform; defaults to "web" only for kind "web"
  os: "macos",                 // → $os, overriding detection (usually unnecessary)
```

Add after the Runtime API block (before `### Masking URLs`):

```
### Detection

The SDK detects the operating system, browser and form factor on the
client and sends them on every batch — `$os`, `$browser` and `$device`
always, `$os_version`, `$os_name` and `$browser_version` when it can
determine them — because two things a User-Agent cannot tell are exactly
the ones worth knowing: iPadOS in desktop mode (separable only by
`maxTouchPoints`) and Brave (identical to Chrome except for
`navigator.brave`). Windows 11 and true macOS versions come from
`navigator.userAgentData.getHighEntropyValues`, started at `init()` and
read at flush time, so the first batch already carries them on Chromium.

Detection is public API, so a page can see what will be sent:

```js
twillingate.detectOS();       // { os: "ipados", osVersion: "17.2", osName: "iPadOS 17.2" }
twillingate.detectBrowser();  // { browser: "brave", browserVersion: "126" }
twillingate.detectDevice();   // { device: "tablet" }
```

Each takes an optional `ClientSignals` — a flat list of the only inputs
detection reads (`userAgent`, `platform`, `maxTouchPoints`, `brave`,
`brands`, `uaPlatform`, `mobile`, `platformVersion`) — and when one is
supplied consults **only** what it contains, so
`twillingate.detectBrowser({ userAgent })` answers for that User-Agent and
nothing else. The type is the whole record of what the SDK touches on the
device. These are pure detection: an `os`, `browser` or `device` option
passed to `init()` overrides what a batch carries but does not change
what `detect*` returns.

Detection returns `other` for a User-Agent that names nothing on the
list and `unknown` when there is no User-Agent at all — a non-browser
runtime — and never returns the declare-only values `watchos`,
`visionos` and `wearable`. `$platform` is never detected: it is the
option, or `web` while `kind` is `web`, or absent.
```

- [ ] **Step 6: Run the doc binding and the Go server tests**

Run: `cd internal/api && go test -run 'TestDocument' ./ && cd ../server && go test ./`
Expected: PASS (the server serves the rebuilt bundle; its script tests read the embedded file).

- [ ] **Step 7: Commit**

```bash
git add sdk/src internal/server/twillingate.js internal/api/docs_sync_test.go docs/twillingate.md
git commit -m "feat(sdk)!: declare platform and send the detected os, browser and device on every batch"
```

---

### Task 7: Evidence, the deployment page and the smoke scripts

Implements the operator half of spec "Breaking changes and rollout" and the one dashboard query that reads the rekeyed view. No Evidence page for platforms (spec: out of scope).

**Files:**
- Modify: `evidence/pages/views/[project].md` (~line 191, the `app_versions` query)
- Modify: `evidence/sources/twillingate/v_views_app_versions.sql`
- Modify: `docs/deployment.md` — the "Upgrade across a schema change" row (~line 553) and a new section after "Upgrading to integer project ids (migration 014)" (~line 608)
- Check only: `scripts/smoke.sh:55`, `scripts/test-compose.sh:73` (they send `$os: ios`, which is a valid value; no change required)

- [ ] **Step 1: The Evidence source and page**

`evidence/sources/twillingate/v_views_app_versions.sql` — the select becomes `select project_id, day, platform, app_version, visitors, views` (the sentinel row keeps six columns).

`evidence/pages/views/[project].md` `app_versions` query — `select day, platform || ' ' || app_version as version, visitors`.

The `oses`, `browsers` and `devices` queries keep their `!= ''` filters: values are never empty now, so the filters are no-ops, and dropping them is churn.

- [ ] **Step 2: Build the dashboards against a migrated database and an empty one**

```bash
make build
DATABASE_DSN=sqlite://local/twillingate.db ./twillingate migrate     # seed db, now at 015
DATABASE_DSN=sqlite://local/empty/t.db ./twillingate migrate         # empty db, now at 015
cd evidence
EVIDENCE_SOURCE__twillingate__filename=../../../local/twillingate.db npm run sources && npm run build
EVIDENCE_SOURCE__twillingate__filename=../../../local/empty/t.db npm run sources && npm run build
```

The filename is resolved relative to `evidence/sources/twillingate/` (see `connection.yaml`), which is why it starts with `../../..`; `make dashboards` uses the same variable.

Expected: both builds succeed; `build/views/1/index.html` exists for the seeded db.

- [ ] **Step 3: The deployment page**

`docs/deployment.md` — in the routine-operations table, the "Upgrade across a schema change" row becomes:

```
| Upgrade across a schema change | Take a Litestream snapshot first (`litestream snapshots …`, or copy the db file while the service is stopped): migrations such as 012 (web and app folded into one views family), 014 (integer project ids) and 015 (declared environment) are irreversible |
```

After the 014 section, add:

```
### Upgrading to the declared environment (migration 015)

From this migration the client declares its operating system, browser
and device class and the server only validates them, against closed
lower-case vocabularies; the User-Agent is read for nothing but the
crawler drop. `platform` becomes its own column, aggregate and breakdown
dimension, and `v_views_app_versions` is keyed by it instead of `os`.
Before upgrading, run these against the live database:

```sql
-- 1. Two spellings of one OS on one aggregate key. Any hit aborts
--    migration 015 (lower-casing them would collide) and leaves the
--    database at 014. Merge or delete the duplicate by hand first.
SELECT project_id, day, lower(os), os_version, COUNT(*) FROM agg_views_os
GROUP BY 1, 2, 3, 4 HAVING COUNT(*) > 1;
SELECT project_id, day, event_name, lower(attr_value), COUNT(*) FROM agg_product_attrs
WHERE attr_key = '$os' GROUP BY 1, 2, 3, 4 HAVING COUNT(*) > 1;

-- 2. OS values outside the vocabulary. Raw rows fold to 'other' and keep
--    the original in os_name; aggregate history keeps these spellings as
--    they are, so decide now whether any deserves a client-side fix.
SELECT os, COUNT(*) FROM views WHERE lower(os) NOT IN (
  'windows','macos','linux','bsd','chromeos','ios','ipados','android','fireos','harmonyos','kaios',
  'tvos','watchos','visionos','tizen','webos','playstation','xbox','nintendo','other','unknown')
GROUP BY os;
```

Then stop the service, copy the database (or take a Litestream snapshot),
run the installer, and check `journalctl` for migration 015.

What changes on the day:

- **Every stored `os`, `browser` and `device` value is lower-case**
  (`ios`, `chrome`, `samsung_internet`), rows that were empty are
  `unknown`, and `v_views_app_versions` has a `platform` column where
  `os` was. Saved SQL and dashboards comparing against `'iOS'` or
  `'Chrome'`, or selecting `os` from the app-versions view, return
  nothing until rewritten.
- **Clients that are not the served JS SDK** — a backend relay, a native
  app, a custom SDK — record `unknown` for OS, browser and device until
  they declare `$os`, `$browser` and `$device` (and `$platform`). Snippet
  sites pick up detection with the upgrade because the collector serves
  the SDK; bundled or npm consumers when they update.
- **Deployed app clients that sent `$platform` as an alias for `$os`**
  lose their OS dimension until they ship an update that sends both; their
  platform dimension starts working at once.
- iPad traffic moves from `ios` to `ipados`, Brave from `chrome` to
  `brave`, and consoles and TVs from `desktop` to `other`, so those
  series step on the upgrade day.
- `agg_views_app_versions` history is rekeyed by `platform = lower(os)`,
  which is value-preserving; going forward, versions from clients that
  do not yet send `$platform` roll up under `unknown` until they update.
```

- [ ] **Step 4: Run the deployment doc binding**

Run: `cd internal/api && go test -run 'TestDeployment' ./`
Expected: PASS (no environment variable changed; the section is prose).

- [ ] **Step 5: Commit**

```bash
git add evidence docs/deployment.md
git commit -m "docs(deploy): describe the migration 015 upgrade and key the app-versions dashboard by platform"
```

---

### Task 8: Whole-tree verification, spec status, PR

- [ ] **Step 1: `make check`** (about 15 minutes; run in the background with a 30-minute timeout)

Run: `make check 2>&1 | tee /tmp/make-check-015.log | tail -20`
Expected: vet clean, every coverage gate ≥ 90% (`store`, `enrich`, `pipeline`, `identity`, `config`, `manage`, `api`), restore test PASS.

- [ ] **Step 2: SDK drift check, as CI does it**

Run: `cd sdk && npm ci && npm run typecheck && npm test && npm run build && git diff --exit-code ../internal/server/twillingate.js`
Expected: no diff — the committed bundle is what the source builds.

- [ ] **Step 3: Smoke**

Run: `make smoke`
Expected: the smoke checker reports the same counts as before this branch (`views=2 events=2` or whatever `scripts/smokecheck` prints); the batch in `scripts/smoke.sh` declares `$os: ios` and sends no `$platform`, so the stored view carries `platform=unknown, os=ios` — assert nothing about that, just that ingest still accepts it.

- [ ] **Step 4: Mark the spec implemented**

In `docs/superpowers/specs/2026-09-20-os-and-platform-design.md` change `Status: proposed` to `Status: implemented (2026-09-21)`.

```bash
git add docs/superpowers/specs/2026-09-20-os-and-platform-design.md
git commit -m "docs: mark the declared-environment spec implemented"
```

- [ ] **Step 5: Push and open the draft PR**

```bash
git push -u origin feat/environment
```

PR title (the squash subject): `feat!: let the client declare its environment`

PR body, following the repo's problem-first convention:

```
## Problems

1. `$platform` is a deprecated alias for `$os`, so "web build vs native build" and "which OS versions must I support" are one column and neither can be answered.
2. Browser and device class exist only as `ParseUserAgent` output. Brave ships Chrome's User-Agent on purpose and iPadOS in desktop mode reports as a Mac, so the server counts both wrong and always will.
3. `os` is an open, mixed-case column: `iOS` and `ios` land in different rows, and an empty value is indistinguishable from "declared nothing".

## Changes

- Migration 015: `platform` on `views` and `events`, `os_name` on `views`; `os`, `browser`, `device` folded to closed lower-case vocabularies with `other`/`unknown` floors; `agg_views_platforms` + `v_views_platforms`; `agg_views_app_versions` rekeyed `(platform, app_version)`; `v_product_attrs` gains `$platform`.
- Server validates `$platform`, `$os`, `$os_name`, `$browser`, `$browser_version`, `$device`; warns on unrecognised values; reads the User-Agent only for the crawler drop. `ParseUserAgent` deleted.
- SDK detects OS (with `platformVersion` for Windows 11 / real macOS), browser (Brave, brands) and device (xr, consoles) from a flat `ClientSignals`, exposes `detectOS`/`detectBrowser`/`detectDevice`, sends every environment key on every batch, defaults `platform` to `web` only for kind `web`.
- API: `views_breakdown` gains `platforms`; `app_versions` returns `platform, app_version`; `schema://views` updated.
- Docs: `docs/twillingate.md` (declaring the environment, detection, keys, views), `docs/deployment.md` (015 upgrade, pre-checks).

## Breaking

BREAKING CHANGE: `$platform` no longer fills `os`; `os`, `browser` and `device` are lower-case closed vocabularies (`iOS` → `ios`, `Samsung Internet` → `samsung_internet`, empty → `unknown`); `v_views_app_versions` is keyed by `platform` not `os`; the server no longer derives OS, browser or device from the User-Agent, so any client other than the served JS SDK records `unknown` until it declares them.

## Test plan

- [ ] `make check` green (coverage gates)
- [ ] SDK: `npm test`, `npm run typecheck`, bundle rebuilt with no drift
- [ ] Evidence builds against a migrated seed db and an empty db
- [ ] `make smoke`
- [ ] Prod: run the two pre-checks in docs/deployment.md, snapshot, install, verify migration 015 in journalctl and `views_breakdown platforms`

Closes #38 (spec PR, subsumed).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
```

Create it as a draft with `gh pr create --draft --title … --body-file …`.

---

## Self-review notes

**Spec coverage.** Decisions 1–13 map to: 1, 3, 4, 7 → Tasks 2 and 3; 2 → Task 3 (`$platform` never reaches `OS`, asserted by `TestPlatformIsValidatedIndependentlyOfOS`); 5, 11, 13 → Tasks 1 and 3; 6 → Tasks 2 and 4; 8 → Tasks 2 and 3 (`os_name` column, copy rule, product-event drop); 9, 12 → Tasks 5 and 6; 10 → Tasks 1, 2, 3. Storage §1–§8 → Task 2 in order. "Interaction with the existing OS aggregate" needs no code (noted in Task 2's `viewDimensions` comment only through the `platform` comment; nothing to change). "Reporting" table → Tasks 2, 3, 4. "JS SDK" → Tasks 5, 6. "Documentation" → Tasks 3, 4, 6, 7. "Testing" → every bullet has a named test above. "Breaking changes and rollout" → Task 7 (deployment page) and Task 8 (PR footer). "The plausible shim needs no change" → nothing to do; `TestPlausibleShimServed` keeps binding the bytes.

**Type consistency.** `enrich.Normalize*` return `(string, bool)` everywhere they are called (Task 3). `store.View.Platform/OSName` and `store.ProductEvent.Platform` are the names used in `write.go`, the server, and every fixture. `viewsDimensions` keys `platforms`/`app_versions` match `schemaViews`, the docs table and the tests. SDK: `detectAll`, `ambientSignals`, `primePlatformVersion`, `resetPlatformVersion` are exported from `detect.ts` and imported by name in `twillingate.ts` and the tests; `ClientSignals` is re-exported so `TestDocumentMatchesSDK` finds the string in `twillingate.ts`.

**Known judgement calls (not in the spec, decided here).**
- The store writes `Platform`/`OS`/`Browser`/`Device` as given; the never-empty guarantee is enforced by the server, the only writer. Store-level fixtures set the fields explicitly.
- `FxiOS/` (Firefox on iOS) is added to the UA browser markers; the spec's list omits it and it would otherwise read as `safari`.
- Migration 015 aborts (whole transaction) if lower-casing `agg_views_os` or the `$os` rows of `agg_product_attrs` would collide. That is loud and safe; the deployment page's pre-check finds such rows first.
- Product events warn about an unrecognised `$os`/`$platform` (columns they carry) but not about `$browser`/`$device` (which they drop).
