# One Events Table Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store views and product events in one raw `events` table keyed by a `family` column. Every row keeps every reserved key it is sent. The SDK sends location on product events and honours `autoAttributes: false` and `null` families.

**Architecture:**
- Migration 020 rebuilds `events` as the union of both raw tables plus `family` (`views` | `product`), copies the raw window in, and drops `views`.
- Two internal views, `raw_views` and `raw_product`, are the only read path, so a product query cannot count pageviews by forgetting a filter.
- Every `v_*` view is recreated against them; the aggregate tables do not change.
- Go gets one row type (`store.Event`), one queue, one `WriteEvents`.
- The SDK learns `autoAttributes`, literal-prefix null families, and location on `track()`.

**Tech Stack:** Go 1.x with SQLite (modernc), TypeScript SDK (vitest, esbuild via `npm run build`), Python for the one-off SQL generator.

**Spec:** `docs/superpowers/specs/2026-09-24-one-events-table-design.md` (merged in #57). Read it before starting any task.

## Global Constraints

- Go lives at `/usr/local/go/bin`. Run `export PATH=$PATH:/usr/local/go/bin` before any `go` or `make` command.
- **Implementers do not commit.** Each task ends with a report; the controller reviews and commits with the message given in the task.
- Migration file: `internal/store/sqlite/migrations/020_one_events_table.sql`.
  - Every migration test pins its ceiling with `newTestDBAt(t, N)` / `db.migrateThrough(ctx, N)`.
  - A migration test that then calls current Go store code calls `db.Migrate(ctx)` first.
- `family` values are exactly `views` and `product`, written by Go (`store.FamilyViews`, `store.FamilyProduct`). The database holds no list of families, view names or vocabularies.
- **Read path:**
  - Go code and every `v_*` definition read the raw table only through `raw_views` or `raw_product`.
  - The one exception is `v_events_flat`, which reads `events` and carries `family`.
  - Writes (`INSERT INTO events`) and deletes (`DELETE FROM events … family = ?`) go to the table.
- Raw SQL filters on `day`, never on `ts` ranges or `substr(ts,1,10)`.
- **Index:** exactly one on `events`, `idx_events_family` on `(family, project_id, day, event_name)`, plus the primary key. This deviates from the spec's two indexes; the user approved it (see "Deviation from the spec").
- The script tag keeps exactly eight `data-` attributes. `autoAttributes` is code-only and defaults to `true`.
- After any change under `sdk/`, run `npm run build` in `sdk/` so `internal/server/twillingate.js` is regenerated.
- Docs change in the same task as the behaviour they describe (CLAUDE.md table). `internal/api/docs_sync_test.go` must pass.
- Commits use Conventional Commits. No model identifiers in any repo file or commit.
- `make check` must pass at the end of every task.

## Deviation from the spec (approved by the user, 2026-09-24)

The spec names two indexes, `(project_id, family, day)` and `(project_id, event_name, day)`. Before this plan, the migration was prototyped on the demo seed: 160k raw views and 5k product events at schema 19, with no `ANALYZE`, as in production. Every `v_*` view returned identical rows before and after.

With the two spec indexes:

| read | 019 | 020, spec indexes |
| --- | --- | --- |
| `v_product_attrs` | 30 ms | 122 ms |
| `v_product_daily` | 6 ms | 30 ms |
| `v_identity_daily` | 74 ms | 110 ms |

The cause: the live halves' union arms get no project filter pushed into them, so they scan by `family` alone. Product rows are also now interleaved with 20× as many view rows.

Three single-index shapes were measured:

| read | 019 | A: `family, project_id, day, event_name` | A + `actor_id` | A + `actor_id, user_id, group_id` |
| --- | --- | --- | --- | --- |
| views paths | 156 ms | 160 | 164 | 165 |
| views daily | 1356 ms | 1444 | 1450 | 1446 |
| `v_product_daily` | 6.2 ms | 2.3 | 1.7 | 1.9 |
| `v_product_totals` | 3.3 ms | 1.9 | 1.5 | 1.7 |
| `v_product_attrs` | 29 ms | 33 | 37 | 35 |
| `v_identity_daily` | 75 ms | 117 | 125 | 81 |
| index size (165k rows) | — | 6.4 MB | 9.0 MB | 11.2 MB |

`actor_id` alone buys nothing. `user_id, group_id` only speed up the `v_identity_daily` live half, which feeds the `identities` tool and the nightly dashboard build; neither is latency-sensitive. **The user chose A.** The identity live half is accepted at about +40 ms on this data. If it ever matters, the fix is letting that query push its project filter down, not widening the index. The per-event, per-day lookup the user asked for is the index itself. Task 6 re-measures with the Go benchmarks.

## Review Focus

1. **The legacy `$pageview` name.** A deployed tag still sends `$pageview`. Its row must be stored with `event_name = '$page_view'`, not the alias. Owned by Task 4.
2. **A project that already declares a `$` key** (e.g. `"$os"` from before 020). Renaming it must fail with `ErrInvalid` naming the offending key, not a generic error. Owned by Task 3.
3. **A `maskUrl` that throws during `track()`.** The event must still be sent, without `$host`/`$path`; a pageview is dropped in that case, an event is not. Owned by Task 5.
4. **Precedence against a family null.** `attrs({ $utm: null })` followed by `track("x", { $utm_source: "mail" })` must send `$utm_source: "mail"`: a later layer's explicit value beats an earlier family null. Owned by Task 5.
5. **Views of a kind other than `web` or `app`** (e.g. `cli`). The migration copies them as `$screen_view`, and `v_views_daily` per kind must be unchanged. Owned by Task 2.

---

### Task 1: One row type, one queue, one write call (schema unchanged)

Pure refactor: `store.View` and `store.ProductEvent` become `store.Event` with a `Family`. The schema stays at 019; `WriteEvents` still writes each family to its old table. Also adds the benchmarks Task 6 compares against and records their baseline.

**Files:**
- Modify: `internal/store/store.go` (types, `Store` interface)
- Modify: `internal/store/sqlite/write.go` (`WriteEvents` replaces `WriteViews` and `WriteProductEvents`)
- Modify: `internal/pipeline/pipeline.go` (`Sink.WriteEvents`, `Buffer.Enqueue`, one slice)
- Modify: `internal/server/server.go` (`Enqueuer.Enqueue`), `internal/server/handlers.go` (build `store.Event`)
- Modify: `internal/store/sqlite/bench_test.go` (new product/identity benchmarks)
- Modify: every test that builds rows or fakes the sink/queue: `internal/store/sqlite/*_test.go`, `internal/pipeline/pipeline_test.go`, `internal/jobs/jobs_test.go`, `internal/jobs/errors_test.go`, `internal/server/server_test.go`, and any other file `grep` finds.

**Interfaces:**
- Produces:
  ```go
  // internal/store/store.go
  type Family string
  const (
      FamilyViews   Family = "views"
      FamilyProduct Family = "product"
  )
  type Event struct { /* fields below */ }
  // Store interface: WriteEvents(ctx context.Context, evs []Event) error
  //   (replaces WriteViews and WriteProductEvents)
  // internal/pipeline: type Sink interface { WriteEvents(ctx context.Context, evs []store.Event) error }
  //                    func (b *Buffer) Enqueue(e store.Event)
  // internal/server:   type Enqueuer interface { Enqueue(e store.Event) }
  ```

- [ ] **Step 1: Record the baseline benchmarks (before touching code)**

Add to `internal/store/sqlite/bench_test.go`, below `seedBenchViews`:

```go
// seedBenchEvents adds 30 days x 250 product events for benchProject,
// across 5 event names and the same actor pool as seedBenchViews, with a
// declared-style attribute and the environment columns a product event
// kept at 019. Written through the store's own write path.
func seedBenchEvents(b *testing.B, db *DB) {
	b.Helper()
	ctx := context.Background()
	const (
		days      = 30
		perDay    = 250
		numActors = 2000
	)
	names := []string{"signup", "activated", "export", "invite_sent", "subscribed"}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for d := 0; d < days; d++ {
		dayStart := start.AddDate(0, 0, d)
		batch := make([]store.ProductEvent, perDay)
		for i := 0; i < perDay; i++ {
			ts := dayStart.Add(time.Duration(i) * (24 * time.Hour / perDay))
			batch[i] = store.ProductEvent{
				ID:         fmt.Sprintf("bench-ev-%02d-%04d", d, i),
				ProjectID:  benchProject,
				EventName:  names[i%len(names)],
				TS:         ts,
				ReceivedAt: ts,
				ActorID:    fmt.Sprintf("actor-%04d", i%numActors),
				ActorKind:  store.ActorUser,
				UserID:     fmt.Sprintf("actor-%04d", i%numActors),
				GroupID:    fmt.Sprintf("org-%02d", i%40),
				Platform:   "web",
				OS:         []string{"windows", "macos", "ios"}[i%3],
				AppVersion: []string{"1.0", "1.1"}[i%2],
				Attributes: map[string]string{"plan": []string{"free", "pro", "team"}[i%3]},
			}
		}
		if err := db.WriteProductEvents(ctx, batch); err != nil {
			b.Fatalf("seed events day %d: %v", d, err)
		}
	}
}

// benchLiveQuery runs q with (benchProject, from, to) b.N times and fails
// on an empty result, so a broken view cannot benchmark as fast.
func benchLiveQuery(b *testing.B, db *DB, q string) {
	b.Helper()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.db.QueryContext(ctx, q, benchProject, "2026-08-01", "2026-08-07")
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
		rows.Close()
		if n == 0 {
			b.Fatal("query returned no rows")
		}
	}
}

// The product and identity live halves, on a raw table that also holds
// 150k views: the cost the one-table merge (migration 020) must not raise.
func BenchmarkProductAttrsLiveHalf(b *testing.B) {
	db := setupBenchDB(b)
	seedBenchViews(b, db)
	seedBenchEvents(b, db)
	benchLiveQuery(b, db, `SELECT attr_key, attr_value, SUM(count) FROM v_product_attrs
		WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1, 2`)
}

func BenchmarkProductDailyLiveHalf(b *testing.B) {
	db := setupBenchDB(b)
	seedBenchViews(b, db)
	seedBenchEvents(b, db)
	benchLiveQuery(b, db, `SELECT event_name, SUM(count) FROM v_product_daily
		WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1`)
}

func BenchmarkIdentityDailyLiveHalf(b *testing.B) {
	db := setupBenchDB(b)
	seedBenchViews(b, db)
	seedBenchEvents(b, db)
	benchLiveQuery(b, db, `SELECT kind, COUNT(*) FROM v_identity_daily
		WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1`)
}
```

`seedBenchEvents` writes `ProductEvent` because this step runs before the type change. Step 3's rename converts it along with everything else.

Run on the unchanged code:

```bash
export PATH=$PATH:/usr/local/go/bin
go test ./internal/store/sqlite/ -run '^$' -bench 'LiveHalf' -benchtime 5x -count 3 | tee /tmp/bench-019.txt
```

Expected: five benchmarks (`ViewsPaths`, `ViewsDaily`, `ProductAttrs`, `ProductDaily`, `IdentityDaily`), all passing. **Paste the output into your task report**: Task 6 compares against it.

- [ ] **Step 2: Replace the row types in `internal/store/store.go`**

Replace the `View` and `ProductEvent` declarations (and their comments) with:

```go
// Family names the aggregate family a raw row feeds: views (v_views_*,
// agg_views_*, views_overview, views_breakdown) or product (v_product_*,
// agg_product_*, product_events, product_attributes). Ingest decides it
// from the event name; the database holds no list of families.
type Family string

const (
	FamilyViews   Family = "views"
	FamilyProduct Family = "product"
)

// Event is one row of the raw events table: a view ($page_view,
// $screen_view) or a product event (any other name), told apart by Family.
// Kind is the client-declared surface ("web", "app", "cli", …); empty on a
// product event that declared none. ActorKind records how the actor was
// identified and is what retention cohorts on.
//
// Platform is the surface the product is used through (web, ios, electron,
// …); OS is the operating system it runs on. They coincide for a native
// app and diverge everywhere else. OSName is the free-form name the client
// reported, kept beside an OS of other so the bucket stays investigable.
// Attributes holds the custom (non-$) keys, for views and product events
// alike.
type Event struct {
	ID                                             string
	ProjectID                                      int64
	Family                                         Family
	EventName                                      string
	TS, ReceivedAt                                 time.Time
	Kind                                           string
	ActorID, ActorKind, UserID, GroupID, SessionID string
	Host, Path, ReferrerSource                     string
	UTMSource, UTMMedium, UTMCampaign              string
	Platform, OS, OSVersion, OSName                string
	Browser, BrowserVersion                        string
	AppVersion, AppLocale, BrowserLocale           string
	Device, DeviceModel                            string
	DisplayWidth, DisplayHeight                    int
	Country                                        string
	Consent                                        Consent
	Attributes                                     map[string]string
}
```

In the `Store` interface, replace the two write methods with:

```go
	WriteEvents(ctx context.Context, evs []Event) error
```

- [ ] **Step 3: Mechanically rename every use, then fix what the rename cannot**

```bash
cd "$(git rev-parse --show-toplevel)"
files=$(grep -rl --include=*.go -E 'store\.View\b|store\.ProductEvent\b|WriteViews|WriteProductEvents|EnqueueView|EnqueueEvent' .)
sed -i -E \
  -e 's/store\.View\{/store.Event{Family: store.FamilyViews, /g' \
  -e 's/store\.ProductEvent\{/store.Event{Family: store.FamilyProduct, /g' \
  -e 's/\[\]store\.View\b/[]store.Event/g' \
  -e 's/\[\]store\.ProductEvent\b/[]store.Event/g' \
  -e 's/\bstore\.View\b/store.Event/g' \
  -e 's/\bstore\.ProductEvent\b/store.Event/g' \
  -e 's/\bWriteViews\(/WriteEvents(/g' \
  -e 's/\bWriteProductEvents\(/WriteEvents(/g' \
  $files
gofmt -w $files
go build ./... 2>&1 | head -40
go vet ./... 2>&1 | head -60
```

The compiler then points at what `sed` cannot do; fix each by hand:

- **Fakes that implemented both `WriteViews` and `WriteProductEvents`** (pipeline, jobs, server tests) now define `WriteEvents` twice. Merge them into one `WriteEvents(ctx, evs []store.Event) error` that appends to the fake's existing slices by `e.Family`. Keep the fake's field names (`views`, `events`) so assertions stay unchanged.
- **Fakes of `server.Enqueuer`** (`EnqueueView` / `EnqueueEvent`) become one `Enqueue(e store.Event)` that appends to `q.views` when `e.Family == store.FamilyViews` and to `q.events` otherwise. Their slice types become `[]store.Event`.
- **Positional composite literals**, if any: convert them to keyed literals with `Family` set.

- [ ] **Step 4: `WriteEvents` in `internal/store/sqlite/write.go`**

Replace `WriteViews` and `WriteProductEvents` with one exported method that splits by family into the two unexported bodies. The bodies are the old functions, unchanged except for their names and parameter types:

```go
// WriteEvents stores a batch of raw rows. Until migration 020 merges the
// raw tables, views and product events still land in separate tables.
func (d *DB) WriteEvents(ctx context.Context, evs []store.Event) error {
	var views, product []store.Event
	for _, e := range evs {
		if e.Family == store.FamilyViews {
			views = append(views, e)
		} else {
			product = append(product, e)
		}
	}
	if err := d.writeViews(ctx, views); err != nil {
		return err
	}
	return d.writeProduct(ctx, product)
}
```

Rename `func (d *DB) WriteViews(ctx context.Context, views []store.View)` to `func (d *DB) writeViews(ctx context.Context, views []store.Event)`. Rename `WriteProductEvents` to `writeProduct(ctx context.Context, evs []store.Event)`. Keep their SQL.

- [ ] **Step 5: One queue in `internal/pipeline/pipeline.go`**

```go
type Sink interface {
	WriteEvents(ctx context.Context, evs []store.Event) error
}
```

Delete the `item` type. The channel becomes `chan store.Event`, `Enqueue(e store.Event)` sends `e`, and `Run` keeps one `var batch []store.Event`:

```go
func (b *Buffer) Enqueue(e store.Event) { b.enqueue(e) }

func (b *Buffer) enqueue(e store.Event) {
	for {
		select {
		case b.ch <- e:
			return
		default:
			select {
			case <-b.ch:
				b.dropped.Add(1)
			default:
			}
		}
	}
}
```

In `Run`, `take` appends to `batch`, and `flush` becomes:

```go
	flush := func(ctx context.Context) {
		if len(batch) > 0 {
			b.write(ctx, func(c context.Context) error { return b.sink.WriteEvents(c, batch) }, len(batch), "events")
			batch = nil
		}
	}
```

The size check becomes `len(batch) >= b.cfg.FlushMaxEvents`. Update the package comment if it mentions two kinds.

- [ ] **Step 6: Server builds `store.Event`**

- In `internal/server/server.go`: `type Enqueuer interface { Enqueue(e store.Event) }`.
- In `internal/server/handlers.go`, the product branch calls `s.queue.Enqueue(store.Event{Family: store.FamilyProduct, EventName: ev.Name, …same fields…})`.
- The view branch builds `v := store.Event{Family: store.FamilyViews, EventName: ev.Name, …}` and calls `s.queue.Enqueue(v)`.

Behaviour is unchanged in this task, including which keys each family keeps.

- [ ] **Step 7: Verify**

```bash
export PATH=$PATH:/usr/local/go/bin
go vet ./... && go test ./... 2>&1 | tail -20
make check
```

Expected: all packages `ok`, `make check` exit 0. Also rerun the benchmarks once to confirm they still pass on the renamed code:

```bash
go test ./internal/store/sqlite/ -run '^$' -bench 'LiveHalf' -benchtime 1x
```

- [ ] **Step 8: Report.** Include the baseline benchmark output from Step 1. Controller commits:
`refactor(store): one Event row type, one queue and one WriteEvents call`

---

### Task 2: Migration 020 — one raw table read through `raw_views` / `raw_product`

Semantics-preserving. After this task every `v_*` view returns the same rows as before, product events still keep only their eight keys (ingest changes in Task 4), and `v_product_attrs` still has its four system arms (Task 3 adds more).

**Files:**
- Create: `internal/store/sqlite/migrations/020_one_events_table.sql`
- Create: `scripts/gen-020-views.py` (one-off generator; deleted in Step 4 after use)
- Modify: `internal/store/sqlite/write.go` (one INSERT)
- Modify: `internal/store/sqlite/aggregate_views.go` (`daysBefore`, `viewSessionsCTE`, `aggregateSQL`, `AggregateViewDay`)
- Modify: `internal/store/sqlite/aggregate_product.go` (all SQL onto `raw_product` + `day`)
- Modify: `internal/store/sqlite/identities.go`, `internal/store/sqlite/retention.go`
- Modify: `internal/store/sqlite/registry.go` (`projectTables`: drop `"views"`)
- Modify: `internal/store/sqlite/flatview.go` (base columns)
- Create: `internal/store/sqlite/migration020_test.go`, `internal/store/sqlite/rawreads_test.go`
- Modify: `internal/store/sqlite/views_test.go` (`TestViewsLiveHalvesUseTheDayIndex`), `internal/store/sqlite/registry_test.go`, `internal/store/sqlite/coverage_test.go`, `internal/api/seed_test.go` (raw `INSERT INTO views` → `events`)
- Modify: `scripts/seed-demo.py` (inserts go to `events` with `family`)
- Modify: `docs/twillingate.md` (event model, `v_events_flat`), `deploy/UPGRADES.md` (020 section)

**Interfaces:**
- Consumes: `store.Event`, `store.FamilyViews`, `store.FamilyProduct`, `(*DB).WriteEvents` from Task 1.
- Produces:
  - SQL objects `raw_views` and `raw_product` (views over `events`);
  - the `events` columns listed in Step 2;
  - the index `idx_events_family`;
  - Go constants in `aggregate_views.go`: `const rawViews = "raw_views"` and `const rawProduct = "raw_product"`, used by every query builder.

- [ ] **Step 1: Write the load-bearing migration test**

Create `internal/store/sqlite/migration020_test.go`:

```go
package sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// snapshotViews reads every v_* view except v_events_flat, each row
// rendered as text and the rows sorted, so two schemas can be compared
// for identical answers.
func snapshotViews(t *testing.T, db *DB) map[string][]string {
	t.Helper()
	names := []string{}
	rows, err := db.db.Query(`SELECT name FROM sqlite_schema WHERE type='view'
		AND name LIKE 'v\_%' ESCAPE '\' AND name <> 'v_events_flat' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	out := map[string][]string{}
	for _, n := range names {
		r, err := db.db.Query("SELECT * FROM " + n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				parts[i] = fmt.Sprintf("%v", v)
			}
			out[n] = append(out[n], strings.Join(parts, "|"))
		}
		r.Close()
		sort.Strings(out[n])
	}
	return out
}

