package enginesql

import "strings"

// Trim strips trailing ';' and ASCII whitespace, as readsql does, so the
// statement fits inside a wrap's parens. Only ASCII: Params lets nothing
// else follow a ';'.
func Trim(q string) string { return strings.TrimRight(q, "; \t\r\n\f") }

// Text wraps q so every cell is tw_cell's text of its value: numbers as
// SQLite's cells read them (100, not 100.0) and NULL as "", which is what
// a widget row shows for it anyway. The text a filter or sort compares is
// then the text the reader sees. The newline ends a line comment q may
// end with. Column names, their order and the row order stay q's.
func Text(q string) string {
	return "SELECT tw_cell(COLUMNS(*)) FROM (" + Trim(q) + "\n)"
}

// Shape is q with no rows: its columns only, for a check that q runs and
// for the names a page validates against.
func Shape(q string) string { return "SELECT * FROM (" + Trim(q) + "\n) LIMIT 0" }
