package manage

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ReceivedColumn is the submissions table's first display column, the
// time a submission arrived.
const ReceivedColumn = "Received"

// SubmissionsQuery builds the SQL of a form's submissions table (spec
// D12a). The query takes two arguments, the project id and the form's
// name, in that order; it selects id first, then the display columns,
// newest first. columns are the display columns, without id: Received,
// then one per field (an approved form's expected fields in their order,
// a draft's every field seen). A field whose name matches Received or
// id, ignoring ASCII case, is shown as "<name> (field)" (again until the
// name is free), so every column filters and sorts as itself.
//
// The query names its columns by position, never by display name: c0 is
// id and c<i> is columns[i-1]. A field name reaches the SQL only inside a
// quoted JSON path, so a field called meta, dbstat or sqlite_x (names
// readsql refuses as identifiers) reads like any other. QuerySubmissions
// translates between the two.
func SubmissionsQuery(f store.Form) (query string, columns []string) {
	fields := f.Fields
	if f.Status == store.FormApproved {
		fields = f.ExpectedFields
	}
	used := map[string]bool{"id": true, strings.ToLower(ReceivedColumn): true}
	sel := []string{"id AS " + submissionsAlias(0), "received_at AS " + submissionsAlias(1)}
	columns = []string{ReceivedColumn}
	for _, name := range fields {
		shown := name
		for used[strings.ToLower(shown)] {
			shown += " (field)"
		}
		used[strings.ToLower(shown)] = true
		columns = append(columns, shown)
		sel = append(sel, fieldValue(name)+" AS "+submissionsAlias(len(columns)))
	}
	query = "SELECT " + strings.Join(sel, ",\n       ") +
		"\nFROM submissions\nWHERE project_id = ? AND form = ?\nORDER BY received_at DESC, id DESC"
	return query, columns
}

// submissionsAlias is the SQL name of the submissions query's column i:
// 0 is id, i the display column columns[i-1].
func submissionsAlias(i int) string { return "c" + strconv.Itoa(i) }

// QuerySubmissions runs one page of f's submissions table on db. pg names
// columns as the table shows them (id or a display column); they are
// translated to the query's positional names and the result's back, so
// its columns are id and then the display columns (with Distinct, value
// and rows). An unknown column is refused with ErrInvalid naming the
// table's columns; other errors are readsql's, unwrapped.
func QuerySubmissions(ctx context.Context, db *readsql.DB, f store.Form, pg readsql.Page) (readsql.PageResult, error) {
	q, columns := SubmissionsQuery(f)
	names := append([]string{"id"}, columns...)
	alias := func(name string) (string, error) {
		for i, n := range names {
			if n == name {
				return submissionsAlias(i), nil
			}
		}
		return "", fmt.Errorf("%w: no column %q; columns are %s", ErrInvalid, name, strings.Join(names, ", "))
	}
	var err error
	filters := make([]readsql.Filter, len(pg.Filters))
	for i, fl := range pg.Filters {
		if fl.Column, err = alias(fl.Column); err != nil {
			return readsql.PageResult{}, err
		}
		filters[i] = fl
	}
	pg.Filters = filters
	if pg.Sort != nil {
		s := *pg.Sort
		if s.Column, err = alias(s.Column); err != nil {
			return readsql.PageResult{}, err
		}
		pg.Sort = &s
	}
	if pg.Distinct != "" {
		if pg.Distinct, err = alias(pg.Distinct); err != nil {
			return readsql.PageResult{}, err
		}
	}
	res, err := db.QueryPage(ctx, q, pg, f.ProjectID, f.Name)
	if err != nil {
		return readsql.PageResult{}, err
	}
	if pg.Distinct == "" {
		res.Columns = names
	}
	return res, nil
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

// AllSubmissions pages through every row of f's submissions table that pg's
// filters match, a page of db.MaxRows() at a time, and returns the ids, the
// display columns and the rows without their ids. db must be a handle that
// can read submissions (opened without that name refused). Without a sort
// it sorts by Received, newest first: a sorted page breaks ties by every
// column, so the pages neither overlap nor skip a row. Errors are
// QuerySubmissions'.
func AllSubmissions(ctx context.Context, db *readsql.DB, f store.Form, pg readsql.Page) (ids, columns []string, rows [][]string, err error) {
	_, columns = SubmissionsQuery(f)
	if pg.Sort == nil {
		pg.Sort = &readsql.Sort{Column: ReceivedColumn, Desc: true}
	}
	pg.Limit = db.MaxRows()
	for pg.Offset = 0; ; pg.Offset += pg.Limit {
		res, err := QuerySubmissions(ctx, db, f, pg)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, r := range res.Rows {
			ids = append(ids, r[0])
			rows = append(rows, r[1:])
		}
		if !res.Truncated || len(res.Rows) == 0 {
			return ids, columns, rows, nil
		}
	}
}

// CSVSafeTable applies CSVSafe to every header cell and every row cell, in
// place, and returns them: what visitors typed must not run as a
// spreadsheet formula.
func CSVSafeTable(header []string, rows [][]string) ([]string, [][]string) {
	for i, c := range header {
		header[i] = CSVSafe(c)
	}
	for _, r := range rows {
		for i, c := range r {
			r[i] = CSVSafe(c)
		}
	}
	return header, rows
}
