package enginesql

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// Stmt is one statement for engine.Query and exactly the arguments it
// uses: the engine refuses an argument the SQL does not use as firmly as
// a parameter without one, so each statement carries its own list.
type Stmt struct {
	SQL  string
	Args []engine.Arg
}

// Page builds the statement readsql's QueryPage runs, in DuckDB, for page
// p over q, whose result columns are cols (Shape's). q is bound text,
// Bind's output, and args are the arguments Bind returned for it; the
// statement's Args are those and then the page's own filter values,
// bound as $tw_pN, so the caller passes the list on unchanged.
//
// The statement's rows are the page's rows as text (Text's cells, NULL
// kept), then the matched and total counts as two BIGINT columns. For a
// Distinct page the rows are the value (NULL as the empty string), its
// row count as text and the same two counts, matched counting values. A
// page with no row carries no counts: Count builds them.
//
// Comparisons go through the child's tw_cell and tw_num macros, so they
// mean what the cells show; tw_cell compares VARCHAR byte-wise, which is
// code-point order, as SQLite's BINARY and the web app's table do.
func Page(q string, args []engine.Arg, cols []string, p readsql.Page, maxRows int) (Stmt, error) {
	limit := p.Limit
	switch {
	case limit == 0:
		limit = maxRows
	case limit < 1 || limit > maxRows:
		return Stmt{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrRefused, maxRows)
	}
	if err := check(q, cols, p); err != nil {
		return Stmt{}, err
	}

	// The columns the wrap adds are named so no result column can collide with them.
	added := addedNames(cols)
	total, matched := ident(added.total), ident(added.matched)
	b := binder{args: slices.Clone(args)}
	where := whereClause(filtersFor(p), &b)

	// Every row carries both counts: matched counts after the filters,
	// total before them.
	counted := fmt.Sprintf("SELECT *, COUNT(*) OVER () AS %s FROM (%s)", total, Text(q))
	// Integers checked above, so spliced; only the filters' values are text.
	paging := fmt.Sprintf(" LIMIT %d OFFSET %d", limit, p.Offset)

	var stmt string
	if p.Distinct != "" {
		// Grouped one level down, so the ORDER BY reads the BIGINT count
		// (DuckDB binds ORDER BY names to the select list first, where
		// "rows" is text) and the value as a plain column. total is the
		// same on every row; MAX reads it through the GROUP BY.
		value, rows := ident(added.cell[0]), ident(added.num[0])
		stmt = fmt.Sprintf(`SELECT %[1]s AS "value", CAST(%[2]s AS VARCHAR) AS "rows", COUNT(*) OVER () AS %[3]s, %[4]s FROM (SELECT tw_cell(%[5]s) AS %[1]s, COUNT(*) AS %[2]s, MAX(%[4]s) AS %[4]s FROM (%[6]s) WHERE %[7]s GROUP BY 1) ORDER BY %[2]s DESC, %[8]s%[9]s`,
			value, rows, matched, total, ident(p.Distinct), counted, where, sortKeys(value, "tw_num("+value+")", false), paging)
	} else {
		var names, keys []string
		for i, c := range cols {
			names = append(names, ident(c))
			// Computed once per row, so the sort reads them instead of
			// calling both macros twice per column per comparison.
			if p.Sort != nil {
				keys = append(keys, fmt.Sprintf(", tw_cell(%[1]s) AS %[2]s, tw_num(%[1]s) AS %[3]s",
					ident(c), ident(added.cell[i]), ident(added.num[i])))
			}
		}
		stmt = fmt.Sprintf(`SELECT %s, %s, %s FROM (SELECT *, COUNT(*) OVER () AS %s%s FROM (%s) WHERE %s)%s%s`,
			strings.Join(names, ", "), matched, total, matched, strings.Join(keys, ""),
			counted, where, orderBy(p.Sort, cols, added), paging)
	}
	return Stmt{SQL: stmt, Args: b.args}, nil
}

