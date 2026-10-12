package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// seed040 inserts raw rows at schema 40 covering every encoding case:
// each id form, the empty string in every text column, all three
// families, an empty received_at, attributes on views and product rows,
// and a key with no catalog row; then seedViews040's rows, which give
// every v_* view rows on both of its halves.
func seed040(t *testing.T, db *DB) {
	t.Helper()
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]'), (2, 'q', '[]')`,
		`INSERT INTO project_attributes (project_id, key, breakdown) VALUES (1, 'plan', 1)`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, received_at, kind, actor_id, actor_kind,
		   user_id, group_id, session_id, host, path, referrer_source, platform, os, browser, country, attributes)
		 VALUES
		 ('views', 1, '2026-10-10', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f6', '$page_view', '2026-10-10T10:00:00Z', '2026-10-10T10:00:01Z', 'web',
		  '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f7', 'install', '', '', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f8', 'x.com', '/a', 'google', 'web', 'Linux', 'Firefox', 'DE', '{"plan":"pro","tier":"a"}'),
		 ('views', 1, '2026-10-10', '0192F1A2-7C3B-7D4E-8F90-A1B2C3D4E5F9', '$page_view', '2026-10-10T11:00:00Z', '', 'web',
		  'ABCDEF', 'install', 'user@example.com', 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3', 'abc', 'x.com', '', '', 'unknown', '', '', '', '{}'),
		 ('product', 1, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa', 'signup', '2026-10-11T09:00:00Z', '2026-10-11T09:00:00Z', 'app',
		  '0192f1a27c3b7d4e8f90a1b2c3d4e5f6', 'user', '42', 'a1b2c3d4e5f6', '', '', '', '', 'ios', 'iOS', '', 'FR', '{"plan":"free","seats":"3"}')`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, received_at, kind, actor_id, actor_kind, measure, value, sample_rate, attributes)
		 VALUES ('measures', 1, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fb', '$lcp', '2026-10-11T09:00:05Z', '2026-10-11T09:00:05Z', 'web',
		  '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f7', 'install', 'time', 1234.5, 0.5, '{"plan":"pro"}')`,
	)
	seedViews040(t, db)
}