// Migration 020 moves both raw tables into one. Every v_* view must answer
// exactly as it did at 19, for raw days and rolled-up days, views of every
// kind (web, app, cli) and product events alike.
func TestMigration020KeepsEveryViewsAnswer(t *testing.T) {
	db := newTestDBAt(t, 19)
	ctx := context.Background()
	view := func(id, day, kind, actor, path, user, group string, consent string) string {
		return fmt.Sprintf(`INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind,
			user_id, group_id, host, path, referrer_source, utm_source, platform, os, os_version, browser,
			browser_version, device, browser_locale, app_locale, display_width, display_height, country, consent)
			VALUES ('%s', 1, '%sT10:00:00Z', '%sT10:00:00Z', '%s', '%s', 'user', '%s', '%s', 'shop.example.com',
			'%s', 'google', 'hn', 'web', 'macos', '14', 'safari', '17', 'desktop', 'de-DE', 'en', 1440, 900, 'DE', %s)`,
			id, day, day, kind, actor, path, user, group, consent)
	}
	event := func(id, day, name, actor, user, group, attrs string) string {
		return fmt.Sprintf(`INSERT INTO events (id, project_id, event_name, actor_id, ts, attributes, user_id,
			group_id, os, app_version, received_at, actor_kind, platform, consent, app_locale)
			VALUES ('%s', 1, '%s', '%s', '%sT11:00:00Z', '%s', '%s', '%s', 'ios', '2.4.1', '%sT11:00:00Z',
			'user', 'ios', 1, 'de')`, id, name, actor, day, attrs, user, group, day)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, attributes) VALUES (1, 'Site', '["plan"]')`,
		view("v1", "2026-09-20", "web", "a1", "/", "u1", "g1", "1"),
		view("v2", "2026-09-20", "web", "a1", "/pricing", "u1", "g1", "1"),
		view("v3", "2026-09-20", "app", "a2", "/home", "", "", "0"),
		view("v4", "2026-09-20", "cli", "a3", "/run", "u3", "", "NULL"),
		view("v5", "2026-09-21", "web", "a4", "/", "", "g2", "NULL"),
		event("e1", "2026-09-20", "signup", "a1", "u1", "g1", `{"plan":"pro"}`),
		event("e2", "2026-09-20", "signup", "a2", "", "", `{"plan":"free"}`),
		event("e3", "2026-09-21", "export", "a1", "u1", "g1", `{}`),
		`INSERT INTO agg_views_paths (project_id, day, path, visitors, views) VALUES (1, '2026-09-01', '/', 4, 9)`,
		`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (1, '2026-09-01', 'signup', 3, 2)`,
	} {
		if _, err := db.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	before := snapshotViews(t, db)
	for _, must := range []string{"v_views_daily", "v_views_paths", "v_views_locales", "v_views_consent",
		"v_product_daily", "v_product_attrs", "v_identity_daily"} {
		if len(before[must]) == 0 {
			t.Fatalf("%s is empty before the migration; the comparison would be vacuous", must)
		}
	}
	if err := db.migrateThrough(ctx, 20); err != nil {
		t.Fatalf("migration 020: %v", err)
	}
	after := snapshotViews(t, db)
	for name, rows := range before {
		if strings.Join(rows, "\n") != strings.Join(after[name], "\n") {
			t.Errorf("%s changed:\nbefore %v\nafter  %v", name, rows, after[name])
		}
	}
	if hasTable(t, db, "views") {
		t.Error("the views table survived migration 020")
	}
	got := map[string]string{}
	rows, err := db.db.Query(`SELECT id, family || ' ' || event_name FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		got[id] = v
	}
	rows.Close()
	want := map[string]string{
		"v1": "views $page_view", "v2": "views $page_view", "v3": "views $screen_view",
		"v4": "views $screen_view", "v5": "views $page_view",
		"e1": "product signup", "e2": "product signup", "e3": "product export",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("row %s = %q, want %q", id, got[id], w)
		}
	}
}
```

If `hasTable` does not exist in the package's test helpers, add it to `internal/store/sqlite/sqlite_test.go` next to `hasColumn`:

```go
func hasTable(t *testing.T, db *DB, name string) bool {
	t.Helper()
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}
```

Run: `go test ./internal/store/sqlite/ -run TestMigration020 -v`
Expected: FAIL — migration 20 does not exist (`migrateThrough` applies nothing, so `views` survives and the family query errors).

- [ ] **Step 2: Write the table half of the migration**

Create `internal/store/sqlite/migrations/020_one_events_table.sql` with this header and table section. The view section is appended in Step 3.

```sql
-- 020: one raw table for views and product events.
-- Spec: docs/superpowers/specs/2026-09-24-one-events-table-design.md
--
-- events becomes the union of both raw tables plus family ('views' or
-- 'product', written by ingest; the database holds no list of families).
-- Raw tables only hold days not yet rolled up, so this copies about a
-- month of rows. Copied views are named from their kind, the pairing
-- viewName() uses: the original name was never stored.
--
-- Every view that read a raw table is dropped first: ALTER TABLE ... RENAME
-- re-parses the schema and would fail on a view naming a dropped table.
-- They are recreated at the end against raw_views and raw_product, the
-- only read path (v_events_flat excepted: it holds both families).
DROP VIEW IF EXISTS v_events_flat;
-- (Step 3 inserts the generated DROP VIEW lines here.)

