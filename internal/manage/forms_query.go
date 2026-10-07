package manage

import (
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// submissionsFixed are the display columns after a form's fields, each
// with the expression that fills it.
var submissionsFixed = []struct{ name, expr string }{
	{"Page", "host || path"},
	{"Referrer", "json_extract(visit, '$.referrer')"},
	{"UTM source", "json_extract(visit, '$.utm_source')"},
	{"UTM medium", "json_extract(visit, '$.utm_medium')"},
	{"UTM campaign", "json_extract(visit, '$.utm_campaign')"},
}

// ReceivedColumn is the submissions table's first display column, the
// time a submission arrived.
const ReceivedColumn = "Received"

// SubmissionsQuery builds the SQL of a form's submissions table (spec
// D12a). The query takes two arguments, the project id and the form's
// name, in that order; it selects id first, then the display columns,
// newest first. columns are the display columns, without id: Received,
// one per field (an approved form's expected fields in their order, a
// draft's every field seen), then Page, Referrer and the three UTM
// columns. A field whose name matches a fixed column or id, ignoring
// ASCII case as SQLite's column names do, is shown as "<name> (field)"
// (again until the name is free), so every column filters and sorts as
// itself. Field names reach the SQL only as quoted literals.
func SubmissionsQuery(f store.Form) (query string, columns []string) {
	fields := f.Fields
	if f.Status == store.FormApproved {
		fields = f.ExpectedFields
	}
	used := map[string]bool{"id": true, strings.ToLower(ReceivedColumn): true}
	for _, c := range submissionsFixed {
		used[strings.ToLower(c.name)] = true
	}
	sel := []string{"id", "received_at AS " + quoteIdent(ReceivedColumn)}
	columns = []string{ReceivedColumn}
	for _, name := range fields {
		shown := name
		for used[strings.ToLower(shown)] {
			shown += " (field)"
		}
		used[strings.ToLower(shown)] = true
		columns = append(columns, shown)
		sel = append(sel, fieldValue(name)+" AS "+quoteIdent(shown))
	}
	for _, c := range submissionsFixed {
		columns = append(columns, c.name)
		sel = append(sel, c.expr+" AS "+quoteIdent(c.name))
	}
	query = "SELECT " + strings.Join(sel, ",\n       ") +
		"\nFROM submissions\nWHERE project_id = ? AND form = ?\nORDER BY received_at DESC, id DESC"
	return query, columns
}

// fieldValue is the expression reading one field out of the fields JSON.
// SQLite's JSON path has no escape for a quote or a backslash inside a
// quoted label, so a name holding either is matched through json_each,
// whose key is the name as stored.
func fieldValue(name string) string {
	if strings.ContainsAny(name, `"\`) {
		return "(SELECT value FROM json_each(fields) WHERE key = " + quoteString(name) + ")"
	}
	return "json_extract(fields, " + quoteString(`$."`+name+`"`) + ")"
}

func quoteIdent(s string) string  { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func quoteString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// CSVSafe makes one cell of a CSV export inert in a spreadsheet: a cell
// starting with =, +, -, @, a tab or a carriage return is prefixed with
// ', so what a visitor typed is shown as text rather than run as a
// formula. Every other cell is returned as it is.
func CSVSafe(cell string) string {
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "'" + cell
	}
	return cell
}
