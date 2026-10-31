package native

import (
	"slices"
	"strings"
)

// columnType is a dimension column's type, as ColumnTypes names it.
func columnType(column string) string {
	switch {
	case column == "day", isFirstSeen(column):
		return "day"
	case column == "bucket", plainColumns[column]:
		return "number"
	}
	return "text"
}

// Columns are the non-attribute columns a dimension, series or filter on
// the event tables may name (bucket aside, which only measures has; the
// computed ones on the tables computedTables lists).
var Columns = []string{
	"project_id", "family", "event_name", "day", "kind", "actor_id", "actor_kind",
	"user_id", "group_id", "session_id", "install_id", "user_name", "group_name",
	"host", "path", "referrer_source", "utm_source", "utm_medium", "utm_campaign",
	"platform", "os", "os_version", "os_name", "browser", "browser_version",
	"browser_locale", "app_version", "app_locale", "device", "device_model",
	"display_width", "display_height", "country", "consent", "measure",
	"resolution", "device_label", "platform_version", "consent_state",
	"user_status", "group_status", "user_label", "group_label",
	"user_first_seen", "group_first_seen", "vital", "vital_rating",
}

// computedTables are the tables each computed column (computedColumns)
// is a column of.
var computedTables = map[string][]string{
	"resolution":       {"events", "views"},
	"device_label":     {"events", "views", "product"},
	"platform_version": {"events", "views", "product"},
	"consent_state":    {"events", "views", "product"},
	"hour":             {"events", "views", "product", "measures"},
	"weekday":          {"events", "views", "product", "measures"},
	"user_status":      {"events", "views", "product"},
	"group_status":     {"events", "views", "product"},
	"user_label":       {"events", "views", "product"},
	"group_label":      {"events", "views", "product"},
	"user_first_seen":  {"events", "views", "product"},
	"group_first_seen": {"events", "views", "product"},
	"vital":            {"measures"},
	"vital_rating":     {"measures"},
}

// SessionColumns are the columns a dimension, series or filter on
// sessions may name: its text columns, day and project_id; and, filters
// only, the numbers duration_sec and views. started_at and ended_at are
// not.
var SessionColumns = []string{
	"project_id", "day", "kind", "actor_id", "actor_kind", "session_id",
	"entry_host", "entry_path", "exit_path", "referrer_source",
	"utm_source", "utm_medium", "utm_campaign", "duration_sec", "views",
}

// sessionColumnSet is SessionColumns as a set; sessionFilterOnly those of
// them a dimension or series may not name.
var (
	sessionColumnSet  = setOf(SessionColumns)
	sessionFilterOnly = setOf([]string{"duration_sec", "views"})
)

// IdentityColumns are the columns a dimension, series or filter on
// identities may name: kind, id and name, and day, its first_seen's day;
// and, filters only, the numbers views and events.
var IdentityColumns = []string{"kind", "id", "name", "day", "views", "events"}

// identityColumnSet is IdentityColumns as a set; identityFilterOnly those
// of them a dimension or series may not name.
var (
	identityColumnSet  = setOf(IdentityColumns)
	identityFilterOnly = setOf([]string{"views", "events"})
)

// plainColumns are Columns, SessionColumns and IdentityColumns, each with
// whether it is numeric (BIGINT, or the DATE day, which takes gt/lt
// against YYYY-MM-DD); the computed ones are text.
var plainColumns = func() map[string]bool {
	numeric := setOf([]string{"project_id", "day", "display_width", "display_height", "consent", "duration_sec", "views", "events"})
	m := make(map[string]bool, len(Columns)+len(SessionColumns)+len(IdentityColumns))
	for _, c := range slices.Concat(Columns, SessionColumns, IdentityColumns) {
		m[c] = numeric[c]
	}
	return m
}()

// columnInfo reports whether column takes gt/gte/lt/lte, and ok whether the
// table has the column at all as a filter (as a dimension or series too,
// but for sessionFilterOnly).
func columnInfo(table, column string) (orderable, ok bool) {
	if rest, found := strings.CutPrefix(column, defPrefix); found {
		return false, rest != "" // a defined column; validateDefines checks it is
	}
	switch table {
	case "sessions":
		return plainColumns[column], sessionColumnSet[column]
	case "identities":
		return plainColumns[column], identityColumnSet[column]
	}
	if column == "bucket" {
		return true, table == "measures"
	}
	if tables, found := computedTables[column]; found {
		return isFirstSeen(column), slices.Contains(tables, table)
	}
	if rest, found := strings.CutPrefix(column, "attr_"); found {
		return false, rest != ""
	}
	if !slices.Contains(Columns, column) {
		return false, false
	}
	return plainColumns[column], true
}

// dimensionOf says whether table's breakdowns and trends may group by
// column.
func dimensionOf(table, column string) bool {
	_, ok := columnInfo(table, column)
	return ok && !(table == "sessions" && sessionFilterOnly[column]) && !(table == "identities" && identityFilterOnly[column])
}
