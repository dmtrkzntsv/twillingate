package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestDBAt opens a temp database migrated only through version v.
func newTestDBAt(t *testing.T, v int) *DB {
	t.Helper()
	db, err := openAt(filepath.Join(t.TempDir(), "at.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.migrateThrough(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigration012Folds(t *testing.T) {
	db := newTestDBAt(t, 11)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// raw rows
	exec(`INSERT INTO web_hits (id, project, ts, received_at, actor_id, user_id, group_id, host, path,
	      referrer_source, utm_source, utm_medium, utm_campaign, country, device, browser, os)
	      VALUES ('w1','p','2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','h1','','','x.com','/a','google','','','','DE','mobile','Chrome','Android'),
	             ('w2','p','2026-09-10T10:00:00Z','2026-09-10T10:00:00Z','u1','u1','','x.com','/b','','','','','DE','desktop','Firefox','Linux')`)
	exec(`INSERT INTO app_views (id, project, ts, received_at, actor_id, user_id, group_id, session_id, screen,
	      platform, app_version, os_version, device_model, locale, country)
	      VALUES ('a1','p','2026-09-10T11:00:00Z','2026-09-10T11:00:00Z','i1','','','s1','/home','ios','2.4.1','17.2','iPhone15,2','en-US','FR')`)
	exec(`INSERT INTO product_events (id, project, event_name, ts, received_at, actor_id, user_id, group_id, platform, app_version, attributes)
	      VALUES ('e1','p','signup','2026-09-10T12:00:00Z','2026-09-10T12:00:00Z','u1','u1','','IOS','2.4.1','{}')`)
	// aggregates: one day where both families overlap
	exec(`INSERT INTO agg_web_daily VALUES ('p','2026-09-01',10,25,12,3,600)`)
	exec(`INSERT INTO agg_app_daily VALUES ('p','2026-09-01',6,20,8,480)`)
	exec(`INSERT INTO agg_web_pages VALUES ('p','2026-09-01','/home',8,15), ('p','2026-09-01','/post',4,10)`)
	exec(`INSERT INTO agg_app_screens VALUES ('p','2026-09-01','/home',5,12)`)
	exec(`INSERT INTO agg_web_hosts VALUES ('p','2026-09-01','x.com',9,20)`)
	exec(`INSERT INTO agg_web_referrers VALUES ('p','2026-09-01','google',3,4)`)
	exec(`INSERT INTO agg_web_utm VALUES ('p','2026-09-01','nl','email','aug',6,9)`)
	exec(`INSERT INTO agg_web_countries VALUES ('p','2026-09-01','DE',7,18)`)
	exec(`INSERT INTO agg_app_countries VALUES ('p','2026-09-01','DE',2,6)`)
	exec(`INSERT INTO agg_web_devices VALUES ('p','2026-09-01','mobile',5,9)`)
	exec(`INSERT INTO agg_app_devices VALUES ('p','2026-09-01','iPhone15,2',5,12)`)
	exec(`INSERT INTO agg_web_browsers VALUES ('p','2026-09-01','Chrome',7,14)`)
	exec(`INSERT INTO agg_web_os VALUES ('p','2026-09-01','Android',5,9)`)
	exec(`INSERT INTO agg_app_os VALUES ('p','2026-09-01','android','14',2,6), ('p','2026-09-01','ios','17.4',3,6)`)
	exec(`INSERT INTO agg_app_versions VALUES ('p','2026-09-01','ios','2.4.1',5,12)`)
	// actor_kind follows 011's is_user, not the surface: the signed-in app
	// actor becomes a user and the anonymous web actor an install.
	exec(`INSERT INTO actors (project, actor_id, surface, first_seen_day, last_seen_day, is_user) VALUES
	      ('p','u1','web','2026-08-01','2026-09-01',1),
	      ('p','i1','app','2026-08-01','2026-09-01',0),
	      ('p','u2','app','2026-08-01','2026-09-01',1),
	      ('p','h1','web','2026-08-01','2026-09-01',0)`)
	// Three cohort days: one with users counted on both offsets, one product
	// cohort predating the users count (NULL), one app cohort.
	exec(`INSERT INTO agg_retention (project, surface, cohort_day, day_offset, actors, users) VALUES
	      ('p','web','2026-08-01',0,10,6), ('p','web','2026-08-01',7,4,3),
	      ('p','product','2026-08-02',0,3,NULL),
	      ('p','app','2026-08-03',0,5,2),
	      ('p','web','2026-07-20',0,8,NULL), ('p','web','2026-07-20',3,5,2),
	      ('p','app','2026-08-01',0,5,5),
	      ('p','app','2026-08-05',0,3,3)`)
	exec(`INSERT INTO agg_identity_daily VALUES ('p','2026-09-01','user','u1',1,1,5,3,2)`)
	exec(`INSERT INTO agg_product_attrs VALUES ('p','2026-09-01','signup','$platform','ios',3,3),
	                                           ('p','2026-09-01','signup','$platform','iOS',2,2),
	                                           ('p','2026-09-01','signup','$app_version','2.4.1',5,5)`)

	if err := db.migrateThrough(ctx, 13); err != nil {
		t.Fatalf("migration 012: %v", err)
	}

	row := func(q string, dst ...any) {
		t.Helper()
		if err := db.db.QueryRowContext(ctx, q).Scan(dst...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var s string
	var a, b, c, d2, e int

	// raw rows folded with kind, actor_kind, os normalised, screen -> path
	row(`SELECT kind || '|' || actor_kind || '|' || path || '|' || os FROM views WHERE id='w1'`, &s)
	if s != "web|connection|/a|Android" {
		t.Errorf("w1 = %q", s)
	}
	row(`SELECT kind || '|' || actor_kind FROM views WHERE id='w2'`, &s)
	if s != "web|user" {
		t.Errorf("w2 = %q", s)
	}
	row(`SELECT kind || '|' || actor_kind || '|' || path || '|' || os || '|' || app_version || '|' || session_id FROM views WHERE id='a1'`, &s)
	if s != "app|install|/home|iOS|2.4.1|s1" {
		t.Errorf("a1 = %q", s)
	}
	row(`SELECT os || '|' || actor_kind FROM product_events WHERE id='e1'`, &s)
	if s != "iOS|" {
		t.Errorf("e1 = %q (os normalised, actor_kind empty for old rows)", s)
	}

	// daily: one row per kind; app bounces are zero
	row(`SELECT visitors, views, sessions, bounces, duration_sec FROM agg_views_daily WHERE day='2026-09-01' AND kind='web'`, &a, &b, &c, &d2, &e)
	if a != 10 || b != 25 || c != 12 || d2 != 3 || e != 600 {
		t.Errorf("web daily = %d %d %d %d %d", a, b, c, d2, e)
	}
	row(`SELECT visitors, views, sessions, bounces, duration_sec FROM agg_views_daily WHERE day='2026-09-01' AND kind='app'`, &a, &b, &c, &d2, &e)
	if a != 6 || b != 20 || c != 8 || d2 != 0 || e != 480 {
		t.Errorf("app daily = %d %d %d %d %d", a, b, c, d2, e)
	}
	// paths and countries sum on collision; the others copy
	row(`SELECT visitors, views FROM agg_views_paths WHERE path='/home'`, &a, &b)
	if a != 13 || b != 27 {
		t.Errorf("/home = (%d,%d), want (13,27)", a, b)
	}
	row(`SELECT visitors, views FROM agg_views_countries WHERE country='DE'`, &a, &b)
	if a != 9 || b != 24 {
		t.Errorf("DE = (%d,%d), want (9,24)", a, b)
	}
	row(`SELECT visitors FROM agg_views_hosts WHERE host='x.com'`, &a)
	if a != 9 {
		t.Errorf("host = %d", a)
	}
	row(`SELECT visitors FROM agg_views_referrers WHERE source='google'`, &a)
	if a != 3 {
		t.Errorf("referrer = %d", a)
	}
	row(`SELECT visitors FROM agg_views_utm WHERE utm_campaign='aug'`, &a)
	if a != 6 {
		t.Errorf("utm = %d", a)
	}
	row(`SELECT visitors FROM agg_views_browsers WHERE browser='Chrome' AND browser_version=''`, &a)
	if a != 7 {
		t.Errorf("browser = %d", a)
	}
	// os: web 'Android' ('' version) stays apart from app 'Android' '14'
	row(`SELECT COUNT(*) FROM agg_views_os WHERE os='Android'`, &a)
	if a != 2 {
		t.Errorf("Android os rows = %d, want 2", a)
	}
	row(`SELECT visitors FROM agg_views_os WHERE os='iOS' AND os_version='17.4'`, &a)
	if a != 3 {
		t.Errorf("iOS 17.4 = %d", a)
	}
	row(`SELECT visitors FROM agg_views_app_versions WHERE os='iOS' AND app_version='2.4.1'`, &a)
	if a != 5 {
		t.Errorf("app version = %d", a)
	}
	row(`SELECT visitors FROM agg_views_devices WHERE device='mobile' AND device_model=''`, &a)
	if a != 5 {
		t.Errorf("web device = %d", a)
	}
	row(`SELECT visitors FROM agg_views_devices WHERE device='' AND device_model='iPhone15,2'`, &a)
	if a != 5 {
		t.Errorf("app device = %d", a)
	}
	row(`SELECT COUNT(*) FROM agg_views_displays`, &a)
	if a != 0 {
		t.Errorf("displays should start empty, got %d", a)
	}
	// retention and identities
	row(`SELECT actor_kind FROM actors WHERE actor_id='u1'`, &s)
	if s != "user" {
		t.Errorf("u1 actor_kind = %q", s)
	}
	row(`SELECT actor_kind FROM actors WHERE actor_id='i1'`, &s)
	if s != "install" {
		t.Errorf("i1 actor_kind = %q", s)
	}
	row(`SELECT actor_kind FROM actors WHERE actor_id='u2'`, &s)
	if s != "user" {
		t.Errorf("u2 actor_kind = %q, want user: a signed-in app actor is a user", s)
	}
	row(`SELECT actor_kind FROM actors WHERE actor_id='h1'`, &s)
	if s != "install" {
		t.Errorf("h1 actor_kind = %q, want install: an anonymous web actor is not a user", s)
	}
	// The web cohort splits along users: 6 of 10 back on day 0, 3 of 4 on
	// day 7, the remainder under install. The app cohort added below on the
	// same cohort_day knows its own offset-0 users and adds its 5 in.
	row(`SELECT actors FROM agg_retention WHERE actor_kind='user' AND cohort_day='2026-08-01' AND day_offset=0`, &a)
	if a != 11 {
		t.Errorf("user cohort offset 0 = %d, want 11 (6 web + 5 app)", a)
	}
	row(`SELECT actors FROM agg_retention WHERE actor_kind='user' AND cohort_day='2026-08-01' AND day_offset=7`, &a)
	if a != 3 {
		t.Errorf("user cohort offset 7 = %d, want 3", a)
	}
	row(`SELECT actors FROM agg_retention WHERE actor_kind='install' AND cohort_day='2026-08-01' AND day_offset=0`, &a)
	if a != 4 {
		t.Errorf("install cohort offset 0 = %d, want 4 (the app cohort below is 5-5, adds nothing)", a)
	}
	row(`SELECT actors FROM agg_retention WHERE actor_kind='install' AND cohort_day='2026-08-01' AND day_offset=7`, &a)
	if a != 1 {
		t.Errorf("install cohort offset 7 = %d, want 1", a)
	}
	// A cohort counted before signed-in tracking cannot be split: it sits
	// wholly under install, with no user row at all.
	row(`SELECT actors FROM agg_retention WHERE actor_kind='install' AND cohort_day='2026-08-02'`, &a)
	if a != 3 {
		t.Errorf("NULL-users cohort under install = %d, want 3", a)
	}
	row(`SELECT COUNT(*) FROM agg_retention WHERE actor_kind='user' AND cohort_day='2026-08-02'`, &a)
	if a != 0 {
		t.Errorf("NULL-users cohort left %d user rows, want 0", a)
	}
	// The app cohort splits the same way; the surface it arrived on is gone.
	row(`SELECT actors FROM agg_retention WHERE actor_kind='user' AND cohort_day='2026-08-03'`, &a)
	if a != 2 {
		t.Errorf("app user cohort = %d, want 2", a)
	}
	row(`SELECT actors FROM agg_retention WHERE actor_kind='install' AND cohort_day='2026-08-03'`, &a)
	if a != 3 {
		t.Errorf("app install cohort = %d, want 3", a)
	}
	// A cohort whose offset-0 row doesn't know users can't be split at any
	// offset, even one where a later row does know users: it folds wholly
	// under install, offset by offset.
	row(`SELECT COUNT(*) FROM agg_retention WHERE actor_kind='user' AND cohort_day='2026-07-20'`, &a)
	if a != 0 {
		t.Errorf("straddling cohort left %d user rows, want 0", a)
	}
	row(`SELECT actors FROM agg_retention WHERE actor_kind='install' AND cohort_day='2026-07-20' AND day_offset=0`, &a)
	if a != 8 {
		t.Errorf("straddling cohort install offset 0 = %d, want 8", a)
	}
	row(`SELECT actors FROM agg_retention WHERE actor_kind='install' AND cohort_day='2026-07-20' AND day_offset=3`, &a)
	if a != 5 {
		t.Errorf("straddling cohort install offset 3 = %d, want 5", a)
	}
	// A cohort with no unsigned-in remainder produces no install row at all.
	row(`SELECT actors FROM agg_retention WHERE actor_kind='user' AND cohort_day='2026-08-05'`, &a)
	if a != 3 {
		t.Errorf("all-signed-in cohort user = %d, want 3", a)
	}
	row(`SELECT COUNT(*) FROM agg_retention WHERE actor_kind='install' AND cohort_day='2026-08-05'`, &a)
	if a != 0 {
		t.Errorf("all-signed-in cohort left %d install rows, want 0", a)
	}
	row(`SELECT views, events FROM agg_identity_daily WHERE id='u1'`, &a, &b)
	if a != 8 || b != 2 {
		t.Errorf("identity daily = (%d,%d), want (8,2): hits + views", a, b)
	}
	// product attrs: $platform rows normalised, merged and renamed
	row(`SELECT count, unique_users FROM agg_product_attrs WHERE attr_key='$os' AND attr_value='iOS'`, &a, &b)
	if a != 5 || b != 5 {
		t.Errorf("$os iOS = (%d,%d), want (5,5)", a, b)
	}
	row(`SELECT COUNT(*) FROM agg_product_attrs WHERE attr_key='$platform'`, &a)
	if a != 0 {
		t.Errorf("$platform rows left: %d", a)
	}
	// old tables are gone
	row(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND (name LIKE 'agg_web_%' OR name LIKE 'agg_app_%' OR name IN ('web_hits','app_views'))`, &a)
	if a != 0 {
		t.Errorf("%d old tables survived", a)
	}
}
