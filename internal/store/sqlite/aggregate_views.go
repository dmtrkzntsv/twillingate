package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// noCap is the cap 0 stands for: a rank no day reaches. The views read the
// same number (025_live_halves.sql) when the meta row holds 0. An int64, as
// SQLite's integers are: it does not fit a 32-bit int (linux/arm).
const noCap int64 = 1 << 62

// capRows is the rank a rollup keeps up to for a configured cap: n itself,
// noCap for 0, and def for a negative n (config refuses one; the guard
// keeps a bad call from folding every value into "(other)").
func capRows(n, def int) int64 {
	switch {
	case n == 0:
		return noCap
	case n < 0:
		return int64(def)
	}
	return int64(n)
}

const otherBucket = "(other)"

// displaySQL is the display-resolution key, one expression shared by the
// aggregator and the live half of v_views_displays so the two cannot drift.
const displaySQL = `display_width || 'x' || display_height`

// consentSQL maps the raw consent flag onto the breakdown value: 1 given,
// 0 none, anything else unknown, the three answers the live half of
// v_views_consent (041_events_encoding.sql) counts side by side.
const consentSQL = `CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END`

// rawViews and rawProduct are the decoding family views over the raw
// events table (041_events_encoding.sql) that the identity and retention
// passes read. Every other read of events names its family code in the
// statement (TestRawTableIsReadOnlyThroughFamilyViews): the v_* live
// halves and the rollups group on the stored ids and never decode a raw
// row, only the grouped values (events encoding spec D13, D14).
const (
	rawViews   = "raw_views"
	rawProduct = "raw_product"
)

func (d *DB) ViewDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, familyCode(store.FamilyViews), projectID, before)
}

func (d *DB) ProductDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, familyCode(store.FamilyProduct), projectID, before)
}

func (d *DB) MeasureDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, familyCode(store.FamilyMeasures), projectID, before)
}

// daysBefore lists the project's raw days of one family before before:
// the key's (family, project_id, day) prefix, read in order.
func (d *DB) daysBefore(ctx context.Context, family, projectID int64, before civil.Date) ([]civil.Date, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT DISTINCT day FROM events WHERE family = ? AND project_id = ? AND day < ? ORDER BY 1`,
		family, projectID, before.String())
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

// viewSessionsCTE sessionizes one project-day on the stored ids, as the
// live half of v_views_daily (041_events_encoding.sql) does per raw day;
// views_test.go and rollup_ids_test.go enforce the parity. Kinds past the
// cap fold into "(other)" first, so a hostile client cannot grow
// agg_views_daily: kinds ranks the day's kinds by views, ties by their
// text, and buckets each under its strings id, or NULL for "(other)" (a
// kind spelled "(other)" joins it). A client-declared session_id is
// authoritative (the app knows its own foreground/background transitions);
// otherwise a gap over 30 minutes (t is the stored Unix seconds) per actor
// splits sessions. A session id
// is the stored value, a tagged blob or text (spec D3), so a client id of
// even-length digits ("12", a blob) is never the gap counter's text "12".
const viewSessionsCTE = `
WITH kinds AS (
  SELECT kind AS k,
         CASE WHEN ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, ` + kindText + `) <= :n
                   AND ` + kindText + ` <> '(other)' THEN kind END AS b
  FROM events WHERE family = 1 AND project_id = :p AND day = :day
  GROUP BY kind
),
bucketed AS (
  SELECT k.b AS kind, e.actor_id, e.session_id, e.ts AS t
  FROM events e CROSS JOIN kinds k ON k.k = e.kind
  WHERE e.family = 1 AND e.project_id = :p AND e.day = :day
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

// kindText decodes a stored kind: a kinds row's, or a span's bucket.
const kindText = `COALESCE((SELECT value FROM strings WHERE id = kind), '')`

// viewDimension is one rollup. keys are the result columns; cols the
// stored expressions grouped on (defaults to the key names, each a strings
// id). The last key is the one whose tail collapses into "(other)"; a
// leading key stays intact, so a collapsed os_version row still says which
// os it belongs to. plain marks a dimension whose one expression is
// already its text (display, consent). where filters the stored columns.
type viewDimension struct {
	table string
	keys  []string
	cols  []string
	plain bool
	where string
}

var viewDimensions = []viewDimension{
	{table: "agg_views_paths", keys: []string{"path"}},
	// No where clause: unlike utm, an empty host is a real bucket (rows
	// predating migration 008, and every non-web kind).
	{table: "agg_views_hosts", keys: []string{"host"}},
	{table: "agg_views_referrers", keys: []string{"source"}, cols: []string{"referrer_source"}},
	{table: "agg_views_utm", keys: []string{"utm_source", "utm_medium", "utm_campaign"},
		where: "AND NOT (utm_source = 0 AND utm_medium = 0 AND utm_campaign = 0)"},
	{table: "agg_views_countries", keys: []string{"country"}},
	{table: "agg_views_platforms", keys: []string{"platform"}},
	{table: "agg_views_os", keys: []string{"os", "os_version"}},
	{table: "agg_views_browsers", keys: []string{"browser", "browser_version"}},
	// Keyed on platform, not os: 2.4.1 means unrelated things across the
	// iOS and Android builds of one product, and a web build has no os
	// of its own to key on.
	{table: "agg_views_app_versions", keys: []string{"platform", "app_version"}, where: "AND app_version <> 0"},
	{table: "agg_views_devices", keys: []string{"device", "device_model"}},
	{table: "agg_views_displays", keys: []string{"display"}, cols: []string{displaySQL}, plain: true,
		where: "AND display_width > 0 AND display_height > 0"},
	{table: "agg_views_consent", keys: []string{"consent"}, cols: []string{consentSQL}, plain: true},
	// Keyed on the pair: a browser in German showing the product in
	// English is the row worth seeing. v_views_locales (041_events_encoding.sql)
	// is the live half.
	{table: "agg_views_locales", keys: []string{"browser_locale", "app_locale"},
		where: "AND NOT (browser_locale = 0 AND app_locale = 0)"},
}

// AggregateViewDay rolls one day of views into agg_views_* and deletes the
// raw rows, in one transaction. Idempotent: every write is INSERT OR
// REPLACE keyed on (project_id, day, ...), recomputed wholly from raw rows.
// topN is ATTRIBUTE_VALUES_TOP_N: values (and kinds) kept per breakdown
// and day, 0 for all of them. It applies to every breakdown, paths
// included: an unbounded one is the wrong default on the SD-card hardware
// target (a path carrying record ids would grow the aggregate without
// limit). It reads the raw rows on their stored ids and writes text
// (events encoding spec D14).
func (d *DB) AggregateViewDay(ctx context.Context, projectID int64, day civil.Date, topN int) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var raw bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM events WHERE family = 1 AND project_id = ? AND day = ?)`,
			projectID, day.String()).Scan(&raw); err != nil {
			return err
		}
		if !raw {
			return nil // already aggregated (or empty day): no-op keeps idempotency
		}
		named := []any{sql.Named("p", projectID), sql.Named("day", day.String()),
			sql.Named("n", capRows(topN, defaultAttrsTopN))}
		// Every view is in exactly one span, so the spans give the views
		// and the visitors too.
		if _, err := tx.ExecContext(ctx, viewSessionsCTE+`
