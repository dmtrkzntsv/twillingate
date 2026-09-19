# Views Family Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge the web (`$pageview` → `web_hits`) and app (`$screen_view` → `app_views`) families into one `views` family with a client-declared free-form `kind`, leaving product events alone except for two columns.

**Architecture:** One raw table `views`, one aggregator `AggregateViewDay`, eleven `v_views_*` stitch views, two MCP/REST tools, one Evidence page. Kind is a value on the row (`web`, `app`, `cli`, …); only `web` is User-Agent parsed and bot-filtered. Retention keys on how the actor was identified (`actor_kind`), recorded at ingest. Migration 012 folds both old families and their aggregates into the new tables in one transaction.

**Tech Stack:** Go 1.2x (modernc sqlite, MCP go-sdk), SQLite migrations embedded from `internal/store/sqlite/migrations/`, TypeScript SDK built with esbuild + tested with vitest, Evidence dashboards (markdown + DuckDB SQL).

**Spec:** `docs/superpowers/specs/2026-09-17-views-family-design.md` — read it first; every task below cites the section it implements.

## Global Constraints

- Package layering is enforced by `internal/archtest`; no new imports that point up or sideways (CLAUDE.md "Layout").
- Every commit uses Conventional Commits; the final squash subject is `feat!: merge web and app analytics into one views family`. Intermediate commits on the branch may be `feat(store): …`, `refactor(api): …` etc.
- `docs/twillingate.md` and `docs/deployment.md` change in the same commit as the code they describe (CLAUDE.md "Documentation"). `internal/api/docs_sync_test.go` enforces reserved keys, tool names + count, env vars, SDK symbols, view names and REST routes.
- Names, verbatim from spec §3–§4: events `$page_view` / `$screen_view` (alias `$pageview`); keys `$kind` `$os` (alias `$platform`) `$os_version` `$app_version` `$device_model` `$locale` `$display_width` `$display_height` `$host` `$path` `$screen` `$utm_source` `$utm_medium` `$utm_campaign` `$referrer` plus the six identity keys; tables `views`, `agg_views_*`; views `v_views_*`; tools `views_overview`, `views_breakdown`; env `RETENTION_VIEWS_RAW_DAYS` (30) `RETENTION_VIEWS_AGGREGATE_DAYS` (365); dimensions `kinds, paths, hosts, referrers, utm, countries, os, browsers, app_versions, devices, displays`; retention parameter `actor` ∈ {`user`,`install`}.
- Kind pattern: `^[a-z][a-z0-9_]{0,15}$`. Dimension cap: 500 per day, trailing key collapses into `(other)`.
- Go toolchain lives at `/usr/local/go/bin` on the dev box (not on PATH): prefix commands with `PATH=/usr/local/go/bin:$PATH` or export it once. `make check` needs `sqlite3` for the restore step, which is missing locally — run `make vet && ./scripts/coverage.sh` locally and let CI run the restore step.
- Run Go tests from the repo root with `go test ./internal/...`; the SDK from `sdk/` with `npm test`; rebuild the bundle with `npm run build` in `sdk/` and commit `internal/server/twillingate.js` (CI diffs it).
- Never edit migrations 001–011. All schema change is `012_views.sql`.

## File map

| Area | Create | Modify | Delete |
|---|---|---|---|
| enrich | — | `internal/enrich/ua.go`, `ua_test.go` | — |
| store | `internal/store/sqlite/migrations/012_views.sql`, `internal/store/sqlite/aggregate_views.go`, `aggregate_views_test.go`, `migration012_test.go` | `internal/store/store.go`, `sqlite/write.go`, `sqlite/prune.go`, `sqlite/retention.go`, `sqlite/identities.go`, `sqlite/registry.go`, `sqlite/migrate.go`, `sqlite/aggregate_product.go`, tests | `sqlite/aggregate_web.go`, `sqlite/aggregate_app.go` and their tests |
| config | — | `internal/config/config.go`, `config_test.go`, `internal/manage/registry.go` | — |
| jobs | — | `internal/jobs/jobs.go`, `jobs_test.go`, `errors_test.go` | — |
| pipeline | — | `internal/pipeline/pipeline.go`, `pipeline_test.go` | — |
| server | — | `internal/server/ingest.go`, `handlers.go`, `server.go` (Enqueuer iface), tests | — |
| api | — | `internal/api/ops_read.go`, `ops_product.go`, `resources.go`, `guide.go`, `docs_sync_test.go`, `seed_test.go`, `ops_read_test.go`, `rest_test.go`, `guide_test.go` | — |
| docs | — | `docs/twillingate.md`, `docs/deployment.md`, `docs/plausible/README.md` | — |
| sdk | — | `sdk/src/twillingate.ts`, tests, `internal/server/twillingate.js` (rebuilt) | — |
| evidence | `evidence/pages/views/[project].md`, `evidence/pages/views/[project]/page.md`, `evidence/sources/twillingate/v_views_*.sql` (11) | `pages/index.md`, `pages/retention/[project].md`, `pages/users/[project].md`, `pages/groups/[project].md` | `pages/web/**`, `pages/app/**`, `sources/twillingate/v_web_*.sql` (9), `v_app_*.sql` (6) |
| scripts | — | `scripts/seed-demo.py`, `scripts/smoke.sh`, `scripts/test-compose.sh`, `scripts/smokecheck/main.go` | — |

Task order matters: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12 → 13. Go packages compile against each other, so the branch only builds again after Task 8; tasks 2–7 each run their own package tests, which is enough for the gate.

---

### Task 1: User-Agent parser — browser version and OS normalisation

Spec §5 (steps 4–5, "OS normalisation").

**Files:**
- Modify: `internal/enrich/ua.go`
- Test: `internal/enrich/ua_test.go`

**Interfaces:**
- Produces: `func ParseUserAgent(ua string) (device, browser, browserVersion, os string)` (four returns, was three) and `func NormalizeOS(v string) string`.

- [ ] **Step 1: Write the failing tests**

Replace `TestParseUserAgent` in `internal/enrich/ua_test.go` with a four-value version and add `TestNormalizeOS`:

```go
func TestParseUserAgent(t *testing.T) {
	cases := []struct{ ua, device, browser, version, os string }{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			"desktop", "Chrome", "126", "Windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
			"desktop", "Safari", "17", "macOS"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0",
			"desktop", "Firefox", "127", "Linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			"mobile", "Safari", "17", "iOS"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
			"mobile", "Chrome", "126", "Android"},
		{"Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			"tablet", "Safari", "17", "iOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0",
			"desktop", "Edge", "126", "Windows"},
		{"Mozilla/5.0 (Linux; Android 13; SM-G991B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36",
			"mobile", "Samsung Internet", "25", "Android"},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 OPR/112.0.0.0",
			"desktop", "Opera", "112", "Linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/126.0.6478.54 Mobile/15E148 Safari/604.1",
			"mobile", "Chrome", "126", "iOS"},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			"desktop", "Chrome", "126", "ChromeOS"},
		{"weird thing", "desktop", "", "", ""},
	}
	for _, c := range cases {
		d, b, v, o := ParseUserAgent(c.ua)
		if d != c.device || b != c.browser || v != c.version || o != c.os {
			t.Errorf("%q => (%s,%s,%s,%s), want (%s,%s,%s,%s)", c.ua, d, b, v, o, c.device, c.browser, c.version, c.os)
		}
	}
}

func TestNormalizeOS(t *testing.T) {
	cases := map[string]string{
		"ios": "iOS", "IOS": "iOS", "android": "Android", "macos": "macOS", "MacOS": "macOS",
		"windows": "Windows", "linux": "Linux", "chromeos": "ChromeOS",
		"iOS": "iOS", "": "", "haiku": "haiku", " ios ": "iOS",
	}
	for in, want := range cases {
		if got := NormalizeOS(in); got != want {
			t.Errorf("NormalizeOS(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/enrich/ -run 'TestParseUserAgent|TestNormalizeOS'`
Expected: compile error "assignment mismatch: 4 variables but ParseUserAgent returns 3 values" and "undefined: NormalizeOS".

- [ ] **Step 3: Implement**

In `internal/enrich/ua.go`, change the signature and add the version extraction after the browser switch, and add `NormalizeOS`:

```go
// ParseUserAgent derives coarse device class, browser name, browser major
// version and OS name. Only substring matching; order matters (see inline).
func ParseUserAgent(ua string) (device, browser, browserVersion, os string) {
	// OS first — some browser checks depend on it.
	switch {
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Mac OS X"):
		os = "macOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	// Browser: check derivatives before their bases (Edge/Samsung before
	// Chrome, Chrome before Safari — every Chrome UA contains "Safari").
	// The marker that named the browser is also where its version starts.
	var marker string
	switch {
	case strings.Contains(ua, "Edg/"):
		browser, marker = "Edge", "Edg/"
	case strings.Contains(ua, "Edge/"):
		browser, marker = "Edge", "Edge/"
	case strings.Contains(ua, "SamsungBrowser/"):
		browser, marker = "Samsung Internet", "SamsungBrowser/"
	case strings.Contains(ua, "OPR/"):
		browser, marker = "Opera", "OPR/"
	case strings.Contains(ua, "Opera/"):
		browser, marker = "Opera", "Opera/"
	case strings.Contains(ua, "Firefox/"):
		browser, marker = "Firefox", "Firefox/"
	case strings.Contains(ua, "Chrome/"):
		browser, marker = "Chrome", "Chrome/"
	case strings.Contains(ua, "CriOS/"):
		browser, marker = "Chrome", "CriOS/"
	case strings.Contains(ua, "Safari/"):
		// Safari carries its version under Version/, not Safari/ (that is
		// the WebKit build number).
		browser, marker = "Safari", "Version/"
	}
	browserVersion = majorAfter(ua, marker)
	switch {
	case strings.Contains(ua, "iPad"), strings.Contains(ua, "Tablet"):
		device = "tablet"
	case strings.Contains(ua, "Mobile"), strings.Contains(ua, "iPhone"):
		device = "mobile"
	default:
		device = "desktop"
	}
	return device, browser, browserVersion, os
}

// majorAfter returns the run of digits that follows marker, or "" when the
// marker is absent or not followed by a digit.
func majorAfter(ua, marker string) string {
	if marker == "" {
		return ""
	}
	i := strings.Index(ua, marker)
	if i < 0 {
		return ""
	}
	rest := ua[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	return rest[:end]
}

// osNames maps the lower-cased tokens apps declare in $os to the names the
// parser produces, so a declared "ios" and a parsed iPhone land in one row.
var osNames = map[string]string{
	"ios": "iOS", "android": "Android", "macos": "macOS",
	"windows": "Windows", "linux": "Linux", "chromeos": "ChromeOS",
}

// NormalizeOS folds a declared OS token into the parser's vocabulary. An
// unknown value is stored as sent: the vocabulary is a convenience, not an
// allowlist.
func NormalizeOS(v string) string {
	v = strings.TrimSpace(v)
	if name, ok := osNames[strings.ToLower(v)]; ok {
		return name
	}
	return v
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/enrich/`
Expected: PASS. (Other packages will not compile until Task 8 — that is expected; do not run `go build ./...` yet.)

- [ ] **Step 5: Commit**

```bash
git add internal/enrich
git commit -m "feat(enrich): parse the browser major version and normalise declared OS names"
```

---

### Task 2: Store types, migration 012 and the write path

Spec §6, §11. This task lands the schema and the row type; the aggregator (Task 3), retention (Task 4) and the fold test (Task 5) build on it. The `sqlite` package will not compile until Task 3 removes the old aggregators, so this task's verification is the schema-level tests only after Task 3 — commit at the end of Task 3 if you prefer one green commit; the step list below still commits per task for reviewability, accepting that `go test ./internal/store/...` first passes in Task 3.

**Files:**
- Modify: `internal/store/store.go`
- Create: `internal/store/sqlite/migrations/012_views.sql`
- Modify: `internal/store/sqlite/write.go`, `internal/store/sqlite/registry.go` (projectTables), `internal/store/sqlite/prune.go`, `internal/store/sqlite/aggregate_product.go` (systemDims + column), `internal/store/sqlite/sqlite_test.go`, `internal/store/sqlite/write_test.go`
- Delete: `internal/store/sqlite/aggregate_web.go`, `aggregate_app.go`, `aggregate_web_test.go`, `aggregate_app_test.go` (their still-needed helpers move in Task 3)

**Interfaces:**
- Produces: `store.View` (below), `store.ProductEvent{…, OS, AppVersion, ActorKind}` (Platform renamed OS), constants `store.ActorUser/ActorInstall/ActorConnection`, `Store.WriteViews`, `Store.ViewDaysBefore`, `Store.AggregateViewDay`, `Store.PruneAggregates(ctx, project, viewsBefore, productBefore)`.

- [ ] **Step 1: Row types and interface in `internal/store/store.go`**

Replace `WebHit` and `AppView` with:

```go
// View is one page or screen view of any kind ($page_view, $screen_view).
// Kind is the client-declared surface ("web", "app", "cli", …); only "web"
// rows are enriched from the User-Agent. ActorKind records how the actor
// was identified and is what retention cohorts on.
type View struct {
	ID, Project                                    string
	TS, ReceivedAt                                 time.Time
	Kind                                           string
	ActorID, ActorKind, UserID, GroupID, SessionID string
	Host, Path, ReferrerSource                     string
	UTMSource, UTMMedium, UTMCampaign              string
	OS, OSVersion, Browser, BrowserVersion         string
	AppVersion, Device, DeviceModel, Locale        string
	DisplayWidth, DisplayHeight                    int
	Country                                        string
}

// ProductEvent represents a custom event from any surface.
type ProductEvent struct {
	ID, Project, EventName   string
	TS, ReceivedAt           time.Time
	ActorID, ActorKind       string
	UserID, GroupID          string
	OS, AppVersion           string
	Attributes               map[string]string
}

// Actor kinds: how an actor id was derived. Only user and install actors
// are stable enough to cohort; a connection hash rotates with the salt.
const (
	ActorUser       = "user"
	ActorInstall    = "install"
	ActorConnection = "connection"
)
```

In the `Store` interface replace the five web/app methods and `PruneAggregates` with:

```go
	WriteViews(ctx context.Context, views []View) error
	WriteProductEvents(ctx context.Context, evs []ProductEvent) error
	UpsertIdentities(ctx context.Context, ids []Identity) error
	ViewDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	AggregateViewDay(ctx context.Context, project string, day civil.Date) error
	AggregateProductDay(ctx context.Context, project string, day civil.Date, attrs []string, topN int) error
	UpsertActors(ctx context.Context, project string, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, project string, day civil.Date) error
	PruneActors(ctx context.Context, project string, before civil.Date) error
	AggregateIdentityDay(ctx context.Context, project string, day civil.Date) error
	PruneIdentities(ctx context.Context, project string, before civil.Date) error
	PruneAggregates(ctx context.Context, project string, viewsBefore, productBefore civil.Date) error
```

(`WriteWebHits`, `WriteAppViews`, `WebDaysBefore`, `AppDaysBefore`, `AggregateWebDay`, `AggregateAppDay` are gone.)

- [ ] **Step 2: Write `012_views.sql`**

Create `internal/store/sqlite/migrations/012_views.sql`. The runner wraps the whole file in one transaction (`migrate.go`), so a failure midway leaves the old schema intact.