CREATE TABLE events_new (
    id              TEXT PRIMARY KEY,
    project_id      INTEGER NOT NULL,
    family          TEXT NOT NULL,
    event_name      TEXT NOT NULL,
    ts              TEXT NOT NULL,
    day             TEXT GENERATED ALWAYS AS (substr(ts,1,10)) STORED,
    received_at     TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT '',
    actor_id        TEXT NOT NULL,
    actor_kind      TEXT NOT NULL DEFAULT '',
    user_id         TEXT NOT NULL DEFAULT '',
    group_id        TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    host            TEXT NOT NULL DEFAULT '',
    path            TEXT NOT NULL DEFAULT '',
    referrer_source TEXT NOT NULL DEFAULT '',
    utm_source      TEXT NOT NULL DEFAULT '',
    utm_medium      TEXT NOT NULL DEFAULT '',
    utm_campaign    TEXT NOT NULL DEFAULT '',
    platform        TEXT NOT NULL DEFAULT 'unknown',
    os              TEXT NOT NULL DEFAULT '',
    os_version      TEXT NOT NULL DEFAULT '',
    os_name         TEXT NOT NULL DEFAULT '',
    browser         TEXT NOT NULL DEFAULT '',
    browser_version TEXT NOT NULL DEFAULT '',
    browser_locale  TEXT NOT NULL DEFAULT '',
    app_version     TEXT NOT NULL DEFAULT '',
    app_locale      TEXT NOT NULL DEFAULT '',
    device          TEXT NOT NULL DEFAULT '',
    device_model    TEXT NOT NULL DEFAULT '',
    display_width   INTEGER NOT NULL DEFAULT 0,
    display_height  INTEGER NOT NULL DEFAULT 0,
    country         TEXT NOT NULL DEFAULT '',
    consent         INTEGER,
    attributes      TEXT NOT NULL DEFAULT '{}'
);

INSERT INTO events_new (id, project_id, family, event_name, ts, received_at, kind,
    actor_id, actor_kind, user_id, group_id, session_id, host, path, referrer_source,
    utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser,
    browser_version, browser_locale, app_version, app_locale, device, device_model,
    display_width, display_height, country, consent, attributes)
SELECT id, project_id, 'views', CASE kind WHEN 'web' THEN '$page_view' ELSE '$screen_view' END,
    ts, received_at, kind,
    actor_id, actor_kind, user_id, group_id, session_id, host, path, referrer_source,
    utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser,
    browser_version, browser_locale, app_version, app_locale, device, device_model,
    display_width, display_height, country, consent, '{}'
FROM views;

-- OR IGNORE: ids are client UUIDs, so a product event reusing a view's id
-- is the only collision possible, and the view wins.
INSERT OR IGNORE INTO events_new (id, project_id, family, event_name, ts, received_at,
    actor_id, actor_kind, user_id, group_id, platform, os, app_version, app_locale,
    consent, attributes)
SELECT id, project_id, 'product', event_name, ts, received_at,
    actor_id, actor_kind, user_id, group_id, platform, os, app_version, app_locale,
    consent, attributes
FROM events;

DROP TABLE views;
DROP TABLE events;
ALTER TABLE events_new RENAME TO events;

-- One index serves every read. Leading on family lets a union arm that
-- gets no project filter pushed into it (the live halves of v_product_attrs
-- and v_identity_daily) search one family instead of scanning both;
-- project_id and day serve every ranged read and the daily pass;
-- event_name serves the per-event product rollup. The old actor and
-- session indexes had no reader and are not carried over.
CREATE INDEX idx_events_family ON events(family, project_id, day, event_name);

-- The only read path for Go code and every v_* definition: a query cannot
-- forget the family filter it never writes. SQLite flattens these into the
-- outer query, so the index above still applies.
CREATE VIEW raw_views   AS SELECT * FROM events WHERE family = 'views';
CREATE VIEW raw_product AS SELECT * FROM events WHERE family = 'product';

-- Base shape only; RebuildFlatView replaces it with the declared attr_
-- columns on the next boot or registry write.
CREATE VIEW v_events_flat AS SELECT id, project_id, family, event_name, actor_id, kind, consent, ts, attributes FROM events;
```

- [ ] **Step 3: Generate the view section from a schema-19 database**

Create `scripts/gen-020-views.py`:

```python
"""Emit the view half of migration 020 from a schema-19 database.

Reads every v_* view that reads a raw table, rewrites its raw-table
references onto raw_views / raw_product and its substr(ts,1,10) day
expressions onto the generated day column, and prints the DROP lines and
the CREATE statements. One-off: run once, paste, delete.
"""
import re
import sqlite3
import sys

db = sqlite3.connect(sys.argv[1])
rows = db.execute("""SELECT name, sql FROM sqlite_schema WHERE type='view'
  AND name <> 'v_events_flat'
  AND (sql LIKE '%FROM views%' OR sql LIKE '%JOIN views%'
       OR sql LIKE '%FROM events%' OR sql LIKE '%JOIN events%')
  ORDER BY name""").fetchall()


def rewrite(sql):
    sql = re.sub(r'\b(FROM|JOIN)(\s+)views\b', r'\1\2raw_views', sql)
    sql = re.sub(r'\b(FROM|JOIN)(\s+)events\b', r'\1\2raw_product', sql)
    sql = re.sub(r'substr\((\w+\.)?ts,\s*1,\s*10\)',
                 lambda m: (m.group(1) or '') + 'day', sql)
    return sql


mode = sys.argv[2]
if mode == "drops":
    print("\n".join(f"DROP VIEW {n};" for n, _ in rows))
else:
    print("\n\n".join(rewrite(s) + ";" for _, s in rows))
