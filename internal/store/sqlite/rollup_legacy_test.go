package sqlite

// The rollups and the usage counts as they were before they read events on
// ids (events encoding spec D14): every statement that read raw rows reads
// the decoding raw_* views, as written at 922b6f8. They serve two
// purposes:
//
//   - the oracle the id-based code must match
//     (TestRollupsOnIDsMatchTheDecodedRollups,
//     TestRollupsReadUndecodableValuesAsRawDoes);
//   - the rollup textRollup runs on a database pinned below 041, where
//     events stores text and only the raw_* views read it (the migration
//     025 tests).
//
// They share these live symbols with the store's code: store.SystemAttributes,
// store.DeclarableAttributes, capRows, defaultAttrsTopN, otherBucket,
// countDeclared, recordCaps, tableBytes, dataTables, rowsPerProject,
// putStat and shareOfBytes. Edit this file only to follow a change to the
// schema or to one of those symbols that both sides need; never to make it
// agree with a change in how the store's rollups read the raw rows, which
// is what it checks. It can go once no migration test below 041 needs
// textRollup and the parity tests have served their purpose (the id-based
// rollups have shipped, and views_test.go's parity with the live halves
// guards them).

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const legacyViewSessionsCTE = `
WITH src AS (
  SELECT kind, actor_id, session_id, CAST(strftime('%s', ts) AS INTEGER) AS t
  FROM raw_views WHERE project_id = :p AND day = :day
),
kinds AS (
  SELECT kind, ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, kind) AS rn FROM src GROUP BY kind
),
bucketed AS (
  SELECT CASE WHEN k.rn <= :n THEN src.kind ELSE '(other)' END AS kind,
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

type legacyViewDimension struct {
	table string
	keys  []string
	exprs []string
	where string
}

var legacyViewDimensions = []legacyViewDimension{
	{table: "agg_views_paths", keys: []string{"path"}},
	{table: "agg_views_hosts", keys: []string{"host"}},
	{table: "agg_views_referrers", keys: []string{"source"}, exprs: []string{"referrer_source"}},
	{table: "agg_views_utm", keys: []string{"utm_source", "utm_medium", "utm_campaign"},
		where: "AND NOT (utm_source='' AND utm_medium='' AND utm_campaign='')"},
	{table: "agg_views_countries", keys: []string{"country"}},
	{table: "agg_views_platforms", keys: []string{"platform"}},
	{table: "agg_views_os", keys: []string{"os", "os_version"}},
	{table: "agg_views_browsers", keys: []string{"browser", "browser_version"}},
	{table: "agg_views_app_versions", keys: []string{"platform", "app_version"}, where: "AND app_version <> ''"},
	{table: "agg_views_devices", keys: []string{"device", "device_model"}},
	{table: "agg_views_displays", keys: []string{"display"}, exprs: []string{`display_width || 'x' || display_height`},
		where: "AND display_width > 0 AND display_height > 0"},
	{table: "agg_views_consent", keys: []string{"consent"},
		exprs: []string{`CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END`}},
	{table: "agg_views_locales", keys: []string{"browser_locale", "app_locale"},
		where: "AND NOT (browser_locale='' AND app_locale='')"},
}

func legacyAggregateViewDay(ctx context.Context, d *DB, projectID int64, day civil.Date, topN int) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM raw_views WHERE project_id=? AND day=?`,
			projectID, day.String()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		named := []any{sql.Named("p", projectID), sql.Named("day", day.String()),
			sql.Named("n", capRows(topN, defaultAttrsTopN))}
		if _, err := tx.ExecContext(ctx, legacyViewSessionsCTE+`
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
		for _, dim := range legacyViewDimensions {
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

func (dim legacyViewDimension) aggregateSQL() string {
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
	bucket := fmt.Sprintf("CASE WHEN r.rn <= :n THEN s.%s ELSE '%s' END", last, otherBucket)
	cols := strings.Join(dim.keys, ", ")
	group := strings.Join(append(append([]string{}, lead...), bucket), ", ")
	return fmt.Sprintf(`
INSERT OR REPLACE INTO %s (project_id, day, %s, visitors, views)
WITH src AS (
  SELECT %s, actor_id FROM raw_views
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

func legacyAttrPath(key string) string {
	return `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
}

func legacyAggregateProductDay(ctx context.Context, d *DB, projectID int64, day civil.Date, attrs []string, topN int) error {
	keep := capRows(topN, defaultAttrsTopN)
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE family=2 AND project_id=? AND day=?
			AND event_name IN (SELECT s.id FROM strings s JOIN rules r ON r.name = s.value
			  WHERE r.project_id=? AND r.kind='event' AND r.family='product' AND r.action='block')`,
			projectID, day.String(), projectID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM raw_product WHERE project_id=? AND day=?`,
			projectID, day.String()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := legacyRollupProduct(ctx, tx, projectID, day, attrs, keep); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM events WHERE family=2 AND project_id=? AND day=?`, projectID, day.String())
		return err
	})
}

