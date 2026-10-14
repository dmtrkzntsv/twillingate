package archive

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Lookup asks where one form submission came from (Attribute).
type Lookup struct {
	ProjectID          int64
	ID                 string    // the submission's id
	ActorKind, ActorID string    // its actor
	InstallID          string    // the $install_id it was sent with, "" if none
	At                 time.Time // its arrival
}

// Found is what the files say of one Lookup: Source holds the referrer
// and UTMs of the newest session with any (zero: direct), Newest the
// newest view's kind, group, session id, environment and consent (zero:
// no view).
type Found struct {
	ProjectID int64
	ID        string
	Source    store.Event
	Newest    store.Event
}

// sourceColumns and newestColumns are what Found reads off a session and
// a view, in the order attributionSQL selects them.
var (
	sourceColumns = []string{"referrer_source", "utm_source", "utm_medium", "utm_campaign"}
	newestColumns = []string{"kind", "group_id", "session_id", "platform", "os", "os_version", "os_name",
		"browser", "browser_version", "browser_locale", "app_version", "app_locale", "device", "device_model",
		"display_width", "display_height", "consent"}
)

// Attribute answers ls in one query over the tables Query reads (so rules
// is the full rule set, as for Query). For each lookup it reads the
// sessions and views from `days` days before At up to At, of the
// lookup's actor, of the install id it was sent with and, for a user, of
// every install id aliases pairs with the user. One Found per lookup, in
// no particular order.
func (r *Reader) Attribute(ctx context.Context, ls []Lookup, days int, rules Rules) ([]Found, error) {
	if len(ls) == 0 {
		return nil, nil
	}
	res, err := r.Query(ctx, attributionSQL(ls, days), 0, rules)
	if err != nil {
		return nil, err
	}
	out := make([]Found, 0, len(res.Rows))
	for _, row := range res.Rows {
		str := func(i int) string {
			s, _ := row[i].(string)
			return s
		}
		project, err := strconv.ParseInt(str(0), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("archive: attribution project %v: %w", row[0], err)
		}
		f := Found{ProjectID: project, ID: str(1)}
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

// attributionSQL is Attribute's query. Every value from a lookup is a
// literal; every column it returns is VARCHAR, NULL when nothing matched.
func attributionSQL(ls []Lookup, days int) string {
	rows := make([]string, len(ls))
	for i, l := range ls {
		rows[i] = fmt.Sprintf("(%d, %s, %s, %s, %s, CAST(%s AS TIMESTAMP))", l.ProjectID, literal(l.ID),
			literal(l.ActorKind), literal(l.ActorID), literal(l.InstallID), literal(l.At.UTC().Format("2006-01-02 15:04:05")))
	}
	window := fmt.Sprintf("INTERVAL %d DAY", days)
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
	install, user := literal(store.ActorInstall), literal(store.ActorUser)
	return `WITH l(project_id, id, actor_kind, actor_id, install_id, arrived) AS (VALUES ` + strings.Join(rows, ", ") + `),
who AS (
  SELECT project_id, id, actor_kind, actor_id, arrived FROM l
  UNION SELECT project_id, id, ` + install + `, install_id, arrived FROM l WHERE install_id <> ''
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
  WHERE ` + strings.Join(hasSource, " OR ") + `
),
newest AS (
  SELECT w.project_id, w.id, ` + as("v", newestColumns) + `,
    row_number() OVER (PARTITION BY w.project_id, w.id ORDER BY v.ts DESC, v.id DESC) AS n
  FROM who w JOIN views v ON v.project_id = w.project_id
    AND v.actor_kind = w.actor_kind AND v.actor_id = w.actor_id
    AND v.ts <= w.arrived AND v.ts >= w.arrived - ` + window + `
)
SELECT CAST(l.project_id AS VARCHAR), l.id, ` + pick("src", sourceColumns) + `, ` + pick("newest", newestColumns) + `
FROM l
LEFT JOIN src ON src.project_id = l.project_id AND src.id = l.id AND src.n = 1
LEFT JOIN newest ON newest.project_id = l.project_id AND newest.id = l.id AND newest.n = 1`
}