```

Build a schema-19 database with the binary as it is now (Task 1's code, migrations up to 019), then generate:

```bash
export PATH=$PATH:/usr/local/go/bin
tmp=$(mktemp -d)
mv internal/store/sqlite/migrations/020_one_events_table.sql $tmp/020.sql   # hide 020 while building 019
go build -o $tmp/tg ./cmd/twillingate
mv $tmp/020.sql internal/store/sqlite/migrations/020_one_events_table.sql
DATABASE_DSN="sqlite://$tmp/e.db" $tmp/tg migrate
sqlite3 $tmp/e.db "SELECT MAX(version) FROM schema_migrations"   # expect 19
python3 scripts/gen-020-views.py $tmp/e.db drops    > $tmp/drops.sql
python3 scripts/gen-020-views.py $tmp/e.db creates  > $tmp/creates.sql
grep -c 'CREATE VIEW' $tmp/creates.sql    # expect 18
grep -n -E '\b(FROM|JOIN)\s+(views|events)\b|substr\(' $tmp/creates.sql   # expect no output
```

The binary embeds migrations at build time, which is why 020 is moved aside during `go build`: otherwise the build would include a half-written 020. Do not use `git stash` for this; the stash stack is shared with other worktrees.

Paste `drops.sql` in place of the `-- (Step 3 inserts …)` comment. Append the contents of `creates.sql` at the end of the migration, under the heading:

```sql
-- ===== Every view that read a raw table, unchanged but for its source =====
-- Generated by rewriting the schema-19 definitions: FROM/JOIN views ->
-- raw_views, FROM/JOIN events -> raw_product, substr(ts,1,10) -> day.
```

The 18 views are `v_identity_daily`, `v_product_attrs`, `v_product_daily`, `v_product_totals` and the 14 `v_views_*`. `v_retention` does not read a raw table and is untouched.

- [ ] **Step 4: Delete the generator**

```bash
rm scripts/gen-020-views.py
```

- [ ] **Step 5: Point the Go SQL at the new shape**

In `internal/store/sqlite/aggregate_views.go`, add:

```go
// rawViews and rawProduct are the only read path into the raw events
// table (020_one_events_table.sql): each is a view carrying one family's
// filter, so no query can forget it. Writes and deletes go to events
// with an explicit family.
const (
	rawViews   = "raw_views"
	rawProduct = "raw_product"
)
```

Then:

- **`ViewDaysBefore` / `ProductDaysBefore`** call `d.daysBefore(ctx, rawViews, …)` / `d.daysBefore(ctx, rawProduct, …)`. `daysBefore`'s query becomes
  `fmt.Sprintf(`SELECT DISTINCT day FROM %s WHERE project_id=? AND day < ? ORDER BY 1`, source)` with args `projectID, before.String()`.
- **`viewSessionsCTE`**: `FROM views WHERE project_id = :p AND day = :day` becomes `FROM raw_views WHERE …`.
- **`aggregateSQL`**: `SELECT %s, actor_id FROM views` becomes `FROM raw_views`.
- **`AggregateViewDay`**: the count reads `SELECT COUNT(*) FROM raw_views WHERE project_id=? AND day=?`. The delete becomes
  `DELETE FROM events WHERE family='views' AND project_id=? AND day=?`.

In `internal/store/sqlite/aggregate_product.go`:
- `AggregateProductDay` no longer calls `dayRange`.
  - The count: `SELECT COUNT(*) FROM raw_product WHERE project_id=? AND day=?`.
  - The delete: `DELETE FROM events WHERE family='product' AND project_id=? AND day=?`.
- `rollupProduct(ctx, tx, projectID, day, attrs, topN)` loses its `from, to` parameters. Its three queries read `FROM raw_product WHERE project_id=? AND day=?` with args `projectID, day.String()`.
- In the `named` slices, drop `sql.Named("from", …)` and `sql.Named("to", …)`.
- `rollupAttrValue`'s three statements replace `ts>=:from AND ts<:to` with `day=:day` and `FROM events` with `FROM raw_product`. Update its doc comment's list of required named parameters to `:p, :day, :event, :key, :n`.
- If `dayRange` has no remaining callers, delete it.

In `internal/store/sqlite/identities.go`, the `src` CTE becomes:

```sql
  SELECT %[1]s AS id, actor_id, user_id, 1 AS is_view, 0 AS is_event
  FROM raw_views   WHERE project_id=? AND day=? AND %[1]s <> ''
  UNION ALL
  SELECT %[1]s, actor_id, user_id, 0, 1
  FROM raw_product WHERE project_id=? AND day=? AND %[1]s <> ''
```

In `internal/store/sqlite/retention.go`:
- `actorSources = []string{rawViews, rawProduct}`.
- Delete the `dayExpr` special case and use `day` in the query.
- In `AggregateRetentionDay`'s `active` CTE, read `raw_views` and `raw_product`, both filtered `AND day=?`.
- Update the `actorSources` comment to say they are the two family views over the one raw table.

In `internal/store/sqlite/write.go`, `WriteEvents` becomes one statement (delete `writeViews` and `writeProduct`):

```go
// WriteEvents stores a batch of raw rows, views and product events alike,
// in the one raw table. INSERT OR IGNORE: with client-supplied UUIDv7 ids,
// a batch retried after a timeout that actually succeeded is a no-op.
func (d *DB) WriteEvents(ctx context.Context, evs []store.Event) error {
	if len(evs) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO events
			(id, project_id, family, event_name, ts, received_at, kind,
			 actor_id, actor_kind, user_id, group_id, session_id,
			 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
			 platform, os, os_version, os_name, browser, browser_version, browser_locale,
			 app_version, app_locale, device, device_model, display_width, display_height,
			 country, consent, attributes)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range evs {
			attrs := e.Attributes
			if attrs == nil {
				attrs = map[string]string{}
			}
			blob, err := json.Marshal(attrs)
			if err != nil {
				return fmt.Errorf("event %s attributes: %w", e.ID, err)
			}
			if _, err := stmt.ExecContext(ctx, e.ID, e.ProjectID, string(e.Family), e.EventName,
				e.TS.UTC().Format(tsFormat), e.ReceivedAt.UTC().Format(tsFormat), e.Kind,
				e.ActorID, e.ActorKind, e.UserID, e.GroupID, e.SessionID,
				e.Host, e.Path, e.ReferrerSource, e.UTMSource, e.UTMMedium, e.UTMCampaign,
				e.Platform, e.OS, e.OSVersion, e.OSName, e.Browser, e.BrowserVersion, e.BrowserLocale,
				e.AppVersion, e.AppLocale, e.Device, e.DeviceModel, e.DisplayWidth, e.DisplayHeight,
				e.Country, e.Consent, string(blob)); err != nil {
				return fmt.Errorf("event %s: %w", e.ID, err)
			}
		}
		return nil
	})
}
```

In `internal/store/sqlite/registry.go`, `projectTables` loses `"views"` (keep `"events"`). `TestProjectTablesMatchesSchema` checks this against the live schema.

In `internal/store/sqlite/flatview.go`, `flatViewBaseColumns` becomes:

```go
var flatViewBaseColumns = []string{"id", "project_id", "family", "event_name", "actor_id", "kind", "consent", "ts", "attributes"}
```

Update its comment to say the view holds both families and `family` tells them apart. The statement stays `… FROM events`, the one documented exception to the read path. Say so in a comment above the `stmt :=` line.

- [ ] **Step 6: Guard the read path**

Create `internal/store/sqlite/rawreads_test.go`:

```go
package sqlite

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// rawRead matches a read of the raw table: FROM or JOIN events. A delete
// ("DELETE FROM events") is a write and is excluded by the caller.
var rawRead = regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+events\b`)

// Nothing reads the raw events table except raw_views, raw_product and
// v_events_flat (which holds both families on purpose). Everything else
// reads through the two family views, so a product query can never count
// pageviews by forgetting a filter.
func TestRawTableIsReadOnlyThroughFamilyViews(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.db.Query(`SELECT name, sql FROM sqlite_schema
		WHERE type='view' AND name NOT IN ('raw_views', 'raw_product', 'v_events_flat')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, sql string
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatal(err)
		}
		if rawRead.MatchString(sql) {
			t.Errorf("view %s reads the raw events table directly", name)
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "flatview.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, loc := range rawRead.FindAllIndex(src, -1) {
			before := strings.ToUpper(string(src[max(0, loc[0]-8):loc[0]]))
			if strings.Contains(before, "DELETE") {
				continue
			}
			line := 1 + strings.Count(string(src[:loc[0]]), "\n")
			t.Errorf("%s:%d reads the raw events table directly; use raw_views or raw_product", f, line)
		}
	}
}
```

Run: `go test ./internal/store/sqlite/ -run 'TestRawTable|TestMigration020' -v`
Expected: PASS.

- [ ] **Step 7: Move the query-plan test to the new index**

In `internal/store/sqlite/views_test.go`, `TestViewsLiveHalvesUseTheDayIndex`:
- replace every `idx_views_project_ts` / `idx_views_project_day` with `idx_events_family`;
- the positive check becomes a detail containing `USING INDEX idx_events_family (family=? AND project_id=? AND day` (SQLite reports the searched columns; a bounded search shows `day>?` or `day=?`).

Keep the test's long explanatory comment accurate: replace "idx_views_project_day" in it and add one sentence saying the index leads on `family`.

Add a product case to the same test's query list, or as a sibling test with the same checks:

```go
	"v_product_daily":  `SELECT * FROM v_product_daily  WHERE project_id=? AND day BETWEEN ? AND ?`,
	"v_product_totals": `SELECT * FROM v_product_totals WHERE project_id=? AND day BETWEEN ? AND ?`,
```

These must also show a search on `idx_events_family` with `family=? AND project_id=? AND day`.

- [ ] **Step 8: Fix raw INSERTs in tests and the demo seeder**

`grep -rn "INTO views" internal --include=*_test.go`. Every hit that runs at the latest schema (not a `migration0NN_test.go` pinned below 20) becomes `INSERT INTO events (… , family, event_name) VALUES (… , 'views', '$page_view')`. Known hits:
- `internal/api/seed_test.go:75`
- `internal/store/sqlite/registry_test.go:445`
- `internal/store/sqlite/coverage_test.go:78`

In `scripts/seed-demo.py`, both `INSERT INTO views` statements become `INSERT INTO events` with `family, event_name` columns (`'views'`, and `'$page_view'` for the web profile or `'$screen_view'` for the app views). The `INSERT INTO events` for product rows adds `family` = `'product'`. Verify:

```bash
tmp=$(mktemp -d); go build -o $tmp/tg ./cmd/twillingate
DATABASE_DSN="sqlite://$tmp/t.db" $tmp/tg migrate
DATABASE_DSN="sqlite://$tmp/t.db" $tmp/tg project create -name app
python3 scripts/seed-demo.py $tmp/t.db && rm -rf scripts/__pycache__
sqlite3 $tmp/t.db "SELECT family, COUNT(*) FROM events GROUP BY 1"   # expect both families
```

- [ ] **Step 9: Docs**

`docs/twillingate.md`, section **The event model**: after the family table, add:

> Both families are stored in one raw table, `events`, whose `family` column is `views` or `product`; the aggregates, views and tools of each family read only its own rows.

In **Writing SQL against the views**, the sentence naming `v_events_flat` becomes:

> …plus `v_events_flat`, which holds every raw row of both families (views and product events) with its `family` column — filter `family = 'product'` for product events alone — its `consent` column (1, 0 or NULL) and one column per declared attribute.

Keep the surrounding sentences as they are.

`deploy/UPGRADES.md`: append a section. Tasks 3–5 append their own bullets to it.

```markdown
### Upgrading to one events table (migration 020)

Views and product events move into one raw table, `events`, with a `family`
column (`views` or `product`). Every `v_*` view, aggregate table and tool
answers exactly as before. The migration copies the raw window (days not yet
rolled up, 30 by default) and drops the `views` table: seconds on a month
of traffic.

Before upgrading, run this against the live database:

```sql
-- A project declaring a $ key. Until now such a declaration extracted
-- nothing. From 020, $host, $path, $referrer, $utm_source, $utm_medium,
-- $utm_campaign, $os_version, $browser_version and $device_model start
-- working; any other $ key is refused on the project's next edit.
SELECT id, attributes FROM projects WHERE attributes LIKE '%"$%';
```

What changes on the day:

- `v_events_flat` returns view rows too. Saved SQL over it adds
  `WHERE family = 'product'` to keep its old answer.
- SQL reading the `views` table directly (the CLI's database, not the
  `query` tool) reads `events WHERE family = 'views'`.

There is no down migration. The previous binary writes a `views` table that no
longer exists.
```

- [ ] **Step 10: Verify**

```bash
export PATH=$PATH:/usr/local/go/bin
go vet ./... && go test ./internal/store/... ./internal/api/... ./internal/jobs/... 2>&1 | tail -15
make check
```

Expected: all `ok`, exit 0.

- [ ] **Step 11: Report.** Controller commits:
`feat(store): keep views and product events in one raw table`

---

### Task 3: Product attributes — four more system keys, declarable `$` keys, refusals

**Files:**
- Modify: `internal/store/store.go` (the two key lists)
- Modify: `internal/store/sqlite/aggregate_product.go` (`systemDims` → `store.SystemAttributes`; declared `$` keys)
- Modify: `internal/store/sqlite/migrations/020_one_events_table.sql` (replace the generated `v_product_attrs`)
- Modify: `internal/store/sqlite/flatview.go` (skip `$` keys)
- Modify: `internal/manage/ops.go` (`validate`)
- Modify: `internal/api/ops_read.go` (`product_attributes` description), `internal/api/resources.go` (`v_product_attrs` comment)
- Modify: `docs/twillingate.md` (Attribute breakdowns, `product_attributes` row), `deploy/UPGRADES.md`
- Test: `internal/store/sqlite/views_test.go`, `internal/store/sqlite/flatview_test.go`, `internal/manage/ops_test.go`

**Interfaces:**
- Consumes: `raw_product`, the `events` columns, and `rollupAttrValue(ctx, tx, expr, present string, named []any)` with named `:p, :day, :event, :key, :n` (Task 2).
- Produces:
  ```go
  // internal/store/store.go
  type SystemAttribute struct{ Key, Column string }
  var SystemAttributes []SystemAttribute        // 8 always-on keys, in rollup order
  var DeclarableAttributes map[string]string    // 9 declarable $ keys -> events column
  func DeclarableAttributeKeys() []string       // sorted keys of DeclarableAttributes
  ```

- [ ] **Step 1: Write the failing tests**

In `internal/store/sqlite/views_test.go`:
- extend `TestProductAttrsViewSystemDimensionsWithoutDeclaredKeys`'s `case` list to `"$os", "$platform", "$app_version", "$app_locale", "$kind", "$browser", "$device", "$browser_locale"`;
- in `seedAttrDay`, set on each event `Kind: "web"`, `Browser: []string{"chrome", "safari"}[i%2]`, `Device: "desktop"`, `BrowserLocale: "en-US"`, `Path: fmt.Sprintf("/p/%02d", i)`.

Then add:

```go
// A declared $ key breaks down by its column exactly like a custom key:
// the live half and the rollup agree, the cap folds the tail into
// (other), and a project that does not declare it gets no rows.
func TestProductAttrsDeclaredSystemKeysAcrossBoundary(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	keys := store.DeclarableAttributeKeys()
	id := seedDeclaredProject(t, db, keys)
	other := seedDeclaredProject(t, db, nil)
	var evs []store.Event
	for i := 0; i < 70; i++ {
		for _, pid := range []int64{id, other} {
			evs = append(evs, store.Event{
				ID: fmt.Sprintf("d%d-%03d", pid, i), ProjectID: pid, Family: store.FamilyProduct,
				EventName: "signup", ActorID: fmt.Sprintf("a%d", i%7), TS: ts("2026-08-01T10:00:00Z"),
				Host: "shop.example.com", Path: fmt.Sprintf("/p/%02d", i), ReferrerSource: "google",
				UTMSource: "hn", UTMMedium: "social", UTMCampaign: "launch",
				OSVersion: "17", BrowserVersion: "126", DeviceModel: "iPhone15,2",
			})
		}
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	before := readAttrs(t, db, id, "2026-08-01")
	seen := map[string]bool{}
	for _, r := range before {
		seen[r.Key] = true
	}
	for _, k := range keys {
		if !seen[k] {
			t.Errorf("declared %s produced no rows in the live half", k)
		}
	}
	for _, r := range readAttrs(t, db, other, "2026-08-01") {
		if _, declarable := store.DeclarableAttributes[r.Key]; declarable {
			t.Errorf("undeclared %s produced a row for a project that did not declare it", r.Key)
		}
	}
	if err := db.AggregateProductDay(ctx, id, civil.DateOf(ts("2026-08-01T00:00:00Z")), keys, 50); err != nil {
		t.Fatal(err)
	}
	if after := readAttrs(t, db, id, "2026-08-01"); !reflect.DeepEqual(before, after) {
		t.Fatalf("declared $ keys changed across the rollup:\nbefore %v\nafter  %v", before, after)
	}
}
```

If `readAttrs` returns rows in an unstable order, sort both slices by key and value before comparing, as `TestProductAttrsViewInvariant` does. Check `attrRow`'s field names in `views_test.go` and use them (`r.Key` above assumes a `Key` field).

In `internal/manage/ops_test.go`:

```go
// newOps builds Ops over a fresh migrated store, as the other tests here do.
func newOps(t *testing.T) (*Ops, store.Store, *Registry) {
	t.Helper()
	st := testStore(t)
	reg := New(st, discard())
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewOps(reg, st), st, reg
}

func TestDeclaringSystemKeys(t *testing.T) {
	ops, _, _ := newOps(t)
	ctx := context.Background()
	p, err := ops.CreateProject(ctx, "api", ProjectSpec{Name: "Site", Attributes: []string{"plan", "$path", "$utm_source"}})
	if err != nil {
		t.Fatalf("declarable $ keys refused: %v", err)
	}
	for _, bad := range []string{"$os", "$user_id", "$session_id", "$consent", "$os_name", "$display_width", "$nope"} {
		_, err := ops.UpdateProject(ctx, "api", ProjectSpec{ID: p.ID, Attributes: []string{"plan", bad}})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), bad) || !strings.Contains(err.Error(), "$path") {
			t.Errorf("declaring %s: err = %v, want ErrInvalid naming it and listing the declarable keys", bad, err)
		}
	}
}