```sql
-- One views family (views-family spec §6, §11). web_hits and app_views fold
-- into `views`; every agg_web_*/agg_app_* folds into agg_views_*; retention
-- and identity aggregates lose their surface/hits split. Irreversible.
--
-- Views are dropped first and recreated at the end, as 003 did: RENAME
-- COLUMN must not leave a stale view definition behind.

DROP VIEW IF EXISTS v_web_daily;
DROP VIEW IF EXISTS v_web_pages;
DROP VIEW IF EXISTS v_web_hosts;
DROP VIEW IF EXISTS v_web_referrers;
DROP VIEW IF EXISTS v_web_countries;
DROP VIEW IF EXISTS v_web_devices;
DROP VIEW IF EXISTS v_web_browsers;
DROP VIEW IF EXISTS v_web_os;
DROP VIEW IF EXISTS v_web_utm;
DROP VIEW IF EXISTS v_app_daily;
DROP VIEW IF EXISTS v_app_screens;
DROP VIEW IF EXISTS v_app_versions;
DROP VIEW IF EXISTS v_app_os;
DROP VIEW IF EXISTS v_app_devices;
DROP VIEW IF EXISTS v_app_countries;
DROP VIEW IF EXISTS v_product_daily;
DROP VIEW IF EXISTS v_product_totals;
DROP VIEW IF EXISTS v_product_attrs;
DROP VIEW IF EXISTS v_identity_daily;
DROP VIEW IF EXISTS v_retention;
DROP VIEW IF EXISTS v_events_flat;

-- ===== raw: views =====
CREATE TABLE views (
    id              TEXT PRIMARY KEY,
    project         TEXT NOT NULL,
    ts              TEXT NOT NULL,
    received_at     TEXT NOT NULL,
    kind            TEXT NOT NULL,
    actor_id        TEXT NOT NULL,
    actor_kind      TEXT NOT NULL,
    user_id         TEXT NOT NULL DEFAULT '',
    group_id        TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    host            TEXT NOT NULL DEFAULT '',
    path            TEXT NOT NULL,
    referrer_source TEXT NOT NULL DEFAULT '',
    utm_source      TEXT NOT NULL DEFAULT '',
    utm_medium      TEXT NOT NULL DEFAULT '',
    utm_campaign    TEXT NOT NULL DEFAULT '',
    os              TEXT NOT NULL DEFAULT '',
    os_version      TEXT NOT NULL DEFAULT '',
    browser         TEXT NOT NULL DEFAULT '',
    browser_version TEXT NOT NULL DEFAULT '',
    app_version     TEXT NOT NULL DEFAULT '',
    device          TEXT NOT NULL DEFAULT '',
    device_model    TEXT NOT NULL DEFAULT '',
    locale          TEXT NOT NULL DEFAULT '',
    display_width   INTEGER NOT NULL DEFAULT 0,
    display_height  INTEGER NOT NULL DEFAULT 0,
    country         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_views_project_ts ON views(project, ts);
CREATE INDEX idx_views_actor      ON views(project, actor_id, ts);
CREATE INDEX idx_views_session    ON views(project, session_id, ts);

-- Old raw rows: web rows were identified by user id or the connection
-- hash; app rows by user id or install id. Declared platform tokens fold
-- into the parser's OS vocabulary (enrich.NormalizeOS).
INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind,
    user_id, group_id, session_id, host, path, referrer_source,
    utm_source, utm_medium, utm_campaign, os, os_version, browser,
    browser_version, app_version, device, device_model, locale,
    display_width, display_height, country)
SELECT id, project, ts, received_at, 'web', actor_id,
       CASE WHEN user_id <> '' THEN 'user' ELSE 'connection' END,
       user_id, group_id, '', host, path, referrer_source,
       utm_source, utm_medium, utm_campaign, os, '', browser,
       '', '', device, '', '', 0, 0, country
FROM web_hits;

INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind,
    user_id, group_id, session_id, host, path, referrer_source,
    utm_source, utm_medium, utm_campaign, os, os_version, browser,
    browser_version, app_version, device, device_model, locale,
    display_width, display_height, country)
SELECT id, project, ts, received_at, 'app', actor_id,
       CASE WHEN user_id <> '' THEN 'user' ELSE 'install' END,
       user_id, group_id, session_id, '', screen, '',
       '', '', '',
       CASE lower(platform)
         WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
         WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
         WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
         ELSE platform END,
       os_version, '', '', app_version, '', device_model, locale,
       0, 0, country
FROM app_views;

-- ===== aggregates =====
-- Counts, not rates. visitors = COUNT(DISTINCT actor_id), views = COUNT(*).
CREATE TABLE agg_views_daily (
    project TEXT NOT NULL, day TEXT NOT NULL, kind TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    sessions INTEGER NOT NULL, bounces INTEGER NOT NULL, duration_sec INTEGER NOT NULL,
    PRIMARY KEY (project, day, kind)
) WITHOUT ROWID;
CREATE TABLE agg_views_paths (
    project TEXT NOT NULL, day TEXT NOT NULL, path TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, path)
) WITHOUT ROWID;
CREATE TABLE agg_views_hosts (
    project TEXT NOT NULL, day TEXT NOT NULL, host TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, host)
) WITHOUT ROWID;
CREATE TABLE agg_views_referrers (
    project TEXT NOT NULL, day TEXT NOT NULL, source TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, source)
) WITHOUT ROWID;
CREATE TABLE agg_views_utm (
    project TEXT NOT NULL, day TEXT NOT NULL,
    utm_source TEXT NOT NULL, utm_medium TEXT NOT NULL, utm_campaign TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, utm_source, utm_medium, utm_campaign)
) WITHOUT ROWID;
CREATE TABLE agg_views_countries (
    project TEXT NOT NULL, day TEXT NOT NULL, country TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, country)
) WITHOUT ROWID;
CREATE TABLE agg_views_os (
    project TEXT NOT NULL, day TEXT NOT NULL, os TEXT NOT NULL, os_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, os, os_version)
) WITHOUT ROWID;
CREATE TABLE agg_views_browsers (
    project TEXT NOT NULL, day TEXT NOT NULL, browser TEXT NOT NULL, browser_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, browser, browser_version)
) WITHOUT ROWID;
CREATE TABLE agg_views_app_versions (
    project TEXT NOT NULL, day TEXT NOT NULL, os TEXT NOT NULL, app_version TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, os, app_version)
) WITHOUT ROWID;
CREATE TABLE agg_views_devices (
    project TEXT NOT NULL, day TEXT NOT NULL, device TEXT NOT NULL, device_model TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, device, device_model)
) WITHOUT ROWID;
CREATE TABLE agg_views_displays (
    project TEXT NOT NULL, day TEXT NOT NULL, display TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, display)
) WITHOUT ROWID;

-- Fold the two old families. Sums are exact: the two populations had
-- disjoint actor ids, so distinct-visitor counts add. App days carry zero
-- bounces (the old family never counted them).
INSERT INTO agg_views_daily (project, day, kind, visitors, views, sessions, bounces, duration_sec)
SELECT project, day, 'web', visitors, pageviews, sessions, bounces, duration_sec FROM agg_web_daily;
INSERT INTO agg_views_daily (project, day, kind, visitors, views, sessions, bounces, duration_sec)
SELECT project, day, 'app', actives, views, sessions, 0, duration_sec FROM agg_app_daily;

INSERT INTO agg_views_paths (project, day, path, visitors, views)
SELECT project, day, path, SUM(visitors), SUM(views) FROM (
  SELECT project, day, path, visitors, pageviews AS views FROM agg_web_pages
  UNION ALL
  SELECT project, day, screen, actives, views FROM agg_app_screens
) GROUP BY project, day, path;

INSERT INTO agg_views_hosts (project, day, host, visitors, views)
SELECT project, day, host, visitors, pageviews FROM agg_web_hosts;
INSERT INTO agg_views_referrers (project, day, source, visitors, views)
SELECT project, day, source, visitors, pageviews FROM agg_web_referrers;
INSERT INTO agg_views_utm (project, day, utm_source, utm_medium, utm_campaign, visitors, views)
SELECT project, day, utm_source, utm_medium, utm_campaign, visitors, pageviews FROM agg_web_utm;
INSERT INTO agg_views_browsers (project, day, browser, browser_version, visitors, views)
SELECT project, day, browser, '', visitors, pageviews FROM agg_web_browsers;

INSERT INTO agg_views_countries (project, day, country, visitors, views)
SELECT project, day, country, SUM(visitors), SUM(views) FROM (
  SELECT project, day, country, visitors, pageviews AS views FROM agg_web_countries
  UNION ALL
  SELECT project, day, country, actives, views FROM agg_app_countries
) GROUP BY project, day, country;

INSERT INTO agg_views_os (project, day, os, os_version, visitors, views)
SELECT project, day, os, os_version, SUM(visitors), SUM(views) FROM (
  SELECT project, day, os, '' AS os_version, visitors, pageviews AS views FROM agg_web_os
  UNION ALL
  SELECT project, day,
         CASE lower(platform)
           WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
           WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
           WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
           ELSE platform END,
         os_version, actives, views FROM agg_app_os
) GROUP BY project, day, os, os_version;

INSERT INTO agg_views_app_versions (project, day, os, app_version, visitors, views)
SELECT project, day,
       CASE lower(platform)
         WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
         WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
         WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
         ELSE platform END,
       app_version, SUM(actives), SUM(views)
FROM agg_app_versions GROUP BY project, day, 3, app_version;

INSERT INTO agg_views_devices (project, day, device, device_model, visitors, views)
SELECT project, day, device, '', visitors, pageviews FROM agg_web_devices;
INSERT INTO agg_views_devices (project, day, device, device_model, visitors, views)
SELECT project, day, '', device_model, actives, views FROM agg_app_devices;

-- ===== retention: surface -> actor_kind =====
CREATE TABLE actors_new (
    project TEXT NOT NULL, actor_id TEXT NOT NULL,
    actor_kind TEXT NOT NULL,
    first_seen_day TEXT NOT NULL,
    last_seen_day  TEXT NOT NULL,
    PRIMARY KEY (project, actor_id)
) WITHOUT ROWID;
INSERT INTO actors_new SELECT project, actor_id,
       CASE surface WHEN 'app' THEN 'install' ELSE 'user' END,
       first_seen_day, last_seen_day FROM actors;
DROP TABLE actors;
ALTER TABLE actors_new RENAME TO actors;
CREATE INDEX idx_actors_last_seen ON actors(project, last_seen_day);

CREATE TABLE agg_retention_new (
    project TEXT NOT NULL, actor_kind TEXT NOT NULL,
    cohort_day TEXT NOT NULL, day_offset INTEGER NOT NULL,
    actors INTEGER NOT NULL,
    PRIMARY KEY (project, actor_kind, cohort_day, day_offset)
) WITHOUT ROWID;
INSERT INTO agg_retention_new SELECT project,
       CASE surface WHEN 'app' THEN 'install' ELSE 'user' END,
       cohort_day, day_offset, actors FROM agg_retention;
DROP TABLE agg_retention;
ALTER TABLE agg_retention_new RENAME TO agg_retention;

-- ===== identity daily: hits + views -> views =====
CREATE TABLE agg_identity_daily_new (
    project TEXT NOT NULL, day TEXT NOT NULL,
    kind TEXT NOT NULL, id TEXT NOT NULL,
    actors INTEGER NOT NULL, users INTEGER NOT NULL,
    views INTEGER NOT NULL, events INTEGER NOT NULL,
    PRIMARY KEY (project, day, kind, id)
) WITHOUT ROWID;
INSERT INTO agg_identity_daily_new
SELECT project, day, kind, id, actors, users, hits + views, events FROM agg_identity_daily;
DROP TABLE agg_identity_daily;
ALTER TABLE agg_identity_daily_new RENAME TO agg_identity_daily;

-- ===== product events: actor_kind, platform -> os =====
ALTER TABLE product_events ADD COLUMN actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events RENAME COLUMN platform TO os;
UPDATE product_events SET os = CASE lower(os)
    WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
    WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
    WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
    ELSE os END
  WHERE os <> '';

-- '$platform' rows become '$os' with normalised values; 'ios' and 'iOS'
-- rows for one (event, day) collapse into one, summed.
CREATE TEMP TABLE os_attrs AS
SELECT project, day, event_name, '$os' AS attr_key,
       CASE lower(attr_value)
         WHEN 'ios' THEN 'iOS' WHEN 'android' THEN 'Android'
         WHEN 'macos' THEN 'macOS' WHEN 'windows' THEN 'Windows'
         WHEN 'linux' THEN 'Linux' WHEN 'chromeos' THEN 'ChromeOS'
         ELSE attr_value END AS attr_value,
       SUM(count) AS count, SUM(unique_users) AS unique_users
FROM agg_product_attrs WHERE attr_key = '$platform'
GROUP BY project, day, event_name, 5;
DELETE FROM agg_product_attrs WHERE attr_key = '$platform';
INSERT OR REPLACE INTO agg_product_attrs (project, day, event_name, attr_key, attr_value, count, unique_users)
SELECT project, day, event_name, attr_key, attr_value, count, unique_users FROM os_attrs;
DROP TABLE os_attrs;

-- ===== drop the old families =====
DROP TABLE web_hits;
DROP TABLE app_views;
DROP TABLE agg_web_daily;    DROP TABLE agg_web_pages;   DROP TABLE agg_web_hosts;
DROP TABLE agg_web_referrers; DROP TABLE agg_web_countries; DROP TABLE agg_web_devices;
DROP TABLE agg_web_browsers; DROP TABLE agg_web_os;      DROP TABLE agg_web_utm;
DROP TABLE agg_app_daily;    DROP TABLE agg_app_screens; DROP TABLE agg_app_versions;
DROP TABLE agg_app_os;       DROP TABLE agg_app_devices; DROP TABLE agg_app_countries;
```

The `CREATE VIEW` statements for `v_views_*`, `v_product_*`, `v_identity_daily` and `v_retention` are appended to this same file in Task 3, where the aggregator SQL they must mirror is written. (`v_events_flat` is rebuilt by `RebuildFlatView` on the next daily pass and by `internal/app` at boot; it needs no statement here.)

- [ ] **Step 3: `write.go` — replace `WriteWebHits`/`WriteAppViews` with `WriteViews`, rename the product columns**

```go
func (d *DB) WriteViews(ctx context.Context, views []store.View) error {
	if len(views) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		// INSERT OR IGNORE: with client-supplied UUIDv7 ids, a batch
		// retried after a timeout that actually succeeded is a no-op.
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO views
			(id, project, ts, received_at, kind, actor_id, actor_kind, user_id, group_id, session_id,
			 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
			 os, os_version, browser, browser_version, app_version,
			 device, device_model, locale, display_width, display_height, country)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, v := range views {
			if _, err := stmt.ExecContext(ctx, v.ID, v.Project,
				v.TS.UTC().Format(tsFormat), v.ReceivedAt.UTC().Format(tsFormat),
				v.Kind, v.ActorID, v.ActorKind, v.UserID, v.GroupID, v.SessionID,
				v.Host, v.Path, v.ReferrerSource, v.UTMSource, v.UTMMedium, v.UTMCampaign,
				v.OS, v.OSVersion, v.Browser, v.BrowserVersion, v.AppVersion,
				v.Device, v.DeviceModel, v.Locale, v.DisplayWidth, v.DisplayHeight, v.Country); err != nil {
				return fmt.Errorf("view %s: %w", v.ID, err)
			}
		}
		return nil
	})
}
```

In `WriteProductEvents` change the column list to `(id, project, event_name, ts, received_at, actor_id, actor_kind, user_id, group_id, os, app_version, attributes)` with twelve `?` and the arguments `e.ID, e.Project, e.EventName, ts, received, e.ActorID, e.ActorKind, e.UserID, e.GroupID, e.OS, e.AppVersion, string(blob)`.

- [ ] **Step 4: `aggregate_product.go` — the system dimension keys**

```go
var systemDims = []struct{ column, key string }{
	{"os", "$os"},
	{"app_version", "$app_version"},
}
```

Update the comment above it: "os and app_version are typed columns…".

- [ ] **Step 5: `prune.go` — one views list, two cutoffs**

```go
var viewsAggTables = []string{
	"agg_views_daily", "agg_views_paths", "agg_views_hosts", "agg_views_referrers",
	"agg_views_utm", "agg_views_countries", "agg_views_os", "agg_views_browsers",
	"agg_views_app_versions", "agg_views_devices", "agg_views_displays",
}

var productAggTables = []string{"agg_product_daily", "agg_product_totals", "agg_product_attrs"}

// agg_retention and agg_identity_daily are keyed by cohort_day and day and
// follow the views cutoff. agg_retention is pruned by PruneActors, which
// owns the cohort/actor pair; agg_identity_daily by PruneIdentities.
var identityAggTables = []string{"agg_identity_daily"}

// PruneAggregates drops aggregate rows older than the per-family retention
// cutoffs for one project, atomically across tables.
func (d *DB) PruneAggregates(ctx context.Context, project string, viewsBefore, productBefore civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		del := func(tables []string, before civil.Date) error {
			for _, tbl := range tables {
				if _, err := tx.ExecContext(ctx,
					fmt.Sprintf(`DELETE FROM %s WHERE project=? AND day < ?`, tbl),
					project, before.String()); err != nil {
					return fmt.Errorf("prune %s: %w", tbl, err)
				}
			}
			return nil
		}
		if err := del(viewsAggTables, viewsBefore); err != nil {
			return err
		}
		return del(productAggTables, productBefore)
	})
}
```

Delete `webAggTables` and `appAggTables`. In `prune_test.go` `TestPruneAggregatesCoversAllAggTables`, build `all` from `viewsAggTables`, `productAggTables`, `identityAggTables` plus `"agg_retention"`; in any `PruneAggregates` call in tests drop the third cutoff argument.

- [ ] **Step 6: `registry.go` — `projectTables`**

```go
var projectTables = []string{
	"views", "product_events",
	"agg_views_daily", "agg_views_paths", "agg_views_hosts", "agg_views_referrers",
	"agg_views_utm", "agg_views_countries", "agg_views_os", "agg_views_browsers",
	"agg_views_app_versions", "agg_views_devices", "agg_views_displays",
	"agg_product_daily", "agg_product_totals", "agg_product_attrs",
	"actors", "agg_retention", "identities", "agg_identity_daily",
	"ingest_keys",
}
```

- [ ] **Step 7: Delete the old aggregators and update the schema tests**

```bash
git rm internal/store/sqlite/aggregate_web.go internal/store/sqlite/aggregate_app.go \
       internal/store/sqlite/aggregate_web_test.go internal/store/sqlite/aggregate_app_test.go
```

`dayRange`, `daysBefore`, `ProductDaysBefore`, `topNDimension`, `otherBucket` lived in the deleted files and are recreated in Task 3's `aggregate_views.go`. In `sqlite_test.go`: in the column-presence test replace any `web_hits`/`app_views` entries with `{"views", "kind"}, {"views", "actor_kind"}, {"views", "display_width"}, {"product_events", "actor_kind"}, {"product_events", "os"}` and replace the `visitor_hash` assertion with `if hasColumn(t, db, "product_events", "platform") { t.Error("product_events.platform should have been renamed to os") }`. Rename `TestMigration004Views` to `TestMigrationViews` with the list:

```go
	for _, view := range []string{
		"v_views_daily", "v_views_paths", "v_views_hosts", "v_views_referrers", "v_views_utm",
		"v_views_countries", "v_views_os", "v_views_browsers", "v_views_app_versions",
		"v_views_devices", "v_views_displays",
		"v_product_daily", "v_product_totals", "v_product_attrs",
		"v_identity_daily", "v_retention",
	} {
```

In `write_test.go` replace every `store.WebHit`/`store.AppView` fixture with a `store.View` (set `Kind: "web"` or `"app"`, `ActorKind: store.ActorConnection`/`store.ActorInstall`, `Path` for what was `Screen`, `OS` for what was `Platform`) and every `WriteWebHits`/`WriteAppViews` call with `WriteViews`; product fixtures rename `Platform:` to `OS:`. Assertions that read `web_hits`/`app_views` read `views`.

- [ ] **Step 8: Commit (package compiles fully only after Task 3)**

```bash
git add internal/store
git commit -m "feat(store): one views table and migration 012 folding the web and app families"
```

---

### Task 3: The views aggregator and its stitch views

Spec §7, §8. One aggregator, `AggregateViewDay`, and the `CREATE VIEW` statements that mirror it byte-for-number.

**Files:**
- Create: `internal/store/sqlite/aggregate_views.go`, `internal/store/sqlite/aggregate_views_test.go`
- Modify: `internal/store/sqlite/migrations/012_views.sql` (append the views), `internal/store/sqlite/views_test.go`

**Interfaces:**
- Consumes: `store.View`, `WriteViews` (Task 2).
- Produces: `(*DB).ViewDaysBefore`, `(*DB).ProductDaysBefore`, `(*DB).AggregateViewDay`, package constants `topNDimension = 500`, `otherBucket = "(other)"`, `displaySQL`, helpers `dayRange`, `daysBefore` (used by Task 4's tests and Task 5).

- [ ] **Step 1: Write the failing aggregator tests**

Create `internal/store/sqlite/aggregate_views_test.go`:

```go
package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func day(s string) civil.Date {
	d, err := civil.Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

func at(h, m int) time.Time { return time.Date(2026, 8, 10, h, m, 0, 0, time.UTC) }

// seedViews writes views into project "app" with defaults filled in.
func seedViews(t *testing.T, db *DB, views ...store.View) {
	t.Helper()
	for i := range views {
		if views[i].ReceivedAt.IsZero() {
			views[i].ReceivedAt = views[i].TS
		}
		if views[i].Project == "" {
			views[i].Project = "app"
		}
		if views[i].Kind == "" {
			views[i].Kind = "web"
		}
		if views[i].ActorKind == "" {
			views[i].ActorKind = store.ActorConnection
		}
	}
	if err := db.WriteViews(context.Background(), views); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// seedViewDay is the deterministic fixture for 2026-08-10, project "app":
//
//	web v1: 10:00 /a, 10:10 /b          -> 1 session, 2 views, dur 600
//	web v1: 12:00 /a                    -> gap > 30 min: 2nd session, bounce
//	web v2: 11:00 /a (DE, mobile, Chrome 126, Android, google, hn/social/launch,
//	        display 390x844)            -> 1 session, bounce
//	app  i1: 10:00 /home s1, 10:05 /settings s1 (iOS 17.2, 2.4.1, iPhone15,2, DE)
//	                                    -> 1 client session, 2 views, dur 300
//	app  i2: 11:00 /home s2 (Android 14, 2.4.1, Pixel 8, FR)
//	                                    -> 1 session, bounce
//
// web totals: visitors 2, views 4, sessions 3, bounces 2, duration 600.
// app totals: visitors 2, views 3, sessions 2, bounces 1, duration 300.
func seedViewDay(t *testing.T, db *DB) {
	t.Helper()
	web := func(id, actor, path string, ts time.Time) store.View {
		return store.View{ID: id, TS: ts, ActorID: actor, Kind: "web",
			Host: "shop.example.com", Path: path, Country: "US",
			Device: "desktop", Browser: "Firefox", BrowserVersion: "127", OS: "Linux"}
	}
	v2 := web("4", "v2", "/a", at(11, 0))
	v2.Country, v2.Device, v2.Browser, v2.BrowserVersion, v2.OS = "DE", "mobile", "Chrome", "126", "Android"
	v2.ReferrerSource = "google"
	v2.UTMSource, v2.UTMMedium, v2.UTMCampaign = "hn", "social", "launch"
	v2.DisplayWidth, v2.DisplayHeight = 390, 844
	app := func(id, actor, path, session, os, osv, model, country string, ts time.Time) store.View {
		return store.View{ID: id, TS: ts, ActorID: actor, ActorKind: store.ActorInstall, Kind: "app",
			SessionID: session, Path: path, OS: os, OSVersion: osv, AppVersion: "2.4.1",
			DeviceModel: model, Locale: "en-US", Country: country}
	}
	seedViews(t, db,
		web("1", "v1", "/a", at(10, 0)), web("2", "v1", "/b", at(10, 10)), web("3", "v1", "/a", at(12, 0)), v2,
		app("5", "i1", "/home", "s1", "iOS", "17.2", "iPhone15,2", "DE", at(10, 0)),
		app("6", "i1", "/settings", "s1", "iOS", "17.2", "iPhone15,2", "DE", at(10, 5)),
		app("7", "i2", "/home", "s2", "Android", "14", "Pixel 8", "FR", at(11, 0)),
	)
}

type dailyRow struct{ visitors, views, sessions, bounces, dur int }

func readDaily(t *testing.T, db *DB, table, kind string) dailyRow {
	t.Helper()
	var r dailyRow
	err := db.db.QueryRow(fmt.Sprintf(`SELECT visitors, views, sessions, bounces, duration_sec
		FROM %s WHERE project='app' AND day='2026-08-10' AND kind=?`, table), kind).
		Scan(&r.visitors, &r.views, &r.sessions, &r.bounces, &r.dur)
	if err != nil {
		t.Fatalf("%s/%s: %v", table, kind, err)
	}
	return r
}

func TestAggregateViewDayPerKind(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	if err := db.AggregateViewDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	if got, want := readDaily(t, db, "agg_views_daily", "web"), (dailyRow{2, 4, 3, 2, 600}); got != want {
		t.Errorf("web = %+v, want %+v", got, want)
	}
	if got, want := readDaily(t, db, "agg_views_daily", "app"), (dailyRow{2, 3, 2, 1, 300}); got != want {
		t.Errorf("app = %+v, want %+v", got, want)
	}
	var raw int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM views WHERE project='app'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 0 {
		t.Errorf("raw rows left after aggregation: %d", raw)
	}
}

func TestAggregateViewDayDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	if err := db.AggregateViewDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	check := func(q string, args []any, wantV, wantP int) {
		t.Helper()
		var v, p int
		if err := db.db.QueryRow(q, args...).Scan(&v, &p); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if v != wantV || p != wantP {
			t.Errorf("%s %v = (%d,%d), want (%d,%d)", q, args, v, p, wantV, wantP)
		}
	}
	const w = ` WHERE project='app' AND day='2026-08-10' AND `
	check(`SELECT visitors, views FROM agg_views_paths`+w+`path=?`, []any{"/a"}, 2, 3)
	check(`SELECT visitors, views FROM agg_views_paths`+w+`path=?`, []any{"/home"}, 2, 2)
	check(`SELECT visitors, views FROM agg_views_hosts`+w+`host=?`, []any{""}, 2, 3)
	check(`SELECT visitors, views FROM agg_views_referrers`+w+`source=?`, []any{"google"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_utm`+w+`utm_source=?`, []any{"hn"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_countries`+w+`country=?`, []any{"DE"}, 2, 3)
	check(`SELECT visitors, views FROM agg_views_os`+w+`os=? AND os_version=?`, []any{"iOS", "17.2"}, 1, 2)
	check(`SELECT visitors, views FROM agg_views_os`+w+`os=? AND os_version=?`, []any{"Linux", ""}, 1, 3)
	check(`SELECT visitors, views FROM agg_views_browsers`+w+`browser=? AND browser_version=?`, []any{"Chrome", "126"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_app_versions`+w+`os=? AND app_version=?`, []any{"iOS", "2.4.1"}, 1, 2)
	check(`SELECT visitors, views FROM agg_views_devices`+w+`device=? AND device_model=?`, []any{"", "Pixel 8"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_devices`+w+`device=? AND device_model=?`, []any{"desktop", ""}, 1, 3)
	check(`SELECT visitors, views FROM agg_views_displays`+w+`display=?`, []any{"390x844"}, 1, 1)
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_utm WHERE utm_source='' AND utm_medium='' AND utm_campaign=''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty utm row written: %d", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_app_versions WHERE app_version=''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty app_version row written: %d", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_displays`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("displays rows = %d, want 1 (rows without a display are skipped)", n)
	}
}

// Every dimension collapses past the cap; the trailing key becomes
// "(other)" with visitors recomputed as distinct actors, not summed.
func TestAggregateViewDayCapsDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var views []store.View
	for i := 0; i < topNDimension+10; i++ {
		// two actors see every collapsed path, so a summed count would say 20
		for _, actor := range []string{"a", "b"} {
			views = append(views, store.View{ID: fmt.Sprintf("%s-%d", actor, i), TS: at(9, 0).Add(time.Duration(i) * time.Second),
				ActorID: actor, Path: fmt.Sprintf("/p/%04d", i), OS: "Linux", OSVersion: fmt.Sprintf("%d", i)})
		}
	}
	// One popular path stays out of the tail.
	for i := 0; i < 5; i++ {
		views = append(views, store.View{ID: fmt.Sprintf("hot-%d", i), TS: at(10, 0), ActorID: "c", Path: "/hot", OS: "Linux", OSVersion: "0"})
	}
	seedViews(t, db, views...)
	if err := db.AggregateViewDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	var rows, otherV, otherP int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_paths WHERE project='app'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != topNDimension+1 {
		t.Errorf("paths rows = %d, want %d (top-N plus the other bucket)", rows, topNDimension+1)
	}
	if err := db.db.QueryRow(`SELECT visitors, views FROM agg_views_paths WHERE project='app' AND path='(other)'`).Scan(&otherV, &otherP); err != nil {
		t.Fatal(err)
	}
	if otherV != 2 || otherP != 22 {
		t.Errorf("(other) = (%d,%d), want (2,22): 11 collapsed paths x 2 actors, 2 distinct actors", otherV, otherP)
	}
	// Two-key dimension: the leading key stays intact, only os_version collapses.
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_os WHERE project='app' AND os='Linux' AND os_version='(other)'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("os other bucket rows = %d, want 1", rows)
	}
}