// seedViews040 adds rows that make every v_* view return rows at a cap of
// 2 (attributes_top_n, identities_top_n), so each capped view folds
// values into (other), and its tie-breaks decide which values stay: three
// days, three projects (the third holds ties), every id form in the actor, user, group and session
// columns, consent 1, 0 and NULL, a path and a kind spelled "(other)",
// product events with declared breakdowns (a custom key, a declarable
// $host, a system key declared again, an unknown $ key), measures with
// attributes, and a few aggregated rows (one on a day that still has raw
// rows, which v_identity_daily must then leave out).
func seedViews040(t *testing.T, db *DB) {
	t.Helper()
	pick := func(xs []string, i int) string { return xs[((i%len(xs))+len(xs))%len(xs)] }
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	actors := []string{"0192f1a2-7c3b-7d4e-8f90-00000000a001", "a1b2c3d4", "ABCDEF", "user@example.com",
		"0192F1A2-7C3B-7D4E-8F90-00000000A002", "visitor-1", "0192f1a2-7c3b-7d4e-8f90-00000000a003"}
	sessions := []string{"", "", "0192f1a2-7c3b-7d4e-8f90-00000000b001", "abc", "deadbeef", ""}
	users := []string{"", "42", "user@example.com", "0192f1a2-7c3b-7d4e-8f90-00000000c001", "beef"}
	groups := []string{"", "acme", "a1b2c3d4e5f6", "0192f1a2-7c3b-7d4e-8f90-00000000d001"}
	days := []string{"2026-10-09", "2026-10-10", "2026-10-11"}
	seq := 0x1000
	var rows []string
	row := func(family string, project int, day string, minute int, i int, eventName, measure, value, sampleRate, attrs string) {
		seq++
		ts := fmt.Sprintf("%sT%02d:%02d:00Z", day, 6+minute/60, minute%60)
		received := ts
		if i%7 == 3 {
			received = ""
		}
		consent := []string{"NULL", "1", "0"}[i%3]
		rows = append(rows, "("+strings.Join([]string{
			q(family), fmt.Sprint(project), q(day), q(fmt.Sprintf("0192f1a2-7c3b-7d4e-8f90-%012x", seq)),
			q(eventName), q(ts), q(received),
			q(pick([]string{"web", "web", "app", "tv", "(other)", "kiosk"}, i)),
			q(pick(actors, i*5+len(day)%3)), q(pick([]string{"install", "user", "connection"}, i)),
			q(pick(users, i)), q(pick(groups, i/2)), q(pick(sessions, i)),
			q(pick([]string{"x.com", "y.com", "z.org", ""}, i/3)),
			q(pick([]string{"/p1", "/p2", "/p3", "/p4", "", "/p5", "(other)", "/p6", "/p1", "/p2"}, i*3+minute)),
			q(pick([]string{"", "google", "bing", "duck"}, i)),
			q(pick([]string{"", "news", "ads"}, i)), q(pick([]string{"", "email", "cpc"}, i/2)), q(pick([]string{"", "fall", "spring", "x"}, i)),
			q(pick([]string{"web", "web", "ios", "android", ""}, i)), q(pick([]string{"Linux", "Windows", "macOS", "iOS"}, i)),
			q(pick([]string{"", "10", "11", "14.1", "17"}, i/3)), q(pick([]string{"", "Ubuntu", "Windows 11"}, i)),
			q(pick([]string{"Firefox", "Chrome", "Safari", ""}, i)), q(pick([]string{"120", "121", "17", "", "122"}, i/2)),
			q(pick([]string{"en-US", "de-DE", "", "", "fr-FR"}, i)), q(pick([]string{"", "1.0", "1.1", "2.0", "2.1"}, i/2)),
			q(pick([]string{"", "en", "fr", "", "de"}, i/3)),
			q(pick([]string{"desktop", "mobile", "tablet"}, i)), q(pick([]string{"", "iPhone", "Pixel", "Galaxy"}, i/2)),
			pick([]string{"0", "1920", "390", "1440", "1920", "800"}, i), pick([]string{"0", "1080", "844", "900", "1200", "0"}, i),
			q(pick([]string{"DE", "FR", "US", "", "JP"}, i)), consent, q(attrs), value, q(measure), sampleRate,
		}, ", ")+")")
	}
	for d, day := range days {
		for i := 0; i < 26; i++ {
			project := 1
			if i%9 == 8 {
				project = 2
			}
			attrs := "{}"
			if i%5 == 0 {
				attrs = `{"plan":"pro"}`
			}
			row("views", project, day, (i*37+d*11)%840, i+d, "$page_view", "", "NULL", "1", attrs)
		}
		for i := 0; i < 16; i++ {
			project := 1
			if i%7 == 6 {
				project = 2
			}
			attrs := fmt.Sprintf(`{"plan":%q,"color":%q,"size":%q}`, pick([]string{"pro", "free"}, i+d),
				pick([]string{"red", "green", "blue", "cyan", "", "red", "amber"}, i*2+d), pick([]string{"s", "m"}, i))
			if i%6 == 5 {
				attrs = `{"seats":"3"}`
			}
			row("product", project, day, (i*53+d*7)%840, i+2*d, pick([]string{"signup", "export", "invite", "export"}, i), "", "NULL", "1", attrs)
		}
		for i := 0; i < 12; i++ {
			attrs := fmt.Sprintf(`{"plan":%q,"color":%q}`, pick([]string{"pro", "free"}, i), pick([]string{"red", "green", "blue", "cyan"}, i+d))
			value := pick([]string{"1234.5", "0", "88", "2400", "1234.5", "17.25"}, i+d)
			row("measures", 1+i%5/4, day, (i*29+d*5)%840, i+d, pick([]string{"$lcp", "$inp", "$lcp"}, i), pick([]string{"time", "time", "size"}, i),
				value, pick([]string{"1", "0.5", "0.25"}, i), attrs)
		}
	}
	// Project 3 holds ties at the cap: on 2026-10-09 three views differ in
	// every column, on 2026-10-10 they share each two-column view's first
	// column and differ in the second, and three product rows differ in
	// user and group. Each tie must break on the text (a before m before
	// z), while the dictionaries number the z values first (five more rows
	// on 2026-10-11 carry them) and a stored blob id sorts after any text.
	tieCols := []string{"kind", "host", "path", "referrer_source", "utm_source", "utm_medium", "utm_campaign",
		"platform", "os", "os_version", "browser", "browser_version", "browser_locale", "app_version", "app_locale",
		"device", "device_model", "country"}
	shared := map[string]bool{"platform": true, "browser": true, "device": true, "browser_locale": true, "os": true,
		"utm_source": true, "utm_medium": true}
	var ties []string
	tie := func(day string, n int, value func(r int, col string) string) {
		for r := 0; r < n; r++ {
			seq++
			vals := []string{q(day), q(fmt.Sprintf("0192f1a2-7c3b-7d4e-8f90-%012x", seq)),
				q(fmt.Sprintf("%sT1%d:00:00Z", day, r)), q(fmt.Sprintf("tie-actor-%d", r))}
			for _, c := range tieCols {
				vals = append(vals, q(value(r, c)))
			}
			ties = append(ties, "('views', 3, "+strings.Join(vals[:2], ", ")+", '$page_view', "+vals[2]+", "+vals[2]+
				", "+vals[3]+", 'install', '', '', '', '{}', NULL, '', 1, "+strings.Join(vals[4:], ", ")+")")
		}
	}
	letter := []string{"z", "a", "m"}
	tie("2026-10-09", 3, func(r int, c string) string { return letter[r] + "-" + c })
	tie("2026-10-10", 3, func(r int, c string) string {
		if shared[c] {
			return "same-" + c
		}
		return letter[r] + "-" + c
	})
	tie("2026-10-11", 5, func(r int, c string) string { return "z-" + c })
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (3, 'r', '[]')`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, received_at, actor_id, actor_kind,
		   user_id, group_id, session_id, attributes, consent, measure, sample_rate, `+strings.Join(tieCols, ", ")+`)
		 VALUES `+strings.Join(ties, ",\n"),
		`INSERT INTO events (family, project_id, day, id, event_name, ts, actor_id, actor_kind, user_id, group_id, attributes)
		 VALUES ('product', 3, '2026-10-10', '0192f1a2-7c3b-7d4e-8f90-00000000e001', 'signup', '2026-10-10T10:00:00Z', 'z1', 'user',
		         '0192f1a2-7c3b-7d4e-8f90-00000000e0aa', 'ab12', '{}'),
		        ('product', 3, '2026-10-10', '0192f1a2-7c3b-7d4e-8f90-00000000e002', 'signup', '2026-10-10T10:01:00Z', 'z2', 'user',
		         'auser', 'agroup', '{}'),
		        ('product', 3, '2026-10-10', '0192f1a2-7c3b-7d4e-8f90-00000000e003', 'signup', '2026-10-10T10:02:00Z', 'z3', 'user',
		         'zuser', 'zgroup', '{}')`,
	)
	execAll(t, db,
		`INSERT INTO meta (key, value) VALUES ('attributes_top_n', '2'), ('identities_top_n', '2')
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		`INSERT INTO project_attributes (project_id, key, breakdown) VALUES
		 (1, 'color', 1), (1, 'size', 0), (1, '$host', 1), (1, '$platform', 1), (1, '$weird', 1), (2, 'color', 1)`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, received_at, kind, actor_id, actor_kind,
		   user_id, group_id, session_id, host, path, referrer_source, utm_source, utm_medium, utm_campaign,
		   platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale,
		   device, device_model, display_width, display_height, country, consent, attributes, value, measure, sample_rate)
		 VALUES `+strings.Join(rows, ",\n"),
		`INSERT INTO agg_views_paths (project_id, day, path, visitors, views) VALUES (1, '2026-10-08', '/p1', 3, 5), (1, '2026-10-08', '(other)', 1, 1)`,
		`INSERT INTO agg_identity_daily (project_id, day, kind, id, actors, users, views, events) VALUES
		 (1, '2026-10-08', 'user', '42', 1, 1, 2, 3), (1, '2026-10-09', 'group', 'acme', 2, 1, 4, 0)`,
	)
}

