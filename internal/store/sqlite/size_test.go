package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// textEventsDDL is the events table as migrations 023 and 024 left it and
// 041 replaced it: every column text, attributes the JSON ingest used to
// write.
const textEventsDDL = `CREATE TABLE events_text (
	family TEXT NOT NULL, project_id INTEGER NOT NULL, day TEXT NOT NULL, id TEXT NOT NULL,
	event_name TEXT NOT NULL, ts TEXT NOT NULL, received_at TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT '', actor_id TEXT NOT NULL, actor_kind TEXT NOT NULL DEFAULT '',
	user_id TEXT NOT NULL DEFAULT '', group_id TEXT NOT NULL DEFAULT '', session_id TEXT NOT NULL DEFAULT '',
	host TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '', referrer_source TEXT NOT NULL DEFAULT '',
	utm_source TEXT NOT NULL DEFAULT '', utm_medium TEXT NOT NULL DEFAULT '', utm_campaign TEXT NOT NULL DEFAULT '',
	platform TEXT NOT NULL DEFAULT 'unknown', os TEXT NOT NULL DEFAULT '', os_version TEXT NOT NULL DEFAULT '',
	os_name TEXT NOT NULL DEFAULT '', browser TEXT NOT NULL DEFAULT '', browser_version TEXT NOT NULL DEFAULT '',
	browser_locale TEXT NOT NULL DEFAULT '', app_version TEXT NOT NULL DEFAULT '', app_locale TEXT NOT NULL DEFAULT '',
	device TEXT NOT NULL DEFAULT '', device_model TEXT NOT NULL DEFAULT '',
	display_width INTEGER NOT NULL DEFAULT 0, display_height INTEGER NOT NULL DEFAULT 0,
	country TEXT NOT NULL DEFAULT '', consent INTEGER, attributes TEXT NOT NULL DEFAULT '{}',
	value REAL, measure TEXT NOT NULL DEFAULT '', sample_rate REAL NOT NULL DEFAULT 1,
	PRIMARY KEY (family, project_id, day, id)
) WITHOUT ROWID`

// TestEncodedRowsAreSmall pins the encoding's point: the same prod-shaped
// batch stored in the encoded layout takes under half the bytes it takes in
// the text layout it replaced (dbstat counts each table's pages, its
// primary key b-tree included since the table is WITHOUT ROWID).
func TestEncodedRowsAreSmall(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`, textEventsDDL)
	// 2,000 product events shaped like econumo's: 14 attributes of ~10-char
	// keys and ~5-char values drawn from small sets, UUID ids, 32-hex actors.
	evs := prodShapedEvents(2000)
	for i, e := range evs {
		if len(e.Attributes) != 14 {
			t.Fatalf("event %d carries %d attributes, want 14", i, len(e.Attributes))
		}
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := insertTextEvents(ctx, db, evs); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"events", "events_text"} {
		var rows int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != len(evs) {
			t.Fatalf("%s holds %d rows, want %d", table, rows, len(evs))
		}
	}
	size := func(table string) int64 {
		var n int64
		if err := db.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE name = ?`, table).Scan(&n); err != nil {
			t.Skipf("dbstat unavailable: %v", err)
		}
		return n
	}
	enc, text := size("events"), size("events_text")
	t.Logf("encoded: %d bytes = %d per row; text: %d bytes = %d per row; ratio %.3f",
		enc, enc/int64(len(evs)), text, text/int64(len(evs)), float64(enc)/float64(text))
	var dicts int64
	if err := db.db.QueryRow(`SELECT COALESCE(SUM(d.pgsize), 0) FROM dbstat d JOIN sqlite_schema s ON s.name = d.name
		WHERE s.tbl_name IN ('strings', 'attribute_values')`).Scan(&dicts); err == nil {
		t.Logf("with the dictionaries (%d bytes): %d per row, ratio %.3f",
			dicts, (enc+dicts)/int64(len(evs)), float64(enc+dicts)/float64(text))
	}
	if r, err := db.db.Query(`SELECT d.name, SUM(d.pgsize) FROM dbstat d JOIN sqlite_schema s ON s.name = d.name
		WHERE s.tbl_name IN ('events', 'strings', 'attribute_values') GROUP BY d.name ORDER BY 2 DESC`); err == nil {
		for r.Next() {
			var name string
			var n int64
			if r.Scan(&name, &n) == nil {
				t.Logf("  %s: %d bytes", name, n)
			}
		}
		r.Close()
	}
	if enc*2 > text {
		t.Errorf("encoded events take %d bytes, text layout %d: want at most half", enc, text)
	}
}

