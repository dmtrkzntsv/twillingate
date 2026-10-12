package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
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
// It guards the write path's cost: usage_attributes counting (spec D3)
// may add at most 10% to attrs=5. attrs=0/5/20 have no attribute rules:
// they may pay one rules query per batch and nothing per event (project
// attributes D9). attrs=5/rules adds 10 attribute rules, one of which
// renames key01, so every event's attributes are mapped.
func BenchmarkWriteEvents(b *testing.B) {
	for _, c := range []struct {
		attrs int
		rules bool
	}{{0, false}, {5, false}, {20, false}, {5, true}} {
		name := fmt.Sprintf("attrs=%d", c.attrs)
		if c.rules {
			name += "/rules"
		}
		b.Run(name, func(b *testing.B) {
			n := c.attrs
			db := setupBenchDB(b) // the file's helper: a fresh migrated database
			ctx := context.Background()
			if c.rules {
				seedBenchAttributeRules(b, db)
			}
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

// seedBenchAttributeRules gives benchProject 10 attribute rules: renames
// and blocks of keys BenchmarkWriteEvents never sends, and a rename of
// key01, which every event of attrs>=2 carries.
func seedBenchAttributeRules(b *testing.B, db *DB) {
	b.Helper()
	ctx := context.Background()
	a := store.AuditEntry{Actor: "bench", Action: "attribute.rename"}
	for _, r := range [][2]string{
		{"old_plan", "plan"}, {"tier", "plan"}, {"ver", "version"}, {"lang", "locale"}, {"key01", "key01_renamed"},
	} {
		if _, err := db.RenameAttribute(ctx, benchProject, r[0], r[1], false, a); err != nil {
			b.Fatal(err)
		}
	}
	for _, k := range []string{"email", "ssn", "phone", "ip", "token"} {
		if err := db.BlockAttribute(ctx, benchProject, k, a); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRenameAttribute times one RenameAttribute of key0, which every
// event of the BenchmarkCountReceived window carries (30 days x 2,000
// product events and 200 measure samples): the raw rewrite of 66,000 rows
// and the usage rows, in one transaction while ingest waits. Iterations
// rename key0 to keyX and back.
func BenchmarkRenameAttribute(b *testing.B) {
	db := setupBenchDB(b)
	seedReceivedWindow(b, db)
	ctx := context.Background()
	a := store.AuditEntry{Actor: "bench", Action: "attribute.rename"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		from, to := "key0", "keyX"
		if i%2 == 1 {
			from, to = to, from
		}
		got, err := db.RenameAttribute(ctx, benchProject, from, to, false, a)
		if err != nil {
			b.Fatal(err)
		}
		if got["events"] != 30*2200 {
			b.Fatalf("rewrote %d rows, want %d", got["events"], 30*2200)
		}
	}
}

// BenchmarkCountReceived runs the nightly usage_attributes recount
// (countReceived, inside RecordUsageHistory' transaction, while ingest
// waits) over a raw window of 30 days x 2,000 web product events as the SDK
// sends them (5 custom attributes, host, path, OS and browser versions, and
// the other environment columns that widen a row), plus 30 days x 200
// measure samples. It guards the one-pass shape of receivedSQL.
func BenchmarkCountReceived(b *testing.B) {
	db := setupBenchDB(b)
	ctx := context.Background()
	start, days := seedReceivedWindow(b, db)
	today := start.AddDate(0, 0, days).Format("2006-01-02")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.tx(ctx, func(tx *sql.Tx) error { return countReceived(ctx, tx, today) }); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM usage_attributes WHERE max_values IS NOT NULL`).Scan(&n); err != nil {
		b.Fatal(err)
	}
	if n != days*10 { // key0..key4, $host, $path, $os_version, $browser_version, $device_model
		b.Fatalf("recount wrote %d rows, want %d", n, days*10)
	}
}

// seedReceivedWindow writes 30 days x 2,000 web product events as the SDK
// sends them (5 custom attributes key0..key4, host, path, OS and browser
// versions, and the other environment columns that widen a row), plus 30
// days x 200 measure samples carrying key0, for benchProject. It returns
// the first day and the number of days.
func seedReceivedWindow(b *testing.B, db *DB) (time.Time, int) {
	b.Helper()
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
	return start, days
}

// BenchmarkCleanDictionaries times the nightly dictionary cleanup
// (CleanDictionaries, one transaction, while ingest waits) for a project
// with 10,000 keys received on each of 30 days (300,000 usage_attributes
// rows) and 1,000 catalog rows, each with a value, that no usage row holds
// and no raw row can carry, which it deletes. No ANALYZE runs, as none does
// in production: the cleanup must not be quadratic in the key count on an
// un-analyzed database.
func BenchmarkCleanDictionaries(b *testing.B) {
	db := setupBenchDB(b)
	ctx := context.Background()
	const keys, days, stale = 10000, 30, 1000
	if _, err := db.db.ExecContext(ctx, `WITH RECURSIVE k(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM k WHERE i + 1 < ?1),
		d(j) AS (SELECT 0 UNION ALL SELECT j + 1 FROM d WHERE j + 1 < ?2)
		INSERT INTO usage_attributes (project_id, day, attr_key, events)
		SELECT ?3, date('2026-08-01', '+' || j || ' days'), 'key' || i, 1 FROM k, d`, keys, days, benchProject); err != nil {
		b.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO project_attributes (project_id, key)
		SELECT DISTINCT project_id, attr_key FROM usage_attributes`); err != nil {
		b.Fatal(err)
	}
	plant := func() {
		if _, err := db.db.ExecContext(ctx, `WITH RECURSIVE k(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM k WHERE i + 1 < ?1)
			INSERT INTO project_attributes (project_id, key, last_seen_day) SELECT ?2, 'gone' || i, '2026-01-01' FROM k`, stale, benchProject); err != nil {
			b.Fatal(err)
		}
		if _, err := db.db.ExecContext(ctx, `INSERT INTO attribute_values (key_id, n, value, last_seen_day)
			SELECT id, 1, 'v', '2026-01-01' FROM project_attributes WHERE key LIKE 'gone%'`); err != nil {
			b.Fatal(err)
		}
	}
	count := func(table string) (n int) {
		if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			b.Fatal(err)
		}
		return n
	}
	before := civil.Date{Year: 2026, Month: 10, Day: 1}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		plant()
		b.StartTimer()
		if err := db.CleanDictionaries(ctx, before); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if n := count("project_attributes"); n != keys {
			b.Fatalf("catalog holds %d rows after the cleanup, want %d", n, keys)
		}
		if n := count("attribute_values"); n != 0 {
			b.Fatalf("attribute_values holds %d rows after the cleanup, want 0", n)
		}
		b.StartTimer()
	}
}

// seedBenchIDs writes days of rows through WriteEvents for benchProject,
// from 2026-08-01: 10,000 views, 1,000 product events and 500 measures a
// day, with UUID event, actor, user and session ids as the SDK sends them,
// 300 paths, and two breakdown keys (plan, color) declared.
func seedBenchIDs(b *testing.B, db *DB, days int) {
	b.Helper()
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `INSERT INTO project_attributes (project_id, key, breakdown)
		VALUES (1, 'plan', 1), (1, 'color', 1) ON CONFLICT (project_id, key) DO UPDATE SET breakdown = 1`); err != nil {
		b.Fatal(err)
	}
	uuid := func(kind, n int) string { return fmt.Sprintf("0192f1a2-7c3b-7d4e-%04x-%012x", kind, n) }
	pick := func(xs []string, i int) string { return xs[i%len(xs)] }
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	seq := 0
	for d := 0; d < days; d++ {
		day := start.AddDate(0, 0, d)
		var batch []store.Event
		add := func(e store.Event, i, n int) {
			seq++
			e.ID = uuid(1, seq)
			e.ProjectID = benchProject
			e.TS = day.Add(time.Duration(i) * (24 * time.Hour / time.Duration(n)))
			e.ReceivedAt = e.TS
			a := (i*7919 + d*31) % 5000
			e.ActorID = uuid(2, a)
			e.Kind = pick([]string{"web", "web", "web", "app"}, i)
			e.Platform = pick([]string{"web", "web", "ios", "android"}, a)
			e.OS = pick([]string{"Windows", "macOS", "Linux", "iOS", "Android"}, a)
			e.OSVersion = pick([]string{"10", "11", "14", "17.1", "13"}, a/5)
			e.Browser = pick([]string{"Chrome", "Firefox", "Safari", "Edge"}, a)
			e.BrowserVersion = fmt.Sprintf("%d", 118+a%6)
			e.BrowserLocale = pick([]string{"en-US", "de-DE", "fr-FR", "es-ES", "ja-JP"}, a)
			e.AppVersion = pick([]string{"", "1.0", "1.1", "2.0"}, a)
			e.Device = pick([]string{"desktop", "mobile", "tablet"}, a)
			e.Country = pick([]string{"US", "DE", "FR", "GB", "JP", "BR", "IN", "CA", "ES", "IT"}, a)
			batch = append(batch, e)
		}
		for i := 0; i < 10000; i++ {
			add(store.Event{Family: store.FamilyViews, ActorKind: store.ActorConnection,
				SessionID:      uuid(3, (i*7919+d*31)%5000*100+d),
				Host:           pick([]string{"example.com", "www.example.com", "docs.example.com"}, i),
				Path:           fmt.Sprintf("/path-%03d", (i*31)%300),
				ReferrerSource: pick([]string{"", "", "google", "bing", "news.ycombinator.com", "t.co"}, i),
				UTMSource:      pick([]string{"", "", "", "newsletter", "ads"}, i),
				UTMMedium:      pick([]string{"", "email", "cpc"}, i/5),
				UTMCampaign:    pick([]string{"", "fall", "spring", "launch"}, i/7),
				DisplayWidth:   []int{1920, 1440, 390, 412}[i%4],
				DisplayHeight:  []int{1080, 900, 844, 915}[i%4],
				Consent:        []store.Consent{store.ConsentGiven, store.ConsentNone, store.ConsentUnknown}[i%3],
			}, i, 10000)
		}
		for i := 0; i < 1000; i++ {
			add(store.Event{Family: store.FamilyProduct, ActorKind: store.ActorUser,
				EventName: pick([]string{"signup", "activated", "export", "invite_sent", "subscribed"}, i),
				UserID:    uuid(4, (i*7919+d*31)%5000),
				GroupID:   fmt.Sprintf("org-%02d", i%40),
				Attributes: map[string]string{
					"plan": pick([]string{"free", "pro", "team"}, i), "color": fmt.Sprintf("c%02d", (i*13)%20),
					"source": pick([]string{"a", "b"}, i), "step": fmt.Sprint(i % 9), "ref": fmt.Sprint(i % 50),
				},
			}, i, 1000)
		}
		for i := 0; i < 500; i++ {
			v := float64(200 + (i*37)%4000)
			add(store.Event{Family: store.FamilyMeasures, ActorKind: store.ActorConnection,
				EventName: pick([]string{"$lcp", "$inp", "$cls"}, i), Measure: "time", Value: &v, SampleRate: 1,
				Attributes: map[string]string{"plan": pick([]string{"free", "pro", "team"}, i), "color": fmt.Sprintf("c%02d", (i*13)%20)},
			}, i, 500)
		}
		if err := db.WriteEvents(ctx, batch); err != nil {
			b.Fatalf("seed day %d: %v", d, err)
		}
	}
}

// BenchmarkLiveHalvesOnIDs times the v_* live halves on a raw table of
// 345,000 rows written through WriteEvents (seedBenchIDs, 30 days): the
// views over a 7-day range, the product and measure views over all 30
// days. The seed is written once; each sub-benchmark runs one query.
func BenchmarkLiveHalvesOnIDs(b *testing.B) {
	db := setupBenchDB(b)
	ctx := context.Background()
	seedBenchIDs(b, db, 30)
	queries := []struct{ name, q, to string }{
		{"views_paths_7d", `SELECT path, SUM(visitors), SUM(views) FROM v_views_paths WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1`, "2026-08-07"},
		{"views_daily_7d", `SELECT kind, SUM(visitors), SUM(sessions) FROM v_views_daily WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1`, "2026-08-07"},
		{"views_browsers_7d", `SELECT browser, browser_version, SUM(views) FROM v_views_browsers WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1, 2`, "2026-08-07"},
		{"views_utm_7d", `SELECT utm_source, utm_campaign, SUM(views) FROM v_views_utm WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1, 2`, "2026-08-07"},
		{"views_consent_7d", `SELECT consent, SUM(views) FROM v_views_consent WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1`, "2026-08-07"},
		{"product_attrs_30d", `SELECT attr_key, attr_value, SUM(count) FROM v_product_attrs WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1, 2`, "2026-08-30"},
		{"product_daily_30d", `SELECT event_name, SUM(count) FROM v_product_daily WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1`, "2026-08-30"},
		{"product_totals_30d", `SELECT SUM(total_events), SUM(active_users) FROM v_product_totals WHERE project_id = ? AND day BETWEEN ? AND ?`, "2026-08-30"},
		{"identity_daily_30d", `SELECT kind, COUNT(*) FROM v_identity_daily WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1`, "2026-08-30"},
		{"measures_daily_30d", `SELECT event_name, SUM(samples) FROM v_measures_daily WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1`, "2026-08-30"},
		{"measures_attrs_30d", `SELECT attr_key, attr_value, SUM(samples) FROM v_measures_attrs WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1, 2`, "2026-08-30"},
	}
	for _, c := range queries {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.db.QueryContext(ctx, c.q, benchProject, "2026-08-01", c.to)
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
		})
	}
}

