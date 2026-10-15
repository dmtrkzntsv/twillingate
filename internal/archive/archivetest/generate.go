package archivetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// Generated is a project's event files at the volume of a busy install,
// for the measurements (go test -tags measure): one merged month file per
// month, sessions of several views per visitor. Months selects how many,
// PerMonth the rows of each (a half views, four tenths product events
// with eight attributes, a tenth measures).
type Generated struct {
	Project  int64
	Start    time.Time // the first day of the first month, UTC
	Months   int
	PerMonth int
	// RareMonth is the index (0-based) of the month that holds one product
	// event named "rare", for "first time an event happened" questions; -1
	// for none.
	RareMonth int
}

// Generate writes the month files of g under dir (a raw/ directory) as
// <dir>/<project>/<YYYY-MM>.parquet, through eng, and skips a month whose
// file exists, so a directory kept between runs is generated once. A file
// appears only whole (written aside, then renamed). The engine needs
// room for the sort: eng is better opened with a memory limit above the
// measured one. It does not derive anything: archive.Archive.Maintain does.
func (g Generated) Generate(t testing.TB, eng *engine.Engine, dir string) {
	t.Helper()
	pdir := filepath.Join(dir, strconv.FormatInt(g.Project, 10))
	if err := os.MkdirAll(pdir, 0o750); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for m := 0; m < g.Months; m++ {
		from := g.Start.AddDate(0, m, 0)
		out := filepath.Join(pdir, from.Format("2006-01")+".parquet")
		if _, err := os.Stat(out); err == nil {
			continue
		}
		tmp := out + ".tmp"
		began := time.Now()
		if err := eng.Exec(ctx, g.monthSQL(m, from, tmp)); err != nil {
			os.Remove(tmp)
			t.Fatalf("generating %s: %v", filepath.Base(out), err)
		}
		if err := os.Rename(tmp, out); err != nil {
			t.Fatal(err)
		}
		t.Logf("generated %s in %v", filepath.Base(out), time.Since(began).Round(time.Millisecond))
	}
}

// pick is an expression choosing from items by the hash of key (an
// integer expression) and salt, skewed toward the first ones: the head
// carries most of the traffic, as a real dimension does.
func pick(key string, salt int, items ...string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "'" + s + "'"
	}
	return fmt.Sprintf("[%s][1 + CAST(floor(%d * pow((hash(%s * %d) %% 10000) / 10000.0, 2.2)) AS INTEGER)]",
		strings.Join(quoted, ", "), len(items), key, salt)
}

// pickN is pick over the numbers 0..n-1 as a string prefixed by prefix.
func pickN(key string, salt, n int, prefix string) string {
	return fmt.Sprintf("'%s' || CAST(floor(%d * pow((hash(%s * %d) %% 10000) / 10000.0, 2.2)) AS INTEGER)",
		prefix, n, key, salt)
}

var countries = strings.Fields("US DE GB FR IN BR CA NL ES IT AU PL SE JP MX CH AT BE NO DK FI PT IE CZ TR RU UA ZA SG NZ AR CL CO KR ID TH VN EG NG IL")