// Count builds the two counts for a page with no rows, which has no row to
// carry them, over the same q, args and cols as Page. total counts q's
// rows and carries q's args only. matched counts the rows (for Distinct,
// the values) past the filters, with the filters' values too; its SQL is
// empty at offset 0, where an empty page means nothing matched.
func Count(q string, args []engine.Arg, cols []string, p readsql.Page) (total, matched Stmt, err error) {
	if err := check(q, cols, p); err != nil {
		return Stmt{}, Stmt{}, err
	}
	total = Stmt{SQL: "SELECT COUNT(*) FROM (" + Trim(q) + "\n)", Args: slices.Clone(args)}
	if p.Offset == 0 {
		return total, Stmt{}, nil
	}
	b := binder{args: slices.Clone(args)}
	where := whereClause(filtersFor(p), &b)
	what := "COUNT(*)"
	if p.Distinct != "" {
		what = "COUNT(DISTINCT tw_cell(" + ident(p.Distinct) + "))"
	}
	matched = Stmt{SQL: fmt.Sprintf("SELECT %s FROM (%s) WHERE %s", what, Text(q), where), Args: b.args}
	return total, matched, nil
}

// check refuses a page Page and Count cannot build: q that does not scan
// as bound text, or uses the page's own parameter names, and a p that
// names no column of q or no operator, with messages that say what to
// change.
func check(q string, cols []string, p readsql.Page) error {
	occ, err := scan(q, true)
	if err != nil {
		return err
	}
	for _, o := range occ {
		if n, ok := strings.CutPrefix(strings.ToLower(o.name), "$"+paramPrefix); ok && n != "" && strings.Trim(n, "0123456789") == "" {
			return fmt.Errorf("%w: parameter %s is the page's own; rename it", ErrRefused, o.name)
		}
	}
	if p.Offset < 0 {
		return fmt.Errorf("%w: offset must not be negative", ErrRefused)
	}
	if p.Distinct != "" && p.Sort != nil {
		return fmt.Errorf("%w: distinct and sort cannot be combined; drop one", ErrRefused)
	}
	known := func(name string) error {
		if slices.Contains(cols, name) {
			return nil
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

// filtersFor is p's filters, less those on a Distinct page's own column,
// which would otherwise hide the values a viewer is choosing among.
func filtersFor(p readsql.Page) []readsql.Filter {
	var out []readsql.Filter
	for _, f := range p.Filters {
		if p.Distinct == "" || f.Column != p.Distinct {
			out = append(out, f)
		}
	}
	return out
}

// added names the columns the wrap adds: the two counts, and each result
// column's tw_cell and tw_num, which a sorted page orders by (a Distinct
// page reuses the first pair for its value and count).
type added struct {
	total, matched string
	cell, num      []string
}

// addedNames picks names for the wrap's columns that no result column
// uses. DuckDB matches identifiers without regard to case, so this does
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
		for j := range max(len(cols), 1) {
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

// paramPrefix starts the name of every parameter the page adds.
const paramPrefix = "tw_p"

// binder hands out the page's parameters after the ones it starts with.
type binder struct {
	args []engine.Arg
	n    int
}

// bind returns the placeholder for v.
func (b *binder) bind(v string) string {
	b.n++
	name := paramPrefix + strconv.Itoa(b.n)
	b.args = append(b.args, engine.Arg{Name: name, Value: v})
	return "$" + name
}

// whereClause ANDs the filters. Values are bound, never spliced, and an
// empty cell reaches only the negative operators.
func whereClause(filters []readsql.Filter, b *binder) string {
	if len(filters) == 0 {
		return "true"
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
			if readsql.IsDecimal(f.Values[0]) {
				// Past DOUBLE's range the cast is an infinity, which still orders correctly.
				num := "tw_num(" + ident(f.Column) + ")"
				terms = append(terms, num+" IS NOT NULL AND "+num+" "+f.Op+" CAST("+b.bind(f.Values[0])+" AS DOUBLE)")
			} else {
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
// a Sort it is empty and the query's own order stands, which DuckDB keeps
// through the wrap (insertion order is preserved); a tiebreak on every
// column would replace it.
func orderBy(s *readsql.Sort, cols []string, a added) string {
	if s == nil {
		return ""
	}
	key := func(i int, desc bool) string {
		return sortKeys(ident(a.cell[i]), ident(a.num[i]), desc)
	}
	var keys []string
	if i := slices.Index(cols, s.Column); i >= 0 {
		keys = append(keys, key(i, s.Desc))
	}
	for i := range cols {
		keys = append(keys, key(i, false))
	}
	return " ORDER BY " + strings.Join(keys, ", ")
}