func legacyRollupProduct(ctx context.Context, tx *sql.Tx, projectID int64, day civil.Date, attrs []string, topN int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_daily
		(project_id, day, event_name, count, unique_users)
		SELECT project_id, ?, event_name, COUNT(*), COUNT(DISTINCT actor_id)
		FROM raw_product WHERE project_id=? AND day=?
		GROUP BY event_name`, day.String(), projectID, day.String()); err != nil {
		return fmt.Errorf("agg_product_daily: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_totals
		(project_id, day, total_events, active_users)
		SELECT project_id, ?, COUNT(*), COUNT(DISTINCT actor_id)
		FROM raw_product WHERE project_id=? AND day=?
		GROUP BY project_id`, day.String(), projectID, day.String()); err != nil {
		return fmt.Errorf("agg_product_totals: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT event_name FROM raw_product
		WHERE project_id=? AND day=?`, projectID, day.String())
	if err != nil {
		return err
	}
	var events []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, event := range events {
		for _, key := range attrs {
			expr, present := `json_extract(attributes, :path)`, `json_extract(attributes, :path) IS NOT NULL`
			if strings.HasPrefix(key, "$") {
				col, ok := store.DeclarableAttributes[key]
				if !ok {
					continue
				}
				expr, present = col, col+` <> ''`
			}
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", event), sql.Named("key", key),
				sql.Named("path", legacyAttrPath(key)), sql.Named("n", topN),
			}
			if err := legacyRollupAttrValue(ctx, tx, expr, present, named); err != nil {
				return fmt.Errorf("attr %s/%s: %w", event, key, err)
			}
		}
	}
	for _, dim := range store.SystemAttributes {
		for _, event := range events {
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", event), sql.Named("key", dim.Key),
				sql.Named("n", topN),
			}
			if err := legacyRollupAttrValue(ctx, tx, dim.Column, dim.Column+` <> ''`, named); err != nil {
				return fmt.Errorf("system dim %s/%s: %w", event, dim.Key, err)
			}
		}
	}
	return nil
}

func legacyRollupAttrValue(ctx context.Context, tx *sql.Tx, expr, present string, named []any) error {
	if _, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT `+expr+` AS v, COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
		         COUNT(DISTINCT NULLIF(group_id,'')) AS g
		  FROM raw_product
		  WHERE project_id=:p AND day=:day AND event_name=:event
		    AND `+present+`
		  GROUP BY v
		),
		ranked AS (SELECT v, c, u, g, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		SELECT :p, :day, :event, :key, v, c, u, g FROM ranked WHERE rn <= :n`, named...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT `+expr+` AS v, COUNT(*) AS c
		  FROM raw_product
		  WHERE project_id=:p AND day=:day AND event_name=:event
		    AND `+present+`
		  GROUP BY v
		),
		ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted),
		keep AS (SELECT v FROM ranked WHERE rn <= :n)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		SELECT :p, :day, :event, :key, '(other)', COUNT(*), COUNT(DISTINCT actor_id),
		       COUNT(DISTINCT NULLIF(group_id,''))
		FROM raw_product
		WHERE project_id=:p AND day=:day AND event_name=:event
		  AND `+present+`
		  AND `+expr+` NOT IN (SELECT v FROM keep)
		HAVING COUNT(*) > 0`, named...)
	return err
}

func legacyAggregateMeasureDay(ctx context.Context, d *DB, projectID int64, day civil.Date, attrs []string, topN int) error {
	keep := capRows(topN, defaultAttrsTopN)
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE family=3 AND project_id=? AND day=?
			AND event_name IN (SELECT s.id FROM strings s JOIN rules r ON r.name = s.value
			  WHERE r.project_id=? AND r.kind='event' AND r.family='measures' AND r.action='block')`,
			projectID, day.String(), projectID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM raw_measures WHERE project_id=? AND day=?`,
			projectID, day.String()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := legacyRollupMeasures(ctx, tx, projectID, day, attrs, keep); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM events WHERE family=3 AND project_id=? AND day=?`, projectID, day.String())
		return err
	})
}

