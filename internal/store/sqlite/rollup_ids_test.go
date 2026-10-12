package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// rollupDay is the day the parity tests roll up; the next day stays raw
// and the one after is "today" for the usage pass.
const rollupDay = "2026-08-10"

// rollupKeys is every key the parity fixture's project declares, as the
// daily pass hands them to the rollups: custom keys, declarable reserved
// keys, a $ key the store does not map, and a repeat.
var rollupKeys = []string{"plan", "step", "endpoint", "$path", "$utm_source", "$weird", "plan"}

// slot spreads the 24 rows of a family's day over five values: z 3, a 3,
// top 12, (other) 4 and m 2. Under a cap of 3, top and (other) are kept and
// a and z tie for the last place: the text keeps a. z is written first, so
// it has the lower strings id (and value number), and a tie-break on the
// stored ids would keep z instead.
func slot(i int, z, a, top, m string) string {
	switch {
	case i < 3:
		return z
	case i < 6:
		return a
	case i < 18:
		return top
	case i < 22:
		return "(other)"
	}
	return m
}

// seedRollupDay writes the parity fixture into project p1, which declares
// rollupKeys, and p2, which declares nothing: 24 views, 24 product events
// and 24 measures on rollupDay with every id form (a lowercase UUID, an
// uppercase one, even- and odd-length hex, an email), ties at the cap
// between values whose ids sort the other way, values spelled "(other)",
// empty values, gap and client sessions, and the three consent answers;
// a few rows of each family on the next day, and one product event on
// "today".
func seedRollupDay(t *testing.T, db *DB, p1 int64) {
	t.Helper()
	p2 := p1 + 1
	actors := []string{"0192f1a2-7c3b-7d4e-8000-00000000000a", "0192F1A2-7C3B-7D4E-8000-00000000000B",
		"ab12", "abc", "user@example.com", "00ff"}
	groups := []string{"", "0192f1a2-7c3b-7d4e-9000-000000000001", "org-1", "beef", "Org-2"}
	base := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	at := func(i int) time.Time {
		t := base.Add(time.Duration(i) * 4 * time.Minute)
		if i >= 12 {
			t = t.Add(2 * time.Hour)
		}
		return t
	}
	session := func(i int, actor string) string {
		switch {
		case i%4 == 0:
			return fmt.Sprintf("0192f1a2-7c3b-7d4e-5e55-%012x", len(actor))
		case i%4 == 1:
			return "S-1"
		case i%8 == 2:
			return "7" // text, odd length: it shares the gap counter's keys
		}
		return ""
	}
	var evs []store.Event
	for i := 0; i < 24; i++ {
		actor := actors[(i*5)%len(actors)]
		e := store.Event{Family: store.FamilyViews, ID: fmt.Sprintf("0192f1a2-7c3b-7d4e-8001-%012x", i),
			ProjectID: p1, TS: at(i), ActorID: actor, ActorKind: store.ActorConnection,
			Kind:           slot(i, "zz", "aa", "web", "mm"),
			SessionID:      session(i, actor),
			Host:           slot(i, "z.example", "a.example", "example.com", "m.example"),
			Path:           slot(i, "/z", "/a", "/", "/m"),
			ReferrerSource: slot(i, "zref", "aref", "", "mref"),
			UTMSource:      []string{"", "hn"}[i%2],
			UTMMedium:      []string{"", "social", ""}[i%3],
			UTMCampaign:    slot(i, "zc", "ac", "", "mc"),
			Platform:       slot(i, "zp", "ap", "web", "mp"),
			OS:             []string{"linux", "ios"}[i%2],
			OSVersion:      slot(i, "z1", "a1", "17.2", "m1"),
			Browser:        []string{"firefox", "chrome"}[i%2],
			BrowserVersion: slot(i, "z9", "a9", "127", "m9"),
			AppVersion:     slot(i, "z.1", "a.1", "", "m.1"),
			Device:         []string{"desktop", "mobile", "unknown"}[i%3],
			DeviceModel:    slot(i, "zmodel", "amodel", "", "mmodel"),
			DisplayWidth:   []int{1920, 390, 0, 1440}[i%4],
			DisplayHeight:  []int{1080, 844, 0, 900}[i%4],
			Country:        slot(i, "ZW", "AR", "US", "MX"),
			Consent:        []store.Consent{store.ConsentGiven, store.ConsentNone, store.ConsentUnknown}[i%3],
			BrowserLocale:  slot(i, "zu", "af", "en-US", ""),
			AppLocale:      []string{"", "en", "de"}[i%3],
		}
		evs = append(evs, e)
	}
	values := []float64{100, 340, 0, 5000, 12.5}
	for i := 0; i < 24; i++ {
		attrs := map[string]string{"step": fmt.Sprint(i % 5)}
		if plan := slot(i, "zplan", "aplan", "pro", ""); plan != "" {
			attrs["plan"] = plan
		}
		evs = append(evs, store.Event{Family: store.FamilyProduct, ID: fmt.Sprintf("0192f1a2-7c3b-7d4e-8002-%012x", i),
			ProjectID: p1, EventName: []string{"signup", "signup", "export"}[i%3], TS: at(i),
			ActorID: actors[(i*5)%len(actors)], ActorKind: store.ActorUser, UserID: actors[i%len(actors)],
			GroupID: groups[i%len(groups)], Kind: slot(i, "zz", "aa", "web", "mm"),
			Path: slot(i, "/z", "/a", "/", "/m"), UTMSource: slot(i, "zs", "as", "", "ms"),
			Platform: slot(i, "zp", "ap", "web", "mp"), OS: slot(i, "zos", "aos", "macos", "mos"),
			AppVersion: slot(i, "z.1", "a.1", "", "m.1"), Browser: slot(i, "zb", "ab", "chrome", "mb"),
			Device: []string{"desktop", "mobile"}[i%2], BrowserLocale: slot(i, "zu", "af", "en-US", ""),
			AppLocale: []string{"", "en"}[i%2], Country: slot(i, "ZW", "AR", "US", "MX"),
			Attributes: attrs})
		v := values[i%len(values)]
		m := store.Event{Family: store.FamilyMeasures, ID: fmt.Sprintf("0192f1a2-7c3b-7d4e-8003-%012x", i),
			ProjectID: p1, EventName: []string{"checkout", "checkout", "$lcp"}[i%3], Measure: store.MeasureTime,
			Value: &v, SampleRate: []float64{1, 0.5, 0.25}[i%3], TS: at(i),
			ActorID: actors[(i*5)%len(actors)], ActorKind: store.ActorConnection,
			Path: slot(i, "/z", "/a", "/", "/m"), Browser: slot(i, "zb", "ab", "chrome", "mb"),
			Country: slot(i, "ZW", "AR", "US", "MX"), Platform: "web",
			Attributes: map[string]string{"endpoint": slot(i, "zend", "aend", "/api", "mend")}}
		if plan := slot(i, "zplan", "aplan", "pro", ""); plan != "" {
			m.Attributes["plan"] = plan
		}
		evs = append(evs, m)
	}
	// The second project declares nothing: its product and measure rows
	// roll up the system keys only.
	for i := 0; i < 6; i++ {
		v := values[i%len(values)]
		evs = append(evs,
			store.Event{Family: store.FamilyViews, ID: fmt.Sprintf("0192f1a2-7c3b-7d4e-8004-%012x", i),
				ProjectID: p2, TS: at(i * 3), ActorID: actors[i%len(actors)], ActorKind: store.ActorConnection,
				Kind: "web", Path: []string{"/x", "/y"}[i%2], Country: "DE"},
			store.Event{Family: store.FamilyProduct, ID: fmt.Sprintf("0192f1a2-7c3b-7d4e-8005-%012x", i),
				ProjectID: p2, EventName: "ping", TS: at(i), ActorID: actors[i%len(actors)],
				OS: []string{"ios", "android", "ios"}[i%3], Attributes: map[string]string{"plan": "x"}},
			store.Event{Family: store.FamilyMeasures, ID: fmt.Sprintf("0192f1a2-7c3b-7d4e-8006-%012x", i),
				ProjectID: p2, EventName: "boot", Measure: store.MeasureTime, Value: &v, SampleRate: 1,
				TS: at(i), ActorID: actors[i%len(actors)], DeviceModel: []string{"Pixel 8", ""}[i%2]})
	}
	// The next day stays raw; today is left to ingest.
	next := base.AddDate(0, 0, 1)
	for i := 0; i < 4; i++ {
		v := values[i]
		evs = append(evs,
			store.Event{Family: store.FamilyViews, ID: fmt.Sprintf("0192f1a2-7c3b-7d4e-8007-%012x", i),
				ProjectID: p1, TS: next, ActorID: actors[i], Kind: "web", Path: "/"},
			store.Event{Family: store.FamilyProduct, ID: fmt.Sprintf("0192f1a2-7c3b-7d4e-8008-%012x", i),
				ProjectID: p1, EventName: "signup", TS: next, ActorID: actors[i], Host: "example.com",
				Attributes: map[string]string{"plan": []string{"pro", "free"}[i%2], "seen": "1"}},
			store.Event{Family: store.FamilyMeasures, ID: fmt.Sprintf("0192f1a2-7c3b-7d4e-8009-%012x", i),
				ProjectID: p1, EventName: "checkout", Measure: store.MeasureTime, Value: &v, SampleRate: 1,
				TS: next, ActorID: actors[i], Attributes: map[string]string{"endpoint": "/api"}})
	}
	evs = append(evs, store.Event{Family: store.FamilyProduct, ID: "0192f1a2-7c3b-7d4e-800a-000000000000",
		ProjectID: p1, EventName: "signup", TS: base.AddDate(0, 0, 2), ActorID: actors[0],
		Attributes: map[string]string{"plan": "pro"}})
	for i := range evs {
		if evs[i].ReceivedAt.IsZero() {
			evs[i].ReceivedAt = evs[i].TS
		}
		if evs[i].ActorKind == "" {
			evs[i].ActorKind = store.ActorConnection
		}
	}
	if err := db.writeTestEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// rollupTables dumps every table the rollups and the usage pass write, each
// as its rows rendered with their SQL types, sorted.
func rollupTables(t *testing.T, db *DB) map[string][]string {
	t.Helper()
	rows, err := db.db.Query(`SELECT name FROM sqlite_schema WHERE type = 'table'
		AND (name LIKE 'agg\_views\_%' ESCAPE '\' OR name LIKE 'agg\_product\_%' ESCAPE '\'
		     OR name LIKE 'agg\_measures\_%' ESCAPE '\' OR name IN ('usage_attributes', 'usage_history'))`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, table := range tables {
		got := snapshotRows(t, db, `SELECT * FROM "`+table+`"`)
		sort.Strings(got)
		out[table] = got
	}
	out["events"] = snapshotRows(t, db, `SELECT family, project_id, day, COUNT(*) FROM events GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`)
	return out
}

// diffTables reports each table whose rows differ between want and got.
func diffTables(t *testing.T, stage string, want, got map[string][]string) {
	t.Helper()
	for table, rows := range want {
		if !reflect.DeepEqual(rows, got[table]) {
			t.Errorf("%s: %s differs:\nwant %v\ngot  %v", stage, table, rows, got[table])
		}
	}
	for table := range got {
		if _, ok := want[table]; !ok {
			t.Errorf("%s: %s only in got", stage, table)
		}
	}
}

// The rollups and the usage pass read events on stored ids (events
// encoding spec D14); what they write must be what they wrote when they
// read the decoding raw_* views: the same agg_*, usage_attributes and
// usage_history rows, byte for byte and type for type. Two copies of one
// fixture: A runs the frozen raw_* code (rollup_legacy_test.go), B the
// store's.
func TestRollupsOnIDsMatchTheDecodedRollups(t *testing.T) {
	ctx := context.Background()
	open := func() (*DB, int64) {
		db := newTestDB(t)
		var keys []string
		seen := map[string]bool{}
		for _, k := range rollupKeys {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		p1 := seedDeclaredProject(t, db, keys)
		if err := db.SetMeta(ctx, "attributes_top_n", "3"); err != nil {
			t.Fatal(err)
		}
		seedRollupDay(t, db, p1)
		return db, p1
	}
	a, p1 := open()
	b, _ := open()
	if diff := rollupTables(t, a); !reflect.DeepEqual(diff, rollupTables(t, b)) {
		t.Fatal("the two copies differ before any pass")
	}
	now := time.Date(2026, 8, 12, 3, 0, 0, 0, time.UTC)
	d := day(rollupDay)

	if err := legacyRecordUsageHistory(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	if err := b.RecordUsageHistory(ctx, now); err != nil {
		t.Fatal(err)
	}
	want := rollupTables(t, a)
	diffTables(t, "usage pass on raw days", want, rollupTables(t, b))
	var folded, received int
	if err := a.db.QueryRow(`SELECT (SELECT SUM(value) FROM usage_history WHERE key = ?),
		(SELECT COUNT(*) FROM usage_attributes WHERE max_values > 1)`, store.StatAttributeValuesFolded).
		Scan(&folded, &received); err != nil {
		t.Fatal(err)
	}
	if folded == 0 || received == 0 {
		t.Errorf("fixture: %d values folded and %d keys with several values; both must be exercised", folded, received)
	}

	for _, p := range []int64{p1, p1 + 1} {
		if err := legacyAggregateViewDay(ctx, a, p, d, 3); err != nil {
			t.Fatal(err)
		}
		if err := legacyAggregateProductDay(ctx, a, p, d, rollupKeys, 3); err != nil {
			t.Fatal(err)
		}
		if err := legacyAggregateMeasureDay(ctx, a, p, d, rollupKeys, 3); err != nil {
			t.Fatal(err)
		}
		if err := b.AggregateViewDay(ctx, p, d, 3); err != nil {
			t.Fatal(err)
		}
		if err := b.AggregateProductDay(ctx, p, d, rollupKeys, 3); err != nil {
			t.Fatal(err)
		}
		if err := b.AggregateMeasureDay(ctx, p, d, rollupKeys, 3); err != nil {
			t.Fatal(err)
		}
	}
	want = rollupTables(t, a)
	diffTables(t, "rollup", want, rollupTables(t, b))
	for table, rows := range want {
		if strings.HasPrefix(table, "agg_") && len(rows) == 0 {
			t.Errorf("fixture: %s has no rows", table)
		}
	}
	// The fixture reaches the cases it claims: an (other) row in a
	// breakdown of each family and in the kinds, and the tie at the cap
	// kept by text, against the ids' order.
	var others, tie int
	if err := a.db.QueryRow(`SELECT
		  (SELECT COUNT(*) FROM agg_views_paths WHERE path = '(other)')
		+ (SELECT COUNT(*) FROM agg_views_daily WHERE kind = '(other)')
		+ (SELECT COUNT(*) FROM agg_product_attrs WHERE attr_key = 'plan' AND attr_value = '(other)')
		+ (SELECT COUNT(*) FROM agg_measures_attrs WHERE attr_key = 'endpoint' AND attr_value = '(other)'),
		  (SELECT COUNT(*) FROM agg_views_paths WHERE path = '/a')
		- (SELECT COUNT(*) FROM agg_views_paths WHERE path = '/z')`).Scan(&others, &tie); err != nil {
		t.Fatal(err)
	}
	var zFirst bool
	if err := b.db.QueryRow(`SELECT (SELECT id FROM strings WHERE value = '/z') < (SELECT id FROM strings WHERE value = '/a')`).
		Scan(&zFirst); err != nil {
		t.Fatal(err)
	}
	if others < 4 || tie != 1 || !zFirst {
		t.Errorf("fixture: %d (other) rows (want 4 or more), paths keep /a over /z: %v, /z has the lower id: %v",
			others, tie == 1, zFirst)
	}

	// A second pass reads the rolled-up day from the aggregates.
	if err := legacyRecordUsageHistory(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	if err := b.RecordUsageHistory(ctx, now); err != nil {
		t.Fatal(err)
	}
	diffTables(t, "usage pass after the rollup", rollupTables(t, a), rollupTables(t, b))
}

// A client session id made of an even number of digits, such as "12", is
// stored as a tagged blob (spec D3), so it is never the same session as the
// actor's twelfth gap-split session, whose key is the text "12". The live
// half of v_views_daily splits sessions on the stored ids; the rollup must
// split them the same way, so the day reads the same before and after it
// rolls up.
func TestViewsDailySameAcrossRollupWithADigitSessionID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	var views []store.Event
	// Twelve views 40 minutes apart: twelve gap sessions, keyed "1" to
	// "12"; then one view in the client session "12", inside the twelfth.
	for i := 0; i < 12; i++ {
		views = append(views, store.Event{Family: store.FamilyViews, ID: fmt.Sprintf("g%02d", i),
			TS: base.Add(time.Duration(i) * 40 * time.Minute), ActorID: "a", Path: "/"})
	}
	views = append(views, store.Event{Family: store.FamilyViews, ID: "s12",
		TS: base.Add(11*40*time.Minute + 5*time.Minute), ActorID: "a", Path: "/", SessionID: "12"})
	seedViews(t, db, views...)
	const q = `SELECT * FROM v_views_daily ORDER BY 1, 2, 3`
	before := snapshotRows(t, db, q)
	var sessions int
	if err := db.db.QueryRow(`SELECT sessions FROM v_views_daily WHERE day = '2026-08-10'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 13 {
		t.Fatalf("live half: %d sessions, want 13 (twelve gap sessions and the client's)", sessions)
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10"), defaultAttrsTopN); err != nil {
		t.Fatal(err)
	}
	if after := snapshotRows(t, db, q); !reflect.DeepEqual(before, after) {
		t.Errorf("v_views_daily changed across the rollup:\nbefore %v\nafter  %v", before, after)
	}
}
