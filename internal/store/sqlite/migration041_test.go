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
// and a key with no catalog row.
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
	for _, n := range []string{"raw_views", "raw_product", "raw_measures", "v_identity_daily", "v_product_attrs"} {
		if len(before[n]) == 0 {
			t.Errorf("%s has no rows: the seed does not exercise it", n)
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
	if err := db.db.QueryRow(`SELECT typeof(id) || typeof(actor_id) || typeof(ts) || typeof(event_name) FROM events WHERE family = 2`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "blobblobintegerinteger" {
		t.Errorf("stored types = %s", typ)
	}
	var attrs string
	db.db.QueryRow(`SELECT attributes FROM events WHERE family = 2`).Scan(&attrs)
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
	execAll(t, ingested, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]'), (2, 'q', '[]')`)
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
	if len(evs) != 4 {
		t.Fatalf("read %d seeded rows, want 4", len(evs))
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
