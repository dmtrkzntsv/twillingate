package readsql

import (
	"database/sql"
	"database/sql/driver"
	"math"
	"regexp"
	"strconv"

	"modernc.org/sqlite"
)

// Two SQL functions that compare cells the way a reader sees them, not
// the way SQLite's type affinity does. SQLite compares a TEXT-affinity
// column with a bound number as text, but the same text produced by an
// expression orders after every number; a caller filtering a result it
// did not write cannot know which applies, and a browser repeating the
// filter on the rows it holds cannot either. Comparing through these two
// gives one answer for both.
//
//   - tw_cell(x): x as Run's scan renders it (Cell).
//   - tw_num(x): tw_cell(x) as a number when it is a decimal number, else NULL.
func init() {
	if err := sqlite.RegisterDeterministicScalarFunction("tw_cell", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return Cell(args[0]), nil
		}); err != nil {
		panic(err)
	}
	if err := sqlite.RegisterDeterministicScalarFunction("tw_num", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if n, ok := CellNumber(Cell(args[0])); ok {
				return n, nil
			}
			return nil, nil
		}); err != nil {
		panic(err)
	}
}

// Cell renders one value as Run does: through sql.NullString.Scan, so
// integers in decimal, reals in Go's shortest form, text and blobs as
// is, NULL as "".
func Cell(v any) string {
	var ns sql.NullString
	if err := ns.Scan(v); err != nil || !ns.Valid {
		return ""
	}
	return ns.String
}

var decimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// IsDecimal reports whether s is a decimal number as a cell shows one:
// optional minus, digits, optional fraction and exponent, no spaces.
func IsDecimal(s string) bool { return decimal.MatchString(s) }

// CellNumber is s as a number when it is a finite decimal number.
func CellNumber(s string) (float64, bool) {
	if !IsDecimal(s) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, false
	}
	return n, true
}