// BenchmarkRollupDay times the daily pass's readers of the raw rows on a
// table of 7 days written through WriteEvents (seedBenchIDs: 80,500 rows):
// each family's rollup of the first day (one transaction, while ingest
// waits), and the usage pass (RecordUsageHistory) over the seven raw days.
// Each run starts from a copy of the seeded file, since a rollup deletes
// the day it reads.
func BenchmarkRollupDay(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	seeded := filepath.Join(dir, "seeded.db")
	db, err := openAt(seeded)
	if err != nil {
		b.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		b.Fatal(err)
	}
	if _, err := db.CreateProject(ctx, store.RegistryProject{Name: "bench", AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "bench", Action: "project.create"}); err != nil {
		b.Fatal(err)
	}
	seedBenchIDs(b, db, 7)
	if _, err := db.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		b.Fatal(err)
	}
	if err := db.Close(); err != nil {
		b.Fatal(err)
	}
	src, err := os.ReadFile(seeded)
	if err != nil {
		b.Fatal(err)
	}
	first := civil.Date{Year: 2026, Month: 8, Day: 1}
	keys := []string{"plan", "color"}
	// Each case names a table it must have filled, so a broken pass
	// cannot benchmark as fast.
	for _, c := range []struct {
		name, wrote string
		run         func(db *DB) error
	}{
		{"views", "agg_views_paths", func(db *DB) error {
			return db.AggregateViewDay(ctx, benchProject, first, defaultAttrsTopN)
		}},
		{"product", "agg_product_attrs", func(db *DB) error {
			return db.AggregateProductDay(ctx, benchProject, first, keys, defaultAttrsTopN)
		}},
		{"measures", "agg_measures_attrs", func(db *DB) error {
			return db.AggregateMeasureDay(ctx, benchProject, first, keys, defaultAttrsTopN)
		}},
		{"usage", "usage_attributes", func(db *DB) error {
			return db.RecordUsageHistory(ctx, time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC))
		}},
	} {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				path := filepath.Join(dir, fmt.Sprintf("%s-%d.db", c.name, i))
				if err := os.WriteFile(path, src, 0o600); err != nil {
					b.Fatal(err)
				}
				db, err := openAt(path)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := c.run(db); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				var n int
				if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+c.wrote).Scan(&n); err != nil || n == 0 {
					b.Fatalf("%s holds %d rows (%v)", c.wrote, n, err)
				}
				if err := db.Close(); err != nil {
					b.Fatal(err)
				}
				for _, f := range []string{path, path + "-wal", path + "-shm"} {
					if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