// snapshotViewRows reads every row of every raw_* and v_* view (v_events_flat
// is process-owned and skipped), ordered, as strings; attributes columns
// are normalized as parsed JSON (key order is not contract, spec D6).
func snapshotViewRows(t *testing.T, db *DB) map[string][]string {
	t.Helper()
	ctx := context.Background()
	names := []string{}
	rows, err := db.db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type='view'
		AND (name LIKE 'raw\_%' ESCAPE '\' OR name LIKE 'v\_%' ESCAPE '\') AND name <> 'v_events_flat' ORDER BY name`)
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
		r, err := db.db.QueryContext(ctx, `SELECT * FROM `+n)
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
			parts := make([]string, len(cols))
			for i, v := range vals {
				s := fmt.Sprint(v)
				if b, ok := v.([]byte); ok {
					s = string(b)
				}
				if cols[i] == "attributes" {
					var m map[string]any
					if json.Unmarshal([]byte(s), &m) == nil {
						keys := make([]string, 0, len(m))
						for k := range m {
							keys = append(keys, k)
						}
						sort.Strings(keys)
						var b strings.Builder
						for _, k := range keys {
							fmt.Fprintf(&b, "%s=%v;", k, m[k])
						}
						s = b.String()
					}
				}
				parts[i] = cols[i] + ":" + s
			}
			out[n] = append(out[n], strings.Join(parts, "|"))
		}
		r.Close()
		sort.Strings(out[n])
	}
	return out
}

