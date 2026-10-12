package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// writeTextEvents is WriteEvents as it was before migration 041 encoded
// the raw table: every column stored as text, the event rename looked up
// in SQL, and each received key's catalog row added beside its usage row.
// Migration tests that seed through ingest on a database pinned below 41
// use it, since today's WriteEvents needs 041's dictionaries; a database
// below 40 needs withWriteTables first, as the old write path did.
func (d *DB) writeTextEvents(ctx context.Context, evs []store.Event) error {
	var fresh []receivedKey
	var gen uint64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO events
			(id, project_id, family, event_name, ts, day, received_at, kind,
			 actor_id, actor_kind, user_id, group_id, session_id,
			 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
			 platform, os, os_version, os_name, browser, browser_version, browser_locale,
			 app_version, app_locale, device, device_model, display_width, display_height,
			 country, consent, attributes, value, measure, sample_rate)
			VALUES (?,?,?,
			 COALESCE((SELECT to_name FROM rules WHERE project_id=? AND kind='event' AND family=? AND name=? AND action='rename'), ?),
			 ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		rules, err := attributeRules(ctx, tx, evs)
		if err != nil {
			return err
		}
		received := receivedKeys{}
		for _, e := range evs {
			if rs, ok := rules[e.ProjectID]; ok && e.Family != store.FamilyViews {
				e.Attributes, _ = store.MapAttributes(e.Attributes, rs)
			}
			attrs := e.Attributes
			if attrs == nil {
				attrs = map[string]string{}
			}
			blob, err := json.Marshal(attrs)
			if err != nil {
				return err
			}
			rate := e.SampleRate
			if rate == 0 {
				rate = 1
			}
			day := e.TS.UTC().Format("2006-01-02")
			res, err := stmt.ExecContext(ctx, e.ID, e.ProjectID, string(e.Family),
				e.ProjectID, string(e.Family), e.EventName, e.EventName,
				e.TS.UTC().Format(tsFormat), day,
				e.ReceivedAt.UTC().Format(tsFormat), e.Kind,
				e.ActorID, e.ActorKind, e.UserID, e.GroupID, e.SessionID,
				e.Host, e.Path, e.ReferrerSource, e.UTMSource, e.UTMMedium, e.UTMCampaign,
				e.Platform, e.OS, e.OSVersion, e.OSName, e.Browser, e.BrowserVersion, e.BrowserLocale,
				e.AppVersion, e.AppLocale, e.Device, e.DeviceModel, e.DisplayWidth, e.DisplayHeight,
				e.Country, e.Consent, string(blob), e.Value, e.Measure, rate)
			if err != nil {
				return fmt.Errorf("event %s: %w", e.ID, err)
			}
			if n, err := res.RowsAffected(); err == nil && n == 1 {
				received.add(e, day)
			}
		}
		fresh, gen = d.seen.unseen(received)
		if err := writeReceived(ctx, tx, fresh); err != nil {
			return err
		}
		for _, k := range fresh {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_attributes (project_id, key)
				SELECT ?1, ?2 WHERE EXISTS (SELECT 1 FROM projects WHERE id = ?1)`, k.project, k.key); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		d.seen.remember(fresh, gen)
	}
	return err
}

// writeTestEvents is WriteEvents for tests that write events of project
// ids they never registered: ingest drops the rows of a project that does
// not exist (insertEvents), so it adds a projects row for each id first.
// A test of that drop calls WriteEvents itself.
func (d *DB) writeTestEvents(ctx context.Context, evs []store.Event) error {
	for _, e := range evs {
		if _, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO projects (id, name) VALUES (?, ?)`,
			e.ProjectID, fmt.Sprintf("project %d", e.ProjectID)); err != nil {
			return err
		}
	}
	return d.WriteEvents(ctx, evs)
}

// textRollup runs rollup, today's code for one family's day, on a
// database pinned below 41, where events stores the family as text: its
// own delete of the day's raw rows names the family's code and so matches
// nothing there, and its delete of blocked names reads strings, which 041
// creates (added here empty: no row is encoded). It then deletes the day's
// raw rows as the rollup did before 041.
func textRollup(t *testing.T, db *DB, family store.Family, projectID int64, day civil.Date, rollup func() error) {
	t.Helper()
	execAll(t, db, `CREATE TABLE IF NOT EXISTS strings (
		id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT NOT NULL UNIQUE, last_seen_day TEXT NOT NULL)`)
	if err := rollup(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM events WHERE family=? AND project_id=? AND day=?`,
		string(family), projectID, day.String()); err != nil {
		t.Fatal(err)
	}
}
