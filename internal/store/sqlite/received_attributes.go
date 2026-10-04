package sqlite

import (
	"context"
	"database/sql"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// declarableValues pairs each reserved key a project may declare
// (store.DeclarableAttributes) with the event's column value; empty when
// the event did not carry it.
func declarableValues(e store.Event) [9]struct{ key, value string } {
	return [9]struct{ key, value string }{
		{"$host", e.Host}, {"$path", e.Path}, {"$referrer", e.ReferrerSource},
		{"$utm_source", e.UTMSource}, {"$utm_medium", e.UTMMedium}, {"$utm_campaign", e.UTMCampaign},
		{"$os_version", e.OSVersion}, {"$browser_version", e.BrowserVersion}, {"$device_model", e.DeviceModel},
	}
}

type receivedKey struct {
	project int64
	day     string
	key     string
}

// receivedCounts sums, over a batch's inserted product and measure rows,
// how many carried each key per project and day.
type receivedCounts map[receivedKey]int64

func (c receivedCounts) add(e store.Event, day string) {
	if e.Family == store.FamilyViews {
		return
	}
	for k := range e.Attributes {
		c[receivedKey{e.ProjectID, day, k}]++
	}
	for _, kv := range declarableValues(e) {
		if kv.value != "" {
			c[receivedKey{e.ProjectID, day, kv.key}]++
		}
	}
}

// write upserts the batch's counts: one statement per distinct
// (project, day, key), however many events carried it.
func (c receivedCounts) write(ctx context.Context, tx *sql.Tx) error {
	if len(c) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO received_attributes (project_id, day, attr_key, events)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (project_id, day, attr_key) DO UPDATE SET events = events + excluded.events`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for k, n := range c {
		if _, err := stmt.ExecContext(ctx, k.project, k.day, k.key, n); err != nil {
			return err
		}
	}
	return nil
}