func legacyRollupMeasures(ctx context.Context, tx *sql.Tx, projectID int64, day civil.Date, attrs []string, topN int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_measures_daily
		(project_id, day, event_name, measure, bucket, samples, weight, sum)
		SELECT project_id, ?, event_name, measure, bucket,
		       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
		FROM raw_measures WHERE project_id=? AND day=?
		GROUP BY event_name, measure, bucket`, day.String(), projectID, day.String()); err != nil {
		return fmt.Errorf("agg_measures_daily: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT event_name, measure FROM raw_measures
		WHERE project_id=? AND day=?`, projectID, day.String())
	if err != nil {
		return err
	}
	var metrics []struct{ event, measure string }
	for rows.Next() {
		var m struct{ event, measure string }
		if err := rows.Scan(&m.event, &m.measure); err != nil {
			rows.Close()
			return err
		}
		metrics = append(metrics, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var keys []string
	seen := map[string]bool{}
	for _, key := range attrs {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for _, m := range metrics {
		for _, key := range keys {
			expr, present := `json_extract(attributes, :path)`, `json_extract(attributes, :path) IS NOT NULL`
			if strings.HasPrefix(key, "$") {
				col, ok := store.DeclarableAttributes[key]
				if !ok {
					continue
				}
				expr, present = col, col+` <> ''`
			}
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", m.event), sql.Named("measure", m.measure),
				sql.Named("key", key), sql.Named("path", legacyAttrPath(key)), sql.Named("n", topN),
			}
			if err := legacyRollupMeasureAttr(ctx, tx, expr, present, named); err != nil {
				return fmt.Errorf("attr %s/%s/%s: %w", m.event, m.measure, key, err)
			}
		}
		for _, dim := range store.SystemAttributes {
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", m.event), sql.Named("measure", m.measure),
				sql.Named("key", dim.Key), sql.Named("n", topN),
			}
			if err := legacyRollupMeasureAttr(ctx, tx, dim.Column, dim.Column+` <> ''`, named); err != nil {
				return fmt.Errorf("system dim %s/%s/%s: %w", m.event, m.measure, dim.Key, err)
			}
		}
	}
	return nil
}

func legacyRollupMeasureAttr(ctx context.Context, tx *sql.Tx, expr, present string, named []any) error {
	const scope = `project_id=:p AND day=:day AND event_name=:event AND measure=:measure`
	keep := `WITH counted AS (
		  SELECT ` + expr + ` AS v, COUNT(*) AS c FROM raw_measures
		  WHERE ` + scope + ` AND ` + present + `
		  GROUP BY v),
		ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted),
		keep AS (SELECT v FROM ranked WHERE rn <= :n)`
	if _, err := tx.ExecContext(ctx, keep+`
		INSERT OR REPLACE INTO agg_measures_attrs
		  (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		SELECT :p, :day, :event, :measure, :key, `+expr+`, bucket,
		       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
		FROM raw_measures
		WHERE `+scope+` AND `+present+` AND `+expr+` IN (SELECT v FROM keep)
		GROUP BY `+expr+`, bucket`, named...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, keep+`
		INSERT INTO agg_measures_attrs
		  (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		SELECT :p, :day, :event, :measure, :key, '(other)', bucket,
		       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
		FROM raw_measures
		WHERE `+scope+` AND `+present+` AND `+expr+` NOT IN (SELECT v FROM keep)
		GROUP BY bucket
		ON CONFLICT (project_id, day, event_name, measure, attr_key, attr_value, bucket) DO UPDATE SET
		  samples = samples + excluded.samples,
		  weight = weight + excluded.weight,
		  sum = sum + excluded.sum`, named...)
	return err
}

// legacyRecordUsageHistory is RecordUsageHistory with the measurements
// that read raw rows as they read raw_*; the rest is today's code.
func legacyRecordUsageHistory(ctx context.Context, d *DB, now time.Time) error {
	day := civil.DateOf(now).String()
	return d.tx(ctx, func(tx *sql.Tx) error {
		if err := legacyMeasureSizes(ctx, tx, day); err != nil {
			return err
		}
		if err := countDeclared(ctx, tx, day); err != nil {
			return err
		}
		if err := recordCaps(ctx, tx, day); err != nil {
			return err
		}
		if err := legacyCountDays(ctx, tx, day); err != nil {
			return err
		}
		if err := legacyCountAttributes(ctx, tx, day); err != nil {
			return err
		}
		return legacyCountReceived(ctx, tx, day)
	})
}