// A project that declared a now-refused $ key before 020 cannot be edited
// in any other way until the key goes, and the refusal says which key.
func TestLegacyRefusedDeclarationBlocksRename(t *testing.T) {
	ops, st, reg := newOps(t)
	ctx := context.Background()
	p, err := ops.CreateProject(ctx, "api", ProjectSpec{Name: "Site"})
	if err != nil {
		t.Fatal(err)
	}
	// Plant the legacy declaration through the store, bypassing manage's
	// validation, as a pre-020 binary would have written it.
	if err := st.UpdateProject(ctx, store.RegistryProject{ID: p.ID, Name: "Site", AllowedOrigins: "[]",
		Attributes: `["plan","$os"]`}, store.AuditEntry{Actor: "test", Action: "project.update"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = ops.UpdateProject(ctx, "api", ProjectSpec{ID: p.ID, Name: "Renamed"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "$os") {
		t.Fatalf("rename with a legacy $os declaration: err = %v, want ErrInvalid naming $os", err)
	}
}
```

`testStore` and `discard` live in `internal/manage/registry_test.go`; if `New` returns a type other than `*Registry`, use that type in `newOps`'s signature. Add `errors`, `strings` and `internal/store` to the test file's imports as needed.

In `internal/store/sqlite/flatview_test.go`:

```go
// A declared $ key is a column of the raw table already; it gets no
// attr_ column (it would only ever extract NULL from the blob).
func TestFlatViewSkipsSystemKeys(t *testing.T) {
	db := newTestDB(t)
	if err := db.RebuildFlatView(context.Background(), []string{"plan", "$path"}); err != nil {
		t.Fatal(err)
	}
	def, err := db.flatViewDefinition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "attr_plan") || strings.Contains(def, "attr_path") {
		t.Fatalf("v_events_flat = %s, want attr_plan and no attr_path", def)
	}
}
```

Run: `go test ./internal/store/... ./internal/manage/ -run 'SystemDimensions|DeclaredSystemKeys|DeclaringSystemKeys|LegacyRefused|FlatViewSkips' -v`
Expected: FAIL (undefined `store.DeclarableAttributeKeys`, missing rows).

- [ ] **Step 2: The key lists in `internal/store/store.go`**

```go
// SystemAttribute is a reserved key that product_attributes always breaks
// down, and the events column it reads.
type SystemAttribute struct{ Key, Column string }

// SystemAttributes are rolled up for every project, declared or not:
// low-cardinality environment keys. The v_product_attrs live half
// (020_one_events_table.sql) carries one arm per entry.
var SystemAttributes = []SystemAttribute{
	{"$platform", "platform"},
	{"$os", "os"},
	{"$app_version", "app_version"},
	{"$app_locale", "app_locale"},
	{"$kind", "kind"},
	{"$browser", "browser"},
	{"$device", "device"},
	{"$browser_locale", "browser_locale"},
}

// DeclarableAttributes are the reserved keys a project may declare in its
// attributes to get a per-value breakdown, mapped to the events column
// each reads. Any other $ key is refused (manage.ErrInvalid): always-on
// ones need no declaration, and identity, session, consent, os_name and
// display sizes are unbounded or not breakdowns.
var DeclarableAttributes = map[string]string{
	"$host":            "host",
	"$path":            "path",
	"$referrer":        "referrer_source",
	"$utm_source":      "utm_source",
	"$utm_medium":      "utm_medium",
	"$utm_campaign":    "utm_campaign",
	"$os_version":      "os_version",
	"$browser_version": "browser_version",
	"$device_model":    "device_model",
}

// DeclarableAttributeKeys lists DeclarableAttributes' keys, sorted.
func DeclarableAttributeKeys() []string {
	keys := make([]string, 0, len(DeclarableAttributes))
	for k := range DeclarableAttributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
```

Add `"sort"` to the imports.

- [ ] **Step 3: The rollup in `internal/store/sqlite/aggregate_product.go`**

Delete `systemDims`. The system loop ranges over `store.SystemAttributes` (`dim.Column`, `dim.Key`). In the declared-key loop, branch on the `$` prefix:

```go
		for _, key := range attrs {
			expr, present := `json_extract(attributes, :path)`, `json_extract(attributes, :path) IS NOT NULL`
			if strings.HasPrefix(key, "$") {
				// A declared reserved key reads its column; the blob never
				// holds $ keys. One the store does not map (a declaration
				// manage now refuses) has nothing to break down.
				col, ok := store.DeclarableAttributes[key]
				if !ok {
					continue
				}
				expr, present = col, col+` <> ''`
			}
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", event), sql.Named("key", key),
				sql.Named("path", attrPath(key)), sql.Named("n", topN),
			}
			if err := d.rollupAttrValue(ctx, tx, expr, present, named); err != nil {
				return fmt.Errorf("attr %s/%s: %w", event, key, err)
			}
		}
```

Import `internal/store` if the file doesn't already. Rewrite the comment above the system loop to name the eight keys and point at `store.SystemAttributes`.

- [ ] **Step 4: Replace the generated `v_product_attrs` in migration 020**

In `020_one_events_table.sql`, replace the whole generated `CREATE VIEW v_product_attrs AS …;` statement with:

```sql
-- v_product_attrs: 016's shape over raw_product, with an arm per
-- store.SystemAttributes entry (eight) and declared $ keys read from their
-- column (store.DeclarableAttributes). The CASE and the Go map must list
-- the same keys; TestProductAttrsDeclaredSystemKeysAcrossBoundary fails on
-- any key one of them misses. NULLIF turns an empty column into "absent",
-- the same meaning json_extract's NULL has for a custom key.
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
declared_vals AS (
  SELECT e.project_id AS project_id, e.day AS day,
         e.event_name AS event_name, d.attr_key AS attr_key,
         CASE d.attr_key
           WHEN '$host'            THEN NULLIF(e.host, '')
           WHEN '$path'            THEN NULLIF(e.path, '')
           WHEN '$referrer'        THEN NULLIF(e.referrer_source, '')
           WHEN '$utm_source'      THEN NULLIF(e.utm_source, '')
           WHEN '$utm_medium'      THEN NULLIF(e.utm_medium, '')
           WHEN '$utm_campaign'    THEN NULLIF(e.utm_campaign, '')
           WHEN '$os_version'      THEN NULLIF(e.os_version, '')
           WHEN '$browser_version' THEN NULLIF(e.browser_version, '')
           WHEN '$device_model'    THEN NULLIF(e.device_model, '')
           ELSE CASE WHEN d.attr_key LIKE '$%' THEN NULL
                     ELSE json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') END
         END AS attr_value,
         e.actor_id AS actor_id, e.group_id AS group_id
  FROM raw_product e
  JOIN declared d ON d.project_id = e.project_id
),
vals AS (
  SELECT project_id, day, event_name, attr_key, attr_value, actor_id, group_id
  FROM declared_vals WHERE attr_value IS NOT NULL
  UNION ALL
  SELECT project_id, day, event_name, '$platform', platform, actor_id, group_id FROM raw_product WHERE platform <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$os', os, actor_id, group_id FROM raw_product WHERE os <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$app_version', app_version, actor_id, group_id FROM raw_product WHERE app_version <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$app_locale', app_locale, actor_id, group_id FROM raw_product WHERE app_locale <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$kind', kind, actor_id, group_id FROM raw_product WHERE kind <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$browser', browser, actor_id, group_id FROM raw_product WHERE browser <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$device', device, actor_id, group_id FROM raw_product WHERE device <> ''
  UNION ALL
  SELECT project_id, day, event_name, '$browser_locale', browser_locale, actor_id, group_id FROM raw_product WHERE browser_locale <> ''
),
counted AS (
  SELECT project_id, day, event_name, attr_key, attr_value,
         COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
         COUNT(DISTINCT NULLIF(group_id,'')) AS g
  FROM vals
  GROUP BY project_id, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT project_id, day, event_name, attr_key, attr_value, c, u, g,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, attr_key
                            ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups
FROM agg_product_attrs
UNION ALL
SELECT project_id, day, event_name, attr_key, attr_value, c, u, g
FROM ranked
WHERE rn <= (SELECT n FROM cap)
UNION ALL
SELECT v.project_id, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id), COUNT(DISTINCT NULLIF(v.group_id,''))
FROM vals v
WHERE NOT EXISTS (
  SELECT 1 FROM ranked r
  WHERE r.project_id = v.project_id AND r.day = v.day
    AND r.event_name = v.event_name AND r.attr_key = v.attr_key
    AND r.attr_value = v.attr_value
    AND r.rn <= (SELECT n FROM cap))
GROUP BY v.project_id, v.day, v.event_name, v.attr_key;
```

`TestMigration020KeepsEveryViewsAnswer` still passes. Its 019 fixture has no product row with kind, browser, device or browser locale; those columns are empty after the copy, so the four new arms add no rows.

- [ ] **Step 5: Flat view skips `$` keys**

In `RebuildFlatView`'s loop, before sanitizing:

```go
		if strings.HasPrefix(key, "$") {
			continue // a reserved key is a base column of events, never in the blob
		}
```

- [ ] **Step 6: Refuse undeclarable `$` keys in `internal/manage/ops.go`**

In `validate()`, after the origins loop:

```go
	for _, a := range sp.Attributes {
		if !strings.HasPrefix(a, "$") {
			continue
		}
		if _, ok := store.DeclarableAttributes[a]; !ok {
			return fmt.Errorf("%w: attribute %s cannot be declared; the reserved keys a project may declare are %s",
				ErrInvalid, a, strings.Join(store.DeclarableAttributeKeys(), ", "))
		}
	}
```

- [ ] **Step 7: API text and docs**

- `internal/api/ops_read.go`, the `product_attributes` description: replace "The system dimensions $platform, $os, $app_version and $app_locale are always included; a custom key only appears once the project declares it in attributes (see update_project)." with
  "The system dimensions $platform, $os, $app_version, $app_locale, $kind, $browser, $device and $browser_locale are always included. A custom key, or one of $host, $path, $referrer, $utm_source, $utm_medium, $utm_campaign, $os_version, $browser_version and $device_model, only appears once the project declares it in attributes (see update_project)."
- `internal/api/resources.go`, the `v_product_attrs` comment: the always-present list becomes `'$platform', '$os', '$app_version', '$app_locale', '$kind', '$browser', '$device', '$browser_locale'`.
- `docs/twillingate.md`, **Attribute breakdowns**: replace the sentences from "Never declare an unbounded key such as a URL or session id." through "extracts nothing." with:

  > Never declare an unbounded custom key such as a session id. `$platform`, `$os`, `$app_version`, `$app_locale`, `$kind`, `$browser`, `$device` and `$browser_locale` roll up automatically and need no declaration. Nine more reserved keys can be declared like a custom key to get the same per-value breakdown: `$host`, `$path`, `$referrer`, `$utm_source`, `$utm_medium`, `$utm_campaign`, `$os_version`, `$browser_version` and `$device_model`. The top-N cap keeps a declared `$path` bounded. Declaring any other `$` key is refused.

- The `product_attributes` row in the docs' tools table: same list change as the tool description.
- `deploy/UPGRADES.md`, 020 section, add to "What changes on the day":

  > - `product_attributes` always includes `$kind`, `$browser`, `$device` and `$browser_locale`. They are empty for product events stored before the upgrade, which never kept them.

- [ ] **Step 8: Verify**

```bash
export PATH=$PATH:/usr/local/go/bin
go test ./internal/store/... ./internal/manage/... ./internal/api/... 2>&1 | tail -10
make check
```

- [ ] **Step 9: Report.** Controller commits:
`feat: break product events down by browser, device, kind and locale, and by declared reserved keys`

---

### Task 4: Ingest stores every reserved key on both families

**Files:**
- Modify: `internal/server/handlers.go` (one row builder), `internal/server/ingest.go` (`mergeAttributes`, `resolveAttributes`, the canonical view name)
- Modify: `internal/server/server_test.go`, `internal/server/ingest_test.go`
- Modify: `docs/twillingate.md` (Declaring the environment, Attribute merge, Reserved attribute keys), `deploy/UPGRADES.md`

**Interfaces:**
- Consumes: `store.Event`, `store.FamilyViews`, `store.FamilyProduct`, `Enqueuer.Enqueue` (Task 1).
- Produces: no new exported names. `viewName(name) (kind string, ok bool)` is unchanged; add `canonicalViewName(name string) string`.

- [ ] **Step 1: Write the failing tests**

In `internal/server/server_test.go`:
- **`TestOSIsValidatedAndTheNamePreserved`**: the `signup` row's `$os_name` value becomes `"Haiku R1 beta"`. Assert `q.events[0].OS == "other"` and `q.events[0].OSName == "Haiku R1 beta"`. The expected warning count becomes 4, since the product event's `$os` now warns like a view's.
- **`TestBrowserAndDeviceAreValidated`**: replace its doc comment's second sentence with "Product events keep them too, validated the same way." Replace the final assertion with:

  ```go
	if len(q.events) != 1 || q.events[0].Browser != "safari" || q.events[0].Device != "mobile" || len(q.events[0].Attributes) != 0 {
		t.Errorf("product event = %+v, want safari/mobile stored as columns, not attributes", q.events)
	}
  ```

Add:

```go
// A product event keeps every reserved key a view keeps, normalised the
// same way, and a view keeps its custom attributes.
func TestProductEventsAndViewsKeepEverything(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$kind":"web","$platform":"web","$os":"macOS","$os_version":"14.2",
	    "$browser":"Chrome","$browser_version":"126","$browser_locale":"de-DE","$device":"desktop",
	    "$display_width":1440,"$display_height":900,"$session_id":"s9"},
	  "events":[
	    {"name":"signup","attributes":{"$host":"shop.example.com","$path":"/pricing","$referrer":"https://www.google.com/",
	      "$utm_source":"hn","plan":"pro"}},
	    {"name":"$page_view","attributes":{"$host":"shop.example.com","$path":"/","plan":"free"}},
	    {"name":"$pageview","attributes":{"$host":"shop.example.com","$path":"/legacy"}}
	  ]}`
	w := post(h, body, map[string]string{"Origin": testOrigin})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if len(q.events) != 1 || len(q.views) != 2 {
		t.Fatalf("events %d views %d", len(q.events), len(q.views))
	}
	e := q.events[0]
	if e.Family != store.FamilyProduct || e.EventName != "signup" || e.Kind != "web" || e.OS != "macos" ||
		e.OSVersion != "14.2" || e.Browser != "chrome" || e.BrowserVersion != "126" || e.BrowserLocale != "de-DE" ||
		e.Device != "desktop" || e.DisplayWidth != 1440 || e.DisplayHeight != 900 || e.SessionID != "s9" ||
		e.Host != "shop.example.com" || e.Path != "/pricing" || e.ReferrerSource != "google" || e.UTMSource != "hn" ||
		e.Country != "DE" || e.Attributes["plan"] != "pro" {
		t.Errorf("product event = %+v", e)
	}
	if v := q.views[0]; v.EventName != "$page_view" || v.Attributes["plan"] != "free" {
		t.Errorf("view = %+v, want its custom attribute kept", v)
	}
	if v := q.views[1]; v.EventName != "$page_view" || v.Path != "/legacy" {
		t.Errorf("legacy $pageview stored as %q, want the canonical $page_view", v.EventName)
	}
}

// An event-level null removes the batch default for that event: a
// reserved key reads as undeclared, a custom key is absent, not "".
func TestEventNullRemovesTheBatchDefault(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$browser":"chrome","plan":"pro"},
	  "events":[{"name":"signup","attributes":{"$browser":null,"plan":null}},{"name":"signup"}]}`
	post(h, body, map[string]string{"Origin": testOrigin})
	if len(q.events) != 2 {
		t.Fatalf("events = %+v", q.events)
	}
	if q.events[0].Browser != "unknown" {
		t.Errorf("nulled $browser = %q, want unknown (undeclared)", q.events[0].Browser)
	}
	if _, ok := q.events[0].Attributes["plan"]; ok {
		t.Errorf("nulled plan stored as %q, want absent", q.events[0].Attributes["plan"])
	}
	if q.events[1].Browser != "chrome" || q.events[1].Attributes["plan"] != "pro" {
		t.Errorf("second event lost the batch defaults: %+v", q.events[1])
	}
}
```

The Origin header, `testKey`, `post` and country `DE` follow `TestRoutesViewsAndCustom`; mirror whatever that test uses if the geo stub differs.

In `internal/server/ingest_test.go`, `TestResolveAttributesSplitsReservedFromCustom`: the `"nothing": nil` input now yields no custom key. Change `r.Custom["nothing"] != ""` to

```go
		func() bool { _, ok := r.Custom["nothing"]; return ok }()