func TestAggregateViewDayIsIdempotentAndSkipsEmptyDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	for i := 0; i < 2; i++ {
		if err := db.AggregateViewDay(ctx, "app", day("2026-08-10")); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := readDaily(t, db, "agg_views_daily", "web"), (dailyRow{2, 4, 3, 2, 600}); got != want {
		t.Errorf("after re-run web = %+v, want %+v", got, want)
	}
	if err := db.AggregateViewDay(ctx, "app", day("2026-08-11")); err != nil {
		t.Fatalf("empty day must be a no-op: %v", err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_daily WHERE day='2026-08-11'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty day wrote %d rows", n)
	}
}

func TestViewDaysBefore(t *testing.T) {
	db := newTestDB(t)
	seedViews(t, db,
		store.View{ID: "1", TS: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC), ActorID: "a", Path: "/"},
		store.View{ID: "2", TS: time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC), ActorID: "a", Path: "/"},
		store.View{ID: "3", TS: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC), ActorID: "a", Path: "/"},
	)
	days, err := db.ViewDaysBefore(context.Background(), "app", day("2026-08-05"))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].String() != "2026-08-01" || days[1].String() != "2026-08-03" {
		t.Errorf("days = %v", days)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store/sqlite/ -run 'TestAggregateViewDay|TestViewDaysBefore'`
Expected: compile errors (`undefined: topNDimension`, `AggregateViewDay`, …).

- [ ] **Step 3: Implement `aggregate_views.go`**

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
)

// topNDimension caps client-supplied dimension values per day; the tail
// collapses into "(other)". Applies to every dimension, paths included: an
// unbounded dimension is the wrong default on the SD-card hardware target
// (a path carrying record ids would grow the aggregate without limit).
const topNDimension = 500

const otherBucket = "(other)"

// displaySQL is the display-resolution key, one expression shared by the
// aggregator and the live half of v_views_displays so the two cannot drift.
const displaySQL = `display_width || 'x' || display_height`

func dayRange(day civil.Date) (string, string) {
	return day.String() + "T00:00:00Z", day.AddDays(1).String() + "T00:00:00Z"
}

func (d *DB) ViewDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, "views", project, before)
}

func (d *DB) ProductDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, "product_events", project, before)
}

func (d *DB) daysBefore(ctx context.Context, table, project string, before civil.Date) ([]civil.Date, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT DISTINCT substr(ts,1,10) FROM %s WHERE project=? AND ts < ? ORDER BY 1`, table),
		project, before.String()+"T00:00:00Z")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []civil.Date
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		day, err := civil.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, day)
	}
	return out, rows.Err()
}

// viewSessionsCTE sessionizes one project-day. Kinds past the cap fold into
// "(other)" first, so a hostile client cannot grow agg_views_daily. A
// client-declared session_id is authoritative (the app knows its own
// foreground/background transitions); otherwise a gap over 30 minutes per
// actor splits sessions. The live half of v_views_daily (012_views.sql)
// mirrors this per (project, day); views_test.go enforces the parity.
const viewSessionsCTE = `
WITH src AS (
  SELECT kind, actor_id, session_id, CAST(strftime('%s', ts) AS INTEGER) AS t
  FROM views WHERE project = :p AND ts >= :from AND ts < :to
),
kinds AS (
  SELECT kind, ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, kind) AS rn FROM src GROUP BY kind
),
bucketed AS (
  SELECT CASE WHEN k.rn <= 500 THEN src.kind ELSE '(other)' END AS kind,
         src.actor_id, src.session_id, src.t
  FROM src JOIN kinds k ON k.kind = src.kind
),
marked AS (
  SELECT kind, actor_id, session_id, t,
         CASE WHEN session_id <> '' THEN 0
              WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
              ELSE 0 END AS new_session
  FROM bucketed WINDOW w AS (PARTITION BY kind, actor_id ORDER BY t)
),
keyed AS (
  SELECT kind, actor_id, t,
         CASE WHEN session_id <> '' THEN session_id
              ELSE CAST(SUM(new_session) OVER (PARTITION BY kind, actor_id ORDER BY t) AS TEXT)
         END AS skey
  FROM marked
),
spans AS (
  SELECT kind, actor_id, skey, COUNT(*) AS view_count, MAX(t) - MIN(t) AS dur
  FROM keyed GROUP BY kind, actor_id, skey
)`

// viewDimension is one rollup. keys are the result columns; exprs the SQL
// producing them (defaults to the key names). The last key is the one
// whose tail collapses into "(other)"; a leading key stays intact, so a
// collapsed os_version row still says which os it belongs to.
type viewDimension struct {
	table string
	keys  []string
	exprs []string
	where string
}

var viewDimensions = []viewDimension{
	{table: "agg_views_paths", keys: []string{"path"}},
	// No where clause: unlike utm, an empty host is a real bucket (rows
	// predating migration 008, and every non-web kind).
	{table: "agg_views_hosts", keys: []string{"host"}},
	{table: "agg_views_referrers", keys: []string{"source"}, exprs: []string{"referrer_source"}},
	{table: "agg_views_utm", keys: []string{"utm_source", "utm_medium", "utm_campaign"},
		where: "AND NOT (utm_source='' AND utm_medium='' AND utm_campaign='')"},
	{table: "agg_views_countries", keys: []string{"country"}},
	{table: "agg_views_os", keys: []string{"os", "os_version"}},
	{table: "agg_views_browsers", keys: []string{"browser", "browser_version"}},
	{table: "agg_views_app_versions", keys: []string{"os", "app_version"}, where: "AND app_version <> ''"},
	{table: "agg_views_devices", keys: []string{"device", "device_model"}},
	{table: "agg_views_displays", keys: []string{"display"}, exprs: []string{displaySQL},
		where: "AND display_width > 0 AND display_height > 0"},
}

// AggregateViewDay rolls one day of views into agg_views_* and deletes the
// raw rows, in one transaction. Idempotent: every write is INSERT OR
// REPLACE keyed on (project, day, ...), recomputed wholly from raw rows.
func (d *DB) AggregateViewDay(ctx context.Context, project string, day civil.Date) error {
	from, to := dayRange(day)
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM views WHERE project=? AND ts>=? AND ts<?`,
			project, from, to).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil // already aggregated (or empty day): no-op keeps idempotency
		}
		named := []any{sql.Named("p", project), sql.Named("from", from), sql.Named("to", to), sql.Named("day", day.String())}
		if _, err := tx.ExecContext(ctx, viewSessionsCTE+`
INSERT OR REPLACE INTO agg_views_daily
  (project, day, kind, visitors, views, sessions, bounces, duration_sec)
SELECT :p, :day, s.kind,
  (SELECT COUNT(DISTINCT actor_id) FROM bucketed b WHERE b.kind = s.kind),
  (SELECT COUNT(*) FROM bucketed b WHERE b.kind = s.kind),
  COUNT(*),
  SUM(CASE WHEN view_count = 1 THEN 1 ELSE 0 END),
  COALESCE(SUM(dur), 0)
FROM spans s GROUP BY s.kind`, named...); err != nil {
			return fmt.Errorf("agg_views_daily: %w", err)
		}
		for _, dim := range viewDimensions {
			if _, err := tx.ExecContext(ctx, dim.aggregateSQL(), named...); err != nil {
				return fmt.Errorf("%s: %w", dim.table, err)
			}
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM views WHERE project=? AND ts>=? AND ts<?`, project, from, to)
		return err
	})
}

// aggregateSQL builds the one-statement rollup: rank values by views desc,
// keep the top N, map the rest onto "(other)". One statement rather than a
// top-N insert plus a tail insert keeps visitors honest: it is always
// COUNT(DISTINCT actor_id) over the grouped raw rows, never a sum that
// double-counts an actor who saw two collapsed values.
func (dim viewDimension) aggregateSQL() string {
	exprs := dim.exprs
	if exprs == nil {
		exprs = dim.keys
	}
	var sel, lead, join []string
	for i, k := range dim.keys {
		sel = append(sel, exprs[i]+" AS "+k)
		join = append(join, "r."+k+" = s."+k)
		if i < len(dim.keys)-1 {
			lead = append(lead, "s."+k)
		}
	}
	last := dim.keys[len(dim.keys)-1]
	bucket := fmt.Sprintf("CASE WHEN r.rn <= %d THEN s.%s ELSE '%s' END", topNDimension, last, otherBucket)
	cols := strings.Join(dim.keys, ", ")
	group := strings.Join(append(append([]string{}, lead...), bucket), ", ")
	return fmt.Sprintf(`
INSERT OR REPLACE INTO %s (project, day, %s, visitors, views)
WITH src AS (
  SELECT %s, actor_id FROM views
  WHERE project = :p AND ts >= :from AND ts < :to %s
),
ranked AS (
  SELECT %s, ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, %s) AS rn FROM src GROUP BY %s
)
SELECT :p, :day, %s, COUNT(DISTINCT s.actor_id), COUNT(*)
FROM src s JOIN ranked r ON %s
GROUP BY %s`,
		dim.table, cols, strings.Join(sel, ", "), dim.where,
		cols, cols, cols,
		group, strings.Join(join, " AND "), group)
}
```

- [ ] **Step 4: Run the aggregator tests**

Run: `go test ./internal/store/sqlite/ -run 'TestAggregateViewDay|TestViewDaysBefore'`
Expected: FAIL only on missing tables/views if 012's views are not yet appended (Step 5); the daily and dimension assertions themselves should pass. If `views_test.go` or other files still reference deleted symbols, fix those compile errors first by applying Steps 5–6 of this task.

- [ ] **Step 5: Append the stitch views to `012_views.sql`**

Append to the end of `internal/store/sqlite/migrations/012_views.sql`. The live halves must stay numerically identical to `AggregateViewDay`; `views_test.go` enforces it.

```sql
-- ===== stitch views: aggregates UNION ALL live computation over raw rows =====
-- Raw and aggregated days are disjoint (aggregation deletes raw in-tx), so
-- no day is double counted. Live halves mirror aggregate_views.go exactly,
-- including the per-day top-N cap and the "(other)" tail.

```

`v_views_daily` restates the kind bucketing in two inline subqueries (SQLite cannot share a CTE between the counting half and the sessionizing half); parity with the aggregator holds because both derive from the same ranking:

```sql
CREATE VIEW v_views_daily AS
SELECT project, day, kind, visitors, views, sessions, bounces, duration_sec FROM agg_views_daily
UNION ALL
SELECT c.project, c.day, c.kind, c.visitors, c.views, p.sessions, p.bounces, p.duration_sec
FROM (
  -- visitors and views per bucketed kind
  SELECT b.project, b.day, b.kind, COUNT(DISTINCT b.actor_id) AS visitors, COUNT(*) AS views
  FROM (
    SELECT v.project, substr(v.ts,1,10) AS day,
           CASE WHEN k.rn <= 500 THEN v.kind ELSE '(other)' END AS kind, v.actor_id
    FROM views v
    JOIN (
      SELECT project, substr(ts,1,10) AS day, kind,
             ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, kind) AS rn
      FROM views GROUP BY project, substr(ts,1,10), kind
    ) k ON k.project = v.project AND k.day = substr(v.ts,1,10) AND k.kind = v.kind
  ) b
  GROUP BY b.project, b.day, b.kind
) c
JOIN (
  -- sessions, bounces and duration per bucketed kind
  WITH src AS (
    SELECT v.project, substr(v.ts,1,10) AS day,
           CASE WHEN k.rn <= 500 THEN v.kind ELSE '(other)' END AS kind,
           v.actor_id, v.session_id, CAST(strftime('%s', v.ts) AS INTEGER) AS t
    FROM views v
    JOIN (
      SELECT project, substr(ts,1,10) AS day, kind,
             ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, kind) AS rn
      FROM views GROUP BY project, substr(ts,1,10), kind
    ) k ON k.project = v.project AND k.day = substr(v.ts,1,10) AND k.kind = v.kind
  ),
  marked AS (
    SELECT project, day, kind, actor_id, session_id, t,
           CASE WHEN session_id <> '' THEN 0
                WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
                ELSE 0 END AS new_session
    FROM src WINDOW w AS (PARTITION BY project, day, kind, actor_id ORDER BY t)
  ),
  keyed AS (
    SELECT project, day, kind, actor_id, t,
           CASE WHEN session_id <> '' THEN session_id
                ELSE CAST(SUM(new_session) OVER (PARTITION BY project, day, kind, actor_id ORDER BY t) AS TEXT)
           END AS skey
    FROM marked
  ),
  spans AS (
    SELECT project, day, kind, actor_id, skey, COUNT(*) AS view_count, MAX(t) - MIN(t) AS dur
    FROM keyed GROUP BY project, day, kind, actor_id, skey
  )
  SELECT project, day, kind, COUNT(*) AS sessions,
         SUM(CASE WHEN view_count = 1 THEN 1 ELSE 0 END) AS bounces,
         COALESCE(SUM(dur), 0) AS duration_sec
  FROM spans GROUP BY project, day, kind
) p ON p.project = c.project AND p.day = c.day AND p.kind = c.kind;
```

Then the dimension views. Single-key views follow this template (shown for paths; repeat for `hosts` with `host`, `referrers` with `referrer_source AS source`/`source`, `countries` with `country`):

```sql
CREATE VIEW v_views_paths AS
SELECT project, day, path, visitors, views FROM agg_views_paths
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.path, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, path,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, path) AS rn
    FROM views GROUP BY project, substr(ts,1,10), path
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.path = v.path
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN path ELSE '(other)' END;

CREATE VIEW v_views_hosts AS
SELECT project, day, host, visitors, views FROM agg_views_hosts
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.host, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, host,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, host) AS rn
    FROM views GROUP BY project, substr(ts,1,10), host
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.host = v.host
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN host ELSE '(other)' END;

CREATE VIEW v_views_referrers AS
SELECT project, day, source, visitors, views FROM agg_views_referrers
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.referrer_source AS source, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, referrer_source AS source,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, referrer_source) AS rn
    FROM views GROUP BY project, substr(ts,1,10), referrer_source
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.source = v.referrer_source
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN source ELSE '(other)' END;

CREATE VIEW v_views_countries AS
SELECT project, day, country, visitors, views FROM agg_views_countries
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.country, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, country,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, country) AS rn
    FROM views GROUP BY project, substr(ts,1,10), country
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.country = v.country
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN country ELSE '(other)' END;

CREATE VIEW v_views_displays AS
SELECT project, day, display, visitors, views FROM agg_views_displays
UNION ALL
SELECT project, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.display_width || 'x' || v.display_height AS display, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, display_width || 'x' || display_height AS display,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, display_width || 'x' || display_height) AS rn
    FROM views WHERE display_width > 0 AND display_height > 0
    GROUP BY project, substr(ts,1,10), display_width || 'x' || display_height
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.display = v.display_width || 'x' || v.display_height
  WHERE v.display_width > 0 AND v.display_height > 0
)
GROUP BY project, day, CASE WHEN rn <= 500 THEN display ELSE '(other)' END;
```

Two-key views (leading key intact, trailing key collapses):

```sql
CREATE VIEW v_views_os AS
SELECT project, day, os, os_version, visitors, views FROM agg_views_os
UNION ALL
SELECT project, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.os, v.os_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, os, os_version,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, os, os_version) AS rn
    FROM views GROUP BY project, substr(ts,1,10), os, os_version
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.os = v.os AND r.os_version = v.os_version
)
GROUP BY project, day, os, CASE WHEN rn <= 500 THEN os_version ELSE '(other)' END;

CREATE VIEW v_views_browsers AS
SELECT project, day, browser, browser_version, visitors, views FROM agg_views_browsers
UNION ALL
SELECT project, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.browser, v.browser_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, browser, browser_version,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, browser, browser_version) AS rn
    FROM views GROUP BY project, substr(ts,1,10), browser, browser_version
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.browser = v.browser AND r.browser_version = v.browser_version
)
GROUP BY project, day, browser, CASE WHEN rn <= 500 THEN browser_version ELSE '(other)' END;

CREATE VIEW v_views_app_versions AS
SELECT project, day, os, app_version, visitors, views FROM agg_views_app_versions
UNION ALL
SELECT project, day, os, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.os, v.app_version, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, os, app_version,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, os, app_version) AS rn
    FROM views WHERE app_version <> '' GROUP BY project, substr(ts,1,10), os, app_version
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.os = v.os AND r.app_version = v.app_version
  WHERE v.app_version <> ''
)
GROUP BY project, day, os, CASE WHEN rn <= 500 THEN app_version ELSE '(other)' END;

CREATE VIEW v_views_devices AS
SELECT project, day, device, device_model, visitors, views FROM agg_views_devices
UNION ALL
SELECT project, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.device, v.device_model, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, device, device_model,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, device, device_model) AS rn
    FROM views GROUP BY project, substr(ts,1,10), device, device_model
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10) AND r.device = v.device AND r.device_model = v.device_model
)
GROUP BY project, day, device, CASE WHEN rn <= 500 THEN device_model ELSE '(other)' END;

CREATE VIEW v_views_utm AS
SELECT project, day, utm_source, utm_medium, utm_campaign, visitors, views FROM agg_views_utm
UNION ALL
SELECT project, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM (
  SELECT v.project, substr(v.ts,1,10) AS day, v.utm_source, v.utm_medium, v.utm_campaign, v.actor_id, r.rn
  FROM views v
  JOIN (
    SELECT project, substr(ts,1,10) AS day, utm_source, utm_medium, utm_campaign,
           ROW_NUMBER() OVER (PARTITION BY project, substr(ts,1,10) ORDER BY COUNT(*) DESC, utm_source, utm_medium, utm_campaign) AS rn
    FROM views WHERE NOT (utm_source='' AND utm_medium='' AND utm_campaign='')
    GROUP BY project, substr(ts,1,10), utm_source, utm_medium, utm_campaign
  ) r ON r.project = v.project AND r.day = substr(v.ts,1,10)
     AND r.utm_source = v.utm_source AND r.utm_medium = v.utm_medium AND r.utm_campaign = v.utm_campaign
  WHERE NOT (v.utm_source='' AND v.utm_medium='' AND v.utm_campaign='')
)
GROUP BY project, day, utm_source, utm_medium, CASE WHEN rn <= 500 THEN utm_campaign ELSE '(other)' END;
```

Product views, recreated (`v_product_daily` and `v_product_totals` verbatim from 004; `v_product_attrs` is 007's `CREATE VIEW v_product_attrs AS …` copied verbatim with exactly two edits in the `vals` CTE: the arm `SELECT project, substr(ts,1,10), event_name, '$platform', platform, actor_id FROM product_events WHERE platform <> ''` becomes `… '$os', os, actor_id FROM product_events WHERE os <> ''`). Then identity and retention:

```sql
CREATE VIEW v_product_daily AS
SELECT project, day, event_name, count, unique_users FROM agg_product_daily
UNION ALL
SELECT project, substr(ts,1,10), event_name, COUNT(*), COUNT(DISTINCT actor_id)
FROM product_events GROUP BY project, substr(ts,1,10), event_name;

