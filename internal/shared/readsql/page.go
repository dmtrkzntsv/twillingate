package readsql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Filter keeps the rows whose Column satisfies Op against Values. Op is
// one of "=", "!=", "<", ">", "in" and "not in"; the first four take
// exactly one value, the last two at least one. A cell that is empty
// (NULL or the empty string) matches "!=" and "not in" only. "<" and ">" compare as
// numbers when the value is a decimal number and as text otherwise.
type Filter struct {
	Column string
	Op     string
	Values []string
}

// Sort orders the rows by one column: non-empty cells first, then
// numbers before text, empty cells last in either direction. Ties break
// by every column in order, so pages never overlap or skip a row.
type Sort struct {
	Column string
	Desc   bool
}

// Page is what QueryPage asks of a query's rows. Filters combine with
// AND. A nil Sort leaves the query's own order. Distinct, when set, asks
// for the values of that column and how many rows carry each instead of
// the rows, ignoring any filter on that column; it cannot be combined
// with Sort. Offset is at least 0, Limit 1 to MaxRows (0 means MaxRows).
type Page struct {
	Filters  []Filter
	Sort     *Sort
	Distinct string
	Offset   int
	Limit    int
}

// PageResult is one page. Truncated reports that more matched rows follow
// the page. Matched is the number of rows (or, for Distinct, values) that
// passed the filters and Total the number of rows the query returns
// before any filter, so a caller can show "1-1,000 of 5,335".
type PageResult struct {
	Result
	Matched, Total int
}

// QueryPage runs q wrapped so the database filters, sorts, counts and
// pages it in one statement. That statement follows a shape query, and an
// empty page past offset 0 adds two counts, so one call runs up to four
// statements and each gets Run's deadline of its own. It is QueryLimit's
// guard 3 extended, not a second path around it: q passes Check first and
// the only text added around it is this package's own, with every column
// name taken from q's own result and quoted, and every value bound.
// Comparisons go through tw_cell and tw_num (cell.go), never SQLite's own
// affinity rules, so they mean what the cells show.
func (d *DB) QueryPage(ctx context.Context, q string, p Page, args ...any) (PageResult, error) {
	limit := p.Limit
	switch {
	case p.Offset < 0:
		return PageResult{}, fmt.Errorf("%w: offset must not be negative", ErrRefused)
	case limit == 0:
		limit = d.maxRows
	case limit < 1 || limit > d.maxRows:
		return PageResult{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrRefused, d.maxRows)
	}
	if p.Distinct != "" && p.Sort != nil {
		return PageResult{}, fmt.Errorf("%w: distinct and sort cannot be combined; drop one", ErrRefused)
	}

	// The wrap's own values are bound by name, under a prefix q may not
	// use: the driver binds a plain ? by its position among all the
	// arguments, so a caller passing named arguments q does not use (which
	// plain Query accepts) would shift every value this wrap adds.
	params, err := d.Check(q)
	if err != nil {
		return PageResult{}, err
	}
	for _, name := range params {
		if strings.HasPrefix(strings.ToLower(name[1:]), paramPrefix) {
			return PageResult{}, fmt.Errorf("%w: parameter %s uses the reserved prefix %q; rename it", ErrRefused, name, paramPrefix)
		}
	}
	// The shape query also runs Check again and the empty-text refusal.
	shape, err := d.QueryLimit(ctx, q, 0, args...)
	if err != nil {
		return PageResult{}, err
	}
	cols := shape.Columns
	if err := p.validate(cols); err != nil {
		return PageResult{}, err
	}
	// Exactly as QueryLimit trims, so SQLite parses the text Check examined.
	trimmed := strings.TrimRight(q, "; \t\r\n\f")

	// The columns the wrap adds are named so no result column can collide with them.
	added := addedNames(cols)
	total, matched := added.total, added.matched

	var filters []Filter
	for _, f := range p.Filters {
		if p.Distinct == "" || f.Column != p.Distinct {
			filters = append(filters, f)
		}
	}
	var b binder
	where := whereClause(filters, &b)

	// Every row carries both counts: matched counts after the filters,
	// total before them.
	counted := fmt.Sprintf("SELECT *, COUNT(*) OVER () AS %s FROM (%s\n)", ident(total), trimmed)
	paging := " LIMIT " + b.bind(limit) + " OFFSET " + b.bind(p.Offset)

	var stmt string
	if p.Distinct != "" {
		c := ident(p.Distinct)
		// MAX over the window: total is the same on every row but is not
		// grouped, so it must be aggregated to be read.
		stmt = fmt.Sprintf(`SELECT tw_cell(%[1]s) AS "value", COUNT(*) AS "rows", COUNT(*) OVER () AS %[2]s, MAX(%[3]s) OVER () AS %[3]s FROM (%[4]s) WHERE %[5]s GROUP BY tw_cell(%[1]s) ORDER BY COUNT(*) DESC, %[6]s%[7]s`,
			c, ident(matched), ident(total), counted, where, sortKeys("tw_cell("+c+")", "tw_num("+c+")", false), paging)
	} else {
		var names, keys []string
		for i, c := range cols {
			names = append(names, ident(c))
			// Computed once per row here, in a subquery SQLite never
			// flattens (it has a window function), so the sort reads them
			// instead of calling both functions twice per column per row.
			if p.Sort != nil {
				keys = append(keys, fmt.Sprintf(", tw_cell(%[1]s) AS %[2]s, tw_num(%[1]s) AS %[3]s",
					ident(c), ident(added.cell[i]), ident(added.num[i])))
			}
		}
		stmt = fmt.Sprintf(`SELECT %s, %s, %s FROM (SELECT *, COUNT(*) OVER () AS %s%s FROM (%s) WHERE %s)%s%s`,
			strings.Join(names, ", "), ident(matched), ident(total), ident(matched), strings.Join(keys, ""),
			counted, where, orderBy(p.Sort, cols, added), paging)
	}
	res, err := d.Run(ctx, stmt, append(append([]any{}, args...), b.args...)...)
	if err != nil {
		return PageResult{}, err
	}

	out := PageResult{Result: Result{Columns: cols, Rows: res.Rows}}
	if p.Distinct != "" {
		out.Columns = []string{"value", "rows"}
	}
	keep := len(out.Columns)
	if len(res.Rows) > 0 {
		first := res.Rows[0]
		if out.Matched, err = strconv.Atoi(first[keep]); err != nil {
			return PageResult{}, fmt.Errorf("readsql: matched count %q: %w", first[keep], err)
		}
		if out.Total, err = strconv.Atoi(first[keep+1]); err != nil {
			return PageResult{}, fmt.Errorf("readsql: total count %q: %w", first[keep+1], err)
		}
		for i, row := range res.Rows {
			out.Rows[i] = row[:keep]
		}
	} else if err := d.countWhenEmpty(ctx, &out, trimmed, p, where, append(append([]any{}, args...), b.args...)); err != nil {
		return PageResult{}, err
	}
	out.Truncated = out.Matched > p.Offset+len(out.Rows)
	return out, nil
}