func TestMigration041KeepsEveryViewRow(t *testing.T) {
	db := newTestDBAt(t, 40)
	seed040(t, db)
	before := snapshotViewRows(t, db)
	if err := db.migrateThrough(context.Background(), 41); err != nil {
		t.Fatal(err)
	}
	after := snapshotViewRows(t, db)
	delete(after, "v_identities") // new in 041
	if !reflect.DeepEqual(before, after) {
		for n := range before {
			if !reflect.DeepEqual(before[n], after[n]) {
				t.Errorf("%s:\nbefore %v\nafter  %v", n, before[n], after[n])
			}
		}
	}
	if len(before) < 23 {
		t.Errorf("snapshot read %d views, want the 3 raw_* and 20 v_*", len(before))
	}
	for n, rows := range before {
		if len(rows) == 0 {
			t.Errorf("%s has no rows: the seed does not exercise it", n)
		}
	}
	// The cap folds: each capped view holds an (other) row.
	for _, n := range []string{"v_views_browsers", "v_views_utm", "v_product_attrs", "v_measures_attrs"} {
		if !strings.Contains(strings.Join(before[n], "\n"), ":(other)|") {
			t.Errorf("%s has no (other) row: the seed does not reach its cap", n)
		}
	}
	// The seed also sends a path and a kind spelled "(other)", so there a
	// folded row is one on a raw day that holds no such literal.
	for _, c := range []struct{ view, col string }{{"v_views_paths", "path"}, {"v_views_daily", "kind"}} {
		var folded int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM ` + c.view + ` v WHERE v.` + c.col + ` = '(other)'
			AND EXISTS (SELECT 1 FROM raw_views r WHERE r.project_id = v.project_id AND r.day = v.day)
			AND NOT EXISTS (SELECT 1 FROM raw_views r WHERE r.project_id = v.project_id AND r.day = v.day
			                AND r.` + c.col + ` = '(other)')`).Scan(&folded); err != nil {
			t.Fatal(err)
		}
		if folded == 0 {
			t.Errorf("%s has no folded (other) row: the seed does not reach its cap", c.view)
		}
	}
}

func TestMigration041StoresEncoded(t *testing.T) {
	db := newTestDBAt(t, 40)
	seed040(t, db)
	if err := db.migrateThrough(context.Background(), 41); err != nil {
		t.Fatal(err)
	}
	var typ string
	const signup = `family = 2 AND id = unhex('0192f1a27c3b7d4e8f90a1b2c3d4e5fa')`
	if err := db.db.QueryRow(`SELECT typeof(id) || typeof(actor_id) || typeof(ts) || typeof(event_name) FROM events WHERE ` + signup).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "blobblobintegerinteger" {
		t.Errorf("stored types = %s", typ)
	}
	var attrs string
	db.db.QueryRow(`SELECT attributes FROM events WHERE ` + signup).Scan(&attrs)
	if strings.Contains(attrs, "plan") || strings.Contains(attrs, "free") {
		t.Errorf("attributes still carry text: %s", attrs)
	}
	var missing int // 'tier' and 'seats' had no catalog row before
	db.db.QueryRow(`SELECT COUNT(*) FROM project_attributes WHERE project_id = 1 AND key IN ('tier', 'seats')`).Scan(&missing)
	if missing != 2 {
		t.Errorf("catalog rows added by the pre-step = %d, want 2", missing)
	}
	// The breakdown flag survives the rebuild, and every key gets the
	// newest day a raw row carried it.
	var breakdown int
	var seen string
	db.db.QueryRow(`SELECT breakdown, last_seen_day FROM project_attributes WHERE project_id = 1 AND key = 'plan'`).Scan(&breakdown, &seen)
	if breakdown != 1 || seen != "2026-10-11" {
		t.Errorf("plan: breakdown %d, last_seen_day %q", breakdown, seen)
	}
	// values_next follows the numbers the copy gave out.
	var next, maxN int
	db.db.QueryRow(`SELECT k.values_next, (SELECT MAX(n) FROM attribute_values WHERE key_id = k.id)
		FROM project_attributes k WHERE project_id = 1 AND key = 'plan'`).Scan(&next, &maxN)
	if maxN != 2 || next != 3 {
		t.Errorf("plan: values_next %d with max n %d, want 3 and 2", next, maxN)
	}
}

func TestMigration041RefusesNonStringValues(t *testing.T) {
	db := newTestDBAt(t, 40)
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, actor_id, attributes)
		 VALUES ('product', 1, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa', 'e', '2026-10-11T09:00:00Z', 'a', '{"n":3}')`)
	err := db.migrateThrough(context.Background(), 41)
	if err == nil || !strings.Contains(err.Error(), "not a string") {
		t.Fatalf("err = %v, want a refusal naming non-string values", err)
	}
}

