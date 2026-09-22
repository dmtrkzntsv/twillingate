package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/enrich"
)

// foldMode says what becomes of a value the validator does not recognise.
type foldMode int

const (
	// foldToOther stores the validator's floor -- other for the closed
	// vocabularies, unknown for a platform -- and, where keepName is set,
	// preserves the original first. Raw rows only: one row is one hit, so
	// relabelling loses a name, never a count.
	foldToOther foldMode = iota
	// foldKeep leaves an unrecognised value exactly as it was. Aggregate
	// history only: its visitors were counted as distinct actors, so
	// folding two values onto one key would SUM actors that may be the
	// same person. The tail stays unfolded instead.
	foldKeep
)

// fold is one column rewrite: every distinct value of table.column
// (optionally restricted by where) goes through normalize, and the result
// is written back to column, or to target when the fold derives one
// column from another.
type fold struct {
	table     string
	column    string // read, and written unless target names another column
	target    string // optional destination, for the platform backfill
	where     string // optional literal predicate, ANDed onto every statement
	normalize func(string) (string, bool)
	mode      foldMode
	keepName  string // column that receives the original when the value is unrecognised
}

// dest is the column the fold writes.
func (f fold) dest() string {
	if f.target != "" {
		return f.target
	}
	return f.column
}

// foldValues applies one fold with a single UPDATE per distinct value, so
// a column with three spellings costs three statements rather than a scan
// of the table in Go.
func foldValues(ctx context.Context, tx *sql.Tx, f fold) error {
	query := fmt.Sprintf(`SELECT DISTINCT %s FROM %s`, f.column, f.table)
	if f.where != "" {
		query += ` WHERE ` + f.where
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("fold %s.%s: %w", f.table, f.column, err)
	}
	var values []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("fold %s.%s: %w", f.table, f.column, err)
		}
		values = append(values, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("fold %s.%s: %w", f.table, f.column, err)
	}

	for _, raw := range values {
		folded, known := f.normalize(raw)
		if f.mode == foldKeep && !known {
			continue
		}
		if f.target == "" && folded == raw && (known || f.keepName == "") {
			continue
		}
		set := f.dest() + ` = ?`
		args := []any{folded}
		if !known && f.keepName != "" {
			set += `, ` + f.keepName + ` = ?`
			args = append(args, raw)
		}
		update := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = ?`, f.table, set, f.column)
		args = append(args, raw)
		if f.where != "" {
			update += ` AND ` + f.where
		}
		if _, err := tx.ExecContext(ctx, update, args...); err != nil {
			return fmt.Errorf("fold %s.%s %q: %w", f.table, f.column, raw, err)
		}
	}
	return nil
}

// foldEnvironment is migration 015's data step: every value the migration
// writes goes through the validators the server applies at ingest, so the
// vocabularies live in internal/enrich alone and the database carries no
// copy of them. Order is load-bearing -- the platform backfill reads
// views.os and must run before the OS fold rewrites it. Spec:
// docs/superpowers/specs/2026-09-20-os-and-platform-design.md, "Storage".
func foldEnvironment(ctx context.Context, tx *sql.Tx) error {
	for _, f := range []fold{
		// 1. Platform backfill for app rows inverts 012's fold of
		//    app_views.platform into os; a token outside the platform
		//    shape is unknown. Web rows were set by the SQL, and every
		//    other kind keeps the column default.
		{table: "views", column: "os", target: "platform", where: `kind = 'app'`, normalize: enrich.NormalizePlatform},

		// 3. OS fold, raw rows: the original is kept in os_name when the
		//    value is unrecognised, then os becomes the canonical value,
		//    unknown for '', other for the rest. events: the same fold,
		//    with no os_name to preserve the original in.
		{table: "views", column: "os", normalize: enrich.NormalizeOS, keepName: "os_name"},
		{table: "events", column: "os", normalize: enrich.NormalizeOS},

		// 4. OS fold, aggregate history: only what merges nothing --
		//    canonical values lower-cased, '' relabelled unknown,
		//    unrecognised values left exactly as they were. A collision
		//    between two spellings of one canonical value fails the
		//    UPDATE's primary key and aborts the migration;
		//    docs/deployment.md lists the query that finds them first.
		{table: "agg_views_os", column: "os", normalize: enrich.NormalizeOS, mode: foldKeep},
		{table: "agg_product_attrs", column: "attr_value", where: `attr_key = '$os'`, normalize: enrich.NormalizeOS, mode: foldKeep},

		// 5. Browser and device folds: loss-free. No client could write
		//    these columns before 015, so every value came from the old
		//    parser and is in the new vocabularies; the fold is injective
		//    and merges nothing. Aggregate rows keep an unrecognised
		//    value for the same reason as 4.
		{table: "views", column: "browser", normalize: enrich.NormalizeBrowser},
		{table: "views", column: "device", normalize: enrich.NormalizeDevice},
		{table: "agg_views_browsers", column: "browser", normalize: enrich.NormalizeBrowser, mode: foldKeep},
		{table: "agg_views_devices", column: "device", normalize: enrich.NormalizeDevice, mode: foldKeep},
	} {
		if err := foldValues(ctx, tx, f); err != nil {
			return err
		}
	}
	return rekeyAppVersions(ctx, tx)
}

// rekeyAppVersions is statement 7's other half: copy
// agg_views_app_versions_old onto the new (platform, app_version) key
// through the platform validator, then drop it. Rows that land on one key
// are summed rather than aborting as 4 does -- case variants of one token
// and values outside the pattern both merge, and so can overcount an
// actor counted in both; docs/deployment.md pre-check 1b finds such rows
// before the upgrade.
func rekeyAppVersions(ctx context.Context, tx *sql.Tx) error {
	type key struct {
		projectID  int64
		day        string
		platform   string
		appVersion string
	}
	type total struct{ visitors, views int64 }

	rows, err := tx.QueryContext(ctx,
		`SELECT project_id, day, os, app_version, visitors, views FROM agg_views_app_versions_old`)
	if err != nil {
		return fmt.Errorf("rekey agg_views_app_versions: %w", err)
	}
	totals := make(map[key]total)
	var order []key
	for rows.Next() {
		var (
			k        key
			os       string
			visitors int64
			views    int64
		)
		if err := rows.Scan(&k.projectID, &k.day, &os, &k.appVersion, &visitors, &views); err != nil {
			rows.Close()
			return fmt.Errorf("rekey agg_views_app_versions: %w", err)
		}
		k.platform, _ = enrich.NormalizePlatform(os)
		if _, seen := totals[k]; !seen {
			order = append(order, k)
		}
		t := totals[k]
		totals[k] = total{t.visitors + visitors, t.views + views}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rekey agg_views_app_versions: %w", err)
	}

	for _, k := range order {
		t := totals[k]
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agg_views_app_versions (project_id, day, platform, app_version, visitors, views)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			k.projectID, k.day, k.platform, k.appVersion, t.visitors, t.views); err != nil {
			return fmt.Errorf("rekey agg_views_app_versions %q: %w", k.platform, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE agg_views_app_versions_old`); err != nil {
		return fmt.Errorf("drop agg_views_app_versions_old: %w", err)
	}
	return nil
}