// countWhenEmpty fills Matched and Total for a page with no rows, which has
// no row to carry them. Offset 0 with no rows means nothing matched; a page
// past the end still has to count what it skipped.
func (d *DB) countWhenEmpty(ctx context.Context, out *PageResult, trimmed string, p Page, where string, args []any) error {
	count := func(what, cond string) (int, error) {
		res, err := d.Run(ctx, fmt.Sprintf("SELECT %s FROM (%s\n) WHERE %s", what, trimmed, cond), args...)
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(res.Rows[0][0])
	}
	var err error
	if out.Total, err = count("COUNT(*)", "1"); err != nil {
		return err
	}
	if p.Offset == 0 {
		return nil
	}
	what := "COUNT(*)"
	if p.Distinct != "" {
		what = "COUNT(DISTINCT tw_cell(" + ident(p.Distinct) + "))"
	}
	out.Matched, err = count(what, where)
	return err
}

// validate refuses what names no column of the query or no operator, with
// messages that say what to change.
func (p Page) validate(cols []string) error {
	known := func(name string) error {
		for _, c := range cols {
			if c == name {
				return nil
			}
		}
		return fmt.Errorf("%w: no column %q; columns are %s", ErrRefused, name, strings.Join(cols, ", "))
	}
	for _, f := range p.Filters {
		if err := known(f.Column); err != nil {
			return err
		}
		switch f.Op {
		case "=", "!=", "<", ">":
			if len(f.Values) != 1 {
				return fmt.Errorf("%w: operator %q takes one value, got %d; use \"in\" for a list", ErrRefused, f.Op, len(f.Values))
			}
		case "in", "not in":
			if len(f.Values) == 0 {
				return fmt.Errorf("%w: operator %q takes at least one value", ErrRefused, f.Op)
			}
		default:
			return fmt.Errorf("%w: unknown operator %q; use =, !=, <, >, in or not in", ErrRefused, f.Op)
		}
	}
	if p.Sort != nil {
		if err := known(p.Sort.Column); err != nil {
			return err
		}
	}
	if p.Distinct != "" {
		return known(p.Distinct)
	}
	return nil
}