func (g Generated) monthSQL(index int, from time.Time, out string) string {
	end := from.AddDate(0, 1, 0)
	monthUS := end.Sub(from).Microseconds()
	views := g.PerMonth / 2
	product := g.PerMonth * 4 / 10
	measures := g.PerMonth - views - product
	const avgViews = 2.4 // of the sessions' length distribution below
	rare := -1
	if index == g.RareMonth {
		rare = product / 2
	}
	device := "(" + pick("a", 31, "desktop", "mobile", "tablet") + ")"
	size := "CAST(floor(10 * pow((hash(a * 43) % 10000) / 10000.0, 1.6)) AS INTEGER)"
	r := strings.NewReplacer(
		"{OUT}", out,
		"{START}", from.Format("2006-01-02 15:04:05"),
		"{MONTH_US}", strconv.FormatInt(monthUS, 10),
		"{PROJECT}", strconv.FormatInt(g.Project, 10),
		"{MONTH}", strconv.Itoa(index),
		"{SESSIONS}", strconv.Itoa(int(float64(views)/avgViews)),
		"{PRODUCT}", strconv.Itoa(product),
		"{MEASURES}", strconv.Itoa(measures),
		"{RARE}", strconv.Itoa(rare),
		"{DEVICE}", device,
		"{OS}", pick("a", 17, "windows", "macos", "ios", "android", "linux"),
		"{OS_VERSION}", pick("a", 19, "10", "11", "14.5", "17.2", "13", "12", "6.1", "15"),
		"{BROWSER}", pick("a", 23, "chrome", "safari", "firefox", "edge", "samsung internet", "opera"),
		"{BROWSER_VERSION}", pick("a", 29, "129.0", "128.0", "17.6", "130.0", "127.0", "126.0", "125.0", "16.5"),
		"{LOCALE}", pick("a", 37, "en-US", "en-GB", "de-DE", "fr-FR", "es-ES", "pt-BR", "ja-JP", "pl-PL"),
		"{COUNTRY}", pick("a", 13, countries...),
		"{WIDTH}", "[1920, 1366, 1536, 1440, 390, 414, 360, 2560, 768, 1280]["+size+" + 1]",
		"{HEIGHT}", "[1080, 768, 864, 900, 844, 896, 800, 1440, 1024, 720]["+size+" + 1]",
		"{REFERRER}", pick("sid", 47, "direct", "google", "bing", "duckduckgo", "twitter", "facebook", "linkedin", "reddit",
			"news.ycombinator.com", "github.com", "youtube.com", "t.co", "instagram", "baidu", "yandex", "ecosia",
			"producthunt.com", "medium.com", "substack.com", "pinterest"),
		"{UTM_SOURCE}", pick("sid", 53, "newsletter", "twitter", "google", "facebook", "partner", "linkedin"),
		"{UTM_MEDIUM}", pick("sid", 59, "email", "cpc", "social", "referral"),
		"{UTM_CAMPAIGN}", pickN("sid", 61, 12, "campaign-"),
		"{HOST}", pick("sid", 67, "www.example.com", "blog.example.com", "docs.example.com"),
		"{APP_VERSION}", pick("a", 71, "4.2.0", "4.1.3", "4.1.0", "4.0.2", "3.9.9"),
	)
	return r.Replace(`COPY (SELECT *, CAST(ts AS DATE) AS day FROM (
WITH s AS (
  -- Sessions: a visitor (one of 300000, the same across months) arrives at
  -- a random time and views 1 to 7 pages seconds to minutes apart; one in
  -- twenty returns after a gap of 45 minutes before its third view, which
  -- ends the session there, and one in five declares its session id.
  SELECT sid, CAST(hash(sid * 3) % 300000 AS BIGINT) AS a,
         CAST(CASE WHEN hash(sid * 5) % 100 < 40 THEN 1 WHEN hash(sid * 5) % 100 < 65 THEN 2 WHEN hash(sid * 5) % 100 < 80 THEN 3
              WHEN hash(sid * 5) % 100 < 90 THEN 4 ELSE 5 + hash(sid * 7) % 3 END AS BIGINT) AS nviews,
         CAST((hash(sid * 2 + 1) % 1000000) / 1000000.0 * ({MONTH_US} - 3600000000) AS BIGINT) AS start_us,
         CAST(20 + hash(sid * 29) % 250 AS BIGINT) AS gap_s, hash(sid * 17) % 20 = 0 AS long_gap, hash(sid * 23) % 5 = 0 AS declared
  FROM range({SESSIONS}) t(sid)
), v AS (
  SELECT *, unnest(range(nviews)) AS k FROM s
)
SELECT uuid()::VARCHAR AS id, {PROJECT}::BIGINT AS project_id, 'views' AS family, '$page_view' AS event_name,
       TIMESTAMP '{START}' + to_microseconds(start_us + (k * gap_s + CASE WHEN long_gap AND k >= 2 THEN 2700 ELSE 0 END) * 1000000) AS ts,
       TIMESTAMP '{START}' + to_microseconds(start_us + (k * gap_s + CASE WHEN long_gap AND k >= 2 THEN 2700 ELSE 0 END) * 1000000 + 1000000) AS received_at,
       CASE WHEN a % 10 = 0 THEN 'app' ELSE 'web' END AS kind, 'v-' || a AS actor_id,
       CASE WHEN a % 10 = 0 THEN 'install' ELSE 'connection' END AS actor_kind,
       CASE WHEN a % 7 = 0 THEN 'u-' || a END AS user_id,
       CASE WHEN declared THEN 's-{MONTH}-' || sid END AS session_id,
       CASE WHEN a % 10 = 0 THEN NULL ELSE {HOST} END AS host,
       '/p/' || CAST(floor(2000 * pow((hash(sid * 7 + k) % 10000) / 10000.0, 3)) AS INTEGER) AS path,
       CASE WHEN k = 0 THEN {REFERRER} END AS referrer_source,
       CASE WHEN k = 0 AND hash(sid * 19) % 10 = 0 THEN {UTM_SOURCE} END AS utm_source,
       CASE WHEN k = 0 AND hash(sid * 19) % 10 = 0 THEN {UTM_MEDIUM} END AS utm_medium,
       CASE WHEN k = 0 AND hash(sid * 19) % 10 = 0 THEN {UTM_CAMPAIGN} END AS utm_campaign,
       CASE WHEN a % 10 = 0 THEN CASE WHEN a % 20 = 0 THEN 'ios' ELSE 'android' END ELSE 'web' END AS platform,
       {OS} AS os, {OS_VERSION} AS os_version, {BROWSER} AS browser, {BROWSER_VERSION} AS browser_version,
       {LOCALE} AS browser_locale, CASE WHEN a % 10 = 0 THEN {APP_VERSION} END AS app_version,
       {DEVICE} AS device, CASE WHEN {DEVICE} = 'mobile' THEN 'model-' || (hash(a * 41) % 30) END AS device_model,
       {WIDTH}::BIGINT AS display_width, {HEIGHT}::BIGINT AS display_height, {COUNTRY} AS country,
       CASE WHEN hash(a * 53) % 100 < 70 THEN 1 WHEN hash(a * 53) % 100 < 90 THEN 0 END::BIGINT AS consent,
       1::DOUBLE AS sample_rate
FROM v
UNION ALL BY NAME
SELECT uuid()::VARCHAR AS id, {PROJECT}::BIGINT AS project_id, 'product' AS family,
       CASE WHEN i = {RARE} THEN 'rare' ELSE 'event_' || (i % 20) END AS event_name,
       TIMESTAMP '{START}' + to_microseconds(CAST(hash(i * 5 + 1) % {MONTH_US} AS BIGINT)) AS ts,
       TIMESTAMP '{START}' + to_microseconds(CAST(hash(i * 5 + 1) % {MONTH_US} AS BIGINT) + 1000000) AS received_at,
       'web' AS kind, 'v-' || (hash(i * 7919) % 300000) AS actor_id, 'connection' AS actor_kind,
       CASE WHEN i % 3 = 0 THEN 'u-' || (i % 50000) END AS user_id, 's-p{MONTH}-' || (i // 30) AS session_id,
       'www.example.com' AS host, '/p/' || (i % 500) AS path, 'web' AS platform,
       ['macos', 'windows', 'ios', 'android'][i % 4 + 1] AS os, ['chrome', 'safari', 'firefox'][i % 3 + 1] AS browser,
       ['DE', 'US', 'FR', 'GB'][i % 4 + 1] AS country, 1::BIGINT AS consent, 1::DOUBLE AS sample_rate,
       ['free', 'pro', 'team'][i % 3 + 1] AS attr_plan, ['a', 'b'][i % 2 + 1] AS attr_variant,
       (i % 7)::VARCHAR AS attr_step, 'sku-' || (i % 20000) AS attr_item_id, 'category-' || (i % 40) AS attr_category,
       'exp-' || (i % 13) AS attr_experiment, ['true', 'false'][i % 2 + 1] AS attr_logged_in,
       (i % 300)::VARCHAR || '.' || (i % 100)::VARCHAR AS attr_cart_value
FROM range({PRODUCT}) t(i)
UNION ALL BY NAME
SELECT uuid()::VARCHAR AS id, {PROJECT}::BIGINT AS project_id, 'measures' AS family, 'load' AS event_name,
       TIMESTAMP '{START}' + to_microseconds(CAST(hash(i * 5 + 3) % {MONTH_US} AS BIGINT)) AS ts,
       TIMESTAMP '{START}' + to_microseconds(CAST(hash(i * 5 + 3) % {MONTH_US} AS BIGINT) + 1000000) AS received_at,
       'web' AS kind, 'v-' || (hash(i * 7919) % 300000) AS actor_id, 'connection' AS actor_kind,
       'www.example.com' AS host, '/p/' || (i % 500) AS path, 'web' AS platform,
       ['DE', 'US', 'FR', 'GB'][i % 4 + 1] AS country, 1::BIGINT AS consent,
       (i % 1000)::DOUBLE AS value, 'time' AS measure, 1::DOUBLE AS sample_rate
FROM range({MEASURES}) t(i)
) ORDER BY family, ts) TO '{OUT}' (FORMAT parquet, COMPRESSION zstd)`)
}
