package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
)

// topNDimension caps client-supplied dimension values per day; the tail
// collapses into "(other)". Applies to every dimension, paths included: an
// unbounded dimension is the wrong default on the SD-card hardware target
// (a path carrying record ids would grow the aggregate without limit).
const topNDimension = 500

const otherBucket = "(other)"

// displaySQL is the display-resolution key, one expression shared by the
// aggregator and the live half of v_views_displays so the two cannot drift.
const displaySQL = `display_width || 'x' || display_height`

func dayRange(day civil.Date) (string, string) {
	return day.String() + "T00:00:00Z", day.AddDays(1).String() + "T00:00:00Z"
}

func (d *DB) ViewDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, "views", projectID, before)
}

func (d *DB) ProductDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, "events", projectID, before)
}

func (d *DB) daysBefore(ctx context.Context, table string, projectID int64, before civil.Date) ([]civil.Date, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT DISTINCT substr(ts,1,10) FROM %s WHERE project_id=? AND ts < ? ORDER BY 1`, table),
		projectID, before.String()+"T00:00:00Z")
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

// viewSessionsCTE sessionizes one project-day. Kinds past the cap fold into
// "(other)" first, so a hostile client cannot grow agg_views_daily. A
// client-declared session_id is authoritative (the app knows its own
// foreground/background transitions); otherwise a gap over 30 minutes per
// actor splits sessions. The live half of v_views_daily (012_views.sql)
// mirrors this per (project_id, day); views_test.go enforces the parity.
const viewSessionsCTE = `
WITH src AS (
  SELECT kind, actor_id, session_id, CAST(strftime('%s', ts) AS INTEGER) AS t
  FROM views WHERE project_id = :p AND day = :day
),
kinds AS (
  SELECT kind, ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, kind) AS rn FROM src GROUP BY kind
),
bucketed AS (
  SELECT CASE WHEN k.rn <= 500 THEN src.kind ELSE '(other)' END AS kind,
         src.actor_id, src.session_id, src.t
  FROM src JOIN kinds k ON k.kind = src.kind
),
marked AS (
  SELECT kind, actor_id, session_id, t,
         CASE WHEN session_id <> '' THEN 0
              WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
              ELSE 0 END AS new_session
  FROM bucketed WINDOW w AS (PARTITION BY kind, actor_id ORDER BY t)
),
keyed AS (
  SELECT kind, actor_id, t,
         CASE WHEN session_id <> '' THEN session_id
              ELSE CAST(SUM(new_session) OVER (PARTITION BY kind, actor_id ORDER BY t) AS TEXT)
         END AS skey
  FROM marked
),
spans AS (
  SELECT kind, actor_id, skey, COUNT(*) AS view_count, MAX(t) - MIN(t) AS dur
  FROM keyed GROUP BY kind, actor_id, skey
)`

// viewDimension is one rollup. keys are the result columns; exprs the SQL
// producing them (defaults to the key names). The last key is the one
// whose tail collapses into "(other)"; a leading key stays intact, so a
// collapsed os_version row still says which os it belongs to.
type viewDimension struct {
	table string
	keys  []string
	exprs []string
	where string
}

var viewDimensions = []viewDimension{
	{table: "agg_views_paths", keys: []string{"path"}},
	// No where clause: unlike utm, an empty host is a real bucket (rows
	// predating migration 008, and every non-web kind).
	{table: "agg_views_hosts", keys: []string{"host"}},
	{table: "agg_views_referrers", keys: []string{"source"}, exprs: []string{"referrer_source"}},
	{table: "agg_views_utm", keys: []string{"utm_source", "utm_medium", "utm_campaign"},
		where: "AND NOT (utm_source='' AND utm_medium='' AND utm_campaign='')"},
	{table: "agg_views_countries", keys: []string{"country"}},
	{table: "agg_views_platforms", keys: []string{"platform"}},
	{table: "agg_views_os", keys: []string{"os", "os_version"}},
	{table: "agg_views_browsers", keys: []string{"browser", "browser_version"}},
	// Keyed on platform, not os: 2.4.1 means unrelated things across the
	// iOS and Android builds of one product, and a web build has no os
	// of its own to key on.
	{table: "agg_views_app_versions", keys: []string{"platform", "app_version"}, where: "AND app_version <> ''"},
	{table: "agg_views_devices", keys: []string{"device", "device_model"}},
	{table: "agg_views_displays", keys: []string{"display"}, exprs: []string{displaySQL},
		where: "AND display_width > 0 AND display_height > 0"},
}

// AggregateViewDay rolls one day of views into agg_views_* and deletes the
// raw rows, in one transaction. Idempotent: every write is INSERT OR
// REPLACE keyed on (project_id, day, ...), recomputed wholly from raw rows.
func (d *DB) AggregateViewDay(ctx context.Context, projectID int64, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM views WHERE project_id=? AND day=?`,
			projectID, day.String()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil // already aggregated (or empty day): no-op keeps idempotency
		}
		named := []any{sql.Named("p", projectID), sql.Named("day", day.String())}
		if _, err := tx.ExecContext(ctx, viewSessionsCTE+`
INSERT OR REPLACE INTO agg_views_daily
  (project_id, day, kind, visitors, views, sessions, bounces, duration_sec)
SELECT :p, :day, s.kind,
  (SELECT COUNT(DISTINCT actor_id) FROM bucketed b WHERE b.kind = s.kind),
  (SELECT COUNT(*) FROM bucketed b WHERE b.kind = s.kind),
  COUNT(*),
  SUM(CASE WHEN view_count = 1 THEN 1 ELSE 0 END),
  COALESCE(SUM(dur), 0)
FROM spans s GROUP BY s.kind`, named...); err != nil {
			return fmt.Errorf("agg_views_daily: %w", err)
		}
		for _, dim := range viewDimensions {
			if _, err := tx.ExecContext(ctx, dim.aggregateSQL(), named...); err != nil {
				return fmt.Errorf("%s: %w", dim.table, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM views WHERE project_id=? AND day=?`, projectID, day.String()); err != nil {
			return fmt.Errorf("prune raw views: %w", err)
		}
		return nil
	})
}

// aggregateSQL builds the one-statement rollup: rank values by views desc,
// keep the top N, map the rest onto "(other)". One statement rather than a
// top-N insert plus a tail insert keeps visitors honest: it is always
// COUNT(DISTINCT actor_id) over the grouped raw rows, never a sum that
// double-counts an actor who saw two collapsed values.
func (dim viewDimension) aggregateSQL() string {
	exprs := dim.exprs
	if exprs == nil {
		exprs = dim.keys
	}
	var sel, lead, join []string
	for i, k := range dim.keys {
		sel = append(sel, exprs[i]+" AS "+k)
		join = append(join, "r."+k+" = s."+k)
		if i < len(dim.keys)-1 {
			lead = append(lead, "s."+k)
		}
	}
	last := dim.keys[len(dim.keys)-1]
	bucket := fmt.Sprintf("CASE WHEN r.rn <= %d THEN s.%s ELSE '%s' END", topNDimension, last, otherBucket)
	cols := strings.Join(dim.keys, ", ")
	group := strings.Join(append(append([]string{}, lead...), bucket), ", ")
	return fmt.Sprintf(`
INSERT OR REPLACE INTO %s (project_id, day, %s, visitors, views)
WITH src AS (
  SELECT %s, actor_id FROM views
  WHERE project_id = :p AND day = :day %s
),
ranked AS (
  SELECT %s, ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, %s) AS rn FROM src GROUP BY %s
)
SELECT :p, :day, %s, COUNT(DISTINCT s.actor_id), COUNT(*)
FROM src s JOIN ranked r ON %s
GROUP BY %s`,
		dim.table, cols, strings.Join(sel, ", "), dim.where,
		cols, cols, cols,
		group, strings.Join(join, " AND "), group)
}
