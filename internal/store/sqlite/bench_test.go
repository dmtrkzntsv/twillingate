package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// benchProject is seeded fresh for each benchmark function (never
// aggregated), so every row stays in the raw `views` table and the
// v_views_* live halves have to scan the whole window on every query —
// exactly the cost the low-resource retention guidance in
// docs/deployment.md is about.
const benchProject int64 = 1

// seedBenchViews writes 30 days x 5,000 views (150,000 rows) for
// benchProject in batches of 5,000 (one WriteEvents call per day), spread
// over ~200 distinct paths and ~2,000 distinct actors.
func seedBenchViews(b *testing.B, db *DB) {
	b.Helper()
	ctx := context.Background()
	const (
		days      = 30
		perDay    = 5000
		numPaths  = 200
		numActors = 2000
	)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for d := 0; d < days; d++ {
		dayStart := start.AddDate(0, 0, d)
		batch := make([]store.Event, perDay)
		for i := 0; i < perDay; i++ {
			ts := dayStart.Add(time.Duration(i) * (24 * time.Hour / perDay))
			batch[i] = store.Event{Family: store.FamilyViews,
				ID:         fmt.Sprintf("bench-%02d-%05d", d, i),
				ProjectID:  benchProject,
				TS:         ts,
				ReceivedAt: ts,
				Kind:       "web",
				ActorID:    fmt.Sprintf("actor-%04d", i%numActors),
				ActorKind:  store.ActorConnection,
				Path:       fmt.Sprintf("/path-%03d", i%numPaths),
			}
		}
		if err := db.WriteEvents(ctx, batch); err != nil {
			b.Fatalf("seed day %d: %v", d, err)
		}
	}
}

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
		batch := make([]store.Event, perDay)
		for i := 0; i < perDay; i++ {
			ts := dayStart.Add(time.Duration(i) * (24 * time.Hour / perDay))
			batch[i] = store.Event{Family: store.FamilyProduct,
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
		if err := db.WriteEvents(ctx, batch); err != nil {
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

// BenchmarkEventsFileSize reports the on-disk size of the database file
// after the same 150k views + 7.5k product events fixture the live-half
// benchmarks use, once checkpointed out of the WAL: the metric the
// clustering migration (023) exists for. b.N is not the point here — the
// loop body does no work after the first iteration's setup, so the
// reported ns/op is meaningless and only the MB metric matters (run with
// -benchtime=1x or a small Nx).
func BenchmarkEventsFileSize(b *testing.B) {
	db := setupBenchDB(b)
	seedBenchViews(b, db)
	seedBenchEvents(b, db)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		b.Fatal(err)
	}
	var pageCount, pageSize int64
	if err := db.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		b.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(pageCount*pageSize)/1e6, "MB")
}

func setupBenchDB(b *testing.B) *DB {
	b.Helper()
	db, err := openAt(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		b.Fatal(err)
	}
	// Registered, as every project with events is: the attribute views
	// read each project's keys from its row.
	id, err := db.CreateProject(ctx, store.RegistryProject{
		Name: "bench", AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "bench", Action: "project.create"})
	if err != nil {
		b.Fatal(err)
	}
	if id != benchProject {
		b.Fatalf("bench project id = %d, want %d", id, benchProject)
	}
	return db
}

// BenchmarkViewsPathsLiveHalf measures a top-paths breakdown over a 7-day
// range of v_views_paths when none of the underlying days have been
// aggregated: the whole query runs against the live half, over the raw
// rows of the days in the range.
func BenchmarkViewsPathsLiveHalf(b *testing.B) {
	db := setupBenchDB(b)
	seedBenchViews(b, db)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.db.QueryContext(ctx, `
			SELECT path, SUM(visitors), SUM(views) FROM v_views_paths
			WHERE project_id = ? AND day BETWEEN ? AND ?
			GROUP BY path ORDER BY 2 DESC LIMIT 20`,
			benchProject, "2026-08-01", "2026-08-07")
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var path string
			var visitors, views int
			if err := rows.Scan(&path, &visitors, &views); err != nil {
				rows.Close()
				b.Fatal(err)
			}
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

// BenchmarkViewsDailyLiveHalf runs the same 7-day breakdown shape against
// v_views_daily (grouped by kind, the only breakdown dimension that view
// has) for comparison: it additionally computes sessions and bounces via a
// LAG window over every actor's rows, so it is expected to cost more than
// the paths breakdown above.
func BenchmarkViewsDailyLiveHalf(b *testing.B) {
	db := setupBenchDB(b)
	seedBenchViews(b, db)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.db.QueryContext(ctx, `
			SELECT kind, SUM(visitors), SUM(views) FROM v_views_daily
			WHERE project_id = ? AND day BETWEEN ? AND ?
			GROUP BY kind ORDER BY 2 DESC LIMIT 20`,
			benchProject, "2026-08-01", "2026-08-07")
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var kind string
			var visitors, views int
			if err := rows.Scan(&kind, &visitors, &views); err != nil {
				rows.Close()
				b.Fatal(err)
			}
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

// BenchmarkWriteEvents writes 500-event product batches, the most one
// request carries (wire.MaxBatchEvents), with 0, 5 and 20 custom
// attributes per event, into a fresh database per sub-benchmark. Event ids
// are unique across iterations so every row is inserted, never ignored.
// It guards the write path's cost: received_attributes counting (spec D3)
// may add at most 10% to attrs=5.
func BenchmarkWriteEvents(b *testing.B) {
	for _, n := range []int{0, 5, 20} {
		b.Run(fmt.Sprintf("attrs=%d", n), func(b *testing.B) {
			db := setupBenchDB(b) // the file's helper: a fresh migrated database
			ctx := context.Background()
			start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
			batch := make([]store.Event, 500)
			b.ReportAllocs()
			b.ResetTimer()
			for it := 0; it < b.N; it++ {
				for i := range batch {
					attrs := make(map[string]string, n)
					for k := 0; k < n; k++ {
						attrs[fmt.Sprintf("key%02d", k)] = fmt.Sprintf("v%d", (i+k)%7)
					}
					batch[i] = store.Event{Family: store.FamilyProduct,
						ID: fmt.Sprintf("w-%d-%03d", it, i), ProjectID: benchProject,
						EventName: fmt.Sprintf("event-%d", i%5), TS: start, ReceivedAt: start,
						ActorID: fmt.Sprintf("actor-%03d", i%200), ActorKind: store.ActorUser,
						Path: fmt.Sprintf("/p/%d", i%20), Attributes: attrs}
				}
				if err := db.WriteEvents(ctx, batch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkCountReceived runs the nightly received-attributes recount
// (countReceived, inside MeasureServerStats' transaction, while ingest
// waits) over a raw window of 30 days x 2,000 web product events as the SDK
// sends them (5 custom attributes, host, path, OS and browser versions, and
// the other environment columns that widen a row), plus 30 days x 200
// measure samples. It guards the one-pass shape of receivedSQL.
func BenchmarkCountReceived(b *testing.B) {
	db := setupBenchDB(b)
	ctx := context.Background()
	const (
		days     = 30
		perDay   = 2000
		measures = 200
	)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	boot := 120.0
	for d := 0; d < days; d++ {
		dayStart := start.AddDate(0, 0, d)
		batch := make([]store.Event, 0, perDay+measures)
		for i := 0; i < perDay; i++ {
			ts := dayStart.Add(time.Duration(i) * (24 * time.Hour / perDay))
			attrs := make(map[string]string, 5)
			for k := 0; k < 5; k++ {
				attrs[fmt.Sprintf("key%d", k)] = fmt.Sprintf("v%d", (i+k)%(7*(k+1)))
			}
			actor := fmt.Sprintf("%064x", i%500)
			batch = append(batch, store.Event{Family: store.FamilyProduct,
				ID: fmt.Sprintf("0192f1c4-%02d%02x-7c3e-9a51-%012x", d, i%256, i), ProjectID: benchProject,
				EventName: fmt.Sprintf("event-%d", i%5), TS: ts, ReceivedAt: ts,
				ActorID: actor, ActorKind: store.ActorUser, UserID: fmt.Sprintf("user-%04d", i%500),
				SessionID: fmt.Sprintf("0192f1c4-%04x-7c3e-9a51-0242ac120002", i%300), Host: "app.example.com",
				Path: fmt.Sprintf("/p/%d", i%50), Platform: "web", OS: "macos", OSVersion: []string{"14.5", "15.0"}[i%2], OSName: "macOS",
				Browser: "chrome", BrowserVersion: []string{"128.0", "129.0", "130.0"}[i%3],
				BrowserLocale: "en-US", Device: "desktop", DisplayWidth: 1512, DisplayHeight: 982,
				Country: "CA", AppVersion: "2.4.1", Attributes: attrs})
		}
		for i := 0; i < measures; i++ {
			ts := dayStart.Add(time.Duration(i) * (24 * time.Hour / measures))
			batch = append(batch, store.Event{Family: store.FamilyMeasures,
				ID: fmt.Sprintf("cm-%02d-%04d", d, i), ProjectID: benchProject,
				EventName: "boot", Measure: store.MeasureTime, Value: &boot, TS: ts, ReceivedAt: ts,
				ActorID: fmt.Sprintf("actor-%04d", i%500), ActorKind: store.ActorUser,
				DeviceModel: fmt.Sprintf("model-%d", i%9), Attributes: map[string]string{"key0": "x"}})
		}
		if err := db.WriteEvents(ctx, batch); err != nil {
			b.Fatalf("seed day %d: %v", d, err)
		}
	}
	today := start.AddDate(0, 0, days).Format("2006-01-02")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.tx(ctx, func(tx *sql.Tx) error { return countReceived(ctx, tx, today) }); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM received_attributes WHERE max_values IS NOT NULL`).Scan(&n); err != nil {
		b.Fatal(err)
	}
	if n != days*10 { // key0..key4, $host, $path, $os_version, $browser_version, $device_model
		b.Fatalf("recount wrote %d rows, want %d", n, days*10)
	}
}