```

keeping the `||` chain, and update the comment above it to "bool round-trips; nil is absent".

Run: `go test ./internal/server/ -run 'KeepEverything|EventNull|OSIsValidated|BrowserAndDevice|ResolveAttributesSplits' -v`
Expected: FAIL.

- [ ] **Step 2: Null semantics in `internal/server/ingest.go`**

```go
// mergeAttributes layers per-event attributes over batch defaults, key by
// key. This is the only merge rule, and it applies to system and ordinary
// keys alike — which is what lets an offline queue spanning an app
// self-update stamp $app_version on just the events that differ, instead of
// grouping the queue by context before flushing. A null means "not sent":
// a null batch value is skipped, and a null event value removes the batch
// default for that event.
//
// Neither input is mutated: batch defaults are reused across every event.
func mergeAttributes(batch, event map[string]any) map[string]any {
	out := make(map[string]any, len(batch)+len(event))
	for k, v := range batch {
		if v != nil {
			out[k] = v
		}
	}
	for k, v := range event {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}
```

In `resolveAttributes`, skip nil values at the top of the loop (`if v == nil { continue }`), so direct callers get the same answer.

Add beside `viewName`:

```go
// canonicalViewName is the name a view is stored under: the legacy
// $pageview spelling is stored as $page_view, so one view is one name.
func canonicalViewName(name string) string {
	if name == aliasPageview {
		return namePageView
	}
	return name
}
```

- [ ] **Step 3: One row builder in `internal/server/handlers.go`**

Replace everything from `defaultKind, isView := viewName(ev.Name)` through the view branch's `res.Accepted++` with:

```go
		defaultKind, isView := viewName(ev.Name)
		family, name := store.FamilyProduct, ev.Name
		if isView {
			family, name = store.FamilyViews, canonicalViewName(ev.Name)
		} else if strings.HasPrefix(ev.Name, "$") {
			res.warn(i, "unknown reserved name %s, stored as a custom event", ev.Name)
		}
		// A view's kind defaults from its name; a product event has no
		// default and keeps an empty kind unless it declares one.
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
		if isView && path == "" {
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
		// Hoisted out of the literal below so the warning order is
		// explicit: $os, $platform, then $browser and $device.
		browser := res.declared(i, "$browser", rv.Browser, enrich.NormalizeBrowser)
		device := res.declared(i, "$device", rv.Device, enrich.NormalizeDevice)
		e := store.Event{
			ID: id, ProjectID: p.ID, Family: family, EventName: name,
			TS: ts, ReceivedAt: received, Kind: kind,
			ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group, SessionID: rv.SessionID,
			Host: rv.Host, Path: path,
			UTMSource: rv.UTMSource, UTMMedium: rv.UTMMedium, UTMCampaign: rv.UTMCampaign,
			Platform: platform, OS: osv, OSVersion: rv.OSVersion, OSName: osName,
			Browser: browser, BrowserVersion: rv.BrowserVersion,
			Device: device, DeviceModel: rv.DeviceModel,
			AppVersion: rv.AppVersion, AppLocale: rv.AppLocale, BrowserLocale: rv.BrowserLocale,
			Country: country, Consent: consent, Attributes: rv.Custom,
		}
		// Bot filtering is the one thing still read off the User-Agent,
		// and it applies to web views only: any other kind declares what
		// it is and is never filtered, whatever HTTP library it uses.
		if kind == "web" {
			if isView && botUA {
				// Accepted and silently ignored: the client did nothing
				// wrong, so it must not retry.
				res.Accepted++
				continue
			}
			e.ReferrerSource = enrich.CleanReferrer(rv.Referrer, rv.Host)
		} else {
			// No host to compare against, so a referrer is taken at face
			// value — a deep link can still carry one.
			e.ReferrerSource = enrich.CleanReferrer(rv.Referrer, "")
		}
		var bad bool
		if e.DisplayWidth, bad = parseDisplay(rv.displayWidthRaw); bad {
			res.warn(i, "$display_width %q is not a positive integer, ignored", rv.displayWidthRaw)
		}
		if e.DisplayHeight, bad = parseDisplay(rv.displayHeightRaw); bad {
			res.warn(i, "$display_height %q is not a positive integer, ignored", rv.displayHeightRaw)
		}
		s.queue.Enqueue(e)
		noteRow(actorKind, rv)
		res.Accepted++
	}
```

Delete the comment "The environment is declared, validated and never parsed … the rest are views-only and are resolved and dropped on a product event." above the `osv` line. Replace it with: "The environment is declared, validated and never parsed: the User-Agent is read for nothing but the bot check below. Views and product events keep the same keys." Update `handleEvents`' doc comment: "It demultiplexes by event name: views and product events land in the one raw table under their family."

- [ ] **Step 4: Docs**

`docs/twillingate.md`:
- **Declaring the environment**: replace the sentence starting "Product events keep `$platform`, `$os`," through "is correct and cheap." with:

  > Views and product events store every one of these keys, validated the same way; the JS SDK sends them on every batch.

- **Attribute merge**: append:

  > A `null` means "not sent": a `null` batch value is ignored, and a `null` per-event value removes the batch value for that event, so a reserved key reads as undeclared and a custom key is absent.

- **Reserved attribute keys**: after the table, add:

  > Every key is stored on views and product events alike.

  The `$consent` paragraph's "(and a per-event `null` overrides a batch value to unknown, as the merge rule implies)" stays true; leave it.

`deploy/UPGRADES.md`, 020 section, "What changes on the day", add:

> - Product events keep every reserved key they are sent (`$browser`, `$device`, `$host`, `$path`, `$referrer`, UTM, display size, `$session_id`, country), and views keep their custom attributes. Rows stored before the upgrade have empty columns for what was dropped then.
> - A `null` per-event attribute now removes the batch value instead of storing `""`.

- [ ] **Step 5: Verify**

```bash
export PATH=$PATH:/usr/local/go/bin
go test ./internal/server/... ./internal/api/... 2>&1 | tail -10
make check
```

- [ ] **Step 6: Report.** Controller commits:
`feat(server): keep every reserved key on product events and custom keys on views`

---

### Task 5: SDK — location on events, `autoAttributes`, null families

**Files:**
- Modify: `sdk/src/twillingate.ts`
- Create: `sdk/src/auto.test.ts`
- Modify: `internal/server/twillingate.js` (regenerated by `npm run build`)
- Modify: `docs/twillingate.md` (`init()` sample, code-only list, Precedence paragraph), `internal/api/docs_sync_test.go` (`autoAttributes`), `deploy/UPGRADES.md`

**Interfaces:**
- Consumes: the server's null semantics (Task 4): a JSON `null` on an event removes the batch value.
- Produces: `InitOptions.autoAttributes?: boolean`, and the exported helper `expandNulls(layer)` (exported for tests).

- [ ] **Step 1: Write the failing tests**

Create `sdk/src/auto.test.ts` with the harness `api.test.ts` uses (copy its imports, `vi.mock("./origin", …)`, `sent`, `beforeEach` / `afterEach` and `drain()` verbatim), then:

```ts
function tg(opts: Partial<InitOptions> = {}): Twillingate {
  const t = new Twillingate();
  t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, ...opts });
  return t;
}