func TestMigration041RefusesInvalidJSON(t *testing.T) {
	db := newTestDBAt(t, 40)
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, actor_id, attributes)
		 VALUES ('product', 1, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa', 'e', '2026-10-11T09:00:00Z', 'a', 'nope'),
		        ('product', 1, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fb', 'e', '2026-10-11T09:00:00Z', 'a', '[]')`)
	err := db.migrateThrough(context.Background(), 41)
	if err == nil || !strings.Contains(err.Error(), "2 rows with attributes that are not a JSON object") {
		t.Fatalf("err = %v, want a refusal counting the rows", err)
	}
}

func TestMigration041DropsOrphanRows(t *testing.T) {
	db := newTestDBAt(t, 40)
	execAll(t, db, `INSERT INTO events (family, project_id, day, id, event_name, ts, actor_id, attributes)
		 VALUES ('product', 99, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa', 'e', '2026-10-11T09:00:00Z', 'a', '{"k":"v"}')`)
	if err := db.migrateThrough(context.Background(), 41); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	if n != 0 {
		t.Errorf("orphan rows kept: %d", n)
	}
}

// TestMigrationEncodesLikeIngest writes the same event through ingest on
// a migrated database and through the migration on a seeded one, and
// requires byte-equal id columns and equal decoded rows.
func TestMigrationEncodesLikeIngest(t *testing.T) {
	ctx := context.Background()
	migrated := newTestDBAt(t, 40)
	seed040(t, migrated)
	if err := migrated.migrateThrough(ctx, 41); err != nil {
		t.Fatal(err)
	}
	ingested := newTestDB(t)
	execAll(t, ingested, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]'), (2, 'q', '[]'), (3, 'r', '[]')`)
	rows, err := migrated.db.QueryContext(ctx, `SELECT family, project_id, id, event_name, ts, received_at, kind,
		actor_id, actor_kind, user_id, group_id, session_id, host, path, referrer_source,
		utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version,
		browser_locale, app_version, app_locale, device, device_model, display_width, display_height,
		country, consent, attributes, value, measure, sample_rate
		FROM (SELECT * FROM raw_views UNION ALL SELECT * FROM raw_product UNION ALL SELECT * FROM raw_measures)`)
	if err != nil {
		t.Fatal(err)
	}
	var evs []store.Event
	for rows.Next() {
		var ev store.Event
		var family, ts, received, attrs string
		var consent sql.NullInt64
		var value sql.NullFloat64
		if err := rows.Scan(&family, &ev.ProjectID, &ev.ID, &ev.EventName, &ts, &received, &ev.Kind,
			&ev.ActorID, &ev.ActorKind, &ev.UserID, &ev.GroupID, &ev.SessionID, &ev.Host, &ev.Path, &ev.ReferrerSource,
			&ev.UTMSource, &ev.UTMMedium, &ev.UTMCampaign, &ev.Platform, &ev.OS, &ev.OSVersion, &ev.OSName,
			&ev.Browser, &ev.BrowserVersion, &ev.BrowserLocale, &ev.AppVersion, &ev.AppLocale, &ev.Device,
			&ev.DeviceModel, &ev.DisplayWidth, &ev.DisplayHeight, &ev.Country, &consent, &attrs, &value,
			&ev.Measure, &ev.SampleRate); err != nil {
			t.Fatal(err)
		}
		ev.Family = store.Family(family)
		if ev.TS, err = time.Parse(tsFormat, ts); err != nil {
			t.Fatal(err)
		}
		if received != "" {
			if ev.ReceivedAt, err = time.Parse(tsFormat, received); err != nil {
				t.Fatal(err)
			}
		}
		switch {
		case !consent.Valid:
			ev.Consent = store.ConsentUnknown
		case consent.Int64 == 1:
			ev.Consent = store.ConsentGiven
		default:
			ev.Consent = store.ConsentNone
		}
		if err := json.Unmarshal([]byte(attrs), &ev.Attributes); err != nil {
			t.Fatal(err)
		}
		if value.Valid {
			v := value.Float64
			ev.Value = &v
		}
		evs = append(evs, ev)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var seeded int
	migrated.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&seeded)
	if len(evs) != seeded || seeded < 4 {
		t.Fatalf("read %d seeded rows, want %d", len(evs), seeded)
	}
	if err := ingested.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	q := `SELECT family, hex(id), hex(actor_id), hex(user_id), hex(group_id), hex(session_id), ts, received_at FROM events ORDER BY hex(id)`
	a, b := allRows(t, migrated, q), allRows(t, ingested, q)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("migration %v\ningest    %v", a, b)
	}
	x, y := snapshotViewRows(t, migrated), snapshotViewRows(t, ingested)
	for _, n := range []string{"raw_views", "raw_product", "raw_measures"} {
		if !reflect.DeepEqual(x[n], y[n]) {
			t.Errorf("%s decoded rows differ:\nmigration %v\ningest    %v", n, x[n], y[n])
		}
	}
}

// allRows reads every row of q as strings.
func allRows(t *testing.T, db *DB, q string) [][]string {
	t.Helper()
	rows, err := db.db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				row[i] = string(b)
			} else {
				row[i] = fmt.Sprint(v)
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