CREATE VIEW v_product_totals AS
SELECT project, day, total_events, active_users FROM agg_product_totals
UNION ALL
SELECT project, substr(ts,1,10), COUNT(*), COUNT(DISTINCT actor_id)
FROM product_events GROUP BY project, substr(ts,1,10);

-- (v_product_attrs: 007's definition with the two $platform → $os edits)

CREATE VIEW v_identity_daily AS
SELECT project, day, kind, id, actors, users, views, events
FROM agg_identity_daily
UNION ALL
SELECT project, day, kind, id,
       COUNT(DISTINCT actor_id),
       CASE WHEN kind = 'user' THEN 1 ELSE COUNT(DISTINCT NULLIF(user_id, '')) END,
       SUM(is_view), SUM(is_event)
FROM (
  SELECT project, substr(ts,1,10) AS day, 'user' AS kind, user_id AS id,
         actor_id, user_id, 1 AS is_view, 0 AS is_event
  FROM views WHERE user_id <> ''
  UNION ALL
  SELECT project, substr(ts,1,10), 'user', user_id, actor_id, user_id, 0, 1
  FROM product_events WHERE user_id <> ''
  UNION ALL
  SELECT project, substr(ts,1,10), 'group', group_id, actor_id, user_id, 1, 0
  FROM views WHERE group_id <> ''
  UNION ALL
  SELECT project, substr(ts,1,10), 'group', group_id, actor_id, user_id, 0, 1
  FROM product_events WHERE group_id <> ''
)
GROUP BY project, day, kind, id;

-- Retention is defined only over aggregated days, so there is no live half.
CREATE VIEW v_retention AS
SELECT r.project, r.actor_kind, r.cohort_day, r.day_offset, r.actors,
       c.actors AS cohort_size
FROM agg_retention r
JOIN agg_retention c
  ON c.project = r.project AND c.actor_kind = r.actor_kind
 AND c.cohort_day = r.cohort_day AND c.day_offset = 0;
```

- [ ] **Step 6: Rewrite `views_test.go` for the views family**

Replace `TestStitchViewsInvariantWeb` and `TestStitchViewsInvariantAllWebDimensions` with:

```go
func TestStitchViewsInvariantDaily(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db) // raw only

	read := func(kind string) dailyRow { return readDaily(t, db, "v_views_daily", kind) }
	webBefore, appBefore := read("web"), read("app")
	if webBefore != (dailyRow{2, 4, 3, 2, 600}) || appBefore != (dailyRow{2, 3, 2, 1, 300}) {
		t.Fatalf("live v_views_daily web=%+v app=%+v; fixture expectations wrong", webBefore, appBefore)
	}
	if err := db.AggregateViewDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	if got := read("web"); got != webBefore {
		t.Errorf("web stitch mismatch: before %+v after %+v", webBefore, got)
	}
	if got := read("app"); got != appBefore {
		t.Errorf("app stitch mismatch: before %+v after %+v", appBefore, got)
	}
}

// Every dimension view must hold the invariant, including the cap and the
// "(other)" tail, or one dimension jumps the moment aggregation runs.
func TestStitchViewsInvariantAllViewsDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	// Push one dimension past the cap so the other bucket is exercised on
	// both sides of the boundary.
	var extra []store.View
	for i := 0; i < topNDimension+5; i++ {
		extra = append(extra, store.View{ID: fmt.Sprintf("x-%d", i), TS: at(13, 0).Add(time.Duration(i) * time.Second),
			ActorID: "v3", Path: fmt.Sprintf("/x/%d", i), OS: "Linux", Browser: "Firefox", BrowserVersion: "127", Device: "desktop"})
	}
	seedViews(t, db, extra...)

	type dim struct{ view, key string }
	dims := []dim{
		{"v_views_paths", "path"},
		{"v_views_hosts", "host"},
		{"v_views_referrers", "source"},
		{"v_views_utm", "utm_source || '|' || utm_medium || '|' || utm_campaign"},
		{"v_views_countries", "country"},
		{"v_views_os", "os || '|' || os_version"},
		{"v_views_browsers", "browser || '|' || browser_version"},
		{"v_views_app_versions", "os || '|' || app_version"},
		{"v_views_devices", "device || '|' || device_model"},
		{"v_views_displays", "display"},
	}
	snapshot := func(d dim) map[string][2]int {
		t.Helper()
		rows, err := db.db.Query(fmt.Sprintf(
			`SELECT %s, visitors, views FROM %s WHERE project='app' AND day='2026-08-10'`, d.key, d.view))
		if err != nil {
			t.Fatalf("%s: %v", d.view, err)
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
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := map[string]map[string][2]int{}
	for _, d := range dims {
		before[d.view] = snapshot(d)
		if len(before[d.view]) == 0 {
			t.Fatalf("%s returned no live rows; invariant check would be vacuous", d.view)
		}
	}
	if _, ok := before["v_views_paths"]["(other)"]; !ok {
		t.Fatal("paths fixture did not exceed the cap; the other-bucket parity is untested")
	}
	if err := db.AggregateViewDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	for _, d := range dims {
		after := snapshot(d)
		if !reflect.DeepEqual(after, before[d.view]) {
			t.Errorf("%s: before %v, after %v", d.view, before[d.view], after)
		}
	}
}
```

`TestStitchViewUTMExcludesEmpty`, `TestStitchViewsMixedAggregatedAndRawDays` and `TestStitchViewIdentityDailyCoversRawDays` keep their intent: change `seedWebDay` → `seedViewDay`, `AggregateWebDay` → `AggregateViewDay`, `v_web_utm` → `v_views_utm`, `v_web_daily` → `v_views_daily` (add `AND kind='web'` to its WHERE), `pageviews` → `views`, `store.WebHit`/`store.AppView` fixtures → `store.View` with `Kind`, and in the identity test the columns `hits, views, events` → `views, events` (the expected views count is the old hits + views). The product-attrs tests at the bottom of the file that write `platform` on a `store.ProductEvent` write `OS` instead and expect attr key `$os`.

- [ ] **Step 6b: Port the remaining tests in the package**

`errors_test.go`, `coverage_test.go`, `zz_seed_test.go`, `registry_test.go`, `prune_test.go`, `sqlite_test.go` and `aggregate_product_test.go` still name the old symbols. Rules, applied mechanically:

- `store.WebHit{…}` → `store.View{Kind: "web", ActorKind: store.ActorConnection, …}` and `store.AppView{…, Screen: x, Platform: y}` → `store.View{Kind: "app", ActorKind: store.ActorInstall, Path: x, OS: y, …}`; `WriteWebHits`/`WriteAppViews` → `WriteViews`.
- `AggregateWebDay`/`AggregateAppDay` → `AggregateViewDay`; `appDay()` → `day("2026-08-10")` (and fixture timestamps via `at(h, m)`, which now lives in `aggregate_views_test.go` and is 2026-08-10).
- Raw SQL: `web_hits`/`app_views` → `views` (add the NOT NULL columns `kind`, `actor_kind`, `received_at` to hand-written INSERTs); `agg_web_daily (project, day, visitors, pageviews, …)` → `agg_views_daily (project, day, kind, visitors, views, …)` with a `'web'` kind; `agg_web_*`/`agg_app_*` → the matching `agg_views_*`; `platform` → `os` on `product_events`; `ProductEvent{Platform: …}` → `OS:`.
- `errors_test.go`'s "fails on missing table" cases drop `agg_web_*`/`agg_app_*` tables to provoke failures: drop `agg_views_daily` / `agg_views_paths` instead, and expect the error text `agg_views_daily:` / `agg_views_paths:`. Merge the web and app variants of each case into one; the count of tests goes down, the coverage of the aggregator does not.
- `coverage_test.go`'s method table: one `"AggregateViewDay"` entry and one `"WriteViews"` entry replace the four web/app entries; `PruneAggregates` takes two cutoffs.
- `registry_test.go` `TestRenameProjectMovesEveryTable`: insert into `views` and `agg_views_daily` and check those two tables move.

- [ ] **Step 7: Run the package**

Run: `go test ./internal/store/sqlite/`
Expected: PASS, apart from retention and identities tests, which Task 4 fixes (they reference `surface`). If only those fail, proceed.

- [ ] **Step 8: Commit**

```bash
git add internal/store
git commit -m "feat(store): one views aggregator with capped dimensions and matching stitch views"
```

---

### Task 4: Retention on actor kind, identity daily on one views column

Spec §6.4, §9.

**Files:**
- Modify: `internal/store/sqlite/retention.go`, `retention_test.go`, `internal/store/sqlite/identities.go`, `identities_test.go`

**Interfaces:**
- Consumes: `views.actor_kind`, `product_events.actor_kind` (Task 2), `store.ActorUser/ActorInstall`.
- Produces: unchanged method names `UpsertActors`, `AggregateRetentionDay`, `PruneActors`, `AggregateIdentityDay`, `PruneIdentities`; `actors.actor_kind`, `agg_retention.actor_kind`, `agg_identity_daily.views`.

- [ ] **Step 1: Failing retention tests**

In `retention_test.go`, replace the fixtures: every `store.WebHit`/`store.AppView` becomes a `store.View` with `ActorKind` set (`store.ActorUser` for rows with a user id, `store.ActorInstall` for install-identified app rows), written through `WriteViews`; product fixtures get `ActorKind`. Replace reads of `surface` with `actor_kind` and expected values `web`→`user`, `app`→`install`. Add:

```go
func TestUpsertActorsSkipsConnectionActors(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViews(t, db,
		store.View{ID: "1", TS: at(10, 0), ActorID: "hash-1", ActorKind: store.ActorConnection, Path: "/"},
		store.View{ID: "2", TS: at(10, 0), ActorID: "u1", ActorKind: store.ActorUser, UserID: "u1", Path: "/"},
		store.View{ID: "3", TS: at(10, 0), ActorID: "i1", ActorKind: store.ActorInstall, Path: "/", Kind: "app"},
	)
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "4", Project: "app", EventName: "x", TS: at(10, 0), ReceivedAt: at(10, 0), ActorID: "legacy", ActorKind: ""},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT actor_id, actor_kind FROM actors WHERE project='app' ORDER BY actor_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			t.Fatal(err)
		}
		got[id] = kind
	}
	want := map[string]string{"u1": "user", "i1": "install"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("actors = %v, want %v (connection and legacy '' actors are never cohorted)", got, want)
	}
}
```

(`reflect` import needed.) Also assert in the existing cohort test that `v_retention` exposes `actor_kind` and that `AggregateRetentionDay` groups by it.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store/sqlite/ -run 'Retention|Actors'`
Expected: FAIL (no such column: surface / actor_kind mismatch).

- [ ] **Step 3: Implement `retention.go`**

```go
// actorSources lists the raw tables an actor can appear in. Both carry
// actor_kind since 012, so the source table no longer implies anything
// about the population; only the kind does.
var actorSources = []string{"views", "product_events"}

// cohortKinds are the actor kinds stable enough to cohort. A connection
// hash rotates with the salt (and a pre-012 product row carries ''), so
// recording those only ever produced an offset-0 row.
const cohortKinds = `('user', 'install')`

// UpsertActors records first/last seen for every user- or install-identified
// actor active on the given day. Must run before AggregateRetentionDay for
// the same day, and before that day's raw rows are deleted.
func (d *DB) UpsertActors(ctx context.Context, project string, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, table := range actorSources {
			q := fmt.Sprintf(`
INSERT INTO actors (project, actor_id, actor_kind, first_seen_day, last_seen_day)
SELECT ?, actor_id, actor_kind, ?, ?
FROM %s WHERE project=? AND substr(ts,1,10)=? AND actor_id <> '' AND actor_kind IN %s
GROUP BY actor_id, actor_kind
ON CONFLICT(project, actor_id) DO UPDATE SET
  first_seen_day = MIN(actors.first_seen_day, excluded.first_seen_day),
  last_seen_day  = MAX(actors.last_seen_day,  excluded.last_seen_day)`, table, cohortKinds)
			if _, err := tx.ExecContext(ctx, q,
				project, day.String(), day.String(), project, day.String()); err != nil {
				return fmt.Errorf("upsert actors from %s: %w", table, err)
			}
		}
		return nil
	})
}
```

`AggregateRetentionDay`: the `active` CTE becomes two arms (`views`, `product_events`), the insert column list is `(project, actor_kind, cohort_day, day_offset, actors)`, and the select/group use `a.actor_kind` in place of `a.surface`. Delete `surfaceWeb`/`surfaceApp`. `PruneActors` is unchanged. Keep the doc comment about anonymous projects.

- [ ] **Step 4: Failing identity tests**

In `identities_test.go` the fixtures become `store.View` rows (kind web and app) plus product events; the assertions read `(actors, users, views, events)` and expect `views` = what was `hits + views`. Any read of `hits` is removed.

- [ ] **Step 5: Implement `identities.go`**

Replace the `src` CTE and the insert:

```go
			q := fmt.Sprintf(`
INSERT OR REPLACE INTO agg_identity_daily
	(project, day, kind, id, actors, users, views, events)
WITH src AS (
  SELECT %[1]s AS id, actor_id, user_id, 1 AS is_view, 0 AS is_event
  FROM views          WHERE project=? AND substr(ts,1,10)=? AND %[1]s <> ''
  UNION ALL
  SELECT %[1]s, actor_id, user_id, 0, 1
  FROM product_events WHERE project=? AND substr(ts,1,10)=? AND %[1]s <> ''
),
ranked AS (
  SELECT id,
         COUNT(DISTINCT actor_id) AS actors,
         COUNT(DISTINCT NULLIF(user_id, '')) AS users,
         SUM(is_view) AS views, SUM(is_event) AS events,
         ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, id) AS rn
  FROM src GROUP BY id
)
SELECT ?, ?, ?, id, actors,
       CASE WHEN ? = 'user' THEN 1 ELSE users END,
       views, events
FROM ranked WHERE rn <= %[2]d`, k.column, topNDimension)

			if _, err := tx.ExecContext(ctx, q,
				project, day.String(), project, day.String(),
				project, day.String(), k.kind, k.kind); err != nil {
```

Update the comment "Must run before AggregateAppDay" to "before AggregateViewDay and AggregateProductDay".

- [ ] **Step 6: Run the whole package**

Run: `go test ./internal/store/...`
Expected: PASS. Then `go vet ./internal/store/...` clean.

- [ ] **Step 7: Commit**

```bash
git add internal/store
git commit -m "feat(store): cohort retention on actor kind and count views once in identity rollups"
```

---

### Task 5: Migration 012 fold test

Spec §11 ("A dedicated `TestMigration012Folds`"). The test opens a database migrated only through 011, seeds every old table, runs 012 and asserts the folded totals.

**Files:**
- Modify: `internal/store/sqlite/migrate.go`
- Create: `internal/store/sqlite/migration012_test.go`

**Interfaces:**
- Produces: `(*DB).migrateThrough(ctx, maxVersion int) error` (unexported; `Migrate` calls it with `math.MaxInt`).

- [ ] **Step 1: Refactor `Migrate` to take a ceiling**

In `migrate.go`:

```go
func (d *DB) Migrate(ctx context.Context) error {
	return d.migrateThrough(ctx, math.MaxInt)
}

// migrateThrough applies every pending migration whose version is <=
// maxVersion. Migrate uses no ceiling; tests use one to build a database
// at an older schema and exercise the next migration against it.
func (d *DB) migrateThrough(ctx context.Context, maxVersion int) error {
```

with the loop body unchanged except `if done > 0 || version > maxVersion { continue }`. Add `"math"` to the imports.

- [ ] **Step 2: Write the test**