function only(): { batch: Record<string, unknown>; event: Record<string, unknown> } {
  expect(sent).toHaveLength(1);
  return { batch: sent[0].body.attributes, event: sent[0].body.events[0].attributes as Record<string, unknown> };
}

describe("location on product events", () => {
  it("track() carries the page's $host and $path on the web kind", async () => {
    history.replaceState(null, "", "/pricing?utm_source=hn");
    tg().track("signup");
    await drain();
    const { event } = only();
    expect(event.$host).toBe(location.hostname);
    expect(event.$path).toBe("/pricing");
    expect(event).not.toHaveProperty("$utm_source");
    expect(event).not.toHaveProperty("$referrer");
  });

  it("track() carries $screen on an app kind", async () => {
    history.replaceState(null, "", "/settings");
    tg({ kind: "app" }).track("export");
    await drain();
    const { event } = only();
    expect(event.$screen).toBe("/settings");
    expect(event).not.toHaveProperty("$host");
  });

  it("a mask that throws costs the event its location, not the event", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    tg({ maskUrl: () => { throw new Error("boom"); } }).track("signup", { plan: "pro" });
    await drain();
    const { event } = only();
    expect(event.plan).toBe("pro");
    expect(event).not.toHaveProperty("$path");
    expect(warn).toHaveBeenCalled();
  });

  it("the call's own $path wins over the derived one", async () => {
    history.replaceState(null, "", "/a");
    tg().track("signup", { $path: "/b" });
    await drain();
    expect(only().event.$path).toBe("/b");
  });
});

describe("autoAttributes: false", () => {
  it("sends nothing derived but keeps what a view needs, the flags and what the site set", async () => {
    history.replaceState(null, "", "/x?utm_source=hn");
    const t = tg({ autoAttributes: false, appVersion: "2.4.1", appLocale: "de" });
    t.attrs({ tier: "beta" });
    t.page();
    t.track("signup");
    await drain();
    const batch = sent[0].body.attributes;
    for (const k of ["$os", "$os_version", "$os_name", "$browser", "$browser_version", "$device", "$browser_locale"]) {
      expect(batch).not.toHaveProperty(k);
    }
    expect(batch).toMatchObject({ $kind: "web", $platform: "web", $app_version: "2.4.1", $app_locale: "de" });
    expect(batch).toHaveProperty("$consent");
    const [view, event] = sent[0].body.events.map((e) => e.attributes as Record<string, unknown>);
    expect(view).toMatchObject({ $path: "/x", tier: "beta" });
    for (const k of ["$referrer", "$utm_source", "$display_width"]) expect(view).not.toHaveProperty(k);
    expect(event).toEqual({ tier: "beta" });
  });
});

describe("null drops a key wherever it came from", () => {
  it("attrs({ $browser: null }) drops the whole $browser family from the batch", async () => {
    const t = tg();
    t.attrs({ $browser: null });
    t.track("signup");
    await drain();
    const { batch, event } = only();
    for (const k of ["$browser", "$browser_version", "$browser_locale"]) expect(batch).not.toHaveProperty(k);
    expect(batch).toHaveProperty("$os");
    expect(event.$browser).toBeNull();
    expect(event.$browser_locale).toBeNull();
  });

  it("a per-call $os: null sends null on that event only", async () => {
    const t = tg();
    t.track("a", { $os: null });
    t.track("b");
    await drain();
    const [a, b] = sent[0].body.events.map((e) => e.attributes as Record<string, unknown>);
    expect(a.$os).toBeNull();
    expect(a.$os_version === undefined || a.$os_version === null).toBe(true);
    expect(b).not.toHaveProperty("$os");
    expect(sent[0].body.attributes).toHaveProperty("$os");
  });

  it("a per-call $utm: null drops all three campaign keys from a view", async () => {
    history.replaceState(null, "", "/landing?utm_source=hn&utm_medium=social&utm_campaign=launch");
    tg().page({ $utm: null });
    await drain();
    const { event } = only();
    for (const k of ["$utm_source", "$utm_medium", "$utm_campaign"]) expect(event).not.toHaveProperty(k);
  });

  it("a later explicit value beats an earlier family null", async () => {
    const t = tg();
    t.attrs({ $utm: null });
    t.track("signup", { $utm_source: "mail" });
    await drain();
    expect(only().event.$utm_source).toBe("mail");
  });

  it("a null on a custom key never expands", async () => {
    const t = tg();
    t.attrs({ plan: null, plan_tier: "gold" });
    t.track("signup");
    await drain();
    expect(only().event).toEqual(expect.objectContaining({ plan_tier: "gold" }));
  });
});
```

`page(attrs)` with a plain object records the current page with extra attributes (see `api.test.ts`, "page(attrs) still treats an object as extra attributes").

Run: `cd sdk && npx vitest run src/auto.test.ts`
Expected: FAIL.

- [ ] **Step 2: Implement in `sdk/src/twillingate.ts`**

Near the other module constants:

```ts
/** Every reserved attribute key: a `$x: null` family expands over these. */
const RESERVED_KEYS = [
  "$install_id", "$user_id", "$user_name", "$group_id", "$group_name", "$session_id", "$consent",
  "$kind", "$platform", "$os", "$os_version", "$os_name", "$browser", "$browser_version", "$browser_locale",
  "$device", "$device_model", "$app_version", "$app_locale", "$display_width", "$display_height",
  "$host", "$path", "$screen", "$utm_source", "$utm_medium", "$utm_campaign", "$referrer",
];

