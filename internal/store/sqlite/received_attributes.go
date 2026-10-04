package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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

// receivedSQL lists every (project, day, family, event, measure, key,
// value) the raw product and measure rows before ?1 carry: their JSON
// attributes and the declarable reserved columns that are set. Each raw row
// is read once: jsonb_patch lays the set columns over the attributes as one
// object (a null in a merge patch removes the key, so an empty column adds
// nothing) and json_each walks it; one arm per column instead re-read every
// raw page ten times (the JSONB forms skip rendering the merged text). r.
// qualifies the row's columns, since json_each has a path column too. A
// custom key cannot shadow a reserved one: ingest drops every `$` key it
// does not route to a column (server.resolveAttributes), so stored
// attributes hold no `$` key.
var receivedSQL = func() string {
	cols := []struct{ key, col string }{
		{"$host", "host"}, {"$path", "path"}, {"$referrer", "referrer_source"},
		{"$utm_source", "utm_source"}, {"$utm_medium", "utm_medium"}, {"$utm_campaign", "utm_campaign"},
		{"$os_version", "os_version"}, {"$browser_version", "browser_version"}, {"$device_model", "device_model"},
	}
	var pairs []string
	for _, c := range cols {
		pairs = append(pairs, "'"+c.key+"', NULLIF(r."+c.col+", '')")
	}
	keyed := `json_each(jsonb_patch(CASE WHEN json_valid(r.attributes) THEN r.attributes ELSE '{}' END,
		jsonb_object(` + strings.Join(pairs, ", ") + `))) j`
	arm := func(view string) string {
		return `SELECT r.project_id, r.day, r.family, r.event_name, r.measure, j.key AS k, j.value AS v
		FROM ` + view + ` r, ` + keyed + ` WHERE r.day < ?1`
	}
	return arm("raw_product") + `
	UNION ALL ` + arm("raw_measures")
}()

// countReceived rewrites received_attributes for every day before today:
// the days whose raw product or measure rows remain get exact counts and
// max_values (the most distinct values in one (family, event, measure)
// partition, the partition ATTRIBUTE_VALUES_TOP_N caps); the rest, days
// already rolled up, are dropped. Today stays as ingest counts it.
func countReceived(ctx context.Context, tx *sql.Tx, today string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM received_attributes WHERE day < ?1`, today); err != nil {
		return fmt.Errorf("received attributes: %w", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO received_attributes (project_id, day, attr_key, events, max_values)
		SELECT project_id, day, k, SUM(n), MAX(vals) FROM (
		  SELECT project_id, day, k, COUNT(*) AS n, COUNT(DISTINCT v) AS vals
		  FROM (`+receivedSQL+`)
		  GROUP BY project_id, day, family, event_name, measure, k
		) GROUP BY project_id, day, k`, today)
	if err != nil {
		return fmt.Errorf("received attributes: %w", err)
	}
	return nil
}