```go
package sqlite

import (
	"context"
	"testing"
)

// newTestDBAt opens a temp database migrated only through version v.
func newTestDBAt(t *testing.T, v int) *DB {
	t.Helper()
	db, err := open(t.TempDir() + "/at.db") // the same opener newTestDB uses
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.migrateThrough(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigration012Folds(t *testing.T) {
	db := newTestDBAt(t, 8)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// raw rows
	exec(`INSERT INTO web_hits (id, project, ts, received_at, actor_id, user_id, group_id, host, path,
	      referrer_source, utm_source, utm_medium, utm_campaign, country, device, browser, os)
	      VALUES ('w1','p','2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','h1','','','x.com','/a','google','','','','DE','mobile','Chrome','Android'),
	             ('w2','p','2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','u1','u1','','x.com','/b','','','','','DE','desktop','Firefox','Linux')`)
	exec(`INSERT INTO app_views (id, project, ts, received_at, actor_id, user_id, group_id, session_id, screen,
	      platform, app_version, os_version, device_model, locale, country)
	      VALUES ('a1','p','2026-09-10T11:00:00Z','2026-09-10T11:00:00Z','i1','','','s1','/home','ios','2.4.1','17.2','iPhone15,2','en-US','FR')`)
	exec(`INSERT INTO product_events (id, project, event_name, ts, received_at, actor_id, user_id, group_id, platform, app_version, attributes)
	      VALUES ('e1','p','signup','2026-09-10T12:00:00Z','2026-09-10T12:00:00Z','u1','u1','','IOS','2.4.1','{}')`)
	// aggregates: one day where both families overlap
	exec(`INSERT INTO agg_web_daily VALUES ('p','2026-09-01',10,25,12,3,600)`)
	exec(`INSERT INTO agg_app_daily VALUES ('p','2026-09-01',6,20,8,480)`)
	exec(`INSERT INTO agg_web_pages VALUES ('p','2026-09-01','/home',8,15), ('p','2026-09-01','/post',4,10)`)
	exec(`INSERT INTO agg_app_screens VALUES ('p','2026-09-01','/home',5,12)`)
	exec(`INSERT INTO agg_web_hosts VALUES ('p','2026-09-01','x.com',9,20)`)
	exec(`INSERT INTO agg_web_referrers VALUES ('p','2026-09-01','google',3,4)`)
	exec(`INSERT INTO agg_web_utm VALUES ('p','2026-09-01','nl','email','aug',6,9)`)
	exec(`INSERT INTO agg_web_countries VALUES ('p','2026-09-01','DE',7,18)`)
	exec(`INSERT INTO agg_app_countries VALUES ('p','2026-09-01','DE',2,6)`)
	exec(`INSERT INTO agg_web_devices VALUES ('p','2026-09-01','mobile',5,9)`)
	exec(`INSERT INTO agg_app_devices VALUES ('p','2026-09-01','iPhone15,2',5,12)`)
	exec(`INSERT INTO agg_web_browsers VALUES ('p','2026-09-01','Chrome',7,14)`)
	exec(`INSERT INTO agg_web_os VALUES ('p','2026-09-01','Android',5,9)`)
	exec(`INSERT INTO agg_app_os VALUES ('p','2026-09-01','android','14',2,6), ('p','2026-09-01','ios','17.4',3,6)`)
	exec(`INSERT INTO agg_app_versions VALUES ('p','2026-09-01','ios','2.4.1',5,12)`)
	exec(`INSERT INTO actors VALUES ('p','u1','web','2026-08-01','2026-09-01'), ('p','i1','app','2026-08-01','2026-09-01')`)
	exec(`INSERT INTO agg_retention VALUES ('p','web','2026-08-01',0,10), ('p','app','2026-08-01',0,4)`)
	exec(`INSERT INTO agg_identity_daily VALUES ('p','2026-09-01','user','u1',1,1,5,3,2)`)
	exec(`INSERT INTO agg_product_attrs VALUES ('p','2026-09-01','signup','$platform','ios',3,3),
	                                           ('p','2026-09-01','signup','$platform','iOS',2,2),
	                                           ('p','2026-09-01','signup','$app_version','2.4.1',5,5)`)

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migration 012: %v", err)
	}

	row := func(q string, dst ...any) {
		t.Helper()
		if err := db.db.QueryRowContext(ctx, q).Scan(dst...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var s string
	var a, b, c, d2, e int

	// raw rows folded with kind, actor_kind, os normalised, screen -> path
	row(`SELECT kind || '|' || actor_kind || '|' || path || '|' || os FROM views WHERE id='w1'`, &s)
	if s != "web|connection|/a|Android" {
		t.Errorf("w1 = %q", s)
	}
	row(`SELECT kind || '|' || actor_kind FROM views WHERE id='w2'`, &s)
	if s != "web|user" {
		t.Errorf("w2 = %q", s)
	}
	row(`SELECT kind || '|' || actor_kind || '|' || path || '|' || os || '|' || app_version || '|' || session_id FROM views WHERE id='a1'`, &s)
	if s != "app|install|/home|iOS|2.4.1|s1" {
		t.Errorf("a1 = %q", s)
	}
	row(`SELECT os || '|' || actor_kind FROM product_events WHERE id='e1'`, &s)
	if s != "iOS|" {
		t.Errorf("e1 = %q (os normalised, actor_kind empty for old rows)", s)
	}

	// daily: one row per kind; app bounces are zero
	row(`SELECT visitors, views, sessions, bounces, duration_sec FROM agg_views_daily WHERE day='2026-09-01' AND kind='web'`, &a, &b, &c, &d2, &e)
	if a != 10 || b != 25 || c != 12 || d2 != 3 || e != 600 {
		t.Errorf("web daily = %d %d %d %d %d", a, b, c, d2, e)
	}
	row(`SELECT visitors, views, sessions, bounces, duration_sec FROM agg_views_daily WHERE day='2026-09-01' AND kind='app'`, &a, &b, &c, &d2, &e)
	if a != 6 || b != 20 || c != 8 || d2 != 0 || e != 480 {
		t.Errorf("app daily = %d %d %d %d %d", a, b, c, d2, e)
	}
	// paths and countries sum on collision; the others copy
	row(`SELECT visitors, views FROM agg_views_paths WHERE path='/home'`, &a, &b)
	if a != 13 || b != 27 {
		t.Errorf("/home = (%d,%d), want (13,27)", a, b)
	}
	row(`SELECT visitors, views FROM agg_views_countries WHERE country='DE'`, &a, &b)
	if a != 9 || b != 24 {
		t.Errorf("DE = (%d,%d), want (9,24)", a, b)
	}
	row(`SELECT visitors FROM agg_views_hosts WHERE host='x.com'`, &a)
	if a != 9 {
		t.Errorf("host = %d", a)
	}
	row(`SELECT visitors FROM agg_views_referrers WHERE source='google'`, &a)
	if a != 3 {
		t.Errorf("referrer = %d", a)
	}
	row(`SELECT visitors FROM agg_views_utm WHERE utm_campaign='aug'`, &a)
	if a != 6 {
		t.Errorf("utm = %d", a)
	}
	row(`SELECT visitors FROM agg_views_browsers WHERE browser='Chrome' AND browser_version=''`, &a)
	if a != 7 {
		t.Errorf("browser = %d", a)
	}
	// os: web 'Android' ('' version) stays apart from app 'Android' '14'
	row(`SELECT COUNT(*) FROM agg_views_os WHERE os='Android'`, &a)
	if a != 2 {
		t.Errorf("Android os rows = %d, want 2", a)
	}
	row(`SELECT visitors FROM agg_views_os WHERE os='iOS' AND os_version='17.4'`, &a)
	if a != 3 {
		t.Errorf("iOS 17.4 = %d", a)
	}
	row(`SELECT visitors FROM agg_views_app_versions WHERE os='iOS' AND app_version='2.4.1'`, &a)
	if a != 5 {
		t.Errorf("app version = %d", a)
	}
	row(`SELECT visitors FROM agg_views_devices WHERE device='mobile' AND device_model=''`, &a)
	if a != 5 {
		t.Errorf("web device = %d", a)
	}
	row(`SELECT visitors FROM agg_views_devices WHERE device='' AND device_model='iPhone15,2'`, &a)
	if a != 5 {
		t.Errorf("app device = %d", a)
	}
	row(`SELECT COUNT(*) FROM agg_views_displays`, &a)
	if a != 0 {
		t.Errorf("displays should start empty, got %d", a)
	}
	// retention and identities
	row(`SELECT actor_kind FROM actors WHERE actor_id='u1'`, &s)
	if s != "user" {
		t.Errorf("u1 actor_kind = %q", s)
	}
	row(`SELECT actor_kind FROM actors WHERE actor_id='i1'`, &s)
	if s != "install" {
		t.Errorf("i1 actor_kind = %q", s)
	}
	row(`SELECT actors FROM agg_retention WHERE actor_kind='install' AND cohort_day='2026-08-01'`, &a)
	if a != 4 {
		t.Errorf("install cohort = %d", a)
	}
	row(`SELECT views, events FROM agg_identity_daily WHERE id='u1'`, &a, &b)
	if a != 8 || b != 2 {
		t.Errorf("identity daily = (%d,%d), want (8,2): hits + views", a, b)
	}
	// product attrs: $platform rows normalised, merged and renamed
	row(`SELECT count, unique_users FROM agg_product_attrs WHERE attr_key='$os' AND attr_value='iOS'`, &a, &b)
	if a != 5 || b != 5 {
		t.Errorf("$os iOS = (%d,%d), want (5,5)", a, b)
	}
	row(`SELECT COUNT(*) FROM agg_product_attrs WHERE attr_key='$platform'`, &a)
	if a != 0 {
		t.Errorf("$platform rows left: %d", a)
	}
	// old tables are gone
	row(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND (name LIKE 'agg_web_%' OR name LIKE 'agg_app_%' OR name IN ('web_hits','app_views'))`, &a)
	if a != 0 {
		t.Errorf("%d old tables survived", a)
	}
}
```

Check how `newTestDB` in `sqlite_test.go` opens its database and use the same constructor in `newTestDBAt` (it may be `Open(dsn)` returning `store.Store` that needs a type assertion to `*DB`; mirror whatever `newTestDB` does).

- [ ] **Step 3: Run**

Run: `go test ./internal/store/sqlite/ -run 'TestMigration012Folds|TestMigrationsAreIdempotent|TestMigrationViews|TestProjectTablesMatchesSchema|TestPruneAggregatesCoversAllAggTables'`
Expected: PASS. If the `$os` merge asserts (5,5) fails with (3,3) or (2,2), the `INSERT OR REPLACE … FROM os_attrs` ran against a temp table that did not group — check the `GROUP BY … 5` ordinal in 012.

- [ ] **Step 4: Commit**

```bash
git add internal/store
git commit -m "test(store): prove migration 012 folds every old table into the views family"
```

---

### Task 6: Configuration — one views retention class

Spec §10. Also updates the env table in `docs/deployment.md` because `TestDeploymentDocumentsEveryEnvVar` reads it (that test lives in `internal/api`, which compiles again only after Task 9, but the doc edit belongs with this change).

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `internal/manage/registry.go`, `internal/manage/registry_test.go` (if it names `.Web`/`.App`), `docs/deployment.md`, `.env.example`

**Interfaces:**
- Produces: `config.Retention{Views, Product RetentionClass}`, `config.RetentionOverride{Views, Product *RetentionClassOverride}` (JSON `views`, `product`; legacy `web`/`app` decode into `Views`), env `RETENTION_VIEWS_RAW_DAYS`/`RETENTION_VIEWS_AGGREGATE_DAYS`, `MaxEventAge()` from `Retention.Views.RawDays`, `Snapshot.RetentionFor` applying `Views` and `Product`.

- [ ] **Step 1: Failing config tests**

In `config_test.go` replace `TestAppRetentionDefaultsAndMaxEventAge`, `TestAppRetentionFromEnv`, `TestRejectsNegativeAppRetention` and the `RETENTION_WEB_*` entries in the table-driven load test with:

```go
func TestViewsRetentionDefaultsAndMaxEventAge(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.Views.RawDays != 30 || c.Retention.Views.AggregateDays != 365 {
		t.Fatalf("views retention = %+v", c.Retention.Views)
	}
	if want := 30 * 24 * time.Hour; c.MaxEventAge() != want {
		t.Errorf("MaxEventAge() = %v, want %v", c.MaxEventAge(), want)
	}
}