/** Keys batchAttributes() can set: a null for one of these has to reach the wire. */
const BATCH_KEYS = new Set([
  "$user_id", "$user_name", "$install_id", "$group_id", "$group_name", "$kind", "$consent", "$platform",
  "$os", "$os_version", "$os_name", "$browser", "$browser_version", "$device",
  "$app_version", "$app_locale", "$browser_locale",
]);

/**
 * Expand every `$x: null` in one layer into a null for `$x` and each
 * reserved `$x_*` key, so a family null drops the whole family. Expanding
 * per layer, before layers merge, keeps precedence: a later layer's
 * explicit value still beats an earlier family null. A key the same layer
 * sets explicitly keeps its value; custom keys never expand.
 */
export function expandNulls(layer: Record<string, unknown> | undefined | null): Record<string, unknown> {
  if (!layer) return {};
  const out: Record<string, unknown> = { ...layer };
  for (const [key, value] of Object.entries(layer)) {
    if (value !== null || !key.startsWith("$")) continue;
    for (const r of RESERVED_KEYS) {
      if ((r === key || r.startsWith(key + "_")) && !(r in layer)) out[r] = null;
    }
  }
  return out;
}
```

In `InitOptions`, after `appLocale`:

```ts
  /**
   * Send what the SDK derives on its own: OS, browser and device detection,
   * the browser locale, display size, referrer and campaign on views, and
   * the page's location on product events. Default true. false sends only
   * what the site sets, plus what a view needs to exist, $kind, $platform
   * for the web kind, $consent and identity.
   */
  autoAttributes?: boolean;
```

Class field beside `appLocale`: `private autoAttributes = true;`.

In `init()`, replace the unconditional `primePlatformVersion();` with:

```ts
    this.autoAttributes = opts.autoAttributes !== false;
    // The one async detection input; read at flush time, not awaited.
    if (this.autoAttributes) primePlatformVersion();
```

In `page()`, build the derived layer conditionally and expand the two caller layers:

```ts
    // derived < attrs() defaults < the call's own attributes
    const derived: Record<string, unknown> = { $host: host, $path: path };
    if (this.autoAttributes) Object.assign(derived, { $referrer: referrer }, utm, displaySize());
    let attributes: Record<string, unknown> = {
      ...derived, ...expandNulls(this.defaultAttrs), ...expandNulls(attrs),
    };
```

In the same method's listener loop: `attributes = { ...attributes, ...expandNulls(r) };`.

In `screen()`:

```ts
    this.emit("$screen_view", {
      ...(this.autoAttributes ? displaySize() : {}),
      ...expandNulls(this.defaultAttrs), $screen: String(name), ...expandNulls(attrs),
    });
```

`track()`:

```ts
  /** Opt-in product event, carrying where it happened unless autoAttributes is false. */
  track(name: string, attrs?: Record<string, unknown>): void {
    if (!this.ready) return this.hold(() => this.track(name, attrs));
    if (!this.live() || !name) return;
    this.emit(String(name), { ...this.eventContext(), ...expandNulls(this.defaultAttrs), ...expandNulls(attrs) });
  }

  // Where a product event happened: the host and path (or, on a non-web
  // kind, the screen) a view of this page would carry, masked the same
  // way, plus the display size. A mask that throws or returns junk costs
  // the event its location, never the event itself.
  private eventContext(): Record<string, unknown> {
    if (!this.autoAttributes || typeof location === "undefined") return {};
    const out: Record<string, unknown> = { ...displaySize() };
    let masked: unknown = location.href;
    if (this.mask) {
      try {
        masked = this.mask(location.href);
      } catch (e) {
        console.warn("twillingate: mask threw, sending the event without its location", e);
        return out;
      }
      if (typeof masked !== "string") {
        console.warn("twillingate: mask returned a non-string, sending the event without its location");
        return out;
      }
    }
    const split = splitLocation(masked as string, this.routing);
    if (!split) return out;
    if (this.kind === "web") {
      out.$host = split.host;
      out.$path = split.path;
    } else {
      out.$screen = split.path;
    }
    return out;
  }
```

In `emit()`, the listener merge becomes `if (r && typeof r === "object") merged = { ...merged, ...expandNulls(r) };`. The final pass becomes:

```ts
    // null drops a key. A batch attribute cannot be dropped by leaving it
    // out of the event, so its null goes on the wire, where the collector
    // reads it as "not sent" for this event.
    const attributes: Record<string, unknown> = {};
    for (const key of Object.keys(merged)) {
      const v = merged[key];
      if (v === null) {
        if (BATCH_KEYS.has(key)) attributes[key] = null;
        continue;
      }
      if (v !== undefined) attributes[key] = v;
    }
```

In `batchAttributes()`, gate detection and the browser locale, then drop what the defaults null out:

```ts
    if (this.autoAttributes) {
      // Detection runs per flush and is the only source of the environment.
      const d = detectAll();
      a.$os = d.os;
      if (d.osVersion) a.$os_version = d.osVersion;
      if (d.osName) a.$os_name = d.osName;
      a.$browser = d.browser;
      if (d.browserVersion) a.$browser_version = d.browserVersion;
      a.$device = d.device;
      if (typeof navigator !== "undefined" && navigator.language) a.$browser_locale = navigator.language;
    }
    if (this.appVersion) a.$app_version = this.appVersion;
    if (this.appLocale) a.$app_locale = this.appLocale;
    // A key the attrs() defaults null out, family included, is not sent.
    const nulled = expandNulls(this.defaultAttrs);
    for (const key of Object.keys(a)) if (nulled[key] === null) delete a[key];
    return a;
```

Tagged elements and anything else that calls `track()` pick up the location automatically; no other call site changes.

- [ ] **Step 3: Run the SDK suite, typecheck, rebuild**

```bash
cd sdk && npx vitest run && npx tsc --noEmit -p . && npm run build && cd ..
```

Expected: all tests pass (the existing 258 plus the new ones); `built ../internal/server/twillingate.js`.

If an existing test asserted that `track()` sends no `$host` / `$path` / display size (grep `sdk/src/*.test.ts` for `toEqual(` on a tracked event's attributes), update its expectation to include them, or construct that instance with `autoAttributes: false` when the test is about something else.

- [ ] **Step 4: Docs and the sync test**

`docs/twillingate.md`:
- **From code** `init()` sample, after `appLocale`:

  ```js
    autoAttributes: true,        // default; false sends only what you set (see Precedence)
  ```

- The code-only list: "`flushInterval`, `platform`, `appVersion`, `appLocale`, `storage`, …" gains `autoAttributes` after `appLocale`.
- Replace the **Precedence** paragraph with:

  > **Precedence**: SDK-derived values (`$host`, `$path`, `$referrer`, campaign parameters and display size on views; the page's `$host` and `$path`, or `$screen` on a non-web kind, and display size on product events), then `attrs()` defaults, then the call's attributes, then listener returns in registration order. Later layers win, and a `null` drops the key wherever it came from: `twillingate.attrs({ $host: "selfhosted_ab12", $referrer: null })` sends that host and no referrer. A batch attribute (`$os`, `$browser`, …) set to `null` is sent as `null` on the event, which the collector reads as not sent. A `null` on a `$` prefix drops the family: `$utm: null` drops `$utm_source`, `$utm_medium` and `$utm_campaign`, and `$browser: null` drops `$browser`, `$browser_version` and `$browser_locale`. A later layer's explicit value still beats an earlier family `null`. `autoAttributes: false` sends none of the derived values except what a view needs to exist (`$host` and `$path`, or `$screen`), plus `$kind`, `$platform` for the web kind, `$consent` and identity.

- **Transport** or **Views**: nothing to change.

`internal/api/docs_sync_test.go`: add `"autoAttributes"` to the SDK symbol list in `TestDocumentMatchesSDK`.

`deploy/UPGRADES.md`, 020 section, add:

> - The served SDK adds the page's `$host` and `$path` (masked like a view's) to every product event, and accepts `autoAttributes: false` and `null` families. Pages on the cached old SDK send events without location for up to a day; purge the CDN copy of `/js/twillingate.js` if one sits in front of the collector.

- [ ] **Step 5: Verify**

```bash
export PATH=$PATH:/usr/local/go/bin
go test ./internal/api/ ./internal/server/ 2>&1 | tail -5
make check
```

- [ ] **Step 6: Report.** Controller commits:
`feat(sdk): send location on product events and add autoAttributes and null families`

---

### Task 6: Measure against the baseline and verify the whole branch

**Files:** none changed, unless the stopping rule fires (see Step 2).

- [ ] **Step 1: Benchmarks after**

```bash
export PATH=$PATH:/usr/local/go/bin
go test ./internal/store/sqlite/ -run '^$' -bench 'LiveHalf' -benchtime 5x -count 3 | tee /tmp/bench-020.txt
```

Compare each benchmark's median against Task 1's baseline and record both in the report as a table.

- [ ] **Step 2: Apply the stopping rule**

- **If every live half is within 15% of its baseline, or faster:** continue. Known and accepted: `BenchmarkIdentityDailyLiveHalf` is slower by the margin the prototype showed (about +55%, 75 → 117 ms there). It does not trigger the rule, but report its number.
- **If one is slower:**
  1. Try index changes first: column order, or adding a covered column. Re-run.
  2. If that doesn't win it back, stop and report to the controller with the numbers. The spec says the fallback is two raw tables built from one column list, and that is the user's call; do not implement it unasked.

- [ ] **Step 3: Whole-branch checks**

```bash
make check
tmp=$(mktemp -d); go build -o $tmp/tg ./cmd/twillingate
DATABASE_DSN="sqlite://$tmp/t.db" $tmp/tg migrate && DATABASE_DSN="sqlite://$tmp/t.db" $tmp/tg project create -name app
python3 scripts/seed-demo.py $tmp/t.db && rm -rf scripts/__pycache__
grep -rn --include=*.go -E '\bWriteViews\b|\bWriteProductEvents\b|store\.View\b|store\.ProductEvent\b|EnqueueView|EnqueueEvent' . ; echo "leftovers: $?"   # expect exit 1 (none)
```

- [ ] **Step 4: Report** the benchmark table and the check results. The controller runs the final whole-branch review, then opens the PR.

---

## Self-review notes

- **Spec coverage:**

  | spec decision | task |
  | --- | --- |
  | 1–5, migration steps 1–6 | Task 2 |
  | 6 (one row type) | Task 1 |
  | 7 and 8 (store everything, view attributes) | Task 4 |
  | 9–12 (SDK) | Task 5 |
  | 13 (null on the wire) | Task 4 server, Task 5 client |
  | 14 (product attributes, declarable keys, refusals) | Task 3 |
  | Docs and UPGRADES | the task that owns each behaviour |
  | Tests | each task; the bench in Tasks 1 and 6 |
  | Stopping rule | Task 6 |

- **Index:** the spec's two indexes are replaced by one, measured. This is flagged above for the user.
- **`v_events_flat`:** it reads `events` directly on purpose. The read-path test exempts it and `flatview.go` by name.
