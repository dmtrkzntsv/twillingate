package sqlite

import (
	"context"
	"strings"
	"testing"
)

// seed014 builds a database at schema 014 holding one row per case the
// 015 folds have to get right: canonical, out-of-vocabulary and empty
// values in every column that closes, an app row 012 folded a platform
// into, and aggregate history in each shape.
func seed014(t *testing.T) *DB {
	t.Helper()
	db := newTestDBAt(t, 14)
	for _, q := range []string{
		`INSERT INTO projects (id, name) VALUES (1, 'Blog'), (2, 'App')`,
		`INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind, path, os, browser, device, app_version) VALUES
		 ('w1',1,'2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','web','a1','connection','/','Windows','Chrome','desktop',''),
		 ('w2',1,'2026-09-10T10:01:00Z','2026-09-10T10:01:00Z','web','a2','connection','/','Haiku','Samsung Internet','mobile',''),
		 ('w3',1,'2026-09-10T10:02:00Z','2026-09-10T10:02:00Z','web','a3','connection','/','','','',''),
		 ('a1',2,'2026-09-10T11:00:00Z','2026-09-10T11:00:00Z','app','i1','install','/home','iOS','','','2.4.1'),
		 ('a2',2,'2026-09-10T11:01:00Z','2026-09-10T11:01:00Z','app','i2','install','/home','Not A Platform!','','','2.4.1'),
		 ('c1',2,'2026-09-10T12:00:00Z','2026-09-10T12:00:00Z','cli','i3','install','deploy','Linux','','','')`,
		`INSERT INTO events (id, project_id, event_name, actor_id, ts, os) VALUES
		 ('e1',2,'signup','i1','2026-09-10T11:00:00Z','iOS'),
		 ('e2',2,'signup','i2','2026-09-10T11:00:00Z',''),
		 ('e3',2,'signup','i3','2026-09-10T11:00:00Z','Haiku')`,
		`INSERT INTO agg_views_daily VALUES (1,'2026-09-01','web',10,25,12,3,600), (2,'2026-09-01','app',6,20,8,0,480), (2,'2026-09-01','cli',2,4,2,0,0)`,
		`INSERT INTO agg_views_os VALUES (1,'2026-09-01','iOS','17.4',3,6), (1,'2026-09-01','','',2,2), (1,'2026-09-01','Haiku','',1,1), (1,'2026-09-01','Mac OS','',1,1)`,
		`INSERT INTO agg_views_browsers VALUES (1,'2026-09-01','Chrome','126',7,14), (1,'2026-09-01','Samsung Internet','25',1,1), (1,'2026-09-01','','',2,2), (1,'2026-09-01','Netscape','4',1,1)`,
		`INSERT INTO agg_views_devices VALUES (1,'2026-09-01','desktop','',7,13), (1,'2026-09-01','','iPhone15,3',5,12)`,
		`INSERT INTO agg_views_app_versions VALUES
		 (2,'2026-09-01','iOS','2.4.1',5,12), (2,'2026-09-01','Android','2.4.1',4,9),
		 (2,'2026-09-01','Haiku','2.4.1',1,1), (2,'2026-09-01','Beta OS','2.4.1',1,1), (2,'2026-09-01','Not/OS','2.4.1',1,1)`,
		`INSERT INTO agg_product_attrs VALUES
		 (2,'2026-09-01','signup','$os','iOS',3,3), (2,'2026-09-01','signup','$os','Haiku',1,1), (2,'2026-09-01','signup','plan','Pro',2,2)`,
	} {
		if _, err := db.db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return db
}

func TestMigration015FoldsAndBackfills(t *testing.T) {
	db := seed014(t)
	ctx := context.Background()
	if err := db.migrateThrough(ctx, 15); err != nil {
		t.Fatalf("migration 015: %v", err)
	}
	row := func(q string, dst ...any) {
		t.Helper()
		if err := db.db.QueryRowContext(ctx, q).Scan(dst...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var s, s2 string
	var n, m int

	// 1. platform backfill: web is web, app inverts 012's fold, anything
	//    else (and a token outside the pattern) is unknown.
	for id, want := range map[string]string{"w1": "web", "w2": "web", "w3": "web", "a1": "ios", "a2": "unknown", "c1": "unknown"} {
		row(`SELECT platform FROM views WHERE id='`+id+`'`, &s)
		if s != want {
			t.Errorf("views %s platform = %q, want %q", id, s, want)
		}
	}
	// 2. events: no backfill, the default stands.
	row(`SELECT COUNT(*) FROM events WHERE platform <> 'unknown'`, &n)
	if n != 0 {
		t.Errorf("%d events rows have a backfilled platform; expected none", n)
	}
	// 3. OS fold on raw rows: lower-cased, '' -> unknown, unlisted -> other
	//    with the original preserved in os_name (and only then).
	for id, want := range map[string][2]string{
		"w1": {"windows", ""}, "w2": {"other", "Haiku"}, "w3": {"unknown", ""},
		"a1": {"ios", ""}, "a2": {"other", "Not A Platform!"}, "c1": {"linux", ""},
	} {
		row(`SELECT os, os_name FROM views WHERE id='`+id+`'`, &s, &s2)
		if s != want[0] || s2 != want[1] {
			t.Errorf("views %s os = (%q, %q), want %v", id, s, s2, want)
		}
	}
	for id, want := range map[string]string{"e1": "ios", "e2": "unknown", "e3": "other"} {
		row(`SELECT os FROM events WHERE id='`+id+`'`, &s)
		if s != want {
			t.Errorf("events %s os = %q, want %q", id, s, want)
		}
	}
	// 4. OS fold on aggregate history: canonical lower-cased, '' relabelled,
	//    the out-of-vocabulary tail left exactly as it was, no count moved.
	row(`SELECT visitors FROM agg_views_os WHERE os='ios' AND os_version='17.4'`, &n)
	if n != 3 {
		t.Errorf("agg_views_os ios/17.4 visitors = %d, want 3", n)
	}
	row(`SELECT visitors FROM agg_views_os WHERE os='unknown' AND os_version=''`, &n)
	if n != 2 {
		t.Errorf("agg_views_os unknown visitors = %d, want 2", n)
	}
	row(`SELECT COUNT(*) FROM agg_views_os WHERE os='Haiku'`, &n)
	if n != 1 {
		t.Errorf("agg_views_os Haiku rows = %d, want 1 (left unfolded)", n)
	}
	// A spelling the validator would canonicalise by more than case --
	// NormalizeOS folds "Mac OS" to macos -- is left alone too: the
	// pre-upgrade check groups by lower(os), so anything wider than
	// lower-casing could collide on a key the operator was told was clean.
	row(`SELECT COUNT(*) FROM agg_views_os WHERE os='Mac OS'`, &n)
	if n != 1 {
		t.Errorf("agg_views_os 'Mac OS' rows = %d, want 1 (only case changes are folded)", n)
	}
	row(`SELECT COUNT(*) FROM agg_views_os WHERE os IN ('iOS','','other')`, &n)
	if n != 0 {
		t.Errorf("agg_views_os still has %d unfolded or over-folded rows", n)
	}
	row(`SELECT count FROM agg_product_attrs WHERE attr_key='$os' AND attr_value='ios'`, &n)
	row(`SELECT count FROM agg_product_attrs WHERE attr_key='$os' AND attr_value='Haiku'`, &m)
	if n != 3 || m != 1 {
		t.Errorf("agg_product_attrs $os = ios %d, Haiku %d; want 3, 1", n, m)
	}
	row(`SELECT attr_value FROM agg_product_attrs WHERE attr_key='plan'`, &s)
	if s != "Pro" {
		t.Errorf("a custom attribute value was case-folded: %q", s)
	}
	// 5. browser and device fold: loss-free, raw and aggregate alike.
	for id, want := range map[string][2]string{
		"w1": {"chrome", "desktop"}, "w2": {"samsung_internet", "mobile"}, "w3": {"unknown", "unknown"}, "a1": {"unknown", "unknown"},
	} {
		row(`SELECT browser, device FROM views WHERE id='`+id+`'`, &s, &s2)
		if s != want[0] || s2 != want[1] {
			t.Errorf("views %s browser/device = (%q, %q), want %v", id, s, s2, want)
		}
	}
	for _, c := range []struct {
		q    string
		want int
	}{
		{`SELECT visitors FROM agg_views_browsers WHERE browser='chrome' AND browser_version='126'`, 7},
		{`SELECT visitors FROM agg_views_browsers WHERE browser='samsung_internet' AND browser_version='25'`, 1},
		{`SELECT visitors FROM agg_views_browsers WHERE browser='unknown' AND browser_version=''`, 2},
		{`SELECT visitors FROM agg_views_devices WHERE device='desktop' AND device_model=''`, 7},
		{`SELECT visitors FROM agg_views_devices WHERE device='unknown' AND device_model='iPhone15,3'`, 5},
		{`SELECT COUNT(*) FROM agg_views_browsers WHERE browser='Netscape'`, 1},
		{`SELECT COUNT(*) FROM agg_views_browsers`, 4},
		{`SELECT COUNT(*) FROM agg_views_devices`, 2},
	} {
		row(c.q, &n)
		if n != c.want {
			t.Errorf("%s = %d, want %d", c.q, n, c.want)
		}
	}
	// 6. agg_views_platforms seeded from agg_views_daily: web exact, every
	//    other kind summed into unknown.
	row(`SELECT visitors, views FROM agg_views_platforms WHERE project_id=1 AND day='2026-09-01' AND platform='web'`, &n, &m)
	if n != 10 || m != 25 {
		t.Errorf("platforms web = (%d,%d), want (10,25)", n, m)
	}
	row(`SELECT visitors, views FROM agg_views_platforms WHERE project_id=2 AND day='2026-09-01' AND platform='unknown'`, &n, &m)
	if n != 8 || m != 24 {
		t.Errorf("platforms unknown = (%d,%d), want (8,24): app and cli summed", n, m)
	}
	row(`SELECT COUNT(*) FROM agg_views_platforms`, &n)
	if n != 2 {
		t.Errorf("agg_views_platforms rows = %d, want 2", n)
	}
	// 7. app_versions rekey: canonical values lower-cased and unmerged, a
	//    pattern-conforming unlisted value kept as its own platform, only
	//    values outside the pattern merge into unknown.
	if hasColumn(t, db, "agg_views_app_versions", "os") || !hasColumn(t, db, "agg_views_app_versions", "platform") {
		t.Fatal("agg_views_app_versions was not rekeyed to platform")
	}
	for _, c := range []struct {
		platform string
		v, p     int
	}{{"ios", 5, 12}, {"android", 4, 9}, {"haiku", 1, 1}, {"unknown", 2, 2}} {
		row(`SELECT visitors, views FROM agg_views_app_versions WHERE platform='`+c.platform+`' AND app_version='2.4.1'`, &n, &m)
		if n != c.v || m != c.p {
			t.Errorf("app_versions %s = (%d,%d), want (%d,%d)", c.platform, n, m, c.v, c.p)
		}
	}
	row(`SELECT COUNT(*) FROM agg_views_app_versions`, &n)
	if n != 4 {
		t.Errorf("agg_views_app_versions rows = %d, want 4", n)
	}
	row(`SELECT COUNT(*) FROM sqlite_master WHERE name='agg_views_app_versions_old'`, &n)
	if n != 0 {
		t.Errorf("the rekey left %d agg_views_app_versions_old objects behind", n)
	}
	// 8. views: platforms created, app_versions rekeyed, product attrs
	//    gains $platform. The live halves see the folded raw rows.
	row(`SELECT visitors, views FROM v_views_platforms WHERE project_id=1 AND day='2026-09-10' AND platform='web'`, &n, &m)
	if n != 3 || m != 3 {
		t.Errorf("v_views_platforms live web = (%d,%d), want (3,3)", n, m)
	}
	row(`SELECT visitors FROM v_views_app_versions WHERE project_id=2 AND day='2026-09-10' AND platform='ios' AND app_version='2.4.1'`, &n)
	if n != 1 {
		t.Errorf("v_views_app_versions live ios = %d, want 1", n)
	}
	row(`SELECT count FROM v_product_attrs WHERE project_id=2 AND day='2026-09-10' AND attr_key='$platform' AND attr_value='unknown'`, &n)
	if n != 3 {
		t.Errorf("v_product_attrs $platform unknown = %d, want 3", n)
	}
	var version int
	row(`SELECT MAX(version) FROM schema_migrations`, &version)
	if version != 15 {
		t.Errorf("schema version = %d, want 15", version)
	}
}

// Lower-casing aggregate history is only safe because no two spellings of
// one canonical value can share a key. If they do, the migration must
// abort inside its transaction and leave the database at 014, rather than
// silently dropping or summing one of them. docs/deployment.md tells the
// operator how to find such rows before upgrading.
func TestMigration015AbortsOnCaseCollision(t *testing.T) {
	db := seed014(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO agg_views_os VALUES (1,'2026-09-01','ios','17.4',1,1)`); err != nil {
		t.Fatal(err)
	}
	err := db.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("Migrate = %v, want a constraint failure", err)
	}
	var version int
	if err := db.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 14 {
		t.Errorf("schema version after the aborted migration = %d, want 14", version)
	}
	if hasColumn(t, db, "views", "platform") {
		t.Error("views.platform exists after an aborted 015: the migration did not run in one transaction")
	}
}