// insertTextEvents stores evs in events_text the way ingest wrote them
// before migration 041.
func insertTextEvents(ctx context.Context, db *DB, evs []store.Event) error {
	stmt, err := db.db.PrepareContext(ctx, `INSERT INTO events_text
		(family, project_id, day, id, event_name, ts, received_at, kind, actor_id, actor_kind,
		 user_id, group_id, session_id, host, path, referrer_source, utm_source, utm_medium, utm_campaign,
		 platform, os, os_version, os_name, browser, browser_version, browser_locale,
		 app_version, app_locale, device, device_model, display_width, display_height,
		 country, consent, attributes, value, measure, sample_rate)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range evs {
		blob, err := json.Marshal(e.Attributes)
		if err != nil {
			return err
		}
		rate := e.SampleRate
		if rate == 0 {
			rate = 1
		}
		if _, err := stmt.ExecContext(ctx, string(e.Family), e.ProjectID, e.TS.UTC().Format("2006-01-02"), e.ID,
			e.EventName, e.TS.UTC().Format(tsFormat), e.ReceivedAt.UTC().Format(tsFormat), e.Kind,
			e.ActorID, e.ActorKind, e.UserID, e.GroupID, e.SessionID,
			e.Host, e.Path, e.ReferrerSource, e.UTMSource, e.UTMMedium, e.UTMCampaign,
			e.Platform, e.OS, e.OSVersion, e.OSName, e.Browser, e.BrowserVersion, e.BrowserLocale,
			e.AppVersion, e.AppLocale, e.Device, e.DeviceModel, e.DisplayWidth, e.DisplayHeight,
			e.Country, e.Consent, string(blob), e.Value, e.Measure, rate); err != nil {
			return err
		}
	}
	return nil
}

// prodShapedEvents returns n deterministic product events: 14 attributes
// of 10-character keys whose values (about five characters) come from 3 to
// 40 choices per key, 50 paths, UUID ids from a counter, and 32-hex actors,
// users and sessions drawn from pools, over about two days (three calendar
// days).
func prodShapedEvents(n int) []store.Event {
	keys := make([]string, 14)
	cards := make([]int, 14)
	for k := range keys {
		keys[k] = fmt.Sprintf("attr_k_%03d", k) // 10 characters
		cards[k] = 3 + (k*37)%38                // 3..40, spread over the keys
	}
	names := []string{"txn_created", "acct_opened", "budget_set", "report_run", "login_ok", "export_csv"}
	hex32 := func(seed int) string {
		return fmt.Sprintf("%016x%016x", uint64(seed)*0x9e3779b97f4a7c15, uint64(seed)*0xc2b2ae3d27d4eb4f)
	}
	base := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	out := make([]store.Event, n)
	for i := range out {
		attrs := make(map[string]string, len(keys))
		for k, key := range keys {
			attrs[key] = fmt.Sprintf("v%d_%02d", k%10, (i*(k+3)+i/7)%cards[k])
		}
		ts := base.Add(time.Duration(i) * 97 * time.Second)
		out[i] = store.Event{
			Family:     store.FamilyProduct,
			ID:         fmt.Sprintf("0192f1a2-7c3b-7d4e-%04x-%012x", i%0xffff, uint64(i)*2654435761),
			ProjectID:  1,
			EventName:  names[i%len(names)],
			TS:         ts,
			ReceivedAt: ts.Add(time.Second),
			Kind:       "web",
			ActorID:    hex32(i % 400),
			ActorKind:  store.ActorConnection,
			UserID:     hex32(1000 + i%150),
			SessionID:  hex32(5000 + i/6),
			Path:       fmt.Sprintf("/section/%02d/page", i%50),
			Host:       "app.example.com",
			Platform:   "web",
			OS:         []string{"macOS", "Windows", "Linux", "iOS", "Android"}[i%5],
			Browser:    []string{"Chrome", "Safari", "Firefox"}[i%3],
			AppVersion: "3.14.2",
			Country:    []string{"US", "DE", "GB", "FR", "NL", "PL"}[i%6],
			Attributes: attrs,
		}
	}
	return out
}
