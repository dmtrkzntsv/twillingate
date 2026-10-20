// The reads of the events table: `twillingate events import` copies
// SQLite's raw rows into the event files, and migration 042, which drops
// the table, refuses to run before it has. Everything here is valid only on
// a schema through LastEventsVersion; internal/sqlcheck leaves this file
// out (it prepares against the current schema) and the tests run it
// against a database at that version.

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// LastEventsVersion is the last migration with the events table: 042
// drops it.
const LastEventsVersion = 41

// EventsImportedKey is the meta row `twillingate events import` writes
// once every raw row is in the event files (app.ImportEvents).
const EventsImportedKey = "events_imported"

// eventsNotImported is migration 042's refusal while events holds rows
// and EventsImportedKey is unset.
const eventsNotImported = "migration 042 drops the events table, which still holds raw events the event files may lack: " +
	"stop the service, run `twillingate events import`, then start again"

// checkEventsImported is migration 042's pre-check (preChecks): it refuses
// while events holds a row and the import has not run. A database with no
// events table passes.
func checkEventsImported(ctx context.Context, conn *sql.Conn) error {
	var table bool
	if err := conn.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'events')`).Scan(&table); err != nil {
		return err
	}
	if !table {
		return nil
	}
	var held, imported bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events)`).Scan(&held); err != nil {
		return err
	}
	if !held {
		return nil
	}
	if err := conn.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM meta WHERE key = ? AND value <> '')`, EventsImportedKey).Scan(&imported); err != nil {
		return err
	}
	if !imported {
		return errors.New(eventsNotImported)
	}
	return nil
}

// EventsDropped reports whether migration 042 has run, so the events
// table is gone. A file nothing has migrated yet has not.
func (d *DB) EventsDropped(ctx context.Context) (bool, error) {
	var migrated bool
	if err := d.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations')`).Scan(&migrated); err != nil {
		return false, err
	}
	if !migrated {
		return false, nil
	}
	var dropped bool
	err := d.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version > ?)`, LastEventsVersion).Scan(&dropped)
	return dropped, err
}

// ViewDaysBefore, ProductDaysBefore and MeasureDaysBefore list, in order,
// the days before before on which the project has raw rows of the family.
func (d *DB) ViewDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, store.FamilyViews, projectID, before)
}

func (d *DB) ProductDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, store.FamilyProduct, projectID, before)
}

func (d *DB) MeasureDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, store.FamilyMeasures, projectID, before)
}

func (d *DB) daysBefore(ctx context.Context, family store.Family, projectID int64, before civil.Date) ([]civil.Date, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT DISTINCT day FROM events WHERE family = ? AND project_id = ? AND day < ? ORDER BY 1`,
		string(family), projectID, before.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []civil.Date
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		day, err := civil.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, day)
	}
	return out, rows.Err()
}

// rawEventsSQL reads one project's day of raw rows, with the names
// identities holds for each row's user and group. The family list makes
// it a seek on the events key.
const rawEventsSQL = `SELECT e.id, e.family, e.event_name, e.ts, e.received_at, e.kind,
	e.actor_id, e.actor_kind, e.user_id, e.group_id, e.session_id,
	e.host, e.path, e.referrer_source, e.utm_source, e.utm_medium, e.utm_campaign,
	e.platform, e.os, e.os_version, e.os_name, e.browser, e.browser_version, e.browser_locale,
	e.app_version, e.app_locale, e.device, e.device_model, e.display_width, e.display_height,
	e.country, e.consent, e.attributes, e.value, e.measure, e.sample_rate,
	coalesce(u.name, ''), coalesce(g.name, '')
FROM events e
LEFT JOIN identities u ON e.user_id <> '' AND u.project_id = e.project_id AND u.kind = 'user' AND u.id = e.user_id
LEFT JOIN identities g ON e.group_id <> '' AND g.project_id = e.project_id AND g.kind = 'group' AND g.id = e.group_id
WHERE e.family IN ('views', 'product', 'measures') AND e.project_id = ? AND e.day = ?
ORDER BY e.ts, e.id`

// RawEvents returns the project's raw rows of day, in (ts, id) order, as
// the event files hold an event (raw-first D19, PR 5c). A row stored
// without received_at has a zero ReceivedAt. The three fields SQLite never stored are what it
// can say of them: InstallID is the actor id where the install was the
// actor (an $install_id a $user_id outranked was not kept), and UserName
// and GroupName are the latest names identities holds for UserID and
// GroupID, not the ones sent with the row.
//
// The day is read whole before it returns, so no cursor stays open on the
// store's one connection while the caller writes files.
func (d *DB) RawEvents(ctx context.Context, projectID int64, day civil.Date) ([]store.Event, error) {
	rows, err := d.db.QueryContext(ctx, rawEventsSQL, projectID, day.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Event
	for rows.Next() {
		e := store.Event{ProjectID: projectID}
		var family, ts, received, attrs string
		var consent sql.NullInt64
		var value sql.NullFloat64
		if err := rows.Scan(&e.ID, &family, &e.EventName, &ts, &received, &e.Kind,
			&e.ActorID, &e.ActorKind, &e.UserID, &e.GroupID, &e.SessionID,
			&e.Host, &e.Path, &e.ReferrerSource, &e.UTMSource, &e.UTMMedium, &e.UTMCampaign,
			&e.Platform, &e.OS, &e.OSVersion, &e.OSName, &e.Browser, &e.BrowserVersion, &e.BrowserLocale,
			&e.AppVersion, &e.AppLocale, &e.Device, &e.DeviceModel, &e.DisplayWidth, &e.DisplayHeight,
			&e.Country, &consent, &attrs, &value, &e.Measure, &e.SampleRate,
			&e.UserName, &e.GroupName); err != nil {
			return nil, err
		}
		e.Family = store.Family(family)
		if e.TS, err = time.Parse(time.RFC3339, ts); err != nil {
			return nil, fmt.Errorf("event %s ts: %w", e.ID, err)
		}
		if received != "" {
			if e.ReceivedAt, err = time.Parse(time.RFC3339, received); err != nil {
				return nil, fmt.Errorf("event %s received_at: %w", e.ID, err)
			}
		}
		if consent.Valid {
			e.Consent = store.ConsentNone
			if consent.Int64 == 1 {
				e.Consent = store.ConsentGiven
			}
		}
		if value.Valid {
			v := value.Float64
			e.Value = &v
		}
		if e.Attributes, err = rawAttributes(attrs); err != nil {
			return nil, fmt.Errorf("event %s attributes: %w", e.ID, err)
		}
		if e.ActorKind == store.ActorInstall {
			e.InstallID = e.ActorID
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// rawAttributes reads an attributes blob: a string value as is, any other
// JSON value (a number or a boolean an older row may hold) as its text,
// null dropped. An empty object is nil, as an event built without
// attributes has them.
func rawAttributes(blob string) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return nil, err
	}
	var out map[string]string
	for k, v := range raw {
		if bytes.Equal(v, []byte("null")) {
			continue
		}
		s := string(v)
		if len(v) > 0 && v[0] == '"' {
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, err
			}
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = s
	}
	return out, nil
}