// added names the columns the wrap adds: the two counts, and each result
// column's tw_cell and tw_num, which a sorted page orders by.
type added struct {
	total, matched string
	cell, num      []string
}

// addedNames picks names for the wrap's columns that no result column
// uses. SQLite compares identifiers without regard to case, so this does
// too.
func addedNames(cols []string) added {
	taken := map[string]bool{}
	for _, c := range cols {
		taken[strings.ToLower(c)] = true
	}
	for i := 0; ; i++ {
		suffix := ""
		if i > 0 {
			suffix = "_" + strconv.Itoa(i)
		}
		a := added{total: "__tw_total" + suffix, matched: "__tw_matched" + suffix}
		free := !taken[a.total] && !taken[a.matched]
		for j := range cols {
			a.cell = append(a.cell, "__tw_cell"+suffix+"_"+strconv.Itoa(j))
			a.num = append(a.num, "__tw_num"+suffix+"_"+strconv.Itoa(j))
			free = free && !taken[a.cell[j]] && !taken[a.num[j]]
		}
		if free {
			return a
		}
	}
}

// ident quotes a column name for SQL.
func ident(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// paramPrefix starts the name of every parameter this wrap adds.
const paramPrefix = "tw_p"

// binder hands out the wrap's named parameters and keeps their values.
type binder struct{ args []any }

// bind returns the placeholder for v, bound by name when the statement runs.
func (b *binder) bind(v any) string {
	name := paramPrefix + strconv.Itoa(len(b.args)+1)
	b.args = append(b.args, sql.Named(name, v))
	return ":" + name
}

// whereClause ANDs the filters. Values are bound, never spliced, and an
// empty cell reaches only the negative operators.
func whereClause(filters []Filter, b *binder) string {
	if len(filters) == 0 {
		return "1"
	}
	var terms []string
	for _, f := range filters {
		cell := "tw_cell(" + ident(f.Column) + ")"
		switch f.Op {
		case "=":
			terms = append(terms, cell+" <> '' AND "+cell+" = "+b.bind(f.Values[0]))
		case "!=":
			terms = append(terms, "("+cell+" = '' OR "+cell+" <> "+b.bind(f.Values[0])+")")
		case "in", "not in":
			marks := make([]string, len(f.Values))
			for i, v := range f.Values {
				marks[i] = b.bind(v)
			}
			list := "(" + strings.Join(marks, ", ") + ")"
			if f.Op == "in" {
				terms = append(terms, cell+" <> '' AND "+cell+" IN "+list)
			} else {
				terms = append(terms, "("+cell+" = '' OR "+cell+" NOT IN "+list+")")
			}
		case "<", ">":
			if IsDecimal(f.Values[0]) {
				// Out of range reads as an infinity, which still orders correctly.
				n, _ := strconv.ParseFloat(f.Values[0], 64)
				num := "tw_num(" + ident(f.Column) + ")"
				terms = append(terms, num+" IS NOT NULL AND "+num+" "+f.Op+" "+b.bind(n))
			} else {
				// BINARY collation over UTF-8 is code-point order.
				terms = append(terms, cell+" <> '' AND "+cell+" "+f.Op+" "+b.bind(f.Values[0]))
			}
		}
	}
	return "(" + strings.Join(terms, ") AND (") + ")"
}

// sortKeys orders one column, given its tw_cell and tw_num: non-empty
// before empty, numbers before text, each in the given direction.
func sortKeys(cell, num string, desc bool) string {
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	return fmt.Sprintf("(%[1]s = '') ASC, (%[2]s IS NULL) ASC, %[2]s %[3]s, %[1]s %[3]s", cell, num, dir)
}

// orderBy is the ORDER BY of a page, over the keys the wrap added. Without
// a Sort it is empty and the query's own order stands; a tiebreak on every
// column would replace it.
func orderBy(s *Sort, cols []string, a added) string {
	if s == nil {
		return ""
	}
	key := func(i int, desc bool) string {
		return sortKeys(ident(a.cell[i]), ident(a.num[i]), desc)
	}
	var keys []string
	for i, c := range cols {
		if c == s.Column {
			keys = append(keys, key(i, s.Desc))
			break
		}
	}
	for i := range cols {
		keys = append(keys, key(i, false))
	}
	return " ORDER BY " + strings.Join(keys, ", ")
}
