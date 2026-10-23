package native

// attributionSQL, the attribution's DuckDB oracle, leaves with DuckDB in
// step 6.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// oracleAttribute is archive.(*Reader).Attribute as it was before the
// native engine took attribution over: the reference the native answer
// is compared with. It runs attributionSQL through r, after the same
// LatestDays short-circuit.
func oracleAttribute(ctx context.Context, r *archive.Reader, dir string, ls []archive.Lookup, days int, rules archive.Rules) ([]archive.Found, error) {
	if len(ls) == 0 {
		return nil, nil
	}
	latest, err := archive.LatestDays(dir)
	if err != nil {
		return nil, err
	}
	var out []archive.Found
	ask := make([]archive.Lookup, 0, len(ls))
	for _, l := range ls {
		if day, ok := latest[l.ProjectID]; ok && day >= archive.Day(l.At.AddDate(0, 0, -days)) {
			ask = append(ask, l)
		} else {
			out = append(out, archive.Found{ProjectID: l.ProjectID, ID: l.ID})
		}
	}
	if len(ask) == 0 {
		return out, nil
	}
	res, err := r.Query(ctx, attributionSQL(ask, days), 0, rules)
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		str := func(i int) string {
			s, _ := row[i].(string)
			return s
		}
		project, err := strconv.ParseInt(str(0), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("attribution project %v: %w", row[0], err)
		}
		f := archive.Found{ProjectID: project, ID: str(1)}
		f.Source = store.Event{ReferrerSource: str(2), UTMSource: str(3), UTMMedium: str(4), UTMCampaign: str(5)}
		n := 6 // the first of newestColumns
		f.Newest = store.Event{Kind: str(n), GroupID: str(n + 1), SessionID: str(n + 2), Platform: str(n + 3),
			OS: str(n + 4), OSVersion: str(n + 5), OSName: str(n + 6), Browser: str(n + 7),
			BrowserVersion: str(n + 8), BrowserLocale: str(n + 9), AppVersion: str(n + 10), AppLocale: str(n + 11),
			Device: str(n + 12), DeviceModel: str(n + 13)}
		f.Newest.DisplayWidth, _ = strconv.Atoi(str(n + 14))
		f.Newest.DisplayHeight, _ = strconv.Atoi(str(n + 15))
		switch str(n + 16) {
		case "1":
			f.Newest.Consent = store.ConsentGiven
		case "0":
			f.Newest.Consent = store.ConsentNone
		}
		out = append(out, f)
	}
	return out, nil
}

// sqlLiteral is s as a SQL string literal, a NUL spelled chr(0).
func sqlLiteral(s string) string {
	parts := strings.Split(s, "\x00")
	for i, p := range parts {
		parts[i] = "'" + strings.ReplaceAll(p, "'", "''") + "'"
	}
	return strings.Join(parts, " || chr(0) || ")
}

// attributionSQL is the old Attribute's query. Every value from a lookup
// is a literal; every column it returns is VARCHAR, NULL when nothing
// matched.
func attributionSQL(ls []archive.Lookup, days int) string {
	literal := sqlLiteral
	tsLiteral := func(t time.Time) string {
		return "CAST(" + literal(t.UTC().Format("2006-01-02 15:04:05")) + " AS TIMESTAMP)"
	}
	rows := make([]string, len(ls))
	projects := make([]string, 0, len(ls))
	seen := map[int64]bool{}
	first, last := ls[0].At, ls[0].At
	for i, l := range ls {
		if !seen[l.ProjectID] {
			seen[l.ProjectID] = true
			projects = append(projects, strconv.FormatInt(l.ProjectID, 10))
		}
		if l.At.Before(first) {
			first = l.At
		}
		if l.At.After(last) {
			last = l.At
		}
		rows[i] = fmt.Sprintf("(%d, %s, %s, %s, %s, %s, %s)", l.ProjectID, literal(l.ID),
			literal(l.ActorKind), literal(l.ActorID), literal(l.InstallID), literal(l.ConnectionID), tsLiteral(l.At))
	}
	window := fmt.Sprintf("INTERVAL %d DAY", days)
	inBatch := func(alias, col string) string {
		return fmt.Sprintf("%s.project_id IN (%s) AND %s.%s >= %s AND %s.%s <= %s", alias, strings.Join(projects, ", "),
			alias, col, tsLiteral(first.AddDate(0, 0, -days)), alias, col, tsLiteral(last))
	}
	as := func(alias string, cols []string) string {
		out := make([]string, len(cols))
		for i, c := range cols {
			out[i] = fmt.Sprintf("CAST(%s.%s AS VARCHAR) AS %s", alias, c, c)
		}
		return strings.Join(out, ", ")
	}
	pick := func(alias string, cols []string) string {
		out := make([]string, len(cols))
		for i, c := range cols {
			out[i] = alias + "." + c
		}
		return strings.Join(out, ", ")
	}
	hasSource := make([]string, len(sourceColumns))
	for i, c := range sourceColumns {
		hasSource[i] = "coalesce(s." + c + ", '') <> ''"
	}
	install, user, connection := literal(store.ActorInstall), literal(store.ActorUser), literal(store.ActorConnection)
	return `WITH l(project_id, id, actor_kind, actor_id, install_id, connection_id, arrived) AS (VALUES ` + strings.Join(rows, ", ") + `),
who AS (
  SELECT project_id, id, actor_kind, actor_id, arrived FROM l
  UNION SELECT project_id, id, ` + install + `, install_id, arrived FROM l WHERE install_id <> ''
  UNION SELECT project_id, id, ` + connection + `, connection_id, arrived FROM l WHERE connection_id <> ''
  UNION SELECT l.project_id, l.id, ` + install + `, a.install_id, l.arrived FROM l
    JOIN aliases a ON a.project_id = l.project_id AND a.user_id = l.actor_id
    WHERE l.actor_kind = ` + user + `
),
src AS (
  SELECT w.project_id, w.id, ` + as("s", sourceColumns) + `,
    row_number() OVER (PARTITION BY w.project_id, w.id ORDER BY s.started_at DESC, s.actor_kind, s.actor_id) AS n
  FROM who w JOIN sessions s ON s.project_id = w.project_id
    AND s.actor_kind = w.actor_kind AND s.actor_id = w.actor_id
    AND s.started_at <= w.arrived AND s.started_at >= w.arrived - ` + window + `
  WHERE ` + inBatch("s", "started_at") + ` AND (` + strings.Join(hasSource, " OR ") + `)
),
newest AS (
  SELECT w.project_id, w.id, ` + as("v", newestColumns) + `,
    row_number() OVER (PARTITION BY w.project_id, w.id ORDER BY v.ts DESC, v.id DESC) AS n
  FROM who w JOIN views v ON v.project_id = w.project_id
    AND v.actor_kind = w.actor_kind AND v.actor_id = w.actor_id
    AND v.ts <= w.arrived AND v.ts >= w.arrived - ` + window + `
  WHERE ` + inBatch("v", "ts") + `
)
SELECT CAST(l.project_id AS VARCHAR), l.id, ` + pick("src", sourceColumns) + `, ` + pick("newest", newestColumns) + `
FROM l
LEFT JOIN src ON src.project_id = l.project_id AND src.id = l.id AND src.n = 1
LEFT JOIN newest ON newest.project_id = l.project_id AND newest.id = l.id AND newest.n = 1`
}