func legacyMeasureSizes(ctx context.Context, tx *sql.Tx, day string) error {
	bytes, err := tableBytes(ctx, tx)
	if err != nil {
		return fmt.Errorf("table sizes: %w", err)
	}
	tables, err := dataTables(ctx, tx)
	if err != nil {
		return fmt.Errorf("data tables: %w", err)
	}
	raw, aggregate := map[int64]int64{}, map[int64]int64{}
	hasRows := map[int64]bool{}
	for _, table := range tables {
		q := `SELECT project_id, COUNT(*) FROM "` + table + `" GROUP BY project_id`
		into := aggregate
		if table == "events" {
			q = `SELECT project_id, SUM(n) FROM (
				SELECT project_id, COUNT(*) AS n FROM raw_views GROUP BY project_id
				UNION ALL SELECT project_id, COUNT(*) FROM raw_product GROUP BY project_id
				UNION ALL SELECT project_id, COUNT(*) FROM raw_measures GROUP BY project_id
			) GROUP BY project_id`
			into = raw
		}
		per, total, err := rowsPerProject(ctx, tx, q)
		if err != nil {
			return fmt.Errorf("count %s: %w", table, err)
		}
		for id, n := range per {
			into[id] += shareOfBytes(bytes[table], n, total)
			hasRows[id] = true
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_history WHERE key IN (?, ?, ?) AND measured_at = ?`,
		store.StatRawBytes, store.StatAggregateBytes, store.StatDatabaseBytes, day); err != nil {
		return err
	}
	for id := range hasRows {
		for key, v := range map[string]int64{store.StatRawBytes: raw[id], store.StatAggregateBytes: aggregate[id]} {
			if err := putStat(ctx, tx, key, id, day, v); err != nil {
				return err
			}
		}
	}
	var file int64
	if err := tx.QueryRowContext(ctx, `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&file); err != nil {
		return fmt.Errorf("database size: %w", err)
	}
	return putStat(ctx, tx, store.StatDatabaseBytes, 0, day, file)
}

var legacyDailyCounts = []struct{ key, agg, raw string }{
	{store.StatViews, `SELECT project_id, day, SUM(views) AS n FROM agg_views_daily WHERE day < ?1 GROUP BY project_id, day`,
		`SELECT project_id, day, COUNT(*) FROM raw_views WHERE day < ?1 GROUP BY project_id, day`},
	{store.StatEvents, `SELECT project_id, day, SUM(total_events) AS n FROM agg_product_totals WHERE day < ?1 GROUP BY project_id, day`,
		`SELECT project_id, day, COUNT(*) FROM raw_product WHERE day < ?1 GROUP BY project_id, day`},
	{store.StatMeasures, `SELECT project_id, day, SUM(samples) AS n FROM agg_measures_daily WHERE day < ?1 GROUP BY project_id, day`,
		`SELECT project_id, day, COUNT(*) FROM raw_measures WHERE day < ?1 GROUP BY project_id, day`},
}

func legacyCountDays(ctx context.Context, tx *sql.Tx, today string) error {
	for _, c := range legacyDailyCounts {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO usage_history (key, project_id, measured_at, value)
			SELECT ?2, project_id, day, SUM(n) FROM (`+c.agg+` UNION ALL `+c.raw+`) GROUP BY project_id, day`,
			today, c.key); err != nil {
			return fmt.Errorf("count %s: %w", c.key, err)
		}
	}
	return nil
}

var legacyAttrColumns = []struct{ key, col string }{
	{"$platform", "platform"}, {"$os", "os"}, {"$app_version", "app_version"}, {"$app_locale", "app_locale"},
	{"$kind", "kind"}, {"$browser", "browser"}, {"$device", "device"}, {"$browser_locale", "browser_locale"},
	{"$country", "country"}, {"$host", "host"}, {"$path", "path"}, {"$referrer", "referrer_source"}, {"$utm_source", "utm_source"},
	{"$utm_medium", "utm_medium"}, {"$utm_campaign", "utm_campaign"}, {"$os_version", "os_version"},
	{"$browser_version", "browser_version"}, {"$device_model", "device_model"},
}

var legacyFoldedSQL = func() string {
	var value, defaults strings.Builder
	value.WriteString("CASE k.attr_key")
	for _, c := range legacyAttrColumns {
		fmt.Fprintf(&value, " WHEN '%s' THEN NULLIF(e.%s, '')", c.key, c.col)
	}
	value.WriteString(` ELSE CASE WHEN k.attr_key LIKE '$%' THEN NULL
		ELSE json_extract(e.attributes, '$."' || replace(k.attr_key,'"','\"') || '"') END END`)
	for i, c := range legacyAttrColumns[:9] {
		if i > 0 {
			defaults.WriteString(", ")
		}
		fmt.Fprintf(&defaults, "('%s')", c.key)
	}
	return `WITH cap AS (
		  SELECT COALESCE((SELECT CASE CAST(value AS INTEGER) WHEN 0 THEN 4611686018427387904
		                                ELSE CAST(value AS INTEGER) END
		                   FROM meta WHERE key='attributes_top_n'
		                     AND (CAST(value AS INTEGER) > 0 OR value = '0')), 100) AS n
		), keys AS (
		  SELECT project_id, key AS attr_key FROM project_attributes WHERE breakdown = 1
		  UNION
		  SELECT p.id, s.column1 FROM projects p, (VALUES ` + defaults.String() + `) s
		), parts AS (
		  SELECT project_id, day, COUNT(DISTINCT v) AS n FROM (
		    SELECT e.project_id, e.day, 'p' AS fam, e.event_name AS ev, '' AS measure, k.attr_key AS attr_key,
		           ` + value.String() + ` AS v
		    FROM raw_product e JOIN keys k ON k.project_id = e.project_id WHERE e.day < ?1
		    UNION ALL
		    SELECT e.project_id, e.day, 'm', e.event_name, e.measure, k.attr_key,
		           ` + value.String() + `
		    FROM raw_measures e JOIN keys k ON k.project_id = e.project_id WHERE e.day < ?1
		  ) WHERE v IS NOT NULL
		  GROUP BY project_id, day, fam, ev, measure, attr_key
		)
		SELECT project_id, day, SUM(MAX(n - (SELECT n FROM cap), 0)) FROM parts GROUP BY project_id, day`
}()

func legacyCountAttributes(ctx context.Context, tx *sql.Tx, today string) error {
	received := `SELECT e.project_id AS project_id, e.day AS day, j.key AS k, j.value AS v
		FROM (SELECT project_id, day, attributes FROM raw_product WHERE day < ?1
		      UNION ALL SELECT project_id, day, attributes FROM raw_measures WHERE day < ?1) e,
		     json_each(CASE WHEN json_valid(e.attributes) THEN e.attributes ELSE '{}' END) j`
	for _, q := range []struct{ key, sql string }{
		{store.StatAttributeKeys, `SELECT project_id, day, COUNT(DISTINCT k) FROM (` + received + `) GROUP BY project_id, day`},
		{store.StatAttributeValues, `SELECT project_id, day, COUNT(*) FROM (SELECT DISTINCT project_id, day, k, v FROM (` + received + `)) GROUP BY project_id, day`},
		{store.StatAttributeValuesFolded, legacyFoldedSQL},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO usage_history (key, project_id, measured_at, value)
			SELECT ?2, * FROM (`+q.sql+`)`, today, q.key); err != nil {
			return fmt.Errorf("count %s: %w", q.key, err)
		}
	}
	return nil
}

var legacyReceivedSQL = func() string {
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

func legacyCountReceived(ctx context.Context, tx *sql.Tx, today string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_attributes WHERE day < ?1`, today); err != nil {
		return fmt.Errorf("usage attributes: %w", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_attributes (project_id, day, attr_key, events, max_values)
		SELECT project_id, day, k, SUM(n), MAX(vals) FROM (
		  SELECT project_id, day, k, COUNT(*) AS n, COUNT(DISTINCT v) AS vals
		  FROM (`+legacyReceivedSQL+`)
		  GROUP BY project_id, day, family, event_name, measure, k
		) GROUP BY project_id, day, k`, today)
	if err != nil {
		return fmt.Errorf("usage attributes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_attributes (project_id, key)
		SELECT DISTINCT project_id, attr_key FROM usage_attributes
		WHERE project_id IN (SELECT id FROM projects)`); err != nil {
		return fmt.Errorf("attribute catalog: %w", err)
	}
	return nil
}