func TestViewsRetentionFromEnv(t *testing.T) {
	c, err := load(t, map[string]string{
		"RETENTION_VIEWS_RAW_DAYS":       "14",
		"RETENTION_VIEWS_AGGREGATE_DAYS": "90",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.Views.RawDays != 14 || c.Retention.Views.AggregateDays != 90 {
		t.Errorf("views retention = %+v", c.Retention.Views)
	}
	if want := 14 * 24 * time.Hour; c.MaxEventAge() != want {
		t.Errorf("MaxEventAge() = %v, want %v", c.MaxEventAge(), want)
	}
}

func TestRejectsNegativeViewsRetention(t *testing.T) {
	if _, err := load(t, map[string]string{"RETENTION_VIEWS_RAW_DAYS": "-1"}); err == nil {
		t.Fatal("want error for negative views retention")
	}
}

func TestRetentionOverrideDecodesLegacyKeys(t *testing.T) {
	var o RetentionOverride
	if err := json.Unmarshal([]byte(`{"web":{"raw_days":3},"app":{"raw_days":9,"aggregate_days":30}}`), &o); err != nil {
		t.Fatal(err)
	}
	if o.Views == nil || o.Views.RawDays == nil || *o.Views.RawDays != 9 {
		t.Fatalf("legacy web/app should decode into views with the larger raw window, got %+v", o.Views)
	}
	if o.Views.AggregateDays == nil || *o.Views.AggregateDays != 30 {
		t.Errorf("aggregate_days = %v", o.Views.AggregateDays)
	}
	var v RetentionOverride
	if err := json.Unmarshal([]byte(`{"views":{"raw_days":5},"web":{"raw_days":99}}`), &v); err != nil {
		t.Fatal(err)
	}
	if *v.Views.RawDays != 5 {
		t.Errorf("an explicit views key must win over legacy keys, got %d", *v.Views.RawDays)
	}
	out, _ := json.Marshal(RetentionOverride{Views: &RetentionClassOverride{RawDays: intp(7)}})
	if string(out) != `{"views":{"raw_days":7,"aggregate_days":null},"product":null}` {
		t.Errorf("marshal = %s (legacy keys must never be written back)", out)
	}
}

func intp(n int) *int { return &n }
```

Add `TestRenamedVariablesRefuse` entries: `"RETENTION_WEB_RAW_DAYS": "RETENTION_VIEWS_RAW_DAYS"`, `"RETENTION_WEB_AGGREGATE_DAYS": "RETENTION_VIEWS_AGGREGATE_DAYS"`, `"RETENTION_APP_RAW_DAYS": "RETENTION_VIEWS_RAW_DAYS"`, `"RETENTION_APP_AGGREGATE_DAYS": "RETENTION_VIEWS_AGGREGATE_DAYS"`. Replace any other `RETENTION_WEB_*`/`RETENTION_APP_*` in the file with the views names and `.Web`/`.App` with `.Views`. Check whether `intPtr`/`intp` already exists in the package's tests and reuse it.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/`
Expected: compile errors on `.Views`.

- [ ] **Step 3: Implement**

In `config.go`:

```go
type Retention struct {
	Views   RetentionClass `json:"views"`
	Product RetentionClass `json:"product"`
}

type RetentionOverride struct {
	Views   *RetentionClassOverride `json:"views"`
	Product *RetentionClassOverride `json:"product"`
}

// UnmarshalJSON accepts the pre-views keys `web` and `app` and folds them
// into Views (the larger of each field wins), so a per-project override
// stored before the merge keeps working. An explicit `views` key wins
// outright. Marshal never writes the legacy keys back.
func (o *RetentionOverride) UnmarshalJSON(b []byte) error {
	var raw struct {
		Views   *RetentionClassOverride `json:"views"`
		Product *RetentionClassOverride `json:"product"`
		Web     *RetentionClassOverride `json:"web"`
		App     *RetentionClassOverride `json:"app"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	o.Product = raw.Product
	o.Views = raw.Views
	if o.Views == nil && (raw.Web != nil || raw.App != nil) {
		o.Views = &RetentionClassOverride{}
		max := func(a, b *int) *int {
			switch {
			case a == nil:
				return b
			case b == nil:
				return a
			case *a >= *b:
				return a
			default:
				return b
			}
		}
		var web, app RetentionClassOverride
		if raw.Web != nil {
			web = *raw.Web
		}
		if raw.App != nil {
			app = *raw.App
		}
		o.Views.RawDays = max(web.RawDays, app.RawDays)
		o.Views.AggregateDays = max(web.AggregateDays, app.AggregateDays)
	}
	return nil
}
```

Env parsing block:

```go
		Retention: Retention{
			Views: RetentionClass{
				RawDays:       e.num("RETENTION_VIEWS_RAW_DAYS", 30),
				AggregateDays: e.num("RETENTION_VIEWS_AGGREGATE_DAYS", 365),
			},
			Product: RetentionClass{
				RawDays:       e.num("RETENTION_PRODUCT_RAW_DAYS", 30),
				AggregateDays: e.num("RETENTION_PRODUCT_AGGREGATE_DAYS", 365),
			},
		},
```

`renamed` gains:

```go
	{"RETENTION_WEB_RAW_DAYS", "RETENTION_VIEWS_RAW_DAYS"},
	{"RETENTION_WEB_AGGREGATE_DAYS", "RETENTION_VIEWS_AGGREGATE_DAYS"},
	{"RETENTION_APP_RAW_DAYS", "RETENTION_VIEWS_RAW_DAYS"},
	{"RETENTION_APP_AGGREGATE_DAYS", "RETENTION_VIEWS_AGGREGATE_DAYS"},
```

`validate()` loops `[]RetentionClass{c.Retention.Views, c.Retention.Product}`. `MaxEventAge`:

```go
// MaxEventAge is derived from the views raw window rather than separately
// configurable: the two must agree or a clamped timestamp could land in an
// already-aggregated day.
func (c *Config) MaxEventAge() time.Duration {
	return time.Duration(c.Retention.Views.RawDays) * 24 * time.Hour
}
```

Search the package for other `.Web`/`.App` uses (`grep -n "\.Web\b\|\.App\b" internal/config/*.go`) and fix them, including `LegacyAggregation`-era import code if it maps retention.

In `internal/manage/registry.go` `RetentionFor`:

```go
	apply(&r.Views, p.Retention.Views)
	apply(&r.Product, p.Retention.Product)
```

- [ ] **Step 4: Run**

Run: `go test ./internal/config/ ./internal/manage/`
Expected: PASS (manage tests that set `RetentionOverride{Web: …}` must be changed to `Views:`).

- [ ] **Step 5: `docs/deployment.md` env table**

In the table under `## Configure the collector` delete the four `RETENTION_WEB_*` / `RETENTION_APP_*` rows and add, after `BUFFER_CAPACITY`:

```markdown
| `RETENTION_VIEWS_RAW_DAYS` | Days raw page and screen views are kept before rollup. Also the oldest client timestamp accepted: older events are clamped to this edge. Default 30. |
| `RETENTION_VIEWS_AGGREGATE_DAYS` | Days view aggregates (and actors, cohorts, identities) are kept. Default 365. |
```

Below the table, after the litestream paragraph, add:

```markdown
`RETENTION_WEB_*` and `RETENTION_APP_*` were replaced by `RETENTION_VIEWS_*`
when web and app analytics merged into one family; a set old name refuses
the boot with the replacement named.
```

In `### Raspberry Pi and low-resource hosts` add a bullet:

```markdown
- Lower `RETENTION_VIEWS_RAW_DAYS` (for example `7`): raw view rows are
  the largest table, and the window only buys late-arrival tolerance for
  offline clients — events older than it are clamped, not lost.
```

In the `### Routine operations` table add a row after "Apply migrations only":

```markdown
| Upgrade across a schema change | Take a Litestream snapshot first (`litestream snapshots …`, or copy the db file while the service is stopped): migrations such as 012 (web and app folded into one views family) are irreversible |
```

- [ ] **Step 6: `.env.example`**

Lines 25–30 list the six commented retention variables. Replace them with:

```
#RETENTION_VIEWS_RAW_DAYS=30
#RETENTION_VIEWS_AGGREGATE_DAYS=365
#RETENTION_PRODUCT_RAW_DAYS=30
#RETENTION_PRODUCT_AGGREGATE_DAYS=365
```

`.env.example` ships inside the release tarball (`make dist`), so an operator copying it must not inherit a refused name.

- [ ] **Step 7: Commit**

```bash
git add internal/config internal/manage docs/deployment.md .env.example
git commit -m "feat(config): one RETENTION_VIEWS_* window replaces the web and app pairs"
```

---

### Task 7: The daily pass

Spec §7, §10 (prune cutoffs).

**Files:**
- Modify: `internal/jobs/jobs.go`, `internal/jobs/jobs_test.go`, `internal/jobs/errors_test.go`

**Interfaces:**
- Consumes: `Store.ViewDaysBefore`, `AggregateViewDay`, `PruneAggregates(ctx, project, viewsBefore, productBefore)` (Task 2), `config.Retention.Views` (Task 6).

- [ ] **Step 1: Update the tests first**

In `jobs_test.go`: `jobsVars` becomes `{"RETENTION_VIEWS_RAW_DAYS": "7", "RETENTION_VIEWS_AGGREGATE_DAYS": "365", "RETENTION_PRODUCT_RAW_DAYS": "7", "RETENTION_PRODUCT_AGGREGATE_DAYS": "365"}`. Every `store.WebHit`/`store.AppView` fixture becomes a `store.View` (`Kind: "web"`/`"app"`, `ActorKind` set — `store.ActorUser` where a `UserID` is set, else `store.ActorInstall` for app rows and `store.ActorConnection` for web rows), written with `WriteViews`. Reads of `agg_web_daily`/`agg_app_daily` become `agg_views_daily … AND kind='web'`/`'app'`; `pageviews`→`views`, `actives`→`visitors`; `surface`→`actor_kind` with `web`→`user`, `app`→`install`. In `errors_test.go` the failing-store fakes rename `AggregateWebDay`/`AggregateAppDay` to `AggregateViewDay`, `WebDaysBefore`/`AppDaysBefore` to `ViewDaysBefore`, and `PruneAggregates` loses its third argument.

Add one test that the raw window applies to both kinds at once:

```go
func TestDailyPassRollsUpEveryKindPastTheWindow(t *testing.T) {
	st, _, r := setup(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	old := mustTime("2026-08-10T10:00:00Z") // 12 days before the fixed clock; window is 7
	if err := st.WriteViews(ctx, []store.View{
		{ID: "w", Project: "app", TS: old, ReceivedAt: old, Kind: "web", ActorID: "h", ActorKind: store.ActorConnection, Path: "/"},
		{ID: "a", Project: "app", TS: old, ReceivedAt: old, Kind: "app", ActorID: "i", ActorKind: store.ActorInstall, Path: "/home"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+os.Getenv("JOBS_TEST_DB")) // the pattern the file's other tests use
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var kinds int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agg_views_daily WHERE project='app' AND day='2026-08-10'`).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if kinds != 2 {
		t.Errorf("agg_views_daily rows = %d, want one per kind", kinds)
	}
	var raw int
	if err := db.QueryRow(`SELECT COUNT(*) FROM views WHERE project='app'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 0 {
		t.Errorf("raw views left = %d", raw)
	}
}
```

(`setup` stores the database path in `JOBS_TEST_DB`, and the file already opens it with `sql.Open("sqlite", "file:"+os.Getenv("JOBS_TEST_DB"))`; the imports `database/sql` and `os` are already present.)

- [ ] **Step 2: Implement**

`jobs.Store`:

```go
type Store interface {
	ProjectAliases(ctx context.Context) ([]string, error)
	ViewDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	AggregateViewDay(ctx context.Context, project string, day civil.Date) error
	AggregateProductDay(ctx context.Context, project string, day civil.Date, attrs []string, topN int) error
	UpsertActors(ctx context.Context, project string, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, project string, day civil.Date) error
	PruneActors(ctx context.Context, project string, before civil.Date) error
	AggregateIdentityDay(ctx context.Context, project string, day civil.Date) error
	PruneIdentities(ctx context.Context, project string, before civil.Date) error
	PruneAggregates(ctx context.Context, project string, viewsBefore, productBefore civil.Date) error
	RebuildFlatView(ctx context.Context, keys []string) error
	IncrementalVacuum(ctx context.Context) error
}
```

In `RunDailyPass`, replace the web and app blocks with one:

```go
		days, err := r.store.ViewDaysBefore(ctx, id, today.AddDays(-ret.Views.RawDays))
		if err != nil {
			return err
		}
		for _, day := range days {
			if err := r.store.AggregateViewDay(ctx, id, day); err != nil {
				r.logger.Error("aggregate views failed", "project", id, "day", day.String(), "error", err)
			}
		}
```

keep the product block, then:

```go
		if err := r.store.PruneAggregates(ctx, id,
			today.AddDays(-ret.Views.AggregateDays),
			today.AddDays(-ret.Product.AggregateDays)); err != nil {
			r.logger.Error("prune failed", "project", id, "error", err)
		}
		if err := r.store.PruneActors(ctx, id, today.AddDays(-ret.Views.AggregateDays)); err != nil {
			r.logger.Error("prune actors failed", "project", id, "error", err)
		}
		if err := r.store.PruneIdentities(ctx, id, today.AddDays(-ret.Views.AggregateDays)); err != nil {
			r.logger.Error("prune identities failed", "project", id, "error", err)
		}
```

`allRawDays` iterates `r.store.ViewDaysBefore, r.store.ProductDaysBefore`. Update the comment above the identity loop ("across all three classes" → "across both raw tables"; "a web-only project has no app_views" → "restricting them to aged-out days would leave the users, groups and retention pages a whole raw window stale").

- [ ] **Step 3: Run**

Run: `go test ./internal/jobs/`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/jobs
git commit -m "refactor(jobs): roll up one views family in the daily pass"
```

---

### Task 8: Pipeline and ingest — kind, aliases, actor kind, display size

Spec §4, §5. After this task `go build ./...` succeeds again (the API package is fixed in Task 9 but compiles because it never used the old row types directly — verify; if it does not compile, Task 9's edits are the fix).

**Files:**
- Modify: `internal/pipeline/pipeline.go`, `pipeline_test.go`, `internal/server/server.go` (Enqueuer), `internal/server/ingest.go`, `internal/server/handlers.go`, `internal/server/ingest_test.go`, `internal/server/server_test.go`, `internal/app/app_test.go`, `scripts/smokecheck/main.go`

**Interfaces:**
- Consumes: `store.View`, `store.ProductEvent.{OS,ActorKind}`, `enrich.ParseUserAgent` (4 returns), `enrich.NormalizeOS`.
- Produces: `pipeline.Buffer.EnqueueView(store.View)`, `server.Enqueuer{EnqueueView, EnqueueEvent}`, ingest names `namePageView = "$page_view"`, `nameScreenView = "$screen_view"`, `aliasPageview = "$pageview"`; `resolved.{Kind, DisplayWidth, DisplayHeight, OS}` fields; `resolveIdentity` returns `(actor, actorKind, user, group string)`.

- [ ] **Step 1: Pipeline**

`pipeline.go`: `Sink` becomes `WriteViews(ctx, []store.View) error` + `WriteProductEvents`; `item` has `view *store.View` and `event *store.ProductEvent`; `EnqueueView(v store.View)` replaces `EnqueueHit`/`EnqueueAppView`; `Run` keeps two slices, flush labels `"views"` and `"product_events"`, the threshold `len(views)+len(events)`. In `pipeline_test.go` the `fakeSink` drops `WriteWebHits`/`WriteAppViews` for `WriteViews`, fixtures become `store.View`. Run `go test ./internal/pipeline/` → PASS.

- [ ] **Step 2: Failing ingest tests**

In `server_test.go`, `fakeQueue` keeps `views []store.View` and `events`, with `EnqueueView`; delete `EnqueueHit`/`EnqueueAppView` and the `hits` slice. Replace `TestRoutesPageviewScreenViewAndCustom` and `TestBotFilterAppliesOnlyToPageviews` with:

```go
func TestRoutesViewsAndCustom(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$os":"ios","$app_version":"2.4.1"},
	  "events":[
	    {"name":"$page_view","attributes":{"$host":"app.com","$path":"/pricing","$utm_source":"hn","$display_width":1920,"$display_height":1080,"$locale":"de-DE"}},
	    {"name":"$screen_view","attributes":{"$screen":"/settings","$os_version":"17.2","$device_model":"iPhone15,2","$locale":"en-US","$session_id":"s1"}},
	    {"name":"subscribed","attributes":{"plan":"pro"}}
	  ]}`
	w := post(h, body, map[string]string{"Origin": testOrigin})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if res := decodeResult(t, w); res.Accepted != 3 || res.Rejected != 0 {
		t.Errorf("result = %+v", res)
	}
	if len(q.views) != 2 {
		t.Fatalf("views = %+v", q.views)
	}
	web, app := q.views[0], q.views[1]
	if web.Kind != "web" || web.Host != "app.com" || web.Path != "/pricing" || web.UTMSource != "hn" ||
		web.Country != "DE" || web.Browser != "Chrome" || web.BrowserVersion != "126" || web.Device != "desktop" ||
		web.DisplayWidth != 1920 || web.DisplayHeight != 1080 || web.Locale != "de-DE" {
		t.Errorf("web view = %+v", web)
	}
	// a declared $os overrides the parsed one, on any kind
	if web.OS != "iOS" {
		t.Errorf("web os = %q, want the declared iOS to beat the parsed Windows", web.OS)
	}
	if app.Kind != "app" || app.Path != "/settings" || app.OS != "iOS" || app.OSVersion != "17.2" ||
		app.AppVersion != "2.4.1" || app.DeviceModel != "iPhone15,2" || app.Locale != "en-US" ||
		app.SessionID != "s1" || app.Country != "DE" || app.Browser != "" || app.Device != "" {
		t.Errorf("app view = %+v", app)
	}
	if len(q.events) != 1 || q.events[0].OS != "iOS" || q.events[0].AppVersion != "2.4.1" {
		t.Errorf("events = %+v", q.events)
	}
}

func TestLegacyNamesAreSilentAliases(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$pageview","attributes":{"$host":"app.com","$path":"/x","$platform":"linux"}}`), nil)
	res := decodeResult(t, w)
	if res.Accepted != 1 || len(res.Warnings) != 0 {
		t.Errorf("aliases must be accepted without a warning: %+v", res)
	}
	if len(q.views) != 1 || q.views[0].Kind != "web" || q.views[0].OS != "Linux" {
		t.Errorf("views = %+v", q.views)
	}
}

func TestKindDeclaredValidatedAndDefaulted(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$kind":"cli"},
	  "events":[
	    {"name":"$screen_view","attributes":{"$screen":"deploy"}},
	    {"name":"$page_view","attributes":{"$path":"/","$kind":"Bad Kind!"}},
	    {"name":"$page_view","attributes":{"$path":"/","$kind":""}}
	  ]}`
	// A crawler User-Agent: web-kind rows are filtered, the cli row is not.
	w := post(h, body, map[string]string{"User-Agent": "Googlebot/2.1"})
	res := decodeResult(t, w)
	if res.Accepted != 3 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Reason, `invalid $kind "Bad Kind!", using "web"`) {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	// The invalid kind and the empty kind both fall back to the name's
	// default, web, and a crawler User-Agent on a web row is a bot:
	// accepted, never stored. Only the cli view reaches the queue.
	if len(q.views) != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	if q.views[0].Kind != "cli" || q.views[0].Path != "deploy" || q.views[0].Browser != "" {
		t.Errorf("cli view = %+v (non-web kinds are never parsed or filtered)", q.views[0])
	}
}
```

Then:

```go
func TestBotFilterAppliesToWebKindOnly(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"$page_view","attributes":{"$host":"app.com","$path":"/x"}},
		{"name":"$screen_view","attributes":{"$screen":"/s"}},
		{"name":"$page_view","attributes":{"$path":"/y","$kind":"app"}},
		{"name":"custom"}`)
	w := post(h, body, map[string]string{"User-Agent": "Googlebot/2.1"})
	if len(q.views) != 2 {
		t.Errorf("only web-kind rows are bot-filtered, got %+v", q.views)
	}
	for _, v := range q.views {
		if v.Kind == "web" {
			t.Errorf("web view survived the bot filter: %+v", v)
		}
	}
	if len(q.events) != 1 {
		t.Errorf("bot filter must not touch custom events: %+v", q.events)
	}
	if res := decodeResult(t, w); res.Accepted != 4 || res.Rejected != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestViewRequiresALocation(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$page_view"},{"name":"$screen_view","attributes":{"$path":"/via-path"}}`), nil)
	res := decodeResult(t, w)
	if res.Rejected != 1 || len(res.Errors) != 1 || res.Errors[0].Reason != "view requires $path or $screen" {
		t.Errorf("result = %+v", res)
	}
	if len(q.views) != 1 || q.views[0].Path != "/via-path" || q.views[0].Kind != "app" {
		t.Errorf("views = %+v ($path is accepted on a screen view)", q.views)
	}
}

func TestDisplaySizeParsing(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$page_view","attributes":{"$path":"/","$display_width":"1440","$display_height":-5}},
		{"name":"$page_view","attributes":{"$path":"/b","$display_width":"wide"}}`), nil)
	res := decodeResult(t, w)
	if res.Accepted != 2 || len(res.Warnings) != 2 {
		t.Errorf("result = %+v (want one warning per unusable value)", res)
	}
	if q.views[0].DisplayWidth != 1440 || q.views[0].DisplayHeight != 0 || q.views[1].DisplayWidth != 0 {
		t.Errorf("views = %+v", q.views)
	}
}

func TestActorKindRecorded(t *testing.T) {
	q, h := testServerWithIdentity(t, "identified")
	body := `{"key":"` + testKey + `","events":[
	    {"name":"$page_view","attributes":{"$path":"/","$user_id":"u1","$install_id":"i1"}},
	    {"name":"$page_view","attributes":{"$path":"/","$install_id":"i1"}},
	    {"name":"$page_view","attributes":{"$path":"/"}},
	    {"name":"x","attributes":{"$install_id":"i1"}}]}`
	post(h, body, nil)
	if len(q.views) != 3 || len(q.events) != 1 {
		t.Fatalf("views=%d events=%d", len(q.views), len(q.events))
	}
	want := []string{store.ActorUser, store.ActorInstall, store.ActorConnection}
	for i, v := range q.views {
		if v.ActorKind != want[i] {
			t.Errorf("view %d actor_kind = %q, want %q", i, v.ActorKind, want[i])
		}
	}
	if q.events[0].ActorKind != store.ActorInstall {
		t.Errorf("event actor_kind = %q", q.events[0].ActorKind)
	}
}
```

Update every other test in `server_test.go` and `app_test.go` that posts `$pageview`/`$screen_view` or reads `q.hits`: `$pageview` may stay in one or two tests (it is a supported alias) but the ones asserting stored fields should use `$page_view` and read `q.views`; `Platform` → `OS`, `Screen` → `Path`. In `ingest_test.go` rename `$platform` in fixtures to `$os` except in one alias assertion, and add to `TestResolveAttributesSplitsReservedFromCustom` that `$kind`, `$display_width`, `$display_height` resolve into `Kind`, `DisplayWidth`, `DisplayHeight`.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/server/`
Expected: compile errors (`EnqueueHit`, `Kind`, …).

- [ ] **Step 4: Implement `ingest.go`**

Names and kinds:

```go
// Reserved event names. Both are views; the name only supplies the default
// kind. $pageview is the pre-views spelling every deployed tag still sends,
// accepted silently and never documented.
const (
	namePageView   = "$page_view"
	nameScreenView = "$screen_view"
	aliasPageview  = "$pageview"
)

// kindPattern bounds a client-declared $kind so a typo cannot mint a
// dimension value; an invalid kind falls back to the name's default.
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

// viewName reports whether name is a view and its default kind.
func viewName(name string) (kind string, ok bool) {
	switch name {
	case namePageView, aliasPageview:
		return "web", true
	case nameScreenView:
		return "app", true
	}
	return "", false
}
```

`resolved` gains `Kind string`, `DisplayWidth, DisplayHeight int`, and `Platform` is renamed `OS` (grep the package for `rv.Platform`). Raw display strings are parsed after resolution, so add `displayWidthRaw, displayHeightRaw string` to `resolved` and:

```go
	"$kind":           func(r *resolved, v string) { r.Kind = v },
	"$os":             func(r *resolved, v string) { r.OS = v },
	"$platform":       func(r *resolved, v string) { r.OS = v }, // alias, see aliasKeys in docs_sync_test
	"$display_width":  func(r *resolved, v string) { r.displayWidthRaw = v },
	"$display_height": func(r *resolved, v string) { r.displayHeightRaw = v },
```

(`$app_version`, `$os_version`, `$device_model`, `$locale`, `$screen`, location and identity keys stay as they are.) Add:

```go
// parseDisplay turns a raw pixel string into an int, reporting whether the
// value was present but unusable (non-integer or <= 0) so the caller can
// warn. Absent → 0, no warning.
func parseDisplay(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0, true
	}
	return n, false
}
```

- [ ] **Step 5: Implement `handlers.go`**

`resolveIdentity` returns the kind too:

```go
func resolveIdentity(p *manage.Project, rv resolved, salt, ip, ua string) (actor, actorKind, user, group string) {
	raw := rv.UserID
	actorKind = store.ActorUser
	if raw == "" {
		raw = rv.InstallID
		actorKind = store.ActorInstall
	}
	if raw == "" {
		actorKind = store.ActorConnection
	}
	if p.Identity == config.IdentityIdentified {
		actor = raw
		if actor == "" {
			// No client identifier at all: fall back to the rotating hash
			// rather than dropping the event.
			actor = identity.VisitorHash(salt, ip, ua, p.Alias)
		}
		return actor, actorKind, rv.UserID, rv.GroupID
	}
	if raw == "" {
		actor = identity.VisitorHash(salt, ip, ua, p.Alias)
	} else {
		actor = identity.ActorHash(salt, raw, p.Alias)
	}
	if rv.UserID != "" {
		user = identity.ActorHash(salt, rv.UserID, p.Alias)
	}
	return actor, actorKind, user, rv.GroupID
}
```

The loop body in `handleEvents`, from `actor, user, group := …` to the end of the switch, becomes:

```go
		actor, actorKind, user, group := resolveIdentity(p, rv, salt, ip, ua)
		names = append(names, identityNames(p, rv)...)

		defaultKind, isView := viewName(ev.Name)
		if !isView {
			if strings.HasPrefix(ev.Name, "$") {
				res.warn(i, "unknown reserved name %s, stored as a custom event", ev.Name)
			}
			s.queue.EnqueueEvent(store.ProductEvent{
				ID: id, Project: p.Alias, EventName: ev.Name,
				TS: ts, ReceivedAt: received,
				ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group,
				OS: enrich.NormalizeOS(rv.OS), AppVersion: rv.AppVersion,
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
		v := store.View{
			ID: id, Project: p.Alias, TS: ts, ReceivedAt: received, Kind: kind,
			ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group, SessionID: rv.SessionID,
			Host: rv.Host, Path: path,
			UTMSource: rv.UTMSource, UTMMedium: rv.UTMMedium, UTMCampaign: rv.UTMCampaign,
			OSVersion: rv.OSVersion, AppVersion: rv.AppVersion,
			DeviceModel: rv.DeviceModel, Locale: rv.Locale, Country: country,
		}
		// Only web rows are enriched from the connection: the User-Agent
		// names the browser, OS and device class, and a crawler is dropped.
		// Every other kind declares its own environment and is never
		// filtered, whatever HTTP library it uses.
		if kind == "web" {
			if botUA {
				// Accepted and silently ignored: the client did nothing
				// wrong, so it must not retry.
				res.Accepted++
				continue
			}
			v.Device, v.Browser, v.BrowserVersion, v.OS = enrich.ParseUserAgent(ua)
			v.ReferrerSource = enrich.CleanReferrer(rv.Referrer, rv.Host)
		} else {
			// No host to compare against, so a referrer is taken at face
			// value — a deep link can still carry one.
			v.ReferrerSource = enrich.CleanReferrer(rv.Referrer, "")
		}
		// Declared environment overrides whatever was parsed.
		if rv.OS != "" {
			v.OS = enrich.NormalizeOS(rv.OS)
		}
		var bad bool
		if v.DisplayWidth, bad = parseDisplay(rv.displayWidthRaw); bad {
			res.warn(i, "$display_width %q is not a positive integer, ignored", rv.displayWidthRaw)
		}
		if v.DisplayHeight, bad = parseDisplay(rv.displayHeightRaw); bad {
			res.warn(i, "$display_height %q is not a positive integer, ignored", rv.displayHeightRaw)
		}
		s.queue.EnqueueView(v)
		res.Accepted++
```

Update the `handleEvents` doc comment ("demultiplexes by event name: views to the views table, everything else to product_events") and the bot-filter comment near `botUA` ("keyed on kind, not name"). In `server.go`:

```go
type Enqueuer interface {
	EnqueueView(v store.View)
	EnqueueEvent(e store.ProductEvent)
}
```

`scripts/smokecheck/main.go`: `fmt.Printf("views=%d product=%d\n", count("views"), count("product_events"))`.

- [ ] **Step 6: Run**

Run: `go test ./internal/server/ ./internal/pipeline/ ./internal/app/` then `go build ./... && go vet ./...`
Expected: server and pipeline PASS; `internal/app` and the build may still fail on `internal/api` symbols — if so, that is Task 9's scope; everything else must be green.

- [ ] **Step 7: Commit**

```bash
git add internal/pipeline internal/server internal/app scripts/smokecheck
git commit -m "feat(server): route every page and screen view into one family keyed by a declared kind"
```

---

### Task 9: API — `views_overview`, `views_breakdown`, `retention(actor)`, schema catalogue

Spec §12. The docs sync tests in this package also read `docs/twillingate.md`; they go green in Task 10, so this task runs the package with `-skip 'TestDocument|TestDeployment'` and Task 10 runs it whole.

**Files:**
- Modify: `internal/api/ops_read.go`, `internal/api/ops_product.go`, `internal/api/resources.go`, `internal/api/guide.go`, `internal/api/seed_test.go`, `internal/api/ops_read_test.go`, `internal/api/rest_test.go`, `internal/api/guide_test.go`, `internal/api/docs_sync_test.go`, `internal/api/ops_product_test.go` (retention/identities cases)

**Interfaces:**
- Produces: tools `views_overview` (`overviewIn{rangeIn; Kind string}`), `views_breakdown` (`breakdownIn` with `viewsDimensions`), `retention` (`retentionIn{rangeIn; Actor string}`), REST `GET /api/projects/{project}/views/overview`, `…/views/breakdown`; `projectOut.FirstViewDay/LastViewDay`; `schemaViews` text.

- [ ] **Step 1: Seed the new schema in `seed_test.go`**

`testRetention` becomes `config.Retention{Views: {RawDays: 30, AggregateDays: 365}, Product: {RawDays: 30, AggregateDays: 365}}`. Replace the web/app seeds:

```go
	seed(`INSERT INTO agg_views_daily (project, day, kind, visitors, views, sessions, bounces, duration_sec)
	      VALUES ('blog','2026-08-20','web',10,25,12,3,600), ('blog','2026-08-21','web',12,30,14,4,720),
	             ('blog','2026-08-20','app',6,20,8,0,480)`)
	seed(`INSERT INTO agg_views_paths (project, day, path, visitors, views)
	      VALUES ('blog','2026-08-20','/post-1',8,15), ('blog','2026-08-20','/post-2',4,10), ('blog','2026-08-20','/settings',5,12)`)
	seed(`INSERT INTO agg_views_hosts (project, day, host, visitors, views)
	      VALUES ('blog','2026-08-20','blog.example.com',9,20), ('blog','2026-08-20','shop.example.com',3,5)`)
	seed(`INSERT INTO agg_views_utm (project, day, utm_source, utm_medium, utm_campaign, visitors, views)
	      VALUES ('blog','2026-08-20','newsletter','email','august',6,9)`)
	seed(`INSERT INTO agg_views_os (project, day, os, os_version, visitors, views)
	      VALUES ('blog','2026-08-20','iOS','17.4',5,12), ('blog','2026-08-20','Windows','',7,13)`)
	seed(`INSERT INTO agg_views_browsers (project, day, browser, browser_version, visitors, views)
	      VALUES ('blog','2026-08-20','Chrome','126',7,13)`)
	seed(`INSERT INTO agg_views_app_versions (project, day, os, app_version, visitors, views)
	      VALUES ('blog','2026-08-20','iOS','2.4.1',5,12)`)
	seed(`INSERT INTO agg_views_devices (project, day, device, device_model, visitors, views)
	      VALUES ('blog','2026-08-20','desktop','',7,13), ('blog','2026-08-20','','iPhone15,3',5,12)`)
	seed(`INSERT INTO agg_views_countries (project, day, country, visitors, views)
	      VALUES ('blog','2026-08-20','US',12,25)`)
	seed(`INSERT INTO agg_views_displays (project, day, display, visitors, views)
	      VALUES ('blog','2026-08-20','1920x1080',6,11)`)
	seed(`INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind, user_id, path)
	      VALUES ('h1','blog','2026-08-26T10:00:00Z','2026-08-26T10:00:00Z','web','a1','user','u1','/live')`)
	seed(`INSERT INTO agg_retention (project, actor_kind, cohort_day, day_offset, actors)
	      VALUES ('blog','user','2026-08-01',0,10), ('blog','user','2026-08-01',7,4)`)
	seed(`INSERT INTO agg_identity_daily (project, day, kind, id, actors, users, views, events)
	      VALUES ('blog','2026-08-20','user','u1',1,1,5,2)`)
	seed(`INSERT INTO agg_identity_daily (project, day, kind, id, actors, users, views, events)
	      VALUES ('blog','2026-08-20','group','g1',1,0,5,2)`)
```

(keep the product, identities and attribute seeds; delete every `agg_web_*`, `agg_app_*`, `web_hits` seed).

- [ ] **Step 2: Failing API tests**

In `ops_read_test.go` rename `TestWebOverviewStitchesAggregatedAndLive` → `TestViewsOverviewStitchesAggregatedAndLive` calling `views_overview`, and add:

```go
func TestViewsOverviewSumsKindsUnlessFiltered(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "views_overview", map[string]any{
		"project": "blog", "from": "2026-08-20", "to": "2026-08-20"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	// web 10 visitors + app 6 = 16; views 25 + 20 = 45
	if out := textOf(res); !strings.Contains(out, `"16"`) || !strings.Contains(out, `"45"`) {
		t.Errorf("unfiltered overview should sum kinds: %s", out)
	}
	res = callTool(t, cs, "views_overview", map[string]any{
		"project": "blog", "from": "2026-08-20", "to": "2026-08-20", "kind": "app"})
	if out := textOf(res); !strings.Contains(out, `"6"`) || strings.Contains(out, `"16"`) {
		t.Errorf("kind filter not applied: %s", out)
	}
}

func TestViewsBreakdownEveryDimension(t *testing.T) {
	_, cs := newTestHost(t)
	want := map[string]string{
		"kinds": "web", "paths": "/post-1", "hosts": "blog.example.com", "utm": "newsletter",
		"countries": "US", "os": "17.4", "browsers": "126", "app_versions": "2.4.1",
		"devices": "iPhone15,3", "displays": "1920x1080",
	}
	for dim, needle := range want {
		res := callTool(t, cs, "views_breakdown", map[string]any{
			"project": "blog", "from": "2026-08-01", "to": "2026-08-31", "dimension": dim})
		if res.IsError {
			t.Errorf("%s: %s", dim, textOf(res))
			continue
		}
		if out := textOf(res); !strings.Contains(out, needle) {
			t.Errorf("%s: missing %q in %s", dim, needle, out)
		}
	}
	res := callTool(t, cs, "views_breakdown", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31", "dimension": "referrers"})
	if res.IsError {
		t.Errorf("referrers with no rows must not error: %s", textOf(res))
	}
	res = callTool(t, cs, "views_breakdown", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31", "dimension": "sandwiches"})
	if !res.IsError || !strings.Contains(textOf(res), "app_versions") {
		t.Errorf("invalid dimension should list the valid ones: %v %s", res.IsError, textOf(res))
	}
}
```

Keep the existing `limit 1` assertion in the breakdown test (`/post-1` present, `/post-2` absent) under `views_breakdown`. Delete the app tool tests. In `ops_product_test.go` (or wherever `retention` is tested) change `"surface": "web"` to `"actor": "user"` and assert `"actor": "app"` is rejected with a message containing `user or install`; in the identities test assert the columns `views` and `events` and no `hits`. In `rest_test.go` the parity table rows become:

```go
		{"views_overview", "/api/projects/blog/views/overview?" + rng, args},
		{"views_breakdown", "/api/projects/blog/views/breakdown?dimension=paths&" + rng, with(map[string]any{"dimension": "paths"})},
		{"retention", "/api/projects/blog/retention?actor=user&" + rng, with(map[string]any{"actor": "user"})},
```

and every `/web/overview` path in that file becomes `/views/overview`. In `guide_test.go` keep the `$screen_view` and `$install_id` assertions for the mobile guide and add `"data-kind"` to the strings expected in the web guide.

- [ ] **Step 3: Implement `ops_read.go`**

`projectOut` replaces the four day fields with `FirstViewDay string \`json:"first_view_day,omitempty"\`` and `LastViewDay`; `listProjects` probes one view:

```go
		_, rows, _, err := queryRows(ctx, h.db, h.timeout, 1,
			`SELECT COALESCE(MIN(day),''), COALESCE(MAX(day),'') FROM v_views_daily WHERE project=?`, p.Alias)
		if err != nil {
			return out, err
		}
		if len(rows) == 1 {
			po.FirstViewDay, po.LastViewDay = rows[0][0], rows[0][1]
		}
```

Replace `webOverview`, `webDimensions`, `breakdownIn`, `webBreakdown`, `appDimensions`, `appOverview`, `appBreakdown` with:

```go
// ---- views_overview ----

type overviewIn struct {
	rangeIn
	Kind string `json:"kind,omitempty" jsonschema:"optional: only this kind (web, app, cli, …); absent sums every kind per day"`
}

func (h *host) viewsOverview(ctx context.Context, in overviewIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	q := `SELECT day, SUM(visitors) AS visitors, SUM(views) AS views, SUM(sessions) AS sessions,
		SUM(bounces) AS bounces, SUM(duration_sec) AS duration_sec,
		ROUND(CAST(SUM(bounces) AS REAL)/MAX(SUM(sessions),1), 3) AS bounce_rate,
		CAST(SUM(duration_sec)/MAX(SUM(sessions),1) AS INTEGER) AS avg_session_sec
		FROM v_views_daily WHERE project=? AND day BETWEEN ? AND ?`
	args := []any{in.Project, in.From, in.To}
	if in.Kind != "" {
		q += ` AND kind=?`
		args = append(args, in.Kind)
	}
	out, err := h.table(ctx, q+` GROUP BY day ORDER BY day`, args...)
	return out, err
}

// ---- views_breakdown ----

// viewsDimension is one breakdown: the view it reads and the key columns
// it groups by (one or two). kinds has no view of its own; it reads the
// daily table.
type viewsDimension struct {
	view string
	cols []string
}

var viewsDimensions = map[string]viewsDimension{
	"kinds":        {"v_views_daily", []string{"kind"}},
	"paths":        {"v_views_paths", []string{"path"}},
	"hosts":        {"v_views_hosts", []string{"host"}},
	"referrers":    {"v_views_referrers", []string{"source"}},
	"utm":          {"v_views_utm", []string{"utm_source", "utm_medium", "utm_campaign"}},
	"countries":    {"v_views_countries", []string{"country"}},
	"os":           {"v_views_os", []string{"os", "os_version"}},
	"browsers":     {"v_views_browsers", []string{"browser", "browser_version"}},
	"app_versions": {"v_views_app_versions", []string{"os", "app_version"}},
	"devices":      {"v_views_devices", []string{"device", "device_model"}},
	"displays":     {"v_views_displays", []string{"display"}},
}

// dimensionNames lists the enum, sorted, for the schema text and errors.
func dimensionNames() string {
	keys := make([]string, 0, len(viewsDimensions))
	for k := range viewsDimensions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

type breakdownIn struct {
	rangeIn
	Dimension string `json:"dimension" jsonschema:"one of: app_versions, browsers, countries, devices, displays, hosts, kinds, os, paths, referrers, utm"`
	Limit     int    `json:"limit,omitempty" jsonschema:"top-N rows, default 20"`
}

func (h *host) viewsBreakdown(ctx context.Context, in breakdownIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	dim, ok := viewsDimensions[in.Dimension]
	if !ok {
		return tableOut{}, invalidf("unknown dimension %q; valid: %s", in.Dimension, dimensionNames())
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	cols := strings.Join(dim.cols, ", ")
	out, err := h.table(ctx, `SELECT `+cols+`,
		SUM(visitors) AS visitors, SUM(views) AS views
		FROM `+dim.view+` WHERE project=? AND day BETWEEN ? AND ?
		GROUP BY `+cols+` ORDER BY visitors DESC LIMIT ?`,
		in.Project, in.From, in.To, limit)
	return out, err
}
```

Add a test in `ops_read_test.go` (import `reflect`) that pins the hand-written enum to the map so they cannot drift:

```go
func TestBreakdownEnumMatchesDimensions(t *testing.T) {
	f, _ := reflect.TypeOf(breakdownIn{}).FieldByName("Dimension")
	if want := "one of: " + dimensionNames(); f.Tag.Get("jsonschema") != want {
		t.Errorf("breakdownIn.Dimension jsonschema = %q, want %q", f.Tag.Get("jsonschema"), want)
	}
}
```

In `register`, the four web/app `expose` calls become:

```go
	expose(r, spec{Name: "views_overview", Annotations: ro, Method: "GET", Path: p + "/views/overview",
		Description: "Daily views for one project: visitors, views, sessions, bounces, duration, with derived bounce_rate and avg_session_sec. Sums every kind (web, app, cli, …) unless kind is given. Includes yesterday and today (live)."},
		h.viewsOverview)
	expose(r, spec{Name: "views_breakdown", Annotations: ro, Method: "GET", Path: p + "/views/breakdown",
		Description: "Top values for one dimension of a project's views over a date range: kinds, paths, hosts, referrers, utm, countries, os, browsers, app_versions, devices or displays. Two-key dimensions (os, browsers, app_versions, devices) return both columns."},
		h.viewsBreakdown)
```

`product_attributes` description: "The system dimensions $os and $app_version are always included; …". `retention` description: add "Cohorted by how the actor was identified: actor=user or actor=install."

- [ ] **Step 4: `ops_product.go` retention and identities**

```go
type retentionIn struct {
	rangeIn
	Actor string `json:"actor" jsonschema:"user or install: cohorts are kept apart by how the actor was identified"`
}
```

In `retention`: `if in.Actor != "user" && in.Actor != "install" { return retentionOut{}, invalidf("actor must be user or install, got %q", in.Actor) }` and the query `… FROM v_retention WHERE project=? AND actor_kind=? AND cohort_day BETWEEN ? AND ? …` with `in.Actor`. In `identities` the select becomes `SUM(d.actors) AS actors, SUM(d.users) AS users, SUM(d.views) AS views, SUM(d.events) AS events` and `ORDER BY views+events DESC`.

- [ ] **Step 5: `resources.go`**

Replace the view list in `schemaViews` with:

```
  v_views_daily(project, day, kind, visitors, views, sessions, bounces, duration_sec)  -- kind: 'web'|'app'|'cli'|…
  v_views_paths(project, day, path, visitors, views)
  v_views_hosts(project, day, host, visitors, views)
  v_views_referrers(project, day, source, visitors, views)
  v_views_utm(project, day, utm_source, utm_medium, utm_campaign, visitors, views)
  v_views_countries(project, day, country, visitors, views)
  v_views_os(project, day, os, os_version, visitors, views)
  v_views_browsers(project, day, browser, browser_version, visitors, views)
  v_views_app_versions(project, day, os, app_version, visitors, views)
  v_views_devices(project, day, device, device_model, visitors, views)
  v_views_displays(project, day, display, visitors, views)  -- display: 'WxH', e.g. '1920x1080'
  v_product_daily(project, day, event_name, count, unique_users)
  v_product_totals(project, day, total_events, active_users)
  v_identity_daily(project, day, kind, id, actors, users, views, events)  -- kind: 'user'|'group'
  v_retention(project, actor_kind, cohort_day, day_offset, actors, cohort_size)  -- actor_kind: 'user'|'install'
  v_product_attrs(project, day, event_name, attr_key, attr_value, count, unique_users)
  identities(project, kind, id, name)  -- display names, joinable to v_identity_daily
```

Add a fourth caveat: "4. Every dimension is capped at 500 values per day; the rest sit in one '(other)' row per day whose visitors are distinct actors, not a sum."

- [ ] **Step 6: `guide.go`**

Mobile branch: `"$os":"ios"` instead of `"$platform":"ios"`; add `"$kind":"app"` is not needed (the default). Web/spa branch: after the pageviews sentence add `b.WriteString("An Electron or Tauri app loading the same tag adds data-kind=\"app\" and data-os / data-app-version so its views are declared rather than parsed from the User-Agent.\n\n")`.

- [ ] **Step 7: `docs_sync_test.go` adjustments**

- `TestDocumentMatchesReservedKeys`: add `var aliasKeys = map[string]bool{"$platform": true}` and skip alias keys in the "exists in ingest.go but missing from the doc" direction; the event-name loop becomes `for _, name := range []string{"$page_view", "$screen_view"}`.
- `TestDocumentMatchesSDK` symbol list: replace `"$pageview"` with `"$page_view"`, add `"data-kind", "data-os", "data-app-version", "$kind", "$os", "$display_width", "$display_height"`.
- `TestDocumentCoversEveryWebDimension` → `TestDocumentCoversEveryViewsDimension` iterating `viewsDimensions` and checking `d.view`.

- [ ] **Step 8: Run**

Run: `go test ./internal/api/ -skip 'TestDocument|TestDeployment'` and `go build ./... && go vet ./...`
Expected: PASS and a clean build of the whole module.

- [ ] **Step 9: Commit**

```bash
git add internal/api
git commit -m "feat(api): views_overview and views_breakdown replace the web and app tool pairs"
```

---

### Task 10: The contract page and the plausible note

Spec §15. `docs/twillingate.md` is the normative document served over MCP; `docs_sync_test.go` binds its tables to the code. This task makes the whole `internal/api` package green.

**Files:**
- Modify: `docs/twillingate.md`, `docs/plausible/README.md`

- [ ] **Step 1: "What twillingate is" (line ~21)**

Replace "It collects three kinds of analytics through a single endpoint — web pageviews, native app screen views, and custom product events —" with "It collects two kinds of analytics through a single endpoint — views (page views from websites, screen views from apps and CLIs) and custom product events —".

- [ ] **Step 2: Attribute breakdowns (line ~158)**

Replace the paragraph starting "`$platform` and `$app_version` roll up automatically" with:

```markdown
`$os` and `$app_version` roll up automatically without being declared. Do
not add them to `attributes`: `$`-prefixed keys are reserved and never reach
the custom attribute blob, so `"attributes": ["$os"]` extracts nothing.
```

- [ ] **Step 3: Snippet mode table (line ~196)**

Add three rows to the `| Attribute | init() option | Meaning |` table:

```markdown
| `data-kind` | `kind` | What this client is: `web` (default), `app`, `cli`, or any short lower-case token. Anything but `web` switches automatic tracking from `$page_view` to `$screen_view` (the route path becomes the screen) and tells the server to trust the declared environment instead of parsing the User-Agent. |
| `data-os` | `os` | The operating system, for a client that knows better than its User-Agent (`macos` under Electron). Normalised server-side to `iOS`, `Android`, `macOS`, `Windows`, `Linux`, `ChromeOS`. |
| `data-app-version` | `appVersion` | The version of the client application — a site build, an app release, a CLI version. |
```

Change the sentence after the table to: "**Every `data-*` attribute has an `init()` equivalent**, enforced by a test. The reverse does not hold: `url`, `installId` and `flushInterval` are code-only, because an attribute can only carry a string." Change "Pageviews are automatic, including on `history.pushState` and `popstate`" to "Views are automatic, including on `history.pushState` and `popstate`".

- [ ] **Step 4: SDK-only mode and runtime API (lines ~236–290)**

In the `init` example replace the three "app analytics context" lines with:

```js
  // client context, sent as batch attributes:
  kind: "web",                 // → $kind ("app" for Electron/Tauri, "cli", …)
  os: "macos",                 // → $os
  appVersion: "2.4.1",         // → $app_version
  installId: "018f…",          // → $install_id (stable per install)
```

In the runtime API block change the comments `// $pageview for the current page` → `// $page_view for the current page`, `// $pageview for an explicit path` → `// $page_view for an explicit path`, `// app $screen_view` → `// $screen_view (any kind)`. In the bullet list replace the `screen(name, attrs?)` bullet with "`screen(name, attrs?)` — an explicit `$screen_view`. With `kind` set to anything but `web` the automatic tracker already sends one per navigation, so this is for screens that are not routes."

- [ ] **Step 5: The event model (lines ~469–522)**

Replace from "Everything goes to one endpoint" through the end of `### App ($screen_view)` with:

```markdown
Everything goes to one endpoint, `POST /ingest/events`. The event **name**
decides which family it lands in:

| name | family | default `$kind` | feeds |
| --- | --- | --- | --- |
| `$page_view` | views | `web` | the views dashboard, `views_overview`, `views_breakdown`, retention |
| `$screen_view` | views | `app` | same |
| anything else | product | — | `product_events`, `product_attributes` |

The `$` prefix is reserved for the system. An unrecognized `$` **name** is
stored as an ordinary custom event with a warning (forward compatibility);
an unrecognized `$` **attribute key** is dropped, with a warning in the
response body.

### Views (`$page_view`, `$screen_view`)

A view is one page or screen shown to someone. The two names are the same
row; the name only sets the default **kind**. `$kind` is a short lower-case
token the client declares for itself — `web`, `app`, `cli`, or anything
matching `^[a-z][a-z0-9_]{0,15}$` — usually as a batch attribute. An
invalid value is warned about and replaced by the name's default, so a
typo can never mint a dimension.

**Only `web` has server-side meaning.** A web view is enriched from the
connection that carried it: client IP for country, User-Agent for browser,
browser version, OS and device class, `Origin` for the allowlist, and bot
filtering on the User-Agent. Every other kind is taken as declared — never
parsed, never filtered — so an app or CLI whose HTTP library sends a
non-browser User-Agent is never dropped as a crawler, and an Electron app
is never misclassified as desktop Chrome.

Declared environment keys are stored on any kind and override the parsed
value where both exist: `$os` (normalised to `iOS`, `Android`, `macOS`,
`Windows`, `Linux`, `ChromeOS`; anything else stored as sent),
`$os_version`, `$app_version`, `$device_model`, `$locale`,
`$display_width` and `$display_height` (integer pixels of the physical
display). `$app_version` is the version of whatever client sent the event —
a site build, an app release, a CLI version — and is not tied to
`kind: app`.

A view carries its location already split — `$path` (or `$screen`, an
alias) **required**, and `$host` — stored verbatim. Campaign parameters
travel explicitly as `$utm_source`, `$utm_medium` and `$utm_campaign`.
`$referrer` is reduced to a source name; on a web view it is suppressed as a
self-referral when its host matches `$host`, on any other kind it is taken
at face value (a deep link can carry one).

`$path` may contain a `#` (hash routing) or a `?` (opt-in query routing).

Sessions: a client `$session_id` is authoritative (an app knows its own
foreground/background transitions); without one, a gap over 30 minutes per
actor starts a new session. A bounce is a session with one view. Expect a
high bounce rate on app kinds, where a single-screen session is normal use.

Country comes from the connection on every kind; the client IP and
User-Agent are never stored. **A backend must not relay web views on behalf
of other people** — every one would be attributed to the backend's IP and
User-Agent. The server cannot detect this; it is a contract you keep.
```

Keep `### Product (everything else)` and `### Identity` as they are. In the identity section change "`$user_id`, `$install_id`" wording nothing; add after the actor-precedence paragraph:

```markdown
How the actor was identified is recorded alongside it (`user`, `install` or
`connection`) and is what retention cohorts on: a connection hash rotates
with the salt and can never appear in a later cohort, so only user- and
install-identified actors are tracked.
```

- [ ] **Step 6: Wire format envelope (line ~584)**

In the envelope example replace `"$platform": "ios"` with `"$kind": "app", "$os": "ios"`, add `"$display_width": 1179, "$display_height": 2556` after `$locale`, and change the second event's name `"$pageview"` to `"$page_view"`.

- [ ] **Step 7: Reserved event names (line ~634)**

```markdown
| `name` | Stored as | Default `$kind` | Requires |
| --- | --- | --- | --- |
| `$page_view` | view | `web` | `$path` (or `$screen`) |
| `$screen_view` | view | `app` | `$screen` (or `$path`) |
| anything else | custom event | — | `name` |
```

- [ ] **Step 8: Reserved attribute keys (line ~650)**

```markdown
| Group | Keys |
| --- | --- |
| Identity | `$install_id` `$user_id` `$user_name` `$group_id` `$group_name` `$session_id` |
| Environment | `$kind` `$os` `$os_version` `$app_version` `$device_model` `$locale` `$display_width` `$display_height` |
| Location | `$host` `$path` `$screen` `$utm_source` `$utm_medium` `$utm_campaign` `$referrer` |
```

- [ ] **Step 9: Timestamps (line ~683) and responses (line ~697)**

Replace "`max_event_age` equals the deployment's `RETENTION_APP_RAW_DAYS`" with "`max_event_age` equals the deployment's `RETENTION_VIEWS_RAW_DAYS`". In the 202 example change `"$pageview requires $path"` to `"view requires $path or $screen"`.

- [ ] **Step 10: Tools (line ~780), routes (line ~828), views prose (line ~879)**

"A connected session gets nineteen tools" → "seventeen tools". Replace the four web/app rows of the reading table with:

```markdown
| `views_overview` | `kind` (optional) | Visitors, views, sessions, bounces, average session length per day, summed across kinds unless `kind` filters one |
| `views_breakdown` | `dimension`, `limit` (default 20) | Top rows for one of `kinds`, `paths`, `hosts`, `referrers`, `utm`, `countries`, `os`, `browsers`, `app_versions`, `devices`, `displays`. Two-key dimensions return both columns |
```

`product_attributes` row: "`$os` and `$app_version` are always available". `retention` row: "`actor` (`user` or `install`)". `identities` row unchanged. Route table: replace the four web/app rows with

```markdown
| `GET` | `/api/projects/{project}/views/overview` | `views_overview` | query: `from`, `to`, `kind` |
| `GET` | `/api/projects/{project}/views/breakdown` | `views_breakdown` | query: `from`, `to`, `dimension`, `limit` |
```

and the retention row's input becomes `query: from, to, actor`. The curl example path becomes `/views/overview`. Replace the paragraph "The web views are `v_web_daily`…" with:

```markdown
The views family is `v_views_daily` (per kind), `v_views_paths`,
`v_views_hosts`, `v_views_referrers`, `v_views_utm`, `v_views_countries`,
`v_views_os`, `v_views_browsers`, `v_views_app_versions`, `v_views_devices`
and `v_views_displays`. Every dimension is capped at 500 values per day;
the tail is one `(other)` row whose visitors are distinct actors, not a sum.
Product events have `v_product_daily`, `v_product_totals` and
`v_product_attrs`, plus a per-project `v_events_flat` with one column per
declared attribute. `v_identity_daily` and `identities` join user and group
activity to display names; `v_retention` is keyed by `actor_kind`.
```

- [ ] **Step 11: "Changed in this release" note**

Directly under the `## The event model` heading's table (before "The `$` prefix…") add:

```markdown
> **Changed 2026-09 — one views family.** Web and app analytics merged.
> `$pageview` is now `$page_view` (the old spelling is still accepted);
> `$platform` is now `$os` (likewise). Tools `web_*`/`app_*` became
> `views_*`, routes `/web/*` and `/app/*` became `/views/*`, views
> `v_web_*`/`v_app_*` became `v_views_*`, `retention` takes `actor`
> instead of `surface`, and `RETENTION_WEB_*`/`RETENTION_APP_*` became
> `RETENTION_VIEWS_*`. Paths are now capped at 500 per day like every
> other dimension. Rows before the merge carry an empty device class on
> app days, an empty device model and browser version on web days, and
> zero bounces on app days.
```

- [ ] **Step 12: `docs/plausible/README.md`**

Line ~70: "Any name that is not `$page_view` or `$screen_view` is stored as a custom event".

- [ ] **Step 13: Run the sync tests**

Run: `go test ./internal/api/`
Expected: PASS, including `TestDocumentMatchesReservedKeys`, `TestDocumentMatchesSDK` (fails until Task 11 adds the SDK symbols — if so, re-run after Task 11), `TestDocumentNamesEveryTool`, `TestDocumentMatchesRoutes`, `TestDeploymentDocumentsEveryEnvVar`, `TestDocumentCoversEveryViewsDimension`.

- [ ] **Step 14: Commit**

```bash
git add docs/twillingate.md docs/plausible/README.md
git commit -m "docs: describe the views family, its kinds and the renamed keys, tools and routes"
```

---

### Task 11: SDK — kind, os, display size, locale

Spec §14. The bundle `internal/server/twillingate.js` is committed and CI rebuilds it to check for drift, so rebuild and commit it here.

**Files:**
- Modify: `sdk/src/twillingate.ts`, `sdk/src/twillingate.test.ts`, `sdk/src/api.test.ts`, `sdk/src/identity.test.ts`, `internal/server/twillingate.js` (generated)

**Interfaces:**
- Produces: `InitOptions.kind?: string`, `InitOptions.os?: string` (with `platform` kept as a deprecated alias), tag attributes `data-kind`, `data-os`, `data-app-version`; batch attributes `$kind` (always), `$os`, `$app_version`, `$locale`; per-view attributes `$display_width`, `$display_height`; event names `$page_view` and `$screen_view`.

- [ ] **Step 1: Failing SDK tests**

In `sdk/src/twillingate.test.ts`, under `describe("snippet auto-init")`, add:

```ts
  it("sends $kind web by default and $page_view on load", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet" }));
    t.flush();
    await drain();
    expect(sent[0].body.attributes.$kind).toBe("web");
    expect(sent[0].body.events[0].name).toBe("$page_view");
  });

  it("data-kind switches automatic tracking to $screen_view with the route path", async () => {
    history.replaceState(null, "", "/settings/profile?tab=1");
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet", "data-kind": "app", "data-os": "macos", "data-app-version": "2.4.1" }));
    t.flush();
    await drain();
    const attrs = sent[0].body.attributes;
    expect(attrs.$kind).toBe("app");
    expect(attrs.$os).toBe("macos");
    expect(attrs.$app_version).toBe("2.4.1");
    const ev = sent[0].body.events[0];
    expect(ev.name).toBe("$screen_view");
    const ea = ev.attributes as Record<string, unknown>;
    expect(ea.$screen).toBe("/settings/profile");
    expect(ea.$host).toBeUndefined();
    expect(ea.$referrer).toBeUndefined();
  });

  it("app kind tracks pushState navigations as screen views", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet", "data-kind": "app" }));
    history.pushState(null, "", "/two");
    t.flush();
    await drain();
    const names = sent.flatMap((s) => s.body.events.map((e) => e.name));
    expect(names).toEqual(["$screen_view", "$screen_view"]);
  });
```

Under `describe("payload shape")` add:

```ts
  it("stamps display size on views and locale on every batch", async () => {
    Object.defineProperty(window, "screen", { value: { width: 1920, height: 1080 }, configurable: true });
    Object.defineProperty(navigator, "language", { value: "de-DE", configurable: true });
    const t = tg();
    t.page("/x");
    t.track("probe");
    t.flush();
    await drain();
    const [view, probe] = sent[0].body.events;
    const va = view.attributes as Record<string, unknown>;
    expect(va.$display_width).toBe(1920);
    expect(va.$display_height).toBe(1080);
    expect((probe.attributes as Record<string, unknown>).$display_width).toBeUndefined();
    expect(sent[0].body.attributes.$locale).toBe("de-DE");
  });
```

In the parity test's `optionFor` map add `kind: "kind", os: "os", "app-version": "appVersion"`. In `api.test.ts` and `identity.test.ts` every `"$pageview"` expectation becomes `"$page_view"`, and the `$platform` assertion (if any) becomes `$os`. Keep one test that `init({ platform: "ios" })` still yields `$os: "ios"` (deprecated alias).

- [ ] **Step 2: Run to verify failure**

Run: `cd sdk && npm test`
Expected: the new tests fail (`$kind` undefined, name `$pageview`).

- [ ] **Step 3: Implement**

`InitOptions`: replace the `platform`/`appVersion`/`installId` block with

```ts
  /**
   * What this client is: "web" (default), "app", "cli", or any short
   * lower-case token. Anything but "web" makes automatic tracking emit
   * $screen_view with the route path as the screen, and tells the server
   * to trust the declared environment instead of the User-Agent.
   */
  kind?: string;
  /** Declared OS ($os), for a client that knows better than its User-Agent. */
  os?: string;
  /** @deprecated use os */
  platform?: string;
  /** Version of this client application ($app_version). */
  appVersion?: string;
  installId?: string;
```

Fields: rename `private platform` to `private os: string | null = null;` and add `private kind = "web";`. In `init`:

```ts
    this.kind = opts.kind && /^[a-z][a-z0-9_]{0,15}$/.test(opts.kind) ? opts.kind : "web";
    this.os = opts.os || opts.platform || null;
    this.appVersion = opts.appVersion || null;
```

`batchAttributes`:

```ts
    a.$kind = this.kind;
    if (this.os) a.$os = this.os;
    if (this.appVersion) a.$app_version = this.appVersion;
    if (typeof navigator !== "undefined" && navigator.language) a.$locale = navigator.language;
```

`page()` at the end: the emitted event depends on kind. Replace `this.emit("$pageview", attributes);` with:

```ts
    this.firstPageviewSent = true;
    if (this.kind === "web") {
      this.emit("$page_view", { ...attributes, ...displaySize() });
      return;
    }
    // A non-web kind is an app: the route is the screen, and the page
    // context (host, referrer, campaign) does not apply.
    const { $host: _h, $referrer: _r, $utm_source: _s, $utm_medium: _m, $utm_campaign: _c, $path, ...rest } = attributes;
    this.emit("$screen_view", { $screen: $path, ...rest, ...displaySize() });
```

(Prefix the unused destructured names with `_` so `tsc --noEmit` stays clean under `noUnusedLocals`; if the config rejects them, delete the keys with `delete` instead.) `screen()` emits `$screen_view` with `{ $screen: String(name), ...displaySize(), ...attrs }`. Add near `splitLocation`:

```ts
/** Physical display size in pixels, when the runtime exposes one. */
function displaySize(): Record<string, number> {
  if (typeof screen === "undefined" || !screen.width || !screen.height) return {};
  return { $display_width: screen.width, $display_height: screen.height };
}
```

`autoInit` gains:

```ts
    kind: script.getAttribute("data-kind") || undefined,
    os: script.getAttribute("data-os") || undefined,
    appVersion: script.getAttribute("data-app-version") || undefined,
```

Update the file header comment ("web, product and app analytics") and the `build.mjs` banner to "views and product analytics SDK". Rename `firstPageviewSent` only if you touch it anyway; not required.

- [ ] **Step 4: Run, typecheck, build**

Run: `cd sdk && npm test && npm run typecheck && npm run build`
Expected: all green; `git status` shows `internal/server/twillingate.js` modified. Then `go test ./internal/server/ -run Script` (the served-script tests) and `go test ./internal/api/ -run TestDocumentMatchesSDK`.

- [ ] **Step 5: Commit**

```bash
git add sdk internal/server/twillingate.js
git commit -m "feat(sdk): declare the client kind, os and display size; emit \$page_view"
```

---

### Task 12: Evidence — one views page

Spec §13. Evidence pages are markdown with DuckDB SQL; sources are passthrough `.sql` files with the empty-database sentinel. `internal/dashboards/prerender_test.go` derives routes from the page tree, so no config edit is needed.

**Files:**
- Create: `evidence/pages/views/[project].md`, `evidence/pages/views/[project]/page.md`, `evidence/sources/twillingate/v_views_daily.sql`, `v_views_paths.sql`, `v_views_hosts.sql`, `v_views_referrers.sql`, `v_views_utm.sql`, `v_views_countries.sql`, `v_views_os.sql`, `v_views_browsers.sql`, `v_views_app_versions.sql`, `v_views_devices.sql`, `v_views_displays.sql`
- Modify: `evidence/pages/index.md`, `evidence/pages/retention/[project].md`, `evidence/pages/users/[project].md`, `evidence/pages/groups/[project].md`, `evidence/sources/twillingate/v_retention.sql`, `evidence/sources/twillingate/v_identity_daily.sql`, `evidence/sources/twillingate/projects.sql` (comment only)
- Delete: `evidence/pages/web/`, `evidence/pages/app/`, `evidence/sources/twillingate/v_web_*.sql` (9), `v_app_*.sql` (6)

- [ ] **Step 1: Sources**

Every source has the same shape. `v_views_daily.sql`:

```sql
-- Empty-database guard: the sqlite connector infers column types from the
-- first row, so a zero-row result throws "Cannot convert undefined or null to
-- object" and fails the whole source build -- taking every other query on the
-- page down with it. A fresh install has no traffic yet, so emit a sentinel
-- row when the view is empty; pages filter it out via their project clause.
select project, day, kind, visitors, views, sessions, bounces, duration_sec
from v_views_daily
union all
select '', '1970-01-01', '', 0, 0, 0, 0, 0
where not exists (select 1 from v_views_daily)
```

The ten dimension sources follow the same pattern with these column lists and sentinels (keep the comment block in each):

| File | select | sentinel |
|---|---|---|
| `v_views_paths.sql` | `project, day, path, visitors, views` | `'', '1970-01-01', '', 0, 0` |
| `v_views_hosts.sql` | `project, day, host, visitors, views` | same |
| `v_views_referrers.sql` | `project, day, source, visitors, views` | same |
| `v_views_utm.sql` | `project, day, utm_source, utm_medium, utm_campaign, visitors, views` | `'', '1970-01-01', '', '', '', 0, 0` |
| `v_views_countries.sql` | `project, day, country, visitors, views` | `'', '1970-01-01', '', 0, 0` |
| `v_views_os.sql` | `project, day, os, os_version, visitors, views` | `'', '1970-01-01', '', '', 0, 0` |
| `v_views_browsers.sql` | `project, day, browser, browser_version, visitors, views` | same |
| `v_views_app_versions.sql` | `project, day, os, app_version, visitors, views` | same |
| `v_views_devices.sql` | `project, day, device, device_model, visitors, views` | same |
| `v_views_displays.sql` | `project, day, display, visitors, views` | `'', '1970-01-01', '', 0, 0` |

`v_retention.sql`: `select project, actor_kind, cohort_day, day_offset, actors, cohort_size from v_retention union all select '', '', '1970-01-01', 0, 0, 0 where not exists (…)`. `v_identity_daily.sql`: columns `project, day, kind, id, actors, users, views, events` with sentinel `'', '1970-01-01', '', '', 0, 0, 0, 0`. `projects.sql` comment: "see the note in v_views_daily.sql". Delete the fifteen old source files.

- [ ] **Step 2: `pages/views/[project].md`**

Every query uses the same date-window clause as the old pages. Written out:

```markdown
# {params.project} — Views

<Dropdown name=range title="Date range" defaultValue="30">
    <DropdownOption value="1" valueLabel="Last 1 day" />
    <DropdownOption value="7" valueLabel="Last 7 days" />
    <DropdownOption value="30" valueLabel="Last 30 days" />
    <DropdownOption value="90" valueLabel="Last 90 days" />
    <DropdownOption value="180" valueLabel="Last 180 days" />
</Dropdown>

```sql daily
select day, sum(visitors) as visitors, sum(views) as views, sum(sessions) as sessions,
       case when sum(sessions) > 0 then sum(bounces) * 1.0 / sum(sessions) else 0 end as bounce_rate,
       case when sum(sessions) > 0 then sum(duration_sec) * 1.0 / sum(sessions) else 0 end as avg_session_sec
from twillingate.v_views_daily
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by day order by day
```

```sql totals
select sum(visitors) as visitors, sum(views) as views, sum(sessions) as sessions,
       case when sum(sessions) > 0 then sum(bounces) * 1.0 / sum(sessions) else 0 end as bounce_rate,
       case when sum(sessions) > 0 then sum(duration_sec) * 1.0 / sum(sessions) else 0 end as avg_session_sec
from twillingate.v_views_daily
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

```sql kinds
select day, kind, visitors
from twillingate.v_views_daily
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day, kind
```

<Grid cols=4>
    <BigValue data={totals} value=visitors fmt=num0 title="Visitors" />
    <BigValue data={totals} value=views fmt=num0 title="Views" />
    <BigValue data={totals} value=bounce_rate fmt=pct1 title="Bounce rate" />
    <BigValue data={totals} value=avg_session_sec fmt=num0 title="Avg session (sec)" />
</Grid>

<LineChart data={daily} x=day y={["visitors","views"]} title="Visitors & views" yFmt=num0 />

<Grid cols=2>
    <AreaChart data={kinds} x=day y=visitors series=kind title="Visitors by kind" yFmt=num0 />
    <LineChart data={daily} x=day y=avg_session_sec yFmt=num0 title="Avg session length (sec)" />
</Grid>
```

Then the paths / referrers / hosts block from the old web page with `v_web_pages` → `v_views_paths`, `pageviews` → `views`, the detail link `'/views/${params.project}/page?path=' || path`, `v_web_referrers` → `v_views_referrers`, `v_web_hosts` → `v_views_hosts`; the campaigns block with `v_web_utm` → `v_views_utm`. Audience block:

```markdown
```sql countries
select country, sum(visitors) as visitors from twillingate.v_views_countries
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and country != ''
group by country order by visitors desc limit 20
```

```sql oses
select case when os_version != '' then os || ' ' || os_version else os end as os, sum(visitors) as visitors
from twillingate.v_views_os
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and os != ''
group by 1 order by visitors desc limit 15
```

```sql browsers
select case when browser_version != '' then browser || ' ' || browser_version else browser end as browser, sum(visitors) as visitors
from twillingate.v_views_browsers
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and browser != ''
group by 1 order by visitors desc limit 15
```

```sql devices
select case when device_model != '' then device_model else device end as device, sum(visitors) as visitors
from twillingate.v_views_devices
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and (device != '' or device_model != '')
group by 1 order by visitors desc limit 15
```

```sql displays
select display, sum(visitors) as visitors from twillingate.v_views_displays
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and display != ''
group by display order by visitors desc limit 15
```

## Audience

<Grid cols=2>
    <BarChart data={countries} x=country y=visitors swapXY=true title="Countries" yFmt=num0 />
    <BarChart data={devices} x=device y=visitors swapXY=true title="Devices" yFmt=num0 />
</Grid>

<Grid cols=2>
    <BarChart data={browsers} x=browser y=visitors swapXY=true title="Browsers" yFmt=num0 />
    <BarChart data={oses} x=os y=visitors swapXY=true title="Operating systems" yFmt=num0 />
</Grid>

<BarChart data={displays} x=display y=visitors swapXY=true title="Display resolutions" yFmt=num0 />

```sql app_versions
select day, os || ' ' || app_version as version, visitors
from twillingate.v_views_app_versions
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and app_version != ''
order by day, version
```

{#if app_versions.length > 0}

## Version adoption

<AreaChart data={app_versions} x=day y=visitors series=version title="Visitors by app version" yFmt=num0 />

{/if}
```

`pages/views/[project]/page.md` is the old `pages/web/[project]/page.md` with `v_web_pages` → `v_views_paths`, `pageviews` → `views`, the back link `/views/{params.project}`, and titles "Views" instead of "Pageviews".

- [ ] **Step 3: Retention, users, groups, index**

`retention/[project].md`: `surface` → `actor_kind` everywhere (queries, `series=`, column ids), the column title "Surface" → "Identified by", and the intro paragraph becomes "Cohorts are kept apart by how the actor was identified: a `$user_id` and an `$install_id` describe different populations, and blending their curves would describe neither." `users/[project].md` and `groups/[project].md`: `hits + views + events` → `views + events` (all occurrences). `index.md`: replace `[web](/web/{p.alias}) · [app](/app/{p.alias})` with `[views](/views/{p.alias})` in both lists.

- [ ] **Step 4: Verify**

Run: `go test ./internal/dashboards/` (prerender route derivation) and, if Node and the Evidence deps are installed, `cd evidence && npm install && EVIDENCE_SOURCE__twillingate__filename=../../../local/twillingate.db npm run sources` against a seeded local database (Task 13 updates the seeder). Expected: the sources build; the views page renders.

- [ ] **Step 5: Commit**

```bash
git add evidence
git commit -m "feat(dashboards): one views page replaces the web and app pages"
```

---

### Task 13: Scripts, spec status and the full check

**Files:**
- Modify: `scripts/seed-demo.py`, `scripts/smoke.sh`, `scripts/test-compose.sh`, `docs/superpowers/specs/2026-09-17-views-family-design.md`

- [ ] **Step 1: `scripts/seed-demo.py`**

The `DELETE` loop iterates `("views", "product_events", "actors", "identities")`. The web insert becomes:

```python
                cur.execute(
                    "INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind,"
                    " user_id, group_id, path, referrer_source, country, device, browser,"
                    " browser_version, os, utm_source, utm_medium, utm_campaign, display_width, display_height)"
                    " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (str(uuid.uuid4()), alias, ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
                     ts.strftime("%Y-%m-%dT%H:%M:%SZ"), "web", vh,
                     "install" if identified else "connection", "", "",
                     pick(profile["pages"]), ref, country, device, browser, pick(BROWSER_VERSIONS),
                     osname, us, um, uc, *pick(DISPLAYS)))
```

with `BROWSER_VERSIONS = ["126", "127", "128"]` and `DISPLAYS = [(1920, 1080), (1440, 900), (390, 844), (2560, 1440), (360, 800)]` added next to the other constant lists. The product insert uses the column list `(…, actor_id, actor_kind, user_id, group_id, os, app_version, attributes)` with `"user" if identified else "connection"` after `actor_for(...)`. The app insert becomes:

```python
                cur.execute(
                    "INSERT INTO views (id, project, ts, received_at, kind, actor_id, actor_kind, user_id,"
                    " group_id, session_id, path, os, app_version, os_version,"
                    " device_model, locale, country) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (str(uuid.uuid4()), alias, ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
                     ts.strftime("%Y-%m-%dT%H:%M:%SZ"), "app", actor,
                     "user" if identified else "install",
                     f"user-{alias}-{n}" if identified else "",
                     GROUPS[n % len(GROUPS)][0] if identified else "",
                     session, pick(SCREENS), platform, version,
                     pick(OS_VERSIONS[platform]), pick(DEVICE_MODELS[platform]),
                     pick(LOCALES), pick(COUNTRIES)))
```

and `PLATFORMS` (and the keys of `OS_VERSIONS`/`DEVICE_MODELS`) become `"iOS"` / `"Android"`. The summary print becomes `views=` / `product_events=` counts (fold the old `web_hits` and `app_views` counters into one `views` count). Run `python3 -m py_compile scripts/seed-demo.py`.

- [ ] **Step 2: `scripts/smoke.sh` and `scripts/test-compose.sh`**

In both payloads: `"$platform":"ios"` → `"$os":"ios"`, `"$pageview"` → `"$page_view"`. Any `sqlite3 … SELECT COUNT(*) FROM web_hits/app_views` assertion becomes one against `views` (with `kind='web'` / `kind='app'` where the script distinguishes them). Comments mentioning pageviews being bot-filtered stay true (web kind).

- [ ] **Step 3: Full verification**

Run, from the repo root:

```bash
make vet && ./scripts/coverage.sh
cd sdk && npm test && npm run typecheck && npm run build && cd .. && git diff --exit-code internal/server/twillingate.js
```

Expected: vet clean, coverage ≥ 90% total and per core package, SDK green with no bundle drift. `./scripts/test-restore.sh` needs `sqlite3`; run it if installed, otherwise CI runs it on push.

Then boot the binary against a fresh database and against a copy of a pre-merge database to see 012 apply:

```bash
make build
DATABASE_DSN=sqlite://$(mktemp -d)/fresh.db GEO_DSN=none:// ./twillingate migrate
```

Expected: exits 0; `twillingate migrate` on a copy of an existing `twillingate.db` (for example `local/twillingate.db` after `make seed-demo` on `main`) also exits 0 and the log names migration 012.

- [ ] **Step 4: Spec status**

In `docs/superpowers/specs/2026-09-17-views-family-design.md` change `Status: approved 2026-09-18, implementation pending` to `Status: implemented`.

- [ ] **Step 5: Commit**

```bash
git add scripts docs/superpowers/specs
git commit -m "chore: seed and smoke the views family; mark the spec implemented"
```

- [ ] **Step 6: Open the PR**

Push the branch and open a PR whose title is the squash subject `feat!: merge web and app analytics into one views family`. The body lists the breaking changes from spec §17 verbatim under a "Breaking" heading and reminds the operator to snapshot before upgrading. Before merging to `main`, take a Litestream snapshot of prod (`prod-us-3`), since merging cuts a release and the installer upgrades in place.
