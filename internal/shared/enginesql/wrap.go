package enginesql

import "strings"

// Trim strips trailing ';' and ASCII whitespace, as readsql does, so the
// statement fits inside a wrap's parens. Only ASCII: Params lets nothing
// else follow a ';'.
func Trim(q string) string { return strings.TrimRight(q, "; \t\r\n\f") }

// Text wraps q so every cell is DuckDB's text of its value, NULL kept as
// NULL: the text a filter or sort compares (tw_cell) is then the text the
// reader sees. The newline ends a line comment q may end with. Column
// names, their order and the row order stay q's.
func Text(q string) string {
	return "SELECT CAST(COLUMNS(*) AS VARCHAR) FROM (" + Trim(q) + "\n)"
}

// Shape is q with no rows: its columns only, for a check that q runs and
// for the names a page validates against.
func Shape(q string) string { return "SELECT * FROM (" + Trim(q) + "\n) LIMIT 0" }
