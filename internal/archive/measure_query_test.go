//go:build measure

// Questions over 3 years of files through the Reader (raw-first PR 4's
// "done when"; D10's targets: a 3-year question under 30 s, a timeline
// under 1 s, at 2 threads and 512 MB): go test -tags measure -run
// TestMeasureQueries -v -timeout 2h ./internal/archive. Not part of make
// check. QUERY_MONTHS (default 36), QUERY_MONTH_EVENTS (default 1000000)
// and QUERY_THREADS (default 2) override the defaults; QUERY_DIR keeps the
// generated files between runs (they are generated only when missing).
package archive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

func queryEnv(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

func TestMeasureQueries(t *testing.T) {
	months, perMonth, threads := queryEnv("QUERY_MONTHS", 36), queryEnv("QUERY_MONTH_EVENTS", 1_000_000), queryEnv("QUERY_THREADS", 2)
	data := os.Getenv("QUERY_DIR")
	if data == "" {
		data = t.TempDir()
	}
	raw := filepath.Join(data, "raw")
	if err := os.MkdirAll(filepath.Join(raw, "1"), 0o750); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{childEnv + "=1"}, AllowedDirs: []string{raw},
		SpillRoot: filepath.Join(data, "spill"), SlotDir: filepath.Join(data, "slots"), MaxChildren: 1,
		MemoryLimit: "512MB", Threads: threads})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx := context.Background()
	start := time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC)
	rare := (perMonth/2)/10*10 + 6 // a product row (i % 10 in 5..8) in month 17
	for m := 0; m < months; m++ {
		from := start.AddDate(0, m, 0)
		out := filepath.Join(raw, "1", from.Format("2006-01")+".parquet")
		if _, err := os.Stat(out); err == nil {
			continue
		}
		step := from.AddDate(0, 1, 0).Sub(from).Microseconds() / int64(perMonth)
		q := fmt.Sprintf(`COPY (SELECT *, CAST(ts AS DATE) AS day FROM (SELECT 1::BIGINT AS v, uuid()::VARCHAR AS id, 1::BIGINT AS project_id,
  CASE WHEN i %% 10 < 5 THEN 'views' WHEN i %% 10 < 9 THEN 'product' ELSE 'measures' END AS family,
  CASE WHEN i %% 10 < 5 THEN '$page_view' WHEN i %% 10 < 9 THEN (CASE WHEN %d = 17 AND i = %d THEN 'rare' ELSE 'event_' || (i %% 20) END) ELSE 'load' END AS event_name,
  TIMESTAMP '%s' + to_microseconds(i * %d) AS ts, TIMESTAMP '%s' + to_microseconds(i * %d + 1000000) AS received_at,
  'web' AS kind, 'a-' || (i * 7919 %% 200000) AS actor_id, 'connection' AS actor_kind,
  CASE WHEN i %% 3 = 0 THEN 'u-' || (i %% 50000) END AS user_id, 's-' || (i // 30) AS session_id,
  'www.example.com' AS host, '/p/' || (i %% 500) AS path, ['google', 'direct', 'twitter'][i %% 3 + 1] AS referrer_source,
  'web' AS platform, ['macos', 'windows', 'ios', 'android'][i %% 4 + 1] AS os, ['chrome', 'safari', 'firefox'][i %% 3 + 1] AS browser,
  ['DE', 'US', 'FR', 'GB'][i %% 4 + 1] AS country, 1::BIGINT AS consent,
  CASE WHEN i %% 10 = 9 THEN (i %% 1000)::DOUBLE END AS value, CASE WHEN i %% 10 = 9 THEN 'time' END AS measure, 1::DOUBLE AS sample_rate,
  CASE WHEN i %% 10 BETWEEN 5 AND 8 THEN ['free', 'pro', 'team'][i %% 3 + 1] END AS attr_plan,
  CASE WHEN i %% 10 BETWEEN 5 AND 8 THEN ['a', 'b'][i %% 2 + 1] END AS attr_variant,
  CASE WHEN i %% 10 BETWEEN 5 AND 8 THEN (i %% 7)::VARCHAR END AS attr_step,
  CASE WHEN i %% 10 BETWEEN 5 AND 8 THEN 'sku-' || (i %% 20000) END AS attr_item_id,
  CASE WHEN i %% 10 BETWEEN 5 AND 8 THEN 'category-' || (i %% 40) END AS attr_category,
  CASE WHEN i %% 10 BETWEEN 5 AND 8 THEN 'exp-' || (i %% 13) END AS attr_experiment,
  CASE WHEN i %% 10 BETWEEN 5 AND 8 THEN ['true', 'false'][i %% 2 + 1] END AS attr_logged_in,
  CASE WHEN i %% 10 BETWEEN 5 AND 8 THEN (i %% 300)::VARCHAR || '.' || (i %% 100)::VARCHAR END AS attr_cart_value
FROM range(%d) t(i)) ORDER BY family, ts) TO '%s' (FORMAT parquet, COMPRESSION zstd)`,
			m, rare, from.Format("2006-01-02 15:04:05"), step, from.Format("2006-01-02 15:04:05"), step, perMonth, out)
		began := time.Now()
		if err := eng.Exec(ctx, q); err != nil {
			t.Fatalf("month %d: %v", m, err)
		}
		t.Logf("generated %s in %v", filepath.Base(out), time.Since(began).Round(time.Millisecond))
	}
	r, err := NewReader(raw, eng)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, sql string
		max       time.Duration
	}{
		{"build (first query)", "SELECT count(*) FROM events", 30 * time.Second},
		{"views and visitors per day, 3 years", "SELECT day, count(*), count(DISTINCT actor_id) FROM views GROUP BY day", 30 * time.Second},
		{"distinct users, 3 years", "SELECT count(DISTINCT user_id) FROM product", 30 * time.Second},
		{"plan x variant", "SELECT attr_plan, attr_variant, count(*) FROM product GROUP BY ALL", 30 * time.Second},
		{"plan x country", "SELECT attr_plan, country, count(*) FROM product GROUP BY ALL", 30 * time.Second},
		{"events per name per month", "SELECT date_trunc('month', ts), event_name, count(*) FROM product GROUP BY ALL", 30 * time.Second},
		{"first rare event", "SELECT min(ts) FROM product WHERE event_name = 'rare'", 30 * time.Second},
		{"one user's timeline, 3 years", "SELECT ts, event_name FROM events WHERE user_id = 'u-123' ORDER BY ts", time.Second},
	} {
		began := time.Now()
		res, err := r.Query(ctx, c.sql, 0, Rules{})
		took := time.Since(began)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		t.Logf("%-40s %8v  %d rows", c.name, took.Round(time.Millisecond), len(res.Rows))
		if took > c.max {
			t.Errorf("%s took %v, over the %v target", c.name, took, c.max)
		}
	}
}
