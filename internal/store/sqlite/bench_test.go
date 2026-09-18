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
const benchProject = "bench"

// seedBenchViews writes 30 days x 5,000 views (150,000 rows) for
// benchProject in batches of 5,000 (one WriteViews call per day), spread
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
		batch := make([]store.View, perDay)
		for i := 0; i < perDay; i++ {
			ts := dayStart.Add(time.Duration(i) * (24 * time.Hour / perDay))
			batch[i] = store.View{
				ID:         fmt.Sprintf("bench-%02d-%05d", d, i),
				Project:    benchProject,
				TS:         ts,
				ReceivedAt: ts,
				Kind:       "web",
				ActorID:    fmt.Sprintf("actor-%04d", i%numActors),
				ActorKind:  store.ActorConnection,
				Path:       fmt.Sprintf("/path-%03d", i%numPaths),
			}
		}
		if err := db.WriteViews(ctx, batch); err != nil {
			b.Fatalf("seed day %d: %v", d, err)
		}
	}
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
			WHERE project = ? AND day BETWEEN ? AND ?
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
			WHERE project = ? AND day BETWEEN ? AND ?
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
