package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const tsFormat = "2006-01-02T15:04:05Z"

// WriteEvents stores a batch of raw rows, views, product events and
// measures alike, in the one raw table. INSERT OR IGNORE: with
// client-supplied UUIDv7 ids, a batch retried after a timeout that
// actually succeeded is a no-op —
// duplicates are detected on the whole primary key (family, project_id,
// day, id), so a retry is a no-op only because it repeats all four
// unchanged. The one gap: a retry of an event whose ts was clamped, landing
// on the other side of midnight, clamps to a different day and is stored
// twice (deploy/UPGRADES.md).
//
// A row whose family is none of views, product and measures refuses the
// whole batch before anything is written: every raw_* view would miss it,
// so it would never be read, rolled up or pruned.
//
// value is NULL outside measures (a nil Value), and a SampleRate of 0
// means unset and is stored as 1.
//
// Each inserted product or measure row also counts its attribute keys into
// received_attributes, in the same transaction.
func (d *DB) WriteEvents(ctx context.Context, evs []store.Event) error {
	if len(evs) == 0 {
		return nil
	}
	for _, e := range evs {
		switch e.Family {
		case store.FamilyViews, store.FamilyProduct, store.FamilyMeasures:
		default:
			return fmt.Errorf("event %s: family %q is not %q, %q or %q",
				e.ID, e.Family, store.FamilyViews, store.FamilyProduct, store.FamilyMeasures)
		}
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO events
			(id, project_id, family, event_name, ts, day, received_at, kind,
			 actor_id, actor_kind, user_id, group_id, session_id,
			 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
			 platform, os, os_version, os_name, browser, browser_version, browser_locale,
			 app_version, app_locale, device, device_model, display_width, display_height,
			 country, consent, attributes, value, measure, sample_rate)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		counts := receivedCounts{}
		for _, e := range evs {
			attrs := e.Attributes
			if attrs == nil {
				attrs = map[string]string{}
			}
			blob, err := json.Marshal(attrs)
			if err != nil {
				return fmt.Errorf("event %s attributes: %w", e.ID, err)
			}
			rate := e.SampleRate
			if rate == 0 {
				rate = 1
			}
			day := e.TS.UTC().Format("2006-01-02")
			res, err := stmt.ExecContext(ctx, e.ID, e.ProjectID, string(e.Family), e.EventName,
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
			// INSERT OR IGNORE: a duplicate id (a retried batch) inserts
			// nothing and so counts nothing.
			if n, err := res.RowsAffected(); err == nil && n == 1 {
				counts.add(e, day)
			}
		}
		return counts.write(ctx, tx)
	})
}

// UpsertIdentities records display names, latest write wins.
// identities.last_seen_day is maintained by the daily pass, not here.
func (d *DB) UpsertIdentities(ctx context.Context, ids []store.Identity) error {
	if len(ids) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO identities
			(project_id, kind, id, name, updated_at)
			VALUES (?,?,?,?,datetime('now'))
			ON CONFLICT(project_id, kind, id) DO UPDATE SET
			  name=excluded.name, updated_at=excluded.updated_at`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, i := range ids {
			if i.ID == "" || i.Name == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, i.ProjectID, i.Kind, i.ID, i.Name); err != nil {
				return fmt.Errorf("identity %s/%s: %w", i.Kind, i.ID, err)
			}
		}
		return nil
	})
}

func (d *DB) ProjectIDs(ctx context.Context) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// tx runs fn in a transaction with commit/rollback handling; shared by all
// sqlite write paths.
func (d *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