INSERT OR REPLACE INTO agg_views_daily
  (project_id, day, kind, visitors, views, sessions, bounces, duration_sec)
SELECT :p, :day, CASE WHEN kind IS NULL THEN '(other)' ELSE `+kindText+` END,
  COUNT(DISTINCT actor_id),
  SUM(view_count),
  COUNT(*),
  SUM(CASE WHEN view_count = 1 THEN 1 ELSE 0 END),
  COALESCE(SUM(dur), 0)
FROM spans GROUP BY kind`, named...); err != nil {
			return fmt.Errorf("agg_views_daily: %w", err)
		}
		for _, dim := range viewDimensions {
			if _, err := tx.ExecContext(ctx, dim.aggregateSQL(), named...); err != nil {
				return fmt.Errorf("%s: %w", dim.table, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM events WHERE family=1 AND project_id=? AND day=?`, projectID, day.String()); err != nil {
			return fmt.Errorf("prune raw views: %w", err)
		}
		return nil
	})
}

// aggregateSQL builds the one-statement rollup on the stored ids. ranked
// groups the day's rows by the dimension's stored values, ranks the groups
// by views desc (ties by their text, in key order, as the text was ranked)
// and buckets each: the last value's id when it is kept, NULL for the
// tail, which a kept value spelled "(other)" joins. The day's rows are then
// grouped by the leading values and the bucket. The raw rows never decode:
// each distinct value decodes once to rank, and the written rows once
// more. One statement rather than a top-N insert plus a tail insert
// keeps visitors honest: it is always COUNT(DISTINCT actor_id) over the
// grouped raw rows (the stored ids: the encoding is one-to-one), never a
// sum that double-counts an actor who saw two collapsed values.
func (dim viewDimension) aggregateSQL() string {
	cols := dim.cols
	if cols == nil {
		cols = dim.keys
	}
	text := func(c string) string {
		if dim.plain {
			return c
		}
		return `COALESCE((SELECT value FROM strings WHERE id = ` + c + `), '')`
	}
	last := len(cols) - 1
	var groups, order, join, lead, leadText []string
	for i, c := range cols {
		g := fmt.Sprintf("g%d", i)
		groups = append(groups, c+" AS "+g)
		order = append(order, text(c))
		join = append(join, "r."+g+" = "+c)
		if i < last {
			lead = append(lead, "r."+g)
			leadText = append(leadText, text("r."+g))
		}
	}
	return fmt.Sprintf(`
INSERT OR REPLACE INTO %[1]s (project_id, day, %[2]s, visitors, views)
WITH ranked AS (
  SELECT %[3]s,
         CASE WHEN ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, %[4]s) <= :n
                   AND %[5]s <> '%[6]s' THEN %[7]s END AS b
  FROM events
  WHERE family = 1 AND project_id = :p AND day = :day %[8]s
  GROUP BY %[9]s
)
SELECT :p, :day, %[10]sCASE WHEN r.b IS NULL THEN '%[6]s' ELSE %[11]s END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM events CROSS JOIN ranked r ON %[12]s
WHERE family = 1 AND project_id = :p AND day = :day %[8]s
GROUP BY %[13]s`,
		dim.table, strings.Join(dim.keys, ", "), strings.Join(groups, ", "), strings.Join(order, ", "),
		text(cols[last]), otherBucket, cols[last], dim.where, strings.Join(cols, ", "),
		prefixed(leadText), text("r.b"), strings.Join(join, " AND "),
		strings.Join(append(lead, "r.b"), ", "))
}

// prefixed joins exprs, each followed by a comma and a space.
func prefixed(exprs []string) string {
	var b strings.Builder
	for _, e := range exprs {
		b.WriteString(e + ", ")
	}
	return b.String()
}
