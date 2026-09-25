package sqlite

import (
	"context"
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

func setupBenchDB(b *testing.B) *DB {
	b.Helper()
	db, err := openAt(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		b.Fatal(err)
	}
	return db
}

// BenchmarkViewsPathsLiveHalf measures a top-paths breakdown over a 7-day
// range of v_views_paths when none of the underlying days have been
// aggregated: the whole query runs against the live half, which windows
// over every row in the raw `views` table regardless of the day filter.
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
